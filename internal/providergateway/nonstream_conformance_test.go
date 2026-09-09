package providergateway

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesJSONRejectsMalformedBoundsAndTerminalContradictions(t *testing.T) {
	usage := `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10}`
	message := `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"ok"}],"role":"assistant"}]`
	valid := responsesSnapshot("completed", message, usage)
	cases := map[string]string{
		"duplicate root identity": strings.Replace(valid, `"id":"resp_fixture"`, `"id":"other","id":"resp_fixture"`, 1),
		"contradictory error":     strings.TrimSuffix(valid, "}") + `,"error":{"code":"server_error","message":"secret"}}`,
		"incomplete details":      strings.TrimSuffix(valid, "}") + `,"incomplete_details":{"reason":"max_output_tokens"}}`,
		"unfinished item":         strings.Replace(valid, `"status":"completed","content"`, `"status":"in_progress","content"`, 1),
		"usage sum":               strings.Replace(valid, `"total_tokens":10`, `"total_tokens":9`, 1),
		"duplicate item":          responsesSnapshot("completed", strings.TrimSuffix(message, "]")+`,`+strings.TrimPrefix(message, "["), usage),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesJSON([]byte(raw), len(raw), 10); err == nil {
				t.Fatal("malformed or contradictory Responses object admitted")
			}
		})
	}
	if _, err := ParseResponsesJSON([]byte(valid), len(valid)-1, 10); err == nil {
		t.Fatal("Responses object exceeding its byte bound admitted")
	}
	if _, err := ParseResponsesJSON([]byte(valid), len(valid), 9); err == nil {
		t.Fatal("Responses object exceeding its token bound admitted")
	}
}

func TestAnthropicJSONRejectsMalformedBoundsAndTopology(t *testing.T) {
	valid := `{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"provider-model","stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}`
	cases := map[string]string{
		"duplicate root identity":  strings.Replace(valid, `"id":"msg_fixture"`, `"id":"other","id":"msg_fixture"`, 1),
		"tool finish without tool": strings.Replace(valid, `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, 1),
		"tool under text finish":   strings.Replace(valid, `[{"type":"text","text":"ok"}]`, `[{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{}}]`, 1),
		"duplicate tool identity":  strings.Replace(strings.Replace(valid, `[{"type":"text","text":"ok"}]`, `[{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{}},{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{}}]`, 1), `"stop_reason":"end_turn"`, `"stop_reason":"tool_use"`, 1),
		"missing output usage":     strings.Replace(valid, `,"output_tokens":3`, "", 1),
		"usage overflow":           strings.Replace(valid, `"output_tokens":3`, `"output_tokens":4`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseAnthropicMessagesJSON([]byte(raw), len(raw), 10); err == nil {
				t.Fatal("malformed Anthropic object admitted")
			}
		})
	}
	if _, err := ParseAnthropicMessagesJSON([]byte(valid), len(valid)-1, 10); err == nil {
		t.Fatal("Anthropic object exceeding its byte bound admitted")
	}
	incompleteUsage := []byte(strings.Replace(valid, `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0`, "", 1))
	binding := qualifyJSONResponseBinding(t, anthropicBinding(t, anthropicCapabilities()))
	if _, err := DecodeAdapterResponse(incompleteUsage, binding, AdapterResponseExpectation{MaxBytes: len(incompleteUsage), MaxTotalTokens: 10, ResponseFraming: ResponseFramingJSON}); err == nil {
		t.Fatal("incomplete Anthropic accounting admitted through the common response boundary")
	}
}

func TestFiniteJSONPreservesResponsesCacheAndReasoningParity(t *testing.T) {
	want, err := ParseResponsesSSE(responsesFunctionFixture(), len(responsesFunctionFixture()), 20)
	if err != nil {
		t.Fatal(err)
	}
	finite := []byte(responsesSnapshot("completed", `[{"id":"rs_fixture","type":"reasoning","encrypted_content":"terminal-cipher","summary":[{"type":"summary_text","text":"private plan"}]},{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"lookup"}]`, `{"input_tokens":18,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":20}`))
	got, err := ParseResponsesJSON(finite, len(finite), 20)
	if err != nil || !reflect.DeepEqual(got.Usage, want.Usage) || len(got.Reasoning) != 1 || len(want.Reasoning) != 1 || got.Reasoning[0].ItemID != want.Reasoning[0].ItemID || len(got.Reasoning[0].Summary) != 1 || got.Reasoning[0].Summary[0] != want.Reasoning[0].Summary[0] || got.Reasoning[0].TerminalEncryptedContent == nil || *got.Reasoning[0].TerminalEncryptedContent != "terminal-cipher" {
		t.Fatal("Responses JSON cache or reasoning projection differs from streaming", got, want, err)
	}
}

func TestFiniteJSONPreservesAnthropicCacheAndReasoningParity(t *testing.T) {
	wantRaw := anthropicToolThinkingFixture()
	want, err := ParseAnthropicMessagesSSE(wantRaw, len(wantRaw), 30)
	if err != nil {
		t.Fatal(err)
	}
	finite := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"thinking","thinking":"private plan","signature":"signed-thinking"},{"type":"redacted_thinking","data":"opaque-redaction"},{"type":"tool_use","id":"toolu_fixture","name":"lookup","input":{"city":"Iași"}}],"model":"provider-model","stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":10,"output_tokens_details":{"thinking_tokens":4}}}`)
	got, err := ParseAnthropicMessagesJSON(finite, len(finite), 30)
	if err != nil || !reflect.DeepEqual(got.Usage, want.Usage) || len(got.Thinking) != 1 || len(want.Thinking) != 1 || got.Thinking[0] != want.Thinking[0] || len(got.RedactedThinking) != 1 || len(want.RedactedThinking) != 1 || got.RedactedThinking[0] != want.RedactedThinking[0] {
		t.Fatal("Anthropic JSON cache or reasoning projection differs from streaming", got, want, err)
	}
}

func TestFiniteJSONEnforcesStrictStructuredOutput(t *testing.T) {
	capabilities := responsesCapabilities(false, false)
	capabilities.TextFormats = []string{"json_schema", "plain"}
	binding := qualifyJSONResponseBinding(t, responsesBinding(t, capabilities))
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"answer":{"type":"number"}},"required":["answer"],"type":"object"}`)
	required := &RequiredCapabilities{StructuredOutput: StructuredOutputStrictSchema, SchemaName: "answer", Schema: schema}
	usage := `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":10}`
	response := func(text string) []byte {
		quoted, _ := json.Marshal(text)
		raw := responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":`+string(quoted)+`}],"role":"assistant"}]`, usage)
		return []byte(strings.Replace(raw, `"model":"provider-model"`, `"model":"any-qualified-model"`, 1))
	}
	valid := response(`{"answer":1}`)
	if _, err := DecodeAdapterResponse(valid, binding, AdapterResponseExpectation{MaxBytes: len(valid), MaxTotalTokens: 10, RequiredCapabilities: required, ResponseFraming: ResponseFramingJSON}); err != nil {
		t.Fatal("valid strict JSON response rejected", err)
	}
	invalid := response(`{"answer":"one"}`)
	if _, err := DecodeAdapterResponse(invalid, binding, AdapterResponseExpectation{MaxBytes: len(invalid), MaxTotalTokens: 10, RequiredCapabilities: required, ResponseFraming: ResponseFramingJSON}); err == nil {
		t.Fatal("schema-invalid JSON response admitted")
	}
}

func qualifyJSONResponseBinding(t *testing.T, binding Binding) Binding {
	t.Helper()
	capabilities := *binding.Model.Capabilities
	capabilities.StructuredOutputModes = append([]StructuredOutputMode(nil), binding.Model.Capabilities.StructuredOutputModes...)
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

func TestLegacySSEExpectationGoldenIdentityAndReplayBinding(t *testing.T) {
	const goldenLegacyExpectationID = "b2cd34ad1a9ef7e0a10ad04710a457e807aedf61a533a0a498046cea89dc97a2"
	fixture := newV2BindingFixture(t, "api")
	expected := AdapterRequestExpectation{MaxBytes: 4096, MaxOutputTokens: 10, Controls: json.RawMessage("{}")}
	got, err := AdapterRequestExpectationID(fixture.binding, expected)
	if err != nil || got != goldenLegacyExpectationID {
		t.Fatal("legacy SSE expectation identity changed", got, goldenLegacyExpectationID, err)
	}
	call, err := BeginWithExpectation(fixture.path, fixture.accessPath, fixture.policy, fixture.intent, fixture.binding, strings.Repeat("d", 64), 256, expected)
	if err != nil || call.RequestExpectationID != goldenLegacyExpectationID {
		t.Fatal("legacy SSE call did not bind the golden expectation", call, err)
	}
	state, err := Inspect(fixture.path)
	if err != nil || state.Pending == nil || state.Pending.RequestExpectationID != goldenLegacyExpectationID {
		t.Fatal("legacy SSE expectation identity was not replayed", state, err)
	}
}

func TestAnthropicJSONDistinguishesExplicitTerminalReason(t *testing.T) {
	raw := []byte(`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"partial"}],"model":"provider-model","stop_reason":"max_tokens","stop_sequence":null,"usage":{"input_tokens":7,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}`)
	got, err := ParseAnthropicMessagesJSON(raw, len(raw), 10)
	var terminal *AnthropicTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "max_tokens" || got.Status != "max_tokens" || !got.UsageComplete {
		t.Fatal("Anthropic complete non-success terminal was not distinguished", got, err)
	}
}
