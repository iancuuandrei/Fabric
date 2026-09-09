package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/taskpool"
)

func TestPoolStatusRetainsActiveAttemptsAndRejectsLimitDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool")
	cfg := &config.TaskPool{Version: 1, Path: path, Limits: taskpool.Limits{Total: 2}}
	var out bytes.Buffer
	if err := poolStatus(context.Background(), cfg, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created pool", err)
	}
	if err := taskpool.Bind(path, cfg.Limits); err != nil {
		t.Fatal(err)
	}
	request := taskpool.Request{ID: strings.Repeat("a", 64), RunID: strings.Repeat("b", 64), AccessProfileID: strings.Repeat("c", 64), Provider: "fixture", Model: "exact-model"}
	if err := taskpool.Acquire(path, request); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := poolStatus(context.Background(), cfg, &out); err != nil || !strings.Contains(out.String(), request.ID) {
		t.Fatal("missing active attempt", err, out.String())
	}
	snapshot, err := taskpool.Inspect(path)
	if err != nil || len(snapshot.Active) != 1 || len(snapshot.Settled) != 0 {
		t.Fatal("inspection changed capacity", err)
	}
	cfg.Limits.Total++
	out.Reset()
	if err := poolStatus(context.Background(), cfg, &out); err == nil || out.Len() != 0 {
		t.Fatal("limit drift accepted")
	}
}
