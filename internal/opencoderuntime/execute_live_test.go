package opencoderuntime

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
)

const (
	executeLiveDigest = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	executeLiveModel  = "wire-responses-model"
	executeLiveSecret = "execute-live-provider-secret"
	executeLiveCallID = "call_engorch_execute_source_read_1"
	executeLiveText   = "synthetic Execute tool loop complete"
)

type executeLiveProvider struct {
	mu       sync.Mutex
	requests []json.RawMessage
}

type executeLiveOutput struct {
	mu  sync.Mutex
	raw strings.Builder
}

func (o *executeLiveOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.raw.Write(p)
}

func (o *executeLiveOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.raw.String()
}

func (p *executeLiveProvider) snapshot() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		result[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	return result
}

func (p *executeLiveProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+executeLiveSecret {
		http.Error(writer, "invalid fixture provider request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	sequence := len(p.requests)
	p.mu.Unlock()
	var response []byte
	switch sequence {
	case 1:
		response = executeLiveFunctionSSE()
	case 2:
		response = executeLiveTextSSE()
	default:
		http.Error(writer, "unexpected extra provider request", http.StatusConflict)
		return
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(response)
}

func executeLiveSnapshot(id, status string, output, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": 123, "status": status,
		"background": false, "completed_at": nil, "error": nil, "frequency_penalty": 0.0,
		"incomplete_details": nil, "instructions": nil, "max_output_tokens": 128,
		"max_tool_calls": nil, "model": executeLiveModel, "moderation": nil, "output": output,
		"parallel_tool_calls": true, "presence_penalty": 0.0, "previous_response_id": nil,
		"prompt_cache_key": nil, "prompt_cache_retention": nil,
		"reasoning":         map[string]any{"effort": "high", "summary": "auto"},
		"safety_identifier": nil, "service_tier": "default", "store": false, "temperature": 1.0,
		"text":        map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "high"},
		"tool_choice": "auto", "tools": []any{}, "top_logprobs": 0, "top_p": 1.0,
		"truncation": "disabled", "usage": usage, "user": nil, "metadata": map[string]any{},
	}
}

func executeLiveEvents(events ...map[string]any) []byte {
	var output strings.Builder
	for _, event := range events {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&output, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
	return []byte(output.String())
}

func executeLiveFunctionSSE() []byte {
	id, item, reasoning := "resp_execute_tool_1", "fc_execute_tool_1", "rs_execute_reasoning_1"
	arguments := `{"path":"source.txt","offset":0,"limit":64}`
	addedReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "execute-added", "summary": []any{}}
	doneReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "execute-done", "summary": []any{}}
	terminalReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "execute-terminal", "summary": []any{}}
	pending := map[string]any{"id": item, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": executeLiveCallID, "name": "engorch_source_read"}
	done := map[string]any{"id": item, "type": "function_call", "status": "completed", "arguments": arguments, "call_id": executeLiveCallID, "name": "engorch_source_read"}
	return executeLiveEvents(
		map[string]any{"type": "response.created", "response": executeLiveSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": executeLiveSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": addedReasoning, "sequence_number": 2},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": doneReasoning, "sequence_number": 3},
		map[string]any{"type": "response.output_item.added", "output_index": 1, "item": pending, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": arguments, "item_id": item, "obfuscation": "fixture", "output_index": 1, "sequence_number": 5},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": arguments, "item_id": item, "output_index": 1, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": executeLiveSnapshot(id, "completed", []any{terminalReasoning, done}, map[string]any{"input_tokens": 9, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 1}, "total_tokens": 13}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func executeLiveTextSSE() []byte {
	id, item := "resp_execute_text_2", "msg_execute_text_2"
	pending := map[string]any{"id": item, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"}
	empty := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""}
	part := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": executeLiveText}
	done := map[string]any{"id": item, "type": "message", "status": "completed", "content": []any{part}, "role": "assistant"}
	return executeLiveEvents(
		map[string]any{"type": "response.created", "response": executeLiveSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": executeLiveSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": pending, "sequence_number": 2},
		map[string]any{"type": "response.content_part.added", "content_index": 0, "item_id": item, "output_index": 0, "part": empty, "sequence_number": 3},
		map[string]any{"type": "response.output_text.delta", "content_index": 0, "delta": executeLiveText, "item_id": item, "logprobs": []any{}, "obfuscation": "fixture", "output_index": 0, "sequence_number": 4},
		map[string]any{"type": "response.output_text.done", "content_index": 0, "item_id": item, "logprobs": []any{}, "output_index": 0, "sequence_number": 5, "text": executeLiveText},
		map[string]any{"type": "response.content_part.done", "content_index": 0, "item_id": item, "output_index": 0, "part": part, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": executeLiveSnapshot(id, "completed", []any{done}, map[string]any{"input_tokens": 18, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 21}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func TestPinnedOpenCodeExecuteResponsesSourceRead(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	runPinnedOpenCodeExecuteResponsesSourceRead(t)
}

func runPinnedOpenCodeExecuteResponsesSourceRead(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	sourceRoot := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", sourceRoot}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture failed: %v: %s", err, output)
		}
	}
	git("init", "-q")
	const committed = "committed-only-execute-live"
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte(committed), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(ctx, sourceRoot, "opencode-execute-live")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("dirty-checkout-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	prompt := "Use the permitted source_read tool once to read source.txt from offset 0 with limit 64, then answer with the fixture completion."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-responses-fixture", Model: executeLiveModel, Effort: "high", Role: "explorer"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	contextBinding, err := contextbroker.NewBinding(invocation.ID, source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: contextmcp.MaxContentBytes})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
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

	provider := &executeLiveProvider{}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	adapterCapabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, Reasoning: true, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{"prompt_cache_key"}, TrailingCostPingV1: true})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := providergateway.EndpointContract{Version: 2, Provider: invocation.Profile.Provider, URL: upstream.URL + "/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "execute-live-key"}}
	model := providergateway.ModelContract{Version: 2, Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: adapterCapabilities, ContextWindowTokens: 8192, MaxCalls: 2, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	profile := access.Profile{Version: 1, Name: "execute-live", Kind: "subscription", Runtime: invocation.Profile.Runtime, Provider: invocation.Profile.Provider, CredentialRef: "execute-live-key", RepositoryClasses: []access.Class{access.Public}}
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
	inputID, err := access.InputID(prompt)
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
	required := &providergateway.RequiredCapabilities{Tools: true, Reasoning: true, StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 128, StateMode: "full-input-stateless", Store: false, SystemRole: "developer", ReasoningEffort: "high", ReasoningSummary: "auto", ToolChoice: "auto", TextVerbosity: "high", Include: []string{"reasoning.encrypted_content"}, FunctionToolStrict: false})
	if err != nil {
		t.Fatal(err)
	}
	expectation := providergateway.AdapterRequestExpectation{MaxBytes: 1 << 20, MaxOutputTokens: 128, Tools: []providergateway.RequestTool{{Name: projected[0].Name, Description: projected[0].Description, Parameters: projected[0].InputSchema}}, Controls: controls, RequiredCapabilities: required}
	if _, err := providergateway.AdapterRequestExpectationID(gateway, expectation); err != nil {
		withoutRequired := expectation
		withoutRequired.RequiredCapabilities = nil
		_, withoutRequiredErr := providergateway.AdapterRequestExpectationID(gateway, withoutRequired)
		withoutTools := withoutRequired
		withoutTools.Tools = nil
		_, withoutToolsErr := providergateway.AdapterRequestExpectationID(gateway, withoutTools)
		t.Fatal("live request expectation invalid before Execute:", err, "without required:", withoutRequiredErr, "without tools:", withoutToolsErr)
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
	runtimeIntent := Intent{Version: 2, Invocation: invocation, Directory: sourceRoot, Project: opencode.ProjectExpectation{Directory: sourceRoot, Mode: opencode.ProjectModeGit, Worktree: sourceRoot}, Context: contextBinding, Session: opencode.ToolSessionBinding{ToolNames: []string{}}, SessionPlan: sessionPlan, ProviderGatewayBindingID: gatewayID}
	runtimeIntent.IntentID, err = runtimeIntent.ID()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	paths := Paths{Version: 1, Session: filepath.Join(root, "session.jsonl"), Dispatch: filepath.Join(root, "dispatch.jsonl"), Broker: contextPath, Seal: filepath.Join(root, "seal.jsonl"), Gateway: gatewayPath}
	role := config.ResolvedProviderRole{Endpoint: endpoint, Model: model, AdapterControls: controls, Variant: config.ProviderVariant{Effort: "high", SystemRole: "developer", ReasoningSummary: "auto", TextFormat: "plain", TextVerbosity: "high"}, RequiredCapabilities: required, CredentialEnvironment: "ENGORCH_EXECUTE_LIVE_KEY"}
	output := &executeLiveOutput{}
	execution := ExecuteConfig{RuntimePath: filepath.Join(root, "runtime.jsonl"), StateRoot: hostRoot, Paths: paths, Intent: runtimeIntent, AccessJournalPath: accessPath, Policy: policy, AccessIntent: accessIntent, Gateway: gateway, Credential: credential, Transport: transport, Broker: broker, ProviderRole: role, RequestExpectation: expectation, Executable: filepath.Join(repositoryRoot, ".local", "toolchains", "opencode-1.18.29", "opencode.exe"), ExecutableSHA256: executeLiveDigest, Port: executeLivePort(t), ParentMessageID: "msg_execute_live", Output: output, ProviderTimeout: 20 * time.Second, ToolTimeout: 5 * time.Second, ReadinessTimeout: 30 * time.Second, SealTimeout: 10 * time.Second, Diagnostic: os.Getenv("ENGORCH_OPENCODE_EXECUTE_DIAGNOSTIC") == "1"}
	result, err := Execute(ctx, execution)
	if err != nil {
		t.Fatalf("production Execute path failed: %v; provider_requests=%d; output=%s", err, len(provider.snapshot()), output.String())
	}
	requests := provider.snapshot()
	if result.Result.Output != executeLiveText || len(requests) != 2 || result.Result.Usage.InputTokens == nil || *result.Result.Usage.InputTokens != 27 || result.Result.Usage.OutputTokens == nil || *result.Result.Usage.OutputTokens != 7 {
		t.Fatal("Execute result or provider request count mismatch", result, len(requests))
	}
	contextState, err := contextbroker.Inspect(contextPath)
	if err != nil || !contextState.Closed || contextState.Calls != 1 || len(contextState.Responses) != 1 {
		t.Fatal("Execute context broker did not seal one call", contextState, err)
	}
	var chunk repository.SourceChunk
	if err := canonical.Decode(contextState.Responses[0].Content, &chunk); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(content) != committed || chunk.Commit != source.Commit {
		t.Fatal("Execute source_read did not preserve committed source", string(content), chunk, err)
	}
	gatewayState, err := providergateway.Inspect(gatewayPath)
	if err != nil || gatewayState.Pending != nil || !gatewayState.Finished || gatewayState.Exhausted || len(gatewayState.Calls) != 2 {
		t.Fatal("Execute provider gateway did not finish exactly", gatewayState, err)
	}
	beforeRecovery := len(provider.snapshot())
	recovered, err := Execute(context.Background(), ExecuteConfig{RuntimePath: execution.RuntimePath, Intent: runtimeIntent})
	if err != nil || !reflect.DeepEqual(recovered, result) || len(provider.snapshot()) != beforeRecovery {
		t.Fatal("offline Execute recovery changed result or repeated provider effects", recovered, err, len(provider.snapshot()))
	}
	t.Logf("OPENCODE_EXECUTE_LIVE_PASS runtime_intent=%s invocation=%s executable_sha256=%s source_bytes_sha256=%s provider_requests=%d context_calls=%d result_head=%s", runtimeIntent.IntentID, invocation.ID, executeLiveDigest, executeLiveHash(committed), len(requests), contextState.Calls, result.SealHead)
}

type executeLiveBenchmarkSample struct {
	Task             int     `json:"task"`
	ElapsedMillis    float64 `json:"elapsed_ms"`
	Completed        bool    `json:"completed"`
	ProviderRequests int     `json:"provider_requests"`
	BrokerCalls      int     `json:"broker_calls"`
}

func TestPinnedOpenCodeExecuteBenchmarkLevel(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_BENCHMARK") != "1" {
		t.Skip("explicit pinned OpenCode Execute benchmark opt-in required")
	}
	width, err := strconv.Atoi(os.Getenv("ENGORCH_OPENCODE_EXECUTE_CONCURRENCY"))
	if err != nil || width < 1 || width > 8 {
		t.Fatal("bounded benchmark concurrency required")
	}
	tasks, err := strconv.Atoi(os.Getenv("ENGORCH_OPENCODE_EXECUTE_TASKS"))
	if err != nil || tasks < 1 || tasks > 32 {
		t.Fatal("bounded benchmark task count required")
	}
	started := time.Now()
	jobs := make(chan int)
	var mu sync.Mutex
	samples := make([]executeLiveBenchmarkSample, 0, tasks)
	var workers sync.WaitGroup
	for worker := 0; worker < width; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for task := range jobs {
				taskStarted := time.Now()
				ok := t.Run(fmt.Sprintf("task_%02d", task), func(st *testing.T) {
					runPinnedOpenCodeExecuteResponsesSourceRead(st)
				})
				mu.Lock()
				sample := executeLiveBenchmarkSample{Task: task, ElapsedMillis: float64(time.Since(taskStarted).Microseconds()) / 1000, Completed: ok}
				if ok {
					sample.ProviderRequests = 2
					sample.BrokerCalls = 1
				}
				samples = append(samples, sample)
				mu.Unlock()
			}
		}()
	}
	for task := 1; task <= tasks; task++ {
		jobs <- task
	}
	close(jobs)
	workers.Wait()
	sort.Slice(samples, func(i, j int) bool { return samples[i].Task < samples[j].Task })
	raw, err := json.Marshal(struct {
		Concurrency         int                          `json:"concurrency"`
		Tasks               int                          `json:"tasks"`
		ElapsedMillis       float64                      `json:"elapsed_ms"`
		ExecutableSHA256    string                       `json:"executable_sha256"`
		FixtureSourceSHA256 string                       `json:"fixture_source_sha256"`
		Samples             []executeLiveBenchmarkSample `json:"samples"`
	}{Concurrency: width, Tasks: tasks, ElapsedMillis: float64(time.Since(started).Microseconds()) / 1000, ExecutableSHA256: executeLiveDigest, FixtureSourceSHA256: executeLiveHash("committed-only-execute-live"), Samples: samples})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("OPENCODE_EXECUTE_BENCHMARK_RESULT %s", raw)
}

func executeLiveHash(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func executeLivePort(t *testing.T) int {
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
