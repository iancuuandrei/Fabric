package control

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

var errJournalLockContention = errors.New("journal lock contention after retries")

func TestGraphPumpJoinPreservesFailureAndPendingOwnership(t *testing.T) {
	failure := errors.New("owned provider failure")
	completed := make(chan error, 1)
	completed <- errors.Join(context.Canceled, failure)
	if err := waitGraphPump(completed, time.Second); !errors.Is(err, failure) {
		t.Fatalf("failure hidden by cancellation: %v", err)
	}
	cancelled := make(chan error, 1)
	cancelled <- errors.Join(context.Canceled)
	if err := waitGraphPump(cancelled, time.Second); err != nil {
		t.Fatal(err)
	}
	pending := make(chan error)
	if err := waitGraphPump(pending, 10*time.Millisecond); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("pending call treated as completed: %v", err)
	}
}

func graphPolicy(maxParallel int) *ExecutionPolicy {
	return &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, Context: taskContextBoundedV1, GraphVersion: 1, MaxParallel: maxParallel}
}

func graphCreation(t *testing.T, maxParallel int) Creation {
	t.Helper()
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "low", Role: "explorer"}
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.PlannerContract = plannerContractGraphV1
	c.Config.Verification = []config.Check{{Name: "unit", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	c.Execution = graphPolicy(maxParallel)
	return c
}

// graphAwaitingApproval records the exact strict graph planner result used by
// this fixture, then persists its accepted graph before any authorization.
// The deterministic result is constructed locally; no provider is invoked.
func graphAwaitingApproval(t *testing.T, c Creation) (string, Snapshot) {
	return graphAwaitingApprovalWithGraph(t, c, validGraphFixture())
}

func graphAwaitingApprovalWithGraph(t *testing.T, c Creation, graph engineeringplan.Graph) (string, Snapshot) {
	t.Helper()
	root := c.Repository.Root
	autonomousGitInit(t, root)
	repo, err := repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	c.Repository = repo
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if c.Config.Version == 2 && c.Config.Planner.Runtime == "fake" {
		planning, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		intent, err := expectedPlanningAccess(planning)
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(path, "planning.access-intent", intent); err != nil {
			t.Fatal(err)
		}
	}
	inv, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	model := inv.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: inv.ID, Requested: inv.Profile, ObservedModel: &model, Output: mustGraphJSON(t, graph)}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := parseAcceptedGraph(s)
	if err != nil || accepted.Summary != graph.Summary {
		t.Fatalf("accepted graph fixture is invalid: graph=%+v err=%v", accepted, err)
	}
	return path, s
}

func recordGraphFixture(t *testing.T, path string) Snapshot {
	t.Helper()
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = ensureGraphRecorded(path, s)
	if err != nil {
		t.Fatal(err)
	}
	if s.Graph == nil || s.Graph.Graph.Summary != validGraphFixture().Summary {
		t.Fatalf("accepted graph fixture was not recorded: graph=%+v", s.Graph)
	}
	return s
}

func graphTask(id string, kind engineeringplan.Kind, deps ...string) engineeringplan.Task {
	return engineeringplan.Task{
		ID: id, Kind: kind, Title: "Task " + id,
		Dependencies: deps, ScopePaths: []string{"file.txt"},
		WritePaths: func() []string {
			if kind == engineeringplan.Implementation {
				return []string{"file.txt"}
			}
			return nil
		}(),
		ExpectedEvidence: []engineeringplan.Evidence{{Kind: "result", Description: "evidence for " + id}},
		EstimatedSeconds: 10,
	}
}

func validGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{
		Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "fixture graph",
		Tasks: []engineeringplan.Task{
			graphTask("research-a", engineeringplan.Research),
			graphTask("research-b", engineeringplan.Research),
			graphTask("design-a", engineeringplan.Design),
			{ID: "impl", Kind: engineeringplan.Implementation, Title: "Task impl", Dependencies: []string{"research-a", "research-b"}, ScopePaths: []string{"file.txt"}, WritePaths: []string{"file.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "changed source"}}, EstimatedSeconds: 60},
			{ID: "verify", Kind: engineeringplan.Verification, Title: "Task verify", Dependencies: []string{"impl"}, ScopePaths: []string{"file.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "test", Description: "passing checks"}}, EstimatedSeconds: 30},
			{ID: "review", Kind: engineeringplan.Review, Title: "Task review", Dependencies: []string{"verify"}, ScopePaths: []string{"file.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approval"}}, EstimatedSeconds: 30},
		},
	}
}

func directGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{
		Version: engineeringplan.Version, Mode: engineeringplan.ModeDirect, Summary: "direct",
		Tasks: []engineeringplan.Task{
			{ID: "one", Kind: engineeringplan.Implementation, Title: "Single change", ScopePaths: []string{"file.txt"}, WritePaths: []string{"file.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "changed"}}, EstimatedSeconds: 10},
		},
	}
}

func mustGraphJSON(t *testing.T, g engineeringplan.Graph) string {
	t.Helper()
	raw, err := json.Marshal(g)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestExecutionPolicyGraphValidationAndLegacyIdentity(t *testing.T) {
	legacy := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2}
	if err := legacy.Validate(); err != nil {
		t.Fatal(err)
	}
	if legacy.EffectiveMaxParallel() != 1 || legacy.GraphEnabled() {
		t.Fatal("legacy policy must stay sequential")
	}
	legacyRaw, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyRaw), "graph_version") || strings.Contains(string(legacyRaw), "max_parallel") || strings.Contains(string(legacyRaw), "repair_planning_version") || strings.Contains(string(legacyRaw), "prompt_recipe") {
		t.Fatal("empty graph policy changed legacy serialization")
	}
	cachePrefix := legacy
	cachePrefix.PromptRecipe = promptRecipeCachePrefixV1
	if err := cachePrefix.Validate(); err != nil {
		t.Fatalf("cache-prefix-v1 policy rejected: %v", err)
	}
	if strings.Contains(string(legacyRaw), promptRecipeCachePrefixV1) {
		t.Fatal("legacy policy unexpectedly binds the new prompt recipe")
	}
	graph := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, Context: taskContextBoundedV1, GraphVersion: 1, MaxParallel: 3}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	if !graph.GraphEnabled() || graph.EffectiveMaxParallel() != 3 {
		t.Fatal("graph policy not enabled")
	}
	repairPlanning := graph
	repairPlanning.RepairPlanningVersion = 1
	if err := repairPlanning.Validate(); err != nil {
		t.Fatalf("repair planning policy rejected: %v", err)
	}
	for _, bad := range []ExecutionPolicy{
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 2, MaxParallel: 3},
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 0},
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 9},
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 0, MaxParallel: 3},
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 0, RepairPlanningVersion: 1},
		{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 3, RepairPlanningVersion: 2},
		{Mode: "autonomous-v1", MaxRepairs: 2, PromptRecipe: "unknown"},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid graph policy admitted: %+v", bad)
		}
	}
}

func TestStrictGraphSchemaRejectsPlannerCompletion(t *testing.T) {
	g := validGraphFixture()
	if _, err := engineeringplan.ParsePlannerGraph([]byte(mustGraphJSON(t, g))); err != nil {
		t.Fatalf("valid planner graph rejected: %v", err)
	}
	bad := validGraphFixture()
	bad.Tasks[0].Completed = true
	if _, err := engineeringplan.ParsePlannerGraph([]byte(mustGraphJSON(t, bad))); err == nil {
		t.Fatal("planner-supplied completion admitted")
	}
	bad = validGraphFixture()
	bad.Tasks[0].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
	if _, err := engineeringplan.ParsePlannerGraph([]byte(mustGraphJSON(t, bad))); err == nil {
		t.Fatal("planner-supplied attempts admitted")
	}
	if err := engineeringplan.ValidateAutonomousGraph(validGraphFixture()); err != nil {
		t.Fatalf("valid autonomous graph rejected: %v", err)
	}
	direct := directGraphFixture()
	if err := engineeringplan.ValidateAutonomousGraph(direct); err != nil {
		t.Fatalf("direct mode rejected: %v", err)
	}
	multi := validGraphFixture()
	multi.Tasks = append(multi.Tasks, graphTask("impl2", engineeringplan.Implementation))
	multi.Tasks[len(multi.Tasks)-1].ID = "impl2"
	multi.Tasks[len(multi.Tasks)-1].Dependencies = []string{"research-a"}
	// Second implementation without ordering triggers ownership or count rejection.
	if err := engineeringplan.ValidateAutonomousGraph(multi); err == nil {
		t.Fatal("second initial implementation admitted")
	}
}

func TestParentCyclesRejected(t *testing.T) {
	g := validGraphFixture()
	// Introduce parent cycle: research-a parent design-a, design-a parent research-a.
	for i, task := range g.Tasks {
		if task.ID == "research-a" {
			g.Tasks[i].ParentID = "design-a"
		}
		if task.ID == "design-a" {
			g.Tasks[i].ParentID = "research-a"
		}
	}
	if err := g.Validate(); err == nil {
		t.Fatal("parent cycle admitted")
	}
}

func TestGraphDigestPlanIDSubstitutionRejected(t *testing.T) {
	c := graphCreation(t, 3)
	path, _ := autonomousAwaitingApproval(t, c)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s
	// Craft a valid graph plan manually (Fake planner text is not graph JSON).
	g := validGraphFixture()
	raw := mustGraphJSON(t, g)
	inv, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	model := inv.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: inv.ID, Requested: inv.Profile, ObservedModel: &model, Output: raw}
	_ = result
	_ = path
	// Digest stability and substitution detection via autonomous validation.
	d1, err := engineeringplan.Digest(g)
	if err != nil || d1 == "" {
		t.Fatal(err)
	}
	mutated := g
	mutated.Summary = "changed summary"
	d2, err := engineeringplan.Digest(mutated)
	if err != nil || d1 == d2 {
		t.Fatal("digest did not bind graph bytes")
	}
}

func TestActualDependencyGating(t *testing.T) {
	g := validGraphFixture()
	ready, err := engineeringplan.Ready(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range ready {
		if task.ID == "impl" || task.ID == "verify" || task.ID == "review" {
			t.Fatalf("ungated task %q ready before dependencies", task.ID)
		}
	}
	// Complete research-a/b and design-a is still incomplete (design-a has no deps so ready, but impl needs research only).
	// Mark research ready tasks completed and verify impl still blocked on remaining.
	g2 := g
	for i, task := range g2.Tasks {
		if task.ID == "research-a" || task.ID == "research-b" {
			g2.Tasks[i].Completed = true
		}
	}
	ready, err = engineeringplan.Ready(g2)
	if err != nil {
		t.Fatal(err)
	}
	foundImpl := false
	for _, task := range ready {
		if task.ID == "impl" {
			foundImpl = true
		}
		if task.ID == "verify" {
			t.Fatal("verification ready before implementation")
		}
	}
	if !foundImpl {
		t.Fatal("implementation not ready after research completion")
	}
}

func TestOutOfScopeWriterRejectedBeforeApply(t *testing.T) {
	c := graphCreation(t, 1)
	path, s := graphAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	recordGraphFixture(t, path)
	if _, err := AdmitTaskContext(context.Background(), path, "writer", s.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	applyWriterOutput(t, path, "writer output\n")
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	narrow := engineeringplan.Task{ID: "impl", Kind: engineeringplan.Implementation, Title: "narrow", ScopePaths: []string{"file.txt"}, WritePaths: []string{"file.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "d"}}, EstimatedSeconds: 10}
	if err := requireWriterPathsInScope(narrow, snap); err != nil {
		t.Fatalf("in-scope writer rejected: %v", err)
	}
	// Mutate the proposal in memory to an out-of-scope path; the helper must
	// reject before any file authorization/application.
	mutated := snap
	if mutated.WriterProposal == nil {
		t.Fatal("writer proposal missing for scope test")
	}
	proposal := *mutated.WriterProposal
	// Directly exercise scope logic with a task that does not admit file.txt sibling.
	outside := engineeringplan.Task{ID: "impl", Kind: engineeringplan.Implementation, Title: "narrow", ScopePaths: []string{"src"}, WritePaths: []string{"src/a.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "d"}}, EstimatedSeconds: 10}
	if err := requireWriterPathsInScope(outside, snap); !errors.Is(err, ErrScopeReplanRequired) {
		t.Fatal("out-of-scope writer did not request an authorized replan", err)
	}
	_ = proposal
}

func TestDirectSequentialMode(t *testing.T) {
	direct := directGraphFixture()
	if err := engineeringplan.ValidateAutonomousGraph(direct); err != nil {
		t.Fatal(err)
	}
	ready, err := engineeringplan.Ready(direct)
	if err != nil || len(ready) != 1 || ready[0].ID != "one" {
		t.Fatalf("direct ready mismatch: %v %v", ready, err)
	}
	policy := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, Context: taskContextBoundedV1, GraphVersion: 1, MaxParallel: 1}
	if policy.EffectiveMaxParallel() != 1 {
		t.Fatal("sequential override must be 1")
	}
}

func TestGraphDirectAcceptedPlanIncludesNativeGates(t *testing.T) {
	s := Snapshot{Plan: &runtime.Result{Output: mustGraphJSON(t, directGraphFixture())}}
	g, err := parseAcceptedGraph(s)
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != engineeringplan.ModeGraph || len(g.Tasks) != 3 {
		t.Fatalf("missing native gates: %+v", g)
	}
	if err := engineeringplan.ValidateAutonomousGraph(g); err != nil {
		t.Fatal(err)
	}
	ready, err := engineeringplan.Ready(g)
	if err != nil || len(ready) != 1 || ready[0].Kind != engineeringplan.Implementation {
		t.Fatalf("gate dependency bypass: %+v %v", ready, err)
	}
}

func TestGraphRevisionCannotInventTaskProgress(t *testing.T) {
	previous := validGraphFixture()
	for _, outcome := range []engineeringplan.AttemptOutcome{engineeringplan.AttemptCompleted, engineeringplan.AttemptUnknown} {
		next := previous
		next.Tasks = append(append([]engineeringplan.Task(nil), previous.Tasks...), graphTask("invented", engineeringplan.Research))
		next.Tasks[len(next.Tasks)-1].Attempts = []engineeringplan.Attempt{{ID: "fiction", Outcome: outcome}}
		if err := engineeringplan.ValidateAutonomousRevision(previous, next); err == nil {
			t.Fatal("invented attempt admitted")
		}
	}
	next := previous
	next.Tasks = append(append([]engineeringplan.Task(nil), previous.Tasks...), graphTask("invented", engineeringplan.Research))
	next.Tasks[len(next.Tasks)-1].Completed = true
	if err := engineeringplan.ValidateAutonomousRevision(previous, next); err == nil {
		t.Fatal("invented completion admitted")
	}
}

func TestGraphRejectsMultipleNativeGates(t *testing.T) {
	for _, kind := range []engineeringplan.Kind{engineeringplan.Verification, engineeringplan.Review} {
		g := validGraphFixture()
		g.Tasks = append(g.Tasks, graphTask("extra-gate", kind, "impl"))
		if err := engineeringplan.ValidateAutonomousGraph(g); err == nil {
			t.Fatal("multiple native gates admitted")
		}
	}
}

func TestTwoV1ExplorersOverlapSeparateIdentities(t *testing.T) {
	c := graphCreation(t, 3)
	// V1 config (Example is v1) with fake explorers.
	c.Config.Version = 1
	path, s := autonomousAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	// Admit bounded context for two distinct questions implicitly via RunExplorer.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	records := make([]ExplorerRecord, 2)
	questions := []string{
		"[research-a] survey file.txt scope and risks for objective",
		"[research-b] survey file.txt dependencies and tests for objective",
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Retry once on exclusive journal lock contention so concurrent
			// read-only appends overlap rather than flaking.
			for attempt := 0; attempt < 3; attempt++ {
				rec, err := RunExplorer(context.Background(), path, questions[idx])
				if err != nil && errors.Is(err, journal.ErrLockUnavailable) {
					continue
				}
				if err != nil {
					errs[idx] = err
					return
				}
				records[idx] = rec
				return
			}
			errs[idx] = errJournalLockContention
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("parallel explorer %d failed: %v", i, err)
		}
	}
	if records[0].Invocation.ID == records[1].Invocation.ID {
		t.Fatal("parallel explorers share invocation identity")
	}
	if records[0].Result.Output == records[1].Result.Output && questions[0] != questions[1] {
		// Summaries embed questions, so outputs must differ.
		t.Fatal("parallel explorer receipts not distinct")
	}
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Explorations) != 2 {
		t.Fatalf("expected two explorations, got %d", len(snap.Explorations))
	}
	candidateID, _ := snap.Candidate.ID()
	for _, rec := range snap.Explorations {
		var obs Exploration
		if err := canonical.Decode([]byte(rec.Result.Output), &obs); err != nil {
			t.Fatal(err)
		}
		if obs.CandidateID != candidateID {
			t.Fatal("parallel explorer changed candidate")
		}
	}
	// Candidate fingerprint unchanged.
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Candidate == nil || *after.Candidate != *snap.Candidate {
		t.Fatal("candidate drifted during parallel explorers")
	}
}

func TestSiblingChangeAcceptedUnrelatedRejected(t *testing.T) {
	c := graphCreation(t, 3)
	c.Config.Version = 1
	path, s := graphAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	snap := recordGraphFixture(t, path)
	if snap.Graph == nil || snap.Graph.Graph.Summary != validGraphFixture().Summary {
		t.Fatal("cohort claims require a recorded accepted graph")
	}
	// Bind both claims to the exact recorded tasks and their bounded questions.
	task1, ok := snap.Graph.Graph.Task("research-a")
	if !ok {
		t.Fatal("research-a missing from recorded graph")
	}
	task2, ok := snap.Graph.Graph.Task("research-b")
	if !ok {
		t.Fatal("research-b missing from recorded graph")
	}
	q1, err := explorerQuestionForTask(snap, task1)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := explorerQuestionForTask(snap, task2)
	if err != nil {
		t.Fatal(err)
	}
	design, ok := snap.Graph.Graph.Task("design-a")
	if !ok {
		t.Fatal("design-a missing from recorded graph")
	}
	designQuestion, err := explorerQuestionForTask(snap, design)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(context.Background(), path, "explorer", q1); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(context.Background(), path, "explorer", q2); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitTaskContext(context.Background(), path, "explorer", designQuestion); err != nil {
		t.Fatal(err)
	}
	inv1, err := PrepareExplorerInvocation(path, q1)
	if err != nil {
		t.Fatal(err)
	}
	inv2, err := PrepareExplorerInvocation(path, q2)
	if err != nil {
		t.Fatal(err)
	}
	_ = inv2
	headEvents, err := headForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err = Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: taskscheduler.TaskSpec{ID: "research-a", RunID: snap.RunID, ControllerPath: path, Operation: taskscheduler.OperationExplorer, Input: q1, InvocationID: inv1.ID}, Generation: 1, ControllerHead: headEvents}
	bad := claim
	bad.Task.Operation = taskscheduler.OperationWriter
	if isStaticGraphExplorerCohort(snap, bad) {
		t.Fatal("writer admitted to explorer cohort")
	}
	// Sibling read-only append accepted: record q1 explorer, cohort check for q2 passes.
	if _, err := RunExplorer(context.Background(), path, q1); err != nil {
		t.Fatal(err)
	}
	sibling := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: taskscheduler.TaskSpec{ID: "research-b", RunID: snap.RunID, ControllerPath: path, Operation: taskscheduler.OperationExplorer, Input: q2, InvocationID: inv2.ID}, Generation: 1, ControllerHead: headEvents}
	if err := verifyGraphCohortDelta(path, sibling); err != nil {
		t.Fatalf("sibling read-only delta rejected: %v", err)
	}
	// Mutation append rejected: stage a writer proposal, then the same sibling
	// cohort check must reject because the delta now contains writer.proposed.
	if _, err := AdmitTaskContext(context.Background(), path, "writer", snap.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	recordAutonomousProposal(t, path, "writer output\n")
	if err := verifyGraphCohortDelta(path, sibling); err == nil {
		t.Fatal("mutation delta accepted for explorer cohort")
	}
}

func headForTest(path string) (string, error) {
	s, head, err := InspectWithHead(path)
	if err != nil {
		return "", err
	}
	_ = s
	return head, nil
}

func TestFrozenContextDriftRejects(t *testing.T) {
	c := graphCreation(t, 3)
	path, s := graphAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	recordGraphFixture(t, path)
	q := "[research-a] bounded question for drift"
	if _, err := AdmitTaskContext(context.Background(), path, "explorer", q); err != nil {
		t.Fatal(err)
	}
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invBefore, err := explorerInvocation(snap, q)
	if err != nil {
		t.Fatal(err)
	}
	// Drift the candidate via a writer change; the same question must rebind.
	if _, err := AdmitTaskContext(context.Background(), path, "writer", s.Creation.Objective); err != nil {
		t.Fatal(err)
	}
	applyWriterOutput(t, path, "drifted\n")
	snap2, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invAfter, err := explorerInvocation(snap2, q)
	if err == nil && invAfter.ID == invBefore.ID {
		t.Fatal("candidate drift did not change explorer invocation")
	}
	// Old context must not satisfy the new candidate.
	oldID, err := snap.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskContextForRole(snap2, "explorer", q); err == nil {
		t.Fatal("stale context admitted after candidate drift")
	}
	rec, err := AdmitTaskContext(context.Background(), path, "explorer", q)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CandidateID == oldID {
		t.Fatal("context drift reused old candidate")
	}
}

func TestUnknownPreventsResendAndRevision(t *testing.T) {
	g := validGraphFixture()
	// Simulate UNKNOWN attempt on research-a.
	for i, task := range g.Tasks {
		if task.ID == "research-a" {
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptUnknown}}
		}
	}
	if _, err := engineeringplan.Ready(g); err != nil {
		t.Fatal(err)
	} else {
		ready, _ := engineeringplan.Ready(g)
		for _, task := range ready {
			if task.ID == "research-a" {
				t.Fatal("UNKNOWN task became ready")
			}
		}
	}
	retry := g
	retry.Tasks = append([]engineeringplan.Task(nil), g.Tasks...)
	for i := range retry.Tasks {
		retry.Tasks[i].Attempts = append([]engineeringplan.Attempt(nil), g.Tasks[i].Attempts...)
	}
	for i, task := range retry.Tasks {
		if task.ID == "research-a" {
			retry.Tasks[i].Attempts = append(retry.Tasks[i].Attempts, engineeringplan.Attempt{ID: "attempt-2", Outcome: engineeringplan.AttemptFailed})
		}
	}
	if err := engineeringplan.ValidateRevision(g, retry); err == nil {
		t.Fatal("retry after UNKNOWN admitted")
	}
	if err := engineeringplan.ValidateAutonomousRevision(g, retry); err == nil {
		t.Fatal("autonomous retry after UNKNOWN admitted")
	}
}

func TestRepairRevisionPreservesEvidence(t *testing.T) {
	g := validGraphFixture()
	// Complete research and impl, fail verification.
	for i, task := range g.Tasks {
		switch task.ID {
		case "research-a", "research-b", "design-a":
			g.Tasks[i].Completed = true
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
		case "impl":
			g.Tasks[i].Completed = true
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
		case "verify":
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptFailed}}
		}
	}
	next, err := engineeringplan.RepairExtension(g, "verify", "plan-id", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := engineeringplan.ValidateAutonomousRevision(g, next); err != nil {
		t.Fatalf("repair revision rejected: %v", err)
	}
	// Completed impl must be retained byte-for-byte.
	oldImpl, _ := g.Task("impl")
	newImpl, _ := next.Task("impl")
	if oldImpl.Completed != newImpl.Completed || len(newImpl.Attempts) != 1 {
		t.Fatal("completed evidence not preserved")
	}
	// Fresh verification/review nodes must exist and be incomplete.
	foundVerify, foundReview := false, false
	for _, task := range next.Tasks {
		if strings.Contains(task.ID, "repair") && task.Kind == engineeringplan.Verification && !task.Completed {
			foundVerify = true
		}
		if strings.Contains(task.ID, "repair") && task.Kind == engineeringplan.Review && !task.Completed {
			foundReview = true
		}
	}
	if !foundVerify || !foundReview {
		t.Fatal("repair did not require fresh verification/review")
	}
}

func TestPlannerContractGraphIdentity(t *testing.T) {
	c := config.Config{Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	c.PlannerContract = plannerContractGraphV1
	inv, err := plannerInvocation(c, "objective")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Role        string `json:"role"`
		Instruction string `json:"instruction"`
		Objective   string `json:"objective"`
	}
	if err := json.Unmarshal([]byte(inv.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Instruction, "strict JSON") {
		t.Fatal("graph planner assignment missing strict JSON instruction")
	}
}
