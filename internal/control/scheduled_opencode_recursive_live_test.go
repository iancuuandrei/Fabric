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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolreceipts"
)

const (
	scheduledRecursiveSpawnCall  = "call_scheduled_recursive_spawn"
	scheduledRecursiveSourceCall = "call_scheduled_recursive_child_source"
	scheduledRecursiveMaxWaits   = 8
)

type scheduledRecursiveProvider struct {
	mu sync.Mutex

	requests       []json.RawMessage
	parentStarted  bool
	parentKey      string
	childKey       string
	childQuestion  string
	expectedCommit string
	childOutput    string
	parentOutput   string
	child          scheduledRecursiveSpawnResult
	waits          int
	after          int
	accepted       *waitAgentAcceptedResult
	catalogOK      bool
	failure        string

	childAdmitted chan struct{}
	childRelease  chan struct{}
	admitOnce     sync.Once
	releaseOnce   sync.Once
}

type scheduledRecursivePlannerProvider struct {
	mu       sync.Mutex
	requests int
}

func (p *scheduledRecursivePlannerProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
		http.Error(writer, "invalid recursive planner request", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests++
	count := p.requests
	p.mu.Unlock()
	if count != 1 {
		http.Error(writer, "unexpected recursive planner request", http.StatusConflict)
		return
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(scheduledOpenCodeTextSSE(77, `{"plan":"fixture"}`))
}

func (p *scheduledRecursivePlannerProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

type scheduledRecursiveSpawnResult struct {
	AgentID       string `json:"agent_id"`
	ParentAgentID string `json:"parent_agent_id"`
	TurnID        string `json:"turn_id"`
	TurnSequence  int    `json:"turn_sequence"`
	TaskID        string `json:"task_id"`
}

type scheduledRecursiveFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type scheduledRecursiveRequest struct {
	PromptCacheKey string                             `json:"prompt_cache_key"`
	Input          []scheduledRecursiveFunctionOutput `json:"input"`
}

func (p *scheduledRecursiveProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
		p.setFailure("invalid-request")
		http.Error(writer, "invalid recursive fixture request", http.StatusBadRequest)
		return
	}
	var decoded scheduledRecursiveRequest
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.PromptCacheKey == "" {
		p.setFailure("invalid-envelope")
		http.Error(writer, "invalid recursive fixture envelope", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	response, child, err := p.responseLocked(body, decoded)
	if err != nil && p.failure == "" {
		p.failure = err.Error()
	}
	p.mu.Unlock()
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	if child {
		p.admitOnce.Do(func() { close(p.childAdmitted) })
		select {
		case <-p.childRelease:
		case <-request.Context().Done():
			return
		}
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(response)
}

func (p *scheduledRecursiveProvider) setFailure(class string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == "" {
		p.failure = class
	}
}

func (p *scheduledRecursiveProvider) responseLocked(body []byte, request scheduledRecursiveRequest) ([]byte, bool, error) {
	outputs := make([]scheduledRecursiveFunctionOutput, 0, len(request.Input))
	for _, item := range request.Input {
		if item.Type == "function_call_output" {
			outputs = append(outputs, item)
		}
	}
	if len(outputs) == 0 {
		if !p.parentStarted {
			if err := scheduledValidateRecursiveCatalog(body); err != nil {
				return nil, false, err
			}
			p.parentStarted, p.catalogOK, p.parentKey = true, true, request.PromptCacheKey
			arguments, _ := canonical.Bytes(spawnAgentArgs{Name: "recursive-child", Question: p.childQuestion, Nonce: "recursive-child-nonce"})
			return scheduledRecursiveToolSSE("recursive_spawn", scheduledRecursiveSpawnCall, "engorch_spawn_agent", string(arguments)), false, nil
		}
		if !bytes.Contains(body, []byte(p.childQuestion)) || request.PromptCacheKey == p.parentKey || p.childOutput == "" {
			return nil, false, errors.New("recursive child request identity changed")
		}
		if p.childKey != "" && p.childKey != request.PromptCacheKey {
			return nil, false, errors.New("recursive child cache identity changed")
		}
		p.childKey = request.PromptCacheKey
		return scheduledRecursiveToolSSE("recursive_child_source", scheduledRecursiveSourceCall, "engorch_source_read", `{"path":"file.txt","offset":0,"limit":64}`), true, nil
	}
	latest := outputs[len(outputs)-1]
	switch {
	case latest.CallID == scheduledRecursiveSpawnCall:
		var envelope struct {
			Result scheduledRecursiveSpawnResult `json:"result"`
		}
		if err := json.Unmarshal([]byte(latest.Output), &envelope); err != nil || envelope.Result.AgentID == "" || envelope.Result.ParentAgentID == "" || envelope.Result.TurnID == "" || envelope.Result.TaskID != envelope.Result.TurnID || envelope.Result.TurnSequence != 1 {
			return nil, false, errors.New("recursive spawn result invalid")
		}
		p.child = envelope.Result
		p.waits = 1
		return scheduledRecursiveWaitSSE(p.waits, p.child.AgentID, 0), false, nil
	case strings.HasPrefix(latest.CallID, "call_scheduled_recursive_wait_"):
		var envelope struct {
			Result waitAgentOutput `json:"result"`
		}
		if err := json.Unmarshal([]byte(latest.Output), &envelope); err != nil || envelope.Result.DeliveryVersion != waitAgentDeliveryVersion {
			return nil, false, errors.New("recursive wait result invalid")
		}
		if len(envelope.Result.AcceptedResults) > 0 {
			if len(envelope.Result.AcceptedResults) != 1 || p.childOutput == "" || p.parentOutput == "" {
				return nil, false, errors.New("recursive accepted result count changed")
			}
			accepted := envelope.Result.AcceptedResults[0]
			if accepted.Result.Reference.AgentID != p.child.AgentID || accepted.Result.Reference.ParentAgentID != p.child.ParentAgentID || accepted.Result.Reference.TurnID != p.child.TurnID || accepted.Result.Reference.TurnSequence != p.child.TurnSequence || accepted.Exploration.Summary == "" {
				return nil, false, errors.New("recursive accepted result identity changed")
			}
			encoded, encodeErr := canonical.Bytes(accepted.Exploration)
			if encodeErr != nil || string(encoded) != p.childOutput {
				return nil, false, errors.New("recursive accepted result body changed")
			}
			copy := accepted
			p.accepted = &copy
			return scheduledOpenCodeTextSSE(43, p.parentOutput), false, nil
		}
		if p.waits >= scheduledRecursiveMaxWaits {
			return nil, false, errors.New("recursive wait bound exhausted")
		}
		after := p.after
		if envelope.Result.NextAfter > after {
			after = envelope.Result.NextAfter
		}
		for _, activity := range envelope.Result.Activities {
			if activity.Sequence > after {
				after = activity.Sequence
			}
		}
		if after < p.after {
			return nil, false, errors.New("recursive wait cursor regressed")
		}
		p.after = after
		p.waits++
		return scheduledRecursiveWaitSSE(p.waits, p.child.AgentID, after), false, nil
	case latest.CallID == scheduledRecursiveSourceCall:
		var response contextbroker.Response
		if err := canonical.Decode([]byte(latest.Output), &response); err != nil || !response.Success || response.CallID == "" || response.InvocationID == "" || len(response.Content) == 0 {
			return nil, false, errors.New("recursive child source receipt envelope changed")
		}
		var chunk repository.SourceChunk
		if err := canonical.Decode(response.Content, &chunk); err != nil || chunk.Commit != p.expectedCommit {
			return nil, false, errors.New("recursive child source result identity changed")
		}
		content, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
		if decodeErr != nil || string(content) != "base\n" {
			return nil, false, errors.New("recursive child source content changed")
		}
		return scheduledOpenCodeTextSSE(42, p.childOutput), false, nil
	default:
		return nil, false, errors.New("recursive fixture received unknown tool output")
	}
}

func (p *scheduledRecursiveProvider) releaseChild() {
	p.releaseOnce.Do(func() { close(p.childRelease) })
}

func (p *scheduledRecursiveProvider) snapshot() ([]json.RawMessage, scheduledRecursiveSpawnResult, int, *waitAgentAcceptedResult, string, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	requests := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		requests[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	var accepted *waitAgentAcceptedResult
	if p.accepted != nil {
		copy := *p.accepted
		accepted = &copy
	}
	return requests, p.child, p.waits, accepted, p.parentKey, p.childKey, p.catalogOK
}

func (p *scheduledRecursiveProvider) failureClass() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failure
}

func scheduledValidateRecursiveCatalog(raw json.RawMessage) error {
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
	projected, err := opencoderuntime.ProviderRequestToolsForCatalog(catalog, []string{"spawn_agent", "wait_agent"})
	if err != nil || len(projected) != 2 {
		return errors.Join(errors.New("recursive provider tools unavailable"), err)
	}
	want := map[string]json.RawMessage{}
	for _, tool := range projected {
		want[tool.Name] = tool.Parameters
	}
	seen := map[string]bool{}
	for _, tool := range request.Tools {
		expected, ok := want[tool.Name]
		if !ok {
			continue
		}
		actual, normalizeErr := canonical.Normalize(tool.Parameters)
		if normalizeErr != nil || !bytes.Equal(actual, expected) {
			return errors.New("recursive provider tool schema changed")
		}
		seen[tool.Name] = true
	}
	if len(seen) != len(want) {
		return errors.New("recursive provider catalog incomplete")
	}
	return nil
}

func scheduledRecursiveToolSSE(suffix, callID, name, arguments string) []byte {
	responseID, itemID := "resp_scheduled_"+suffix, "fc_scheduled_"+suffix
	pending := map[string]any{"id": itemID, "type": "function_call", "status": "in_progress", "arguments": "", "call_id": callID, "name": name}
	done := map[string]any{"id": itemID, "type": "function_call", "status": "completed", "arguments": arguments, "call_id": callID, "name": name}
	return scheduledOpenCodeEvents(
		map[string]any{"type": "response.created", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 0},
		map[string]any{"type": "response.in_progress", "response": scheduledOpenCodeSnapshot(responseID, "in_progress", []any{}, nil), "sequence_number": 1},
		map[string]any{"type": "response.output_item.added", "output_index": 0, "item": pending, "sequence_number": 2},
		map[string]any{"type": "response.function_call_arguments.delta", "delta": arguments, "item_id": itemID, "obfuscation": "fixture", "output_index": 0, "sequence_number": 3},
		map[string]any{"type": "response.function_call_arguments.done", "arguments": arguments, "item_id": itemID, "output_index": 0, "sequence_number": 4},
		map[string]any{"type": "response.output_item.done", "output_index": 0, "item": done, "sequence_number": 5},
		map[string]any{"type": "response.completed", "response": scheduledOpenCodeSnapshot(responseID, "completed", []any{done}, map[string]any{"input_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 1, "output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 2}), "sequence_number": 6},
		map[string]any{"type": "ping", "cost": "0"},
	)
}

func scheduledRecursiveWaitSSE(sequence int, agentID string, after int) []byte {
	callID := fmt.Sprintf("call_scheduled_recursive_wait_%d", sequence)
	arguments, _ := canonical.Bytes(waitAgentArgs{AgentID: agentID, AfterSequence: after, Limit: 16, TimeoutMillis: 5000})
	return scheduledRecursiveToolSSE(fmt.Sprintf("recursive_wait_%d", sequence), callID, "engorch_wait_agent", string(arguments))
}

func scheduledRecursiveCreation(t *testing.T, creation Creation, taskPoolPath string) Creation {
	t.Helper()
	for index := range creation.Config.Provider.Models {
		if creation.Config.Provider.Models[index].Name == "explorer-model" {
			creation.Config.Provider.Models[index].MaxCalls = scheduledRecursiveMaxWaits + 2
		}
	}
	reservation, _, err := creation.Config.Provider.RoleReservation("explorer")
	if err != nil {
		t.Fatal(err)
	}
	plannerTokens := creation.Config.Access.Invocations["planner"].Tokens
	creation.Config.Access.Invocations["explorer"] = config.InvocationLimit{Tokens: reservation}
	creation.Config.Access.Limits = access.Limits{Tokens: plannerTokens + 2*reservation, Concurrency: 2}
	creation.Config.TaskPool = &config.TaskPool{Version: 1, Path: taskPoolPath, Limits: taskpool.Limits{Total: 2}}
	return creation
}

func TestScheduledOpenCodeRecursiveFixturePreflight(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	planner := &scheduledRecursivePlannerProvider{}
	plannerUpstream := httptest.NewUnstartedServer(planner)
	plannerUpstream.EnableHTTP2 = false
	plannerUpstream.StartTLS()
	defer plannerUpstream.Close()
	root := t.TempDir()
	controllerPath, schedulerPath := filepath.Join(root, "controller.jsonl"), filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	creation := scheduledOpenCodeCreation(t, "https://127.0.0.1:1/v1/responses", executable, filepath.Join(root, "opencode-state"))
	creation = scheduledOpenCodeInterruptCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	creation = scheduledRecursiveCreation(t, creation, filepath.Join(root, "task-pool.jsonl"))
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, withProviderTransportClient(ctx, transport), controllerPath, schedulerPath, creation, ScheduledDispatchAdapter{JournalPath: schedulerPath}, "scheduled-opencode-recursive-preflight", "recursive-parent", "Spawn one explorer and wait for its accepted result.", "recursive-parent-nonce")
	state, err := taskscheduler.Inspect(schedulerPath)
	if err != nil || planner.count() != 1 || state.Tasks[prepared.turn.Task.ID].Status != taskscheduler.StatusReady || state.Tasks[prepared.turn.Task.ID].Claim != nil {
		t.Fatal("recursive preflight did not stop before dispatch", err, state.Tasks[prepared.turn.Task.ID])
	}
	if matches, globErr := filepath.Glob(controllerPath + ".explorer.turn-*.opencode-runtime.jsonl"); globErr != nil || len(matches) != 0 {
		t.Fatal("recursive preflight launched OpenCode", matches, globErr)
	}
}

func TestPinnedOpenCodeScheduledRecursiveExplorer(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	provider := &scheduledRecursiveProvider{childAdmitted: make(chan struct{}), childRelease: make(chan struct{}), childQuestion: "Inspect the committed file and return the exact observed source path."}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	planner := &scheduledRecursivePlannerProvider{}
	plannerUpstream := httptest.NewUnstartedServer(planner)
	plannerUpstream.EnableHTTP2 = false
	plannerUpstream.StartTLS()
	defer func() {
		provider.releaseChild()
		cancel()
		upstream.Close()
		plannerUpstream.Close()
	}()

	root := t.TempDir()
	controllerPath, schedulerPath := filepath.Join(root, "controller.jsonl"), filepath.Join(root, "scheduler.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	creation := scheduledOpenCodeCreation(t, upstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state"))
	creation = scheduledOpenCodeInterruptCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	creation = scheduledRecursiveCreation(t, creation, filepath.Join(root, "task-pool.jsonl"))
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	dispatchCtx := withProviderTransportClient(ctx, transport)
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, dispatchCtx, controllerPath, schedulerPath, creation, adapter, "scheduled-opencode-recursive", "recursive-parent", "Spawn one explorer, wait until its accepted result is available, and synthesize that exact evidence.", "recursive-parent-nonce")
	if planner.count() != 1 {
		t.Fatal("recursive planner provider request count changed", planner.count())
	}
	candidateID, err := prepared.snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	childOutput, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: "Recursive child observed the committed base source.", Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	parentOutput, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: "Recursive parent accepted: Recursive child observed the committed base source.", Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	provider.mu.Lock()
	provider.childOutput, provider.parentOutput, provider.expectedCommit = string(childOutput), string(parentOutput), prepared.snapshot.Creation.Repository.Commit
	provider.mu.Unlock()

	pumpCtx, stopPump := context.WithCancel(dispatchCtx)
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- taskscheduler.Pump(pumpCtx, schedulerPath, adapter, taskscheduler.PumpOptions{Workers: 2, PollInterval: 10 * time.Millisecond})
	}()
	pumpJoined := false
	defer func() {
		if pumpJoined {
			return
		}
		provider.releaseChild()
		stopPump()
		select {
		case <-pumpDone:
		case <-time.After(10 * time.Second):
			t.Error("recursive scheduler pump did not stop during cleanup")
		}
	}()
	select {
	case <-provider.childAdmitted:
	case <-time.After(60 * time.Second):
		stopPump()
		t.Fatal("recursive child provider request was not admitted")
	}

	_, child, _, _, parentKey, childKey, catalogOK := provider.snapshot()
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	parentState, childState := scheduled.Tasks[prepared.turn.Task.ID], scheduled.Tasks[child.TaskID]
	childTurn, found := scheduledRecursiveDynamicByID(scheduled, child.TaskID)
	if !found || child.AgentID == "" || child.ParentAgentID != prepared.turn.AgentID || child.TurnID != child.TaskID || parentKey == "" || childKey == "" || parentKey == childKey || !catalogOK || parentState.Claim == nil || childState.Claim == nil || !scheduledRecursiveOpenStatus(parentState.Status) || !scheduledRecursiveOpenStatus(childState.Status) {
		stopPump()
		t.Fatal("recursive parent and child were not concurrently claimed", found, child, parentState, childState, parentKey == childKey, catalogOK)
	}
	controller, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	parentRuntime := scheduledRecursiveRuntimePath(controllerPath, prepared.turn)
	childRuntime := scheduledRecursiveRuntimePath(controllerPath, childTurn)
	if parentRuntime == childRuntime {
		t.Fatal("recursive turns shared a runtime journal")
	}
	for _, item := range []struct {
		turn taskscheduler.DynamicTask
		path string
	}{{prepared.turn, parentRuntime}, {childTurn, childRuntime}} {
		intent := scheduledOpenCodeRuntimeIntent(t, controller, item.turn.Task.InvocationID, item.path)
		verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, item.path, controller, item.turn)
		state, inspectErr := opencoderuntime.InspectComposite(item.path, intent, verify)
		if inspectErr != nil || state.Bound == nil || state.Bound.Version != 2 || state.Bound.Composite == nil || state.Result != nil {
			stopPump()
			t.Fatal("recursive overlapping runtime is not durably bound", item.turn.TurnID, inspectErr, state)
		}
	}

	provider.releaseChild()
	deadline := time.Now().Add(45 * time.Second)
	for {
		scheduled, err = taskscheduler.Inspect(schedulerPath)
		if err == nil && scheduled.Tasks[prepared.turn.Task.ID].Status == taskscheduler.StatusSucceeded && scheduled.Tasks[child.TaskID].Status == taskscheduler.StatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			stopPump()
			t.Fatal("recursive scheduler did not settle", err, scheduledRecursiveFailureDiagnostic(controllerPath, provider, scheduled, prepared.turn, childTurn))
		}
		select {
		case pumpErr := <-pumpDone:
			pumpJoined = true
			t.Fatal("recursive scheduler pump failed before settlement", pumpErr, scheduledRecursiveFailureDiagnostic(controllerPath, provider, scheduled, prepared.turn, childTurn))
		case <-time.After(20 * time.Millisecond):
		}
	}
	stopPump()
	select {
	case pumpErr := <-pumpDone:
		pumpJoined = true
		if !errors.Is(pumpErr, context.Canceled) {
			t.Fatal("recursive scheduler pump returned unexpectedly", pumpErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recursive scheduler pump did not stop")
	}

	requests, child, waits, accepted, _, _, _ := provider.snapshot()
	if waits < 1 || waits > scheduledRecursiveMaxWaits || len(requests) != waits+4 || accepted == nil || accepted.Result.Reference.AgentID != child.AgentID || accepted.Result.Reference.TurnID != child.TurnID || !reflect.DeepEqual(accepted.Exploration, Exploration{CandidateID: candidateID, Summary: "Recursive child observed the committed base source.", Paths: []string{"file.txt"}}) {
		t.Fatal("recursive provider did not receive the exact accepted child body", len(requests), waits, accepted, child)
	}
	controller, err = Inspect(controllerPath)
	if err != nil || len(controller.Explorations) != 2 {
		t.Fatal("recursive controller results unavailable", err, controller.Explorations)
	}
	parentRecord, childRecord := scheduledRecursiveRecords(controller, prepared.turn.Task.InvocationID, childTurn.Task.InvocationID)
	if parentRecord == nil || childRecord == nil || parentRecord.Result.Output != string(parentOutput) || childRecord.Result.Output != string(childOutput) {
		t.Fatal("recursive controller result bodies changed", parentRecord, childRecord)
	}
	controlState, err := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduledRecursiveValidateAcceptedIndexes(controllerPath, controller, controlState, prepared.turn, childTurn, parentRecord, childRecord); err != nil {
		t.Fatal(err)
	}
	controlBytes, err := os.ReadFile(controllerPath + ".agent-control")
	if err != nil || bytes.Contains(controlBytes, []byte("Recursive child observed")) || bytes.Contains(controlBytes, []byte("Recursive parent accepted")) {
		t.Fatal("recursive accepted-result index retained semantic bodies", err)
	}

	requestsBeforeRecovery := len(requests)
	controlEvents, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(controlEvents) == 0 {
		t.Fatal(err)
	}
	controlHead, controlCount := controlEvents[len(controlEvents)-1].Hash, len(controlEvents)
	for _, item := range []struct {
		turn      taskscheduler.DynamicTask
		path      string
		wantInput int64
		wantOut   int64
	}{{prepared.turn, parentRuntime, int64(waits+1) + 18, int64(waits+1) + 3}, {childTurn, childRuntime, 19, 4}} {
		intent := scheduledOpenCodeRuntimeIntent(t, controller, item.turn.Task.InvocationID, item.path)
		verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, item.path, controller, item.turn)
		state, inspectErr := opencoderuntime.InspectComposite(item.path, intent, verify)
		if inspectErr != nil || state.Result == nil || state.Result.Version != 2 || state.Result.CompositeSealReceipt == nil || state.Result.GatewayUsage == nil || state.Result.GatewayUsage.InputTokens != item.wantInput || state.Result.GatewayUsage.OutputTokens != item.wantOut {
			t.Fatal("recursive composite result or usage changed", item.turn.TurnID, inspectErr, state.Result)
		}
		if item.turn.TurnID == childTurn.TurnID {
			broker, brokerErr := contextbroker.Inspect(item.path + ".broker")
			receipts, receiptsErr := toolreceipts.Inspect(item.path + ".tool-receipts")
			if brokerErr != nil || receiptsErr != nil || broker.Calls != 1 || len(broker.Responses) != 1 || !broker.Closed || len(receipts.Calls) != 1 || receipts.Calls[0].Receipt == nil || receipts.Calls[0].Receipt.Owner != toolreceipts.OwnerContext || receipts.Calls[0].Receipt.Tool != "source_read" {
				t.Fatal("recursive child source receipt unavailable", brokerErr, receiptsErr, broker, receipts)
			}
		}
		recovered, recoverErr := opencoderuntime.Execute(context.Background(), opencoderuntime.ExecuteConfig{RuntimePath: item.path, Intent: intent, VerifyComposite: verify})
		if recoverErr != nil || !reflect.DeepEqual(recovered, *state.Result) {
			t.Fatal("recursive runtime recovery changed result", item.turn.TurnID, recoverErr)
		}
		evidence, reconcileErr := adapter.Reconcile(context.Background(), *scheduled.Tasks[item.turn.Task.ID].Claim)
		if reconcileErr != nil || evidence.Status != taskscheduler.StatusSucceeded || evidence.InvocationID != item.turn.Task.InvocationID {
			t.Fatal("recursive scheduler recovery changed evidence", item.turn.TurnID, reconcileErr, evidence)
		}
	}
	afterRequests, _, _, _, _, _, _ := provider.snapshot()
	afterEvents, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(afterRequests) != requestsBeforeRecovery || len(afterEvents) != controlCount || afterEvents[len(afterEvents)-1].Hash != controlHead {
		t.Fatal("recursive offline recovery resent or duplicated accepted results", err, len(afterRequests), requestsBeforeRecovery, len(afterEvents), controlCount)
	}
	t.Logf("OPENCODE_SCHEDULED_RECURSIVE_PASS executable_sha256=%s parent_turn=%s child_turn=%s waits=%d provider_requests=%d", scheduledOpenCodeLiveDigest, prepared.turn.TurnID, child.TurnID, waits, len(requests))
}

func scheduledRecursiveRuntimePath(controllerPath string, turn taskscheduler.DynamicTask) string {
	stem := providerRoleJournalStem("explorer", &taskscheduler.AgentTurnBinding{ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence})
	return controllerPath + "." + stem + ".opencode-runtime.jsonl"
}

type scheduledRecursiveTurnDiagnostic struct {
	TurnID           string
	SchedulerStatus  taskscheduler.Status
	Claimed          bool
	RuntimeEvents    int
	GatewayCalls     int
	GatewayFinished  bool
	ReceiptCalls     int
	ReceiptBound     bool
	RuntimeReadClass string
	GatewayReadClass string
	ReceiptReadClass string
}

type scheduledRecursiveDiagnostic struct {
	ProviderRequests int
	ProviderWaits    int
	ProviderFailure  string
	Turns            []scheduledRecursiveTurnDiagnostic
}

func scheduledRecursiveFailureDiagnostic(controllerPath string, provider *scheduledRecursiveProvider, scheduled taskscheduler.Snapshot, turns ...taskscheduler.DynamicTask) scheduledRecursiveDiagnostic {
	requests, _, waits, _, _, _, _ := provider.snapshot()
	diagnostic := scheduledRecursiveDiagnostic{ProviderRequests: len(requests), ProviderWaits: waits, ProviderFailure: provider.failureClass()}
	for _, turn := range turns {
		path := scheduledRecursiveRuntimePath(controllerPath, turn)
		state := scheduled.Tasks[turn.Task.ID]
		item := scheduledRecursiveTurnDiagnostic{TurnID: turn.TurnID, SchedulerStatus: state.Status, Claimed: state.Claim != nil}
		runtimeEvents, runtimeErr := journal.Read(path)
		if runtimeErr != nil {
			item.RuntimeReadClass = "unavailable"
		} else {
			item.RuntimeEvents = len(runtimeEvents)
		}
		gatewayPath := strings.TrimSuffix(path, ".opencode-runtime.jsonl") + ".provider-gateway.jsonl"
		gateway, gatewayErr := providergateway.Inspect(gatewayPath)
		if gatewayErr != nil {
			item.GatewayReadClass = "unavailable"
		} else {
			item.GatewayCalls, item.GatewayFinished = len(gateway.Calls), gateway.Finished
		}
		receipts, receiptsErr := toolreceipts.Inspect(path + ".tool-receipts")
		if receiptsErr != nil {
			item.ReceiptReadClass = "unavailable"
		} else {
			item.ReceiptCalls, item.ReceiptBound = len(receipts.Calls), receipts.Binding != nil
		}
		diagnostic.Turns = append(diagnostic.Turns, item)
	}
	return diagnostic
}

func scheduledRecursiveDynamicByID(snapshot taskscheduler.Snapshot, taskID string) (taskscheduler.DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.Task.ID == taskID {
			return dynamic, true
		}
	}
	return taskscheduler.DynamicTask{}, false
}

func scheduledRecursiveOpenStatus(status taskscheduler.Status) bool {
	return status == taskscheduler.StatusClaimed || status == taskscheduler.StatusRunning
}

func scheduledRecursiveRecords(snapshot Snapshot, parentInvocation, childInvocation string) (*ExplorerRecord, *ExplorerRecord) {
	var parent, child *ExplorerRecord
	for index := range snapshot.Explorations {
		record := &snapshot.Explorations[index]
		switch record.Invocation.ID {
		case parentInvocation:
			parent = record
		case childInvocation:
			child = record
		}
	}
	return parent, child
}

func scheduledRecursiveValidateAcceptedIndexes(controllerPath string, controller Snapshot, state agentcontrol.Snapshot, parentTurn, childTurn taskscheduler.DynamicTask, parentRecord, childRecord *ExplorerRecord) error {
	head, err := controllerJournalHead(controllerPath)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		turn   taskscheduler.DynamicTask
		record *ExplorerRecord
	}{{parentTurn, parentRecord}, {childTurn, childRecord}} {
		binding := &taskscheduler.AgentTurnBinding{ParentAgentID: item.turn.ParentAgentID, AgentID: item.turn.AgentID, TurnID: item.turn.TurnID, TurnSequence: item.turn.TurnSequence}
		reference, found, referenceErr := acceptedExplorerResultReference(controllerPath, head, controller, item.record.Invocation, binding, item.record)
		if referenceErr != nil || !found {
			return errors.Join(errors.New("recursive accepted reference unavailable"), referenceErr)
		}
		id, idErr := reference.ID()
		if idErr != nil {
			return idErr
		}
		results, audiences := 0, map[string]int{}
		for _, result := range state.Results {
			if result.ResultID == id && result.Reference == reference {
				results++
			}
		}
		for _, activity := range state.Activities {
			if activity.Kind == "result" && activity.ResultID == id && activity.TurnID == item.turn.TurnID && activity.ResultSHA256 == reference.ResultSHA256 {
				audiences[activity.AgentID]++
			}
		}
		if results != 1 || audiences[item.turn.ParentAgentID] != 1 || audiences[item.turn.AgentID] != 1 || len(audiences) != 2 {
			return errors.New("recursive accepted-result index audiences changed")
		}
	}
	return nil
}
