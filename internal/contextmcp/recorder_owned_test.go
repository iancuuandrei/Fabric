package contextmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func TestRecorderOwnedRunningBindsBrokerCallerCatalogAndBearer(t *testing.T) {
	broker := bridgeFixture(t)
	foreign := bridgeFixture(t)
	projection := recorderAgentProjection(func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
		return toolbridge.Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
	})
	catalogID, err := RecorderCatalogSHA256(broker, projection)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareRecorderBindingWithQueue(broker, strings.Repeat("a", 64), strings.Repeat("c", 64), projection, 4)
	if err != nil || prepared.CatalogSHA256 != catalogID || prepared.Version != 2 || prepared.QueuePolicy != toolreceipts.QueuePolicySerialFIFO || prepared.MaxQueuedCalls != 4 {
		t.Fatal("prepare recorder binding", prepared, err)
	}
	legacy, err := PrepareRecorderBinding(broker, strings.Repeat("a", 64), strings.Repeat("c", 64), projection)
	if err != nil || legacy.Version != 1 || legacy.QueuePolicy != "" || legacy.MaxQueuedCalls != 0 {
		t.Fatal("legacy recorder binding shape changed", legacy, err)
	}
	for _, capacity := range []int{-1, 33} {
		if binding, err := PrepareRecorderBindingWithQueue(broker, strings.Repeat("a", 64), strings.Repeat("c", 64), projection, capacity); err == nil || !reflect.DeepEqual(binding, toolreceipts.Binding{}) {
			t.Fatal("invalid queue capacity admitted", capacity, binding, err)
		}
	}
	path := filepath.Join(t.TempDir(), "tool-receipts.jsonl")
	const bearer = "recorder-owned-context-bearer-00000001"
	server, err := NewRecorderOwned(broker, bearer, RecorderOwnedConfig{
		Path: path, InvocationID: strings.Repeat("a", 64),
		CallerBindingSHA256: strings.Repeat("c", 64), CatalogSHA256: catalogID,
		AgentProjection: projection, MaxQueuedCalls: 4,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	binding, ok := running.RecorderBinding()
	if !ok || binding.CallerBindingSHA256 != strings.Repeat("c", 64) || binding.CatalogSHA256 != catalogID || binding.InvocationID != strings.Repeat("a", 64) {
		t.Fatal("recorder binding unavailable", binding, ok)
	}
	if !reflect.DeepEqual(binding, prepared) {
		t.Fatal("opened recorder differs from prepared binding", binding, prepared)
	}
	if err := running.ValidateRecorderOwner(broker, bearer, path, binding); err != nil {
		t.Fatal(err)
	}
	if err := running.ValidateOwner(broker, bearer); err == nil {
		t.Fatal("legacy owner validation admitted recorder-backed transport")
	}
	if err := running.ValidateRecorderOwner(foreign, bearer, path, binding); err == nil {
		t.Fatal("foreign broker admitted")
	}
	if err := running.ValidateRecorderOwner(broker, strings.Repeat("x", 40), path, binding); err == nil {
		t.Fatal("foreign bearer admitted")
	}
	if err := running.ValidateRecorderOwner(broker, bearer, path+".other", binding); err == nil {
		t.Fatal("foreign recorder path admitted")
	}
	changed := binding
	changed.CallerBindingSHA256 = strings.Repeat("d", 64)
	if err := running.ValidateRecorderOwner(broker, bearer, path, changed); err == nil {
		t.Fatal("substituted recorder binding admitted")
	}
	changed = binding
	changed.MaxQueuedCalls++
	if err := running.ValidateRecorderOwner(broker, bearer, path, changed); err == nil {
		t.Fatal("substituted recorder queue capacity admitted")
	}
	binding.Tools[0].Tool = "mutated"
	again, ok := running.RecorderBinding()
	if !ok || again.Tools[0].Tool == "mutated" {
		t.Fatal("recorder binding was not defensively copied")
	}

	response := recorderPost(t, running.URL(), bearer, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"agent_list","arguments":{}}}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("agent tool call failed", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	state, err := toolreceipts.Inspect(path)
	if err != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.Owner != toolreceipts.OwnerAgent {
		t.Fatal("agent transport receipt unavailable", state, err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := running.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}
	closed = true
}

func TestNewRecorderOwnedPreservesLegacyZeroQueueBinding(t *testing.T) {
	broker := bridgeFixture(t)
	projection := recorderAgentProjection(func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
		return toolbridge.Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
	})
	catalogID, err := RecorderCatalogSHA256(broker, projection)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy-tool-receipts.jsonl")
	const bearer = "legacy-recorder-context-bearer-000001"
	server, err := NewRecorderOwned(broker, bearer, RecorderOwnedConfig{
		Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("c", 64),
		CatalogSHA256: catalogID, AgentProjection: projection, MaxQueuedCalls: 0,
	}, nil)
	if err != nil {
		t.Fatal("legacy recorder-owned construction failed", err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	binding, ok := running.RecorderBinding()
	if !ok || binding.Version != 1 || binding.QueuePolicy != "" || binding.MaxQueuedCalls != 0 {
		t.Fatal("legacy recorder-owned binding shape changed", binding, ok)
	}
	if err := running.ValidateRecorderOwner(broker, bearer, path, binding); err != nil {
		t.Fatal("legacy recorder-owned identity rejected", err)
	}
}

func TestNewRecorderOwnedRejectsInvalidProjectionBeforeJournalMutation(t *testing.T) {
	broker := bridgeFixture(t)
	path := filepath.Join(t.TempDir(), "tool-receipts.jsonl")
	config := RecorderOwnedConfig{
		Path: path, InvocationID: strings.Repeat("a", 64),
		CallerBindingSHA256: strings.Repeat("c", 64), CatalogSHA256: strings.Repeat("d", 64),
		MaxQueuedCalls: 4,
	}
	if server, err := NewRecorderOwned(broker, strings.Repeat("b", 40), config, nil); err == nil || server != nil {
		t.Fatal("nil agent projection admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid projection mutated recorder journal", err)
	}
	valid := recorderAgentProjection(func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
		return toolbridge.Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
	})
	catalogID, err := RecorderCatalogSHA256(broker, valid)
	if err != nil {
		t.Fatal(err)
	}
	config.AgentProjection, config.CatalogSHA256 = valid, catalogID
	config.Path = "relative-tool-receipts.jsonl"
	if server, err := NewRecorderOwned(broker, strings.Repeat("b", 40), config, nil); err == nil || server != nil {
		t.Fatal("relative recorder journal admitted")
	}
	config.Path = path
	config.InvocationID = strings.Repeat("f", 64)
	if server, err := NewRecorderOwned(broker, strings.Repeat("b", 40), config, nil); err == nil || server != nil {
		t.Fatal("foreign recorder invocation admitted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("foreign invocation mutated recorder journal", err)
	}
}

func TestRecorderOwnedCloseDrainsActiveCallback(t *testing.T) {
	broker := bridgeFixture(t)
	entered := make(chan struct{})
	exited := make(chan struct{})
	projection := recorderAgentProjection(func(ctx context.Context, _ toolbridge.Call) (toolbridge.Result, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return toolbridge.Result{}, ctx.Err()
	})
	catalogID, err := RecorderCatalogSHA256(broker, projection)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tool-receipts.jsonl")
	const bearer = "recorder-owned-context-bearer-00000002"
	server, err := NewRecorderOwned(broker, bearer, RecorderOwnedConfig{
		Path: path, InvocationID: strings.Repeat("a", 64),
		CallerBindingSHA256: strings.Repeat("c", 64), CatalogSHA256: catalogID,
		AgentProjection: projection, MaxQueuedCalls: 4,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		response, requestErr := recorderRequest(running.URL(), bearer, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"agent_list","arguments":{}}}`)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		close(requestDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("agent callback did not start")
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := running.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := running.Wait(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("listener closed before active callback drained")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler did not drain")
	}
	state, err := toolreceipts.Inspect(path)
	if err != nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
		t.Fatal("cancelled callback receipt state differs", state, err)
	}
}

func recorderAgentProjection(call toolbridge.CallFunc) toolbridge.Projection {
	return toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			return []toolbridge.ToolDefinition{{Name: "agent_list", Description: "list agents", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}}, nil
		},
		Call: call,
	}
}

func recorderPost(t *testing.T, endpoint, bearer, body string) *http.Response {
	t.Helper()
	response, err := recorderRequest(endpoint, bearer, body)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func recorderRequest(endpoint, bearer, body string) (*http.Response, error) {
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", toolbridge.ProtocolVersion)
	return (&http.Client{Timeout: 5 * time.Second}).Do(request)
}
