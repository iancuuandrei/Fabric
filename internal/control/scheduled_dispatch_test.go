package control

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskpool"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
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

func TestScheduledUsageQualificationIsNoEffectOnlyWithoutExactAdmission(t *testing.T) {
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}, "bounded writer task")
	if err != nil {
		t.Fatal(err)
	}
	ready := taskscheduler.Evidence{Status: taskscheduler.StatusReady}
	if !scheduledUsageQualificationParkable(Snapshot{}, invocation, ready) {
		t.Fatal("exact no-effect preflight was not eligible to park")
	}
	withGraphHost := Snapshot{GraphWriterHosts: map[string]WriterHostState{
		"impl-a": {Intent: WriterHostIntent{Invocation: invocation}},
	}}
	if !scheduledInvocationHasEffect(withGraphHost, invocation) {
		t.Fatal("matching graph writer intent was not recognized as an effect")
	}
	if scheduledUsageQualificationParkable(withGraphHost, invocation, ready) {
		t.Fatal("matching host intent was incorrectly treated as a no-effect refusal")
	}
	withAccess := Snapshot{ModelAccess: []ModelAccessState{{RuntimeInvocationID: invocation.ID}}}
	if !scheduledInvocationHasEffect(withAccess, invocation) {
		t.Fatal("matching model-access admission was not recognized as an effect")
	}
	if scheduledUsageQualificationParkable(withAccess, invocation, ready) {
		t.Fatal("matching model-access admission was incorrectly treated as a no-effect refusal")
	}
	reviewer, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "reviewer"}, "bounded scheduled review")
	if err != nil {
		t.Fatal(err)
	}
	withScheduledReview := Snapshot{ScheduledReviewHosts: map[string]ReviewHostState{
		"review-task": {Intent: ReviewHostIntent{Invocation: reviewer}},
	}}
	if !scheduledInvocationHasEffect(withScheduledReview, reviewer) {
		t.Fatal("matching scheduled review host intent was not recognized as an effect")
	}
	if scheduledUsageQualificationParkable(withScheduledReview, reviewer, ready) {
		t.Fatal("matching scheduled review intent was incorrectly treated as no effect")
	}
	admitted := ready
	admitted.AdmissionID = strings.Repeat("a", 64)
	if scheduledUsageQualificationParkable(Snapshot{}, invocation, admitted) {
		t.Fatal("scheduler evidence with an admission was treated as no effect")
	}
	other, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}, "different writer task")
	if err != nil {
		t.Fatal(err)
	}
	if scheduledInvocationHasEffect(withGraphHost, other) {
		t.Fatal("unrelated host intent was treated as the exact claim effect")
	}
}

func TestScheduledReviewHostLookupUsesExactCorrectionTurn(t *testing.T) {
	base, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "reviewer"}, "review correction")
	if err != nil {
		t.Fatal(err)
	}
	taskID := strings.Repeat("c", 64)
	scoped, err := scheduledTurnInvocation(base, taskscheduler.OperationReviewer, taskID)
	if err != nil {
		t.Fatal(err)
	}
	wanted := ReviewHostState{Intent: ReviewHostIntent{Invocation: scoped}, Ready: true}
	s := Snapshot{
		RoleCorrections: []RoleSemanticCorrection{{Invocation: base, ScheduledTaskID: taskID}},
		ScheduledReviewHosts: map[string]ReviewHostState{
			taskID: wanted,
		},
	}
	got := scheduledReviewHostForInvocation(s, scoped)
	if got == nil || !sameCanonical(*got, wanted) {
		t.Fatalf("scheduled reviewer correction receipt was not resolved by exact turn: %#v", got)
	}
	if got := scheduledReviewHostForInvocation(s, base); got != nil {
		t.Fatal("base reviewer invocation matched a scheduled correction host")
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

func TestScheduledTurnInvocationPreservesCodexAutoCompactOption(t *testing.T) {
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "model", Effort: "high", Role: "writer"}
	base, err := runtime.NewInvocationWithCodexAutoCompact(profile, "base input", &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 50000})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := scheduledTurnInvocation(base, taskscheduler.OperationWriter, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if scoped.CodexAutoCompactOption() == nil || *scoped.CodexAutoCompactOption() != *base.CodexAutoCompactOption() {
		t.Fatalf("scheduled invocation dropped native thread option: %+v", scoped.CodexAutoCompactOption())
	}
	if err := scoped.Validate(); err != nil {
		t.Fatal(err)
	}
}
