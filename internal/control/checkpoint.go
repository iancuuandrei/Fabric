package control

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
)

// RunCheckpoint is a payload-free projection of one validated controller
// journal prefix. It is status evidence, not authority to resume or dispatch.
type RunCheckpoint struct {
	Version              int               `json:"version"`
	RunID                string            `json:"run_id"`
	JournalHead          string            `json:"journal_head"`
	SourceID             string            `json:"source_id"`
	SourceCommit         string            `json:"source_commit"`
	SourceTree           string            `json:"source_tree"`
	CandidateID          string            `json:"candidate_id,omitempty"`
	GraphPlanID          string            `json:"graph_plan_id,omitempty"`
	GraphDigest          string            `json:"graph_digest,omitempty"`
	GraphRevision        int               `json:"graph_revision,omitempty"`
	CompletedTaskIDs     []string          `json:"completed_task_ids"`
	VerificationPlanID   string            `json:"verification_plan_id,omitempty"`
	Verification         []CheckpointCheck `json:"verification"`
	Review               CheckpointReview  `json:"review"`
	State                string            `json:"state"`
	ActiveIntentCount    int               `json:"active_intent_count"`
	UncertainIntentCount int               `json:"uncertain_intent_count"`
	AcceptedCheckpoint   bool              `json:"accepted_checkpoint"`
	AcceptanceReason     string            `json:"acceptance_reason"`
}

// CheckpointCheck reports only the configured check label, result status and
// candidate binding. Process output, command arguments and diagnostics are
// deliberately excluded.
type CheckpointCheck struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	CandidateBound bool   `json:"candidate_bound"`
}

// CheckpointReview exposes only the structured verdict and exact binding
// booleans; reviewer output and findings are not copied into the projection.
type CheckpointReview struct {
	Status            string `json:"status"`
	Decision          string `json:"decision,omitempty"`
	CandidateBound    bool   `json:"candidate_bound"`
	VerificationBound bool   `json:"verification_bound"`
}

// ReadCheckpoint validates and summarizes a single consistent journal prefix.
// The snapshot and its head are obtained from InspectWithHead's single read.
func ReadCheckpoint(path string) (RunCheckpoint, error) {
	s, head, err := InspectWithHead(path)
	if err != nil {
		return RunCheckpoint{}, err
	}
	return checkpointFromSnapshot(s, head)
}

func checkpointFromSnapshot(s Snapshot, head string) (RunCheckpoint, error) {
	if s.RunID == "" || head == "" {
		return RunCheckpoint{}, errors.New("validated run journal prefix required")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return RunCheckpoint{}, err
	}
	result := RunCheckpoint{
		Version: 1, RunID: s.RunID, JournalHead: head,
		SourceID: sourceID, SourceCommit: s.Creation.Repository.Commit, SourceTree: s.Creation.Repository.Tree,
		State: s.State, CompletedTaskIDs: []string{}, Verification: []CheckpointCheck{},
		Review:           CheckpointReview{Status: "not_recorded"},
		AcceptanceReason: "run has not reached a candidate-bound passing verification and approved review",
	}
	if s.Candidate != nil {
		result.CandidateID, err = s.Candidate.ID()
		if err != nil {
			return RunCheckpoint{}, err
		}
	}
	if s.Graph != nil {
		result.GraphPlanID = s.Graph.PlanID
		result.GraphDigest = s.Graph.Digest
		result.GraphRevision = s.Graph.Revision
		for _, task := range s.Graph.Graph.Tasks {
			evidence, ok := s.Graph.Evidence[task.ID]
			if task.Completed && ok && evidence.Outcome == string(engineeringplan.AttemptCompleted) &&
				evidence.AttemptID != "" && len(task.Attempts) > 0 &&
				task.Attempts[len(task.Attempts)-1].ID == evidence.AttemptID &&
				task.Attempts[len(task.Attempts)-1].Outcome == engineeringplan.AttemptCompleted {
				result.CompletedTaskIDs = append(result.CompletedTaskIDs, task.ID)
			}
		}
		sort.Strings(result.CompletedTaskIDs)
	}
	if s.Verification != nil {
		result.VerificationPlanID = s.Verification.PlanID
		for index, invocation := range s.Verification.Plan.Invocations {
			check := CheckpointCheck{Name: invocation.Check.Name, Status: "not_started"}
			if index < len(s.Verification.Observations) {
				observation := s.Verification.Observations[index]
				check.Status = observation.Result.Status
				invocationID, invocationErr := invocation.ID()
				check.CandidateBound = invocationErr == nil && result.CandidateID != "" && observation.Result.CandidateID == result.CandidateID &&
					observation.Result.InvocationID == invocationID && observation.Result.Started &&
					observation.After.Candidate != nil && *observation.After.Candidate == *s.Candidate && observation.After.Error == ""
			}
			result.Verification = append(result.Verification, check)
		}
		if s.Verification.Pending {
			result.Verification = append(result.Verification, CheckpointCheck{Name: "pending", Status: "pending"})
		}
	}
	if s.Review != nil {
		result.Review.Status = "recorded"
		var verdict ReviewVerdict
		if err := json.Unmarshal([]byte(s.Review.Result.Output), &verdict); err == nil {
			result.Review.Decision = verdict.Decision
			result.Review.CandidateBound = result.CandidateID != "" && verdict.CandidateID == result.CandidateID
			result.Review.VerificationBound = s.Verification != nil && verdict.VerificationPlanID == s.Verification.PlanID
		}
	}
	result.ActiveIntentCount, result.UncertainIntentCount = checkpointIntentCounts(s)
	if acceptedCheckpoint(s, result) {
		result.AcceptedCheckpoint = true
		result.AcceptanceReason = "candidate-bound native verification passed and candidate-bound review approved at READY"
	}
	return result, nil
}

func acceptedCheckpoint(s Snapshot, c RunCheckpoint) bool {
	if s.State != "READY" || s.WorkspaceOutcome != "CONFIRMED" || s.Candidate == nil || c.CandidateID == "" || s.Verification == nil || s.Verification.Pending || s.Verification.Closure != nil || s.Review == nil {
		return false
	}
	if s.Verification.Plan.CandidateID != c.CandidateID || len(s.Verification.Plan.Invocations) == 0 || len(s.Verification.Observations) != len(s.Verification.Plan.Invocations) || len(c.Verification) != len(s.Verification.Plan.Invocations) {
		return false
	}
	for _, check := range c.Verification {
		if check.Status != "PASS" || !check.CandidateBound {
			return false
		}
	}
	if c.Review.Status != "recorded" || c.Review.Decision != "approve" || !c.Review.CandidateBound || !c.Review.VerificationBound {
		return false
	}
	var verdict ReviewVerdict
	if err := canonical.Decode([]byte(s.Review.Result.Output), &verdict); err != nil || verdict.Findings == nil || len(verdict.Findings) != 0 {
		return false
	}
	return verdict.CandidateID == c.CandidateID && verdict.VerificationPlanID == s.Verification.PlanID
}

// Counts summarize unresolved admissions without returning their identifiers.
// Planning can be active before an invocation or effect is admitted. Unclosed
// admitted operations are counted separately as uncertain.
func checkpointIntentCounts(s Snapshot) (active, uncertain int) {
	unresolved := lifecycleUnresolved(s)
	active = len(unresolved)
	for _, intent := range unresolved {
		// PLANNING without a recorded plan is active workflow, but not yet an
		// admitted invocation/effect whose terminal outcome is uncertain.
		if intent != "planning" {
			uncertain++
		}
	}
	return active, uncertain
}
