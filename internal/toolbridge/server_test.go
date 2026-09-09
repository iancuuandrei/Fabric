package toolbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestStreamableHTTPInitializeListCallPingAndNotifications(t *testing.T) {
	var catalogCalls atomic.Int32
	var calls atomic.Int32
	var observations []Observation
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			catalogCalls.Add(1)
			return []ToolDefinition{{Name: "source_read", Description: "read source", InputSchema: schema}}, nil
		},
		Call: func(_ context.Context, call Call) (Result, error) {
			calls.Add(1)
			if string(call.RequestID) != "2" || call.Tool != "source_read" || string(call.Arguments) != `{"value":"ok"}` {
				t.Fatalf("unexpected call: %#v", call)
			}
			return Result{JSON: json.RawMessage(`{"repository_id":"fixed","value":"ok"}`)}, nil
		},
		Observe: func(observation Observation) { observations = append(observations, observation) },
	})
	originalHash := server.bridge.CatalogHash()
	copy(schema, bytes.Repeat([]byte{' '}, len(schema)))

	response := server.post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"OpenCode","title":"OpenCode","version":"1","description":"client"}}}`, false)
	assertStatus(t, response, http.StatusOK)
	var initialized struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools map[string]any `json:"tools"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	decodeResponse(t, response, &initialized)
	if initialized.JSONRPC != "2.0" || initialized.ID != 1 || initialized.Result.ProtocolVersion != ProtocolVersion || initialized.Result.Capabilities.Tools == nil {
		t.Fatal(initialized)
	}
	if response.Header.Get("MCP-Session-Id") != "" {
		t.Fatal("stateless server assigned a session")
	}

	response = server.post(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, true)
	assertStatus(t, response, http.StatusAccepted)
	if data := readResponse(t, response); len(data) != 0 {
		t.Fatalf("notification returned a body: %q", data)
	}

	response = server.post(t, `{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{}}`, true)
	assertStatus(t, response, http.StatusOK)
	var listed struct {
		ID     string `json:"id"`
		Result struct {
			Tools []ToolDefinition `json:"tools"`
		} `json:"result"`
	}
	decodeResponse(t, response, &listed)
	if listed.ID != "list" || len(listed.Result.Tools) != 1 || listed.Result.Tools[0].Name != "source_read" || string(listed.Result.Tools[0].InputSchema) == string(schema) {
		t.Fatal("catalog was not pinned", listed)
	}
	if catalogCalls.Load() != 1 || server.bridge.CatalogHash() != originalHash {
		t.Fatal("catalog callback was re-evaluated or hash drifted")
	}

	response = server.post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"source_read","arguments":{"value":"ok"}}}`, true)
	assertStatus(t, response, http.StatusOK)
	var called struct {
		ID     int `json:"id"`
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
	}
	decodeResponse(t, response, &called)
	if called.ID != 2 || called.Result.IsError || len(called.Result.Content) != 1 || called.Result.Content[0].Type != "text" || called.Result.Content[0].Text != `{"repository_id":"fixed","value":"ok"}` || called.Result.Structured["repository_id"] != "fixed" || calls.Load() != 1 {
		t.Fatal(called, calls.Load())
	}

	response = server.post(t, `{"jsonrpc":"2.0","id":"ping","method":"ping"}`, true)
	assertStatus(t, response, http.StatusOK)
	var ping map[string]any
	decodeResponse(t, response, &ping)
	if ping["id"] != "ping" || len(ping["result"].(map[string]any)) != 0 {
		t.Fatal(ping)
	}
	if len(observations) != 5 || observations[0].Method != "initialize" || observations[1].Method != "notifications/initialized" || !observations[1].Notification || observations[2].Method != "tools/list" || observations[2].CatalogHash != originalHash || observations[3].Method != "tools/call" || observations[4].Method != "ping" {
		t.Fatal("admitted operation observations differ", observations)
	}
}

func TestTransportAdmissionRejectsBeforeToolCallback(t *testing.T) {
	var calls atomic.Int32
	var observations atomic.Int32
	server := newTestServer(t, Config{
		Token:           testToken,
		MaxRequestBytes: 1024,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "allowed", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			calls.Add(1)
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		},
		Observe: func(Observation) { observations.Add(1) },
	})
	valid := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","arguments":{}}}`

	tests := []struct {
		name   string
		body   string
		status int
		change func(*http.Request)
	}{
		{"missing auth", valid, http.StatusUnauthorized, func(request *http.Request) { request.Header.Del("Authorization") }},
		{"wrong origin", valid, http.StatusForbidden, func(request *http.Request) { request.Header.Set("Origin", "https://attacker.invalid") }},
		{"wrong host", valid, http.StatusMisdirectedRequest, func(request *http.Request) { request.Host = "localhost" }},
		{"missing protocol", valid, http.StatusBadRequest, func(request *http.Request) { request.Header.Del("MCP-Protocol-Version") }},
		{"missing accept type", valid, http.StatusNotAcceptable, func(request *http.Request) { request.Header.Set("Accept", "application/json") }},
		{"wrong content type", valid, http.StatusUnsupportedMediaType, func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") }},
		{"malformed json", `{`, http.StatusBadRequest, nil},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"other","arguments":{}}}`, http.StatusOK, nil},
		{"array arguments", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"allowed","arguments":[]}}`, http.StatusOK, nil},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, http.StatusOK, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := server.postChanged(t, test.body, true, test.change)
			assertStatus(t, response, test.status)
			body := readResponse(t, response)
			if bytes.Contains(body, []byte(testToken)) {
				t.Fatal("credential exposed in response")
			}
		})
	}
	response := server.post(t, strings.Repeat(" ", 1100), true)
	assertStatus(t, response, http.StatusRequestEntityTooLarge)
	_ = readResponse(t, response)

	request, err := http.NewRequest(http.MethodGet, server.running.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, response, http.StatusMethodNotAllowed)
	_ = readResponse(t, response)
	if calls.Load() != 0 {
		t.Fatal("tool callback ran before admission", calls.Load())
	}
	if observations.Load() != 0 {
		t.Fatal("operation observed before admission", observations.Load())
	}
}

func TestOutputBoundCredentialFilterAndDomainError(t *testing.T) {
	var mode atomic.Int32
	server := newTestServer(t, Config{
		Token:            testToken,
		MaxResponseBytes: 1024,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "bounded", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			switch mode.Add(1) {
			case 1:
				return Result{JSON: json.RawMessage(`{"data":"` + strings.Repeat("x", 2000) + `"}`)}, nil
			case 2:
				return Result{JSON: json.RawMessage(`{"secret":"` + testToken + `"}`)}, nil
			default:
				return Result{Error: &ToolError{Code: "INVALID_ARGUMENT", Message: "limit is outside the supported range"}}, nil
			}
		},
	})
	request := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"bounded","arguments":{}}}`
	for call := 1; call <= 3; call++ {
		response := server.post(t, request, true)
		assertStatus(t, response, http.StatusOK)
		body := readResponse(t, response)
		if len(body) > 1024 || bytes.Contains(body, []byte(testToken)) {
			t.Fatal("unbounded or credential-bearing response", len(body), string(body))
		}
		if call == 3 && (!bytes.Contains(body, []byte(`"isError":true`)) || !bytes.Contains(body, []byte("INVALID_ARGUMENT"))) {
			t.Fatal("domain error was not an MCP tool error", string(body))
		}
	}
}

func TestStructuredContentPreservesLargeIntegerAndRejectsAmbiguity(t *testing.T) {
	var calls atomic.Int32
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "number", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			if calls.Add(1) == 1 {
				return Result{JSON: json.RawMessage(`{"large":9007199254740993}`)}, nil
			}
			return Result{JSON: json.RawMessage(`{"duplicate":1,"duplicate":2}`)}, nil
		},
	})
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"number","arguments":{}}}`
	response := server.post(t, request, true)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK || bytes.Contains(body, []byte("9007199254740992")) || bytes.Count(body, []byte("9007199254740993")) != 2 {
		t.Fatal("large integer changed across MCP framing", response.StatusCode, string(body))
	}
	response = server.post(t, request, true)
	body = readResponse(t, response)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"code":-32603`)) || bytes.Contains(body, []byte(`"structuredContent"`)) {
		t.Fatal("ambiguous JSON result admitted", response.StatusCode, string(body))
	}
}

func TestJSONDomainFailurePreservesExactControllerContent(t *testing.T) {
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "domain", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			return Result{JSON: json.RawMessage(`{"error":"bounded controller response"}`), IsError: true}, nil
		},
	})
	response := server.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"domain","arguments":{}}}`, true)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"isError":true`)) || !bytes.Contains(body, []byte(`"text":"{\"error\":\"bounded controller response\"}"`)) {
		t.Fatal("controller domain response changed", response.StatusCode, string(body))
	}
}

func TestAmbiguousControllerResultRejected(t *testing.T) {
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "domain", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			return Result{JSON: json.RawMessage(`{"value":1}`), Error: &ToolError{Code: "FAILED", Message: "ambiguous"}}, nil
		},
	})
	response := server.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"domain","arguments":{}}}`, true)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ambiguous tool result")) || bytes.Contains(body, []byte(`"structuredContent"`)) {
		t.Fatal("ambiguous controller result admitted", response.StatusCode, string(body))
	}
}

func TestRequestIDCanonicalizationPreservesJSONType(t *testing.T) {
	escaped, escapedKey, ok := canonicalRequestID(json.RawMessage(`"act\u0069ve"`))
	if !ok || string(escaped) != `"active"` || escapedKey != `"active"` {
		t.Fatal("escaped string id did not canonicalize", string(escaped), escapedKey, ok)
	}
	number, numberKey, ok := canonicalRequestID(json.RawMessage(` -0 `))
	if !ok || string(number) != "0" || numberKey != "0" {
		t.Fatal("numeric id did not canonicalize", string(number), numberKey, ok)
	}
	stringID, stringKey, ok := canonicalRequestID(json.RawMessage(`"0"`))
	if !ok || string(stringID) != `"0"` || stringKey == numberKey {
		t.Fatal("string and numeric ids collided", string(stringID), stringKey, numberKey, ok)
	}
}

func TestCanonicalRequestIDRejectsEscapedSemanticDuplicate(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := newTestServer(t, Config{
		Token:              testToken,
		MaxConcurrentCalls: 2,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(ctx context.Context, call Call) (Result, error) {
			calls.Add(1)
			if string(call.RequestID) != `"active"` {
				t.Errorf("request id was not canonical: %s", call.RequestID)
			}
			close(started)
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	firstDone := make(chan *http.Response, 1)
	go func() {
		firstDone <- server.post(t, `{"jsonrpc":"2.0","id":"active","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start")
	}
	duplicate := server.post(t, `{"jsonrpc":"2.0","id":"act\u0069ve","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	body := readResponse(t, duplicate)
	if duplicate.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("duplicate active request id")) || calls.Load() != 1 {
		t.Fatal("semantic duplicate request id admitted", duplicate.StatusCode, string(body), calls.Load())
	}
	cancelled := server.post(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"act\u0069ve"}}`, true)
	assertStatus(t, cancelled, http.StatusAccepted)
	_ = readResponse(t, cancelled)
	select {
	case response := <-firstDone:
		_ = readResponse(t, response)
	case <-time.After(2 * time.Second):
		t.Fatal("canonical cancellation id did not match active request")
	}
}

func TestCancellationAndConcurrencyLimit(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := newTestServer(t, Config{
		Token:              testToken,
		MaxConcurrentCalls: 1,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(ctx context.Context, _ Call) (Result, error) {
			calls.Add(1)
			close(started)
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	})
	firstDone := make(chan *http.Response, 1)
	go func() {
		firstDone <- server.post(t, `{"jsonrpc":"2.0","id":"active","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not start")
	}
	second := server.post(t, `{"jsonrpc":"2.0","id":"second","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	assertStatus(t, second, http.StatusTooManyRequests)
	_ = readResponse(t, second)
	if calls.Load() != 1 {
		t.Fatal("concurrency admission invoked callback", calls.Load())
	}
	cancelled := server.post(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"active","reason":"client stopped"}}`, true)
	assertStatus(t, cancelled, http.StatusAccepted)
	_ = readResponse(t, cancelled)
	select {
	case response := <-firstDone:
		assertStatus(t, response, http.StatusOK)
		body := readResponse(t, response)
		if !bytes.Contains(body, []byte(`"code":-32800`)) {
			t.Fatal("cancelled call did not return cancellation error", string(body))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled callback did not return")
	}
}

type testRunning struct {
	bridge  *Server
	running *Running
}

func newTestServer(t *testing.T, config Config) *testRunning {
	t.Helper()
	bridge, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	running, err := bridge.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := running.Close(ctx); err != nil {
			t.Error(err)
		}
		if err := running.Wait(); err != nil {
			t.Error(err)
		}
	})
	return &testRunning{bridge: bridge, running: running}
}

func (s *testRunning) post(t *testing.T, body string, version bool) *http.Response {
	t.Helper()
	return s.postChanged(t, body, version, nil)
}

func (s *testRunning) postChanged(t *testing.T, body string, version bool, change func(*http.Request)) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, s.running.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if version {
		request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	}
	if change != nil {
		change(request)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertStatus(t *testing.T, response *http.Response, status int) {
	t.Helper()
	if response.StatusCode != status {
		body := readResponse(t, response)
		t.Fatalf("status %d, want %d: %s", response.StatusCode, status, body)
	}
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	body := readResponse(t, response)
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatal(err, string(body))
	}
}

func readResponse(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
