package control

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// ReviewFindingRecheck is explicit reviewer judgment on an original concern.
// It is not a native test receipt or external-effect authority.
type ReviewFindingRecheck struct {
	FindingID string `json:"finding_id"`
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
}

// ReviewRecheckHistory retains bounded original concerns derived from admitted
// reviews. Omitted concern identities remain bound in a deterministic hash chain.
type ReviewRecheckHistory struct {
	Findings            []RepairFinding `json:"findings"`
	OmittedFindings     int             `json:"omitted_findings"`
	OmittedEvidenceHash string          `json:"omitted_evidence_hash,omitempty"`
}

type reviewRecheckPrompt struct {
	Version             int             `json:"version"`
	Findings            []RepairFinding `json:"findings"`
	OmittedFindings     int             `json:"omitted_findings"`
	OmittedEvidenceHash string          `json:"omitted_evidence_hash,omitempty"`
}

func reviewRechecksEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.ReviewRecheckVersion == 1
}

func recordReviewRecheckHistory(s *Snapshot) error {
	if !reviewRechecksEnabled(*s) {
		return nil
	}
	r := RepairDiagnosis{Findings: []RepairFinding{}}
	if err := r.addReviewFindings(*s); err != nil {
		return err
	}
	if len(r.Findings) == 0 {
		return nil
	}
	updated := &ReviewRecheckHistory{Findings: []RepairFinding{}}
	if prior := s.ReviewRecheckHistory; prior != nil {
		updated.OmittedFindings, updated.OmittedEvidenceHash = prior.OmittedFindings, prior.OmittedEvidenceHash
		updated.Findings = append(updated.Findings, prior.Findings...)
	}
	updated.Findings = append(updated.Findings, r.Findings...)
	if err := boundReviewRecheckHistory(updated); err != nil {
		return err
	}
	s.ReviewRecheckHistory = updated
	return nil
}

func boundReviewRecheckHistory(h *ReviewRecheckHistory) error {
	excess := len(h.Findings) - 128
	if excess <= 0 {
		return nil
	}
	for _, f := range h.Findings[:excess] {
		hash, err := canonical.Hash("harness.review-recheck-omission.v1", struct {
			Previous  string `json:"previous"`
			FindingID string `json:"finding_id"`
		}{h.OmittedEvidenceHash, f.ID})
		if err != nil {
			return err
		}
		h.OmittedEvidenceHash = hash
	}
	h.OmittedFindings += excess
	copy(h.Findings, h.Findings[excess:])
	h.Findings = h.Findings[:128]
	return nil
}

func reviewRechecksForSnapshot(s Snapshot) (*reviewRecheckPrompt, error) {
	if !reviewRechecksEnabled(s) || s.ReviewRecheckHistory == nil || len(s.ReviewRecheckHistory.Findings) == 0 {
		return nil, nil
	}
	h := s.ReviewRecheckHistory
	// Every unresolved answer needs ordinary repair feedback, whose existing
	// contract admits at most 64 findings. Do not request an impossible verdict.
	if len(h.Findings) > 64 {
		return nil, nil
	}
	p := &reviewRecheckPrompt{Version: 1, Findings: append([]RepairFinding(nil), h.Findings...), OmittedFindings: h.OmittedFindings, OmittedEvidenceHash: h.OmittedEvidenceHash}
	current, err := s.Candidate.ID()
	if err != nil {
		return nil, err
	}
	for i := range p.Findings {
		p.Findings[i].CandidateBinding = "stale_candidate"
		if p.Findings[i].CandidateID == current {
			p.Findings[i].CandidateBinding = "matching_recorded_candidate"
		}
	}
	raw, err := canonical.Bytes(p)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, nil
	}
	return p, nil
}

func validateReviewRechecks(s Snapshot, invocation runtime.Invocation, verdict ReviewVerdict) error {
	var envelope struct {
		Rechecks *reviewRecheckPrompt `json:"repair_rechecks"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &envelope); err != nil {
		return err
	}
	if envelope.Rechecks == nil {
		if verdict.Rechecks != nil {
			return errors.New("rechecks were not requested")
		}
		return nil
	}
	if !reviewRechecksEnabled(s) || verdict.Rechecks == nil || len(verdict.Rechecks) != len(envelope.Rechecks.Findings) {
		return errors.New("exact requested reviewer rechecks required")
	}
	expected := map[string]RepairFinding{}
	for _, f := range envelope.Rechecks.Findings {
		expected[f.ID] = f
	}
	for _, answer := range verdict.Rechecks {
		original, ok := expected[answer.FindingID]
		if !ok {
			return errors.New("foreign or duplicate reviewer recheck")
		}
		delete(expected, answer.FindingID)
		if err := validateReviewRecheckOutcome(answer, original, verdict); err != nil {
			return err
		}
	}
	return nil
}

func validateReviewRecheckOutcome(answer ReviewFindingRecheck, original RepairFinding, verdict ReviewVerdict) error {
	if err := validateReviewRecheckAnswer(answer, verdict.Decision); err != nil {
		return err
	}
	if answer.Decision == "unresolved" && !reviewRecheckFeedbackPresent(verdict.Findings, original.Path, answer.Rationale) {
		return errors.New("unresolved recheck requires current repair feedback")
	}
	return nil
}

func reviewRecheckFeedbackPresent(findings []ReviewFinding, path, message string) bool {
	for _, f := range findings {
		if f.Path == path && f.Message == message {
			return true
		}
	}
	return false
}

func validateReviewRecheckAnswer(answer ReviewFindingRecheck, decision string) error {
	if answer.Decision != "closed" && answer.Decision != "unresolved" {
		return errors.New("invalid reviewer recheck decision")
	}
	if decision == "approve" && answer.Decision != "closed" {
		return errors.New("approval conflicts with unresolved reviewer concern")
	}
	if strings.TrimSpace(answer.Rationale) == "" || len(answer.Rationale) > 512 || !utf8.ValidString(answer.Rationale) {
		return errors.New("reviewer recheck rationale outside bounds")
	}
	return nil
}
