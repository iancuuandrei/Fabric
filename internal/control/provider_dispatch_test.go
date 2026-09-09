package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providerruntime"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

func TestOpenCodeRecoveryNeverCreatesFreshAccessAdmission(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"), ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "opencode-recovery", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Produce an implementation plan.", Config: cfg}
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
	invocation, err := runtime.NewInvocation(cfg.Planner, created.Objective)
	if err != nil {
		t.Fatal(err)
	}
	runtimePath := controllerPath + ".planner.opencode-runtime.jsonl"
	if _, err := journal.Append(runtimePath, "partial", struct{}{}, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeOpenCodeProvider(context.Background(), controllerPath, s, invocation, nil); !errors.Is(err, opencoderuntime.ErrRecoveryRequired) {
		t.Fatalf("partial runtime did not remain recovery-only: %v", err)
	}
	if events, err := journal.Read(controllerPath + ".model-access.jsonl"); err != nil || len(events) != 0 {
		t.Fatal("recovery created a fresh access admission", len(events), err)
	}
	if _, err := os.Stat(runtimePath + ".broker"); !os.IsNotExist(err) {
		t.Fatal("recovery created missing broker history", err)
	}
}

func TestDirectProviderToolRolesFailCapabilityAdmission(t *testing.T) {
	for _, role := range []string{"explorer", "writer", "fixer", "reviewer"} {
		profile := runtime.Profile{Runtime: "provider-api", Provider: "openai", Model: "model", Effort: "none", Role: role}
		if err := requireExecutableRoleRuntime(profile); !errors.Is(err, providergateway.ErrCapabilityUnavailable) {
			t.Fatalf("%s did not fail with CAPABILITY_UNAVAILABLE: %v", role, err)
		}
	}
}

func TestOpenCodePlannerReceiptAdmitsExactRuntimeResult(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("opencode-http", "engorch-openai")
	cfg.Repository = "fixture"
	role := cfg.Provider.Roles["planner"]
	role.RequiredCapabilities = &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	cfg.Provider.Roles["planner"] = role
	cfg.OpenCode = &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: `D:\state\opencode`}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "opencode-run", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Produce an implementation plan.", Config: cfg}
	if err := Append(controllerPath, "run.created", created); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, _ := Inspect(controllerPath)
	invocation, _ := runtime.NewInvocation(cfg.Planner, created.Objective)
	model, provider, effort := cfg.Planner.Model, cfg.Planner.Provider, cfg.Planner.Effort
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "Inspect the repository, implement the bounded change, and run the configured check."}
	inputHash, _ := access.InputID(invocation.Input)
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	resultHash, _ := canonical.Hash("harness.planner-result.v1", result)
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: invocation.ID, AccessInvocationID: routing.Intent.Reservation.InvocationID, RuntimeJournalHead: strings.Repeat("c", 64), GatewayJournalHead: strings.Repeat("d", 64), ResultHash: resultHash, ObservedModel: model, ObservedProvider: provider, Result: result}
	if err := Append(controllerPath, "planning.provider-observed", receipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(controllerPath)
	if err != nil || s.State != "AWAITING_APPROVAL" || s.PlannerProvider == nil {
		t.Fatal("OpenCode planner result was not admitted", s, err)
	}
}

func TestDirectPlannerRequiresExactProviderReceiptBeforePlanAdmission(t *testing.T) {
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	cfg := providerRoutingConfig("provider-api", "openai")
	cfg.Repository = "fixture"
	cfg.Provider.Models[0].ObservedModelAliases = []string{"observed-alias"}
	repositoryRoot := t.TempDir()
	created := Creation{Version: 1, Nonce: "provider-run", Repository: repository.Identity{Version: 1, Name: "fixture", Root: repositoryRoot, CommonDir: filepath.Join(repositoryRoot, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Return a bounded implementation plan as JSON.", Config: cfg}
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
	invocation, _ := runtime.NewInvocation(cfg.Planner, created.Objective)
	model, provider, effort := cfg.Planner.Model, cfg.Planner.Provider, cfg.Planner.Effort
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: `{}`}
	if err := Append(controllerPath, "plan.recorded", result); err == nil {
		t.Fatal("direct plan admitted without provider receipt")
	}
	resolved, expectation, err := ConfiguredProviderExpectation(cfg, "planner")
	if err != nil {
		t.Fatal(err)
	}
	direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
	inputHash, err := direct.InputHash(expectation)
	if err != nil {
		t.Fatal(err)
	}
	routing, err := ResolveProviderRouting(cfg, s.RunID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	resultHash, _ := canonical.Hash("harness.planner-result.v1", result)
	receipt := providerDispatchReceipt{Version: 1, Role: "planner", InvocationID: invocation.ID, AccessInvocationID: routing.Intent.Reservation.InvocationID, RuntimeJournalHead: strings.Repeat("c", 64), GatewayJournalHead: strings.Repeat("d", 64), ResultHash: resultHash, ObservedModel: "observed-alias", ObservedProvider: resolved.Model.Provider, Result: result}
	if err := Append(controllerPath, "planning.provider-observed", receipt); err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(controllerPath)
	if err != nil || s.State != "AWAITING_APPROVAL" || s.PlannerProvider == nil || s.PlannerProvider.ObservedModel != "observed-alias" {
		t.Fatal("direct planner receipt was not retained", s, err)
	}
}
