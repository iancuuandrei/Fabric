package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func recheckFixture(t *testing.T) (Snapshot, runtime.Invocation, ReviewVerdict) {
	t.Helper()
	s, _, _ := recordedRepairFixture(t)
	s.Creation.Execution.ReviewRecheckVersion = 1
	finding := RepairFinding{ID: strings.Repeat("a", 64), CandidateID: "old", Path: "src/bool.go", Message: "wrapper lost methods"}
	s.ReviewRecheckHistory = &ReviewRecheckHistory{Findings: []RepairFinding{finding}}
	p, err := reviewRechecksForSnapshot(s)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(struct {
		Rechecks *reviewRecheckPrompt `json:"repair_rechecks"`
	}{p})
	if err != nil {
		t.Fatal(err)
	}
	return s, runtime.Invocation{Input: string(raw)}, ReviewVerdict{Decision: "approve", Findings: []ReviewFinding{}, Rechecks: []ReviewFindingRecheck{{FindingID: finding.ID, Decision: "closed", Rationale: "Current candidate retains the required methods."}}}
}

func TestReviewRechecksRequireExactAnswersAndActionableUnresolvedFeedback(t *testing.T) {
	s, invocation, verdict := recheckFixture(t)
	if err := validateReviewRechecks(s, invocation, verdict); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ReviewVerdict){
		func(v *ReviewVerdict) { v.Rechecks = nil },
		func(v *ReviewVerdict) { v.Rechecks[0].FindingID = strings.Repeat("b", 64) },
		func(v *ReviewVerdict) { v.Rechecks = append(v.Rechecks, v.Rechecks[0]) },
		func(v *ReviewVerdict) { v.Rechecks[0].Decision = "unresolved" },
		func(v *ReviewVerdict) { v.Rechecks[0].Rationale = " " },
		func(v *ReviewVerdict) { v.Rechecks[0].Rationale = strings.Repeat("x", 513) },
		func(v *ReviewVerdict) { v.Rechecks[0].Rationale = string([]byte{0xff}) },
	} {
		s, invocation, verdict := recheckFixture(t)
		change(&verdict)
		if validateReviewRechecks(s, invocation, verdict) == nil {
			t.Fatal("invalid recheck admitted", verdict)
		}
	}
	verdict.Decision = "changes_requested"
	verdict.Rechecks[0].Decision = "unresolved"
	verdict.Rechecks[0].Rationale = "Current wrapper still omits the original methods."
	verdict.Findings = []ReviewFinding{{Path: "other.go", Message: verdict.Rechecks[0].Rationale}}
	if validateReviewRechecks(s, invocation, verdict) == nil {
		t.Fatal("unresolved concern lost its exact repair path")
	}
	verdict.Findings[0].Path = "src/bool.go"
	if err := validateReviewRechecks(s, invocation, verdict); err != nil {
		t.Fatal("actionable unresolved recheck rejected", err)
	}
}

func TestReviewRechecksPreserveLegacyAndBoundHistoryProjection(t *testing.T) {
	s, invocation, verdict := recheckFixture(t)
	s.Creation.Execution.ReviewRecheckVersion = 0
	if validateReviewRechecks(s, invocation, verdict) == nil {
		t.Fatal("legacy policy accepted rechecks")
	}
	verdict.Rechecks = nil
	if err := validateReviewRechecks(s, runtime.Invocation{Input: "{}"}, verdict); err != nil {
		t.Fatal(err)
	}
	p, err := reviewRechecksForSnapshot(s)
	if err != nil || p != nil {
		t.Fatal("legacy policy gained context", err)
	}
	s, _, _ = recheckFixture(t)
	first := s.ReviewRecheckHistory.Findings[0]
	for i := 0; i < 128; i++ {
		f := first
		f.Message = strings.Repeat("x", 384)
		f.EvidenceID = strings.Repeat("e", 64)
		f.GateID = strings.Repeat("e", 64)
		f.ClosureOracleID = strings.Repeat("e", 64)
		s.ReviewRecheckHistory.Findings = append(s.ReviewRecheckHistory.Findings, f)
	}
	if err := boundReviewRecheckHistory(s.ReviewRecheckHistory); err != nil {
		t.Fatal(err)
	}
	if len(s.ReviewRecheckHistory.Findings) != 128 || s.ReviewRecheckHistory.OmittedFindings != 1 || len(s.ReviewRecheckHistory.OmittedEvidenceHash) != 64 {
		t.Fatal("unbounded or unidentified history")
	}
	p, err = reviewRechecksForSnapshot(s)
	if err != nil || p != nil {
		t.Fatal("oversized optional context did not degrade", err)
	}
	// Below the count limit, long but bounded paths still exceed the byte budget.
	s.ReviewRecheckHistory.Findings = s.ReviewRecheckHistory.Findings[:64]
	for i := range s.ReviewRecheckHistory.Findings {
		s.ReviewRecheckHistory.Findings[i].Path = strings.Repeat("p", 1000) + ".go"
	}
	p, err = reviewRechecksForSnapshot(s)
	if err != nil || p != nil {
		t.Fatal("byte-bound projection did not degrade", err)
	}
	raw, err := canonical.Bytes(s.Creation.Execution)
	if err != nil || !strings.Contains(string(raw), "review_recheck_version") {
		t.Fatal("policy lost its immutable identity", err)
	}
}

func TestReviewRechecksHistoryPreservesOriginalIDsAndHiddenEvidence(t *testing.T) {
	s, _, _ := recheckFixture(t)
	s.ReviewRecheckHistory = nil
	makeRecord := func(tail string) *ReviewRecord {
		output, err := canonical.Bytes(ReviewVerdict{CandidateID: strings.Repeat("d", 64), VerificationPlanID: strings.Repeat("e", 64), Decision: "changes_requested", Findings: []ReviewFinding{{Path: "src/bool.go", Message: strings.Repeat("x", 384) + tail}}})
		if err != nil {
			t.Fatal(err)
		}
		return &ReviewRecord{Invocation: runtime.Invocation{ID: "review"}, Result: runtime.Result{Output: string(output)}}
	}
	s.Review = makeRecord("first hidden tail")
	r := RepairDiagnosis{Findings: []RepairFinding{}}
	if err := r.addReviewFindings(s); err != nil {
		t.Fatal(err)
	}
	if err := recordReviewRecheckHistory(&s); err != nil {
		t.Fatal(err)
	}
	first := s.ReviewRecheckHistory.Findings[0]
	if first.ID != r.Findings[0].ID || !first.MessageShortened || len(first.Message) != 384 {
		t.Fatal("original review evidence changed")
	}
	s.Review = makeRecord("different hidden tail")
	if err := recordReviewRecheckHistory(&s); err != nil {
		t.Fatal(err)
	}
	if len(s.ReviewRecheckHistory.Findings) != 2 || s.ReviewRecheckHistory.Findings[0].ID != first.ID || s.ReviewRecheckHistory.Findings[1].ID == first.ID {
		t.Fatal("history collapsed hidden producer evidence")
	}
	s.Creation.Execution.ReviewRecheckVersion = 2
	if s.Creation.Execution.Validate() == nil {
		t.Fatal("unsupported recheck policy accepted")
	}
}
