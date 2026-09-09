package journal

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func accept([]Event) error { return nil }

func TestReplayRejectsTamperingAndTornTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if err := os.WriteFile(p, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		if _, err := Append(p, "observation", map[string]int{"n": n}, accept); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	events, err := Replay(b)
	if err != nil || len(events) != 3 {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{b[:len(b)-1], bytes.Replace(b, []byte(`"n":1`), []byte(`"n":9`), 1), append([]byte(" "), b...), append(b, '\n')} {
		if _, err := Replay(bad); err == nil {
			t.Fatal("corruption accepted")
		}
	}
}

func TestValidatorFailureDoesNotAppend(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.jsonl")
	want := errors.New("no authority")
	if _, err := Append(p, "bad", map[string]int{}, func([]Event) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("created journal despite rejection")
	}
}

func TestConcurrentAppendSerializesOrRejects(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.jsonl")
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for n := 0; n < 20; n++ {
		wg.Go(func() {
			if _, err := Append(p, "observation", map[string]int{}, accept); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	events, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != success || success == 0 {
		t.Fatalf("events %d successes %d", len(events), success)
	}
}

func TestStaleLockIsNeverStolen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if err := os.WriteFile(p+".lock", []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil {
		t.Fatal("read through lock")
	}
	if _, err := Append(p, "x", map[string]int{}, accept); err == nil {
		t.Fatal("stole lock")
	}
}

func TestCrashedProcessLeavesExplicitLock(t *testing.T) {
	if path := os.Getenv("HARNESS_TEST_CRASH_LOCK"); path != "" {
		if _, err := lock(path); err != nil {
			os.Exit(2)
		}
		// Deliberately terminate without release, as a killed append owner would.
		os.Exit(0)
	}
	p := filepath.Join(t.TempDir(), "run.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashedProcessLeavesExplicitLock$")
	cmd.Env = append(os.Environ(), "HARNESS_TEST_CRASH_LOCK="+p)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	if _, err := Read(p); err == nil {
		t.Fatal("crashed writer lock stolen")
	}
}

func FuzzReplay(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("{}\n"))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Replay(b) })
}
