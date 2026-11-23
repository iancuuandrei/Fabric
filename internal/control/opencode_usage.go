package control

import (
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/toolreceipts"
)

// OpenCodeTokenUsage reports replay-validated normalized provider totals for
// one completed OpenCode invocation. Cached input is a subset of input,
// reasoning is a subset of output and cache write is a subset of input,
// matching the durable provider gateway invariants. Uncached and ordinary are
// checked subtractions, never additional tokens. Unknown optional axes remain
// null, never zero. Money is always unknown and never reported.
type OpenCodeTokenUsage struct {
	InputTokens          int64  `json:"input_tokens"`
	CachedInputTokens    *int64 `json:"cached_input_tokens"`
	UncachedInputTokens  *int64 `json:"uncached_input_tokens"`
	OutputTokens         int64  `json:"output_tokens"`
	ReasoningTokens      *int64 `json:"reasoning_tokens"`
	OrdinaryOutputTokens *int64 `json:"ordinary_output_tokens"`
	CacheWriteTokens     *int64 `json:"cache_write_tokens"`
}

// OpenCodeUsageEntry describes one admitted OpenCode invocation in actual
// controller admission order. ReceiptMatched distinguishes admitted evidence
// from raw runtime state. Pending work remains UNKNOWN with no usage totals,
// never inferring completion from gateway receipts or zero spend.
// Direct-provider adapters are outside current scope and omitted.
type OpenCodeUsageEntry struct {
	InvocationID    string              `json:"invocation_id"`
	Role            string              `json:"role"`
	TurnID          string              `json:"turn_id,omitempty"`
	JournalPresent  bool                `json:"journal_present"`
	ReceiptMatched  bool                `json:"receipt_matched"`
	Status          string              `json:"status"`
	ObservedModel   string              `json:"observed_model,omitempty"`
	Usage           *OpenCodeTokenUsage `json:"usage,omitempty"`
	GatewayCalls    *int                `json:"gateway_calls,omitempty"`
	GatewayReceipts *int                `json:"gateway_receipts,omitempty"`
	GatewayPending  *int                `json:"gateway_pending,omitempty"`
}

func cloneUsageOptional(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// summarizeOpenCodeGatewayUsage projects a replay-validated normalized
// gateway aggregate into display totals without inventing evidence. Bounds
// come from the replay-validated gateway journal itself; this helper only
// enforces the durable subset invariants and checked subtractions.
func summarizeOpenCodeGatewayUsage(aggregate providergateway.Usage) (OpenCodeTokenUsage, error) {
	if aggregate.InputTokens < 0 || aggregate.OutputTokens < 0 {
		return OpenCodeTokenUsage{}, errors.New("opencode gateway usage out of bounds")
	}
	if aggregate.ReasoningTokens != nil && (*aggregate.ReasoningTokens < 0 || *aggregate.ReasoningTokens > aggregate.OutputTokens) {
		return OpenCodeTokenUsage{}, errors.New("opencode reasoning subset invalid")
	}
	if aggregate.CacheReadTokens != nil && (*aggregate.CacheReadTokens < 0 || *aggregate.CacheReadTokens > aggregate.InputTokens) {
		return OpenCodeTokenUsage{}, errors.New("opencode cached subset invalid")
	}
	if aggregate.CacheWriteTokens != nil && (*aggregate.CacheWriteTokens < 0 || *aggregate.CacheWriteTokens > aggregate.InputTokens) {
		return OpenCodeTokenUsage{}, errors.New("opencode cache write subset invalid")
	}
	var uncached *int64
	if aggregate.CacheReadTokens != nil {
		value := aggregate.InputTokens - *aggregate.CacheReadTokens
		if value < 0 {
			return OpenCodeTokenUsage{}, errors.New("opencode cached subset invalid")
		}
		uncached = &value
	}
	var ordinary *int64
	if aggregate.ReasoningTokens != nil {
		value := aggregate.OutputTokens - *aggregate.ReasoningTokens
		if value < 0 {
			return OpenCodeTokenUsage{}, errors.New("opencode reasoning subset invalid")
		}
		ordinary = &value
	}
	return OpenCodeTokenUsage{
		InputTokens:          aggregate.InputTokens,
		CachedInputTokens:    cloneUsageOptional(aggregate.CacheReadTokens),
		UncachedInputTokens:  uncached,
		OutputTokens:         aggregate.OutputTokens,
		ReasoningTokens:      cloneUsageOptional(aggregate.ReasoningTokens),
		OrdinaryOutputTokens: ordinary,
		CacheWriteTokens:     cloneUsageOptional(aggregate.CacheWriteTokens),
	}, nil
}

func openCodeStemPaths(controllerPath, stem string) (string, string) {
	return controllerPath + "." + stem + ".opencode-runtime.jsonl", controllerPath + "." + stem + ".provider-gateway.jsonl"
}

func openCodeJournalHead(events []journal.Event) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Hash
}

func baseInvocationForReceipt(prefix Snapshot, receipt providerDispatchReceipt) (runtime.Invocation, error) {
	switch receipt.Role {
	case "planner":
		return plannerInvocationForSnapshot(prefix)
	case "explorer":
		return explorerInvocation(prefix, receipt.Question)
	case "writer", "fixer":
		base, err := writerInvocation(prefix)
		if err == nil {
			if base.ID == receipt.InvocationID {
				return base, nil
			}
			if _, resolveErr := resolveScheduledInvocationID(prefix, base, receipt.InvocationID); resolveErr == nil {
				return base, nil
			}
		}
		if taskBase, _, ok := isolatedTaskBaseForReceipt(prefix, receipt); ok {
			return taskBase, nil
		}
		return base, err
	case "reviewer":
		return reviewInvocation(prefix)
	default:
		return runtime.Invocation{}, errors.New("opencode receipt role changed")
	}
}

// authoritativeInvocationForReceipt reconstructs controller authority at the
// correct historical prefix, consistent with replayRoleProvider and
// resolveScheduledInvocationID. It never trusts runtime intent as authority.
func authoritativeInvocationForReceipt(events []journal.Event, receiptIndex int, receipt providerDispatchReceipt) (runtime.Invocation, *taskscheduler.AgentTurnBinding, Snapshot, error) {
	if receiptIndex < 0 || receiptIndex >= len(events) {
		return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode receipt order changed")
	}
	prefix, err := Replay(events[:receiptIndex])
	if err != nil {
		return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode controller prefix changed")
	}
	base, err := baseInvocationForReceipt(prefix, receipt)
	if err != nil {
		return runtime.Invocation{}, nil, Snapshot{}, err
	}
	if receipt.Role == "planner" {
		if base.ID != receipt.InvocationID {
			// Scheduled planner turns resolve through the same admission path
			// as other roles; legacy planner receipts match the base ID.
			resolved, resolveErr := resolveScheduledInvocationID(prefix, base, receipt.InvocationID)
			if resolveErr != nil || resolved.ID != receipt.InvocationID {
				return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode invocation identity changed")
			}
			if dispatch, ok := prefix.AgentDispatch[receipt.InvocationID]; ok {
				return resolved, dispatch.Admission.AgentTurn, prefix, nil
			}
			return resolved, nil, prefix, nil
		}
		var turn *taskscheduler.AgentTurnBinding
		if dispatch, ok := prefix.AgentDispatch[receipt.InvocationID]; ok {
			if dispatch.Admission.Invocation.ID != receipt.InvocationID {
				return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode invocation identity changed")
			}
			turn = dispatch.Admission.AgentTurn
		}
		return base, turn, prefix, nil
	}
	resolved, err := resolveScheduledInvocationID(prefix, base, receipt.InvocationID)
	if err != nil || resolved.ID != receipt.InvocationID {
		return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode invocation identity changed")
	}
	var turn *taskscheduler.AgentTurnBinding
	if dispatch, ok := prefix.AgentDispatch[receipt.InvocationID]; ok {
		if dispatch.Admission.Invocation.ID != receipt.InvocationID {
			return runtime.Invocation{}, nil, Snapshot{}, errors.New("opencode invocation identity changed")
		}
		turn = dispatch.Admission.AgentTurn
	}
	return resolved, turn, prefix, nil
}

// openCodeCompositeVerifier builds the existing journal-only composite
// verifier for one admitted v3 turn. It performs no effects.
func openCodeCompositeVerifier(controllerPath, schedulerPath string, caller agentDispatchBinding, brokerPath string, contextBinding contextbroker.Binding, receipts toolreceipts.Binding) (opencode.CompositeBackendVerifier, error) {
	return newCompositeBackendVerifier(controllerPath, schedulerPath, brokerPath, caller, contextBinding, receipts)
}

// verifyOpenCodeCompositeWithScheduler verifies one admitted v3 turn through
// the existing journal-only verifier and InspectComposite. InspectComposite
// itself derives through ValidateCompositeFinalGateway, so the sealed gateway
// transcript is proved without new execution machinery. Completed effects
// report completed with replay-validated totals, never effect UNKNOWN.
func verifyOpenCodeCompositeWithScheduler(controllerPath, schedulerPath string, s Snapshot, receipt providerDispatchReceipt, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding, intent opencoderuntime.Intent, record *opencoderuntime.ResultRecord, gateway providergateway.State, gatewayEvents []journal.Event) (OpenCodeUsageEntry, error) {
	dispatch, ok := s.AgentDispatch[receipt.InvocationID]
	if !ok || dispatch.Admission.AgentTurn == nil || turn == nil || *dispatch.Admission.AgentTurn != *turn {
		return OpenCodeUsageEntry{}, errors.New("opencode admission missing for receipt")
	}
	admissionID, err := dispatch.Admission.ID()
	if err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode admission missing for receipt")
	}
	stem, err := providerInvocationJournalStemForSnapshot(controllerPath, s, invocation, turn)
	if err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode journal identity unavailable")
	}
	runtimePath, _ := openCodeStemPaths(controllerPath, stem)
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil || len(runtimeEvents) == 0 {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime journal missing for admitted receipt")
	}
	bound, boundOk := openCodeBoundForIntent(runtimeEvents, intent)
	if !boundOk {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	if intent.ToolReceipts == nil || bound.Paths.Broker == "" {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	caller := agentDispatchBinding{JournalPath: controllerPath + ".agent-tree", ControllerPath: controllerPath, AdmissionID: admissionID, Node: dispatch.Admission.Node, InvocationID: invocation.ID, AgentTurn: turn, Recovering: true}
	verify, err := openCodeCompositeVerifier(controllerPath, schedulerPath, caller, bound.Paths.Broker, intent.Context, *intent.ToolReceipts)
	if err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode scheduler binding differs")
	}
	state, err := opencoderuntime.InspectComposite(runtimePath, intent, verify)
	if err != nil || state.Result == nil || state.Bound == nil {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	if !sameCanonical(*state.Result, *record) {
		return OpenCodeUsageEntry{}, errors.New("opencode result changed")
	}
	if gateway.Binding == nil || gateway.Binding.AccessInvocationID != receipt.AccessInvocationID {
		return OpenCodeUsageEntry{}, errors.New("opencode access identity changed")
	}
	if intent.ProviderGatewayBindingID == "" {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway binding missing")
	}
	bindingID, err := gateway.Binding.ID()
	if err != nil || bindingID != intent.ProviderGatewayBindingID {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway binding changed")
	}
	if gateway.Pending != nil || !gateway.Finished || gateway.Exhausted || len(gateway.Calls) == 0 {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
	}
	if record.GatewayHead != openCodeJournalHead(gatewayEvents) || record.GatewayHead != receipt.GatewayJournalHead {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway head changed")
	}
	stateID, err := canonical.Hash("harness.opencode-runtime-final-gateway.v1", gateway)
	if err != nil || record.GatewayStateID == "" || stateID != record.GatewayStateID {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
	}
	if record.GatewayUsage == nil || !sameCanonical(*record.GatewayUsage, gateway.Aggregate) {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway usage changed")
	}
	if receipt.Result.Usage.CostMinorUnits != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode cost accounting unsupported")
	}
	if receipt.Result.Usage.InputTokens == nil || receipt.Result.Usage.OutputTokens == nil {
		return OpenCodeUsageEntry{}, errors.New("opencode usage missing for admitted receipt")
	}
	if *receipt.Result.Usage.InputTokens != gateway.Aggregate.InputTokens || *receipt.Result.Usage.OutputTokens != gateway.Aggregate.OutputTokens {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway usage changed")
	}
	resolved, _, err := ConfiguredProviderExpectation(s.Creation.Config, receipt.Role, invocation.Profile)
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return OpenCodeUsageEntry{}, errors.New("opencode model changed")
	}
	if gateway.Calls[len(gateway.Calls)-1].Receipt == nil || gateway.Calls[len(gateway.Calls)-1].Receipt.ObservedModel != receipt.ObservedModel {
		return OpenCodeUsageEntry{}, errors.New("opencode model changed")
	}
	totals, err := summarizeOpenCodeGatewayUsage(gateway.Aggregate)
	if err != nil {
		return OpenCodeUsageEntry{}, err
	}
	calls, receipts, pending := openCodeGatewayCounts(gateway)
	entry := OpenCodeUsageEntry{
		InvocationID:    receipt.InvocationID,
		Role:            receipt.Role,
		TurnID:          turn.TurnID,
		JournalPresent:  true,
		ReceiptMatched:  true,
		Status:          "completed",
		ObservedModel:   receipt.ObservedModel,
		Usage:           &totals,
		GatewayCalls:    &calls,
		GatewayReceipts: &receipts,
		GatewayPending:  &pending,
	}
	return entry, nil
}

func openCodeBoundForIntent(runtimeEvents []journal.Event, intent opencoderuntime.Intent) (opencoderuntime.Bound, bool) {
	for _, event := range runtimeEvents {
		if event.Kind != "opencode-runtime.bound" {
			continue
		}
		var bound opencoderuntime.Bound
		if err := canonical.Decode(event.Payload, &bound); err != nil {
			return opencoderuntime.Bound{}, false
		}
		if bound.IntentID == intent.IntentID {
			return bound, true
		}
	}
	return opencoderuntime.Bound{}, false
}

// verifyOpenCodeReceipt replays controller evidence and verifies each runtime
// and gateway receipt head before returning accounting. It performs no writes
// and never calls mutating bind, Execute, Complete or settle helpers.
// Composite turns require an exact scheduler binding; use
// verifyOpenCodeReceiptWithScheduler to supply one.
func verifyOpenCodeReceipt(controllerPath string, s Snapshot, events []journal.Event, receiptIndex int, receipt providerDispatchReceipt) (OpenCodeUsageEntry, error) {
	return verifyOpenCodeReceiptWithScheduler(controllerPath, "", s, events, receiptIndex, receipt)
}

// verifyOpenCodeReceiptWithScheduler is verifyOpenCodeReceipt with an exact
// scheduler journal binding for composite turns. The scheduler is consulted
// only through the existing journal-only composite verifier for v3 turns;
// v1/v2 turns never read it. An empty scheduler fails v3 turns safely.
func verifyOpenCodeReceiptWithScheduler(controllerPath, schedulerPath string, s Snapshot, events []journal.Event, receiptIndex int, receipt providerDispatchReceipt) (OpenCodeUsageEntry, error) {
	if receipt.Version != 1 {
		return OpenCodeUsageEntry{}, errors.New("opencode receipt version changed")
	}
	switch receipt.Role {
	case "planner", "explorer", "writer", "fixer", "reviewer":
	default:
		return OpenCodeUsageEntry{}, errors.New("opencode receipt role changed")
	}
	if receipt.Result.Requested.Runtime != "opencode-http" {
		return OpenCodeUsageEntry{}, errors.New("direct provider usage remains outside current scope")
	}
	if safepath.RequireDigest(receipt.InvocationID) != nil || safepath.RequireDigest(receipt.AccessInvocationID) != nil || safepath.RequireDigest(receipt.RuntimeJournalHead) != nil || safepath.RequireDigest(receipt.GatewayJournalHead) != nil || safepath.RequireDigest(receipt.ResultHash) != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode receipt identity changed")
	}
	invocation, turn, receiptPrefix, err := authoritativeInvocationForReceipt(events, receiptIndex, receipt)
	if err != nil {
		return OpenCodeUsageEntry{}, err
	}
	if invocation.Profile.Runtime != "opencode-http" || invocation.Profile.Role != receipt.Role {
		return OpenCodeUsageEntry{}, errors.New("opencode invocation identity changed")
	}
	if receipt.Result.InvocationID != receipt.InvocationID || receipt.Result.Requested != invocation.Profile {
		return OpenCodeUsageEntry{}, errors.New("opencode result identity changed")
	}
	// Resolve through the historical receipt prefix first so the deterministic
	// isolated namespace matches the execution-time derivation exactly; fall
	// back to the final snapshot only when the prefix no longer admits the
	// frozen child binding (e.g. post-integration prefixes).
	stem, err := providerInvocationJournalStemForSnapshot(controllerPath, receiptPrefix, invocation, turn)
	if err != nil {
		stem, err = providerInvocationJournalStemForSnapshot(controllerPath, s, invocation, turn)
	}
	if err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode journal identity unavailable")
	}
	runtimePath, gatewayPath := openCodeStemPaths(controllerPath, stem)
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil || len(runtimeEvents) == 0 {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime journal missing for admitted receipt")
	}
	gatewayEvents, err := journal.Read(gatewayPath)
	if err != nil || len(gatewayEvents) == 0 {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway journal missing for admitted receipt")
	}
	if openCodeJournalHead(runtimeEvents) != receipt.RuntimeJournalHead {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime head changed")
	}
	if openCodeJournalHead(gatewayEvents) != receipt.GatewayJournalHead {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway head changed")
	}
	if runtimeEvents[0].Kind != "opencode-runtime.intent" {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	var intent opencoderuntime.Intent
	if err := canonical.Decode(runtimeEvents[0].Payload, &intent); err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	if _, err := intent.ID(); err != nil || intent.IntentID == "" {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	if intent.Invocation.ID != receipt.InvocationID {
		return OpenCodeUsageEntry{}, errors.New("opencode invocation identity changed")
	}
	if !sameCanonical(intent.Context.Source, s.Creation.Repository) {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	var record *opencoderuntime.ResultRecord
	for _, event := range runtimeEvents {
		if event.Kind != "opencode-runtime.result" {
			continue
		}
		var candidate opencoderuntime.ResultRecord
		if err := canonical.Decode(event.Payload, &candidate); err != nil {
			return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
		}
		copy := candidate
		record = &copy
		break
	}
	if record == nil {
		return OpenCodeUsageEntry{}, errors.New("opencode result missing for admitted receipt")
	}
	if record.IntentID != intent.IntentID {
		return OpenCodeUsageEntry{}, errors.New("opencode result identity changed")
	}
	if !sameCanonical(record.Result, receipt.Result) {
		return OpenCodeUsageEntry{}, errors.New("opencode result changed")
	}
	domain, err := runtimeResultDomain(receipt.Role)
	if err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode receipt role changed")
	}
	digest, err := canonical.Hash(domain, receipt.Result)
	if err != nil || digest != receipt.ResultHash {
		return OpenCodeUsageEntry{}, errors.New("opencode result changed")
	}
	if intent.Version == 3 {
		if schedulerPath == "" {
			return OpenCodeUsageEntry{}, errors.New("opencode scheduler binding unavailable")
		}
		gatewayForComposite, err := providergateway.Inspect(gatewayPath)
		if err != nil || gatewayForComposite.Binding == nil {
			return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
		}
		return verifyOpenCodeCompositeWithScheduler(controllerPath, schedulerPath, s, receipt, invocation, turn, intent, record, gatewayForComposite, gatewayEvents)
	}
	if _, err := opencoderuntime.Inspect(runtimePath, intent); err != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode runtime source changed")
	}
	gateway, err := providergateway.Inspect(gatewayPath)
	if err != nil || gateway.Binding == nil {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
	}
	if gateway.Binding.AccessInvocationID != receipt.AccessInvocationID {
		return OpenCodeUsageEntry{}, errors.New("opencode access identity changed")
	}
	if intent.ProviderGatewayBindingID == "" {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway binding missing")
	}
	bindingID, err := gateway.Binding.ID()
	if err != nil || bindingID != intent.ProviderGatewayBindingID {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway binding changed")
	}
	if gateway.Pending != nil || !gateway.Finished || gateway.Exhausted {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
	}
	if record.GatewayHead != openCodeJournalHead(gatewayEvents) || record.GatewayHead != receipt.GatewayJournalHead {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway head changed")
	}
	stateID, err := canonical.Hash("harness.opencode-runtime-final-gateway.v1", gateway)
	if err != nil || record.GatewayStateID == "" || stateID != record.GatewayStateID {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway changed")
	}
	if record.GatewayUsage == nil || !sameCanonical(*record.GatewayUsage, gateway.Aggregate) {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway usage changed")
	}
	if receipt.Result.Usage.CostMinorUnits != nil {
		return OpenCodeUsageEntry{}, errors.New("opencode cost accounting unsupported")
	}
	if receipt.Result.Usage.InputTokens == nil || receipt.Result.Usage.OutputTokens == nil {
		return OpenCodeUsageEntry{}, errors.New("opencode usage missing for admitted receipt")
	}
	if *receipt.Result.Usage.InputTokens != gateway.Aggregate.InputTokens || *receipt.Result.Usage.OutputTokens != gateway.Aggregate.OutputTokens {
		return OpenCodeUsageEntry{}, errors.New("opencode gateway usage changed")
	}
	resolved, _, err := ConfiguredProviderExpectation(s.Creation.Config, receipt.Role, invocation.Profile)
	if err != nil || receipt.ObservedProvider != resolved.Model.Provider || !providergateway.AcceptsObservedModel(resolved.Model, receipt.ObservedModel) {
		return OpenCodeUsageEntry{}, errors.New("opencode model changed")
	}
	if len(gateway.Calls) == 0 || gateway.Calls[len(gateway.Calls)-1].Receipt == nil || gateway.Calls[len(gateway.Calls)-1].Receipt.ObservedModel != receipt.ObservedModel {
		return OpenCodeUsageEntry{}, errors.New("opencode model changed")
	}
	totals, err := summarizeOpenCodeGatewayUsage(gateway.Aggregate)
	if err != nil {
		return OpenCodeUsageEntry{}, err
	}
	calls, receipts, pending := openCodeGatewayCounts(gateway)
	entry := OpenCodeUsageEntry{
		InvocationID:    receipt.InvocationID,
		Role:            receipt.Role,
		JournalPresent:  true,
		ReceiptMatched:  true,
		Status:          "completed",
		ObservedModel:   receipt.ObservedModel,
		Usage:           &totals,
		GatewayCalls:    &calls,
		GatewayReceipts: &receipts,
		GatewayPending:  &pending,
	}
	if turn != nil {
		entry.TurnID = turn.TurnID
	}
	return entry, nil
}

func openCodeGatewayCounts(state providergateway.State) (int, int, int) {
	calls := len(state.Calls)
	receipts := 0
	for _, call := range state.Calls {
		if call.Receipt != nil {
			receipts++
		}
	}
	pending := 0
	if state.Pending != nil {
		pending = 1
	}
	return calls, receipts, pending
}

func pendingOpenCodeEntry(controllerPath string, s Snapshot, admission AgentDispatchAdmission) (OpenCodeUsageEntry, bool) {
	if admission.Invocation.Profile.Runtime != "opencode-http" {
		return OpenCodeUsageEntry{}, false
	}
	if _, ok := s.ProviderRuntime[admission.Invocation.ID]; ok {
		return OpenCodeUsageEntry{}, false
	}
	if admission.Invocation.Profile.Role == "planner" && s.PlannerProvider != nil && s.PlannerProvider.InvocationID == admission.Invocation.ID {
		return OpenCodeUsageEntry{}, false
	}
	entry := OpenCodeUsageEntry{
		InvocationID:   admission.Invocation.ID,
		Role:           admission.Invocation.Profile.Role,
		JournalPresent: false,
		Status:         "UNKNOWN",
	}
	if admission.AgentTurn != nil {
		entry.TurnID = admission.AgentTurn.TurnID
	}
	stem, err := providerInvocationJournalStemForSnapshot(controllerPath, s, admission.Invocation, admission.AgentTurn)
	if err != nil {
		return entry, true
	}
	_, gatewayPath := openCodeStemPaths(controllerPath, stem)
	runtimePath, _ := openCodeStemPaths(controllerPath, stem)
	if runtimeEvents, err := journal.Read(runtimePath); err == nil && len(runtimeEvents) > 0 {
		entry.JournalPresent = true
	}
	if gatewayEvents, err := journal.Read(gatewayPath); err == nil && len(gatewayEvents) > 0 {
		if gateway, err := providergateway.Inspect(gatewayPath); err == nil && gateway.Binding != nil {
			if expected, err := resolveProviderRoutingForSnapshot(s, admission.Invocation, 1); err == nil && sameCanonical(expected.Gateway, *gateway.Binding) {
				calls, receipts, pending := openCodeGatewayCounts(gateway)
				entry.GatewayCalls, entry.GatewayReceipts, entry.GatewayPending = &calls, &receipts, &pending
			}
		}
	}
	return entry, true
}

type openCodeOrderedReceipt struct {
	receipt providerDispatchReceipt
	index   int
	order   int
}

// collectOpenCodeUsage exposes admitted OpenCode invocations in actual
// controller admission order using only read-only journal APIs. Legacy
// receipts without AgentDispatch order by their receipt event position,
// preserving deterministic controller order. It writes no journals and never
// synthesizes totals for unmatched work.
func collectOpenCodeUsage(controllerPath string, s Snapshot, events []journal.Event) ([]OpenCodeUsageEntry, error) {
	return collectOpenCodeUsageWithScheduler(controllerPath, "", s, events)
}

// collectOpenCodeUsageWithScheduler is collectOpenCodeUsage with an exact
// scheduler journal binding threaded to composite verification. V1/v2 turns
// never read the scheduler; v3 turns fail safely without one.
func collectOpenCodeUsageWithScheduler(controllerPath, schedulerPath string, s Snapshot, events []journal.Event) ([]OpenCodeUsageEntry, error) {
	admissionOrder := map[string]int{}
	for index, event := range events {
		if event.Kind != "agent.dispatch-admitted" {
			continue
		}
		var admission AgentDispatchAdmission
		if err := canonical.Decode(event.Payload, &admission); err != nil {
			return nil, errors.New("opencode admission changed")
		}
		if _, duplicate := admissionOrder[admission.Invocation.ID]; duplicate {
			return nil, errors.New("opencode admission changed")
		}
		admissionOrder[admission.Invocation.ID] = index
	}
	ordered := []openCodeOrderedReceipt{}
	// Retain every replay-admitted historical planner OpenCode completion.
	// A planning semantic correction clears the latest PlannerProvider
	// projection, so gating on the final snapshot would drop predecessor
	// receipts. Each planning receipt is verified at its exact historical
	// prefix through authoritativeInvocationForReceipt; ledger completion is
	// provider/runtime completion, never semantic plan acceptance.
	for index, event := range events {
		if event.Kind != "planning.provider-observed" {
			continue
		}
		var receipt providerDispatchReceipt
		if err := canonical.Decode(event.Payload, &receipt); err != nil {
			return nil, errors.New("opencode receipt changed")
		}
		if receipt.Result.Requested.Runtime != "opencode-http" {
			continue
		}
		order, ok := admissionOrder[receipt.InvocationID]
		if !ok {
			order = index
		}
		ordered = append(ordered, openCodeOrderedReceipt{receipt: receipt, index: index, order: order})
	}
	if s.PlannerProvider != nil && s.PlannerProvider.Result.Requested.Runtime == "opencode-http" {
		found := false
		for _, item := range ordered {
			if item.receipt.InvocationID == s.PlannerProvider.InvocationID && sameCanonical(item.receipt, *s.PlannerProvider) {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("opencode receipt changed")
		}
	}
	for index, event := range events {
		if event.Kind != "role.provider-observed" {
			continue
		}
		var receipt providerDispatchReceipt
		if err := canonical.Decode(event.Payload, &receipt); err != nil {
			return nil, errors.New("opencode receipt changed")
		}
		if receipt.Result.Requested.Runtime != "opencode-http" {
			continue
		}
		stored, ok := s.ProviderRuntime[receipt.InvocationID]
		if !ok || !sameCanonical(receipt, stored) {
			return nil, errors.New("opencode receipt changed")
		}
		order, ok := admissionOrder[receipt.InvocationID]
		if !ok {
			order = index
		}
		ordered = append(ordered, openCodeOrderedReceipt{receipt: receipt, index: index, order: order})
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].order != ordered[j].order {
			return ordered[i].order < ordered[j].order
		}
		return ordered[i].index < ordered[j].index
	})
	seenReceipts := map[string]bool{}
	for _, item := range ordered {
		if seenReceipts[item.receipt.InvocationID] {
			return nil, errors.New("opencode receipt changed")
		}
		seenReceipts[item.receipt.InvocationID] = true
	}
	entries := []OpenCodeUsageEntry{}
	seen := map[string]bool{}
	type pendingWithOrder struct {
		entry OpenCodeUsageEntry
		order int
	}
	pending := []pendingWithOrder{}
	for _, item := range ordered {
		if seen[item.receipt.InvocationID] {
			return nil, errors.New("opencode receipt changed")
		}
		seen[item.receipt.InvocationID] = true
		entry, err := verifyOpenCodeReceiptWithScheduler(controllerPath, schedulerPath, s, events, item.index, item.receipt)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	// Re-sort verified completed entries by admission order already applied;
	// pending admissions follow in their own admission order. Merge both by
	// admission position to return actual controller admission order.
	for index, event := range events {
		if event.Kind != "agent.dispatch-admitted" {
			continue
		}
		var admission AgentDispatchAdmission
		if err := canonical.Decode(event.Payload, &admission); err != nil {
			return nil, errors.New("opencode admission changed")
		}
		if seen[admission.Invocation.ID] {
			continue
		}
		entry, ok := pendingOpenCodeEntry(controllerPath, s, admission)
		if !ok {
			continue
		}
		seen[admission.Invocation.ID] = true
		pending = append(pending, pendingWithOrder{entry: entry, order: index})
	}
	if len(pending) > 0 {
		merged := append([]openCodeOrderedEntry{}, toOrderedEntries(entries, ordered)...)
		for _, item := range pending {
			merged = append(merged, openCodeOrderedEntry{entry: item.entry, order: item.order})
		}
		sort.SliceStable(merged, func(i, j int) bool { return merged[i].order < merged[j].order })
		entries = entries[:0]
		for _, item := range merged {
			entries = append(entries, item.entry)
		}
	}
	return entries, nil
}

type openCodeOrderedEntry struct {
	entry OpenCodeUsageEntry
	order int
}

func toOrderedEntries(entries []OpenCodeUsageEntry, ordered []openCodeOrderedReceipt) []openCodeOrderedEntry {
	byID := map[string]int{}
	for _, item := range ordered {
		byID[item.receipt.InvocationID] = item.order
	}
	result := make([]openCodeOrderedEntry, 0, len(entries))
	for _, entry := range entries {
		order, ok := byID[entry.InvocationID]
		if !ok {
			order = 1 << 30
		}
		result = append(result, openCodeOrderedEntry{entry: entry, order: order})
	}
	return result
}
