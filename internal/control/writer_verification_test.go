package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

func TestWriterReceivesExecutedVerificationFailure(t *testing.T) {
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "high", Role: "writer"}
	c.Config.Verification = []config.Check{{Name: "intentional-failure", Argv: []string{"git", "--engorch-invalid-option"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	before, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "REPAIRING" || s.Verification == nil || len(s.Verification.Observations) != 1 {
		t.Fatal("fixture did not enter repair")
	}
	i, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if i.ID == before.ID {
		t.Fatal("verification failure not bound into invocation")
	}
	var decoded struct {
		Verification writerVerification `json:"verification"`
	}
	if err := json.Unmarshal([]byte(i.Input), &decoded); err != nil {
		t.Fatal(err)
	}
	v := decoded.Verification
	if v.PlanID != s.Verification.PlanID || v.CandidateID != s.Verification.Plan.CandidateID || len(v.RequiredChecks) != 1 || v.RequiredChecks[0] != "intentional-failure" || len(v.Observations) != 1 || v.Observations[0].Result.Status != "FAIL" {
		t.Fatal("failure context mismatch", v)
	}
	// Pure projection checks: truncation must not collapse distinct evidence IDs.
	s.Verification.Observations[0].Result.Stderr.Excerpt = strings.Repeat("ș", 300)
	x, err := writerVerificationContext(s)
	if err != nil {
		t.Fatal(err)
	}
	if !x.Observations[0].ExcerptShortened || !utf8.ValidString(x.Observations[0].Result.Stderr.Excerpt) || len(x.Observations[0].Result.Stderr.Excerpt) > 384 {
		t.Fatal("diagnostic shortening invalid")
	}
	s.Verification.Observations[0].Result.Stderr.Excerpt += "different tail"
	y, err := writerVerificationContext(s)
	if err != nil {
		t.Fatal(err)
	}
	if x.EvidenceHash == y.EvidenceHash || x.Observations[0].Result.Stderr.Excerpt != y.Observations[0].Result.Stderr.Excerpt {
		t.Fatal("hidden diagnostic tail lost its identity")
	}
}
