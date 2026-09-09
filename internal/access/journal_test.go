package access

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestDurableAdmissionReplaysBudgetAndPolicy(t *testing.T) {
	profile := fixture()
	profile.Kind, profile.CredentialRef, profile.AuthMode = "subscription", "", "session"
	pid, _ := profile.ID()
	route := Route{Version: 1, Role: "planner", Runtime: profile.Runtime, Provider: profile.Provider, Model: "fixture", Effort: "none", AccessID: pid, Permission: "read-only"}
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 1}, Routes: []Route{route}, Profiles: []Profile{profile}}
	id, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	i := Intent{Attempt: 1, PolicyID: id, InputHash: strings.Repeat("b", 64), Route: route, Reservation: Reservation{Tokens: 50, BillingMode: "subscription"}}
	i.Reservation.InvocationID, err = i.ID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if RequireActive(path, policy, i) == nil {
		t.Fatal("missing reservation accepted")
	}
	for _, mutate := range []func(*Intent){
		func(i *Intent) { i.InputHash = strings.Repeat("e", 64) },
		func(i *Intent) { i.Attempt++ },
		func(i *Intent) { i.Reservation.Tokens-- },
		func(i *Intent) { i.Reservation.InvocationID = strings.Repeat("f", 64) },
		func(i *Intent) { i.Route.Model = "substituted" },
	} {
		changed := i
		mutate(&changed)
		if ReserveDurable(path, policy, changed) == nil {
			t.Fatal("mutated invocation persisted")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("rejected invocation created journal", err)
		}
	}
	if err := ReserveDurable(path, policy, i); err != nil {
		t.Fatal(err)
	}
	admitted := i
	if err := RequireActive(path, policy, admitted); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	i.Attempt++
	i.Reservation.InvocationID, _ = i.ID()
	if ReserveDurable(path, policy, i) == nil {
		t.Fatal("restart forgot pending budget")
	}
	policy.Limits.Concurrency = 2
	i.PolicyID, _ = policy.ID()
	i.Reservation.InvocationID, _ = i.ID()
	if ReserveDurable(path, policy, i) == nil {
		t.Fatal("policy widened on replay")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("denied admission changed journal")
	}
	if events, err := journal.Read(path); err != nil || len(events) != 1 {
		t.Fatal("durable intent missing", err)
	}
	policy.Limits.Concurrency = 1
	if RequireActive(path, policy, i) == nil {
		t.Fatal("different intent accepted")
	}
	routeID, _ := route.ID()
	if err := RecordTerminal(path, policy, Receipt{InvocationID: admitted.Reservation.InvocationID, RouteID: routeID, Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if RequireActive(path, policy, admitted) == nil {
		t.Fatal("terminal reservation accepted")
	}
}
