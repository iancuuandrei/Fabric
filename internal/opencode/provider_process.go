package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// ProviderStartupAttempt records one terminal provider-host startup attempt.
// Configuration remains present when exact /config admission succeeded but a
// later project readiness gate rejected that same process.
type ProviderStartupAttempt struct {
	Number                   int
	Outcome                  string
	Configuration            ProviderConfigurationReceipt
	Reaped                   bool
	ReadinessElapsedMillis   int64
	ReadinessPolls           int
	ReadinessHTTPObserved    bool
	FirstHTTPResponseMillis  int64
	FinalReadinessErrorClass string
	TCPObserved              bool
	FirstTCPMillis           int64
	HealthHTTPObserved       bool
	FirstHealthHTTPMillis    int64
	FinalHealthErrorClass    string
	ProcessExited            bool
	ProjectReadinessPolls    int
	ProjectReadinessMillis   int64
	Err                      error
}

type processProviderLaunchIdentity struct {
	ProviderID           string
	ModelID              string
	Protocol             ProviderProtocol
	SDKPackage           string
	SDKVersion           string
	GatewayBaseURL       string
	CapabilitySHA256     string
	ConfigurationSHA256  string
	ContextWindowTokens  int64
	MaxOutputTokens      int64
	RuntimeOutputTokens  int64
	Tools                bool
	Reasoning            bool
	TimeoutMillis        int
	Variant              string
	ReasoningEffort      string
	ReasoningSummary     string
	ThinkingMode         string
	ThinkingBudgetTokens *int64
	AnthropicEffort      string
	RuntimeAgent         string
	TextVerbosity        string
	Temperature          string
	TopP                 string
}

// ProviderProcessIdentity is a secret-free, durable snapshot of the provider
// route used to launch one admitted Process. CapabilitySHA256 proves equality
// to a high-entropy scoped capability; it is not an authentication credential.
type ProviderProcessIdentity struct {
	SHA256               string                       `json:"sha256"`
	ProviderID           string                       `json:"provider_id"`
	ModelID              string                       `json:"model_id"`
	Protocol             ProviderProtocol             `json:"protocol"`
	SDKPackage           string                       `json:"sdk_package"`
	SDKVersion           string                       `json:"sdk_version,omitempty"`
	GatewayBaseURL       string                       `json:"gateway_base_url"`
	CapabilitySHA256     string                       `json:"capability_sha256"`
	ConfigurationSHA256  string                       `json:"configuration_sha256"`
	ContextWindowTokens  int64                        `json:"context_window_tokens"`
	MaxOutputTokens      int64                        `json:"max_output_tokens"`
	RuntimeOutputTokens  int64                        `json:"runtime_output_tokens"`
	Tools                bool                         `json:"tools"`
	Reasoning            bool                         `json:"reasoning"`
	TimeoutMillis        int                          `json:"timeout_millis"`
	Variant              string                       `json:"variant"`
	ReasoningEffort      string                       `json:"reasoning_effort,omitempty"`
	ReasoningSummary     string                       `json:"reasoning_summary,omitempty"`
	ThinkingMode         string                       `json:"thinking_mode,omitempty"`
	ThinkingBudgetTokens *int64                       `json:"thinking_budget_tokens,omitempty"`
	AnthropicEffort      string                       `json:"anthropic_effort,omitempty"`
	RuntimeAgent         string                       `json:"runtime_agent,omitempty"`
	TextVerbosity        string                       `json:"text_verbosity,omitempty"`
	Temperature          string                       `json:"temperature,omitempty"`
	TopP                 string                       `json:"top_p,omitempty"`
	Configuration        ProviderConfigurationReceipt `json:"configuration"`
}

// StartReadyLimitedProviderToolsProcess starts the pinned runtime with one
// exact typed provider and MCP configuration. Success requires the same
// provider route, model variant, limits, and tools to be read back from
// /config before any session or dispatch exists.
func StartReadyLimitedProviderToolsProcess(ctx context.Context, binary, digest, root string, port int, user, password string, output io.Writer, attempts int, readinessTimeout time.Duration, diagnostic bool, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec) (*Process, ProviderConfigurationReceipt, []ProviderStartupAttempt, error) {
	return StartReadyLimitedProviderToolsProcessInDirectory(ctx, binary, digest, root, root, port, user, password, output, attempts, readinessTimeout, diagnostic, provider, tools)
}

// StartReadyLimitedProviderToolsProcessInDirectory keeps private OpenCode state
// under stateRoot while starting the admitted host in workingDirectory. The two
// absolute directories are snapshotted independently in the Process launch
// identity; provider and tool admission behavior is otherwise identical.
func StartReadyLimitedProviderToolsProcessInDirectory(ctx context.Context, binary, digest, stateRoot, workingDirectory string, port int, user, password string, output io.Writer, attempts int, readinessTimeout time.Duration, diagnostic bool, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec) (*Process, ProviderConfigurationReceipt, []ProviderStartupAttempt, error) {
	return startReadyLimitedProviderToolsProcessInDirectory(ctx, binary, digest, stateRoot, workingDirectory, port, user, password, output, attempts, readinessTimeout, diagnostic, provider, tools, nil)
}

// StartReadyLimitedProviderToolsProjectProcessInDirectory admits provider and
// tool configuration, then waits boundedly for the exact project identity on
// the same process. It creates no session and performs no provider request.
func StartReadyLimitedProviderToolsProjectProcessInDirectory(ctx context.Context, binary, digest, stateRoot, workingDirectory string, port int, user, password string, output io.Writer, attempts int, readinessTimeout time.Duration, diagnostic bool, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec, project ProjectExpectation) (*Process, ProviderConfigurationReceipt, []ProviderStartupAttempt, error) {
	return startReadyLimitedProviderToolsProcessInDirectory(ctx, binary, digest, stateRoot, workingDirectory, port, user, password, output, attempts, readinessTimeout, diagnostic, provider, tools, &project)
}

func startReadyLimitedProviderToolsProcessInDirectory(ctx context.Context, binary, digest, stateRoot, workingDirectory string, port int, user, password string, output io.Writer, attempts int, readinessTimeout time.Duration, diagnostic bool, provider ProviderConfigurationSpec, tools ToolsConfigurationSpec, project *ProjectExpectation) (*Process, ProviderConfigurationReceipt, []ProviderStartupAttempt, error) {
	provider, tools = snapshotProviderConfiguration(provider), snapshotToolsConfiguration(tools)
	normalized, err := normalizeProviderConfiguration(provider)
	if err != nil {
		return nil, ProviderConfigurationReceipt{}, nil, err
	}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		return nil, ProviderConfigurationReceipt{}, nil, err
	}
	process, admission, attemptsEvidence, err := startReadyLimitedProcessWithAdmissionInDirectory(
		ctx, binary, digest, stateRoot, workingDirectory, port, user, password, output, normalized.runtimeOutputTokens,
		attempts, readinessTimeout, diagnostic, &tools, &provider, content, project,
		func(raw []byte) (startupAdmission, error) {
			receipt, decodeErr := decodeProviderToolsConfiguration(raw, provider, tools)
			return startupAdmission{tools: receipt.ToolsConfiguration, provider: receipt}, decodeErr
		},
	)
	evidence := make([]ProviderStartupAttempt, len(attemptsEvidence))
	for index, attempt := range attemptsEvidence {
		evidence[index] = ProviderStartupAttempt{
			Number: attempt.Number, Outcome: attempt.Outcome,
			Reaped: attempt.Reaped, ReadinessElapsedMillis: attempt.ReadinessElapsedMillis, ReadinessPolls: attempt.ReadinessPolls,
			ReadinessHTTPObserved: attempt.ReadinessHTTPObserved, FirstHTTPResponseMillis: attempt.FirstHTTPResponseMillis,
			FinalReadinessErrorClass: attempt.FinalReadinessErrorClass, Err: attempt.Err,
			TCPObserved: attempt.TCPObserved, FirstTCPMillis: attempt.FirstTCPMillis,
			HealthHTTPObserved: attempt.HealthHTTPObserved, FirstHealthHTTPMillis: attempt.FirstHealthHTTPMillis,
			FinalHealthErrorClass: attempt.FinalHealthErrorClass, ProcessExited: attempt.ProcessExited,
			ProjectReadinessPolls: attempt.ProjectReadinessPolls, ProjectReadinessMillis: attempt.ProjectReadinessMillis,
		}
		evidence[index].Configuration = attempt.ProviderConfiguration
	}
	return process, admission.provider, evidence, err
}

// ProviderCapabilitySHA256 derives the secret-free identity used to bind a
// provider proxy listener to the Process configured with the same capability.
func ProviderCapabilitySHA256(capability string) (string, error) {
	if len(capability) < 32 || len(capability) > 512 || !safeCapability(capability) {
		return "", errors.New("safe scoped provider gateway capability required")
	}
	return canonical.Hash("harness.opencode-process-provider-capability.v1", struct {
		Capability string `json:"capability"`
	}{capability})
}

func newProcessProviderLaunchIdentity(spec ProviderConfigurationSpec, content string) (processProviderLaunchIdentity, error) {
	normalized, err := normalizeProviderConfiguration(spec)
	if err != nil {
		return processProviderLaunchIdentity{}, err
	}
	digest, err := ProviderCapabilitySHA256(spec.GatewayCapability)
	if err != nil {
		return processProviderLaunchIdentity{}, err
	}
	contentHash := sha256.Sum256([]byte(content))
	identity := processProviderLaunchIdentity{
		ProviderID: spec.ProviderID, ModelID: spec.ModelID, Protocol: spec.Protocol,
		SDKPackage: normalized.sdkPackage, SDKVersion: normalized.sdkVersion, GatewayBaseURL: spec.GatewayBaseURL,
		CapabilitySHA256: digest, ConfigurationSHA256: hex.EncodeToString(contentHash[:]), ContextWindowTokens: spec.ContextWindowTokens,
		MaxOutputTokens: spec.MaxOutputTokens, RuntimeOutputTokens: normalized.runtimeOutputTokens, Tools: spec.Tools, Reasoning: spec.Reasoning,
		TimeoutMillis: spec.TimeoutMillis, Variant: spec.Variant.Name,
	}
	if spec.Variant.Responses != nil {
		identity.ReasoningEffort = spec.Variant.Responses.Effort
		identity.ReasoningSummary = spec.Variant.Responses.Summary
		identity.TextVerbosity = spec.Variant.Responses.TextVerbosity
		if spec.Variant.Responses.Temperature != nil {
			identity.RuntimeAgent = spec.RuntimeAgent
			identity.Temperature = spec.Variant.Responses.Temperature.String()
		}
		if spec.Variant.Responses.TopP != nil {
			identity.RuntimeAgent = spec.RuntimeAgent
			identity.TopP = spec.Variant.Responses.TopP.String()
		}
	}
	if spec.Variant.Anthropic != nil {
		identity.ThinkingMode = spec.Variant.Anthropic.ThinkingMode
		identity.AnthropicEffort = spec.Variant.Anthropic.Effort
		if spec.Variant.Anthropic.ThinkingBudgetTokens != nil {
			budget := *spec.Variant.Anthropic.ThinkingBudgetTokens
			identity.ThinkingBudgetTokens = &budget
		}
	}
	return identity, nil
}

func (p *Process) setAdmittedProvider(receipt ProviderConfigurationReceipt) error {
	if p == nil {
		return errors.New("invalid OpenCode provider admission transition")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.launch.Provider == nil || p.admittedProvider != nil || p.admittedTools == nil {
		return errors.New("invalid OpenCode provider admission transition")
	}
	launch := p.launch.Provider
	if safepath.RequireDigest(receipt.SHA256) != nil ||
		receipt.ProviderID != launch.ProviderID || receipt.ModelID != launch.ModelID ||
		receipt.Protocol != launch.Protocol || receipt.SDKPackage != launch.SDKPackage || receipt.SDKVersion != launch.SDKVersion ||
		receipt.GatewayBaseURL != launch.GatewayBaseURL ||
		receipt.ContextWindowTokens != launch.ContextWindowTokens || receipt.MaxOutputTokens != launch.MaxOutputTokens || receipt.RuntimeOutputTokens != launch.RuntimeOutputTokens ||
		receipt.Tools != launch.Tools || receipt.Reasoning != launch.Reasoning ||
		receipt.TimeoutMillis != launch.TimeoutMillis || receipt.Variant != launch.Variant ||
		receipt.ReasoningEffort != launch.ReasoningEffort || receipt.ReasoningSummary != launch.ReasoningSummary ||
		receipt.RuntimeAgent != launch.RuntimeAgent || receipt.TextVerbosity != launch.TextVerbosity || receipt.Temperature != launch.Temperature || receipt.TopP != launch.TopP ||
		receipt.ThinkingMode != launch.ThinkingMode || !equalOptionalInt64(receipt.ThinkingBudgetTokens, launch.ThinkingBudgetTokens) || receipt.AnthropicEffort != launch.AnthropicEffort ||
		receipt.ToolsConfiguration.SHA256 != p.admittedTools.SHA256 ||
		receipt.ToolsConfiguration.MCPServer != p.admittedTools.MCPServer ||
		receipt.ToolsConfiguration.Endpoint != p.admittedTools.Endpoint ||
		receipt.ToolsConfiguration.TimeoutMillis != p.admittedTools.TimeoutMillis ||
		receipt.ToolsConfiguration.AllowStructuredOutput != p.admittedTools.AllowStructuredOutput ||
		!slices.Equal(receipt.ToolsConfiguration.ToolIDs, p.admittedTools.ToolIDs) {
		return errors.New("OpenCode provider admission differs from launch")
	}
	copy := receipt
	copy.ToolsConfiguration.ToolIDs = append([]string(nil), receipt.ToolsConfiguration.ToolIDs...)
	if receipt.ThinkingBudgetTokens != nil {
		budget := *receipt.ThinkingBudgetTokens
		copy.ThinkingBudgetTokens = &budget
	}
	p.admittedProvider = &copy
	return nil
}

// ProviderIdentity returns the exact admitted provider launch identity. It is
// unavailable for legacy, unadmitted, or internally inconsistent processes.
func (p *Process) ProviderIdentity() (ProviderProcessIdentity, error) {
	launchIdentity, ok := p.identity()
	if !ok || launchIdentity.Provider == nil || launchIdentity.AdmittedProvider == nil {
		return ProviderProcessIdentity{}, errors.New("OpenCode provider process is not admitted")
	}
	launch, admitted := launchIdentity.Provider, launchIdentity.AdmittedProvider
	if safepath.RequireDigest(launch.CapabilitySHA256) != nil || safepath.RequireDigest(launch.ConfigurationSHA256) != nil ||
		safepath.RequireDigest(admitted.SHA256) != nil ||
		admitted.ProviderID != launch.ProviderID || admitted.ModelID != launch.ModelID ||
		admitted.Protocol != launch.Protocol || admitted.SDKPackage != launch.SDKPackage || admitted.SDKVersion != launch.SDKVersion ||
		admitted.GatewayBaseURL != launch.GatewayBaseURL ||
		admitted.ContextWindowTokens != launch.ContextWindowTokens || admitted.MaxOutputTokens != launch.MaxOutputTokens || admitted.RuntimeOutputTokens != launch.RuntimeOutputTokens ||
		admitted.Tools != launch.Tools || admitted.Reasoning != launch.Reasoning ||
		admitted.TimeoutMillis != launch.TimeoutMillis || admitted.Variant != launch.Variant ||
		admitted.ReasoningEffort != launch.ReasoningEffort || admitted.ReasoningSummary != launch.ReasoningSummary ||
		admitted.RuntimeAgent != launch.RuntimeAgent || admitted.TextVerbosity != launch.TextVerbosity || admitted.Temperature != launch.Temperature || admitted.TopP != launch.TopP ||
		admitted.ThinkingMode != launch.ThinkingMode || !equalOptionalInt64(admitted.ThinkingBudgetTokens, launch.ThinkingBudgetTokens) || admitted.AnthropicEffort != launch.AnthropicEffort {
		return ProviderProcessIdentity{}, errors.New("OpenCode provider process identity mismatch")
	}
	result := ProviderProcessIdentity{
		ProviderID: launch.ProviderID, ModelID: launch.ModelID, Protocol: launch.Protocol,
		SDKPackage: launch.SDKPackage, SDKVersion: launch.SDKVersion, GatewayBaseURL: launch.GatewayBaseURL,
		CapabilitySHA256: launch.CapabilitySHA256, ConfigurationSHA256: launch.ConfigurationSHA256,
		ContextWindowTokens: launch.ContextWindowTokens, MaxOutputTokens: launch.MaxOutputTokens, RuntimeOutputTokens: launch.RuntimeOutputTokens,
		Tools: launch.Tools, Reasoning: launch.Reasoning, TimeoutMillis: launch.TimeoutMillis,
		Variant: launch.Variant, ReasoningEffort: launch.ReasoningEffort, ReasoningSummary: launch.ReasoningSummary,
		RuntimeAgent: launch.RuntimeAgent, TextVerbosity: launch.TextVerbosity, Temperature: launch.Temperature, TopP: launch.TopP,
		ThinkingMode: launch.ThinkingMode, ThinkingBudgetTokens: launch.ThinkingBudgetTokens, AnthropicEffort: launch.AnthropicEffort,
		Configuration: *admitted,
	}
	result.Configuration.ToolsConfiguration.ToolIDs = append([]string(nil), admitted.ToolsConfiguration.ToolIDs...)
	if launch.ThinkingBudgetTokens != nil {
		budget := *launch.ThinkingBudgetTokens
		result.ThinkingBudgetTokens = &budget
	}
	if admitted.ThinkingBudgetTokens != nil {
		budget := *admitted.ThinkingBudgetTokens
		result.Configuration.ThinkingBudgetTokens = &budget
	}
	digest, err := canonical.Hash("harness.opencode-provider-process-identity.v1", result)
	if err != nil {
		return ProviderProcessIdentity{}, err
	}
	result.SHA256 = digest
	if err := ValidateProviderProcessIdentity(result); err != nil {
		return ProviderProcessIdentity{}, err
	}
	return result, nil
}

// ValidateProviderProcessIdentity verifies a copied or durably replayed public
// identity without requiring the live Process or any scoped capability value.
func ValidateProviderProcessIdentity(identity ProviderProcessIdentity) error {
	configuration := identity.Configuration
	if safepath.RequireDigest(identity.SHA256) != nil || safepath.RequireDigest(identity.CapabilitySHA256) != nil ||
		safepath.RequireDigest(identity.ConfigurationSHA256) != nil || safepath.RequireDigest(configuration.SHA256) != nil ||
		configuration.ProviderID != identity.ProviderID || configuration.ModelID != identity.ModelID ||
		configuration.Protocol != identity.Protocol || configuration.SDKPackage != identity.SDKPackage || configuration.SDKVersion != identity.SDKVersion ||
		configuration.GatewayBaseURL != identity.GatewayBaseURL ||
		configuration.ContextWindowTokens != identity.ContextWindowTokens || configuration.MaxOutputTokens != identity.MaxOutputTokens || configuration.RuntimeOutputTokens != identity.RuntimeOutputTokens ||
		configuration.Tools != identity.Tools || configuration.Reasoning != identity.Reasoning ||
		configuration.TimeoutMillis != identity.TimeoutMillis || configuration.Variant != identity.Variant ||
		configuration.ReasoningEffort != identity.ReasoningEffort || configuration.ReasoningSummary != identity.ReasoningSummary ||
		configuration.RuntimeAgent != identity.RuntimeAgent || configuration.TextVerbosity != identity.TextVerbosity || configuration.Temperature != identity.Temperature || configuration.TopP != identity.TopP ||
		configuration.ThinkingMode != identity.ThinkingMode || !equalOptionalInt64(configuration.ThinkingBudgetTokens, identity.ThinkingBudgetTokens) || configuration.AnthropicEffort != identity.AnthropicEffort ||
		safepath.RequireDigest(configuration.ToolsConfiguration.SHA256) != nil ||
		configuration.ToolsConfiguration.SHA256 != configuration.SHA256 {
		return errors.New("invalid OpenCode provider process identity")
	}
	want := identity.SHA256
	identity.SHA256 = ""
	digest, err := canonical.Hash("harness.opencode-provider-process-identity.v1", identity)
	if err != nil || digest != want {
		return errors.New("OpenCode provider process identity digest mismatch")
	}
	return nil
}

func equalOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
