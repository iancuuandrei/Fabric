package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func isolatedOpenCodeCreation(t *testing.T) Creation {
	t.Helper()
	c := graphCreation(t, 2)
	c.Config.Version = 2
	c.Execution.RepairPlanningVersion = 1
	c.Execution.IsolatedImplementationVersion = 1
	c.Config.PlannerContract = "plan-graph-v7"
	c.Config.WriterContract = "anchored-edits-v1"
	c.Config.ExplorerContract = "json-v2"
	c.Config.Writer = &runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "deployment/opaque-model", Effort: "none", Role: "writer"}
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *c.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: c.Config.Writer.Provider, Model: c.Config.Writer.Model}
	c.Execution.IsolationEstimate = &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	c.Execution.IsolationCapacity = &engineeringplan.ResourceCapacity{
		CPUMilli: 2, MemoryMiB: 2, VerificationSlots: 2, TotalRuntimeSlots: 2,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: route.Provider, Slots: 2}},
		ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: route.Provider, Model: route.Model}, Slots: 2}},
		RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 2}},
	}
	c.Config.ControllerStateRoot = filepath.Join(t.TempDir(), "controller-state")
	c.Config.Access = &config.Access{
		Class:  access.Private,
		Limits: access.Limits{Tokens: 10000, Concurrency: 8},
		Roles: map[string]string{
			"planner":  "fixture-planner",
			"explorer": "fixture-explorer",
			"reviewer": "fixture-reviewer",
			"writer":   "writer-api",
		},
		Invocations: map[string]config.InvocationLimit{
			"planner":  {Tokens: 100},
			"explorer": {Tokens: 100},
			"reviewer": {Tokens: 100},
			"writer":   {Tokens: 1000},
		},
		Profiles: []access.Profile{
			{Version: 1, Name: "fixture-planner", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "fixture-explorer", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "fixture-reviewer", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "writer-api", Kind: "subscription", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}},
		},
	}
	c.Config.Provider = &config.Provider{
		Version:     1,
		Credentials: []config.ProviderCredential{{Ref: "team-key", Environment: "ENGORCH_PROVIDER_TEAM_KEY"}},
		Endpoints:   []config.ProviderEndpoint{{Name: "primary", Version: 2, Provider: "engorch-openai", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "team-key"}}},
		Models:      []config.ProviderModel{{Name: "writer-model", Version: 2, Provider: "engorch-openai", Model: "deployment/opaque-model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: config.ProviderCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: "{}", ContextWindowTokens: 100, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10, Pricing: &config.ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}}},
		Roles:       map[string]config.ProviderRole{"writer": {Endpoint: "primary", Model: "writer-model", AdapterControlsJSON: "{}", Variant: config.ProviderVariant{Effort: "none"}, RequiredCapabilities: &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}},
	}
	c.Config.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: filepath.Join(t.TempDir(), "opencode-state")}
	if err := os.MkdirAll(c.Config.OpenCode.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.Config.Validate(); err != nil {
		if c.Config.Provider != nil {
			if perr := c.Config.Provider.Validate(); perr != nil {
				t.Fatalf("config validation failed: %v (provider: %v)", err, perr)
			}
		}
		t.Fatal(err)
	}
	return c
}

func isolatedOpenCodeGraphFixture(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	return isolatedWriterControllerFixture(t, c, directGraphFixture())
}

// isolatedWriterControllerFixture bootstraps one replay-validated controller
// through plan acceptance for an explicit graph. Both the isolated and the
// staged OpenCode fixtures share this bootstrap; only their graphs differ.
func isolatedWriterControllerFixture(t *testing.T, c Creation, graph engineeringplan.Graph) (string, Snapshot) {
	t.Helper()
	autonomousGitInit(t, c.Repository.Root)
	repo, err := repository.Discover(context.Background(), c.Repository.Root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	c.Repository = repo
	runID, err := canonical.Hash("harness.run.v1", c)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerstate.Initialize(paths, c.Repository); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Runs, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := paths.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	if c.Config.Version == 2 && c.Config.Planner.Runtime == "fake" {
		planning, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		intent, err := expectedPlanningAccess(planning)
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(path, "planning.access-intent", intent); err != nil {
			t.Fatal(err)
		}
	}
	_ = access.Private
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationWithContextAndRecipe(c.Config, c.Objective, nil, c.Execution)
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	if err := Append(path, "plan.recorded", runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(raw)}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, s
}

// authorizeIsolatedGraphCohortForTest records plan authorization and prepares the
// graph isolation cohort through replay-validated controller events. Callers
// keep their own creation, graph choice and scenario assertions explicit.
func authorizeIsolatedGraphCohortForTest(t *testing.T, path string, s Snapshot) {
	t.Helper()
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareGraphIsolationCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

// isolateWriterTaskForTest confirms one task isolation cohort and returns the
// admitted snapshot without admitting writer task context.
func isolateWriterTaskForTest(t *testing.T, path, taskID string) Snapshot {
	t.Helper()
	if _, err := CreateTaskIsolation(context.Background(), path, taskID); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// isolateAndAdmitWriterTaskForTest confirms one task isolation cohort and
// admits its writer task context, returning the admitted snapshot and the
// exact bounded question used for admission.
func isolateAndAdmitWriterTaskForTest(t *testing.T, path, taskID string) (Snapshot, string) {
	t.Helper()
	s := isolateWriterTaskForTest(t, path, taskID)
	question, err := graphWriterTaskQuestion(s, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := maybeAdmitIsolatedWriterTaskContext(context.Background(), path, taskID, question); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, question
}

// sealOpenCodeOfflineTurnForTest seals one admitted OpenCode invocation with
// genuine offline journals (runtime intent/bound/result plus a finished
// gateway) reusing the existing planner sealing helpers. agent and finalText
// select the sealed transcript, resultHashDomain binds the result receipt, and
// receiptRole labels the provider receipt. It performs no provider calls and
// starts no subprocess beyond the in-process broker. Totals mirror the planner
// positive fixture so usage accounting stays comparable.
func sealOpenCodeOfflineTurnForTest(t *testing.T, runtimePath, gatewayPath string, invocation runtime.Invocation, routing ProviderRouting, repo repository.Identity, agent, finalText, resultHashDomain, receiptRole string, inputTokens, cachedTokens, outputTokens, reasoningTokens int64) (runtime.Result, providerDispatchReceipt, opencoderuntime.Intent) {
	t.Helper()
	root := t.TempDir()
	contextBinding, err := contextbroker.NewBinding(invocation.ID, repo, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	sessionBinding := opencode.ToolSessionBinding{Session: opencode.SessionBinding{IntentID: invocation.ID, ProjectID: "global", Directory: root, Agent: agent, Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort}, ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64)}
	intent := opencoderuntime.Intent{Version: 1, Invocation: invocation, Directory: root, Project: opencode.ProjectExpectation{Directory: root, Mode: opencode.ProjectModeGlobal}, Context: contextBinding, Session: sessionBinding}
	bindingID, err := routing.Gateway.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent.ProviderGatewayBindingID = bindingID
	intent.IntentID, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := opencoderuntime.RecordIntent(runtimePath, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(gatewayPath, "provider.bound", routing.Gateway, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	sessionPath := runtimePath + ".session"
	if _, err := journal.Append(sessionPath, "opencode.tool-session-intent", sessionBinding, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(sessionPath, "opencode.tool-session-observed", struct {
		Binding opencode.ToolSessionBinding `json:"binding"`
		ID      string                      `json:"id"`
	}{sessionBinding, "ses_fixture"}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	brokerPath := runtimePath + ".broker"
	broker, err := contextbroker.Open(brokerPath, contextBinding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Close() })
	contextID, _ := contextBinding.ID()
	dispatch, err := opencode.DispatchForInvocation(invocation, "ses_fixture", "msg_user", agent, root, root)
	if err != nil {
		t.Fatal(err)
	}
	syncIntent := opencode.SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch, BrokerBindingID: contextID, BrokerCatalogID: contextBinding.CatalogID}
	tools := opencode.ToolsConfigurationReceipt{SHA256: strings.Repeat("b", 64), MCPServer: opencode.ToolsMCPServerName, Endpoint: "http://127.0.0.1:43123/mcp", ToolIDs: []string{"engorch_source_list"}, TimeoutMillis: 5000}
	sealExpected := opencode.SynchronousToolTurnSealExpected{Dispatch: syncIntent, Session: sessionBinding, Tools: tools, ExecutableSHA256: strings.Repeat("c", 64), HostRoot: root, MaxOutputTokens: 128, RuntimeOutputTokens: 8}
	sealExpected.Provider = plannerProviderSealForBinding(t, invocation, routing.Gateway, tools)
	paths := opencoderuntime.Paths{Version: 1, Session: sessionPath, Dispatch: runtimePath + ".dispatch", Broker: brokerPath, Seal: runtimePath + ".seal", Gateway: gatewayPath}
	project := opencode.ProjectReceipt{SHA256: strings.Repeat("e", 64), ID: "global", Directory: root, Mode: opencode.ProjectModeGlobal, Worktree: "/"}
	if _, err := opencoderuntime.RecordBound(runtimePath, intent, project, paths, sealExpected); err != nil {
		t.Fatal(err)
	}
	observation := writeSealedToolTurn(t, broker, paths, sealExpected, intent, agent, finalText)
	halfInput, halfCached := inputTokens/2, cachedTokens/2
	halfOutput, halfReasoning := outputTokens/2, reasoningTokens/2
	appendGatewayCallForGeneration(t, gatewayPath, routing.Gateway, 1, observation.Generations[0], halfInput, halfOutput, halfCached, halfReasoning)
	appendGatewayCallForGeneration(t, gatewayPath, routing.Gateway, 2, observation.Generations[1], inputTokens-halfInput, outputTokens-halfOutput, cachedTokens-halfCached, reasoningTokens-halfReasoning)
	record, err := opencoderuntime.Complete(runtimePath, intent)
	if err != nil {
		t.Fatal(err)
	}
	gatewayState, err := providergateway.Inspect(gatewayPath)
	if err != nil || !gatewayState.Finished {
		t.Fatal("sealed gateway did not finish", gatewayState, err)
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	gatewayEvents, err := journal.Read(gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	resultHash, err := canonical.Hash(resultHashDomain, record.Result)
	if err != nil {
		t.Fatal(err)
	}
	last := gatewayState.Calls[len(gatewayState.Calls)-1].Receipt
	receipt := providerDispatchReceipt{Version: 1, Role: receiptRole, InvocationID: invocation.ID, AccessInvocationID: routing.Intent.Reservation.InvocationID, RoutingDecision: routing.Intent.RoutingDecision, RuntimeJournalHead: runtimeEvents[len(runtimeEvents)-1].Hash, GatewayJournalHead: gatewayEvents[len(gatewayEvents)-1].Hash, ResultHash: resultHash, ObservedModel: last.ObservedModel, ObservedProvider: routing.ProviderRole.Model.Provider, Result: record.Result}
	return record.Result, receipt, intent
}

func TestIsolatedOpenCodeWriterUsesChildBinding(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedOpenCodeGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, _ = isolateAndAdmitWriterTaskForTest(t, path, "one")
	invocation, err := writerInvocationForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Profile.Runtime != "opencode-http" {
		t.Fatalf("isolated writer did not retain opencode-http runtime: %+v", invocation.Profile)
	}
	var input struct {
		CandidateID string                           `json:"candidate_id"`
		Isolation   *isolatedWriterInvocationContext `json:"isolation"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	parentID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if childID == parentID || input.CandidateID != childID || input.Isolation == nil || input.Isolation.TaskID != "one" || input.Isolation.ChildCandidateID != childID || input.Isolation.BaseCandidateID != parentID {
		t.Fatalf("opencode writer input was not bound to confirmed child: parent=%s child=%s isolation=%+v", parentID, childID, input.Isolation)
	}
	latest, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	latestParentID, err := latest.Candidate.ID()
	if err != nil || latestParentID != parentID {
		t.Fatalf("isolated opencode invocation changed parent candidate: got %s want %s", latestParentID, parentID)
	}
}

func TestIsolatedOpenCodeChildScopeEnforcement(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedOpenCodeGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, _ = isolateAndAdmitWriterTaskForTest(t, path, "one")
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := writerInvocationForIsolatedTask(s, binding)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Profile.Runtime != "opencode-http" {
		t.Fatalf("expected opencode-http isolated invocation, got %s", invocation.Profile.Runtime)
	}
	task := binding.Task.toEngineeringTask()
	within := []fileeffects.Change{{Path: "file.txt"}}
	if !graphWriterChangesWithinTask(task, within) {
		t.Fatal("in-scope child change was rejected")
	}
	outside := []fileeffects.Change{{Path: "outside.txt"}}
	if graphWriterChangesWithinTask(task, outside) {
		t.Fatal("out-of-scope child change was admitted")
	}
	// The same scope gate guards OpenCode and Codex isolated proposals via
	// validateIsolatedGraphWriterProposal; an out-of-scope change must never
	// become a parent effect.
	if len(task.WritePaths) != 1 || task.WritePaths[0] != "file.txt" {
		t.Fatalf("isolated task scope changed: %+v", task.WritePaths)
	}
}

func TestIsolatedOpenCodeSwappedReceiptsReject(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedOpenCodeGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := isolatedWriterInvocationForReceiptID(s, strings.Repeat("f", 64)); err == nil {
		t.Fatal("unknown isolated receipt ID was resolved")
	}
	// Swapped task identity: a record whose outer TaskID differs from its
	// child proposal TaskID must be rejected without touching effects.
	swapped := GraphWriterRecord{TaskID: "one", Isolated: &IsolatedGraphWriterProposal{Version: 1, TaskID: "other"}}
	seen := map[string]bool{}
	if err := replayIsolatedGraphWriterProposal(&s, swapped, seen); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("swapped isolated task identity was not rejected: %v", err)
	}
	// Journal collision: the same invocation ID recorded twice must be
	// rejected as a duplicate, preserving exactly-once proposal identity.
	invocation, err := writerInvocationForTask(s, "one")
	if err != nil {
		// Before child isolation the task-bound invocation is unavailable;
		// the receipt helper must then also reject.
		if _, _, receiptErr := isolatedWriterInvocationForReceiptID(s, strings.Repeat("a", 64)); receiptErr == nil {
			t.Fatal("receipt resolved without a confirmed child binding")
		}
		return
	}
	_ = invocation
	seen = map[string]bool{"graph-writer-proposal:" + invocation.ID: true}
	record := GraphWriterRecord{TaskID: "one", Isolated: &IsolatedGraphWriterProposal{Version: 1, TaskID: "one", Invocation: invocation}}
	if err := replayIsolatedGraphWriterProposal(&s, record, seen); err == nil {
		t.Fatal("duplicate isolated proposal was not rejected")
	}
}

// twoTaskIsolatedGraphFixture admits two independent implementation tasks so
// two distinct valid task invocations can derive journals simultaneously.
func twoTaskIsolatedGraphFixture() engineeringplan.Graph {
	return engineeringplan.Graph{
		Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "direct pair",
		Tasks: []engineeringplan.Task{
			{ID: "one", Kind: engineeringplan.Implementation, Title: "First change", ScopePaths: []string{"one.txt"}, WritePaths: []string{"one.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "changed"}}, EstimatedSeconds: 10},
			{ID: "two", Kind: engineeringplan.Implementation, Title: "Second change", ScopePaths: []string{"two.txt"}, WritePaths: []string{"two.txt"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "changed"}}, EstimatedSeconds: 10},
			{ID: "verify", Kind: engineeringplan.Verification, Title: "Run native checks", Dependencies: []string{"one", "two"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "check", Description: "configured checks pass"}}, EstimatedSeconds: 30},
			{ID: "review", Kind: engineeringplan.Review, Title: "Review candidate", Dependencies: []string{"verify"}, ScopePaths: []string{"."}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "review", Description: "approve the verified candidate"}}, EstimatedSeconds: 30},
		},
	}
}

func TestIsolatedOpenCodeJournalNamespaceCollisionFree(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedWriterControllerFixture(t, c, twoTaskIsolatedGraphFixture())
	authorizeIsolatedGraphCohortForTest(t, path, s)
	for _, taskID := range []string{"one", "two"} {
		if _, err := CreateTaskIsolation(context.Background(), path, taskID); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, taskID := range []string{"one", "two"} {
		question, err := graphWriterTaskQuestion(s, taskID)
		if err != nil {
			t.Fatal(err)
		}
		if err := maybeAdmitIsolatedWriterTaskContext(context.Background(), path, taskID, question); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := writerInvocationForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := writerInvocationForTask(s, "two")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("distinct tasks derived the same invocation identity")
	}
	// Both stems derive before either journal exists: no racing claimant
	// logic is involved, only the controller-admitted task/invocation binding.
	firstStem, err := providerInvocationJournalStemForSnapshot(path, s, first, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondStem, err := providerInvocationJournalStemForSnapshot(path, s, second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if firstStem != "writer.invocation-"+first.ID || secondStem != "writer.invocation-"+second.ID {
		t.Fatalf("isolated namespace must be deterministic per invocation, got %q and %q", firstStem, secondStem)
	}
	if firstStem == secondStem {
		t.Fatal("two distinct task invocations share one journal namespace")
	}
	for _, stem := range []string{firstStem, secondStem} {
		if _, err := os.Stat(path + "." + stem + ".opencode-runtime.jsonl"); !os.IsNotExist(err) {
			t.Fatalf("namespace %q must derive before its journal exists", stem)
		}
	}
	// The legacy claimant path is preserved exactly for non-admitted turns:
	// with empty journals it still returns the shared bare role stem, which is
	// why isolated writers must never resolve through it.
	legacyFirst, err := providerInvocationJournalStem(path, first, nil)
	if err != nil || legacyFirst != "writer" {
		t.Fatalf("legacy serial stem changed, got %q (%v)", legacyFirst, err)
	}
	legacySecond, err := providerInvocationJournalStem(path, second, nil)
	if err != nil || legacySecond != "writer" {
		t.Fatalf("legacy serial stem changed, got %q (%v)", legacySecond, err)
	}
	if providerRoleJournalStem("writer", nil) != "writer" {
		t.Fatal("legacy role stem changed")
	}
	turn := &taskscheduler.AgentTurnBinding{ParentAgentID: strings.Repeat("a", 64), AgentID: strings.Repeat("b", 64), TurnID: "turn-abc", TurnSequence: 2}
	if providerRoleJournalStem("explorer", turn) != "explorer.turn-turn-abc" {
		t.Fatal("scheduled stem changed")
	}
}

func TestIsolatedOpenCodeUnknownNoResend(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedOpenCodeGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s, question := isolateAndAdmitWriterTaskForTest(t, path, "one")
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := writerInvocationForIsolatedTask(s, binding)
	if err != nil {
		t.Fatal(err)
	}
	stem, err := providerInvocationJournalStemForSnapshot(path, s, invocation, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, gatewayPath := path+"."+stem+".opencode-runtime.jsonl", path+"."+stem+".provider-gateway.jsonl"
	// Fixture credential only: resolving the configured team-key reference
	// must not require a live provider. The offline failure under test is the
	// missing OpenCode host executable, which happens after the durable
	// runtime intent is recorded.
	t.Setenv("ENGORCH_PROVIDER_TEAM_KEY", "fixture-credential")
	// The offline OpenCode host cannot start (no executable), so the first
	// legal wrapper dispatch must fail only after durable agent admission,
	// leaving an UNKNOWN observation with no provider receipt and no proposal.
	// This exercises the actual dispatch wrapper, not a map entry.
	_, firstErr := executeIsolatedOpenCodeWriter(context.Background(), path, "one", invocation, binding, question)
	if firstErr == nil {
		t.Fatal("offline isolated dispatch unexpectedly succeeded without a provider")
	}
	t.Logf("first dispatch failed as required offline: %v", firstErr)
	afterFirst, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, ok := afterFirst.AgentDispatch[invocation.ID]
	if !ok || dispatch.Admission.Invocation != invocation {
		t.Fatal("failing dispatch left no durable agent admission for the invocation")
	}
	if dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusUnknown {
		t.Fatalf("failing dispatch must leave UNKNOWN, got %+v", dispatch.Observation)
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil || len(runtimeEvents) == 0 {
		t.Fatal("failing dispatch must retain its durable runtime intent", err)
	}
	intents := 0
	for _, event := range runtimeEvents {
		if event.Kind == "opencode-runtime.intent" {
			intents++
		}
	}
	if intents != 1 {
		t.Fatalf("failing dispatch must record exactly one runtime intent, got %d", intents)
	}
	gateway, err := providergateway.Inspect(gatewayPath)
	if err != nil || gateway.Binding == nil {
		t.Fatal("failing dispatch must retain its durable gateway binding", err)
	}
	calls, receipts, pending := openCodeGatewayCounts(gateway)
	if calls != 0 || receipts != 0 || pending != 0 {
		t.Fatalf("failing dispatch must send no provider calls, got calls=%d receipts=%d pending=%d", calls, receipts, pending)
	}
	runtimeHead := runtimeEvents[len(runtimeEvents)-1].Hash
	observed := countJournalKind(t, path, "agent.dispatch-observed")
	if observed == 0 {
		t.Fatal("UNKNOWN observation missing from the controller journal")
	}
	if _, ok := afterFirst.ProviderRuntime[invocation.ID]; ok {
		t.Fatal("UNKNOWN dispatch recorded a provider receipt without success")
	}
	if _, ok := afterFirst.GraphWriterResults["one"]; ok {
		t.Fatal("UNKNOWN dispatch recorded a writer proposal without success")
	}
	// A repeated legal call must not resend: recovery-only failure keeps the
	// runtime intent count, gateway call counts and observation counts frozen,
	// and the dispatch remains UNKNOWN with no receipt.
	if _, err := executeIsolatedOpenCodeWriter(context.Background(), path, "one", invocation, binding, question); err == nil {
		t.Fatal("offline isolated resume unexpectedly succeeded without a provider")
	} else if !errors.Is(err, opencoderuntime.ErrRecoveryRequired) {
		t.Fatalf("resume must fail recovery-only with no resend, got %v", err)
	}
	afterSecond, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	resumed, ok := afterSecond.AgentDispatch[invocation.ID]
	if !ok || resumed.Observation == nil || resumed.Observation.Status != agenttree.StatusUnknown {
		t.Fatal("resume must preserve the UNKNOWN observation without success")
	}
	runtimeAgain, err := journal.Read(runtimePath)
	if err != nil || len(runtimeAgain) != len(runtimeEvents) || runtimeAgain[len(runtimeAgain)-1].Hash != runtimeHead {
		t.Fatal("resume changed the durable runtime journal; uncertain effects were re-driven")
	}
	gatewayAgain, err := providergateway.Inspect(gatewayPath)
	if err != nil {
		t.Fatal(err)
	}
	callsAgain, receiptsAgain, pendingAgain := openCodeGatewayCounts(gatewayAgain)
	if callsAgain != calls || receiptsAgain != receipts || pendingAgain != pending {
		t.Fatal("resume changed gateway counts; provider effects were resent")
	}
	if countJournalKind(t, path, "agent.dispatch-observed") != observed {
		t.Fatal("resume appended dispatch observations without success")
	}
	if _, ok := afterSecond.ProviderRuntime[invocation.ID]; ok {
		t.Fatal("resume recorded a provider receipt without success")
	}
	if _, ok := afterSecond.GraphWriterResults["one"]; ok {
		t.Fatal("resume recorded a writer proposal without success")
	}
}

func stagedOpenCodeGraphFixture(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	return isolatedWriterControllerFixture(t, c, stagedHubLeavesGraphFixture())
}

func TestStagedOpenCodeDriverAdmitsChildCohort(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	c.Execution.IsolatedImplementationVersion = 3
	c.Execution.MaxParallel = 2
	c.Config.PlannerContract = plannerContractGraphV8
	c = applyTwoSlotIsolationCapacity(t, c)
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path, s := stagedOpenCodeGraphFixture(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareStagedCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStagedBindings(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(s)
	if err != nil || len(ready) == 0 {
		t.Fatalf("staged cohort not ready: %v %d", err, len(ready))
	}
	for _, task := range ready {
		if task.Kind != engineeringplan.Implementation {
			continue
		}
		invocation, err := writerInvocationForTask(s, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if invocation.Profile.Runtime != "opencode-http" {
			t.Fatalf("staged writer %s did not retain opencode-http: %+v", task.ID, invocation.Profile)
		}
	}
	first := ""
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			first = task.ID
			break
		}
	}
	invocation, err := writerInvocationForTask(s, first)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Task: taskscheduler.TaskSpec{ID: first, RunID: s.RunID, ControllerPath: path, Operation: taskscheduler.OperationWriter, InvocationID: invocation.ID}}
	if !isStaticGraphWriterCohort(s, claim) {
		t.Fatal("staged opencode-http writer cohort was not admitted as static")
	}
	// Full staged READY requires a live OpenCode execution with provider
	// qualification; this offline fixture establishes only the deterministic
	// admission, child binding and scheduler seams. Live acceptance is
	// explicitly NOT claimed here.
}

// sealIsolatedWriterTurnForTest seals one admitted isolated writer invocation
// with genuine offline journals (runtime intent/bound/result plus a finished
// gateway) reusing the existing planner sealing helpers. proposalOutput
// becomes the sealed result output. It performs no provider calls and starts
// no subprocess beyond the in-process broker. Totals mirror the planner
// positive fixture so usage accounting stays comparable.
func sealIsolatedWriterTurnForTest(t *testing.T, controllerPath string, invocation runtime.Invocation, proposalOutput string, inputTokens, cachedTokens, outputTokens, reasoningTokens int64) (runtime.Result, providerDispatchReceipt) {
	t.Helper()
	s, err := Inspect(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(s.Creation.Config, s.RunID, "writer", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	stem, err := providerInvocationJournalStemForSnapshot(controllerPath, s, invocation, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, gatewayPath := controllerPath+"."+stem+".opencode-runtime.jsonl", controllerPath+"."+stem+".provider-gateway.jsonl"
	result, receipt, _ := sealOpenCodeOfflineTurnForTest(t, runtimePath, gatewayPath, invocation, routing, s.Creation.Repository, "build", proposalOutput, "harness.writer-result.v1", "writer", inputTokens, cachedTokens, outputTokens, reasoningTokens)
	return result, receipt
}

// installIsolatedWriterSealHook substitutes only the provider-effect execution
// with the genuine offline sealed journals above. The hook honors the exact
// deterministic namespace resolved by the wrapper, so execution, receipt
// replay and usage verification share identical paths. It is test-only,
// restored on cleanup, and involves no external model.
func installIsolatedWriterSealHook(t *testing.T, proposalOutput string) {
	t.Helper()
	previous := isolatedOpenCodeRuntimeForTest
	isolatedOpenCodeRuntimeForTest = func(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation, lease opencoderuntime.CandidateLease, journalStem string) (runtime.Result, providerDispatchReceipt, error) {
		admitted, err := Inspect(controllerPath)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		expected, err := providerInvocationJournalStemForSnapshot(controllerPath, admitted, invocation, nil)
		if err != nil || expected != journalStem {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("isolated test seal must honor the wrapper-resolved journal namespace exactly")
		}
		result, receipt := sealIsolatedWriterTurnForTest(t, controllerPath, invocation, proposalOutput, 48, 16, 12, 4)
		return result, receipt, nil
	}
	t.Cleanup(func() { isolatedOpenCodeRuntimeForTest = previous })
}

func TestIsolatedOpenCodeRunWriterSealedChain(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	path, s := isolatedOpenCodeGraphFixture(t, c)
	authorizeIsolatedGraphCohortForTest(t, path, s)
	s = isolateWriterTaskForTest(t, path, "one")
	binding, err := isolatedWriterBindingForTask(s, "one")
	if err != nil {
		t.Fatal(err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, err := worktree.Capture(context.Background(), binding.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	var beforeHash string
	for _, state := range manifest {
		if state.Path == "file.txt" {
			beforeHash = state.Hash
		}
	}
	if beforeHash == "" {
		t.Fatal("fixture child worktree lacks file.txt for the anchored edit")
	}
	proposalOutput := `{"candidate_id":` + strconv.Quote(childID) + `,"changes":[{"path":"file.txt","before_hash":` + strconv.Quote(beforeHash) + `,"edits":[{"before":"base\n","after":"base\nisolated writer fixture\n"}],"new_content_utf8":null,"executable":false}]}`
	installIsolatedWriterSealHook(t, proposalOutput)
	record, err := RunWriter(withGraphWriterTask(context.Background(), "one"), path)
	if err != nil {
		t.Fatal(err)
	}
	invocation := record.Invocation
	if invocation.Profile.Runtime != "opencode-http" || invocation.Profile.Role != "writer" {
		t.Fatalf("sealed chain ran the wrong role route: %+v", invocation.Profile)
	}
	decoded, err := decodeAnchoredProposal(record.Result.Output)
	if err != nil || decoded.CandidateID != childID {
		t.Fatalf("sealed result is not bound to the child candidate: %v", err)
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := after.ProviderRuntime[invocation.ID]
	if !ok {
		t.Fatal("sealed execution recorded no provider receipt")
	}
	question, err := graphWriterTaskQuestion(after, "one")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Question != question || receipt.InvocationID != invocation.ID || receipt.Role != "writer" {
		t.Fatalf("provider receipt identity mismatch: %+v", receipt)
	}
	if err := requireOpenCodeRoleReceipt(after, invocation, record.Result); err != nil {
		t.Fatal(err)
	}
	stored, ok := after.GraphWriterResults["one"]
	if !ok || stored.TaskID != "one" || stored.Isolated == nil || stored.Isolated.Invocation != invocation {
		t.Fatal("sealed execution recorded no task-bound isolated proposal")
	}
	if err := validateIsolatedGraphWriterProposal(after, *stored.Isolated); err != nil {
		t.Fatal(err)
	}
	dispatch, ok := after.AgentDispatch[invocation.ID]
	if !ok || dispatch.Observation == nil || dispatch.Observation.Status != agenttree.StatusSucceeded || dispatch.Observation.ResultSHA256 != receipt.ResultHash {
		t.Fatal("sealed execution left no successful agent dispatch observation")
	}
	if len(after.GraphWriterHosts) != 0 {
		t.Fatal("isolated OpenCode execution must not engage the legacy Codex host path")
	}
	report, err := MeasureRunUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OpenCodeInvocations) != 1 {
		t.Fatalf("usage must report exactly the sealed writer turn, got %+v", report.OpenCodeInvocations)
	}
	entry := report.OpenCodeInvocations[0]
	if entry.InvocationID != invocation.ID || entry.Role != "writer" || !entry.ReceiptMatched || entry.Status != "completed" || !entry.JournalPresent || entry.Usage == nil {
		t.Fatalf("usage entry is not receipt-matched completed with totals: %+v", entry)
	}
	if entry.Usage.InputTokens != 48 || entry.Usage.CachedInputTokens == nil || *entry.Usage.CachedInputTokens != 16 || entry.Usage.UncachedInputTokens == nil || *entry.Usage.UncachedInputTokens != 32 || entry.Usage.OutputTokens != 12 || entry.Usage.ReasoningTokens == nil || *entry.Usage.ReasoningTokens != 4 || entry.Usage.OrdinaryOutputTokens == nil || *entry.Usage.OrdinaryOutputTokens != 8 {
		t.Fatalf("usage totals must match the sealed gateway aggregate: %+v", entry.Usage)
	}
	if entry.GatewayCalls == nil || *entry.GatewayCalls != 2 || entry.GatewayReceipts == nil || *entry.GatewayReceipts != 2 || entry.GatewayPending == nil || *entry.GatewayPending != 0 {
		t.Fatalf("gateway counts must expose the two sealed calls: %+v", entry)
	}
	// Offline fixtures establish the execution-to-usage chain only; live
	// provider acceptance is explicitly NOT claimed here.
}

func TestStagedOpenCodeRunWriterSealedChain(t *testing.T) {
	c := isolatedOpenCodeCreation(t)
	c.Execution.IsolatedImplementationVersion = 3
	c.Execution.MaxParallel = 2
	c.Config.PlannerContract = plannerContractGraphV8
	c = applyTwoSlotIsolationCapacity(t, c)
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path, s := stagedOpenCodeGraphFixture(t, c)
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareStagedCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStagedBindings(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(s)
	if err != nil {
		t.Fatal(err)
	}
	hub := ""
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			hub = task.ID
			break
		}
	}
	if hub == "" {
		t.Fatal("staged cohort has no ready hub implementation")
	}
	binding, err := stagedForkBindingForTask(s, hub)
	if err != nil {
		t.Fatal(err)
	}
	childID, err := binding.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	// The hub fixture file does not exist yet, so the staged branch exercises
	// the new-file anchored shape through the actual staged driver.
	proposalOutput := `{"candidate_id":` + strconv.Quote(childID) + `,"changes":[{"path":"alpha.txt","before_hash":null,"edits":[],"new_content_utf8":"staged hub fixture\n","executable":false}]}`
	installIsolatedWriterSealHook(t, proposalOutput)
	record, err := RunWriter(withGraphWriterTask(context.Background(), hub), path)
	if err != nil {
		t.Fatal(err)
	}
	invocation := record.Invocation
	if invocation.Profile.Runtime != "opencode-http" {
		t.Fatalf("staged chain ran the wrong runtime: %+v", invocation.Profile)
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := after.ProviderRuntime[invocation.ID]
	if !ok || receipt.InvocationID != invocation.ID {
		t.Fatal("staged execution recorded no provider receipt")
	}
	if _, taskID, err := isolatedWriterInvocationForReceiptID(after, invocation.ID); err != nil || taskID != hub {
		t.Fatalf("staged receipt task binding mismatch: %s %v", taskID, err)
	}
	stored, ok := after.GraphWriterResults[hub]
	if !ok || stored.Isolated == nil {
		t.Fatal("staged execution recorded no task-bound isolated proposal")
	}
	if err := validateIsolatedGraphWriterProposal(after, *stored.Isolated); err != nil {
		t.Fatal(err)
	}
	report, err := MeasureRunUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range report.OpenCodeInvocations {
		if entry.InvocationID != invocation.ID {
			continue
		}
		found = true
		if !entry.ReceiptMatched || entry.Status != "completed" || entry.Usage == nil || entry.Usage.InputTokens != 48 || entry.Usage.OutputTokens != 12 {
			t.Fatalf("staged usage entry is not receipt-matched completed: %+v", entry)
		}
	}
	if !found {
		t.Fatalf("staged usage omits the sealed hub turn: %+v", report.OpenCodeInvocations)
	}
	// Offline fixtures establish the staged hub execution-to-usage chain only;
	// leaf integration and live provider acceptance are NOT claimed here.
}
