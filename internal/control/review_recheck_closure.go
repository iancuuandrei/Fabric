package control

import (
	"encoding/json"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func repairReviewWitnesses(s Snapshot, accepted bool) (map[string]repairNativeWitness, string, error) {
	result := map[string]repairNativeWitness{}
	if !reviewRechecksEnabled(s) {
		return result, "", nil
	}
	if s.Review == nil {
		return result, "not_recorded", nil
	}
	var envelope struct {
		Rechecks *reviewRecheckPrompt `json:"repair_rechecks"`
	}
	if err := json.Unmarshal([]byte(s.Review.Invocation.Input), &envelope); err != nil {
		return nil, "", err
	}
	if envelope.Rechecks == nil {
		return result, "not_requested", nil
	}
	coverage := "requested"
	if envelope.Rechecks.OmittedFindings != 0 {
		coverage = "partial_history"
	}
	if !accepted {
		return result, coverage, nil
	}
	var verdict ReviewVerdict
	if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err != nil {
		return nil, "", err
	}
	evidence, err := canonical.Hash("harness.repair-review-closure.v1", s.Review)
	if err != nil {
		return nil, "", err
	}
	for _, answer := range verdict.Rechecks {
		if answer.Decision == "closed" {
			result[answer.FindingID] = repairNativeWitness{invocationID: s.Review.Invocation.ID, evidenceID: evidence}
		}
	}
	return result, coverage, nil
}
