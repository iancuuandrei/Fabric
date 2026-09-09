package providertransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/providergateway"
)

func terminalExpectationForTransportTest(t *testing.T) providergateway.TerminalStructuredOutputExpectation {
	t.Helper()
	schema := json.RawMessage(`{"additionalProperties":false,"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`)
	digest := sha256.Sum256(schema)
	return providergateway.TerminalStructuredOutputExpectation{Version: 1, Name: providergateway.StructuredOutputToolName, Schema: schema, SchemaSHA256: hex.EncodeToString(digest[:])}
}

func terminalTransportRequest(t *testing.T, fixture transportFixture) transportFixture {
	t.Helper()
	expectation := terminalExpectationForTransportTest(t)
	terminalTool, err := providergateway.StructuredOutputTool(expectation)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(fixture.request.Body, &body); err != nil {
		t.Fatal(err)
	}
	body["tool_choice"] = "required"
	tools := body["tools"].([]any)
	tools = append(tools, map[string]any{"type": "function", "name": terminalTool.Name, "description": terminalTool.Description, "parameters": json.RawMessage(terminalTool.Parameters), "strict": false})
	body["tools"] = tools
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var controls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal(fixture.request.Expectation.Controls, &controls); err != nil {
		t.Fatal(err)
	}
	controls.ToolChoice = "required"
	controlBytes, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Body = raw
	fixture.request.Expectation.MaxBytes = int64(len(raw))
	fixture.request.Expectation.Controls = controlBytes
	fixture.request.Expectation.TerminalStructuredOutput = &expectation
	return fixture
}

func TestExecuteInvalidTerminalStructuredOutputStaysPendingAndNeverResends(t *testing.T) {
	var calls int
	response := strings.ReplaceAll(string(validResponsesToolSSE("provider-wire-model")), `"name":"engorch_lookup"`, `"name":"StructuredOutput"`)
	response = strings.ReplaceAll(response, `\"city\":\"Iași\"`, `\"city\":1`)
	server := newTLSServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(response))
	})
	fixture := terminalTransportRequest(t, newResponsesTransportFixture(t, server))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(err, ErrPending) || len(result.Body) != 0 || calls != 1 {
		t.Fatal("invalid terminal response did not remain unresolved after one POST", result, err, calls)
	}
	state, err := providergateway.Inspect(fixture.gatewayPath)
	if err != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
		t.Fatal("invalid terminal response did not retain pending gateway state", state, err)
	}
	result, err = fixture.client.Execute(ctx, fixture.request)
	if !errors.Is(err, ErrPending) || len(result.Body) != 0 || calls != 1 {
		t.Fatal("pending invalid terminal response was resent", result, err, calls)
	}
}
