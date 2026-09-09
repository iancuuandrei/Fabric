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

	invalidArguments := strings.ReplaceAll(terminalRaw, `\"city\":\"Iași\"`, `\"city\":1`)
	if _, err := DecodeAdapterResponse([]byte(invalidArguments), binding, expected); err == nil {
		t.Fatal("schema-invalid terminal capture was admitted")
	}
	for name, content := range map[string]*ProviderResponseContent{
		"mixed ordinary and terminal": {OutputText: "", ToolCalls: []ProviderToolCallContent{{ID: "ordinary", Name: "lookup", Arguments: json.RawMessage(`{"city":"Iași"}`)}, {ID: "terminal", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}},
		"duplicate terminal":          {OutputText: "", ToolCalls: []ProviderToolCallContent{{ID: "terminal-1", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}, {ID: "terminal-2", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}},
		"terminal text":               {OutputText: "unexpected", ToolCalls: []ProviderToolCallContent{{ID: "terminal", Name: StructuredOutputToolName, Arguments: json.RawMessage(`{"city":"Iași"}`)}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := terminalStructuredOutputIdentity(content, &expectation); err == nil {
				t.Fatal("invalid terminal capture was admitted")
			}
		})
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
