package providergateway

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAnthropicMessagesSSETextWithInclusiveUsage(t *testing.T) {
	raw := anthropicTextFixture(true)
	got, err := ParseAnthropicMessagesSSE(raw, len(raw), 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageID != "msg_fixture" || got.Model != "provider-model" || got.Status != "completed" || got.StopReason != "end_turn" || got.OutputText != "Salut!" || got.PingCount != 1 || !got.UsageComplete || got.Usage.InputTokens != 15 || got.Usage.OutputTokens != 5 || got.TotalTokens != 20 {
		t.Fatal("unexpected Anthropic text observation", got)
	}
	if got.Usage.CacheReadTokens == nil || *got.Usage.CacheReadTokens != 3 || got.Usage.CacheWriteTokens == nil || *got.Usage.CacheWriteTokens != 2 || got.Usage.ReasoningTokens != nil {
		t.Fatal("Anthropic cache categories not retained", got.Usage)
	}
}

func TestParseAnthropicMessagesSSEToolThinkingAndRedactedEvidence(t *testing.T) {
	raw := anthropicToolThinkingFixture()
	got, err := ParseAnthropicMessagesSSE(raw, len(raw), 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.StopReason != "tool_use" || len(got.ToolUses) != 1 || got.ToolUses[0].ID != "toolu_fixture" || got.ToolUses[0].Name != "lookup" || string(got.ToolUses[0].Input) != `{"city":"Iași"}` {
		t.Fatal("Anthropic tool evidence differs", got)
	}
	if len(got.Thinking) != 1 || got.Thinking[0].Thinking != "private plan" || got.Thinking[0].Signature != "signed-thinking" || len(got.RedactedThinking) != 1 || got.RedactedThinking[0].Data != "opaque-redaction" || got.Usage.ReasoningTokens == nil || *got.Usage.ReasoningTokens != 4 {
		t.Fatal("Anthropic reasoning evidence differs", got)
	}
}

func TestParseAnthropicMessagesSSEPreservesAbsentCacheUsage(t *testing.T) {
	raw := anthropicTextFixture(false)
	got, err := ParseAnthropicMessagesSSE(raw, len(raw), 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsageComplete || got.Usage.CacheReadTokens != nil || got.Usage.CacheWriteTokens != nil || got.Usage.InputTokens != 10 || got.TotalTokens != 15 {
		t.Fatal("missing cache categories were invented as zero or complete", got)
	}
}

func TestParseAnthropicMessagesSSEAcceptsCompleteToolInputAtBlockStart(t *testing.T) {
	raw := anthropicEvents(
		anthropicMessageStart(true),
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{"city":"Iași"}}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	got, err := ParseAnthropicMessagesSSE(raw, len(raw), 20)
	if err != nil || len(got.ToolUses) != 1 || string(got.ToolUses[0].Input) != `{"city":"Iași"}` {
		t.Fatal("complete tool input at block start not retained", got, err)
	}
}

func TestParseAnthropicMessagesSSERejectsTopologyIdentityAndAmbiguity(t *testing.T) {
	valid := string(anthropicToolThinkingFixture())
	cases := map[string]string{
		"event mismatch":   strings.Replace(valid, "event: content_block_delta", "event: content_block_stop", 1),
		"index":            strings.Replace(valid, `"index":1,"content_block":{"type":"redacted_thinking"`, `"index":3,"content_block":{"type":"redacted_thinking"`, 1),
		"wrong delta":      strings.Replace(valid, `"type":"thinking_delta","thinking":"private plan"`, `"type":"text_delta","text":"private plan"`, 1),
		"missing stop":     strings.Replace(valid, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "", 1),
		"duplicate JSON":   strings.Replace(valid, `"type":"message_start"`, `"type":"message_start","type":"message_start"`, 1),
		"unicode":          strings.Replace(valid, "msg_fixture", `\ud800`, 1),
		"bad arguments":    strings.Replace(valid, `partial_json":"\"Iași\"}"`, `partial_json":"1,\"city\":2}"`, 1),
		"data after stop":  valid + "event: ping\ndata: {\"type\":\"ping\"}\n\n",
		"data after error": string(anthropicEvents(anthropicMessageStart(true), `{"type":"error","error":{"type":"overloaded_error","message":"secret"}}`, `{"type":"ping"}`)),
		"interleaved blocks": string(anthropicEvents(
			anthropicMessageStart(true),
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		)),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAnthropicMessagesSSE([]byte(raw), len(raw), 30); err == nil {
				t.Fatal("malformed Anthropic stream admitted")
			}
		})
	}
}

func TestParseAnthropicMessagesSSEExplicitNonSuccessTerminals(t *testing.T) {
	for _, reason := range []string{"max_tokens", "refusal", "pause_turn", "model_context_window_exceeded"} {
		t.Run(reason, func(t *testing.T) {
			raw := []byte(strings.Replace(string(anthropicTextFixture(true)), `"stop_reason":"end_turn"`, `"stop_reason":"`+reason+`"`, 1))
			got, err := ParseAnthropicMessagesSSE(raw, len(raw), 20)
			var terminal *AnthropicTerminalError
			if !errors.As(err, &terminal) || terminal.Status != reason || got.Status != reason || !got.UsageComplete {
				t.Fatal("Anthropic non-success terminal not distinguished", got, err)
			}
		})
	}
	errorRaw := anthropicEvents(
		anthropicMessageStart(true),
		`{"type":"error","error":{"type":"overloaded_error","message":"provider secret"}}`,
	)
	got, err := ParseAnthropicMessagesSSE(errorRaw, len(errorRaw), 20)
	var terminal *AnthropicTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "overloaded_error" || strings.Contains(err.Error(), "provider secret") || got.Status != "error" {
		t.Fatal("Anthropic stream error not safely distinguished", got, err)
	}
}

func TestParseAnthropicMessagesSSEPinnedRecordedPayloadCompatibility(t *testing.T) {
	textRaw := anthropicFrameRecordedChunks(pinnedAnthropicTextChunks)
	text, err := ParseAnthropicMessagesSSE(textRaw, len(textRaw), 42)
	if err != nil || text.OutputText != "Hello! I'm doing well, thank you for asking. How are you doing today? Is there anything I can help you with?" || text.TotalTokens != 42 || !text.UsageComplete {
		t.Fatal("pinned recorded Anthropic text payloads rejected", text, err)
	}
	toolRaw := anthropicFrameRecordedChunks(pinnedAnthropicToolChunks)
	tool, err := ParseAnthropicMessagesSSE(toolRaw, len(toolRaw), 896)
	if err != nil || len(tool.ToolUses) != 1 || tool.ToolUses[0].ID != "toolu_01KFbKqPYSuAKujiL6mTfzYA" || string(tool.ToolUses[0].Input) != `{"elements": [{"location": "San Francisco", "temperature": 58, "condition": "sunny"}]}` || tool.TotalTokens != 896 {
		t.Fatal("pinned recorded Anthropic tool payloads rejected", tool, err)
	}
	refusalRaw := anthropicFrameRecordedChunks(pinnedAnthropicRefusalChunks)
	refusal, err := ParseAnthropicMessagesSSE(refusalRaw, len(refusalRaw), 23)
	var terminal *AnthropicTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "refusal" || refusal.Status != "refusal" || refusal.TotalTokens != 23 {
		t.Fatal("pinned recorded Anthropic refusal not distinguished", refusal, err)
	}
	revisedRaw := anthropicFrameRecordedChunks(pinnedAnthropicRevisedInputChunks)
	revised, err := ParseAnthropicMessagesSSE(revisedRaw, len(revisedRaw), 63)
	if err != nil || revised.Usage.InputTokens != 61 || revised.Usage.OutputTokens != 2 || revised.TotalTokens != 63 || revised.UsageComplete || revised.Usage.CacheReadTokens != nil || revised.Usage.CacheWriteTokens != nil {
		t.Fatal("pinned revised-input usage or missing-cache semantics differ", revised, err)
	}
}

func anthropicTextFixture(cache bool) []byte {
	return anthropicEvents(
		anthropicMessageStart(cache),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Salut"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
}

func anthropicToolThinkingFixture() []byte {
	return anthropicEvents(
		anthropicMessageStart(true),
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-thinking"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-redaction"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"Iași\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":10,"output_tokens_details":{"thinking_tokens":4}}}`,
		`{"type":"message_stop"}`,
	)
}

func anthropicMessageStart(cache bool) string {
	usage := `{"input_tokens":10,"output_tokens":1}`
	if cache {
		usage = `{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":1}`
	}
	return `{"type":"message_start","message":{"id":"msg_fixture","type":"message","role":"assistant","content":[],"model":"provider-model","stop_reason":null,"stop_sequence":null,"usage":` + usage + `}}`
}

func anthropicEvents(data ...string) []byte {
	var result strings.Builder
	for _, value := range data {
		object, err := anthropicObject([]byte(value))
		if err != nil {
			panic(err)
		}
		typeName, err := anthropicString(object, "type", 256, true)
		if err != nil {
			panic(err)
		}
		result.WriteString("event: ")
		result.WriteString(typeName)
		result.WriteString("\ndata: ")
		result.WriteString(value)
		result.WriteString("\n\n")
	}
	return []byte(result.String())
}

func anthropicFrameRecordedChunks(chunks string) []byte {
	lines := strings.Split(strings.TrimSuffix(chunks, "\n"), "\n")
	return anthropicEvents(lines...)
}

const pinnedAnthropicTextChunks = `{"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_01QC4g3HwBThD4BaNtBckFDJ","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1,"service_tier":"standard","inference_geo":"not_available"}}}
{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}
{"type":"ping"}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"! I"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"'m doing well, thank you for asking"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":". How are you doing today?"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Is"}}
{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" there anything I can help you with?"}}
{"type":"content_block_stop","index":0}
{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":12,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":30}}
{"type":"message_stop"}
`

const pinnedAnthropicToolChunks = `{"type":"message_start","message":{"model":"claude-haiku-4-5-20251001","id":"msg_01K2JbSUMYhez5RHoK9ZCj9U","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":849,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":10,"service_tier":"standard"}}}
{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01KFbKqPYSuAKujiL6mTfzYA","name":"json","input":{}}}
{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}
{"type":"ping"}
{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"elements\": [{\"location\": \"San Francisco\", \"temperature\": 58, \"condition\": \"sunny\"}]"}}
{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"}"}}
{"type":"content_block_stop","index":0}
{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":849,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":47}}
{"type":"message_stop"}
`

const pinnedAnthropicRefusalChunks = `{"type":"message_start","message":{"model":"claude-fable-5","id":"msg_01RefusalStreamAbcdefghijk","type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":18,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"output_tokens":1,"service_tier":"standard","inference_geo":"not_available"}}}
{"type":"ping"}
{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"This request triggered restrictions on violative cyber content and was blocked under Anthropic's Usage Policy.","recommended_model":"claude-fable-5"}},"usage":{"input_tokens":18,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}
{"type":"message_stop"}
`

const pinnedAnthropicRevisedInputChunks = `{"type":"message_start","message":{"content":[],"id":"msg_3196a1cc08de4d76b85b8f5777c0d42b","model":"claude-opus-4-5-20251101","role":"assistant","stop_reason":null,"stop_sequence":null,"type":"message","usage":{"input_tokens":43,"output_tokens":1}}}
{"type":"content_block_start","index":0,"content_block":{"text":"","type":"text"}}
{"type":"content_block_delta","index":0,"delta":{"text":"p","type":"text_delta"}}
{"type":"ping"}
{"type":"content_block_delta","index":0,"delta":{"text":"ong","type":"text_delta"}}
{"type":"content_block_stop","index":0}
{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":61,"output_tokens":2}}
{"type":"message_stop"}
`
