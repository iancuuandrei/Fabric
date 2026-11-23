package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/effects"
)

// AutonomousRepair records a bounded, candidate-specific repair iteration.
// A candidate can consume at most one repair slot: retrying a failed runtime
// cannot silently spend the budget or create an unbounded loop.
type AutonomousRepair struct {
	Attempt     int    `json:"attempt"`
	CandidateID string `json:"candidate_id"`
}

func autonomousPolicyID(s Snapshot) (string, error) {
	if s.Creation.Execution == nil {
		return "", errors.New("autonomous execution was not enabled for this run")
	}
	if err := s.Creation.Execution.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.execution-policy.v1", *s.Creation.Execution)
}

func autonomousFileAuthorization(s Snapshot, intent effects.Intent) (effects.Authorization, error) {
	policyID, err := autonomousPolicyID(s)
	if err != nil {
		return effects.Authorization{}, err
	}
	id, err := intent.ID()
	if err != nil {
		return effects.Authorization{}, err
	}
	return effects.Authorization{IntentID: id, Actor: "fabric:autonomous", Authority: "autonomous-v1", PolicyID: policyID}, nil
}

// autonomousFilesApplied identifies the restart boundary after a confirmed
// writer effect but before verification was planned. Comparing the complete
// prepared effect prevents an unrelated or older file receipt from advancing
// this run.
func autonomousFilesApplied(s Snapshot) bool {
	if graphWriterBatchEffectApplied(s) {
		return true
	}
	return s.FileOutcome == "CONFIRMED" && s.FileIntent != nil && s.WriterProposal != nil &&
		s.FileIntent.Prepared.Intent == s.WriterProposal.Prepared.Intent &&
		s.Candidate != nil && *s.Candidate == s.FileIntent.Prepared.Proposal.After
}

func graphWriterBatchEffectApplied(s Snapshot) bool {
	return s.GraphWriterBatch != nil && s.FileOutcome == "CONFIRMED" && s.FileIntent != nil &&
		s.FileIntent.Prepared.Intent == s.GraphWriterBatch.Prepared.Intent && s.Candidate != nil &&
		*s.Candidate == s.GraphWriterBatch.Prepared.Proposal.After
}

func autonomousRepairApplied(s Snapshot) bool {
	if !autonomousFilesApplied(s) || s.RepairCandidateID == "" || s.WriterProposal == nil || s.FileIntent == nil ||
		s.FileIntent.Prepared.Intent != s.WriterProposal.Prepared.Intent {
		return false
	}
	beforeID, err := s.WriterProposal.Prepared.Proposal.Before.ID()
	return err == nil && beforeID == s.RepairCandidateID
}

// autonomousVerificationCovered reports whether the exact candidate already has
// verification evidence. A confirmed repair must be verified once before a new
// repair slot is admitted; an already-verified failure must admit a new slot
// instead of re-verifying the same candidate and discarding feedback.
func autonomousVerificationCovered(s Snapshot, candidateID string) bool {
	return s.Verification != nil && s.Verification.Plan.CandidateID == candidateID
}

// autonomousDispatchBlocked refuses to admit new provider or role work while
// earlier work remains pending or UNKNOWN. Reconciliation observes state; it
// never resends an uncertain effect.
func autonomousDispatchBlocked(s Snapshot) (blocked error) {
	defer func() {
		if blocked != nil {
			blocked = errors.Join(ErrAutonomousReconciliation, blocked)
		}
	}()
	if s.Verification != nil && s.Verification.Pending {
		return errors.New("verification is pending and cannot be resent automatically")
	}
	for id, d := range s.AgentDispatch {
		if d.Observation == nil {
			return fmt.Errorf("agent dispatch %s is pending and cannot be resent automatically", id)
		}
		if d.Observation.Status == agenttree.StatusUnknown {
			return fmt.Errorf("agent dispatch %s is UNKNOWN and requires reconciliation", id)
		}
	}
	for _, m := range s.ModelAccess {
		if m.Terminal == nil && m.SemanticPending == nil {
			return errors.New("model access remains unresolved and cannot be resent automatically")
		}
	}
	if s.PlannerAccess != nil && s.Plan == nil {
		invocation, err := plannerInvocationForSnapshot(s)
		if err != nil || semanticUsagePendingFor(s, invocation.ID) == nil {
			return errors.New("planner invocation unresolved; automatic retry denied")
		}
	}
	if s.ExplorerHost != nil && s.ExplorerHost.RuntimeReceipt == nil {
		if semanticUsagePendingFor(s, s.ExplorerHost.Intent.Invocation.ID) == nil {
			return errors.New("explorer runtime remains unresolved and cannot be resent automatically")
		}
	}
	for id, r := range s.ExplorerRuns {
		if r.RuntimeReceipt == nil && semanticUsagePendingFor(s, id) == nil {
			return fmt.Errorf("parallel explorer %s remains unresolved and cannot be resent automatically", id)
		}
	}
	if s.WriterHost != nil && s.WriterHost.RuntimeReceipt == nil {
		if semanticUsagePendingFor(s, s.WriterHost.Intent.Invocation.ID) == nil {
			return errors.New("writer runtime remains unresolved and cannot be resent automatically")
		}
	}
	for taskID, host := range s.GraphWriterHosts {
		if host.RuntimeReceipt == nil && semanticUsagePendingFor(s, host.Intent.Invocation.ID) == nil {
			return fmt.Errorf("graph writer task %s runtime remains unresolved and cannot be resent automatically", taskID)
		}
	}
	if s.ReviewHost != nil && s.ReviewHost.RuntimeReceipt == nil {
		if semanticUsagePendingFor(s, s.ReviewHost.Intent.Invocation.ID) == nil {
			return errors.New("review runtime remains unresolved and cannot be resent automatically")
		}
	}
	for invocationID, host := range s.ScheduledReviewHosts {
		if host.RuntimeReceipt == nil && semanticUsagePendingFor(s, host.Intent.Invocation.ID) == nil {
			return fmt.Errorf("scheduled review %s remains unresolved and cannot be resent automatically", invocationID)
		}
	}
	if s.PlannerHost != nil && s.PlannerReceipt == nil {
		invocation, err := plannerInvocationForSnapshot(s)
		if err != nil || semanticUsagePendingFor(s, invocation.ID) == nil {
			return errors.New("planner runtime remains unresolved and cannot be resent automatically")
		}
	}
	if s.FileOutcome == "UNKNOWN" {
		return errors.New("file effect remains UNKNOWN and requires reconciliation")
	}
	if graphHasUnknown(s) {
		return errors.New("graph task is UNKNOWN and requires reconciliation")
	}
	return nil
}

// autonomousWriterEffect advances the candidate-bound writer/proposal/
// authorization/application sequence shared by IMPLEMENTING and REPAIRING.
// It preserves every dispatch guard, the exact candidate binding, and all
// error handling of the inlined blocks it replaces.
//
// It returns proposalStaged=true when a fresh proposal was requested and the
// caller must continue the outer loop, or proposalStaged=false after the
// confirmed effect was applied and the caller proceeds to the normal next
// iteration.
func autonomousWriterEffect(ctx context.Context, path string, s Snapshot) (Snapshot, bool, error) {
	if s.WriterProposal == nil || s.WriterProposal.Prepared.Proposal.Before != *s.Candidate {
		if err := autonomousDispatchBlocked(s); err != nil {
			return s, false, err
		}
		if _, err := RunWriter(ctx, path); err != nil {
			latest, inspectErr := InspectOr(s, path, err)
			return latest, false, inspectErr
		}
		return s, true, nil
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		return s, false, err
	}
	// Graph runs: native writer proposal paths must be within the declared
	// implementation WritePaths before file authorization/application.
	if s.Graph != nil {
		impl, ok := graphImplementationTask(s)
		if !ok {
			return s, false, errors.New("graph implementation task unavailable for writer scope")
		}
		// For repair extensions the active implementation is the newest
		// incomplete one; fall back to union check across implementations so
		// scoped repair writes remain admissible while out-of-scope writes
		// are rejected before any authorization.
		if err := requireWriterPathsInScope(impl, s); err != nil {
			if scopeReplanningEnabled(s) {
				latest, replanErr := recordGraphScopeReplan(ctx, path, s)
				if replanErr != nil {
					return latest, false, errors.Join(err, replanErr)
				}
				return latest, true, nil
			}
			unionOK := false
			for _, t := range s.Graph.Graph.Tasks {
				if t.Kind == "implementation" {
					// Reuse scope check helper via temporary task.
					if requireWriterPathsInScope(t, s) == nil {
						unionOK = true
						break
					}
				}
			}
			if !unionOK {
				return s, false, err
			}
		}
	}
	a, err := autonomousFileAuthorization(s, s.WriterProposal.Prepared.Intent)
	if err != nil {
		return s, false, err
	}
	if _, err := ApplyFiles(ctx, path, s.WriterProposal.Prepared, a); err != nil {
		latest, inspectErr := InspectOr(s, path, err)
		return latest, false, inspectErr
	}
	return s, false, nil
}

// RunAutonomous advances an explicitly opted-in run through normal controller
// operations. Every operation retains its own intent-first and candidate-bound
// safeguards. In particular this function never recreates an UNKNOWN workspace
// or file effect, never repeats a pending verification, and never publishes,
// commits, or pushes.
//
// Graph-enabled runs (ExecutionPolicy.GraphVersion==1) use hierarchical task
// graph execution with bounded parallel read-only explorers by default;
// legacy nil/old policies remain sequential. It is safe to call after process
// restart. A completed stage is inspected and skipped; an incomplete runtime
// is handed to that role's existing resume path.
func RunAutonomous(ctx context.Context, path string) (result Snapshot, failure error) {
	defer func() {
		if failure != nil {
			failure = ClassifyAutonomousFailure(result, failure)
			_ = runLifecycleHooks(context.WithoutCancel(ctx), result, "on_blocked")
		} else if result.State == "READY" {
			_ = runLifecycleHooks(context.WithoutCancel(ctx), result, "on_ready")
		}
	}()
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	s, err = reconcileExplorerCapacityFailures(path, s)
	if err != nil {
		return s, err
	}
	s, err = reconcileUnrecordedSemanticUsagePending(ctx, path, s)
	if err != nil {
		return s, err
	}
	if graphEnabled(s) {
		return runAutonomousGraph(ctx, path, false)
	}
	progress := autonomousProgress{}
	for {
		if err := ctx.Err(); err != nil {
			s, inspectErr := Inspect(path)
			if inspectErr != nil {
				return s, errors.Join(err, inspectErr)
			}
			return s, err
		}
		s, err := Inspect(path)
		if err != nil {
			return s, err
		}
		if _, err := autonomousPolicyID(s); err != nil {
			return s, err
		}
		if err := RequireDispatchAllowed(s); err != nil {
			return s, err
		}
		if err := progress.observe(s); err != nil {
			return s, err
		}
		switch s.State {
		case "OBJECTIVE", "PLANNING":
			if _, err := ResumePlanning(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
		case "AWAITING_APPROVAL":
			policyID, err := autonomousPolicyID(s)
			if err != nil {
				return s, err
			}
			if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
				return InspectOr(s, path, err)
			}
		case "IMPLEMENTING":
			if s.Workspace == nil {
				if s.WorkspaceIntent != nil {
					if _, err := ReconcileWorkspace(ctx, path); err != nil {
						return InspectOr(s, path, fmt.Errorf("autonomous workspace remains unresolved: %w", err))
					}
				} else {
					if err := autonomousDispatchBlocked(s); err != nil {
						return s, err
					}
					if _, err := StartWorkspace(ctx, path); err != nil {
						return InspectOr(s, path, err)
					}
				}
				continue
			}
			if s.FileOutcome == "UNKNOWN" {
				if _, err := ReconcileFiles(ctx, path); err != nil {
					return InspectOr(s, path, fmt.Errorf("autonomous file effect remains UNKNOWN: %w", err))
				}
				continue
			}
			if err := preflightAutonomousVerification(s); err != nil {
				return s, err
			}
			// A confirmed initial effect restarts directly at verification,
			// never with another writer request.
			if autonomousFilesApplied(s) {
				if err := autonomousDispatchBlocked(s); err != nil {
					return s, err
				}
				if _, err := Verify(ctx, path); err != nil {
					return InspectOr(s, path, err)
				}
				continue
			}
			if len(s.Explorations) == 0 {
				if err := autonomousDispatchBlocked(s); err != nil {
					return s, err
				}
				if _, err := RunExplorer(ctx, path, "Inspect the candidate for the approved objective. Identify the relevant files, tests, and implementation risks."); err != nil {
					return InspectOr(s, path, err)
				}
				continue
			}
			if ns, proposalStaged, err := autonomousWriterEffect(ctx, path, s); err != nil {
				return ns, err
			} else if proposalStaged {
				continue
			}
		case "VERIFYING":
			if s.Verification != nil && s.Verification.Pending {
				return s, errors.New("verification is pending and cannot be resent automatically")
			}
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			if _, err := Verify(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
		case "REVIEWING":
			if err := autonomousDispatchBlocked(s); err != nil {
				return s, err
			}
			if _, err := RunReview(ctx, path); err != nil {
				return InspectOr(s, path, err)
			}
		case "REPAIRING":
			if s.Candidate == nil {
				return s, errors.New("repair requires a candidate")
			}
			candidateID, err := s.Candidate.ID()
			if err != nil {
				return s, err
			}
			if s.FileOutcome == "UNKNOWN" {
				if _, err := ReconcileFiles(ctx, path); err != nil {
					return InspectOr(s, path, fmt.Errorf("autonomous file effect remains UNKNOWN: %w", err))
				}
				continue
			}
			if err := rejectUnstartedAutonomousVerification(s); err != nil {
				return s, err
			}
			// Verify the exact confirmed repair before admitting another
			// slot. Checking the slot first would consume budget for an
			// already-repaired candidate and skip its verification.
			if autonomousRepairApplied(s) {
				if candidateID == s.RepairCandidateID {
					return s, errors.Join(ErrAutonomousNoProgress, errors.New("autonomous repair made no candidate progress"))
				}
				if !autonomousVerificationCovered(s, candidateID) {
					if err := autonomousDispatchBlocked(s); err != nil {
						return s, err
					}
					if _, err := Verify(ctx, path); err != nil {
						return InspectOr(s, path, err)
					}
					continue
				}
			}
			if s.RepairCandidateID != "" && s.RepairCandidateID == candidateID && s.FileOutcome == "NOT_APPLIED" {
				return s, errors.Join(ErrAutonomousNoProgress, errors.New("autonomous repair made no candidate progress"))
			}
			if s.RepairCandidateID != candidateID {
				if s.RepairAttempts >= s.Creation.Execution.MaxRepairs {
					return s, fmt.Errorf("%w: autonomous repair bound reached (%d)", ErrAutonomousRepairBudget, s.RepairAttempts)
				}
				if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: s.RepairAttempts + 1, CandidateID: candidateID}); err != nil {
					return InspectOr(s, path, err)
				}
				continue
			}
			// The repair was durably admitted for this candidate. The writer uses
			// existing verification/review feedback and its normal runtime resume.
			if ns, proposalStaged, err := autonomousWriterEffect(ctx, path, s); err != nil {
				return ns, err
			} else if proposalStaged {
				continue
			}
		case "READY":
			return s, nil
		default:
			return s, fmt.Errorf("autonomous execution cannot advance state %q", s.State)
		}
	}
}

// PrepareAutonomous advances a graph-enabled run through planner acceptance,
// machine authorization, graph recording and pristine workspace confirmation,
// then returns before any explorer or writer dispatch. The run remains in
// IMPLEMENTING and can be continued with RunAutonomous after optional lexical
// artifacts have been staged.
func PrepareAutonomous(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if !graphEnabled(s) {
		return s, errors.New("prepare-only requires graph-enabled autonomous execution")
	}
	switch s.State {
	case "OBJECTIVE", "PLANNING", "AWAITING_APPROVAL", "IMPLEMENTING":
	default:
		return s, errors.New("prepare-only requires a run before verification or review")
	}
	if s.State == "IMPLEMENTING" {
		if err := requireFreshGraphPreparationState(s); err != nil {
			return s, err
		}
	}
	return runAutonomousGraph(ctx, path, true)
}

// InspectOr keeps the durable snapshot available to callers when a normal
// operation returns an error after it may have persisted evidence.
func InspectOr(fallback Snapshot, path string, cause error) (Snapshot, error) {
	latest, err := Inspect(path)
	if err != nil {
		return fallback, errors.Join(cause, err)
	}
	return latest, cause
}
