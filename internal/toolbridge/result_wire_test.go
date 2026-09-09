package toolbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEncodeToolResultMatchesServerHTTPWire(t *testing.T) {
	requestID := json.RawMessage(`"call\u002d1"`)
	result := Result{JSON: json.RawMessage(`{"html":"<tag>","quote":"\"","path":"C:\\tmp\\file"}`), IsError: true}
	want, err := EncodeToolResult(requestID, result)
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "wire", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) { return result, nil },
	})
	response := server.post(t, `{"jsonrpc":"2.0","id":"call\u002d1","method":"tools/call","params":{"name":"wire","arguments":{}}}`, true)
	if response.StatusCode != http.StatusOK {
		t.Fatal("unexpected status", response.StatusCode)
	}
	if got := readResponse(t, response); !bytes.Equal(got, want) {
		t.Fatalf("server and codec wire differ\n got: %s\nwant: %s", got, want)
	}
	if !bytes.Contains(want, []byte(`"text":"{\"html\":\"\u003ctag\u003e\",\"quote\":\"\\\"\",\"path\":\"C:\\\\tmp\\\\file\"}"`)) || !bytes.Contains(want, []byte(`"structuredContent":{"html":"\u003ctag\u003e"`)) {
		t.Fatal("text JSON string or structured content encoding changed", string(want))
	}
}

func TestEncodeToolResultPreservesValidatedRequestIDEncoding(t *testing.T) {
	result := Result{JSON: json.RawMessage(`{"ok":true}`)}
	for _, id := range []json.RawMessage{json.RawMessage(`17`), json.RawMessage(`"call\u002d1"`)} {
		wire, err := EncodeToolResult(id, result)
		if err != nil {
			t.Fatal(id, err)
		}
		if !bytes.Contains(wire, append([]byte(`"id":`), id...)) {
			t.Fatal("request ID encoding changed", string(id), string(wire))
		}
	}
}

func TestEncodeToolResultRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name   string
		id     json.RawMessage
		result Result
	}{
		{"null id", json.RawMessage(`null`), Result{JSON: json.RawMessage(`{}`)}},
		{"object id", json.RawMessage(`{"id":1}`), Result{JSON: json.RawMessage(`{}`)}},
		{"fractional id", json.RawMessage(`1.5`), Result{JSON: json.RawMessage(`{}`)}},
		{"missing JSON", json.RawMessage(`1`), Result{}},
		{"array JSON", json.RawMessage(`1`), Result{JSON: json.RawMessage(`[]`)}},
		{"scalar JSON", json.RawMessage(`1`), Result{JSON: json.RawMessage(`true`)}},
		{"ambiguous JSON", json.RawMessage(`1`), Result{JSON: json.RawMessage(`{"a":1,"a":2}`)}},
		{"tool error", json.RawMessage(`1`), Result{JSON: json.RawMessage(`{}`), Error: &ToolError{Code: "FAILED", Message: "failed"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if wire, err := EncodeToolResult(test.id, test.result); err == nil || wire != nil {
				t.Fatal("invalid tool result encoded", string(wire), err)
			}
		})
	}
}

func TestServerOversizedEncodedToolResultUsesCorrelatedFallback(t *testing.T) {
	server := newTestServer(t, Config{
		Token:            testToken,
		MaxResponseBytes: 1024,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "large", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			return Result{JSON: json.RawMessage(`{"html":"<tag>","data":"` + strings.Repeat(`x`, 2048) + `"}`)}, nil
		},
	})
	response := server.post(t, `{"jsonrpc":"2.0","id":"large\u002did","method":"tools/call","params":{"name":"large","arguments":{}}}`, true)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK || len(body) > 1024 || !bytes.Contains(body, []byte(`"id":"large\u002did"`)) || !bytes.Contains(body, []byte(`"code":-32603`)) || !bytes.Contains(body, []byte(`response exceeds bound`)) || bytes.Contains(body, []byte(`structuredContent`)) {
		t.Fatal("oversized response fallback changed", response.StatusCode, string(body))
	}
}
