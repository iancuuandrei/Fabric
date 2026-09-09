package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/taskscheduler"
)

func TestRepositoryScheduleAdapterRejectsForeignDynamicTask(t *testing.T) {
	root, foreign := t.TempDir(), t.TempDir()
	task := taskscheduler.TaskSpec{RunID: strings.Repeat("a", 64), ControllerPath: filepath.Join(foreign, "run.jsonl")}
	adapter := repositoryScheduleAdapter{root: root}
	ctx := context.Background()
	if _, err := adapter.Probe(ctx, taskscheduler.ProbeRequest{Task: task}); err == nil {
		t.Fatal("foreign probe accepted")
	}
	if _, err := adapter.Dispatch(ctx, taskscheduler.Claim{Task: task}); err == nil {
		t.Fatal("foreign dispatch accepted")
	}
	if _, err := adapter.Reconcile(ctx, taskscheduler.Claim{Task: task}); err == nil {
		t.Fatal("foreign recovery accepted")
	}
	for _, directory := range []string{root, foreign} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejection changed repository: %v %v", entries, err)
		}
	}
}
