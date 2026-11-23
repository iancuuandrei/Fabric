package control

import (
	"context"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func TestRepairDiagnosisObservedFailureAndReadOnlyIdentity(t *testing.T) {
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
	before, err := canonical.Hash("fixture.snapshot", s)
	if err != nil {
		t.Fatal(err)
	}
	r, err := DiagnoseRepair(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || len(r.Specifications) != 0 || r.Findings[0].Source != "native_check" || r.Findings[0].CandidateBinding != "matching_recorded_candidate" || r.Authority != "advisory_only" {
		t.Fatalf("unexpected diagnosis: %+v", r)
	}
	after, _ := canonical.Hash("fixture.snapshot", s)
	if after != before {
		t.Fatal("diagnosis mutated snapshot")
	}
	again, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ = canonical.Hash("fixture.snapshot", again)
	if after != before {
		t.Fatal("diagnosis changed journal")
	}
}

func diagnosisFixture(t *testing.T) Snapshot {
	t.Helper()
	s, graph := repairDesignFixture(t, []string{"src"}, []string{"src/bool.go"}, false)
	s.Graph.Graph = graph
	s.State = "REPAIRING"
	candidate, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	s.Verification = &VerificationState{PlanID: strings.Repeat("e", 64), Plan: verification.Plan{CandidateID: candidate}, Observations: []VerificationObservation{{Start: VerificationStart{Index: 0}, Result: verification.Result{CandidateID: candidate, Status: "FAIL", InvocationID: strings.Repeat("9", 64), Stderr: verification.Stream{Excerpt: "src\\bool.go:11:2: \"strings\" imported and not used"}}}}}
	return s
}

func TestRepairDiagnosisExactGateScopeAndStaleCandidate(t *testing.T) {
	s := diagnosisFixture(t)
	r, err := DiagnoseRepair(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Findings) != 1 || r.Findings[0].Path != "src/bool.go" || r.Findings[0].Line != 11 || r.Findings[0].Column != 2 || r.Findings[0].LocationStatus != "reported_unvalidated" {
		t.Fatalf("compiler localization lost: %+v", r)
	}
	if len(r.Specifications) != 1 || len(r.Specifications[0].AllowedWritePaths) != 1 || r.Specifications[0].AllowedWritePaths[0] != "src/bool.go" || r.Specifications[0].SuggestedStrategy != "localization_required" {
		t.Fatalf("scope expanded: %+v", r)
	}
	s.Verification.PlanID = strings.Repeat("7", 64)
	unrelated, err := DiagnoseRepair(s)
	if err != nil || len(unrelated.Specifications) != 0 {
		t.Fatalf("unrelated gate produced repair spec: %+v %v", unrelated, err)
	}
	s.Verification.PlanID = strings.Repeat("e", 64)
	s.Verification.Observations[0].Result.CandidateID = strings.Repeat("8", 64)
	stale, err := DiagnoseRepair(s)
	if err != nil || stale.Findings[0].CandidateBinding != "stale_candidate" || len(stale.Specifications) != 0 {
		t.Fatalf("stale finding authorized repair: %+v %v", stale, err)
	}
}

func TestRepairDiagnosisUncertaintyAndHiddenEvidence(t *testing.T) {
	s := diagnosisFixture(t)
	s.Verification.Pending = true
	s.Verification.Observations = nil
	r, err := DiagnoseRepair(s)
	if err != nil || !r.VerificationPending || len(r.Findings) != 0 || len(r.Specifications) != 0 {
		t.Fatalf("pending launch became semantic failure: %+v %v", r, err)
	}
	s = diagnosisFixture(t)
	first, err := DiagnoseRepair(s)
	if err != nil {
		t.Fatal(err)
	}
	other := s
	copyCandidate := *s.Candidate
	copyCandidate.Head = strings.Repeat("4", 40)
	other.Candidate = &copyCandidate
	stale, err := DiagnoseRepair(other)
	if err != nil || stale.Findings[0].ID != first.Findings[0].ID || stale.Findings[0].CandidateBinding != "stale_candidate" {
		t.Fatal("candidate drift changed the original finding identity", err)
	}
	s.Verification.Observations[0].Result.Error = "hidden diagnostic tail"
	second, err := DiagnoseRepair(s)
	if err != nil || first.Findings[0].ID == second.Findings[0].ID {
		t.Fatal("full evidence omitted from finding identity", err)
	}
	s.Verification.Observations[0].Result.Status = "NOT_RUN"
	unavailable, err := DiagnoseRepair(s)
	if err != nil || unavailable.Findings[0].Category != "check_unavailable" || unavailable.Findings[0].Path != "" {
		t.Fatal("unavailable dependency became source defect", err)
	}
}

func TestRepairDiagnosisReviewTruthAndBoundedMessages(t *testing.T) {
	s := diagnosisFixture(t)
	s.Verification = nil
	id, _ := s.Candidate.ID()
	verdict := ReviewVerdict{CandidateID: id, VerificationPlanID: "verify", Decision: "changes_requested", Findings: []ReviewFinding{{Path: "src/bool.go", Message: strings.Repeat("ș", 300)}}}
	raw, err := canonical.Bytes(verdict)
	if err != nil {
		t.Fatal(err)
	}
	s.Review = &ReviewRecord{Invocation: runtime.Invocation{ID: "review"}, Result: runtime.Result{Output: string(raw)}}
	r, err := DiagnoseRepair(s)
	if err != nil || len(r.Findings) != 1 || r.Findings[0].Source != "reviewer" || r.Findings[0].Category != "review_concern" || !r.Findings[0].MessageShortened || len(r.Findings[0].Message) > 384 {
		t.Fatalf("review truth lost: %+v %v", r, err)
	}
	verdict.Findings[0].Message += "hidden"
	raw, _ = canonical.Bytes(verdict)
	s.Review.Result.Output = string(raw)
	next, err := DiagnoseRepair(s)
	if err != nil || next.Findings[0].ID == r.Findings[0].ID {
		t.Fatal("hidden review tail lost identity", err)
	}
}

func TestRepairDiagnosticLocationsRejectAliasesAndAmbiguity(t *testing.T) {
	spaced := goDiagnosticLocations(" file.go:2:3: defect\r")
	if len(spaced) != 1 || spaced[0].path != " file.go" {
		t.Fatal("leading filename whitespace was relocated", spaced)
	}
	for _, text := range []string{"../escape.go:1:1: defect", "C:\\project\\file.go:1:1: defect", "/root/file.go:1:1: defect", ".git/file.go:0:1: defect", "file.go:1:0: defect", "file.go:9999999999999999999999999:1: defect"} {
		if got := goDiagnosticLocations(text); len(got) != 0 {
			t.Fatalf("unsafe localization %q: %+v", text, got)
		}
	}
	s := diagnosisFixture(t)
	s.Verification.Observations[0].Result.Stdout.Excerpt = "different.go:3:4: second error"
	r, err := DiagnoseRepair(s)
	if err != nil || r.Findings[0].LocationStatus != "ambiguous" || r.Findings[0].Path != "" {
		t.Fatal("ambiguous localization guessed", err)
	}
}
