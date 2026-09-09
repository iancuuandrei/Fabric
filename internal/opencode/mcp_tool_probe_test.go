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

	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/worktree"
)

const (
	mcpToolProbeDigest    = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	mcpToolProbeModel     = "tool-wire-model"
	mcpToolProbeBearer    = "mcp-tool-probe-fixture-bearer-001"
	mcpToolCallID         = "call_engorch_source_read_1"
	mcpToolFinalText      = "synthetic tool loop complete"
	mcpCandidateCallID    = "call_engorch_candidate_read_1"
	mcpCandidateFinalText = "synthetic candidate tool loop complete"
)

type mcpToolProviderRequest struct {
	Authorization string
	Method        string
	Path          string
	Body          json.RawMessage
}

type mcpToolProvider struct {
	mu        sync.Mutex
	requests  []mcpToolProviderRequest
	responses [][]byte
	toolName  string
	arguments string
	callID    string
	finalText string
}

func (p *mcpToolProvider) responseSnapshot() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([][]byte, len(p.responses))
	for index := range p.responses {
		result[index] = append([]byte(nil), p.responses[index]...)
	}
	return result
}

func (p *mcpToolProvider) snapshot() []mcpToolProviderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]mcpToolProviderRequest, len(p.requests))
	for index, request := range p.requests {
		result[index] = request
		result[index].Body = append(json.RawMessage(nil), request.Body...)
	}
	return result
}

func (p *mcpToolProvider) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) {
		http.Error(w, "invalid fixture request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, mcpToolProviderRequest{Authorization: request.Header.Get("Authorization"), Method: request.Method, Path: request.URL.EscapedPath(), Body: append(json.RawMessage(nil), body...)})
	number := len(p.requests)
	p.mu.Unlock()
	if request.Method != http.MethodPost || request.URL.EscapedPath() != "/v1/chat/completions" || request.URL.RawQuery != "" || number > 2 {
		http.Error(w, "unexpected fixture request", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if number == 1 {
		chunks := []any{
			mcpToolProbeChunk("chatcmpl-tool-1", 1, map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{
					"index": 0, "id": p.callID, "type": "function",
					"function": map[string]any{"name": "engorch_" + p.toolName, "arguments": p.arguments},
				}},
			}, nil),
			mcpToolProbeChunk("chatcmpl-tool-1", 1, map[string]any{}, "tool_calls"),
			map[string]any{"id": "chatcmpl-tool-1", "object": "chat.completion.chunk", "created": 1, "model": mcpToolProbeModel, "choices": []any{}, "usage": map[string]any{"prompt_tokens": 9, "completion_tokens": 4, "total_tokens": 13}},
		}
		p.writeSSE(w, chunks)
		return
	}
	chunks := []any{
		mcpToolProbeChunk("chatcmpl-tool-2", 2, map[string]any{"role": "assistant", "content": p.finalText}, nil),
		mcpToolProbeChunk("chatcmpl-tool-2", 2, map[string]any{}, "stop"),
		map[string]any{"id": "chatcmpl-tool-2", "object": "chat.completion.chunk", "created": 2, "model": mcpToolProbeModel, "choices": []any{}, "usage": map[string]any{"prompt_tokens": 18, "completion_tokens": 3, "total_tokens": 21}},
	}
	p.writeSSE(w, chunks)
}

func mcpToolProbeChunk(id string, created int, delta map[string]any, finish any) map[string]any {
	return map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": mcpToolProbeModel,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func (p *mcpToolProvider) writeSSE(w http.ResponseWriter, chunks []any) {
	var raw strings.Builder
	for _, chunk := range chunks {
		chunkRaw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(&raw, "data: %s\n\n", chunkRaw)
	}
	_, _ = fmt.Fprint(&raw, "data: [DONE]\n\n")
	response := []byte(raw.String())
	p.mu.Lock()
	p.responses = append(p.responses, append([]byte(nil), response...))
	p.mu.Unlock()
	_, _ = w.Write(response)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestPinnedOpenCodeMCPSourceReadToolLoopProbe(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_MCP_TOOL_PROBE") != "1" {
		t.Skip("explicit pinned OpenCode MCP tool-loop probe opt-in required")
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
	const committed = "committed-only-mcp-tool-probe"
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte(committed), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(ctx, sourceRoot, "opencode-mcp-tool-probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("dirty-checkout-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	prompt := "Use the permitted source_read tool once to read source.txt from offset 0 with limit 64, then answer with the fixture completion."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-tool-fixture", Model: mcpToolProbeModel, Effort: "low", Role: "explorer"}, prompt)
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
	bridge, err := contextmcp.NewOwned(broker, mcpToolProbeBearer, nil)
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

	provider := &mcpToolProvider{toolName: "source_read", arguments: `{"path":"source.txt","offset":0,"limit":64}`, callID: mcpToolCallID, finalText: mcpToolFinalText}
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
	configDirectory := filepath.Join(hostRoot, "config", "opencode")
	if err := os.Mkdir(configDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	providerConfig := map[string]any{"provider": map[string]any{"engorch-tool-fixture": map[string]any{
		"npm": "@ai-sdk/openai-compatible", "name": "EngOrch MCP tool fixture",
		"options": map[string]string{"baseURL": providerServer.URL + "/v1", "apiKey": "fixture-provider-key"},
		"models":  map[string]any{mcpToolProbeModel: map[string]any{"name": "Tool wire fixture", "limit": map[string]int64{"context": 8192, "output": 4096}}},
	}}}
	providerConfigRaw, err := json.Marshal(providerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "opencode.json"), providerConfigRaw, 0600); err != nil {
		t.Fatal(err)
	}

	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(repositoryRoot, ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	toolsSpec := ToolsConfigurationSpec{Endpoint: running.URL(), Bearer: mcpToolProbeBearer, ToolNames: []string{"source_read"}, TimeoutMillis: 5000}
	port := providerProbePort(t)
	output := &probeOutput{}
	process, toolsReceipt, attempts, err := StartReadyLimitedToolsProcess(ctx, binary, mcpToolProbeDigest, hostRoot, port, "fixture", "fixture-server-secret", output, 128, 2, 14*time.Second, toolsSpec)
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
	project, err := client.ReadCurrentProject(ctx, ProjectExpectation{Directory: hostRoot, Mode: ProjectModeGlobal})
	if err != nil || project.ID != "global" || project.Directory != hostRoot || project.Mode != ProjectModeGlobal || project.Worktree != "/" || project.VCS != "" || len(project.SHA256) != 64 || strings.Trim(project.SHA256, "0123456789abcdef") != "" {
		t.Fatalf("strict current project receipt mismatch: receipt=%+v err=%v diagnostic=%s", project, err, mcpToolProbeProjectShape(ctx, client))
	}
	sessionBinding := ToolSessionBinding{
		Session:   SessionBinding{IntentID: invocation.ID, ProjectID: project.ID, Directory: hostRoot, Agent: "build", Provider: "engorch-tool-fixture", Model: mcpToolProbeModel, Variant: invocation.Profile.Effort},
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
		t.Fatalf("synchronous tool-loop request failed: %v; provider requests: %d; logs: %s", err, len(provider.snapshot()), providerProbeLogs(hostRoot))
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
	t.Logf("MCP_PROVIDER_REQUEST_SHAPES first=%s second=%s", mcpToolProbeRequestShape(requests[0].Body), mcpToolProbeRequestShape(requests[1].Body))
	for _, request := range requests {
		if request.Method != http.MethodPost || request.Path != "/v1/chat/completions" || request.Authorization != "Bearer fixture-provider-key" {
			t.Fatal("provider wire endpoint or fixture credential changed")
		}
	}
	firstResponseHash, secondResponseHash := assertMCPToolProbeProviderSSE(t, provider.responseSnapshot(), "source_read", mcpToolCallID, `{"path":"source.txt","offset":0,"limit":64}`)
	assertMCPToolProbeProviderRequests(t, requests, invocation.ID, sourcetools.Catalog(), "source_read")
	assertMCPToolProbeFirstProviderRequest(t, requests[0], "source_read")
	assertMCPToolProbeResultRequest(t, requests[1], contextState.Responses[0], "source_read", mcpToolCallID, []byte(`"content_utf8":"committed-only-mcp-tool-probe"`))

	transcript, err := client.read(ctx, "/session/"+sessionID+"/message")
	if err != nil || len(transcript) == 0 || len(transcript) > 1<<20 {
		t.Fatal("bounded raw tool transcript unavailable", len(transcript), err)
	}
	transcriptHash := sha256.Sum256(transcript)
	if !bytesContainAll(transcript, []byte(mcpToolCallID), []byte("source_read"), []byte(mcpToolFinalText)) {
		diagnostic := transcript
		if len(diagnostic) > 4096 {
			diagnostic = diagnostic[:4096]
		}
		t.Fatalf("raw transcript lacks tool-loop markers: %s", diagnostic)
	}
	if observed.Text != mcpToolFinalText || len(observed.Calls) != 1 || observed.Calls[0].ProviderCallID != mcpToolCallID || observed.Calls[0].RequestID != durableRequest.RequestID {
		t.Fatal("decoded source tool turn mismatch", observed, err)
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
		ExecutableSHA256: mcpToolProbeDigest, HostRoot: hostRoot, MaxOutputTokens: 128, Diagnostic: false,
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
	t.Logf("MCP_TOOL_PROBE_PASS invocation_id=%s broker_binding_id=%s broker_catalog_id=%s commit=%s catalog_sha256=%s config_sha256=%s project_sha256=%s status_sha256=%s transcript_sha256=%s provider1_sha256=%s provider2_sha256=%s response1_sha256=%s response2_sha256=%s receipt_sha256=%s sync_journal_sha256=%s seal_journal_sha256=%s terminal_receipt_sha256=%s context_request_id=%s provider_requests=%d tool_calls=%d", invocation.ID, brokerBindingID, binding.CatalogID, source.Commit, running.CatalogHash(), toolsReceipt.SHA256, project.SHA256, statusAfter.SHA256, hex.EncodeToString(transcriptHash[:]), hex.EncodeToString(firstProviderHash[:]), hex.EncodeToString(secondProviderHash[:]), firstResponseHash, secondResponseHash, hex.EncodeToString(receiptHash[:]), hex.EncodeToString(syncJournalHash[:]), hex.EncodeToString(sealJournalHash[:]), hex.EncodeToString(terminalReceiptHash[:]), durableRequest.RequestID, len(requests), contextState.Calls)
}

func TestPinnedOpenCodeMCPCandidateReadToolLoopProbe(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_MCP_CANDIDATE_TOOL_PROBE") != "1" {
		t.Skip("explicit pinned OpenCode MCP candidate tool-loop probe opt-in required")
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
	const committed = "committed-base-candidate-tool-probe"
	const candidateBytes = "candidate-only-mcp-tool-probe"
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte(committed), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	source, err := repository.Discover(ctx, sourceRoot, "opencode-mcp-candidate-tool-probe")
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Review the admitted candidate by using the permitted candidate_read tool once to read source.txt from offset 0 with limit 64, then answer with the fixture completion."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-tool-fixture", Model: mcpToolProbeModel, Effort: "low", Role: "reviewer"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	worktreeRequest, err := worktree.Prepare(invocation.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(worktreeRequest)
	if err != nil {
		t.Fatal(err)
	}
	// This defer is registered before every runtime defer, so the lease is
	// released only after the client, process, bridge and broker are reaped.
	defer func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	}()
	workspace, err := worktree.Create(ctx, worktreeRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeRequest.Path, "source.txt"), []byte(candidateBytes), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := worktree.Fingerprint(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidateBinding := candidatetools.Binding{Workspace: workspace, Candidate: candidate}
	binding, err := contextbroker.NewBinding(invocation.ID, source, &candidateBinding, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: contextmcp.MaxContentBytes})
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
	bridge, err := contextmcp.NewOwned(broker, mcpToolProbeBearer, nil)
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

	provider := &mcpToolProvider{toolName: "candidate_read", arguments: `{"path":"source.txt","offset":0,"limit":64}`, callID: mcpCandidateCallID, finalText: mcpCandidateFinalText}
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
	configDirectory := filepath.Join(hostRoot, "config", "opencode")
	if err := os.Mkdir(configDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	providerConfig := map[string]any{"provider": map[string]any{"engorch-tool-fixture": map[string]any{
		"npm": "@ai-sdk/openai-compatible", "name": "EngOrch MCP tool fixture",
		"options": map[string]string{"baseURL": providerServer.URL + "/v1", "apiKey": "fixture-provider-key"},
		"models":  map[string]any{mcpToolProbeModel: map[string]any{"name": "Tool wire fixture", "limit": map[string]int64{"context": 8192, "output": 4096}}},
	}}}
	providerConfigRaw, err := json.Marshal(providerConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "opencode.json"), providerConfigRaw, 0600); err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(repositoryRoot, ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	toolsSpec := ToolsConfigurationSpec{Endpoint: running.URL(), Bearer: mcpToolProbeBearer, ToolNames: []string{"candidate_read"}, TimeoutMillis: 5000}
	port := providerProbePort(t)
	output := &probeOutput{}
	process, toolsReceipt, attempts, err := StartReadyLimitedToolsProcess(ctx, binary, mcpToolProbeDigest, hostRoot, port, "fixture", "fixture-server-secret", output, 128, 2, 14*time.Second, toolsSpec)
	if err != nil {
		t.Fatalf("pinned candidate tool host startup failed: %v; attempts: %+v; output: %s", err, attempts, output.String())
	}
	defer func() {
		_ = process.Close()
		select {
		case <-process.Done():
		default:
			t.Error("owned OpenCode candidate tool process was not reaped")
		}
	}()
	client, err := NewClient("http://127.0.0.1:"+strconv.Itoa(port), "fixture", "fixture-server-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := waitMCPToolProbeStatus(ctx, client, process); err != nil {
		t.Fatalf("candidate MCP server did not connect: %v; output: %s", err, output.String())
	}
	project, err := client.ReadCurrentProject(ctx, ProjectExpectation{Directory: hostRoot, Mode: ProjectModeGlobal})
	if err != nil || project.ID != "global" || project.Directory != hostRoot || project.Mode != ProjectModeGlobal || project.Worktree != "/" || project.VCS != "" || len(project.SHA256) != 64 || strings.Trim(project.SHA256, "0123456789abcdef") != "" {
		t.Fatalf("strict current project receipt mismatch: receipt=%+v err=%v diagnostic=%s", project, err, mcpToolProbeProjectShape(ctx, client))
	}
	sessionBinding := ToolSessionBinding{
		Session:   SessionBinding{IntentID: invocation.ID, ProjectID: project.ID, Directory: hostRoot, Agent: "build", Provider: "engorch-tool-fixture", Model: mcpToolProbeModel, Variant: invocation.Profile.Effort},
		ToolNames: []string{"candidate_read"}, CatalogSHA256: running.CatalogHash(),
	}
	sessionJournal := filepath.Join(t.TempDir(), "tool-session.jsonl")
	sessionID, err := client.CreateToolSession(ctx, sessionJournal, sessionBinding)
	if err != nil {
		t.Fatal("create exact candidate tool session:", err)
	}
	verifiedSessionID, err := client.ReconcileToolSession(ctx, sessionBinding)
	if err != nil || verifiedSessionID != sessionID {
		t.Fatal("live exact candidate tool session readback failed", verifiedSessionID, sessionID, err)
	}

	dispatch, err := DispatchForInvocation(invocation, sessionID, "msg_engorchmcpcandidate", sessionBinding.Session.Agent, hostRoot, "/")
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
		t.Fatalf("synchronous candidate tool-loop request failed: %v; provider requests: %d; logs: %s", err, len(provider.snapshot()), providerProbeLogs(hostRoot))
	}

	contextState, err := contextbroker.Inspect(contextJournal)
	if err != nil || contextState.Pending != nil || contextState.Calls != 1 || len(contextState.Responses) != 1 || !contextState.Responses[0].Success {
		t.Fatal("durable candidate context response mismatch", contextState, err)
	}
	var chunk worktree.SourceChunk
	if err := canonical.Decode(contextState.Responses[0].Content, &chunk); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if err != nil || string(content) != candidateBytes || string(content) == committed || chunk.CandidateID != candidateID {
		t.Fatal("candidate_read did not return exact admitted candidate bytes", string(content), chunk, err)
	}
	events, err := journal.Read(contextJournal)
	if err != nil || len(events) != 3 || events[1].Kind != "context.request" || events[2].Kind != "context.response" {
		t.Fatal("durable candidate request/response ordering mismatch", events, err)
	}
	var durableRequest contextbroker.Request
	if err := canonical.Decode(events[1].Payload, &durableRequest); err != nil || durableRequest.Tool != "candidate_read" || string(durableRequest.Arguments) != `{"limit":64,"offset":0,"path":"source.txt"}` {
		t.Fatal("durable candidate request mismatch", durableRequest, err)
	}
	var durableResponse contextbroker.Response
	if err := canonical.Decode(events[2].Payload, &durableResponse); err != nil || !reflect.DeepEqual(durableResponse, contextState.Responses[0]) {
		t.Fatal("durable candidate response bytes mismatch", durableResponse, contextState.Responses[0], err)
	}

	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("candidate provider received %d requests, want exactly two", len(requests))
	}
	for _, request := range requests {
		if request.Method != http.MethodPost || request.Path != "/v1/chat/completions" || request.Authorization != "Bearer fixture-provider-key" {
			t.Fatal("candidate provider wire endpoint or fixture credential changed")
		}
	}
	firstResponseHash, secondResponseHash := assertMCPToolProbeProviderSSE(t, provider.responseSnapshot(), "candidate_read", mcpCandidateCallID, `{"path":"source.txt","offset":0,"limit":64}`)
	assertMCPToolProbeProviderRequests(t, requests, invocation.ID, candidatetools.Catalog(), "candidate_read")
	assertMCPToolProbeFirstProviderRequest(t, requests[0], "candidate_read")
	assertMCPToolProbeResultRequest(t, requests[1], contextState.Responses[0], "candidate_read", mcpCandidateCallID, []byte(`"content_utf8":"candidate-only-mcp-tool-probe"`))

	transcript, err := client.read(ctx, "/session/"+sessionID+"/message")
	if err != nil || len(transcript) == 0 || len(transcript) > 1<<20 {
		t.Fatal("bounded raw candidate tool transcript unavailable", len(transcript), err)
	}
	transcriptHash := sha256.Sum256(transcript)
	if !bytesContainAll(transcript, []byte(mcpCandidateCallID), []byte("candidate_read"), []byte(mcpCandidateFinalText), []byte(candidateID)) {
		diagnostic := transcript
		if len(diagnostic) > 4096 {
			diagnostic = diagnostic[:4096]
		}
		t.Fatalf("raw candidate transcript lacks tool-loop markers: %s", diagnostic)
	}
	if observed.Text != mcpCandidateFinalText || len(observed.Calls) != 1 || observed.Calls[0].ProviderCallID != mcpCandidateCallID || observed.Calls[0].RequestID != durableRequest.RequestID {
		t.Fatal("decoded candidate tool turn mismatch", observed, err)
	}
	providerRequestsBeforeRecovery := len(provider.snapshot())
	var offline *Client
	recovered, err := offline.RecoverSynchronousToolTurn(context.Background(), syncJournal, contextJournal, syncIntent)
	if err != nil || !reflect.DeepEqual(recovered, observed) || len(provider.snapshot()) != providerRequestsBeforeRecovery {
		t.Fatal("offline candidate tool turn recovery mismatch", recovered, observed, err)
	}
	statusAfter, err := client.ReadMCPStatus(ctx)
	if err != nil {
		t.Fatal("post-turn candidate MCP status gate failed", err)
	}
	providerRequestsBeforeSeal := len(provider.snapshot())
	sealPath := filepath.Join(t.TempDir(), "tool-turn-seal.jsonl")
	sealExpected := SynchronousToolTurnSealExpected{
		Dispatch: syncIntent, Session: sessionBinding, Tools: toolsReceipt,
		ExecutableSHA256: mcpToolProbeDigest, HostRoot: hostRoot, MaxOutputTokens: 128, Diagnostic: false,
	}
	sealCtx, sealCancel := context.WithTimeout(ctx, 10*time.Second)
	terminalReceipt, err := SealSynchronousToolTurn(sealCtx, sealPath, syncJournal, contextJournal, sealExpected, toolsSpec, client, process, running, broker)
	sealCancel()
	if err != nil || !terminalReceipt.MCPHandlersStopped || !terminalReceipt.RootProcessReaped || !terminalReceipt.BrokerClosed {
		t.Fatal("candidate tool turn terminal seal failed", terminalReceipt, err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("sealed candidate OpenCode root was not reaped")
	}
	closedContextState, err := contextbroker.Inspect(contextJournal)
	if err != nil || !closedContextState.Closed || closedContextState.Pending != nil || closedContextState.Calls != contextState.Calls || len(provider.snapshot()) != providerRequestsBeforeSeal {
		t.Fatal("candidate terminal state changed admitted work", closedContextState, err)
	}
	recoveredSeal, err := RecoverSynchronousToolTurnSeal(sealPath, syncJournal, contextJournal, sealExpected)
	if err != nil || !reflect.DeepEqual(recoveredSeal, terminalReceipt) {
		t.Fatal("offline candidate terminal seal recovery mismatch", recoveredSeal, terminalReceipt, err)
	}
	sealJournalBytes, err := journal.ExportJSONL(sealPath)
	if err != nil {
		t.Fatal(err)
	}
	sealEvents, err := journal.Read(sealPath)
	if err != nil || len(sealEvents) != 2 || sealEvents[0].Kind != "opencode.tool-turn-seal-intent" || sealEvents[1].Kind != "opencode.tool-turn-sealed" {
		t.Fatal("durable candidate terminal seal journal mismatch", sealEvents, err)
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
		t.Fatal("durable candidate synchronous tool journal mismatch", syncEvents, err)
	}
	syncJournalHash := sha256.Sum256(syncJournalBytes)
	t.Logf("MCP_CANDIDATE_TOOL_PROBE_PASS invocation_id=%s broker_binding_id=%s broker_catalog_id=%s commit=%s candidate_id=%s catalog_sha256=%s config_sha256=%s project_sha256=%s status_sha256=%s transcript_sha256=%s provider1_sha256=%s provider2_sha256=%s response1_sha256=%s response2_sha256=%s receipt_sha256=%s sync_journal_sha256=%s seal_journal_sha256=%s terminal_receipt_sha256=%s context_request_id=%s provider_requests=%d tool_calls=%d", invocation.ID, brokerBindingID, binding.CatalogID, source.Commit, candidateID, running.CatalogHash(), toolsReceipt.SHA256, project.SHA256, statusAfter.SHA256, hex.EncodeToString(transcriptHash[:]), hex.EncodeToString(firstProviderHash[:]), hex.EncodeToString(secondProviderHash[:]), firstResponseHash, secondResponseHash, hex.EncodeToString(receiptHash[:]), hex.EncodeToString(syncJournalHash[:]), hex.EncodeToString(sealJournalHash[:]), hex.EncodeToString(terminalReceiptHash[:]), durableRequest.RequestID, len(requests), contextState.Calls)
}

func waitMCPToolProbeStatus(ctx context.Context, client *Client, process *Process) (MCPStatus, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := client.ReadMCPStatus(ctx)
		if err == nil {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return MCPStatus{}, ctx.Err()
		case <-process.Done():
			return MCPStatus{}, fmt.Errorf("OpenCode exited: %w", process.Wait())
		case <-ticker.C:
		}
	}
}

func assertMCPToolProbeFirstProviderRequest(t *testing.T, request mcpToolProviderRequest, toolName string) {
	t.Helper()
	var body struct {
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "engorch_"+toolName {
		t.Fatalf("provider tool catalog was not exact session allowlist: %+v", body.Tools)
	}
}

func assertMCPToolProbeResultRequest(t *testing.T, request mcpToolProviderRequest, expected contextbroker.Response, toolName, callID string, contentNeedle []byte) {
	t.Helper()
	var body struct {
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatal(err)
	}
	seenAssistant, seenResult := false, false
	var providerReceipt contextbroker.Response
	for _, message := range body.Messages {
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				seenAssistant = seenAssistant || call.ID == callID && call.Function.Name == "engorch_"+toolName
			}
		}
		if message.Role == "tool" && message.ToolCallID == callID {
			var content string
			if err := json.Unmarshal(message.Content, &content); err == nil {
				var receipt contextbroker.Response
				if err := canonical.Decode([]byte(content), &receipt); err == nil && reflect.DeepEqual(receipt, expected) {
					providerReceipt = receipt
					seenResult = true
				}
			}
		}
	}
	if !seenAssistant || !seenResult {
		t.Fatalf("second provider request lacks exact tool call/result: assistant=%v result=%v body=%s", seenAssistant, seenResult, request.Body)
	}
	if !providerReceipt.Success || !bytesContainAll(providerReceipt.Content, contentNeedle) {
		t.Fatalf("provider receipt does not retain exact durable tool content: %s", providerReceipt.Content)
	}
}

func assertMCPToolProbeProviderSSE(t *testing.T, responses [][]byte, toolName, callID, arguments string) (string, string) {
	t.Helper()
	if len(responses) != 2 {
		t.Fatalf("provider returned %d SSE responses, want exactly two", len(responses))
	}
	first, err := providergateway.ParseChatCompletionSSE(responses[0], len(responses[0]), 13)
	if err != nil {
		t.Fatal("production provider parser rejected tool-call SSE", err)
	}
	if first.Model != mcpToolProbeModel || first.FinishReason != "tool_calls" || first.Usage.InputTokens != 9 || first.Usage.OutputTokens != 4 || first.Usage.ReasoningTokens != nil || first.Usage.CacheReadTokens != nil || first.Usage.CacheWriteTokens != nil || len(first.ToolCalls) != 1 {
		t.Fatal("parsed tool-call SSE metadata mismatch", first)
	}
	call := first.ToolCalls[0]
	if call.Index != 0 || call.ID != callID || call.Name != "engorch_"+toolName || string(call.Arguments) != arguments {
		t.Fatal("parsed provider tool call mismatch", call)
	}
	second, err := providergateway.ParseChatCompletionSSE(responses[1], len(responses[1]), 21)
	if err != nil {
		t.Fatal("production provider parser rejected final SSE", err)
	}
	if second.Model != mcpToolProbeModel || second.FinishReason != "stop" || second.Usage.InputTokens != 18 || second.Usage.OutputTokens != 3 || second.Usage.ReasoningTokens != nil || second.Usage.CacheReadTokens != nil || second.Usage.CacheWriteTokens != nil || len(second.ToolCalls) != 0 {
		t.Fatal("parsed final SSE metadata mismatch", second)
	}
	firstHash := sha256.Sum256(responses[0])
	secondHash := sha256.Sum256(responses[1])
	if first.SHA256 != hex.EncodeToString(firstHash[:]) || second.SHA256 != hex.EncodeToString(secondHash[:]) {
		t.Fatal("production provider parser response hash mismatch")
	}
	return first.SHA256, second.SHA256
}

func assertMCPToolProbeProviderRequests(t *testing.T, requests []mcpToolProviderRequest, invocationID string, catalog []sourcetools.Definition, toolName string) {
	t.Helper()
	endpoint := providergateway.EndpointContract{Version: 1, Provider: "fixture", URL: "https://fixture.invalid/v1/chat/completions", Protocol: "openai-chat-completions-sse-v1"}
	model := providergateway.ModelContract{Version: 1, Provider: endpoint.Provider, Model: mcpToolProbeModel, Protocol: endpoint.Protocol, MaxCalls: 2, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{
		Version: 1, AccessPolicyID: strings.Repeat("c", 64), AccessInvocationID: invocationID,
		RouteID: strings.Repeat("d", 64), ReservedTokens: 8192,
		EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model,
	}
	if _, err := binding.ID(); err != nil {
		t.Fatal(err)
	}
	var selected *sourcetools.Definition
	for index := range catalog {
		if catalog[index].Name == toolName {
			selected = &catalog[index]
			break
		}
	}
	if selected == nil {
		t.Fatal("selected provider tool absent from broker catalog")
	}
	parameters, err := canonical.Bytes(selected.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []providergateway.RequestTool{{Name: "engorch_" + selected.Name, Description: selected.Description, Parameters: parameters}}
	for index, request := range requests {
		observation, err := providergateway.ValidateChatCompletionRequest(request.Body, int64(len(request.Body)), binding, 128, allowed)
		if err != nil {
			t.Fatalf("production provider request validator rejected pinned request %d: %v; shape=%s", index+1, err, mcpToolProbeRequestShape(request.Body))
		}
		hash := sha256.Sum256(request.Body)
		wantMessages := 2
		if index == 1 {
			wantMessages = 4
		}
		if observation.SHA256 != hex.EncodeToString(hash[:]) || observation.SizeBytes != int64(len(request.Body)) || observation.Model != mcpToolProbeModel || observation.MaxOutputTokens != 128 || observation.MessageCount != wantMessages || observation.ToolCount != 1 {
			t.Fatal("provider request observation mismatch", index+1, observation)
		}
	}
}

func mcpToolProbeRequestShape(raw json.RawMessage) string {
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
	var stream bool
	if json.Unmarshal(body["model"], &model) == nil && json.Unmarshal(body["max_tokens"], &maxTokens) == nil && json.Unmarshal(body["stream"], &stream) == nil {
		parts = append(parts, fmt.Sprintf("model=%s;max_tokens=%d;stream=%t", model, maxTokens, stream))
	}
	var messages []map[string]json.RawMessage
	if json.Unmarshal(body["messages"], &messages) == nil {
		for index, message := range messages {
			messageKeys := make([]string, 0, len(message))
			for key := range message {
				messageKeys = append(messageKeys, key)
			}
			sort.Strings(messageKeys)
			var role string
			_ = json.Unmarshal(message["role"], &role)
			parts = append(parts, fmt.Sprintf("message%d[%s]=%s", index, role, strings.Join(messageKeys, ",")))
		}
	}
	for _, fieldName := range []string{"stream_options"} {
		var object map[string]json.RawMessage
		if json.Unmarshal(body[fieldName], &object) == nil {
			objectKeys := make([]string, 0, len(object))
			for key := range object {
				objectKeys = append(objectKeys, key)
			}
			sort.Strings(objectKeys)
			parts = append(parts, fieldName+"="+strings.Join(objectKeys, ","))
		}
	}
	return strings.Join(parts, ";")
}

func mcpToolProbeProjectShape(ctx context.Context, client *Client) string {
	raw, err := client.read(ctx, "/project/current")
	if err != nil {
		return "read-error"
	}
	var project map[string]json.RawMessage
	if json.Unmarshal(raw, &project) != nil {
		return "invalid-json"
	}
	keys := make([]string, 0, len(project))
	for key := range project {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var id, worktree string
	var sandboxes []string
	_ = json.Unmarshal(project["id"], &id)
	_ = json.Unmarshal(project["worktree"], &worktree)
	_ = json.Unmarshal(project["sandboxes"], &sandboxes)
	return fmt.Sprintf("keys=%s id=%q worktree=%q sandboxes=%q time=%s", strings.Join(keys, ","), id, worktree, sandboxes, project["time"])
}

func bytesContainAll(value []byte, needles ...[]byte) bool {
	for _, needle := range needles {
		if !strings.Contains(string(value), string(needle)) {
			return false
		}
	}
	return true
}
