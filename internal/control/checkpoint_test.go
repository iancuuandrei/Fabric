package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/verification"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestReadCheckpointBindsOneJournalPrefixAndRejectsTampering(t *testing.T) {
	c := creation(t)
	runID, err := canonical.Hash("harness.run.v1", c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	first, err := ReadCheckpoint(path)
	if err != nil || first.RunID != runID || first.JournalHead == "" || first.SourceCommit != c.Repository.Commit {
		t.Fatalf("checkpoint lost run/source binding: %+v err=%v", first, err)
	}
	if first.AcceptedCheckpoint || first.CandidateID != "" || first.State != "OBJECTIVE" {
		t.Fatalf("partial progress became accepted: %+v", first)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	second, err := ReadCheckpoint(path)
	if err != nil || second.JournalHead == first.JournalHead || first.State != "OBJECTIVE" {
		t.Fatalf("new head mutated or failed to advance checkpoint: first=%+v second=%+v err=%v", first, second, err)
	}
	if second.ActiveIntentCount != 1 || second.UncertainIntentCount != 0 {
		t.Fatalf("active planning state was misclassified: %+v", second)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCheckpoint(path); err == nil {
		t.Fatal("tampered journal produced a checkpoint")
	}
}

func acceptedCheckpointFixture(t *testing.T) Snapshot {
	t.Helper()
	c := creation(t)
	runID := strings.Repeat("a", 64)
	candidate := worktree.Candidate{Version: 1, WorktreeID: strings.Repeat("b", 64), Head: strings.Repeat("c", 40), IndexHash: strings.Repeat("d", 64), FilesHash: strings.Repeat("e", 64)}
	candidateID, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := verification.Prepare(candidateID, c.Repository.Root, c.Config.Verification[0])
	if err != nil {
		t.Fatal(err)
	}
	invocationID, err := invocation.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan := verification.Plan{Version: 1, RunID: runID, Nonce: "check-1", CandidateID: candidateID, Invocations: []verification.Invocation{invocation}}
	planID, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	observedCandidate := candidate
	result := verification.Result{Version: 1, InvocationID: invocationID, CandidateID: candidateID, Status: "PASS", Started: true}
	reviewOutput := `{"candidate_id":"` + candidateID + `","verification_plan_id":"` + planID + `","decision":"approve","findings":[]}`
	return Snapshot{
		RunID: runID, State: "READY", Creation: c, WorkspaceOutcome: "CONFIRMED", Candidate: &candidate,
		Verification: &VerificationState{PlanID: planID, Plan: plan, Observations: []VerificationObservation{{Result: result, After: FileObservation{Candidate: &observedCandidate}}}},
		Review:       &ReviewRecord{Result: runtime.Result{Output: reviewOutput}},
	}
}

func TestCheckpointAcceptanceRequiresEveryCandidateBoundGate(t *testing.T) {
	s := acceptedCheckpointFixture(t)
	base, err := checkpointFromSnapshot(s, strings.Repeat("f", 64))
	if err != nil || !base.AcceptedCheckpoint {
		t.Fatalf("complete bound gates not recognized: %+v err=%v", base, err)
	}
	cases := map[string]func(*Snapshot){
		"not ready":                   func(s *Snapshot) { s.State = "REPAIRING" },
		"missing review":              func(s *Snapshot) { s.Review = nil },
		"pending verification":        func(s *Snapshot) { s.Verification.Pending = true },
		"failed native check":         func(s *Snapshot) { s.Verification.Observations[0].Result.Status = "FAIL" },
		"wrong candidate observation": func(s *Snapshot) { s.Verification.Observations[0].Result.CandidateID = strings.Repeat("9", 64) },
		"stale after observation":     func(s *Snapshot) { s.Verification.Observations[0].After.Candidate.FilesHash = strings.Repeat("8", 64) },
		"wrong review candidate": func(s *Snapshot) {
			candidateID, _ := s.Candidate.ID()
			s.Review.Result.Output = strings.Replace(s.Review.Result.Output, candidateID, strings.Repeat("7", 64), 1)
		},
		"missing native checks": func(s *Snapshot) { s.Verification.Plan.Invocations = nil; s.Verification.Observations = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := acceptedCheckpointFixture(t)
			mutate(&fixture)
			checkpoint, err := checkpointFromSnapshot(fixture, strings.Repeat("f", 64))
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.AcceptedCheckpoint {
				t.Fatalf("invalid gates were accepted: %+v", checkpoint)
			}
		})
	}
}

func TestCheckpointListsOnlyCompletedGraphEvidenceAndHidesPayload(t *testing.T) {
	s := acceptedCheckpointFixture(t)
	s.Creation.Objective = "PRIVATE PROMPT MUST NOT LEAK"
	s.Graph = &GraphState{
		PlanID: "plan", Digest: strings.Repeat("1", 64), Revision: 1,
		Graph: engineeringplan.Graph{Tasks: []engineeringplan.Task{
			{ID: "done", Completed: true, Attempts: []engineeringplan.Attempt{{ID: "a1", Outcome: engineeringplan.AttemptCompleted}}},
			{ID: "claim-only", Completed: true, Attempts: []engineeringplan.Attempt{{ID: "a2", Outcome: engineeringplan.AttemptUnknown}}},
			{ID: "unfinished"},
		}},
		Evidence: map[string]GraphTaskEvidence{"done": {TaskID: "done", AttemptID: "a1", Outcome: "completed"}, "claim-only": {TaskID: "claim-only", AttemptID: "a2", Outcome: "completed"}},
	}
	checkpoint, err := checkpointFromSnapshot(s, strings.Repeat("f", 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.CompletedTaskIDs) != 1 || checkpoint.CompletedTaskIDs[0] != "done" {
		t.Fatalf("unverified graph completion leaked: %+v", checkpoint.CompletedTaskIDs)
	}
	encoded, err := canonical.Bytes(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE PROMPT") || strings.Contains(string(encoded), "approve\",\"findings") {
		t.Fatalf("checkpoint exposed raw source/prompt/review payload: %s", encoded)
	}
}
