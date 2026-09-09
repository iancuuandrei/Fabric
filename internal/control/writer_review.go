package control

import "harness.local/engorch/internal/canonical"

type writerReview struct {
	EvidenceHash       string          `json:"evidence_hash"`
	InvocationID       string          `json:"invocation_id"`
	CandidateID        string          `json:"candidate_id"`
	VerificationPlanID string          `json:"verification_plan_id"`
	Decision           string          `json:"decision"`
	Findings           []ReviewFinding `json:"findings"`
	Shortened          bool            `json:"shortened"`
}

func writerReviewContext(s Snapshot) (*writerReview, error) {
	if s.Review == nil {
		return nil, nil
	}
	hash, err := canonical.Hash("harness.writer-review-context.v1", s.Review)
	if err != nil {
		return nil, err
	}
	var verdict ReviewVerdict
	if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err != nil {
		return nil, err
	}
	context := &writerReview{EvidenceHash: hash, InvocationID: s.Review.Invocation.ID, CandidateID: verdict.CandidateID, VerificationPlanID: verdict.VerificationPlanID, Decision: verdict.Decision, Findings: []ReviewFinding{}}
	for _, finding := range verdict.Findings {
		message := diagnosticPrefix(finding.Message)
		context.Shortened = context.Shortened || message != finding.Message
		context.Findings = append(context.Findings, ReviewFinding{finding.Path, message})
	}
	return context, nil
}
