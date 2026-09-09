package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/writercontract"
)

func TestOptionalHostPolicyIsStrictAndIdentityBound(t *testing.T) {
	base, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	without, err := base.ID()
	if err != nil {
		t.Fatal(err)
	}
	raw := Example + `
[host_policy]
version = 1
allowed_hosts = ["native", "codex"]
require_verified_sandbox = true
allowed_sandbox_modes = ["workspace-write"]
`
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.HostPolicy == nil || !c.HostPolicy.RequireVerifiedSandbox || c.HostPolicy.AllowedSandboxModes[0] != hostenvironment.SandboxWorkspaceWrite {
		t.Fatal("explicit host policy was not decoded", c.HostPolicy)
	}
	with, err := c.ID()
	if err != nil || with == without {
		t.Fatal("host policy was not configuration-identity-bound", err)
	}

	invalid := []string{
		Example + "\n[host_policy]\nversion = 1\nrequire_verified_sandbox = false\n",
		strings.Replace(raw, `allowed_hosts = ["native", "codex"]`, `allowed_hosts = ["native", "container"]`, 1),
		strings.Replace(raw, `allowed_hosts = ["native", "codex"]`, `allowed_hosts = []`, 1),
		strings.Replace(raw, `allowed_sandbox_modes = ["workspace-write"]`, `allowed_sandbox_modes = ["danger-full-access"]`, 1),
		strings.Replace(raw, `allowed_sandbox_modes = ["workspace-write"]`, `allowed_sandbox_modes = ["workspace-write", "workspace-write"]`, 1),
		strings.Replace(raw, `allowed_sandbox_modes = ["workspace-write"]`, ``, 1),
	}
	for _, candidate := range invalid {
		if _, err := Parse([]byte(candidate)); err == nil {
			t.Fatal("invalid host policy was admitted", candidate)
		}
	}
}

func TestOptionalControllerStateRootIsStrictAndIdentityBound(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	without, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.ControllerStateRoot = filepath.Join(t.TempDir(), "controller-state")
	with, err := c.ID()
	if err != nil || with == without {
		t.Fatal("controller state root was not configuration-identity-bound", err)
	}
	for _, unsafe := range []string{"relative", filepath.Clean(c.ControllerStateRoot) + string(filepath.Separator) + ".."} {
		candidate := c
		candidate.ControllerStateRoot = unsafe
		if err := candidate.Validate(); err == nil {
			t.Fatal("unsafe controller state root admitted", unsafe)
		}
	}
}

func TestOptionalSharedTaskPoolIsStrictAndIdentityBound(t *testing.T) {
	c := providerBackedConfig("provider-api", "openai")
	without, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.TaskPool = &TaskPool{Version: 1, Path: filepath.Join(t.TempDir(), "provider-pool.jsonl"), Limits: taskpool.Limits{Total: 3, Profiles: map[string]int{"profile-id": 2}, Providers: map[string]int{"openai": 2}, Models: []taskpool.ModelLimit{{Key: taskpool.ModelKey{Provider: "openai", Model: "deployment/opaque-model"}, Cap: 1}}}}
	with, err := c.ID()
	if err != nil || with == without {
		t.Fatal("shared task pool was not identity-bound", err)
	}
	c.TaskPool.Path = "relative/pool.jsonl"
	if err := c.Validate(); err == nil {
		t.Fatal("relative shared task pool path admitted")
	}
	c.TaskPool.Path = filepath.Join(t.TempDir(), "provider-pool.jsonl")
	c.TaskPool.Limits.Total = 0
	if err := c.Validate(); err == nil {
		t.Fatal("invalid shared task pool limits admitted")
	}
}

func TestOptionalExplorationPolicyIsStrictAndIdentityBound(t *testing.T) {
	c := providerBackedConfig("provider-api", "openai")
	without, err := c.ID()
	if err != nil || c.MaxExplorationRecords() != 16 || c.MaxExplorationContextRecords() != 16 {
		t.Fatal("legacy exploration bounds changed", err)
	}
	c.Exploration = &ExplorationPolicy{Version: 1, MaxRecords: 32, MaxContextRecords: 12}
	with, err := c.ID()
	if err != nil || with == without || c.MaxExplorationRecords() != 32 || c.MaxExplorationContextRecords() != 12 {
		t.Fatal("exploration policy was not identity-bound", err)
	}
	for _, invalid := range []ExplorationPolicy{
		{Version: 2, MaxRecords: 32, MaxContextRecords: 12},
		{Version: 1, MaxRecords: 0, MaxContextRecords: 1},
		{Version: 1, MaxRecords: 257, MaxContextRecords: 1},
		{Version: 1, MaxRecords: 32, MaxContextRecords: 0},
		{Version: 1, MaxRecords: 32, MaxContextRecords: 33},
		{Version: 1, MaxRecords: 128, MaxContextRecords: 65},
	} {
		candidate := c
		candidate.Exploration = &invalid
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid exploration policy admitted", invalid)
		}
	}
}

func TestConfigurationBindingAndStrictness(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.Verification[0].Argv = append(c.Verification[0].Argv, "-count=1")
	changed, err := c.ID()
	if err != nil || id == changed {
		t.Fatal("check change not bound", err)
	}
	for _, bad := range []string{strings.Replace(Example, "version = 1", "version = 2", 1), "unknown = true\n" + Example, Example + "timeout_seconds = 2\n", strings.Replace(Example, "role = \"planner\"", "role = \"writer\"", 1)} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid config admitted")
		}
	}
}

func TestTopLevelProviderAndOpenCodeSelection(t *testing.T) {
	direct := providerBackedConfig("provider-api", "openai")
	if err := direct.Validate(); err != nil {
		t.Fatal(err)
	}
	if direct.OpenCode != nil {
		t.Fatal("direct inference unexpectedly acquired an OpenCode host")
	}
	directID, err := direct.ID()
	if err != nil {
		t.Fatal(err)
	}
	direct.Provider.Roles["planner"] = ProviderRole{Endpoint: "primary", Model: "planner-model", AdapterControlsJSON: "{}", Variant: ProviderVariant{Effort: "none"}, RequiredCapabilities: &ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}
	if err := direct.Validate(); err == nil {
		t.Fatal("direct inference admitted provider tools")
	}

	opencode := providerBackedConfig("opencode-http", "engorch-openai")
	role := opencode.Provider.Roles["planner"]
	role.RequiredCapabilities = &ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	opencode.Provider.Roles["planner"] = role
	if err := opencode.Validate(); err == nil {
		t.Fatal("OpenCode runtime admitted without pinned host")
	}
	opencode.OpenCode = &OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("a", 64), StateRoot: `D:\state\opencode`}
	if err := opencode.Validate(); err != nil {
		t.Fatal(err)
	}
	opencodeID, err := opencode.ID()
	if err != nil || opencodeID == directID {
		t.Fatal("selected runtime host was not identity-bound", err)
	}
}

func TestNativeWriterOutputIsExplicitAndSchemaBound(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	if err := c.validateNativeWriterOutput(); err != nil {
		t.Fatal("valid native writer configuration rejected", err)
	}
	schema := writercontract.UTF8Schema()
	input := `{"output_schema":` + string(schema) + `,"instruction":"write"}`
	invocation, err := runtime.NewInvocation(*c.Writer, input)
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := c.OpenCodeWriterOutputExpectation(invocation)
	if err != nil || expectation == nil {
		t.Fatal("native writer expectation was not derived", err)
	}
	if !stringEqualCanonical(expectation.Schema, schema) || expectation.RetryCount != 0 {
		t.Fatal("native writer expectation differs from utf8-v2 schema", expectation)
	}

	unauthorized := strings.Replace(input, `"type":"object"`, `"type":"object","additional":"field"`, 1)
	mutated, err := runtime.NewInvocation(*c.Writer, unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenCodeWriterOutputExpectation(mutated); err == nil {
		t.Fatal("unauthorized output_schema field was admitted")
	}
	role := c.Provider.Roles["writer"]
	var controls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal([]byte(role.AdapterControlsJSON), &controls); err != nil {
		t.Fatal(err)
	}
	controls.ToolChoice = "none"
	encoded, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	role.AdapterControlsJSON = string(encoded)
	c.Provider.Roles["writer"] = role
	if err := c.validateNativeWriterOutput(); err == nil {
		t.Fatal("native writer configuration with a forbidden terminal choice was admitted")
	}
}

func TestNativeWriterOutputAllowsExplicitAutoOrRequiredForWriterAndFixer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   string
		choice string
		valid  bool
	}{
		{name: "writer auto", role: "writer", choice: "auto", valid: true},
		{name: "writer required", role: "writer", choice: "required", valid: true},
		{name: "fixer auto", role: "fixer", choice: "auto", valid: true},
		{name: "fixer required", role: "fixer", choice: "required", valid: true},
		{name: "writer empty", role: "writer", choice: "", valid: false},
		{name: "writer none", role: "writer", choice: "none", valid: false},
		{name: "writer named", role: "writer", choice: "structured_output", valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fullNativeWriterOutputConfig(t, tc.role, tc.choice)
			validateErr := c.Validate()
			_, resolveErr := c.ResolveProviderRole(tc.role)
			if tc.valid {
				if validateErr != nil || resolveErr != nil {
					t.Fatalf("explicit native terminal choice rejected: validate=%v resolve=%v", validateErr, resolveErr)
				}
				return
			}
			if validateErr == nil || resolveErr == nil {
				t.Fatalf("forbidden native terminal choice admitted: validate=%v resolve=%v", validateErr, resolveErr)
			}
		})
	}
}

func TestNativeWriterOutputDefaultsToLegacyAbsent(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	c.OpenCode.NativeWriterOutput = false
	schema := writercontract.UTF8Schema()
	invocation, err := runtime.NewInvocation(*c.Writer, `{"output_schema":`+string(schema)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	expectation, err := c.OpenCodeWriterOutputExpectation(invocation)
	if err != nil || expectation != nil {
		t.Fatal("native writer bridge enabled without explicit opt in", expectation, err)
	}
}

func nativeWriterOutputConfig(t *testing.T) Config {
	t.Helper()
	capabilities, err := canonical.Bytes(providergateway.ResponsesModelCapabilities{
		Version: 1, FunctionTools: true, Reasoning: false,
		SystemRoles: []string{"developer"}, TextFormats: []string{"plain"}, KnownExtensions: []string{}, TrailingCostPingV1: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	controls, err := json.Marshal(providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 32, StateMode: "full-input-stateless", Store: false,
		SystemRole: "developer", ToolChoice: "required", FunctionToolStrict: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	providerID := "engorch-openai"
	return Config{
		Version: 2, WriterContract: "utf8-v2",
		Writer: &runtime.Profile{Runtime: "opencode-http", Provider: providerID, Model: "writer-model", Effort: "none", Role: "writer"},
		Provider: &Provider{
			Version:     1,
			Credentials: []ProviderCredential{{Ref: "writer-key", Environment: "ENGORCH_WRITER_KEY"}},
			Endpoints:   []ProviderEndpoint{{Name: "responses", Version: 2, Provider: providerID, URL: "https://api.example.test/v1/responses", AdapterID: providergateway.OpenAIResponsesAdapter, Auth: ProviderAuth{Scheme: "bearer", CredentialRef: "writer-key"}}},
			Models:      []ProviderModel{{Name: "writer-model", Version: 2, Provider: providerID, Model: "writer-model", AdapterID: providergateway.OpenAIResponsesAdapter, Capabilities: ProviderCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: string(capabilities), ContextWindowTokens: 128, MaxCalls: 4, MaxRequestBytes: 1 << 16, MaxResponseBytes: 1 << 16, MaxOutputTokens: 32}},
			Roles:       map[string]ProviderRole{"writer": {Endpoint: "responses", Model: "writer-model", AdapterControlsJSON: string(controls), Variant: ProviderVariant{Effort: "none", SystemRole: "developer"}, RequiredCapabilities: &ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}}},
		},
		OpenCode: &OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("a", 64), StateRoot: `D:\state\opencode`, NativeWriterOutput: true},
	}
}

func fullNativeWriterOutputConfig(t *testing.T, roleName, toolChoice string) Config {
	t.Helper()
	c := nativeWriterOutputConfig(t)
	c.Repository = "native-writer-project"
	c.BaseBranch = "main"
	c.Planner = runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture-v1", Effort: "none", Role: "planner"}
	c.Verification = []Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 120}}
	c.Provider.Models[0].Pricing = &ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}
	role := c.Provider.Roles["writer"]
	var controls providergateway.ResponsesRequestExpectation
	if err := json.Unmarshal([]byte(role.AdapterControlsJSON), &controls); err != nil {
		t.Fatal(err)
	}
	controls.ToolChoice = toolChoice
	encoded, err := json.Marshal(controls)
	if err != nil {
		t.Fatal(err)
	}
	role.AdapterControlsJSON = string(encoded)
	c.Provider.Roles["writer"] = role

	cost := int64(4)
	c.Access = &Access{
		Class:  access.Private,
		Limits: access.Limits{Tokens: 2000, Concurrency: 3},
		Roles:  map[string]string{"planner": "fixture", "writer": "writer-api"},
		Invocations: map[string]InvocationLimit{
			"planner": {Tokens: 1},
			"writer":  {Tokens: 640, CostMicroUSD: &cost},
		},
		Profiles: []access.Profile{
			{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture", RepositoryClasses: []access.Class{access.Private}},
			{Version: 1, Name: "writer-api", Kind: "api", Runtime: "opencode-http", Provider: "engorch-openai", CredentialRef: "writer-key", RepositoryClasses: []access.Class{access.Private}},
		},
	}
	if roleName == "fixer" {
		c.Fixer = &runtime.Profile{Runtime: "opencode-http", Provider: "engorch-openai", Model: "writer-model", Effort: "none", Role: "fixer"}
		c.Provider.Roles["fixer"] = c.Provider.Roles["writer"]
		fixerRole := c.Provider.Roles["fixer"]
		fixerRole.Endpoint = "responses"
		fixerRole.Model = "writer-model"
		c.Provider.Roles["fixer"] = fixerRole
		c.Access.Roles["fixer"] = "writer-api"
		c.Access.Invocations["fixer"] = InvocationLimit{Tokens: 640, CostMicroUSD: &cost}
	}
	return c
}

func stringEqualCanonical(left, right []byte) bool {
	a, err := canonical.Normalize(left)
	if err != nil {
		return false
	}
	b, err := canonical.Normalize(right)
	return err == nil && string(a) == string(b)
}

func TestParseTopLevelDirectProviderConfiguration(t *testing.T) {
	raw := `version = 2
repository = "provider-project"
base_branch = "main"

[planner]
runtime = "provider-api"
provider = "openai"
model = "deployment/opaque-model"
effort = "none"
role = "planner"

[[verification]]
name = "unit"
argv = ["go", "test", "./..."]
timeout_seconds = 120

[access]
class = "PRIVATE"
[access.limits]
tokens = 1000
cost_micro_usd = 10
concurrency = 1
[access.roles]
planner = "provider-api"
[access.invocations.planner]
tokens = 330
cost_micro_usd = 3
[[access.profiles]]
version = 1
name = "provider-api"
kind = "api"
runtime = "provider-api"
provider = "openai"
credential_ref = "team-key"
repository_classes = ["PRIVATE"]

[provider]
version = 1
[[provider.credentials]]
ref = "team-key"
environment = "ENGORCH_PROVIDER_TEAM_KEY"
[[provider.endpoints]]
name = "primary"
version = 2
provider = "openai"
url = "https://api.example.test/v1/chat/completions"
adapter_id = "openai-chat-completions-sse-v1"
[provider.endpoints.auth]
scheme = "bearer"
credential_ref = "team-key"
[[provider.models]]
name = "planner-model"
version = 2
provider = "openai"
model = "deployment/opaque-model"
adapter_id = "openai-chat-completions-sse-v1"
adapter_capabilities_json = "{}"
context_window_tokens = 100
max_calls = 3
max_request_bytes = 4096
max_response_bytes = 8192
max_output_tokens = 10
[provider.models.capabilities]
tools = false
reasoning = false
output_cap = true
complete_usage = true
[provider.models.pricing]
currency = "USD"
unit = "micro_usd_per_million_tokens"
max_input_micro_usd_per_million = 1001
max_output_micro_usd_per_million = 2001
[provider.roles.planner]
endpoint = "primary"
model = "planner-model"
adapter_controls_json = "{}"
[provider.roles.planner.variant]
effort = "none"
[provider.roles.planner.required_capabilities]
tools = false
reasoning = false
structured_output = "TEXT_PARSE_REQUIRED"
`
	raw += fmt.Sprintf(`
[task_pool]
version = 1
path = %q
[task_pool.limits]
total = 2
[task_pool.limits.providers]
openai = 1
[[task_pool.limits.models]]
cap = 1
[task_pool.limits.models.key]
provider = "openai"
model = "deployment/opaque-model"
`, filepath.Join(t.TempDir(), "shared-provider-pool.jsonl"))
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider == nil || c.Provider.Roles["planner"].RequiredCapabilities == nil || c.Provider.Roles["planner"].RequiredCapabilities.StructuredOutput != providergateway.StructuredOutputTextParseRequired {
		t.Fatal("top-level provider role was not decoded")
	}
	if c.TaskPool == nil || c.TaskPool.Limits.Total != 2 || len(c.TaskPool.Limits.Models) != 1 {
		t.Fatal("top-level shared task pool was not decoded")
	}
	if _, err := c.ID(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyConfigIdentityOmitsNewProviderSections(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	type legacyConfig struct {
		Version      int              `json:"version"`
		Repository   string           `json:"repository"`
		BaseBranch   string           `json:"base_branch"`
		Planner      runtime.Profile  `json:"planner"`
		Writer       *runtime.Profile `json:"writer,omitempty"`
		Fixer        *runtime.Profile `json:"fixer,omitempty"`
		Explorer     *runtime.Profile `json:"explorer,omitempty"`
		Reviewer     *runtime.Profile `json:"reviewer,omitempty"`
		Verification []Check          `json:"verification"`
		Codex        *Codex           `json:"codex"`
		Access       *Access          `json:"access,omitempty"`
	}
	want, err := canonical.Hash("harness.config.v1", legacyConfig{c.Version, c.Repository, c.BaseBranch, c.Planner, c.Writer, c.Fixer, c.Explorer, c.Reviewer, c.Verification, c.Codex, c.Access})
	got, gotErr := c.ID()
	if err != nil || gotErr != nil || got != want {
		t.Fatal("legacy configuration identity changed", err, gotErr, got, want)
	}
}

func providerBackedConfig(selectedRuntime, providerID string) Config {
	cost := int64(3)
	return Config{
		Version: 2, Repository: "provider-project", BaseBranch: "main",
		Planner:      runtime.Profile{Runtime: selectedRuntime, Provider: providerID, Model: "deployment/opaque-model", Effort: "none", Role: "planner"},
		Verification: []Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 120}},
		Access:       &Access{Class: access.Private, Limits: access.Limits{Tokens: 1000, CostMicroUSD: int64ConfigPointer(10), Concurrency: 1}, Roles: map[string]string{"planner": "provider-api"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 330, CostMicroUSD: &cost}}, Profiles: []access.Profile{{Version: 1, Name: "provider-api", Kind: "api", Runtime: selectedRuntime, Provider: providerID, CredentialRef: "team-key", RepositoryClasses: []access.Class{access.Private}}}},
		Provider: &Provider{Version: 1,
			Credentials: []ProviderCredential{{Ref: "team-key", Environment: "ENGORCH_PROVIDER_TEAM_KEY"}},
			Endpoints:   []ProviderEndpoint{{Name: "primary", Version: 2, Provider: providerID, URL: "https://api.example.test/v1/chat/completions", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Auth: ProviderAuth{Scheme: "bearer", CredentialRef: "team-key"}}},
			Models:      []ProviderModel{{Name: "planner-model", Version: 2, Provider: providerID, Model: "deployment/opaque-model", AdapterID: providergateway.OpenAIChatCompletionsAdapter, Capabilities: ProviderCapabilities{Tools: true, OutputCap: true, CompleteUsage: true}, AdapterCapabilitiesJSON: "{}", ContextWindowTokens: 100, MaxCalls: 3, MaxRequestBytes: 4096, MaxResponseBytes: 8192, MaxOutputTokens: 10, Pricing: &ProviderPricing{Currency: "USD", Unit: "micro_usd_per_million_tokens", MaxInputMicroUSDPerMillion: 1001, MaxOutputMicroUSDPerMillion: 2001}}},
			Roles:       map[string]ProviderRole{"planner": {Endpoint: "primary", Model: "planner-model", AdapterControlsJSON: "{}", Variant: ProviderVariant{Effort: "none"}, RequiredCapabilities: &ProviderRequiredCapabilities{StructuredOutput: providergateway.StructuredOutputTextParseRequired}}},
		},
	}
}
