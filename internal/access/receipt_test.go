package access

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalReceiptReleasesOnlyConcurrency(t *testing.T) {
	p := fixture()
	p.Kind, p.CredentialRef, p.AuthMode = "subscription", "", "session"
	pid, _ := p.ID()
	route := Route{Version: 1, Role: "reviewer", Runtime: p.Runtime, Provider: p.Provider, Model: "fixture", Effort: "none", AccessID: pid, Permission: "read-only"}
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 1}, Routes: []Route{route}, Profiles: []Profile{p}}
	id, _ := policy.ID()
	i := Intent{Attempt: 1, PolicyID: id, InputHash: strings.Repeat("b", 64), Route: route, Reservation: Reservation{Tokens: 60, BillingMode: "subscription"}}
	i.Reservation.InvocationID, _ = i.ID()
	routeID, _ := route.ID()
	r := Receipt{InvocationID: i.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: strings.Repeat("c", 64)}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if RecordTerminal(path, policy, r) == nil {
		t.Fatal("receipt before intent")
	}
	if err := ReserveDurable(path, policy, i); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Receipt){
		func(r *Receipt) { r.Status = "UNKNOWN" },
		func(r *Receipt) { r.RouteID = strings.Repeat("d", 64) },
		func(r *Receipt) { r.OutputHash = "" },
		func(r *Receipt) { s := "other"; r.ObservedModel = &s },
		func(r *Receipt) { n := int64(0); r.CostMicroUSD = &n },
		func(r *Receipt) { n := int64(61); r.InputTokens = &n },
	} {
		bad := r
		mutate(&bad)
		if RecordTerminal(path, policy, bad) == nil {
			t.Fatal("invalid terminal observation admitted")
		}
	}
	if err := RecordTerminal(path, policy, r); err != nil {
		t.Fatal(err)
	}
	if RecordTerminal(path, policy, r) == nil {
		t.Fatal("double receipt")
	}
	i.Attempt++
	i.Reservation.InvocationID, _ = i.ID()
	if ReserveDurable(path, policy, i) == nil {
		t.Fatal("terminal receipt refunded cumulative token ceiling")
	}
	i.Reservation.Tokens = 40
	i.Reservation.InvocationID, _ = i.ID()
	if err := ReserveDurable(path, policy, i); err != nil {
		t.Fatal("terminal receipt did not release concurrency", err)
	}
}

func TestUnlimitedReceiptSkipsOnlyTokenUpperBound(t *testing.T) {
	p := fixture()
	p.Kind, p.CredentialRef, p.AuthMode = "subscription", "", "session"
	pid, _ := p.ID()
	route := Route{Version: 1, Role: "reviewer", Runtime: p.Runtime, Provider: p.Provider, Model: "fixture", Effort: "none", AccessID: pid, Permission: "read-only"}
	i := Intent{Attempt: 1, PolicyID: strings.Repeat("a", 64), InputHash: strings.Repeat("b", 64), Route: route, Reservation: Reservation{UnlimitedTokens: true, BillingMode: "subscription"}}
	i.Reservation.InvocationID, _ = i.ID()
	routeID, _ := route.ID()
	input, output, cached := int64(1_000_000), int64(2_000_000), int64(500_000)
	r := Receipt{InvocationID: i.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: strings.Repeat("c", 64), InputTokens: &input, OutputTokens: &output, CachedTokens: &cached}
	if err := r.Validate(i); err != nil {
		t.Fatal("unlimited receipt rejected observed accounting", err)
	}
	cached = input + 1
	if r.Validate(i) == nil {
		t.Fatal("unlimited receipt bypassed token count consistency")
	}
	cached = 500_000
	i.Reservation.Tokens = 1
	i.Reservation.InvocationID, _ = i.ID()
	r.InvocationID = i.Reservation.InvocationID
	if r.Validate(i) == nil {
		t.Fatal("ambiguous unlimited reservation admitted")
	}
}
