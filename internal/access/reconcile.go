package access

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

// Reconcile reads an existing invocation through a controller-owned observation
// adapter. The adapter MUST NOT create/resume a model turn or retry inference.
// Only an exact pending intent can reach it. No raw input is needed for readback.
// Concurrent readbacks may race; journal validation admits at most one receipt.
func Reconcile(ctx context.Context, path string, policy Policy, expected Intent, observe func(context.Context, Intent) (Receipt, error)) error {
	if observe == nil {
		return errors.New("observation adapter required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := expected.ID()
	if err != nil || id != expected.Reservation.InvocationID {
		return errors.New("invalid reconciliation identity")
	}
	events, err := journal.Read(path)
	if err != nil {
		return err
	}
	if err := replayAdmissions(events, policy); err != nil {
		return err
	}
	var pending *Intent
	for _, event := range events {
		switch event.Kind {
		case "access.intent":
			var recorded Intent
			if err := canonical.Decode(event.Payload, &recorded); err != nil {
				return err
			}
			if recorded.Reservation.InvocationID == id {
				pending = &recorded
			}
		case "access.receipt":
			var receipt Receipt
			if err := canonical.Decode(event.Payload, &receipt); err != nil {
				return err
			}
			if receipt.InvocationID == id {
				return errors.New("invocation already terminal")
			}
		}
	}
	if pending == nil {
		return errors.New("no matching pending invocation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	receipt, err := observe(ctx, *pending)
	if err != nil {
		return errors.Join(errors.New("model observation unavailable"), ctx.Err())
	}
	if err := receipt.Validate(*pending); err != nil {
		return err
	}
	return RecordTerminal(path, policy, receipt)
}
