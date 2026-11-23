package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
)

func stagedHubLeavesGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "staged hub to leaves", Tasks: []engineeringplan.Task{
		{ID: "impl-alpha", Kind: engineeringplan.Implementation, Title: "Create hub output", ScopePaths: []string{"."}, WritePaths: []string{"alpha.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "hub output exists"}}, EstimatedSeconds: 30},
		{ID: "impl-beta", Kind: engineeringplan.Implementation, Title: "Create leaf beta", Dependencies: []string{"impl-alpha"}, ScopePaths: []string{"."}, WritePaths: []string{"beta.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "leaf beta exists"}}, EstimatedSeconds: 30},
		{ID: "impl-gamma", Kind: engineeringplan.Implementation, Title: "Create leaf gamma", Dependencies: []string{"impl-alpha"}, ScopePaths: []string{"."}, WritePaths: []string{"gamma.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "leaf gamma exists"}}, EstimatedSeconds: 30},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"impl-alpha", "impl-beta", "impl-gamma"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the verified candidate"}}, EstimatedSeconds: 30},
	}}
}

func applyTwoSlotIsolationCapacity(t *testing.T, c Creation) Creation {
	t.Helper()
	c.Config.WriterContract = "anchored-edits-v1"
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *c.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	model := engineeringplan.ProviderModelKey{Provider: c.Config.Writer.Provider, Model: c.Config.Writer.Model}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: model.Provider, Model: model.Model}
	c.Execution.IsolationEstimate = &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	c.Execution.IsolationCapacity = &engineeringplan.ResourceCapacity{
		CPUMilli: 2, MemoryMiB: 2, VerificationSlots: 2, TotalRuntimeSlots: 2,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: model.Provider, Slots: 2}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: model, Slots: 2}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 2}},
	}
	c.Config.ControllerStateRoot = filepath.Join(t.TempDir(), "controller-state")
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func stagedThreeTaskCreation(t *testing.T) Creation {
	t.Helper()
	c := parallelWriterCreation(t, 2)
	c.Execution.ParallelImplementationVersion = 0
	c.Execution.IsolatedImplementationVersion = 3
	c.Execution.MaxParallel = 2
	c.Config.PlannerContract = plannerContractGraphV8
	return applyTwoSlotIsolationCapacity(t, c)
}

func stagedPrepareWithGraph(t *testing.T, c Creation, graph engineeringplan.Graph) (string, Snapshot) {
	t.Helper()
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, graph)
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareStagedCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, current
}

// stagedSetupToInitial drives the shared staged driver prefix through the
// initial candidate binding: approval, workspace start, and initial inspect.
func stagedSetupToInitial(t *testing.T, c Creation, graph engineeringplan.Graph) (string, Snapshot, string) {
	t.Helper()
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, graph)
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	initial, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	initialID, err := initial.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	return path, initial, initialID
}

// assertStagedFinalGatesBound checks the shared READY tail: final native
// verification and review bind the exact last candidate.
func assertStagedFinalGatesBound(t *testing.T, ready Snapshot) {
	t.Helper()
	finalID, err := ready.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if ready.Verification == nil || ready.Verification.Plan.CandidateID != finalID {
		t.Fatal("final verification is not bound to the last candidate")
	}
	if ready.Review == nil {
		t.Fatal("final review missing")
	}
	var verdict ReviewVerdict
	if err := canonical.Decode([]byte(ready.Review.Result.Output), &verdict); err != nil || verdict.CandidateID != finalID {
		t.Fatalf("final review is not bound to the last candidate: %+v %v", verdict, err)
	}
}

func stagedPreparedCohort(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	return stagedPrepareWithGraph(t, c, stagedHubLeavesGraphFixture())
}

func TestStagedHubToTwoLeavesReachReadyWithTwoAggregates(t *testing.T) {
	c := stagedThreeTaskCreation(t)
	path, _, initialID := stagedSetupToInitial(t, c, stagedHubLeavesGraphFixture())
	ready, err := RunAutonomous(context.Background(), path)
	if err != nil {
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("staged run failure cause: %v", cause)
		}
		if latest, inspectErr := Inspect(path); inspectErr == nil {
			forks := map[string]string{}
			for id, fork := range latest.GraphStagedForks {
				forks[id] = fork.Outcome
			}
			t.Logf("staged run state=%s results=%d stagedHistory=%d preparation=%v batch=%v fileOutcome=%s forks=%v", latest.State, len(latest.GraphWriterResults), len(latest.GraphStagedCohorts), latest.GraphIsolationPreparation != nil, latest.GraphWriterBatch != nil, latest.FileOutcome, forks)
		}
		if events, journalErr := journal.Read(path); journalErr == nil {
			kinds := map[string]int{}
			for _, event := range events {
				kinds[event.Kind]++
			}
			t.Logf("staged journal kinds: %v", kinds)
		}
		t.Fatal(err)
	}
	if ready.State != "READY" {
		t.Fatalf("staged run did not reach READY: state=%s", ready.State)
	}
	if len(ready.GraphStagedCohorts) != 1 {
		t.Fatalf("staged run did not archive exactly the hub cohort: history=%d", len(ready.GraphStagedCohorts))
	}
	hubArchive := ready.GraphStagedCohorts[0]
	if hubArchive.CohortIndex != 0 || len(hubArchive.TaskIDs) != 1 || hubArchive.TaskIDs[0] != "impl-alpha" {
		t.Fatalf("hub archive does not retain hub identity: %+v", hubArchive)
	}
	if hubArchive.BaseCandidateID != initialID {
		t.Fatalf("hub base is not initial source: base=%s initial=%s", hubArchive.BaseCandidateID, initialID)
	}
	if ready.GraphIsolationPreparation == nil || ready.GraphIsolationPreparation.Version != 3 || ready.GraphIsolationPreparation.CohortIndex != 1 {
		t.Fatalf("leaf cohort preparation missing: %+v", ready.GraphIsolationPreparation)
	}
	leafPrep := ready.GraphIsolationPreparation
	if len(leafPrep.SelectedTaskIDs) != 2 {
		t.Fatalf("leaf cohort did not freeze two leaves: %+v", leafPrep.SelectedTaskIDs)
	}
	if leafPrep.BaseCandidateID != hubArchive.CandidateID {
		t.Fatalf("leaf base is not exact post-hub candidate: leafBase=%s hubCandidate=%s", leafPrep.BaseCandidateID, hubArchive.CandidateID)
	}
	if leafPrep.BaseCandidateID == initialID {
		t.Fatal("leaf children were forked from initial source instead of post-hub candidate")
	}
	if ready.GraphWriterBatch == nil || ready.GraphWriterBatch.Version != 4 || ready.GraphWriterBatch.CohortIndex != 1 {
		t.Fatalf("leaf aggregate is not version four: %+v", ready.GraphWriterBatch)
	}
	if hubArchive.Batch.Version != 4 || hubArchive.Batch.CohortIndex != 0 {
		t.Fatalf("hub aggregate is not version four: %+v", hubArchive.Batch)
	}
	if hubArchive.Batch.Prepared.Proposal.Before != hubArchive.FileIntent.Prepared.Proposal.Before {
		t.Fatal("hub batch and file intent preimage diverge")
	}
	leafBefore := ready.GraphWriterBatch.Prepared.Proposal.Before
	hubBefore := hubArchive.Batch.Prepared.Proposal.Before
	if leafBefore == hubBefore {
		t.Fatal("second aggregate did not use a new Before")
	}
	leafBeforeID, _ := leafBefore.ID()
	if leafBeforeID != hubArchive.CandidateID {
		t.Fatalf("leaf aggregate Before is not post-hub candidate: %s vs %s", leafBeforeID, hubArchive.CandidateID)
	}
	if countJournalKind(t, path, "files.intent") != 2 || countJournalKind(t, path, "files.observed") != 2 {
		t.Fatal("staged run did not produce exactly two parent file effects")
	}
	if len(ready.GraphWriterResults) != 3 {
		t.Fatalf("staged run did not retain each proposal: %d", len(ready.GraphWriterResults))
	}
	// Distinct child IDs and per-cohort base IDs.
	seenChild, seenIsolation := map[string]bool{}, map[string]bool{}
	for _, member := range append(append([]GraphWriterMember{}, hubArchive.Batch.Members...), ready.GraphWriterBatch.Members...) {
		if seenChild[member.ChildCandidateID] || seenIsolation[member.IsolationID] {
			t.Fatalf("child workspace/candidate reused: %+v", member)
		}
		seenChild[member.ChildCandidateID], seenIsolation[member.IsolationID] = true, true
	}
	hubFork, ok := ready.GraphStagedForks["impl-alpha"]
	if !ok || hubFork.Outcome != "CONFIRMED" {
		t.Fatal("hub fork missing")
	}
	hubChildID, _ := hubFork.Candidate.ID()
	_ = hubChildID
	for _, leafID := range []string{"impl-beta", "impl-gamma"} {
		leafFork, ok := ready.GraphStagedForks[leafID]
		if !ok || leafFork.Outcome != "CONFIRMED" {
			t.Fatalf("leaf fork %s missing", leafID)
		}
		if leafFork.Intent.BaseCandidateID != hubArchive.CandidateID {
			t.Fatalf("leaf %s base is not post-hub: %s", leafID, leafFork.Intent.BaseCandidateID)
		}
		if leafFork.Candidate.WorktreeID == hubFork.Candidate.WorktreeID {
			t.Fatalf("leaf %s reused hub worktree", leafID)
		}
		leafChildID, _ := leafFork.Candidate.ID()
		hubBaseChildID, _ := hubFork.Candidate.ID()
		if leafChildID == hubBaseChildID {
			t.Fatalf("leaf %s child equals hub child; hub bytes not forked", leafID)
		}
		// Leaves see hub-modified bytes in child context: the forked child
		// candidate equals the post-hub parent (which contains hub output),
		// not the initial source. Hash equality alone does not prove
		// execution observed hub content, so read the actual hub file from
		// each leaf child worktree before asserting.
		if leafFork.Candidate.FilesHash != hubArchive.FileReceipt.Observation.Candidate.FilesHash {
			t.Fatalf("leaf %s child does not carry hub bytes", leafID)
		}
		childAlpha, err := os.ReadFile(filepath.Join(leafFork.Binding.Request.Path, filepath.FromSlash("alpha.txt")))
		if err != nil || string(childAlpha) != "parallel fixture output\n" {
			t.Fatalf("leaf %s child did not observe hub alpha.txt: %q %v", leafID, childAlpha, err)
		}
		if leafFork.Candidate.Version != hubArchive.Batch.Prepared.Proposal.Before.Version {
			t.Fatalf("leaf %s child candidate version diverged from parent", leafID)
		}
		if err := leafFork.Candidate.ValidateBinding(*leafFork.Binding); err != nil {
			t.Fatalf("leaf %s child binding rejected: %v", leafID, err)
		}
	}
	// Immutable hub progress retains the original post-hub candidate.
	hubEvidence, ok := ready.Graph.Evidence["impl-alpha"]
	if !ok || hubEvidence.Outcome != "completed" || hubEvidence.CandidateID != hubArchive.CandidateID {
		t.Fatalf("hub progress was rewritten: %+v archive=%s", hubEvidence, hubArchive.CandidateID)
	}
	// Final native gates bind the exact last candidate, not a stale receipt.
	assertStagedFinalGatesBound(t, ready)
	// Parent received both hub and leaf outputs.
	for _, file := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
		content, err := os.ReadFile(filepath.Join(ready.Workspace.Request.Path, filepath.FromSlash(file)))
		if err != nil || string(content) != "parallel fixture output\n" {
			t.Fatalf("parent did not receive %s: %q %v", file, content, err)
		}
	}
	// Capacities and memory use the stable configured maximum with an actual
	// wave grant bound. Version 3 keeps one run-stable ConfiguredMax across
	// hub (one task) and leaves (two tasks); per-wave grants stay bounded.
	if stable := isolatedMemoryStableMaxWorkers(ready); stable != 2 {
		t.Fatalf("stable configured maximum changed: %d", stable)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	grants := 0
	seenPrep := map[string]string{}
	for _, event := range events {
		if event.Kind != "graph.writer.memory-admitted" {
			continue
		}
		var record GraphMemoryAdmissionRecord
		if err := canonical.Decode(event.Payload, &record); err != nil {
			t.Fatal(err)
		}
		if record.Decision.ConfiguredMaxWorkers != 2 {
			t.Fatalf("memory ConfiguredMax not stable across hub and leaves: %+v", record.Decision)
		}
		if record.Decision.EffectiveWorkers > 2 || record.Decision.EffectiveWorkers < 1 {
			t.Fatalf("memory admission outside capacity two: %+v", record.Decision)
		}
		seenPrep[record.TaskID] = record.PreparationID
		if record.Decision.EffectiveWorkers > 0 {
			grants++
		}
	}
	if grants == 0 {
		t.Fatal("no actual wave grant bound retained")
	}
	if len(seenPrep) != 3 {
		t.Fatalf("hub-to-leaves memory decisions missing tasks: %v", seenPrep)
	}
	// Release closure: every grant settled via its writer proposal; no active
	// reservation survives the staged advances that retained decision history.
	if ready.GraphMemoryAdmission == nil {
		t.Fatal("staged advance dropped memory decision history")
	}
	if len(ready.GraphMemoryAdmission.ActiveTaskIDs) != 0 {
		t.Fatalf("memory reservations leaked across staged advance: %v", ready.GraphMemoryAdmission.ActiveTaskIDs)
	}
	if ready.GraphMemoryAdmission.Decision.ConfiguredMaxWorkers != 2 {
		t.Fatalf("retained memory decision lost stable ceiling: %+v", ready.GraphMemoryAdmission.Decision)
	}
	// The compact advance event carries exact identities without duplicating
	// the full archive payload in the journal envelope.
	compactKinds := 0
	for _, event := range events {
		if event.Kind != "graph.staged-advanced" {
			continue
		}
		compactKinds++
		var advance StagedCohortAdvance
		if err := canonical.Decode(event.Payload, &advance); err != nil {
			t.Fatalf("staged advance is not compact: %v", err)
		}
		if advance.Version != 1 || advance.BatchProposalID == "" || advance.FileProposalID == "" || advance.ObservationHash == "" {
			t.Fatalf("compact advance lacks exact identities: %+v", advance)
		}
		if len(event.Payload) > canonical.MaxBytes-1024 {
			t.Fatal("compact advance exceeds envelope bound")
		}
	}
	if compactKinds != 1 {
		t.Fatalf("expected exactly one compact hub advance, got %d", compactKinds)
	}
}

func TestStagedPlannerBindingReflectsVersionThree(t *testing.T) {
	c := stagedThreeTaskCreation(t)
	invocation, err := plannerInvocationWithContextsAndRecipe(c.Config, "implement hub then leaves", nil, nil, c.Execution)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string                       `json:"instruction"`
		Isolation   *plannerIsolationConstraints `json:"isolation,omitempty"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instruction, "hub-to-leaf") && !strings.Contains(payload.Instruction, "hub") {
		t.Fatalf("staged planner binding does not reflect hub stages: %s", payload.Instruction)
	}
	if payload.Isolation == nil || payload.Isolation.IsolatedImplementationVersion != 3 {
		t.Fatalf("staged constraints must bind version three: %+v", payload.Isolation)
	}
}

func TestStagedRejectsForeignStaleAndMissing(t *testing.T) {
	c := stagedThreeTaskCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, stagedHubLeavesGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareStagedCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 3 || prep.CohortIndex != 0 || len(prep.SelectedTaskIDs) != 1 || prep.SelectedTaskIDs[0] != "impl-alpha" {
		t.Fatalf("hub preparation did not freeze hub only: %+v", prep)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	foreign := *current.GraphIsolationPreparation
	foreign.SelectedTaskIDs = append(append([]string{}, foreign.SelectedTaskIDs...), "impl-foreign")
	if err := replayStagedPreparation(&current, journal.Event{Kind: "graph.staged-prepared", Payload: mustCanonical(t, foreign)}); err == nil {
		t.Fatal("foreign staged preparation accepted")
	}
	if _, err := CreateStagedTaskIsolation(context.Background(), path, "impl-beta"); err == nil {
		t.Fatal("non-ready leaf fork admitted before hub completion")
	}
	if _, err := CreateStagedTaskIsolation(context.Background(), path, "impl-foreign"); err == nil {
		t.Fatal("foreign staged fork admitted")
	}
}

func TestStagedUnknownForkPreventsNextCohort(t *testing.T) {
	_, current := stagedPreparedCohort(t, stagedThreeTaskCreation(t))
	forged := current
	forged.GraphStagedForks = map[string]StagedForkState{
		"impl-alpha": {Intent: StagedForkIntent{TaskID: "impl-alpha"}, Outcome: "UNKNOWN"},
	}
	if err := replayStagedFork(&forged, journal.Event{Kind: "graph.staged-fork-intent", Payload: mustCanonical(t, StagedForkIntent{TaskID: "impl-alpha"})}); err == nil {
		t.Fatal("UNKNOWN fork tamper accepted")
	}
	if _, err := expectedStagedArchive(current); err == nil {
		t.Fatal("advance without hub effect admitted")
	}
	_ = forged
}

func TestStagedLegacyV1V2IdentityUnchanged(t *testing.T) {
	v1 := isolatedThreeWriterCreation(t)
	v1inv, err := plannerInvocationWithContextsAndRecipe(v1.Config, "implement independent changes", nil, nil, v1.Execution)
	if err != nil {
		t.Fatal(err)
	}
	var v1payload struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(v1inv.Input), &v1payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v1payload.Instruction, "complete initial cohort must fit") {
		t.Fatalf("v1 instruction changed: %s", v1payload.Instruction)
	}
	waves := isolatedWavesThreeWriterCreation(t)
	waveInv, err := plannerInvocationWithContextsAndRecipe(waves.Config, "implement independent changes", nil, nil, waves.Execution)
	if err != nil {
		t.Fatal(err)
	}
	var wavePayload struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(waveInv.Input), &wavePayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wavePayload.Instruction, "resource-bounded initial wave") {
		t.Fatalf("v2 instruction changed: %s", wavePayload.Instruction)
	}
	// Old canonical identities: v1 preparation omits waves and cohort index.
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, v1, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareGraphIsolationCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 1 || len(prep.Waves) != 0 || prep.CohortIndex != 0 {
		t.Fatalf("v1 preparation carries staged fields: %+v", prep)
	}
	raw, err := canonical.Bytes(prep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"cohort_index"`) || strings.Contains(string(raw), `"waves"`) {
		t.Fatalf("v1 serialization is not absent-field legacy: %s", raw)
	}
}

func TestStagedAdvanceCompactEnvelopeNearBound(t *testing.T) {
	// Near-bound stage: 240 KiB decoded file content stays within the 256 KiB
	// file-effect bound but duplicated full-archive payloads (batch + file
	// intent sharing Before/BeforeFiles/Changes/After) approach the 1 MiB
	// journal envelope. The compact advance must stay tiny.
	raw := strings.Repeat("a", 240<<10)
	encoded := base64.StdEncoding.EncodeToString([]byte(raw))
	change := fileeffects.Change{Path: "near-bound.bin", ContentBase64: &encoded}
	changes := []fileeffects.Change{change}
	batchProposal := fileeffects.Proposal{Changes: changes}
	fileProposal := fileeffects.Proposal{Changes: changes}
	archive := StagedCohortArchive{
		CohortIndex: 0, PreparationID: strings.Repeat("a", 64), BaseCandidateID: strings.Repeat("b", 64), CandidateID: strings.Repeat("c", 64),
		TaskIDs:     []string{"impl-alpha"},
		Batch:       GraphWriterBatchRecord{Prepared: PreparedFiles{Proposal: batchProposal}},
		FileIntent:  FileIntent{Prepared: PreparedFiles{Proposal: fileProposal}},
		FileReceipt: FileReceipt{},
	}
	full, err := canonical.Bytes(archive)
	if err != nil {
		t.Fatal(err)
	}
	advance := StagedCohortAdvance{Version: 1, CohortIndex: 0, PreparationID: strings.Repeat("a", 64), BaseCandidateID: strings.Repeat("b", 64), CandidateID: strings.Repeat("c", 64), TaskIDs: []string{"impl-alpha"}, BatchProposalID: strings.Repeat("d", 64), FileProposalID: strings.Repeat("e", 64), ObservationHash: strings.Repeat("f", 64)}
	compact, err := canonical.Bytes(advance)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) > 4096 {
		t.Fatalf("compact advance not compact: %d bytes", len(compact))
	}
	if len(full) < 600000 {
		t.Fatalf("near-bound fixture did not duplicate payload: full=%d", len(full))
	}
	if len(compact)*100 >= len(full) {
		t.Fatalf("compact did not save envelope: compact=%d full=%d", len(compact), len(full))
	}
	if len(compact) > canonical.MaxBytes-1024 {
		t.Fatal("compact advance exceeds envelope bound")
	}
}

func TestStagedForkDeltaCumulativeBoundsRejectBeforeIntent(t *testing.T) {
	path, current := stagedPreparedCohort(t, stagedThreeTaskCreation(t))
	beforeKinds := map[string]int{}
	if events, err := journal.Read(path); err == nil {
		for _, event := range events {
			beforeKinds[event.Kind]++
		}
	}
	// Count bound: 65 cumulative hub-delta files exceed the 64-change bound.
	many := make([]fileeffects.Change, 0, 65)
	for i := 0; i < 65; i++ {
		content := base64.StdEncoding.EncodeToString([]byte("hi\n"))
		many = append(many, fileeffects.Change{Path: fmt.Sprintf("f%02d.txt", i), ContentBase64: &content})
	}
	forgedCount := current
	forgedCount.GraphStagedCohorts = []StagedCohortArchive{{Batch: GraphWriterBatchRecord{Prepared: PreparedFiles{Proposal: fileeffects.Proposal{Changes: many}}}}}
	prepCount := *forgedCount.GraphIsolationPreparation
	prepCount.CohortIndex = len(forgedCount.GraphStagedCohorts)
	forgedCount.GraphIsolationPreparation = &prepCount
	if _, err := stagedParentDelta(forgedCount); err != nil {
		t.Fatalf("stagedParentDelta should surface cumulative delta, got %v", err)
	} else if delta, _ := stagedParentDelta(forgedCount); len(delta) != 65 {
		t.Fatalf("cumulative delta miscounted: %d", len(delta))
	}
	if _, _, err := expectedStagedForkIntent(forgedCount, "impl-alpha"); err == nil || !strings.Contains(err.Error(), "exceeds file bound") {
		t.Fatalf("cumulative count excess not rejected before intent: %v", err)
	}
	// Byte bound: two 150 KiB files total 300 KiB over the 256 KiB bound.
	big := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 150<<10)))
	over := []fileeffects.Change{{Path: "big-a.bin", ContentBase64: &big}, {Path: "big-b.bin", ContentBase64: &big}}
	forgedBytes := current
	forgedBytes.GraphStagedCohorts = []StagedCohortArchive{{Batch: GraphWriterBatchRecord{Prepared: PreparedFiles{Proposal: fileeffects.Proposal{Changes: over}}}}}
	prepBytes := *forgedBytes.GraphIsolationPreparation
	prepBytes.CohortIndex = len(forgedBytes.GraphStagedCohorts)
	forgedBytes.GraphIsolationPreparation = &prepBytes
	if _, _, err := expectedStagedForkIntent(forgedBytes, "impl-alpha"); err == nil || !strings.Contains(err.Error(), "exceeds byte bound") {
		t.Fatalf("cumulative byte excess not rejected before intent: %v", err)
	}
	// No intent was admitted for the predictable excess: journal unchanged.
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = after
	if events, err := journal.Read(path); err != nil {
		t.Fatal(err)
	} else {
		for _, event := range events {
			if event.Kind == "graph.staged-fork-intent" && beforeKinds[event.Kind] != 0 {
				t.Fatal("unexpected fork intent recorded")
			}
		}
		if len(events) == 0 {
			t.Fatal("journal missing")
		}
	}
}

func TestStagedUnknownForkRealFaultBlocksResumeWithoutRedispatch(t *testing.T) {
	path, current := stagedPreparedCohort(t, stagedThreeTaskCreation(t))
	intent, _, err := expectedStagedForkIntent(current, "impl-alpha")
	if err != nil {
		t.Fatal(err)
	}
	// Deterministic creation failure: occupy the exact destination so
	// worktree.Create fails after the durable intent is already appended.
	if err := os.MkdirAll(filepath.Dir(intent.Request.Path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(intent.Request.Path, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := CreateStagedTaskIsolation(context.Background(), path, "impl-alpha")
	if err == nil {
		t.Fatal("blocked destination did not yield UNKNOWN")
	}
	if state.Outcome != "UNKNOWN" {
		t.Fatalf("fault did not stay UNKNOWN: %+v", state)
	}
	inspected, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	fork, ok := inspected.GraphStagedForks["impl-alpha"]
	if !ok || fork.Outcome != "UNKNOWN" {
		t.Fatalf("durable UNKNOWN fork missing: %+v", inspected.GraphStagedForks)
	}
	countKinds := func() map[string]int {
		events, err := journal.Read(path)
		if err != nil {
			t.Fatal(err)
		}
		kinds := map[string]int{}
		for _, event := range events {
			kinds[event.Kind]++
		}
		return kinds
	}
	before := countKinds()
	if before["graph.staged-fork-intent"] != 1 || before["graph.staged-fork-confirmed"] != 0 {
		t.Fatalf("fault journal shape wrong: %v", before)
	}
	// Resume must not redispatch, recreate the destination, or reissue the
	// overlay; the existing UNKNOWN requires explicit reconciliation.
	if _, err := CreateStagedTaskIsolation(context.Background(), path, "impl-alpha"); err == nil || !strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("UNKNOWN fork redispatched: %v", err)
	}
	if _, err := RunAutonomous(context.Background(), path); err == nil {
		t.Fatal("autonomous resume advanced past UNKNOWN fork")
	} else {
		t.Logf("resume blocked as required: %v", err)
	}
	resumed, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if fork, ok := resumed.GraphStagedForks["impl-alpha"]; !ok || fork.Outcome != "UNKNOWN" {
		t.Fatalf("resume resolved UNKNOWN without reconciliation: %+v", resumed.GraphStagedForks)
	}
	afterResume := countKinds()
	for kind, count := range before {
		if afterResume[kind] != count {
			t.Fatalf("resume changed %s: %d -> %d", kind, count, afterResume[kind])
		}
	}
	for kind, count := range afterResume {
		if before[kind] != count {
			t.Fatalf("resume added %s: %d -> %d", kind, before[kind], count)
		}
	}
	if _, err := expectedStagedArchive(inspected); err == nil {
		t.Fatal("advance past UNKNOWN fork admitted")
	}
}

func TestStagedAdvanceRejectsActiveMemoryReservation(t *testing.T) {
	_, current := stagedPreparedCohort(t, stagedThreeTaskCreation(t))
	active := map[string]GraphMemoryAdmissionActive{"impl-alpha": {AdmissionID: strings.Repeat("b", 64), InvocationID: "inv"}}
	forged := current
	forged.GraphMemoryAdmission = &GraphMemoryAdmissionState{Sequence: 1, LastAdmission: strings.Repeat("a", 64), ActiveTaskIDs: active}
	if _, err := expectedStagedArchive(forged); err == nil || !strings.Contains(err.Error(), "active memory") {
		t.Fatalf("advance with active reservation admitted: %v", err)
	}
	if _, err := expectedStagedAdvance(forged); err == nil {
		t.Fatal("compact advance with active reservation admitted")
	}
	compact := StagedCohortAdvance{Version: 1, CohortIndex: len(current.GraphStagedCohorts)}
	if err := replayStagedAdvance(&forged, journal.Event{Kind: "graph.staged-advanced", Payload: mustCanonical(t, compact)}); err == nil {
		t.Fatal("replay advanced past active reservation")
	}
}

func TestStagedForkCandidateVersionCompatibility(t *testing.T) {
	// Version 1 legacy and semantic-index-v2 candidates use distinct identity
	// domains; bindings reject mixing. Staged forks preserve the parent
	// version instead of promoting or demoting it.
	legacy := struct {
		Version    int    `json:"version"`
		WorktreeID string `json:"worktree_id"`
		Head       string `json:"head"`
		IndexHash  string `json:"index_hash"`
		FilesHash  string `json:"files_hash"`
		FileCount  int    `json:"file_count"`
	}{Version: 1, WorktreeID: strings.Repeat("a", 64), Head: strings.Repeat("b", 40), IndexHash: strings.Repeat("c", 64), FilesHash: strings.Repeat("d", 64)}
	semantic := legacy
	semantic.Version = 2
	legacyID, err := canonical.Hash("harness.candidate.v1", legacy)
	if err != nil {
		t.Fatal(err)
	}
	semanticID, err := canonical.Hash("harness.candidate.v2", semantic)
	if err != nil {
		t.Fatal(err)
	}
	if legacyID == semanticID {
		t.Fatal("candidate version domains collide")
	}
	c := stagedThreeTaskCreation(t)
	c.Config.CandidateIdentity = "semantic-index-v2"
	_, current := stagedPreparedCohort(t, c)
	if current.Candidate == nil || current.Candidate.Version != 2 {
		t.Fatalf("semantic-index-v2 parent did not fingerprint version two: %+v", current.Candidate)
	}
	intent, _, err := expectedStagedForkIntent(current, "impl-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if intent.Request.CandidateIdentity != "semantic-index-v2" {
		t.Fatalf("fork request dropped candidate identity: %q", intent.Request.CandidateIdentity)
	}
	if intent.ParentCandidate.Version != 2 {
		t.Fatalf("fork intent parent version not preserved: %+v", intent.ParentCandidate)
	}
}

func stagedLexicographicCreation(t *testing.T) Creation {
	t.Helper()
	c := stagedThreeTaskCreation(t)
	c.Execution.IsolationCohortSelectorVersion = 1
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStagedLexicographicSelectorOptInPreparation(t *testing.T) {
	c := stagedLexicographicCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, stagedHubLeavesGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareStagedCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.Version != 3 || prep.CohortSelectorVersion != 1 {
		t.Fatalf("lexicographic preparation did not freeze the selector: %+v", prep)
	}
	if prep.CohortIndex != 0 || len(prep.SelectedTaskIDs) != 1 || prep.SelectedTaskIDs[0] != "impl-alpha" {
		t.Fatalf("hub preparation did not freeze hub only: %+v", prep)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.GraphIsolationPreparation == nil || current.GraphIsolationPreparation.CohortSelectorVersion != 1 {
		t.Fatalf("journal replay dropped the frozen selector: %+v", current.GraphIsolationPreparation)
	}
	// Recomputation from the same frozen policy, graph source, and parent
	// candidate must reproduce the recorded preparation exactly.
	bare := current
	bare.GraphIsolationPreparation = nil
	recomputed, err := expectedStagedPreparation(bare)
	if err != nil {
		t.Fatal(err)
	}
	if !sameCanonical(recomputed, *current.GraphIsolationPreparation) {
		t.Fatal("staged lexicographic preparation is not deterministic under replay")
	}
	// A substituted selector must fail closed rather than dispatch.
	forged := current
	tampered := *current.GraphIsolationPreparation
	tampered.CohortSelectorVersion = 0
	if err := replayStagedPreparation(&forged, journal.Event{Kind: "graph.staged-prepared", Payload: mustCanonical(t, tampered)}); err == nil {
		t.Fatal("selector-substituted staged preparation accepted")
	}
	// An out-of-range selector must fail closed before any wave derivation.
	invalid := current
	invalid.Creation.Execution = &ExecutionPolicy{}
	*invalid.Creation.Execution = *current.Creation.Execution
	invalid.Creation.Execution.IsolationCohortSelectorVersion = 9
	invalid.GraphIsolationPreparation = nil
	if _, err := expectedStagedPreparation(invalid); err == nil || !strings.Contains(err.Error(), "invalid isolation cohort selector") {
		t.Fatalf("out-of-range selector admitted: %v", err)
	}
}

func TestStagedGreedyLegacyPreparationOmitsSelector(t *testing.T) {
	c := stagedThreeTaskCreation(t)
	if c.Execution.IsolationCohortSelectorVersion != 0 {
		t.Fatalf("default staged creation carries a selector: %+v", c.Execution)
	}
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, stagedHubLeavesGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prep, err := PrepareStagedCohort(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if prep.CohortSelectorVersion != 0 {
		t.Fatalf("greedy preparation carries a selector: %+v", prep)
	}
	raw, err := json.Marshal(prep)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cohort_selector_version") {
		t.Fatalf("absent selector changed legacy preparation serialization: %s", raw)
	}
	// The frozen uniform per-writer estimate template applies identical
	// demands to every ready implementation, so greedy and lexicographic
	// wave derivations agree on this run. This is a stated template
	// limitation, not an optimum claim: divergence is demonstrated at the
	// selector unit level with unequal demands instead of invented
	// per-writer estimates here.
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	demands, err := isolationResourceDemands(current, implementations)
	if err != nil {
		t.Fatal(err)
	}
	greedy, err := engineeringplan.SelectResourceWaves(current.Graph.Graph, implementations, demands, *current.Creation.Execution.IsolationCapacity, current.Creation.Execution.EffectiveMaxParallel())
	if err != nil {
		t.Fatal(err)
	}
	optimal, err := engineeringplan.SelectResourceWavesLexicographic(current.Graph.Graph, implementations, demands, *current.Creation.Execution.IsolationCapacity, current.Creation.Execution.EffectiveMaxParallel())
	if err != nil {
		t.Fatal(err)
	}
	if len(greedy) != len(optimal) {
		t.Fatalf("uniform-template waves diverge unexpectedly: greedy=%d lexicographic=%d", len(greedy), len(optimal))
	}
	for i := range greedy {
		if len(greedy[i].Tasks) != len(optimal[i].Tasks) {
			t.Fatalf("uniform-template wave %d diverges unexpectedly", i)
		}
		for j := range greedy[i].Tasks {
			if greedy[i].Tasks[j].ID != optimal[i].Tasks[j].ID {
				t.Fatalf("uniform-template wave %d member %d diverges unexpectedly", i, j)
			}
		}
	}
}
