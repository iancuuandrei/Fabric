package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestRecordedCompositeMCPPreservesBothOwnerReceipts(t *testing.T) {
	fixture := newAgentToolFixture(t)
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(fixture.binding.InvocationID, snapshot.Creation.Repository, nil, contextbroker.Limits{MaxCalls: 4, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	brokerPath := filepath.Join(t.TempDir(), "broker")
	broker, err := contextbroker.Open(brokerPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	catalogID, err := contextmcp.RecorderCatalogSHA256(broker, fixture.projection)
	if err != nil {
		t.Fatal(err)
	}
	callerID, err := agentToolCallerIdentity(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(t.TempDir(), "calls")
	const bearer = "0123456789abcdef0123456789abcdef"
	server, err := contextmcp.NewRecorderOwned(broker, bearer, contextmcp.RecorderOwnedConfig{Path: receiptPath, InvocationID: fixture.binding.InvocationID, CallerBindingSHA256: callerID, CatalogSHA256: catalogID, AgentProjection: fixture.projection}, nil)
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
	recorderBinding, ok := running.RecorderBinding()
	if !ok || running.ValidateRecorderOwner(broker, bearer, receiptPath, recorderBinding) != nil {
		t.Fatal("composite transport owner differs")
	}
	if running.ValidateOwner(broker, bearer) == nil {
		t.Fatal("legacy context-only ownership accepted composite")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	var observations []toolreceipts.Observation
	for i, entry := range []struct {
		tool string
		args any
	}{
		{"source_read", map[string]any{"path": "file.txt", "offset": 0, "limit": 32}},
		{"send_message", map[string]any{"agent_id": fixture.binding.Node.ParentAgentID, "nonce": "recorded-http", "body": "source inspected"}},
	} {
		args, _ := json.MarshalIndent(entry.args, "", " ")
		requestID, _ := json.Marshal(i + 1)
		wire, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": entry.tool, "arguments": json.RawMessage(args)}})
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, running.URL(), bytes.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+bearer)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", toolbridge.ProtocolVersion)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool            `json:"isError"`
				JSON    json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		err = json.NewDecoder(response.Body).Decode(&decoded)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(decoded.Error) != 0 || decoded.Result.IsError || len(decoded.Result.JSON) == 0 {
			t.Fatal("composite HTTP call failed", entry.tool, decoded, err)
		}
		observations = append(observations, toolreceipts.Observation{Call: toolbridge.Call{RequestID: requestID, Tool: entry.tool, Arguments: args}, Result: toolbridge.Result{JSON: decoded.Result.JSON}})
	}
	state, err := toolreceipts.Inspect(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ValidateTranscript(recorderBinding, observations); err != nil {
		t.Fatal("recorded HTTP transcript differs", err)
	}
	if len(state.Calls) != 2 || state.Calls[0].Receipt.Owner != toolreceipts.OwnerContext || state.Calls[1].Receipt.Owner != toolreceipts.OwnerAgent {
		t.Fatal("owner identity lost")
	}
	brokerState, err := contextbroker.Inspect(brokerPath)
	if err != nil || brokerState.Pending != nil || len(brokerState.Responses) != 1 {
		t.Fatal("context source ledger unsettled", err)
	}
	brokerResult, err := canonical.Bytes(brokerState.Responses[0])
	observedContext, decodeErr := canonical.Normalize(observations[0].Result.JSON)
	if err != nil || decodeErr != nil || !bytes.Equal(brokerResult, observedContext) {
		t.Fatal("HTTP context result differs from authoritative broker receipt", err, decodeErr)
	}
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(observations[1].Call, observations[1].Result); err != nil {
		t.Fatal("HTTP agent result differs from authoritative source receipts", err)
	}
	compositeVerifier, err := newCompositeBackendVerifier(fixture.controllerPath, fixture.schedulerPath, brokerPath, fixture.binding, binding, recorderBinding)
	if err != nil {
		t.Fatal(err)
	}
	for index, owner := range []toolreceipts.Owner{toolreceipts.OwnerContext, toolreceipts.OwnerAgent} {
		if err := compositeVerifier(owner, observations[index].Call, observations[index].Result); err != nil {
			t.Fatal("composite backend rejected exact source", err)
		}
		wrongOwner := toolreceipts.OwnerContext
		if owner == wrongOwner {
			wrongOwner = toolreceipts.OwnerAgent
		}
		if err := compositeVerifier(wrongOwner, observations[index].Call, observations[index].Result); err == nil {
			t.Fatal("owner substitution accepted")
		}
	}
	foreign := recorderBinding
	foreign.CallerBindingSHA256 = catalogID
	foreign.BindingID, _ = foreign.ID()
	if _, err := newCompositeBackendVerifier(fixture.controllerPath, fixture.schedulerPath, brokerPath, fixture.binding, binding, foreign); err == nil {
		t.Fatal("foreign caller receipt binding accepted")
	}
	observations[1].Result.JSON = json.RawMessage(`{"substituted":true}`)
	if err := state.ValidateTranscript(recorderBinding, observations); err == nil {
		t.Fatal("substituted transcript accepted")
	}
}
