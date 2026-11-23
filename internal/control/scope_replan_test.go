package control

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestScopeReplanIsCandidateBoundAndPreservesCompletedEvidence(t *testing.T) {
	s := scopeReplanFixture(t)
	record, err := buildScopeReplanRecord(s)
	if err != nil {
		t.Fatal(err)
	}
	if record.Sequence != 1 || record.Revision != 2 || len(record.RequestedPaths) != 1 || record.RequestedPaths[0] != "pkg/new.go" {
		t.Fatalf("unexpected scoped replan: %+v", record)
	}
	var replayed Snapshot
	replayed = s
	payload, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayGraphScopeReplan(&replayed, journal.Event{Kind: "graph.scope-replanned", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(replayed.ScopeReplans) != 1 || replayed.Graph.Revision != 2 || replayed.Graph.Digest != record.GraphHash {
		t.Fatalf("scope replan not durably replayed: %+v", replayed.Graph)
	}
	if _, ok := replayed.Graph.Graph.Task("impl-a"); ok {
		t.Fatal("previous unstarted task remains executable")
	}
	next, ok := replayed.Graph.Graph.Task(record.NextTaskID)
	if !ok || next.Completed || len(next.Attempts) != 0 || strings.Join(next.ScopePaths, ",") != "pkg" || strings.Join(next.WritePaths, ",") != "pkg/new.go,pkg/old.go" {
		t.Fatalf("replacement task lost its immutable scope or exact write set: %+v", next)
	}
	if !replayed.Graph.Graph.Tasks[0].Completed || replayed.Graph.Evidence["research"].Outcome != "completed" {
		t.Fatal("completed task evidence was not preserved")
	}
}

func TestScopeReplanRejectsEscapeBudgetUnknownAndTampering(t *testing.T) {
	t.Run("immutable scope escape", func(t *testing.T) {
		s := scopeReplanFixture(t)
		s.WriterProposal.Prepared.Proposal.Changes[1].Path = "private/escape.go"
		if _, err := buildScopeReplanRecord(s); err == nil {
			t.Fatal("scope escape was admitted")
		}
	})
	t.Run("budget exhausted", func(t *testing.T) {
		s := scopeReplanFixture(t)
		s.ScopeReplans = []ScopeReplanRecord{{Sequence: 1}}
		if _, err := buildScopeReplanRecord(s); err == nil {
			t.Fatal("exhausted scope replan budget was ignored")
		}
	})
	t.Run("unknown file effect", func(t *testing.T) {
		s := scopeReplanFixture(t)
		s.FileOutcome = "UNKNOWN"
		if _, err := buildScopeReplanRecord(s); err == nil {
			t.Fatal("unknown file effect did not block replan")
		}
	})
	t.Run("pending writer effect", func(t *testing.T) {
		s := scopeReplanFixture(t)
		s.WriterHost.RuntimeReceipt = nil
		if _, err := buildScopeReplanRecord(s); err == nil {
			t.Fatal("pending writer was allowed to replan")
		}
	})
	t.Run("candidate substitution", func(t *testing.T) {
		s := scopeReplanFixture(t)
		record, err := buildScopeReplanRecord(s)
		if err != nil {
			t.Fatal(err)
		}
		record.CandidateID = strings.Repeat("f", 64)
		payload, err := canonical.Bytes(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := replayGraphScopeReplan(&s, journal.Event{Kind: "graph.scope-replanned", Payload: payload}); err == nil {
			t.Fatal("candidate substitution replayed")
		}
	})
	t.Run("graph substitution", func(t *testing.T) {
		s := scopeReplanFixture(t)
		record, err := buildScopeReplanRecord(s)
		if err != nil {
			t.Fatal(err)
		}
		record.Graph.Summary = "substituted graph"
		payload, err := canonical.Bytes(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := replayGraphScopeReplan(&s, journal.Event{Kind: "graph.scope-replanned", Payload: payload}); err == nil {
			t.Fatal("substituted graph replayed")
		}
	})
	t.Run("old runs remain disabled", func(t *testing.T) {
		s := scopeReplanFixture(t)
		s.Creation.Execution.ScopeReplanVersion = 0
		s.Creation.Execution.MaxScopeReplans = 0
		if _, err := buildScopeReplanRecord(s); err == nil {
			t.Fatal("legacy run admitted scope replan")
		}
	})
}

func TestScopeReplanPolicyIsExplicitAndBounded(t *testing.T) {
	policy := ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, ScopeReplanVersion: 1, MaxScopeReplans: 1}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	cohortPolicy := ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 2, RepairPlanningVersion: 1, Context: taskContextBoundedV1, ParallelImplementationVersion: 1, ScopeReplanVersion: 2, ScopeReplanDesignVersion: 2, MaxScopeReplans: 1}
	if err := cohortPolicy.Validate(); err != nil {
		t.Fatalf("parallel scope replan policy invalid: %v", err)
	}
	isolatedPolicy := ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 2, RepairPlanningVersion: 1, Context: taskContextBoundedV1, IsolatedImplementationVersion: 1, IsolationCapacity: &engineeringplan.ResourceCapacity{}, IsolationEstimate: &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, RuntimeSlots: 1}, ScopeReplanVersion: 2, ScopeReplanDesignVersion: 2, MaxScopeReplans: 1}
	if err := isolatedPolicy.Validate(); err != nil {
		t.Fatalf("isolated scope replan policy invalid: %v", err)
	}
	for _, invalid := range []ExecutionPolicy{
		{Mode: "autonomous-v1", ScopeReplanVersion: 1, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, ScopeReplanVersion: 1, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, ScopeReplanVersion: 1, MaxScopeReplans: 3},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, ScopeReplanVersion: 0, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, ScopeReplanVersion: 1, ScopeReplanDesignVersion: 2, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, ScopeReplanVersion: 2, ScopeReplanDesignVersion: 2, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 2, RepairPlanningVersion: 1, Context: taskContextBoundedV1, ParallelImplementationVersion: 1, IsolatedImplementationVersion: 1, ScopeReplanVersion: 2, ScopeReplanDesignVersion: 2, MaxScopeReplans: 1},
		{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 2, RepairPlanningVersion: 1, Context: taskContextBoundedV1, ParallelImplementationVersion: 1, ScopeReplanVersion: 2, ScopeReplanDesignVersion: 1, MaxScopeReplans: 1},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid policy admitted: %+v", invalid)
		}
	}
}

func TestScopeReplannedCohortGraphReplacesSelectedTasksAndPreservesCompletedWork(t *testing.T) {
	graph := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "cohort scope refinement", Tasks: []engineeringplan.Task{
		{ID: "research", Kind: engineeringplan.Research, Title: "Inspect", ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "note", Description: "inspect"}}, EstimatedSeconds: 5, Completed: true, Attempts: []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}},
		{ID: "impl-a", Kind: engineeringplan.Implementation, Title: "Implement A", Dependencies: []string{"research"}, ScopePaths: []string{"pkg"}, WritePaths: []string{"pkg/a.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "A"}}, EstimatedSeconds: 10},
		{ID: "impl-b", Kind: engineeringplan.Implementation, Title: "Implement B", Dependencies: []string{"research"}, ScopePaths: []string{"pkg"}, WritePaths: []string{"pkg/b.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "B"}}, EstimatedSeconds: 10},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Verify", Dependencies: []string{"impl-a", "impl-b"}, ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "verify"}}, EstimatedSeconds: 10},
	}}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	updated, err := buildScopeReplannedCohortGraph(graph, []ScopeReplanTaskUpdate{
		{TaskID: "impl-a", NextTaskID: "impl-a-v2", RequestedPaths: []string{"pkg/new.go"}},
		// Isolated sibling bindings are revision-bound. A sibling can receive a
		// fresh graph identity without widening its path set.
		{TaskID: "impl-b", NextTaskID: "impl-b-v2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := updated.Task("impl-a-v2")
	if !ok || a.Completed || len(a.Attempts) != 0 || strings.Join(a.WritePaths, ",") != "pkg/a.go,pkg/new.go" {
		t.Fatalf("replanned writer lost exact expanded ownership: %+v", a)
	}
	b, ok := updated.Task("impl-b-v2")
	if !ok || b.Completed || len(b.Attempts) != 0 || strings.Join(b.WritePaths, ",") != "pkg/b.go" {
		t.Fatalf("rebound sibling changed ownership: %+v", b)
	}
	verify, ok := updated.Task("verify")
	if !ok || strings.Join(verify.Dependencies, ",") != "impl-a-v2,impl-b-v2" {
		t.Fatalf("downstream dependencies were not remapped: %+v", verify)
	}
	research, ok := updated.Task("research")
	if !ok || !research.Completed || research.Attempts[0].Outcome != engineeringplan.AttemptCompleted {
		t.Fatal("completed work was not preserved")
	}
}

func TestScopeReplannedCohortGraphRejectsUnsafeOrUnstartedViolations(t *testing.T) {
	graph := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "bounded scope", Tasks: []engineeringplan.Task{
		{ID: "impl", Kind: engineeringplan.Implementation, Title: "Implement", ScopePaths: []string{"pkg"}, WritePaths: []string{"pkg/a.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "implementation"}}, EstimatedSeconds: 5},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Verify", Dependencies: []string{"impl"}, ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "verify"}}, EstimatedSeconds: 5},
	}}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, update := range map[string]ScopeReplanTaskUpdate{
		"outer scope escape":    {TaskID: "impl", NextTaskID: "impl-v2", RequestedPaths: []string{"private/escape.go"}},
		"outside request bound": {TaskID: "impl", NextTaskID: "impl-v2", RequestedPaths: []string{"pkg/b.go", "pkg/c.go", "pkg/d.go", "pkg/e.go", "pkg/f.go", "pkg/g.go", "pkg/h.go", "pkg/i.go", "pkg/j.go"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildScopeReplannedCohortGraph(graph, []ScopeReplanTaskUpdate{update}); err == nil {
				t.Fatal("unsafe or over-budget scope replan was accepted")
			}
		})
	}
	started := graph
	started.Tasks[0].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptFailed}}
	if _, err := buildScopeReplannedCohortGraph(started, []ScopeReplanTaskUpdate{{TaskID: "impl", NextTaskID: "impl-v2", RequestedPaths: []string{"pkg/b.go"}}}); err == nil {
		t.Fatal("started task was replaced by a scope replan")
	}
	if _, err := buildScopeReplannedCohortGraph(graph, []ScopeReplanTaskUpdate{{TaskID: "impl", NextTaskID: "impl-v2"}}); err == nil {
		t.Fatal("identity-only revision without an expanded path was accepted")
	}
}

func scopeReplanFixture(t *testing.T) Snapshot {
	t.Helper()
	creation := graphCreation(t, 1)
	creation.Execution.Context = ""
	creation.Execution.RepairPlanningVersion = 1
	creation.Execution.ScopeReplanVersion = 1
	creation.Execution.MaxScopeReplans = 1
	if err := creation.Execution.Validate(); err != nil {
		t.Fatalf("scope policy invalid: %v", err)
	}
	graph := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "scoped implementation", Tasks: []engineeringplan.Task{
		{ID: "research", Kind: engineeringplan.Research, Title: "Inspect relevant files", ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "note", Description: "inspect files"}}, EstimatedSeconds: 10, Completed: true, Attempts: []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}},
		{ID: "impl-a", Kind: engineeringplan.Implementation, Title: "Implement requested behavior", Dependencies: []string{"research"}, ScopePaths: []string{"pkg"}, WritePaths: []string{"pkg/old.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "implementation"}}, EstimatedSeconds: 20},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run checks", Dependencies: []string{"impl-a"}, ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "checks pass"}}, EstimatedSeconds: 20},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"pkg"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "review candidate"}}, EstimatedSeconds: 20},
	}}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	graphHash, err := engineeringplan.Digest(graph)
	if err != nil {
		t.Fatal(err)
	}
	filesHash, err := worktree.FilesID([]worktree.FileState{})
	if err != nil {
		t.Fatal(err)
	}
	candidate := worktree.Candidate{Version: 1, WorktreeID: strings.Repeat("a", 64), Head: strings.Repeat("b", 40), IndexHash: strings.Repeat("c", 64), FilesHash: filesHash}
	s := Snapshot{
		RunID: strings.Repeat("1", 64), State: "IMPLEMENTING", PlanID: strings.Repeat("2", 64), Creation: creation, ApprovedBy: "human",
		Plan:      &runtime.Result{Version: 1, InvocationID: strings.Repeat("3", 64), Output: "accepted planner output"},
		Workspace: &worktree.Binding{}, WorkspaceOutcome: "CONFIRMED", Candidate: &candidate,
		Graph: &GraphState{PlanID: strings.Repeat("2", 64), Digest: graphHash, Revision: 1, Graph: graph, Evidence: map[string]GraphTaskEvidence{"research": {TaskID: "research", AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: strings.Repeat("4", 64)}}},
	}
	invocation, err := writerInvocationForTask(s, "")
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: "{}"}
	resultHash, err := canonical.Hash("harness.writer-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	beforeHash := strings.Repeat("d", 64)
	newContent := base64.StdEncoding.EncodeToString([]byte("package pkg\n"))
	s.WriterProposal = &WriterRecord{Invocation: invocation, Result: result, Prepared: PreparedFiles{Proposal: fileeffects.Proposal{Before: candidate, Changes: []fileeffects.Change{
		{Path: "pkg/old.go", BeforeHash: &beforeHash, ContentBase64: &newContent},
		{Path: "pkg/new.go", ContentBase64: &newContent},
	}}}}
	s.WriterHost = &WriterHostState{Intent: WriterHostIntent{Invocation: invocation}, RuntimeReceipt: &WriterRuntimeReceipt{InvocationID: invocation.ID, ThreadID: "thread-1", TurnID: "turn-1", JournalHead: strings.Repeat("e", 64), ResultHash: resultHash}}
	return s
}
