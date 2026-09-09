package access

import "errors"

const maxExactInteger int64 = 9007199254740991

// Limits bounds cumulative token reservations, optional API micro-USD
// reservations, and simultaneous unresolved invocations in one ledger scope.
// Nil monetary limits mean no monetary ceiling, never a zero-cost assertion.
type Limits struct {
	Tokens          int64  `json:"tokens" toml:"tokens"`
	UnlimitedTokens bool   `json:"unlimited_tokens,omitempty" toml:"unlimited_tokens"`
	CostMicroUSD    *int64 `json:"cost_micro_usd" toml:"cost_micro_usd"`
	Concurrency     int    `json:"concurrency" toml:"concurrency"`
}

// Reservation binds conservative resource ceilings to one invocation identity.
// API cost ceilings require runtime-enforceable pricing/output bounds before
// dispatch; this ledger alone cannot enforce a provider's spending behavior.
type Reservation struct {
	InvocationID    string `json:"invocation_id"`
	Tokens          int64  `json:"tokens"`
	UnlimitedTokens bool   `json:"unlimited_tokens,omitempty"`
	CostMicroUSD    *int64 `json:"cost_micro_usd"`
	BillingMode     string `json:"billing_mode"`
}

// Ledger applies reservation transitions in journal order. It is not concurrent
// or durable by itself; the controller must replay and mutate it under its journal
// lock. Completion releases concurrency only: cumulative ceilings are not refunded.
type Ledger struct {
	limits       Limits
	tokens, cost int64
	active       int
	entries      map[string]bool
}

// NewLedger validates limits and owns a copy of the optional monetary ceiling.
func NewLedger(limits Limits) (*Ledger, error) {
	if limits.UnlimitedTokens && limits.Tokens != 0 || !limits.UnlimitedTokens && (limits.Tokens < 1 || limits.Tokens > maxExactInteger) || limits.Concurrency < 1 || limits.Concurrency > 1024 {
		return nil, errors.New("invalid model budget")
	}
	if limits.CostMicroUSD != nil {
		value := *limits.CostMicroUSD
		if value < 0 || value > maxExactInteger {
			return nil, errors.New("invalid monetary budget")
		}
		limits.CostMicroUSD = &value
	}
	return &Ledger{limits: limits, entries: map[string]bool{}}, nil
}

// Reserve rejects duplicate invocation IDs and budget exhaustion atomically.
// An uncertain invocation stays active; elapsed time never releases authority.
func (l *Ledger) Reserve(r Reservation) error {
	if l == nil || l.entries == nil {
		return errors.New("uninitialized budget ledger")
	}
	if len(r.InvocationID) != 64 {
		return errors.New("invalid invocation identity")
	}
	for _, c := range r.InvocationID {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return errors.New("invalid invocation identity")
		}
	}
	if _, exists := l.entries[r.InvocationID]; exists {
		return errors.New("invocation already reserved")
	}
	if r.UnlimitedTokens && r.Tokens != 0 || !r.UnlimitedTokens && (r.Tokens < 1 || r.Tokens > maxExactInteger) || r.UnlimitedTokens && !l.limits.UnlimitedTokens || !l.limits.UnlimitedTokens && r.Tokens > l.limits.Tokens-l.tokens || l.active >= l.limits.Concurrency {
		return errors.New("model token or concurrency budget exhausted")
	}
	cost := int64(0)
	switch r.BillingMode {
	case "api":
		if r.CostMicroUSD == nil {
			return errors.New("API reservation requires a cost ceiling")
		}
		cost = *r.CostMicroUSD
		if cost < 0 || cost > maxExactInteger-l.cost {
			return errors.New("invalid API cost reservation")
		}
	case "subscription":
		if r.CostMicroUSD != nil || l.limits.CostMicroUSD != nil {
			return errors.New("subscription cost cannot satisfy a monetary ceiling")
		}
	default:
		return errors.New("invalid billing mode")
	}
	if l.limits.CostMicroUSD != nil && cost > *l.limits.CostMicroUSD-l.cost {
		return errors.New("model cost budget exhausted")
	}
	l.entries[r.InvocationID] = true
	if !l.limits.UnlimitedTokens {
		l.tokens += r.Tokens
	}
	l.cost += cost
	l.active++
	return nil
}

// Complete releases a reservation's concurrency slot after a terminal receipt.
// The caller must verify terminal evidence; UNKNOWN must never call Complete.
// Repeated completion is rejected and cannot create another available slot.
func (l *Ledger) Complete(invocationID string) error {
	if l == nil || !l.entries[invocationID] {
		return errors.New("no active invocation reservation")
	}
	l.entries[invocationID] = false
	l.active--
	return nil
}
