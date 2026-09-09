package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"harness.local/engorch/internal/providergateway"
)

func providerConfigurationFixture(protocol ProviderProtocol) ProviderConfigurationSpec {
	return ProviderConfigurationSpec{
		ProviderID: "engorch-operator-selected", ModelID: "generic-model", Protocol: protocol,
		GatewayBaseURL: "http://127.0.0.1:43124/v1", GatewayCapability: strings.Repeat("p", 64),
		ContextWindowTokens: 8192, MaxOutputTokens: 1024, Tools: true,
		TimeoutMillis: 15_000, Variant: ProviderVariantSpec{Name: "none"},
	}
}

func providerToolsFixture() ToolsConfigurationSpec {
	return ToolsConfigurationSpec{
		Endpoint: "http://127.0.0.1:43123/mcp", Bearer: strings.Repeat("t", 64),
		ToolNames: []string{"source_read"}, TimeoutMillis: 5000,
	}
}

func TestBuildAndDecodeProviderToolsConfigurationProtocols(t *testing.T) {
	tests := []struct {
		protocol ProviderProtocol
		npm      string
	}{
		{ProviderProtocolOpenAIChat, "@ai-sdk/openai-compatible"},
		{ProviderProtocolOpenAIResponses, "@ai-sdk/openai"},
		{ProviderProtocolAnthropic, "@ai-sdk/anthropic"},
	}
	for _, test := range tests {
		t.Run(string(test.protocol), func(t *testing.T) {
			provider, tools := providerConfigurationFixture(test.protocol), providerToolsFixture()
			content, err := BuildProviderToolsConfiguration(provider, tools)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := decodeProviderToolsConfiguration([]byte(content), provider, tools)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ProviderID != provider.ProviderID || receipt.ModelID != provider.ModelID || receipt.Protocol != test.protocol ||
				receipt.SDKPackage != test.npm || receipt.GatewayBaseURL != provider.GatewayBaseURL ||
				receipt.ContextWindowTokens != provider.ContextWindowTokens || receipt.MaxOutputTokens != provider.MaxOutputTokens ||
				!receipt.Tools || receipt.Reasoning || receipt.Variant != "none" || receipt.ReasoningEffort != "" ||
				receipt.ToolsConfiguration.MCPServer != ToolsMCPServerName || len(receipt.SHA256) != 64 || receipt.SHA256 != receipt.ToolsConfiguration.SHA256 {
				t.Fatalf("unexpected provider receipt: %+v", receipt)
			}
			encoded, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), provider.GatewayCapability) || strings.Contains(string(encoded), tools.Bearer) {
				t.Fatal("receipt exposed a scoped capability")
			}
		})
	}
}

func TestProviderAnthropicVariantsMatchRequestExpectation(t *testing.T) {
	tests := []struct {
		name      string
		mode      string
		budget    *int64
		effort    string
		reasoning bool
		runtime   int64
	}{
		{name: "enabled", mode: "enabled", budget: int64Pointer(256), effort: "high", reasoning: true, runtime: 768},
		{name: "adaptive", mode: "adaptive", effort: "medium", reasoning: true, runtime: 1024},
		{name: "disabled", mode: "disabled", reasoning: false, runtime: 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, tools := providerConfigurationFixture(ProviderProtocolAnthropic), providerToolsFixture()
			provider.Reasoning = test.reasoning
			variantName := test.effort
			if variantName == "" {
				variantName = "none"
			}
			provider.Variant = ProviderVariantSpec{Name: variantName, Anthropic: &AnthropicVariantOptions{
				ThinkingMode: test.mode, ThinkingBudgetTokens: test.budget, Effort: test.effort,
			}}
			content, err := BuildProviderToolsConfiguration(provider, tools)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := decodeProviderToolsConfiguration([]byte(content), provider, tools)
			if err != nil {
				t.Fatal(err)
			}
			expected := providergateway.AnthropicMessagesRequestExpectation{
				MaxOutputTokens: provider.MaxOutputTokens, ThinkingMode: test.mode,
				ThinkingBudgetTokens: test.budget, Effort: test.effort,
			}
			if receipt.SDKPackage != "@ai-sdk/anthropic" || receipt.SDKVersion != "3.0.111" ||
				receipt.MaxOutputTokens != expected.MaxOutputTokens || receipt.RuntimeOutputTokens != test.runtime ||
				receipt.ThinkingMode != expected.ThinkingMode || !equalOptionalInt64(receipt.ThinkingBudgetTokens, expected.ThinkingBudgetTokens) ||
				receipt.AnthropicEffort != expected.Effort {
				t.Fatalf("Anthropic receipt differs from request expectation: %+v", receipt)
			}
			config, err := wireObject([]byte(content))
			if err != nil {
				t.Fatal(err)
			}
			variant := configuredVariant(t, config, provider)
			thinking, err := wireObject(variant["thinking"])
			if err != nil {
				t.Fatal(err)
			}
			expectedThinkingKeys := []string{"type"}
			if test.budget != nil {
				expectedThinkingKeys = append(expectedThinkingKeys, "budgetTokens")
			}
			if !toolsExactKeys(thinking, expectedThinkingKeys...) {
				t.Fatalf("unexpected Anthropic thinking defaults: %s", variant["thinking"])
			}
			var mode string
			if field(thinking, "type", &mode) != nil || mode != test.mode {
				t.Fatal("Anthropic thinking mode changed")
			}
			if test.effort == "" {
				if !toolsExactKeys(variant, "thinking") {
					t.Fatal("unsupported Anthropic variant defaults emitted")
				}
			} else {
				var effort string
				if !toolsExactKeys(variant, "thinking", "effort") || field(variant, "effort", &effort) != nil || effort != test.effort {
					t.Fatal("Anthropic effort changed")
				}
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }

func configuredVariant(t *testing.T, config map[string]json.RawMessage, provider ProviderConfigurationSpec) map[string]json.RawMessage {
	t.Helper()
	var providers map[string]map[string]json.RawMessage
	if field(config, "provider", &providers) != nil {
		t.Fatal("provider config unavailable")
	}
	var models map[string]map[string]json.RawMessage
	if field(providers[provider.ProviderID], "models", &models) != nil {
		t.Fatal("model config unavailable")
	}
	var variants map[string]map[string]json.RawMessage
	if field(models[provider.ModelID], "variants", &variants) != nil {
		t.Fatal("variant config unavailable")
	}
	return variants[provider.Variant.Name]
}

func TestProviderResponsesReasoningVariantIsExact(t *testing.T) {
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	provider.Reasoning = true
	provider.Variant = ProviderVariantSpec{
		Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"},
	}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeProviderToolsConfiguration([]byte(content), provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Reasoning || receipt.Variant != "high" || receipt.ReasoningEffort != "high" || receipt.ReasoningSummary != "auto" {
		t.Fatalf("reasoning selection missing from receipt: %+v", receipt)
	}
	config, err := wireObject([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	var providers map[string]map[string]json.RawMessage
	if field(config, "provider", &providers) != nil {
		t.Fatal("provider config unavailable")
	}
	var models map[string]map[string]json.RawMessage
	if field(providers[provider.ProviderID], "models", &models) != nil {
		t.Fatal("model config unavailable")
	}
	var variants map[string]map[string]json.RawMessage
	if field(models[provider.ModelID], "variants", &variants) != nil {
		t.Fatal("variant config unavailable")
	}
	var include []string
	var effort, summary string
	if field(variants["high"], "reasoningEffort", &effort) != nil || effort != "high" ||
		field(variants["high"], "reasoningSummary", &summary) != nil || summary != "auto" ||
		field(variants["high"], "include", &include) != nil || !reflect.DeepEqual(include, []string{encryptedReasoningInclude}) {
		t.Fatal("Responses reasoning controls changed")
	}
}

func TestProviderResponsesExplicitControlsAreTypedAndExact(t *testing.T) {
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	temperature, topP := json.Number("0.25"), json.Number("0.875")
	provider.RuntimeAgent = "build"
	provider.Variant = ProviderVariantSpec{Name: "none", Responses: &OpenAIResponsesVariantOptions{
		TextVerbosity: "high", Temperature: &temperature, TopP: &topP,
	}}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := decodeProviderToolsConfiguration([]byte(content), provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RuntimeAgent != "build" || receipt.TextVerbosity != "high" || receipt.Temperature != "0.25" || receipt.TopP != "0.875" {
		t.Fatalf("explicit Responses controls missing from receipt: %+v", receipt)
	}
	var config map[string]json.RawMessage
	if json.Unmarshal([]byte(content), &config) != nil {
		t.Fatal("invalid provider configuration")
	}
	variant := configuredVariant(t, config, provider)
	var verbosity string
	if field(variant, "textVerbosity", &verbosity) != nil || verbosity != "high" {
		t.Fatal("Responses text verbosity was not emitted as a provider option")
	}
	var agents map[string]map[string]json.RawMessage
	if json.Unmarshal(config["agent"], &agents) != nil || len(agents) != 1 {
		t.Fatal("exact sampling agent was not emitted")
	}
	if !providerOptionalNumberEquals(agents["build"]["temperature"], &temperature) ||
		!providerOptionalNumberEquals(agents["build"]["top_p"], &topP) {
		t.Fatal("Responses sampling controls changed", agents["build"])
	}
	var raw map[string]any
	if json.Unmarshal([]byte(content), &raw) != nil || providerModel(raw, provider.ProviderID, provider.ModelID)["temperature"] != true {
		t.Fatal("model sampling capability was not enabled")
	}
}

func TestProviderConfigurationValidationFailsBeforeComposition(t *testing.T) {
	valid := providerConfigurationFixture(ProviderProtocolOpenAIResponses)
	tests := []struct {
		name   string
		mutate func(*ProviderConfigurationSpec)
	}{
		{"native provider collision", func(value *ProviderConfigurationSpec) { value.ProviderID = "openai" }},
		{"unsafe provider identity", func(value *ProviderConfigurationSpec) { value.ProviderID = "engorch-UPPER" }},
		{"missing model", func(value *ProviderConfigurationSpec) { value.ModelID = "" }},
		{"non-loopback gateway", func(value *ProviderConfigurationSpec) { value.GatewayBaseURL = "https://example.test/v1" }},
		{"wrong gateway path", func(value *ProviderConfigurationSpec) { value.GatewayBaseURL = "http://127.0.0.1:43124/v1/responses" }},
		{"short capability", func(value *ProviderConfigurationSpec) { value.GatewayCapability = "short" }},
		{"output exceeds context", func(value *ProviderConfigurationSpec) { value.MaxOutputTokens = value.ContextWindowTokens + 1 }},
		{"unbounded timeout", func(value *ProviderConfigurationSpec) { value.TimeoutMillis = providerMaximumTimeoutMillis + 1 }},
		{"reasoning without controls", func(value *ProviderConfigurationSpec) { value.Reasoning = true }},
		{"controls without reasoning", func(value *ProviderConfigurationSpec) {
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"}}
		}},
		{"chat reasoning unsupported", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolOpenAIChat, true
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"}}
		}},
		{"anthropic reasoning unsupported", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"}}
		}},
		{"enabled Anthropic budget missing", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			value.Variant = ProviderVariantSpec{Name: "high", Anthropic: &AnthropicVariantOptions{ThinkingMode: "enabled", Effort: "high"}}
		}},
		{"enabled Anthropic budget consumes ceiling", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			budget := value.MaxOutputTokens
			value.Variant = ProviderVariantSpec{Name: "high", Anthropic: &AnthropicVariantOptions{ThinkingMode: "enabled", ThinkingBudgetTokens: &budget, Effort: "high"}}
		}},
		{"adaptive Anthropic fixed budget", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			budget := int64(256)
			value.Variant = ProviderVariantSpec{Name: "high", Anthropic: &AnthropicVariantOptions{ThinkingMode: "adaptive", ThinkingBudgetTokens: &budget, Effort: "high"}}
		}},
		{"disabled Anthropic marked reasoning", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			value.Variant = ProviderVariantSpec{Name: "none", Anthropic: &AnthropicVariantOptions{ThinkingMode: "disabled"}}
		}},
		{"Anthropic effort differs from variant", func(value *ProviderConfigurationSpec) {
			value.Protocol, value.Reasoning = ProviderProtocolAnthropic, true
			value.Variant = ProviderVariantSpec{Name: "medium", Anthropic: &AnthropicVariantOptions{ThinkingMode: "adaptive", Effort: "high"}}
		}},
		{"multiple variant protocols", func(value *ProviderConfigurationSpec) {
			value.Reasoning = true
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"}, Anthropic: &AnthropicVariantOptions{ThinkingMode: "adaptive", Effort: "high"}}
		}},
		{"variant effort mismatch", func(value *ProviderConfigurationSpec) {
			value.Reasoning = true
			value.Variant = ProviderVariantSpec{Name: "medium", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"}}
		}},
		{"unsafe summary", func(value *ProviderConfigurationSpec) {
			value.Reasoning = true
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "not safe"}}
		}},
		{"sampling without runtime agent", func(value *ProviderConfigurationSpec) {
			temperature := json.Number("0.25")
			value.Variant = ProviderVariantSpec{Name: "none", Responses: &OpenAIResponsesVariantOptions{Temperature: &temperature}}
		}},
		{"reasoning sampling silently omitted by pinned SDK", func(value *ProviderConfigurationSpec) {
			value.Reasoning = true
			value.RuntimeAgent = "build"
			temperature := json.Number("0.25")
			value.Variant = ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto", Temperature: &temperature}}
		}},
		{"temperature above protocol range", func(value *ProviderConfigurationSpec) {
			value.RuntimeAgent = "build"
			temperature := json.Number("2.01")
			value.Variant = ProviderVariantSpec{Name: "none", Responses: &OpenAIResponsesVariantOptions{Temperature: &temperature}}
		}},
		{"top p above protocol range", func(value *ProviderConfigurationSpec) {
			value.RuntimeAgent = "build"
			topP := json.Number("1.01")
			value.Variant = ProviderVariantSpec{Name: "none", Responses: &OpenAIResponsesVariantOptions{TopP: &topP}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if _, err := BuildProviderToolsConfiguration(value, providerToolsFixture()); err == nil {
				t.Fatal("invalid provider configuration was accepted")
			}
		})
	}
}

func TestReadProviderToolsConfigurationIsOneExactRead(t *testing.T) {
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIChat), providerToolsFixture()
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		user, password, ok := request.BasicAuth()
		if request.Method != http.MethodGet || request.URL.Path != "/config" || request.URL.RawQuery != "" || !ok || user != "fixture" || password != "secret" {
			t.Error("unexpected provider readback request")
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(content))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.ReadProviderToolsConfiguration(context.Background(), provider, tools); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider readback used %d requests", calls.Load())
	}
}

func TestReadProviderToolsConfigurationSnapshotsInputsBeforeHTTP(t *testing.T) {
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	provider.Reasoning = true
	provider.Variant = ProviderVariantSpec{
		Name: "high", Responses: &OpenAIResponsesVariantOptions{Effort: "high", Summary: "auto"},
	}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		provider.Variant.Responses.Effort = "mutated"
		tools.ToolNames[0] = "mutated"
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(content))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	receipt, err := client.ReadProviderToolsConfiguration(context.Background(), provider, tools)
	if err != nil {
		t.Fatal("caller mutation changed in-flight admission:", err)
	}
	if receipt.ReasoningEffort != "high" || !reflect.DeepEqual(receipt.ToolsConfiguration.ToolIDs, []string{"engorch_source_read"}) {
		t.Fatalf("admission did not retain its initial snapshot: %+v", receipt)
	}
}

func TestDecodeProviderToolsConfigurationRejectsSubstitution(t *testing.T) {
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if json.Unmarshal([]byte(content), &base) != nil {
		t.Fatal("invalid fixture config")
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"enabled fallback", func(value map[string]any) { value["enabled_providers"] = []string{provider.ProviderID, "foreign"} }},
		{"model fallback", func(value map[string]any) { value["small_model"] = "foreign/model" }},
		{"provider fallback", func(value map[string]any) { value["provider"].(map[string]any)["foreign"] = map[string]any{} }},
		{"SDK substitution", func(value map[string]any) { providerMap(value, provider.ProviderID)["npm"] = "@ai-sdk/anthropic" }},
		{"gateway substitution", func(value map[string]any) {
			providerOptions(value, provider.ProviderID)["baseURL"] = "http://127.0.0.1:43125/v1"
		}},
		{"capability substitution", func(value map[string]any) {
			providerOptions(value, provider.ProviderID)["apiKey"] = strings.Repeat("x", 64)
		}},
		{"model expansion", func(value map[string]any) { providerModels(value, provider.ProviderID)["foreign"] = map[string]any{} }},
		{"limit substitution", func(value map[string]any) {
			providerModel(value, provider.ProviderID, provider.ModelID)["limit"] = map[string]any{"context": 8192, "output": 2048}
		}},
		{"variant expansion", func(value map[string]any) {
			providerModel(value, provider.ProviderID, provider.ModelID)["variants"] = map[string]any{"none": map[string]any{}, "high": map[string]any{}}
		}},
		{"unknown provider option", func(value map[string]any) {
			providerOptions(value, provider.ProviderID)["headers"] = map[string]string{"X": "y"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cloneRaw, _ := json.Marshal(base)
			var value map[string]any
			_ = json.Unmarshal(cloneRaw, &value)
			test.mutate(value)
			tampered, _ := json.Marshal(value)
			if _, err := decodeProviderToolsConfiguration(tampered, provider, tools); err == nil {
				t.Fatal("provider substitution was accepted")
			}
		})
	}
}

func providerMap(config map[string]any, provider string) map[string]any {
	return config["provider"].(map[string]any)[provider].(map[string]any)
}

func providerOptions(config map[string]any, provider string) map[string]any {
	return providerMap(config, provider)["options"].(map[string]any)
}

func providerModels(config map[string]any, provider string) map[string]any {
	return providerMap(config, provider)["models"].(map[string]any)
}

func providerModel(config map[string]any, provider, model string) map[string]any {
	return providerModels(config, provider)[model].(map[string]any)
}
