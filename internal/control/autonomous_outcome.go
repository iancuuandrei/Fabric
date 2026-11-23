package control

import (
	"context"
	"errors"

	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// ErrAutonomousReconciliation prohibits new work until existing effects settle.
var ErrAutonomousReconciliation = errors.New("autonomous effects require reconciliation")

// ErrAutonomousRepairBudget retains a candidate after its repair budget is spent.
var ErrAutonomousRepairBudget = errors.New("autonomous repair budget exhausted")

// ErrAutonomousNoProgress marks a runner that cannot advance its durable state.
var ErrAutonomousNoProgress = errors.New("autonomous execution made no durable progress")

// ErrAutonomousUnsafe marks an untrusted identity, history or authority boundary.
var ErrAutonomousUnsafe = errors.New("autonomous authority validation failed")

// ErrAutonomousPause marks closed admission, not an inferred quiescence receipt.
var ErrAutonomousPause = errors.New("autonomous admission is paused")

// ErrSemanticOutputInvalid identifies rejected content after runtime identity validation.
// It grants no retry authority and never describes an uncertain provider effect.
var ErrSemanticOutputInvalid = errors.New("completed semantic output is invalid")

// AutonomousStop is a typed, safe public outcome; its private cause retains
// errors.Is identity without including provider details in Error or JSON.
type AutonomousStop struct {
	disposition GateDisposition
	reason      string
	nextAction  string
	cause       error
}

// Error exposes only the closed public reason, never the underlying cause.
func (e *AutonomousStop) Error() string { return e.reason }

// Unwrap retains cancellation, recovery and authority sentinel identities.
func (e *AutonomousStop) Unwrap() error { return e.cause }

// Disposition describes continuation without granting new authority.
func (e *AutonomousStop) Disposition() GateDisposition { return e.disposition }

// PublicReason returns a stable, non-provider diagnostic.
func (e *AutonomousStop) PublicReason() string { return e.reason }

// NextAction identifies the admissible operator or continuation action.
func (e *AutonomousStop) NextAction() string { return e.nextAction }

// Status returns the product outcome; attention and replan never mean READY.
func (e *AutonomousStop) Status() string {
	switch e.disposition {
	case GateDeny:
		return "UNSAFE"
	case GateReconcile:
		return "UNKNOWN"
	case GateReplan:
		return "NEEDS_REPLAN"
	case GatePause:
		return "PAUSED"
	default:
		return "NEEDS_ATTENTION"
	}
}

// ClassifyAutonomousFailure uses typed causes and validated controller state.
// Arbitrary error text cannot impersonate a receipt, pause or budget outcome.
// Unknown untyped errors request inspection; they never authorize a retry.
func ClassifyAutonomousFailure(s Snapshot, cause error) *AutonomousStop {
	if cause == nil {
		return nil
	}
	stop := func(d GateDisposition, reason, action string) *AutonomousStop {
		return &AutonomousStop{disposition: d, reason: reason, nextAction: action, cause: cause}
	}
	if errors.Is(cause, ErrAutonomousUnsafe) {
		return stop(GateDeny, "authority_validation_failed", "inspect_identity_history_and_authority")
	}
	// The durable state wins over a format/repair/cancellation diagnosis: no
	// correction or replan may step around an unresolved admitted effect.
	if blocked := autonomousDispatchBlocked(s); blocked != nil || s.WorkspaceOutcome == "UNKNOWN" {
		cause = errors.Join(cause, ErrAutonomousReconciliation, blocked)
		return stop(GateReconcile, "effect_requires_reconciliation", "reconcile_existing_effect_without_resend")
	}
	if autonomousAuxiliaryEffectUnknown(s) {
		cause = errors.Join(cause, ErrAutonomousReconciliation)
		return stop(GateReconcile, "effect_requires_reconciliation", "reconcile_existing_effect_without_resend")
	}
	var prior *AutonomousStop
	if errors.As(cause, &prior) && prior.reason != "" {
		return prior
	}
	switch {
	case errors.Is(cause, ErrAutonomousReconciliation), errors.Is(cause, opencoderuntime.ErrRecoveryRequired):
		return stop(GateReconcile, "effect_requires_reconciliation", "reconcile_existing_effect_without_resend")
	case errors.Is(cause, ErrScopeReplanRequired):
		return stop(GateReplan, "scope_replan_required", "request_candidate_bound_scope_replan")
	case errors.Is(cause, ErrAutonomousRepairBudget):
		return stop(GateAttention, "repair_budget_exhausted", "inspect_candidate_and_remaining_gates")
	case errors.Is(cause, ErrAutonomousNoProgress):
		return stop(GateAttention, "durable_progress_stalled", "inspect_last_transition_and_remaining_tasks")
	case errors.Is(cause, ErrAutonomousVerificationNotRun):
		return stop(GateAttention, "verification_not_run", "repair_verification_environment_before_dispatch")
	case errors.Is(cause, worktree.ErrDestinationUnsupported):
		return stop(GateAttention, "workspace_path_unsupported", "use_shorter_repository_checkout_before_new_run")
	case errors.Is(cause, ErrSemanticOutputInvalid):
		return stop(GateAttention, "semantic_output_invalid", "inspect_completed_receipt_and_correction_budget")
	case errors.Is(cause, ErrUsageQualification):
		return stop(GateAttention, "usage_not_qualified", "qualify_usage_accounting_before_new_dispatch")
	case errors.Is(cause, ErrPlannerCapacityRefused):
		if !plannerCapacityReceiptAdmitted(s) {
			return stop(GateReconcile, "planner_failure_receipt_unverified", "inspect_planner_runtime_and_receipt_without_resend")
		}
		return stop(GateAttention, "planner_capacity_refused", "wait_for_provider_capacity_then_start_a_new_run")
	case errors.Is(cause, ErrSemanticUsagePending):
		return stop(GateAttention, "required_usage_pending", "reconcile_usage_before_new_provider_call")
	case errors.Is(cause, ErrSemanticCorrectionBudget):
		return stop(GateAttention, "semantic_correction_budget_exhausted", "inspect_retained_output_and_remaining_tasks")
	case errors.Is(cause, ErrAutonomousPause):
		if lifecycleStatus(s) == LifecyclePaused {
			return stop(GatePause, "run_paused", "inspect_pause_and_resume_when_authorized")
		}
		return stop(GateAttention, "pause_settlement_required", "settle_existing_work_without_new_dispatch")
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return stop(GateAttention, "execution_interrupted", "inspect_admitted_work_before_continuation")
	default:
		return stop(GateAttention, "operator_attention_required", "inspect_run_and_classify_remaining_gate")
	}
}

func autonomousAuxiliaryEffectUnknown(s Snapshot) bool {
	if s.Commit != nil && (s.Commit.Outcome == "UNKNOWN" ||
		s.Commit.Recovery != nil && s.Commit.Recovery.Outcome == "UNKNOWN" ||
		s.Commit.LeaseRecovery != nil && s.Commit.LeaseRecovery.Outcome == "UNKNOWN") {
		return true
	}
	if s.Push != nil && (s.Push.Outcome == "UNKNOWN" || s.Push.LeaseRecovery != nil && s.Push.LeaseRecovery.Outcome == "UNKNOWN") {
		return true
	}
	if s.Draft != nil && (s.Draft.Outcome == "UNKNOWN" || s.Draft.LeaseRecovery != nil && s.Draft.LeaseRecovery.Outcome == "UNKNOWN") {
		return true
	}
	return s.RIProducer != nil && s.RIProducer.Outcome == "UNKNOWN" && s.RIProducer.Closure == nil ||
		s.RIPublish != nil && s.RIPublish.Outcome == "UNKNOWN" ||
		s.RIImport != nil && s.RIImport.Outcome == "UNKNOWN" ||
		s.RILexical != nil && s.RILexical.Outcome == "UNKNOWN" ||
		s.RILexicalOverlay != nil && s.RILexicalOverlay.Outcome == "UNKNOWN"
}
