package access

import (
	"strings"
	"testing"
)

func TestReservationLifecycleAndExhaustion(t *testing.T) {
	cap := int64(10)
	l, err := NewLedger(Limits{Tokens: 100, CostMicroUSD: &cap, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	cap = 999 // Caller mutation must not widen the ledger ceiling.
	cost := int64(6)
	r := Reservation{InvocationID: strings.Repeat("a", 64), Tokens: 40, CostMicroUSD: &cost, BillingMode: "api"}
	if err := l.Reserve(r); err != nil {
		t.Fatal(err)
	}
	if l.Reserve(r) == nil {
		t.Fatal("duplicate invocation")
	}
	next := r
	next.InvocationID = strings.Repeat("b", 64)
	if l.Reserve(next) == nil {
		t.Fatal("pending reservation released")
	}
	if err := l.Complete(r.InvocationID); err != nil {
		t.Fatal(err)
	}
	if l.Complete(r.InvocationID) == nil {
		t.Fatal("double release")
	}
	if l.Reserve(next) == nil {
		t.Fatal("cost budget widened or refunded")
	}
	cost = 4
	next.Tokens = 61
	if l.Reserve(next) == nil {
		t.Fatal("token ceiling exceeded")
	}
	next.Tokens = 60
	if err := l.Reserve(next); err != nil {
		t.Fatal("failed reservations mutated state", err)
	}
	if err := l.Complete(next.InvocationID); err != nil {
		t.Fatal(err)
	}
	next.InvocationID = strings.Repeat("c", 64)
	next.Tokens = 1
	if l.Reserve(next) == nil {
		t.Fatal("completed tokens refunded")
	}
}

func TestSubscriptionHasUnknownCost(t *testing.T) {
	l, _ := NewLedger(Limits{Tokens: 100, Concurrency: 2})
	r := Reservation{InvocationID: strings.Repeat("a", 64), Tokens: 1, BillingMode: "subscription"}
	zero := int64(0)
	r.CostMicroUSD = &zero
	if l.Reserve(r) == nil {
		t.Fatal("subscription free cost admitted")
	}
	r.CostMicroUSD = nil
	if err := l.Reserve(r); err != nil {
		t.Fatal(err)
	}
	bounded, _ := NewLedger(Limits{Tokens: 100, CostMicroUSD: &zero, Concurrency: 1})
	if bounded.Reserve(r) == nil {
		t.Fatal("unknown cost satisfies monetary cap")
	}
	r.BillingMode = "api"
	if bounded.Reserve(r) == nil {
		t.Fatal("API unknown reservation admitted")
	}
}

func TestExplicitUnlimitedTokensRetainsConcurrencyAndCostLimits(t *testing.T) {
	l, err := NewLedger(Limits{UnlimitedTokens: true, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	first := Reservation{InvocationID: strings.Repeat("a", 64), UnlimitedTokens: true, BillingMode: "subscription"}
	if err := l.Reserve(first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.InvocationID = strings.Repeat("b", 64)
	if l.Reserve(second) == nil {
		t.Fatal("unlimited token policy bypassed concurrency")
	}
	if err := l.Complete(first.InvocationID); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve(second); err != nil {
		t.Fatal("released concurrency was unavailable", err)
	}

	cap := int64(5)
	api, err := NewLedger(Limits{UnlimitedTokens: true, CostMicroUSD: &cap, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	cost := int64(6)
	request := Reservation{InvocationID: strings.Repeat("c", 64), UnlimitedTokens: true, CostMicroUSD: &cost, BillingMode: "api"}
	if api.Reserve(request) == nil {
		t.Fatal("unlimited token policy bypassed monetary ceiling")
	}
	cost = 5
	if err := api.Reserve(request); err != nil {
		t.Fatal(err)
	}
}

func TestUnlimitedTokenMarkersRejectAmbiguousAndFiniteCombinations(t *testing.T) {
	if _, err := NewLedger(Limits{Tokens: 1, UnlimitedTokens: true, Concurrency: 1}); err == nil {
		t.Fatal("unlimited run admitted a numeric token ceiling")
	}
	finite, err := NewLedger(Limits{Tokens: 100, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	unlimited := Reservation{InvocationID: strings.Repeat("a", 64), UnlimitedTokens: true, BillingMode: "subscription"}
	if finite.Reserve(unlimited) == nil {
		t.Fatal("finite run admitted an unlimited reservation")
	}
	unbounded, err := NewLedger(Limits{UnlimitedTokens: true, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	unlimited.Tokens = 1
	if unbounded.Reserve(unlimited) == nil {
		t.Fatal("unlimited reservation admitted a numeric token ceiling")
	}
}
