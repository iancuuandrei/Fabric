package providercredential

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
)

type leaseFixture struct {
	path     string
	policy   access.Policy
	intent   access.Intent
	endpoint providergateway.EndpointContract
	profile  access.Profile
}

func newLeaseFixture(t *testing.T, kind string, authModeOnly bool) leaseFixture {
	t.Helper()
	profile := access.Profile{Version: 1, Name: "provider-access", Kind: kind, Runtime: "opencode-http", Provider: "openai", CredentialRef: "provider-key", RepositoryClasses: []access.Class{access.Private}}
	if authModeOnly {
		profile.CredentialRef, profile.AuthMode = "", "existing-login"
	}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "planner", Runtime: profile.Runtime, Provider: profile.Provider, Model: "qualified-model", Effort: "high", AccessID: profileID, Permission: "read-only"}
	limits := access.Limits{Tokens: 100, Concurrency: 1}
	reservation := access.Reservation{Tokens: 80, BillingMode: kind}
	if kind == "api" {
		limitCost, reservationCost := int64(1000), int64(800)
		limits.CostMicroUSD = &limitCost
		reservation.CostMicroUSD = &reservationCost
	}
	policy := access.Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: access.Private, Limits: limits, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route, Reservation: reservation}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.jsonl")
	if err := access.ReserveDurable(path, policy, intent); err != nil {
		t.Fatal(err)
	}
	endpointRef := profile.CredentialRef
	if endpointRef == "" {
		endpointRef = "provider-key"
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "openai", URL: "https://api.example.test/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: endpointRef}}
	if _, err := endpoint.ID(); err != nil {
		t.Fatal(err)
	}
	return leaseFixture{path: path, policy: policy, intent: intent, endpoint: endpoint, profile: profile}
}

type recordingSource struct {
	secret []byte
	refs   []string
	err    error
}

func (s *recordingSource) Take(_ context.Context, ref string) ([]byte, error) {
	s.refs = append(s.refs, ref)
	return s.secret, s.err
}

func TestResolveSupportsExplicitAPIAndSubscriptionCredentialRefs(t *testing.T) {
	for _, kind := range []string{"api", "subscription"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newLeaseFixture(t, kind, false)
			source := &recordingSource{secret: []byte("fixture-secret")}
			lease, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if len(source.refs) != 1 || source.refs[0] != fixture.profile.CredentialRef {
				t.Fatal("resolver did not request exact selected credential", source.refs)
			}
			for _, value := range source.secret {
				if value != 0 {
					t.Fatal("source-owned credential bytes were not cleared after transfer")
				}
			}
			binding := lease.Binding()
			if binding.BillingMode != kind || binding.CredentialRef != fixture.profile.CredentialRef || binding.Provider != fixture.intent.Route.Provider || binding.Runtime != fixture.intent.Route.Runtime || binding.AuthScheme != "bearer" {
				t.Fatal("credential binding differs from selected admission", binding)
			}
			if _, err := binding.ID(); err != nil {
				t.Fatal(err)
			}
			seen := ""
			if err := lease.WithSecret(context.Background(), func(_ context.Context, secret []byte) error {
				seen = string(secret)
				secret[0] = 'X'
				return nil
			}); err != nil || seen != "fixture-secret" {
				t.Fatal("lease did not provide the resolved credential", err)
			}
			if err := lease.WithSecret(context.Background(), func(_ context.Context, secret []byte) error {
				if string(secret) != "fixture-secret" {
					t.Fatal("callback mutation changed retained credential")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestResolveRejectsAuthModeOnlySubscriptionBeforeSource(t *testing.T) {
	fixture := newLeaseFixture(t, "subscription", true)
	source := &recordingSource{secret: []byte("fixture-secret")}
	if _, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source); err == nil || len(source.refs) != 0 {
		t.Fatal("auth-mode-only subscription reached credential source", err, source.refs)
	}
}

func TestResolveRejectsMismatchedEndpointAndAdmissionBeforeSource(t *testing.T) {
	mutations := map[string]func(*leaseFixture){
		"provider":       func(f *leaseFixture) { f.endpoint.Provider = "anthropic" },
		"credential_ref": func(f *leaseFixture) { f.endpoint.Auth.CredentialRef = "other-key" },
		"invalid_auth":   func(f *leaseFixture) { f.endpoint.Auth.HeaderName = "authorization" },
		"version_one": func(f *leaseFixture) {
			f.endpoint = providergateway.EndpointContract{Version: 1, Provider: "openai", URL: "https://api.example.test/v1/chat/completions", Protocol: providergateway.OpenAIChatCompletionsAdapter}
		},
		"route": func(f *leaseFixture) { f.intent.Route.Model = "other-model" },
		"billing": func(f *leaseFixture) {
			f.intent.Reservation.BillingMode = "subscription"
			f.intent.Reservation.CostMicroUSD = nil
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fixture := newLeaseFixture(t, "api", false)
			mutate(&fixture)
			source := &recordingSource{secret: []byte("fixture-secret")}
			if _, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source); err == nil || len(source.refs) != 0 {
				t.Fatal("mismatched identity reached credential source", err, source.refs)
			}
		})
	}
}

func TestResolveAndUseRequireExactActiveAdmission(t *testing.T) {
	fixture := newLeaseFixture(t, "api", false)
	missingSource := &recordingSource{secret: []byte("fixture-secret")}
	if _, err := Resolve(context.Background(), filepath.Join(t.TempDir(), "missing.jsonl"), fixture.policy, fixture.intent, fixture.endpoint, missingSource); err == nil || len(missingSource.refs) != 0 {
		t.Fatal("missing admission reached source", err)
	}
	source := &recordingSource{secret: []byte("fixture-secret")}
	lease, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	routeID, _ := fixture.intent.Route.ID()
	if err := access.RecordTerminal(fixture.path, fixture.policy, access.Receipt{InvocationID: fixture.intent.Reservation.InvocationID, RouteID: routeID, Status: "failed"}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := lease.WithSecret(context.Background(), func(context.Context, []byte) error { called = true; return nil }); err == nil || called {
		t.Fatal("terminal admission allowed credential use", err)
	}
}

func TestLeaseFreezesAdmissionRedactsAndCloses(t *testing.T) {
	fixture := newLeaseFixture(t, "api", false)
	source := &recordingSource{secret: []byte("sentinel-secret")}
	lease, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source)
	if err != nil {
		t.Fatal(err)
	}
	fixture.policy.Profiles[0].CredentialRef = "mutated"
	fixture.policy.Routes[0].Model = "mutated"
	fixture.intent.Route.Model = "mutated"
	if err := lease.WithSecret(context.Background(), func(context.Context, []byte) error { return errors.New("sentinel-secret") }); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("consumer error leaked credential data", err)
	}
	raw, err := canonical.Bytes(lease.Binding())
	if err != nil || strings.Contains(string(raw), "sentinel-secret") || strings.Contains(fmt.Sprintf("%#v", lease), "sentinel-secret") {
		t.Fatal("safe credential projection exposed the secret", err, string(raw), fmt.Sprintf("%#v", lease))
	}
	if err := lease.Close(); err != nil || lease.Close() != nil {
		t.Fatal("lease close was not idempotent", err)
	}
	if err := lease.WithSecret(context.Background(), func(context.Context, []byte) error { return nil }); err == nil {
		t.Fatal("closed lease allowed credential use")
	}
}

func TestLeaseCloseWaitsForInProcessUse(t *testing.T) {
	fixture := newLeaseFixture(t, "api", false)
	lease, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, &recordingSource{secret: []byte("fixture-secret")})
	if err != nil {
		t.Fatal(err)
	}
	entered, release, used := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		used <- lease.WithSecret(context.Background(), func(context.Context, []byte) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	var closed atomic.Bool
	closedResult := make(chan error, 1)
	go func() { err := lease.Close(); closed.Store(true); closedResult <- err }()
	time.Sleep(20 * time.Millisecond)
	if closed.Load() {
		t.Fatal("Close returned while credential callback was active")
	}
	close(release)
	if err := <-used; err != nil {
		t.Fatal(err)
	}
	if err := <-closedResult; err != nil || !closed.Load() {
		t.Fatal("Close did not finish after callback", err)
	}
}

func TestResolveRejectsNilContextAndOversizedSourceWithoutLeak(t *testing.T) {
	fixture := newLeaseFixture(t, "api", false)
	if _, err := Resolve(nil, fixture.path, fixture.policy, fixture.intent, fixture.endpoint, &recordingSource{secret: []byte("secret")}); err == nil {
		t.Fatal("nil context accepted")
	}
	sentinel := strings.Repeat("s", maxSecretBytes+1)
	if _, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, &recordingSource{secret: []byte(sentinel)}); err == nil || strings.Contains(err.Error(), sentinel) {
		t.Fatal("oversized source accepted or leaked", err)
	}
}

func TestResolveClearsPartialSourceBytesAndRedactsSourceError(t *testing.T) {
	fixture := newLeaseFixture(t, "api", false)
	transferred := []byte("sentinel-secret")
	source := &recordingSource{secret: transferred, err: errors.New("sentinel-secret upstream failure")}
	if _, err := Resolve(context.Background(), fixture.path, fixture.policy, fixture.intent, fixture.endpoint, source); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("source failure accepted or leaked", err)
	}
	for _, value := range transferred {
		if value != 0 {
			t.Fatal("partial source bytes were not cleared")
		}
	}
}
