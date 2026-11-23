package control

import (
	"context"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func repairClosureFixture(t *testing.T) (Snapshot, []journal.Event, VerificationState) {
	t.Helper()
	s := acceptedCheckpointFixture(t)
	oldInvocation := s.Verification.Plan.Invocations[0]
	oldInvocation.CandidateID = strings.Repeat("9", 64)
	oldID, err := oldInvocation.ID()
	if err != nil {
		t.Fatal(err)
	}
	oldPlan := s.Verification.Plan
	oldPlan.Nonce, oldPlan.CandidateID = "failed-original", oldInvocation.CandidateID
	oldPlan.Invocations = []verification.Invocation{oldInvocation}
	oldPlanID, err := oldPlan.ID()
	if err != nil {
		t.Fatal(err)
	}
	oldObservation := s.Verification.Observations[0]
	oldObservation.Start = VerificationStart{PlanID: oldPlanID, Index: 0}
	oldObservation.Result.CandidateID, oldObservation.Result.InvocationID, oldObservation.Result.Status = oldInvocation.CandidateID, oldID, "FAIL"
	oldObservation.Result.Stderr.Excerpt = "src/bool.go:2:5: compile error"
	old := VerificationState{PlanID: oldPlanID, Plan: oldPlan, Observations: []VerificationObservation{oldObservation}}
	events := []journal.Event{}
	add := func(kind string, payload any) {
		raw, err := canonical.Bytes(payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, journal.Event{Sequence: len(events) + 1, Kind: kind, Payload: raw, Hash: strings.Repeat("f", 64)})
	}
	add("verification.planned", oldPlan)
	add("verification.started", oldObservation.Start)
	add("verification.observed", oldObservation)
	add("verification.planned", s.Verification.Plan)
	add("verification.started", VerificationStart{PlanID: s.Verification.PlanID, Index: 0})
	add("verification.observed", s.Verification.Observations[0])
	return s, events, old
}

func TestRepairClosureRequiresExactNativeOracleAndFinalAcceptedCandidate(t *testing.T) {
	s, events, old := repairClosureFixture(t)
	r, err := repairClosureFromValidatedEvents(s, events)
	if err != nil || !r.Accepted || len(r.Findings) != 1 || r.Findings[0].Status != "native_oracle_passed" || r.Findings[0].ClosureCandidateID != r.CandidateID || r.Findings[0].ClosureInvocationID != s.Verification.Observations[0].Result.InvocationID {
		t.Fatalf("matching exact oracle not retained: %+v %v", r, err)
	}
	original := RepairDiagnosis{CandidateID: r.CandidateID, Findings: []RepairFinding{}}
	if err := original.addNativeFindings(&old); err != nil {
		t.Fatal(err)
	}
	if r.Findings[0].Finding.ID != original.Findings[0].ID || r.Findings[0].Finding.EvidenceID != original.Findings[0].EvidenceID {
		t.Fatal("historical finding identity changed")
	}
	for _, change := range []func(*Snapshot){
		func(s *Snapshot) { s.State = "REPAIRING" },
		func(s *Snapshot) { s.Review = nil },
		func(s *Snapshot) { s.Verification.Pending = true },
		func(s *Snapshot) { s.Verification.Observations[0].Result.Started = false },
		func(s *Snapshot) { s.Verification.Observations[0].After.Error = "source observation unavailable" },
	} {
		s, events, _ := repairClosureFixture(t)
		change(&s)
		r, err := repairClosureFromValidatedEvents(s, events)
		if err != nil || r.Findings[0].Status != "open" {
			t.Fatal("incomplete acceptance closed finding", r, err)
		}
	}
}

func TestRepairNativeOracleBindsEveryInputExceptCandidate(t *testing.T) {
	s, _, _ := repairClosureFixture(t)
	original := s.Verification.Plan.Invocations[0]
	want, err := repairNativeOracleID(original)
	if err != nil {
		t.Fatal(err)
	}
	same := original
	same.CandidateID = strings.Repeat("0", 64)
	got, _ := repairNativeOracleID(same)
	if want != got {
		t.Fatal("candidate prevents recheck equivalence")
	}
	for _, change := range []func(*verification.Invocation){
		func(i *verification.Invocation) { i.Check.Name += "different" },
		func(i *verification.Invocation) {
			i.Check.Argv = append(append([]string(nil), i.Check.Argv...), "--different")
		},
		func(i *verification.Invocation) { i.Check.TimeoutSeconds++ },
		func(i *verification.Invocation) { i.Directory += "different" },
		func(i *verification.Invocation) { i.EnvironmentPolicy += "different" },
		func(i *verification.Invocation) { i.EnvironmentHash = strings.Repeat("0", 64) },
		func(i *verification.Invocation) { i.Environment = map[string]string{"PATH": "different"} },
		func(i *verification.Invocation) {
			copy := *i.Executable
			copy.Hash = strings.Repeat("0", 64)
			i.Executable = &copy
		},
		func(i *verification.Invocation) {
			copy := *i.Executable
			copy.Path += "different"
			i.Executable = &copy
		},
	} {
		changed := original
		change(&changed)
		got, err := repairNativeOracleID(changed)
		if err != nil || got == want {
			t.Fatal("oracle input substitution ignored", err)
		}
	}
}

func TestRepairClosureUnavailableAndSameAttemptRemainUnclosed(t *testing.T) {
	s, events, _ := repairClosureFixture(t)
	history, latest, err := repairFindingHistory(events, strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	old := history.findings[0]
	report := RepairClosureReport{Accepted: true}
	old.sequence = latest
	witnesses, err := repairNativeWitnesses(s, true)
	if err != nil {
		t.Fatal(err)
	}
	c := nativeFindingClosure(s, report, old, latest, witnesses)
	if c.Status != "open" {
		t.Fatal("nonlater attempt closed", err)
	}
	old.sequence = 1
	old.finding.Category = "check_unavailable"
	c = nativeFindingClosure(s, report, old, latest, witnesses)
	if c.Status != "unavailable_not_semantic" {
		t.Fatal("unavailable became semantic success", err)
	}
	old.finding.Source = "reviewer"
	c = nativeFindingClosure(s, report, old, latest, witnesses)
	if c.Status != "recheck_required" {
		t.Fatal("generic review closed concern", err)
	}
}

func TestRepairClosureReadValidatesRunRepositoryAndPreservesJournal(t *testing.T) {
	c := creation(t)
	c.Config.Verification = []config.Check{{Name: "failure", Argv: []string{"git", "--engorch-invalid-option"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := canonical.Hash("fixture.snapshot", s)
	r, err := ReadRepairClosure(path, s.Creation.Repository.Root, s.RunID)
	if err != nil || len(r.Findings) != 1 || r.Findings[0].Status != "open" {
		t.Fatal("actual journal failure lost", r, err)
	}
	if _, err := ReadRepairClosure(path, t.TempDir(), s.RunID); err == nil {
		t.Fatal("foreign repository accepted")
	}
	if _, err := ReadRepairClosure(path, s.Creation.Repository.Root, "foreign"); err == nil {
		t.Fatal("foreign run accepted")
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := canonical.Hash("fixture.snapshot", after)
	if before != actual {
		t.Fatal("closure appended or modified journal")
	}
	// Fixture-only injection retains chain integrity while breaking semantics.
	if _, err := journal.Append(path, "unknown-semantic-event", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRepairClosure(path, s.Creation.Repository.Root, s.RunID); err == nil {
		t.Fatal("invalid semantic history projected")
	}
}

func TestRepairHistoryOmissionPreservesChangedEvidence(t *testing.T) {
	makeHistory := func(first string) repairHistory {
		h := repairHistory{}
		for i := 0; i < 129; i++ {
			id := "retained"
			if i == 0 {
				id = first
			}
			if err := h.retain([]historicalRepairFinding{{finding: RepairFinding{ID: id}, sequence: i}}); err != nil {
				t.Fatal(err)
			}
		}
		return h
	}
	a, b := makeHistory("first"), makeHistory("changed")
	if len(a.findings) != 128 || a.omitted != 1 || len(a.omissionHash) != 64 || a.omissionHash == b.omissionHash {
		t.Fatal("history omission lost bound/identity")
	}
}

func TestRepairClosureRealNativeFailureRepairAndFreshReview(t *testing.T) {
	ctx := context.Background()
	c := creation(t)
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.Verification = []config.Check{{Name: "required-content", Argv: []string{"git", "grep", "--quiet", "ready", "--", "file.txt"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	failed, err := Verify(ctx, path)
	if err != nil || failed.State != "REPAIRING" || failed.Verification.Observations[0].Result.Status != "FAIL" {
		t.Fatal("real check did not fail", err)
	}
	original, err := DiagnoseRepair(failed)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareFiles(ctx, path, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("ready\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(ctx, path, prepared, authorize(t, prepared)); err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(ctx, path)
	if err != nil || verified.State != "REVIEWING" {
		t.Fatal("fresh check failed", err)
	}
	pending, err := ReadRepairClosure(path, verified.Creation.Repository.Root, verified.RunID)
	if err != nil || pending.Findings[0].Status != "open" {
		t.Fatal("verification bypassed review", pending, err)
	}
	invocation, err := PrepareReviewInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := verified.Candidate.ID()
	output, err := canonical.Bytes(ReviewVerdict{CandidateID: id, VerificationPlanID: verified.Verification.PlanID, Decision: "approve", Findings: []ReviewFinding{}})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := RecordReview(path, ReviewRecord{Invocation: invocation, Result: repairDesignFakeResult(t, invocation, string(output))})
	if err != nil || ready.State != "READY" {
		t.Fatal("fixture review did not accept", err)
	}
	report, err := ReadRepairClosure(path, ready.Creation.Repository.Root, ready.RunID)
	if err != nil || !report.Accepted || len(report.Findings) != 1 || report.Findings[0].Status != "native_oracle_passed" || report.Findings[0].Finding.ID != original.Findings[0].ID {
		t.Fatalf("actual native repaired closure lost: %+v %v", report, err)
	}
}
