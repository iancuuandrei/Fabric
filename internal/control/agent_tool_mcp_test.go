package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/toolbridge"
)

func TestAgentToolProjectionAcceptsMCPWireArguments(t *testing.T) {
	fixture := newAgentToolFixture(t)
	const bearer = "0123456789abcdef0123456789abcdef"
	server, err := toolbridge.New(toolbridge.Config{Token: bearer, Catalog: fixture.projection.Catalog, Call: fixture.projection.Call})
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := running.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, running.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_agents","arguments": { "limit" : 1 }}}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", toolbridge.ProtocolVersion)
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var decoded struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError           bool            `json:"isError"`
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(decoded.Error) != 0 || decoded.Result.IsError || len(decoded.Result.StructuredContent) == 0 {
		t.Fatalf("valid MCP wire arguments rejected: status=%d error=%s result=%+v", response.StatusCode, decoded.Error, decoded.Result)
	}
	const body = "message retained only in the durable mailbox"
	parent := fixture.binding.Node.ParentAgentID
	messageRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, running.URL, strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"send_message","arguments": { "nonce" : "http-message", "body" : %q, "agent_id" : %q }}}`, body, parent)))
	if err != nil {
		t.Fatal(err)
	}
	messageRequest.Header = request.Header.Clone()
	messageResponse, err := client.Do(messageRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer messageResponse.Body.Close()
	decoded.Error = nil
	decoded.Result.StructuredContent = nil
	if err := json.NewDecoder(messageResponse.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	if messageResponse.StatusCode != http.StatusOK || len(decoded.Error) != 0 || decoded.Result.IsError || len(decoded.Result.StructuredContent) == 0 || strings.Contains(string(decoded.Result.StructuredContent), body) {
		t.Fatal("MCP message rejected or echoed body", decoded)
	}
	service, err := agentcontrol.Bind(fixture.binding.JournalPath, fixture.controllerPath+".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	messages, err := service.MessagesAfter(parent, 0, 64)
	if err != nil || len(messages) != 1 || messages[0].Body != body || messages[0].Message.FromAgentID != fixture.binding.Node.AgentID || messages[0].Message.Wake {
		t.Fatal("MCP message lost caller identity, body, or non-waking semantics", messages, err)
	}
}
