package providergateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
)

func newUnlimitedV2Fixture(t *testing.T) gatewayFixture {
	t.Helper()
	root := t.TempDir()
	profile := access.Profile{Version: 1, Name: "provider-subscription", Kind: "subscription", Runtime: "controller-gateway", Provider: "openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: "writer", Runtime: profile.Runtime, Provider: profile.Provider, Model: "deployment/opaque-model", Effort: "high", AccessID: profileID, Permission: "workspace-write"}
	policy := access.Policy{Version: 1, RunID: strings.Repeat("a", 64), Class: access.Private, Limits: access.Limits{UnlimitedTokens: true, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: strings.Repeat("b", 64), Route: route, Reservation: access.Reservation{UnlimitedTokens: true, BillingMode: "subscription"}}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, intent); err != nil {
		t.Fatal(err)
	}
	model := chatV2Model(false)
	path := filepath.Join(root, "provider.jsonl")
	binding, err := Bind(path, accessPath, policy, intent, v2Endpoint(), model)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayFixture{accessPath: accessPath, path: path, policy: policy, intent: intent, endpoint: v2Endpoint(), model: model, binding: binding}
}

func TestUnlimitedBindingSeparatesAccountingFromTechnicalReservationAndReplaysReceipt(t *testing.T) {
	f := newUnlimitedV2Fixture(t)
	limit, err := ProviderTokenLimit(f.binding)
	if err != nil || limit != 330 || f.binding.ReservedTokens != 0 || f.binding.EngOrchBudget == nil || f.binding.EngOrchBudget.Mode != "unlimited" || f.binding.ProviderReservation == nil || f.binding.ProviderReservation.Tokens != 330 || f.binding.ProviderReservation.Reason != providerReservationReason || !f.binding.ProviderReservation.HardLimit {
		t.Fatal("unlimited accounting and technical reservation were conflated", f.binding, limit, err)
	}
	call := beginV2(t, f, strings.Repeat("c", 64), 10)
	receipt := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: 100, OutputTokens: 10})
	receipt.Semantic = responseSemanticProjection("answer", []ResponseToolIdentity{})
	if err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt); err != nil {
		t.Fatal("unlimited provider receipt was not durable", err)
	}
	state, err := Inspect(f.path)
	if err != nil || state.Pending != nil || !state.Finished || state.Exhausted || state.Aggregate.InputTokens != 100 || state.Aggregate.OutputTokens != 10 {
		t.Fatal("unlimited provider journal did not replay exact usage", state, err)
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	journal := string(raw)
	for _, required := range []string{`"reserved_tokens":0`, `"engorch_budget":{"mode":"unlimited"}`, `"provider_reservation":{`, `"tokens":330`, `"reason":"model_contract_conservative_maximum"`, `"hard_limit":true`} {
		if !strings.Contains(journal, required) {
			t.Fatal("unlimited receipt evidence is ambiguous", required, journal)
		}
	}
}

func TestUnlimitedTechnicalHardLimitIsNotBudgetExhaustion(t *testing.T) {
	f := newUnlimitedV2Fixture(t)
	call := beginV2(t, f, strings.Repeat("c", 64), 10)
	receipt := f.receipt(call, strings.Repeat("d", 64), "stop", Usage{InputTokens: f.model.ContextWindowTokens + 1, OutputTokens: 1})
	receipt.Semantic = responseSemanticProjection("answer", []ResponseToolIdentity{})
	err := Complete(f.path, f.accessPath, f.policy, f.intent, f.binding, call, receipt)
	if err == nil || strings.Contains(strings.ToUpper(err.Error()), "BUDGET") {
		t.Fatal("technical model-contract violation was misclassified as budget exhaustion", err)
	}
	state, inspectErr := Inspect(f.path)
	if inspectErr != nil || state.Pending == nil || state.Exhausted || state.Calls[0].Receipt != nil {
		t.Fatal("technical violation changed durable state or exhausted accounting", state, inspectErr)
	}
}

func TestUnlimitedBindingFailsClosedWithoutExactTechnicalBounds(t *testing.T) {
	f := newUnlimitedV2Fixture(t)
	legacy := ModelContract{Version: 1, Provider: f.model.Provider, Model: f.model.Model, Protocol: OpenAIChatCompletionsAdapter, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10}
	legacyEndpoint := EndpointContract{Version: 1, Provider: f.endpoint.Provider, URL: "https://api.example.test/v1/chat/completions", Protocol: OpenAIChatCompletionsAdapter}
	if _, err := BindingForAccess(f.policy, f.intent, legacyEndpoint, legacy); err == nil {
		t.Fatal("unlimited admission invented a technical reservation for a legacy model")
	}
	missingContext := f.model
	missingContext.ContextWindowTokens = 0
	if _, err := BindingForAccess(f.policy, f.intent, f.endpoint, missingContext); err == nil {
		t.Fatal("unlimited admission accepted a model without a context bound")
	}
}

func TestUnlimitedBindingRejectsMixedOrTamperedBudgetEvidence(t *testing.T) {
	f := newUnlimitedV2Fixture(t)
	for name, mutate := range map[string]func(*Binding){
		"finite marker with technical fields": func(b *Binding) { b.EngOrchBudget.Mode = "finite" },
		"legacy reservation mixed in":         func(b *Binding) { b.ReservedTokens = 330 },
		"technical value changed":             func(b *Binding) { b.ProviderReservation.Tokens-- },
		"technical reason changed":            func(b *Binding) { b.ProviderReservation.Reason = "default_budget" },
		"hard limit omitted":                  func(b *Binding) { b.ProviderReservation.HardLimit = false },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := f.binding
			budget := *candidate.EngOrchBudget
			reservation := *candidate.ProviderReservation
			candidate.EngOrchBudget = &budget
			candidate.ProviderReservation = &reservation
			mutate(&candidate)
			if _, err := candidate.ID(); err == nil {
				t.Fatal("mixed or tampered budget evidence admitted")
			}
		})
	}

	raw, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"tokens":330`, `"tokens":329`, 1)
	if tampered == string(raw) {
		t.Fatal("fixture did not contain technical reservation evidence")
	}
	if err := os.WriteFile(f.path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(f.path); err == nil {
		t.Fatal("tampered durable technical reservation evidence replayed")
	}
}

func TestFiniteBindingIdentityRemainsHistorical(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	want, err := canonical.Hash("harness.provider-gateway-binding.v1", struct {
		Version            int              `json:"version"`
		AccessPolicyID     string           `json:"access_policy_id"`
		AccessInvocationID string           `json:"access_invocation_id"`
		RouteID            string           `json:"route_id"`
		ReservedTokens     int64            `json:"reserved_tokens"`
		EndpointID         string           `json:"endpoint_id"`
		ModelID            string           `json:"model_id"`
		Endpoint           EndpointContract `json:"endpoint"`
		Model              ModelContract    `json:"model"`
	}{f.binding.Version, f.binding.AccessPolicyID, f.binding.AccessInvocationID, f.binding.RouteID, f.binding.ReservedTokens, f.binding.EndpointID, f.binding.ModelID, f.binding.Endpoint, f.binding.Model})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.binding.ID()
	if err != nil || got != want || f.binding.EngOrchBudget != nil || f.binding.ProviderReservation != nil {
		t.Fatal("finite binding identity changed", got, want, f.binding, err)
	}
}
