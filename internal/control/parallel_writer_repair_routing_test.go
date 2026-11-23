package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/effects"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestParallelCohortSerialRepairSurvivesJournalRestart(t *testing.T) {
	ctx := context.Background()
	c := parallelWriterCreation(t, 2)
	c.Objective = "parallel repair fixture changes requested"
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path, _ := graphAwaitingApprovalWithGraph(t, c, parallelWriterGraphFixture())
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 20; n++ {
		s, err = Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if s.State == "REVIEWING" {
			break
		}
		if s.State != "IMPLEMENTING" {
			t.Fatalf("initial cohort stopped in unexpected state %s", s.State)
		}
		_, progressed, runErr := autonomousGraphImplementing(ctx, path, s)
		if runErr != nil {
			t.Fatal(runErr)
		}
		if !progressed {
			t.Fatal("initial implementation cohort made no progress")
		}
	}
	s, err = Inspect(path)
	if err != nil || s.State != "REVIEWING" || s.GraphWriterBatch == nil || len(s.GraphWriterBatch.Members) != 2 {
		t.Fatalf("two-writer cohort did not reach review: state=%s err=%v", s.State, err)
	}
	if _, err = RunReview(ctx, path); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	if err != nil || s.State != "REPAIRING" {
		t.Fatalf("failed review did not enter repair: state=%s err=%v", s.State, err)
	}
	var repairTaskID string
	for n := 0; n < 20; n++ {
		s, err = autonomousGraphRepairing(ctx, path, s)
		if err != nil {
			t.Fatal(err)
		}
		// RunAutonomous starts each bounded iteration from a fresh replayed snapshot.
		s, err = Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range s.Graph.Graph.Tasks {
			if task.Kind == engineeringplan.Implementation && task.ParentID != "" && task.Completed {
				repairTaskID = task.ID
			}
		}
		if repairTaskID != "" {
			break // This return is the durable crash boundary before fresh verification.
		}
	}
	if repairTaskID == "" || s.Candidate == nil || !autonomousRepairApplied(s) || graphWriterBatchEffectApplied(s) {
		t.Fatalf("serial repair was not independently applied: task=%q repair=%v batch=%v", repairTaskID, autonomousRepairApplied(s), graphWriterBatchEffectApplied(s))
	}
	if _, err := os.Stat(filepath.Join(s.Workspace.Request.Path, "repair.go")); err != nil {
		t.Fatalf("repair file was not applied: %v", err)
	}

	// Reconstruct only from the journal, then advance the fresh native gate.
	s, err = Inspect(path)
	repairTaskIndex := -1
	if err == nil && s.Graph != nil {
		repairTaskIndex = mustGraphTaskIndex(s.Graph.Graph, repairTaskID)
	}
	if err != nil || s.State != "REPAIRING" || repairTaskIndex < 0 || !s.Graph.Graph.Tasks[repairTaskIndex].Completed {
		t.Fatalf("confirmed repair progress did not survive inspect/restart boundary: state=%s err=%v", s.State, err)
	}
	s, err = autonomousGraphRepairing(ctx, path, s)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "REVIEWING" || s.Verification == nil || s.Verification.Plan.CandidateID != mustCandidateID(t, s.Candidate) {
		t.Fatalf("fresh verification did not move to review on the repaired candidate: state=%s verification=%+v", s.State, s.Verification)
	}
	ready, err := RunAutonomous(ctx, path)
	if err != nil || ready.State != "READY" {
		t.Fatalf("serial repair did not reach READY after fresh verification/review: state=%s err=%v", ready.State, err)
	}
	if countJournalKind(t, path, "files.intent") != 2 {
		t.Fatal("expected exactly one aggregate effect and one serial repair effect")
	}
}

func TestParallelPolicyWithOneInitialImplementationUsesItsBatchProof(t *testing.T) {
	ctx := context.Background()
	c := parallelWriterCreation(t, 2)
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	graph := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "one opt-in implementation", Tasks: []engineeringplan.Task{
		{ID: "impl-alpha", Kind: engineeringplan.Implementation, Title: "Create alpha output", ScopePaths: []string{"."}, WritePaths: []string{"alpha.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "alpha output exists"}}, EstimatedSeconds: 30},
		{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"impl-alpha"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
		{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the candidate"}}, EstimatedSeconds: 30},
	}}
	path, s := graphAwaitingApprovalWithGraph(t, c, graph)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	ready, err := RunAutonomous(ctx, path)
	if err != nil || ready.State != "READY" {
		t.Fatalf("single implementation opt-in graph did not reach READY: state=%s err=%v", ready.State, err)
	}
	if ready.GraphWriterBatch == nil || len(ready.GraphWriterBatch.Members) != 1 || len(ready.GraphWriterResults) != 1 || autonomousRepairApplied(ready) {
		t.Fatalf("single initial writer was not classified as exactly one aggregate member: batch=%+v results=%d", ready.GraphWriterBatch, len(ready.GraphWriterResults))
	}
}

func mustGraphTaskIndex(g engineeringplan.Graph, taskID string) int {
	for index, task := range g.Tasks {
		if task.ID == taskID {
			return index
		}
	}
	return -1
}

func mustCandidateID(t *testing.T, candidate *worktree.Candidate) string {
	t.Helper()
	if candidate == nil {
		t.Fatal("candidate unavailable")
	}
	id, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestParallelBatchProofDoesNotCaptureSerialRepair(t *testing.T) {
	before := worktree.Candidate{Version: 1, WorktreeID: strings.Repeat("a", 64), Head: strings.Repeat("b", 40), IndexHash: strings.Repeat("c", 64), FilesHash: strings.Repeat("d", 64), FileCount: 1}
	beforeID, err := before.ID()
	if err != nil {
		t.Fatal(err)
	}
	initialIntent := effects.Intent{}
	initialPrepared := PreparedFiles{Intent: initialIntent, Proposal: fileeffects.Proposal{Before: before, After: before}}
	initialInvocation := runtime.Invocation{ID: strings.Repeat("1", 64)}
	secondInitialInvocation := runtime.Invocation{ID: strings.Repeat("3", 64)}
	initial := Snapshot{
		Creation:  Creation{Execution: &ExecutionPolicy{ParallelImplementationVersion: 1}},
		Candidate: &before, FileOutcome: "CONFIRMED",
		FileIntent: &FileIntent{Prepared: initialPrepared},
		GraphWriterBatch: &GraphWriterBatchRecord{Members: []GraphWriterMember{
			{TaskID: "impl-initial", InvocationID: initialInvocation.ID},
			{TaskID: "impl-initial-2", InvocationID: secondInitialInvocation.ID},
		}, Prepared: initialPrepared},
		GraphWriterResults: map[string]GraphWriterRecord{
			"impl-initial":   {TaskID: "impl-initial", Writer: WriterRecord{Invocation: initialInvocation}},
			"impl-initial-2": {TaskID: "impl-initial-2", Writer: WriterRecord{Invocation: secondInitialInvocation}},
		},
		RepairCandidateID: beforeID,
	}
	if autonomousRepairApplied(initial) {
		t.Fatal("initial aggregate was misclassified as an applied serial repair")
	}
	if record, member, valid := graphWriterResultForTask(initial, "impl-initial", initialInvocation.ID); !member || !valid || record.Writer.Invocation.ID != initialInvocation.ID {
		t.Fatal("exact initial batch member was not recognized")
	}
	if _, member, _ := graphWriterResultForTask(initial, "repair-1", ""); member {
		t.Fatal("serial repair was incorrectly admitted as an initial batch member")
	}
	if _, member, valid := graphWriterResultForTask(initial, "impl-initial", strings.Repeat("4", 64)); !member || valid {
		t.Fatal("substituted initial-member invocation was not rejected fail-closed")
	}

	after := before
	after.FilesHash = strings.Repeat("e", 64)
	afterID, err := after.ID()
	if err != nil {
		t.Fatal(err)
	}
	repairInvocation := runtime.Invocation{ID: strings.Repeat("2", 64)}
	repairPrepared := PreparedFiles{Intent: effects.Intent{Kind: "filesystem"}, Proposal: fileeffects.Proposal{
		Before: before, After: after, Changes: []fileeffects.Change{{Path: "repair.go"}},
	}}
	task := engineeringplan.Task{ID: "repair-1", Kind: engineeringplan.Implementation, WritePaths: []string{"repair.go"}}
	repair := initial
	repair.Candidate = &after
	repair.FileIntent = &FileIntent{Prepared: repairPrepared}
	repair.WriterProposal = &WriterRecord{Invocation: repairInvocation, Prepared: repairPrepared}
	repair.Graph = &GraphState{Graph: engineeringplan.Graph{Tasks: []engineeringplan.Task{task}}}
	repair.GraphWriterResults = map[string]GraphWriterRecord{
		"impl-initial":   initial.GraphWriterResults["impl-initial"],
		"impl-initial-2": initial.GraphWriterResults["impl-initial-2"],
	}
	if graphWriterBatchEffectApplied(repair) || !autonomousFilesApplied(repair) || !autonomousRepairApplied(repair) {
		t.Fatal("confirmed singleton repair was not selected as the current file effect")
	}
	progress := GraphProgress{CandidateID: afterID, WriterInvocationID: repairInvocation.ID}
	if err := validateGraphEvidence(repair, task, progress); err != nil {
		t.Fatalf("repair implementation evidence did not use its exact singleton proposal: %v", err)
	}
	if err := requireWriterPathsInScope(task, repair); err != nil {
		t.Fatalf("repair scope check did not use its singleton proposal: %v", err)
	}
}
