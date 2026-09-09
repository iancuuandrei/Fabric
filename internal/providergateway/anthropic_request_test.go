package providergateway

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

func anthropicCapabilities() AnthropicMessagesModelCapabilities {
	thinkingMin, thinkingMax := int64(16), int64(64)
	return AnthropicMessagesModelCapabilities{
		Version: 1, FunctionTools: true, SystemBlocks: true,
		ThinkingModes: []string{"adaptive", "enabled"}, ThinkingBudgetMin: &thinkingMin, ThinkingBudgetMax: &thinkingMax,
		EffortValues: []string{"high", "low"}, ToolChoiceModes: []string{"any", "auto", "tool"},
		ToolStrict: true, DisableParallelTools: true, StopSequences: true,
		SamplingParameters:         []string{},
		RequireCacheUsageBreakdown: true,
	}
}

func anthropicBinding(t *testing.T, capabilities AnthropicMessagesModelCapabilities) Binding {
	t.Helper()
	raw, err := canonical.Bytes(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/messages", AdapterID: AnthropicMessagesAdapter, Auth: &AuthContract{Scheme: "api-key-header", CredentialRef: "KEY", HeaderName: "x-api-key"}}
	reasoning := len(capabilities.ThinkingModes) > 0 || len(capabilities.EffortValues) > 0
	model := ModelContract{Version: 2, Provider: "fixture", Model: "qualified-model", AdapterID: AnthropicMessagesAdapter, Capabilities: &ModelCapabilities{Tools: capabilities.FunctionTools, Reasoning: reasoning, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: raw, ContextWindowTokens: 8192, MaxCalls: 4, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 512}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 4096, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	return binding
}

func anthropicTools() []RequestTool {
	return []RequestTool{{Name: "source_read", Description: "Read admitted source.", Parameters: json.RawMessage(`{"additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`)}}
}

func anthropicExpectation() AnthropicMessagesRequestExpectation {
	budget := int64(32)
	parallel, strict := false, false
	return AnthropicMessagesRequestExpectation{MaxOutputTokens: 128, RequireSystem: true, ThinkingMode: "enabled", ThinkingBudgetTokens: &budget, Effort: "high", ToolChoice: "auto", DisableParallelTools: &parallel, FunctionToolStrict: &strict, StopSequences: []string{"END"}}
}

func validAnthropicBody() []byte {
	return []byte(`{"model":"qualified-model","max_tokens":128,"stream":true,"system":[{"type":"text","text":"Follow policy."}],"thinking":{"type":"enabled","budget_tokens":32},"output_config":{"effort":"high"},"tools":[{"name":"source_read","description":"Read admitted source.","input_schema":{"additionalProperties":false,"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"},"strict":false}],"tool_choice":{"type":"auto","disable_parallel_tool_use":false},"stop_sequences":["END"],"messages":[{"role":"user","content":[{"type":"text","text":"Inspect."}]},{"role":"assistant","content":[{"type":"thinking","thinking":"bounded thought","signature":"signed"},{"type":"text","text":"Checking."},{"type":"tool_use","id":"toolu_1","name":"source_read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"result"},{"type":"text","text":"Continue."}]}]}`)
}

func TestValidateAnthropicMessagesRequestTextToolsThinking(t *testing.T) {
	capabilities := anthropicCapabilities()
	binding := anthropicBinding(t, capabilities)
	raw := validAnthropicBody()
	observation, err := ValidateAnthropicMessagesRequest(raw, int64(len(raw)), binding, capabilities, anthropicExpectation(), anthropicTools())
	if err != nil {
		t.Fatal(err)
	}
	if observation.MessageCount != 3 || observation.ToolCount != 1 || !observation.HasThinking || observation.Model != "qualified-model" {
		t.Fatal("unexpected observation", observation)
	}
}

func TestValidateAnthropicMessagesRequestFractionalSamplingBound(t *testing.T) {
	capabilities := AnthropicMessagesModelCapabilities{Version: 1, SystemBlocks: true, ThinkingModes: []string{}, EffortValues: []string{}, ToolChoiceModes: []string{}, SamplingParameters: []string{"temperature"}, RequireCacheUsageBreakdown: true}
	minimum, maximum := "0.1", "0.9"
	capabilities.TemperatureMin, capabilities.TemperatureMax = &minimum, &maximum
	binding := anthropicBinding(t, capabilities)
	temperature := json.Number("0.50")
	expected := AnthropicMessagesRequestExpectation{MaxOutputTokens: 64, RequireSystem: true, Temperature: &temperature}
	raw := []byte(`{"model":"qualified-model","max_tokens":64,"stream":true,"system":[{"type":"text","text":"Policy."}],"temperature":0.5,"messages":[{"role":"user","content":[{"type":"text","text":"Hello."}]}]}`)
	if _, err := ValidateAnthropicMessagesRequest(raw, int64(len(raw)), binding, capabilities, expected, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAnthropicMessagesRequestDistinguishesDisabledFromOmittedThinking(t *testing.T) {
	capabilities := AnthropicMessagesModelCapabilities{
		Version: 1, ThinkingModes: []string{"disabled"}, EffortValues: []string{},
		ToolChoiceModes: []string{}, SamplingParameters: []string{}, RequireCacheUsageBreakdown: true,
	}
	binding := anthropicBinding(t, capabilities)
	disabled := AnthropicMessagesRequestExpectation{MaxOutputTokens: 64, ThinkingMode: "disabled"}
	disabledRaw := []byte(`{"model":"qualified-model","max_tokens":64,"stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":[{"type":"text","text":"Hello."}]}]}`)
	if _, err := ValidateAnthropicMessagesRequest(disabledRaw, int64(len(disabledRaw)), binding, capabilities, disabled, nil); err != nil {
		t.Fatal(err)
	}
	omitted := AnthropicMessagesRequestExpectation{MaxOutputTokens: 64}
	omittedRaw := []byte(`{"model":"qualified-model","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"Hello."}]}]}`)
	if _, err := ValidateAnthropicMessagesRequest(omittedRaw, int64(len(omittedRaw)), binding, capabilities, omitted, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAnthropicMessagesRequest(omittedRaw, int64(len(omittedRaw)), binding, capabilities, disabled, nil); err == nil {
		t.Fatal("disabled thinking expectation accepted omitted wire field")
	}
	if _, err := ValidateAnthropicMessagesRequest(disabledRaw, int64(len(disabledRaw)), binding, capabilities, omitted, nil); err == nil {
		t.Fatal("omission expectation accepted explicit disabled thinking")
	}
}

func TestValidateAnthropicMessagesRequestRejectsControlDrift(t *testing.T) {
	capabilities := anthropicCapabilities()
	binding := anthropicBinding(t, capabilities)
	expected := anthropicExpectation()
	for name, mutate := range map[string]func(map[string]any){
		"model":     func(body map[string]any) { body["model"] = "substitute" },
		"cap":       func(body map[string]any) { body["max_tokens"] = float64(129) },
		"stream":    func(body map[string]any) { body["stream"] = false },
		"thinking":  func(body map[string]any) { delete(body, "thinking") },
		"effort":    func(body map[string]any) { body["output_config"].(map[string]any)["effort"] = "low" },
		"choice":    func(body map[string]any) { body["tool_choice"].(map[string]any)["type"] = "any" },
		"extension": func(body map[string]any) { body["metadata"] = map[string]any{"x": "y"} },
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(validAnthropicBody(), &body); err != nil {
				t.Fatal(err)
			}
			mutate(body)
			raw, _ := json.Marshal(body)
			if _, err := ValidateAnthropicMessagesRequest(raw, 1<<20, binding, capabilities, expected, anthropicTools()); err == nil {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestValidateAnthropicMessagesRequestRejectsToolHistoryDrift(t *testing.T) {
	capabilities := anthropicCapabilities()
	binding := anthropicBinding(t, capabilities)
	expected := anthropicExpectation()
	for name, mutate := range map[string]func(map[string]any){
		"unmatched result": func(body map[string]any) {
			body["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["tool_use_id"] = "other"
		},
		"duplicate call": func(body map[string]any) {
			assistant := body["messages"].([]any)[1].(map[string]any)["content"].([]any)
			body["messages"].([]any)[1].(map[string]any)["content"] = append(assistant, assistant[2])
		},
		"tool result after text": func(body map[string]any) {
			content := body["messages"].([]any)[2].(map[string]any)["content"].([]any)
			body["messages"].([]any)[2].(map[string]any)["content"] = []any{content[1], content[0]}
		},
		"unsupported image": func(body map[string]any) {
			body["messages"].([]any)[0].(map[string]any)["content"] = []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64"}}}
		},
		"explicit false error flag": func(body map[string]any) {
			body["messages"].([]any)[2].(map[string]any)["content"].([]any)[0].(map[string]any)["is_error"] = false
		},
		"text after tool use": func(body map[string]any) {
			content := body["messages"].([]any)[1].(map[string]any)["content"].([]any)
			body["messages"].([]any)[1].(map[string]any)["content"] = []any{content[0], content[2], content[1]}
		},
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			if err := json.Unmarshal(validAnthropicBody(), &body); err != nil {
				t.Fatal(err)
			}
			mutate(body)
			raw, _ := json.Marshal(body)
			if _, err := ValidateAnthropicMessagesRequest(raw, 1<<20, binding, capabilities, expected, anthropicTools()); err == nil {
				t.Fatal("invalid history accepted")
			}
		})
	}
}

func TestValidateAnthropicMessagesCapabilitiesExactAndBound(t *testing.T) {
	capabilities := anthropicCapabilities()
	raw, err := canonical.Bytes(capabilities)
	if err != nil || validateAnthropicMessagesModelCapabilities(raw) != nil {
		t.Fatal("valid capability rejected", err)
	}
	changed := capabilities
	changed.EffortValues = []string{"low"}
	if _, err := ValidateAnthropicMessagesRequest(validAnthropicBody(), 1<<20, anthropicBinding(t, capabilities), changed, anthropicExpectation(), anthropicTools()); err == nil {
		t.Fatal("unbound capabilities accepted")
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{}`),
		json.RawMessage(`{"version":1,"function_tools":false,"system_blocks":true,"thinking_modes":[],"effort_values":[],"tool_choice_modes":[],"tool_strict":false,"disable_parallel_tools":false,"stop_sequences":false,"sampling_parameters":[],"require_cache_usage_breakdown":false}`),
	} {
		if validateAnthropicMessagesModelCapabilities(invalid) == nil {
			t.Fatal("invalid capability accepted", string(invalid))
		}
	}
}
