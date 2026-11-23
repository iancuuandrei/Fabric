package control

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

// Overlap regression: two disjoint isolated OpenCode writers must genuinely
// overlap inside the provider-effect hook through the production
// memory-gated dispatch + two-worker Pump path. The barrier proves overlap:
// the first hook stays active until the second has entered; neither seals
// before both entered. No sleeps claim overlap; sleeps/polls only bound
// settlement. Sealing reuses the genuine offline receipt seam with exact
// wrapper-resolved namespaces and child-bound proposals. No provider calls.
func TestIsolatedOpenCodeOverlapRegression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c := isolatedOpenCodeCreation(t)
	c.Execution.MaxParallel = 2
	c.Execution.IsolatedImplementationVersion = 1
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}

	path, s := isolatedWriterControllerFixture(t, c, twoTaskIsolatedGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	bindings, err := prepareIsolatedGraphWriterCohort(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("isolated cohort did not confirm two disjoint bindings: %d", len(bindings))
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	childIDByTask := map[string]string{}
	for _, b := range bindings {
		id, err := b.Candidate.ID()
		if err != nil {
			t.Fatal(err)
		}
		childIDByTask[b.TaskID] = id
	}
	validFor := func(taskID, file string) string {
		return `{"candidate_id":` + strconv.Quote(childIDByTask[taskID]) + `,"changes":[{"path":` + strconv.Quote(file) + `,"before_hash":null,"edits":[],"new_content_utf8":` + strconv.Quote(taskID+" fixture\n") + `,"executable":false}]}`
	}
	validByTask := map[string]string{"one": validFor("one", "one.txt"), "two": validFor("two", "two.txt")}

	oneInvocation, err := writerInvocationForTask(current, "one")
	if err != nil {
		t.Fatal(err)
	}
	twoInvocation, err := writerInvocationForTask(current, "two")
	if err != nil {
		t.Fatal(err)
	}
	oneID, twoID := oneInvocation.ID, twoInvocation.ID
	if oneID == "" || twoID == "" || oneID == twoID {
		t.Fatal("isolated overlap requires two distinct original invocation IDs")
	}

	// Barrier state. The hook holds no controller journal transaction while
	// waiting: entry bookkeeping is in-memory only, sealing writes to the
	// per-invocation runtime/gateway journals (distinct files per task), and
	// each dispatch holds only its own child-workspace read lease, so the
	// first waiter cannot block the second claim/admission.
	bothEntered := make(chan struct{})
	var enterOnce sync.Once
	var mu sync.Mutex
	entries := map[string]int{}
	var unexpected, duplicate bool

	previous := isolatedOpenCodeRuntimeForTest
	isolatedOpenCodeRuntimeForTest = func(hookCtx context.Context, controllerPath string, hookSnap Snapshot, invocation runtime.Invocation, lease opencoderuntime.CandidateLease, journalStem string) (runtime.Result, providerDispatchReceipt, error) {
		if journalStem == "" {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("isolated overlap seal requires the wrapper-resolved journal namespace")
		}
		if invocation.ID != oneID && invocation.ID != twoID {
			mu.Lock()
			unexpected = true
			mu.Unlock()
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("isolated overlap hook saw an unexpected invocation identity")
		}
		// Validate the exact wrapper-resolved namespace without test-fatal
		// hazards: a mismatch fails closed via error return so Pump can
		// still settle instead of stranding on Goexit.
		if admitted, err := Inspect(controllerPath); err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		} else if expected, err := providerInvocationJournalStemForSnapshot(controllerPath, admitted, invocation, nil); err != nil || expected != journalStem {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("isolated overlap namespace mismatch"), err)
		}
		mu.Lock()
		entries[invocation.ID]++
		if entries[invocation.ID] > 1 {
			duplicate = true
		}
		if len(entries) == 2 {
			enterOnce.Do(func() { close(bothEntered) })
		}
		mu.Unlock()
		// First entrant stays active here until the second has entered.
		// Bounded by finite contexts only; no sleep claims overlap.
		select {
		case <-bothEntered:
		case <-hookCtx.Done():
			return runtime.Result{}, providerDispatchReceipt{}, hookCtx.Err()
		case <-ctx.Done():
			return runtime.Result{}, providerDispatchReceipt{}, ctx.Err()
		}
		mu.Lock()
		dup := duplicate
		mu.Unlock()
		if dup {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("isolated overlap saw a duplicate hook entry")
		}
		taskID := "one"
		if invocation.ID == twoID {
			taskID = "two"
		}
		routing, err := resolveProviderRoutingForSnapshot(hookSnap, invocation, 1)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		runtimePath := controllerPath + "." + journalStem + ".opencode-runtime.jsonl"
		gatewayPath := controllerPath + "." + journalStem + ".provider-gateway.jsonl"
		// Genuine offline seam (no provider calls). Same helper the
		// correction regression uses from Pump workers.
		result, receipt, _ := sealOpenCodeOfflineTurnForTest(t, runtimePath, gatewayPath, invocation, routing, hookSnap.Creation.Repository, "build", validByTask[taskID], "harness.writer-result.v1", "writer", 48, 16, 12, 4)
		return result, receipt, nil
	}
	t.Cleanup(func() { isolatedOpenCodeRuntimeForTest = previous })

	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			tasks = append(tasks, task)
		}
	}
	specs, err := buildGraphWriterBatchSpecs(path, current, tasks)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("isolated overlap cohort did not bind two specs: %d", len(specs))
	}
	schedulePath := path + ".isolated-opencode-overlap-regression.jsonl"
	definition := taskscheduler.Definition{Version: 1, Nonce: "isolated-opencode-overlap-regression", Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		t.Fatal(err)
	}

	// Ample host memory with no host RAM dependency; the gate must keep
	// both workers admissible.
	gate := newGraphMemoryAdmissionGate(path, func() memoryadmission.Observation {
		return memoryadmission.Observation{Status: memoryadmission.ObservationObserved, Source: memoryadmission.SourceWindowsGlobal, AvailableMiB: 1 << 14, TotalMiB: 1 << 14}
	})
	adapter := memoryGatedScheduledDispatchAdapter{ScheduledDispatchAdapter: ScheduledDispatchAdapter{JournalPath: schedulePath}, gate: gate}
	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()
	pumpResult := make(chan error, 1)
	go func() {
		pumpResult <- taskscheduler.Pump(pumpCtx, schedulePath, adapter, taskscheduler.PumpOptions{Workers: 2, PollInterval: 10 * time.Millisecond})
	}()

	// Barrier failure must shut down owned work cleanly and fail with a
	// concrete seam instead of hanging Pump. No t.Fatal inside the hook
	// after this point; the hook returns ctx errors so Pump can join.
	select {
	case <-bothEntered:
	case <-ctx.Done():
		pumpCancel()
		_ = waitGraphPump(pumpResult, 5*time.Second)
		t.Fatal("isolated overlap barrier never reached: second dispatch did not enter the hook; production dispatch serialized the cohort (memory gate, head park, or lease contention)")
	}

	settleDeadline := time.Now().Add(60 * time.Second)
	for {
		sched, err := taskscheduler.Inspect(schedulePath)
		if err != nil {
			pumpCancel()
			_ = waitGraphPump(pumpResult, 5*time.Second)
			t.Fatal(err)
		}
		one, okOne := sched.Tasks["one"]
		two, okTwo := sched.Tasks["two"]
		if okOne && okTwo && one.Status == taskscheduler.StatusSucceeded && two.Status == taskscheduler.StatusSucceeded {
			break
		}
		for taskID, state := range sched.Tasks {
			if state.Status == taskscheduler.StatusUnknown {
				pumpCancel()
				_ = waitGraphPump(pumpResult, 5*time.Second)
				t.Fatalf("isolated overlap left UNKNOWN on task %q", taskID)
			}
			if state.Status == taskscheduler.StatusFailed || state.Status == taskscheduler.StatusCancelled {
				pumpCancel()
				_ = waitGraphPump(pumpResult, 5*time.Second)
				t.Fatalf("isolated overlap task %q settled %q before both succeeded", taskID, string(state.Status))
			}
		}
		if time.Now().After(settleDeadline) {
			pumpCancel()
			_ = waitGraphPump(pumpResult, 5*time.Second)
			t.Fatal("isolated overlap did not settle both tasks after the barrier")
		}
		select {
		case <-ctx.Done():
			pumpCancel()
			_ = waitGraphPump(pumpResult, 5*time.Second)
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	pumpCancel()
	if err := waitGraphPump(pumpResult, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	entriesCopy := map[string]int{}
	for k, v := range entries {
		entriesCopy[k] = v
	}
	unexpectedCopy, duplicateCopy := unexpected, duplicate
	mu.Unlock()
	if unexpectedCopy {
		t.Fatal("isolated overlap hook resolved an unexpected invocation identity")
	}
	if duplicateCopy || len(entriesCopy) != 2 || entriesCopy[oneID] != 1 || entriesCopy[twoID] != 1 {
		t.Fatalf("isolated overlap requires exactly one hook entry per original invocation, got %+v duplicate=%t", entriesCopy, duplicateCopy)
	}

	schedule, err := taskscheduler.Inspect(schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedule.Tasks) != 2 {
		t.Fatalf("isolated overlap schedule must hold exactly two tasks, got %d", len(schedule.Tasks))
	}
	for _, taskID := range []string{"one", "two"} {
		state, ok := schedule.Tasks[taskID]
		if !ok || state.Status != taskscheduler.StatusSucceeded {
			t.Fatalf("isolated overlap task %q did not succeed: %+v", taskID, state)
		}
	}

	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.RoleCorrections) != 0 {
		t.Fatalf("isolated overlap must not admit corrections, got %d", len(after.RoleCorrections))
	}
	if len(after.GraphWriterResults) != 2 {
		t.Fatalf("isolated overlap must integrate two proposals, got %d", len(after.GraphWriterResults))
	}
	for _, taskID := range []string{"one", "two"} {
		record, ok := after.GraphWriterResults[taskID]
		if !ok || record.Isolated == nil {
			t.Fatalf("task %q has no integrated isolated proposal", taskID)
		}
		if err := validateIsolatedGraphWriterProposal(after, *record.Isolated); err != nil {
			t.Fatalf("task %q proposal invalid: %v", taskID, err)
		}
		childID, err := record.Isolated.Candidate.ID()
		if err != nil || childID != childIDByTask[taskID] {
			t.Fatalf("task %q proposal has wrong child identity: got %q want %q (%v)", taskID, childID, childIDByTask[taskID], err)
		}
		wantInvocation := oneID
		if taskID == "two" {
			wantInvocation = twoID
		}
		if graphWriterRecordInvocation(record).ID != wantInvocation {
			t.Fatalf("task %q proposal cites wrong invocation: got %q want %q", taskID, graphWriterRecordInvocation(record).ID, wantInvocation)
		}
	}
	for _, dispatch := range after.AgentDispatch {
		if dispatch.Observation == nil {
			continue
		}
		if dispatch.Observation.Status == agenttree.StatusUnknown {
			t.Fatal("isolated overlap left an UNKNOWN agent dispatch observation")
		}
	}
	if after.GraphMemoryAdmission == nil {
		t.Fatal("isolated overlap recorded no memory admission")
	}
	if after.GraphMemoryAdmission.Decision.EffectiveWorkers != 2 {
		t.Fatalf("isolated overlap memory gate did not retain two workers, got %d", after.GraphMemoryAdmission.Decision.EffectiveWorkers)
	}
	if len(after.GraphMemoryAdmission.ActiveTaskIDs) != 0 {
		t.Fatalf("isolated overlap left %d active memory reservations", len(after.GraphMemoryAdmission.ActiveTaskIDs))
	}
}
