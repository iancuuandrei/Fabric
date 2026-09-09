package control

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolreceipts"
	"harness.local/engorch/internal/worktree"
)

const (
	scheduledOpenCodeLiveDigest = "88d2fa691b2d9e32fde6d1039382a850ddf96fe49cd41683c6375fe1dc8ec2a5"
	scheduledOpenCodeLiveModel  = "wire-responses-model"
	scheduledOpenCodeLiveSecret = "scheduled-opencode-provider-secret"
	scheduledCompositeListCall  = "call_scheduled_composite_list"
	scheduledCompositeListArgs  = `{"limit":8}`
)

type scheduledOpenCodeLiveProvider struct {
	mu        sync.Mutex
	requests  []json.RawMessage
	finalText [2]string
}

type scheduledOpenCodeBlockingProvider struct {
	once       sync.Once
	cancelOnce sync.Once
	admitted   chan struct{}
	canceled   chan struct{}
	mu         sync.Mutex
	requests   int
}

type scheduledOpenCodeCompositeProvider struct {
	mu               sync.Mutex
	requests         []json.RawMessage
	finalText        string
	parallel         bool
	catalogValidated bool
	listOutputClass  string
	listOutputBytes  int
	listOutputDigest string
}

func (p *scheduledOpenCodeCompositeProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
		http.Error(writer, "invalid fixture composite provider request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	sequence, finalText := len(p.requests), p.finalText
	p.mu.Unlock()
	if sequence == 1 {
		if err := scheduledValidateCompositeProviderCatalog(body); err != nil {
			http.Error(writer, "fixture composite catalog mismatch", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.catalogValidated = true
		p.mu.Unlock()
	}
	listResultSequence := 3
	if p.parallel {
		listResultSequence = 2
	}
	if sequence == listResultSequence {
		class, size, digest := scheduledCompositeListOutputFacts(body)
		p.mu.Lock()
		p.listOutputClass, p.listOutputBytes, p.listOutputDigest = class, size, digest
		p.mu.Unlock()
	}
	var response []byte
	switch {
	case sequence == 1 && p.parallel:
		response = scheduledOpenCodeParallelCompositeToolSSE()
	case sequence == 1:
		response = scheduledOpenCodeToolSSE(8)
	case sequence == 2 && !p.parallel:
		response = scheduledOpenCodeListToolSSE()
	case sequence == listResultSequence:
		if finalText == "" {
			http.Error(writer, "fixture composite result unavailable", http.StatusConflict)
			return
		}
		response = scheduledOpenCodeTextSSE(9, finalText)
	default:
		http.Error(writer, "unexpected extra composite provider request", http.StatusConflict)
		return
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(response)
}

func (p *scheduledOpenCodeCompositeProvider) snapshot() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		result[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	return result
}

func (p *scheduledOpenCodeCompositeProvider) facts() (bool, string, int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.catalogValidated, p.listOutputClass, p.listOutputBytes, p.listOutputDigest
}

func scheduledValidateCompositeProviderCatalog(raw json.RawMessage) error {
	var request struct {
		Tools []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return err
	}
	catalog, err := agentToolCatalog()
	if err != nil {
		return err
	}
	projected, err := opencoderuntime.ProviderRequestToolsForCatalog(catalog, []string{"list_agents"})
	if err != nil || len(projected) != 1 {
		return errors.Join(errors.New("fixture list_agents provider projection unavailable"), err)
	}
	for _, tool := range request.Tools {
		if tool.Name != projected[0].Name {
			continue
		}
		actual, normalizeErr := canonical.Normalize(tool.Parameters)
		if normalizeErr != nil || !bytes.Equal(actual, projected[0].Parameters) {
			return errors.New("fixture list_agents provider schema differs")
		}
		var arguments listAgentsArgs
		if canonical.Decode([]byte(scheduledCompositeListArgs), &arguments) != nil || arguments.Limit != 8 || arguments.After != "" {
			return errors.New("fixture list_agents arguments differ")
		}
		return nil
	}
	return errors.New("fixture list_agents tool unavailable")
}

func scheduledCompositeListOutputFacts(raw json.RawMessage) (string, int, string) {
	var request struct {
		Input []struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
			Output string `json:"output"`
		} `json:"input"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return "request-decode", 0, ""
	}
	for _, item := range request.Input {
		if item.Type != "function_call_output" || item.CallID != scheduledCompositeListCall {
			continue
		}
		digest, _ := canonical.Hash("harness.control.composite-list-output.v1", item.Output)
		lower := strings.ToLower(item.Output)
		switch {
		case strings.Contains(lower, "invalid list_agents arguments"):
			return "invalid-arguments", len(item.Output), digest
		case strings.Contains(lower, "caller") && strings.Contains(lower, "authority"):
			return "caller-authority", len(item.Output), digest
		case strings.Contains(lower, "scheduled claim"):
			return "scheduled-claim", len(item.Output), digest
		case strings.Contains(lower, "agent tool") && strings.Contains(lower, "unavailable"):
			return "agent-tool-unavailable", len(item.Output), digest
		case strings.Contains(lower, "error"):
			return "other-error", len(item.Output), digest
		case json.Valid([]byte(item.Output)):
			return "json-result", len(item.Output), digest
		default:
			return "other-output", len(item.Output), digest
		}
	}
	return "missing", 0, ""
}

func TestScheduledOpenCodeCompositeFixturePreflight(t *testing.T) {
	testScheduledOpenCodeCompositeFixturePreflight(t, false)
}

func TestScheduledOpenCodeParallelCompositeFixturePreflight(t *testing.T) {
	testScheduledOpenCodeCompositeFixturePreflight(t, true)
}

func testScheduledOpenCodeCompositeFixturePreflight(t *testing.T, parallel bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	schedulerPath := filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	providerCalls := 3
	fixtureName := "scheduled-opencode-composite-preflight"
	agentName := "composite-explorer"
	turnNonce := "composite-turn-nonce"
	if parallel {
		providerCalls = 2
		fixtureName = "scheduled-opencode-parallel-composite-preflight"
		agentName = "parallel-composite-explorer"
		turnNonce = "parallel-composite-turn-nonce"
	}
	creation := scheduledOpenCodeCompositeCreationForCalls(t, scheduledOpenCodeCreation(t, "https://127.0.0.1:1/v1/responses", executable, filepath.Join(root, "opencode-state")), providerCalls)
	prepared := scheduledOpenCodePrepareExplorerTurn(t, ctx, controllerPath, schedulerPath, creation, ScheduledDispatchAdapter{JournalPath: schedulerPath}, fixtureName, agentName, "Read file.txt and list your durable agent subtree.", turnNonce)
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	state := scheduled.Tasks[prepared.turn.Task.ID]
	tree, treeErr := agenttree.Inspect(controllerPath + ".agent-tree")
	if state.Status != taskscheduler.StatusReady || state.Claim != nil || treeErr != nil || tree.TreeID != prepared.snapshot.RunID || prepared.turn.AgentID == "" || prepared.turn.ParentAgentID != prepared.rootAgent.AgentID {
		t.Fatal("composite fixture did not stop immediately before dynamic dispatch", state, treeErr, tree)
	}
	if matches, globErr := filepath.Glob(controllerPath + ".explorer.turn-*.opencode-runtime.jsonl"); globErr != nil || len(matches) != 0 {
		t.Fatal("composite preflight launched OpenCode", matches, globErr)
	}
}

func TestPinnedOpenCodeScheduledExplorerCompositeTools(t *testing.T) {
	testPinnedOpenCodeScheduledExplorerCompositeTools(t, false)
}

func TestPinnedOpenCodeScheduledExplorerParallelCompositeTools(t *testing.T) {
	testPinnedOpenCodeScheduledExplorerCompositeTools(t, true)
}

func testPinnedOpenCodeScheduledExplorerCompositeTools(t *testing.T, parallel bool) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provider := &scheduledOpenCodeCompositeProvider{parallel: parallel}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()
	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	schedulerPath := filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	providerRequests := 3
	fixtureName := "scheduled-opencode-composite"
	agentName := "composite-explorer"
	turnNonce := "composite-turn-nonce"
	if parallel {
		providerRequests = 2
		fixtureName = "scheduled-opencode-parallel-composite"
		agentName = "parallel-composite-explorer"
		turnNonce = "parallel-composite-turn-nonce"
	}
	creation := scheduledOpenCodeCompositeCreationForCalls(t, scheduledOpenCodeCreation(t, upstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state")), providerRequests)
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	dispatchCtx := withProviderTransportClient(ctx, transport)
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, dispatchCtx, controllerPath, schedulerPath, creation, adapter, fixtureName, agentName, "Read file.txt with source_read and list your durable agent subtree with list_agents.", turnNonce)
	candidateID, err := prepared.snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	finalOutput, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: "Composite scripted turn observed the committed source and its durable agent subtree.", Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.finalText = string(finalOutput)
	provider.mu.Unlock()
	decision, err := taskscheduler.Tick(dispatchCtx, schedulerPath, adapter)
	if err != nil || decision.TaskID != prepared.turn.Task.ID || decision.Status != taskscheduler.StatusSucceeded {
		stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: prepared.turn.ParentAgentID, AgentID: prepared.turn.AgentID, TurnID: prepared.turn.TurnID, TurnSequence: prepared.turn.TurnSequence})
		runtimePath := controllerPath + "." + stem + ".opencode-runtime.jsonl"
		receipts, receiptErr := toolreceipts.Inspect(runtimePath + ".tool-receipts")
		type receiptDiagnostic struct {
			Tool      string
			Completed bool
			Owner     toolreceipts.Owner
		}
		diagnostics := make([]receiptDiagnostic, len(receipts.Calls))
		for index, call := range receipts.Calls {
			diagnostics[index].Tool = call.Intent.Tool
			if call.Receipt != nil {
				diagnostics[index].Completed = true
				diagnostics[index].Owner = call.Receipt.Owner
			}
		}
		broker, brokerErr := contextbroker.Inspect(runtimePath + ".broker")
		catalogOK, listClass, listBytes, listDigest := provider.facts()
		t.Fatal("composite scheduled dispatch failed", decision, err, len(provider.snapshot()), "catalog", catalogOK, "list_output", listClass, listBytes, listDigest, "receipts", receiptErr, diagnostics, "broker", brokerErr, broker.Calls, len(broker.Responses), broker.Closed)
	}
	requests := provider.snapshot()
	catalogOK, listClass, _, _ := provider.facts()
	if len(requests) != providerRequests || !catalogOK || listClass != "json-result" || !bytes.Contains(requests[0], []byte(`"name":"engorch_source_read"`)) || !bytes.Contains(requests[0], []byte(`"name":"engorch_list_agents"`)) {
		t.Fatal("provider did not receive exact composite catalog once", len(requests), string(requests[0]))
	}
	controller, err := Inspect(controllerPath)
	if err != nil || len(controller.Explorations) != 1 || controller.Explorations[0].Result.Output != string(finalOutput) {
		t.Fatal("composite controller result unavailable", err, controller.Explorations)
	}
	stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: prepared.turn.ParentAgentID, AgentID: prepared.turn.AgentID, TurnID: prepared.turn.TurnID, TurnSequence: prepared.turn.TurnSequence})
	runtimePath := controllerPath + "." + stem + ".opencode-runtime.jsonl"
	controllerHead, err := controllerJournalHead(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	agentTurn := &taskscheduler.AgentTurnBinding{ParentAgentID: prepared.turn.ParentAgentID, AgentID: prepared.turn.AgentID, TurnID: prepared.turn.TurnID, TurnSequence: prepared.turn.TurnSequence}
	acceptedReference, found, err := acceptedExplorerResultReference(controllerPath, controllerHead, controller, controller.Explorations[0].Invocation, agentTurn, &controller.Explorations[0])
	if err != nil || !found {
		t.Fatal("actual composite accepted result reference unavailable", found, err)
	}
	acceptedID, err := acceptedReference.ID()
	acceptedState, acceptedErr := agentcontrol.Inspect(controllerPath + ".agent-control")
	acceptedResults, acceptedAudiences := 0, map[string]int{}
	for _, result := range acceptedState.Results {
		if result.ResultID == acceptedID && result.Reference == acceptedReference {
			acceptedResults++
		}
	}
	for _, activity := range acceptedState.Activities {
		if activity.Kind == "result" && activity.ResultID == acceptedID && activity.TurnID == prepared.turn.TurnID && activity.ResultSHA256 == acceptedReference.ResultSHA256 {
			acceptedAudiences[activity.AgentID]++
		}
	}
	if err != nil || acceptedErr != nil || acceptedResults != 1 || acceptedAudiences[prepared.turn.ParentAgentID] != 1 || acceptedAudiences[prepared.turn.AgentID] != 1 || len(acceptedAudiences) != 2 {
		t.Fatal("actual composite accepted result index differs", err, acceptedErr, acceptedResults, acceptedAudiences)
	}
	acceptedEventsBeforeRecovery, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(acceptedEventsBeforeRecovery) == 0 {
		t.Fatal("actual composite accepted result journal unavailable", err)
	}
	acceptedBytes, err := os.ReadFile(controllerPath + ".agent-control")
	if err != nil || bytes.Contains(acceptedBytes, []byte("Composite scripted turn observed the committed source")) {
		t.Fatal("actual composite accepted result journal retained result body", err)
	}
	acceptedHeadBeforeRecovery := acceptedEventsBeforeRecovery[len(acceptedEventsBeforeRecovery)-1].Hash
	expectedIntent := scheduledOpenCodeRuntimeIntent(t, controller, prepared.turn.Task.InvocationID, runtimePath)
	if parallel && (expectedIntent.ToolReceipts.QueuePolicy != toolreceipts.QueuePolicySerialFIFO || expectedIntent.ToolReceipts.MaxQueuedCalls != 32) {
		t.Fatal("parallel composite admission queue identity changed", expectedIntent.ToolReceipts)
	}
	verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, runtimePath, controller, prepared.turn)
	runtimeState, err := opencoderuntime.InspectComposite(runtimePath, expectedIntent, verify)
	if err != nil || runtimeState.Bound == nil || runtimeState.Bound.Version != 2 || runtimeState.Bound.Composite == nil || runtimeState.Result == nil || runtimeState.Result.Version != 2 || runtimeState.Result.CompositeSealReceipt == nil || runtimeState.Result.SealReceipt.Version != 0 {
		t.Fatal("composite runtime result unavailable", err, runtimeState)
	}
	receipt := runtimeState.Result.CompositeSealReceipt
	if receipt.InvocationID != expectedIntent.Invocation.ID || receipt.ReceiptBindingID != expectedIntent.ToolReceipts.BindingID || receipt.ReceiptJournalHead != runtimeState.Result.ToolReceiptsHead || receipt.ReceiptStateSHA256 != runtimeState.Result.ToolReceiptsStateID {
		t.Fatal("composite terminal receipt identity changed", receipt, runtimeState.Result)
	}
	if runtimeState.Result.GatewayUsage == nil || runtimeState.Result.GatewayUsage.InputTokens != 35 || runtimeState.Result.GatewayUsage.OutputTokens != 10 || runtimeState.Result.Result.Usage.InputTokens == nil || *runtimeState.Result.Result.Usage.InputTokens != 35 || runtimeState.Result.Result.Usage.OutputTokens == nil || *runtimeState.Result.Result.Usage.OutputTokens != 10 {
		t.Fatal("composite provider usage identity changed", runtimeState.Result.GatewayUsage, runtimeState.Result.Result.Usage)
	}
	broker, brokerErr := contextbroker.Inspect(runtimePath + ".broker")
	receipts, receiptsErr := toolreceipts.Inspect(runtimePath + ".tool-receipts")
	owners := map[toolreceipts.Owner]int{}
	tools := map[string]int{}
	for _, call := range receipts.Calls {
		if call.Receipt != nil {
			owners[call.Receipt.Owner]++
			tools[call.Receipt.Tool]++
		}
	}
	if brokerErr != nil || receiptsErr != nil || broker.Binding == nil || broker.Calls != 1 || len(broker.Responses) != 1 || !broker.Closed || len(receipts.Calls) != 2 || owners[toolreceipts.OwnerContext] != 1 || owners[toolreceipts.OwnerAgent] != 1 || tools["source_read"] != 1 || tools["list_agents"] != 1 {
		t.Fatal("composite backend journals differ", brokerErr, receiptsErr, broker, receipts, owners, tools)
	}
	var chunk repository.SourceChunk
	if err := canonical.Decode(broker.Responses[0].Content, &chunk); err != nil {
		t.Fatal(err)
	}
	content, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if decodeErr != nil || string(content) != "base\n" || chunk.Commit != prepared.snapshot.Creation.Repository.Commit {
		t.Fatal("composite source backend differs", string(content), chunk, decodeErr)
	}
	receiptBytes, err := os.ReadFile(runtimePath + ".tool-receipts")
	if err != nil || bytes.Contains(receiptBytes, []byte("file.txt")) || bytes.Contains(receiptBytes, []byte(`{"limit":8}`)) {
		t.Fatal("composite receipt journal retained raw tool arguments", err, string(receiptBytes))
	}
	before := len(provider.snapshot())
	recovered, recoverErr := opencoderuntime.Execute(context.Background(), opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, Intent: expectedIntent, VerifyComposite: verify})
	if recoverErr != nil || !reflect.DeepEqual(recovered, *runtimeState.Result) || len(provider.snapshot()) != before {
		t.Fatal("offline composite runtime recovery resent or changed result", recoverErr, recovered, len(provider.snapshot()))
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	evidence, reconcileErr := adapter.Reconcile(context.Background(), *scheduled.Tasks[prepared.turn.Task.ID].Claim)
	if reconcileErr != nil || evidence.Status != taskscheduler.StatusSucceeded || evidence.InvocationID != prepared.turn.Task.InvocationID || len(provider.snapshot()) != before {
		t.Fatal("offline composite scheduler recovery resent or changed evidence", reconcileErr, evidence, len(provider.snapshot()))
	}
	acceptedEventsAfterRecovery, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(acceptedEventsAfterRecovery) != len(acceptedEventsBeforeRecovery) || acceptedEventsAfterRecovery[len(acceptedEventsAfterRecovery)-1].Hash != acceptedHeadBeforeRecovery {
		t.Fatal("offline composite recovery duplicated accepted result notifications", err)
	}
	mode := "sequential"
	if parallel {
		mode = "parallel-generation"
	}
	t.Logf("OPENCODE_SCHEDULED_COMPOSITE_PASS mode=%s executable_sha256=%s turn=%s provider_requests=%d receipt_head=%s", mode, scheduledOpenCodeLiveDigest, prepared.turn.TurnID, len(requests), runtimeState.Result.ToolReceiptsHead)
}

func (p *scheduledOpenCodeBlockingProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(request.Body, (1<<20)+1))
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	p.once.Do(func() { close(p.admitted) })
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	<-request.Context().Done()
	p.cancelOnce.Do(func() { close(p.canceled) })
}

func (p *scheduledOpenCodeBlockingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func TestPinnedOpenCodeScheduledExplorerInterruptStopsOwnedRuntime(t *testing.T) {
	testPinnedOpenCodeScheduledExplorerInterruptStopsOwnedRuntime(t, false)
}

func TestPinnedOpenCodeScheduledCompositeExplorerInterruptStopsOwnedRuntime(t *testing.T) {
	testPinnedOpenCodeScheduledExplorerInterruptStopsOwnedRuntime(t, true)
}

func testPinnedOpenCodeScheduledExplorerInterruptStopsOwnedRuntime(t *testing.T, composite bool) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	blocking := &scheduledOpenCodeBlockingProvider{admitted: make(chan struct{}), canceled: make(chan struct{})}
	upstream := httptest.NewUnstartedServer(blocking)
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer func() {
		cancel()
		upstream.Close()
	}()
	var plannerMu sync.Mutex
	plannerCalls := 0
	plannerUpstream := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(request.Body, (1<<20)+1))
		plannerMu.Lock()
		plannerCalls++
		plannerMu.Unlock()
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
			http.Error(writer, "invalid fixture planner request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write(scheduledOpenCodeTextSSE(0, `{"plan":"fixture"}`))
	}))
	plannerUpstream.EnableHTTP2 = true
	plannerUpstream.StartTLS()
	defer func() {
		cancel()
		plannerUpstream.Close()
	}()
	root := t.TempDir()
	controllerPath, schedulerPath := filepath.Join(root, "controller.jsonl"), filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	creation := scheduledOpenCodeCreation(t, upstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state"))
	creation = scheduledOpenCodeInterruptCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	tickCtx := withProviderTransportClient(ctx, transport)
	adapter := ScheduledDispatchAdapter{}
	fixtureName := "scheduled-opencode-interrupt"
	agentName := "interrupt-explorer"
	turnNonce := "interrupt-turn-nonce"
	if composite {
		adapter.JournalPath = schedulerPath
		fixtureName = "scheduled-opencode-composite-interrupt"
		agentName = "composite-interrupt-explorer"
		turnNonce = "composite-interrupt-turn-nonce"
	}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, tickCtx, controllerPath, schedulerPath, creation, adapter, fixtureName, agentName, "Read file.txt with source_read, then wait.", turnNonce)
	plannerMu.Lock()
	completedPlannerCalls := plannerCalls
	plannerMu.Unlock()
	if completedPlannerCalls != 1 {
		t.Fatal("fixture planner provider request count changed", completedPlannerCalls)
	}
	snapshot, rootAgent, turn := prepared.snapshot, prepared.rootAgent, prepared.turn
	type tickResult struct {
		decision taskscheduler.Decision
		err      error
	}
	tickDone := make(chan tickResult, 1)
	go func() {
		decision, tickErr := taskscheduler.Tick(tickCtx, schedulerPath, adapter)
		tickDone <- tickResult{decision, tickErr}
	}()
	select {
	case <-blocking.admitted:
	case <-time.After(45 * time.Second):
		t.Fatal("provider request was not admitted")
	}
	stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence})
	runtimePath := controllerPath + "." + stem + ".opencode-runtime.jsonl"
	if composite {
		controller, inspectErr := Inspect(controllerPath)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		intent := scheduledOpenCodeRuntimeIntent(t, controller, turn.Task.InvocationID, runtimePath)
		verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, runtimePath, controller, turn)
		runtimeState, runtimeErr := opencoderuntime.InspectComposite(runtimePath, intent, verify)
		receipts, receiptsErr := toolreceipts.Inspect(runtimePath + ".tool-receipts")
		if runtimeErr != nil || runtimeState.Bound == nil || runtimeState.Bound.Version != 2 || runtimeState.Bound.Composite == nil || intent.ToolReceipts.QueuePolicy != toolreceipts.QueuePolicySerialFIFO || intent.ToolReceipts.MaxQueuedCalls != 32 || receiptsErr != nil || receipts.Binding == nil || receipts.Binding.QueuePolicy != toolreceipts.QueuePolicySerialFIFO || receipts.Binding.MaxQueuedCalls != 32 || len(receipts.Calls) != 0 {
			t.Fatal("composite interrupt did not bind the unused serial FIFO recorder before provider dispatch", runtimeErr, runtimeState.Bound, receiptsErr, receipts)
		}
	}
	requested, err := RequestAgentInterrupt(context.Background(), controllerPath, schedulerPath, turn.TurnID, rootAgent.AgentID, "interrupt-request-nonce")
	if err != nil {
		t.Fatal(err)
	}
	var tick tickResult
	select {
	case tick = <-tickDone:
	case <-time.After(30 * time.Second):
		t.Fatal("interrupted scheduled dispatch did not return")
	}
	if tick.err == nil {
		t.Fatal("interrupted scheduled dispatch reported success", tick.decision)
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	state := scheduled.Tasks[turn.Task.ID]
	if state.Status != taskscheduler.StatusUnknown || state.Claim == nil {
		t.Fatal("interrupted task did not retain UNKNOWN claim", state)
	}
	controlState, err := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	var observed *agentcontrol.InterruptState
	for _, interrupt := range controlState.Interrupts {
		if interrupt.RequestID == requested.RequestID {
			copy := interrupt
			observed = &copy
			break
		}
	}
	if observed == nil || observed.Observation == nil || observed.Observation.Outcome != agentcontrol.InterruptConfirmedLocalStop {
		t.Fatal("owned interrupt shutdown was not confirmed", observed)
	}
	shutdown, err := opencoderuntime.InspectInterruptShutdownRequest(runtimePath, requested.RequestID, requested.Request)
	if err != nil || !shutdown.MCPHandlersStopped || !shutdown.ProviderProxyStopped || !shutdown.ProviderHandlersSettled || !shutdown.RootProcessReaped || !shutdown.BrokerClosed || shutdown.BrokerSettledHead == shutdown.BrokerClosedHead {
		t.Fatal("owned shutdown receipt unavailable", err, shutdown)
	}
	brokerState, err := contextbroker.Inspect(runtimePath + ".broker")
	if err != nil || !brokerState.Closed {
		t.Fatal("interrupt broker did not close", err, brokerState)
	}
	if !composite {
		select {
		case <-blocking.canceled:
		default:
			t.Fatal("provider handler did not observe cancellation")
		}
	} else {
		receipts, receiptsErr := toolreceipts.Inspect(runtimePath + ".tool-receipts")
		if receiptsErr != nil || receipts.Binding == nil || receipts.Binding.QueuePolicy != toolreceipts.QueuePolicySerialFIFO || receipts.Binding.MaxQueuedCalls != 32 || len(receipts.Calls) != 0 {
			t.Fatal("composite interrupt changed its unused durable recorder", receiptsErr, receipts)
		}
	}
	if blocking.count() != 1 {
		t.Fatal("interrupt changed provider send count", blocking.count())
	}
	_, _, invocation, err := scheduledInvocation(turn.Task, state.Claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ResolveProviderRouting(snapshot.Creation.Config, snapshot.RunID, "explorer", inputHash, 1)
	if err != nil || access.RequireActive(controllerPath+".model-access.jsonl", selected.Policy, selected.Intent) != nil {
		t.Fatal("interrupt released provider access authority", err)
	}
	poolRequest, enabled, err := taskPoolRequest(snapshot.Creation.Config, snapshot.RunID, selected.Intent)
	poolState, poolErr := taskpool.Inspect(snapshot.Creation.Config.TaskPool.Path)
	if err != nil || !enabled || poolErr != nil || poolState.Active[poolRequest.ID] != poolRequest {
		t.Fatal("interrupt released task-pool authority", err, poolErr)
	}
	gatewayState, err := providergateway.Inspect(controllerPath + "." + stem + ".provider-gateway.jsonl")
	if err != nil || gatewayState.Finished || len(gatewayState.Calls) != 1 {
		t.Fatal("interrupted gateway was incorrectly completed", err, gatewayState)
	}
	before := blocking.count()
	claimID, err := state.Claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	evidence, recoverErr := taskscheduler.RecoverClaim(context.Background(), schedulerPath, claimID, adapter)
	if recoverErr == nil || evidence.Status != taskscheduler.StatusUnknown || blocking.count() != before {
		t.Fatal("offline interrupt recovery resent or resolved UNKNOWN authority", recoverErr, evidence, blocking.count())
	}
	mode := "legacy"
	if composite {
		mode = "composite"
	}
	t.Logf("OPENCODE_SCHEDULED_INTERRUPT_PASS mode=%s turn=%s request=%s provider_requests=%d", mode, turn.TurnID, requested.RequestID, blocking.count())
}

func scheduledOpenCodeInterruptCreation(t *testing.T, creation Creation, plannerURL, taskPoolPath string) Creation {
	t.Helper()
	plannerProfile := runtime.Profile{Runtime: "provider-api", Provider: "openai", Model: scheduledOpenCodeLiveModel, Effort: "high", Role: "planner"}
	creation.Config.Planner = plannerProfile
	creation.Config.Provider.Endpoints = append(creation.Config.Provider.Endpoints, config.ProviderEndpoint{Name: "planner-responses", Version: 2, Provider: plannerProfile.Provider, URL: plannerURL, AdapterID: providergateway.OpenAIResponsesAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "control-live-key"}})
	plannerModel := creation.Config.Provider.Models[0]
	plannerModel.Name = "planner-model"
	plannerModel.Provider = plannerProfile.Provider
	creation.Config.Provider.Models = append(creation.Config.Provider.Models, plannerModel)
	explorerRole := creation.Config.Provider.Roles["explorer"]
	plannerRole := explorerRole
	plannerRole.Endpoint = "planner-responses"
	plannerRole.Model = plannerModel.Name
	plannerRole.RequiredCapabilities = &config.ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}
	var plannerControls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal([]byte(plannerRole.AdapterControlsJSON), &plannerControls); err != nil {
		t.Fatal(err)
	}
	plannerControls.ToolChoice = ""
	encodedPlannerControls, err := json.Marshal(plannerControls)
	if err != nil {
		t.Fatal(err)
	}
	plannerRole.AdapterControlsJSON = string(encodedPlannerControls)
	creation.Config.Provider.Roles["planner"] = plannerRole
	for index := range creation.Config.Access.Profiles {
		if creation.Config.Access.Profiles[index].Name == "fixture-planner" {
			creation.Config.Access.Profiles[index] = access.Profile{Version: 1, Name: "fixture-planner", Kind: "subscription", Runtime: "provider-api", Provider: plannerProfile.Provider, CredentialRef: "control-live-key", RepositoryClasses: []access.Class{access.Public}}
		}
	}
	plannerReservation, _, err := creation.Config.Provider.RoleReservation("planner")
	if err != nil {
		t.Fatal(err)
	}
	explorerReservation := creation.Config.Access.Invocations["explorer"].Tokens
	creation.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: plannerReservation}
	creation.Config.Access.Limits.Tokens = plannerReservation + 2*explorerReservation
	creation.Config.TaskPool = &config.TaskPool{Version: 1, Path: taskPoolPath, Limits: taskpool.Limits{Total: 1}}
	return creation
}

func scheduledOpenCodeCompositeCreation(t *testing.T, creation Creation) Creation {
	return scheduledOpenCodeCompositeCreationForCalls(t, creation, 3)
}

func scheduledOpenCodeCompositeCreationForCalls(t *testing.T, creation Creation, calls int) Creation {
	t.Helper()
	for index := range creation.Config.Provider.Models {
		if creation.Config.Provider.Models[index].Name == "explorer-model" {
			creation.Config.Provider.Models[index].MaxCalls = calls
		}
	}
	reservation, _, err := creation.Config.Provider.RoleReservation("explorer")
	if err != nil {
		t.Fatal(err)
	}
	plannerTokens := creation.Config.Access.Invocations["planner"].Tokens
	creation.Config.Access.Invocations["explorer"] = config.InvocationLimit{Tokens: reservation}
	creation.Config.Access.Limits.Tokens = plannerTokens + reservation
	return creation
}

type scheduledOpenCodeInterruptTurn struct {
	snapshot  Snapshot
	rootAgent agenttree.Node
	turn      taskscheduler.DynamicTask
}

func scheduledOpenCodePrepareExplorerTurn(t *testing.T, ctx context.Context, controllerPath, schedulerPath string, creation Creation, adapter ScheduledDispatchAdapter, scheduleNonce, agentName, question, turnNonce string) scheduledOpenCodeInterruptTurn {
	t.Helper()
	if err := os.MkdirAll(creation.Config.OpenCode.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(ctx, controllerPath)
	if err != nil || planned.Plan == nil {
		t.Fatal("fixture planner did not complete", err)
	}
	if err := Append(controllerPath, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := StartWorkspace(ctx, controllerPath)
	if err != nil || snapshot.Candidate == nil || snapshot.Workspace == nil {
		t.Fatal("real Git workspace unavailable", err)
	}
	planner, err := runtime.NewInvocation(snapshot.Creation.Config.Planner, snapshot.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	plannerContext, err := access.InputID(planner.Input)
	if err != nil {
		t.Fatal(err)
	}
	treePath := controllerPath + ".agent-tree"
	tree, err := agenttree.Inspect(treePath)
	if err != nil {
		t.Fatal(err)
	}
	var rootAgent agenttree.Node
	if tree.TreeID == "" {
		rootAgent, err = agenttree.Create(treePath, snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: plannerContext})
	} else {
		rootAgent, _ = agentNodeByID(tree, tree.RootAgentID)
		if tree.TreeID != snapshot.RunID || rootAgent.ParentAgentID != "" || rootAgent.Path != "/root" || rootAgent.Name != "root" || rootAgent.Role != "planner" || rootAgent.Authority != agenttree.AuthorityReadOnly || rootAgent.InvocationID != planner.ID || rootAgent.ContextSHA256 != plannerContext {
			err = errors.New("fixture planner root identity changed")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	seed, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("a", 64)
	if _, err := taskscheduler.Bind(schedulerPath, taskscheduler.Definition{Version: 1, Nonce: scheduleNonce, Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	_, turn, err := SpawnExplorerAgent(ctx, controllerPath, schedulerPath, rootAgent.AgentID, agentName, question, turnNonce)
	if err != nil {
		t.Fatal(err)
	}
	seedDecision, err := taskscheduler.Tick(ctx, schedulerPath, adapter)
	if err != nil || seedDecision.TaskID != seed.ID {
		t.Fatal("planner seed did not settle before interrupt turn", seedDecision, err)
	}
	return scheduledOpenCodeInterruptTurn{snapshot: snapshot, rootAgent: rootAgent, turn: turn}
}

func TestScheduledOpenCodeInterruptFixturePreflight(t *testing.T) {
	testScheduledOpenCodeInterruptFixturePreflight(t, false)
}

func TestScheduledOpenCodeCompositeInterruptFixturePreflight(t *testing.T) {
	testScheduledOpenCodeInterruptFixturePreflight(t, true)
}

func testScheduledOpenCodeInterruptFixturePreflight(t *testing.T, composite bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	explorerUpstream := httptest.NewTLSServer(http.NotFoundHandler())
	defer explorerUpstream.Close()
	var plannerMu sync.Mutex
	plannerCalls := 0
	plannerUpstream := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(request.Body, (1<<20)+1))
		plannerMu.Lock()
		plannerCalls++
		plannerMu.Unlock()
		if request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
			http.Error(writer, "invalid fixture planner request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
		_, _ = writer.Write(scheduledOpenCodeTextSSE(0, `{"plan":"fixture"}`))
	}))
	defer plannerUpstream.Close()
	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	schedulerPath := filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	creation := scheduledOpenCodeCreation(t, explorerUpstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state"))
	creation = scheduledOpenCodeInterruptCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(explorerUpstream.Certificate())
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	adapter := ScheduledDispatchAdapter{}
	fixtureName := "scheduled-opencode-interrupt-preflight"
	agentName := "interrupt-explorer"
	turnNonce := "interrupt-turn-nonce"
	if composite {
		adapter.JournalPath = schedulerPath
		fixtureName = "scheduled-opencode-composite-interrupt-preflight"
		agentName = "composite-interrupt-explorer"
		turnNonce = "composite-interrupt-turn-nonce"
	}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, withProviderTransportClient(ctx, transport), controllerPath, schedulerPath, creation, adapter, fixtureName, agentName, "Read file.txt with source_read, then wait.", turnNonce)
	plannerMu.Lock()
	completedPlannerCalls := plannerCalls
	plannerMu.Unlock()
	pool, poolErr := taskpool.Inspect(creation.Config.TaskPool.Path)
	scheduler, schedulerErr := taskscheduler.Inspect(schedulerPath)
	turnState := scheduler.Tasks[prepared.turn.Task.ID]
	if completedPlannerCalls != 1 || poolErr != nil || len(pool.Active) != 0 || schedulerErr != nil || turnState.Status != taskscheduler.StatusReady || turnState.Claim != nil {
		t.Fatal("fixture planning/scheduling did not settle exact authority", completedPlannerCalls, poolErr, pool, schedulerErr, turnState)
	}
}

func (p *scheduledOpenCodeLiveProvider) setFinalText(first, second string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalText = [2]string{first, second}
}

func (p *scheduledOpenCodeLiveProvider) snapshot() []json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		result[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	return result
}

func (p *scheduledOpenCodeLiveProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
		http.Error(writer, "invalid fixture provider request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	sequence := len(p.requests)
	finalText := p.finalText
	p.mu.Unlock()

	var response []byte
	switch sequence {
	case 1, 3:
		turn := (sequence + 1) / 2
		response = scheduledOpenCodeToolSSE(turn)
	case 2, 4:
		turn := sequence / 2
		if finalText[turn-1] == "" {
			http.Error(writer, "fixture result unavailable", http.StatusConflict)
			return
		}
		response = scheduledOpenCodeTextSSE(turn, finalText[turn-1])
	default:
		http.Error(writer, "unexpected extra provider request", http.StatusConflict)
		return
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(response)
}

func TestPinnedOpenCodeScheduledExplorerMultiTurn(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	provider := &scheduledOpenCodeLiveProvider{}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer upstream.Close()

	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	schedulerPath := filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	creation := scheduledOpenCodeCreation(t, upstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state"))
	if err := os.MkdirAll(creation.Config.OpenCode.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(ctx, controllerPath)
	if err != nil || planned.Plan == nil {
		t.Fatal("fixture planner did not complete", err)
	}
	if err := Append(controllerPath, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := StartWorkspace(ctx, controllerPath)
	if err != nil || snapshot.Candidate == nil || snapshot.Workspace == nil {
		t.Fatal("real Git workspace unavailable", err)
	}
	candidateID, err := snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	scheduledAssertOpenCodeStartupParity(t, snapshot)
	longRootProbe := scheduledOpenCodeAPIProjectDiagnostic(t, executable, snapshot, false)
	if strings.HasPrefix(longRootProbe, "api_ready=true") {
		t.Fatal("production-shaped long state root unexpectedly passed", longRootProbe)
	}
	t.Logf("OPENCODE_STARTUP_LONG_ROOT %s", longRootProbe)
	preflight := scheduledOpenCodeAPIProjectDiagnostic(t, executable, snapshot, true)
	if !strings.HasPrefix(preflight, "api_ready=true") {
		t.Fatal("compact state-root public startup failed", preflight)
	}
	t.Logf("OPENCODE_STARTUP_COMPACT_ROOT %s", preflight)
	firstOutput, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: "First scripted turn observed the committed fixture source.", Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	secondOutput, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: "Second scripted turn independently observed the committed fixture source.", Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	provider.setFinalText(string(firstOutput), string(secondOutput))
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)

	planner, err := runtime.NewInvocation(snapshot.Creation.Config.Planner, snapshot.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	plannerContext, err := access.InputID(planner.Input)
	if err != nil {
		t.Fatal(err)
	}
	rootAgent, err := agenttree.Create(controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: planner.ID, ContextSHA256: plannerContext})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("a", 64)
	if _, err := taskscheduler.Bind(schedulerPath, taskscheduler.Definition{Version: 1, Nonce: "scheduled-opencode-multiturn", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}

	const question = "Read file.txt with source_read and report the exact bounded observation."
	child, first, err := SpawnExplorerAgent(ctx, controllerPath, schedulerPath, rootAgent.AgentID, "scheduled-explorer", question, "turn-one-nonce")
	if err != nil {
		t.Fatal(err)
	}
	message, second, err := FollowUpExplorerAgent(ctx, controllerPath, schedulerPath, agentcontrol.MessageRequest{FromAgentID: rootAgent.AgentID, ToAgentID: child.AgentID, Nonce: "turn-two-nonce", Body: question}, question)
	if err != nil {
		t.Fatal(err)
	}
	if first.Task.InvocationID == second.Task.InvocationID || first.TurnID == second.TurnID || first.AgentID != second.AgentID || first.TurnSequence != 1 || second.TurnSequence != 2 || message.Body != question {
		t.Fatal("distinct same-agent turns were not precommitted exactly", first, second, message)
	}

	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	ctx = withProviderTransportClient(ctx, transport)
	adapter := ScheduledDispatchAdapter{}
	for attempts := 0; attempts < 4; attempts++ {
		current, inspectErr := taskscheduler.Inspect(schedulerPath)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if current.Tasks[first.Task.ID].Status == taskscheduler.StatusSucceeded && current.Tasks[second.Task.ID].Status == taskscheduler.StatusSucceeded {
			break
		}
		if firstState := current.Tasks[first.Task.ID]; firstState.Status == taskscheduler.StatusUnknown && firstState.Claim != nil {
			claimID, claimErr := firstState.Claim.ID()
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			recovered, recoveryErr := taskscheduler.RecoverClaim(ctx, schedulerPath, claimID, adapter)
			t.Fatalf("first scheduled turn became unknown before provider dispatch: recovery=%+v err=%v provider_requests=%d", recovered, recoveryErr, len(provider.snapshot()))
		}
		decision, tickErr := taskscheduler.Tick(ctx, schedulerPath, adapter)
		if tickErr != nil {
			diagnostic := "not-run"
			if strings.Contains(tickErr.Error(), "global identity") || strings.Contains(tickErr.Error(), "readback unavailable") {
				diagnostic = scheduledOpenCodeAPIProjectDiagnostic(t, executable, snapshot, true)
			}
			failedTurn := first
			if current.Tasks[first.Task.ID].Status == taskscheduler.StatusSucceeded {
				failedTurn = second
			}
			diagnostic += scheduledOpenCodeIntentDiagnostic(controllerPath, failedTurn, snapshot.Workspace.Request.Path)
			t.Fatalf("scheduled dispatch %d failed: %v; provider_requests=%d; project_diagnostic=%s", attempts+1, tickErr, len(provider.snapshot()), diagnostic)
		}
		if decision.TaskID == "" {
			stalled, _ := taskscheduler.Inspect(schedulerPath)
			t.Fatalf("scheduled dispatch %d made no decision: first=%+v second=%+v seed=%+v provider_requests=%d", attempts+1, stalled.Tasks[first.Task.ID], stalled.Tasks[second.Task.ID], stalled.Tasks[seed.ID], len(provider.snapshot()))
		}
	}

	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []taskscheduler.DynamicTask{first, second} {
		state := scheduled.Tasks[turn.Task.ID]
		if state.Status != taskscheduler.StatusSucceeded || state.Claim == nil || state.Claim.AgentTurn == nil || state.Claim.AgentTurn.TurnID != turn.TurnID || state.Evidence == nil || state.Evidence.InvocationID != turn.Task.InvocationID {
			t.Fatal("scheduled turn lacks exact successful evidence", turn, state)
		}
	}

	controller, err := Inspect(controllerPath)
	if err != nil || len(controller.Explorations) != 2 || len(controller.ProviderRuntime) != 2 {
		t.Fatal("controller did not retain two explorer results", len(controller.Explorations), len(controller.ProviderRuntime), err)
	}
	if controller.Explorations[0].Question != question || controller.Explorations[1].Question != question || controller.Explorations[0].Invocation.ID != first.Task.InvocationID || controller.Explorations[1].Invocation.ID != second.Task.InvocationID || controller.Explorations[0].Result.Output != string(firstOutput) || controller.Explorations[1].Result.Output != string(secondOutput) {
		t.Fatal("per-turn explorer results changed", controller.Explorations)
	}
	activities, err := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	turnStatus := map[string][]agenttree.Status{}
	turnOrder := make([]string, 0, 4)
	for _, activity := range activities.Activities {
		if activity.Kind == "turn" {
			turnStatus[activity.TurnID] = append(turnStatus[activity.TurnID], activity.Status)
			turnOrder = append(turnOrder, activity.TurnID+":"+string(activity.Status))
		}
	}
	wantFirst := []agenttree.Status{agenttree.StatusRunning, agenttree.StatusSucceeded}
	wantSecond := []agenttree.Status{agenttree.StatusRunning, agenttree.StatusSucceeded}
	if !reflect.DeepEqual(turnStatus[first.TurnID], wantFirst) || !reflect.DeepEqual(turnStatus[second.TurnID], wantSecond) || len(turnOrder) != 4 || turnOrder[1] != first.TurnID+":"+string(agenttree.StatusSucceeded) || turnOrder[2] != second.TurnID+":"+string(agenttree.StatusRunning) {
		t.Fatal("agent control did not retain FIFO per-turn terminal observations", turnOrder, turnStatus)
	}

	requestsBeforeRecovery := len(provider.snapshot())
	if requestsBeforeRecovery != 4 {
		t.Fatal("two tool turns did not issue exactly four provider requests", requestsBeforeRecovery)
	}
	runtimeHeads := map[string]bool{}
	gatewayHeads := map[string]bool{}
	for _, turn := range []taskscheduler.DynamicTask{first, second} {
		stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence})
		runtimePath := controllerPath + "." + stem + ".opencode-runtime.jsonl"
		gatewayPath := controllerPath + "." + stem + ".provider-gateway.jsonl"
		expectedIntent := scheduledOpenCodeRuntimeIntent(t, controller, turn.Task.InvocationID, runtimePath)
		runtimeState, inspectErr := opencoderuntime.Inspect(runtimePath, expectedIntent)
		gatewayState, gatewayErr := providergateway.Inspect(gatewayPath)
		brokerState, brokerErr := contextbroker.Inspect(runtimePath + ".broker")
		receipt := controller.ProviderRuntime[turn.Task.InvocationID]
		runtimeHead, headErr := journalHead(runtimePath)
		gatewayHead, gatewayHeadErr := journalHead(gatewayPath)
		if inspectErr != nil || gatewayErr != nil || brokerErr != nil || headErr != nil || gatewayHeadErr != nil || runtimeState.Intent == nil || runtimeState.Result == nil || runtimeState.Intent.Invocation.ID != turn.Task.InvocationID || receipt.InvocationID != turn.Task.InvocationID || receipt.RuntimeJournalHead != runtimeHead || receipt.GatewayJournalHead != gatewayHead || runtimeState.Result.GatewayHead != gatewayHead || len(gatewayState.Calls) != 2 || !gatewayState.Finished || gatewayState.Exhausted || brokerState.Calls != 1 || len(brokerState.Responses) != 1 || !brokerState.Closed {
			t.Fatal("per-turn runtime receipt is incomplete", turn.TurnSequence, inspectErr, gatewayErr, brokerErr, headErr, gatewayHeadErr, receipt, runtimeState, gatewayState, brokerState)
		}
		var chunk repository.SourceChunk
		if err := canonical.Decode(brokerState.Responses[0].Content, &chunk); err != nil {
			t.Fatal(err)
		}
		content, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		if decodeErr != nil || string(content) != "base\n" || chunk.Commit != snapshot.Creation.Repository.Commit {
			t.Fatal("turn source receipt differs from committed bytes", turn.TurnSequence, string(content), chunk, decodeErr)
		}
		if runtimeHeads[runtimeHead] || gatewayHeads[gatewayHead] {
			t.Fatal("turn journals share a terminal head", turn.TurnSequence, runtimeHead, gatewayHead)
		}
		runtimeHeads[runtimeHead], gatewayHeads[gatewayHead] = true, true

		recovered, recoverErr := opencoderuntime.Execute(context.Background(), opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, Intent: *runtimeState.Intent})
		if recoverErr != nil || !reflect.DeepEqual(recovered, *runtimeState.Result) || len(provider.snapshot()) != requestsBeforeRecovery {
			t.Fatal("offline OpenCode recovery changed a turn or repeated provider effects", turn.TurnSequence, recoverErr, recovered, len(provider.snapshot()))
		}
		evidence, reconcileErr := adapter.Reconcile(context.Background(), *scheduled.Tasks[turn.Task.ID].Claim)
		if reconcileErr != nil || evidence.Status != taskscheduler.StatusSucceeded || evidence.InvocationID != turn.Task.InvocationID || len(provider.snapshot()) != requestsBeforeRecovery {
			t.Fatal("scheduled offline reconciliation changed a turn or repeated provider effects", turn.TurnSequence, reconcileErr, evidence, len(provider.snapshot()))
		}
	}

	requests := provider.snapshot()
	firstCache, err := scheduledOpenCodePromptCacheKey(requests[0])
	if err != nil {
		t.Fatal(err)
	}
	secondCache, err := scheduledOpenCodePromptCacheKey(requests[2])
	if err != nil || firstCache == secondCache {
		t.Fatal("turn-scoped OpenCode sessions did not produce distinct provider cache identities", firstCache, secondCache, err)
	}
	t.Logf("OPENCODE_SCHEDULED_MULTITURN_PASS executable_sha256=%s turn1=%s turn2=%s provider_requests=%d", scheduledOpenCodeLiveDigest, first.TurnID, second.TurnID, len(requests))
}

func scheduledOpenCodeIntentDiagnostic(controllerPath string, turn taskscheduler.DynamicTask, candidate string) string {
	stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence})
	events, err := journal.Read(controllerPath + "." + stem + ".opencode-runtime.jsonl")
	if err != nil || len(events) == 0 {
		return ",runtime_intent=false"
	}
	var intent opencoderuntime.Intent
	if events[0].Kind != "opencode-runtime.intent" || canonical.Decode(events[0].Payload, &intent) != nil {
		return ",runtime_intent=false"
	}
	return fmt.Sprintf(",runtime_intent=true,runtime_directory_candidate=%t,project_directory_candidate=%t,project_worktree_candidate=%t", intent.Directory == candidate, intent.Project.Directory == candidate, intent.Project.Worktree == candidate)
}

func scheduledOpenCodeProjectDiagnostic(t *testing.T, executable string, snapshot Snapshot) string {
	t.Helper()
	workingDirectory, expectedGitDir, sourceRoot := snapshot.Workspace.Request.Path, snapshot.Workspace.GitDir, snapshot.Creation.Repository.Root
	probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stateRoot := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(stateRoot, name), 0700); err != nil {
			return "state-directory-error"
		}
	}
	environment, err := opencode.HostEnvironment(stateRoot, os.Environ())
	if err != nil {
		return "environment-error"
	}
	diagnosticBinding, err := contextbroker.NewBinding(strings.Repeat("f", 64), snapshot.Creation.Repository, &candidatetools.Binding{Workspace: *snapshot.Workspace, Candidate: *snapshot.Candidate}, contextbroker.Limits{MaxCalls: 4, MaxRequestBytes: 64 << 10, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 4 << 20})
	if err != nil {
		return "broker-binding-error"
	}
	diagnosticBroker, err := contextbroker.Open(filepath.Join(stateRoot, "diagnostic-broker.jsonl"), diagnosticBinding)
	if err != nil {
		return "broker-open-error"
	}
	defer diagnosticBroker.Close()
	const diagnosticBearer = "diagnostic-context-capability-0000000000000001"
	diagnosticServer, err := contextmcp.NewOwned(diagnosticBroker, diagnosticBearer, nil)
	if err != nil {
		return "context-server-error"
	}
	diagnosticRunning, err := diagnosticServer.Listen()
	if err != nil {
		return "context-listen-error"
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		_ = diagnosticRunning.Close(closeCtx)
		closeCancel()
		_ = diagnosticRunning.Wait()
	}()
	var diagnosticGatewayMu sync.Mutex
	diagnosticGatewayRequests := 0
	diagnosticGateway := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		diagnosticGatewayMu.Lock()
		diagnosticGatewayRequests++
		diagnosticGatewayMu.Unlock()
		http.NotFound(response, request)
	}))
	defer diagnosticGateway.Close()
	diagnosticToolNames := make([]string, 0, len(diagnosticBroker.Catalog()))
	for _, definition := range diagnosticBroker.Catalog() {
		diagnosticToolNames = append(diagnosticToolNames, definition.Name)
	}
	sort.Strings(diagnosticToolNames)
	configuration := scheduledOpenCodeDiagnosticConfiguration(t, snapshot.Creation.Config, diagnosticGateway.URL+"/v1", diagnosticRunning.URL(), diagnosticBearer, diagnosticToolNames)
	configurationKeys := 0
	for index, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, "OPENCODE_CONFIG_CONTENT") {
			configurationKeys++
			environment[index] = "OPENCODE_CONFIG_CONTENT=" + configuration
		}
	}
	if configurationKeys != 1 {
		return "configuration-environment-error"
	}
	gitControl, gitControlErr := os.ReadFile(filepath.Join(workingDirectory, ".git"))
	gitControlRegular := gitControlErr == nil
	gitTargetMatch := false
	if gitControlRegular {
		prefix := "gitdir: "
		value := strings.TrimSpace(string(gitControl))
		if strings.HasPrefix(value, prefix) {
			target := strings.TrimPrefix(value, prefix)
			if !filepath.IsAbs(target) {
				target = filepath.Join(workingDirectory, target)
			}
			gitTargetMatch = filepath.Clean(target) == filepath.Clean(expectedGitDir)
		}
	}
	var gitStdout, gitStderr strings.Builder
	gitExecutable, gitResolved := scheduledExecutableFromEnvironment(environment, "git")
	var gitErr error
	if gitResolved {
		git := exec.CommandContext(probeCtx, gitExecutable, "rev-parse", "--show-toplevel")
		git.Dir, git.Env = workingDirectory, environment
		git.Stdout, git.Stderr = &gitStdout, &gitStderr
		gitErr = git.Run()
	} else {
		gitErr = fmt.Errorf("git unavailable in private environment")
	}
	gitOK := gitErr == nil
	gitTopLevel := strings.TrimSpace(gitStdout.String())
	gitTopCandidate := gitOK && filepath.Clean(gitTopLevel) == filepath.Clean(workingDirectory)
	gitTopSource := gitOK && filepath.Clean(gitTopLevel) == filepath.Clean(sourceRoot)
	gitStderrEmpty := gitStderr.Len() == 0
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,port-error", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	const user, password = "diagnostic", "diagnostic-password"
	command := exec.CommandContext(probeCtx, executable, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	command.Dir = workingDirectory
	command.Env = append(environment, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128", "OPENCODE_SERVER_USERNAME="+user, "OPENCODE_SERVER_PASSWORD="+password)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,start-error", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch)
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	healthClient := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	healthy := false
	for !healthy && probeCtx.Err() == nil {
		req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint+"/global/health", nil)
		req.SetBasicAuth(user, password)
		response, requestErr := healthClient.Do(req)
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			healthy = response.StatusCode == http.StatusOK
		}
		if !healthy {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !healthy {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,health=false", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch)
	}
	healthClient.CloseIdleConnections()
	configClient := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	configCtx, stopConfig := context.WithCancel(probeCtx)
	configReq, _ := http.NewRequestWithContext(configCtx, http.MethodGet, endpoint+"/config?"+url.Values{"directory": []string{workingDirectory}}.Encode(), nil)
	configReq.SetBasicAuth(user, password)
	configStarted := time.Now()
	configResponse, configErr := configClient.Do(configReq)
	configElapsedMillis := time.Since(configStarted).Milliseconds()
	configTimeout := false
	if networkError, ok := configErr.(net.Error); ok {
		configTimeout = networkError.Timeout()
	}
	configStatus := 0
	configProviders, configMCPServers := -1, -1
	if configErr == nil {
		configStatus = configResponse.StatusCode
		configBody, _ := io.ReadAll(io.LimitReader(configResponse.Body, 1<<20))
		_ = configResponse.Body.Close()
		var configured struct {
			Provider map[string]json.RawMessage `json:"provider"`
			MCP      map[string]json.RawMessage `json:"mcp"`
		}
		if json.Unmarshal(configBody, &configured) == nil {
			configProviders, configMCPServers = len(configured.Provider), len(configured.MCP)
		}
	}
	stopConfig()
	configClient.CloseIdleConnections()
	projectClient := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint+"/project/current?"+url.Values{"directory": []string{workingDirectory}}.Encode(), nil)
	req.SetBasicAuth(user, password)
	response, err := projectClient.Do(req)
	if err != nil {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,config_status=%d,config_error=%t,config_timeout=%t,config_elapsed_ms=%d,project-request-error", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch, configStatus, configErr != nil, configTimeout, configElapsedMillis)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil || !json.Valid(body) {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,status=%d,project-body-error", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch, response.StatusCode)
	}
	var project struct {
		ID        string   `json:"id"`
		Worktree  string   `json:"worktree"`
		VCS       string   `json:"vcs"`
		Sandboxes []string `json:"sandboxes"`
	}
	if json.Unmarshal(body, &project) != nil {
		return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,status=%d,project-shape-error", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch, response.StatusCode)
	}
	sandboxCandidate := false
	for _, sandbox := range project.Sandboxes {
		sandboxCandidate = sandboxCandidate || sandbox == workingDirectory
	}
	diagnosticGatewayMu.Lock()
	gatewayRequests := diagnosticGatewayRequests
	diagnosticGatewayMu.Unlock()
	return fmt.Sprintf("git=%t,git_stderr_empty=%t,git_top_candidate=%t,git_top_source=%t,dotgit_regular=%t,dotgit_target_match=%t,config_status=%d,config_error=%t,config_timeout=%t,config_elapsed_ms=%d,config_providers=%d,config_mcp_servers=%d,status=%d,global=%t,vcs_git=%t,worktree_candidate=%t,worktree_source=%t,sandbox_candidate=%t,gateway_requests=%d", gitOK, gitStderrEmpty, gitTopCandidate, gitTopSource, gitControlRegular, gitTargetMatch, configStatus, configErr != nil, configTimeout, configElapsedMillis, configProviders, configMCPServers, response.StatusCode, project.ID == "global", project.VCS == "git", project.Worktree == workingDirectory, project.Worktree == sourceRoot, sandboxCandidate, gatewayRequests)
}

func scheduledOpenCodeDiagnosticConfiguration(t *testing.T, configured config.Config, gatewayEndpoint, mcpEndpoint, mcpBearer string, toolNames []string) string {
	t.Helper()
	resolved, expectation, err := ConfiguredProviderExpectation(configured, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	var controls providergateway.ResponsesRequestExpectation
	if err := canonical.Decode(expectation.Controls, &controls); err != nil {
		t.Fatal(err)
	}
	content, err := opencode.BuildProviderToolsConfiguration(opencode.ProviderConfigurationSpec{
		ProviderID:          configured.Explorer.Provider,
		ModelID:             configured.Explorer.Model,
		Protocol:            opencode.ProviderProtocol(resolved.Model.AdapterID),
		GatewayBaseURL:      gatewayEndpoint,
		GatewayCapability:   strings.Repeat("d", 64),
		ContextWindowTokens: resolved.Model.ContextWindowTokens,
		MaxOutputTokens:     expectation.MaxOutputTokens,
		Tools:               true,
		Reasoning:           controls.ReasoningEffort != "",
		TimeoutMillis:       int((5 * time.Minute).Milliseconds()),
		RuntimeAgent:        "build",
		Variant: opencode.ProviderVariantSpec{Name: resolved.Variant.Effort, Responses: &opencode.OpenAIResponsesVariantOptions{
			Effort: controls.ReasoningEffort, Summary: controls.ReasoningSummary, TextVerbosity: controls.TextVerbosity, Temperature: controls.Temperature, TopP: controls.TopP,
		}},
	}, opencode.ToolsConfigurationSpec{Endpoint: mcpEndpoint, Bearer: mcpBearer, ToolNames: toolNames, TimeoutMillis: int((30 * time.Second).Milliseconds())})
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func scheduledOpenCodeAPIProjectDiagnostic(t *testing.T, executable string, snapshot Snapshot, compact bool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	base := snapshot.Creation.Config.OpenCode.StateRoot
	diagnosticID := fmt.Sprintf("%064x", time.Now().UnixNano())
	stateRoot := filepath.Join(base, diagnosticID, "explorer-"+strings.Repeat("b", 64))
	if compact {
		stateRoot = filepath.Join(base, diagnosticID)
	}
	stateRootLength, longestPrivatePath := len(stateRoot), len(filepath.Join(stateRoot, "config"))
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		return "api-state-root-error"
	}
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(stateRoot, name), 0700); err != nil {
			return "api-state-directory-error"
		}
	}
	binding, err := contextbroker.NewBinding(strings.Repeat("f", 64), snapshot.Creation.Repository, &candidatetools.Binding{Workspace: *snapshot.Workspace, Candidate: *snapshot.Candidate}, contextbroker.Limits{MaxCalls: 4, MaxRequestBytes: 64 << 10, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 4 << 20})
	if err != nil {
		return "api-broker-binding-error"
	}
	broker, err := contextbroker.Open(filepath.Join(t.TempDir(), "api-broker.jsonl"), binding)
	if err != nil {
		return "api-broker-open-error"
	}
	defer broker.Close()
	const bearer = "diagnostic-context-capability-0000000000000001"
	server, err := contextmcp.NewOwned(broker, bearer, nil)
	if err != nil {
		return "api-context-server-error"
	}
	running, err := server.Listen()
	if err != nil {
		return "api-context-listen-error"
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(closeCtx)
		stop()
		_ = running.Wait()
	}()
	gateway := httptest.NewServer(http.NotFoundHandler())
	defer gateway.Close()
	names := make([]string, 0, len(broker.Catalog()))
	for _, definition := range broker.Catalog() {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	resolved, expectation, err := ConfiguredProviderExpectation(snapshot.Creation.Config, "explorer")
	if err != nil {
		return "api-provider-selection-error"
	}
	var controls providergateway.ResponsesRequestExpectation
	if canonical.Decode(expectation.Controls, &controls) != nil {
		return "api-controls-error"
	}
	providerSpec := opencode.ProviderConfigurationSpec{ProviderID: snapshot.Creation.Config.Explorer.Provider, ModelID: snapshot.Creation.Config.Explorer.Model, Protocol: opencode.ProviderProtocol(resolved.Model.AdapterID), GatewayBaseURL: gateway.URL + "/v1", GatewayCapability: strings.Repeat("d", 64), ContextWindowTokens: resolved.Model.ContextWindowTokens, MaxOutputTokens: expectation.MaxOutputTokens, Tools: true, Reasoning: controls.ReasoningEffort != "", TimeoutMillis: int((5 * time.Minute).Milliseconds()), RuntimeAgent: "build", Variant: opencode.ProviderVariantSpec{Name: resolved.Variant.Effort, Responses: &opencode.OpenAIResponsesVariantOptions{Effort: controls.ReasoningEffort, Summary: controls.ReasoningSummary, TextVerbosity: controls.TextVerbosity, Temperature: controls.Temperature, TopP: controls.TopP}}}
	toolsSpec := opencode.ToolsConfigurationSpec{Endpoint: running.URL(), Bearer: bearer, ToolNames: names, TimeoutMillis: int((30 * time.Second).Milliseconds())}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "api-port-error"
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	lease, err := worktree.AcquireRead(snapshot.Workspace.Request)
	if err != nil {
		return "api-candidate-lease-error"
	}
	defer lease.Close()
	var process *opencode.Process
	var configReceipt opencode.ProviderConfigurationReceipt
	var attempts []opencode.ProviderStartupAttempt
	var receipt opencode.ProjectReceipt
	gitDirOK, gitCommonDirOK := false, false
	err = lease.WithOwnership(snapshot.Workspace.Request, func(worktree.LeaseIdentity) error {
		environment, environmentErr := opencode.HostEnvironment(stateRoot, os.Environ())
		gitExecutable, resolvedGit := scheduledExecutableFromEnvironment(environment, "git")
		if environmentErr == nil && resolvedGit {
			for argument, target := range map[string]*bool{"--git-dir": &gitDirOK, "--git-common-dir": &gitCommonDirOK} {
				command := exec.CommandContext(ctx, gitExecutable, "rev-parse", argument)
				command.Dir, command.Env, command.Stdout, command.Stderr = snapshot.Workspace.Request.Path, environment, io.Discard, io.Discard
				*target = command.Run() == nil
			}
		}
		var startErr error
		process, configReceipt, attempts, startErr = opencode.StartReadyLimitedProviderToolsProjectProcessInDirectory(ctx, executable, scheduledOpenCodeLiveDigest, stateRoot, snapshot.Workspace.Request.Path, port, "diagnostic", "diagnostic-password", io.Discard, 1, 8*time.Second, false, providerSpec, toolsSpec, opencode.ProjectExpectation{Directory: snapshot.Workspace.Request.Path, Mode: opencode.ProjectModeGit, Worktree: snapshot.Workspace.Request.Path})
		if startErr != nil {
			return startErr
		}
		client, clientErr := opencode.NewClient(fmt.Sprintf("http://127.0.0.1:%d", port), "diagnostic", "diagnostic-password")
		if clientErr != nil {
			return clientErr
		}
		defer client.Close()
		receipt, clientErr = client.ReadCurrentProject(ctx, opencode.ProjectExpectation{Directory: snapshot.Workspace.Request.Path, Mode: opencode.ProjectModeGit, Worktree: snapshot.Workspace.Request.Path})
		return clientErr
	})
	if err != nil {
		outcome, projectPolls := "none", 0
		if len(attempts) == 1 {
			outcome, projectPolls = attempts[0].Outcome, attempts[0].ProjectReadinessPolls
		}
		return fmt.Sprintf("api_start_error=true,attempts=%d,outcome=%s,project_polls=%d,state_root_len=%d,longest_private_path_len=%d", len(attempts), outcome, projectPolls, stateRootLength, longestPrivatePath)
	}
	expectedToolIDs, _ := opencode.ToolPermissionIDs(toolsSpec.ToolNames)
	configIdentityMatches := configReceipt.ProviderID == providerSpec.ProviderID && configReceipt.ModelID == providerSpec.ModelID && configReceipt.GatewayBaseURL == providerSpec.GatewayBaseURL && configReceipt.ToolsConfiguration.Endpoint == toolsSpec.Endpoint
	configToolsMatch := reflect.DeepEqual(configReceipt.ToolsConfiguration.ToolIDs, expectedToolIDs)
	defer process.Close()
	elapsed, polls := int64(0), 0
	if len(attempts) == 1 {
		elapsed, polls = attempts[0].ReadinessElapsedMillis, attempts[0].ReadinessPolls
	}
	return fmt.Sprintf("api_ready=true,attempts=%d,config_elapsed_ms=%d,config_polls=%d,config_identity=%t,config_tools=%t,project_git=%t,project_worktree_candidate=%t,lease_git_dir=%t,lease_git_common_dir=%t,state_root_len=%d,longest_private_path_len=%d", len(attempts), elapsed, polls, configIdentityMatches, configToolsMatch, receipt.VCS == "git", receipt.Worktree == snapshot.Workspace.Request.Path, gitDirOK, gitCommonDirOK, stateRootLength, longestPrivatePath)
}

type scheduledNormalizedStartup struct {
	ExecutableSHA256 string                             `json:"executable_sha256"`
	StateRoot        string                             `json:"state_root"`
	WorkingDirectory string                             `json:"working_directory"`
	Provider         opencode.ProviderConfigurationSpec `json:"provider"`
	Tools            opencode.ToolsConfigurationSpec    `json:"tools"`
	Environment      map[string]string                  `json:"environment"`
}

func scheduledAssertOpenCodeStartupParity(t *testing.T, snapshot Snapshot) {
	t.Helper()
	binding, err := contextbroker.NewBinding(strings.Repeat("e", 64), snapshot.Creation.Repository, &candidatetools.Binding{Workspace: *snapshot.Workspace, Candidate: *snapshot.Candidate}, contextbroker.Limits{MaxCalls: 64, MaxRequestBytes: 64 << 10, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := contextbroker.Open(filepath.Join(t.TempDir(), "parity-broker.jsonl"), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	names := make([]string, 0, len(broker.Catalog()))
	for _, definition := range broker.Catalog() {
		names = append(names, definition.Name)
	}
	sort.Strings(names)
	productionProvider, productionTools := scheduledOpenCodeStartupSpecs(t, snapshot.Creation.Config, "http://127.0.0.1:41001/v1", strings.Repeat("p", 64), "http://127.0.0.1:41002/mcp", strings.Repeat("m", 64), names)
	diagnosticProvider, diagnosticTools := scheduledOpenCodeStartupSpecs(t, snapshot.Creation.Config, "http://127.0.0.1:42001/v1", strings.Repeat("q", 64), "http://127.0.0.1:42002/mcp", strings.Repeat("n", 64), names)
	production := scheduledNormalizeStartup(t, filepath.Join(t.TempDir(), strings.Repeat("a", 64), "explorer-"+strings.Repeat("b", 64)), snapshot.Workspace.Request.Path, productionProvider, productionTools, map[string]string{"http://127.0.0.1:41001/v1": "<gateway>", strings.Repeat("p", 64): "<provider-capability>", "http://127.0.0.1:41002/mcp": "<mcp>", strings.Repeat("m", 64): "<mcp-capability>"})
	diagnostic := scheduledNormalizeStartup(t, filepath.Join(t.TempDir(), strings.Repeat("c", 64), "explorer-"+strings.Repeat("d", 64)), snapshot.Workspace.Request.Path, diagnosticProvider, diagnosticTools, map[string]string{"http://127.0.0.1:42001/v1": "<gateway>", strings.Repeat("q", 64): "<provider-capability>", "http://127.0.0.1:42002/mcp": "<mcp>", strings.Repeat("n", 64): "<mcp-capability>"})
	productionHash, productionErr := canonical.Hash("harness.control.scheduled-opencode-startup-parity.v1", production)
	diagnosticHash, diagnosticErr := canonical.Hash("harness.control.scheduled-opencode-startup-parity.v1", diagnostic)
	if productionErr != nil || diagnosticErr != nil || productionHash != diagnosticHash || !reflect.DeepEqual(production, diagnostic) {
		t.Fatal("normalized production and diagnostic OpenCode startup projections differ", productionErr, diagnosticErr, productionHash, diagnosticHash)
	}
	t.Logf("OPENCODE_STARTUP_PARITY normalized_sha256=%s", productionHash)
}

func scheduledOpenCodeStartupSpecs(t *testing.T, configured config.Config, gatewayEndpoint, gatewayCapability, mcpEndpoint, mcpBearer string, toolNames []string) (opencode.ProviderConfigurationSpec, opencode.ToolsConfigurationSpec) {
	t.Helper()
	resolved, expectation, err := ConfiguredProviderExpectation(configured, "explorer")
	if err != nil {
		t.Fatal(err)
	}
	var controls providergateway.ResponsesRequestExpectation
	if err := canonical.Decode(expectation.Controls, &controls); err != nil {
		t.Fatal(err)
	}
	provider := opencode.ProviderConfigurationSpec{ProviderID: configured.Explorer.Provider, ModelID: configured.Explorer.Model, Protocol: opencode.ProviderProtocol(resolved.Model.AdapterID), GatewayBaseURL: gatewayEndpoint, GatewayCapability: gatewayCapability, ContextWindowTokens: resolved.Model.ContextWindowTokens, MaxOutputTokens: expectation.MaxOutputTokens, Tools: true, Reasoning: controls.ReasoningEffort != "", TimeoutMillis: int((5 * time.Minute).Milliseconds()), RuntimeAgent: "build", Variant: opencode.ProviderVariantSpec{Name: resolved.Variant.Effort, Responses: &opencode.OpenAIResponsesVariantOptions{Effort: controls.ReasoningEffort, Summary: controls.ReasoningSummary, TextVerbosity: controls.TextVerbosity, Temperature: controls.Temperature, TopP: controls.TopP}}}
	tools := opencode.ToolsConfigurationSpec{Endpoint: mcpEndpoint, Bearer: mcpBearer, ToolNames: append([]string(nil), toolNames...), TimeoutMillis: int((30 * time.Second).Milliseconds())}
	return provider, tools
}

func scheduledNormalizeStartup(t *testing.T, stateRoot, workingDirectory string, provider opencode.ProviderConfigurationSpec, tools opencode.ToolsConfigurationSpec, replacements map[string]string) scheduledNormalizedStartup {
	t.Helper()
	content, err := opencode.BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := opencode.HostEnvironment(stateRoot, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	seenConfig := 0
	for index, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "OPENCODE_CONFIG_CONTENT") {
			environment[index] = key + "=" + content
			seenConfig++
		}
	}
	if seenConfig != 1 {
		t.Fatal("startup environment did not contain exactly one configuration key")
	}
	environment = append(environment, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=128", "OPENCODE_SERVER_USERNAME=startup-user", "OPENCODE_SERVER_PASSWORD=startup-password")
	normalize := func(value string) string {
		value = strings.ReplaceAll(value, stateRoot, "<state-root>")
		for source, target := range replacements {
			value = strings.ReplaceAll(value, source, target)
		}
		return value
	}
	provider.GatewayBaseURL, provider.GatewayCapability = "<gateway>", "<provider-capability>"
	tools.Endpoint, tools.Bearer = "<mcp>", "<mcp-capability>"
	values := map[string]string{}
	seen := map[string]bool{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if !ok || seen[key] {
			t.Fatal("ambiguous normalized startup environment")
		}
		seen[key] = true
		values[key] = normalize(value)
	}
	return scheduledNormalizedStartup{ExecutableSHA256: scheduledOpenCodeLiveDigest, StateRoot: "<state-root>", WorkingDirectory: workingDirectory, Provider: provider, Tools: tools, Environment: values}
}

func scheduledExecutableFromEnvironment(environment []string, name string) (string, bool) {
	values := map[string]string{}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[strings.ToUpper(key)] = value
		}
	}
	extensions := []string{""}
	if configured := values["PATHEXT"]; configured != "" {
		extensions = filepath.SplitList(configured)
	}
	for _, directory := range filepath.SplitList(values["PATH"]) {
		for _, extension := range extensions {
			candidate := filepath.Join(directory, name+strings.ToLower(extension))
			stat, err := os.Stat(candidate)
			if err == nil && stat.Mode().IsRegular() {
				return candidate, true
			}
			candidate = filepath.Join(directory, name+strings.ToUpper(extension))
			stat, err = os.Stat(candidate)
			if err == nil && stat.Mode().IsRegular() {
				return candidate, true
			}
		}
	}
	return "", false
}

func scheduledOpenCodeRuntimeIntent(t *testing.T, controller Snapshot, invocationID, runtimePath string) opencoderuntime.Intent {
	t.Helper()
	dispatch, ok := controller.AgentDispatch[invocationID]
	if !ok || controller.Workspace == nil {
		t.Fatal("turn dispatch or workspace unavailable")
	}
	brokerState, err := contextbroker.Inspect(runtimePath + ".broker")
	if err != nil || brokerState.Binding == nil {
		t.Fatal("turn context binding unavailable", err)
	}
	broker, err := contextbroker.Open(runtimePath+".broker", *brokerState.Binding)
	if err != nil {
		t.Fatal(err)
	}
	catalogHash, err := opencoderuntime.ContextCatalogSHA256(broker)
	if err != nil {
		t.Fatal(err)
	}
	toolNames := make([]string, 0, len(broker.Catalog()))
	for _, definition := range broker.Catalog() {
		toolNames = append(toolNames, definition.Name)
	}
	sort.Strings(toolNames)
	version := 2
	var receiptBinding *toolreceipts.Binding
	if receipts, receiptErr := toolreceipts.Inspect(runtimePath + ".tool-receipts"); receiptErr == nil && receipts.Binding != nil {
		copy := *receipts.Binding
		copy.Tools = append([]toolreceipts.ToolOwner(nil), receipts.Binding.Tools...)
		receiptBinding = &copy
		version = 3
		catalogHash = copy.CatalogSHA256
		toolNames = make([]string, len(copy.Tools))
		for index, tool := range copy.Tools {
			toolNames[index] = tool.Tool
		}
		sort.Strings(toolNames)
	}
	inputHash, err := access.InputID(dispatch.Admission.Invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ResolveProviderRouting(controller.Creation.Config, controller.RunID, "explorer", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	gatewayID, err := selected.Gateway.ID()
	if err != nil {
		t.Fatal(err)
	}
	workingDirectory := controller.Workspace.Request.Path
	intent := opencoderuntime.Intent{
		Version: version, Invocation: dispatch.Admission.Invocation, Directory: workingDirectory,
		Project: opencode.ProjectExpectation{Directory: workingDirectory, Mode: opencode.ProjectModeGit, Worktree: workingDirectory},
		Context: *brokerState.Binding, Session: opencode.ToolSessionBinding{ToolNames: []string{}},
		SessionPlan:              &opencoderuntime.SessionPlan{Agent: "build", Provider: dispatch.Admission.Invocation.Profile.Provider, Model: dispatch.Admission.Invocation.Profile.Model, Variant: dispatch.Admission.Invocation.Profile.Effort, ToolNames: toolNames, CatalogSHA256: catalogHash},
		ProviderGatewayBindingID: gatewayID,
		ToolReceipts:             receiptBinding,
	}
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func scheduledOpenCodeCompositeVerifier(t *testing.T, controllerPath, schedulerPath, runtimePath string, controller Snapshot, turn taskscheduler.DynamicTask) opencode.CompositeBackendVerifier {
	t.Helper()
	dispatch, ok := controller.AgentDispatch[turn.Task.InvocationID]
	if !ok || dispatch.Admission.AgentTurn == nil {
		t.Fatal("composite agent dispatch unavailable")
	}
	admissionID, err := dispatch.Admission.ID()
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	state := scheduled.Tasks[turn.Task.ID]
	if state.Claim == nil || state.Claim.AgentTurn == nil || *state.Claim.AgentTurn != *dispatch.Admission.AgentTurn {
		t.Fatal("composite scheduled claim unavailable")
	}
	brokerState, err := contextbroker.Inspect(runtimePath + ".broker")
	if err != nil || brokerState.Binding == nil {
		t.Fatal("composite broker binding unavailable", err)
	}
	receipts, err := toolreceipts.Inspect(runtimePath + ".tool-receipts")
	if err != nil || receipts.Binding == nil {
		t.Fatal("composite receipt binding unavailable", err)
	}
	caller := agentDispatchBinding{JournalPath: controllerPath + ".agent-tree", ControllerPath: controllerPath, AdmissionID: admissionID, Node: dispatch.Admission.Node, InvocationID: turn.Task.InvocationID, AgentTurn: dispatch.Admission.AgentTurn, Recovering: true}
	verify, err := newCompositeBackendVerifier(controllerPath, schedulerPath, runtimePath+".broker", caller, *brokerState.Binding, *receipts.Binding)
	if err != nil {
		t.Fatal(err)
	}
	return verify
}

func scheduledOpenCodeCreation(t *testing.T, endpointURL, executable, stateRoot string) Creation {
	t.Helper()
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, Reasoning: true, SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{"prompt_cache_key"}, TrailingCostPingV1: true})
	if err != nil {
		t.Fatal(err)
	}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 128, StateMode: "full-input-stateless", Store: false, SystemRole: "developer", ReasoningEffort: "high", ReasoningSummary: "auto", ToolChoice: "auto", TextVerbosity: "high", Include: []string{"reasoning.encrypted_content"}, FunctionToolStrict: false})
	if err != nil {
		t.Fatal(err)
	}
	providerID := "engorch-responses-fixture"
	model := config.ProviderModel{Name: "explorer-model", Version: 2, Provider: providerID, Model: scheduledOpenCodeLiveModel, AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: config.ProviderCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: string(capabilities), ContextWindowTokens: 8192, MaxCalls: 2, MaxRequestBytes: 1 << 20, MaxResponseBytes: 1 << 20, MaxOutputTokens: 128}
	contract, err := model.Contract()
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := contract.ConservativeReservation()
	if err != nil {
		t.Fatal(err)
	}
	creation := creation(t)
	creation.Config.Version = 2
	explorer := runtime.Profile{Runtime: "opencode-http", Provider: providerID, Model: scheduledOpenCodeLiveModel, Effort: "high", Role: "explorer"}
	creation.Config.Explorer = &explorer
	creation.Config.Access = &config.Access{
		Class:  access.Public,
		Limits: access.Limits{Tokens: 100 + 2*reservation, Concurrency: 1},
		Profiles: []access.Profile{
			{Version: 1, Name: "fixture-planner", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Public}},
			{Version: 1, Name: "fixture-provider", Kind: "subscription", Runtime: "opencode-http", Provider: providerID, CredentialRef: "control-live-key", RepositoryClasses: []access.Class{access.Public}},
		},
		Roles:       map[string]string{"planner": "fixture-planner", "explorer": "fixture-provider"},
		Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 100}, "explorer": {Tokens: reservation}},
	}
	creation.Config.Provider = &config.Provider{
		Version:     1,
		Credentials: []config.ProviderCredential{{Ref: "control-live-key", Environment: "ENGORCH_CONTROL_OPENCODE_KEY"}},
		Endpoints:   []config.ProviderEndpoint{{Name: "responses", Version: 2, Provider: providerID, URL: endpointURL, AdapterID: providergateway.OpenAIResponsesAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "control-live-key"}}},
		Models:      []config.ProviderModel{model},
		Roles:       map[string]config.ProviderRole{"explorer": {Endpoint: "responses", Model: "explorer-model", AdapterControlsJSON: string(controls), Variant: config.ProviderVariant{Effort: "high", SystemRole: "developer", ReasoningSummary: "auto", TextFormat: "", TextVerbosity: "high"}, RequiredCapabilities: &config.ProviderRequiredCapabilities{Tools: true, Reasoning: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}},
	}
	creation.Config.OpenCode = &config.OpenCodeHost{Version: 1, Executable: executable, ExecutableHash: scheduledOpenCodeLiveDigest, StateRoot: stateRoot}
	sourceRoot := creation.Repository.Root
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", sourceRoot}, arguments...)...)
		if output, runErr := command.CombinedOutput(); runErr != nil {
			t.Fatalf("git fixture failed: %v: %s", runErr, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(sourceRoot, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	creation.Repository, err = repository.Discover(context.Background(), sourceRoot, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	return creation
}

func scheduledOpenCodeRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("repository root unavailable")
		}
		directory = parent
	}
}

func scheduledOpenCodePromptCacheKey(raw json.RawMessage) (string, error) {
	var request struct {
		PromptCacheKey string `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return "", err
	}
	if request.PromptCacheKey == "" {
		return "", fmt.Errorf("provider request lacks prompt cache key")
	}
	return request.PromptCacheKey, nil
}

func scheduledOpenCodeSnapshot(id, status string, output, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": 123, "status": status,
		"background": false, "completed_at": nil, "error": nil, "frequency_penalty": 0.0,
		"incomplete_details": nil, "instructions": nil, "max_output_tokens": 128,
		"max_tool_calls": nil, "model": scheduledOpenCodeLiveModel, "moderation": nil, "output": output,
		"parallel_tool_calls": true, "presence_penalty": 0.0, "previous_response_id": nil,
		"prompt_cache_key": nil, "prompt_cache_retention": nil,
		"reasoning":         map[string]any{"effort": "high", "summary": "auto"},
		"safety_identifier": nil, "service_tier": "default", "store": false, "temperature": 1.0,
		"text":        map[string]any{"format": map[string]any{"type": "text"}, "verbosity": "high"},
		"tool_choice": "auto", "tools": []any{}, "top_logprobs": 0, "top_p": 1.0,
		"truncation": "disabled", "usage": usage, "user": nil, "metadata": map[string]any{},
	}
}

func scheduledOpenCodeEvents(events ...map[string]any) []byte {
	var output strings.Builder
	for _, event := range events {
		raw, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(&output, "event: %s\ndata: %s\n\n", event["type"], raw)
	}
	return []byte(output.String())
}

func scheduledOpenCodeToolSSE(turn int) []byte {
	id := fmt.Sprintf("resp_scheduled_tool_%d", turn)
	item := fmt.Sprintf("fc_scheduled_tool_%d", turn)
	reasoning := fmt.Sprintf("rs_scheduled_reasoning_%d", turn)
	callID := fmt.Sprintf("call_scheduled_source_read_%d", turn)
	arguments := `{"path":"file.txt","offset":0,"limit":64}`
	addedReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-added", "summary": []any{}}
	doneReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-done", "summary": []any{}}
	terminalReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-terminal", "summary": []any{}}
	pending := map[string]any{"id": item, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": callID, "name": "engorch_source_read"}
	done := map[string]any{"id": item, "type": "function_call", "status": "completed", "arguments": arguments, "call_id": callID, "name": "engorch_source_read"}
	return scheduledOpenCodeEvents(
		map[string]any{"type": "response.created", "response": scheduledOpenCodeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": scheduledOpenCodeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": addedReasoning, "sequence_number": 2},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": doneReasoning, "sequence_number": 3},
		map[string]any{"type": "response.output_item.added", "output_index": 1, "item": pending, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": arguments, "item_id": item, "obfuscation": "fixture", "output_index": 1, "sequence_number": 5},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": arguments, "item_id": item, "output_index": 1, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": scheduledOpenCodeSnapshot(id, "completed", []any{terminalReasoning, done}, map[string]any{"input_tokens": 9, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 4, "output_tokens_details": map[string]any{"reasoning_tokens": 1}, "total_tokens": 13}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func scheduledOpenCodeListToolSSE() []byte {
	const (
		responseID = "resp_scheduled_composite_list"
		reasoning  = "rs_scheduled_composite_list"
		listItem   = "fc_scheduled_composite_list"
	)
	addedReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-composite-list-added", "summary": []any{}}
	doneReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-composite-list-done", "summary": []any{}}
	terminalReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-composite-list-terminal", "summary": []any{}}
	listPending := map[string]any{"id": listItem, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": scheduledCompositeListCall, "name": "engorch_list_agents"}
	listDone := map[string]any{"id": listItem, "type": "function_call", "status": "completed", "arguments": scheduledCompositeListArgs, "call_id": scheduledCompositeListCall, "name": "engorch_list_agents"}
	return scheduledOpenCodeEvents(
		map[string]any{"type": "response.created", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": addedReasoning, "sequence_number": 2},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": doneReasoning, "sequence_number": 3},
		map[string]any{"type": "response.output_item.added", "output_index": 1, "item": listPending, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": scheduledCompositeListArgs, "item_id": listItem, "obfuscation": "fixture", "output_index": 1, "sequence_number": 5},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": scheduledCompositeListArgs, "item_id": listItem, "output_index": 1, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": listDone, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": scheduledOpenCodeSnapshot(responseID, "completed", []any{terminalReasoning, listDone}, map[string]any{"input_tokens": 8, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 1}, "total_tokens": 11}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func scheduledOpenCodeParallelCompositeToolSSE() []byte {
	const (
		responseID = "resp_scheduled_parallel_composite"
		reasoning  = "rs_scheduled_parallel_composite"
		sourceItem = "fc_scheduled_parallel_source"
		listItem   = "fc_scheduled_parallel_list"
		sourceCall = "call_scheduled_source_read_parallel"
		sourceArgs = `{"path":"file.txt","offset":0,"limit":64}`
	)
	addedReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-parallel-added", "summary": []any{}}
	doneReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-parallel-done", "summary": []any{}}
	terminalReasoning := map[string]any{"id": reasoning, "type": "reasoning", "encrypted_content": "scheduled-parallel-terminal", "summary": []any{}}
	sourcePending := map[string]any{"id": sourceItem, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": sourceCall, "name": "engorch_source_read"}
	sourceDone := map[string]any{"id": sourceItem, "type": "function_call", "status": "completed", "arguments": sourceArgs, "call_id": sourceCall, "name": "engorch_source_read"}
	listPending := map[string]any{"id": listItem, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": scheduledCompositeListCall, "name": "engorch_list_agents"}
	listDone := map[string]any{"id": listItem, "type": "function_call", "status": "completed", "arguments": scheduledCompositeListArgs, "call_id": scheduledCompositeListCall, "name": "engorch_list_agents"}
	return scheduledOpenCodeEvents(
		map[string]any{"type": "response.created", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": addedReasoning, "sequence_number": 2},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": doneReasoning, "sequence_number": 3},
		map[string]any{"type": "response.output_item.added", "output_index": 1, "item": sourcePending, "sequence_number": 4},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": sourceArgs, "item_id": sourceItem, "obfuscation": "fixture", "output_index": 1, "sequence_number": 5},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": sourceArgs, "item_id": sourceItem, "output_index": 1, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 1, "item": sourceDone, "sequence_number": 7},
		map[string]any{"type": "response.output_item.added", "output_index": 2, "item": listPending, "sequence_number": 8},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": scheduledCompositeListArgs, "item_id": listItem, "obfuscation": "fixture", "output_index": 2, "sequence_number": 9},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": scheduledCompositeListArgs, "item_id": listItem, "output_index": 2, "sequence_number": 10},
		map[string]any{"type": "response.output_item.done", "output_index": 2, "item": listDone, "sequence_number": 11},
		map[string]any{"type": "response.completed", "response": scheduledOpenCodeSnapshot(responseID, "completed", []any{terminalReasoning, sourceDone, listDone}, map[string]any{"input_tokens": 17, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 7, "output_tokens_details": map[string]any{"reasoning_tokens": 2}, "total_tokens": 24}), "sequence_number": 12},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func scheduledOpenCodeTextSSE(turn int, text string) []byte {
	id := fmt.Sprintf("resp_scheduled_text_%d", turn)
	item := fmt.Sprintf("msg_scheduled_text_%d", turn)
	pending := map[string]any{"id": item, "type": "message", "status": "in_progress", "content": []any{}, "role": "assistant"}
	empty := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": ""}
	part := map[string]any{"type": "output_text", "annotations": []any{}, "logprobs": []any{}, "text": text}
	done := map[string]any{"id": item, "type": "message", "status": "completed", "content": []any{part}, "role": "assistant"}
	return scheduledOpenCodeEvents(
		map[string]any{"type": "response.created", "response": scheduledOpenCodeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": scheduledOpenCodeSnapshot(id, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": pending, "sequence_number": 2},
		map[string]any{"type": "response.content_part.added", "content_index": 0, "item_id": item, "output_index": 0, "part": empty, "sequence_number": 3},
		map[string]any{"type": "response.output_text.delta", "content_index": 0, "delta": text, "item_id": item, "logprobs": []any{}, "obfuscation": "fixture", "output_index": 0, "sequence_number": 4},
		map[string]any{"type": "response.output_text.done", "content_index": 0, "item_id": item, "logprobs": []any{}, "output_index": 0, "sequence_number": 5, "text": text},
		map[string]any{"type": "response.content_part.done", "content_index": 0, "item_id": item, "output_index": 0, "part": part, "sequence_number": 6},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": done, "sequence_number": 7},
		map[string]any{"type": "response.completed", "response": scheduledOpenCodeSnapshot(id, "completed", []any{done}, map[string]any{"input_tokens": 18, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 3, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 21}), "sequence_number": 8},
		map[string]any{"type": "ping", "cost": "0"},
	)
}
