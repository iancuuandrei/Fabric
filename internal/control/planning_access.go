package control

import (
	"errors"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func expectedPlanningAccess(s Snapshot) (access.Intent, error) {
	invocation, err := plannerInvocation(s.Creation.Config, s.Creation.Objective)
	if err != nil {
		return access.Intent{}, err
	}
	i, err := deriveModelAccessIntent(s, invocation, 1)
	if err != nil {
		return access.Intent{}, err
	}
	// The fake has no provider billing evidence. Do not manufacture an API price.
	if i.Reservation.BillingMode != "subscription" {
		return i, errors.New("fake planner requires unknown-cost access fixture")
	}
	return i, nil
}

func replayPlanningAccess(s *Snapshot, e journal.Event) error {
	if s.State != "PLANNING" || s.PlannerAccess != nil {
		return errors.New("planner admission transition rejected")
	}
	expected, err := expectedPlanningAccess(*s)
	if err != nil {
		return err
	}
	var got access.Intent
	if err := canonical.Decode(e.Payload, &got); err != nil {
		return err
	}
	id, err := got.ID()
	if err != nil || id != expected.Reservation.InvocationID || got.Reservation.InvocationID != id {
		return errors.New("planner admission differs from expected input/route/budget")
	}
	p, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return err
	}
	l, err := access.NewLedger(p.Limits)
	if err != nil {
		return err
	}
	if err := l.Reserve(got.Reservation); err != nil {
		return err
	}
	s.PlannerAccess = &got
	return nil
}

func validatePlanningAccessResult(s Snapshot, r runtime.Result) error {
	_, err := planningAccessReceipt(s, r)
	return err
}

func planningAccessReceipt(s Snapshot, r runtime.Result) (access.Receipt, error) {
	if s.PlannerAccess == nil {
		return access.Receipt{}, errors.New("planner output lacks durable access intent")
	}
	routeID, err := s.PlannerAccess.Route.ID()
	if err != nil {
		return access.Receipt{}, err
	}
	output, err := canonical.Hash("harness.planner-result.v1", r)
	if err != nil {
		return access.Receipt{}, err
	}
	receipt := access.Receipt{InvocationID: s.PlannerAccess.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: output, ObservedModel: r.ObservedModel, ObservedProvider: r.ObservedProvider, InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens}
	return receipt, receipt.Validate(*s.PlannerAccess)
}
