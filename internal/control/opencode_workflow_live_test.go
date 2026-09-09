package control

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/worktree"
)

type scheduledWorkflowRole struct {
	name          string
	candidateID   string
	expectedBytes string
	result        string
}

type scheduledWorkflowProvider struct {
	mu       sync.Mutex
	role     scheduledWorkflowRole
	phase    int
	requests []json.RawMessage
	failure  string
	keys     map[string]string
}

func (p *scheduledWorkflowProvider) setRole(role scheduledWorkflowRole) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.phase != 0 || role.name == "" || role.candidateID == "" || role.result == "" {
		return errors.New("workflow provider role transition invalid")
	}
	p.role = role
	return nil
}

func (p *scheduledWorkflowProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || !json.Valid(body) || request.Method != http.MethodPost || request.URL.RequestURI() != "/v1/responses" || request.Header.Get("authorization") != "Bearer "+scheduledOpenCodeLiveSecret {
		p.fail("invalid-request")
		http.Error(writer, "invalid workflow fixture request", http.StatusBadRequest)
		return
	}
	var envelope scheduledRecursiveRequest
	if json.Unmarshal(body, &envelope) != nil || envelope.PromptCacheKey == "" {
		p.fail("invalid-envelope")
		http.Error(writer, "invalid workflow fixture envelope", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.requests = append(p.requests, append(json.RawMessage(nil), body...))
	response, responseErr := p.responseLocked(body, envelope)
	if responseErr != nil && p.failure == "" {
		p.failure = responseErr.Error()
	}
	p.mu.Unlock()
	if responseErr != nil {
		http.Error(writer, responseErr.Error(), http.StatusConflict)
		return
	}
	writer.Header().Set("content-type", "text/event-stream; charset=utf-8")
	writer.Header().Set("cache-control", "no-store")
	_, _ = writer.Write(response)
}

func (p *scheduledWorkflowProvider) responseLocked(body []byte, envelope scheduledRecursiveRequest) ([]byte, error) {
	role := p.role
	if role.name == "" {
		return nil, errors.New("workflow role unavailable")
	}
	outputs := make([]scheduledRecursiveFunctionOutput, 0, len(envelope.Input))
	for _, item := range envelope.Input {
		if item.Type == "function_call_output" {
			outputs = append(outputs, item)
		}
	}
	callID := "call_workflow_" + role.name + "_candidate_read"
	if p.phase == 0 {
		if len(outputs) != 0 || !bytes.Contains(body, []byte(`"name":"engorch_candidate_read"`)) {
			return nil, errors.New("workflow candidate_read catalog unavailable")
		}
		if p.keys == nil {
			p.keys = map[string]string{}
		}
		if prior := p.keys[role.name]; prior != "" && prior != envelope.PromptCacheKey {
			return nil, errors.New("workflow role cache identity changed")
		}
		p.keys[role.name] = envelope.PromptCacheKey
		p.phase = 1
		return scheduledRecursiveToolSSE("workflow_"+role.name+"_read", callID, "engorch_candidate_read", `{"path":"workflow.go","offset":0,"limit":4096}`), nil
	}
	if p.phase != 1 || len(outputs) == 0 || outputs[len(outputs)-1].CallID != callID {
		return nil, errors.New("workflow candidate_read result unavailable")
	}
	var receipt contextbroker.Response
	if canonical.Decode([]byte(outputs[len(outputs)-1].Output), &receipt) != nil || !receipt.Success || receipt.InvocationID == "" {
		return nil, errors.New("workflow candidate_read receipt invalid")
	}
	var chunk worktree.SourceChunk
	if canonical.Decode(receipt.Content, &chunk) != nil || chunk.CandidateID != role.candidateID || chunk.Path != "workflow.go" {
		return nil, errors.New("workflow candidate_read identity changed")
	}
	content, decodeErr := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if decodeErr != nil || string(content) != role.expectedBytes {
		return nil, errors.New("workflow candidate_read content changed")
	}
	p.phase = 0
	p.role = scheduledWorkflowRole{}
	return scheduledOpenCodeTextSSE(len(p.requests)+100, role.result), nil
}

func (p *scheduledWorkflowProvider) fail(class string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == "" {
		p.failure = class
	}
}

func (p *scheduledWorkflowProvider) snapshot() ([]json.RawMessage, string, map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	requests := make([]json.RawMessage, len(p.requests))
	for index := range p.requests {
		requests[index] = append(json.RawMessage(nil), p.requests[index]...)
	}
	keys := map[string]string{}
	for role, key := range p.keys {
		keys[role] = key
	}
	return requests, p.failure, keys
}

func TestPinnedOpenCodeWriterRepairReviewCommitWorkflow(t *testing.T) {
	if os.Getenv("ENGORCH_OPENCODE_EXECUTE_LIVE") != "1" {
		t.Skip("explicit pinned OpenCode Execute probe opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	provider := &scheduledWorkflowProvider{}
	upstream := httptest.NewUnstartedServer(provider)
	upstream.EnableHTTP2 = false
	upstream.StartTLS()
	defer func() {
		cancel()
		upstream.Close()
	}()

	root := t.TempDir()
	controllerPath := filepath.Join(root, "controller.jsonl")
	executable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "opencode-1.18.29", "opencode.exe")
	goExecutable := filepath.Join(scheduledOpenCodeRepositoryRoot(t), ".local", "toolchains", "go", "bin", "go.exe")
	creation := scheduledOpenCodeCreation(t, upstream.URL+"/v1/responses", executable, filepath.Join(root, "opencode-state"))
	creation = scheduledWorkflowCreation(t, creation, goExecutable)
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	workflowCtx := withProviderTransportClient(ctx, transport)
	if err := os.MkdirAll(creation.Config.OpenCode.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(workflowCtx, controllerPath)
	if err != nil || planned.Plan == nil {
		t.Fatal("workflow planning failed", err)
	}
	if err := Append(controllerPath, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "fixture-operator"}); err != nil {
		t.Fatal(err)
	}
	state, err := StartWorkspace(workflowCtx, controllerPath)
	if err != nil || state.Candidate == nil || state.Workspace == nil {
		t.Fatal("workflow workspace unavailable", err)
	}

	const baseSource = "package fixture\n\nfunc Value() string { return \"base\" }\n"
	const wrongSource = "package fixture\n\nfunc Value() string { return \"wrong\" }\n"
	const fixedSource = "package fixture\n\nfunc Value() string { return \"fixed\" }\n"
	initialID, err := state.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	writerOutput, err := canonical.Bytes(WriterProposal{CandidateID: initialID, Changes: []fileeffects.Change{{Path: "workflow.go", BeforeHash: digestText(baseSource), ContentBase64: content64(wrongSource), Executable: false}}})
	if err != nil || provider.setRole(scheduledWorkflowRole{name: "writer", candidateID: initialID, expectedBytes: baseSource, result: string(writerOutput)}) != nil {
		t.Fatal("writer fixture output unavailable", err)
	}
	writerRecord, err := RunWriter(workflowCtx, controllerPath)
	if err != nil || writerRecord.Invocation.Profile.Role != "writer" {
		t.Fatal("actual OpenCode writer failed", err)
	}
	if content, readErr := os.ReadFile(filepath.Join(state.Workspace.Request.Path, "workflow.go")); readErr != nil || string(content) != baseSource {
		t.Fatal("writer proposal mutated before authorization", readErr)
	}
	fileIntentID, err := writerRecord.Prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(workflowCtx, controllerPath, writerRecord.Prepared, effects.Authorization{}); err == nil {
		t.Fatal("writer files applied without exact authorization")
	}
	state, err = ApplyFiles(workflowCtx, controllerPath, writerRecord.Prepared, effects.Authorization{IntentID: fileIntentID, Actor: "fixture-operator"})
	if err != nil || state.FileOutcome != "CONFIRMED" {
		t.Fatal("authorized writer files failed", err)
	}
	state, err = Verify(workflowCtx, controllerPath)
	if err != nil || state.State != "REPAIRING" || state.Verification == nil || len(state.Verification.Observations) != 1 || state.Verification.Observations[0].Result.Status != "FAIL" {
		t.Fatal("controlled verification failure was not retained", err, state.State)
	}

	wrongID, err := state.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	fixerOutput, err := canonical.Bytes(WriterProposal{CandidateID: wrongID, Changes: []fileeffects.Change{{Path: "workflow.go", BeforeHash: digestText(wrongSource), ContentBase64: content64(fixedSource), Executable: false}}})
	if err != nil || provider.setRole(scheduledWorkflowRole{name: "fixer", candidateID: wrongID, expectedBytes: wrongSource, result: string(fixerOutput)}) != nil {
		t.Fatal("fixer fixture output unavailable", err)
	}
	wantFailure, err := writerVerificationContext(state)
	if err != nil {
		t.Fatal(err)
	}
	fixerRecord, err := RunWriter(workflowCtx, controllerPath)
	var fixerInput struct {
		CandidateID  string              `json:"candidate_id"`
		Verification *writerVerification `json:"verification"`
	}
	decodeFixerErr := scheduledWorkflowDecodeProjection(fixerRecord.Invocation.Input, &fixerInput)
	if err != nil || decodeFixerErr != nil || fixerRecord.Invocation.Profile.Role != "fixer" || fixerRecord.Invocation.ID == writerRecord.Invocation.ID || fixerInput.CandidateID != wrongID || !reflect.DeepEqual(fixerInput.Verification, wantFailure) || len(fixerInput.Verification.Observations) != 1 || fixerInput.Verification.Observations[0].Index != 0 || fixerInput.Verification.Observations[0].Result.Status != "FAIL" {
		t.Fatal("actual OpenCode fixer lacked exact verification failure", err, decodeFixerErr)
	}
	fixIntentID, err := fixerRecord.Prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(workflowCtx, controllerPath, fixerRecord.Prepared, effects.Authorization{}); err == nil {
		t.Fatal("fixer files applied without exact authorization")
	}
	state, err = ApplyFiles(workflowCtx, controllerPath, fixerRecord.Prepared, effects.Authorization{IntentID: fixIntentID, Actor: "fixture-operator"})
	if err != nil || state.FileOutcome != "CONFIRMED" {
		t.Fatal("authorized fixer files failed", err)
	}
	state, err = Verify(workflowCtx, controllerPath)
	if err != nil || state.State != "REVIEWING" || state.Verification == nil || len(state.Verification.Observations) != 1 || state.Verification.Observations[0].Result.Status != "PASS" {
		t.Fatal("repaired candidate did not pass fresh verification", err, state.State)
	}

	readyID, err := state.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	reviewOutput, err := canonical.Bytes(ReviewVerdict{CandidateID: readyID, VerificationPlanID: state.Verification.PlanID, Decision: "approve", Findings: []ReviewFinding{}})
	if err != nil || provider.setRole(scheduledWorkflowRole{name: "reviewer", candidateID: readyID, expectedBytes: fixedSource, result: string(reviewOutput)}) != nil {
		t.Fatal("reviewer fixture output unavailable", err)
	}
	beforeReview := *state.Candidate
	wantReviewVerification, err := writerVerificationContext(state)
	if err != nil {
		t.Fatal(err)
	}
	reviewRecord, err := RunReview(workflowCtx, controllerPath)
	var reviewerInput struct {
		CandidateID        string              `json:"candidate_id"`
		VerificationPlanID string              `json:"verification_plan_id"`
		Verification       *writerVerification `json:"verification"`
	}
	decodeReviewerErr := scheduledWorkflowDecodeProjection(reviewRecord.Invocation.Input, &reviewerInput)
	if err != nil || decodeReviewerErr != nil || reviewRecord.Invocation.Profile.Role != "reviewer" || reviewerInput.CandidateID != readyID || reviewerInput.VerificationPlanID != state.Verification.PlanID || !reflect.DeepEqual(reviewerInput.Verification, wantReviewVerification) || len(reviewerInput.Verification.Observations) != 1 || reviewerInput.Verification.Observations[0].Result.Status != "PASS" {
		t.Fatal("actual OpenCode reviewer failed", err, decodeReviewerErr)
	}
	state, err = Inspect(controllerPath)
	if err != nil || state.State != "READY" || state.Review == nil || state.Candidate == nil || *state.Candidate != beforeReview {
		t.Fatal("review did not admit unchanged candidate", err, state.State)
	}

	journalBeforeCommit, err := journal.Read(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
	preparedCommit, err := PrepareCommit(workflowCtx, controllerPath, identity, identity, "workflow fixture\n")
	if err != nil {
		t.Fatal(err)
	}
	journalAfterPrepare, err := journal.Read(controllerPath)
	if err != nil || len(journalAfterPrepare) != len(journalBeforeCommit) {
		t.Fatal("commit preparation performed an effect", err)
	}
	commitIntentID, err := preparedCommit.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteCommit(workflowCtx, controllerPath, preparedCommit, effects.Authorization{}); err == nil {
		t.Fatal("local commit executed without exact authorization")
	}
	state, err = ExecuteCommit(workflowCtx, controllerPath, preparedCommit, effects.Authorization{IntentID: commitIntentID, Actor: "fixture-operator"})
	if err != nil || state.State != "COMMITTED" || state.Commit == nil || state.Commit.Outcome != "CONFIRMED" || state.Commit.Observation == nil || state.Commit.Observation.Observation.Candidate == nil {
		t.Fatal("authorized local commit failed", err, state.State)
	}
	commitID, treeID := state.Commit.Intent.CommitID, state.Commit.Intent.TreeID
	if output, gitErr := exec.Command("git", "-C", state.Workspace.Request.Path, "show", "-s", "--format=%T", commitID).Output(); gitErr != nil || strings.TrimSpace(string(output)) != treeID {
		t.Fatal("local commit tree differs from admitted candidate", gitErr)
	}
	if source, readErr := os.ReadFile(filepath.Join(creation.Repository.Root, "workflow.go")); readErr != nil || string(source) != baseSource {
		t.Fatal("workflow changed canonical source", readErr)
	}

	requests, failure, keys := provider.snapshot()
	if failure != "" || len(requests) != 6 || len(keys) != 3 || keys["writer"] == "" || keys["fixer"] == "" || keys["reviewer"] == "" || keys["writer"] == keys["fixer"] || keys["writer"] == keys["reviewer"] || keys["fixer"] == keys["reviewer"] {
		t.Fatal("workflow provider identity or request count changed", failure, len(requests), keys)
	}
	beforeOfflineEvents, err := journal.Read(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"writer", "fixer", "reviewer"} {
		runtimePath := controllerPath + "." + role + ".opencode-runtime.jsonl"
		intent, intentErr := scheduledWorkflowRuntimeIntent(runtimePath)
		runtimeState, inspectErr := opencoderuntime.Inspect(runtimePath, intent)
		if intentErr != nil || inspectErr != nil || runtimeState.Result == nil || runtimeState.Result.GatewayUsage == nil || runtimeState.Result.GatewayUsage.InputTokens != 19 || runtimeState.Result.GatewayUsage.OutputTokens != 4 {
			t.Fatal("workflow runtime seal or usage unavailable", role, intentErr, inspectErr)
		}
		recovered, recoverErr := opencoderuntime.Execute(context.Background(), opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, Intent: intent})
		if recoverErr != nil || !reflect.DeepEqual(recovered, *runtimeState.Result) {
			t.Fatal("workflow runtime recovery changed result", role, recoverErr)
		}
		gateway, gatewayErr := providergateway.Inspect(controllerPath + "." + role + ".provider-gateway.jsonl")
		broker, brokerErr := contextbroker.Inspect(runtimePath + ".broker")
		if gatewayErr != nil || brokerErr != nil || len(gateway.Calls) != 2 || !gateway.Finished || gateway.Exhausted || broker.Calls != 1 || len(broker.Responses) != 1 || !broker.Closed {
			t.Fatal("workflow subordinate journals differ", role, gatewayErr, brokerErr, gateway, broker)
		}
	}
	requestsBeforeRetry := len(requests)
	if _, retryErr := RunWriter(context.Background(), controllerPath); retryErr == nil {
		t.Fatal("committed workflow re-entered writer")
	}
	if _, retryErr := RunReview(context.Background(), controllerPath); retryErr == nil {
		t.Fatal("committed workflow re-entered reviewer")
	}
	if _, retryErr := ExecuteCommit(context.Background(), controllerPath, preparedCommit, effects.Authorization{IntentID: commitIntentID, Actor: "fixture-operator"}); retryErr == nil {
		t.Fatal("committed workflow repeated commit")
	}
	afterRequests, _, _ := provider.snapshot()
	afterOfflineEvents, err := journal.Read(controllerPath)
	if err != nil || len(afterRequests) != requestsBeforeRetry || len(afterOfflineEvents) != len(beforeOfflineEvents) || afterOfflineEvents[len(afterOfflineEvents)-1].Hash != beforeOfflineEvents[len(beforeOfflineEvents)-1].Hash {
		t.Fatal("workflow offline recovery resent or duplicated effects", err, len(afterRequests), len(afterOfflineEvents))
	}
	t.Logf("OPENCODE_WORKFLOW_PASS executable_sha256=%s provider_requests=%d commit=%s", scheduledOpenCodeLiveDigest, len(requests), commitID)
}

// The assertions inspect selected fields of the full production input contract.
// Validate the complete JSON first, then decode that projection without rejecting
// unrelated instruction, plan and repository-context fields.
func scheduledWorkflowDecodeProjection(input string, target any) error {
	normal, err := canonical.Normalize([]byte(input))
	if err != nil {
		return err
	}
	return json.Unmarshal(normal, target)
}

func scheduledWorkflowCreation(t *testing.T, creation Creation, goExecutable string) Creation {
	t.Helper()
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "engorch-responses-fixture", Model: scheduledOpenCodeLiveModel, Effort: "high"}
	writer, fixer, reviewer := profile, profile, profile
	writer.Role, fixer.Role, reviewer.Role = "writer", "fixer", "reviewer"
	creation.Config.Writer, creation.Config.Fixer, creation.Config.Reviewer = &writer, &fixer, &reviewer
	role := creation.Config.Provider.Roles["explorer"]
	creation.Config.Provider.Roles["writer"], creation.Config.Provider.Roles["fixer"], creation.Config.Provider.Roles["reviewer"] = role, role, role
	creation.Config.Access.Roles["writer"], creation.Config.Access.Roles["fixer"], creation.Config.Access.Roles["reviewer"] = "fixture-provider", "fixture-provider", "fixture-provider"
	reservation, _, err := creation.Config.Provider.RoleReservation("writer")
	if err != nil {
		t.Fatal(err)
	}
	creation.Config.Access.Invocations["writer"] = config.InvocationLimit{Tokens: reservation}
	creation.Config.Access.Invocations["fixer"] = config.InvocationLimit{Tokens: reservation}
	creation.Config.Access.Invocations["reviewer"] = config.InvocationLimit{Tokens: reservation}
	creation.Config.Access.Limits = access.Limits{Tokens: creation.Config.Access.Invocations["planner"].Tokens + 3*reservation, Concurrency: 1}
	creation.Config.Verification = []config.Check{{Name: "intentional-workflow-check", Argv: []string{goExecutable, "test", "-p", "1", "./..."}, TimeoutSeconds: 60}}
	root := creation.Repository.Root
	for path, content := range map[string]string{
		"go.mod":           "module fixture\n\ngo 1.22\n",
		"workflow.go":      "package fixture\n\nfunc Value() string { return \"base\" }\n",
		"workflow_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestValue(t *testing.T) { if Value() != \"fixed\" { t.Fatal(Value()) } }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("git", "-C", root, "add", "go.mod", "workflow.go", "workflow_test.go")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	command = exec.Command("git", "-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "workflow base")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	creation.Repository, err = repository.Discover(context.Background(), root, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	return creation
}

func scheduledWorkflowRuntimeIntent(path string) (opencoderuntime.Intent, error) {
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 || events[0].Kind != "opencode-runtime.intent" {
		return opencoderuntime.Intent{}, errors.Join(errors.New("workflow runtime intent unavailable"), err)
	}
	var intent opencoderuntime.Intent
	if err := canonical.Decode(events[0].Payload, &intent); err != nil {
		return opencoderuntime.Intent{}, err
	}
	return intent, nil
}
