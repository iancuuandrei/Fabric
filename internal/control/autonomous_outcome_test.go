package control

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/codexhost"
)

func TestAutonomousFailureClassificationUsesTypesNotProviderText(t *testing.T) {
	for _, text := range []string{"UNKNOWN provider", "pending repair bound", "uncertain cancellation", "repair budget exhausted", "verification_not_run"} {
		outcome := ClassifyAutonomousFailure(Snapshot{}, errors.New(text+" token=private"))
		if outcome.Status() != "NEEDS_ATTENTION" || outcome.PublicReason() != "operator_attention_required" || strings.Contains(outcome.Error(), "private") {
			t.Fatal("untrusted error text impersonated a typed outcome", outcome)
		}
	}
	for _, tc := range []struct {
		cause       error
		status      string
		disposition GateDisposition
	}{
		{ErrAutonomousReconciliation, "UNKNOWN", GateReconcile},
		{ErrScopeReplanRequired, "NEEDS_REPLAN", GateReplan},
		{ErrAutonomousRepairBudget, "NEEDS_ATTENTION", GateAttention},
		{ErrAutonomousUnsafe, "UNSAFE", GateDeny},
		{context.Canceled, "NEEDS_ATTENTION", GateAttention},
		{ErrSemanticOutputInvalid, "NEEDS_ATTENTION", GateAttention},
		{ErrUsageQualification, "NEEDS_ATTENTION", GateAttention},
		{ErrSemanticUsagePending, "NEEDS_ATTENTION", GateAttention},
	} {
		wrapped := errors.Join(tc.cause, errors.New("secret=must-not-print"))
		outcome := ClassifyAutonomousFailure(Snapshot{}, wrapped)
		if outcome.Status() != tc.status || outcome.Disposition() != tc.disposition || !errors.Is(outcome, tc.cause) || strings.Contains(outcome.Error(), "secret") {
			t.Fatal("typed outcome lost classification or leaked details", outcome)
		}
	}
}

func TestPlannerCapacityOutcomeRequiresSealedReceipt(t *testing.T) {
	outcome := ClassifyAutonomousFailure(Snapshot{}, errors.Join(ErrPlannerCapacityRefused, errors.New("private server detail")))
	if outcome.Status() != "UNKNOWN" || outcome.PublicReason() != "planner_failure_receipt_unverified" || !errors.Is(outcome, ErrPlannerCapacityRefused) || strings.Contains(outcome.Error(), "private") {
		t.Fatal("capacity sentinel without a sealed receipt was trusted", outcome)
	}
	if ClassifyAutonomousFailure(Snapshot{PlannerHost: &codexhost.Launch{}}, ErrPlannerCapacityRefused).Status() != "UNKNOWN" {
		t.Fatal("capacity sentinel settled a host without a durable planner receipt")
	}
}

func TestAutonomousFailureUnresolvedStatePrecedesReplanAndRepair(t *testing.T) {
	s := Snapshot{FileOutcome: "UNKNOWN"}
	for _, cause := range []error{ErrScopeReplanRequired, ErrAutonomousRepairBudget, ErrSemanticOutputInvalid, ErrUsageQualification, ErrSemanticUsagePending, context.Canceled} {
		outcome := ClassifyAutonomousFailure(s, cause)
		if outcome.Status() != "UNKNOWN" || outcome.NextAction() != "reconcile_existing_effect_without_resend" || !errors.Is(outcome, ErrAutonomousReconciliation) || !errors.Is(outcome, cause) {
			t.Fatal("unresolved state permitted another continuation", outcome)
		}
	}
	if ClassifyAutonomousFailure(s, ErrAutonomousUnsafe).Status() != "UNSAFE" {
		t.Fatal("untrusted history was classified as trusted unresolved state")
	}
}

func TestRequiredUsagePendingRetainsAttentionWithoutInventingCompletion(t *testing.T) {
	outcome := ClassifyAutonomousFailure(Snapshot{}, errors.Join(ErrSemanticUsagePending, errors.New("private provider detail")))
	if outcome.Status() != "NEEDS_ATTENTION" || outcome.PublicReason() != "required_usage_pending" || outcome.NextAction() != "reconcile_usage_before_new_provider_call" || !errors.Is(outcome, ErrSemanticUsagePending) {
		t.Fatal("known semantic accounting gate lost its typed outcome", outcome)
	}
	if strings.Contains(outcome.Error(), "private") {
		t.Fatal("accounting diagnostic exposed private provider detail")
	}
	for _, s := range []Snapshot{
		{WriterHost: &WriterHostState{}},
		{ScheduledReviewHosts: map[string]ReviewHostState{"turn": {}}},
	} {
		if ClassifyAutonomousFailure(s, ErrSemanticUsagePending).Status() != "UNKNOWN" {
			t.Fatal("sentinel alone incorrectly settled a provider host")
		}
	}
}

func TestAutonomousPauseRequiresSettledLifecycle(t *testing.T) {
	requested := Snapshot{Lifecycle: LifecycleState{Status: LifecyclePauseRequested}}
	if ClassifyAutonomousFailure(requested, ErrAutonomousPause).Status() == "PAUSED" {
		t.Fatal("pause request was presented as settled pause")
	}
	settled := Snapshot{Lifecycle: LifecycleState{Status: LifecyclePaused}}
	if ClassifyAutonomousFailure(settled, ErrAutonomousPause).Status() != "PAUSED" {
		t.Fatal("settled lifecycle pause lost its product outcome")
	}
}

func TestUsageQualificationDoesNotSettleAnExistingHost(t *testing.T) {
	s := Snapshot{WriterHost: &WriterHostState{}}
	outcome := ClassifyAutonomousFailure(s, ErrUsageQualification)
	if outcome.Status() != "UNKNOWN" || !errors.Is(outcome, ErrAutonomousReconciliation) || !errors.Is(outcome, ErrUsageQualification) {
		t.Fatal("preflight refusal must not settle an already admitted host", outcome)
	}
}

func TestUsageQualificationDoesNotSettleScheduledReviewHost(t *testing.T) {
	s := Snapshot{ScheduledReviewHosts: map[string]ReviewHostState{"turn": {}}}
	outcome := ClassifyAutonomousFailure(s, ErrUsageQualification)
	if outcome.Status() != "UNKNOWN" || !errors.Is(outcome, ErrAutonomousReconciliation) || !errors.Is(outcome, ErrUsageQualification) {
		t.Fatal("preflight refusal settled a scheduled reviewer", outcome)
	}
}

func TestAutonomousAuxiliaryUncertaintyPrecedesContinuation(t *testing.T) {
	cases := map[string]Snapshot{
		"commit-recovery": {Commit: &CommitState{Outcome: "CONFIRMED", Recovery: &CommitRecoveryState{Outcome: "UNKNOWN"}}},
		"commit-lease":    {Commit: &CommitState{Outcome: "CONFIRMED", LeaseRecovery: &CommitLeaseState{Outcome: "UNKNOWN"}}},
		"push-lease":      {Push: &PushState{Outcome: "CONFIRMED", LeaseRecovery: &PushLeaseState{Outcome: "UNKNOWN"}}},
		"draft-lease":     {Draft: &DraftState{Outcome: "CONFIRMED", LeaseRecovery: &DraftLeaseState{Outcome: "UNKNOWN"}}},
		"ri-producer":     {RIProducer: &RIProducerState{Outcome: "UNKNOWN"}},
		"ri-publish":      {RIPublish: &RIPublishState{Outcome: "UNKNOWN"}},
		"ri-import":       {RIImport: &RIImportState{Outcome: "UNKNOWN"}},
		"ri-lexical":      {RILexical: &RILexicalState{Outcome: "UNKNOWN"}},
		"ri-overlay":      {RILexicalOverlay: &RILexicalOverlayState{Outcome: "UNKNOWN"}},
	}
	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			for _, cause := range []error{ErrScopeReplanRequired, ErrAutonomousRepairBudget, ErrAutonomousPause} {
				if outcome := ClassifyAutonomousFailure(snapshot, cause); outcome.Status() != "UNKNOWN" || !errors.Is(outcome, ErrAutonomousReconciliation) || !errors.Is(outcome, cause) {
					t.Fatalf("uncertain auxiliary effect admitted continuation: %s", outcome.Status())
				}
			}
		})
	}
}
