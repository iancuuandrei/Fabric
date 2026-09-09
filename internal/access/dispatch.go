package access

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
)

// InputID hashes exact bounded UTF-8 runtime input without retaining its body.
// The runtime adapter must include all model-visible request context in this input.
func InputID(input string) (string, error) {
	if !utf8.ValidString(input) || strings.TrimSpace(input) == "" || len(input) > 256<<10 {
		return "", errors.New("invalid model input")
	}
	return canonical.Hash("harness.access-input.v1", input)
}

// Dispatch persists an input-bound reservation before calling the selected
// controller-owned adapter exactly once. invoke is trusted code, never a worker
// callback; it must enforce the admitted route and resource ceilings. This helper
// does not provide OS isolation or erase the adapter's independent authority.
// A callback error is sanitized and leaves the invocation unresolved. Even a nil
// callback error does not release budget: a separately validated terminal receipt
// is required. Repeating Dispatch cannot resume or retry an existing reservation.
func Dispatch(ctx context.Context, path string, policy Policy, intent Intent, input string, invoke func(context.Context, Intent, string) error) error {
	if invoke == nil {
		return errors.New("runtime adapter required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := InputID(input)
	if err != nil {
		return err
	}
	if id != intent.InputHash {
		return errors.New("runtime input differs from admission")
	}
	// Freeze pointer-bearing resource fields before durable validation and adapter
	// dispatch. Caller mutation must not change the dispatched cost ceiling.
	if intent.Reservation.CostMicroUSD != nil {
		cost := *intent.Reservation.CostMicroUSD
		intent.Reservation.CostMicroUSD = &cost
	}
	if err := ReserveDurable(path, policy, intent); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := invoke(ctx, intent, input); err != nil {
		return errors.Join(errors.New("model invocation unresolved"), ctx.Err())
	}
	return nil
}
