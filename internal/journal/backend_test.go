package journal

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestReadMissingDoesNotCreateJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	events, err := Read(path)
	if err != nil || len(events) != 0 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("read created path: %v", err)
	}
}

func TestCanceledExportDoesNotCreateOrReturnPartialHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, err := ExportJSONLContext(ctx, path)
	if err == nil || data != nil {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("canceled export created path: %v", err)
	}
}

func TestAppendMissingDefaultsToSQLiteRegardlessOfExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	event, err := Append(path, "created", map[string]int{"n": 1}, accept)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < len(sqliteHeader) || !bytes.Equal(b[:len(sqliteHeader)], sqliteHeader) {
		t.Fatal("new journal is not SQLite")
	}
	events, err := Read(path)
	if err != nil || len(events) != 1 || events[0].Hash != event.Hash {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	exported, err := ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(exported)
	if err != nil || len(replayed) != 1 || replayed[0].Hash != event.Hash {
		t.Fatalf("export replay=%#v err=%v", replayed, err)
	}
}

func TestExistingJSONLRemainsJSONLAndExportsExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Append(path, "legacy", map[string]int{"n": 1}, accept); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(want, sqliteHeader) {
		t.Fatal("legacy JSONL was silently migrated")
	}
	got, err := ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("export differs\ngot  %s\nwant %s", got, want)
	}
}

func TestConcurrentFirstAppendPublishesOneSQLiteChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	const writers = 16
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := Append(path, "writer", map[string]int{"n": n}, accept)
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := Read(path)
	if err != nil || len(events) != writers {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	for i, event := range events {
		if event.Sequence != i+1 {
			t.Fatalf("sequence=%d index=%d", event.Sequence, i)
		}
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".run.jsonl.sqlite-init-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("initialization leftovers=%v err=%v", leftovers, err)
	}
}

func TestConcurrentProcessesAppendOneSQLiteChain(t *testing.T) {
	if path := os.Getenv("ENGORCH_SQLITE_PROCESS_APPEND_PATH"); path != "" {
		_, err := Append(path, "process", map[string]string{"writer": os.Getenv("ENGORCH_SQLITE_PROCESS_APPEND_ID")}, accept)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	const writers = 6
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentProcessesAppendOneSQLiteChain$")
			cmd.Env = append(os.Environ(), "ENGORCH_SQLITE_PROCESS_APPEND_PATH="+path, fmt.Sprintf("ENGORCH_SQLITE_PROCESS_APPEND_ID=%d", n))
			if output, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("process %d: %w: %s", n, err, output)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	events, err := Read(path)
	if err != nil || len(events) != writers {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestReadOnlySQLiteRejectsUnsupportedSchemaWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.sqlite3")
	s := openTestSQLite(t, path)
	if _, err := s.db.Exec(`UPDATE journal_metadata SET value = '2' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Read(path); err == nil {
		t.Fatal("unsupported SQLite schema accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("read-only inspection rewrote unsupported database")
	}
}
