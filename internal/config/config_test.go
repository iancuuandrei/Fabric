package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/hostenvironment"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskpool"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func testAbsPath(elems ...string) string {
	root := filepath.VolumeName(os.TempDir()) + string(filepath.Separator)
	return filepath.Join(append([]string{root}, elems...)...)
}

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

func TestUTF8ReplaceV3IsAnExplicitFrozenWriterContract(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.WriterContract = writercontract.ContractUTF8ReplaceV3
	if err := c.Validate(); err != nil {
		t.Fatal("v3 writer contract rejected", err)
	}
	v3ID, err := c.ID()
	if err != nil || legacyID == v3ID {
		t.Fatal("v3 contract was not bound into config identity", err)
	}
	c.WriterContract = "unknown-v3"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown writer contract admitted")
	}
}

func TestUTF8ScopedV4IsAnExplicitWriterContract(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.WriterContract = writercontract.ContractUTF8ScopedV4
	if err := c.Validate(); err != nil {
		t.Fatal("v4 writer contract rejected", err)
	}
	v4ID, err := c.ID()
	if err != nil || v4ID == legacyID {
		t.Fatal("v4 contract was not configuration-identity-bound", err)
	}
	c.WriterContract = "utf8-scoped-v5"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown writer contract admitted")
	}
}

func TestAnchoredEditsV1IsAnExplicitWriterContract(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.WriterContract = writercontract.ContractAnchoredEditsV1
	if err := c.Validate(); err != nil {
		t.Fatal("anchored-edits-v1 rejected", err)
	}
	newID, err := c.ID()
	if err != nil || newID == legacyID {
		t.Fatal("anchored contract was not configuration-identity-bound", err)
	}
	c.WriterContract = "unknown-anchor-v1"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown writer contract admitted")
	}
}

func TestAnchoredEditsV2IsCodexOnlyAndIdentityBound(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Writer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "writer", Effort: "high", Role: "writer"}
	root := t.TempDir()
	c.Codex = &Codex{Executable: filepath.Join(root, "codex.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: filepath.Join(root, "state"), AuthSource: filepath.Join(root, "auth.json")}
	c.WriterContract = writercontract.ContractAnchoredEditsV2
	if err := c.Validate(); err != nil {
		t.Fatal("anchored-edits-v2 Codex route rejected", err)
	}
	v2ID, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.WriterContract = writercontract.ContractAnchoredEditsV1
	v1ID, err := c.ID()
	if err != nil || v1ID == v2ID {
		t.Fatal("v2 validation opt-in was not configuration-identity-bound", err)
	}
	c.WriterContract = writercontract.ContractAnchoredEditsV3
	if err := c.Validate(); err != nil {
		t.Fatal("anchored-edits-v3 Codex route rejected", err)
	}
	v3ID, err := c.ID()
	if err != nil || v3ID == v2ID {
		t.Fatal("v3 strict opt-in was not configuration-identity-bound", err)
	}

	for _, runtimeName := range []string{"fake", "provider-api", "opencode-http"} {
		for _, contract := range []string{writercontract.ContractAnchoredEditsV2, writercontract.ContractAnchoredEditsV3} {
			unsupported := c
			unsupported.WriterContract = contract
			unsupported.Writer = &runtime.Profile{Runtime: runtimeName, Provider: "deterministic", Model: "fixture", Effort: "none", Role: "writer"}
			if runtimeName == "provider-api" || runtimeName == "opencode-http" {
				unsupported.Version = 2
				unsupported.Access = &Access{}
				unsupported.Provider = &Provider{}
			}
			if err := unsupported.Validate(); err == nil || !strings.Contains(err.Error(), "validated anchored edits require a Codex writer runtime") {
				t.Fatalf("%s admitted unsupported runtime %q: %v", contract, runtimeName, err)
			}
		}
	}

	graph := c
	graph.WriterContract = writercontract.ContractAnchoredEditsV2
	graph.PlannerContract = "plan-graph-v4"
	graph.ExplorerContract = "json-v2"
	graph.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "explorer"}
	if err := graph.Validate(); err != nil {
		t.Fatal("plan-graph-v4 rejected anchored-edits-v2", err)
	}
}

func TestExplorerJSONV2ContractIsExplicitAndIdentityBound(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.ExplorerContract = "json-v2"
	if err := c.Validate(); err != nil {
		t.Fatal("json-v2 explorer contract rejected", err)
	}
	v2ID, err := c.ID()
	if err != nil || v2ID == legacyID {
		t.Fatal("json-v2 was not configuration-identity-bound", err)
	}
	c.ExplorerContract = "json-v3"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown explorer contract admitted")
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
	opencode.OpenCode = &OpenCodeHost{Version: 1, Executable: testAbsPath("tools", "opencode"), ExecutableHash: strings.Repeat("a", 64), StateRoot: testAbsPath("state", "opencode")}
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

func TestNativeWriterOutputAcceptsUTF8ReplaceV3Schema(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	c.WriterContract = writercontract.ContractUTF8ReplaceV3
	if err := c.validateNativeWriterOutput(); err != nil {
		t.Fatal("native writer v3 configuration rejected", err)
	}
	in := `{"output_schema":` + string(writercontract.UTF8Schema()) + `,"instruction":"replace complete files"}`
	invocation, err := runtime.NewInvocation(*c.Writer, in)
	if err != nil {
		t.Fatal(err)
	}
	if expectation, err := c.OpenCodeWriterOutputExpectation(invocation); err != nil || expectation == nil {
		t.Fatal("v3 schema was not admitted for native writer output", err)
	}
}

func TestNativeWriterOutputAcceptsCandidateBoundV4Schema(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	c.WriterContract = writercontract.ContractUTF8ScopedV4
	if err := c.validateNativeWriterOutput(); err != nil {
		t.Fatal("native writer v4 configuration rejected", err)
	}
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.UTF8SchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input, err := canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "implement"})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(*c.Writer, string(input))
	if err != nil {
		t.Fatal(err)
	}
	if expectation, err := c.OpenCodeWriterOutputExpectation(invocation); err != nil || expectation == nil {
		t.Fatal("candidate-bound v4 schema rejected", err)
	}
	other, _ := writercontract.UTF8SchemaForCandidate(strings.Repeat("b", 64))
	input, _ = canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": other, "instruction": "implement"})
	invocation, err = runtime.NewInvocation(*c.Writer, string(input))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenCodeWriterOutputExpectation(invocation); err == nil {
		t.Fatal("native schema candidate substitution admitted")
	}
	for _, wrongSchema := range [][]byte{writercontract.UTF8Schema(), writercontract.ChangesJSONSchema()} {
		input, _ := canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": wrongSchema, "instruction": "implement"})
		wrongShape, err := runtime.NewInvocation(*c.Writer, string(input))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.OpenCodeWriterOutputExpectation(wrongShape); err == nil {
			t.Fatal("native v4 writer accepted a different contract schema")
		}
	}
}

func TestNativeWriterOutputAcceptsCandidateBoundAnchoredSchema(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	c.WriterContract = writercontract.ContractAnchoredEditsV1
	if err := c.validateNativeWriterOutput(); err != nil {
		t.Fatal("native anchored writer configuration rejected", err)
	}
	candidate := strings.Repeat("a", 64)
	schema, err := writercontract.AnchoredEditsSchemaForCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	input, err := canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": schema, "instruction": "edit anchors"})
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(*c.Writer, string(input))
	if err != nil {
		t.Fatal(err)
	}
	if expectation, err := c.OpenCodeWriterOutputExpectation(invocation); err != nil || expectation == nil {
		t.Fatal("candidate-bound anchored schema rejected", err)
	}
	other, _ := writercontract.AnchoredEditsSchemaForCandidate(strings.Repeat("b", 64))
	wrongInput, _ := canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": other, "instruction": "edit anchors"})
	wrong, err := runtime.NewInvocation(*c.Writer, string(wrongInput))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.OpenCodeWriterOutputExpectation(wrong); err == nil {
		t.Fatal("anchored schema candidate substitution admitted")
	}
	for _, wrongSchema := range [][]byte{writercontract.UTF8Schema(), writercontract.ChangesJSONSchema()} {
		wrongInput, _ := canonical.Bytes(map[string]any{"candidate_id": candidate, "output_schema": wrongSchema, "instruction": "edit anchors"})
		wrongShape, err := runtime.NewInvocation(*c.Writer, string(wrongInput))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.OpenCodeWriterOutputExpectation(wrongShape); err == nil {
			t.Fatal("native anchored writer accepted a different contract schema")
		}
	}
}

func TestNativeWriterOutputUsesOnlyChangesJSONContractSchema(t *testing.T) {
	c := nativeWriterOutputConfig(t)
	c.WriterContract = writercontract.ContractChangesJSONV1
	if err := c.validateNativeWriterOutput(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		schema json.RawMessage
		valid  bool
	}{
		{name: "changes-json", schema: writercontract.ChangesJSONSchema(), valid: true},
		{name: "utf8", schema: writercontract.UTF8Schema()},
		{name: "anchored", schema: writercontract.AnchoredEditsSchema()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := canonical.Bytes(map[string]any{"candidate_id": strings.Repeat("a", 64), "output_schema": tc.schema, "instruction": "write"})
			if err != nil {
				t.Fatal(err)
			}
			invocation, err := runtime.NewInvocation(*c.Writer, string(input))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.OpenCodeWriterOutputExpectation(invocation)
			if (err == nil) != tc.valid {
				t.Fatalf("contract schema admission mismatch: %v", err)
			}
		})
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
		OpenCode: &OpenCodeHost{Version: 1, Executable: testAbsPath("tools", "opencode"), ExecutableHash: strings.Repeat("a", 64), StateRoot: testAbsPath("state", "opencode"), NativeWriterOutput: true},
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

func TestModelPolicyTOMLExample(t *testing.T) {
	raw := Example + `
[writer]
runtime = "fake"
provider = "deterministic"
model = "writer-standard"
effort = "medium"
role = "writer"

[explorer]
runtime = "fake"
provider = "deterministic"
model = "explorer-standard"
effort = "medium"
role = "explorer"

[model_policy]
Version = 1

[[model_policy.Profiles]]
Name = "writer-standard"
Runtime = "fake"
Provider = "deterministic"
Model = "writer-standard"
Effort = "medium"

[[model_policy.Profiles]]
Name = "writer-strong"
Runtime = "fake"
Provider = "deterministic"
Model = "writer-strong"
Effort = "high"

[[model_policy.Profiles]]
Name = "explorer-economy"
Runtime = "fake"
Provider = "deterministic"
Model = "explorer-economy"
Effort = "low"

[[model_policy.Profiles]]
Name = "explorer-standard"
Runtime = "fake"
Provider = "deterministic"
Model = "explorer-standard"
Effort = "medium"

[[model_policy.Profiles]]
Name = "explorer-strong"
Runtime = "fake"
Provider = "deterministic"
Model = "explorer-strong"
Effort = "high"

[model_policy.Rules.writer]
DefaultProfile = "writer-standard"
EscalatedProfile = "writer-strong"
ContextEscalationTokens = 1000000000
ContextEscalationBytes = 16384
FailureEscalationCount = 1

[model_policy.Rules.explorer]
DefaultProfile = "explorer-standard"
CheapProfile = "explorer-economy"
CheapContextBytes = 2048
EscalatedProfile = "explorer-strong"
ContextEscalationTokens = 1000000000
ContextEscalationBytes = 16384
FailureEscalationCount = 1
`
	c, err := Parse([]byte(raw))
	if err != nil || c.ModelPolicy == nil {
		t.Fatalf("CamelCase model policy TOML rejected: %v", err)
	}
	explorer, err := modelpolicy.Select(*c.ModelPolicy, modelpolicy.Request{Role: "explorer", ReadOnly: true, Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium, Uncertainty: modelpolicy.LevelMedium, ContextBytes: 2048})
	if err != nil || explorer.Profile.Name != "explorer-economy" {
		t.Fatalf("bounded explorer economy route differs: %+v %v", explorer, err)
	}
	writer, err := modelpolicy.Select(*c.ModelPolicy, modelpolicy.Request{Role: "writer", Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium, Uncertainty: modelpolicy.LevelMedium, ContextBytes: 1, Failures: 1})
	if err != nil || writer.Profile.Name != "writer-strong" || writer.Reason != "prior-failures" {
		t.Fatalf("accepted writer failure did not escalate: %+v %v", writer, err)
	}
	if _, err := Parse([]byte(strings.Replace(raw, "DefaultProfile", "default_profile", 1))); err == nil {
		t.Fatal("JSON snake_case policy field was accepted")
	}
}

func TestIntegrationsTOMLParsingValidationAndIDInvariance(t *testing.T) {
	base, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	baseID, err := base.ID()
	if err != nil {
		t.Fatal(err)
	}

	raw := Example + `
[integrations.jira]
base_url = "https://acme.atlassian.net"
email_env = "JIRA_EMAIL"
token_env = "JIRA_API_TOKEN"
api_version = "3"
auto_comment = true
ready_transition = "In Review"

[integrations.notify]
slack_webhook_env = "FABRIC_SLACK_WEBHOOK"
events = ["READY", "FAILED"]

[integrations.webhooks]
secret_env = "FABRIC_WEBHOOK_SECRET"
trigger_label = "fabric"
default_max_usd = 2.5
default_max_turns = 20
`
	cfg, err := Parse([]byte(raw))
	if err != nil || cfg.Integrations == nil || cfg.Integrations.Jira == nil || cfg.Integrations.Notify == nil || cfg.Integrations.Webhooks == nil {
		t.Fatalf("failed parsing [integrations] config: %v", err)
	}
	if cfg.Integrations.Jira.ReadyTransition != "In Review" || cfg.Integrations.Webhooks.TriggerLabel != "fabric" {
		t.Fatalf("unexpected integrations values: %+v", cfg.Integrations)
	}
	badURL := strings.Replace(raw, "https://acme.atlassian.net", "http://acme.atlassian.net", 1)
	if _, err := Parse([]byte(badURL)); err == nil {
		t.Fatal("expected non-loopback HTTP Jira URL rejection")
	}
	// Reloading base Example confirms Config.ID stays unchanged when [integrations] is absent.
	again, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	againID, err := again.ID()
	if err != nil || againID != baseID {
		t.Fatalf("Config.ID shifted unexpectedly: %s != %s", againID, baseID)
	}
}
