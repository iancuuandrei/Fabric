package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/contextmcp"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/toolbridge"
	"github.com/iancuuandrei/fabric/internal/toolreceipts"
)

func TestSummarizeOpenCodeGatewayValidCompletedTotals(t *testing.T) {
	cached := int64(289966)
	reasoning := int64(6037)
	aggregate := providergateway.Usage{InputTokens: 386751, OutputTokens: 8470, ReasoningTokens: &reasoning, CacheReadTokens: &cached}
	totals, err := summarizeOpenCodeGatewayUsage(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if totals.InputTokens != 386751 || totals.OutputTokens != 8470 {
		t.Fatal("totals changed", totals)
	}
	if totals.CachedInputTokens == nil || *totals.CachedInputTokens != 289966 {
		t.Fatal("cached total missing", totals.CachedInputTokens)
	}
	if totals.UncachedInputTokens == nil || *totals.UncachedInputTokens != 96785 {
		t.Fatal("uncached subtraction changed", totals.UncachedInputTokens)
	}
	if totals.ReasoningTokens == nil || *totals.ReasoningTokens != 6037 {
		t.Fatal("reasoning missing")
	}
	if totals.OrdinaryOutputTokens == nil || *totals.OrdinaryOutputTokens != 2433 {
		t.Fatal("ordinary subtraction changed", totals.OrdinaryOutputTokens)
	}
	if totals.CacheWriteTokens != nil {
		t.Fatal("unknown cache write must remain null, not zero")
	}
}

func TestSummarizeOpenCodeGatewayUnknownAxesNullNotZero(t *testing.T) {
	totals, err := summarizeOpenCodeGatewayUsage(providergateway.Usage{InputTokens: 10, OutputTokens: 4})
	if err != nil {
		t.Fatal(err)
	}
	if totals.CachedInputTokens != nil || totals.UncachedInputTokens != nil || totals.ReasoningTokens != nil || totals.OrdinaryOutputTokens != nil || totals.CacheWriteTokens != nil {
		t.Fatal("unknown axes must remain null, not zero", totals)
	}
	raw, err := canonical.Bytes(totals)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"cached_input_tokens", "uncached_input_tokens", "reasoning_tokens", "ordinary_output_tokens", "cache_write_tokens"} {
		value, ok := decoded[field]
		if !ok {
			t.Fatalf("optional axis %s omitted; must be null", field)
		}
		if value != nil {
			t.Fatalf("optional axis %s must be null, not zero: %v", field, value)
		}
	}
}

func TestSummarizeOpenCodeGatewayRejectsSubsetViolation(t *testing.T) {
	cached := int64(11)
	if _, err := summarizeOpenCodeGatewayUsage(providergateway.Usage{InputTokens: 10, OutputTokens: 4, CacheReadTokens: &cached}); err == nil {
		t.Fatal("cached larger than input accepted")
	}
	reasoning := int64(5)
	if _, err := summarizeOpenCodeGatewayUsage(providergateway.Usage{InputTokens: 10, OutputTokens: 4, ReasoningTokens: &reasoning}); err == nil {
		t.Fatal("reasoning larger than output accepted")
	}
	badWrite := int64(7)
	if _, err := summarizeOpenCodeGatewayUsage(providergateway.Usage{InputTokens: 3, OutputTokens: 30, CacheWriteTokens: &badWrite}); err == nil {
		t.Fatal("cache write larger than input accepted")
	}
}

func writeOfflineGatewayJournal(t *testing.T, path string, input, output int64, cached, reasoning *int64) providergateway.State {
	t.Helper()
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: json.RawMessage(`{}`), ContextWindowTokens: 1 << 20, MaxCalls: 4, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 64}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 1 << 20, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(path, "provider.bound", binding, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	intent := providergateway.CallIntent{Version: 1, Sequence: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: strings.Repeat("d", 64), RequestBytes: 128, MaxOutputTokens: 16, RequestExpectationID: strings.Repeat("e", 64)}
	intent.CallID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(path, "provider.call-intent", intent, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	usage := providergateway.Usage{InputTokens: input, OutputTokens: output, ReasoningTokens: reasoning, CacheReadTokens: cached}
	receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat("0", 64), ResponseBytes: 256, ResponseID: "response-fixture", ObservedModel: model.Model, Finish: "stop", StreamComplete: true, UsageComplete: true, Usage: usage, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: []providergateway.ResponseToolIdentity{}, OutputTextSHA256: strings.Repeat("f", 64)}, RequestExpectationID: intent.RequestExpectationID}
	if _, err := journal.Append(path, "provider.call-receipt", receipt, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	state, err := providergateway.Inspect(path)
	if err != nil || !state.Finished || state.Pending != nil || len(state.Calls) != 1 {
		t.Fatal("offline gateway fixture did not replay to finished", state, err)
	}
	return state
}

func TestOfflineGatewayAggregateProjectsCanonicalUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.jsonl")
	cached := int64(6)
	reasoning := int64(2)
	state := writeOfflineGatewayJournal(t, path, 10, 4, &cached, &reasoning)
	totals, err := summarizeOpenCodeGatewayUsage(state.Aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if totals.InputTokens != 10 || totals.OutputTokens != 4 || *totals.UncachedInputTokens != 4 || *totals.OrdinaryOutputTokens != 2 {
		t.Fatal("canonical aggregate projection changed", totals)
	}
	calls, receipts, pending := openCodeGatewayCounts(state)
	if calls != 1 || receipts != 1 || pending != 0 {
		t.Fatal("call/receipt/pending counts changed", calls, receipts, pending)
	}
}

func TestMeasureRunUsageRejectsKnownReceiptWithMissingJournal(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "opencode-missing", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	model, provider := invocation.Profile.Model, invocation.Profile.Provider
	effort := invocation.Profile.Effort
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "Inspect the repository, implement the bounded change, and run the configured check."}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	resultHash, err := canonical.Hash("harness.planner-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: invocation.ID, AccessInvocationID: routing.Intent.Reservation.InvocationID, RuntimeJournalHead: strings.Repeat("c", 64), GatewayJournalHead: strings.Repeat("d", 64), ResultHash: resultHash, ObservedModel: model, ObservedProvider: provider, Result: result}
	if err := Append(controllerPath, "planning.provider-observed", receipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = MeasureRunUsage(controllerPath)
	if err == nil || !strings.Contains(err.Error(), "opencode runtime journal missing") {
		t.Fatalf("missing admitted journal must reject, got %v", err)
	}
	if strings.Contains(err.Error(), controllerPath) || strings.Contains(err.Error(), invocation.Input) {
		t.Fatal("diagnostic leaked path or prompt")
	}
	afterBytes, readErr := os.ReadFile(controllerPath)
	if readErr != nil || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("read-only usage mutated the controller journal")
	}
}

func TestMeasureRunUsageRejectsForgedResultHash(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "opencode-forged", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	model, provider := invocation.Profile.Model, invocation.Profile.Provider
	effort := invocation.Profile.Effort
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "Inspect the repository, implement the bounded change, and run the configured check."}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	forged := result
	forged.Output = "substituted output"
	resultHash, err := canonical.Hash("harness.planner-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: invocation.ID, AccessInvocationID: routing.Intent.Reservation.InvocationID, RuntimeJournalHead: strings.Repeat("c", 64), GatewayJournalHead: strings.Repeat("d", 64), ResultHash: resultHash, ObservedModel: model, ObservedProvider: provider, Result: forged}
	if err := Append(controllerPath, "planning.provider-observed", receipt); err == nil {
		t.Fatal("forged provider result was admitted")
	}
}

func TestOpenCodePendingRemainsUnknownWithoutInference(t *testing.T) {
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "Implement the bounded change.")
	if err != nil {
		t.Fatal(err)
	}
	admission := AgentDispatchAdmission{Version: 1, RunID: strings.Repeat("a", 64), TreeID: strings.Repeat("a", 64), Invocation: invocation}
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	snapshot := Snapshot{RunID: admission.RunID, ProviderRuntime: map[string]providerDispatchReceipt{}, AgentDispatch: map[string]AgentDispatchState{}}
	entry, ok := pendingOpenCodeEntry(controllerPath, snapshot, admission)
	if !ok {
		t.Fatal("opencode writer admission must be reported")
	}
	if entry.Status != "UNKNOWN" || entry.ReceiptMatched || entry.Usage != nil {
		t.Fatal("pending work must remain explicitly unmatched with no totals", entry)
	}
	if entry.GatewayCalls != nil || entry.GatewayReceipts != nil || entry.GatewayPending != nil {
		t.Fatal("unproven gateway counts must be omitted, not zero")
	}
}

func TestOpenCodeStemPreservesScheduledAndRepeatedIdentities(t *testing.T) {
	if got := providerRoleJournalStem("writer", nil); got != "writer" {
		t.Fatal("first-turn stem changed", got)
	}
	turn := &taskscheduler.AgentTurnBinding{ParentAgentID: strings.Repeat("a", 64), AgentID: strings.Repeat("b", 64), TurnID: "turn-abc", TurnSequence: 2}
	if got := providerRoleJournalStem("explorer", turn); got != "explorer.turn-turn-abc" {
		t.Fatal("scheduled stem changed", got)
	}
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "Implement the bounded change.")
	if err != nil {
		t.Fatal(err)
	}
	stem, err := providerInvocationJournalStem(controllerPath, invocation, nil)
	if err != nil || stem != "writer" {
		t.Fatal("missing first-turn journal must resolve to the historical stem", stem, err)
	}
	if _, err := journal.Append(controllerPath+".writer.opencode-runtime.jsonl", "partial", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := providerInvocationJournalStem(controllerPath, invocation, nil); err == nil {
		t.Fatal("corrupt first-turn intent must reject")
	}
}

func TestRunUsageLegacySerializationOmitsOpenCodeWhenAbsent(t *testing.T) {
	report := RunUsage{RunID: strings.Repeat("a", 64), ControllerHead: strings.Repeat("b", 64), Scope: "journaled_codex_hosts", Invocations: []RuntimeUsageEntry{}}
	raw, err := canonical.Bytes(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("open_code")) {
		t.Fatal("Codex-only report must omit the additive OpenCode field")
	}
	report.OpenCodeInvocations = []OpenCodeUsageEntry{{InvocationID: strings.Repeat("c", 64), Role: "writer", JournalPresent: true, ReceiptMatched: true, Status: "completed"}}
	raw, err = canonical.Bytes(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("open_code_invocations")) {
		t.Fatal("completed OpenCode report must expose the additive field")
	}
}

func TestRunUsageScopePreservedWithOpenCode(t *testing.T) {
	report := RunUsage{RunID: strings.Repeat("a", 64), ControllerHead: strings.Repeat("b", 64), Scope: "controller_admissions_and_journaled_codex_hosts", Invocations: []RuntimeUsageEntry{}}
	report.OpenCodeInvocations = []OpenCodeUsageEntry{{InvocationID: strings.Repeat("c", 64), Role: "planner", JournalPresent: true, ReceiptMatched: true, Status: "completed"}}
	if report.Scope != "controller_admissions_and_journaled_codex_hosts" {
		t.Fatal("Scope must remain exactly the Codex scope with additive OpenCode field", report.Scope)
	}
}

func TestEvidenceFeedbackAdmitsOpenCodeTotals(t *testing.T) {
	codexID, openCodeID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	entry := tokenFeedbackEntry()
	entry.InvocationID = codexID
	usage := RunUsage{Invocations: []RuntimeUsageEntry{entry}}
	cached, reasoning := int64(16), int64(4)
	uncached, ordinary := int64(32), int64(8)
	calls, receipts, pending := 2, 2, 0
	usage.OpenCodeInvocations = []OpenCodeUsageEntry{{
		InvocationID: openCodeID, Role: "writer", JournalPresent: true, ReceiptMatched: true, Status: "completed",
		Usage:           &OpenCodeTokenUsage{InputTokens: 48, CachedInputTokens: &cached, UncachedInputTokens: &uncached, OutputTokens: 12, ReasoningTokens: &reasoning, OrdinaryOutputTokens: &ordinary},
		GatewayCalls:    &calls,
		GatewayReceipts: &receipts,
		GatewayPending:  &pending,
	}}
	usage.controllerOrder = []string{codexID, openCodeID}
	observations, unavailable, err := evidenceTokenObservations(usage)
	if err != nil || len(unavailable) != 0 || len(observations) != 2 {
		t.Fatal("completed OpenCode totals must contribute alongside Codex", err, unavailable)
	}
	if observations[0].InvocationID != codexID || observations[1].InvocationID != openCodeID {
		t.Fatal("mixed-route observations must follow controller order", observations[0].InvocationID, observations[1].InvocationID)
	}
	values := map[string]string{}
	for name, cost := range observations[1].Costs {
		if cost != nil {
			values[name] = string(*cost)
		}
	}
	want := map[string]string{"uncached_input_tokens": "32", "cached_input_tokens": "16", "ordinary_output_tokens": "8", "reasoning_output_tokens": "4"}
	if !reflect.DeepEqual(values, want) {
		t.Fatal("OpenCode token subsets mispriced", values)
	}
}

// Positive full-chain offline fixture: planner legacy base without
// AgentDispatch, with sealed runtime and matched gateway, exercised through
// MeasureRunUsage. No live provider, no subprocess beyond in-process broker.
type plannerPositiveFixture struct {
	controllerPath string
	invocation     runtime.Invocation
	result         runtime.Result
	receipt        providerDispatchReceipt
	inputTokens    int64
	cachedTokens   int64
	outputTokens   int64
	reasoning      int64
}

func plannerProviderSealForBinding(t *testing.T, invocation runtime.Invocation, binding providergateway.Binding, tools opencode.ToolsConfigurationReceipt) *opencode.ProviderToolTurnSealExpected {
	t.Helper()
	protocol := opencode.ProviderProtocol(binding.Model.AdapterID)
	configuration := opencode.ProviderConfigurationReceipt{
		ProviderID: invocation.Profile.Provider, ModelID: invocation.Profile.Model, Protocol: protocol,
		SDKPackage: "fixture-sdk", GatewayBaseURL: "http://127.0.0.1:43124/v1",
		ContextWindowTokens: binding.Model.ContextWindowTokens, MaxOutputTokens: 128, RuntimeOutputTokens: 8,
		Tools: true, TimeoutMillis: 30000, Variant: invocation.Profile.Effort,
		ToolsConfiguration: tools,
	}
	configuration.SHA256 = tools.SHA256
	process := opencode.ProviderProcessIdentity{
		ProviderID: invocation.Profile.Provider, ModelID: invocation.Profile.Model, Protocol: protocol,
		SDKPackage: "fixture-sdk", GatewayBaseURL: "http://127.0.0.1:43124/v1",
		CapabilitySHA256: strings.Repeat("9", 64), ConfigurationSHA256: strings.Repeat("8", 64),
		ContextWindowTokens: binding.Model.ContextWindowTokens, MaxOutputTokens: 128, RuntimeOutputTokens: 8,
		Tools: true, TimeoutMillis: 30000, Variant: invocation.Profile.Effort,
		Configuration: configuration,
	}
	process.SHA256 = ""
	digest, err := canonical.Hash("harness.opencode-provider-process-identity.v1", process)
	if err != nil {
		t.Fatal(err)
	}
	process.SHA256 = digest
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	proxy := opencode.ProviderProxyIdentity{
		Version: 1, BindingSHA256: strings.Repeat("7", 64), CapabilitySHA256: process.CapabilitySHA256,
		URL: "http://127.0.0.1:43124/v1/chat/completions", BaseURL: "http://127.0.0.1:43124/v1",
		AdapterID: string(protocol), AccessInvocationID: binding.AccessInvocationID, GatewayBindingID: bindingID,
	}
	proxy.SHA256 = ""
	proxyDigest, err := canonical.Hash("harness.opencode-provider-proxy-identity.v1", proxy)
	if err != nil {
		t.Fatal(err)
	}
	proxy.SHA256 = proxyDigest
	if err := opencode.ValidateProviderProcessIdentity(process); err != nil {
		t.Fatal("fixture process identity invalid", err)
	}
	if err := opencode.ValidateProviderProxyIdentity(proxy); err != nil {
		t.Fatal("fixture proxy identity invalid", err)
	}
	return &opencode.ProviderToolTurnSealExpected{Process: process, Proxy: proxy}
}

func buildPlannerPositiveFixture(t *testing.T, nonce string, inputTokens, cachedTokens, outputTokens, reasoningTokens int64) plannerPositiveFixture {
	t.Helper()
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repositoryRoot}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "source.txt"), []byte("fixture source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	discovered, err := repository.Discover(context.Background(), repositoryRoot, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	created := Creation{Version: 1, Nonce: nonce, Repository: discovered, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, gatewayPath := controllerPath+".planner.opencode-runtime.jsonl", controllerPath+".planner.provider-gateway.jsonl"
	result, receipt, intent := sealOpenCodeOfflineTurnForTest(t, runtimePath, gatewayPath, invocation, routing, created.Repository, "plan", "tool result accepted", "harness.planner-result.v1", "planner", inputTokens, cachedTokens, outputTokens, reasoningTokens)
	state, err := opencoderuntime.Inspect(runtimePath, intent)
	if err != nil || state.Result == nil {
		t.Fatal("sealed runtime did not replay", err)
	}
	if err := Append(controllerPath, "planning.provider-observed", receipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	return plannerPositiveFixture{controllerPath: controllerPath, invocation: invocation, result: result, receipt: receipt, inputTokens: inputTokens, cachedTokens: cachedTokens, outputTokens: outputTokens, reasoning: reasoningTokens}
}

func writeSealedPlannerTurn(t *testing.T, broker *contextbroker.Broker, paths opencoderuntime.Paths, sealExpected opencode.SynchronousToolTurnSealExpected, intent opencoderuntime.Intent) opencode.ToolTurnObservation {
	t.Helper()
	return writeSealedToolTurn(t, broker, paths, sealExpected, intent, "plan", "tool result accepted")
}

// writeSealedToolTurn seals one synchronous tool turn offline with a caller
// chosen agent and final text. The planner wrapper above preserves the exact
// historical transcript; writer fixtures reuse the same sealed machinery with
// the build agent and a proposal payload as the final text.
func writeSealedToolTurn(t *testing.T, broker *contextbroker.Broker, paths opencoderuntime.Paths, sealExpected opencode.SynchronousToolTurnSealExpected, intent opencoderuntime.Intent, agent, finalText string) opencode.ToolTurnObservation {
	t.Helper()
	response, err := broker.Call(context.Background(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
	if err != nil || !response.Success {
		t.Fatal("fixture broker source_list must succeed", err, string(response.Content))
	}
	responseBytes, err := canonical.Bytes(response)
	if err != nil {
		t.Fatal(err)
	}
	binding := sealExpected.Dispatch.Dispatch.Binding
	quotedRoot := strconv.Quote(binding.Root)
	quotedDirectory := strconv.Quote(binding.Directory)
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","time":{"created":1},"agent":"` + agent + `","model":{"providerID":"` + intent.Invocation.Profile.Provider + `","modelID":"` + intent.Invocation.Profile.Model + `","variant":"` + intent.Invocation.Profile.Effort + `"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":` + strconv.Quote(intent.Invocation.Input) + `}]}`
	intermediate := `{"info":{"id":"msg_tool","sessionID":"ses_fixture","parentID":"msg_user","providerID":"` + intent.Invocation.Profile.Provider + `","modelID":"` + intent.Invocation.Profile.Model + `","agent":"` + agent + `","role":"assistant","finish":"tool-calls","variant":"` + intent.Invocation.Profile.Effort + `","cost":0.0123,"time":{"created":10,"completed":20},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_step_1","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_tool","messageID":"msg_tool","sessionID":"ses_fixture","type":"tool","callID":"provider-call-1","tool":"engorch_source_list","state":{"status":"completed","input":{"after":"","limit":1},"output":` + strconv.Quote(string(responseBytes)) + `,"title":"","metadata":{"truncated":false},"time":{"start":12,"end":18},"attachments":[]}},{"id":"prt_step_2","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-finish","reason":"tool-calls","tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}}]}`
	finalParts := `[{"id":"prt_step_3","messageID":"msg_final","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_text","messageID":"msg_final","sessionID":"ses_fixture","type":"text","text":` + strconv.Quote(finalText) + `,"time":{"start":22,"end":29}},{"id":"prt_step_4","messageID":"msg_final","sessionID":"ses_fixture","type":"step-finish","reason":"stop","tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}}]`
	final := `{"info":{"id":"msg_final","sessionID":"ses_fixture","parentID":"msg_user","providerID":"` + intent.Invocation.Profile.Provider + `","modelID":"` + intent.Invocation.Profile.Model + `","agent":"` + agent + `","role":"assistant","finish":"stop","variant":"` + intent.Invocation.Profile.Effort + `","cost":0.0345,"time":{"created":21,"completed":30},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":` + finalParts + `}`
	responseFinal := final
	transcript := "[" + user + "," + intermediate + "," + final + "]"
	open, err := contextbroker.Inspect(paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	openStateID, err := canonical.Hash("harness.opencode-tool-turn-broker-state.v1", open)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(paths.Dispatch, "opencode.sync-tool-intent", sealExpected.Dispatch, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(paths.Dispatch, "opencode.sync-tool-observed", struct {
		Response        string `json:"response,omitempty"`
		Transcript      string `json:"transcript"`
		BrokerBindingID string `json:"broker_binding_id"`
		BrokerCatalogID string `json:"broker_catalog_id"`
		BrokerStateID   string `json:"broker_state_id"`
	}{responseFinal, transcript, sealExpected.Dispatch.BrokerBindingID, sealExpected.Dispatch.BrokerCatalogID, openStateID}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var offline *opencode.Client
	observation, err := offline.RecoverSynchronousToolTurn(context.Background(), paths.Dispatch, paths.Broker, sealExpected.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	observationID, _ := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	sealIntent := opencode.SynchronousToolTurnSealIntent{Version: 1, Expected: sealExpected, ObservationSHA256: observationID, TranscriptSHA256: observation.TranscriptSHA256, OpenBrokerStateID: observation.BrokerStateID, ToolsConfigurationSHA256: sealExpected.Tools.SHA256, MCPStatusSHA256: strings.Repeat("d", 64), ProcessID: 123, OpenCodeEndpoint: "http://127.0.0.1:43124", MCPEndpoint: sealExpected.Tools.Endpoint}
	sealIntent.ID, err = canonical.Hash("harness.opencode-tool-turn-seal-intent.v1", struct {
		Version                  int                                      `json:"version"`
		ID                       string                                   `json:"id"`
		Expected                 opencode.SynchronousToolTurnSealExpected `json:"expected"`
		ObservationSHA256        string                                   `json:"observation_sha256"`
		TranscriptSHA256         string                                   `json:"transcript_sha256"`
		OpenBrokerStateID        string                                   `json:"open_broker_state_id"`
		ToolsConfigurationSHA256 string                                   `json:"tools_configuration_sha256"`
		MCPStatusSHA256          string                                   `json:"mcp_status_sha256"`
		ProcessID                int                                      `json:"process_id"`
		OpenCodeEndpoint         string                                   `json:"opencode_endpoint"`
		MCPEndpoint              string                                   `json:"mcp_endpoint"`
	}{sealIntent.Version, "", sealIntent.Expected, sealIntent.ObservationSHA256, sealIntent.TranscriptSHA256, sealIntent.OpenBrokerStateID, sealIntent.ToolsConfigurationSHA256, sealIntent.MCPStatusSHA256, sealIntent.ProcessID, sealIntent.OpenCodeEndpoint, sealIntent.MCPEndpoint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(paths.Seal, "opencode.tool-turn-seal-intent", sealIntent, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := contextbroker.Inspect(paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	closedID, _ := canonical.Hash("harness.opencode-tool-turn-sealed-broker.v1", closed)
	terminal := opencode.ToolTurnTerminalReceipt{Version: 1, IntentID: sealIntent.ID, ObservationSHA256: sealIntent.ObservationSHA256, TranscriptSHA256: sealIntent.TranscriptSHA256, OpenBrokerStateID: sealIntent.OpenBrokerStateID, ClosedBrokerStateID: closedID, ToolsConfigurationSHA256: sealIntent.ToolsConfigurationSHA256, MCPStatusSHA256: sealIntent.MCPStatusSHA256, ProcessID: sealIntent.ProcessID, MCPHandlersStopped: true, RootProcessReaped: true, BrokerClosed: true, ProviderProxyStopped: true, ProviderProcessSHA256: sealExpected.Provider.Process.SHA256, ProviderProxySHA256: sealExpected.Provider.Proxy.SHA256}
	if _, err := journal.Append(paths.Seal, "opencode.tool-turn-sealed", terminal, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return observation
}

func appendGatewayCallForGeneration(t *testing.T, gatewayPath string, binding providergateway.Binding, sequence int, generation opencode.ToolGenerationObservation, input, output, cached, reasoning int64) {
	t.Helper()
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	// Gateway request digests must be unique valid hex per call.
	requestDigest := strings.Repeat("a", 64)
	expectationID := strings.Repeat("c", 64)
	if sequence == 2 {
		requestDigest = strings.Repeat("b", 64)
		expectationID = strings.Repeat("d", 64)
	}
	intent := providergateway.CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: requestDigest, RequestBytes: 128, MaxOutputTokens: 10, RequestExpectationID: expectationID}
	intent.CallID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(gatewayPath, "provider.call-intent", intent, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cachedPtr, reasoningPtr := &cached, &reasoning
	usage := providergateway.Usage{InputTokens: input, OutputTokens: output, CacheReadTokens: cachedPtr, ReasoningTokens: reasoningPtr}
	toolIdentities := make([]providergateway.ResponseToolIdentity, 0, len(generation.Calls))
	for _, call := range generation.Calls {
		toolIdentities = append(toolIdentities, providergateway.ResponseToolIdentity{ID: call.ProviderCallID, Name: call.Tool, ArgumentsSHA256: call.ArgumentsSHA256})
	}
	if toolIdentities == nil {
		toolIdentities = []providergateway.ResponseToolIdentity{}
	}
	receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat(string(rune('0'+sequence)), 64), ResponseBytes: 256, ResponseID: "response-" + strconv.Itoa(sequence), ObservedModel: binding.Model.Model, Finish: strings.ReplaceAll(generation.Finish, "-", "_"), StreamComplete: true, UsageComplete: true, Usage: usage, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: toolIdentities, OutputTextSHA256: generation.TextSHA256}, RequestExpectationID: intent.RequestExpectationID}
	if _, err := journal.Append(gatewayPath, "provider.call-receipt", receipt, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestPositivePlannerFullChainViaMeasureRunUsage(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-positive", 48, 16, 12, 4)
	before, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	report, err := MeasureRunUsage(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scope != "controller_admissions_and_journaled_codex_hosts" && report.Scope != "journaled_codex_hosts" {
		t.Fatal("Scope must remain the Codex scope", report.Scope)
	}
	if len(report.OpenCodeInvocations) != 1 {
		t.Fatalf("positive fixture must report one OpenCode entry: %+v", report.OpenCodeInvocations)
	}
	entry := report.OpenCodeInvocations[0]
	if entry.InvocationID != fixture.invocation.ID || entry.Role != "planner" || !entry.ReceiptMatched || entry.Status != "completed" || entry.Usage == nil {
		t.Fatal("positive entry must be receipt-matched completed with totals", entry)
	}
	if entry.Usage.InputTokens != 48 || *entry.Usage.CachedInputTokens != 16 || *entry.Usage.UncachedInputTokens != 32 || entry.Usage.OutputTokens != 12 || *entry.Usage.ReasoningTokens != 4 || *entry.Usage.OrdinaryOutputTokens != 8 {
		t.Fatal("positive totals changed", entry.Usage)
	}
	if entry.GatewayCalls == nil || *entry.GatewayCalls != 2 || entry.GatewayReceipts == nil || *entry.GatewayReceipts != 2 || entry.GatewayPending == nil || *entry.GatewayPending != 0 {
		t.Fatal("positive call counts changed", entry.GatewayCalls, entry.GatewayReceipts, entry.GatewayPending)
	}
	afterBytes, err := os.ReadFile(fixture.controllerPath)
	if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("read-only usage mutated controller state")
	}
	_ = before
	_ = fixture.result
}

func TestMeasureRunUsageWithSchedulerPreservesLegacy(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-scheduler-legacy", 48, 16, 12, 4)
	legacy, err := MeasureRunUsage(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	withScheduler, err := MeasureRunUsageWithScheduler(fixture.controllerPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(withScheduler.OpenCodeInvocations) != 1 || len(legacy.OpenCodeInvocations) != 1 {
		t.Fatalf("scheduler seam must preserve legacy reporting: %+v", withScheduler.OpenCodeInvocations)
	}
	if !sameCanonical(legacy, withScheduler) {
		t.Fatal("empty scheduler must report byte-identical usage")
	}
}

func TestMeasureRunUsageWithSchedulerIgnoresSchedulerForV1(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-scheduler-unused", 48, 16, 12, 4)
	report, err := MeasureRunUsageWithScheduler(fixture.controllerPath, filepath.Join(t.TempDir(), "no-scheduler.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OpenCodeInvocations) != 1 || report.OpenCodeInvocations[0].Status != "completed" {
		t.Fatal("v1 turns must not consult the scheduler", report.OpenCodeInvocations)
	}
}

func TestCollectAdmissionOrderPending(t *testing.T) {
	first, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "First bounded change.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "reviewer"}, "Second bounded review.")
	if err != nil {
		t.Fatal(err)
	}
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	snapshot := Snapshot{RunID: strings.Repeat("a", 64), ProviderRuntime: map[string]providerDispatchReceipt{}, AgentDispatch: map[string]AgentDispatchState{}}
	admissions := []AgentDispatchAdmission{
		{Version: 1, RunID: snapshot.RunID, TreeID: snapshot.RunID, Invocation: first},
		{Version: 1, RunID: snapshot.RunID, TreeID: snapshot.RunID, Invocation: second},
	}
	events := []journal.Event{}
	for _, admission := range admissions {
		payload, err := canonical.Bytes(admission)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, journal.Event{Kind: "agent.dispatch-admitted", Payload: payload})
	}
	_ = controllerPath
	collected := []OpenCodeUsageEntry{}
	seen := map[string]bool{}
	for _, event := range events {
		var admission AgentDispatchAdmission
		if err := canonical.Decode(event.Payload, &admission); err != nil {
			t.Fatal(err)
		}
		entry, ok := pendingOpenCodeEntry(filepath.Join(t.TempDir(), "missing.jsonl"), snapshot, admission)
		if !ok || seen[entry.InvocationID] {
			t.Fatal("pending admission must be reported once")
		}
		seen[entry.InvocationID] = true
		collected = append(collected, entry)
	}
	if len(collected) != 2 || collected[0].InvocationID != first.ID || collected[1].InvocationID != second.ID {
		t.Fatal("pending entries must follow actual controller admission order", collected)
	}
}

func TestMeasureRunUsageRejectsForgedRuntimeHeadAtConsumer(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-stale-runtime", 48, 16, 12, 4)
	runtimePath := fixture.controllerPath + ".planner.opencode-runtime.jsonl"
	if _, err := journal.Append(runtimePath, "opencode-runtime.forged", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := MeasureRunUsage(fixture.controllerPath)
	if err == nil || !strings.Contains(err.Error(), "opencode runtime head changed") {
		t.Fatalf("stale runtime head must reject at consumer, got %v", err)
	}
	if strings.Contains(err.Error(), fixture.controllerPath) {
		t.Fatal("diagnostic leaked path")
	}
}

func TestMeasureRunUsageRejectsForgedGatewayHeadAtConsumer(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-stale-gateway", 48, 16, 12, 4)
	gatewayPath := fixture.controllerPath + ".planner.provider-gateway.jsonl"
	if _, err := journal.Append(gatewayPath, "provider.call-intent", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		// Appending an invalid gateway event still changes the head; the
		// consumer head gate must reject before gateway replay details leak.
		t.Log("gateway append rejected at journal layer", err)
	}
	_, err := MeasureRunUsage(fixture.controllerPath)
	if err == nil {
		t.Fatal("stale gateway must reject at consumer")
	}
	if !(strings.Contains(err.Error(), "opencode gateway head changed") || strings.Contains(err.Error(), "opencode gateway changed")) {
		t.Fatalf("gateway mismatch must reject with safe diagnostic, got %v", err)
	}
}

func TestMeasureRunUsageRejectsDetachedSourceAtConsumer(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-detached-source", 48, 16, 12, 4)
	events, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	// Rebind the same valid receipt and journals against a detached
	// repository identity; controller replay remains valid so the failure
	// reaches consumer source validation, not fixture Append.
	detached := snapshot
	detached.Creation.Repository.Commit = strings.Repeat("f", 40)
	detached.Creation.Repository.Tree = strings.Repeat("e", 40)
	receiptIndex := -1
	for index, event := range events {
		if event.Kind == "planning.provider-observed" {
			receiptIndex = index
			break
		}
	}
	if receiptIndex < 0 {
		t.Fatal("planner receipt missing from fixture")
	}
	var receipt providerDispatchReceipt
	if err := canonical.Decode(events[receiptIndex].Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyOpenCodeReceipt(fixture.controllerPath, detached, events, receiptIndex, receipt); err == nil || !strings.Contains(err.Error(), "opencode runtime source changed") {
		t.Fatalf("detached source must reject at consumer, got %v", err)
	}
}

func TestVerifyRejectsCostAndDirectProviderAtConsumer(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-cost-direct", 48, 16, 12, 4)
	events, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptIndex := -1
	for index, event := range events {
		if event.Kind == "planning.provider-observed" {
			receiptIndex = index
			break
		}
	}
	if receiptIndex < 0 {
		t.Fatal("planner receipt missing")
	}
	var receipt providerDispatchReceipt
	if err := canonical.Decode(events[receiptIndex].Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	cost := int64(5)
	withCost := receipt
	costed := withCost.Result
	costed.Usage.CostMinorUnits = &cost
	withCost.Result = costed
	withCost.ResultHash, _ = canonical.Hash("harness.planner-result.v1", costed)
	if _, err := verifyOpenCodeReceipt(fixture.controllerPath, snapshot, events, receiptIndex, withCost); err == nil || !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("cost-bearing receipt must reject at consumer, got %v", err)
	} else if strings.Contains(err.Error(), fixture.controllerPath) {
		t.Fatal("diagnostic leaked path")
	}
	direct := receipt
	direct.Result.Requested = runtime.Profile{Runtime: "provider-api", Provider: "openai", Model: "model", Effort: "none", Role: "planner"}
	if _, err := verifyOpenCodeReceipt(fixture.controllerPath, snapshot, events, receiptIndex, direct); err == nil || !strings.Contains(err.Error(), "outside current scope") {
		t.Fatalf("direct-provider receipt must stay outside scope at consumer, got %v", err)
	}
}

func TestMeasureRunUsageRejectsModelMismatchAtConsumer(t *testing.T) {
	fixture := buildPlannerPositiveFixture(t, "opencode-model-mismatch", 48, 16, 12, 4)
	events, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptIndex := -1
	for index, event := range events {
		if event.Kind == "planning.provider-observed" {
			receiptIndex = index
			break
		}
	}
	if receiptIndex < 0 {
		t.Fatal("planner receipt missing")
	}
	var receipt providerDispatchReceipt
	if err := canonical.Decode(events[receiptIndex].Payload, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.ObservedModel = "substituted-model"
	if _, err := verifyOpenCodeReceipt(fixture.controllerPath, snapshot, events, receiptIndex, receipt); err == nil || !strings.Contains(err.Error(), "opencode model changed") {
		t.Fatalf("model substitution must reject at consumer, got %v", err)
	}
}

func TestCollectRejectsDuplicateReceiptAtConsumer(t *testing.T) {
	receipt := providerDispatchReceipt{Version: 1, Role: "writer", InvocationID: strings.Repeat("c", 64), Result: runtime.Result{Requested: runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}}}
	snapshot := Snapshot{ProviderRuntime: map[string]providerDispatchReceipt{receipt.InvocationID: receipt}}
	payload, err := canonical.Bytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	events := []journal.Event{
		{Kind: "role.provider-observed", Payload: payload},
		{Kind: "role.provider-observed", Payload: payload},
	}
	if _, err := collectOpenCodeUsage(filepath.Join(t.TempDir(), "run.jsonl"), snapshot, events); err == nil || !strings.Contains(err.Error(), "opencode receipt changed") {
		t.Fatalf("duplicate receipt must reject at consumer, got %v", err)
	}
}

func digestTextForCompositeTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestCompositeFinalGatewayValidatesFinishedTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "composite-gateway.jsonl")
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: json.RawMessage(`{}`), ContextWindowTokens: 1 << 20, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 32}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("b", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 1 << 20, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(path, "provider.bound", binding, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	mcpHash, err := toolreceipts.ArgumentsSHA256(json.RawMessage(`{"path":"file.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	providerHash := sha256.Sum256([]byte(`{"path":"file.txt"}`))
	providerDigest := hex.EncodeToString(providerHash[:])
	appendCompositeCall := func(sequence int, finish, text, tool, callID, argsSHA string, input, output int64) {
		expectationIDs := map[int]string{1: strings.Repeat("c", 64), 2: strings.Repeat("d", 64), 3: strings.Repeat("e", 64)}
		requestDigests := map[int]string{1: strings.Repeat("a", 64), 2: strings.Repeat("b", 64), 3: strings.Repeat("f", 64)}
		intent := providergateway.CallIntent{Version: 1, Sequence: sequence, BindingID: bindingID, InvocationID: binding.AccessInvocationID, RouteID: binding.RouteID, EndpointID: binding.EndpointID, ModelID: binding.ModelID, RequestSHA256: requestDigests[sequence], RequestBytes: 128, MaxOutputTokens: 16, RequestExpectationID: expectationIDs[sequence]}
		intent.CallID, err = intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := journal.Append(path, "provider.call-intent", intent, func([]journal.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
		tools := []providergateway.ResponseToolIdentity{}
		if tool != "" {
			tools = append(tools, providergateway.ResponseToolIdentity{ID: callID, Name: "engorch_" + tool, ArgumentsSHA256: argsSHA})
		}
		receipt := providergateway.CallReceipt{Version: 1, BindingID: bindingID, InvocationID: binding.AccessInvocationID, CallID: intent.CallID, ResponseSHA256: strings.Repeat(string(rune('0'+sequence)), 64), ResponseBytes: 256, ResponseID: "response-" + strconv.Itoa(sequence), ObservedModel: model.Model, Finish: finish, StreamComplete: true, UsageComplete: true, Usage: providergateway.Usage{InputTokens: input, OutputTokens: output}, Semantic: &providergateway.ResponseSemanticProjection{Version: 1, Kind: "assistant-turn", ToolCalls: tools, OutputTextSHA256: digestTextForCompositeTest(text)}, RequestExpectationID: intent.RequestExpectationID}
		if _, err := journal.Append(path, "provider.call-receipt", receipt, func([]journal.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	appendCompositeCall(1, "tool_calls", "source preface", "source_read", "call-1", providerDigest, 5, 2)
	appendCompositeCall(2, "tool_calls", "agent preface", "list_agents", "call-2", digestTextForCompositeTest(`{"limit":8}`), 5, 2)
	appendCompositeCall(3, "stop", "done", "", "", "", 5, 2)
	state, err := providergateway.Inspect(path)
	if err != nil || !state.Finished || state.Exhausted {
		t.Fatal("composite gateway fixture must finish", state, err)
	}
	totals, err := summarizeOpenCodeGatewayUsage(state.Aggregate)
	if err != nil || totals.InputTokens != 15 || totals.OutputTokens != 6 {
		t.Fatal("composite aggregate must project without inventing totals", totals, err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatal(err)
	}
	initial := providergateway.State{Binding: &binding}
	initialID, err := canonical.Hash("harness.opencode-runtime-initial-gateway.v1", initial)
	if err != nil {
		t.Fatal(err)
	}
	bound := opencoderuntime.Bound{Paths: opencoderuntime.Paths{Gateway: path}, Gateway: &opencoderuntime.GatewayBound{Binding: binding, BindingID: bindingID, InitialHead: events[0].Hash, InitialStateID: initialID}}
	observation := opencode.CompositeToolTurnObservation{
		Text: "done",
		Generations: []opencode.CompositeToolGenerationObservation{
			{Finish: "tool-calls", TextSHA256: digestTextForCompositeTest("source preface"), Calls: []opencode.CompositeToolCallObservation{{ProviderCallID: "call-1", Tool: "source_read", ArgumentsSHA256: mcpHash, ProviderArgumentsSHA256: providerDigest}}},
			{Finish: "tool-calls", TextSHA256: digestTextForCompositeTest("agent preface"), Calls: []opencode.CompositeToolCallObservation{{ProviderCallID: "call-2", Tool: "list_agents", ArgumentsSHA256: digestTextForCompositeTest(`{"limit":8}`), ProviderArgumentsSHA256: digestTextForCompositeTest(`{"limit":8}`)}}},
			{Finish: "stop", TextSHA256: digestTextForCompositeTest("done"), Calls: []opencode.CompositeToolCallObservation{}},
		},
	}
	validated, err := opencoderuntime.ValidateCompositeFinalGateway(bound, observation)
	if err != nil || !validated.Finished {
		t.Fatal("finished composite transcript must validate", validated, err)
	}
	mutated := observation
	mutated.Generations = append([]opencode.CompositeToolGenerationObservation(nil), observation.Generations...)
	mutated.Generations[0].TextSHA256 = strings.Repeat("0", 64)
	if _, err := opencoderuntime.ValidateCompositeFinalGateway(bound, mutated); err == nil {
		t.Fatal("substituted composite generation must reject")
	}
}

func TestV3SchedulerSeamReachesCompositePath(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	repo := repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	created := Creation{Version: 1, Nonce: "opencode-v3-seam", Repository: repo, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	contextBinding, err := contextbroker.NewBinding(invocation.ID, repo, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	receipts := toolreceipts.Binding{Version: 2, InvocationID: invocation.ID, CallerBindingSHA256: strings.Repeat("9", 64), CatalogSHA256: strings.Repeat("a", 64), Tools: []toolreceipts.ToolOwner{{Tool: "list_agents", Owner: toolreceipts.OwnerAgent}, {Tool: "source_list", Owner: toolreceipts.OwnerContext}}, QueuePolicy: toolreceipts.QueuePolicySerialFIFO, MaxQueuedCalls: 1}
	bindingID, err := receipts.ID()
	if err != nil {
		t.Fatal(err)
	}
	receipts.BindingID = bindingID
	plan := &opencoderuntime.SessionPlan{Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort, ToolNames: []string{"list_agents", "source_list"}, CatalogSHA256: strings.Repeat("a", 64)}
	intent := opencoderuntime.Intent{Version: 3, Invocation: invocation, Directory: root, Project: opencode.ProjectExpectation{Directory: root, Mode: opencode.ProjectModeGlobal}, Context: contextBinding, SessionPlan: plan, ToolReceipts: &receipts, ProviderGatewayBindingID: strings.Repeat("b", 64)}
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := controllerPath + ".planner.opencode-runtime.jsonl"
	if err := opencoderuntime.RecordIntent(runtimePath, intent); err != nil {
		t.Fatal(err)
	}
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, Output: "sealed composite result"}
	record := opencoderuntime.ResultRecord{Version: 2, IntentID: intent.IntentID, Result: result}
	if _, err := journal.Append(runtimePath, "opencode-runtime.result", record, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	gatewayPath := controllerPath + ".planner.provider-gateway.jsonl"
	endpoint := providergateway.EndpointContract{Version: 2, Provider: "fixture", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: &providergateway.AuthContract{Scheme: "bearer", CredentialRef: "fixture-key"}}
	endpointID, err := endpoint.ID()
	if err != nil {
		t.Fatal(err)
	}
	model := providergateway.ModelContract{Version: 2, Provider: "fixture", Model: "model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilities: json.RawMessage(`{}`), ContextWindowTokens: 1 << 20, MaxCalls: 4, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 64}
	modelID, err := model.ID()
	if err != nil {
		t.Fatal(err)
	}
	gatewayBinding := providergateway.Binding{Version: 1, AccessPolicyID: strings.Repeat("a", 64), AccessInvocationID: strings.Repeat("d", 64), RouteID: strings.Repeat("c", 64), ReservedTokens: 1 << 20, EndpointID: endpointID, ModelID: modelID, Endpoint: endpoint, Model: model}
	if _, err := journal.Append(gatewayPath, "provider.bound", gatewayBinding, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	gatewayEvents, err := journal.Read(gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	resultHash, err := canonical.Hash("harness.planner-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: invocation.ID, AccessInvocationID: strings.Repeat("d", 64), RuntimeJournalHead: runtimeEvents[len(runtimeEvents)-1].Hash, GatewayJournalHead: gatewayEvents[len(gatewayEvents)-1].Hash, ResultHash: resultHash, ObservedModel: "model", ObservedProvider: "fixture", Result: result}
	events, err := journal.Read(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyOpenCodeReceiptWithScheduler(controllerPath, "", snapshot, events, 1, receipt); err == nil || !strings.Contains(err.Error(), "opencode scheduler binding unavailable") {
		t.Fatalf("v3 without scheduler must fail safe, got %v", err)
	}
	bogus := filepath.Join(t.TempDir(), "no-scheduler.jsonl")
	_, err = verifyOpenCodeReceiptWithScheduler(controllerPath, bogus, snapshot, events, 1, receipt)
	if err == nil || strings.Contains(err.Error(), "opencode scheduler binding unavailable") {
		t.Fatalf("supplied scheduler must reach the composite path, got %v", err)
	}
	if !strings.Contains(err.Error(), "opencode admission missing for receipt") {
		t.Fatalf("legacy planner v3 without dispatch must stop at admission binding, got %v", err)
	}
}

func TestCompositeVerifierBindsCallerWithoutEffects(t *testing.T) {
	fixture := newAgentToolFixture(t)
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(fixture.binding.InvocationID, snapshot.Creation.Repository, nil, contextbroker.Limits{MaxCalls: 4, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	brokerPath := filepath.Join(t.TempDir(), "broker.jsonl")
	broker, err := contextbroker.Open(brokerPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	callerID, err := agentToolCallerIdentity(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	recorderBinding, err := contextmcp.PrepareRecorderBindingWithQueue(broker, fixture.binding.InvocationID, callerID, fixture.projection, 1)
	if err != nil {
		t.Fatal(err)
	}
	verify, err := newCompositeBackendVerifier(fixture.controllerPath, fixture.schedulerPath, brokerPath, fixture.binding, binding, recorderBinding)
	if err != nil {
		t.Fatal("journal-only composite verifier must bind", err)
	}
	if verify(toolreceipts.Owner("foreign"), toolbridge.Call{}, toolbridge.Result{}) == nil {
		t.Fatal("foreign composite owner must reject")
	}
	foreign := recorderBinding
	foreign.CallerBindingSHA256 = strings.Repeat("0", 64)
	if _, err := newCompositeBackendVerifier(fixture.controllerPath, fixture.schedulerPath, brokerPath, fixture.binding, binding, foreign); err == nil {
		t.Fatal("substituted composite caller must reject")
	}
}

// sealPlannerTurnForCorrection seals one OpenCode planner turn for an explicit
// invocation using the existing sealed fixture scaffolding. It performs no
// controller writes; the caller appends the returned receipt through the
// replay-validated controller journal. No new provider harness is introduced.
func sealPlannerTurnForCorrection(t *testing.T, controllerPath string, cfg config.Config, repo repository.Identity, invocation runtime.Invocation, inputTokens, cachedTokens, outputTokens, reasoningTokens int64) (runtime.Result, providerDispatchReceipt) {
	t.Helper()
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	stem, err := providerInvocationJournalStem(controllerPath, invocation, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, gatewayPath := controllerPath+"."+stem+".opencode-runtime.jsonl", controllerPath+"."+stem+".provider-gateway.jsonl"
	result, receipt, _ := sealOpenCodeOfflineTurnForTest(t, runtimePath, gatewayPath, invocation, routing, repo, "plan", "tool result accepted", "harness.planner-result.v1", "planner", inputTokens, cachedTokens, outputTokens, reasoningTokens)
	return result, receipt
}

// correctionControllerFixture builds a replay-validated controller through a
// first sealed planner receipt and recorded plan, with semantic correction
// enabled so the sealed output (not a valid plan graph) admits a real
// planning.semantic-correction event. Every controller mutation below goes
// through Append replay; no Snapshot is hand-edited.
func correctionControllerFixture(t *testing.T, nonce string) (string, config.Config, repository.Identity, runtime.Invocation, runtime.Result, providerDispatchReceipt) {
	t.Helper()
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repositoryRoot}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "source.txt"), []byte("fixture source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	discovered, err := repository.Discover(context.Background(), repositoryRoot, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	created := Creation{Version: 1, Nonce: nonce, Repository: discovered, Objective: "Produce an implementation plan.", Config: cfg,
		Execution: &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 1, SemanticCorrectionVersion: 1}}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	result, receipt := sealPlannerTurnForCorrection(t, controllerPath, cfg, discovered, invocation, 48, 16, 12, 4)
	if err := Append(controllerPath, "planning.provider-observed", receipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	return controllerPath, cfg, discovered, invocation, result, receipt
}

func TestCollectRetainsHistoricalPlannerAfterSemanticCorrection(t *testing.T) {
	controllerPath, _, _, invocation, _, receipt := correctionControllerFixture(t, "opencode-correction-predecessor")
	before, err := MeasureRunUsage(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.OpenCodeInvocations) != 1 {
		t.Fatalf("pre-correction must report one OpenCode entry: %+v", before.OpenCodeInvocations)
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := startPlannerSemanticCorrection(controllerPath, s); err != nil {
		t.Fatal("sealed plan must admit a real semantic correction", err)
	}
	after, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "PLANNING" || after.PlannerProvider != nil || after.Plan != nil || len(after.PlannerCorrections) != 1 {
		t.Fatalf("correction must clear the latest planner projection and retain rejection: %+v", after)
	}
	if after.PlannerCorrections[0].Invocation.ID == invocation.ID {
		t.Fatal("correction must issue a new invocation identity, never resend the predecessor")
	}
	report, err := MeasureRunUsage(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OpenCodeInvocations) != 1 {
		t.Fatalf("predecessor completion must survive semantic correction, got %+v", report.OpenCodeInvocations)
	}
	entry := report.OpenCodeInvocations[0]
	if entry.InvocationID != invocation.ID || entry.InvocationID != receipt.InvocationID || entry.Role != "planner" || !entry.ReceiptMatched || entry.Status != "completed" || entry.Usage == nil {
		t.Fatal("predecessor entry must stay receipt-matched completed with totals", entry)
	}
	if entry.Usage.InputTokens != 48 || entry.Usage.CachedInputTokens == nil || *entry.Usage.CachedInputTokens != 16 || entry.Usage.UncachedInputTokens == nil || *entry.Usage.UncachedInputTokens != 32 || entry.Usage.OutputTokens != 12 || entry.Usage.ReasoningTokens == nil || *entry.Usage.ReasoningTokens != 4 || entry.Usage.OrdinaryOutputTokens == nil || *entry.Usage.OrdinaryOutputTokens != 8 {
		t.Fatal("predecessor totals changed across correction", entry.Usage)
	}
	if !sameCanonical(before.OpenCodeInvocations[0], entry) {
		t.Fatal("predecessor entry must be byte-identical before and after correction")
	}
}

func TestCollectRetainsMultiplePlannerReceiptsAcrossCorrection(t *testing.T) {
	controllerPath, cfg, discovered, first, _, _ := correctionControllerFixture(t, "opencode-correction-pair")
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := startPlannerSemanticCorrection(controllerPath, s); err != nil {
		t.Fatal(err)
	}
	corrected, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	secondInvocation, err := plannerInvocationForSnapshot(corrected)
	if err != nil {
		t.Fatal(err)
	}
	if secondInvocation.ID == first.ID {
		t.Fatal("correction must advance the planner invocation identity")
	}
	secondResult, secondReceipt := sealPlannerTurnForCorrection(t, controllerPath, cfg, discovered, secondInvocation, 64, 20, 16, 6)
	if err := Append(controllerPath, "planning.provider-observed", secondReceipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", secondResult); err != nil {
		t.Fatal(err)
	}
	report, err := MeasureRunUsage(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OpenCodeInvocations) != 2 {
		t.Fatalf("both planner completions must be retained, got %+v", report.OpenCodeInvocations)
	}
	// Actual receipt order: predecessor first, successor second.
	if report.OpenCodeInvocations[0].InvocationID != first.ID || report.OpenCodeInvocations[1].InvocationID != secondInvocation.ID {
		t.Fatal("planner entries must preserve actual receipt order", report.OpenCodeInvocations)
	}
	for _, entry := range report.OpenCodeInvocations {
		if entry.Role != "planner" || !entry.ReceiptMatched || entry.Status != "completed" || entry.Usage == nil {
			t.Fatal("each planner receipt must be receipt-matched completed with totals", entry)
		}
	}
	predecessor, successor := report.OpenCodeInvocations[0], report.OpenCodeInvocations[1]
	if predecessor.Usage.InputTokens != 48 || *predecessor.Usage.CachedInputTokens != 16 || successor.Usage.InputTokens != 64 || *successor.Usage.CachedInputTokens != 20 || successor.Usage.OutputTokens != 16 || *successor.Usage.ReasoningTokens != 6 {
		t.Fatal("per-receipt totals must stay bound to their own gateway aggregate", predecessor.Usage, successor.Usage)
	}
}

func TestCollectRejectsDuplicatePlannerReceipt(t *testing.T) {
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "planner"}
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: strings.Repeat("c", 64), Result: runtime.Result{Requested: profile}}
	snapshot := Snapshot{PlannerProvider: &receipt}
	payload, err := canonical.Bytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	events := []journal.Event{
		{Kind: "planning.provider-observed", Payload: payload},
		{Kind: "planning.provider-observed", Payload: payload},
	}
	if _, err := collectOpenCodeUsage(filepath.Join(t.TempDir(), "run.jsonl"), snapshot, events); err == nil || !strings.Contains(err.Error(), "opencode receipt changed") {
		t.Fatalf("duplicate planner receipt must reject, got %v", err)
	}
}

func TestCollectRequiresLatestPlannerReceiptWhenPresent(t *testing.T) {
	profile := runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "planner"}
	stored := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: strings.Repeat("c", 64), Result: runtime.Result{Requested: profile}}
	snapshot := Snapshot{PlannerProvider: &stored}
	other := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: strings.Repeat("d", 64), Result: runtime.Result{Requested: profile}}
	payload, err := canonical.Bytes(other)
	if err != nil {
		t.Fatal(err)
	}
	events := []journal.Event{{Kind: "planning.provider-observed", Payload: payload}}
	if _, err := collectOpenCodeUsage(filepath.Join(t.TempDir(), "run.jsonl"), snapshot, events); err == nil || !strings.Contains(err.Error(), "opencode receipt changed") {
		t.Fatalf("missing latest planner receipt must reject, got %v", err)
	}
}

func TestCollectHistoricalPredecessorMissingJournalRejects(t *testing.T) {
	controllerPath, _, _, _, _, _ := correctionControllerFixture(t, "opencode-correction-missing")
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := startPlannerSemanticCorrection(controllerPath, s); err != nil {
		t.Fatal(err)
	}
	runtimePath := controllerPath + ".planner.opencode-runtime.jsonl"
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	if _, err := MeasureRunUsage(controllerPath); err == nil || !strings.Contains(err.Error(), "opencode runtime journal missing") {
		t.Fatalf("missing predecessor journal must reject with totals unavailable, got %v", err)
	}
}

func TestCollectHistoricalPredecessorGatewayTamperRejects(t *testing.T) {
	controllerPath, _, _, _, _, _ := correctionControllerFixture(t, "opencode-correction-tamper")
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := startPlannerSemanticCorrection(controllerPath, s); err != nil {
		t.Fatal(err)
	}
	gatewayPath := controllerPath + ".planner.provider-gateway.jsonl"
	if _, err := journal.Append(gatewayPath, "opencode-runtime.forged", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Log("gateway append rejected at journal layer", err)
	}
	if _, err := MeasureRunUsage(controllerPath); err == nil {
		t.Fatal("tampered predecessor gateway must reject")
	} else if !(strings.Contains(err.Error(), "opencode gateway head changed") || strings.Contains(err.Error(), "opencode gateway changed")) {
		t.Fatalf("gateway mismatch must reject with safe diagnostic, got %v", err)
	}
}

func TestHistoricalPredecessorLeavesUnmatchedPendingUnknown(t *testing.T) {
	controllerPath, _, _, _, _, _ := correctionControllerFixture(t, "opencode-correction-pending")
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := startPlannerSemanticCorrection(controllerPath, s); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "high", Role: "writer"}, "Implement the bounded change.")
	if err != nil {
		t.Fatal(err)
	}
	admission := AgentDispatchAdmission{Version: 1, RunID: snapshot.RunID, TreeID: snapshot.RunID, Invocation: invocation}
	entry, ok := pendingOpenCodeEntry(controllerPath, snapshot, admission)
	if !ok {
		t.Fatal("unmatched writer admission must be reported")
	}
	if entry.Status != "UNKNOWN" || entry.ReceiptMatched || entry.Usage != nil {
		t.Fatal("unmatched work must remain explicitly UNKNOWN with no totals", entry)
	}
	report, err := MeasureRunUsage(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OpenCodeInvocations) != 1 || report.OpenCodeInvocations[0].Role != "planner" || report.OpenCodeInvocations[0].Status != "completed" {
		t.Fatalf("historical predecessor must report exactly one completed planner entry: %+v", report.OpenCodeInvocations)
	}
}
