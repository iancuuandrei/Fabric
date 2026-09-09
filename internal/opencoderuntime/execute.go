package opencoderuntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providerproxy"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/worktree"
)

// ErrRecoveryRequired means durable execution history exists and no fresh
// process, session, dispatch, or provider call may be started for this intent.
var ErrRecoveryRequired = errors.New("OpenCode runtime requires offline recovery")

// CandidateLease proves that the caller retains either exclusive writer or
// shared reader ownership of the exact candidate request during execution.
type CandidateLease interface {
	WithOwnership(worktree.Request, func(worktree.LeaseIdentity) error) error
}

// InterruptShutdownLookup returns the exact durable interrupt request, when
// one exists, for the supplied runtime intent. Implementations must be
// observation-only; the runtime owns shutdown and receipt production.
type InterruptShutdownLookup func(Intent) (InterruptShutdownExpected, bool, error)

// ExecuteConfig fixes one controller-owned OpenCode process, its local context
// and provider capabilities, and every durable journal used by the turn.
// Credential and candidate leases remain caller-owned for the whole call.
type ExecuteConfig struct {
	RuntimePath        string
	StateRoot          string
	Paths              Paths
	Intent             Intent
	AccessJournalPath  string
	Policy             access.Policy
	AccessIntent       access.Intent
	Gateway            providergateway.Binding
	Credential         *providercredential.Lease
	Transport          *providertransport.Client
	Broker             *contextbroker.Broker
	CandidateLease     CandidateLease
	ProviderRole       config.ResolvedProviderRole
	RequestExpectation providergateway.AdapterRequestExpectation
	Executable         string
	ExecutableSHA256   string
	Port               int
	ParentMessageID    string
	Output             io.Writer
	ProviderTimeout    time.Duration
	ToolTimeout        time.Duration
	ReadinessTimeout   time.Duration
	SealTimeout        time.Duration
	ReadbackTimeout    time.Duration
	Diagnostic         bool
	InterruptShutdown  InterruptShutdownLookup
	Composite          *contextmcp.RecorderOwnedConfig
	VerifyComposite    opencode.CompositeBackendVerifier
}

// Execute starts one fresh provider-backed OpenCode tool turn. Durable partial
// history is never resent or restarted: already sealed history is completed by
// journal-only recovery, while every other partial state is returned UNKNOWN.
func Execute(ctx context.Context, cfg ExecuteConfig) (ResultRecord, error) {
	cfg.Intent = normalizedIntent(cfg.Intent)
	if cfg.Composite != nil {
		composite := *cfg.Composite
		cfg.Composite = &composite
	}
	if ctx == nil {
		return ResultRecord{}, errors.New("OpenCode runtime context required")
	}
	state, err := inspectRuntimeJournal(cfg.RuntimePath)
	if err != nil {
		return ResultRecord{}, err
	}
	if state.Intent != nil {
		if !equalCanonical(*state.Intent, normalizedIntent(cfg.Intent)) {
			return ResultRecord{}, errors.New("OpenCode runtime intent already differs")
		}
		if state.Bound != nil {
			record, completeErr := completeExecution(cfg)
			if completeErr == nil {
				return record, nil
			}
		}
		return ResultRecord{}, ErrRecoveryRequired
	}
	if err := ctx.Err(); err != nil {
		return ResultRecord{}, err
	}
	prepared, err := prepareExecution(cfg)
	if err != nil {
		return ResultRecord{}, err
	}
	attempted := false
	run := func(worktree.LeaseIdentity) error {
		// Candidate ownership may have been queued behind another caller which
		// finished or partially attempted this exact runtime after the initial
		// inspection. Recheck under the lease mutex before granting a fresh turn.
		current, inspectErr := inspectRuntimeJournal(cfg.RuntimePath)
		if inspectErr != nil {
			return inspectErr
		}
		if current.Intent != nil {
			if !equalCanonical(*current.Intent, normalizedIntent(cfg.Intent)) {
				return errors.New("OpenCode runtime intent already differs")
			}
			if current.Bound != nil {
				record, completeErr := completeExecution(cfg)
				if completeErr == nil {
					prepared.result = record
					return nil
				}
			}
			return ErrRecoveryRequired
		}
		attempted = true
		var executeErr error
		prepared.result, executeErr = executeFresh(ctx, prepared)
		return executeErr
	}
	if cfg.Intent.Context.Candidate != nil {
		err = cfg.CandidateLease.WithOwnership(cfg.Intent.Context.Candidate.Workspace.Request, run)
	} else {
		err = run(worktree.LeaseIdentity{})
	}
	if err != nil && attempted {
		// executeFresh durably records the runtime intent before it starts any
		// listener, process, session, or dispatch work. From that point onward a
		// failure is recovery-only even when the immediate failing operation
		// appears to precede the provider POST: its durable effect may be partial
		// and a second fresh attempt would violate the one-dispatch contract.
		return ResultRecord{}, errors.Join(ErrRecoveryRequired, err)
	}
	return prepared.result, err
}

type preparedExecution struct {
	config      ExecuteConfig
	sessionPlan SessionPlan
	proxy       *providerproxy.Server
	mcp         *contextmcp.OwnedServer
	result      ResultRecord
	proxyBearer string
	mcpBearer   string
	serverUser  string
	serverPass  string
}

func prepareExecution(cfg ExecuteConfig) (*preparedExecution, error) {
	if cfg.ReadbackTimeout == 0 {
		cfg.ReadbackTimeout = 30 * time.Second
	}
	framing, framingErr := providergateway.EffectiveResponseFraming(cfg.RequestExpectation.ResponseFraming)
	if framingErr != nil || framing != providergateway.ResponseFramingSSE || cfg.ProviderRole.ResponseFraming != cfg.RequestExpectation.ResponseFraming {
		return nil, errors.New("OpenCode runtime requires SSE provider framing")
	}
	if cfg.Broker == nil || cfg.Credential == nil || cfg.Transport == nil || !filepath.IsAbs(cfg.StateRoot) || filepath.Clean(cfg.StateRoot) != cfg.StateRoot || cfg.Port < 1 || cfg.Port > 65535 || cfg.ProviderTimeout <= 0 || cfg.ProviderTimeout > 5*time.Minute || cfg.ToolTimeout <= 0 || cfg.ToolTimeout > 30*time.Second || cfg.ReadinessTimeout <= 0 || cfg.ReadinessTimeout > 5*time.Minute || cfg.SealTimeout <= 0 || cfg.SealTimeout > 5*time.Minute || cfg.ReadbackTimeout <= 0 || cfg.ReadbackTimeout > 5*time.Minute || cfg.ParentMessageID == "" {
		return nil, errors.New("invalid OpenCode runtime execution configuration")
	}
	if cfg.Intent.Context.Candidate == nil {
		if cfg.CandidateLease != nil {
			return nil, errors.New("unexpected candidate writer lease")
		}
	} else if cfg.CandidateLease == nil {
		return nil, errors.New("candidate writer lease required")
	}
	if cfg.Paths.Gateway == "" || cfg.Paths.Gateway != cfg.GatewayJournalPath() || cfg.Broker.JournalPath() != cfg.Paths.Broker {
		return nil, errors.New("runtime subordinate journal mismatch")
	}
	sessionPlan, err := cfg.Intent.EffectiveSessionPlan()
	if err != nil {
		return nil, err
	}
	if err := validateStructuredOutputIntent(cfg.Intent); err != nil {
		return nil, err
	}
	terminalExpectation, err := terminalStructuredOutputExpectation(cfg.Intent.StructuredOutput)
	if err != nil || !equalCanonical(cfg.RequestExpectation.TerminalStructuredOutput, terminalExpectation) {
		return nil, errors.New("runtime terminal structured output differs from intent")
	}
	if cfg.Intent.StructuredOutput != nil {
		required := cfg.RequestExpectation.RequiredCapabilities
		if cfg.Gateway.Model.AdapterID != providergateway.OpenAIResponsesAdapter || required == nil || !required.Tools || required.Reasoning || required.StructuredOutput != providergateway.StructuredOutputUnsupported || required.SchemaName != "" || len(required.Schema) != 0 || !equalCanonical(required, cfg.ProviderRole.RequiredCapabilities) {
			return nil, errors.New("native structured output requires tool-only Responses capabilities")
		}
	}
	providerTools, err := executionProviderTools(cfg, sessionPlan)
	if err != nil || !providerToolsEqual(cfg.RequestExpectation.Tools, providerTools) {
		return nil, errors.New("runtime provider tools differ from context session")
	}
	if err := opencode.ValidateDurableInvocationAccess(cfg.AccessJournalPath, cfg.Policy, cfg.Intent.Invocation, cfg.AccessIntent); err != nil {
		return nil, err
	}
	gatewayID, err := cfg.Gateway.ID()
	if err != nil || gatewayID != cfg.Intent.ProviderGatewayBindingID || !equalCanonical(cfg.Gateway.Endpoint, cfg.ProviderRole.Endpoint) || !equalCanonical(cfg.Gateway.Model, cfg.ProviderRole.Model) || !bytes.Equal(cfg.RequestExpectation.Controls, cfg.ProviderRole.AdapterControls) || !equalCanonical(cfg.RequestExpectation.RequiredCapabilities, cfg.ProviderRole.RequiredCapabilities) || cfg.RequestExpectation.ResponseFraming != cfg.ProviderRole.ResponseFraming {
		return nil, errors.New("runtime provider role differs from gateway")
	}
	if _, err := providergateway.AdapterRequestExpectationID(cfg.Gateway, cfg.RequestExpectation); err != nil {
		return nil, err
	}
	state, err := providergateway.Inspect(cfg.Paths.Gateway)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, cfg.Gateway) || len(state.Calls) != 0 || state.Pending != nil || state.Finished || state.Exhausted {
		return nil, errors.New("unused durable provider gateway required")
	}
	brokerState, err := contextbroker.Inspect(cfg.Paths.Broker)
	if err != nil || brokerState.Binding == nil || !equalCanonical(*brokerState.Binding, cfg.Intent.Context) || brokerState.Closed || brokerState.Pending != nil || brokerState.Calls != 0 || len(brokerState.Requests) != 0 || len(brokerState.Responses) != 0 {
		return nil, errors.New("unused exact context broker required")
	}
	for _, path := range []string{cfg.Paths.Session, cfg.Paths.Dispatch, cfg.Paths.Seal} {
		events, err := journal.Read(path)
		if err != nil || len(events) != 0 {
			return nil, errors.New("fresh OpenCode subordinate journals required")
		}
	}
	secrets, err := randomCapabilities(4)
	if err != nil {
		return nil, err
	}
	mcp, err := newExecutionMCP(cfg, secrets[0])
	if err != nil {
		return nil, err
	}
	proxy, err := providerproxy.New(providerproxy.Config{AccessJournalPath: cfg.AccessJournalPath, GatewayJournalPath: cfg.Paths.Gateway, Policy: cfg.Policy, Intent: cfg.AccessIntent, Gateway: cfg.Gateway, Credential: cfg.Credential, Transport: cfg.Transport, Bearer: secrets[1], Expectation: cfg.RequestExpectation, HandlerTimeout: cfg.ProviderTimeout})
	if err != nil {
		return nil, err
	}
	return &preparedExecution{config: cfg, sessionPlan: sessionPlan, proxy: proxy, mcp: mcp, mcpBearer: secrets[0], proxyBearer: secrets[1], serverUser: secrets[2], serverPass: secrets[3]}, nil
}

// GatewayJournalPath keeps the exact configured path relation explicit at the
// call site without introducing a second path authority.
func (cfg ExecuteConfig) GatewayJournalPath() string { return cfg.Paths.Gateway }

func executeFresh(ctx context.Context, prepared *preparedExecution) (result ResultRecord, resultErr error) {
	cfg := prepared.config
	sessionPlan := prepared.sessionPlan
	if err := RecordIntent(cfg.RuntimePath, cfg.Intent); err != nil {
		return ResultRecord{}, err
	}
	contextRunning, err := prepared.mcp.Listen()
	if err != nil {
		return ResultRecord{}, err
	}
	var proxyRunning *providerproxy.Running
	var process *opencode.Process
	var client *opencode.Client
	cleanup := true
	defer func() {
		if cleanup {
			resultErr = errors.Join(resultErr, closeRuntimeResourcesAfterInterrupt(cfg, prepared, process, client, contextRunning, proxyRunning))
		}
	}()
	proxyRunning, err = prepared.proxy.Listen()
	if err != nil {
		return ResultRecord{}, err
	}
	initialProxyIdentity, err := proxyRunning.ProviderSealIdentity(prepared.proxyBearer)
	if err != nil {
		return ResultRecord{}, err
	}
	providerSpec, err := providerConfiguration(cfg, initialProxyIdentity.BaseURL, prepared.proxyBearer)
	if err != nil {
		return ResultRecord{}, err
	}
	toolNames := append([]string(nil), sessionPlan.ToolNames...)
	toolsSpec := opencode.ToolsConfigurationSpec{Endpoint: contextRunning.URL(), Bearer: prepared.mcpBearer, ToolNames: toolNames, TimeoutMillis: int(cfg.ToolTimeout.Milliseconds()), AllowStructuredOutput: cfg.Intent.StructuredOutput != nil}
	if contextRunning.CatalogHash() != sessionPlan.CatalogSHA256 || validateExecutionMCPOwner(cfg, contextRunning, prepared.mcpBearer) != nil || proxyRunning.ValidateOwner(cfg.Transport, cfg.Credential, prepared.proxyBearer) != nil {
		return ResultRecord{}, errors.New("local runtime capability ownership mismatch")
	}
	var startupAttempts []opencode.ProviderStartupAttempt
	process, _, startupAttempts, err = opencode.StartReadyLimitedProviderToolsProjectProcessInDirectory(ctx, cfg.Executable, cfg.ExecutableSHA256, cfg.StateRoot, cfg.Intent.Directory, cfg.Port, prepared.serverUser, prepared.serverPass, cfg.Output, 1, cfg.ReadinessTimeout, cfg.Diagnostic, providerSpec, toolsSpec, cfg.Intent.Project)
	if err != nil {
		for _, attempt := range startupAttempts {
			diagnostic := fmt.Errorf("OpenCode startup attempt %d (%s): elapsed_ms=%d polls=%d config_http_observed=%t first_config_http_ms=%d config_error_class=%s tcp_observed=%t first_tcp_ms=%d health_http_observed=%t first_health_http_ms=%d health_error_class=%s project_polls=%d project_elapsed_ms=%d process_exited=%t", attempt.Number, attempt.Outcome, attempt.ReadinessElapsedMillis, attempt.ReadinessPolls, attempt.ReadinessHTTPObserved, attempt.FirstHTTPResponseMillis, attempt.FinalReadinessErrorClass, attempt.TCPObserved, attempt.FirstTCPMillis, attempt.HealthHTTPObserved, attempt.FirstHealthHTTPMillis, attempt.FinalHealthErrorClass, attempt.ProjectReadinessPolls, attempt.ProjectReadinessMillis, attempt.ProcessExited)
			if attempt.Err != nil {
				diagnostic = fmt.Errorf("%w: %v", diagnostic, attempt.Err)
			}
			err = errors.Join(err, diagnostic)
		}
		return ResultRecord{}, err
	}
	client, err = opencode.NewClientWithPolicy(opencodeEndpoint(cfg.Port), prepared.serverUser, prepared.serverPass, opencode.TransportPolicy{ReadbackTimeout: cfg.ReadbackTimeout})
	if err != nil {
		return ResultRecord{}, err
	}
	project, err := client.ReadCurrentProject(ctx, cfg.Intent.Project)
	if err != nil {
		return ResultRecord{}, err
	}
	if err := waitForMCPConnection(ctx, client, process, cfg.Intent.Directory); err != nil {
		return ResultRecord{}, err
	}
	confirmedProject, err := client.ReadCurrentProject(ctx, cfg.Intent.Project)
	if err != nil {
		return ResultRecord{}, err
	}
	if project.ID != confirmedProject.ID || project.Directory != confirmedProject.Directory || project.Mode != confirmedProject.Mode || project.Worktree != confirmedProject.Worktree || project.VCS != confirmedProject.VCS {
		return ResultRecord{}, errors.New("OpenCode project identity changed during MCP readiness")
	}
	project = confirmedProject
	sessionBinding, err := cfg.Intent.ResolveToolSessionBinding(project.ID)
	if err != nil {
		return ResultRecord{}, err
	}
	sessionID, err := client.CreateToolSession(ctx, cfg.Paths.Session, sessionBinding)
	if err != nil {
		return ResultRecord{}, err
	}
	if cfg.Gateway.Model.AdapterID == providergateway.OpenAIResponsesAdapter {
		if err := proxyRunning.BindResponsesPromptCacheKey(sessionID); err != nil {
			return ResultRecord{}, err
		}
	}
	proxyIdentity, err := proxyRunning.ProviderSealIdentity(prepared.proxyBearer)
	if err != nil {
		return ResultRecord{}, err
	}
	dispatch, err := opencode.DispatchForInvocationWithStructuredOutput(cfg.Intent.Invocation, sessionID, cfg.ParentMessageID, sessionPlan.Agent, cfg.Intent.Directory, project.Worktree, cfg.Intent.StructuredOutput)
	if err != nil {
		return ResultRecord{}, err
	}
	contextID, _ := cfg.Intent.Context.ID()
	processIdentity, err := process.ProviderIdentity()
	if err != nil {
		return ResultRecord{}, err
	}
	metadataAuthority := opencode.MetadataAuthorityReadOnly
	if cfg.Intent.Context.Candidate != nil {
		metadataAuthority = opencode.MetadataAuthorityProposalOnly
	}
	metadataExpectation, err := opencode.NewRuntimeMetadataExpectation(metadataAuthority, cfg.Intent.Directory, runtimeMetadataControllerPaths(cfg))
	if err != nil {
		return ResultRecord{}, err
	}
	metadataExpected := &metadataExpectation
	syncIntent := opencode.SynchronousToolDispatchIntent{Invocation: cfg.Intent.Invocation, Dispatch: dispatch, BrokerBindingID: contextID, BrokerCatalogID: cfg.Intent.Context.CatalogID, RuntimeMetadata: metadataExpected}
	sealExpected := opencode.SynchronousToolTurnSealExpected{Dispatch: syncIntent, Session: sessionBinding, Tools: processIdentity.Configuration.ToolsConfiguration, ExecutableSHA256: cfg.ExecutableSHA256, HostRoot: cfg.StateRoot, WorkingDirectory: cfg.Intent.Directory, MaxOutputTokens: cfg.RequestExpectation.MaxOutputTokens, RuntimeOutputTokens: processIdentity.RuntimeOutputTokens, Diagnostic: cfg.Diagnostic, Provider: &opencode.ProviderToolTurnSealExpected{Process: processIdentity, Proxy: proxyIdentity}, RuntimeMetadata: metadataExpected}
	if _, err := RecordBound(cfg.RuntimePath, cfg.Intent, project, cfg.Paths, sealExpected); err != nil {
		return ResultRecord{}, err
	}
	if cfg.Intent.Version == 3 {
		composite := opencode.CompositeDispatchIntent{Version: 1, DispatchPath: cfg.Paths.Dispatch, Turn: syncIntent, BrokerPath: cfg.Paths.Broker, ReceiptPath: cfg.Paths.ToolReceipts, Receipts: *cfg.Intent.ToolReceipts}
		if _, err := client.SubmitCompositeToolTurn(ctx, cfg.Paths.Dispatch, composite, cfg.VerifyComposite); err != nil {
			return ResultRecord{}, recordCompositeTransportFailure(cfg, err)
		}
		sealCtx, cancel := context.WithTimeout(ctx, cfg.SealTimeout)
		defer cancel()
		if _, err := opencode.SealProviderCompositeToolTurn(sealCtx, cfg.Paths.Seal, composite, sealExpected, toolsSpec, client, process, contextRunning, cfg.Broker, proxyRunning, prepared.proxyBearer, cfg.VerifyComposite); err != nil {
			return ResultRecord{}, err
		}
	} else {
		if _, err := client.SubmitSynchronousToolTurn(ctx, cfg.Paths.Dispatch, cfg.Paths.Broker, syncIntent); err != nil {
			if recoveryErr := recoverSynchronousTransportFailure(ctx, cfg, client, syncIntent, err); recoveryErr != nil {
				return ResultRecord{}, recoveryErr
			}
		}
		sealCtx, cancel := context.WithTimeout(ctx, cfg.SealTimeout)
		defer cancel()
		if _, err := opencode.SealProviderSynchronousToolTurn(sealCtx, cfg.Paths.Seal, cfg.Paths.Dispatch, cfg.Paths.Broker, sealExpected, toolsSpec, client, process, contextRunning, cfg.Broker, proxyRunning, prepared.proxyBearer); err != nil {
			return ResultRecord{}, err
		}
	}
	cleanup = false
	return completeExecution(cfg)
}

func recoverSynchronousTransportFailure(ctx context.Context, cfg ExecuteConfig, client *opencode.Client, intent opencode.SynchronousToolDispatchIntent, submitErr error) error {
	failure, ok := opencode.TransportFailureFromError(submitErr)
	if !ok {
		return submitErr
	}
	return recoverSynchronousTransportFailureEvidence(ctx, cfg, client, intent, failure, submitErr)
}

func recoverSynchronousTransportFailureEvidence(ctx context.Context, cfg ExecuteConfig, client *opencode.Client, intent opencode.SynchronousToolDispatchIntent, failure opencode.TransportFailureEvidence, submitErr error) error {
	if _, err := RecordTransportFailure(cfg.RuntimePath, cfg.Intent, failure); err != nil {
		return errors.Join(submitErr, err)
	}
	if failure.Phase != opencode.TransportFailurePhaseMessagePostWait || failure.DispatchState != opencode.TransportDispatchStateUnknown || failure.Endpoint != opencodeEndpoint(cfg.Port) || failure.ContextError != "" || ctx.Err() != nil {
		return submitErr
	}
	readbackCtx, cancel := context.WithTimeout(ctx, cfg.ReadbackTimeout)
	defer cancel()
	if _, err := client.RecoverSynchronousToolTurn(readbackCtx, cfg.Paths.Dispatch, cfg.Paths.Broker, intent); err != nil {
		return errors.Join(submitErr, fmt.Errorf("OpenCode GET-only recovery failed: %w", err))
	}
	return nil
}

func recordCompositeTransportFailure(cfg ExecuteConfig, submitErr error) error {
	failure, ok := opencode.TransportFailureFromError(submitErr)
	if !ok {
		return submitErr
	}
	if _, err := RecordTransportFailure(cfg.RuntimePath, cfg.Intent, failure); err != nil {
		return errors.Join(submitErr, err)
	}
	// Composite evidence requires the synchronous response bytes. Without them,
	// the existing recovery API is deliberately journal-only, so no GET or POST
	// can safely settle this turn.
	return submitErr
}

func runtimeMetadataControllerPaths(cfg ExecuteConfig) []string {
	bases := []string{cfg.RuntimePath, cfg.AccessJournalPath, cfg.Paths.Session, cfg.Paths.Dispatch, cfg.Paths.Broker, cfg.Paths.Seal, cfg.Paths.Gateway, cfg.Paths.ToolReceipts, cfg.RuntimePath + ".state-root.jsonl"}
	const runtimeSuffix = ".planner.opencode-runtime.jsonl"
	if strings.HasSuffix(cfg.RuntimePath, runtimeSuffix) {
		bases = append(bases, strings.TrimSuffix(cfg.RuntimePath, runtimeSuffix)+".agent-tree")
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(bases)*3)
	for _, base := range bases {
		if base == "" {
			continue
		}
		for _, path := range []string{base, base + "-shm", base + "-wal"} {
			key := filepath.Clean(path)
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if !seen[key] {
				seen[key] = true
				result = append(result, path)
			}
		}
	}
	return result
}

func completeExecution(cfg ExecuteConfig) (ResultRecord, error) {
	if cfg.Intent.Version == 3 {
		return CompleteComposite(cfg.RuntimePath, cfg.Intent, cfg.VerifyComposite)
	}
	return Complete(cfg.RuntimePath, cfg.Intent)
}

func waitForMCPConnection(ctx context.Context, client *opencode.Client, process *opencode.Process, directory string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := client.ReadMCPStatusInDirectory(ctx, directory); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-process.Done():
			return errors.Join(errors.New("OpenCode exited before MCP connection"), process.Wait())
		case <-ticker.C:
		}
	}
}

func providerConfiguration(cfg ExecuteConfig, gatewayBaseURL, capability string) (opencode.ProviderConfigurationSpec, error) {
	sessionPlan, err := cfg.Intent.EffectiveSessionPlan()
	if err != nil {
		return opencode.ProviderConfigurationSpec{}, err
	}
	parsed, err := url.Parse(gatewayBaseURL)
	if err != nil || parsed.Path != "/v1" {
		return opencode.ProviderConfigurationSpec{}, errors.New("invalid local provider proxy base URL")
	}
	variant := opencode.ProviderVariantSpec{Name: cfg.ProviderRole.Variant.Effort}
	reasoning := false
	switch cfg.Gateway.Model.AdapterID {
	case providergateway.OpenAIChatCompletionsAdapter:
		variant.Name = "none"
	case providergateway.OpenAIResponsesAdapter:
		var controls providergateway.ResponsesRequestExpectation
		if err := canonical.Decode(cfg.RequestExpectation.Controls, &controls); err != nil {
			return opencode.ProviderConfigurationSpec{}, errors.Join(errors.New("invalid OpenAI Responses runtime controls"), err)
		}
		if controls.MaxOutputTokens != cfg.RequestExpectation.MaxOutputTokens {
			return opencode.ProviderConfigurationSpec{}, errors.New("OpenAI Responses runtime output cap differs")
		}
		options := &opencode.OpenAIResponsesVariantOptions{Effort: controls.ReasoningEffort, Summary: controls.ReasoningSummary, TextVerbosity: controls.TextVerbosity, Temperature: controls.Temperature, TopP: controls.TopP}
		if options.Effort != "" || options.Summary != "" || options.TextVerbosity != "" || options.Temperature != nil || options.TopP != nil {
			variant.Responses = options
		}
		reasoning = controls.ReasoningEffort != ""
	case providergateway.AnthropicMessagesAdapter:
		var budget *int64
		if cfg.ProviderRole.Variant.ThinkingBudgetTokens != nil {
			value := *cfg.ProviderRole.Variant.ThinkingBudgetTokens
			budget = &value
		}
		variant.Anthropic = &opencode.AnthropicVariantOptions{ThinkingMode: cfg.ProviderRole.Variant.ThinkingMode, ThinkingBudgetTokens: budget, Effort: cfg.ProviderRole.Variant.Effort}
		reasoning = cfg.ProviderRole.Variant.ThinkingMode == "enabled" || cfg.ProviderRole.Variant.ThinkingMode == "adaptive"
	default:
		return opencode.ProviderConfigurationSpec{}, errors.New("unsupported OpenCode provider protocol")
	}
	tools := len(sessionPlan.ToolNames) != 0
	spec := opencode.ProviderConfigurationSpec{ProviderID: cfg.Intent.Invocation.Profile.Provider, ModelID: cfg.Intent.Invocation.Profile.Model, Protocol: opencode.ProviderProtocol(cfg.Gateway.Model.AdapterID), GatewayBaseURL: gatewayBaseURL, GatewayCapability: capability, ContextWindowTokens: cfg.Gateway.Model.ContextWindowTokens, MaxOutputTokens: cfg.RequestExpectation.MaxOutputTokens, Tools: tools, Reasoning: reasoning, TimeoutMillis: int(cfg.ProviderTimeout.Milliseconds()), RuntimeAgent: sessionPlan.Agent, Variant: variant}
	if _, err := opencode.BuildProviderToolsConfiguration(spec, opencode.ToolsConfigurationSpec{Endpoint: "http://127.0.0.1:1/mcp", Bearer: capability, ToolNames: sessionPlan.ToolNames, TimeoutMillis: int(cfg.ToolTimeout.Milliseconds()), AllowStructuredOutput: cfg.Intent.StructuredOutput != nil}); err != nil {
		return opencode.ProviderConfigurationSpec{}, err
	}
	return spec, nil
}

func closeRuntimeResources(process *opencode.Process, client *opencode.Client, contextRunning *contextmcp.OwnedRunning, proxyRunning *providerproxy.Running, broker *contextbroker.Broker, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if contextRunning != nil {
		_ = contextRunning.Close(ctx)
		_ = contextRunning.Wait()
	}
	if proxyRunning != nil {
		_ = proxyRunning.Close(ctx)
		_ = proxyRunning.Wait()
	}
	if process != nil {
		_ = process.CloseContext(ctx)
	}
	if client != nil {
		client.Close()
	}
	if broker != nil {
		_ = broker.Close()
	}
}

func randomCapabilities(count int) ([]string, error) {
	result := make([]string, count)
	for index := range result {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		result[index] = hex.EncodeToString(raw)
	}
	return result, nil
}

func opencodeEndpoint(port int) string {
	return "http://127.0.0.1:" + itoa(port)
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := [20]byte{}
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = digits[value%10]
		value /= 10
	}
	return string(buffer[position:])
}

func providerToolsEqual(expected []providergateway.RequestTool, catalog []toolbridge.ToolDefinition) bool {
	if len(expected) != len(catalog) {
		return false
	}
	for index := range expected {
		if expected[index].Name != catalog[index].Name || expected[index].Description != catalog[index].Description || !bytes.Equal(expected[index].Parameters, catalog[index].InputSchema) {
			return false
		}
	}
	return slices.IsSortedFunc(expected, func(left, right providergateway.RequestTool) int {
		return bytes.Compare([]byte(left.Name), []byte(right.Name))
	})
}

func providerToolsForSession(catalog []sourcetools.Definition, names []string) ([]toolbridge.ToolDefinition, error) {
	byName := make(map[string]sourcetools.Definition, len(catalog))
	for _, definition := range catalog {
		if _, exists := byName[definition.Name]; exists {
			return nil, errors.New("duplicate context tool")
		}
		byName[definition.Name] = definition
	}
	original := make([]toolbridge.ToolDefinition, 0, len(names))
	for _, name := range names {
		definition, ok := byName[name]
		if !ok {
			return nil, errors.New("context session tool unavailable")
		}
		schema, err := canonical.Bytes(definition.InputSchema)
		if err != nil {
			return nil, err
		}
		original = append(original, toolbridge.ToolDefinition{Name: definition.Name, Description: definition.Description, InputSchema: schema})
	}
	projection, err := opencode.ProjectOpenCodeOpenAITools(original)
	if err != nil {
		return nil, err
	}
	result := make([]toolbridge.ToolDefinition, len(projection.Tools))
	for index, tool := range projection.Tools {
		// The pinned OpenCode MCP client namespaces tools by its exact server ID
		// before passing the already projected schema to the provider SDK.
		result[index] = toolbridge.ToolDefinition{Name: opencode.ToolsMCPServerName + "_" + tool.Name, Description: tool.Description, InputSchema: append([]byte(nil), tool.Parameters...)}
	}
	return result, nil
}

// ProviderRequestToolsForSession returns the exact namespaced tool projection
// that the pinned OpenCode session will send through the provider adapter.
func ProviderRequestToolsForSession(catalog []sourcetools.Definition, names []string) ([]providergateway.RequestTool, error) {
	projected, err := providerToolsForSession(catalog, names)
	if err != nil {
		return nil, err
	}
	result := make([]providergateway.RequestTool, len(projected))
	for index, tool := range projected {
		result[index] = providergateway.RequestTool{Name: tool.Name, Description: tool.Description, Parameters: append([]byte(nil), tool.InputSchema...)}
	}
	return result, nil
}

// ContextCatalogSHA256 returns the exact immutable MCP catalog identity that
// Execute will serve for broker. It creates no listener and records no effect.
func ContextCatalogSHA256(broker *contextbroker.Broker) (string, error) {
	server, err := contextmcp.New(broker, "00000000000000000000000000000000", nil)
	if err != nil {
		return "", err
	}
	return server.CatalogHash(), nil
}
