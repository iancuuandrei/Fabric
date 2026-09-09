package providergateway

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestChatJSONMatchesStreamingFinalProjection(t *testing.T) {
	stream := stream(
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Salut!"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"provider-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1,"model":"provider-model","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
	)
	finite := []byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"Salut!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	want, err := ParseChatCompletionSSE(stream, len(stream), 10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseChatCompletionJSON(finite, len(finite), 10)
	if err != nil || got.ResponseID != want.ResponseID || got.Model != want.Model || got.FinishReason != want.FinishReason || got.OutputText != want.OutputText || got.Usage != want.Usage || len(got.ToolCalls) != 0 {
		t.Fatal("Chat framing projections differ", got, want, err)
	}
}

func TestResponsesJSONMatchesStreamingFinalProjection(t *testing.T) {
	finite := []byte(responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Salut, lume!"}],"role":"assistant"}]`, `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10}`))
	want, err := ParseResponsesSSE(responsesTextFixture(), len(responsesTextFixture()), 10)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseResponsesJSON(finite, len(finite), 10)
	if err != nil || got.ResponseID != want.ResponseID || got.Model != want.Model || got.Status != want.Status || got.FinishReason != want.FinishReason || got.OutputText != want.OutputText || got.Usage.InputTokens != want.Usage.InputTokens || got.Usage.OutputTokens != want.Usage.OutputTokens || got.Usage.CacheReadTokens == nil || want.Usage.CacheReadTokens == nil || *got.Usage.CacheReadTokens != *want.Usage.CacheReadTokens || len(got.TextOutputs) != 1 || got.TextOutputs[0].ItemID != want.TextOutputs[0].ItemID || len(got.TextOutputs[0].Phase) != 0 {
		t.Fatal("Responses framing projections differ", got, want, err)
	}
}

func TestResponsesJSONRejectsMessagePhaseMetadata(t *testing.T) {
	valid := responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Salut, lume!"}],"role":"assistant"}]`, `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10}`)
	for name, phase := range map[string]string{
		"commentary":   `"commentary"`,
		"final_answer": `"final_answer"`,
		"null":         "null",
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(strings.Replace(valid, `"role":"assistant"}`, `"role":"assistant","phase":`+phase+`}`, 1))
			if _, err := ParseResponsesJSON(raw, len(raw), 10); err == nil {
				t.Fatal("Responses JSON message phase admitted")
			}
		})
	}
}

func TestAnthropicJSONMatchesStreamingFinalProjection(t *testing.T) {
	finite := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"Salut!"}],"model":"provider-model","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":5}}`)
	wantRaw := anthropicTextFixture(true)
	want, err := ParseAnthropicMessagesSSE(wantRaw, len(wantRaw), 20)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAnthropicMessagesJSON(finite, len(finite), 20)
	if err != nil || got.MessageID != want.MessageID || got.Model != want.Model || got.Status != want.Status || got.StopReason != want.StopReason || got.OutputText != want.OutputText || got.Usage.InputTokens != want.Usage.InputTokens || got.Usage.OutputTokens != want.Usage.OutputTokens || !got.UsageComplete {
		t.Fatal("Anthropic framing projections differ", got, want, err)
	}
}

func TestFiniteJSONRejectsAmbiguityBoundsAndTopology(t *testing.T) {
	validChat := `{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	for name, raw := range map[string]string{
		"duplicate": strings.Replace(validChat, `"id":"chatcmpl-fixture"`, `"id":"other","id":"chatcmpl-fixture"`, 1),
		"model":     strings.Replace(validChat, `"model":"provider-model"`, `"model":""`, 1),
		"usage":     strings.Replace(validChat, `"total_tokens":5`, `"total_tokens":4`, 1),
		"topology":  strings.Replace(validChat, `"finish_reason":"stop"`, `"finish_reason":"tool_calls"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseChatCompletionJSON([]byte(raw), len(raw), 5); err == nil {
				t.Fatal("invalid finite Chat response admitted")
			}
		})
	}
	if _, err := ParseChatCompletionJSON([]byte(validChat), len(validChat)-1, 5); err == nil {
		t.Fatal("oversized finite response admitted")
	}
}

func TestAllAdapterRequestValidatorsBindFiniteFraming(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		binding := jsonCapableBinding(t, newV2BindingFixture(t, "api").binding)
		raw := []byte(`{"model":"deployment/opaque-model","messages":[{"role":"user","content":"hello"}],"max_tokens":10,"stream":false}`)
		if _, err := validateChatCompletionRequestForFraming(raw, int64(len(raw)), binding, 10, nil, ChatRequestExpectation{}, ResponseFramingJSON); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("responses", func(t *testing.T) {
		capabilities := responsesCapabilities(true, true)
		binding := jsonCapableBinding(t, responsesBinding(t, capabilities))
		raw := validResponsesBody(true)
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object["stream"] = false
		raw, _ = json.Marshal(object)
		if _, err := validateResponsesRequestForFraming(raw, int64(len(raw)), binding, capabilities, responsesExpectation(true), responsesTools(), ResponseFramingJSON); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("anthropic", func(t *testing.T) {
		capabilities := anthropicCapabilities()
		binding := jsonCapableBinding(t, anthropicBinding(t, capabilities))
		raw := []byte(strings.Replace(string(validAnthropicBody()), `"stream":true`, `"stream":false`, 1))
		if _, err := validateAnthropicMessagesRequestForFraming(raw, int64(len(raw)), binding, capabilities, anthropicExpectation(), anthropicTools(), ResponseFramingJSON); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAllAdapterJSONResponsesPassCommonIdentitySemanticAndUsageBoundary(t *testing.T) {
	t.Run("chat", func(t *testing.T) {
		binding := jsonCapableBinding(t, newV2BindingFixture(t, "api").binding)
		raw := []byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"deployment/opaque-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
		assertFiniteAdapterProjection(t, binding, raw, "chatcmpl-fixture", "deployment/opaque-model", "ok", 7, 3)
	})
	t.Run("responses", func(t *testing.T) {
		binding := jsonCapableBinding(t, responsesBinding(t, responsesCapabilities(true, true)))
		raw := []byte(strings.Replace(responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"ok"}],"role":"assistant"}]`, `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10}`), `"model":"provider-model"`, `"model":"any-qualified-model"`, 1))
		assertFiniteAdapterProjection(t, binding, raw, "resp_fixture", "any-qualified-model", "ok", 7, 3)
	})
	t.Run("anthropic", func(t *testing.T) {
		binding := jsonCapableBinding(t, anthropicBinding(t, anthropicCapabilities()))
		raw := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"qualified-model","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":5,"cache_creation_input_tokens":1,"cache_read_input_tokens":1,"output_tokens":3}}`)
		assertFiniteAdapterProjection(t, binding, raw, "msg_fixture", "qualified-model", "ok", 7, 3)
	})
}

func TestFiniteJSONPreservesNativeToolIdentityAndArguments(t *testing.T) {
	chat := []byte(`{"id":"chatcmpl-fixture","object":"chat.completion","created":1,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_fixture","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Iași\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`)
	chatGot, err := ParseChatCompletionJSON(chat, len(chat), 10)
	if err != nil || chatGot.FinishReason != "tool_calls" || len(chatGot.ToolCalls) != 1 || chatGot.ToolCalls[0].ID != "call_fixture" || chatGot.ToolCalls[0].Name != "lookup" || string(chatGot.ToolCalls[0].Arguments) != `{"city":"Iași"}` {
		t.Fatal("Chat tool identity differs", chatGot, err)
	}

	responses := []byte(responsesSnapshot("completed", `[{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"lookup"}]`, `{"input_tokens":7,"input_tokens_details":null,"output_tokens":3,"output_tokens_details":null,"total_tokens":10}`))
	responsesGot, err := ParseResponsesJSON(responses, len(responses), 10)
	if err != nil || responsesGot.FinishReason != "tool_calls" || len(responsesGot.FunctionCalls) != 1 || responsesGot.FunctionCalls[0].ItemID != "fc_fixture" || responsesGot.FunctionCalls[0].CallID != "call_fixture" || responsesGot.FunctionCalls[0].Name != "lookup" || string(responsesGot.FunctionCalls[0].Arguments) != `{"city":"Iași"}` {
		t.Fatal("Responses tool identity differs", responsesGot, err)
	}

	anthropic := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{"city":"Iași"}}],"model":"provider-model","stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}`)
	anthropicGot, err := ParseAnthropicMessagesJSON(anthropic, len(anthropic), 10)
	if err != nil || anthropicGot.StopReason != "tool_use" || len(anthropicGot.ToolUses) != 1 || anthropicGot.ToolUses[0].ID != "toolu_fixture" || anthropicGot.ToolUses[0].Name != "lookup" || string(anthropicGot.ToolUses[0].Input) != `{"city":"Iași"}` {
		t.Fatal("Anthropic tool identity differs", anthropicGot, err)
	}
}

func assertFiniteAdapterProjection(t *testing.T, binding Binding, raw []byte, responseID, model, text string, input, output int64) {
	t.Helper()
	got, err := DecodeAdapterResponse(raw, binding, AdapterResponseExpectation{MaxBytes: len(raw), MaxTotalTokens: input + output, ResponseFraming: ResponseFramingJSON})
	if err != nil || got.ResponseID != responseID || got.ObservedModel != model || got.Status != "completed" || got.Finish != "stop" || !got.UsageComplete || got.Usage.InputTokens != input || got.Usage.OutputTokens != output || got.Content == nil || got.Content.OutputText != text || got.Semantic == nil || len(got.Semantic.ToolCalls) != 0 {
		t.Fatal("finite adapter projection differs", got, err)
	}
}

func TestResponseFramingIsExplicitAndLegacyExpectationIdentityIsStable(t *testing.T) {
	f := newV2BindingFixture(t, "api")
	f.binding = jsonCapableBinding(t, f.binding)
	legacy := AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}")}
	legacyID, err := AdapterRequestExpectationID(f.binding, legacy)
	if err != nil {
		t.Fatal(err)
	}
	explicitSSE := legacy
	explicitSSE.ResponseFraming = ResponseFramingSSE
	jsonExpected := legacy
	jsonExpected.ResponseFraming = ResponseFramingJSON
	sseID, sseErr := AdapterRequestExpectationID(f.binding, explicitSSE)
	jsonID, jsonErr := AdapterRequestExpectationID(f.binding, jsonExpected)
	if sseErr != nil || jsonErr != nil || legacyID == sseID || legacyID == jsonID || sseID == jsonID {
		t.Fatal("framing was not explicit and identity-bearing", legacyID, sseID, jsonID, sseErr, jsonErr)
	}
	invalid := legacy
	invalid.ResponseFraming = "chunked-json"
	if _, err := AdapterRequestExpectationID(f.binding, invalid); err == nil {
		t.Fatal("unknown response framing admitted")
	}
}

func TestExplicitModelFramingSetRejectsUnlistedLegacySSE(t *testing.T) {
	binding := newV2BindingFixture(t, "api").binding
	capabilities := *binding.Model.Capabilities
	capabilities.SupportedResponseFramings = []ResponseFraming{ResponseFramingJSON}
	binding.Model.Capabilities = &capabilities
	binding.ModelID, _ = binding.Model.ID()
	legacy := AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}")}
	if _, err := AdapterRequestExpectationID(binding, legacy); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("JSON-only model admitted omitted legacy SSE framing", err)
	}
	legacy.ResponseFraming = ResponseFramingSSE
	if _, err := AdapterRequestExpectationID(binding, legacy); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("JSON-only model admitted explicit SSE framing", err)
	}
	legacy.ResponseFraming = ResponseFramingJSON
	if _, err := AdapterRequestExpectationID(binding, legacy); err != nil {
		t.Fatal("JSON-only model rejected configured JSON framing", err)
	}
}

func jsonCapableBinding(t *testing.T, binding Binding) Binding {
	t.Helper()
	capabilities := *binding.Model.Capabilities
	capabilities.SupportedResponseFramings = []ResponseFraming{ResponseFramingJSON, ResponseFramingSSE}
	binding.Model.Capabilities = &capabilities
	modelID, err := binding.Model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding.ModelID = modelID
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	return binding
}
