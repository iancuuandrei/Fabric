package control

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "app-server" {
		os.Exit(runFixtureAppServer())
	}
	os.Exit(m.Run())
}

func runFixtureAppServer() int {
	type request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	type response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}
	disabled := []string{"apps", "plugins", "remote_plugin", "hooks", "multi_agent", "multi_agent_v2", "shell_tool", "code_mode", "code_mode_host", "code_mode_only", "browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "in_app_browser", "image_generation", "memories", "skill_search", "skill_mcp_dependency_install", "workspace_dependencies", "recommended_plugins", "goals", "tool_suggest", "view_image", "sleep_tool", "unbounded_connection_retries"}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req request
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			return 2
		}
		if home := os.Getenv("CODEX_HOME"); home != "" {
			log, err := os.OpenFile(filepath.Join(home, "fixture-methods.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return 9
			}
			_, writeErr := log.WriteString(req.Method + "\n")
			if errors.Join(writeErr, log.Close()) != nil {
				return 10
			}
		}
		if len(req.ID) == 0 && req.Method == "initialized" {
			continue
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "fixture", "platformOs": "windows", "userAgent": "engorch-fixture"}
		case "config/read":
			result = map[string]any{"config": map[string]any{"approval_policy": "never", "sandbox_mode": "read-only", "web_search": "disabled"}}
		case "experimentalFeature/list":
			features := make([]map[string]any, 0, len(disabled)+1)
			for _, name := range disabled {
				features = append(features, map[string]any{"name": name, "enabled": false})
			}
			features = append(features, map[string]any{"name": "skip_host_skill_discovery", "enabled": true})
			result = map[string]any{"data": features}
		case "mcpServerStatus/list":
			result = map[string]any{"data": []any{}}
		case "account/login/start":
			result = map[string]any{"type": "chatgptAuthTokens"}
		case "thread/start":
			var params struct {
				Model  string `json:"model"`
				CWD    string `json:"cwd"`
				Config struct {
					Effort string `json:"model_reasoning_effort"`
				} `json:"config"`
			}
			if json.Unmarshal(req.Params, &params) != nil {
				return 3
			}
			result = map[string]any{"thread": map[string]any{"id": "thread-v2"}, "model": params.Model, "modelProvider": "openai", "reasoningEffort": params.Config.Effort, "cwd": params.CWD, "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}}
		case "turn/start":
			var params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			}
			if json.Unmarshal(req.Params, &params) != nil || len(params.Input) != 1 {
				return 4
			}
			var input struct {
				CandidateID string `json:"candidate_id"`
			}
			if json.Unmarshal([]byte(params.Input[0].Text), &input) != nil || input.CandidateID == "" {
				return 5
			}
			before := sha256.Sum256([]byte("base\n"))
			change := map[string]any{"path": "file.txt", "before_hash": hex.EncodeToString(before[:]), "content_base64": base64.StdEncoding.EncodeToString([]byte("fixture writer output\n")), "executable": false}
			output, _ := json.Marshal(map[string]any{"candidate_id": input.CandidateID, "changes": []any{change}})
			result = map[string]any{"turn": map[string]any{"id": "turn-v2", "status": "completed", "itemsView": "full", "error": nil, "items": []any{map[string]any{"type": "agentMessage", "id": "message-v2", "phase": "final_answer", "text": string(output)}}}}
		default:
			return 6
		}
		if encoder.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: result}) != nil {
			return 7
		}
	}
	if scanner.Err() != nil {
		return 8
	}
	return 0
}

func codexAccessCreation(t *testing.T) Creation {
	t.Helper()
	c := creation(t)
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-fixture", Effort: "high", Role: "planner"}
	c.Config.Version = 2
	c.Config.Planner = profile
	c.Config.Codex = &config.Codex{
		Executable:     filepath.Join(t.TempDir(), "codex.exe"),
		ExecutableHash: strings.Repeat("c", 64),
		StateRoot:      filepath.Join(t.TempDir(), "state"),
		AuthSource:     filepath.Join(t.TempDir(), "auth"),
	}
	c.Config.Access = &config.Access{
		Class:  access.Private,
		Limits: access.Limits{Tokens: 1000, Concurrency: 1},
		Profiles: []access.Profile{{
			Version: 1, Name: "chatgpt", Kind: "subscription", Runtime: "codex-app-server", Provider: "openai",
			AuthMode: "chatgpt-session", RepositoryClasses: []access.Class{access.Private},
		}},
		Roles:       map[string]string{"planner": "chatgpt"},
		Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 100}},
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func activePlannerAccess(t *testing.T) (string, Snapshot, runtime.Invocation, ModelAccessState) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", codexAccessCreation(t)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	i, err := runtime.NewInvocation(s.Creation.Config.Planner, s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	s, admitted, err := ensureModelAccessIntent(path, s, i)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireModelAccessActive(path, s, admitted); err != nil {
		t.Fatal(err)
	}
	return path, s, i, admitted
}

func appendUnchecked(t *testing.T, path, kind string, payload any) string {
	t.Helper()
	event, err := journal.Append(path, kind, payload, func([]journal.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return event.Hash
}

func writeCodexTerminal(t *testing.T, path string, invocation runtime.Invocation, source *repository.Identity, status string) (runtime.Result, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "workspace")
	effort := invocation.Profile.Effort
	appendUnchecked(t, path, "runtime.intent", codexruntime.Intent{Invocation: invocation, Directory: directory})
	if source != nil {
		appendUnchecked(t, path, "runtime.source", *source)
	}
	appendUnchecked(t, path, "runtime.thread", codexrpc.ThreadSettings{
		ThreadID: "thread-1", Model: invocation.Profile.Model, Provider: invocation.Profile.Provider, Effort: &effort,
		Directory: directory, Approval: "never", Sandbox: "readOnly", Network: false,
	})
	appendUnchecked(t, path, "runtime.turn-intent", struct {
		InvocationID string `json:"invocation_id"`
	}{invocation.ID})
	appendUnchecked(t, path, "runtime.turn", struct {
		ID string `json:"id"`
	}{"turn-1"})
	head := appendUnchecked(t, path, "runtime.turn-status", codexruntime.TurnStatus{ID: "turn-1", Status: status})
	if status != "completed" {
		return runtime.Result{}, head
	}
	inputTokens, outputTokens := int64(17), int64(9)
	model, provider, observedEffort := invocation.Profile.Model, invocation.Profile.Provider, invocation.Profile.Effort
	result := runtime.Result{
		Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile,
		ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &observedEffort,
		Output: `{"summary":"journaled result"}`, Usage: runtime.Usage{InputTokens: &inputTokens, OutputTokens: &outputTokens},
	}
	head = appendUnchecked(t, path, "runtime.result", codexruntime.TurnResult{TurnID: "turn-1", Result: result})
	return result, head
}

func TestModelAccessCompletesFromExactCodexJournal(t *testing.T) {
	path, initial, invocation, admitted := activePlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	result, head := writeCodexTerminal(t, runtimePath, invocation, &initial.Creation.Repository, "completed")
	s, terminal, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1")
	if err != nil {
		t.Fatal(err)
	}
	expectedHash, _ := canonicalResultHash("harness.planner-result.v1", result)
	if terminal.RuntimeJournalHead != head || terminal.Receipt.Status != "completed" || terminal.Receipt.OutputHash != expectedHash {
		t.Fatal("terminal access receipt did not bind exact runtime result", terminal)
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.RequireTerminal(modelAccessJournal(path), policy, admitted.Intent, terminal.Receipt); err != nil {
		t.Fatal(err)
	}
	report, err := MeasureRunUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Admissions) != 1 || report.Admissions[0].Receipt == nil || report.Admissions[0].EvidenceScope != "controller_admission_and_runtime_terminal" {
		t.Fatal("usage omitted generic admission receipt", report.Admissions)
	}
}

func TestCodexDispatchUsesConfiguredSharedTaskPool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	created := codexAccessCreation(t)
	created.Config.TaskPool = &config.TaskPool{Version: 1, Path: filepath.Join(t.TempDir(), "shared-pool.jsonl"), Limits: taskpool.Limits{Total: 1}}
	if err := created.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(s.Creation.Config.Planner, s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	s, admitted, err := ensureModelAccessIntent(path, s, invocation)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := taskpool.Inspect(created.Config.TaskPool.Path)
	if err != nil || len(pool.Active) != 1 {
		t.Fatal("Codex admission did not acquire shared capacity", pool, err)
	}
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	writeCodexTerminal(t, runtimePath, invocation, &s.Creation.Repository, "completed")
	if _, _, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1"); err != nil {
		t.Fatal(err)
	}
	pool, err = taskpool.Inspect(created.Config.TaskPool.Path)
	if err != nil || len(pool.Active) != 0 || len(pool.Settled) != 1 {
		t.Fatal("Codex terminal receipt did not settle shared capacity", admitted, pool, err)
	}
}

func TestModelAccessRecoversTerminalBeforeControllerMirror(t *testing.T) {
	path, s, invocation, admitted := activePlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	writeCodexTerminal(t, runtimePath, invocation, &s.Creation.Repository, "completed")
	expected, err := observedModelAccessTerminal(runtimePath, invocation, admitted.Intent, s.Creation.Repository, "harness.planner-result.v1")
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.Reconcile(context.Background(), modelAccessJournal(path), policy, admitted.Intent, func(context.Context, access.Intent) (access.Receipt, error) {
		return expected.Receipt, nil
	}); err != nil {
		t.Fatal(err)
	}
	s, recovered, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1")
	if err != nil || !sameCanonical(recovered, expected) || s.ModelAccess[0].Terminal == nil {
		t.Fatal("durable terminal was not mirrored without re-observation", recovered, err)
	}
}

func TestPublicAppendCannotAssertModelAccessTerminal(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			path, s, invocation, admitted := activePlannerAccess(t)
			runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
			writeCodexTerminal(t, runtimePath, invocation, &s.Creation.Repository, status)
			terminal, err := observedModelAccessTerminal(runtimePath, invocation, admitted.Intent, s.Creation.Repository, "harness.planner-result.v1")
			if err != nil {
				t.Fatal(err)
			}
			if err := Append(path, "model.access-receipt", terminal); err == nil {
				t.Fatal("public controller append asserted a runtime terminal receipt")
			}
			s, err = Inspect(path)
			if err != nil || s.ModelAccess[0].Terminal != nil {
				t.Fatal("rejected public terminal append changed controller state", err)
			}
			policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
			if err := access.RequireActive(modelAccessJournal(path), policy, admitted.Intent); err != nil {
				t.Fatal("rejected public terminal released durable admission", err)
			}
		})
	}
}

func TestModelAccessTerminalRequiresExactRuntimeSource(t *testing.T) {
	for _, test := range []struct {
		name  string
		wrong bool
	}{{name: "missing"}, {name: "substituted", wrong: true}} {
		t.Run(test.name, func(t *testing.T) {
			path, s, invocation, admitted := activePlannerAccess(t)
			runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
			var source *repository.Identity
			if test.wrong {
				changed := s.Creation.Repository
				changed.Commit = strings.Repeat("9", 40)
				source = &changed
			}
			writeCodexTerminal(t, runtimePath, invocation, source, "completed")
			if _, _, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1"); err == nil {
				t.Fatal("runtime terminal without exact source was admitted")
			}
			policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
			if err := access.RequireActive(modelAccessJournal(path), policy, admitted.Intent); err != nil {
				t.Fatal("source substitution released active admission", err)
			}
		})
	}
}

func TestModelAccessStaysActiveWithoutExactRuntimeTerminal(t *testing.T) {
	path, s, invocation, admitted := activePlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	directory := filepath.Join(t.TempDir(), "workspace")
	effort := invocation.Profile.Effort
	appendUnchecked(t, runtimePath, "runtime.intent", codexruntime.Intent{Invocation: invocation, Directory: directory})
	appendUnchecked(t, runtimePath, "runtime.source", s.Creation.Repository)
	appendUnchecked(t, runtimePath, "runtime.thread", codexrpc.ThreadSettings{ThreadID: "thread", Model: invocation.Profile.Model, Provider: invocation.Profile.Provider, Effort: &effort, Directory: directory, Approval: "never", Sandbox: "readOnly"})
	appendUnchecked(t, runtimePath, "runtime.turn-intent", struct {
		InvocationID string `json:"invocation_id"`
	}{invocation.ID})
	appendUnchecked(t, runtimePath, "runtime.turn", struct {
		ID string `json:"id"`
	}{"turn"})
	if _, _, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1"); err == nil {
		t.Fatal("pending runtime was converted into a terminal receipt")
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.RequireActive(modelAccessJournal(path), policy, admitted.Intent); err != nil {
		t.Fatal("unresolved runtime did not retain active reservation", err)
	}
	latest, _ := Inspect(path)
	if latest.ModelAccess[0].Terminal != nil {
		t.Fatal("controller fabricated terminal state")
	}
}

func TestModelAccessMapsOnlyObservedRuntimeTerminalStatuses(t *testing.T) {
	for observed, expected := range map[string]string{"failed": "failed", "interrupted": "cancelled"} {
		t.Run(observed, func(t *testing.T) {
			path, initial, invocation, _ := activePlannerAccess(t)
			runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
			_, head := writeCodexTerminal(t, runtimePath, invocation, &initial.Creation.Repository, observed)
			s, terminal, err := completeModelAccess(context.Background(), path, runtimePath, invocation, "harness.planner-result.v1")
			if err != nil {
				t.Fatal(err)
			}
			if terminal.RuntimeJournalHead != head || terminal.Receipt.Status != expected || terminal.Receipt.OutputHash != "" || s.ModelAccess[0].Terminal == nil {
				t.Fatal("runtime terminal status was not mirrored exactly", terminal)
			}
			if err := requireModelAccessActive(path, s, s.ModelAccess[0]); err == nil {
				t.Fatal("terminal invocation remained dispatchable")
			}
		})
	}
}

func TestModelAccessRequiresSupportedSubscriptionBinding(t *testing.T) {
	c := codexAccessCreation(t)
	c.Config.Access.Profiles[0].Kind = "api"
	c.Config.Access.Profiles[0].AuthMode = ""
	c.Config.Access.Profiles[0].CredentialRef = "OPENAI_KEY"
	cost := int64(10)
	c.Config.Access.Limits.CostMicroUSD = &cost
	c.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: 100, CostMicroUSD: &cost}
	s := Snapshot{RunID: strings.Repeat("a", 64), Creation: c, State: "PLANNING"}
	i, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	intent, err := deriveModelAccessIntent(s, i, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCodexSubscriptionAccess(s, intent); err == nil {
		t.Fatal("API credential route was enabled for Codex subscription lifecycle")
	}
	c = codexAccessCreation(t)
	c.Config.Codex.AuthSource = "relative-auth"
	s.Creation = c
	if err := validateCodexSubscriptionAccess(s, intent); err == nil {
		t.Fatal("unbound relative ChatGPT login source was accepted")
	}
}

func TestCancelledReconciliationDoesNotFabricateReceipt(t *testing.T) {
	path, s, invocation, admitted := activePlannerAccess(t)
	runtimePath := filepath.Join(t.TempDir(), "planner.jsonl")
	writeCodexTerminal(t, runtimePath, invocation, &s.Creation.Repository, "failed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := completeModelAccess(ctx, path, runtimePath, invocation, "harness.planner-result.v1"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reconciliation did not remain unresolved", err)
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.RequireActive(modelAccessJournal(path), policy, admitted.Intent); err != nil {
		t.Fatal("cancelled local reconciliation wrote a receipt", err)
	}
}

func TestMixedFakePlannerAndCodexWriterShareRunBudget(t *testing.T) {
	c := creation(t)
	c.Config.Version = 2
	writer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer-model", Effort: "high", Role: "writer"}
	c.Config.Writer = &writer
	c.Config.Codex = &config.Codex{Executable: filepath.Join(t.TempDir(), "codex.exe"), ExecutableHash: strings.Repeat("d", 64), StateRoot: filepath.Join(t.TempDir(), "state"), AuthSource: filepath.Join(t.TempDir(), "auth")}
	c.Config.Access = &config.Access{
		Class:  access.Private,
		Limits: access.Limits{Tokens: 1000, Concurrency: 1},
		Profiles: []access.Profile{
			{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "chatgpt", Kind: "subscription", Runtime: "codex-app-server", Provider: "openai", AuthMode: "chatgpt-session", RepositoryClasses: []access.Class{access.Private}},
		},
		Roles: map[string]string{"planner": "fixture", "writer": "chatgpt"},
		Invocations: map[string]config.InvocationLimit{
			"planner": {Tokens: 600},
			"writer":  {Tokens: 500},
		},
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, _ := Inspect(path)
	planner, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	plannerIntent, err := expectedPlanningAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.access-intent", plannerIntent); err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, _ = Inspect(path)
	if err := ensureLegacyPlannerAccessMirror(path, s); err != nil {
		t.Fatal(err)
	}
	writerInvocation, _ := runtime.NewInvocation(writer, "exact writer input")
	writerIntent, err := deriveModelAccessIntent(s, writerInvocation, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.ReserveDurable(modelAccessJournal(path), policy, writerIntent); err == nil {
		t.Fatal("Codex writer ignored tokens already consumed by legacy fake planner")
	}
}

func TestV2MixedWriterRunsThroughCodexHostAndAccessLifecycle(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableHash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	c := creation(t)
	c.Config.Version = 2
	writer := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer-fixture", Effort: "high", Role: "writer"}
	c.Config.Writer = &writer
	stateRoot := t.TempDir()
	authSource := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authSource, []byte(`{"tokens":{"access_token":"fixture-token","account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	c.Config.Codex = &config.Codex{Executable: executable, ExecutableHash: executableHash, StateRoot: stateRoot, AuthSource: authSource}
	c.Config.Access = &config.Access{
		Class: access.Private, Limits: access.Limits{Tokens: 1000, Concurrency: 1},
		Profiles: []access.Profile{
			{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "chatgpt", Kind: "subscription", Runtime: "codex-app-server", Provider: "openai", AuthMode: "chatgpt-session", RepositoryClasses: []access.Class{access.Private}},
		},
		Roles: map[string]string{"planner": "fixture", "writer": "chatgpt"},
		Invocations: map[string]config.InvocationLimit{
			"planner": {Tokens: 100},
			"writer":  {Tokens: 200},
		},
	}
	root := c.Repository.Root
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, _ := Inspect(path)
	planner, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	plannerIntent, err := expectedPlanningAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.access-intent", plannerIntent); err != nil {
		t.Fatal(err)
	}
	plan, err := (&runtime.Fake{}).Execute(context.Background(), planner)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", plan); err != nil {
		t.Fatal(err)
	}
	s, _ = Inspect(path)
	if err := Append(path, "plan.approved", Approval{PlanID: s.PlanID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	record, err := RunWriter(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	if err != nil || s.WriterHost == nil || s.WriterHost.RuntimeReceipt == nil || s.WriterProposal == nil || len(s.ModelAccess) != 1 || s.ModelAccess[0].Terminal == nil || s.ModelAccess[0].Terminal.Receipt.Status != "completed" || record.Invocation.Profile != writer {
		t.Fatal("v2 writer lifecycle evidence incomplete", err)
	}
	report, err := MeasureRunUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Admissions) != 2 || report.Admissions[0].EvidenceScope != "deterministic_fake_planner" || report.Admissions[1].EvidenceScope != "controller_admission_and_runtime_terminal" || len(report.Invocations) != 1 || !report.Invocations[0].JournalPresent || !report.Invocations[0].ReceiptMatched || report.Invocations[0].Usage == nil || !report.Invocations[0].Usage.Completed {
		t.Fatal("v2 writer usage did not bind admission and runtime receipt", report)
	}
}

func fixtureFileHash(path string) (string, error) {
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

func TestUsageReportsDurableReservationBeforeControllerMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", codexAccessCreation(t)); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, _ := Inspect(path)
	invocation, _ := runtime.NewInvocation(s.Creation.Config.Planner, s.Creation.Objective)
	intent, err := deriveModelAccessIntent(s, invocation, 1)
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := s.Creation.Config.AccessPolicy(s.RunID)
	if err := access.ReserveDurable(modelAccessJournal(path), policy, intent); err != nil {
		t.Fatal(err)
	}
	report, err := MeasureRunUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Admissions) != 1 || report.Admissions[0].Receipt != nil || report.Admissions[0].EvidenceScope != "durable_access_journal_only" || !sameCanonical(report.Admissions[0].Intent, intent) {
		t.Fatal("usage omitted orphan durable reservation", report.Admissions)
	}
}

func TestUsageSeedsCompletedRuntimeHeadBeforeRoleReceiptMirror(t *testing.T) {
	_, s, invocation, admitted := activePlannerAccess(t)
	head := strings.Repeat("e", 64)
	routeID, _ := admitted.Intent.Route.ID()
	outputHash := strings.Repeat("f", 64)
	s.ModelAccess[0].Terminal = &ModelAccessTerminal{RuntimeInvocationID: invocation.ID, RuntimeJournalHead: head, Receipt: access.Receipt{InvocationID: admitted.Intent.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: outputHash}}
	if actual := admittedRuntimeHead(s, invocation); actual != head {
		t.Fatal("completed access terminal did not seed runtime head", actual)
	}
	s.ModelAccess[0].Terminal.Receipt.Status = "failed"
	s.ModelAccess[0].Terminal.Receipt.OutputHash = ""
	if actual := admittedRuntimeHead(s, invocation); actual != "" {
		t.Fatal("failed terminal was treated as completed runtime receipt", actual)
	}
}

func canonicalResultHash(domain string, result runtime.Result) (string, error) {
	return canonical.Hash(domain, result)
}
