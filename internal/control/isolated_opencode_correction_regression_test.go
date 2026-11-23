package control

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

// Coverage limit: the frozen v43 defect stages hub impl-alpha then two
// disjoint leaves impl-beta/impl-gamma with OpenCode legacy text output and
// semantic corrections v1. The smallest useful offline fixture here is an
// isolated same-wave cohort of two disjoint writers ("one","two") through the
// same runScheduledGraphWriterCohort with Workers=2, isolated=true. It
// exercises the exact at-risk seams — scheduled dispatch, completed-receipt
// semantic rejection, AfterSettledScheduledClaim admission, memory-gated
// correction dispatch and two-worker Pump self-cancellation — without the
// staged hub-completion and leaf-fork orchestration. It does not prove staged
// hub-to-leaf base propagation. Parent aggregation is covered only through
// the existing isolated v2 batch seam (buildIsolatedGraphWriterBatch) over
// the two integrated child proposals.
func TestIsolatedOpenCodeScheduledCorrectionRegression(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	c := isolatedOpenCodeCreation(t)
	c.Execution.SemanticCorrectionVersion = 1
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
	// Valid scoped proposals use the new-file anchored shape: one.txt/two.txt
	// do not exist yet in the pristine child worktrees.
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
	validOne := validFor("one", "one.txt")
	validTwo := validFor("two", "two.txt")
	// Legacy text writer output: completed receipt with non-JSON output, so the
	// scheduler settles the claim as Failed via semantic rejection while the
	// runtime receipt stays completed.
	const malformedLegacyText = "one legacy text output\n"

	oneInvocation, err := writerInvocationForTask(current, "one")
	if err != nil {
		t.Fatal(err)
	}
	twoInvocation, err := writerInvocationForTask(current, "two")
	if err != nil {
		t.Fatal(err)
	}
	faultyOriginalID := oneInvocation.ID
	siblingOriginalID := twoInvocation.ID

	// Branching seal hook reusing the genuine offline completed-receipt seam
	// at the wrapper-resolved namespace exactly. First dispatch of the faulty
	// task seals malformed legacy text; every other invocation (sibling
	// original and any correction turn) seals a valid scoped proposal for its
	// own task. The hook writes to the exact journalStem/runtimePath/
	// gatewayPath resolved by executeIsolatedOpenCodeWriter (turn-bound for
	// dynamic correction turns) with routing derived from the admitted
	// snapshot and exact invocation input, so correction dispatch verifies
	// through the same usage path as production execution. No provider calls.
	var mu sync.Mutex
	sealedKindByInvocation := map[string]string{}
	previous := isolatedOpenCodeRuntimeForTest
	isolatedOpenCodeRuntimeForTest = func(hookCtx context.Context, controllerPath string, hookSnap Snapshot, invocation runtime.Invocation, lease opencoderuntime.CandidateLease, journalStem string) (runtime.Result, providerDispatchReceipt, error) {
		if journalStem == "" {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("isolated test seal requires the wrapper-resolved journal namespace")
		}
		runtimePath := controllerPath + "." + journalStem + ".opencode-runtime.jsonl"
		gatewayPath := controllerPath + "." + journalStem + ".provider-gateway.jsonl"
		routing, err := resolveProviderRoutingForSnapshot(hookSnap, invocation, 1)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		output := ""
		kind := ""
		mu.Lock()
		switch invocation.ID {
		case faultyOriginalID:
			if sealedKindByInvocation[invocation.ID] == "" {
				output, kind = malformedLegacyText, "malformed-initial"
			} else {
				output, kind = validOne, "corrected"
			}
		case siblingOriginalID:
			output, kind = validTwo, "sibling-valid"
		default:
			// Correction turns carry a new invocation identity derived from
			// the admitted RoleSemanticCorrection. Only the faulty task is
			// expected to correct here; bind the valid scoped proposal for it.
			// Any unexpected third identity fails closed via proposal scope
			// validation rather than speculative task mapping.
			output, kind = validOne, "corrected"
		}
		sealedKindByInvocation[invocation.ID] = kind
		mu.Unlock()
		result, receipt, _ := sealOpenCodeOfflineTurnForTest(t, runtimePath, gatewayPath, invocation, routing, hookSnap.Creation.Repository, "build", output, "harness.writer-result.v1", "writer", 48, 16, 12, 4)
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
		t.Fatalf("isolated correction cohort did not bind two specs: %d", len(specs))
	}
	schedulePath := path + ".isolated-opencode-correction-regression.jsonl"
	definition := taskscheduler.Definition{Version: 1, Nonce: "isolated-opencode-correction-regression", Tasks: specs}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		t.Fatal(err)
	}

	runErr := runScheduledGraphWriterCohort(ctx, path, schedulePath, specs, 2, true)
	if runErr != nil {
		short := func(id string) string {
			if len(id) > 8 {
				return id[:8]
			}
			return id
		}
		if diag, diagErr := Inspect(path); diagErr != nil {
			t.Logf("diagnostic: controller inspect failed: %v", diagErr)
		} else {
			t.Logf("diagnostic: controller state=%q results=%d corrections=%d dispatches=%d", diag.State, len(diag.GraphWriterResults), len(diag.RoleCorrections), len(diag.AgentDispatch))
			for taskID, record := range diag.GraphWriterResults {
				isolated := record.Isolated != nil
				t.Logf("diagnostic: result task=%q invocation=%q isolated=%t", taskID, short(graphWriterRecordInvocation(record).ID), isolated)
			}
			for _, correction := range diag.RoleCorrections {
				t.Logf("diagnostic: correction task=%q scheduled=%q invocation=%q base=%q", correction.TaskID, short(correction.ScheduledTaskID), short(correction.Invocation.ID), short(correction.BaseInvocationID))
			}
			if diag.GraphMemoryAdmission == nil {
				t.Logf("diagnostic: memory=nil")
			} else {
				for taskID, active := range diag.GraphMemoryAdmission.ActiveTaskIDs {
					t.Logf("diagnostic: memory-active task=%q invocation=%q sequence=%d workers=%d", taskID, short(active.InvocationID), diag.GraphMemoryAdmission.Sequence, diag.GraphMemoryAdmission.Decision.EffectiveWorkers)
				}
				t.Logf("diagnostic: memory sequence=%d workers=%d active=%d", diag.GraphMemoryAdmission.Sequence, diag.GraphMemoryAdmission.Decision.EffectiveWorkers, len(diag.GraphMemoryAdmission.ActiveTaskIDs))
			}
		}
		if sched, schedErr := taskscheduler.Inspect(schedulePath); schedErr != nil {
			t.Logf("diagnostic: scheduler inspect failed: %v", schedErr)
		} else {
			for taskID, state := range sched.Tasks {
				t.Logf("diagnostic: schedule task=%q status=%q claim=%t evidence=%t", taskID, string(state.Status), state.Claim != nil, state.Evidence != nil)
			}
		}
		mu.Lock()
		kindCounts := map[string]int{}
		for _, kind := range sealedKindByInvocation {
			kindCounts[kind]++
		}
		sealed := len(sealedKindByInvocation)
		mu.Unlock()
		for kind, count := range kindCounts {
			t.Logf("diagnostic: sealed kind=%q count=%d total=%d", kind, count, sealed)
		}
		if errors.Is(runErr, context.Canceled) || strings.Contains(runErr.Error(), "context canceled") {
			t.Fatalf("scheduled isolated OpenCode correction self-canceled before correction dispatch: %v", runErr)
		}
		for cause := errors.Unwrap(runErr); cause != nil; cause = errors.Unwrap(cause) {
			if errors.Is(cause, context.Canceled) || strings.Contains(cause.Error(), "context canceled") {
				t.Fatalf("scheduled isolated OpenCode correction wrapped cancellation: %v", runErr)
			}
		}
		t.Fatalf("scheduled isolated OpenCode cohort failed: %v", runErr)
	}

	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.RoleCorrections) == 0 {
		t.Fatal("scheduled isolated OpenCode correction was never admitted for the malformed proposal")
	}
	var correction RoleSemanticCorrection
	foundCorrection := false
	for _, candidate := range after.RoleCorrections {
		if candidate.TaskID == "one" && candidate.ScheduledTaskID != "" {
			correction = candidate
			foundCorrection = true
		}
	}
	if !foundCorrection {
		t.Fatalf("malformed task correction missing: corrections=%d", len(after.RoleCorrections))
	}
	if correction.BaseInvocationID != faultyOriginalID {
		t.Fatalf("correction is not bound to the faulty base invocation: base=%q want %q", correction.BaseInvocationID, faultyOriginalID)
	}
	for _, taskID := range []string{"one", "two"} {
		record, ok := after.GraphWriterResults[taskID]
		if !ok || record.Isolated == nil {
			t.Fatalf("task %q has no integrated isolated proposal", taskID)
		}
		if err := validateIsolatedGraphWriterProposal(after, *record.Isolated); err != nil {
			t.Fatalf("task %q proposal invalid: %v", taskID, err)
		}
	}
	// Sibling preservation: the untouched sibling keeps its original
	// invocation and valid proposal; the corrected task moved to a new scoped
	// correction identity.
	if got := graphWriterRecordInvocation(after.GraphWriterResults["two"]).ID; got != siblingOriginalID {
		t.Fatalf("sibling proposal was not preserved: got %q want %q", got, siblingOriginalID)
	}
	if got := graphWriterRecordInvocation(after.GraphWriterResults["one"]).ID; got == faultyOriginalID {
		t.Fatalf("corrected task still cites the failed base invocation: %q", got)
	}
	// Historical failure is preserved: the initial semantic-failed scheduler
	// task remains Failed after the dynamic correction succeeds. No erasure.
	schedule, err := taskscheduler.Inspect(schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	initialOne, ok := schedule.Tasks["one"]
	if !ok || initialOne.Status != taskscheduler.StatusFailed {
		t.Fatalf("initial malformed task must remain Failed, got %+v", initialOne)
	}
	sibling, ok := schedule.Tasks["two"]
	if !ok || sibling.Status != taskscheduler.StatusSucceeded {
		t.Fatalf("sibling task must succeed, got %+v", sibling)
	}
	corrected, ok := schedule.Tasks[correction.ScheduledTaskID]
	if !ok || corrected.Status != taskscheduler.StatusSucceeded {
		t.Fatalf("admitted correction task must succeed, got %+v", corrected)
	}
	for taskID, state := range schedule.Tasks {
		switch state.Status {
		case taskscheduler.StatusSucceeded:
		case taskscheduler.StatusFailed:
			if taskID != "one" {
				t.Fatalf("scheduled task %q settled failed without correction integration", taskID)
			}
		case taskscheduler.StatusUnknown:
			t.Fatalf("scheduled task %q is UNKNOWN after correction", taskID)
		case taskscheduler.StatusCancelled:
			t.Fatalf("scheduled task %q was canceled before agent.dispatch admission", taskID)
		default:
			t.Fatalf("scheduled task %q did not settle: %s", taskID, string(state.Status))
		}
		if state.Status != taskscheduler.StatusSucceeded && state.Status != taskscheduler.StatusFailed {
			t.Fatalf("scheduled task %q is nonterminal after correction: %s", taskID, string(state.Status))
		}
	}
	for _, dispatch := range after.AgentDispatch {
		if dispatch.Observation == nil {
			continue
		}
		if dispatch.Observation.Status == agenttree.StatusUnknown {
			t.Fatal("isolated correction left an UNKNOWN agent dispatch observation")
		}
	}
	// Actual parent aggregate integration through the existing isolated v2
	// seam over the two integrated child proposals. This prepares (not
	// applies) the single parent effect; it performs no provider calls.
	batch, err := buildIsolatedGraphWriterBatch(ctx, path, []string{"one", "two"})
	if err != nil {
		t.Fatalf("isolated parent aggregate did not build from corrected proposals: %v", err)
	}
	if batch.Version != 2 || len(batch.Members) != 2 {
		t.Fatalf("isolated aggregate has wrong membership: %+v", batch)
	}
	paths := map[string]bool{}
	for _, change := range batch.Prepared.Proposal.Changes {
		paths[change.Path] = true
	}
	if !paths["one.txt"] || !paths["two.txt"] {
		t.Fatalf("isolated aggregate misses child changes: %v", paths)
	}
	if _, err := expectedIsolatedGraphWriterBatch(after, []string{"one", "two"}, batch.Prepared); err != nil {
		t.Fatalf("isolated aggregate does not reconstruct from validated state: %v", err)
	}
}

// TestIsolatedOpenCodeCorrectionSubstitutionRejected proves the resolver-kept
// stale/substitution gate still rejects a substituted, unrecorded, or
// wrong-task correction identity without starting any provider effect.
func TestIsolatedOpenCodeCorrectionSubstitutionRejected(t *testing.T) {
	ctx := context.Background()
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedWriterControllerFixture(t, c, twoTaskIsolatedGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareIsolatedGraphWriterCohort(ctx, path); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	bindingOne, err := isolatedWriterBindingForSnapshot(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	baseOne, err := writerInvocationForIsolatedTask(s, bindingOne)
	if err != nil {
		t.Fatal(err)
	}
	bindingTwo, err := isolatedWriterBindingForSnapshot(s, "two")
	if err != nil {
		t.Fatal(err)
	}
	baseTwo, err := writerInvocationForIsolatedTask(s, bindingTwo)
	if err != nil {
		t.Fatal(err)
	}
	beforeDispatch := len(s.AgentDispatch)
	beforeRuntime := len(s.ProviderRuntime)
	beforeResults := len(s.GraphWriterResults)
	observedProvider := countJournalKind(t, path, "role.provider-observed")
	observedProposals := countJournalKind(t, path, "graph.writer.proposed")

	// Substituted: same ID, mutated input.
	substituted := baseOne
	substituted.Input += " "
	if _, err := resolveScheduledRecordedInvocation(s, baseOne, substituted); err == nil {
		t.Fatal("substituted correction identity was admitted")
	}
	// Unrecorded: well-formed but never admitted.
	unrecorded := baseOne
	unrecorded.ID = strings.Repeat("a", 64)
	if _, err := resolveScheduledRecordedInvocation(s, baseOne, unrecorded); err == nil {
		t.Fatal("unrecorded correction identity was admitted")
	}
	// Wrong task: sibling invocation presented as this task's correction.
	if _, err := resolveScheduledRecordedInvocation(s, baseOne, baseTwo); err == nil {
		t.Fatal("wrong-task correction identity was admitted")
	}
	// Proposal preparation must reject each without recording effects.
	childID, err := bindingOne.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	validOutput := `{"candidate_id":` + strconv.Quote(childID) + `,"changes":[{"path":"one.txt","before_hash":null,"edits":[],"new_content_utf8":"one fixture\n","executable":false}]}`
	for name, candidate := range map[string]runtime.Invocation{"substituted": substituted, "unrecorded": unrecorded, "wrong-task": baseTwo} {
		if _, err := prepareIsolatedGraphWriterProposal(ctx, path, "one", candidate, runtime.Result{Version: 1, InvocationID: candidate.ID, Requested: candidate.Profile, Output: validOutput}); err == nil {
			t.Fatalf("%s correction proposal was admitted", name)
		}
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.AgentDispatch) != beforeDispatch || len(after.ProviderRuntime) != beforeRuntime || len(after.GraphWriterResults) != beforeResults {
		t.Fatal("rejected correction started a provider effect")
	}
	if countJournalKind(t, path, "role.provider-observed") != observedProvider || countJournalKind(t, path, "graph.writer.proposed") != observedProposals {
		t.Fatal("rejected correction appended proposal evidence")
	}
}

// TestScheduledWriterCorrectionEvidenceAdverse is a pure scheduledEvidence
// regression: a dynamic correction turn completes only on the exact recorded
// proposal for its original task. A mismatched original invocation or an
// unrelated sibling-only result must not complete the dynamic turn.
func TestScheduledWriterCorrectionEvidenceAdverse(t *testing.T) {
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "p", Model: "m", Effort: "e", Role: "writer"}
	correctionBase, err := runtime.NewInvocation(profile, "correction base input")
	if err != nil {
		t.Fatal(err)
	}
	dynamicID := strings.Repeat("a", 64)
	scoped, err := scheduledTurnInvocation(correctionBase, taskscheduler.OperationWriter, dynamicID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := runtime.NewInvocation(profile, "unrelated sibling input")
	if err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: dynamicID, Operation: taskscheduler.OperationWriter}
	baseSnapshot := func() Snapshot {
		return Snapshot{
			RunID:    strings.Repeat("b", 64),
			Creation: Creation{Execution: &ExecutionPolicy{ParallelImplementationVersion: 1}},
			RoleCorrections: []RoleSemanticCorrection{
				{TaskID: "one", ScheduledTaskID: dynamicID, Invocation: correctionBase},
			},
		}
	}
	// Mismatched: original task holds a proposal under a different invocation.
	mismatched := baseSnapshot()
	mismatched.GraphWriterResults = map[string]GraphWriterRecord{
		"one": {TaskID: "one", Writer: WriterRecord{Invocation: other}},
	}
	if evidence := scheduledEvidence(mismatched, strings.Repeat("c", 64), scoped, task); evidence.Status == taskscheduler.StatusSucceeded {
		t.Fatal("mismatched original proposal completed the dynamic correction turn")
	}
	// Unrelated: only the sibling task holds a proposal.
	unrelated := baseSnapshot()
	unrelated.GraphWriterResults = map[string]GraphWriterRecord{
		"two": {TaskID: "two", Writer: WriterRecord{Invocation: other}},
	}
	if evidence := scheduledEvidence(unrelated, strings.Repeat("c", 64), scoped, task); evidence.Status == taskscheduler.StatusSucceeded {
		t.Fatal("unrelated sibling proposal completed the dynamic correction turn")
	}
}
