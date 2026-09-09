package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/toolbridge"
)

const (
	responsesProbeDigest    = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	responsesProbeModel     = "wire-responses-model"
	responsesProbeBearer    = "responses-probe-fixture-bearer-001"
	responsesProbeGateway   = "responses-probe-gateway-capability-0001"
	responsesProbeCallID    = "call_engorch_responses_source_read_1"
	responsesProbeFinalText = "synthetic Responses tool loop complete"
)

type responsesProviderRequest struct {
	Authorization    string
	Accept           string
	ContentType      string
	UserAgent        string
	Method           string
	Path             string
	ContentLength    int64
	TransferEncoding []string
	Body             json.RawMessage
}

type responsesProvider struct {
	mu        sync.Mutex
	requests  []responsesProviderRequest
	responses [][]byte
	toolName  string
	arguments string
	callID    string
	finalText string
}

func (p *responsesProvider) snapshot() []responsesProviderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]responsesProviderRequest, len(p.requests))
	for i := range p.requests {
		out[i] = p.requests[i]
		out[i].TransferEncoding = append([]string(nil), p.requests[i].TransferEncoding...)
		out[i].Body = append(json.RawMessage(nil), p.requests[i].Body...)
	}
	return out
}

func (p *responsesProvider) responseSnapshot() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.responses))
	for i := range p.responses {
		out[i] = append([]byte(nil), p.responses[i]...)
	}
	return out
}

func (p *responsesProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) {
		http.Error(w, "invalid fixture request", http.StatusBadRequest)
		return
	}
	request := responsesProviderRequest{
		Authorization: r.Header.Get("Authorization"), Accept: r.Header.Get("Accept"),
		ContentType: r.Header.Get("Content-Type"), UserAgent: r.Header.Get("User-Agent"),
		Method: r.Method, Path: r.URL.EscapedPath(), ContentLength: r.ContentLength,
		TransferEncoding: append([]string(nil), r.TransferEncoding...), Body: append(json.RawMessage(nil), body...),
	}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	number := len(p.requests)
	p.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/responses" || r.URL.RawQuery != "" || number > 2 {
		http.Error(w, "unexpected fixture request", http.StatusNotFound)
		return
	}
	var response []byte
	if number == 1 {
		response = responsesProbeFunctionSSE(p.toolName, p.callID, p.arguments)
	} else {
		response = responsesProbeTextSSE(p.finalText)
	}
	p.mu.Lock()
	p.responses = append(p.responses, append([]byte(nil), response...))
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(response)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func responsesProbeSnapshot(id, status string, output any, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": 123, "status": status,
		"background": false, "completed_at": nil, "error": nil, "frequency_penalty": 0.0,
		"incomplete_details": nil, "instructions": nil, "max_output_tokens": 128,
		"max_tool_calls": nil, "model": responsesProbeModel, "moderation": nil, "output": output,
		"parallel_tool_calls": true, "presence_penalty": 0.0, "previous_response_id": nil,
		"prompt_cache_key": nil, "prompt_cache_retention": nil, "reasoning": map[string]any{"effort": "high", "summary": "auto"},
		"safety_identifier": nil, "service_tier": "default", "store": false, "temperature": 1.0,
		"text":        map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "medium"},
		"tool_choice": "auto", "tools": []any{}, "top_logprobs": 0, "top_p": 1.0,
		"truncation": "disabled", "usage": usage, "user": nil, "metadata": map[string]any{},
	}
}

func responsesProbeEvents(events ...map[string]any) []byte {
	var out strings.Builder
	for _, event := range events {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&out, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
	return []byte(out.String())
}

func responsesProbeFunctionSSE(tool, callID, args string) []byte {
	id, item, reasoningItem := "resp_engorch_tool_1", "fc_engorch_tool_1", "rs_engorch_reasoning_1"
	reasoningAdded := map[string]any{"id": reasoningItem, "type": "reasoning", "encrypted_content": "encrypted_reasoning_added", "summary": []any{}}
	reasoningDone := map[string]any{"id": reasoningItem, "type": "reasoning", "encrypted_content": "encrypted_reasoning_done", "summary": []any{}}
	reasoningTerminal := map[string]any{"id": reasoningItem, "type": "reasoning", "encrypted_content": "encrypted_reasoning_terminal", "summary": []any{}}
	pending := map[string]any{"id": item, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": callID, "name": "engorch_" + tool}
	done := map[string]any{"id": item, "type": "function_call", "status": "completed", "arguments": args, "call_id": callID, "name": "engorch_" + tool}
	return responsesProbeEvents(
		map[string]any{"type": "response.created", "response": responsesProbeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": responsesProbeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": reasoningAdded, "sequence_number": 2},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": reasoningDone, "sequence_number": 3},
		map[string]any{"type": "response.output_item.added", "output_index": 1, "item": pending, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": args, "item_id": item, "obfuscation": "fixture", "output_index": 1, "sequence_number": 5},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": args, "item_id": item, "output_index": 1, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": responsesProbeSnapshot(id, "completed", []any{reasoningTerminal, done}, map[string]any{"input_tokens": 9, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 1}, "total_tokens": 13}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func responsesProbeTextSSE(text string) []byte {
	id, item := "resp_engorch_text_2", "msg_engorch_text_2"
	pending := map[string]any{"id": item, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"}
	part0 := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""}
	part := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": text}
	done := map[string]any{"id": item, "type": "message", "status": "completed", "content": []any{part}, "role": "assistant"}
	return responsesProbeEvents(
		map[string]any{"type": "response.created", "response": responsesProbeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": responsesProbeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": pending, "sequence_number": 2},
		map[string]any{"type": "response.content_part.added", "content_index": 0, "item_id": item, "output_index": 0, "part": part0, "sequence_number": 3},
		map[string]any{"type": "response.output_text.delta", "content_index": 0, "delta": text, "item_id": item, "logprobs": []any{}, "obfuscation": "fixture", "output_index": 0, "sequence_number": 4},
		map[string]any{"type": "response.output_text.done", "content_index": 0, "item_id": item, "logprobs": []any{}, "output_index": 0, "sequence_number": 5, "text": text},
		map[string]any{"type": "response.content_part.done", "content_index": 0, "item_id": item, "output_index": 0, "part": part, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": responsesProbeSnapshot(id, "completed", []any{done}, map[string]any{"input_tokens": 18, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 21}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func responsesProbeRequestShape(raw json.RawMessage) string {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return "invalid"
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{"top=" + strings.Join(keys, ",")}
	var model string
	var maxTokens int64
	var stream, store bool
	if json.Unmarshal(body["model"], &model) == nil && json.Unmarshal(body["max_output_tokens"], &maxTokens) == nil && json.Unmarshal(body["stream"], &stream) == nil && json.Unmarshal(body["store"], &store) == nil {
		parts = append(parts, fmt.Sprintf("model=%s;max_output_tokens=%d;stream=%t;store=%t", model, maxTokens, stream, store))
	}
	var input []map[string]json.RawMessage
	if json.Unmarshal(body["input"], &input) == nil {
		for index, item := range input {
			itemKeys := make([]string, 0, len(item))
			for key := range item {
				itemKeys = append(itemKeys, key)
			}
			sort.Strings(itemKeys)
			var role, kind string
			_ = json.Unmarshal(item["role"], &role)
			_ = json.Unmarshal(item["type"], &kind)
			parts = append(parts, fmt.Sprintf("input%d[%s%s]=%s", index, role, kind, strings.Join(itemKeys, ",")))
		}
	}
	return strings.Join(parts, ";")
}

func assertResponsesProbeSSE(t *testing.T, responses [][]byte, toolName, callID, arguments string) (providergateway.ResponsesObservation, providergateway.ResponsesObservation) {
	t.Helper()
	if len(responses) != 2 {
		t.Fatalf("provider returned %d Responses SSE bodies, want two", len(responses))
	}
	options := providergateway.ResponsesSSEOptions{RequireTrailingCostPingV1: true}
	first, err := providergateway.ParseResponsesSSEWithOptions(responses[0], len(responses[0]), 13, options)
	if err != nil {
		t.Fatal("production Responses parser rejected function stream:", err)
	}
	if first.Model != responsesProbeModel || first.FinishReason != "tool_calls" || first.Usage.InputTokens != 9 || first.Usage.OutputTokens != 4 || first.Usage.ReasoningTokens == nil || *first.Usage.ReasoningTokens != 1 || len(first.FunctionCalls) != 1 || len(first.Reasoning) != 1 || first.TrailingCostPing == nil || first.TrailingCostPing.CostDecimal != "0" {
		t.Fatal("parsed Responses function metadata mismatch", first)
	}
	if first.Reasoning[0].ItemID != "rs_engorch_reasoning_1" || first.Reasoning[0].DoneEncryptedContent == nil || *first.Reasoning[0].DoneEncryptedContent != "encrypted_reasoning_done" || first.Reasoning[0].TerminalEncryptedContent == nil || *first.Reasoning[0].TerminalEncryptedContent != "encrypted_reasoning_terminal" {
		t.Fatal("parsed Responses reasoning metadata mismatch", first.Reasoning)
	}
	call := first.FunctionCalls[0]
	if call.CallID != callID || call.Name != "engorch_"+toolName || string(call.Arguments) != arguments {
		t.Fatal("parsed Responses function call mismatch", call)
	}
	second, err := providergateway.ParseResponsesSSEWithOptions(responses[1], len(responses[1]), 21, options)
	if err != nil {
		t.Fatal("production Responses parser rejected text stream:", err)
	}
	if second.Model != responsesProbeModel || second.FinishReason != "stop" || second.OutputText != responsesProbeFinalText || second.Usage.InputTokens != 18 || second.Usage.OutputTokens != 3 || len(second.FunctionCalls) != 0 || second.TrailingCostPing == nil || second.TrailingCostPing.CostDecimal != "0" {
		t.Fatal("parsed Responses text metadata mismatch", second)
	}
	return first, second
}

func responsesProbeToolCatalog(t *testing.T, catalog []sourcetools.Definition) []toolbridge.ToolDefinition {
	t.Helper()
	result := make([]toolbridge.ToolDefinition, 0, len(catalog))
	for _, definition := range catalog {
		schema, err := canonical.Bytes(definition.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, toolbridge.ToolDefinition{Name: definition.Name, Description: definition.Description, InputSchema: schema})
	}
	return result
}

func responsesProbeAllowedTool(t *testing.T, projection ProviderSchemaProjectionReceipt, toolName string) providergateway.RequestTool {
	t.Helper()
	for _, definition := range projection.Tools {
		if definition.Name == toolName {
			return providergateway.RequestTool{Name: "engorch_" + definition.Name, Description: definition.Description, Parameters: append(json.RawMessage(nil), definition.Parameters...)}
		}
	}
	t.Fatal("selected Responses tool absent from projected catalog")
	return providergateway.RequestTool{}
}

func assertResponsesProbeRequests(t *testing.T, requests []responsesProviderRequest, invocationID, promptCacheKey string, projection ProviderSchemaProjectionReceipt, toolName string) {
	t.Helper()
	capabilities := providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, Reasoning: true, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{"prompt_cache_key"}, TrailingCostPingV1: true}
	capabilityBytes, err := canonical.Bytes(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://fixture.invalid/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "FIXTURE_KEY"}}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: responsesProbeModel, AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: capabilityBytes, ContextWindowTokens: 8192, MaxCalls: 2, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("c", 64), AccessInvocationID: invocationID, RouteID: strings.Repeat("d", 64), ReservedTokens: 8192, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	expected := providergateway.ResponsesRequestExpectation{MaxOutputTokens: 128, StateMode: "full-input-stateless", Store: false, SystemRole: "developer", ReasoningEffort: "high", ReasoningSummary: "auto", TextVerbosity: "high", ToolChoice: "auto", Include: []string{"reasoning.encrypted_content"}, PromptCacheKey: promptCacheKey, FunctionToolStrict: false}
	allowed := []providergateway.RequestTool{responsesProbeAllowedTool(t, projection, toolName)}
	for index, request := range requests {
		observation, err := providergateway.ValidateResponsesRequest(request.Body, int64(len(request.Body)), binding, capabilities, expected, allowed)
		if err != nil {
			t.Fatalf("production Responses request validator rejected pinned request %d: %v; shape=%s expected_tool=%+v", index+1, err, responsesProbeRequestShape(request.Body), allowed[0])
		}
		wantItems := 2
		wantReasoning := false
		if index == 1 {
			wantItems = 5
			wantReasoning = true
		}
		if observation.Model != responsesProbeModel || observation.MaxOutputTokens != 128 || observation.InputItemCount != wantItems || observation.ToolCount != 1 || observation.HasReasoning != wantReasoning {
			t.Fatal("Responses request observation mismatch", index+1, observation)
		}
	}
}

func assertResponsesProbeFirstRequest(t *testing.T, request responsesProviderRequest, toolName string) {
	t.Helper()
	var body struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil || len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Name != "engorch_"+toolName {
		t.Fatalf("Responses provider tool catalog was not exact allowlist: %+v err=%v", body.Tools, err)
	}
}

func assertResponsesProbeResultRequest(t *testing.T, request responsesProviderRequest, expected contextbroker.Response, toolName, callID string, contentNeedle []byte) {
	t.Helper()
	var body struct {
		Input []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Output    string `json:"output"`
		} `json:"input"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	seenCall, seenResult := false, false
	for _, item := range body.Input {
		if item.Type == "function_call" && item.CallID == callID && item.Name == "engorch_"+toolName {
			seenCall = true
		}
		if item.Type == "function_call_output" && item.CallID == callID {
			var receipt contextbroker.Response
			if canonical.Decode([]byte(item.Output), &receipt) == nil && reflect.DeepEqual(receipt, expected) && bytesContainAll(receipt.Content, contentNeedle) {
				seenResult = true
			}
		}
	}
	if !seenCall || !seenResult {
		t.Fatalf("second Responses request lacks exact function/result receipt: call=%v result=%v body=%s", seenCall, seenResult, request.Body)
	}
}
func TestPinnedOpenCodeResponsesSourceReadToolLoopProbe(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_RESPONSES_PROBE") != "1" {
		t.Skip("explicit pinned OpenCode Responses tool-loop probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	sourceRoot := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", sourceRoot}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
	const committed = "committed-only-responses-probe"
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte(committed), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(ctx, sourceRoot, "opencode-responses-probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("dirty-checkout-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	prompt := "Use the permitted source_read tool once to read source.txt from offset 0 with limit 64, then answer with the fixture completion."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-responses-fixture", Model: responsesProbeModel, Effort: "high", Role: "explorer"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(invocation.ID, source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: contextmcp.MaxContentBytes})
	if err != nil {
		t.Fatal(err)
	}
	contextJournal := filepath.Join(t.TempDir(), "context.jsonl")
	broker, err := contextbroker.Open(contextJournal, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := broker.Close(); err != nil {
			t.Error(err)
		}
	}()
	bridge, err := contextmcp.NewOwned(broker, responsesProbeBearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := bridge.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if err := running.Close(closeCtx); err != nil {
			t.Error(err)
		}
		if err := running.Wait(); err != nil {
			t.Error(err)
		}
	}()
	projection, err := ProjectOpenCodeOpenAITools(responsesProbeToolCatalog(t, broker.Catalog()))
	if err != nil || projection.OriginalCatalogSHA256 != running.CatalogHash() || projection.Projection != OpenCodeOpenAISchemaProjection {
		t.Fatal("pinned provider schema projection does not bind MCP catalog", projection, err)
	}

	provider := &responsesProvider{toolName: "source_read", arguments: `{"path":"source.txt","offset":0,"limit":64}`, callID: responsesProbeCallID, finalText: responsesProbeFinalText}
	providerServer := httptest.NewServer(provider)
	defer providerServer.Close()
	if !strings.HasPrefix(providerServer.URL, "http://127.0.0.1:") {
		t.Fatal("fixture provider is not explicit IPv4 loopback")
	}
	hostRoot := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(hostRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	toolsSpec := ToolsConfigurationSpec{Endpoint: running.URL(), Bearer: responsesProbeBearer, ToolNames: []string{"source_read"}, TimeoutMillis: 5000}
	providerSpec := ProviderConfigurationSpec{
		ProviderID: "engorch-responses-fixture", ModelID: responsesProbeModel,
		Protocol: ProviderProtocolOpenAIResponses, GatewayBaseURL: providerServer.URL + "/v1",
		GatewayCapability: responsesProbeGateway, ContextWindowTokens: 8192, MaxOutputTokens: 128,
		Tools: true, Reasoning: true, TimeoutMillis: 15000, Variant: ProviderVariantSpec{Name: "high", Responses: &OpenAIResponsesVariantOptions{
			Effort: "high", Summary: "auto", TextVerbosity: "high",
		}},
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(repositoryRoot, ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	port := providerProbePort(t)
	output := &probeOutput{}
	process, providerReceipt, attempts, err := StartReadyLimitedProviderToolsProcess(ctx, binary, responsesProbeDigest, hostRoot, port, "fixture", "fixture-server-secret", output, 2, 14*time.Second, false, providerSpec, toolsSpec)
	if err != nil {
		t.Fatalf("pinned tool host startup failed: %v; attempts: %+v; output: %s", err, attempts, output.String())
	}
	defer func() {
		_ = process.Close()
		select {
		case <-process.Done():
		default:
			t.Error("owned OpenCode tool process was not reaped")
		}
	}()
	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "fixture", "fixture-server-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := waitMCPToolProbeStatus(ctx, client, process); err != nil {
		t.Fatalf("MCP server did not connect: %v; output: %s", err, output.String())
	}
	toolsReceipt := providerReceipt.ToolsConfiguration
	providerIdentity, err := process.ProviderIdentity()
	if err != nil || providerReceipt.Protocol != ProviderProtocolOpenAIResponses || providerReceipt.SDKPackage != "@ai-sdk/openai" || providerReceipt.GatewayBaseURL != providerServer.URL+"/v1" ||
		providerIdentity.TextVerbosity != "high" || providerIdentity.RuntimeAgent != "" || providerIdentity.Temperature != "" || providerIdentity.TopP != "" ||
		!reflect.DeepEqual(providerIdentity.Configuration, providerReceipt) || ValidateProviderProcessIdentity(providerIdentity) != nil {
		t.Fatal("strict provider/tools process identity mismatch", providerReceipt, providerIdentity, err)
	}
	project, err := client.ReadCurrentProject(ctx, ProjectExpectation{Directory: hostRoot, Mode: ProjectModeGlobal})
	if err != nil || project.ID != "global" || project.Directory != hostRoot || project.Mode != ProjectModeGlobal || project.Worktree != "/" || project.VCS != "" || len(project.SHA256) != 64 || strings.Trim(project.SHA256, "0123456789abcdef") != "" {
		t.Fatalf("strict current project receipt mismatch: receipt=%+v err=%v diagnostic=%s", project, err, mcpToolProbeProjectShape(ctx, client))
	}
	sessionBinding := ToolSessionBinding{
		Session:   SessionBinding{IntentID: invocation.ID, ProjectID: project.ID, Directory: hostRoot, Agent: "build", Provider: "engorch-responses-fixture", Model: responsesProbeModel, Variant: invocation.Profile.Effort},
		ToolNames: []string{"source_read"}, CatalogSHA256: running.CatalogHash(),
	}
	sessionJournal := filepath.Join(t.TempDir(), "tool-session.jsonl")
	sessionID, err := client.CreateToolSession(ctx, sessionJournal, sessionBinding)
	if err != nil {
		t.Fatal("create exact tool session:", err)
	}
	verifiedSessionID, err := client.ReconcileToolSession(ctx, sessionBinding)
	if err != nil || verifiedSessionID != sessionID {
		t.Fatal("live exact tool session readback failed", verifiedSessionID, sessionID, err)
	}

	dispatch, err := DispatchForInvocation(invocation, sessionID, "msg_engorchmcptool", sessionBinding.Session.Agent, hostRoot, "/")
	if err != nil {
		t.Fatal(err)
	}
	brokerBindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	syncJournal := filepath.Join(t.TempDir(), "sync-tool.jsonl")
	syncIntent := SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch, BrokerBindingID: brokerBindingID, BrokerCatalogID: binding.CatalogID}
	observed, err := client.SubmitSynchronousToolTurn(ctx, syncJournal, contextJournal, syncIntent)
	if err != nil {
		transcript, transcriptErr := client.read(ctx, "/session/"+sessionID+"/message")
		if len(transcript) > 8192 {
			transcript = transcript[:8192]
		}
		requests := provider.snapshot()
		requestFacts := make([]string, len(requests))
		for index, request := range requests {
			requestFacts[index] = fmt.Sprintf("%d:path=%s accept=%q content_type=%q content_length=%d transfer=%q user_agent=%q auth_present=%t shape=%s", index+1, request.Path, request.Accept, request.ContentType, request.ContentLength, request.TransferEncoding, request.UserAgent, request.Authorization != "", responsesProbeRequestShape(request.Body))
		}
		t.Fatalf("synchronous tool-loop request failed: %v; provider requests: %s; transcript=%s transcript_err=%v; logs: %s", err, strings.Join(requestFacts, " | "), transcript, transcriptErr, providerProbeLogs(hostRoot))
	}

	contextState, err := contextbroker.Inspect(contextJournal)
	if err != nil || contextState.Pending != nil || contextState.Calls != 1 || len(contextState.Responses) != 1 || !contextState.Responses[0].Success {
		t.Fatal("durable context response mismatch", contextState, err)
	}
	var chunk repository.SourceChunk
	if err := canonical.Decode(contextState.Responses[0].Content, &chunk); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(content) != committed || chunk.Commit != source.Commit || chunk.RepositoryID == "" {
		t.Fatal("source_read did not return exact committed bytes", string(content), chunk, err)
	}
	events, err := journal.Read(contextJournal)
	if err != nil || len(events) != 3 || events[1].Kind != "context.request" || events[2].Kind != "context.response" {
		t.Fatal("durable request/response ordering mismatch", events, err)
	}
	var durableRequest contextbroker.Request
	if err := canonical.Decode(events[1].Payload, &durableRequest); err != nil || durableRequest.Tool != "source_read" || string(durableRequest.Arguments) != `{"limit":64,"offset":0,"path":"source.txt"}` {
		t.Fatal("durable source request mismatch", durableRequest, err)
	}
	var durableResponse contextbroker.Response
	if err := canonical.Decode(events[2].Payload, &durableResponse); err != nil || !reflect.DeepEqual(durableResponse, contextState.Responses[0]) {
		t.Fatal("durable source response bytes mismatch", durableResponse, contextState.Responses[0], err)
	}

	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("provider received %d requests, want exactly two", len(requests))
	}
	t.Logf("RESPONSES_PROVIDER_REQUEST_SHAPES first=%s second=%s", responsesProbeRequestShape(requests[0].Body), responsesProbeRequestShape(requests[1].Body))
	for _, request := range requests {
		if request.Method != http.MethodPost || request.Path != "/v1/responses" || request.Authorization != "Bearer "+responsesProbeGateway {
			t.Fatal("provider wire endpoint or fixture credential changed")
		}
	}
	t.Logf("RESPONSES_PROVIDER_HEADERS first_accept=%q first_content_type=%q first_content_length=%d first_transfer=%q first_user_agent=%q second_accept=%q second_content_type=%q second_content_length=%d second_transfer=%q second_user_agent=%q", requests[0].Accept, requests[0].ContentType, requests[0].ContentLength, requests[0].TransferEncoding, requests[0].UserAgent, requests[1].Accept, requests[1].ContentType, requests[1].ContentLength, requests[1].TransferEncoding, requests[1].UserAgent)
	firstResponse, secondResponse := assertResponsesProbeSSE(t, provider.responseSnapshot(), "source_read", responsesProbeCallID, `{"path":"source.txt","offset":0,"limit":64}`)
	assertResponsesProbeRequests(t, requests, invocation.ID, sessionID, projection, "source_read")
	assertResponsesProbeFirstRequest(t, requests[0], "source_read")
	assertResponsesProbeResultRequest(t, requests[1], contextState.Responses[0], "source_read", responsesProbeCallID, []byte(`"content_utf8":"committed-only-responses-probe"`))

	transcript, err := client.read(ctx, "/session/"+sessionID+"/message")
	if err != nil || len(transcript) == 0 || len(transcript) > 1<<20 {
		t.Fatal("bounded raw tool transcript unavailable", len(transcript), err)
	}
	transcriptHash := sha256.Sum256(transcript)
	if !bytesContainAll(transcript, []byte(responsesProbeCallID), []byte("source_read"), []byte(responsesProbeFinalText)) {
		diagnostic := transcript
		if len(diagnostic) > 4096 {
			diagnostic = diagnostic[:4096]
		}
		t.Fatalf("raw transcript lacks tool-loop markers: %s", diagnostic)
	}
	if observed.Text != responsesProbeFinalText || len(observed.Generations) < 2 || len(observed.Calls) != 1 || observed.Calls[0].ProviderCallID != responsesProbeCallID || observed.Calls[0].RequestID != durableRequest.RequestID {
		t.Fatal("decoded source tool turn mismatch", observed, err)
	}
	lastGeneration := observed.Generations[len(observed.Generations)-1]
	if observed.Calls[0].ProviderItemID != ProviderItemID(firstResponse.FunctionCalls[0].ItemID) || len(secondResponse.TextOutputs) != 1 || len(lastGeneration.TextProviderItemIDs) != 1 || lastGeneration.TextProviderItemIDs[0] != ProviderItemID(secondResponse.TextOutputs[0].ItemID) {
		t.Fatal("OpenCode provider metadata item IDs do not match native Responses items", observed.Calls[0].ProviderItemID, firstResponse.FunctionCalls[0].ItemID, lastGeneration.TextProviderItemIDs, secondResponse.TextOutputs)
	}
	providerRequestsBeforeRecovery := len(provider.snapshot())
	var offline *Client
	recovered, err := offline.RecoverSynchronousToolTurn(context.Background(), syncJournal, contextJournal, syncIntent)
	if err != nil || !reflect.DeepEqual(recovered, observed) || len(provider.snapshot()) != providerRequestsBeforeRecovery {
		t.Fatal("offline source tool turn recovery mismatch", recovered, observed, err)
	}
	statusAfter, err := client.ReadMCPStatus(ctx)
	if err != nil {
		t.Fatal("post-turn MCP status gate failed", err)
	}
	providerRequestsBeforeSeal := len(provider.snapshot())
	sealPath := filepath.Join(t.TempDir(), "tool-turn-seal.jsonl")
	sealExpected := SynchronousToolTurnSealExpected{
		Dispatch: syncIntent, Session: sessionBinding, Tools: toolsReceipt,
		ExecutableSHA256: responsesProbeDigest, HostRoot: hostRoot, MaxOutputTokens: 128, Diagnostic: false,
	}
	sealCtx, sealCancel := context.WithTimeout(ctx, 10*time.Second)
	terminalReceipt, err := SealSynchronousToolTurn(sealCtx, sealPath, syncJournal, contextJournal, sealExpected, toolsSpec, client, process, running, broker)
	sealCancel()
	if err != nil || !terminalReceipt.MCPHandlersStopped || !terminalReceipt.RootProcessReaped || !terminalReceipt.BrokerClosed {
		t.Fatal("source tool turn terminal seal failed", terminalReceipt, err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("sealed source OpenCode root was not reaped")
	}
	closedContextState, err := contextbroker.Inspect(contextJournal)
	if err != nil || !closedContextState.Closed || closedContextState.Pending != nil || closedContextState.Calls != contextState.Calls || len(provider.snapshot()) != providerRequestsBeforeSeal {
		t.Fatal("source terminal state changed admitted work", closedContextState, err)
	}
	recoveredSeal, err := RecoverSynchronousToolTurnSeal(sealPath, syncJournal, contextJournal, sealExpected)
	if err != nil || !reflect.DeepEqual(recoveredSeal, terminalReceipt) {
		t.Fatal("offline source terminal seal recovery mismatch", recoveredSeal, terminalReceipt, err)
	}
	sealJournalBytes, err := journal.ExportJSONL(sealPath)
	if err != nil {
		t.Fatal(err)
	}
	sealEvents, err := journal.Read(sealPath)
	if err != nil || len(sealEvents) != 2 || sealEvents[0].Kind != "opencode.tool-turn-seal-intent" || sealEvents[1].Kind != "opencode.tool-turn-sealed" {
		t.Fatal("durable source terminal seal journal mismatch", sealEvents, err)
	}
	sealJournalHash := sha256.Sum256(sealJournalBytes)
	terminalReceiptBytes, err := canonical.Bytes(terminalReceipt)
	if err != nil {
		t.Fatal(err)
	}
	terminalReceiptHash := sha256.Sum256(terminalReceiptBytes)
	firstProviderHash := sha256.Sum256(requests[0].Body)
	secondProviderHash := sha256.Sum256(requests[1].Body)
	receiptBytes, err := canonical.Bytes(durableResponse)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash := sha256.Sum256(receiptBytes)
	syncJournalBytes, err := journal.ExportJSONL(syncJournal)
	if err != nil {
		t.Fatal(err)
	}
	syncEvents, err := journal.Read(syncJournal)
	if err != nil || len(syncEvents) != 2 || syncEvents[0].Kind != "opencode.sync-tool-intent" || syncEvents[1].Kind != "opencode.sync-tool-observed" {
		t.Fatal("durable source synchronous tool journal mismatch", syncEvents, err)
	}
	syncJournalHash := sha256.Sum256(syncJournalBytes)
	t.Logf("RESPONSES_PROBE_PASS invocation_id=%s broker_binding_id=%s broker_catalog_id=%s commit=%s catalog_sha256=%s projected_catalog_sha256=%s schema_projection=%s config_sha256=%s process_identity_sha256=%s project_sha256=%s status_sha256=%s transcript_sha256=%s provider1_sha256=%s provider2_sha256=%s response1_sha256=%s response2_sha256=%s receipt_sha256=%s sync_journal_sha256=%s seal_journal_sha256=%s terminal_receipt_sha256=%s context_request_id=%s provider_requests=%d tool_calls=%d", invocation.ID, brokerBindingID, binding.CatalogID, source.Commit, running.CatalogHash(), projection.ProjectedCatalogSHA256, projection.Projection, toolsReceipt.SHA256, providerIdentity.SHA256, project.SHA256, statusAfter.SHA256, hex.EncodeToString(transcriptHash[:]), hex.EncodeToString(firstProviderHash[:]), hex.EncodeToString(secondProviderHash[:]), firstResponse.SHA256, secondResponse.SHA256, hex.EncodeToString(receiptHash[:]), hex.EncodeToString(syncJournalHash[:]), hex.EncodeToString(sealJournalHash[:]), hex.EncodeToString(terminalReceiptHash[:]), durableRequest.RequestID, len(requests), contextState.Calls)
}
