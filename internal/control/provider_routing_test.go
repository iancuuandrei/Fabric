package control

import (
	"errors"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
)

func TestResolveProviderRoutingBindsExactDirectAdapterAndReservation(t *testing.T) {
	c := providerRoutingConfig("provider-api", "openai")
	route, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1)
	if err != nil {
		t.Fatal(err)
	}
	if route.Profile != c.Planner || route.Intent.Route.Role != "planner" || route.Intent.Reservation.InvocationID == "" || route.Intent.Reservation.Tokens != 330 || route.Gateway.AccessInvocationID != route.Intent.Reservation.InvocationID || route.Gateway.Model.AdapterID != providergateway.OpenAIChatCompletionsAdapter || route.RequestExpectation.RequiredCapabilities == nil || route.RequestExpectation.RequiredCapabilities.StructuredOutput != providergateway.StructuredOutputTextParseRequired || route.CredentialEnvironment != "ENGORCH_PROVIDER_TEAM_KEY" || route.OpenCode != nil {
		t.Fatal("resolved direct provider route is incomplete or substituted", route)
	}
	route.RequestExpectation.Controls[0] = '['
	if c.Provider.Roles["planner"].AdapterControlsJSON != "{}" {
		t.Fatal("resolved controls alias operator configuration")
	}
	if _, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "writer", strings.Repeat("b", 64), 1); err == nil {
		t.Fatal("unconfigured writer silently borrowed planner route")
	}
}

func TestResolveProviderRoutingCarriesConfiguredFiniteFraming(t *testing.T) {
	c := providerRoutingConfig("provider-api", "openai")
	model := c.Provider.Models[0]
	model.Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	c.Provider.Models[0] = model
	role := c.Provider.Roles["planner"]
	role.ResponseFraming = providergateway.ResponseFramingJSON
	c.Provider.Roles["planner"] = role
	resolved, expectation, err := ConfiguredProviderExpectation(c, "planner")
	if err != nil || resolved.ResponseFraming != providergateway.ResponseFramingJSON || expectation.ResponseFraming != providergateway.ResponseFramingJSON {
		t.Fatal("configured finite framing was not resolved", resolved, expectation, err)
	}
	route, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1)
	if err != nil || route.ProviderRole.ResponseFraming != providergateway.ResponseFramingJSON || route.RequestExpectation.ResponseFraming != providergateway.ResponseFramingJSON {
		t.Fatal("controller route lost finite framing", route, err)
	}
}

func TestResolveProviderRoutingRejectsSSEForJSONOnlyModel(t *testing.T) {
	c := providerRoutingConfig("provider-api", "openai")
	model := c.Provider.Models[0]
	model.Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	c.Provider.Models[0] = model
	if _, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1); !errors.Is(err, providergateway.ErrCapabilityUnavailable) {
		t.Fatal("JSON-only model admitted omitted legacy SSE route", err)
	}
	role := c.Provider.Roles["planner"]
	role.ResponseFraming = providergateway.ResponseFramingSSE
	c.Provider.Roles["planner"] = role
	if _, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1); !errors.Is(err, providergateway.ErrCapabilityUnavailable) {
		t.Fatal("JSON-only model admitted explicit SSE route", err)
	}
}

func TestResolveProviderRoutingRejectsFiniteFramingForOpenCode(t *testing.T) {
	c := providerRoutingConfig("opencode-http", "engorch-openai")
	model := c.Provider.Models[0]
	model.Capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}
	c.Provider.Models[0] = model
	role := c.Provider.Roles["planner"]
	role.ResponseFraming = providergateway.ResponseFramingJSON
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	c.Provider.Roles["planner"] = role
	c.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("c", 64), StateRoot: `D:\state\opencode`}
	if _, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1); err == nil {
		t.Fatal("OpenCode route admitted non-SSE framing")
	}
}

func TestResolveProviderRoutingSurfacesPinnedOpenCodeHost(t *testing.T) {
	c := providerRoutingConfig("opencode-http", "engorch-openai")
	role := c.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	c.Provider.Roles["planner"] = role
	c.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("c", 64), StateRoot: `D:\state\opencode`}
	route, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 2)
	if err != nil {
		t.Fatal(err)
	}
	if route.OpenCode == nil || *route.OpenCode != *c.OpenCode || route.RequestExpectation.RequiredCapabilities == nil || !route.RequestExpectation.RequiredCapabilities.Tools {
		t.Fatal("OpenCode host or tool capability missing", route)
	}
	route.OpenCode.Executable = `D:\changed.exe`
	if c.OpenCode.Executable != `D:\tools\opencode.exe` {
		t.Fatal("resolved OpenCode host aliases operator configuration")
	}
}

func providerRoutingConfig(selectedRuntime, providerID string) config.Config {
	cost, totalCost := int64(3), int64(10)
	return config.Config{
		Version: 2, Repository: "provider-project", BaseBranch: "main",
		Planner:      runtime.Profile{Runtime: selectedRuntime, Provider: providerID, Model: "deployment/opaque-model", Effort: "none", Role: "planner"},
		Verification: []config.Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 120}},
		Access:       &config.Access{Class: access.Private, Limits: access.Limits{Tokens: 1000, CostMicroUSD: &totalCost, Concurrency: 1}, Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: &cost}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: selectedRuntime, Provider: providerID, CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}},
		Provider: &config.Provider{Version: 1,
			Credentials: []config.ProviderCredential{{Ref: "team-key", Environment: "ENGORCH_PROVIDER_TEAM_KEY"}},
			Endpoints:   []config.ProviderEndpoint{{Name: "primary", Version: 2, Provider: providerID, URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "team-key"}}},
			Models:      []config.ProviderModel{{Name: "planner-model", Version: 2, Provider: providerID, Model: "deployment/opaque-model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: config.ProviderCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: "{}", ContextWindowTokens: 100, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10, Pricing: &config.ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}}},
			Roles:       map[string]config.ProviderRole{"planner": {Endpoint: "primary", Model: "planner-model", AdapterControlsJSON: "{}", Variant: config.ProviderVariant{Effort: "none"}, RequiredCapabilities: &config.ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}}},
		},
	}
}

func TestResolveProviderRoutingPreservesUnlimitedAccounting(t *testing.T) {
	for _, selected := range []string{"provider-api", "opencode-http"} {
		t.Run(selected, func(t *testing.T) {
			provider := "openai"
			if selected == "opencode-http" {
				provider = "engorch-openai"
			}
			c := providerRoutingConfig(selected, provider)
			c.Access.Limits.Tokens = 0
			c.Access.Limits.UnlimitedTokens = true
			limit := c.Access.Invocations["planner"]
			limit.Tokens = 0
			limit.UnlimitedTokens = true
			c.Access.Invocations["planner"] = limit
			if selected == "opencode-http" {
				role := c.Provider.Roles["planner"]
				role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
				c.Provider.Roles["planner"] = role
				c.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("c", 64), StateRoot: `D:\state\opencode`}
			}
			routing, err := ResolveProviderRouting(c, strings.Repeat("a", 64), "planner", strings.Repeat("b", 64), 1)
			if err != nil {
				t.Fatal(err)
			}
			if !routing.Intent.Reservation.UnlimitedTokens || routing.Intent.Reservation.Tokens != 0 || routing.Gateway.ReservedTokens != 0 || routing.Gateway.ProviderReservation == nil || routing.Gateway.ProviderReservation.Tokens != 330 {
				t.Fatal("unlimited route accounting lost", routing)
			}
			expected, err := providergateway.BindingForAccess(routing.Policy, routing.Intent, routing.ProviderRole.Endpoint, routing.ProviderRole.Model)
			if err != nil {
				t.Fatal(err)
			}
			gotID, _ := routing.Gateway.ID()
			expectedID, _ := expected.ID()
			if gotID != expectedID {
				t.Fatal("controller and gateway disagree")
			}
		})
	}
}
