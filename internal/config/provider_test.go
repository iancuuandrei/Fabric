package config

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
)

const providerTOML = `version = 1

[[credentials]]
ref = "team-key"
environment = "ENGORCH_PROVIDER_TEAM_KEY"

[[endpoints]]
name = "primary"
version = 2
provider = "engorch-openai"
url = "https://gateway.example.test/custom/deployments/team/chat?api-version=2026-09-01"
adapter_id = "openai-chat-completions-sse-v1"
session_header = "x-engorch-invocation"

[endpoints.auth]
scheme = "api-key-header"
credential_ref = "team-key"
header_name = "api-key"

[endpoints.public_headers]
user-agent = "engorch-fixture"

[[models]]
name = "planner-model"
version = 2
provider = "engorch-openai"
model = "deployment/opaque-model"
adapter_id = "openai-chat-completions-sse-v1"
adapter_capabilities_json = "{}"
context_window_tokens = 100
max_calls = 3
max_request_bytes = 4096
max_response_bytes = 8192
max_output_tokens = 10

[models.capabilities]
tools = true
reasoning = false
output_cap = true
complete_usage = true

[models.pricing]
currency = "USD"
unit = "micro_usd_per_million_tokens"
max_input_micro_usd_per_million = 1001
max_output_micro_usd_per_million = 2001

[roles.planner]
endpoint = "primary"
model = "planner-model"
adapter_controls_json = "{}"

[roles.planner.variant]
effort = "none"

[roles.planner.required_capabilities]
tools = true
structured_output = "UNSUPPORTED"
`

func TestParseProviderResolvesPublicContractsWithoutCredentialValue(t *testing.T) {
	provider, err := ParseProvider([]byte(providerTOML))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := provider.ResolveRole("planner")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Endpoint.URL != "https://gateway.example.test/custom/deployments/team/chat?api-version=2026-09-01" || resolved.Endpoint.Auth.CredentialRef != "team-key" || resolved.CredentialEnvironment != "ENGORCH_PROVIDER_TEAM_KEY" || resolved.Model.Model != "deployment/opaque-model" || string(resolved.AdapterControls) != "{}" || resolved.Variant.Effort != "none" {
		t.Fatal("resolved provider role differs from operator configuration", resolved)
	}
	tokens, cost, err := provider.RoleReservation("planner")
	if err != nil || tokens != 330 || cost == nil || *cost != 3 {
		t.Fatal("provider reservation mismatch", tokens, cost, err)
	}
	if strings.Contains(providerTOML, "actual-secret") {
		t.Fatal("fixture unexpectedly contains credential material")
	}
}

func TestProviderRoleBindsRuntimeAccessAndReservation(t *testing.T) {
	provider, err := ParseProvider([]byte(providerTOML))
	if err != nil {
		t.Fatal(err)
	}
	runtimeProfiles := map[string]runtime.Profile{"planner": {Runtime: "opencode-http", Provider: "engorch-openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	configuredAccess := &Access{Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: int64ConfigPointer(3)}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}}
	if err := provider.ValidateRoles(runtimeProfiles, configuredAccess); err != nil {
		t.Fatal(err)
	}

	underfunded := *configuredAccess
	underfunded.Invocations = map[string]InvocationLimit{"planner": {Tokens: 329, CostMicroUSD: int64ConfigPointer(3)}}
	if err := provider.ValidateRoles(runtimeProfiles, &underfunded); err == nil {
		t.Fatal("underfunded provider role admitted")
	}
	wrongAccess := *configuredAccess
	wrongAccess.Roles = map[string]string{"planner": "missing"}
	if err := provider.ValidateRoles(runtimeProfiles, &wrongAccess); err == nil {
		t.Fatal("role accepted without exact access profile mapping")
	}
	wrongRuntime := map[string]runtime.Profile{"planner": {Runtime: "opencode-http", Provider: "engorch-other", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	if err := provider.ValidateRoles(wrongRuntime, configuredAccess); err == nil {
		t.Fatal("runtime provider identity drift admitted")
	}
}

func TestProviderRoleSupportsUnprefixedDirectRuntime(t *testing.T) {
	directRaw := strings.ReplaceAll(providerTOML, "engorch-openai", "openai")
	provider, err := ParseProvider([]byte(directRaw))
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]runtime.Profile{"planner": {Runtime: "provider-api", Provider: "openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	role := provider.Roles["planner"]
	role.RequiredCapabilities = &ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	provider.Roles["planner"] = role
	configuredAccess := &Access{Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: int64ConfigPointer(3)}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "provider-api", Provider: "openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}}
	if err := provider.ValidateRoles(profiles, configuredAccess); err != nil {
		t.Fatal(err)
	}
	profiles["planner"] = runtime.Profile{Runtime: "opencode-http", Provider: "openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}
	if err := provider.ValidateRoles(profiles, configuredAccess); err == nil {
		t.Fatal("unprefixed provider admitted through OpenCode runtime")
	}
}

func TestProviderRoleFiniteFramingRequiresModelCapabilityAndDirectRuntime(t *testing.T) {
	directRaw := strings.ReplaceAll(providerTOML, "engorch-openai", "openai")
	provider, err := ParseProvider([]byte(directRaw))
	if err != nil {
		t.Fatal(err)
	}
	role := provider.Roles["planner"]
	role.RequiredCapabilities = &ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	role.ResponseFraming = providergateway.ResponseFramingJSON
	provider.Roles["planner"] = role
	if err := provider.Validate(); err == nil {
		t.Fatal("finite framing admitted without model capability")
	}
	provider.Models[0].Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := provider.ResolveRole("planner")
	if err != nil || resolved.ResponseFraming != providergateway.ResponseFramingJSON || len(resolved.Model.Capabilities.SupportedResponseFramings) != 1 {
		t.Fatal("finite framing capability was not resolved", resolved, err)
	}
	profiles := map[string]runtime.Profile{"planner": {Runtime: "provider-api", Provider: "openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	configuredAccess := &Access{Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: int64ConfigPointer(3)}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "provider-api", Provider: "openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}}
	if err := provider.ValidateRoles(profiles, configuredAccess); err != nil {
		t.Fatal(err)
	}
	opencodeProvider, err := ParseProvider([]byte(providerTOML))
	if err != nil {
		t.Fatal(err)
	}
	opencodeProvider.Models[0].Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	opencodeRole := opencodeProvider.Roles["planner"]
	opencodeRole.ResponseFraming = providergateway.ResponseFramingJSON
	opencodeProvider.Roles["planner"] = opencodeRole
	profiles = map[string]runtime.Profile{"planner": {Runtime: "opencode-http", Provider: "engorch-openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	configuredAccess = &Access{Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: int64ConfigPointer(3)}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}}
	if err := opencodeProvider.ValidateRoles(profiles, configuredAccess); err == nil || !strings.Contains(err.Error(), "response framing") {
		t.Fatal("OpenCode role did not reject finite JSON framing at the runtime boundary", err)
	}
}

func TestProviderParserRejectsUnknownAmbiguousAndCredentialBearingMetadata(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":      strings.Replace(providerTOML, "version = 1", "version = 1\nunknown = true", 1),
		"unknown credential": strings.Replace(providerTOML, `credential_ref = "team-key"`, `credential_ref = "missing"`, 1),
		"credential query":   strings.Replace(providerTOML, `?api-version=2026-09-01`, `?api-key=secret`, 1),
		"duplicate endpoint": providerTOML + `
[[endpoints]]
name = "primary"
version = 2
provider = "engorch-openai"
url = "https://example.test/v1/chat/completions"
adapter_id = "openai-chat-completions-sse-v1"
[endpoints.auth]
scheme = "bearer"
credential_ref = "team-key"
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseProvider([]byte(raw)); err == nil {
				t.Fatal("invalid provider configuration admitted")
			}
		})
	}
}

func TestProviderParserRejectsRuntimeVariantAndExactControlsMismatch(t *testing.T) {
	badVariant := strings.Replace(providerTOML, `effort = "none"`, `effort = "high"`, 1)
	if _, err := ParseProvider([]byte(badVariant)); err == nil {
		t.Fatal("unimplemented chat variant admitted")
	}
	badControls := strings.Replace(providerTOML, `adapter_controls_json = "{}"`, `adapter_controls_json = "{\"unknown\":true}"`, 1)
	if _, err := ParseProvider([]byte(badControls)); err == nil {
		t.Fatal("arbitrary adapter controls admitted")
	}
}

func TestProviderConfigurationPreservesFractionalResponsesControls(t *testing.T) {
	minimum, maximum := "0.1", "0.9"
	capabilities := providergateway.ResponsesModelCapabilities{Version: 1, SystemRoles: []string{"developer"}, Sampling: true, TemperatureMin: &minimum, TemperatureMax: &maximum, TextFormats: []string{"plain"}, KnownExtensions: []string{}, TrailingCostPingV1: false}
	capabilityJSON, err := canonical.Bytes(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	temperature := json.Number("0.50")
	controls := providergateway.ResponsesRequestExpectation{MaxOutputTokens: 10, StateMode: "full-input-stateless", SystemRole: "developer", Temperature: &temperature}
	controlJSON, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	provider := Provider{Version: 1,
		Credentials: []ProviderCredential{{Ref: "responses-key", Environment: "RESPONSES_KEY"}},
		Endpoints:   []ProviderEndpoint{{Name: "responses", Version: 2, Provider: "engorch-openai", URL: "https://api.example.test/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: ProviderAuth{Scheme: "bearer", CredentialRef: "responses-key"}}},
		Models:      []ProviderModel{{Name: "reasoner", Version: 2, Provider: "engorch-openai", Model: "reasoner-alias", AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: ProviderCapabilities{OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: string(capabilityJSON), ContextWindowTokens: 100, MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10}},
		Roles:       map[string]ProviderRole{"planner": {Endpoint: "responses", Model: "reasoner", AdapterControlsJSON: string(controlJSON), Variant: ProviderVariant{Effort: "none", SystemRole: "developer"}, RequiredCapabilities: &ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputUnsupported}}},
	}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	resolved, err := provider.ResolveRole("planner")
	if err != nil || !strings.Contains(string(resolved.AdapterControls), `"temperature":0.50`) {
		t.Fatal("fractional control lexeme was not preserved", string(resolved.AdapterControls), err)
	}
}

func int64ConfigPointer(value int64) *int64 { return &value }

// Unlimited access accounting does not remove technical route or cost limits.
func TestProviderUnlimitedAccountingRequiresExplicitTechnicalContract(t *testing.T) {
	provider, err := ParseProvider([]byte(providerTOML))
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]runtime.Profile{"planner": {Runtime: "opencode-http", Provider: "engorch-openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"}}
	configured := &Access{Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {UnlimitedTokens: true, CostMicroUSD: int64ConfigPointer(3)}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}}
	if err := provider.ValidateRoles(profiles, configured); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []InvocationLimit{{Tokens: 1, UnlimitedTokens: true, CostMicroUSD: int64ConfigPointer(3)}, {UnlimitedTokens: true, CostMicroUSD: int64ConfigPointer(2)}, {Tokens: 329, CostMicroUSD: int64ConfigPointer(3)}} {
		configured.Invocations["planner"] = limit
		if provider.ValidateRoles(profiles, configured) == nil {
			t.Fatal("ambiguous accounting or insufficient reservation admitted", limit)
		}
	}
	configured.Invocations["planner"] = InvocationLimit{UnlimitedTokens: true, CostMicroUSD: int64ConfigPointer(3)}
	provider.Models[0].ContextWindowTokens = 0
	if provider.ValidateRoles(profiles, configured) == nil {
		t.Fatal("unknown technical context bound invented")
	}
}
