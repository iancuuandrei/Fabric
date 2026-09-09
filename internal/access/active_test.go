package access

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func activeFixture(t *testing.T) (string, Policy, Intent) {
	t.Helper()
	profile := Profile{
		Version: 1, Name: "opencode-session", Kind: "subscription",
		Runtime: "opencode-http", Provider: "fixture", AuthMode: "session",
		RepositoryClasses: []Class{Private},
	}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := Route{
		Version: 1, Role: "reviewer", Runtime: profile.Runtime, Provider: profile.Provider,
		Model: "model", Effort: "low", AccessID: profileID, Permission: "read-only",
	}
	policy := Policy{
		Version: 1, RunID: strings.Repeat("a", 64), Class: Private,
		Limits: Limits{Tokens: 200, Concurrency: 1}, Routes: []Route{route}, Profiles: []Profile{profile},
	}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := Intent{
		Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route,
		Reservation: Reservation{Tokens: 100, BillingMode: "subscription"},
	}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(t.TempDir(), "access.jsonl"), policy, intent
}

func TestRequireActiveTracksDurableReservationLifecycle(t *testing.T) {
	path, policy, intent := activeFixture(t)
	if err := RequireActive(path, policy, intent); err == nil {
		t.Fatal("missing reservation accepted")
	}
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	if err := RequireActive(path, policy, intent); err != nil {
		t.Fatal("durable active reservation rejected:", err)
	}

	changed := intent
	changed.Reservation.Tokens--
	if err := RequireActive(path, policy, changed); err == nil {
		t.Fatal("changed intent accepted under recorded invocation identity")
	}

	routeID, err := intent.Route.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordTerminal(path, policy, Receipt{
		InvocationID: intent.Reservation.InvocationID, RouteID: routeID, Status: "cancelled",
	}); err != nil {
		t.Fatal(err)
	}
	if err := RequireActive(path, policy, intent); err == nil {
		t.Fatal("terminal reservation accepted")
	}
}

func TestRequireActiveRejectsPolicyDriftAndMalformedJournal(t *testing.T) {
	path, policy, intent := activeFixture(t)
	if err := ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	drifted := policy
	drifted.RunID = strings.Repeat("c", 64)
	if err := RequireActive(path, drifted, intent); err == nil {
		t.Fatal("reservation replayed under a different policy")
	}

	malformed := filepath.Join(t.TempDir(), "access.jsonl")
	if err := os.WriteFile(malformed, []byte("{not-json}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireActive(malformed, policy, intent); err == nil {
		t.Fatal("malformed journal accepted")
	}

	unknown := filepath.Join(t.TempDir(), "access.jsonl")
	if _, err := journal.Append(unknown, "unrelated.event", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := RequireActive(unknown, policy, intent); err == nil {
		t.Fatal("unknown journal event accepted")
	}
}
