package control

import (
	"errors"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/verification"
)

// RepairClosureReport exposes original failures and exact native recheck receipts.
// It is a read-only journal projection and grants no execution authority.
type RepairClosureReport struct {
	Version               int                    `json:"version"`
	RunID                 string                 `json:"run_id"`
	SourceID              string                 `json:"source_id"`
	ControllerHead        string                 `json:"controller_head"`
	CandidateID           string                 `json:"candidate_id,omitempty"`
	Accepted              bool                   `json:"accepted_checkpoint"`
	Findings              []RepairFindingClosure `json:"findings"`
	OmittedFindings       int                    `json:"omitted_findings"`
	OmittedEvidenceHash   string                 `json:"omitted_evidence_hash,omitempty"`
	ReviewRecheckCoverage string                 `json:"review_recheck_coverage,omitempty"`
	Authority             string                 `json:"authority"`
}

// RepairFindingClosure separates an original finding from later closure evidence.
// Reviewer concerns require an explicit recheck; generic approval cannot close them.
type RepairFindingClosure struct {
	Finding             RepairFinding `json:"finding"`
	Status              string        `json:"status"`
	OracleDefinitionID  string        `json:"oracle_definition_id,omitempty"`
	ClosureCandidateID  string        `json:"closure_candidate_id,omitempty"`
	ClosurePlanID       string        `json:"closure_plan_id,omitempty"`
	ClosureInvocationID string        `json:"closure_invocation_id,omitempty"`
	ClosureEvidenceID   string        `json:"closure_evidence_id,omitempty"`
}

type historicalRepairFinding struct {
	finding  RepairFinding
	oracle   string
	sequence int
}

// ReadRepairClosure validates one complete journal prefix before projecting its
// historical failures. It reads no workspace files and never appends events.
func ReadRepairClosure(path, repositoryRoot, runID string) (RepairClosureReport, error) {
	events, err := journal.Read(path)
	if err != nil {
		return RepairClosureReport{}, err
	}
	s, err := Replay(events)
	if err != nil {
		return RepairClosureReport{}, err
	}
	if s.RunID != runID || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(repositoryRoot) {
		return RepairClosureReport{}, errors.New("journal/run repository binding mismatch")
	}
	if err := validateControllerJournalPath(path, s.Creation, s.RunID); err != nil {
		return RepairClosureReport{}, err
	}
	return repairClosureFromValidatedEvents(s, events)
}

func repairClosureFromValidatedEvents(s Snapshot, events []journal.Event) (RepairClosureReport, error) {
	if len(events) == 0 {
		return RepairClosureReport{}, errors.New("validated journal prefix required")
	}
	head := events[len(events)-1].Hash
	checkpoint, err := checkpointFromSnapshot(s, head)
	if err != nil {
		return RepairClosureReport{}, err
	}
	r := RepairClosureReport{Version: 1, RunID: s.RunID, SourceID: checkpoint.SourceID,
		ControllerHead: head, CandidateID: checkpoint.CandidateID, Accepted: checkpoint.AcceptedCheckpoint,
		Findings: []RepairFindingClosure{}, Authority: "advisory_only"}
	history, latestPlanSequence, err := repairFindingHistory(events, r.CandidateID)
	if err != nil {
		return r, err
	}
	r.OmittedFindings, r.OmittedEvidenceHash = history.omitted, history.omissionHash
	witnesses, err := repairNativeWitnesses(s, r.Accepted)
	if err != nil {
		return r, err
	}
	reviewWitnesses, coverage, err := repairReviewWitnesses(s, r.Accepted)
	if err != nil {
		return r, err
	}
	r.ReviewRecheckCoverage = coverage
	for _, original := range history.findings {
		closure := nativeFindingClosure(s, r, original, latestPlanSequence, witnesses)
		if original.finding.Source == "reviewer" && original.sequence < latestPlanSequence {
			if witness, ok := reviewWitnesses[original.finding.ID]; ok {
				closure.Status = "reviewer_recheck_closed"
				closure.ClosureCandidateID, closure.ClosurePlanID = r.CandidateID, s.Verification.PlanID
				closure.ClosureInvocationID, closure.ClosureEvidenceID = witness.invocationID, witness.evidenceID
			}
		}
		r.Findings = append(r.Findings, closure)
	}
	return r, nil
}

func nativeFindingClosure(s Snapshot, r RepairClosureReport, old historicalRepairFinding, latestPlanSequence int, witnesses map[string]repairNativeWitness) RepairFindingClosure {
	c := RepairFindingClosure{Finding: old.finding, Status: "open", OracleDefinitionID: old.oracle}
	if old.finding.Source == "reviewer" {
		c.Status = "recheck_required"
		return c
	}
	if old.finding.Category != "check_failure" {
		c.Status = "unavailable_not_semantic"
		return c
	}
	if !r.Accepted || s.Verification == nil || old.sequence >= latestPlanSequence || old.finding.GateID == s.Verification.PlanID {
		return c
	}
	if witness, ok := witnesses[old.oracle]; ok {
		c.Status, c.ClosureCandidateID, c.ClosurePlanID = "native_oracle_passed", r.CandidateID, s.Verification.PlanID
		c.ClosureInvocationID, c.ClosureEvidenceID = witness.invocationID, witness.evidenceID
		return c
	}
	c.Status = "oracle_changed"
	return c
}

type repairNativeWitness struct{ invocationID, evidenceID string }

func repairNativeWitnesses(s Snapshot, accepted bool) (map[string]repairNativeWitness, error) {
	result := map[string]repairNativeWitness{}
	if !accepted || s.Verification == nil {
		return result, nil
	}
	for index, invocation := range s.Verification.Plan.Invocations {
		oracle, err := repairNativeOracleID(invocation)
		if err != nil {
			return nil, err
		}
		observation := s.Verification.Observations[index]
		evidence, err := canonical.Hash("harness.repair-closure-observation.v1", observation)
		if err != nil {
			return nil, err
		}
		result[oracle] = repairNativeWitness{invocationID: observation.Result.InvocationID, evidenceID: evidence}
	}
	return result, nil
}

// Only candidate identity changes between rechecks. Arguments, limits, directory,
// root executable hash and the complete frozen environment remain in the oracle.
func repairNativeOracleID(invocation verification.Invocation) (string, error) {
	invocation.CandidateID = ""
	return canonical.Hash("harness.repair-native-oracle.v1", invocation)
}
