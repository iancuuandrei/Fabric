package taskpool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"harness.local/engorch/internal/journal"
)

func request(n int) Request {
	return Request{fmt.Sprintf("%064x", n), fmt.Sprintf("%064x", 100), fmt.Sprintf("%064x", 200), "provider", "model"}
}

func TestCancelledPoolInspectionDoesNotCreateJournal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "absent")
	if _, err := InspectContext(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatal("inspection ignored cancellation", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancelled inspection created journal", err)
	}
}
func TestAllApplicableCapacityAndExactSettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool")
	limits := Limits{Total: 4, Profiles: map[string]int{request(1).AccessProfileID: 2}, Providers: map[string]int{"provider": 3}, Models: []ModelLimit{{ModelKey{"provider", "model"}, 1}}}
	if err := Bind(path, limits); err != nil {
		t.Fatal(err)
	}
	if err := Acquire(path, request(1)); err != nil {
		t.Fatal(err)
	}
	before, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Acquire(path, request(2)); err == nil {
		t.Fatal("model ceiling ignored")
	}
	after, err := journal.ExportJSONL(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("denied acquisition changed history", err)
	}
	r := request(2)
	r.Model = "other"
	if err := Acquire(path, r); err != nil {
		t.Fatal(err)
	}
	r = request(3)
	r.Model = "third"
	if err := Acquire(path, r); err == nil {
		t.Fatal("profile ceiling ignored")
	}
	// Recovery does not release a reservation merely because its process vanished.
	s, err := Inspect(path)
	if err != nil || len(s.Active) != 2 {
		t.Fatal(s, err)
	}
	release := Release{request(1).ID, fmt.Sprintf("%064x", 300)}
	if err := Settle(path, release); err != nil {
		t.Fatal(err)
	}
	if err := Settle(path, release); err == nil {
		t.Fatal("duplicate release accepted")
	}
	if err := Acquire(path, request(1)); err == nil {
		t.Fatal("old attempt reused")
	}
	if err := Acquire(path, r); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentAcquisitionCannotOversubscribe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool")
	if err := Bind(path, Limits{Total: 3}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 32)
	for i := 1; i <= 32; i++ {
		wg.Add(1)
		go func(n int) { defer wg.Done(); results <- Acquire(path, request(n)) }(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	s, err := Inspect(path)
	if err != nil || accepted != 3 || len(s.Active) != 3 {
		t.Fatal("capacity accounting mismatch", accepted, s, err)
	}
}

func TestModelIdentityDoesNotAliasSlashConcatenation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool")
	if err := Bind(path, Limits{Total: 4, Models: []ModelLimit{{ModelKey{"a/b", "c"}, 1}}}); err != nil {
		t.Fatal(err)
	}
	a := request(1)
	a.Provider = "a/b"
	a.Model = "c"
	b := request(2)
	b.Provider = "a"
	b.Model = "b/c"
	if err := Acquire(path, a); err != nil {
		t.Fatal(err)
	}
	if err := Acquire(path, b); err != nil {
		t.Fatal("provider/model identity collision", err)
	}
}

func TestSharedPoolAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool")
	if err := Bind(path, Limits{Total: 8, Providers: map[string]int{"provider": 2}}); err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 6)
	for i := range commands {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSharedPoolProcessHelper$")
		cmd.Env = append(os.Environ(), "ENGORCH_POOL_TEST_PATH="+path, fmt.Sprintf("ENGORCH_POOL_TEST_ID=%d", i+1))
		if err := cmd.Start(); err != nil {
			for _, started := range commands[:i] {
				_ = started.Wait()
			}
			t.Fatal(err)
		}
		commands[i] = cmd
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Error("pool subprocess failed", err)
		}
	}
	snapshot, err := Inspect(path)
	if err != nil || len(snapshot.Active) != 2 || len(snapshot.Settled) != 0 {
		t.Fatal("cross-process provider ceiling or unresolved retention failed", snapshot, err)
	}
}

func TestSharedPoolProcessHelper(t *testing.T) {
	path := os.Getenv("ENGORCH_POOL_TEST_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	var n int
	if _, err := fmt.Sscan(os.Getenv("ENGORCH_POOL_TEST_ID"), &n); err != nil || n < 1 {
		t.Fatal("invalid subprocess identity", err)
	}
	r := request(n)
	r.RunID = fmt.Sprintf("%064x", n+1000)
	if err := Acquire(path, r); err != nil && !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
}
