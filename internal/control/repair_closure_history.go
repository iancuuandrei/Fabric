package control

import (
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/verification"
)

// repairFindingHistory scans only already replay-validated events. Its bounded
// second pass avoids replaying every prefix or retaining all historical payloads.
func repairFindingHistory(events []journal.Event, candidateID string) (repairHistory, int, error) {
	scan := repairHistoryScan{candidateID: candidateID, history: repairHistory{findings: []historicalRepairFinding{}}}
	for _, event := range events {
		if err := scan.consume(event); err != nil {
			return repairHistory{}, 0, err
		}
	}
	if err := scan.flush(events[len(events)-1].Sequence); err != nil {
		return repairHistory{}, 0, err
	}
	return scan.history, scan.planSequence, nil
}

type repairHistoryScan struct {
	candidateID  string
	history      repairHistory
	state        *VerificationState
	planSequence int
}

func (s *repairHistoryScan) flush(sequence int) error {
	if s.state == nil {
		return nil
	}
	findings, err := historicalNativeFindings(s.state, s.candidateID, sequence)
	if err != nil {
		return err
	}
	return s.history.retain(findings)
}

func (s *repairHistoryScan) consume(event journal.Event) error {
	switch event.Kind {
	case "verification.planned":
		if err := s.flush(event.Sequence - 1); err != nil {
			return err
		}
		var plan verification.Plan
		if err := canonical.Decode(event.Payload, &plan); err != nil {
			return err
		}
		id, err := plan.ID()
		if err != nil {
			return err
		}
		s.state = &VerificationState{PlanID: id, Plan: plan, Observations: []VerificationObservation{}}
		s.planSequence = event.Sequence
	case "review.recorded":
		// No verification event can follow a recorded review for this plan.
		// Flush first so bounded retention follows chronological gate order.
		if err := s.flush(event.Sequence - 1); err != nil {
			return err
		}
		s.state = nil
		findings, err := historicalReviewFindings(event, s.candidateID)
		if err != nil {
			return err
		}
		return s.history.retain(findings)
	default:
		return updateRepairHistoryVerification(s.state, event)
	}
	return nil
}

func updateRepairHistoryVerification(state *VerificationState, event journal.Event) error {
	if state == nil {
		return nil
	}
	switch event.Kind {
	case "verification.started":
		state.Pending = true
	case "verification.observed":
		var observation VerificationObservation
		if err := canonical.Decode(event.Payload, &observation); err != nil {
			return err
		}
		state.Observations = append(state.Observations, observation)
		state.Pending = false
	case "verification.closed":
		var closure VerificationClosure
		if err := canonical.Decode(event.Payload, &closure); err != nil {
			return err
		}
		state.Closure = &closure
	}
	return nil
}

func historicalNativeFindings(state *VerificationState, candidateID string, sequence int) ([]historicalRepairFinding, error) {
	r := RepairDiagnosis{CandidateID: candidateID, Findings: []RepairFinding{}}
	if err := r.addNativeFindings(state); err != nil {
		return nil, err
	}
	result := []historicalRepairFinding{}
	for _, f := range r.Findings {
		for _, observation := range state.Observations {
			if observation.Result.InvocationID != f.ClosureOracleID {
				continue
			}
			oracle, err := repairNativeOracleID(state.Plan.Invocations[observation.Start.Index])
			if err != nil {
				return nil, err
			}
			result = append(result, historicalRepairFinding{finding: f, oracle: oracle, sequence: sequence})
			break
		}
	}
	return result, nil
}

func historicalReviewFindings(event journal.Event, candidateID string) ([]historicalRepairFinding, error) {
	var record ReviewRecord
	if err := canonical.Decode(event.Payload, &record); err != nil {
		return nil, err
	}
	r := RepairDiagnosis{CandidateID: candidateID, Findings: []RepairFinding{}}
	if err := r.addReviewFindings(Snapshot{Review: &record}); err != nil {
		return nil, err
	}
	result := []historicalRepairFinding{}
	for _, f := range r.Findings {
		result = append(result, historicalRepairFinding{finding: f, sequence: event.Sequence})
	}
	return result, nil
}

type repairHistory struct {
	findings     []historicalRepairFinding
	omitted      int
	omissionHash string
}

func (h *repairHistory) retain(findings []historicalRepairFinding) error {
	h.findings = append(h.findings, findings...)
	excess := len(h.findings) - 128
	if excess <= 0 {
		return nil
	}
	for _, old := range h.findings[:excess] {
		hash, err := canonical.Hash("harness.repair-omitted-history.v1", struct {
			Previous  string `json:"previous"`
			FindingID string `json:"finding_id"`
			OracleID  string `json:"oracle_id"`
			Sequence  int    `json:"sequence"`
		}{h.omissionHash, old.finding.ID, old.oracle, old.sequence})
		if err != nil {
			return err
		}
		h.omissionHash = hash
	}
	h.omitted += excess
	copy(h.findings, h.findings[excess:])
	h.findings = h.findings[:128]
	return nil
}
