package control

import (
	"context"
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// RoleSemanticCorrection retains a completed rejection and grants one new
// invocation identity. It grants no file effect or scheduler claim.
type RoleSemanticCorrection struct {
	Version                     int                        `json:"version"`
	Attempt                     int                        `json:"attempt"`
	CandidateID                 string                     `json:"candidate_id"`
	BaseInvocationID            string                     `json:"base_invocation_id"`
	TaskID                      string                     `json:"task_id,omitempty"`
	PriorTaskID                 string                     `json:"prior_task_id,omitempty"`
	ScheduledTaskID             string                     `json:"scheduled_task_id,omitempty"`
	PriorSchedulePath           string                     `json:"prior_schedule_path,omitempty"`
	PriorScheduleID             string                     `json:"prior_schedule_id,omitempty"`
	PriorScheduleDefinitionHash string                     `json:"prior_schedule_definition_hash,omitempty"`
	PriorClaim                  *taskscheduler.Claim       `json:"prior_claim,omitempty"`
	PriorEvidence               *taskscheduler.Evidence    `json:"prior_evidence,omitempty"`
	Original                    runtime.Invocation         `json:"original"`
	Predecessor                 runtime.Result             `json:"predecessor"`
	ReceiptHead                 string                     `json:"receipt_head"`
	Rejection                   string                     `json:"rejection"`
	Invocation                  runtime.Invocation         `json:"invocation"`
	AnchoredRejection           *AnchoredSemanticRejection `json:"anchored_rejection,omitempty"`
}

func correctedRoleInvocation(s Snapshot, base runtime.Invocation) runtime.Invocation {
	current := base
	for _, correction := range s.RoleCorrections {
		if correction.ScheduledTaskID == "" && correction.BaseInvocationID == base.ID && correction.Original == current {
			current = correction.Invocation
		}
	}
	return current
}

func expectedRoleSemanticCorrection(s Snapshot, original runtime.Invocation, result runtime.Result, proofs ...*AnchoredSemanticRejection) (RoleSemanticCorrection, error) {
	return expectedRoleSemanticCorrectionForTask(s, "", original, result, nil, proofs...)
}

func expectedRoleSemanticCorrectionForTask(s Snapshot, taskID string, original runtime.Invocation, result runtime.Result, scheduled *scheduledRoleCorrectionBinding, proofs ...*AnchoredSemanticRejection) (RoleSemanticCorrection, error) {
	var zero RoleSemanticCorrection
	if len(proofs) > 1 {
		return zero, ErrAutonomousUnsafe
	}
	var proof *AnchoredSemanticRejection
	if len(proofs) == 1 {
		proof = proofs[0]
	}
	if s.Creation.Execution == nil || s.Creation.Execution.SemanticCorrectionVersion != 1 || s.Candidate == nil {
		return zero, errors.New("role semantic correction policy unavailable")
	}
	if err := RequireDispatchAllowed(s); err != nil {
		return zero, err
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return zero, err
	}
	if autonomousAuxiliaryEffectUnknown(s) {
		return zero, ErrAutonomousReconciliation
	}
	var base runtime.Invocation
	var err error
	switch original.Profile.Role {
	case "writer", "fixer":
		base, err = writerInvocationForTaskBase(s, taskID)
	case "reviewer":
		base, err = reviewInvocationBase(s)
	default:
		return zero, errors.New("unsupported role correction")
	}
	if err != nil {
		return zero, err
	}
	current := base
	if taskID == "" {
		current = correctedRoleInvocation(s, base)
	}
	for _, correction := range s.RoleCorrections {
		if correction.TaskID != taskID || correction.ScheduledTaskID == "" || correction.BaseInvocationID != base.ID {
			continue
		}
		operation, operationErr := scheduledOperationForRole(correction.Invocation.Profile.Role)
		if operationErr != nil {
			return zero, operationErr
		}
		current, err = scheduledTurnInvocation(correction.Invocation, operation, correction.ScheduledTaskID)
		if err != nil {
			return zero, err
		}
	}
	if current != original {
		return zero, ErrAutonomousUnsafe
	}
	if err := runtime.ValidateResult(original, result, true); err != nil {
		return zero, errors.Join(ErrAutonomousUnsafe, err)
	}
	domain, err := runtimeResultDomain(original.Profile.Role)
	if err != nil {
		return zero, err
	}
	hash, err := canonical.Hash(domain, result)
	if err != nil {
		return zero, err
	}
	var head string
	switch original.Profile.Runtime {
	case "opencode-http":
		if err := requireOpenCodeRoleReceipt(s, original, result); err != nil {
			return zero, err
		}
		head = s.ProviderRuntime[original.ID].RuntimeJournalHead
	case "codex-app-server":
		if original.Profile.Role == "reviewer" {
			host := scheduledReviewHostForInvocation(s, original)
			if host == nil || host.Intent.Invocation != original || host.RuntimeReceipt == nil || host.RuntimeReceipt.ResultHash != hash {
				return zero, ErrAutonomousUnsafe
			}
			head = host.RuntimeReceipt.JournalHead
		} else if taskID != "" {
			host, ok := s.GraphWriterHosts[roleReceiptTaskID(s, original, taskID)]
			if !ok || host.Intent.Invocation != original || host.RuntimeReceipt == nil || host.RuntimeReceipt.ResultHash != hash {
				return zero, ErrAutonomousUnsafe
			}
			head = host.RuntimeReceipt.JournalHead
		} else {
			if s.WriterHost == nil || s.WriterHost.Intent.Invocation != original || s.WriterHost.RuntimeReceipt == nil || s.WriterHost.RuntimeReceipt.ResultHash != hash {
				return zero, ErrAutonomousUnsafe
			}
			head = s.WriterHost.RuntimeReceipt.JournalHead
		}
	default:
		return zero, errors.New("role correction requires a completed runtime receipt")
	}
	var rejection error
	if original.Profile.Role == "reviewer" {
		if proof != nil {
			return zero, ErrAutonomousUnsafe
		}
		payload, encodeErr := canonical.Bytes(ReviewRecord{original, result})
		if encodeErr != nil {
			return zero, encodeErr
		}
		copy := s
		rejection = replayReview(&copy, journal.Event{Payload: payload})
		if !semanticOutputFailureOnly(rejection) {
			return zero, errors.New("review result is not solely a semantic rejection")
		}
	} else if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		var proposal writercontract.AnchoredProposal
		proposal, rejection = decodeAnchoredProposal(result.Output)
		if rejection == nil {
			rejection, err = validateAnchoredSemanticRejection(s, taskID, proposal, proof)
			if err != nil {
				return zero, err
			}
		} else if proof != nil {
			return zero, ErrAutonomousUnsafe
		}
	} else {
		if proof != nil {
			return zero, ErrAutonomousUnsafe
		}
		_, rejection = decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
		if rejection == nil {
			return zero, errors.New("writer output is not a malformed contract")
		}
	}
	attempt := 1
	for _, correction := range s.RoleCorrections {
		if correction.BaseInvocationID == base.ID && correction.TaskID == taskID {
			attempt++
		}
	}
	diagnostic := boundedSemanticRejection(rejection)
	invocation, err := buildSemanticCorrectionInvocation(original, result, head, attempt, 2, diagnostic)
	if err != nil {
		return zero, err
	}
	candidate, err := s.Candidate.ID()
	if err != nil {
		return zero, err
	}
	correction := RoleSemanticCorrection{Version: 1, Attempt: attempt, CandidateID: candidate, BaseInvocationID: base.ID, TaskID: taskID, Original: original, Predecessor: result, ReceiptHead: head, Rejection: diagnostic, Invocation: invocation}
	correction.AnchoredRejection = proof
	if scheduled != nil {
		correction.ScheduledTaskID = scheduledCorrectionTaskID(s.RunID, taskID, *scheduled, invocation, attempt)
		correction.PriorSchedulePath = scheduled.SchedulePath
		correction.PriorScheduleID = scheduled.ScheduleID
		correction.PriorScheduleDefinitionHash = scheduled.ScheduleDefinitionHash
		correction.PriorTaskID = scheduled.PriorTaskID
		claim := scheduled.Claim
		evidence := scheduled.Evidence
		correction.PriorClaim = &claim
		correction.PriorEvidence = &evidence
	}
	return correction, nil
}

func replayRoleSemanticCorrection(s *Snapshot, e journal.Event) error {
	var correction RoleSemanticCorrection
	if err := canonical.Decode(e.Payload, &correction); err != nil {
		return err
	}
	var expected RoleSemanticCorrection
	var err error
	if correction.ScheduledTaskID != "" {
		if correction.PriorClaim == nil || correction.PriorEvidence == nil {
			return ErrAutonomousUnsafe
		}
		binding := scheduledRoleCorrectionBinding{SchedulePath: correction.PriorSchedulePath, ScheduleID: correction.PriorScheduleID, ScheduleDefinitionHash: correction.PriorScheduleDefinitionHash, PriorTaskID: correction.PriorTaskID, Claim: *correction.PriorClaim, Evidence: *correction.PriorEvidence}
		if err := validateScheduledRoleCorrectionBinding(*s, correction.TaskID, correction.Original, binding); err != nil {
			return err
		}
		expected, err = expectedRoleSemanticCorrectionForTask(*s, correction.TaskID, correction.Original, correction.Predecessor, &binding, correction.AnchoredRejection)
	} else {
		if correction.TaskID != "" || correction.PriorTaskID != "" || correction.PriorClaim != nil || correction.PriorEvidence != nil || correction.PriorSchedulePath != "" || correction.PriorScheduleID != "" || correction.PriorScheduleDefinitionHash != "" {
			return ErrAutonomousUnsafe
		}
		expected, err = expectedRoleSemanticCorrection(*s, correction.Original, correction.Predecessor, correction.AnchoredRejection)
	}
	if err != nil {
		return err
	}
	if !sameCanonical(correction, expected) {
		return ErrAutonomousUnsafe
	}
	s.RoleCorrections = append(s.RoleCorrections, correction)
	return nil
}

func startRoleSemanticCorrection(ctx context.Context, path string, original runtime.Invocation, result runtime.Result) (err error) {
	// A scheduler claim is bound to one exact invocation. Its successor must be
	// admitted by the scheduler rather than run recursively under the old claim.
	if scheduledAgentTurn(ctx) != nil || graphWriterTaskFromContext(ctx) != "" {
		return errors.New("semantic correction requires a new scheduler admission")
	}
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if s.Workspace == nil {
		return ErrAutonomousUnsafe
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return err
	}
	observed, err := observeCandidate(ctx, path, s)
	if err != nil {
		return err
	}
	if s.Candidate == nil || observed != *s.Candidate {
		return ErrAutonomousUnsafe
	}
	proof, err := captureAnchoredSemanticRejection(ctx, path, s, "", result)
	if err != nil {
		return err
	}
	correction, err := expectedRoleSemanticCorrection(s, original, result, proof)
	if err != nil {
		return err
	}
	return Append(path, "role.semantic-correction", correction)
}
