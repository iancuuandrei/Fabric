package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/control"
)

func TestExternalControllerStateRoutesRunLifecycleOutsideRepository(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "source")
	stateRoot := filepath.Join(base, "controller-state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	configuration := fmt.Sprintf(`version = 1
repository = "external-state-fixture"
base_branch = "main"
controller_state_root = %q

[planner]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "planner"

[[verification]]
name = "unit"
argv = ["git", "--version"]
timeout_seconds = 120
`, stateRoot)
	if err := os.WriteFile(filepath.Join(root, "harness.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var output bytes.Buffer
		if err := Execute(context.Background(), args, root, &output); err != nil {
			t.Fatal(err)
		}
		return output.Bytes()
	}
	run("doctor")
	if _, err := os.Stat(stateRoot); !os.IsNotExist(err) {
		t.Fatal("doctor created external controller state", err)
	}
	var emptyStatuses []runStatus
	if err := json.Unmarshal(run("status"), &emptyStatuses); err != nil || len(emptyStatuses) != 0 {
		t.Fatal("fresh external controller state did not report an empty status", emptyStatuses, err)
	}
	if _, err := os.Stat(stateRoot); !os.IsNotExist(err) {
		t.Fatal("empty status created external controller state", err)
	}
	var planned control.Snapshot
	if err := json.Unmarshal(run("plan", "external mutable state"), &planned); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".harness")); !os.IsNotExist(err) {
		t.Fatal("external mode wrote repository-local controller state", err)
	}
	journalPath, err := runPath(root, planned.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(journalPath, stateRoot+string(filepath.Separator)) {
		t.Fatal("run journal was not routed to external controller state", journalPath)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatal("external run journal missing", err)
	}
	var inspected control.Snapshot
	if err := json.Unmarshal(run("inspect", planned.RunID), &inspected); err != nil || inspected.RunID != planned.RunID {
		t.Fatal("external run inspect failed", err)
	}
	var paused control.Snapshot
	if err := json.Unmarshal(run("pause", planned.RunID, "fixture", "pause-1"), &paused); err != nil || paused.Lifecycle.Status != control.LifecyclePauseRequested {
		t.Fatal("external run pause failed", err)
	}
	if err := json.Unmarshal(run("settle-lifecycle", planned.RunID, "fixture", "no active workload", "workloads-stopped"), &paused); err != nil || paused.Lifecycle.Status != control.LifecyclePaused {
		t.Fatal("external run settlement failed", err)
	}
	if err := json.Unmarshal(run("resume", planned.RunID, "fixture", "resume-1"), &paused); err != nil || paused.Lifecycle.Status != control.LifecycleActive {
		t.Fatal("external run resume failed", err)
	}
	var statuses []runStatus
	if err := json.Unmarshal(run("status"), &statuses); err != nil || len(statuses) != 1 || statuses[0].RunID != planned.RunID {
		t.Fatal("external run status failed", statuses, err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("advanced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "advance fixture")
	if err := json.Unmarshal(run("inspect", planned.RunID), &inspected); err != nil || inspected.RunID != planned.RunID {
		t.Fatal("HEAD advance made old external run undiscoverable", err)
	}
	statuses = nil
	if err := json.Unmarshal(run("status"), &statuses); err != nil || len(statuses) != 1 || statuses[0].RunID != planned.RunID {
		t.Fatal("HEAD advance removed old run from external status", statuses, err)
	}
}
