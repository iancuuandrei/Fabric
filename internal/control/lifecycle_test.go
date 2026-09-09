package control

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func lifecycleFixture(t *testing.T) (string, Creation) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.db")
	created := creation(t)
	if err := Append(path, "run.created", created); err != nil {
		t.Fatal(err)
	}
	return path, created
}

func TestLifecyclePauseSettlesAndResumeDoesNotResend(t *testing.T) {
	path, _ := lifecycleFixture(t)
	s, err := Inspect(path)
	if err != nil || lifecycleStatus(s) != LifecycleActive {
		t.Fatal(s.Lifecycle, err)
	}
	s, err = RequestPause(path, "operator", "pause-1")
	if err != nil || lifecycleStatus(s) != LifecyclePauseRequested {
		t.Fatal(s.Lifecycle, err)
	}
	if err = RequireDispatchAllowed(s); err == nil {
		t.Fatal("pause request admitted new dispatch")
	}
	if err = Append(path, "planning.started", struct{}{}); err == nil {
		t.Fatal("pause request admitted a new planning stage")
	}
	if _, err = SettleLifecycle(path, "operator", "no stage was admitted", false); err == nil {
		t.Fatal("pause settled without workload quiescence evidence")
	}
	s, err = SettleLifecycle(path, "operator", "no stage was admitted", true)
	if err != nil || lifecycleStatus(s) != LifecyclePaused || s.Lifecycle.Settlement == nil || s.Lifecycle.Settlement.Unresolved == nil || len(s.Lifecycle.Settlement.Unresolved) != 0 {
		t.Fatal(s.Lifecycle, err)
	}
	before, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = Resume(path, "operator", "resume-1")
	if err != nil || lifecycleStatus(s) != LifecycleActive {
		t.Fatal(s.Lifecycle, err)
	}
	after, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before)+1 || after[len(after)-1].Kind != "run.resumed" {
		t.Fatal("resume appended anything other than its controller event")
	}
	if err = Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal("resumed run did not reopen dispatch", err)
	}
}

func TestLifecycleCancelRetainsUnknownAndAllowsLateReceipt(t *testing.T) {
	path, created := lifecycleFixture(t)
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := RequestCancel(path, "operator", "cancel-1")
	if err != nil || lifecycleStatus(s) != LifecycleCancelRequested {
		t.Fatal(s.Lifecycle, err)
	}
	s, err = SettleLifecycle(path, "operator", "owned process tree observed stopped", true)
	if err != nil || lifecycleStatus(s) != LifecycleCancelled {
		t.Fatal(s.Lifecycle, err)
	}
	if !reflect.DeepEqual(s.Lifecycle.Settlement.Unresolved, []string{"planning"}) || s.State != "PLANNING" || s.Plan != nil {
		t.Fatal("cancel discarded or promoted unresolved planning work", s)
	}
	invocation, err := runtime.NewInvocation(created.Config.Planner, created.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(path, "plan.recorded", result); err != nil {
		t.Fatal("cancel blocked a late receipt", err)
	}
	s, err = Inspect(path)
	if err != nil || lifecycleStatus(s) != LifecycleCancelled || s.Plan == nil {
		t.Fatal(s.Lifecycle, err)
	}
	if err = Append(path, "plan.approved", Approval{PlanID: s.PlanID, Actor: "operator"}); err == nil {
		t.Fatal("cancelled run admitted forward progress")
	}
	if _, err = Resume(path, "operator", "revive-cancelled"); err == nil {
		t.Fatal("cancelled run was revived")
	}
}

func TestLifecycleSettlementBindsHeadAndUnresolvedSet(t *testing.T) {
	path, _ := lifecycleFixture(t)
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := RequestPause(path, "operator", "pause-binding")
	if err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	requestID, err := s.Lifecycle.PauseRequest.ID()
	if err != nil {
		t.Fatal(err)
	}
	settlement := LifecycleSettlement{Version: 1, RunID: s.RunID, RequestID: requestID, ControllerHead: strings.Repeat("0", 64), Actor: "operator", Evidence: "process tree stopped", WorkloadsStopped: true, Unresolved: []string{"planning"}}
	if err = Append(path, "run.paused", settlement); err == nil {
		t.Fatal("settlement with substituted controller head admitted")
	}
	settlement.ControllerHead = events[len(events)-1].Hash
	settlement.Unresolved = []string{}
	if err = Append(path, "run.paused", settlement); err == nil {
		t.Fatal("settlement discarded unresolved work")
	}
	settlement.Unresolved = []string{"planning"}
	if err = Append(path, "run.paused", settlement); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleAdmissionAllowlistIsFinite(t *testing.T) {
	paused := Snapshot{Lifecycle: LifecycleState{Status: LifecyclePaused}}
	for _, kind := range []string{
		"model.access-receipt",
		"agent.dispatch-observed",
		"planning.host-ready", "planning.host-observed", "planning.runtime-observed", "planning.provider-observed", "plan.recorded",
		"explorer.host-ready", "explorer.host-observed", "explorer.runtime-observed", "explorer.recorded",
		"writer.host-ready", "writer.host-observed", "writer.runtime-observed", "writer.proposed",
		"review.host-ready", "review.host-observed", "review.runtime-observed", "review.recorded",
		"role.provider-observed", "workspace.confirmed", "files.observed",
		"verification.observed", "verification.closed", "commit.observed", "commit.lease-observed",
		"push.observed", "push.lease-observed", "draft.observed", "draft.lease-observed",
		"ri.producer-observed", "ri.producer-closed", "ri.publish-observed", "ri.import-observed", "ri.lexical-observed", "ri.overlay-observed",
	} {
		if err := admitLifecycleEvent(paused, kind); err != nil {
			t.Fatalf("reconciliation %q blocked: %v", kind, err)
		}
	}
	for _, kind := range []string{
		"run.created", "agent.dispatch-admitted", "planning.started", "planning.access-intent", "model.access-intent",
		"planning.host-intent", "explorer.host-intent", "writer.host-intent", "review.host-intent",
		"workspace.intent", "files.intent", "files.recovery-intent", "verification.planned", "verification.started",
		"commit.intent", "commit.recovery-intent", "commit.lease-intent",
		"push.intent", "push.lease-intent", "draft.intent", "draft.lease-intent",
		"ri.producer-intent", "ri.publish-intent", "ri.publish-recovery-intent", "ri.import-intent", "ri.lexical-intent", "ri.overlay-intent",
		"plan.approved", "future.unclassified-event",
	} {
		if err := admitLifecycleEvent(paused, kind); err == nil {
			t.Fatalf("new or unclassified event %q admitted", kind)
		}
	}
}

func TestLifecycleExactRetriesAreIdempotent(t *testing.T) {
	path, _ := lifecycleFixture(t)
	first, err := RequestPause(path, "operator", "pause-retry")
	if err != nil {
		t.Fatal(err)
	}
	second, err := RequestPause(path, "operator", "pause-retry")
	if err != nil || !sameCanonical(first, second) {
		t.Fatal("exact pause retry changed state", err)
	}
	settled, err := SettleLifecycle(path, "operator", "no active children", true)
	if err != nil {
		t.Fatal(err)
	}
	again, err := SettleLifecycle(path, "operator", "no active children", true)
	if err != nil || !sameCanonical(settled, again) {
		t.Fatal("exact settlement retry changed state", err)
	}
	resumed, err := Resume(path, "operator", "resume-retry")
	if err != nil {
		t.Fatal(err)
	}
	again, err = Resume(path, "operator", "resume-retry")
	if err != nil || !sameCanonical(resumed, again) {
		t.Fatal("exact resume retry changed state", err)
	}
}
