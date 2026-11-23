package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/pelletier/go-toml/v2"
)

func opencodeUnitConfig(t *testing.T, stateRoot string) config.Config {
	t.Helper()
	return config.Config{
		Planner: runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "fixture-model", Effort: "none", Role: "planner"},
		OpenCode: &config.OpenCodeHost{
			Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"),
			ExecutableHash: strings.Repeat("a", 64), StateRoot: stateRoot,
		},
	}
}

func assertSafePreflightError(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("missing OpenCode state root accepted")
	}
	var preflight *openCodeStateRootError
	if !errors.As(err, &preflight) {
		t.Fatalf("failure is not a safe OpenCode preflight diagnostic: %v", err)
	}
	if !strings.Contains(err.Error(), "opencode-state-root") || !strings.Contains(err.Error(), "create or configure a safe private directory") {
		t.Fatalf("diagnostic omits failed stage or next action: %v", err)
	}
	for _, secret := range forbidden {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("diagnostic leaks a full path or sensitive value: %v", err)
		}
	}
}

func TestRequireOpenCodeStateRootRejectsMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-runtime")
	assertSafePreflightError(t, requireOpenCodeStateRoot(opencodeUnitConfig(t, missing)), missing)
}

func TestRequireOpenCodeStateRootRejectsFileAndUncleanRoot(t *testing.T) {
	fileRoot := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(fileRoot, []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}
	assertSafePreflightError(t, requireOpenCodeStateRoot(opencodeUnitConfig(t, fileRoot)), fileRoot)

	valid := t.TempDir()
	unclean := valid + string(filepath.Separator) + "."
	assertSafePreflightError(t, requireOpenCodeStateRoot(opencodeUnitConfig(t, unclean)), valid)
	assertSafePreflightError(t, requireOpenCodeStateRoot(opencodeUnitConfig(t, "relative/root")))
}

func TestRequireOpenCodeStateRootRejectsUnsafeSymlink(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	assertSafePreflightError(t, requireOpenCodeStateRoot(opencodeUnitConfig(t, link)), link, target)
}

func TestRequireOpenCodeStateRootAcceptsValidRoot(t *testing.T) {
	if err := requireOpenCodeStateRoot(opencodeUnitConfig(t, t.TempDir())); err != nil {
		t.Fatalf("existing safe state root rejected: %v", err)
	}
}

func TestRequireOpenCodeStateRootSkipsUnselectedRoutes(t *testing.T) {
	plain, err := config.Parse([]byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOpenCodeStateRoot(plain); err != nil {
		t.Fatalf("non-OpenCode configuration blocked: %v", err)
	}
	unselected := plain
	unselected.OpenCode = &config.OpenCodeHost{
		Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"),
		ExecutableHash: strings.Repeat("a", 64),
		StateRoot:      filepath.Join(t.TempDir(), "absent-unselected-runtime"),
	}
	if err := requireOpenCodeStateRoot(unselected); err != nil {
		t.Fatalf("unselected optional OpenCode configuration blocked other routes: %v", err)
	}
}

// writeOpenCodePlannerHarness replaces the fixture harness with a minimal
// valid v2 planner-only OpenCode configuration whose state root is fully
// test-controlled. The provider/admin shape mirrors the proven planner-only
// OpenCode admission tuple; only the state root varies per case.
func writeOpenCodePlannerHarness(t *testing.T, root, stateRoot string) {
	t.Helper()
	limitsCost, invocationCost := int64(10), int64(3)
	cfg := config.Config{
		Version: 2, Repository: "provider-project", BaseBranch: "main",
		Planner:      runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "deployment/opaque-model", Effort: "none", Role: "planner"},
		Verification: []config.Check{{Name: "unit", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}},
		Access: &config.Access{
			Class: access.Private, Limits: access.Limits{Tokens: 1000, CostMicroUSD: &limitsCost, Concurrency: 1},
			Roles:       map[string]string{"planner": "provider-api"},
			Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: &invocationCost}},
			Profiles:    []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}},
		},
		Provider: &config.Provider{
			Version:     1,
			Credentials: []config.ProviderCredential{{Ref: "team-key", Environment: "ENGORCH_PROVIDER_TEAM_KEY"}},
			Endpoints:   []config.ProviderEndpoint{{Name: "primary", Version: 2, Provider: "engorch-openai", URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: config.ProviderAuth{Scheme: "bearer", CredentialRef: "team-key"}}},
			Models:      []config.ProviderModel{{Name: "planner-model", Version: 2, Provider: "engorch-openai", Model: "deployment/opaque-model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: config.ProviderCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: "{}", ContextWindowTokens: 100, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10, Pricing: &config.ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}}},
			Roles:       map[string]config.ProviderRole{"planner": {Endpoint: "primary", Model: "planner-model", AdapterControlsJSON: "{}", Variant: config.ProviderVariant{Effort: "none"}, RequiredCapabilities: &config.ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}},
		},
		OpenCode: &config.OpenCodeHost{Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: stateRoot},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("opencode fixture configuration invalid: %v", err)
	}
	content, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "harness.toml"), content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuration(root); err != nil {
		t.Fatalf("opencode fixture harness did not reload: %v", err)
	}
}

func assertNoDurableRuns(t *testing.T, root string) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preflight failure created a durable run or provider intent: %v", entries)
	}
}

func TestDoctorRejectsMissingOpenCodeStateRoot(t *testing.T) {
	root := autonomousCLIFixture(t)
	missing := filepath.Join(t.TempDir(), "absent-runtime")
	writeOpenCodePlannerHarness(t, root, missing)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"doctor"}, root, &out)
	assertSafePreflightError(t, err, missing, "ENGORCH_PROVIDER_TEAM_KEY")
	if out.Len() != 0 {
		t.Fatal("doctor reported success despite missing OpenCode state root")
	}
	assertNoDurableRuns(t, root)
}

func TestInspectPlanRejectsMissingOpenCodeStateRoot(t *testing.T) {
	root := autonomousCLIFixture(t)
	missing := filepath.Join(t.TempDir(), "absent-runtime")
	writeOpenCodePlannerHarness(t, root, missing)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan"}, root, &out)
	assertSafePreflightError(t, err, missing, "ENGORCH_PROVIDER_TEAM_KEY")
	if out.Len() != 0 {
		t.Fatal("inspect-plan reported success despite missing OpenCode state root")
	}
	assertNoDurableRuns(t, root)
}

func TestAutonomousNewRunRejectsMissingOpenCodeStateRootBeforeJournal(t *testing.T) {
	root := autonomousCLIFixture(t)
	missing := filepath.Join(t.TempDir(), "absent-runtime")
	writeOpenCodePlannerHarness(t, root, missing)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "Make a bounded fixture change"}, root, &out)
	assertSafePreflightError(t, err, missing, "ENGORCH_PROVIDER_TEAM_KEY", "Make a bounded fixture change")
	if out.Len() != 0 {
		t.Fatal("new autonomous run reported success despite missing OpenCode state root")
	}
	assertNoDurableRuns(t, root)
}

func TestDoctorAndInspectPlanAcceptValidOpenCodeStateRoot(t *testing.T) {
	root := autonomousCLIFixture(t)
	writeOpenCodePlannerHarness(t, root, t.TempDir())
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"doctor"}, root, &out); err != nil {
		t.Fatalf("doctor rejected an existing safe OpenCode state root: %v", err)
	}
	out.Reset()
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan"}, root, &out); err != nil {
		t.Fatalf("inspect-plan rejected an existing safe OpenCode state root: %v", err)
	}
}
