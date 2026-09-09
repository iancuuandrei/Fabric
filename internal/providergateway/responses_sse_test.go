package providergateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseResponsesSSESanitizedM0gSystemResponse(t *testing.T) {
	raw, err := os.ReadFile("testdata/m0g-live-system-02-response.raw")
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256.Sum256(raw); got != [32]byte{0xf7, 0xca, 0x0d, 0xf6, 0xb9, 0x0b, 0x03, 0xb3, 0xc6, 0xa9, 0xe8, 0xb2, 0x0b, 0x9b, 0xfb, 0x41, 0xc0, 0xbc, 0x6b, 0x1d, 0x6c, 0x67, 0xf6, 0x07, 0x75, 0x3d, 0x18, 0xb6, 0xc9, 0xe1, 0x77, 0xb4} {
		t.Fatalf("sanitized response fixture hash changed: %x", got)
	}
	if _, err := ParseResponsesSSEWithOptions(raw, len(raw), 402, ResponsesSSEOptions{RequireTrailingCostPingV1: true}); err == nil {
		t.Fatal("captured profile admitted without content-part completion capability")
	}
	observation, err := ParseResponsesSSEWithOptions(raw, len(raw), 402, ResponsesSSEOptions{RequireTrailingCostPingV1: true, ContentPartCompletesText: true})
	if err != nil {
		t.Fatal(err)
	}
	if observation.ResponseID != "resp_public_fixture" || observation.Model != "muse-spark-1.3-contributor-free" || observation.Status != "completed" || observation.OutputText != "M0_MUSE_ROUTE_OK" || observation.TotalTokens != 402 || observation.Usage.InputTokens != 153 || observation.Usage.OutputTokens != 249 || observation.Usage.ReasoningTokens == nil || *observation.Usage.ReasoningTokens != 233 || len(observation.Reasoning) != 1 || observation.TrailingCostPing == nil || observation.TrailingCostPing.CostDecimal != "0" {
		t.Fatal("captured response evidence differs", observation)
	}
}

func TestParseResponsesSSEContentPartCompletionRejectsContradictions(t *testing.T) {
	raw, err := os.ReadFile("testdata/m0g-live-system-02-response.raw")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(raw)
	mismatched := strings.Replace(valid, `"part":{"type":"output_text","text":"M0_MUSE_ROUTE_OK"`, `"part":{"type":"output_text","text":"substituted"`, 1)
	lateDone := strings.Replace(valid,
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":8",
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":8,\"output_index\":1,\"content_index\":0,\"item_id\":\"msg_public_fixture\",\"text\":\"M0_MUSE_ROUTE_OK\",\"logprobs\":[]}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":9",
		1)
	lateDone = strings.Replace(lateDone, `"type":"response.completed","sequence_number":9`, `"type":"response.completed","sequence_number":10`, 1)
	options := ResponsesSSEOptions{RequireTrailingCostPingV1: true, ContentPartCompletesText: true}
	for name, malformed := range map[string]string{"snapshot mismatch": mismatched, "late duplicate finalizer": lateDone} {
		t.Run(name, func(t *testing.T) {
			if malformed == valid {
				t.Fatal("test mutation did not apply")
			}
			if _, err := ParseResponsesSSEWithOptions([]byte(malformed), len(malformed), 402, options); err == nil {
				t.Fatal("contradictory content completion admitted")
			}
		})
	}
}

func TestParseResponsesSSEReasoningStatusIsOptionalButStageBound(t *testing.T) {
	withoutStatus := string(responsesFunctionFixture())
	withStatus := strings.Replace(withoutStatus, `"type":"reasoning","encrypted_content":"added-cipher"`, `"type":"reasoning","status":"in_progress","encrypted_content":"added-cipher"`, 1)
	withStatus = strings.Replace(withStatus, `"type":"reasoning","encrypted_content":"done-cipher"`, `"type":"reasoning","status":"completed","encrypted_content":"done-cipher"`, 1)
	withStatus = strings.Replace(withStatus, `"type":"reasoning","encrypted_content":"terminal-cipher"`, `"type":"reasoning","status":"completed","encrypted_content":"terminal-cipher"`, 1)
	for name, raw := range map[string]string{"absent": withoutStatus, "stage matched": withStatus} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 20); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, raw := range map[string]string{
		"encrypted omitted": strings.Replace(withStatus, `,"encrypted_content":"added-cipher"`, ``, 1),
		"encrypted null":    strings.Replace(withStatus, `"encrypted_content":"added-cipher"`, `"encrypted_content":null`, 1),
		"encrypted string":  withStatus,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 20); err != nil {
				t.Fatal(err)
			}
		})
	}

	for name, raw := range map[string]string{
		"added completed":      strings.Replace(withStatus, `"status":"in_progress","encrypted_content":"added-cipher"`, `"status":"completed","encrypted_content":"added-cipher"`, 1),
		"done incomplete":      strings.Replace(withStatus, `"status":"completed","encrypted_content":"done-cipher"`, `"status":"incomplete","encrypted_content":"done-cipher"`, 1),
		"terminal in progress": strings.Replace(withStatus, `"status":"completed","encrypted_content":"terminal-cipher"`, `"status":"in_progress","encrypted_content":"terminal-cipher"`, 1),
		"invalid status type":  strings.Replace(withStatus, `"status":"in_progress","encrypted_content":"added-cipher"`, `"status":1,"encrypted_content":"added-cipher"`, 1),
		"malformed encrypted":  strings.Replace(withStatus, `"encrypted_content":"added-cipher"`, `"encrypted_content":1`, 1),
		"unknown field":        strings.Replace(withStatus, `"status":"in_progress","encrypted_content":"added-cipher"`, `"status":"in_progress","unexpected":true,"encrypted_content":"added-cipher"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 20); err == nil {
				t.Fatal("invalid reasoning status shape admitted")
			}
		})
	}
}

func TestParseResponsesSSETextAndUsage(t *testing.T) {
	raw := responsesTextFixture()
	got, err := ParseResponsesSSE(raw, len(raw), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResponseID != "resp_fixture" || got.Model != "provider-model" || got.Status != "completed" || got.FinishReason != "stop" || got.OutputText != "Salut, lume!" || !got.UsageComplete || got.Usage.InputTokens != 7 || got.Usage.OutputTokens != 3 || got.TotalTokens != 10 || len(got.FunctionCalls) != 0 {
		t.Fatal("unexpected Responses observation", got)
	}
	if len(got.TextOutputs) != 1 || got.TextOutputs[0].Index != 0 || got.TextOutputs[0].ItemID != "msg_fixture" || got.TextOutputs[0].Text != "Salut, lume!" {
		t.Fatal("text output item identity lost", got.TextOutputs)
	}
	if got.Usage.CacheReadTokens == nil || *got.Usage.CacheReadTokens != 0 || got.Usage.ReasoningTokens == nil || *got.Usage.ReasoningTokens != 1 || got.Usage.CacheWriteTokens != nil {
		t.Fatal("optional usage subsets lost", got.Usage)
	}
}

func TestParseResponsesSSEMessagePhasePreservesTypedOptionalState(t *testing.T) {
	valid := string(responsesTextFixture())
	if _, err := ParseResponsesSSE([]byte(valid), len(valid), 10); err != nil {
		t.Fatal(err)
	}

	for name, phase := range map[string]string{
		"final_answer": `"final_answer"`,
		"null":         "null",
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.ReplaceAll(valid, `"role":"assistant"}`, `"role":"assistant","phase":`+phase+`}`)
			if raw == valid {
				t.Fatal("phase fixture mutation did not apply")
			}
			got, err := ParseResponsesSSE([]byte(raw), len(raw), 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.TextOutputs) != 1 || string(got.TextOutputs[0].Phase) != phase {
				t.Fatalf("message phase was not preserved: %#v", got.TextOutputs)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"phase":`+phase) {
				t.Fatalf("serialized observation lost phase %s: %s", phase, encoded)
			}
		})
	}
}

func TestParseResponsesSSEFunctionCallDoneNameCapability(t *testing.T) {
	valid := string(responsesFunctionFixture())
	withName := strings.Replace(valid, `,"item_id":"fc_fixture","output_index":1,"sequence_number":80`, `,"item_id":"fc_fixture","name":"lookup","output_index":1,"sequence_number":80`, 1)
	if withName == valid {
		t.Fatal("function-call done name fixture mutation did not apply")
	}
	if _, err := ParseResponsesSSE([]byte(withName), len(withName), 20); err == nil {
		t.Fatal("unconfigured function-call done name extension admitted")
	}
	options := ResponsesSSEOptions{FunctionCallDoneNameV1: true}
	if _, err := ParseResponsesSSEWithOptions([]byte(valid), len(valid), 20, options); err != nil {
		t.Fatal("legacy function-call done event without name rejected", err)
	}
	if _, err := ParseResponsesSSEWithOptions([]byte(withName), len(withName), 20, options); err != nil {
		t.Fatal("configured function-call done name extension rejected", err)
	}
	for name, malformed := range map[string]string{
		"wrong name":   strings.Replace(withName, `"name":"lookup","output_index":1,"sequence_number":80`, `"name":"other","output_index":1,"sequence_number":80`, 1),
		"null name":    strings.Replace(withName, `"name":"lookup","output_index":1,"sequence_number":80`, `"name":null,"output_index":1,"sequence_number":80`, 1),
		"unknown":      strings.Replace(withName, `"name":"lookup","output_index":1,"sequence_number":80`, `"name":"lookup","unexpected":true,"output_index":1,"sequence_number":80`, 1),
		"missing item": strings.Replace(withName, `"item_id":"fc_fixture","name":"lookup","output_index":1,"sequence_number":80`, `"item_id":"missing","name":"lookup","output_index":1,"sequence_number":80`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSEWithOptions([]byte(malformed), len(malformed), 20, options); err == nil {
				t.Fatal("invalid function-call done name extension admitted")
			}
		})
	}
}

func TestParseResponsesSSEMessagePhaseRejectsInvalidAndContradictoryState(t *testing.T) {
	valid := string(responsesTextFixture())
	commentary := strings.ReplaceAll(valid, `"role":"assistant"}`, `"role":"assistant","phase":"commentary"}`)
	cases := map[string]string{
		"unknown value":           strings.ReplaceAll(commentary, `"phase":"commentary"`, `"phase":"internal"`),
		"non-string value":        strings.ReplaceAll(commentary, `"phase":"commentary"`, `"phase":1`),
		"inconsistent snapshot":   strings.Replace(commentary, `"phase":"commentary"`, `"phase":"final_answer"`, 1),
		"null and value mismatch": strings.Replace(strings.ReplaceAll(valid, `"role":"assistant"}`, `"role":"assistant","phase":null}`), `"phase":null`, `"phase":"commentary"`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if raw == valid {
				t.Fatal("invalid phase fixture mutation did not apply")
			}
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 10); err == nil {
				t.Fatal("invalid or contradictory message phase admitted")
			}
		})
	}

	got, err := ParseResponsesSSE([]byte(valid), len(valid), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TextOutputs) != 1 || len(got.TextOutputs[0].Phase) != 0 {
		t.Fatalf("legacy absent phase was not omitted: %#v", got.TextOutputs)
	}
}

func TestParseResponsesSSERejectsCommentaryTextWithoutToolCalls(t *testing.T) {
	valid := string(responsesTextFixture())
	commentary := strings.ReplaceAll(valid, `"role":"assistant"}`, `"role":"assistant","phase":"commentary"}`)
	if _, err := ParseResponsesSSE([]byte(commentary), len(commentary), 10); err == nil {
		t.Fatal("commentary-only message was projected as a final stop response")
	}

	if _, err := ParseResponsesSSE(responsesMixedMessagePhaseFixture(), len(responsesMixedMessagePhaseFixture()), 10); err == nil {
		t.Fatal("mixed commentary/final message was projected as a final stop response")
	}

	final := strings.ReplaceAll(valid, `"role":"assistant"}`, `"role":"assistant","phase":"final_answer"}`)
	got, err := ParseResponsesSSE([]byte(final), len(final), 10)
	if err != nil || got.FinishReason != "stop" || got.OutputText != "Salut, lume!" {
		t.Fatalf("final-answer-only message did not retain legacy stop semantics: %#v, %v", got, err)
	}
}

func TestParseResponsesSSECommentaryWithToolCallPreservesVisibleText(t *testing.T) {
	raw := responsesCommentaryFunctionFixture()
	got, err := ParseResponsesSSE(raw, len(raw), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinishReason != "tool_calls" || got.OutputText != "Visible commentary" || len(got.TextOutputs) != 1 || string(got.TextOutputs[0].Phase) != `"commentary"` || len(got.FunctionCalls) != 1 {
		t.Fatalf("commentary tool-call composite was normalized incorrectly: %#v", got)
	}
}

func TestParseResponsesSSECapturedM2eConfiguredPhaseAndFunctionExtension(t *testing.T) {
	path := os.Getenv("ENGORCH_M2E_RESPONSE_ARTIFACT")
	if path == "" {
		t.Skip("set ENGORCH_M2E_RESPONSE_ARTIFACT to run the retained private M2e body")
	}
	envelopeRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		CapturedBytes  int    `json:"captured_bytes"`
		CapturedSHA256 string `json:"captured_sha256"`
		Body           string `json:"body"`
	}
	if err := json.Unmarshal(envelopeRaw, &envelope); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(envelope.Body)
	if err != nil {
		t.Fatal(err)
	}
	const expectedCapturedBytes = 14607
	const expectedCapturedSHA256 = "1c8e8a760c29f1afd63a5e8c9f889e91a439302c1fd9c23acf080090f026b4df"
	bodyHash := sha256.Sum256(raw)
	if envelope.CapturedBytes != expectedCapturedBytes || envelope.CapturedSHA256 != expectedCapturedSHA256 || len(raw) != expectedCapturedBytes || hex.EncodeToString(bodyHash[:]) != expectedCapturedSHA256 {
		t.Fatalf("retained body identity changed: bytes=%d sha256=%x", len(raw), bodyHash)
	}
	got, err := ParseResponsesSSEWithOptions(raw, len(raw), 6284, ResponsesSSEOptions{RequireTrailingCostPingV1: true, ContentPartCompletesText: true, FunctionCallDoneNameV1: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "muse-spark-1.3-contributor-free" || got.FinishReason != "tool_calls" || got.TotalTokens != 6284 || got.Usage.InputTokens != 6176 || got.Usage.OutputTokens != 108 || got.Usage.ReasoningTokens == nil || *got.Usage.ReasoningTokens != 21 || len(got.FunctionCalls) != 1 || got.FunctionCalls[0].Name != "engorch_candidate_list" || string(got.FunctionCalls[0].Arguments) != `{"after":"","limit":128}` || len(got.TextOutputs) != 1 || string(got.TextOutputs[0].Phase) != `"commentary"` {
		t.Fatalf("retained configured body normalized incorrectly: %#v", got)
	}
	capabilities := responsesCapabilities(true, true)
	capabilities.TrailingCostPingV1 = true
	capabilities.ContentPartCompletesText = true
	capabilities.FunctionCallDoneNameV1 = true
	metadata, err := decodeResponsesAdapterResponse(raw, responsesBinding(t, capabilities), AdapterResponseExpectation{MaxBytes: len(raw), MaxTotalTokens: 6284})
	if err != nil || metadata.ObservedModel != "muse-spark-1.3-contributor-free" || metadata.Finish != "tool_calls" || metadata.Content == nil || len(metadata.Content.ToolCalls) != 1 {
		t.Fatalf("retained configured body failed the adapter response path: %#v, %v", metadata, err)
	}
}

func TestParseResponsesSSEFunctionAndDistinctReasoningSnapshots(t *testing.T) {
	raw := responsesFunctionFixture()
	got, err := ParseResponsesSSE(raw, len(raw), 20)
	if err != nil {
		t.Fatal(err)
	}
	if got.FinishReason != "tool_calls" || got.OutputText != "" || len(got.FunctionCalls) != 1 || got.FunctionCalls[0].ItemID != "fc_fixture" || got.FunctionCalls[0].CallID != "call_fixture" || got.FunctionCalls[0].Name != "lookup" || string(got.FunctionCalls[0].Arguments) != `{"city":"Iași"}` {
		t.Fatal("function evidence differs", got)
	}
	if len(got.Reasoning) != 1 || got.Reasoning[0].DoneEncryptedContent == nil || *got.Reasoning[0].DoneEncryptedContent != "done-cipher" || got.Reasoning[0].TerminalEncryptedContent == nil || *got.Reasoning[0].TerminalEncryptedContent != "terminal-cipher" {
		t.Fatal("reasoning snapshots were not independently retained", got.Reasoning)
	}
	if len(got.Reasoning[0].Summary) != 1 || got.Reasoning[0].Summary[0] != "private plan" {
		t.Fatal("reasoning summary was not reconstructed", got.Reasoning)
	}
}

func TestParseResponsesSSERejectsOrderIdentityAndSnapshotSubstitution(t *testing.T) {
	valid := string(responsesTextFixture())
	cases := map[string]string{
		"event type":  strings.Replace(valid, "event: response.output_text.delta", "event: response.output_text.done", 1),
		"sequence":    strings.Replace(valid, `"sequence_number":4`, `"sequence_number":3`, 1),
		"item":        strings.Replace(valid, `"item_id":"msg_fixture"`, `"item_id":"msg_other"`, 1),
		"done text":   strings.Replace(valid, `"text":"Salut, lume!"`, `"text":"substituted"`, 1),
		"terminal":    strings.Replace(valid, `"model":"provider-model","output":[{"id":"msg_fixture"`, `"model":"other-model","output":[{"id":"msg_fixture"`, 1),
		"truncated":   strings.TrimSuffix(valid, "\n\n"),
		"done marker": strings.Replace(valid, "event: response.completed", "data: [DONE]\n\nevent: response.completed", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 10); err == nil {
				t.Fatal("malformed Responses stream admitted")
			}
		})
	}
}

func TestParseResponsesSSERejectsAmbiguousUnicodeAndUsage(t *testing.T) {
	valid := string(responsesFunctionFixture())
	cases := map[string]string{
		"duplicate": strings.Replace(valid, `"type":"response.created"`, `"type":"response.created","type":"response.created"`, 1),
		"unicode":   strings.Replace(valid, "resp_fixture", `\ud800`, 1),
		"usage sum": strings.Replace(valid, `"total_tokens":20`, `"total_tokens":19`, 1),
		"subset":    strings.Replace(valid, `"cached_tokens":2`, `"cached_tokens":18`, 1),
		"arguments": strings.Replace(valid, `arguments":"{\"city\":\"Iași\"}"`, `arguments":"{\"city\":1,\"city\":2}"`, 2),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSE([]byte(raw), len(raw), 20); err == nil {
				t.Fatal("ambiguous Responses stream admitted")
			}
		})
	}
}

func TestParseResponsesSSEReturnsSanitizedExplicitTerminalErrors(t *testing.T) {
	completed := string(responsesTextFixture())
	for _, statusName := range []string{"failed", "incomplete"} {
		t.Run(statusName, func(t *testing.T) {
			failed := strings.Replace(completed, "event: response.completed", "event: response."+statusName, 1)
			failed = strings.Replace(failed, `"type":"response.completed"`, `"type":"response.`+statusName+`"`, 1)
			status := strings.LastIndex(failed, `"created_at":123,"status":"completed","model"`)
			failed = failed[:status] + strings.Replace(failed[status:], `"created_at":123,"status":"completed","model"`, `"created_at":123,"status":"`+statusName+`","model"`, 1)
			got, err := ParseResponsesSSE([]byte(failed), len(failed), 10)
			var terminal *ResponsesTerminalError
			if !errors.As(err, &terminal) || terminal.Status != statusName || strings.Contains(err.Error(), "secret") || got.Status != statusName || got.ResponseID != "resp_fixture" || !got.UsageComplete {
				t.Fatal("explicit terminal was not distinguished", got, err)
			}
		})
	}

	errorRaw := responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":0}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":1}`,
		`{"type":"error","sequence_number":2,"code":"upstream_error","message":"provider secret","param":null}`,
	)
	got, err := ParseResponsesSSE(errorRaw, len(errorRaw), 10)
	var terminal *ResponsesTerminalError
	if !errors.As(err, &terminal) || terminal.Status != "error" || strings.Contains(err.Error(), "provider secret") || got.Status != "error" || got.UsageComplete {
		t.Fatal("top-level provider error was not safely distinguished", got, err)
	}
}

func TestParseResponsesSSEExactTrailingCostPingProfile(t *testing.T) {
	core := responsesTextFixture()
	withPing := append(append([]byte(nil), core...), []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0.00042\"}\n\n")...)
	got, err := ParseResponsesSSEWithOptions(withPing, len(withPing), 10, ResponsesSSEOptions{RequireTrailingCostPingV1: true})
	if err != nil || got.TrailingCostPing == nil || got.TrailingCostPing.CostDecimal != "0.00042" || got.SizeBytes != len(withPing) {
		t.Fatal("exact trailing cost ping was not retained", got, err)
	}
	if _, err := ParseResponsesSSE(withPing, len(withPing), 10); err == nil {
		t.Fatal("uncontracted trailing ping admitted")
	}

	for name, raw := range map[string][]byte{
		"missing":   core,
		"negative":  append(append([]byte(nil), core...), []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":\"-1\"}\n\n")...),
		"malformed": append(append([]byte(nil), core...), []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":0}\n\n")...),
		"duplicate": append(append(append([]byte(nil), core...), []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n")...), []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n")...),
		"extra":     append(append([]byte(nil), withPing...), []byte("event: other\ndata: {\"type\":\"other\"}\n\n")...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseResponsesSSEWithOptions(raw, len(raw), 10, ResponsesSSEOptions{RequireTrailingCostPingV1: true}); err == nil {
				t.Fatal("invalid trailing cost ping profile admitted")
			}
		})
	}
	ping := []byte("event: ping\ndata: {\"type\":\"ping\",\"cost\":\"0\"}\n\n")
	terminal := bytes.Index(core, []byte("event: response.completed"))
	if terminal < 0 {
		t.Fatal("completed terminal fixture marker missing")
	}
	preterminal := append(append(append([]byte(nil), core[:terminal]...), ping...), core[terminal:]...)
	if _, err := ParseResponsesSSEWithOptions(preterminal, len(preterminal), 10, ResponsesSSEOptions{RequireTrailingCostPingV1: true}); err == nil {
		t.Fatal("pre-terminal trailing-cost ping was admitted")
	}
}

func responsesTextFixture() []byte {
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":0}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":1}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_fixture","type":"message","status":"in_progress","content":[],"role":"assistant"},"sequence_number":2}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":3}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"Salut, ","item_id":"msg_fixture","logprobs":[],"obfuscation":"opaque","output_index":0,"sequence_number":4}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"lume!","item_id":"msg_fixture","logprobs":[],"obfuscation":"opaque2","output_index":0,"sequence_number":7}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_fixture","logprobs":[],"output_index":0,"sequence_number":8,"text":"Salut, lume!"}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Salut, lume!"},"sequence_number":9}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Salut, lume!"}],"role":"assistant"},"sequence_number":10}`,
		`{"type":"response.completed","response":`+responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Salut, lume!"}],"role":"assistant"}]`, `{"input_tokens":7,"input_tokens_details":{"cached_tokens":0},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":1},"total_tokens":10}`)+`,"sequence_number":11}`,
	)
}

func responsesCommentaryFunctionFixture() []byte {
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":0}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":1}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_fixture","type":"message","status":"in_progress","content":[],"role":"assistant","phase":"commentary"},"sequence_number":2}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":3}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"Visible commentary","item_id":"msg_fixture","logprobs":[],"output_index":0,"sequence_number":4}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_fixture","logprobs":[],"output_index":0,"sequence_number":5,"text":"Visible commentary"}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_fixture","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Visible commentary"},"sequence_number":6}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Visible commentary"}],"role":"assistant","phase":"commentary"},"sequence_number":7}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"in_progress","arguments":"","call_id":"call_fixture","name":"lookup"},"sequence_number":8}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"value\":1}","item_id":"fc_fixture","output_index":1,"sequence_number":9}`,
		`{"type":"response.function_call_arguments.done","arguments":"{\"value\":1}","item_id":"fc_fixture","output_index":1,"sequence_number":10}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"value\":1}","call_id":"call_fixture","name":"lookup"},"sequence_number":11}`,
		`{"type":"response.completed","response":`+responsesSnapshot("completed", `[{"id":"msg_fixture","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Visible commentary"}],"role":"assistant","phase":"commentary"},{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"value\":1}","call_id":"call_fixture","name":"lookup"}]`, `{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":1,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}`)+`,"sequence_number":12}`,
	)
}

func responsesMixedMessagePhaseFixture() []byte {
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":0}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":1}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_commentary","type":"message","status":"in_progress","content":[],"role":"assistant","phase":"commentary"},"sequence_number":2}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_commentary","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":3}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"Commentary","item_id":"msg_commentary","logprobs":[],"output_index":0,"sequence_number":4}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_commentary","logprobs":[],"output_index":0,"sequence_number":5,"text":"Commentary"}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_commentary","output_index":0,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Commentary"},"sequence_number":6}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_commentary","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Commentary"}],"role":"assistant","phase":"commentary"},"sequence_number":7}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_final","type":"message","status":"in_progress","content":[],"role":"assistant","phase":"final_answer"},"sequence_number":8}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_final","output_index":1,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":9}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"Final answer","item_id":"msg_final","logprobs":[],"output_index":1,"sequence_number":10}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_final","logprobs":[],"output_index":1,"sequence_number":11,"text":"Final answer"}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_final","output_index":1,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"Final answer"},"sequence_number":12}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_final","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Final answer"}],"role":"assistant","phase":"final_answer"},"sequence_number":13}`,
		`{"type":"response.completed","response":`+responsesSnapshot("completed", `[{"id":"msg_commentary","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Commentary"}],"role":"assistant","phase":"commentary"},{"id":"msg_final","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"Final answer"}],"role":"assistant","phase":"final_answer"}]`, `{"input_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":3}`)+`,"sequence_number":14}`,
	)
}

func responsesFunctionFixture() []byte {
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":10}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":20}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"added-cipher","summary":[]},"sequence_number":30}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""},"sequence_number":31}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_fixture","output_index":0,"summary_index":0,"delta":"private plan","sequence_number":32}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"text":"private plan","sequence_number":33}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"private plan"},"sequence_number":34}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"done-cipher","summary":[{"type":"summary_text","text":"private plan"}]},"sequence_number":40}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"in_progress","arguments":"","call_id":"call_fixture","name":"lookup"},"sequence_number":50}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"city\":","item_id":"fc_fixture","obfuscation":"x","output_index":1,"sequence_number":60}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"Iași\"}","item_id":"fc_fixture","obfuscation":"y","output_index":1,"sequence_number":70}`,
		`{"type":"response.function_call_arguments.done","arguments":"{\"city\":\"Iași\"}","item_id":"fc_fixture","output_index":1,"sequence_number":80}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"lookup"},"sequence_number":90}`,
		`{"type":"response.completed","response":`+responsesSnapshot("completed", `[{"id":"rs_fixture","type":"reasoning","encrypted_content":"terminal-cipher","summary":[{"type":"summary_text","text":"private plan"}]},{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"lookup"}]`, `{"input_tokens":18,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":20}`)+`,"sequence_number":100}`,
	)
}

func responsesSnapshot(status, output, usage string) string {
	return `{"id":"resp_fixture","object":"response","created_at":123,"status":"` + status + `","model":"provider-model","output":` + output + `,"usage":` + usage + `}`
}

func responsesEvents(data ...string) []byte {
	var result strings.Builder
	for _, event := range data {
		var envelope struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(event), &envelope); err != nil {
			panic(err)
		}
		result.WriteString("event: ")
		result.WriteString(envelope.Type)
		result.WriteString("\ndata: ")
		result.WriteString(event)
		result.WriteString("\n\n")
	}
	return []byte(result.String())
}
