package control

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

type interruptSchedulerAdapter struct {
	seedID string
}

func (a interruptSchedulerAdapter) Probe(_ context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	status, admission := taskscheduler.StatusReady, ""
	if request.Task.ID == a.seedID {
		status, admission = taskscheduler.StatusSucceeded, strings.Repeat("d", 64)
	}
	return taskscheduler.Evidence{RunID: request.Task.RunID, InvocationID: request.Task.InvocationID, ControllerHead: strings.Repeat("e", 64), AdmissionID: admission, Status: status}, nil
}

func (interruptSchedulerAdapter) Dispatch(_ context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	return taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: claim.Task.InvocationID, ControllerHead: strings.Repeat("f", 64), AdmissionID: strings.Repeat("c", 64), Status: taskscheduler.StatusRunning}, nil
}

func (interruptSchedulerAdapter) Reconcile(_ context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	return taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: claim.Task.InvocationID, ControllerHead: strings.Repeat("f", 64), AdmissionID: strings.Repeat("c", 64), Status: taskscheduler.StatusUnknown}, nil
}

type scheduledInterruptFixture struct {
	controllerPath string
	schedulerPath  string
	turnID         string
	claim          taskscheduler.Claim
}

func newScheduledInterruptFixture(t *testing.T) scheduledInterruptFixture {
	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	creation := scheduledOpenCodeCreation(t, "https://api.example.test/v1/responses", filepath.Join(root, "opencode.exe"), filepath.Join(root, "opencode-state"))
	if err := Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(context.Background(), controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := StartWorkspace(context.Background(), controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := runtime.NewInvocation(snapshot.Creation.Config.Planner, snapshot.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	plannerContext, err := access.InputID(planner.Input)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, err := agenttree.Create(controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: plannerContext})
	if err != nil {
		t.Fatal(err)
	}
	question := "Inspect the exact candidate."
	seed, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("a", 64)
	schedulerPath := filepath.Join(t.TempDir(), "scheduler.jsonl")
	if _, err := taskscheduler.Bind(schedulerPath, taskscheduler.Definition{Version: 1, Nonce: "interrupt-watcher", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	_, dynamic, err := SpawnExplorerAgent(context.Background(), controllerPath, schedulerPath, rootAgent.AgentID, "interruptible", question, "interruptible-turn")
	if err != nil {
		t.Fatal(err)
	}
	turnID := dynamic.TurnID
	adapter := interruptSchedulerAdapter{seedID: seed.ID}
	if decision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter); err != nil || decision.TaskID != seed.ID || decision.Status != taskscheduler.StatusSucceeded {
		t.Fatal("seed scheduling failed", decision, err)
	}
	decision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter)
	if err != nil || decision.TaskID != turnID || decision.Status != taskscheduler.StatusRunning {
		t.Fatal("dynamic scheduling failed", decision, err)
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil || scheduled.Tasks[turnID].Claim == nil {
		t.Fatal("open dynamic claim unavailable", err)
	}
	claim := *scheduled.Tasks[turnID].Claim
	_, _, derived, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil || derived.ID != claim.Task.InvocationID {
		t.Fatal("fixture scheduled invocation changed", claim.Task.InvocationID, derived.ID, err)
	}
	return scheduledInterruptFixture{controllerPath: controllerPath, schedulerPath: schedulerPath, turnID: turnID, claim: claim}
}

func TestScheduledInterruptWatcherConsumesDurableRequest(t *testing.T) {
	fixture := newScheduledInterruptFixture(t)
	requested, err := RequestAgentInterrupt(context.Background(), fixture.controllerPath, fixture.schedulerPath, fixture.turnID, "operator", "stop-once")
	if err != nil || requested.Observation != nil {
		t.Fatal("interrupt request failed", requested, err)
	}
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	executionCtx, watcher, err := watchScheduledAgentInterrupt(parentCtx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("durable interrupt was not observed by running watcher")
	}
	_, _, invocation, err := scheduledInvocation(fixture.claim.Task, fixture.claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	lookup := scheduledInterruptShutdownLookup(executionCtx)
	expected, ok, err := lookup(opencoderuntime.Intent{Invocation: invocation})
	if err != nil || !ok || expected.RequestID != requested.RequestID || expected.Request != requested.Request {
		t.Fatal("runtime cleanup lost the exact observed interrupt request", expected, ok, err)
	}
	if err := watcher.finish(); err != nil {
		t.Fatal(err)
	}
	if parentCtx.Err() != nil {
		t.Fatal("watcher cleanup cancelled its parent context")
	}
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	interrupt := state.Interrupts[requested.RequestID]
	if err != nil || interrupt.Observation == nil || interrupt.Observation.Outcome != agentcontrol.InterruptUnknown {
		t.Fatal("interrupt did not retain unknown teardown state", interrupt, err)
	}
}

func TestScheduledInterruptWatcherConsumesCrossProcessRequest(t *testing.T) {
	fixture := newScheduledInterruptFixture(t)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	executionCtx, watcher, err := watchScheduledAgentInterrupt(parentCtx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	helperCtx, stopHelper := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopHelper()
	command := exec.CommandContext(helperCtx, os.Args[0], "-test.run=^TestScheduledInterruptRequestHelper$")
	command.Env = append(os.Environ(),
		"ENGORCH_INTERRUPT_HELPER=1",
		"ENGORCH_INTERRUPT_CONTROLLER="+fixture.controllerPath,
		"ENGORCH_INTERRUPT_SCHEDULER="+fixture.schedulerPath,
		"ENGORCH_INTERRUPT_TURN="+fixture.turnID,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("interrupt helper failed: %v: %s", err, output)
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cross-process interrupt was not observed by running watcher")
	}
	if err := watcher.finish(); err != nil {
		t.Fatal(err)
	}
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(state.Interrupts) != 1 {
		t.Fatal("cross-process interrupt evidence unavailable", state.Interrupts, err)
	}
	for _, interrupt := range state.Interrupts {
		if interrupt.Observation == nil || interrupt.Observation.Outcome != agentcontrol.InterruptUnknown {
			t.Fatal("cross-process interrupt did not retain unknown teardown state", interrupt)
		}
	}
}

func TestAgentInterruptRequestRemainsAvailableDuringLifecycleStop(t *testing.T) {
	for _, test := range []struct {
		name string
		stop func(string) error
	}{
		{"pause", func(path string) error {
			_, err := RequestPause(path, "operator", "pause-before-interrupt")
			return err
		}},
		{"cancel", func(path string) error {
			_, err := RequestCancel(path, "operator", "cancel-before-interrupt")
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newScheduledInterruptFixture(t)
			if err := test.stop(fixture.controllerPath); err != nil {
				t.Fatal(err)
			}
			requested, err := RequestAgentInterrupt(context.Background(), fixture.controllerPath, fixture.schedulerPath, fixture.turnID, "operator", "stop-during-lifecycle")
			if err != nil || requested.Request.AgentTurn.TurnID != fixture.turnID || requested.Observation != nil {
				t.Fatal("stop-only interrupt was rejected by lifecycle gate", requested, err)
			}
		})
	}
}

func TestScheduledInterruptWatcherCancelsChildOnJournalIntegrityLoss(t *testing.T) {
	fixture := newScheduledInterruptFixture(t)
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	executionCtx, watcher, err := watchScheduledAgentInterrupt(parentCtx, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.controllerPath+".agent-control", []byte("not-a-journal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("journal integrity loss did not cancel owned child context")
	}
	if err := watcher.finish(); err == nil {
		t.Fatal("journal integrity loss was hidden")
	}
	if parentCtx.Err() != nil {
		t.Fatal("journal integrity loss cancelled parent context")
	}
}

func TestScheduledInterruptRequestHelper(t *testing.T) {
	if os.Getenv("ENGORCH_INTERRUPT_HELPER") != "1" {
		return
	}
	if _, err := RequestAgentInterrupt(context.Background(), os.Getenv("ENGORCH_INTERRUPT_CONTROLLER"), os.Getenv("ENGORCH_INTERRUPT_SCHEDULER"), os.Getenv("ENGORCH_INTERRUPT_TURN"), "helper", "cross-process-stop"); err != nil {
		t.Fatal(err)
	}
}
