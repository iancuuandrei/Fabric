package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/writercontract"
)

// Check declares required verification. Argv is direct executable plus arguments,
// never a shell expression; the controller must retain every configured check.
type Check struct {
	Name           string   `toml:"name" json:"name"`
	Argv           []string `toml:"argv" json:"argv"`
	TimeoutSeconds int      `toml:"timeout_seconds" json:"timeout_seconds"`
}

// Config is the v1 operator-selected repository policy. It is bound into a run,
// so edits cannot silently change an existing run's routing or required checks.
type Config struct {
	CandidateIdentity   string                  `toml:"candidate_identity" json:"candidate_identity,omitempty"`
	WriterContract      string                  `toml:"writer_contract" json:"writer_contract,omitempty"`
	PlannerContract     string                  `toml:"planner_contract" json:"planner_contract,omitempty"`
	ControllerStateRoot string                  `toml:"controller_state_root" json:"controller_state_root,omitempty"`
	Version             int                     `toml:"version" json:"version"`
	Repository          string                  `toml:"repository" json:"repository"`
	BaseBranch          string                  `toml:"base_branch" json:"base_branch"`
	Planner             runtime.Profile         `toml:"planner" json:"planner"`
	Writer              *runtime.Profile        `toml:"writer" json:"writer,omitempty"`
	Fixer               *runtime.Profile        `toml:"fixer" json:"fixer,omitempty"`
	Explorer            *runtime.Profile        `toml:"explorer" json:"explorer,omitempty"`
	Reviewer            *runtime.Profile        `toml:"reviewer" json:"reviewer,omitempty"`
	Verification        []Check                 `toml:"verification" json:"verification"`
	Codex               *Codex                  `toml:"codex" json:"codex"`
	Access              *Access                 `toml:"access" json:"access,omitempty"`
	Provider            *Provider               `toml:"provider" json:"provider,omitempty"`
	OpenCode            *OpenCodeHost           `toml:"opencode" json:"opencode,omitempty"`
	TaskPool            *TaskPool               `toml:"task_pool" json:"task_pool,omitempty"`
	HostPolicy          *hostenvironment.Policy `toml:"host_policy" json:"host_policy,omitempty"`
	Exploration         *ExplorationPolicy      `toml:"exploration" json:"exploration,omitempty"`
}

// ExplorationPolicy bounds durable explorer evidence separately from the
// smaller projection supplied to writer invocations.
type ExplorationPolicy struct {
	Version           int `toml:"version" json:"version"`
	MaxRecords        int `toml:"max_records" json:"max_records"`
	MaxContextRecords int `toml:"max_context_records" json:"max_context_records"`
}

const legacyExplorationLimit = 16

// MaxExplorationRecords returns the bound fixed into this configuration.
// Configurations that predate ExplorationPolicy retain the legacy limit.
func (c Config) MaxExplorationRecords() int {
	if c.Exploration == nil {
		return legacyExplorationLimit
	}
	return c.Exploration.MaxRecords
}

// MaxExplorationContextRecords returns the maximum explorer records projected
// into a writer invocation. Omitted records remain hash-bound by the writer.
func (c Config) MaxExplorationContextRecords() int {
	if c.Exploration == nil {
		return legacyExplorationLimit
	}
	return c.Exploration.MaxContextRecords
}

// Codex pins the local runtime binary and operator-selected state/authentication
// locations. Authentication contents are never part of configuration or journals.
type Codex struct {
	Executable                  string `toml:"executable" json:"executable"`
	ExecutableHash              string `toml:"executable_hash" json:"executable_hash"`
	StateRoot                   string `toml:"state_root" json:"state_root"`
	AuthSource                  string `toml:"auth_source" json:"auth_source"`
	CapabilityConfinement       string `toml:"capability_confinement" json:"capability_confinement,omitempty"`
	CapabilityAttestationPath   string `toml:"capability_attestation_path" json:"capability_attestation_path,omitempty"`
	CapabilityAttestationSHA256 string `toml:"capability_attestation_sha256" json:"capability_attestation_sha256,omitempty"`
	RequireLiveUsage            bool   `toml:"require_live_usage" json:"require_live_usage,omitempty"`
	UsageQualified              bool   `toml:"usage_qualified" json:"usage_qualified,omitempty"`
}

// OpenCodeHost pins the selected OpenCode executable and its private state
// root. Provider credentials remain controller-owned Provider references.
type OpenCodeHost struct {
	Version                  int    `toml:"version" json:"version"`
	Executable               string `toml:"executable" json:"executable"`
	ExecutableHash           string `toml:"executable_hash" json:"executable_hash"`
	StateRoot                string `toml:"state_root" json:"state_root"`
	InvocationTimeoutSeconds int    `toml:"invocation_timeout_seconds" json:"invocation_timeout_seconds,omitempty"`
	ReadbackTimeoutSeconds   int    `toml:"readback_timeout_seconds" json:"readback_timeout_seconds,omitempty"`
	// NativeWriterOutput opts only writer/fixer OpenCode Responses turns into
	// the native StructuredOutput terminal tool. It is deliberately explicit so
	// legacy utf8-v2 routes remain prompt-only unless an operator opts in.
	NativeWriterOutput bool `toml:"native_writer_output" json:"native_writer_output,omitempty"`
}

// TaskPool fixes one shared cross-run capacity journal and its immutable
// all-applicable limits. It accounts capacity and grants no dispatch authority.
type TaskPool struct {
	Version int             `toml:"version" json:"version"`
	Path    string          `toml:"path" json:"path"`
	Limits  taskpool.Limits `toml:"limits" json:"limits"`
}

// Validate rejects unsupported versions, incomplete profiles and ambiguous checks.
func (c Config) Validate() error {
	if c.ControllerStateRoot != "" && (!filepath.IsAbs(c.ControllerStateRoot) || filepath.Clean(c.ControllerStateRoot) != c.ControllerStateRoot || filepath.Clean(c.ControllerStateRoot) == filepath.VolumeName(c.ControllerStateRoot)+string(filepath.Separator)) {
		return errors.New("controller state root must be an absolute clean non-volume-root path")
	}
	if c.CandidateIdentity != "" && c.CandidateIdentity != "semantic-index-v2" {
		return errors.New("unsupported candidate identity contract")
	}
	if c.WriterContract != "" && c.WriterContract != "nonempty-v1" && c.WriterContract != "utf8-v2" && c.WriterContract != writercontract.ContractChangesJSONV1 {
		return errors.New("unsupported writer contract")
	}
	if c.PlannerContract != "" && c.PlannerContract != "plan-v1" {
		return errors.New("unsupported planner contract")
	}
	if (c.Version != 1 && c.Version != 2) || strings.TrimSpace(c.Repository) == "" || len(c.Repository) > 256 {
		return errors.New("invalid configuration identity")
	}
	if c.Version == 1 && c.Access != nil || c.Version == 2 && c.Access == nil {
		return errors.New("configuration version requires explicit access schema")
	}
	if c.Version == 1 && c.Fixer != nil {
		return errors.New("independent fixer requires configuration v2")
	}
	if c.Access != nil {
		if _, err := c.accessPolicy(strings.Repeat("0", 64)); err != nil {
			return err
		}
	}
	if c.TaskPool != nil {
		if c.Version != 2 || c.TaskPool.Version != 1 || !filepath.IsAbs(c.TaskPool.Path) || filepath.Clean(c.TaskPool.Path) != c.TaskPool.Path || c.TaskPool.Limits.Validate() != nil {
			return errors.New("invalid shared task pool configuration")
		}
	}
	if c.HostPolicy != nil {
		if err := hostenvironment.ValidatePolicy(*c.HostPolicy); err != nil {
			return err
		}
	}
	if c.Exploration != nil {
		p := c.Exploration
		if c.Version != 2 || p.Version != 1 || p.MaxRecords < 1 || p.MaxRecords > 256 || p.MaxContextRecords < 1 || p.MaxContextRecords > p.MaxRecords || p.MaxContextRecords > 64 {
			return errors.New("invalid exploration policy")
		}
	}
	if c.BaseBranch == "" || strings.HasPrefix(c.BaseBranch, "-") || strings.ContainsAny(c.BaseBranch, " \t\r\n~^:?*[\\") || strings.Contains(c.BaseBranch, "..") {
		return errors.New("invalid base branch")
	}
	if c.Planner.Role != "planner" {
		return errors.New("planner role required")
	}
	if err := c.Planner.Validate(); err != nil {
		return err
	}
	needsCodex, needsProvider, needsOpenCode := false, false, false
	profiles := map[string]*runtime.Profile{"planner": &c.Planner, "writer": c.Writer, "fixer": c.Fixer, "explorer": c.Explorer, "reviewer": c.Reviewer}
	for _, role := range []string{"planner", "writer", "fixer", "explorer", "reviewer"} {
		p := profiles[role]
		if p == nil {
			continue
		}
		if p.Role != role {
			return errors.New("configured runtime role mismatch")
		}
		if err := p.Validate(); err != nil {
			return err
		}
		if c.TaskPool != nil && p.Runtime == "fake" {
			return errors.New("shared task pool is unavailable for the deterministic fake runtime")
		}
		switch p.Runtime {
		case "fake":
			if p.Provider != "deterministic" {
				return errors.New("fake runtime requires deterministic provider")
			}
		case "codex-app-server":
			needsCodex = true
		case "provider-api":
			needsProvider = true
		case "opencode-http":
			needsProvider = true
			needsOpenCode = true
		default:
			return errors.New("unsupported configured runtime")
		}
	}
	if needsCodex {
		if c.Codex == nil {
			return errors.New("pinned Codex host configuration required")
		}
		// UsageQualified is an operator-recorded preflight evidence gate. It does
		// not automatically prove that a Codex build reports complete live usage.
		if c.Codex.RequireLiveUsage && !c.Codex.UsageQualified {
			return errors.New("strict Codex live usage requires operator qualification")
		}
		if c.Codex.RequireLiveUsage && c.Version != 2 {
			return errors.New("strict Codex live usage requires explicit v2 access reservations")
		}
		if c.Codex.CapabilityConfinement != "" && (c.Version != 2 || c.Codex.CapabilityConfinement != "required") {
			return errors.New("Codex capability confinement must be required under configuration v2")
		}
		attestationConfigured := c.Codex.CapabilityAttestationPath != "" || c.Codex.CapabilityAttestationSHA256 != ""
		if attestationConfigured {
			if c.Codex.CapabilityConfinement != "required" || !filepath.IsAbs(c.Codex.CapabilityAttestationPath) {
				return errors.New("Codex capability attestation requires strict confinement and an absolute path")
			}
			if err := safepath.RequireDigest(c.Codex.CapabilityAttestationSHA256); err != nil {
				return err
			}
		}
		for _, p := range []string{c.Codex.Executable, c.Codex.StateRoot, c.Codex.AuthSource} {
			if !filepath.IsAbs(p) {
				return errors.New("absolute Codex host paths required")
			}
		}
		if err := safepath.RequireDigest(c.Codex.ExecutableHash); err != nil {
			return err
		}
	} else if c.Codex != nil {
		return errors.New("Codex configuration supplied without a Codex runtime")
	}
	if needsProvider {
		if c.Version != 2 || c.Provider == nil || c.Access == nil {
			return errors.New("provider runtimes require explicit v2 provider and access configuration")
		}
		values := make(map[string]runtime.Profile, len(profiles))
		for role, profile := range profiles {
			if profile != nil {
				values[role] = *profile
			}
		}
		var providerErr error
		if nativeSchemas := c.nativeWriterSchemas(); len(nativeSchemas) != 0 {
			providerErr = c.Provider.ValidateRolesWithNativeWriterOutput(values, c.Access, nativeSchemas)
		} else {
			providerErr = c.Provider.ValidateRoles(values, c.Access)
		}
		if err := providerErr; err != nil {
			return err
		}
	} else if c.Provider != nil {
		return errors.New("provider configuration supplied without a provider runtime")
	}
	if needsOpenCode {
		if c.OpenCode == nil || c.OpenCode.Version != 1 || !filepath.IsAbs(c.OpenCode.Executable) || !filepath.IsAbs(c.OpenCode.StateRoot) {
			return errors.New("pinned OpenCode host configuration required")
		}
		if err := safepath.RequireDigest(c.OpenCode.ExecutableHash); err != nil {
			return err
		}
		if c.OpenCode.InvocationTimeoutSeconds < 0 || c.OpenCode.InvocationTimeoutSeconds > 7200 || c.OpenCode.ReadbackTimeoutSeconds < 0 || c.OpenCode.ReadbackTimeoutSeconds > 300 {
			return errors.New("OpenCode deadlines exceed finite bounds")
		}
		if err := c.validateNativeWriterOutput(); err != nil {
			return err
		}
	} else if c.OpenCode != nil {
		return errors.New("OpenCode configuration supplied without an OpenCode runtime")
	}
	if len(c.Verification) == 0 || len(c.Verification) > 64 {
		return errors.New("one to 64 required checks expected")
	}
	seen := map[string]bool{}
	for _, v := range c.Verification {
		if strings.TrimSpace(v.Name) == "" || seen[v.Name] || len(v.Argv) == 0 || len(v.Argv) > 128 || v.TimeoutSeconds < 1 || v.TimeoutSeconds > 86400 {
			return errors.New("invalid verification check")
		}
		seen[v.Name] = true
		for _, arg := range v.Argv {
			if strings.ContainsRune(arg, 0) || len(arg) > 8192 {
				return errors.New("invalid argv")
			}
		}
		if strings.TrimSpace(v.Argv[0]) == "" {
			return errors.New("empty executable")
		}
	}
	return nil
}

// OpenCodeWriterOutputExpectation returns the exact native terminal contract
// for an explicitly enabled writer/fixer invocation. A nil result preserves
// all legacy routes and every read-only role.
func (c Config) OpenCodeWriterOutputExpectation(invocation runtime.Invocation) (*opencode.StructuredOutputExpectation, error) {
	if c.OpenCode == nil || !c.OpenCode.NativeWriterOutput {
		return nil, nil
	}
	if err := c.validateNativeWriterOutput(); err != nil {
		return nil, err
	}
	if invocation.Profile.Runtime != "opencode-http" || invocation.Profile.Role != "writer" && invocation.Profile.Role != "fixer" {
		return nil, nil
	}
	var request struct {
		OutputSchema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(invocation.Input), &request); err != nil || len(request.OutputSchema) == 0 || string(request.OutputSchema) == "null" {
		return nil, errors.New("native writer output schema missing from invocation")
	}
	wantUTF8, err := canonical.Normalize(writercontract.UTF8Schema())
	if err != nil {
		return nil, err
	}
	wantJSON, err := canonical.Normalize(writercontract.ChangesJSONSchema())
	if err != nil {
		return nil, err
	}
	got, err := canonical.Normalize(request.OutputSchema)
	if err != nil || !bytes.Equal(got, wantUTF8) && !bytes.Equal(got, wantJSON) {
		return nil, errors.New("native writer output schema differs from admitted writer contract")
	}
	expectation, err := opencode.NewStructuredOutputExpectation(request.OutputSchema)
	if err != nil {
		return nil, err
	}
	return &expectation, nil
}

// ResolveProviderRole resolves one role with the configuration's explicit
// native writer/fixer terminal policy. Generic configurations delegate to the
// provider resolver unchanged.
func (c Config) ResolveProviderRole(role string) (ResolvedProviderRole, error) {
	if c.Provider == nil {
		return ResolvedProviderRole{}, errors.New("provider configuration unavailable")
	}
	if schemas := c.nativeWriterSchemas(); len(schemas) != 0 {
		if err := c.Provider.validate(schemas); err != nil {
			return ResolvedProviderRole{}, err
		}
		return c.Provider.resolveRole(role)
	}
	return c.Provider.ResolveRole(role)
}

func (c Config) nativeWriterSchemas() map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	if c.OpenCode == nil || !c.OpenCode.NativeWriterOutput {
		return result
	}
	schema := writercontract.UTF8Schema()
	if c.WriterContract == writercontract.ContractChangesJSONV1 {
		schema = writercontract.ChangesJSONSchema()
	}
	for role, profile := range map[string]*runtime.Profile{"writer": c.Writer, "fixer": c.Fixer} {
		if profile != nil && profile.Runtime == "opencode-http" {
			result[role] = append(json.RawMessage(nil), schema...)
		}
	}
	return result
}

func (c Config) validateNativeWriterOutput() error {
	if c.OpenCode == nil || !c.OpenCode.NativeWriterOutput {
		return nil
	}
	if c.Version != 2 || c.WriterContract != "utf8-v2" && c.WriterContract != writercontract.ContractChangesJSONV1 {
		return errors.New("native OpenCode writer output requires configuration v2 utf8-v2 or changes-json-v1")
	}
	if c.Provider == nil {
		return errors.New("native OpenCode writer output requires provider configuration")
	}
	if err := c.Provider.validate(c.nativeWriterSchemas()); err != nil {
		return err
	}
	eligible := false
	for role, profile := range map[string]*runtime.Profile{"writer": c.Writer, "fixer": c.Fixer} {
		if profile == nil || profile.Runtime != "opencode-http" {
			continue
		}
		eligible = true
		resolved, err := c.ResolveProviderRole(role)
		if err != nil {
			return err
		}
		if resolved.Model.AdapterID != providergateway.OpenAIResponsesAdapter {
			return errors.New("native OpenCode writer output requires Responses adapter")
		}
		var controls providergateway.ResponsesRequestExpectation
		if json.Unmarshal(resolved.AdapterControls, &controls) != nil || controls.ToolChoice != "required" && controls.ToolChoice != "auto" {
			return errors.New("native OpenCode writer output requires Responses tool_choice auto or required")
		}
		if resolved.RequiredCapabilities == nil || !resolved.RequiredCapabilities.Tools || resolved.RequiredCapabilities.StructuredOutput != providergateway.StructuredOutputUnsupported || len(resolved.RequiredCapabilities.Schema) != 0 || resolved.RequiredCapabilities.SchemaName != "" {
			return errors.New("native OpenCode writer output requires tool-only provider capabilities")
		}
	}
	if !eligible {
		return errors.New("native OpenCode writer output requires an OpenCode writer or fixer")
	}
	return nil
}

// Parse rejects unknown keys, duplicate TOML definitions and values beyond 64 KiB.
func Parse(raw []byte) (Config, error) {
	var c Config
	if len(raw) > 64<<10 {
		return c, errors.New("configuration exceeds 64 KiB")
	}
	err := toml.NewDecoder(bytes.NewReader(raw)).DisallowUnknownFields().Decode(&c)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

// ID returns a content identity of validated semantic configuration, independent
// of TOML formatting. It must be rebound when any required check changes.
func (c Config) ID() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if c.Version == 2 {
		return canonical.Hash("harness.config.v2", c)
	}
	return canonical.Hash("harness.config.v1", c)
}

// Access declares explicit privacy, resource limits and route-to-profile mapping.
// Profile names are operator labels; derived policy uses their content identities.
type Access struct {
	Class       access.Class               `toml:"class" json:"class"`
	Limits      access.Limits              `toml:"limits" json:"limits"`
	Profiles    []access.Profile           `toml:"profiles" json:"profiles"`
	Roles       map[string]string          `toml:"roles" json:"roles"`
	Invocations map[string]InvocationLimit `toml:"invocations" json:"invocations"`
}

// InvocationLimit declares the resource ceiling reserved for each invocation of
// a role. It is distinct from the cumulative run budget and bound into Config ID.
type InvocationLimit struct {
	Tokens          int64  `toml:"tokens" json:"tokens"`
	UnlimitedTokens bool   `toml:"unlimited_tokens" json:"unlimited_tokens,omitempty"`
	CostMicroUSD    *int64 `toml:"cost_micro_usd" json:"cost_micro_usd"`
}

// AccessPolicy derives run-bound admission policy. Legacy configurations have no
// implicit access profile and cannot use this entry point.
func (c Config) AccessPolicy(runID string) (access.Policy, error) {
	if err := c.Validate(); err != nil {
		return access.Policy{}, err
	}
	return c.accessPolicy(runID)
}

func (c Config) accessPolicy(runID string) (access.Policy, error) {
	if c.Version != 2 || c.Access == nil {
		return access.Policy{}, errors.New("explicit v2 access configuration required")
	}
	a := c.Access
	p := access.Policy{Version: 1, RunID: runID, Class: a.Class, Limits: a.Limits, Profiles: a.Profiles}
	profiles := map[string]access.Profile{}
	for _, profile := range a.Profiles {
		profiles[profile.Name] = profile
	}
	for _, configured := range []*runtime.Profile{&c.Planner, c.Explorer, c.Writer, c.Fixer, c.Reviewer} {
		if configured == nil {
			continue
		}
		name, ok := a.Roles[configured.Role]
		if !ok {
			return p, errors.New("role access profile missing")
		}
		profile, ok := profiles[name]
		if !ok {
			return p, errors.New("unknown role access profile")
		}
		limit, ok := a.Invocations[configured.Role]
		if !ok {
			return p, errors.New("role invocation limit missing")
		}
		ledger, err := access.NewLedger(a.Limits)
		if err != nil {
			return p, err
		}
		if err := ledger.Reserve(access.Reservation{InvocationID: strings.Repeat("0", 64), Tokens: limit.Tokens, UnlimitedTokens: limit.UnlimitedTokens, CostMicroUSD: limit.CostMicroUSD, BillingMode: profile.Kind}); err != nil {
			return p, err
		}
		id, err := profile.ID()
		if err != nil {
			return p, err
		}
		permission := "read-only"
		if configured.Role == "writer" || configured.Role == "fixer" {
			permission = "workspace-write"
		}
		p.Routes = append(p.Routes, access.Route{Version: 1, Role: configured.Role, Runtime: configured.Runtime, Provider: configured.Provider, Model: configured.Model, Effort: configured.Effort, AccessID: id, Permission: permission})
	}
	if len(a.Roles) != len(p.Routes) || len(a.Invocations) != len(p.Routes) {
		return p, errors.New("access mapping contains unconfigured roles")
	}
	_, err := p.ID()
	return p, err
}

// Example is a local fake-runtime configuration with an explicit Go test check.
// Operators must edit the check to fit their repository before planning.
const Example = `version = 1
repository = "local-project"
base_branch = "main"

[planner]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "planner"

[[verification]]
name = "unit"
argv = ["go", "test", "./..."]
timeout_seconds = 120
`
