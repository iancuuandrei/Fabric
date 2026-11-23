package control

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

func isolatedWriterCreation(t *testing.T) Creation {
	t.Helper()
	c := graphCreation(t, 1)
	c.Execution.RepairPlanningVersion = 1
	c.Execution.IsolatedImplementationVersion = 1
	c.Config.PlannerContract = "plan-graph-v7"
	c.Config.WriterContract = "anchored-edits-v1"
	c.Config.ExplorerContract = "json-v2"
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *c.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: c.Config.Writer.Provider, Model: c.Config.Writer.Model}
	c.Execution.IsolationEstimate = &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	c.Execution.IsolationCapacity = &engineeringplan.ResourceCapacity{
		CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, TotalRuntimeSlots: 1,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: route.Provider, Slots: 1}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: route.Provider, Model: route.Model}, Slots: 1}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 1}},
	}
	c.Config.ControllerStateRoot = filepath.Join(t.TempDir(), "controller-state")
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIsolatedWriterInvocationUsesConfirmedChildBindings(t *testing.T) {
	c := isolatedWriterCreation(t)
	path, s := isolatedWriterGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, _ = isolateAndAdmitWriterTaskForTest(t, path, "one")
	invocation, err := writerInvocationForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		CandidateID        string                           `json:"candidate_id"`
		TaskContext        *TaskContextRecord               `json:"task_context"`
		ImplementationTask *writerImplementationContext     `json:"implementation_task"`
		Isolation          *isolatedWriterInvocationContext `json:"isolation"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	parentID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	workspaceID, err := binding.Workspace.ID()
	if err != nil {
		t.Fatal(err)
	}
	if childID == parentID || input.CandidateID != childID || input.Isolation == nil || input.Isolation.TaskID != "one" || input.Isolation.WorkspaceID != workspaceID || input.Isolation.BaseCandidateID != parentID || input.Isolation.ChildCandidateID != childID {
		t.Fatalf("writer input was not bound to confirmed child: parent=%s child=%s input=%+v", parentID, childID, input)
	}
	if input.TaskContext == nil || input.TaskContext.IsolationTaskID != "one" || input.TaskContext.CandidateID != childID {
		t.Fatalf("writer task context was not child-bound: %+v", input.TaskContext)
	}
	if input.ImplementationTask == nil || input.ImplementationTask.ID != "one" || len(input.ImplementationTask.WritePaths) != 1 || input.ImplementationTask.WritePaths[0] != "file.txt" {
		t.Fatalf("writer task scope mismatch: %+v", input.ImplementationTask)
	}
	latest, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Candidate == nil {
		t.Fatal("parent candidate disappeared while building isolated invocation")
	}
	latestParentID, err := latest.Candidate.ID()
	if err != nil || latestParentID != parentID {
		t.Fatalf("isolated invocation changed parent candidate binding: got %s want %s (%v)", latestParentID, parentID, err)
	}
	if got, err := writerInvocationForTaskWithIsolation(s, "one", &isolatedWriterBinding{TaskID: "substituted"}); err == nil || got.ID != "" {
		t.Fatal("substituted child binding produced an invocation")
	}
}

func isolatedWriterGraphFixture(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	// Both the isolated writer and the isolated/staged OpenCode fixtures share
	// one replay-validated controller bootstrap; only their graphs differ.
	// For the v1 writer creation the shared planning access-intent branch is
	// inert, preserving the exact fixture bytes.
	return isolatedWriterControllerFixture(t, c, directGraphFixture())
}
