package control

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/candidatetools"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providerruntime"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/worktree"
)

type providerDispatchReceipt struct {
	Version            int            `json:"version"`
	Role               string         `json:"role"`
	Question           string         `json:"question,omitempty"`
	InvocationID       string         `json:"invocation_id"`
	AccessInvocationID string         `json:"access_invocation_id"`
	RuntimeJournalHead string         `json:"runtime_journal_head"`
	GatewayJournalHead string         `json:"gateway_journal_head"`
	ResultHash         string         `json:"result_hash"`
	ObservedModel      string         `json:"observed_model"`
	ObservedProvider   string         `json:"observed_provider"`
	Result             runtime.Result `json:"result"`
}

type providerTransportContextKey struct{}

const openCodeProviderRuntimeTimeout = 15 * time.Minute

// openCodeProviderRuntimeContext supplies the finite total deadline required by
// the runtime. The default is fifteen minutes; a validated explicit host
// setting may change it without extending an earlier caller deadline.
func openCodeProviderRuntimeContext(ctx context.Context, selected *config.OpenCodeHost) (context.Context, context.CancelFunc) {
	limit := openCodeProviderRuntimeTimeout
	if selected != nil && selected.InvocationTimeoutSeconds > 0 && selected.InvocationTimeoutSeconds <= 7200 {
		limit = time.Duration(selected.InvocationTimeoutSeconds) * time.Second
	}
	return context.WithTimeout(ctx, limit)
}

func openCodeReadbackTimeout(host *config.OpenCodeHost) time.Duration {
	if host != nil && host.ReadbackTimeoutSeconds > 0 && host.ReadbackTimeoutSeconds <= 300 {
		return time.Duration(host.ReadbackTimeoutSeconds) * time.Second
	}
	return 30 * time.Second
}

func withProviderTransportClient(ctx context.Context, client *providertransport.Client) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, providerTransportContextKey{}, client)
}

func providerTransportClient(ctx context.Context) (*providertransport.Client, error) {
	if ctx != nil {
		if client, ok := ctx.Value(providerTransportContextKey{}).(*providertransport.Client); ok && client != nil {
			return client, nil
		}
	}
	return providertransport.NewClient(providertransport.ClientOptions{})
}

func replayRoleProvider(s *Snapshot, event journal.Event) error {
	var receipt providerDispatchReceipt
	if err := canonical.Decode(event.Payload, &receipt); err != nil {
		return err
	}
	var base runtime.Invocation
	var err error
	switch receipt.Role {
	case "explorer":
		base, err = explorerInvocation(*s, receipt.Question)
	case "writer", "fixer":
		base, err = writerInvocation(*s)
	case "reviewer":
		base, err = reviewInvocation(*s)
	default:
		return errors.New("unknown provider role receipt")
	}
	if err != nil {
		return err
	}
	invocation, err := resolveScheduledInvocationID(*s, base, receipt.InvocationID)
	if err != nil || invocation.Profile.Role != receipt.Role || invocation.Profile.Runtime != "opencode-http" || receipt.InvocationID != invocation.ID || receipt.Version != 1 || safepath.RequireDigest(receipt.AccessInvocationID) != nil || safepath.RequireDigest(receipt.RuntimeJournalHead) != nil || safepath.RequireDigest(receipt.GatewayJournalHead) != nil {
		return errors.New("provider role receipt identity mismatch")
	}
	if err := runtime.ValidateResult(invocation, receipt.Result, true); err != nil {
		return err
	}
	domain, err := runtimeResultDomain(receipt.Role)
	if err != nil {
		return err
	}
	hash, err := canonical.Hash(domain, receipt.Result)
	if err != nil || hash != receipt.ResultHash {
		return errors.New("provider role result hash mismatch")
	}
	resolved, _, err := ConfiguredProviderExpectation(s.Creation.Config, receipt.Role)
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return errors.New("provider role observation substituted")
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		return err
	}
	selected, err := ResolveProviderRouting(s.Creation.Config, s.RunID, receipt.Role, inputHash, 1)
	if err != nil || selected.Intent.Reservation.InvocationID != receipt.AccessInvocationID {
		return errors.New("provider role access identity mismatch")
	}
	if s.ProviderRuntime == nil {
		s.ProviderRuntime = map[string]providerDispatchReceipt{}
	}
	if _, duplicate := s.ProviderRuntime[invocation.ID]; duplicate {
		return errors.New("duplicate provider role receipt")
	}
	s.ProviderRuntime[invocation.ID] = receipt
	return nil
}

func executeOpenCodeProviderRuntime(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation, candidateLease opencoderuntime.CandidateLease) (runtime.Result, providerDispatchReceipt, error) {
	if invocation.Profile.Runtime != "opencode-http" || s.Creation.Config.OpenCode == nil {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("OpenCode provider runtime required")
	}
	runtimeCtx, cancelRuntime := openCodeProviderRuntimeContext(ctx, s.Creation.Config.OpenCode)
	defer cancelRuntime()
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	selected, err := ResolveProviderRouting(s.Creation.Config, s.RunID, invocation.Profile.Role, inputHash, 1)
	if err != nil || selected.Profile != invocation.Profile {
		return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("OpenCode provider selection changed"), err)
	}
	if _, err := ensureTaskPool(s.Creation.Config, s.RunID, selected.Intent); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	base := s.Creation.Config.OpenCode.StateRoot
	if err := safepath.Directory(base); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	accessPath := controllerPath + ".model-access.jsonl"
	journalStem := providerRoleJournalStem(invocation.Profile.Role, scheduledAgentTurn(ctx))
	gatewayPath := controllerPath + "." + journalStem + ".provider-gateway.jsonl"
	runtimePath := controllerPath + "." + journalStem + ".opencode-runtime.jsonl"
	paths := opencoderuntime.Paths{Version: 1, Session: runtimePath + ".session", Dispatch: runtimePath + ".dispatch", Broker: runtimePath + ".broker", Seal: runtimePath + ".seal", Gateway: gatewayPath}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	recovering := len(runtimeEvents) != 0
	relativeRoot, err := bindProviderOpenCodeStateRoot(runtimePath, recovering, s.RunID, invocation.Profile.Role, invocation.ID)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	hostRoot := filepath.Join(base, relativeRoot)
	if recovering {
		if err := safepath.Directory(hostRoot); err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(opencoderuntime.ErrRecoveryRequired, errors.New("OpenCode recovery state root is unavailable"), err)
		}
	} else {
		if err := safepath.EnsureDirectory(base, relativeRoot); err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		for _, directory := range []string{"home", "config", "data", "cache", "state", "tmp"} {
			if err := safepath.EnsureDirectory(hostRoot, directory); err != nil {
				return runtime.Result{}, providerDispatchReceipt{}, err
			}
		}
	}
	var candidate *candidatetools.Binding
	if s.Workspace != nil && s.Candidate != nil {
		candidate = &candidatetools.Binding{Workspace: *s.Workspace, Candidate: *s.Candidate}
		if candidateLease == nil {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("OpenCode candidate lease required")
		}
	} else if candidateLease != nil {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("unexpected OpenCode candidate lease")
	}
	contextBinding, err := contextbroker.NewBinding(invocation.ID, s.Creation.Repository, candidate, contextbroker.Limits{MaxCalls: 64, MaxRequestBytes: 64 << 10, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 4 << 20})
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if recovering {
		brokerEvents, readErr := journal.Read(paths.Broker)
		if readErr != nil || len(brokerEvents) == 0 {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(opencoderuntime.ErrRecoveryRequired, errors.New("OpenCode runtime lacks its bound context journal"), readErr)
		}
	} else {
		if err := access.ReserveDurable(accessPath, selected.Policy, selected.Intent); err != nil {
			if activeErr := access.RequireActive(accessPath, selected.Policy, selected.Intent); activeErr != nil {
				return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("OpenCode invocation is already admitted or unresolved"), err, activeErr)
			}
		}
		bound, bindErr := providergateway.Bind(gatewayPath, accessPath, selected.Policy, selected.Intent, selected.ProviderRole.Endpoint, selected.ProviderRole.Model)
		if bindErr != nil || !reflect.DeepEqual(bound, selected.Gateway) {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("OpenCode gateway binding changed"), bindErr)
		}
	}
	broker, err := contextbroker.Open(paths.Broker, contextBinding)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	catalogHash, err := opencoderuntime.ContextCatalogSHA256(broker)
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	toolNames := make([]string, 0, len(broker.Catalog()))
	for _, definition := range broker.Catalog() {
		toolNames = append(toolNames, definition.Name)
	}
	sort.Strings(toolNames)
	tools, err := opencoderuntime.ProviderRequestToolsForSession(broker.Catalog(), toolNames)
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	compositeConfig, compositeBinding, compositeVerify, err := prepareProviderComposite(runtimeCtx, controllerPath, runtimePath, invocation, broker, contextBinding, runtimeEvents)
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if compositeBinding != nil {
		catalogHash = compositeBinding.CatalogSHA256
		toolNames = make([]string, len(compositeBinding.Tools))
		for index, tool := range compositeBinding.Tools {
			toolNames[index] = tool.Tool
		}
		sort.Strings(toolNames)
		contextProjection, projectionErr := contextmcp.Projection(broker)
		if projectionErr != nil {
			return runtime.Result{}, providerDispatchReceipt{}, projectionErr
		}
		combined, projectionErr := toolbridge.Compose(contextProjection, compositeConfig.AgentProjection)
		if projectionErr != nil {
			return runtime.Result{}, providerDispatchReceipt{}, projectionErr
		}
		catalog, catalogErr := combined.Catalog()
		if catalogErr != nil {
			return runtime.Result{}, providerDispatchReceipt{}, catalogErr
		}
		tools, err = opencoderuntime.ProviderRequestToolsForCatalog(catalog, toolNames)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		paths.Version = 2
		paths.ToolReceipts = compositeConfig.Path
	}
	expectation := selected.RequestExpectation
	expectation.Tools = tools
	structuredOutput, err := s.Creation.Config.OpenCodeWriterOutputExpectation(invocation)
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	if compositeBinding != nil && structuredOutput != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("native structured output is unavailable for composite OpenCode turns")
	}
	expectation.TerminalStructuredOutput, err = opencoderuntime.ProviderTerminalStructuredOutputExpectation(structuredOutput)
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	gatewayID, _ := selected.Gateway.ID()
	agent := "build"
	if invocation.Profile.Role == "planner" {
		agent = "plan"
	}
	workingDirectory := s.Creation.Repository.Root
	if s.Workspace != nil {
		workingDirectory = s.Workspace.Request.Path
	}
	sessionPlan := &opencoderuntime.SessionPlan{Agent: agent, Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort, ToolNames: toolNames, CatalogSHA256: catalogHash}
	runtimeIntent := opencoderuntime.Intent{Version: 2, Invocation: invocation, Directory: workingDirectory, Project: opencode.ProjectExpectation{Directory: workingDirectory, Mode: opencode.ProjectModeGit, Worktree: workingDirectory}, Context: contextBinding, Session: opencode.ToolSessionBinding{ToolNames: []string{}}, SessionPlan: sessionPlan, ProviderGatewayBindingID: gatewayID, StructuredOutput: structuredOutput}
	if compositeBinding != nil {
		runtimeIntent.Version = 3
		runtimeIntent.ToolReceipts = compositeBinding
	}
	runtimeIntent.IntentID, err = runtimeIntent.ID()
	if err != nil {
		_ = broker.Close()
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	var record opencoderuntime.ResultRecord
	if recovering {
		record, err = opencoderuntime.Execute(runtimeCtx, opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, Intent: runtimeIntent, VerifyComposite: compositeVerify})
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
	} else {
		source, err := providercredential.NewEnvironmentSource(map[string]string{selected.ProviderRole.Endpoint.Auth.CredentialRef: selected.CredentialEnvironment}, os.LookupEnv)
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		credential, err := providercredential.Resolve(runtimeCtx, accessPath, selected.Policy, selected.Intent, selected.ProviderRole.Endpoint, source)
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		defer credential.Close()
		transport, err := providerTransportClient(runtimeCtx)
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		port, err := availableLoopbackPort()
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		record, err = opencoderuntime.Execute(runtimeCtx, opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, StateRoot: hostRoot, Paths: paths, Intent: runtimeIntent, AccessJournalPath: accessPath, Policy: selected.Policy, AccessIntent: selected.Intent, Gateway: selected.Gateway, Credential: credential, Transport: transport, Broker: broker, CandidateLease: candidateLease, ProviderRole: selected.ProviderRole, RequestExpectation: expectation, Executable: s.Creation.Config.OpenCode.Executable, ExecutableSHA256: s.Creation.Config.OpenCode.ExecutableHash, Port: port, ParentMessageID: "msg_" + invocation.ID, Output: io.Discard, ReadbackTimeout: openCodeReadbackTimeout(s.Creation.Config.OpenCode), ProviderTimeout: 5 * time.Minute, ToolTimeout: 30 * time.Second, ReadinessTimeout: 5 * time.Minute, SealTimeout: 5 * time.Minute, InterruptShutdown: scheduledInterruptShutdownLookup(runtimeCtx), Composite: compositeConfig, VerifyComposite: compositeVerify})
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
	}
	domain, err := runtimeResultDomain(invocation.Profile.Role)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	resultHash, err := canonical.Hash(domain, record.Result)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	model, provider := invocation.Profile.Model, invocation.Profile.Provider
	observedModel := model
	if gateway, inspectErr := providergateway.Inspect(gatewayPath); inspectErr == nil && len(gateway.Calls) > 0 && gateway.Calls[len(gateway.Calls)-1].Receipt != nil {
		observedModel = gateway.Calls[len(gateway.Calls)-1].Receipt.ObservedModel
	}
	routeID, _ := selected.Intent.Route.ID()
	terminal := access.Receipt{InvocationID: selected.Intent.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: resultHash, ObservedModel: &model, ObservedProvider: &provider, InputTokens: record.Result.Usage.InputTokens, OutputTokens: record.Result.Usage.OutputTokens}
	if err := access.RecordTerminal(accessPath, selected.Policy, terminal); err != nil {
		if exactErr := access.RequireTerminal(accessPath, selected.Policy, selected.Intent, terminal); exactErr != nil {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(err, exactErr)
		}
	}
	runtimeHead, err := journalHead(runtimePath)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	receipt := providerDispatchReceipt{Version: 1, Role: invocation.Profile.Role, InvocationID: invocation.ID, AccessInvocationID: selected.Intent.Reservation.InvocationID, RuntimeJournalHead: runtimeHead, GatewayJournalHead: record.GatewayHead, ResultHash: resultHash, ObservedModel: observedModel, ObservedProvider: selected.ProviderRole.Model.Provider, Result: record.Result}
	return record.Result, receipt, nil
}

func availableLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	return port, listener.Close()
}

func replayPlannerProvider(s *Snapshot, event journal.Event) error {
	runtimeID := s.Creation.Config.Planner.Runtime
	if s.State != "PLANNING" || s.PlannerProvider != nil || runtimeID != "provider-api" && runtimeID != "opencode-http" {
		return errors.New("direct planner receipt transition rejected")
	}
	var receipt providerDispatchReceipt
	if err := canonical.Decode(event.Payload, &receipt); err != nil {
		return err
	}
	invocation, err := plannerInvocation(s.Creation.Config, s.Creation.Objective)
	if err != nil || receipt.Version != 1 || receipt.Role != "planner" || receipt.InvocationID != invocation.ID || safepath.RequireDigest(receipt.AccessInvocationID) != nil || safepath.RequireDigest(receipt.RuntimeJournalHead) != nil || safepath.RequireDigest(receipt.GatewayJournalHead) != nil || safepath.RequireDigest(receipt.ResultHash) != nil {
		return errors.New("invalid direct planner receipt identity")
	}
	if err := runtime.ValidateResult(invocation, receipt.Result, true); err != nil {
		return err
	}
	hash, err := canonical.Hash("harness.planner-result.v1", receipt.Result)
	if err != nil || hash != receipt.ResultHash {
		return errors.New("direct planner result hash mismatch")
	}
	resolved, expectation, err := ConfiguredProviderExpectation(s.Creation.Config, "planner")
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return errors.New("direct planner provider observation substituted")
	}
	var inputHash string
	if runtimeID == "provider-api" {
		direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
		inputHash, err = direct.InputHash(expectation)
	} else {
		inputHash, err = access.InputID(invocation.Input)
	}
	if err != nil {
		return err
	}
	selected, err := ResolveProviderRouting(s.Creation.Config, s.RunID, "planner", inputHash, 1)
	if err != nil || selected.Intent.Reservation.InvocationID != receipt.AccessInvocationID {
		return errors.New("direct planner access identity mismatch")
	}
	copy := receipt
	s.PlannerProvider = &copy
	return nil
}

func requireExecutableRoleRuntime(profile runtime.Profile) error {
	if profile.Runtime == "provider-api" && profile.Role != "planner" {
		return providergateway.ErrCapabilityUnavailable
	}
	return nil
}

type controllerCandidateLease interface {
	opencoderuntime.CandidateLease
	Close() error
}

func executeOpenCodeRole(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation, question string) (result runtime.Result, err error) {
	if s.Workspace == nil || s.Candidate == nil {
		return result, errors.New("OpenCode tool role requires an admitted candidate")
	}
	var lease controllerCandidateLease
	if invocation.Profile.Role == "explorer" || invocation.Profile.Role == "reviewer" {
		lease, err = worktree.AcquireRead(s.Workspace.Request)
	} else {
		lease, err = worktree.Acquire(s.Workspace.Request)
	}
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	latest, err := Inspect(controllerPath)
	if err != nil || latest.Candidate == nil || latest.Workspace == nil || *latest.Candidate != *s.Candidate || *latest.Workspace != *s.Workspace {
		return result, errors.Join(errors.New("OpenCode role state changed before dispatch"), err)
	}
	observed, err := worktree.Fingerprint(ctx, *latest.Workspace)
	if err != nil || observed != *latest.Candidate {
		return result, errors.Join(errors.New("OpenCode candidate changed before dispatch"), err)
	}
	if receipt, ok := latest.ProviderRuntime[invocation.ID]; ok {
		if receipt.Question != question || receipt.Role != invocation.Profile.Role {
			return result, errors.New("recorded OpenCode role receipt changed")
		}
		if err := settleProviderDispatchTaskPool(controllerPath, latest.Creation.Config, latest.RunID, invocation, receipt); err != nil {
			return result, err
		}
		return receipt.Result, nil
	}
	result, receipt, err := executeOpenCodeProvider(ctx, controllerPath, latest, invocation, lease)
	if err != nil {
		return result, err
	}
	receipt.Question = question
	if err := Append(controllerPath, "role.provider-observed", receipt); err != nil {
		return result, err
	}
	if err := settleProviderDispatchTaskPool(controllerPath, latest.Creation.Config, latest.RunID, invocation, receipt); err != nil {
		return result, err
	}
	return result, nil
}

func requireOpenCodeRoleReceipt(s Snapshot, invocation runtime.Invocation, result runtime.Result) error {
	if invocation.Profile.Runtime != "opencode-http" {
		return nil
	}
	receipt, ok := s.ProviderRuntime[invocation.ID]
	if !ok || receipt.InvocationID != invocation.ID || !sameCanonical(receipt.Result, result) {
		return errors.New("role result lacks exact OpenCode provider receipt")
	}
	domain, err := runtimeResultDomain(invocation.Profile.Role)
	if err != nil {
		return err
	}
	hash, err := canonical.Hash(domain, result)
	if err != nil || hash != receipt.ResultHash {
		return errors.New("role result differs from OpenCode provider receipt")
	}
	return nil
}

func executeDirectProviderRuntime(ctx context.Context, controllerPath string, c config.Config, runID string, invocation runtime.Invocation) (runtime.Result, providerDispatchReceipt, error) {
	if invocation.Profile.Runtime != "provider-api" {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("direct provider runtime required")
	}
	configured, expectation, err := ConfiguredProviderExpectation(c, invocation.Profile.Role)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
	inputHash, err := direct.InputHash(expectation)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	selected, err := ResolveProviderRouting(c, runID, invocation.Profile.Role, inputHash, 1)
	if err != nil || selected.Profile != invocation.Profile || !reflect.DeepEqual(selected.ProviderRole, configured) || !reflect.DeepEqual(selected.RequestExpectation, expectation) {
		return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("direct provider selection changed"), err)
	}
	if _, err := ensureTaskPool(c, runID, selected.Intent); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	accessPath := controllerPath + ".model-access.jsonl"
	journalStem := providerRoleJournalStem(invocation.Profile.Role, scheduledAgentTurn(ctx))
	gatewayPath := controllerPath + "." + journalStem + ".provider-gateway.jsonl"
	runtimePath := controllerPath + "." + journalStem + ".provider-runtime.jsonl"
	state, err := providerruntime.Inspect(runtimePath)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	var directResult providerruntime.Result
	if state.Binding != nil {
		if state.Result == nil || !reflect.DeepEqual(state.Binding.Invocation, direct) || !reflect.DeepEqual(state.Binding.AccessIntent, selected.Intent) || !reflect.DeepEqual(state.Binding.GatewayBinding, selected.Gateway) {
			return runtime.Result{}, providerDispatchReceipt{}, errors.New("direct provider invocation requires offline recovery")
		}
		directResult = *state.Result
	} else {
		if err := access.ReserveDurable(accessPath, selected.Policy, selected.Intent); err != nil {
			if activeErr := access.RequireActive(accessPath, selected.Policy, selected.Intent); activeErr != nil {
				return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("direct provider invocation is already admitted or unresolved"), err, activeErr)
			}
		}
		bound, err := providergateway.Bind(gatewayPath, accessPath, selected.Policy, selected.Intent, configured.Endpoint, configured.Model)
		if err != nil || !reflect.DeepEqual(bound, selected.Gateway) {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(errors.New("direct provider gateway binding changed"), err)
		}
		source, err := providercredential.NewEnvironmentSource(map[string]string{configured.Endpoint.Auth.CredentialRef: configured.CredentialEnvironment}, os.LookupEnv)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		credential, err := providercredential.Resolve(ctx, accessPath, selected.Policy, selected.Intent, configured.Endpoint, source)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		defer credential.Close()
		transport, err := providerTransportClient(ctx)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
		dispatchCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		directResult, err = providerruntime.Run(dispatchCtx, providerruntime.Config{JournalPath: runtimePath, AccessJournalPath: accessPath, GatewayJournalPath: gatewayPath, Policy: selected.Policy, Intent: selected.Intent, Binding: selected.Gateway, Expectation: selected.RequestExpectation, Credential: credential, Transport: transport}, direct)
		if err != nil {
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
	}
	inputTokens, outputTokens := directResult.Receipt.Usage.InputTokens, directResult.Receipt.Usage.OutputTokens
	model, provider, effort := invocation.Profile.Model, invocation.Profile.Provider, invocation.Profile.Effort
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: string(directResult.JSON), Usage: runtime.Usage{InputTokens: &inputTokens, OutputTokens: &outputTokens}}
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	domain, err := runtimeResultDomain(invocation.Profile.Role)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	resultHash, err := canonical.Hash(domain, result)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	routeID, _ := selected.Intent.Route.ID()
	terminal := access.Receipt{InvocationID: selected.Intent.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: resultHash, ObservedModel: &model, ObservedProvider: &provider, InputTokens: &inputTokens, OutputTokens: &outputTokens}
	if err := access.RecordTerminal(accessPath, selected.Policy, terminal); err != nil {
		if exactErr := access.RequireTerminal(accessPath, selected.Policy, selected.Intent, terminal); exactErr != nil {
			return runtime.Result{}, providerDispatchReceipt{}, errors.Join(err, exactErr)
		}
	}
	runtimeHead, err := journalHead(runtimePath)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	gatewayHead, err := journalHead(gatewayPath)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	receipt := providerDispatchReceipt{Version: 1, Role: invocation.Profile.Role, InvocationID: invocation.ID, AccessInvocationID: selected.Intent.Reservation.InvocationID, RuntimeJournalHead: runtimeHead, GatewayJournalHead: gatewayHead, ResultHash: resultHash, ObservedModel: directResult.Receipt.ObservedModel, ObservedProvider: configured.Model.Provider, Result: result}
	return result, receipt, nil
}

func journalHead(path string) (string, error) {
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		return "", errors.Join(errors.New("runtime evidence journal is empty"), err)
	}
	return events[len(events)-1].Hash, nil
}
