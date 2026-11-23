package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestGraphAdmissionReservesExecutableBudgets(t *testing.T) {
	c := graphCreation(t, 1)
	c.Config.Exploration = &config.ExplorationPolicy{Version: 1, MaxRecords: 2, MaxContextRecords: 2}
	s := Snapshot{Creation: c, Plan: &runtime.Result{Output: mustGraphJSON(t, validGraphFixture())}}
	if _, err := parseAcceptedGraph(s); err == nil || !strings.Contains(err.Error(), "research/design record budget") {
		t.Fatal("read task budget was not enforced", err)
	}
	g := validGraphFixture()
	for len(g.Tasks) < 41 {
		g.Tasks = append(g.Tasks, graphTask(fmt.Sprintf("extra-%d", len(g.Tasks)), engineeringplan.Research))
	}
	c.Config.Exploration.MaxRecords = 64
	c.Config.Exploration.MaxContextRecords = 64
	c.Execution.MaxRepairs = 8
	s.Creation = c
	s.Plan.Output = mustGraphJSON(t, g)
	if _, err := parseAcceptedGraph(s); err == nil || !strings.Contains(err.Error(), "insufficient task capacity") {
		t.Fatal("configured repair capacity was not enforced", err)
	}
	c = graphCreation(t, 1)
	c.Config.PlannerContract = plannerContractGraphV3
	c.Config.Exploration = &config.ExplorationPolicy{Version: 1, MaxRecords: 3, MaxContextRecords: 3}
	c.Execution.RepairPlanningVersion = 1
	s = Snapshot{Creation: c, Plan: &runtime.Result{Output: mustGraphJSON(t, validGraphFixture())}}
	if _, err := parseAcceptedGraph(s); err == nil || !strings.Contains(err.Error(), "repair design") {
		t.Fatal("repair design record capacity was not reserved", err)
	}
	c.Config.Exploration.MaxRecords = 64
	c.Config.Exploration.MaxContextRecords = 64
	c.Execution.MaxRepairs = 8
	g = validGraphFixture()
	for len(g.Tasks) < 33 {
		g.Tasks = append(g.Tasks, graphTask(fmt.Sprintf("repair-extra-%d", len(g.Tasks)), engineeringplan.Research))
	}
	s.Creation = c
	s.Plan.Output = mustGraphJSON(t, g)
	if _, err := parseAcceptedGraph(s); err == nil || !strings.Contains(err.Error(), "insufficient task capacity") {
		t.Fatal("four-node repair design budget was not enforced", err)
	}
}

func graphWrittenNativeGateFixture(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	ctx := context.Background()
	path, s := graphAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	s = recordGraphFixture(t, path)
	for _, task := range s.Graph.Graph.Tasks {
		if task.Kind != engineeringplan.Research && task.Kind != engineeringplan.Design {
			continue
		}
		question, err := explorerQuestionForTask(s, task)
		if err != nil {
			t.Fatal(err)
		}
		record, err := RunExplorer(ctx, path, question)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recordGraphProgress(path, GraphProgress{Version: 1, PlanID: s.Graph.PlanID, Digest: s.Graph.Digest, TaskID: task.ID, AttemptID: "attempt-1", Outcome: "completed", ExplorerInvocationID: record.Invocation.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := AdmitTaskContext(ctx, path, "writer", c.Objective); err != nil {
		t.Fatal(err)
	}
	s = applyWriterOutput(t, path, "writer output\n")
	return path, s
}

func TestGraphAppliedWriterRecordsSynchronousVerification(t *testing.T) {
	ctx := context.Background()
	path, s := graphWrittenNativeGateFixture(t, graphCreation(t, 1))
	if _, _, err := autonomousGraphImplementing(ctx, path, s); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil || s.State != "REVIEWING" || s.Verification == nil {
		t.Fatal("synchronous verification did not reach review", s.State, err)
	}
	gate, _ := s.Graph.Graph.Task("verify")
	evidence := s.Graph.Evidence[gate.ID]
	if !gate.Completed || evidence.Outcome != "completed" || evidence.VerificationPlanID != s.Verification.PlanID {
		t.Fatal("native verification completed without durable graph evidence", gate, evidence)
	}
	// A repeated local progress observation is idempotent, not a second check.
	if err := recordGraphImplementationProgress(path); err != nil {
		t.Fatal("completed writer progress was not idempotent", err)
	}
}

func TestGraphFailedNativeGateAndRevisionCrashBoundariesResume(t *testing.T) {
	for _, boundary := range []string{"native-gate-recorded", "repair-revision-recorded"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			c := graphCreation(t, 1)
			c.Config.Verification = []config.Check{{Name: "failed-unit", Argv: []string{"git", "--no-such-flag-xyz"}, TimeoutSeconds: 10}}
			path, _ := graphWrittenNativeGateFixture(t, c)
			if err := recordGraphImplementationProgress(path); err != nil {
				t.Fatal(err)
			}
			s, err := Verify(ctx, path)
			if err != nil || s.State != "REPAIRING" {
				t.Fatal("native failure was not recorded", s.State, err)
			}
			if boundary == "repair-revision-recorded" {
				if err := recordGraphVerificationProgress(path); err != nil {
					t.Fatal(err)
				}
				s, err = Inspect(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := maybeEvolveGraphForRepair(path, s); err != nil {
					t.Fatal(err)
				}
				s, err = Inspect(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			resumed, err := autonomousGraphRepairing(ctx, path, s)
			if err != nil {
				t.Fatal("exact observed failed gate did not resume", err)
			}
			if resumed.RepairAttempts != 1 || resumed.Graph.Revision != 2 || countJournalKind(t, path, "autonomous.repair-started") != 1 || countJournalKind(t, path, "graph.revised") != 1 {
				t.Fatal("repair resume duplicated revision or slot", resumed.RepairAttempts, resumed.Graph.Revision)
			}
			if countJournalKind(t, path, "verification.planned") != 1 {
				t.Fatal("resume reran the failed native check")
			}
		})
	}
}

func graphAfterFailedVerification(t *testing.T) engineeringplan.Graph {
	t.Helper()
	g := validGraphFixture()
	for i := range g.Tasks {
		if g.Tasks[i].ID == "verify" {
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptFailed}}
		} else if g.Tasks[i].ID != "review" {
			g.Tasks[i].Completed = true
			g.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-1", Outcome: engineeringplan.AttemptCompleted}}
		}
	}
	return g
}

func TestGraphRepairRevisionResumesSameUnconsumedSlot(t *testing.T) {
	g := graphAfterFailedVerification(t)
	next, err := engineeringplan.RepairExtension(g, "verify", "verification-plan", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Graph: &GraphState{Graph: next, Evidence: map[string]GraphTaskEvidence{
		"verify": {TaskID: "verify", AttemptID: "attempt-1", Outcome: "failed", VerificationPlanID: "verification-plan"},
	}}}
	path := filepath.Join(t.TempDir(), "must-not-create.jsonl")
	if err := maybeEvolveGraphForRepair(path, s); err != nil {
		t.Fatalf("persisted repair revision could not resume its slot: %v", err)
	}
	if s.RepairAttempts != 0 || len(s.Graph.Graph.Tasks) != len(next.Tasks) {
		t.Fatal("resume consumed or duplicated a repair slot")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resume appended a second revision", err)
	}
	// A child's unrelated failure reference cannot authorize this resume.
	for i := range next.Tasks {
		if next.Tasks[i].ID == "impl-repair-1" {
			next.Tasks[i].ExpectedEvidence = []engineeringplan.Evidence{{Kind: "failure", Description: "unrelated-plan"}}
		}
	}
	s.Graph.Graph = next
	if err := maybeEvolveGraphForRepair(path, s); err == nil {
		t.Fatal("unrelated failure evidence admitted as existing repair")
	}
}

func TestGraphRepairSlotSurvivesPlannerIDCollision(t *testing.T) {
	g := graphAfterFailedVerification(t)
	g.Tasks[2].ID = "impl-repair-1"
	next, err := engineeringplan.RepairExtension(g, "verify", "verification-plan", 1)
	if err != nil {
		t.Fatal("planner task name prevented bounded repair", err)
	}
	child, ok := next.Task("impl-repair-1-1")
	if !ok || child.ParentID != "verify" {
		t.Fatal("repair did not allocate a distinct scoped child", child)
	}
	s := Snapshot{Graph: &GraphState{Graph: next, Evidence: map[string]GraphTaskEvidence{
		"verify": {AttemptID: "attempt-1", Outcome: "failed", VerificationPlanID: "verification-plan"},
	}}}
	if err := maybeEvolveGraphForRepair(filepath.Join(t.TempDir(), "absent.jsonl"), s); err != nil {
		t.Fatal("collision-safe persisted revision could not resume its slot", err)
	}
}

func TestGraphRepairSupersedesOnlyObsoleteNativeGates(t *testing.T) {
	g := graphAfterFailedVerification(t)
	next, err := engineeringplan.RepairExtension(g, "verify", "verification-plan", 1)
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{Graph: &GraphState{Graph: next, Evidence: map[string]GraphTaskEvidence{
		"verify": {AttemptID: "attempt-1", Outcome: "failed", VerificationPlanID: "verification-plan"},
	}}}
	for _, id := range []string{"verify", "review"} {
		task, _ := next.Task(id)
		if !graphTaskSuperseded(s, task) {
			t.Fatalf("obsolete gate %s blocks repaired candidate", id)
		}
	}
	for _, id := range []string{"impl", "impl-repair-1", "verify-repair-1", "review-repair-1"} {
		task, _ := next.Task(id)
		if graphTaskSuperseded(s, task) {
			t.Fatalf("mandatory task %s was superseded", id)
		}
	}
	delete(s.Graph.Evidence, "verify")
	review, _ := next.Task("review")
	if graphTaskSuperseded(s, review) {
		t.Fatal("review bypassed without recorded verification failure")
	}
}

func TestGraphChangesRequestedAdmitsRepairWriterContext(t *testing.T) {
	ctx := context.Background()
	c := graphCreation(t, 1)
	path, s := graphWrittenNativeGateFixture(t, c)
	var err error
	if s, _, err = autonomousGraphImplementing(ctx, path, s); err != nil {
		t.Fatal("initial implementation/native verification did not reach review", err)
	}
	s, err = Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "REVIEWING" || s.Verification == nil {
		t.Fatal("fixture lacks current native verification", s.State)
	}
	if err := maybeAdmitTaskContext(ctx, path, "reviewer", c.Objective); err != nil {
		t.Fatal("could not admit review context", err)
	}
	reviewInvocation, err := PrepareReviewInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	verdict := ReviewVerdict{
		CandidateID: candidateID, VerificationPlanID: s.Verification.PlanID,
		Decision: "changes_requested", Findings: []ReviewFinding{{Path: "file.txt", Message: "fixture requires repair"}},
	}
	output, err := canonical.Bytes(verdict)
	if err != nil {
		t.Fatal(err)
	}
	model := reviewInvocation.Profile.Model
	review := ReviewRecord{Invocation: reviewInvocation, Result: runtime.Result{
		Version: 1, InvocationID: reviewInvocation.ID, Requested: reviewInvocation.Profile,
		ObservedModel: &model, Output: string(output),
	}}
	s, err = RecordReview(path, review)
	if err != nil || s.State != "REPAIRING" {
		t.Fatal("changes-requested review did not enter repair", s.State, err)
	}
	s, err = autonomousGraphRepairing(ctx, path, s)
	if err != nil || s.RepairAttempts != 1 || s.Graph.Revision != 2 {
		t.Fatal("bounded repair was not durably admitted", s.RepairAttempts, err)
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		t.Fatal(err)
	}
	var repairReady bool
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation && task.ParentID == "review" {
			repairReady = true
		}
	}
	if !repairReady {
		t.Fatalf("repair implementation was not ready after its failed gate: %+v", ready)
	}
	// RunWriter admits the new-candidate writer context before preparing a
	// provider intent. Exercise that exact offline boundary without dispatching
	// a provider.
	if err := maybeAdmitTaskContext(ctx, path, writerTaskRole(s), c.Objective); err != nil {
		t.Fatalf("repair writer context admission failed: %v", err)
	}
	invocation, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatalf("repair writer invocation was not dispatchable: %v", err)
	}
	var input struct {
		CandidateID string            `json:"candidate_id"`
		TaskContext TaskContextRecord `json:"task_context"`
		Review      writerReview      `json:"review"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.CandidateID != candidateID || input.TaskContext.CandidateID != candidateID || input.TaskContext.Role != "writer" || input.Review.Decision != "changes_requested" || input.Review.CandidateID != candidateID {
		t.Fatalf("repair writer input lost current candidate/context/review binding: %+v", input)
	}
}
