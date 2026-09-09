package taskscheduler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/taskpool"
)

type fakeAdapter struct {
	mu         sync.Mutex
	evidence   map[string]Evidence
	dispatches map[string]int
	dispatch   func(Claim) (Evidence, error)
	reconcile  func(Claim) (Evidence, error)
}

func (f *fakeAdapter) Reconcile(_ context.Context, claim Claim) (Evidence, error) {
	if f.reconcile != nil {
		return f.reconcile(claim)
	}
	return f.Probe(context.Background(), ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
}

func (f *fakeAdapter) Probe(_ context.Context, request ProbeRequest) (Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	evidence, ok := f.evidence[request.Task.ID]
	if !ok {
		return Evidence{}, errors.New("missing fake evidence")
	}
	return evidence, nil
}
func (f *fakeAdapter) Dispatch(_ context.Context, claim Claim) (Evidence, error) {
	f.mu.Lock()
	f.dispatches[claim.Task.ID]++
	f.mu.Unlock()
	if f.dispatch != nil {
		return f.dispatch(claim)
	}
	return admittedEvidence(claim.Task, StatusSucceeded, 'e'), nil
}
func (f *fakeAdapter) set(taskID string, evidence Evidence) {
	f.mu.Lock()
	f.evidence[taskID] = evidence
	f.mu.Unlock()
}
func (f *fakeAdapter) count(taskID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dispatches[taskID]
}

func TestCrossRunCapacityParksThenResumesExactTask(t *testing.T) {
	poolPath := filepath.Join(t.TempDir(), "pool.db")
	if err := taskpool.Bind(poolPath, taskpool.Limits{Total: 1}); err != nil {
		t.Fatal(err)
	}
	definition := scheduleDefinition(t, TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "a.db"), Operation: OperationPlanner, InvocationID: digest('1')}, TaskSpec{ID: "b", RunID: digest('b'), ControllerPath: filepath.Join(t.TempDir(), "b.db"), Operation: OperationWriter, InvocationID: digest('2')})
	path := filepath.Join(t.TempDir(), "schedule.db")
	if _, err := Bind(path, definition); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{}, dispatches: map[string]int{}}
	for _, task := range definition.Tasks {
		adapter.evidence[task.ID] = readyEvidence(task, 'c')
	}
	requests := map[string]taskpool.Request{}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		request := taskpool.Request{ID: digest(rune(claim.Task.ID[0])), RunID: claim.Task.RunID, AccessProfileID: digest('d'), Provider: "provider", Model: "model"}
		requests[claim.Task.ID] = request
		if err := taskpool.Acquire(poolPath, request); err != nil {
			if errors.Is(err, taskpool.ErrCapacity) {
				return Evidence{}, &ParkError{Reason: ParkCapacity}
			}
			return Evidence{}, err
		}
		evidence := admittedEvidence(claim.Task, StatusRunning, 'd')
		adapter.set(claim.Task.ID, evidence)
		return evidence, nil
	}
	first, err := Tick(context.Background(), path, adapter)
	if err != nil || first.TaskID != "a" || first.Status != StatusRunning {
		t.Fatal(first, err)
	}
	second, err := Tick(context.Background(), path, adapter)
	if err != nil || second.TaskID != "b" || second.Status != StatusParked {
		t.Fatal(second, err)
	}
	state, err := Inspect(path)
	if err != nil || state.Tasks["b"].Generation != 1 || state.Tasks["b"].ParkReason != ParkCapacity {
		t.Fatal(state, err)
	}
	if err := taskpool.Settle(poolPath, taskpool.Release{ID: requests["a"].ID, ReceiptSHA256: digest('f')}); err != nil {
		t.Fatal(err)
	}
	adapter.set("a", admittedEvidence(definition.Tasks[0], StatusSucceeded, 'f'))
	third, err := Tick(context.Background(), path, adapter)
	if err != nil || third.TaskID != "b" || third.Status != StatusRunning {
		t.Fatal(third, err)
	}
	if adapter.count("a") != 1 || adapter.count("b") != 2 {
		t.Fatalf("dispatch counts a=%d b=%d", adapter.count("a"), adapter.count("b"))
	}
	state, err = Inspect(path)
	if err != nil || state.Tasks["a"].Status != StatusSucceeded || state.Tasks["b"].Generation != 2 {
		t.Fatal(state, err)
	}
}

func TestUnknownBlocksDependencyAndRecoveryDoesNotRedispatch(t *testing.T) {
	a := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "a.db"), Operation: OperationPlanner, InvocationID: digest('1')}
	b := TaskSpec{ID: "b", DependsOn: []string{"a"}, RunID: digest('b'), ControllerPath: filepath.Join(t.TempDir(), "b.db"), Operation: OperationReviewer, InvocationID: digest('2')}
	definition := scheduleDefinition(t, a, b)
	path := filepath.Join(t.TempDir(), "schedule.db")
	if _, err := Bind(path, definition); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": readyEvidence(a, 'c'), "b": readyEvidence(b, 'd')}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		if claim.Task.ID == "a" {
			evidence := admittedEvidence(a, StatusUnknown, 'e')
			adapter.set("a", evidence)
			return evidence, nil
		}
		return admittedEvidence(b, StatusSucceeded, 'f'), nil
	}
	decision, err := Tick(context.Background(), path, adapter)
	if err != nil || decision.Status != StatusUnknown {
		t.Fatal(decision, err)
	}
	state, _ := Inspect(path)
	if state.Tasks["b"].Status != StatusBlocked {
		t.Fatal(state.Tasks)
	}
	if _, err = RecoverClaim(context.Background(), path, decision.ClaimID, adapter); err != nil {
		t.Fatal(err)
	}
	if adapter.count("a") != 1 || adapter.count("b") != 0 {
		t.Fatal("UNKNOWN was redispatched")
	}
	adapter.reconcile = func(claim Claim) (Evidence, error) {
		return admittedEvidence(a, StatusSucceeded, 'f'), nil
	}
	reconciled, err := RecoverClaim(context.Background(), path, decision.ClaimID, adapter)
	if err != nil || reconciled.Status != StatusSucceeded || adapter.count("a") != 1 {
		t.Fatal("UNKNOWN reconciliation dispatched again", reconciled, err)
	}
	next, err := Tick(context.Background(), path, adapter)
	if err != nil || next.TaskID != "b" || next.Status != StatusSucceeded {
		t.Fatal(next, err)
	}
	if adapter.count("a") != 1 || adapter.count("b") != 1 {
		t.Fatal("unexpected dispatch count")
	}
}

func TestFailedDependencyPropagatesWithoutDispatch(t *testing.T) {
	a := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "a"), Operation: OperationPlanner, InvocationID: digest('1')}
	b := TaskSpec{ID: "b", DependsOn: []string{"a"}, RunID: digest('b'), ControllerPath: filepath.Join(t.TempDir(), "b"), Operation: OperationWriter, InvocationID: digest('2')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, a, b)); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": readyEvidence(a, 'c'), "b": readyEvidence(b, 'd')}, dispatches: map[string]int{}, dispatch: func(claim Claim) (Evidence, error) { return admittedEvidence(claim.Task, StatusFailed, 'e'), nil }}
	if _, err := Tick(context.Background(), path, adapter); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil || state.Tasks["b"].Status != StatusFailed || state.Tasks["b"].BlockedBy != "a" || adapter.count("b") != 0 {
		t.Fatal(state, err)
	}
}

func TestTickAdoptsExistingTerminalControllerEvidenceWithoutDispatch(t *testing.T) {
	task := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, task)); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": admittedEvidence(task, StatusSucceeded, 'd')}, dispatches: map[string]int{}}
	decision, err := Tick(context.Background(), path, adapter)
	if err != nil || decision.TaskID != "a" || decision.Status != StatusSucceeded {
		t.Fatal(decision, err)
	}
	if adapter.count("a") != 0 {
		t.Fatal("existing terminal controller work was dispatched")
	}
	again, err := Tick(context.Background(), path, adapter)
	if err != nil || again != (Decision{}) || adapter.count("a") != 0 {
		t.Fatal("terminal adoption was not stable", again, err)
	}
}

func TestConcurrentTicksDispatchOneClaim(t *testing.T) {
	task := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, task)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": readyEvidence(task, 'c')}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		once.Do(func() { close(entered) })
		<-release
		return admittedEvidence(task, StatusRunning, 'd'), nil
	}
	var decisions [2]Decision
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); decisions[0], errs[0] = Tick(context.Background(), path, adapter) }()
	<-entered
	decisions[1], errs[1] = Tick(context.Background(), path, adapter)
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || adapter.count("a") != 1 {
		t.Fatal(decisions, errs, adapter.count("a"))
	}
	if decisions[0].ClaimID == "" || decisions[1] != (Decision{}) {
		t.Fatal(decisions)
	}
}

func TestConcurrentTicksCanDispatchIndependentTasks(t *testing.T) {
	a := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "a"), Operation: OperationPlanner, InvocationID: digest('1')}
	b := TaskSpec{ID: "b", RunID: digest('b'), ControllerPath: filepath.Join(t.TempDir(), "b"), Operation: OperationWriter, InvocationID: digest('2')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, a, b)); err != nil {
		t.Fatal(err)
	}
	enteredA, enteredB, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": readyEvidence(a, 'c'), "b": readyEvidence(b, 'd')}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		if claim.Task.ID == "a" {
			close(enteredA)
		} else {
			close(enteredB)
		}
		<-release
		return admittedEvidence(claim.Task, StatusRunning, 'e'), nil
	}
	var decisions [2]Decision
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); decisions[0], errs[0] = Tick(context.Background(), path, adapter) }()
	<-enteredA
	go func() { defer wg.Done(); decisions[1], errs[1] = Tick(context.Background(), path, adapter) }()
	<-enteredB
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || decisions[0].TaskID != "a" || decisions[1].TaskID != "b" || adapter.count("a") != 1 || adapter.count("b") != 1 {
		t.Fatal(decisions, errs, adapter.count("a"), adapter.count("b"))
	}
}

func TestClaimCrashRecoveryUsesSameClaimAndNeverResendsUnknown(t *testing.T) {
	task := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationExplorer, Input: "inspect exact state", InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	snapshot, err := Bind(path, scheduleDefinition(t, task))
	if err != nil {
		t.Fatal(err)
	}
	claim := Claim{Version: 1, ScheduleID: snapshot.ScheduleID, Task: task, Generation: 1, ControllerHead: digest('c')}
	claimID, _ := claim.ID()
	if err = appendValidated(path, "task.claimed", claimEvent{Claim: claim, ClaimID: claimID}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the adapter began its effect and durably admitted
	// UNKNOWN, but before Tick could append task.observed.
	providerSends := 1
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": admittedEvidence(task, StatusUnknown, 'd')}, dispatches: map[string]int{}}
	first, err := RecoverClaim(context.Background(), path, claimID, adapter)
	if err != nil || first.Status != StatusUnknown {
		t.Fatal(first, err)
	}
	second, err := RecoverClaim(context.Background(), path, claimID, adapter)
	if err != nil || second.Status != StatusUnknown {
		t.Fatal(second, err)
	}
	if _, err = RecoverClaim(context.Background(), path, claimID, adapter); err != nil {
		t.Fatal(err)
	}
	if adapter.count("a") != 0 || providerSends != 1 {
		t.Fatal("recovery resent dispatch", adapter.count("a"), providerSends)
	}
}

func TestConcurrentUnknownRecoveryConvergesOnExactTerminalEvidence(t *testing.T) {
	task := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, task)); err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{"a": readyEvidence(task, 'b')}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		evidence := admittedEvidence(task, StatusUnknown, 'c')
		adapter.set("a", evidence)
		return evidence, nil
	}
	unknown, err := Tick(context.Background(), path, adapter)
	if err != nil || unknown.Status != StatusUnknown {
		t.Fatal(unknown, err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	adapter.reconcile = func(Claim) (Evidence, error) {
		entered <- struct{}{}
		<-release
		return admittedEvidence(task, StatusSucceeded, 'd'), nil
	}
	var decisions [2]Decision
	var errs [2]error
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range decisions {
		go func(index int) {
			defer wg.Done()
			decisions[index], errs[index] = RecoverClaim(context.Background(), path, unknown.ClaimID, adapter)
		}(i)
	}
	<-entered
	<-entered
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || decisions[0].Status != StatusSucceeded || decisions[1].Status != StatusSucceeded {
		t.Fatal(decisions, errs)
	}
	state, err := Inspect(path)
	if err != nil || state.Tasks["a"].Status != StatusSucceeded || adapter.count("a") != 1 {
		t.Fatal(state, err)
	}
}

func TestDynamicAgentTurnsAreDurableFIFOAndUnknownBlocksLaterTurn(t *testing.T) {
	root := TaskSpec{ID: "root", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "root"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, root)); err != nil {
		t.Fatal(err)
	}
	parentID, agentID := digest('b'), digest('c')
	firstTask := TaskSpec{ID: digest('2'), RunID: digest('d'), ControllerPath: filepath.Join(t.TempDir(), "child"), Operation: OperationExplorer, Input: "first turn", InvocationID: digest('3')}
	secondTask := TaskSpec{ID: digest('4'), RunID: digest('d'), ControllerPath: firstTask.ControllerPath, Operation: OperationExplorer, Input: "second turn", InvocationID: digest('5')}
	first := DynamicTask{Task: firstTask, ParentAgentID: parentID, AgentID: agentID, TurnID: firstTask.ID}
	second := DynamicTask{Task: secondTask, ParentAgentID: parentID, AgentID: agentID, TurnID: secondTask.ID}
	if _, err := AddTask(path, first); err != nil {
		t.Fatal(err)
	}
	state, err := AddTask(path, second)
	if err != nil || state.Dynamic[0].TurnSequence != 1 || state.Dynamic[1].TurnSequence != 2 {
		t.Fatal(state.Dynamic, err)
	}
	if _, err = AddTask(path, first); err != nil {
		t.Fatal("exact dynamic retry was not idempotent", err)
	}
	adapter := &fakeAdapter{evidence: map[string]Evidence{
		"root":        admittedEvidence(root, StatusSucceeded, 'a'),
		firstTask.ID:  readyEvidence(firstTask, 'b'),
		secondTask.ID: readyEvidence(secondTask, 'c'),
	}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		if claim.Task.ID == firstTask.ID {
			evidence := admittedEvidence(firstTask, StatusUnknown, 'd')
			adapter.set(firstTask.ID, evidence)
			return evidence, nil
		}
		return admittedEvidence(secondTask, StatusSucceeded, 'e'), nil
	}
	if decision, err := Tick(context.Background(), path, adapter); err != nil || decision.TaskID != "root" || decision.Status != StatusSucceeded {
		t.Fatal(decision, err)
	}
	firstDecision, err := Tick(context.Background(), path, adapter)
	if err != nil || firstDecision.TaskID != firstTask.ID || firstDecision.Status != StatusUnknown {
		t.Fatal(firstDecision, err)
	}
	state, err = Inspect(path)
	wantTurn := AgentTurnBinding{ParentAgentID: parentID, AgentID: agentID, TurnID: firstTask.ID, TurnSequence: 1}
	if err != nil || state.Tasks[firstTask.ID].Claim == nil || state.Tasks[firstTask.ID].Claim.AgentTurn == nil || *state.Tasks[firstTask.ID].Claim.AgentTurn != wantTurn {
		t.Fatal("dynamic claim lost exact agent turn", state.Tasks[firstTask.ID], err)
	}
	if decision, err := Tick(context.Background(), path, adapter); err != nil || decision != (Decision{}) || adapter.count(secondTask.ID) != 0 {
		t.Fatal("later turn escaped FIFO behind UNKNOWN", decision, err)
	}
	adapter.set(firstTask.ID, admittedEvidence(firstTask, StatusSucceeded, 'e'))
	secondDecision, err := Tick(context.Background(), path, adapter)
	if err != nil || secondDecision.TaskID != secondTask.ID || secondDecision.Status != StatusSucceeded || adapter.count(secondTask.ID) != 1 {
		t.Fatal(secondDecision, err)
	}
}

func TestDefinitionBindsFiniteOperationInputAndIdentity(t *testing.T) {
	base := TaskSpec{ID: "a", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	cases := []TaskSpec{base}
	invalid := base
	invalid.Input = "unexpected"
	cases = append(cases, invalid)
	invalid = base
	invalid.Operation = "shell"
	cases = append(cases, invalid)
	invalid = base
	invalid.ControllerPath = "relative"
	cases = append(cases, invalid)
	invalid = base
	invalid.InvocationID = "invocation"
	cases = append(cases, invalid)
	if _, err := scheduleDefinition(t, cases[0]).ID(); err != nil {
		t.Fatal(err)
	}
	for _, task := range cases[1:] {
		if _, err := scheduleDefinition(t, task).ID(); err == nil {
			t.Fatal("invalid task accepted", task)
		}
	}
}

func TestDynamicClaimRejectsSubstitutedAgentTurn(t *testing.T) {
	root := TaskSpec{ID: "root", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "root"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	snapshot, err := Bind(path, scheduleDefinition(t, root))
	if err != nil {
		t.Fatal(err)
	}
	task := TaskSpec{ID: digest('2'), RunID: digest('b'), ControllerPath: filepath.Join(t.TempDir(), "child"), Operation: OperationExplorer, Input: "turn", InvocationID: digest('3')}
	snapshot, err = AddTask(path, DynamicTask{Task: task, ParentAgentID: digest('c'), AgentID: digest('d'), TurnID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	claim := Claim{Version: 1, ScheduleID: snapshot.ScheduleID, Task: task, Generation: 1, ControllerHead: digest('e'), AgentTurn: &AgentTurnBinding{ParentAgentID: digest('c'), AgentID: digest('f'), TurnID: task.ID, TurnSequence: 1}}
	claimID, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err = appendValidated(path, "task.claimed", claimEvent{Claim: claim, ClaimID: claimID}); err == nil {
		t.Fatal("substituted dynamic agent identity was admitted")
	}
}

func scheduleDefinition(t *testing.T, tasks ...TaskSpec) Definition {
	t.Helper()
	return Definition{Version: 1, Nonce: "fixture", Tasks: tasks}
}
func readyEvidence(task TaskSpec, value rune) Evidence {
	return Evidence{RunID: task.RunID, InvocationID: task.InvocationID, ControllerHead: digest(value), Status: StatusReady}
}
func admittedEvidence(task TaskSpec, status Status, value rune) Evidence {
	return Evidence{RunID: task.RunID, InvocationID: task.InvocationID, ControllerHead: digest(value), AdmissionID: digest('a'), Status: status}
}
func digest(value rune) string { return strings.Repeat(string(value), 64) }
