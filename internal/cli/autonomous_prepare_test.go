package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
)

func TestAutonomousPrepareOnlyFlagUsesBoundedRunCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--prepare-only", "A bounded fixture objective"}, root, &out)
	if err == nil || !strings.Contains(err.Error(), "autonomous run") {
		t.Fatalf("fake planner's non-graph output should stop qualification without invoking a provider: %v", err)
	}
	var failure autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &failure); err != nil {
		t.Fatalf("prepare-only did not return the durable blocked-run summary: %s (%v)", out.String(), err)
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly one durable autonomous run: %v, %v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Execution == nil || !s.Creation.Execution.GraphEnabled() || s.State != "AWAITING_APPROVAL" || s.MachineApproval != nil || len(s.PlannerCorrections) != 2 || s.RepairAttempts != 0 {
		t.Fatalf("prepare-only did not retain the bounded rejected planner inputs: %#v", s)
	}
	if s.Workspace != nil || s.Graph != nil || s.ExplorerHost != nil || len(s.Explorations) != 0 || s.WriterHost != nil || s.WriterProposal != nil {
		t.Fatal("invalid planner graph crossed the preparation boundary")
	}
	if failure.RunID != s.RunID || failure.State != s.State {
		t.Fatalf("blocked summary differs from durable run: %#v state=%s", failure, s.State)
	}

	// The prepare-only flag is autonomous-only and must reject before creating
	// a second run when placed on the legacy command form.
	var legacyOut bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--prepare-only"}, root, &legacyOut); err == nil {
		t.Fatal("legacy run accepted autonomous prepare-only flag")
	}
	entriesAfter, _ := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if len(entriesAfter) != 1 {
		t.Fatalf("invalid legacy flag created a run: %v", entriesAfter)
	}
}
