package cost

import (
	"errors"
	"math"
	"testing"
)

func TestCostEstimationAndBudgetCircuitBreaker(t *testing.T) {
	u := Usage{
		InputTokens:  1_000_000,
		CachedTokens: 800_000,
		OutputTokens: 100_000,
	}
	est := EstimateUSD("claude-sonnet-4-5", u, nil)
	if !est.KnownPricing {
		t.Fatal("expected known pricing for claude-sonnet-4-5")
	}
	// uncached 200k * $3/1M = $0.60, cached 800k * $0.30/1M = $0.24, output 100k * $15/1M = $1.50 -> $2.34
	if math.Abs(est.EstimatedUSD-2.34) > 0.001 {
		t.Fatalf("expected ~$2.34, got $%.4f", est.EstimatedUSD)
	}
	if est.CacheSavedUSD < 2.0 {
		t.Fatalf("expected >$2.00 cache savings, got $%.4f", est.CacheSavedUSD)
	}

	tr := NewTracker("claude-sonnet-4-5", Budget{MaxUSD: 2.00, MaxTurns: 10})
	if err := tr.CheckBeforeTurn(); err != nil {
		t.Fatalf("unexpected pre-turn error: %v", err)
	}
	tr.RecordTurn(u)
	if err := tr.CheckBeforeTurn(); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded after $2.34 spend, got: %v", err)
	}
}
