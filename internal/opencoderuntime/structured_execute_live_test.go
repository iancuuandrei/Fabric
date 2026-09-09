package opencoderuntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/writercontract"
)

const (
	structuredExecuteLiveProviderID              = "engorch-opencode-zen"
	structuredExecuteLiveModel                   = "muse-spark-1.3-contributor-free"
	structuredExecuteLiveCallID                  = "call_engorch_structured_output_1"
	structuredExecuteLiveItemID                  = "fc_structured_output_1"
	structuredExecuteLiveValue                   = `{"candidate_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","changes":[{"path":"new.txt","before_hash":null,"content_utf8":"fixture writer output","executable":false}]}`
	structuredExecuteLiveExecutableEnv           = "ENGORCH_OPENCODE_STRUCTURED_EXECUTE_EXECUTABLE"
	structuredExecuteLiveExecutableSHA256Env     = "ENGORCH_OPENCODE_STRUCTURED_EXECUTE_EXECUTABLE_SHA256"
	structuredExecuteLiveDefaultExecutable       = `D:\dev\EngOrch-M0\tools\opencode.exe`
	structuredExecuteLiveDefaultExecutableSHA256 = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
)

type structuredExecuteLiveProvider struct {
	mu       sync.Mutex
	requests []json.RawMessage
	rejects  []string
}

func (p *structuredExecuteLiveProvider) snapshot() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		result[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	return result
}

func (p *structuredExecuteLiveProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+executeLiveSecret {
		reason := fmt.Sprintf("method=%s uri=%s auth_present=%t auth_match=%t body_bytes=%d json=%t read_error=%t", request.Method, request.URL.RequestURI(), request.Header.Get("authorization") != "", request.Header.Get("authorization") == "Bearer "+executeLiveSecret, len(body), json.Valid(body), err != nil)
		p.mu.Lock()
		p.rejects = append(p.rejects, reason)
		p.mu.Unlock()
		http.Error(writer, "invalid structured Execute fixture provider request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	if len(p.requests) != 0 {
		p.mu.Unlock()
		p.mu.Lock()
		p.rejects = append(p.rejects, "duplicate provider dispatch")
		p.mu.Unlock()
		http.Error(writer, "structured Execute fixture permits one provider dispatch", http.StatusConflict)
		return
	}
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	p.mu.Unlock()
	response := structuredExecuteLiveSSE()
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	writer.Header().Set("content-length", fmt.Sprint(len(response)))
	_, _ = writer.Write(response)
}

func (p *structuredExecuteLiveProvider) rejectionSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.rejects...)
}

func structuredExecuteLiveSSE() []byte {
	responseID := "resp_structured_execute_1"
	arguments := structuredExecuteLiveValue
	pending := map[string]any{
		"id": structuredExecuteLiveItemID, "type": "function_call", "status": "in_progress",
		"arguments": "", "call_id": structuredExecuteLiveCallID, "name": opencode.StructuredOutputToolName,
	}
	done := map[string]any{
		"id": structuredExecuteLiveItemID, "type": "function_call", "status": "completed",
		"arguments": arguments, "call_id": structuredExecuteLiveCallID, "name": opencode.StructuredOutputToolName,
	}
	return executeLiveEvents(
		map[string]any{"type": "response.created", "response": structuredExecuteLiveSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": structuredExecuteLiveSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": pending, "sequence_number": 2},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": arguments[:len(arguments)/2], "item_id": structuredExecuteLiveItemID, "output_index": 0, "sequence_number": 3},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": arguments[len(arguments)/2:], "item_id": structuredExecuteLiveItemID, "output_index": 0, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": arguments, "item_id": structuredExecuteLiveItemID, "output_index": 0, "sequence_number": 5},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": done, "sequence_number": 6},
		map[string]any{"type": "response.completed", "response": structuredExecuteLiveSnapshot(responseID, "completed", []any{done}, map[string]any{"input_tokens": 7, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 10}), "sequence_number": 7},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func structuredExecuteLiveSnapshot(id, status string, output, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": 123, "status": status,
		"background": false, "completed_at": nil, "error": nil, "frequency_penalty": 0.0,
		"incomplete_details": nil, "instructions": nil, "max_output_tokens": 128,
		"max_tool_calls": nil, "model": structuredExecuteLiveModel, "moderation": nil, "output": output,
		"parallel_tool_calls": true, "presence_penalty": 0.0, "previous_response_id": nil,
		"prompt_cache_key": nil, "prompt_cache_retention": nil,
		"reasoning":         map[string]any{"effort": "none", "summary": "auto"},
		"safety_identifier": nil, "service_tier": "default", "store": false, "temperature": 1.0,
		"text":        map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "high"},
		"tool_choice": "auto", "tools": []any{}, "top_logprobs": 0, "top_p": 1.0,
		"truncation": "disabled", "usage": usage, "user": nil, "metadata": map[string]any{},
	}
}

func TestPinnedOpenCodeExecuteResponsesStructuredWriter(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_STRUCTURED_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode structured Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	executable, executableSHA256 := structuredExecuteLivePinnedExecutable(t)

	sourceRoot := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", sourceRoot}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture failed: %v: %s", err, output)
		}
	}
	const committed = "committed-only-structured-execute-live"
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte(committed), 0600); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(ctx, sourceRoot, "opencode-structured-execute-live")
	if err != nil {
		t.Fatal(err)
	}

	schema := writercontract.UTF8Schema()
	expectation, err := opencode.NewStructuredOutputExpectation(schema)
	if err != nil {
		t.Fatal("build UTF8 structured output expectation", err)
	}
	input := `{"output_schema":` + string(schema) + `,"instruction":"Return the fixture writer proposal through the StructuredOutput channel."}`
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: structuredExecuteLiveProviderID, Model: structuredExecuteLiveModel, Effort: "none", Role: "writer"}, input)
	if err != nil {
		t.Fatal(err)
	}
	contextBinding, err := contextbroker.NewBinding(invocation.ID, source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: contextmcp.MaxContentBytes})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "engorch-structured-execute-live-")
	if err != nil {
		t.Fatal(err)
	}
	rootKept := false
	t.Cleanup(func() {
		if !rootKept {
			t.Logf("structured Execute diagnostic root retained at %s", root)
			return
		}
		_ = os.RemoveAll(root)
	})
	contextPath := filepath.Join(root, "context.jsonl")
	broker, err := contextbroker.Open(contextPath, contextBinding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	catalogProbe, err := contextmcp.NewOwned(broker, strings.Repeat("c", 32), nil)
	if err != nil {
		t.Fatal(err)
	}
	probeRunning, err := catalogProbe.Listen()
	if err != nil {
		t.Fatal(err)
	}
	catalogSHA256 := probeRunning.CatalogHash()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := probeRunning.Close(closeCtx); err != nil {
		closeCancel()
		t.Fatal(err)
	}
	closeCancel()
	if err := probeRunning.Wait(); err != nil {
		t.Fatal(err)
	}

	provider := &structuredExecuteLiveProvider{}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	adapterCapabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{
		Version: 1, FunctionTools: true, Reasoning: false,
		SystemRoles: []string{"system"}, TextFormats: []string{"plain"},
		KnownExtensions: []string{"prompt_cache_key"}, TrailingCostPingV1: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: invocation.Profile.Provider, URL: upstream.URL + "/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "execute-live-key"}}
	model := providergateway.ModelContract{Version: 2, Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, Reasoning: false, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: adapterCapabilities, ContextWindowTokens: 8192, MaxCalls: 1, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	profile := access.Profile{Version: 1, Name: "execute-structured-live", Kind: "subscription", Runtime: invocation.Profile.Runtime, Provider: invocation.Profile.Provider, CredentialRef: "execute-live-key", RepositoryClasses: []access.Class{access.Public}}
	profileID, err := profile.ID()
	if err != nil {
		t.Fatal(err)
	}
	route := access.Route{Version: 1, Role: invocation.Profile.Role, Runtime: invocation.Profile.Runtime, Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Effort: invocation.Profile.Effort, AccessID: profileID, Permission: "read-only"}
	reservation, _, err := model.ConservativeReservation()
	if err != nil {
		t.Fatal(err)
	}
	policy := access.Policy{Version: 1, RunID: executeLiveHash("run:" + root), Class: access.Public, Limits: access.Limits{Tokens: reservation, Concurrency: 1}, Routes: []access.Route{route}, Profiles: []access.Profile{profile}}
	policyID, err := policy.ID()
	if err != nil {
		t.Fatal(err)
	}
	inputID, err := access.InputID(input)
	if err != nil {
		t.Fatal(err)
	}
	accessIntent := access.Intent{Attempt: 1, PolicyID: policyID, InputHash: inputID, Route: route, Reservation: access.Reservation{Tokens: reservation, BillingMode: "subscription"}}
	accessIntent.Reservation.InvocationID, err = accessIntent.ID()
	if err != nil {
		t.Fatal(err)
	}
	accessPath := filepath.Join(root, "access.jsonl")
	if err := access.ReserveDurable(accessPath, policy, accessIntent); err != nil {
		t.Fatal(err)
	}
	gatewayPath := filepath.Join(root, "gateway.jsonl")
	gateway, err := providergateway.Bind(gatewayPath, accessPath, policy, accessIntent, endpoint, model)
	if err != nil {
		t.Fatal(err)
	}
	gatewayID, err := gateway.ID()
	if err != nil {
		t.Fatal(err)
	}
	projected, err := providerToolsForSession(broker.Catalog(), []string{"source_read"})
	if err != nil || len(projected) != 1 {
		t.Fatal("provider tool projection failed", projected, err)
	}
	required := &providergateway.RequiredCapabilities{Tools: true, Reasoning: false, StructuredOutput: providergateway.StructuredOutputUnsupported}
	terminal, err := ProviderTerminalStructuredOutputExpectation(&expectation)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 128, StateMode: "full-input-stateless", Store: false, SystemRole: "system", ToolChoice: "auto", TextVerbosity: "high", Include: []string{}, FunctionToolStrict: false})
	if err != nil {
		t.Fatal(err)
	}
	expectationRequest := providergateway.AdapterRequestExpectation{MaxBytes: 1 << 20, MaxOutputTokens: 128, Tools: []providergateway.RequestTool{{Name: projected[0].Name, Description: projected[0].Description, Parameters: projected[0].InputSchema}}, Controls: controls, RequiredCapabilities: required, TerminalStructuredOutput: terminal}
	if _, err := providergateway.AdapterRequestExpectationID(gateway, expectationRequest); err != nil {
		t.Fatal("structured Execute request expectation invalid", err)
	}
	sourceCredential, err := providercredential.NewEnvironmentSource(map[string]string{"execute-live-key": "ENGORCH_EXECUTE_LIVE_KEY"}, func(name string) (string, bool) { return executeLiveSecret, name == "ENGORCH_EXECUTE_LIVE_KEY" })
	if err != nil {
		t.Fatal(err)
	}
	credential, err := providercredential.Resolve(ctx, accessPath, policy, accessIntent, endpoint, sourceCredential)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}

	hostRoot := filepath.Join(root, "host")
	if err := os.Mkdir(hostRoot, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(hostRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	sessionPlan := &SessionPlan{Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort, ToolNames: []string{"source_read"}, CatalogSHA256: catalogSHA256}
	runtimeIntent := Intent{Version: 2, Invocation: invocation, Directory: sourceRoot, Project: opencode.ProjectExpectation{Directory: sourceRoot, Mode: opencode.ProjectModeGit, Worktree: sourceRoot}, Context: contextBinding, Session: opencode.ToolSessionBinding{ToolNames: []string{}}, SessionPlan: sessionPlan, ProviderGatewayBindingID: gatewayID, StructuredOutput: &expectation}
	runtimeIntent.IntentID, err = runtimeIntent.ID()
	if err != nil {
		t.Fatal(err)
	}
	paths := Paths{Version: 1, Session: filepath.Join(root, "session.jsonl"), Dispatch: filepath.Join(root, "dispatch.jsonl"), Broker: contextPath, Seal: filepath.Join(root, "seal.jsonl"), Gateway: gatewayPath}
	role := config.ResolvedProviderRole{Endpoint: endpoint, Model: model, AdapterControls: controls, Variant: config.ProviderVariant{Effort: "none", SystemRole: "system", TextFormat: "plain", TextVerbosity: "high"}, RequiredCapabilities: required}
	output := &executeLiveOutput{}
	execution := ExecuteConfig{RuntimePath: filepath.Join(root, "runtime.jsonl"), StateRoot: hostRoot, Paths: paths, Intent: runtimeIntent, AccessJournalPath: accessPath, Policy: policy, AccessIntent: accessIntent, Gateway: gateway, Credential: credential, Transport: transport, Broker: broker, ProviderRole: role, RequestExpectation: expectationRequest, Executable: executable, ExecutableSHA256: executableSHA256, Port: executeLivePort(t), ParentMessageID: "msg_structured_execute_live", Output: output, ProviderTimeout: 20 * time.Second, ToolTimeout: 5 * time.Second, ReadinessTimeout: 30 * time.Second, SealTimeout: 10 * time.Second, Diagnostic: os.Getenv("ENGORCH_OPENCODE_EXECUTE_DIAGNOSTIC") == "1"}
	result, err := Execute(ctx, execution)
	if err != nil {
		t.Fatalf("production structured Execute path failed: %v; provider_requests=%d; provider_rejections=%v; output=%s; root=%s", err, len(provider.snapshot()), provider.rejectionSnapshot(), output.String(), root)
	}
	requests := provider.snapshot()
	if len(requests) != 1 {
		t.Fatalf("structured Execute performed %d provider dispatches, want exactly one", len(requests))
	}
	assertStructuredExecuteLiveRequest(t, requests[0], expectation)
	wantOutput, err := canonical.Normalize([]byte(structuredExecuteLiveValue))
	if err != nil {
		t.Fatal(err)
	}
	if result.Result.Output != string(wantOutput) || result.Result.Usage.InputTokens == nil || *result.Result.Usage.InputTokens != 7 || result.Result.Usage.OutputTokens == nil || *result.Result.Usage.OutputTokens != 3 {
		t.Fatalf("structured Execute result mismatch: output=%q usage=%+v", result.Result.Output, result.Result.Usage)
	}
	contextState, err := contextbroker.Inspect(contextPath)
	if err != nil || !contextState.Closed || contextState.Calls != 0 {
		t.Fatal("structured Execute context broker did not seal without controller calls", contextState, err)
	}
	gatewayState, err := providergateway.Inspect(gatewayPath)
	if err != nil || gatewayState.Pending != nil || !gatewayState.Finished || gatewayState.Exhausted || len(gatewayState.Calls) != 1 || gatewayState.Calls[0].Receipt == nil || gatewayState.Calls[0].Receipt.Semantic == nil || gatewayState.Calls[0].Receipt.Semantic.TerminalTool == nil || gatewayState.Calls[0].Receipt.Semantic.TerminalTool.Name != providergateway.StructuredOutputToolName {
		t.Fatal("structured Execute provider gateway did not settle exact terminal capture", gatewayState, err)
	}
	beforeRecovery := len(provider.snapshot())
	recovered, err := Execute(context.Background(), execution)
	if err != nil || len(provider.snapshot()) != beforeRecovery || recovered.Result.Output != result.Result.Output || recovered.SealHead != result.SealHead {
		t.Fatal("offline structured Execute recovery changed result or repeated provider effect", recovered, err, len(provider.snapshot()))
	}
	t.Logf("OPENCODE_STRUCTURED_EXECUTE_LIVE_PASS runtime_intent=%s invocation=%s executable_sha256=%s schema_sha256=%s provider_requests=%d gateway_calls=%d result_head=%s", runtimeIntent.IntentID, invocation.ID, executableSHA256, expectation.SchemaSHA256, len(requests), len(gatewayState.Calls), result.SealHead)
	rootKept = true
}

func structuredExecuteLivePinnedExecutable(t *testing.T) (string, string) {
	t.Helper()
	executable := strings.TrimSpace(os.Getenv(structuredExecuteLiveExecutableEnv))
	expected := strings.ToLower(strings.TrimSpace(os.Getenv(structuredExecuteLiveExecutableSHA256Env)))
	if (executable == "") != (expected == "") {
		t.Fatalf("%s and %s must be supplied together", structuredExecuteLiveExecutableEnv, structuredExecuteLiveExecutableSHA256Env)
	}
	defaultExecutable := executable == ""
	if defaultExecutable {
		executable = structuredExecuteLiveDefaultExecutable
		expected = structuredExecuteLiveDefaultExecutableSHA256
	} else {
		if !filepath.IsAbs(executable) {
			t.Fatalf("%s must be an absolute path: %q", structuredExecuteLiveExecutableEnv, executable)
		}
		if len(expected) != sha256.Size*2 {
			t.Fatalf("%s must be a 64-character SHA-256 digest", structuredExecuteLiveExecutableSHA256Env)
		}
		if _, err := hex.DecodeString(expected); err != nil {
			t.Fatalf("%s is not hexadecimal SHA-256: %v", structuredExecuteLiveExecutableSHA256Env, err)
		}
	}
	actual, err := structuredExecuteLiveFileSHA256(executable)
	if err != nil {
		if defaultExecutable {
			t.Skipf("pinned stock OpenCode executable unavailable: %v", err)
		}
		t.Fatalf("pinned OpenCode executable unavailable: %v", err)
	}
	if actual != expected {
		t.Fatalf("OpenCode executable SHA-256 mismatch: path=%q got=%s want=%s", executable, actual, expected)
	}
	return executable, expected
}

func structuredExecuteLiveFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func assertStructuredExecuteLiveRequest(t *testing.T, raw []byte, expectation opencode.StructuredOutputExpectation) {
	t.Helper()
	var body struct {
		Model      string            `json:"model"`
		Stream     bool              `json:"stream"`
		ToolChoice string            `json:"tool_choice"`
		Tools      []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal("decode captured Responses request", err)
	}
	if body.Model != structuredExecuteLiveModel || !body.Stream || body.ToolChoice != "auto" || len(body.Tools) != 2 {
		t.Fatalf("structured Responses request control projection mismatch: model=%q stream=%t tool_choice=%q tools=%d", body.Model, body.Stream, body.ToolChoice, len(body.Tools))
	}
	structuredFound := false
	contextFound := false
	for _, rawTool := range body.Tools {
		var tool struct {
			Type       string          `json:"type"`
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			t.Fatal("decode captured Responses tool", err)
		}
		switch tool.Name {
		case opencode.StructuredOutputToolName:
			structuredFound = true
			schema, err := canonical.Normalize(tool.Parameters)
			if err != nil || !bytes.Equal(schema, expectation.Schema) || tool.Type != "function" {
				t.Fatalf("captured StructuredOutput tool schema/type mismatch: type=%q schema_error=%v", tool.Type, err)
			}
		case "engorch_source_read":
			contextFound = true
		}
	}
	if !structuredFound || !contextFound {
		t.Fatalf("captured Responses request omitted exact native/context tool: structured=%t context=%t", structuredFound, contextFound)
	}
}

// Keep the fixture's loopback-only listener allocation local to this test file
// when it is run without execute_live_test.go during package extraction.
func structuredExecuteLivePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}
