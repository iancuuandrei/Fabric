package providergateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
)

func terminalStructuredExpectationForTest(t *testing.T) TerminalStructuredOutputExpectation {
	t.Helper()
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`)
	digest := sha256.Sum256(schema)
	return TerminalStructuredOutputExpectation{Version: 1, Name: StructuredOutputToolName, Schema: schema, SchemaSHA256: hex.EncodeToString(digest[:])}
}

// responsesMessageStructuredFixture mirrors responsesFunctionFixture with one
// synthetic advisory message between reasoning and the terminal
// StructuredOutput call (the M2r live shape: reasoning + short text +
// StructuredOutput). No private provider content is embedded.
func responsesMessageStructuredFixture() []byte {
	messageItem := `{"id":"msg_advisory","type":"message","status":"completed","content":[{"type":"output_text","annotations":[],"logprobs":[],"text":"synthetic advisory note"}],"role":"assistant"}`
	functionItem := `{"id":"fc_fixture","type":"function_call","status":"completed","arguments":"{\"city\":\"Iași\"}","call_id":"call_fixture","name":"StructuredOutput"}`
	return responsesEvents(
		`{"type":"response.created","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":10}`,
		`{"type":"response.in_progress","response":`+responsesSnapshot("in_progress", `[]`, `null`)+`,"sequence_number":20}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"added-cipher","summary":[]},"sequence_number":30}`,
		`{"type":"response.reasoning_summary_part.added","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""},"sequence_number":31}`,
		`{"type":"response.reasoning_summary_text.delta","item_id":"rs_fixture","output_index":0,"summary_index":0,"delta":"private plan","sequence_number":32}`,
		`{"type":"response.reasoning_summary_text.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"text":"private plan","sequence_number":33}`,
		`{"type":"response.reasoning_summary_part.done","item_id":"rs_fixture","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"private plan"},"sequence_number":34}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_fixture","type":"reasoning","encrypted_content":"done-cipher","summary":[{"type":"summary_text","text":"private plan"}]},"sequence_number":40}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_advisory","type":"message","status":"in_progress","content":[],"role":"assistant"},"sequence_number":50}`,
		`{"type":"response.content_part.added","content_index":0,"item_id":"msg_advisory","output_index":1,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":""},"sequence_number":51}`,
		`{"type":"response.output_text.delta","content_index":0,"delta":"synthetic advisory note","item_id":"msg_advisory","logprobs":[],"obfuscation":"x","output_index":1,"sequence_number":52}`,
		`{"type":"response.output_text.done","content_index":0,"item_id":"msg_advisory","logprobs":[],"output_index":1,"sequence_number":53,"text":"synthetic advisory note"}`,
		`{"type":"response.content_part.done","content_index":0,"item_id":"msg_advisory","output_index":1,"part":{"type":"output_text","annotations":[],"logprobs":[],"text":"synthetic advisory note"},"sequence_number":54}`,
		`{"type":"response.output_item.done","output_index":1,"item":`+messageItem+`,"sequence_number":55}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_fixture","type":"function_call","status":"in_progress","arguments":"","call_id":"call_fixture","name":"StructuredOutput"},"sequence_number":60}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"city\":","item_id":"fc_fixture","obfuscation":"x","output_index":2,"sequence_number":70}`,
		`{"type":"response.function_call_arguments.delta","delta":"\"Iași\"}","item_id":"fc_fixture","obfuscation":"y","output_index":2,"sequence_number":80}`,
		`{"type":"response.function_call_arguments.done","arguments":"{\"city\":\"Iași\"}","item_id":"fc_fixture","output_index":2,"sequence_number":90}`,
		`{"type":"response.output_item.done","output_index":2,"item":`+functionItem+`,"sequence_number":95}`,
		`{"type":"response.completed","response":`+responsesSnapshot("completed", `[{"id":"rs_fixture","type":"reasoning","encrypted_content":"terminal-cipher","summary":[{"type":"summary_text","text":"private plan"}]},`+messageItem+`,`+functionItem+`]`, `{"input_tokens":18,"input_tokens_details":{"cached_tokens":2},"output_tokens":2,"output_tokens_details":{"reasoning_tokens":2},"total_tokens":20}`)+`,"sequence_number":100}`,
	)
}

func TestTerminalStructuredOutputBindsResponsesToolCatalogAndExplicitChoices(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	binding.Model.ObservedModelAliases = []string{"provider-model"}
	binding.ModelID, _ = binding.Model.ID()
	expectation := terminalStructuredExpectationForTest(t)
	terminalTool, err := StructuredOutputTool(expectation)
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(validResponsesBody(false), &body); err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]any)
	tools = append(tools, map[string]any{
		"type": "function", "name": terminalTool.Name, "description": terminalTool.Description,
		"parameters": json.RawMessage(terminalTool.Parameters), "strict": false,
	})
	body["tools"] = tools
	body["tool_choice"] = "required"
	requiredRaw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	controls := responsesExpectation(false)
	controls.ToolChoice = "required"
	controlBytes, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	expected := AdapterRequestExpectation{
		MaxBytes: int64(len(requiredRaw)), MaxOutputTokens: 128, Tools: responsesTools(), Controls: controlBytes,
		RequiredCapabilities:     &RequiredCapabilities{Tools: true, StructuredOutput: StructuredOutputTextParseRequired},
		TerminalStructuredOutput: &expectation,
	}
	observation, err := ValidateAdapterRequest(requiredRaw, binding, expected)
	if err != nil || observation.ToolCount != 2 {
		t.Fatalf("native terminal required choice was not admitted: %#v, %v", observation, err)
	}
	if _, err := AdapterRequestExpectationID(binding, expected); err != nil {
		t.Fatal("required terminal expectation did not acquire an identity", err)
	}

	body["tool_choice"] = "auto"
	autoRaw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	autoControls := responsesExpectation(false)
	autoControlBytes, err := json.Marshal(autoControls)
	if err != nil {
		t.Fatal(err)
	}
	autoExpected := expected
	autoExpected.Controls = autoControlBytes
	observation, err = ValidateAdapterRequest(autoRaw, binding, autoExpected)
	if err != nil || observation.ToolCount != 2 {
		t.Fatalf("native terminal auto choice was not admitted: %#v, %v", observation, err)
	}
	if _, err := AdapterRequestExpectationID(binding, autoExpected); err != nil {
		t.Fatal("auto terminal expectation did not acquire an identity", err)
	}

	if _, err := ValidateAdapterRequest(requiredRaw, binding, autoExpected); err == nil {
		t.Fatal("Responses terminal request admitted a wire/config tool choice mismatch")
	}

	var missingBody map[string]any
	if err := json.Unmarshal(autoRaw, &missingBody); err != nil {
		t.Fatal(err)
	}
	missingBody["tools"] = missingBody["tools"].([]any)[:1]
	missingRaw, err := json.Marshal(missingBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAdapterRequest(missingRaw, binding, autoExpected); err == nil {
		t.Fatal("Responses terminal request admitted a catalog without StructuredOutput")
	}

	for _, choice := range []string{"none", "", "function:engorch_source_read", "function:StructuredOutput"} {
		t.Run("forbidden choice "+choice, func(t *testing.T) {
			forbiddenControls := responsesExpectation(false)
			forbiddenControls.ToolChoice = choice
			forbiddenBytes, err := json.Marshal(forbiddenControls)
			if err != nil {
				t.Fatal(err)
			}
			forbiddenExpected := autoExpected
			forbiddenExpected.Controls = forbiddenBytes
			if _, err := AdapterRequestExpectationID(binding, forbiddenExpected); err == nil {
				t.Fatal("forbidden terminal tool choice was admitted")
			}
		})
	}
}

func TestTerminalStructuredOutputResponseAllowsEarlierBrokerCallAndBindsFinalCapture(t *testing.T) {
	capabilities := responsesCapabilities(true, true)
	binding := responsesBinding(t, capabilities)
	binding.Model.ObservedModelAliases = []string{"provider-model"}
	binding.ModelID, _ = binding.Model.ID()
	expectation := terminalStructuredExpectationForTest(t)
	expected := AdapterResponseExpectation{MaxBytes: 1 << 20, MaxTotalTokens: 20, TerminalStructuredOutput: &expectation}

	ordinary, err := DecodeAdapterResponse(responsesFunctionFixture(), binding, expected)
	if err != nil || ordinary.Finish != "tool_calls" || ordinary.Semantic == nil || ordinary.Semantic.TerminalTool != nil || len(ordinary.Content.ToolCalls) != 1 {
		t.Fatalf("ordinary broker call was treated as an invalid terminal: %#v, %v", ordinary, err)
	}

	terminalRaw := strings.ReplaceAll(string(responsesFunctionFixture()), `"name":"lookup"`, `"name":"StructuredOutput"`)
	terminal, err := DecodeAdapterResponse([]byte(terminalRaw), binding, expected)
	if err != nil || terminal.Finish != "tool_calls" || terminal.Semantic == nil || terminal.Semantic.TerminalTool == nil || len(terminal.Semantic.ToolCalls) != 1 || terminal.Semantic.TerminalTool.Name != StructuredOutputToolName {
		t.Fatalf("valid terminal capture was not bound: %#v, %v", terminal, err)
	}
	emptyDigest := sha256.Sum256(nil)
	if terminal.Semantic.OutputTextSHA256 != hex.EncodeToString(emptyDigest[:]) {
		t.Fatal("terminal capture carried visible output text")
	}

	// R34: advisory message text may accompany exactly one valid terminal
	// capture (M2r: reasoning + 52-char text + StructuredOutput, rejected
	// before this fix). The text stays hashed in evidence and grants no
	// effect authority; the single validated capture still closes the turn.
	textTerminal, err := DecodeAdapterResponse(responsesMessageStructuredFixture(), binding, expected)
	if err != nil || textTerminal.Semantic == nil || textTerminal.Semantic.TerminalTool == nil || len(textTerminal.Semantic.ToolCalls) != 1 || textTerminal.Semantic.TerminalTool.Name != StructuredOutputToolName || textTerminal.Semantic.OutputTextSHA256 == hex.EncodeToString(emptyDigest[:]) {
		t.Fatalf("terminal capture with advisory text was not bound with text evidence: %#v, %v", textTerminal, err)
	}

	invalidArguments := strings.ReplaceAll(terminalRaw, `\"city\":\"Iași\"`, `\"city\":1`)
	if _, err := DecodeAdapterResponse([]byte(invalidArguments), binding, expected); err == nil {
		t.Fatal("schema-invalid terminal capture was admitted")
	}
	for name, content := range map[string]*ProviderResponseContent{
		"mixed ordinary and terminal": {OutputText: "", ToolCalls: []ProviderToolCallContent{{ID: "ordinary", Name: "lookup", Arguments: json.RawMessage(`{"city":"Iași"}`)}, {ID: "terminal", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}},
		"duplicate terminal":          {OutputText: "", ToolCalls: []ProviderToolCallContent{{ID: "terminal-1", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}, {ID: "terminal-2", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := terminalStructuredOutputIdentity(content, &expectation); err == nil {
				t.Fatal("invalid terminal capture was admitted")
			}
		})
	}
	// Advisory text alongside the single valid capture is retained as evidence
	// and does not block terminal identity.
	if _, err := terminalStructuredOutputIdentity(&ProviderResponseContent{OutputText: "synthetic advisory note", ToolCalls: []ProviderToolCallContent{{ID: "terminal", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}}, &expectation); err != nil {
		t.Fatal("terminal capture with advisory text was rejected", err)
	}

	tampered := *terminal.Semantic
	identity := *tampered.TerminalTool
	identity.ID = "substituted"
	tampered.TerminalTool = &identity
	if err := validateProviderResponseContent(terminal.Content, &tampered, OpenAIResponsesAdapter, expected); err == nil {
		t.Fatal("terminal semantic identity substitution was admitted")
	}
}

func TestTerminalReceiptClosesGatewayWithoutFalseExhaustion(t *testing.T) {
	binding := responsesBinding(t, responsesCapabilities(true, true))
	expectation := terminalStructuredExpectationForTest(t)
	controlExpectation := responsesExpectation(false)
	controlExpectation.MaxOutputTokens = 10
	controlExpectation.ToolChoice = "required"
	controls, err := json.Marshal(controlExpectation)
	if err != nil {
		t.Fatal(err)
	}
	expected := AdapterRequestExpectation{MaxBytes: binding.Model.MaxRequestBytes, MaxOutputTokens: 10, Tools: responsesTools(), Controls: controls, TerminalStructuredOutput: &expectation}
	expectationID, err := AdapterRequestExpectationID(binding, expected)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	call := CallIntent{Version: 1, Sequence: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: strings.Repeat("d", 64), RequestBytes: 256, MaxOutputTokens: 10, RequestExpectationID: expectationID, TerminalStructuredOutput: &expectation}
	call.CallID, err = call.ID()
	if err != nil {
		t.Fatal(err)
	}
	bindingRaw, err := canonical.Bytes(binding)
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest := sha256.Sum256(nil)
	toolDigest := sha256.Sum256([]byte(`{"city":"Iași"}`))
	terminal := ResponseToolIdentity{ID: "terminal", Name: StructuredOutputToolName, ArgumentsSHA256: hex.EncodeToString(toolDigest[:])}
	receipt := CallReceipt{
		Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: call.CallID,
		ResponseSHA256: strings.Repeat("f", 64), ResponseBytes: 128, ResponseID: "response-terminal", ObservedModel: binding.Model.Model,
		Finish: "tool_calls", StreamComplete: true, UsageComplete: true, Usage: Usage{InputTokens: 1, OutputTokens: 1},
		Semantic:             &ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: []ResponseToolIdentity{terminal}, OutputTextSHA256: hex.EncodeToString(emptyDigest[:]), TerminalTool: &terminal, TerminalSchemaSHA256: expectation.SchemaSHA256},
		RequestExpectationID: call.RequestExpectationID,
	}
	boundPayload, _ := canonical.Bytes(binding)
	callPayload, _ := canonical.Bytes(call)
	receiptPayload, _ := canonical.Bytes(receipt)
	state, err := replay([]journal.Event{{Kind: boundEvent, Payload: boundPayload}, {Kind: intentEvent, Payload: callPayload}, {Kind: receiptEvent, Payload: receiptPayload}})
	if err != nil || !state.Finished || state.Exhausted || state.Pending != nil {
		t.Fatalf("terminal receipt did not close gateway cleanly: %#v, %v", state, err)
	}
	if _, err := replay([]journal.Event{{Kind: boundEvent, Payload: bindingRaw}, {Kind: intentEvent, Payload: callPayload}, {Kind: receiptEvent, Payload: receiptPayload}, {Kind: intentEvent, Payload: callPayload}}); err == nil {
		t.Fatal("gateway admitted a call after terminal structured capture")
	}
	if err := validateReceiptSemantic(2, "tool_calls", receipt.Semantic); err != nil {
		t.Fatal(err)
	}
	if validateReceiptSemantic(2, "stop", receipt.Semantic) == nil {
		t.Fatal("terminal capture was accepted with a relabeled stop finish")
	}
	if !reflect.DeepEqual(receipt.Semantic.ToolCalls[0], *receipt.Semantic.TerminalTool) {
		t.Fatal("terminal semantic marker was not bound to the final tool identity")
	}

	ordinaryCall := call
	ordinaryCall.TerminalStructuredOutput = nil
	ordinaryCall.CallID, _ = ordinaryCall.ID()
	ordinaryReceipt := receipt
	ordinaryReceipt.CallID = ordinaryCall.CallID
	ordinaryReceipt.Semantic = receipt.Semantic
	ordinaryReceipt.RequestExpectationID = ordinaryCall.RequestExpectationID
	ordinaryCallPayload, _ := canonical.Bytes(ordinaryCall)
	ordinaryReceiptPayload, _ := canonical.Bytes(ordinaryReceipt)
	if _, err := replay([]journal.Event{{Kind: boundEvent, Payload: boundPayload}, {Kind: intentEvent, Payload: ordinaryCallPayload}, {Kind: receiptEvent, Payload: ordinaryReceiptPayload}}); err == nil {
		t.Fatal("terminal receipt was admitted without an admitted structured output expectation")
	}

	tamperedReceipt := receipt
	tamperedSemantic := *receipt.Semantic
	tamperedSemantic.TerminalSchemaSHA256 = strings.Repeat("1", 64)
	tamperedReceipt.Semantic = &tamperedSemantic
	tamperedReceiptPayload, _ := canonical.Bytes(tamperedReceipt)
	if _, err := replay([]journal.Event{{Kind: boundEvent, Payload: boundPayload}, {Kind: intentEvent, Payload: callPayload}, {Kind: receiptEvent, Payload: tamperedReceiptPayload}}); err == nil {
		t.Fatal("terminal receipt with mismatched schema identity was admitted")
	}
}
