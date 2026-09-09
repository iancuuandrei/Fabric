package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/worktree"
)

func TestPrepareAndProbeScheduledTaskBindExactInvocation(t *testing.T) {
	controllerPath, snapshot, invocation := agentDispatchFixture(t)
	task, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	task.ID = strings.Repeat("1", 64)
	if task.RunID != snapshot.RunID || task.InvocationID != invocation.ID || task.ControllerPath != controllerPath {
		t.Fatal("scheduled task did not bind exact controller invocation", task)
	}
	evidence, err := (ScheduledDispatchAdapter{}).Probe(context.Background(), taskscheduler.ProbeRequest{Task: task})
	if err != nil || evidence.Status != taskscheduler.StatusReady || evidence.AdmissionID != "" || evidence.ControllerHead == "" {
		t.Fatal("unexpected ready scheduler evidence", evidence, err)
	}
	task.InvocationID = strings.Repeat("2", 64)
	if _, err := (ScheduledDispatchAdapter{}).Probe(context.Background(), taskscheduler.ProbeRequest{Task: task}); err == nil {
		t.Fatal("substituted scheduled invocation admitted")
	}
}

func TestScheduledProbeBindsExactDynamicAgentTurn(t *testing.T) {
	controllerPath, snapshot, planner := agentDispatchFixture(t)
	plannerContext, err := access.InputID(planner.Input)
	if err != nil {
		t.Fatal(err)
	}
	root, err := agenttree.Create(controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: plannerContext})
	if err != nil {
		t.Fatal(err)
	}
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: "explorer"}
	base, err := runtime.NewInvocation(profile, "Inspect the exact candidate.")
	if err != nil {
		t.Fatal(err)
	}
	turnID := strings.Repeat("9", 64)
	invocation, err := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, turnID)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := agenttree.ReserveChild(controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: "dynamic-explorer", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: invocation.ID, ContextSHA256: contextHash})
	if err != nil {
		t.Fatal(err)
	}
	node, err := agenttree.CommitChild(controllerPath+".agent-tree", reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: turnID, RunID: snapshot.RunID, ControllerPath: controllerPath, Operation: taskscheduler.OperationExplorer, Input: "Inspect the exact candidate.", InvocationID: invocation.ID}
	turn := &taskscheduler.AgentTurnBinding{ParentAgentID: root.AgentID, AgentID: node.AgentID, TurnID: task.ID, TurnSequence: 1}
	if err := validateScheduledAgentTurn(task, snapshot, invocation, turn); err != nil {
		t.Fatal(err)
	}
	if _, err := beginAgentDispatchForTurn(controllerPath, snapshot, invocation, turn); err != nil {
		t.Fatal(err)
	}
	admitted, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := resolveScheduledRecordedInvocation(admitted, base, invocation); err != nil || resolved != invocation {
		t.Fatal("turn-scoped invocation did not replay through exact admission", resolved, err)
	}
	substituted := *turn
	substituted.AgentID = strings.Repeat("8", 64)
	if err := validateScheduledAgentTurn(task, snapshot, invocation, &substituted); err == nil {
		t.Fatal("substituted dynamic agent turn accepted")
	}
}

func TestScheduledTurnInvocationSeparatesIdenticalFollowups(t *testing.T) {
	base, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "gpt-5.2-codex", Effort: "high", Role: "explorer"}, "same bounded question")
	if err != nil {
		t.Fatal(err)
	}
	first, err := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, strings.Repeat("6", 64))
	if err != nil {
		t.Fatal(err)
	}
	second, err := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == base.ID || second.ID == base.ID || base.Input != "same bounded question" {
		t.Fatal("scheduled turn identity did not scope identical follow-up bodies", base.ID, first.ID, second.ID)
	}
}

func TestScheduledLeaseContentionParksOnlyBeforeAdmission(t *testing.T) {
	ready := taskscheduler.Evidence{Status: taskscheduler.StatusReady}
	if _, err := classifyScheduledDispatchError(worktree.ErrLeaseContention, ready, nil); err == nil {
		t.Fatal("pre-admission lease contention was not parked")
	} else {
		var parked *taskscheduler.ParkError
		if !errors.As(err, &parked) || parked.Reason != taskscheduler.ParkNoEffect {
			t.Fatal("pre-admission lease contention classification", err)
		}
	}
	unknown := taskscheduler.Evidence{Status: taskscheduler.StatusUnknown, AdmissionID: strings.Repeat("a", 64)}
	if _, err := classifyScheduledDispatchError(worktree.ErrLeaseContention, unknown, nil); !errors.Is(err, worktree.ErrLeaseContention) {
		t.Fatal("admitted lease contention did not retain uncertainty", err)
	} else {
		var parked *taskscheduler.ParkError
		if errors.As(err, &parked) {
			t.Fatal("admitted lease contention was incorrectly parked", err)
		}
	}
	tamper := errors.New("guard substituted")
	if _, err := classifyScheduledDispatchError(tamper, ready, nil); !errors.Is(err, tamper) {
		t.Fatal("guard tamper was reclassified", err)
	} else {
		var parked *taskscheduler.ParkError
		if errors.As(err, &parked) {
			t.Fatal("guard tamper was incorrectly parked", err)
		}
	}
}

func TestScheduledDispatchParksOnFiniteCapacityDenial(t *testing.T) {
	poolPath := filepath.Join(t.TempDir(), "pool.jsonl")
	controllerPath, _, _ := agentDispatchFixtureWithConfig(t, func(c *config.Config) {
		c.TaskPool = &config.TaskPool{Version: 1, Path: poolPath, Limits: taskpool.Limits{Total: 1}}
	})
	task, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	task.ID = strings.Repeat("3", 64)
	if err := taskpool.Bind(poolPath, taskpool.Limits{Total: 1}); err != nil {
		t.Fatal(err)
	}
	blocker := taskpool.Request{ID: strings.Repeat("4", 64), RunID: strings.Repeat("5", 64), AccessProfileID: strings.Repeat("6", 64), Provider: "other", Model: "other"}
	if err := taskpool.Acquire(poolPath, blocker); err != nil {
		t.Fatal(err)
	}
	head, err := controllerJournalHead(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: task, Generation: 1, ControllerHead: head}
	_, err = ExecuteScheduledClaim(context.Background(), controllerPath, claim)
	var parked *taskscheduler.ParkError
	if !errors.As(err, &parked) || parked.Reason != taskscheduler.ParkCapacity {
		t.Fatal("finite capacity denial was not parked", err)
	}
	s, err := Inspect(controllerPath)
	if err != nil || s.AgentDispatch[task.InvocationID].Observation != nil {
		t.Fatal("capacity-denied schedule fabricated runtime outcome", s.AgentDispatch[task.InvocationID], err)
	}
}

func TestScheduledClaimRejectsPathAndHeadSubstitutionBeforeAdmission(t *testing.T) {
	controllerPath, _, _ := agentDispatchFixture(t)
	task, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	task.ID = strings.Repeat("8", 64)
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("9", 64), Task: task, Generation: 1, ControllerHead: strings.Repeat("a", 64)}
	if _, err := ExecuteScheduledClaim(context.Background(), filepath.Join(t.TempDir(), "other.jsonl"), claim); err == nil {
		t.Fatal("scheduled controller path substitution admitted")
	}
	if _, err := ExecuteScheduledClaim(context.Background(), controllerPath, claim); err == nil {
		t.Fatal("stale scheduled controller head admitted")
	} else {
		var parked *taskscheduler.ParkError
		if !errors.As(err, &parked) || parked.Reason != taskscheduler.ParkNoEffect {
			t.Fatal("stale pre-admission head was not finitely parked", err)
		}
	}
	s, err := Inspect(controllerPath)
	if err != nil || len(s.AgentDispatch) != 0 {
		t.Fatal("rejected scheduled claim mutated controller", s.AgentDispatch, err)
	}
}
