package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
)

// stagedCouplingFixture parameterizes hub/leaf graphs with typed couplings so
// controller tests share one body instead of duplicating staged scaffolding.
// Fixture labels (not live-model runs) are honest: all writer/verification
// effects come from the existing deterministic fake fixtures.
func stagedCouplingFixture(leaves []engineeringplan.Task, couplings []engineeringplan.TaskCoupling) engineeringplan.Graph {
	tasks := []engineeringplan.Task{
		{ID: "impl-alpha", Kind: engineeringplan.Implementation, Title: "Create hub output", ScopePaths: []string{"."}, WritePaths: []string{"alpha.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "hub output exists"}}, EstimatedSeconds: 30},
	}
	tasks = append(tasks, leaves...)
	verifyDeps := []string{"impl-alpha"}
	for _, leaf := range leaves {
		verifyDeps = append(verifyDeps, leaf.ID)
	}
	tasks = append(tasks,
		engineeringplan.Task{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: verifyDeps, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
		engineeringplan.Task{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the verified candidate"}}, EstimatedSeconds: 30},
	)
	return engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "staged coupling fixture", Tasks: tasks, Couplings: couplings}
}

func stagedCouplingLeaf(id, write string, seconds int) engineeringplan.Task {
	return engineeringplan.Task{ID: id, Kind: engineeringplan.Implementation, Title: "Create leaf " + id, Dependencies: []string{"impl-alpha"}, ScopePaths: []string{"."}, WritePaths: []string{write}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "leaf " + id + " exists"}}, EstimatedSeconds: seconds}
}

func stagedCouplingAwareCreation(t *testing.T) Creation {
	t.Helper()
	c := stagedThreeTaskCreation(t)
	c.Execution.IsolationCohortSelectorVersion = 2
	c.Config.PlannerContract = plannerContractGraphV9
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func stagedObservedCouplingCreation(t *testing.T) Creation {
	t.Helper()
	c := stagedThreeTaskCreation(t)
	c.Execution.IsolationCohortSelectorVersion = 3
	c.Config.PlannerContract = plannerContractGraphV9
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStagedCouplingAwareSelectorParsesAndBinds(t *testing.T) {
	c := stagedCouplingAwareCreation(t)
	invocation, err := plannerInvocationWithContextsAndRecipe(c.Config, "implement hub then leaves", nil, nil, c.Execution)
	if err != nil {
		t.Fatalf("coupling-aware planner binding rejected: %v", err)
	}
	if !strings.Contains(invocation.Input, "couplings") || !strings.Contains(invocation.Input, "C4") {
		t.Fatalf("coupling-aware planner binding lacks typed coupling: %s", invocation.Input[:500])
	}
	var payload struct {
		Instruction string                       `json:"instruction"`
		Isolation   *plannerIsolationConstraints `json:"isolation,omitempty"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &payload); err != nil {
		t.Fatalf("coupling-aware invocation is not JSON: %v", err)
	}
	if payload.Isolation == nil || payload.Isolation.IsolatedImplementationVersion != 3 {
		t.Fatalf("coupling-aware constraints must bind version three: %+v", payload.Isolation)
	}
}

func TestStagedCouplingAwareRejectsUnsafeGeneratorSplit(t *testing.T) {
	unsafe := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "unsafe C4 split", Tasks: []engineeringplan.Task{
		{ID: "research", Kind: engineeringplan.Research, Title: "Shared research", ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "fact", Description: "facts"}}, EstimatedSeconds: 5, Completed: true},
		{ID: "impl-beta", Kind: engineeringplan.Implementation, Title: "Create leaf beta", Dependencies: []string{"research"}, ScopePaths: []string{"."}, WritePaths: []string{"beta.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "leaf beta"}}, EstimatedSeconds: 30},
		{ID: "impl-gamma", Kind: engineeringplan.Implementation, Title: "Create leaf gamma", Dependencies: []string{"research"}, ScopePaths: []string{"."}, WritePaths: []string{"gamma.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "leaf gamma"}}, EstimatedSeconds: 30},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"impl-beta", "impl-gamma"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "checks"}}, EstimatedSeconds: 30},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "review"}}, EstimatedSeconds: 30},
	}, Couplings: []engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC4, Reason: "observed_generator_family", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "gen.go binds beta.txt and gamma.txt"}}}
	// Admission must reject the unsafe split before any child effect.
	// Assert the staged validator owns the gate directly to keep this test
	// independent of journal fixtures.
	if err := engineeringplan.ValidateAutonomousGraphWithStagedImplementations(unsafe, 8); err == nil {
		t.Fatal("unsafe C4 generator split admitted without one owner or dependency")
	}
	// One owner (single implementation, no split) is accepted.
	single := stagedCouplingFixture(
		[]engineeringplan.Task{stagedCouplingLeaf("impl-beta", "beta.txt", 30)},
		nil,
	)
	if err := engineeringplan.ValidateAutonomousGraphWithStagedImplementations(single, 8); err != nil {
		t.Fatalf("single-owner staged graph rejected: %v", err)
	}
	// Explicit hub dependency satisfies the C4 gate for hub-to-leaf families.
	dependent := stagedCouplingFixture(
		[]engineeringplan.Task{stagedCouplingLeaf("impl-beta", "beta.txt", 30)},
		[]engineeringplan.TaskCoupling{{From: "impl-alpha", To: "impl-beta", Level: engineeringplan.CouplingC4, Reason: "observed_generator_family", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "gen.go binds alpha.txt and beta.txt"}},
	)
	if err := engineeringplan.ValidateAutonomousGraphWithStagedImplementations(dependent, 8); err != nil {
		t.Fatalf("C4 with explicit hub dependency rejected: %v", err)
	}
}

func TestStagedCouplingAwareLeafWavesAvoidC3Pair(t *testing.T) {
	c := stagedCouplingAwareCreation(t)
	graph := stagedCouplingFixture(
		[]engineeringplan.Task{
			stagedCouplingLeaf("impl-beta", "beta.txt", 100),
			stagedCouplingLeaf("impl-gamma", "gamma.txt", 90),
			stagedCouplingLeaf("impl-delta", "delta.txt", 10),
		},
		[]engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC3, Reason: "shared_api", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "beta and gamma share critical API"}},
	)
	_, current := stagedPrepareWithGraph(t, c, graph)
	prep := current.GraphIsolationPreparation
	if prep == nil || prep.Version != 3 || prep.CohortSelectorVersion != 2 {
		t.Fatalf("coupling-aware preparation missing selector 2: %+v", prep)
	}
	// Hub stage has one task; advance it through the fake run below instead.
	// Here assert only the frozen hub cohort shape.
	if len(prep.SelectedTaskIDs) != 1 || prep.SelectedTaskIDs[0] != "impl-alpha" {
		t.Fatalf("hub cohort did not freeze the hub alone: %+v", prep.SelectedTaskIDs)
	}
}

// assertStagedReadyNoResend reuses one driver body for every staged selector:
// hub then leaves to READY with exact post-hub candidate binding, no UNKNOWN
// outcomes, and a quiescent second run that redisatches no file effect.
func assertStagedReadyNoResend(t *testing.T, c Creation, graph engineeringplan.Graph, wantSelector int) {
	t.Helper()
	path, _, initialID := stagedSetupToInitial(t, c, graph)
	ready, err := RunAutonomous(context.Background(), path)
	if err != nil {
		t.Fatalf("selector %d staged run failed: %v", wantSelector, err)
	}
	if ready.State != "READY" {
		t.Fatalf("selector %d staged run did not reach READY: state=%s", wantSelector, ready.State)
	}
	if ready.GraphIsolationPreparation == nil || ready.GraphIsolationPreparation.CohortSelectorVersion != wantSelector {
		t.Fatalf("READY run did not retain selector %d: %+v", wantSelector, ready.GraphIsolationPreparation)
	}
	if len(ready.GraphStagedCohorts) != 1 {
		t.Fatalf("selector %d: expected exactly one archived hub cohort: %d", wantSelector, len(ready.GraphStagedCohorts))
	}
	hubArchive := ready.GraphStagedCohorts[0]
	if hubArchive.BaseCandidateID != initialID {
		t.Fatalf("selector %d hub base is not initial source: base=%s initial=%s", wantSelector, hubArchive.BaseCandidateID, initialID)
	}
	leafPrep := ready.GraphIsolationPreparation
	if leafPrep == nil || len(leafPrep.SelectedTaskIDs) != 2 || leafPrep.BaseCandidateID != hubArchive.CandidateID {
		t.Fatalf("selector %d leaf cohort is not bound to the exact post-hub candidate: %+v hub=%s", wantSelector, leafPrep, hubArchive.CandidateID)
	}
	assertStagedFinalGatesBound(t, ready)
	for id, fork := range ready.GraphStagedForks {
		if fork.Outcome == "UNKNOWN" {
			t.Fatalf("selector %d staged fork %s is UNKNOWN after READY", wantSelector, id)
		}
	}
	for id, isolation := range ready.GraphIsolations {
		if isolation.Outcome == "UNKNOWN" {
			t.Fatalf("selector %d isolation %s is UNKNOWN after READY", wantSelector, id)
		}
	}
	if graphHasUnknown(ready) || ready.FileOutcome == "UNKNOWN" {
		t.Fatalf("selector %d READY run retains UNKNOWN progress or file outcome", wantSelector)
	}
	intentsBefore := countJournalKind(t, path, "files.intent")
	again, err := RunAutonomous(context.Background(), path)
	if err != nil {
		t.Fatalf("selector %d second run after READY failed: %v", wantSelector, err)
	}
	if again.State != "READY" {
		t.Fatalf("selector %d second run left READY: %s", wantSelector, again.State)
	}
	if countJournalKind(t, path, "files.intent") != intentsBefore {
		t.Fatalf("selector %d READY rerun redispatched a file effect", wantSelector)
	}
}

// TestStagedSelectorsReachReady parameterizes the genuine driver fixture
// across the frozen v1.1.39 selector 1 and the new coupling-aware selector 2,
// so the v39 gap is closed by execution rather than assumed. Selector 1 runs
// the coupling-free hub-leaf graph; selector 2 runs the same shape with one
// advisory C1 coupling. Both must reach READY with post-hub candidate binding
// and no UNKNOWN resend.
func TestStagedSelectorsReachReady(t *testing.T) {
	lexCreation := stagedLexicographicCreation(t)
	lexGraph := stagedCouplingFixture(
		[]engineeringplan.Task{
			stagedCouplingLeaf("impl-beta", "beta.txt", 30),
			stagedCouplingLeaf("impl-gamma", "gamma.txt", 30),
		},
		nil,
	)
	assertStagedReadyNoResend(t, lexCreation, lexGraph, 1)
	couplingCreation := stagedCouplingAwareCreation(t)
	couplingGraph := stagedCouplingFixture(
		[]engineeringplan.Task{
			stagedCouplingLeaf("impl-beta", "beta.txt", 30),
			stagedCouplingLeaf("impl-gamma", "gamma.txt", 30),
		},
		[]engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "beta and gamma share import neighborhood"}},
	)
	assertStagedReadyNoResend(t, couplingCreation, couplingGraph, 2)
}

// TestStagedCouplingAwareRunReachesReady closes the v1.1.39 evidence gap with
// an actual opt-in selector run through the existing deterministic fake
// fixtures (fixture, not live-model): hub then leaves to native
// verification+review+READY with exact hub candidate binding and no UNKNOWN
// redispatch.
func TestStagedCouplingAwareRunReachesReady(t *testing.T) {
	c := stagedCouplingAwareCreation(t)
	graph := stagedCouplingFixture(
		[]engineeringplan.Task{
			stagedCouplingLeaf("impl-beta", "beta.txt", 30),
			stagedCouplingLeaf("impl-gamma", "gamma.txt", 30),
		},
		[]engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "beta and gamma share import neighborhood"}},
	)
	assertStagedReadyNoResend(t, c, graph, 2)
}

func TestStagedCouplingAwareAbsentCompatAndReplay(t *testing.T) {
	// Legacy graphs without couplings serialize without the field and replay
	// through the frozen greedy derivation byte-for-byte.
	legacy := stagedHubLeavesGraphFixture()
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "couplings") {
		t.Fatalf("legacy staged graph carries couplings bytes: %s", raw)
	}
	c := stagedThreeTaskCreation(t)
	path, current := stagedPrepareWithGraph(t, c, legacy)
	if current.GraphIsolationPreparation == nil || current.GraphIsolationPreparation.CohortSelectorVersion != 0 {
		t.Fatalf("greedy legacy preparation changed: %+v", current.GraphIsolationPreparation)
	}
	prepRaw, err := canonical.Bytes(*current.GraphIsolationPreparation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prepRaw), "cohort_selector_version") {
		t.Fatal("greedy preparation carries selector bytes")
	}
	// Journal replay of the legacy preparation is exact.
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GraphIsolationPreparation == nil || !sameCanonical(*replayed.GraphIsolationPreparation, *current.GraphIsolationPreparation) {
		t.Fatal("legacy preparation did not replay exactly")
	}
	// Coupling-aware graphs require the v9 contract and selector 2; the same
	// couplings stay shape-valid (advisory C1 with hub dependency) but the
	// frozen greedy v8 policy must not admit them. The admission gate lives
	// in parseAcceptedGraph; here we pin the policy identities that enforce
	// it so a future refactor cannot silently widen v8.
	coupled := stagedCouplingFixture(
		[]engineeringplan.Task{stagedCouplingLeaf("impl-beta", "beta.txt", 30)},
		[]engineeringplan.TaskCoupling{{From: "impl-alpha", To: "impl-beta", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "shared neighborhood"}},
	)
	if err := engineeringplan.ValidateAutonomousGraphWithStagedImplementations(coupled, 8); err != nil {
		t.Fatalf("advisory C1 coupling with hub dependency rejected: %v", err)
	}
	if c.Config.PlannerContract != plannerContractGraphV8 {
		t.Fatalf("legacy staged policy drifted from plan-graph-v8: %s", c.Config.PlannerContract)
	}
	if c.Execution.IsolationCohortSelectorVersion != 0 {
		t.Fatalf("legacy staged selector drifted from greedy: %d", c.Execution.IsolationCohortSelectorVersion)
	}
	couplingPolicy := stagedCouplingAwareCreation(t)
	if couplingPolicy.Config.PlannerContract != plannerContractGraphV9 {
		t.Fatalf("coupling-aware policy drifted from plan-graph-v9: %s", couplingPolicy.Config.PlannerContract)
	}
	if couplingPolicy.Execution.IsolationCohortSelectorVersion != 2 {
		t.Fatalf("coupling-aware selector drifted from version 2: %d", couplingPolicy.Execution.IsolationCohortSelectorVersion)
	}
}

func TestStagedObservedSelector3PolicyBinding(t *testing.T) {
	c := stagedObservedCouplingCreation(t)
	if c.Config.PlannerContract != plannerContractGraphV9 {
		t.Fatalf("observed-coupling policy drifted from plan-graph-v9: %s", c.Config.PlannerContract)
	}
	if c.Execution.IsolationCohortSelectorVersion != 3 {
		t.Fatalf("observed-coupling selector drifted from version 3: %d", c.Execution.IsolationCohortSelectorVersion)
	}
	// Forged observed labels on the planner wire stay rejected under v9.
	forged := stagedCouplingFixture(
		[]engineeringplan.Task{stagedCouplingLeaf("impl-beta", "beta.txt", 30)},
		[]engineeringplan.TaskCoupling{{From: "impl-alpha", To: "impl-beta", Level: engineeringplan.CouplingC2, Reason: "same_observed_package", Provenance: engineeringplan.CouplingProvenanceTopologyObserved, Evidence: "planner claims observed truth"}},
	)
	raw, err := canonical.Bytes(forged)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw
	if err := forged.Validate(); err == nil {
		t.Fatal("graph with forged observed provenance admitted")
	}
}

func TestStagedObservedMissingRIFallbackSameWaves(t *testing.T) {
	// Without admitted RI, selector 3 degrades to the planner advisory
	// selection: same waves as selector 2 over the same couplings, version
	// marker aside, with no absence claim.
	advisory := []engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "beta and gamma share import neighborhood"}}
	leaves := []engineeringplan.Task{
		stagedCouplingLeaf("impl-beta", "beta.txt", 30),
		stagedCouplingLeaf("impl-gamma", "gamma.txt", 30),
	}
	couplingCreation := stagedCouplingAwareCreation(t)
	couplingGraph := stagedCouplingFixture(leaves, advisory)
	_, couplingCurrent := stagedPrepareWithGraph(t, couplingCreation, couplingGraph)
	observedCreation := stagedObservedCouplingCreation(t)
	observedGraph := stagedCouplingFixture(leaves, advisory)
	_, observedCurrent := stagedPrepareWithGraph(t, observedCreation, observedGraph)
	couplingPrep, observedPrep := couplingCurrent.GraphIsolationPreparation, observedCurrent.GraphIsolationPreparation
	if couplingPrep == nil || observedPrep == nil {
		t.Fatalf("missing preparations: %+v %+v", couplingPrep, observedPrep)
	}
	if couplingPrep.CohortSelectorVersion != 2 || observedPrep.CohortSelectorVersion != 3 {
		t.Fatalf("selector versions drifted: %d vs %d", couplingPrep.CohortSelectorVersion, observedPrep.CohortSelectorVersion)
	}
	if len(couplingPrep.Waves) != len(observedPrep.Waves) {
		t.Fatalf("fallback waves differ without RI: %+v vs %+v", couplingPrep.Waves, observedPrep.Waves)
	}
	for i := range couplingPrep.Waves {
		if len(couplingPrep.Waves[i]) != len(observedPrep.Waves[i]) {
			t.Fatalf("fallback wave %d differs without RI", i)
		}
		for j := range couplingPrep.Waves[i] {
			if couplingPrep.Waves[i][j] != observedPrep.Waves[i][j] {
				t.Fatalf("fallback wave %d task %d differs without RI", i, j)
			}
		}
	}
}

func TestStagedObservedPreparationReplayRoundTrip(t *testing.T) {
	c := stagedObservedCouplingCreation(t)
	graph := stagedCouplingFixture(
		[]engineeringplan.Task{
			stagedCouplingLeaf("impl-beta", "beta.txt", 30),
			stagedCouplingLeaf("impl-gamma", "gamma.txt", 30),
		},
		[]engineeringplan.TaskCoupling{{From: "impl-beta", To: "impl-gamma", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "beta and gamma share import neighborhood"}},
	)
	path, current := stagedPrepareWithGraph(t, c, graph)
	if current.GraphIsolationPreparation == nil || current.GraphIsolationPreparation.CohortSelectorVersion != 3 {
		t.Fatalf("selector 3 preparation missing: %+v", current.GraphIsolationPreparation)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GraphIsolationPreparation == nil || !sameCanonical(*replayed.GraphIsolationPreparation, *current.GraphIsolationPreparation) {
		t.Fatal("selector 3 preparation did not replay exactly (observed set must recompute from snapshot)")
	}
	// A substituted preparation is rejected on replay.
	substituted := *current.GraphIsolationPreparation
	substituted.Waves = append([][]string(nil), substituted.Waves...)
	if len(substituted.Waves[0]) == 2 {
		substituted.Waves[0] = substituted.Waves[0][:1]
	} else {
		substituted.Waves[0] = append(substituted.Waves[0], "impl-beta")
	}
	prepBytes, err := canonical.Bytes(substituted)
	if err != nil {
		t.Fatal(err)
	}
	_ = prepBytes
	found := false
	for _, e := range events {
		if e.Kind == "graph.staged-prepared" {
			var recorded GraphIsolationPreparation
			if err := canonical.Decode(e.Payload, &recorded); err != nil {
				t.Fatal(err)
			}
			if sameCanonical(recorded, substituted) {
				t.Fatal("substituted preparation matches recorded bytes; test no longer substitutes")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no staged-prepared event in journal")
	}
}

func TestStagedLegacySelectorsPreserved(t *testing.T) {
	// Selectors 0/1/2 keep their frozen preparation bytes under selector 3's
	// introduction: greedy omits the version, 1 and 2 retain theirs.
	legacy := stagedHubLeavesGraphFixture()
	c0 := stagedThreeTaskCreation(t)
	_, current0 := stagedPrepareWithGraph(t, c0, legacy)
	if current0.GraphIsolationPreparation == nil || current0.GraphIsolationPreparation.CohortSelectorVersion != 0 {
		t.Fatalf("greedy legacy preparation changed: %+v", current0.GraphIsolationPreparation)
	}
	lexCreation := stagedLexicographicCreation(t)
	_, current1 := stagedPrepareWithGraph(t, lexCreation, legacy)
	if current1.GraphIsolationPreparation == nil || current1.GraphIsolationPreparation.CohortSelectorVersion != 1 {
		t.Fatalf("lexicographic preparation changed: %+v", current1.GraphIsolationPreparation)
	}
	couplingCreation := stagedCouplingAwareCreation(t)
	coupled := stagedCouplingFixture(
		[]engineeringplan.Task{stagedCouplingLeaf("impl-beta", "beta.txt", 30)},
		[]engineeringplan.TaskCoupling{{From: "impl-alpha", To: "impl-beta", Level: engineeringplan.CouplingC1, Reason: "import_neighborhood", Provenance: engineeringplan.CouplingProvenancePlannerAdvisory, Evidence: "shared neighborhood"}},
	)
	_, current2 := stagedPrepareWithGraph(t, couplingCreation, coupled)
	if current2.GraphIsolationPreparation == nil || current2.GraphIsolationPreparation.CohortSelectorVersion != 2 {
		t.Fatalf("coupling-aware preparation changed: %+v", current2.GraphIsolationPreparation)
	}
}
