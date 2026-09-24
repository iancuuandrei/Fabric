package control

// Live qualification of the existing recursive explorer machinery against
// REAL Muse Spark 1.3 Contributor Free.
//
// Architecture mirrors TestPinnedOpenCodeScheduledRecursiveExplorer, but the
// provider is the genuine engorch-opencode-zen route instead of the scripted
// fixture. Because live model text is nondeterministic, every assertion here
// is structural: exact identities, topology, separate runtimes, receipted
// tool use, exactly-once accepted-result delivery, settled usage/capacity,
// and offline recovery without new provider effects. No assertion depends on
// model wording, timing, or call counts beyond bounded minima.
//
// Privacy: the source candidate is the PUBLIC EngOrch S0 tree only. The test
// consumes no private evidence and transmits no credentials beyond the
// provider bearer (never logged or asserted on).
//
// Live gate: ENGORCH_MUSE_RECURSIVE_LIVE=1 plus ENGORCH_M0_ZEN_KEY. Without
// both, the test skips with no external effect.

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/taskscheduler"
)

const (
	museRecursiveLiveFlag = "ENGORCH_MUSE_RECURSIVE_LIVE"
	museRecursiveKeyEnv   = "ENGORCH_M0_ZEN_KEY"
	// Optional durable forensics: when set to an ABSENT directory, a failed
	// live run copies its whole TempDir journal tree there before the
	// framework removes it. R39 proved failures without this destroy the
	// only transcript evidence. Read-only; never affects dispatch.
	museRecursiveKeepEnv  = "ENGORCH_MUSE_KEEP_JOURNALS"
	museRecursiveProvider = "engorch-opencode-zen"
	// R41: paid SKU per human direction 2026-09-09 (free-tier pool throttled
	// with identical 429s on two keys).
	// R42 correction: the paid SKU is NOT served on the zen endpoint (401
	// ModelError "not supported", proven live) but on the Go endpoint below
	// (go catalog lists it; HTTP 200 with session header, proven live).
	// Same provider ID, same Responses adapter, same route shape; only the
	// endpoint URL, session header, and model name change. Free-route
	// evidence (M2y PASS, R31 tool_choice=auto exception) does NOT transfer:
	// the qual binary sends tool_choice=required for this pair (observable
	// pre-effect on mismatch), and cost accounting underwrites zero (spend
	// is the operator's subscription).
	museRecursiveModel      = "muse-spark-1.3-contributor"
	museRecursiveEndpoint   = "https://opencode.ai/zen/go/v1/responses"
	museRecursiveBaseCommit = "449263cd38347e5bf49999deda8b5f557917c853"
	museRecursiveS0Source   = "D:/dev/EngOrch-M2/bootstrap"
)

func museRecursiveQualificationBinary(t *testing.T) (string, string) {
	t.Helper()
	const path = "D:/dev/EngOrch-R31/build/opencode-r31-qualification-windows-x64.exe"
	const digest = "8d46d1ba058b597f739a579ffd4b68f2fa06e9627c1f4f801405fde3db3c61b2"
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("qualification OpenCode binary unavailable: %v", err)
	}
	return path, digest
}

func museRecursiveCommonCreation(t *testing.T, stateRoot, taskPoolPath string) Creation {
	t.Helper()
	// R43: FunctionCallDoneNameV1 reflects observed Go-endpoint behavior: the
	// paid route includes `name` in response.function_call_arguments.done
	// (R42 live evidence: exact-keys rejection "unsupported fields" on the
	// 6-key done event). Without the flag the SSE decoder demands the
	// 5-key free-route shape. SSE framing only; JSON-framing gates that
	// forbid this flag are untouched and unreached here.
	// R45: ContentPartCompletesText reflects further observed Go-endpoint
	// behavior: paid turns may emit response.content_part.done with NO
	// preceding response.output_text.done (R44 live evidence on BOTH
	// turns: "content-part done snapshot differs"). The free-route M2y
	// envelope already declares this flag true. Same SSE-only effect.
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{Version: 1, FunctionTools: true, Reasoning: true, SystemRoles: []string{"developer", "system"}, TextFormats: []string{"plain"}, KnownExtensions: []string{"prompt_cache_key"}, TrailingCostPingV1: true, FunctionCallDoneNameV1: true, ContentPartCompletesText: true})
	if err != nil {
		t.Fatal(err)
	}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 131072, StateMode: "full-input-stateless", Store: false, SystemRole: "system", ReasoningEffort: "", ReasoningSummary: "", ToolChoice: "auto", TextVerbosity: "", Include: []string{}, FunctionToolStrict: false})
	if err != nil {
		t.Fatal(err)
	}
	// R46: MaxCalls 24 bounds (not spends) the wait-poll budget: spawn plus
	// ~20 finite waits cover a multi-minute child turn. Reservation scales
	// automatically via RoleReservation below.
	// R47: MaxRequestBytes 256KB -> 1MB (protocol max). Explorer turns over
	// a real repo accumulate full history per request (15K->36K->58K->202K
	// observed; 283KB tool content in one turn); the 5th request crossed
	// 256KB and died proxy-400 with no journal trace, killing the turn via
	// an error assistant. 1MB gives ~3x headroom over observed worst case.
	model := config.ProviderModel{Name: "muse-recursive-explorer", Version: 2, Provider: museRecursiveProvider, Model: museRecursiveModel, AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: config.ProviderCapabilities{Tools: true, Reasoning: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: string(capabilities), ContextWindowTokens: 1048576, MaxCalls: 24, MaxRequestBytes: 1048576, MaxResponseBytes: 1048576, MaxOutputTokens: 131072, Pricing: &config.ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 0, MaxOutputMicroUSDPerMillion: 0}}
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
	creation.Objective = "Recursive explorer qualification on public EngOrch source: read-only inspection only, no writes."
	explorer := runtime.Profile{Runtime: "opencode-http", Provider: museRecursiveProvider, Model: museRecursiveModel, Effort: "none", Role: "explorer"}
	creation.Config.Explorer = &explorer
	var zeroCost int64
	creation.Config.Access = &config.Access{
		Class:  access.Public,
		Limits: access.Limits{Tokens: 100 + 2*reservation, Concurrency: 2},
		Profiles: []access.Profile{
			{Version: 1, Name: "muse-explorer", Kind: "api", Runtime: "opencode-http", Provider: museRecursiveProvider, CredentialRef: "muse-zen-key", RepositoryClasses: []access.Class{access.Public}},
		},
		Roles:       map[string]string{"explorer": "muse-explorer"},
		Invocations: map[string]config.InvocationLimit{"explorer": {Tokens: reservation, CostMicroUSD: &zeroCost}},
	}
	// The planner leg is attached separately by
	// museRecursiveScriptedPlannerCreation: scaffolding only, so it uses the
	// pinned blueprint's canned local planner, never a live model call.
	creation.Config.Provider = &config.Provider{
		Version:     1,
		Credentials: []config.ProviderCredential{{Ref: "muse-zen-key", Environment: museRecursiveKeyEnv}},
		Endpoints: []config.ProviderEndpoint{
			// R42: Go endpoint requires x-opencode-session (400
			// MissingSessionID without it, proven live). The transport
			// derives the value deterministically per invocation; the
			// canned planner endpoint below needs no session header.
			{Name: "muse-zen-responses", Version: 2, Provider: museRecursiveProvider, URL: museRecursiveEndpoint, AdapterID: providergateway.OpenAIResponsesAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "muse-zen-key"}, SessionHeader: "x-opencode-session"},
		},
		Models: []config.ProviderModel{model},
		Roles:  map[string]config.ProviderRole{"explorer": {Endpoint: "muse-zen-responses", Model: "muse-recursive-explorer", AdapterControlsJSON: string(controls), Variant: config.ProviderVariant{Effort: "none", SystemRole: "system"}, RequiredCapabilities: &config.ProviderRequiredCapabilities{Tools: true, Reasoning: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}},
	}
	return creation
}

func museRecursiveCreation(t *testing.T, stateRoot, taskPoolPath string) Creation {
	t.Helper()
	creation := museRecursiveCommonCreation(t, stateRoot, taskPoolPath)
	var err error
	executable, digest := museRecursiveQualificationBinary(t)
	creation.Config.OpenCode = &config.OpenCodeHost{Version: 1, Executable: executable, ExecutableHash: digest, StateRoot: stateRoot}
	creation.Config.TaskPool = &config.TaskPool{Version: 1, Path: taskPoolPath, Limits: taskpool.Limits{Total: 2}}
	sourceRoot := t.TempDir()
	clone := exec.Command("git", "clone", "--local", museRecursiveS0Source, sourceRoot)
	if output, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("public S0 clone failed: %v: %s", err, output)
	}
	for _, args := range [][]string{{"-C", sourceRoot, "checkout", "-q", museRecursiveBaseCommit}, {"-C", sourceRoot, "remote", "remove", "origin"}} {
		command := exec.Command("git", args...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("S0 checkout failed: %v: %s", err, output)
		}
	}
	creation.Repository, err = repository.Discover(context.Background(), sourceRoot, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if creation.Repository.Commit != museRecursiveBaseCommit {
		t.Fatalf("S0 candidate commit changed: %s", creation.Repository.Commit)
	}
	return creation
}

func museRecursiveFixtureCreation(t *testing.T, stateRoot, taskPoolPath string) Creation {
	t.Helper()
	creation := museRecursiveCommonCreation(t, stateRoot, taskPoolPath)
	absent := filepath.Join(t.TempDir(), "opencode-absent")
	creation.Config.OpenCode = &config.OpenCodeHost{Version: 1, Executable: absent, ExecutableHash: "8d46d1ba058b597f739a579ffd4b68f2fa06e9627c1f4f801405fde3db3c61b2", StateRoot: stateRoot}
	creation.Config.TaskPool = &config.TaskPool{Version: 1, Path: taskPoolPath, Limits: taskpool.Limits{Total: 2}}
	repoRoot := t.TempDir()
	if output, err := exec.Command("git", "init", repoRoot).CombinedOutput(); err != nil {
		t.Fatalf("fixture git init failed: %v: %s", err, output)
	}
	for _, args := range [][]string{{"-C", repoRoot, "config", "user.email", "fixture@example.com"}, {"-C", repoRoot, "config", "user.name", "fixture"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture git config failed: %v: %s", err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "fixture.txt"), []byte("offline fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", repoRoot, "add", "fixture.txt").CombinedOutput(); err != nil {
		t.Fatalf("fixture git add failed: %v: %s", err, output)
	}
	if output, err := exec.Command("git", "-C", repoRoot, "commit", "-m", "offline fixture").CombinedOutput(); err != nil {
		t.Fatalf("fixture git commit failed: %v: %s", err, output)
	}
	var err error
	creation.Repository, err = repository.Discover(context.Background(), repoRoot, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", creation.Repository.Root, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("fixture rev-parse failed: %v: %s", err, output)
	}
	head := strings.TrimSpace(string(output))
	if head != creation.Repository.Commit {
		t.Fatalf("fixture HEAD mismatch: %q vs %q", head, creation.Repository.Commit)
	}
	if head == museRecursiveBaseCommit {
		t.Fatalf("fixture HEAD equals base commit: %s", head)
	}
	return creation
}

// museRecursiveScriptedPlannerCreation attaches the planner scaffolding leg
// using the pinned blueprint's canned local planner (one fixed SSE plan,
// zero live model calls). R38 proved a live OpenCode+Muse *text* planner
// turn is unqualified territory (nondeterministic finish: the model may
// emit tool calls under tool_choice=auto where DecodeAssistant demands
// stop). The planner is setup only; parent/child explorers stay on the
// exact real Muse route under test. Mirrors
// scheduledOpenCodeInterruptCreation; TaskPool stays Total 2 per the Phase 3
// minimum (the blueprint's 1 does not apply here).
func museRecursiveScriptedPlannerCreation(t *testing.T, creation Creation, plannerURL, taskPoolPath string) Creation {
	t.Helper()
	// Profile Model must equal the planner model entry's provider-side Model
	// (provider.go identity check), and that name must equal the canned
	// server's snapshot model: AcceptsObservedModel admits only exact match
	// or explicit v2 alias. The blueprint uses the wire model name here for
	// the same reason; R39 preflight caught the mismatch offline.
	plannerProfile := runtime.Profile{Runtime: "provider-api", Provider: "openai", Model: scheduledOpenCodeLiveModel, Effort: "none", Role: "planner"}
	creation.Config.Planner = plannerProfile
	creation.Config.Provider.Credentials = append(creation.Config.Provider.Credentials, config.ProviderCredential{Ref: "control-live-key", Environment: "ENGORCH_CONTROL_OPENCODE_KEY"})
	creation.Config.Provider.Endpoints = append(creation.Config.Provider.Endpoints, config.ProviderEndpoint{Name: "muse-planner-responses", Version: 2, Provider: plannerProfile.Provider, URL: plannerURL, AdapterID: providergateway.OpenAIResponsesAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "control-live-key"}})
	plannerModel := creation.Config.Provider.Models[0]
	plannerModel.Name = "muse-recursive-planner-model"
	plannerModel.Provider = plannerProfile.Provider
	plannerModel.Model = scheduledOpenCodeLiveModel
	creation.Config.Provider.Models = append(creation.Config.Provider.Models, plannerModel)
	explorerRole := creation.Config.Provider.Roles["explorer"]
	plannerRole := explorerRole
	plannerRole.Endpoint = "muse-planner-responses"
	plannerRole.Model = plannerModel.Name
	plannerRole.Variant = config.ProviderVariant{Effort: "none", SystemRole: "system"}
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
	creation.Config.Access.Profiles = append(creation.Config.Access.Profiles,
		access.Profile{Version: 1, Name: "fixture-planner", Kind: "subscription", Runtime: "provider-api", Provider: plannerProfile.Provider, CredentialRef: "control-live-key", RepositoryClasses: []access.Class{access.Public}})
	creation.Config.Access.Roles["planner"] = "fixture-planner"
	plannerReservation, _, err := creation.Config.Provider.RoleReservation("planner")
	if err != nil {
		t.Fatal(err)
	}
	explorerReservation := creation.Config.Access.Invocations["explorer"].Tokens
	creation.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: plannerReservation}
	// R52: three explorer reservations (parent + child + one same-child
	// follow-up turn). R51 live proved the core with two but starved the
	// follow-up at admission ("budget exhausted", zero provider effects),
	// failing an otherwise-green run.
	creation.Config.Access.Limits.Tokens = plannerReservation + 3*explorerReservation
	creation.Config.TaskPool = &config.TaskPool{Version: 1, Path: taskPoolPath, Limits: taskpool.Limits{Total: 2}}
	return creation
}

func decodeMuseExploration(t *testing.T, output string) Exploration {
	t.Helper()
	var exploration Exploration
	if err := canonical.Decode([]byte(output), &exploration); err != nil {
		t.Fatalf("exploration output undecodable: %v", err)
	}
	return exploration
}

type museGatewayTally struct {
	intents  int
	receipts int
	failures int
	finished bool
	input    int64
	output   int64
}

func museGatewayTallyFor(t *testing.T, controllerPath string, turn taskscheduler.DynamicTask) museGatewayTally {
	t.Helper()
	path := scheduledRecursiveRuntimePath(controllerPath, turn)
	gatewayPath := path[:len(path)-len(".opencode-runtime.jsonl")] + ".provider-gateway.jsonl"
	gateway, err := providergateway.Inspect(gatewayPath)
	if err != nil {
		t.Fatal(gatewayErr(gatewayPath, err))
	}
	var tally museGatewayTally
	tally.finished = gateway.Finished
	tally.input = int64(gateway.Aggregate.InputTokens)
	tally.output = int64(gateway.Aggregate.OutputTokens)
	for _, call := range gateway.Calls {
		tally.intents++
		if call.Receipt != nil {
			tally.receipts++
		}
		if call.Failure != nil {
			tally.failures++
		}
	}
	return tally
}

func gatewayErr(path string, err error) error {
	return errors.Join(errors.New("gateway inspect unavailable at "+path), err)
}

// museLogFailureState records structural forensics (counts and statuses
// only, never bodies or credentials) to the test log so a live failure
// leaves durable evidence even if journal preservation is disabled.
func museLogFailureState(t *testing.T, controllerPath, schedulerPath string) {
	t.Helper()
	if scheduled, err := taskscheduler.Inspect(schedulerPath); err != nil {
		t.Logf("forensic: scheduler inspect failed: %v", err)
	} else {
		for id, state := range scheduled.Tasks {
			claim := state.Claim != nil
			t.Logf("forensic: task %.12s status=%s claim=%v", id, state.Status, claim)
		}
		t.Logf("forensic: dynamic turns=%d", len(scheduled.Dynamic))
	}
	controller, err := Inspect(controllerPath)
	if err != nil {
		t.Logf("forensic: controller inspect failed: %v", err)
		return
	}
	t.Logf("forensic: explorations=%d dispatches=%d", len(controller.Explorations), len(controller.AgentDispatch))
	matches, err := filepath.Glob(controllerPath + ".explorer.turn-*.opencode-runtime.jsonl")
	if err != nil {
		t.Logf("forensic: runtime glob failed: %v", err)
		return
	}
	for _, path := range matches {
		gatewayPath := path[:len(path)-len(".opencode-runtime.jsonl")] + ".provider-gateway.jsonl"
		gateway, err := providergateway.Inspect(gatewayPath)
		if err != nil {
			t.Logf("forensic: gateway %.40s unavailable: %v", gatewayPath, err)
			continue
		}
		intents, receipts, failures := 0, 0, 0
		for _, call := range gateway.Calls {
			intents++
			if call.Receipt != nil {
				receipts++
			}
			if call.Failure != nil {
				failures++
			}
		}
		t.Logf("forensic: gateway %.40s intents=%d receipts=%d failures=%d finished=%v in=%d out=%d",
			gatewayPath, intents, receipts, failures, gateway.Finished,
			gateway.Aggregate.InputTokens, gateway.Aggregate.OutputTokens)
	}
}

// musePreserveJournals copies the live run's TempDir journal tree to an
// operator-selected directory when the test failed. Deferred after TempDir
// creation, it runs before the framework's TempDir cleanup (LIFO). Best
// effort: copy errors are logged, never fatal. Journals contain no bearer
// material by construction (credential leases bind environment names, never
// values); the destination must still be treated as private qualification
// evidence, never Muse model context.
func musePreserveJournals(t *testing.T, root string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	dest := os.Getenv(museRecursiveKeepEnv)
	if dest == "" {
		return
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Logf("keep-journals destination must be absent, skipping preservation: %s", dest)
		return
	}
	var files, bytes int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return err
		}
		files++
		bytes += int64(len(data))
		return nil
	})
	if err != nil {
		t.Logf("journal preservation incomplete: %v", err)
		return
	}
	t.Logf("preserved %d journal files (%d bytes) to %s", files, bytes, dest)
}

func TestMuseRecursiveFixturePreflight(t *testing.T) {
	// Fully offline: builds the live-test scaffolding (contract, reservation,
	// S0 clone, canned planner, full prepare with zero live model calls) and
	// stops before dispatch. Catches constructor and config errors before
	// any live call. Mirrors TestScheduledOpenCodeRecursiveFixturePreflight;
	// only the explorer legs touch the real Muse route.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	planner := &scheduledRecursivePlannerProvider{}
	plannerUpstream := httptest.NewUnstartedServer(planner)
	plannerUpstream.EnableHTTP2 = false
	plannerUpstream.StartTLS()
	defer plannerUpstream.Close()
	root := t.TempDir()
	controllerPath, schedulerPath := filepath.Join(root, "controller.jsonl"), filepath.Join(root, "scheduler.jsonl")
	creation := museRecursiveCreation(t, filepath.Join(root, "opencode-state"), filepath.Join(root, "task-pool.jsonl"))
	creation = museRecursiveScriptedPlannerCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	if creation.Repository.Commit != museRecursiveBaseCommit {
		t.Fatal("S0 candidate commit changed")
	}
	explorer := creation.Config.Explorer
	if explorer == nil || explorer.Runtime == "fake" {
		t.Fatal("explorer profile missing or fake", explorer)
	}
	if explorer.Runtime != "opencode-http" || explorer.Provider != museRecursiveProvider || explorer.Model != museRecursiveModel || explorer.Role != "explorer" {
		t.Fatal("explorer is not the exact Muse route", explorer)
	}
	if creation.Config.Planner.Runtime != "provider-api" || creation.Config.Planner.Role != "planner" {
		t.Fatal("planner is not the canned scaffolding leg", creation.Config.Planner)
	}
	if creation.Config.TaskPool == nil || creation.Config.TaskPool.Limits.Total < 2 {
		t.Fatal("task pool capacity below recursive minimum")
	}
	for _, model := range creation.Config.Provider.Models {
		if model.MaxCalls < 2 {
			t.Fatal("model call budget too small", model.Name)
		}
		if model.Pricing == nil {
			t.Fatal("model lacks pricing ceiling", model.Name)
		}
	}
	// R46/R49: the advertised wait_agent schema maximum must equal the
	// enforced controller cap. R45 live proved models pass minute-scale
	// timeouts; a schema/controller mismatch fails them closed instantly
	// and kills the turn (error tool parts are fatal). R49: cap is 25000
	// because the OpenCode MCP client kills tool calls at 30s (R48 live:
	// admitted 45s wait died -32001 client-side); the 60s owned-bridge
	// ceiling then has ample margin. Bridge/client literals live outside
	// this package and are covered by review, not assertable from here.
	if agentToolMaximumWaitMillis != 25000 {
		t.Fatal("wait cap changed without updating schema, client/bridge margins, and preflight")
	}
	catalog, err := agentToolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range catalog {
		if definition.Name != "wait_agent" {
			continue
		}
		var schema struct {
			Properties struct {
				Timeout struct {
					Maximum int `json:"maximum"`
					Minimum int `json:"minimum"`
				} `json:"timeout_milliseconds"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Properties.Timeout.Maximum != agentToolMaximumWaitMillis || schema.Properties.Timeout.Minimum != 1 {
			t.Fatal("wait schema bound differs from enforced cap", schema.Properties.Timeout)
		}
	}
	// R38: every provider-backed role must carry request controls whose
	// output cap equals its model cap. providerConfiguration rejects any
	// mismatch pre-effect ("OpenAI Responses runtime output cap differs");
	// R37 burned a live attempt on exactly this constructor error.
	for _, role := range []string{"planner", "explorer"} {
		resolved, err := creation.Config.ResolveProviderRole(role)
		if err != nil {
			t.Fatal("provider role unresolvable", role, err)
		}
		var controls providergateway.ResponsesRequestExpectation
		if err := canonical.Decode(resolved.AdapterControls, &controls); err != nil {
			t.Fatal("role controls undecodable", role, err)
		}
		if controls.MaxOutputTokens != resolved.Model.MaxOutputTokens {
			t.Fatal("role controls output cap differs from model cap", role, controls.MaxOutputTokens, resolved.Model.MaxOutputTokens)
		}
	}
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, withProviderTransportClient(ctx, transport), controllerPath, schedulerPath, creation, adapter,
		"muse-recursive-preflight", "recursive-parent",
		"Spawn one read-only explorer child and wait for its accepted result.", "recursive-parent-nonce")
	state, err := taskscheduler.Inspect(schedulerPath)
	if err != nil || planner.count() != 1 || state.Tasks[prepared.turn.Task.ID].Status != taskscheduler.StatusReady || state.Tasks[prepared.turn.Task.ID].Claim != nil {
		t.Fatal("recursive preflight did not stop before dispatch", err, state.Tasks[prepared.turn.Task.ID])
	}
	if matches, globErr := filepath.Glob(controllerPath + ".explorer.turn-*.opencode-runtime.jsonl"); globErr != nil || len(matches) != 0 {
		t.Fatal("recursive preflight launched an OpenCode runtime", matches, globErr)
	}
}

func TestMuseScheduledRecursiveExplorer(t *testing.T) {
	if os.Getenv(museRecursiveLiveFlag) != "1" {
		t.Skip("explicit real-Muse recursive qualification opt-in required")
	}
	if os.Getenv(museRecursiveKeyEnv) == "" {
		t.Skip("real-Muse qualification requires a provider key")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	// Canned local planner (scaffolding only): exactly one fixed plan, zero
	// live model calls. Trust roots merge system CAs (real Muse endpoint)
	// with the local planner cert. Fixture bearer is test-local only.
	planner := &scheduledRecursivePlannerProvider{}
	plannerUpstream := httptest.NewUnstartedServer(planner)
	plannerUpstream.EnableHTTP2 = false
	plannerUpstream.StartTLS()
	defer plannerUpstream.Close()
	t.Setenv("ENGORCH_CONTROL_OPENCODE_KEY", scheduledOpenCodeLiveSecret)
	roots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(plannerUpstream.Certificate())
	transport, err := providertransport.NewClient(providertransport.ClientOptions{RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	dispatchCtx := withProviderTransportClient(ctx, transport)
	root := t.TempDir()
	defer musePreserveJournals(t, root)
	controllerPath, schedulerPath := filepath.Join(root, "controller.jsonl"), filepath.Join(root, "scheduler.jsonl")
	creation := museRecursiveCreation(t, filepath.Join(root, "opencode-state"), filepath.Join(root, "task-pool.jsonl"))
	creation = museRecursiveScriptedPlannerCreation(t, creation, plannerUpstream.URL+"/v1/responses", filepath.Join(root, "task-pool.jsonl"))
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	prepared := scheduledOpenCodePrepareExplorerTurn(t, dispatchCtx, controllerPath, schedulerPath, creation, adapter,
		"muse-recursive", "recursive-parent",
		"Spawn one read-only explorer child to inspect a distinct part of this repository, wait until its accepted result is available, then synthesize your answer from that exact evidence. Poll wait_agent with timeouts of at most 25000 milliseconds; larger values are rejected, so wait again when nothing is available yet. Use only source_list and source_read; modify nothing. Tell the child that reads larger than the limit come back truncated and unusable, so it must page any file over 30000 bytes with offset and limit until covered. For list_agents pagination, pass after as the empty string on the first call and the exact next_cursor value afterwards; never invent cursor values, since unknown cursors fail the call.",
		"recursive-parent-nonce")
	if planner.count() != 1 {
		t.Fatal("canned planner request count changed", planner.count())
	}
	candidateID, err := prepared.snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}

	// Pump derives from dispatchCtx so explorer dispatches carry the merged
	// trust roots (system CAs for the real Muse endpoint plus the canned
	// planner cert), mirroring the pinned blueprint.
	pumpCtx, stopPump := context.WithCancel(dispatchCtx)
	pumpDone := make(chan error, 1)
	go func() {
		pumpDone <- taskscheduler.Pump(pumpCtx, schedulerPath, adapter, taskscheduler.PumpOptions{Workers: 2, PollInterval: 100 * time.Millisecond})
	}()
	pumpJoined := false
	defer func() {
		if pumpJoined {
			return
		}
		stopPump()
		select {
		case <-pumpDone:
		case <-time.After(30 * time.Second):
			t.Error("recursive scheduler pump did not stop during cleanup")
		}
	}()

	// The child question is model-chosen: poll for a second dynamic turn
	// under the parent instead of asserting predetermined text.
	var childTurn taskscheduler.DynamicTask
	childDeadline := time.Now().Add(12 * time.Minute)
	for {
		scheduled, inspectErr := taskscheduler.Inspect(schedulerPath)
		if inspectErr == nil {
			for _, dynamic := range scheduled.Dynamic {
				if dynamic.Task.ID != prepared.turn.Task.ID && dynamic.ParentAgentID == prepared.turn.AgentID {
					childTurn = dynamic
				}
			}
			if childTurn.Task.ID != "" {
				break
			}
		}
		if time.Now().After(childDeadline) {
			stopPump()
			t.Fatal("real-Muse parent did not spawn a child turn")
		}
		select {
		case pumpErr := <-pumpDone:
			pumpJoined = true
			museLogFailureState(t, controllerPath, schedulerPath)
			t.Fatal("scheduler pump failed before child admission", pumpErr)
		case <-time.After(5 * time.Second):
		}
	}
	// R44: TurnSequence is 1-based (first turn == 1 per AddTask, enforced at
	// agenttree_dispatch.go:164 and scheduled_dispatch.go:274, pinned by
	// scheduler_test.go:384 and the fixture's != 1 check). R43 asserted 0
	// and failed a genuine, correctly-shaped first child turn live.
	if childTurn.AgentID == "" || childTurn.AgentID == prepared.turn.AgentID || childTurn.TurnID != childTurn.Task.ID || childTurn.TurnSequence != 1 {
		stopPump()
		t.Fatal("child turn identity malformed", childTurn)
	}

	settleDeadline := time.Now().Add(30 * time.Minute)
	for {
		scheduled, inspectErr := taskscheduler.Inspect(schedulerPath)
		if inspectErr == nil && scheduled.Tasks[prepared.turn.Task.ID].Status == taskscheduler.StatusSucceeded && scheduled.Tasks[childTurn.Task.ID].Status == taskscheduler.StatusSucceeded {
			break
		}
		if time.Now().After(settleDeadline) {
			stopPump()
			museLogFailureState(t, controllerPath, schedulerPath)
			t.Fatal("recursive scheduler did not settle")
		}
		select {
		case pumpErr := <-pumpDone:
			pumpJoined = true
			museLogFailureState(t, controllerPath, schedulerPath)
			t.Fatal("scheduler pump failed before settlement", pumpErr)
		case <-time.After(5 * time.Second):
		}
	}
	stopPump()
	select {
	case pumpErr := <-pumpDone:
		pumpJoined = true
		if !errors.Is(pumpErr, context.Canceled) {
			t.Fatal("scheduler pump returned unexpectedly", pumpErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("scheduler pump did not stop")
	}

	controller, err := Inspect(controllerPath)
	if err != nil || len(controller.Explorations) != 2 {
		t.Fatal("recursive controller results unavailable", err, len(controller.Explorations))
	}
	parentRecord, childRecord := scheduledRecursiveRecords(controller, prepared.turn.Task.InvocationID, childTurn.Task.InvocationID)
	if parentRecord == nil || childRecord == nil {
		t.Fatal("parent/child exploration records missing")
	}
	for _, record := range []*ExplorerRecord{parentRecord, childRecord} {
		profile := record.Invocation.Profile
		if profile.Runtime != "opencode-http" || profile.Provider != museRecursiveProvider || profile.Model != museRecursiveModel || profile.Role != "explorer" {
			t.Fatal("recursive route is not the exact Muse explorer route", profile)
		}
		if record.Invocation.ID == "" || record.Result.InvocationID != record.Invocation.ID {
			t.Fatal("runtime/result invocation binding broken")
		}
	}
	if parentRecord.Invocation.ID == childRecord.Invocation.ID {
		t.Fatal("parent and child share an invocation")
	}
	childOutput := decodeMuseExploration(t, childRecord.Result.Output)
	if childOutput.CandidateID != candidateID || childOutput.Summary == "" {
		t.Fatal("child exploration lacks candidate binding or synthesis")
	}
	for _, path := range childOutput.Paths {
		if path == "" || strings.Contains(path, "..") || strings.HasPrefix(path, "/") {
			t.Fatal("child exploration path escapes repository", path)
		}
	}
	parentOutput := decodeMuseExploration(t, parentRecord.Result.Output)
	// R48: consumption is proven by exact evidence-scope agreement, NOT by
	// verbatim summary containment. A genuine synthesis condenses and
	// rephrases; R47 live proved this: the parent referenced the accepted
	// result hash, restated its exact facts (same paths, same observed byte
	// counts), and performed zero source_read calls itself, yet shared no
	// verbatim substring with the child body. Combined with candidate
	// binding (here) and exact accepted-index delivery (below), full path
	// agreement is the honest bar; verbatim containment punishes quality.
	if parentOutput.CandidateID != candidateID {
		t.Fatal("parent synthesis lost candidate binding", parentOutput.CandidateID)
	}
	for _, path := range childOutput.Paths {
		found := false
		for _, parentPath := range parentOutput.Paths {
			if parentPath == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("parent synthesis omits exact accepted child evidence path", path)
		}
	}
	parentRuntime := scheduledRecursiveRuntimePath(controllerPath, prepared.turn)
	childRuntime := scheduledRecursiveRuntimePath(controllerPath, childTurn)
	if parentRuntime == childRuntime {
		t.Fatal("recursive turns shared a runtime journal")
	}
	controlState, err := agentcontrol.Inspect(controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduledRecursiveValidateAcceptedIndexes(controllerPath, controller, controlState, prepared.turn, childTurn, parentRecord, childRecord); err != nil {
		t.Fatal(err)
	}

	tallies := map[string]museGatewayTally{}
	for _, item := range []taskscheduler.DynamicTask{prepared.turn, childTurn} {
		intent := scheduledOpenCodeRuntimeIntent(t, controller, item.Task.InvocationID, scheduledRecursiveRuntimePath(controllerPath, item))
		verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, scheduledRecursiveRuntimePath(controllerPath, item), controller, item)
		state, inspectErr := opencoderuntime.InspectComposite(scheduledRecursiveRuntimePath(controllerPath, item), intent, verify)
		if inspectErr != nil || state.Result == nil || state.Result.GatewayUsage == nil ||
			state.Result.GatewayUsage.InputTokens == 0 || state.Result.GatewayUsage.OutputTokens == 0 {
			t.Fatal("recursive composite result or usage unsettled", item.TurnID, inspectErr)
		}
		broker, brokerErr := contextbroker.Inspect(scheduledRecursiveRuntimePath(controllerPath, item) + ".broker")
		if brokerErr != nil || !broker.Closed || broker.Calls == 0 {
			t.Fatal("recursive context broker evidence unavailable", item.TurnID, brokerErr)
		}
		tally := museGatewayTallyFor(t, controllerPath, item)
		if tally.failures != 0 || tally.intents == 0 || tally.receipts != tally.intents || !tally.finished {
			t.Fatal("recursive provider effects unfinished", item.TurnID, tally)
		}
		tallies[item.TurnID] = tally
	}
	childBroker, err := contextbroker.Inspect(childRuntime + ".broker")
	if err != nil || childBroker.Calls == 0 {
		t.Fatal("child performed no receipted source inspection")
	}

	// Offline recovery: reopen everything and prove zero new provider effects.
	controlEvents, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(controlEvents) == 0 {
		t.Fatal(err)
	}
	controlHead, controlCount := controlEvents[len(controlEvents)-1].Hash, len(controlEvents)
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []taskscheduler.DynamicTask{prepared.turn, childTurn} {
		path := scheduledRecursiveRuntimePath(controllerPath, item)
		intent := scheduledOpenCodeRuntimeIntent(t, controller, item.Task.InvocationID, path)
		verify := scheduledOpenCodeCompositeVerifier(t, controllerPath, schedulerPath, path, controller, item)
		state, inspectErr := opencoderuntime.InspectComposite(path, intent, verify)
		if inspectErr != nil || state.Result == nil {
			t.Fatal("recovery inspect changed", item.TurnID, inspectErr)
		}
		recovered, recoverErr := opencoderuntime.Execute(context.Background(), opencoderuntime.ExecuteConfig{RuntimePath: path, Intent: intent, VerifyComposite: verify})
		if recoverErr != nil || !reflect.DeepEqual(recovered, *state.Result) {
			t.Fatal("runtime recovery changed result", item.TurnID, recoverErr)
		}
		evidence, reconcileErr := adapter.Reconcile(context.Background(), *scheduled.Tasks[item.Task.ID].Claim)
		if reconcileErr != nil || evidence.Status != taskscheduler.StatusSucceeded || evidence.InvocationID != item.Task.InvocationID {
			t.Fatal("scheduler recovery changed evidence", item.TurnID, reconcileErr, evidence)
		}
		after := museGatewayTallyFor(t, controllerPath, item)
		if after != tallies[item.TurnID] {
			t.Fatal("recovery changed provider effects", item.TurnID, after, tallies[item.TurnID])
		}
	}
	afterEvents, err := journal.Read(controllerPath + ".agent-control")
	if err != nil || len(afterEvents) != controlCount || afterEvents[len(afterEvents)-1].Hash != controlHead {
		t.Fatal("recovery duplicated accepted results", err)
	}

	t.Run("followup", func(t *testing.T) {
		followCtx, followCancel := context.WithTimeout(dispatchCtx, 15*time.Minute)
		defer followCancel()
		pumpCtx2, stopPump2 := context.WithCancel(followCtx)
		defer stopPump2()
		pumpDone2 := make(chan error, 1)
		go func() {
			pumpDone2 <- taskscheduler.Pump(pumpCtx2, schedulerPath, adapter, taskscheduler.PumpOptions{Workers: 2, PollInterval: 100 * time.Millisecond})
		}()
		defer func() {
			stopPump2()
			select {
			case <-pumpDone2:
			case <-time.After(30 * time.Second):
				t.Error("follow-up pump did not stop")
			}
		}()
		record, followTurn, err := FollowUpExplorerAgent(followCtx, controllerPath, schedulerPath,
			agentcontrol.MessageRequest{FromAgentID: prepared.turn.AgentID, ToAgentID: childTurn.AgentID, Nonce: "muse-recursive-followup-1", Body: "Name one more relevant source file for this task and say what it owns, using source tools again."},
			"Name one more relevant source file for this task and say what it owns, using source tools again.")
		if err != nil {
			t.Fatal("follow-up admission failed", err)
		}
		if followTurn.AgentID != childTurn.AgentID || followTurn.TurnID == childTurn.TurnID || followTurn.TurnSequence != childTurn.TurnSequence+1 {
			t.Fatal("follow-up turn identity malformed", followTurn, record)
		}
		deadline := time.Now().Add(15 * time.Minute)
		for {
			scheduled, inspectErr := taskscheduler.Inspect(schedulerPath)
			if inspectErr == nil && scheduled.Tasks[followTurn.Task.ID].Status == taskscheduler.StatusSucceeded {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("follow-up turn did not settle")
			}
			select {
			case pumpErr := <-pumpDone2:
				t.Fatal("follow-up pump failed", pumpErr)
			case <-time.After(5 * time.Second):
			}
		}
		controller, err := Inspect(controllerPath)
		if err != nil {
			t.Fatal(err)
		}
		var second *ExplorerRecord
		for index := range controller.Explorations {
			candidate := &controller.Explorations[index]
			if candidate.Invocation.ID == followTurn.Task.InvocationID {
				second = candidate
			}
		}
		if second == nil {
			t.Fatal("second child exploration missing")
		}
		secondOutput := decodeMuseExploration(t, second.Result.Output)
		if secondOutput.CandidateID != candidateID || secondOutput.Summary == "" || secondOutput.Summary == childOutput.Summary {
			t.Fatal("second accepted result not distinct evidence")
		}
	})
	t.Logf("MUSE_SCHEDULED_RECURSIVE_PASS parent_turn=%s child_turn=%s", prepared.turn.TurnID, childTurn.TurnID)
}
