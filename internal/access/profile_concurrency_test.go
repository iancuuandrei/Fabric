package access

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileConcurrencyIsDurableAndIndependent(t *testing.T) {
	first := fixture()
	first.Version = 2
	first.Kind, first.CredentialRef, first.AuthMode = "subscription", "", "session"
	first.Privacy = &PrivacyPolicy{Version: 1, Training: "unknown", Retention: "unknown"}
	first.MaxConcurrentInvocations = 1
	second := first
	second.Name = "second-profile"
	firstID, _ := first.ID()
	secondID, _ := second.ID()
	route := Route{Version: 1, Role: "planner", Runtime: first.Runtime, Provider: first.Provider, Model: "fixture", Effort: "none", AccessID: firstID, Permission: "read-only"}
	other := route
	other.Role = "reviewer"
	other.AccessID = secondID
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 3}, Routes: []Route{route, other}, Profiles: []Profile{first, second}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	makeIntent := func(r Route, attempt int) Intent {
		i := Intent{Attempt: attempt, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: r, Reservation: Reservation{Tokens: 10, BillingMode: "subscription"}}
		i.Reservation.InvocationID, _ = i.ID()
		return i
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	i := makeIntent(route, 1)
	if err := ReserveDurable(path, policy, i); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if ReserveDurable(path, policy, makeIntent(route, 2)) == nil {
		t.Fatal("same profile exceeded concurrency ceiling")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("denied reservation changed durable journal")
	}
	if err := ReserveDurable(path, policy, makeIntent(other, 1)); err != nil {
		t.Fatal("independent profile denied", err)
	}
	routeID, _ := route.ID()
	if err := RecordTerminal(path, policy, Receipt{InvocationID: i.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: strings.Repeat("c", 64)}); err != nil {
		t.Fatal(err)
	}
	if err := ReserveDurable(path, policy, makeIntent(route, 2)); err != nil {
		t.Fatal("verified terminal failed to release profile slot", err)
	}
}
