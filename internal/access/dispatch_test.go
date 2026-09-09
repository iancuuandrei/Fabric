package access

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestDispatchPersistsBeforeInvocationAndNeverRetries(t *testing.T) {
	p := fixture()
	p.Kind, p.CredentialRef, p.AuthMode = "subscription", "", "session"
	pid, _ := p.ID()
	r := Route{Version: 1, Role: "writer", Runtime: p.Runtime, Provider: p.Provider, Model: "fixture", Effort: "none", AccessID: pid, Permission: "workspace-write"}
	policy := Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: Public, Limits: Limits{Tokens: 100, Concurrency: 1}, Routes: []Route{r}, Profiles: []Profile{p}}
	policyID, _ := policy.ID()
	input := "private fixture prompt body"
	inputID, _ := InputID(input)
	i := Intent{Attempt: 1, PolicyID: policyID, InputHash: inputID, Route: r, Reservation: Reservation{Tokens: 10, BillingMode: "subscription"}}
	i.Reservation.InvocationID, _ = i.ID()
	path := filepath.Join(t.TempDir(), "gate.jsonl")
	calls := 0
	adapter := func(ctx context.Context, received Intent, body string) error {
		calls++
		events, err := journal.Read(path)
		if err != nil || len(events) != 1 || replayAdmissions(events, policy) != nil {
			t.Fatal("invoked before durable admission", err)
		}
		if received.Reservation.InvocationID != i.Reservation.InvocationID || body != input {
			t.Fatal("dispatch binding changed")
		}
		return errors.New("credential-secret")
	}
	if Dispatch(context.Background(), path, policy, i, input+" changed", adapter) == nil || calls != 0 {
		t.Fatal("changed input dispatched")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if Dispatch(cancelled, path, policy, i, input, adapter) == nil || calls != 0 {
		t.Fatal("cancelled invocation dispatched")
	}
	err := Dispatch(context.Background(), path, policy, i, input, adapter)
	if err == nil || strings.Contains(err.Error(), "credential-secret") || calls != 1 {
		t.Fatal("runtime failure mishandled", err)
	}
	if Dispatch(context.Background(), path, policy, i, input, adapter) == nil || calls != 1 {
		t.Fatal("unknown invocation retried")
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), input) || strings.Contains(string(raw), "credential-secret") {
		t.Fatal("sensitive body persisted", err)
	}
	reads := 0
	readback := func(ctx context.Context, recorded Intent) (Receipt, error) {
		reads++
		if recorded.Reservation.InvocationID != i.Reservation.InvocationID {
			t.Fatal("foreign invocation observed")
		}
		routeID, _ := recorded.Route.ID()
		return Receipt{InvocationID: recorded.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: strings.Repeat("e", 64)}, nil
	}
	changed := i
	changed.Attempt++
	changed.Reservation.InvocationID, _ = changed.ID()
	if Reconcile(context.Background(), path, policy, changed, readback) == nil || reads != 0 {
		t.Fatal("resume mismatch reached provider")
	}
	if err := Reconcile(context.Background(), path, policy, i, func(context.Context, Intent) (Receipt, error) { return Receipt{}, errors.New("credential-secret") }); err == nil || strings.Contains(err.Error(), "credential-secret") {
		t.Fatal("readback error leaked", err)
	}
	if err := Reconcile(context.Background(), path, policy, i, readback); err != nil {
		t.Fatal(err)
	}
	if Reconcile(context.Background(), path, policy, i, readback) == nil || reads != 1 || calls != 1 {
		t.Fatal("terminal invocation repeated")
	}
}
