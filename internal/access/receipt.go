package access

import (
	"errors"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

// Receipt records a controller-admitted terminal observation. RouteID binds the
// requested route; optional observations retain unavailable provider evidence as
// null. This projection alone is not authentication of provider provenance.
type Receipt struct {
	InvocationID     string  `json:"invocation_id"`
	RouteID          string  `json:"route_id"`
	Status           string  `json:"status"`
	OutputHash       string  `json:"output_hash"`
	ObservedModel    *string `json:"observed_model"`
	ObservedProvider *string `json:"observed_provider"`
	InputTokens      *int64  `json:"input_tokens"`
	OutputTokens     *int64  `json:"output_tokens"`
	CachedTokens     *int64  `json:"cached_tokens"`
	CostMicroUSD     *int64  `json:"cost_micro_usd"`
}

// Validate checks identity, terminal status, known usage bounds and substitution.
// Missing usage is not zero. Exceeding a reservation rejects admission and retains
// the unresolved slot; it must be reconciled as a policy violation, not refunded.
func (r Receipt) Validate(i Intent) error {
	id, err := i.ID()
	if err != nil || id != i.Reservation.InvocationID || r.InvocationID != id {
		return errors.New("receipt invocation mismatch")
	}
	routeID, err := i.Route.ID()
	if err != nil || r.RouteID != routeID {
		return errors.New("receipt route mismatch")
	}
	switch r.Status {
	case "completed":
		if safepath.RequireDigest(r.OutputHash) != nil {
			return errors.New("completed output identity required")
		}
	case "failed", "cancelled":
		if r.OutputHash != "" {
			return errors.New("non-success output cannot be admitted")
		}
	default:
		return errors.New("terminal model observation required")
	}
	if r.ObservedModel != nil && *r.ObservedModel != i.Route.Model || r.ObservedProvider != nil && *r.ObservedProvider != i.Route.Provider {
		return errors.New("observed route substitution")
	}
	for _, value := range []*int64{r.InputTokens, r.OutputTokens, r.CachedTokens, r.CostMicroUSD} {
		if value != nil && (*value < 0 || *value > maxExactInteger) {
			return errors.New("invalid model usage")
		}
	}
	if i.Reservation.UnlimitedTokens && i.Reservation.Tokens != 0 || !i.Reservation.UnlimitedTokens && (i.Reservation.Tokens < 1 || i.Reservation.Tokens > maxExactInteger) {
		return errors.New("invalid token reservation")
	}
	total := int64(0)
	for _, value := range []*int64{r.InputTokens, r.OutputTokens} {
		if value != nil {
			if !i.Reservation.UnlimitedTokens && *value > i.Reservation.Tokens-total {
				return errors.New("token reservation exceeded")
			}
			total += *value
		}
	}
	if r.CachedTokens != nil && r.InputTokens != nil && *r.CachedTokens > *r.InputTokens {
		return errors.New("cached tokens exceed input")
	}
	if i.Reservation.BillingMode == "subscription" && r.CostMicroUSD != nil {
		return errors.New("subscription monetary usage is unknown")
	}
	if r.CostMicroUSD != nil && (i.Reservation.CostMicroUSD == nil || *r.CostMicroUSD > *i.Reservation.CostMicroUSD) {
		return errors.New("cost reservation exceeded")
	}
	return nil
}

// RecordTerminal persists a validated terminal projection and releases its
// concurrency slot on replay. The controller must obtain terminal evidence from
// the bound runtime; arbitrary worker output must not call this function.
func RecordTerminal(path string, policy Policy, receipt Receipt) error {
	_, err := journal.Append(path, "access.receipt", receipt, func(events []journal.Event) error { return replayAdmissions(events, policy) })
	return err
}
