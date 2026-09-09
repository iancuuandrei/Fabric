package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTestSQLite(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func TestSQLiteRoundTripPreservesCanonicalJSONL(t *testing.T) {
	dir := t.TempDir()
	jsonl := filepath.Join(dir, "run.jsonl")
	if err := os.WriteFile(jsonl, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		if _, err := Append(jsonl, "observation", map[string]any{"n": n, "text": "é"}, accept); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(jsonl)
	if err != nil {
		t.Fatal(err)
	}
	s := openTestSQLite(t, filepath.Join(dir, "run.sqlite3"))
	if err = s.ImportJSONL(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExportJSONL()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("export differs\ngot  %s\nwant %s", got, want)
	}
	events, err := s.Read()
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Fatalf("sequence %d at index %d", e.Sequence, i)
		}
	}
}

func TestSQLiteRuntimeConfiguration(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	var version, journalMode string
	var synchronous, busyTimeout int
	if err := s.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if version != "3.53.4" || journalMode != "wal" || synchronous != 2 || busyTimeout != sqliteBusyTimeoutMS {
		t.Fatalf("version=%q journal=%q synchronous=%d busy_timeout=%d", version, journalMode, synchronous, busyTimeout)
	}
}

func TestSQLiteImportIsValidatedAndNonDestructive(t *testing.T) {
	dir := t.TempDir()
	s := openTestSQLite(t, filepath.Join(dir, "run.sqlite3"))
	for _, bad := range [][]byte{
		[]byte("{}\n"),
		[]byte(`{"hash":"no"}`),
		[]byte(`{"hash":"no"}` + "\n"),
	} {
		if err := s.ImportJSONL(bad); err == nil {
			t.Fatalf("accepted invalid import %q", bad)
		}
		events, readErr := s.Read()
		if readErr != nil || len(events) != 0 {
			t.Fatalf("invalid import changed target: events=%d err=%v", len(events), readErr)
		}
	}
	if _, err := s.Append("kept", map[string]int{"n": 1}, accept); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.jsonl")
	if _, err := Append(source, "replacement", map[string]int{"n": 2}, accept); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ImportJSONL(b); err == nil {
		t.Fatal("import replaced a nonempty target")
	}
	events, err := s.Read()
	if err != nil || len(events) != 1 || events[0].Kind != "kept" {
		t.Fatalf("target changed: %#v err=%v", events, err)
	}
}

func TestSQLiteValidatorRollbackAndCommitUncertainty(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	want := errors.New("no authority")
	if _, err := s.Append("rejected", map[string]int{}, func([]Event) error { return want }); !errors.Is(err, want) {
		t.Fatalf("validator error = %v", err)
	}
	if events, err := s.Read(); err != nil || len(events) != 0 {
		t.Fatalf("validator rejection persisted: events=%d err=%v", len(events), err)
	}
	commitFailure := errors.New("connection outcome unavailable")
	s.commitTx = func(*sql.Tx) error { return commitFailure }
	if _, err := s.Append("uncertain", map[string]int{}, accept); !errors.Is(err, ErrCommitUncertain) {
		t.Fatalf("commit error was not uncertain: %v", err)
	} else if !errors.Is(err, commitFailure) {
		t.Fatalf("commit cause was lost: %v", err)
	}
	if events, err := s.Read(); err != nil || len(events) != 0 {
		t.Fatalf("test rollback failed: events=%d err=%v", len(events), err)
	}
}

func TestSQLiteContextCancellationLeavesJournalUnchanged(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AppendContext(ctx, "canceled", map[string]int{}, accept); err == nil {
		t.Fatal("append ignored canceled context")
	}
	if events, err := s.Read(); err != nil || len(events) != 0 {
		t.Fatalf("canceled append changed journal: events=%d err=%v", len(events), err)
	}
}

func TestSQLiteValidatorCannotMutateOrRetainCandidate(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	var retained []Event
	event, err := s.Append("observation", map[string]int{"n": 1}, func(events []Event) error {
		retained = events
		events[len(events)-1].Kind = "mutated"
		events[len(events)-1].Payload[5] = '9'
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retained[0].Payload[5] = '8'
	events, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "observation" || !bytes.Equal(events[0].Payload, []byte(`{"n":1}`)) || events[0].Hash != event.Hash {
		t.Fatalf("validator changed persisted event: %#v", events)
	}
}

func TestSQLiteConcurrentWritersSerializeAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.sqlite3")
	const writers = 24
	stores := make([]*SQLiteStore, writers)
	for i := range stores {
		stores[i] = openTestSQLite(t, path)
	}
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func(n int, store *SQLiteStore) {
			defer wg.Done()
			<-start
			_, err := store.Append("observation", map[string]int{"writer": n}, accept)
			errs <- err
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	events, err := stores[0].Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != writers {
		t.Fatalf("got %d events, want %d", len(events), writers)
	}
	seenHashes := make(map[string]bool, writers)
	for i, e := range events {
		if e.Sequence != i+1 || seenHashes[e.Hash] {
			t.Fatalf("invalid sequence/hash at %d: %#v", i, e)
		}
		seenHashes[e.Hash] = true
	}
}

func TestSQLiteDetectsStoredTampering(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	if _, err := s.Append("observation", map[string]int{"n": 1}, accept); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET kind = 'rewritten' WHERE sequence = 1`); err == nil {
		t.Fatal("append-only trigger admitted an update")
	}
	if _, err := s.db.Exec(`DROP TRIGGER events_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE events SET payload = ? WHERE sequence = 1`, []byte(`{"n":2}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(); err == nil {
		t.Fatal("stored tampering accepted")
	}
}

func TestSQLiteCrashAtCommitBoundaryRequiresReadback(t *testing.T) {
	if path := os.Getenv("ENGORCH_SQLITE_CRASH_PATH"); path != "" {
		s, err := OpenSQLite(path)
		if err != nil {
			os.Exit(2)
		}
		mode := os.Getenv("ENGORCH_SQLITE_CRASH_MODE")
		s.commitTx = func(tx *sql.Tx) error {
			if mode == "after" {
				if err := tx.Commit(); err != nil {
					os.Exit(3)
				}
			}
			os.Exit(0)
			return nil
		}
		_, _ = s.Append("boundary", map[string]string{"mode": mode}, accept)
		os.Exit(4)
	}
	for _, tc := range []struct {
		mode string
		want int
	}{{"before", 0}, {"after", 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.sqlite3")
			cmd := exec.Command(os.Args[0], "-test.run=^TestSQLiteCrashAtCommitBoundaryRequiresReadback$")
			cmd.Env = append(os.Environ(), "ENGORCH_SQLITE_CRASH_PATH="+path, "ENGORCH_SQLITE_CRASH_MODE="+tc.mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("child: %v: %s", err, output)
			}
			s := openTestSQLite(t, path)
			events, err := s.Read()
			if err != nil || len(events) != tc.want {
				t.Fatalf("readback after %s commit: events=%d err=%v", tc.mode, len(events), err)
			}
		})
	}
}

func TestSQLiteDatabaseConstraintsRejectOutOfOrderInsert(t *testing.T) {
	s := openTestSQLite(t, filepath.Join(t.TempDir(), "run.sqlite3"))
	_, err := s.db.Exec(`INSERT INTO events(version, sequence, previous, kind, payload, hash) VALUES (1, 2, ?, 'x', ?, ?)`,
		strings.Repeat("0", 64), []byte(`{}`), fmt.Sprintf("%064d", 1))
	if err == nil {
		t.Fatal("database admitted an out-of-order insert")
	}
}
