package toolbridge

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestAmbiguousRPCNeverReachesCallbacks(t *testing.T) {
	var calls, observations atomic.Int32
	server := newTestServer(t, Config{
		Token: testToken,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: func(context.Context, Call) (Result, error) {
			calls.Add(1)
			return Result{JSON: json.RawMessage(`{}`)}, nil
		},
		Observe: func(Observation) { observations.Add(1) },
	})
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call","params":{"name":"read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","method":"tools/call","params":{"name":"read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"other"},"params":{"name":"read","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{"x":1,"x":2}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read","arguments":{}}} {"id":2}`,
	} {
		response := server.post(t, body, true)
		assertStatus(t, response, http.StatusBadRequest)
		_ = readResponse(t, response)
	}
	if calls.Load() != 0 || observations.Load() != 0 {
		t.Fatal("ambiguous request reached admitted callback")
	}
}
