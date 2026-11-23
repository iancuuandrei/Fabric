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

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/candidatetools"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/contextmcp"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/providercredential"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/providerruntime"
	"github.com/iancuuandrei/fabric/internal/providertransport"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/toolbridge"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// staticExplorerQueueDepth grants the bounded serial FIFO transport queue
// to static scheduled explorer turns, which carry no dynamic AgentTurn and
// hence no composite receipts binding. Queueing is transport admission only:
// the served catalog stays context-only, so no agent-control authority is
// granted. Every other dispatch keeps immediate rejection. Composite turns
// ignore this knob; their queue comes from the receipts binding.
func staticExplorerQueueDepth(ctx context.Context, invocation runtime.Invocation) int {
	schedulerPath, boundSchedule := scheduledDispatchJournalPath(ctx)
	if !boundSchedule || schedulerPath == "" || invocation.Profile.Role != "explorer" || scheduledAgentTurn(ctx) != nil {
		return 0
	}
	return contextmcp.InvocationToolCallBudget - 1
}

// contextRoleQueueDepth serializes bounded bursts of context reads for every
// engineering role. The queue changes admission timing, never the served
// catalog, candidate ownership or effect authority. Composite explorer turns
// continue to use their independently bound receipts queue.
func contextRoleQueueDepth(invocation runtime.Invocation) int {
	switch invocation.Profile.Role {
	case "planner", "explorer", "writer", "fixer", "reviewer":
		return contextmcp.InvocationToolCallBudget - 1
	default:
		return 0
	}
}

type providerDispatchReceipt struct {
	Version            int                     `json:"version"`
	Role               string                  `json:"role"`
	Question           string                  `json:"question,omitempty"`
	InvocationID       string                  `json:"invocation_id"`
	AccessInvocationID string                  `json:"access_invocation_id"`
	RoutingDecision    *access.RoutingDecision `json:"routing_decision,omitempty"`
	RuntimeJournalHead string                  `json:"runtime_journal_head"`
	GatewayJournalHead string                  `json:"gateway_journal_head"`
	ResultHash         string                  `json:"result_hash"`
	ObservedModel      string                  `json:"observed_model"`
	ObservedProvider   string                  `json:"observed_provider"`
	Result             runtime.Result          `json:"result"`
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

// isolatedTaskBaseForReceipt resolves the exact frozen child invocation bound
// to a writer/fixer receipt ID through the controller-admitted cohort. It
// performs no effects and admits only task-bound invocations whose exact child
// binding is still current.
func isolatedTaskBaseForReceipt(s Snapshot, receipt providerDispatchReceipt) (runtime.Invocation, string, bool) {
	if receipt.Role != "writer" && receipt.Role != "fixer" {
		return runtime.Invocation{}, "", false
	}
	if !isolatedImplementationEnabled(s) {
		return runtime.Invocation{}, "", false
	}
	base, taskID, err := isolatedWriterInvocationForReceiptID(s, receipt.InvocationID)
	if err != nil {
		return runtime.Invocation{}, "", false
	}
	return base, taskID, true
}

// isolatedWriterInvocationForReceiptID resolves one opt-in isolated/staged
// task-bound writer invocation by exact invocation ID. Serial writer receipts
// keep their existing identity; this helper only admits task-bound invocations
// whose exact child binding is still current. It performs no effects.
//
// In addition to the frozen initial cohort identities, it admits recorded
// scoped semantic corrections: a journal-recorded
// RoleSemanticCorrection whose TaskID and BaseInvocationID bind the frozen
// original task invocation and whose scheduledTurnInvocation identity equals
// the receipt ID. The frozen child binding must still be current and the
// parent candidate must still match the recorded correction; no admission-only
// lookup is performed.
func isolatedWriterInvocationForReceiptID(s Snapshot, receiptInvocationID string) (runtime.Invocation, string, error) {
	if !isolatedImplementationEnabled(s) || s.GraphIsolationPreparation == nil {
		return runtime.Invocation{}, "", errors.New("isolated writer receipt requires a frozen child cohort")
	}
	for _, taskID := range s.GraphIsolationPreparation.SelectedTaskIDs {
		invocation, err := writerInvocationForTask(s, taskID)
		if err != nil {
			continue
		}
		if invocation.ID != receiptInvocationID {
			continue
		}
		if invocation.Profile.Runtime != "opencode-http" && invocation.Profile.Runtime != "codex-app-server" {
			continue
		}
		return invocation, taskID, nil
	}
	var parentCandidateID string
	if s.Candidate != nil {
		if id, err := s.Candidate.ID(); err == nil {
			parentCandidateID = id
		}
	}
	for _, taskID := range s.GraphIsolationPreparation.SelectedTaskIDs {
		base, err := writerInvocationForTask(s, taskID)
		if err != nil {
			continue
		}
		for _, correction := range s.RoleCorrections {
			if correction.ScheduledTaskID == "" {
				continue
			}
			if correction.TaskID != taskID {
				continue
			}
			if correction.BaseInvocationID != base.ID {
				continue
			}
			if correction.Invocation.Profile.Role != base.Profile.Role {
				continue
			}
			if parentCandidateID != "" && correction.CandidateID != "" && correction.CandidateID != parentCandidateID {
				continue
			}
			operation, err := scheduledOperationForRole(correction.Invocation.Profile.Role)
			if err != nil {
				continue
			}
			scoped, err := scheduledTurnInvocation(correction.Invocation, operation, correction.ScheduledTaskID)
			if err != nil || scoped.ID != receiptInvocationID {
				continue
			}
			if scoped.Profile.Runtime != "opencode-http" && scoped.Profile.Runtime != "codex-app-server" {
				continue
			}
			return scoped, taskID, nil
		}
	}
	return runtime.Invocation{}, "", errors.New("isolated writer receipt task binding unavailable")
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
		if err != nil {
			if taskBase, _, ok := isolatedTaskBaseForReceipt(*s, receipt); ok {
				base, err = taskBase, nil
			}
		}
	case "reviewer":
		base, err = reviewInvocation(*s)
	default:
		return errors.New("unknown provider role receipt")
	}
	if err != nil {
		// Serial writer derivation fails once an isolated cohort is frozen
		// because the parent-bound writer invocation is no longer the
		// task-bound child invocation. Fall back to the exact frozen child
		// binding before rejecting, preserving serial behavior otherwise.
		if taskBase, _, ok := isolatedTaskBaseForReceipt(*s, receipt); ok {
			base, err = taskBase, nil
		}
		if err != nil {
			return err
		}
	}
	invocation, err := resolveScheduledInvocationID(*s, base, receipt.InvocationID)
	if err != nil {
		if taskBase, _, ok := isolatedTaskBaseForReceipt(*s, receipt); ok {
			base = taskBase
			invocation, err = resolveScheduledInvocationID(*s, base, receipt.InvocationID)
		}
	}
	if err != nil || invocation.Profile.Role != receipt.Role || invocation.Profile.Runtime != "opencode-http" || receipt.InvocationID != invocation.ID || receipt.Version != 1 || safepath.RequireDigest(receipt.AccessInvocationID) != nil || safepath.RequireDigest(receipt.RuntimeJournalHead) != nil || safepath.RequireDigest(receipt.GatewayJournalHead) != nil {
		return errors.New("provider role receipt identity mismatch")
	}
	if _, taskID, ok := isolatedTaskBaseForReceipt(*s, receipt); ok {
		expectedQuestion, questionErr := graphWriterTaskQuestion(*s, taskID)
		if questionErr != nil || receipt.Question != expectedQuestion {
			return errors.New("isolated writer receipt task binding mismatch")
		}
	}
	if err := validateRoutingDecisionForSnapshot(*s, invocation, receipt.RoutingDecision); err != nil {
		return err
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
	resolved, _, err := ConfiguredProviderExpectation(s.Creation.Config, receipt.Role, invocation.Profile)
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return errors.New("provider role observation substituted")
	}
	selected, err := resolveProviderRoutingForRecordedEvidence(s.Creation.Config, s.RunID, invocation, 1, receipt.RoutingDecision)
	if err != nil || selected.Intent.Reservation.InvocationID != receipt.AccessInvocationID || !sameCanonical(selected.Intent.RoutingDecision, receipt.RoutingDecision) {
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
	journalStem, err := providerInvocationJournalStemForSnapshot(controllerPath, s, invocation, scheduledAgentTurn(ctx))
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	return executeOpenCodeProviderRuntimeWithStem(ctx, controllerPath, s, invocation, candidateLease, journalStem)
}

// executeOpenCodeProviderRuntimeWithStem runs one OpenCode provider turn
// against caller-resolved journal paths. Serial callers resolve through
// executeOpenCodeProviderRuntime; isolated writers resolve their
// collision-free per-invocation namespace from the parent snapshot before the
// child snapshot replaces the candidate, so execution, usage verification and
// resumed observation share identical paths.
func executeOpenCodeProviderRuntimeWithStem(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation, candidateLease opencoderuntime.CandidateLease, journalStem string) (runtime.Result, providerDispatchReceipt, error) {
	if invocation.Profile.Runtime != "opencode-http" || s.Creation.Config.OpenCode == nil {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("OpenCode provider runtime required")
	}
	runtimeCtx, cancelRuntime := openCodeProviderRuntimeContext(ctx, s.Creation.Config.OpenCode)
	defer cancelRuntime()
	selected, err := resolveProviderRoutingForSnapshot(s, invocation, 1)
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
	if journalStem == "" {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("OpenCode journal namespace required")
	}
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
	contextBinding, err := contextbroker.NewBinding(invocation.ID, s.Creation.Repository, candidate, contextbroker.Limits{MaxCalls: contextmcp.InvocationToolCallBudget, MaxRequestBytes: 64 << 10, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 4 << 20})
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
	mcpQueueDepth := staticExplorerQueueDepth(runtimeCtx, invocation)
	if mcpQueueDepth == 0 {
		mcpQueueDepth = contextRoleQueueDepth(invocation)
	}
	var mcpQueueWait time.Duration
	if mcpQueueDepth > 0 {
		_, mcpQueueWait, err = contextmcp.QueuePolicyForCallBudget(mcpQueueDepth+1, contextmcp.ContextCallTimeout)
		if err != nil {
			_ = broker.Close()
			return runtime.Result{}, providerDispatchReceipt{}, err
		}
	}
	var record opencoderuntime.ResultRecord
	if recovering {
		record, err = opencoderuntime.Execute(runtimeCtx, opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, Intent: runtimeIntent, MCPQueueDepth: mcpQueueDepth, MCPQueueWait: mcpQueueWait, VerifyComposite: compositeVerify})
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
		record, err = opencoderuntime.Execute(runtimeCtx, opencoderuntime.ExecuteConfig{RuntimePath: runtimePath, StateRoot: hostRoot, Paths: paths, Intent: runtimeIntent, AccessJournalPath: accessPath, Policy: selected.Policy, AccessIntent: selected.Intent, Gateway: selected.Gateway, Credential: credential, Transport: transport, Broker: broker, CandidateLease: candidateLease, ProviderRole: selected.ProviderRole, RequestExpectation: expectation, Executable: s.Creation.Config.OpenCode.Executable, ExecutableSHA256: s.Creation.Config.OpenCode.ExecutableHash, Port: port, ParentMessageID: "msg_" + invocation.ID, Output: io.Discard, ReadbackTimeout: openCodeReadbackTimeout(s.Creation.Config.OpenCode), ProviderTimeout: 5 * time.Minute, ToolTimeout: 30 * time.Second, ReadinessTimeout: 5 * time.Minute, SealTimeout: 5 * time.Minute, MCPQueueDepth: mcpQueueDepth, MCPQueueWait: mcpQueueWait, InterruptShutdown: scheduledInterruptShutdownLookup(runtimeCtx), Composite: compositeConfig, VerifyComposite: compositeVerify})
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
	receipt := providerDispatchReceipt{Version: 1, Role: invocation.Profile.Role, InvocationID: invocation.ID, AccessInvocationID: selected.Intent.Reservation.InvocationID, RoutingDecision: selected.Intent.RoutingDecision, RuntimeJournalHead: runtimeHead, GatewayJournalHead: record.GatewayHead, ResultHash: resultHash, ObservedModel: observedModel, ObservedProvider: selected.ProviderRole.Model.Provider, Result: record.Result}
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
	invocation, err := plannerInvocationForSnapshot(*s)
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
	resolved, _, err := ConfiguredProviderExpectation(s.Creation.Config, "planner", invocation.Profile)
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return errors.New("direct planner provider observation substituted")
	}
	if err := validateRoutingDecisionForSnapshot(*s, invocation, receipt.RoutingDecision); err != nil {
		return err
	}
	selected, err := resolveProviderRoutingForRecordedEvidence(s.Creation.Config, s.RunID, invocation, 1, receipt.RoutingDecision)
	if err != nil || selected.Intent.Reservation.InvocationID != receipt.AccessInvocationID || !sameCanonical(selected.Intent.RoutingDecision, receipt.RoutingDecision) {
		return errors.New("direct planner access identity mismatch")
	}
	copy := receipt
	s.PlannerProvider = &copy
	return nil
}

func requireExecutableRoleRuntime(profile runtime.Profile) error {
	switch profile.Runtime {
	case "fake", "codex-app-server", "opencode-http", "provider-api", "claude-cli", "acp-stdio", "direct-api":
		return nil
	default:
		return providergateway.ErrCapabilityUnavailable
	}
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

// isolatedOpenCodeRuntimeForTest substitutes the provider-effect execution for
// one isolated writer turn in tests only. It receives the caller-resolved
// deterministic journal namespace and must honor it exactly so the sealed
// fixture journals it writes verify through the same usage path as production
// execution. Production code leaves it nil; no external model is involved.
var isolatedOpenCodeRuntimeForTest func(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation, lease opencoderuntime.CandidateLease, journalStem string) (runtime.Result, providerDispatchReceipt, error)

// isolatedWriterBindingForSnapshot resolves the confirmed child binding for
// the active isolation mode: staged fork bindings for v3 cohorts, plain child
// bindings otherwise.
func isolatedWriterBindingForSnapshot(s Snapshot, taskID string) (isolatedWriterBinding, error) {
	if stagedIsolationEnabled(s) {
		return stagedForkBindingForTask(s, taskID)
	}
	return isolatedWriterBindingForTask(s, taskID)
}

// executeIsolatedOpenCodeWriter runs one opt-in isolated/staged writer turn
// against its exact confirmed child workspace/candidate. It reuses the
// existing OpenCode runtime/provider/gateway machinery with per-invocation
// journals, immutable policy/scopes, scheduler reservations and source
// ownership. The parent candidate is never bound as the tool candidate; the
// child candidate/task/workspace binding is enforced before and after
// dispatch. UNKNOWN stops with no resend via the shared agent-dispatch
// observation; old active uncertain effects are never probed here.
func executeIsolatedOpenCodeWriter(ctx context.Context, controllerPath string, taskID string, invocation runtime.Invocation, binding isolatedWriterBinding, question string) (result runtime.Result, err error) {
	if invocation.Profile.Runtime != "opencode-http" {
		return result, errors.New("isolated OpenCode writer requires the opencode-http runtime")
	}
	if taskID == "" || binding.TaskID != taskID {
		return result, errors.New("isolated OpenCode writer task binding required")
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return result, err
	}
	if !isolatedImplementationEnabled(s) {
		return result, errors.New("isolated OpenCode writer requires the opt-in isolated implementation policy")
	}
	if s.Workspace == nil || s.Candidate == nil {
		return result, errors.New("isolated OpenCode writer requires an admitted parent candidate")
	}
	var currentBinding isolatedWriterBinding
	currentBinding, err = isolatedWriterBindingForSnapshot(s, taskID)
	if err != nil || !sameCanonical(currentBinding, binding) {
		return result, errors.Join(errors.New("isolated OpenCode writer binding changed before dispatch"), err)
	}
	expected, err := writerInvocationForIsolatedTask(s, binding)
	resolved, resolveErr := resolveScheduledRecordedInvocation(s, expected, invocation)
	if err != nil || resolveErr != nil || resolved != invocation {
		return result, errors.Join(errors.New("isolated OpenCode writer invocation is stale or substituted"), err, resolveErr)
	}
	var lease controllerCandidateLease
	lease, err = worktree.AcquireRead(binding.Workspace.Request)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	latest, err := Inspect(controllerPath)
	if err != nil {
		return result, err
	}
	var freshBinding isolatedWriterBinding
	freshBinding, err = isolatedWriterBindingForSnapshot(latest, taskID)
	if err != nil || !sameCanonical(freshBinding, binding) {
		return result, errors.Join(errors.New("isolated OpenCode writer binding changed before dispatch"), err)
	}
	freshInvocation, err := writerInvocationForIsolatedTask(latest, binding)
	resolvedFresh, resolveFreshErr := resolveScheduledRecordedInvocation(latest, freshInvocation, invocation)
	if err != nil || resolveFreshErr != nil || resolvedFresh != invocation {
		return result, errors.Join(errors.New("isolated OpenCode writer invocation changed before dispatch"), err, resolveFreshErr)
	}
	childCurrent, _, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil || childCurrent != binding.Candidate {
		return result, errors.Join(errors.New("isolated OpenCode child candidate changed before dispatch"), err)
	}
	if latest.Candidate == nil || latest.Workspace == nil {
		return result, errors.New("isolated OpenCode parent candidate unavailable")
	}
	parentCurrent, err := worktree.Fingerprint(ctx, *latest.Workspace)
	if err != nil || parentCurrent != *latest.Candidate {
		return result, errors.Join(errors.New("isolated OpenCode parent candidate changed before dispatch"), err)
	}
	if receipt, ok := latest.ProviderRuntime[invocation.ID]; ok {
		if receipt.Question != question || receipt.Role != invocation.Profile.Role || receipt.InvocationID != invocation.ID {
			return result, errors.New("recorded isolated OpenCode role receipt changed")
		}
		if err := requireOpenCodeRoleReceipt(latest, invocation, receipt.Result); err != nil {
			return result, err
		}
		if err := settleProviderDispatchTaskPool(controllerPath, latest.Creation.Config, latest.RunID, invocation, receipt); err != nil {
			return result, err
		}
		return receipt.Result, nil
	}
	dispatchBinding, err := beginAgentDispatchForTurn(controllerPath, latest, invocation, scheduledAgentTurn(ctx))
	if err != nil {
		return result, err
	}
	if err := ensureOpenCodeDispatchCapacity(latest, invocation); err != nil {
		return result, err
	}
	if err := markAgentDispatchRunning(&dispatchBinding); err != nil {
		return result, err
	}
	// Recheck host admission immediately before runtime dispatch: the lease,
	// fingerprint and capacity checks above leave a TOCTOU window, and the
	// serial path re-inspects plus requires current host admission at the same
	// point. A stale or mutating binding fails here with the admitted effect
	// identity retained and no provider effect started below.
	preDispatch, err := Inspect(controllerPath)
	if err != nil {
		return result, errors.Join(err, finishAgentDispatchUnknown(dispatchBinding))
	}
	if err := requireCurrentHostAdmission(ctx, preDispatch); err != nil {
		return result, errors.Join(err, finishAgentDispatchUnknown(dispatchBinding))
	}
	var preBinding isolatedWriterBinding
	preBinding, err = isolatedWriterBindingForSnapshot(preDispatch, taskID)
	if err != nil || !sameCanonical(preBinding, binding) {
		return result, errors.Join(errors.New("isolated OpenCode writer binding changed before dispatch"), err, finishAgentDispatchUnknown(dispatchBinding))
	}
	preInvocation, err := writerInvocationForIsolatedTask(preDispatch, binding)
	resolvedPre, resolvePreErr := resolveScheduledRecordedInvocation(preDispatch, preInvocation, invocation)
	if err != nil || resolvePreErr != nil || resolvedPre != invocation {
		return result, errors.Join(errors.New("isolated OpenCode writer invocation changed before dispatch"), err, resolvePreErr, finishAgentDispatchUnknown(dispatchBinding))
	}
	journalStem, err := providerInvocationJournalStemForSnapshot(controllerPath, preDispatch, invocation, scheduledAgentTurn(ctx))
	if err != nil {
		return result, errors.Join(err, finishAgentDispatchUnknown(dispatchBinding))
	}
	childSnapshot := preDispatch
	childSnapshot.Workspace = &binding.Workspace
	childSnapshot.Candidate = &binding.Candidate
	runtimeHook := executeOpenCodeProviderRuntimeWithStem
	if isolatedOpenCodeRuntimeForTest != nil {
		runtimeHook = isolatedOpenCodeRuntimeForTest
	}
	dispatched, receipt, dispatchErr := runtimeHook(ctx, controllerPath, childSnapshot, invocation, lease, journalStem)
	if dispatchErr != nil {
		return result, errors.Join(dispatchErr, finishAgentDispatchUnknown(dispatchBinding))
	}
	// Confirm the child, workspace and parent binding after dispatch before
	// observing successful receipt: a mutating provider turn or a host
	// admission change fails safely here with the effect identity retained in
	// the runtime journals and no success observation or retry.
	post, err := Inspect(controllerPath)
	if err != nil {
		return result, errors.Join(err, finishAgentDispatchUnknown(dispatchBinding))
	}
	var postBinding isolatedWriterBinding
	postBinding, err = isolatedWriterBindingForSnapshot(post, taskID)
	if err != nil || !sameCanonical(postBinding, binding) {
		return result, errors.Join(errors.New("isolated OpenCode writer binding changed during dispatch"), err, finishAgentDispatchUnknown(dispatchBinding))
	}
	postInvocation, err := writerInvocationForIsolatedTask(post, binding)
	resolvedPost, resolvePostErr := resolveScheduledRecordedInvocation(post, postInvocation, invocation)
	if err != nil || resolvePostErr != nil || resolvedPost != invocation {
		return result, errors.Join(errors.New("isolated OpenCode writer invocation changed during dispatch"), err, resolvePostErr, finishAgentDispatchUnknown(dispatchBinding))
	}
	if err := requireCurrentHostAdmission(ctx, post); err != nil {
		return result, errors.Join(err, finishAgentDispatchUnknown(dispatchBinding))
	}
	postChild, _, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil || postChild != binding.Candidate {
		return result, errors.Join(errors.New("isolated OpenCode child candidate changed during dispatch"), err, finishAgentDispatchUnknown(dispatchBinding))
	}
	if post.Candidate == nil || post.Workspace == nil {
		return result, errors.Join(errors.New("isolated OpenCode parent candidate unavailable after dispatch"), finishAgentDispatchUnknown(dispatchBinding))
	}
	postParent, err := worktree.Fingerprint(ctx, *post.Workspace)
	if err != nil || postParent != *post.Candidate {
		return result, errors.Join(errors.New("isolated OpenCode parent candidate changed during dispatch"), err, finishAgentDispatchUnknown(dispatchBinding))
	}
	receipt.Question = question
	if err := Append(controllerPath, "role.provider-observed", receipt); err != nil {
		return result, err
	}
	if err := settleProviderDispatchTaskPool(controllerPath, post.Creation.Config, post.RunID, invocation, receipt); err != nil {
		return result, err
	}
	if err := finishAgentDispatch(dispatchBinding, receipt.ResultHash); err != nil {
		return runtime.Result{}, err
	}
	return dispatched, nil
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

func executeDirectProviderRuntime(ctx context.Context, controllerPath string, s Snapshot, invocation runtime.Invocation) (runtime.Result, providerDispatchReceipt, error) {
	c, runID := s.Creation.Config, s.RunID
	if invocation.Profile.Runtime != "provider-api" {
		return runtime.Result{}, providerDispatchReceipt{}, errors.New("direct provider runtime required")
	}
	configured, expectation, err := ConfiguredProviderExpectation(c, invocation.Profile.Role, invocation.Profile)
	if err != nil {
		return runtime.Result{}, providerDispatchReceipt{}, err
	}
	direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
	selected, err := resolveProviderRoutingForSnapshot(s, invocation, 1)
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
	receipt := providerDispatchReceipt{Version: 1, Role: invocation.Profile.Role, InvocationID: invocation.ID, AccessInvocationID: selected.Intent.Reservation.InvocationID, RoutingDecision: selected.Intent.RoutingDecision, RuntimeJournalHead: runtimeHead, GatewayJournalHead: gatewayHead, ResultHash: resultHash, ObservedModel: directResult.Receipt.ObservedModel, ObservedProvider: configured.Model.Provider, Result: result}
	return result, receipt, nil
}

func journalHead(path string) (string, error) {
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		return "", errors.Join(errors.New("runtime evidence journal is empty"), err)
	}
	return events[len(events)-1].Hash, nil
}
