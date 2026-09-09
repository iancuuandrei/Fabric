package control

import (
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/verification"
)

type writerCheck struct {
	Index            int                 `json:"index"`
	Result           verification.Result `json:"result"`
	ExcerptShortened bool                `json:"excerpt_shortened"`
}

type writerVerification struct {
	EvidenceHash    string        `json:"evidence_hash"`
	PlanID          string        `json:"plan_id"`
	CandidateID     string        `json:"candidate_id"`
	Pending         bool          `json:"pending"`
	ClosureRecorded bool          `json:"closure_recorded"`
	RequiredChecks  []string      `json:"required_checks"`
	Observations    []writerCheck `json:"observations"`
}

func diagnosticPrefix(text string) string {
	if len(text) <= 384 {
		return text
	}
	end := 384
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func writerVerificationContext(s Snapshot) (*writerVerification, error) {
	if s.Verification == nil {
		return nil, nil
	}
	v := s.Verification
	hash, err := canonical.Hash("harness.writer-verification-context.v1", v)
	if err != nil {
		return nil, err
	}
	context := &writerVerification{EvidenceHash: hash, PlanID: v.PlanID, CandidateID: v.Plan.CandidateID, Pending: v.Pending, ClosureRecorded: v.Closure != nil, RequiredChecks: []string{}, Observations: []writerCheck{}}
	for _, check := range s.Creation.Config.Verification {
		context.RequiredChecks = append(context.RequiredChecks, check.Name)
	}
	for _, observed := range v.Observations {
		r := observed.Result
		stdout, stderr, diagnostic := diagnosticPrefix(r.Stdout.Excerpt), diagnosticPrefix(r.Stderr.Excerpt), diagnosticPrefix(r.Error)
		shortened := stdout != r.Stdout.Excerpt || stderr != r.Stderr.Excerpt || diagnostic != r.Error
		r.Stdout.Excerpt, r.Stderr.Excerpt, r.Error = stdout, stderr, diagnostic
		context.Observations = append(context.Observations, writerCheck{observed.Start.Index, r, shortened})
	}
	return context, nil
}
