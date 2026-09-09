package agentcontrol

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/taskscheduler"
)

type interruptFixtureAdapter struct {
	anchorID string
	runID    string
}

type terminalInterruptAdapter struct{ interruptFixtureAdapter }

func (a terminalInterruptAdapter) Probe(_ context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	return taskscheduler.Evidence{RunID: request.Task.RunID, InvocationID: request.Task.InvocationID, ControllerHead: strings.Repeat("e", 64), AdmissionID: strings.Repeat("6", 64), Status: taskscheduler.StatusSucceeded}, nil
}

func (a interruptFixtureAdapter) Probe(_ context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	status := taskscheduler.StatusReady
	admission := ""
	if request.Task.ID == a.anchorID {
		status = taskscheduler.StatusSucceeded
		admission = strings.Repeat("8", 64)
	}
	return taskscheduler.Evidence{RunID: request.Task.RunID, InvocationID: request.Task.InvocationID, ControllerHead: strings.Repeat("9", 64), AdmissionID: admission, Status: status}, nil
}

func (a interruptFixtureAdapter) Dispatch(_ context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	return taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: claim.Task.InvocationID, ControllerHead: strings.Repeat("7", 64), AdmissionID: strings.Repeat("6", 64), Status: taskscheduler.StatusRunning}, nil
}

func (a interruptFixtureAdapter) Reconcile(context.Context, taskscheduler.Claim) (taskscheduler.Evidence, error) {
	panic("unexpected interrupt fixture reconciliation")
}

func TestScheduledInterruptBindsExactOpenClaimAndObservesLocalStateOnly(t *testing.T) {
	scheduled, schedulePath, controlPath, turn := interruptFixture(t)
	request, err := scheduled.RequestInterrupt(context.Background(), turn.TurnID, "operator", "interrupt-1")
	if err != nil {
		t.Fatal(err)
	}
	claim := mustInterruptClaim(t, schedulePath, turn.TurnID)
	claimID, err := claim.ID()
	if err != nil || request.Request.TreeID != scheduled.treeID || request.Request.ScheduleID != scheduled.scheduleID || request.Request.AgentTurn.TurnID != turn.TurnID || request.Request.AgentTurn.AgentID != turn.AgentID || request.Request.InvocationID != turn.Task.InvocationID || request.Request.ClaimID != claimID || request.Request.ControllerHead != claim.ControllerHead || request.Observation != nil {
		t.Fatal("interrupt request lost exact durable authority", request, err)
	}
	retry, err := scheduled.RequestInterrupt(context.Background(), turn.TurnID, "operator", "interrupt-1")
	if err != nil || !reflect.DeepEqual(retry, request) {
		t.Fatal("exact interrupt retry changed", retry, err)
	}
	if _, err := scheduled.RequestInterrupt(context.Background(), turn.TurnID, "operator", "different"); err == nil {
		t.Fatal("second interrupt intent replaced the exact request")
	}
	lookup, ok, err := scheduled.Interrupt(turn.TurnID)
	if err != nil || !ok || !reflect.DeepEqual(lookup, request) {
		t.Fatal("pending interrupt lookup differs", lookup, ok, err)
	}
	service, err := Bind(scheduled.treePath, controlPath)
	if err != nil {
		t.Fatal(err)
	}
	delivered := InterruptObservation{Version: 1, Outcome: InterruptSignalDelivered, Evidence: strings.Repeat("a", 64), ControllerHead: strings.Repeat("b", 64)}
	observed, err := service.ObserveInterrupt(context.Background(), request.RequestID, delivered)
	if err != nil || observed.Observation == nil || *observed.Observation != delivered {
		t.Fatal("interrupt delivery observation missing", observed, err)
	}
	confirmed := InterruptObservation{Version: 1, Outcome: InterruptConfirmedLocalStop, Evidence: strings.Repeat("c", 64), ControllerHead: strings.Repeat("d", 64)}
	observed, err = service.ObserveInterrupt(context.Background(), request.RequestID, confirmed)
	if err != nil || observed.Observation == nil || *observed.Observation != confirmed {
		t.Fatal("local-stop observation missing", observed, err)
	}
	if repeated, err := service.ObserveInterrupt(context.Background(), request.RequestID, confirmed); err != nil || !reflect.DeepEqual(repeated, observed) {
		t.Fatal("exact interrupt observation retry changed", repeated, err)
	}
	beforeTerminal, err := taskscheduler.Inspect(schedulePath)
	if err != nil || beforeTerminal.Tasks[turn.TurnID].Status != taskscheduler.StatusRunning {
		t.Fatal("interrupt observation mutated scheduler outcome", beforeTerminal.Tasks[turn.TurnID], err)
	}
	if _, err := taskscheduler.Tick(context.Background(), schedulePath, terminalInterruptAdapter{interruptFixtureAdapter{anchorID: strings.Repeat("2", 64), runID: turn.Task.RunID}}); err != nil {
		t.Fatal("terminal controller evidence was not adopted", err)
	}
	terminalRetry, err := scheduled.RequestInterrupt(context.Background(), turn.TurnID, "operator", "interrupt-1")
	if err != nil || !reflect.DeepEqual(terminalRetry, observed) {
		t.Fatal("exact interrupt retry failed after terminal completion", terminalRetry, err)
	}
	if _, err := scheduled.RequestInterrupt(context.Background(), turn.TurnID, "operator", "new-after-terminal"); err == nil {
		t.Fatal("new interrupt request admitted after terminal completion")
	}
	schedulerState, err := taskscheduler.Inspect(schedulePath)
	if err != nil || schedulerState.Tasks[turn.TurnID].Status != taskscheduler.StatusSucceeded {
		t.Fatal("independent terminal evidence was not retained", schedulerState.Tasks[turn.TurnID], err)
	}
	treeState, err := agenttree.Inspect(scheduled.treePath)
	target, found := nodeByID(treeState, turn.AgentID)
	if err != nil || !found || target.Status != agenttree.StatusQueued {
		t.Fatal("interrupt observation mutated agent outcome", treeState.Nodes, err)
	}
	controlState, err := Inspect(controlPath)
	if err != nil || len(controlState.Activities) != 3 || controlState.Activities[0].Interrupt != InterruptRequested || controlState.Activities[1].Interrupt != InterruptSignalDelivered || controlState.Activities[2].Interrupt != InterruptConfirmedLocalStop {
		t.Fatal("interrupt activity sequence differs", controlState.Activities, err)
	}
}

func TestScheduledInterruptRejectsClosedOrSubstitutedTurnWithoutMutation(t *testing.T) {
	scheduled, schedulePath, controlPath, turn := interruptFixture(t)
	before, err := Inspect(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, turnID := range []string{strings.Repeat("f", 64), "not-a-digest"} {
		if _, err := scheduled.RequestInterrupt(context.Background(), turnID, "operator", "interrupt"); err == nil {
			t.Fatal("substituted interrupt target admitted", turnID)
		}
	}
	after, err := Inspect(controlPath)
	if err != nil || len(after.Interrupts) != len(before.Interrupts) || len(after.Activities) != len(before.Activities) {
		t.Fatal("rejected interrupt mutated journal", after, err)
	}
	claim := mustInterruptClaim(t, schedulePath, turn.TurnID)
	if _, err := scheduled.Service.ObserveInterrupt(context.Background(), strings.Repeat("e", 64), InterruptObservation{Version: 1, Outcome: InterruptUnknown, Evidence: strings.Repeat("a", 64), ControllerHead: claim.ControllerHead}); err == nil {
		t.Fatal("observation without exact request admitted")
	}
}

func interruptFixture(t *testing.T) (*ScheduledService, string, string, taskscheduler.DynamicTask) {
	t.Helper()
	baseService, treePath, root, _ := controlFixture(t)
	directory := filepath.Dir(treePath)
	schedulePath := filepath.Join(directory, "schedule.db")
	controlPath := filepath.Join(directory, "agent-control.db")
	controllerPath := filepath.Join(directory, "controller.db")
	runID := baseService.treeID
	anchor := taskscheduler.TaskSpec{ID: strings.Repeat("2", 64), RunID: runID, ControllerPath: controllerPath, Operation: taskscheduler.OperationPlanner, InvocationID: strings.Repeat("3", 64)}
	if _, err := taskscheduler.Bind(schedulePath, taskscheduler.Definition{Version: 1, Nonce: "interrupt-fixture", Tasks: []taskscheduler.TaskSpec{anchor}}); err != nil {
		t.Fatal(err)
	}
	scheduled, err := BindScheduled(treePath, controlPath, schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	turnID := strings.Repeat("4", 64)
	invocationID := strings.Repeat("5", 64)
	reservation, err := agenttree.ReserveChild(treePath, scheduled.treeID, agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: "interruptible", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: invocationID, ContextSHA256: strings.Repeat("6", 64)})
	if err != nil {
		t.Fatal(err)
	}
	child, err := agenttree.CommitChild(treePath, reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.DynamicTask{Task: taskscheduler.TaskSpec{ID: turnID, RunID: runID, ControllerPath: controllerPath, Operation: taskscheduler.OperationExplorer, Input: "inspect interrupt fixture", InvocationID: invocationID}, ParentAgentID: root.AgentID, AgentID: child.AgentID, TurnID: turnID}
	state, err := taskscheduler.AddTask(schedulePath, turn)
	if err != nil {
		t.Fatal(err)
	}
	turn = state.Dynamic[0]
	adapter := interruptFixtureAdapter{anchorID: anchor.ID, runID: runID}
	if _, err := taskscheduler.Tick(context.Background(), schedulePath, adapter); err != nil {
		t.Fatal(err)
	}
	decision, err := taskscheduler.Tick(context.Background(), schedulePath, adapter)
	if err != nil || decision.TaskID != turnID || decision.Status != taskscheduler.StatusRunning {
		t.Fatal("interrupt fixture did not create open claim", decision, err)
	}
	return scheduled, schedulePath, controlPath, turn
}

func mustInterruptClaim(t *testing.T, schedulePath, turnID string) taskscheduler.Claim {
	t.Helper()
	state, err := taskscheduler.Inspect(schedulePath)
	if err != nil || state.Tasks[turnID].Claim == nil {
		t.Fatal("interrupt claim unavailable", err)
	}
	return *state.Tasks[turnID].Claim
}
