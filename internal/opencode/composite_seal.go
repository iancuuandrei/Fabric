package opencode

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/toolreceipts"
)

// CompositeToolTurnSealIntent is the durable boundary before teardown of one
// provider-backed turn served by the recorder-owned composite MCP listener.
type CompositeToolTurnSealIntent struct {
	Version                  int                             `json:"version"`
	ID                       string                          `json:"id"`
	SealPath                 string                          `json:"seal_path"`
	Expected                 SynchronousToolTurnSealExpected `json:"expected"`
	Dispatch                 CompositeDispatchIntent         `json:"dispatch"`
	ObservationSHA256        string                          `json:"observation_sha256"`
	TranscriptSHA256         string                          `json:"transcript_sha256"`
	OpenBrokerStateID        string                          `json:"open_broker_state_id"`
	ReceiptBindingID         string                          `json:"receipt_binding_id"`
	OpenReceiptJournalHead   string                          `json:"open_receipt_journal_head"`
	OpenReceiptStateSHA256   string                          `json:"open_receipt_state_sha256"`
	ToolsConfigurationSHA256 string                          `json:"tools_configuration_sha256"`
	MCPStatusSHA256          string                          `json:"mcp_status_sha256"`
	ProcessID                int                             `json:"process_id"`
	OpenCodeEndpoint         string                          `json:"opencode_endpoint"`
	MCPEndpoint              string                          `json:"mcp_endpoint"`
}

// CompositeToolTurnTerminalReceipt proves that the exact composite listener
// and provider proxy stopped, the owned root was reaped, and the broker closed.
type CompositeToolTurnTerminalReceipt struct {
	Version                  int    `json:"version"`
	IntentID                 string `json:"intent_id"`
	InvocationID             string `json:"invocation_id"`
	ObservationSHA256        string `json:"observation_sha256"`
	TranscriptSHA256         string `json:"transcript_sha256"`
	OpenBrokerStateID        string `json:"open_broker_state_id"`
	ClosedBrokerStateID      string `json:"closed_broker_state_id"`
	ReceiptBindingID         string `json:"receipt_binding_id"`
	ReceiptJournalHead       string `json:"receipt_journal_head"`
	ReceiptStateSHA256       string `json:"receipt_state_sha256"`
	ToolsConfigurationSHA256 string `json:"tools_configuration_sha256"`
	MCPStatusSHA256          string `json:"mcp_status_sha256"`
	ProcessID                int    `json:"process_id"`
	MCPHandlersStopped       bool   `json:"mcp_handlers_stopped"`
	ProviderProxyStopped     bool   `json:"provider_proxy_stopped"`
	RootProcessReaped        bool   `json:"root_process_reaped"`
	BrokerClosed             bool   `json:"broker_closed"`
	ProviderProcessSHA256    string `json:"provider_process_sha256"`
	ProviderProxySHA256      string `json:"provider_proxy_sha256"`
}

type compositeToolTurnSealState struct {
	Intent  *CompositeToolTurnSealIntent
	Receipt *CompositeToolTurnTerminalReceipt
}

type compositeToolTurnSealSnapshot struct {
	Observation CompositeToolTurnObservation
	Broker      contextbroker.State
	ReceiptHead string
	ReceiptID   string
	Tools       ToolsConfigurationReceipt
	Status      MCPStatus
}

// SealProviderCompositeToolTurn seals one already returned composite turn.
// The caller must supply a deadline and every exact owned runtime handle.
func SealProviderCompositeToolTurn(ctx context.Context, sealPath string, dispatch CompositeDispatchIntent, expected SynchronousToolTurnSealExpected, toolsSpec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker, proxy ProviderProxyLifecycle, capability string, verify CompositeBackendVerifier) (CompositeToolTurnTerminalReceipt, error) {
	var zero CompositeToolTurnTerminalReceipt
	expected = snapshotSynchronousToolTurnSealExpected(expected)
	dispatch.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), dispatch.Receipts.Tools...)
	toolsSpec.ToolNames = append([]string(nil), toolsSpec.ToolNames...)
	if ctx == nil || verify == nil || proxy == nil || expected.Provider == nil {
		return zero, errors.New("complete provider composite seal ownership required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return zero, errors.New("provider composite seal deadline required")
	}
	if err := validateCompositeSealPaths(sealPath, dispatch); err != nil {
		return zero, err
	}
	events, err := journal.Read(sealPath)
	if err != nil || len(events) != 0 {
		return zero, errors.Join(errors.New("provider composite seal already attempted"), err)
	}
	open, stored, pid, err := validateCompositeToolTurnSealLive(dispatch, expected, toolsSpec, client, process, running, broker, verify)
	if err != nil {
		return zero, err
	}
	if err := validateProviderSealLive(process, proxy, capability, *expected.Provider, expected); err != nil {
		return zero, err
	}
	first, err := readCompositeToolTurnSealSnapshot(ctx, dispatch, expected, toolsSpec, client, open, verify)
	if err != nil {
		return zero, err
	}
	if !equalCanonical(first.Observation, stored) {
		return zero, errors.New("live composite turn differs from durable observation")
	}
	timer := time.NewTimer(synchronousToolTurnSealQuietInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return zero, errors.Join(errors.New("provider composite seal quiet interval incomplete"), ctx.Err())
	case <-timer.C:
	}
	second, err := readCompositeToolTurnSealSnapshot(ctx, dispatch, expected, toolsSpec, client, open, verify)
	if err != nil {
		return zero, err
	}
	if !equalCanonical(first, second) {
		return zero, errors.New("provider composite turn changed during quiet interval")
	}
	observationHash, err := canonical.Hash("harness.opencode-composite-seal-observation.v1", second.Observation)
	if err != nil {
		return zero, err
	}
	intent := CompositeToolTurnSealIntent{
		Version: 1, SealPath: sealPath, Expected: expected, Dispatch: dispatch,
		ObservationSHA256: observationHash, TranscriptSHA256: second.Observation.TranscriptSHA256,
		ReceiptBindingID:       dispatch.Receipts.BindingID,
		OpenReceiptJournalHead: second.ReceiptHead, OpenReceiptStateSHA256: second.ReceiptID,
		ToolsConfigurationSHA256: second.Tools.SHA256, MCPStatusSHA256: second.Status.SHA256,
		ProcessID: pid, OpenCodeEndpoint: client.base, MCPEndpoint: running.URL(),
	}
	intent.OpenBrokerStateID, err = canonical.Hash("harness.opencode-tool-turn-open-broker.v1", open)
	if err != nil {
		return zero, err
	}
	intent.ID, err = compositeToolTurnSealIntentID(intent)
	if err != nil {
		return zero, err
	}
	if err := appendCompositeToolTurnSeal(sealPath, "opencode.composite-tool-turn-seal-intent.v1", intent); err != nil {
		return zero, err
	}
	sealed := false
	defer func() {
		if !sealed {
			bestEffortUnsealedCleanup(client, process, running, &providerSealLive{proxy: proxy, capability: capability})
		}
	}()
	if err := running.Close(ctx); err != nil {
		return zero, errors.Join(errors.New("composite listener shutdown failed; turn remains unsealed"), err)
	}
	if err := running.Wait(); err != nil {
		return zero, errors.Join(errors.New("composite listener exit failed; turn remains unsealed"), err)
	}
	if err := requireExactCompositeOpenState(dispatch, open, second.ReceiptHead, second.ReceiptID); err != nil {
		return zero, errors.Join(errors.New("composite journals changed during listener shutdown; turn remains unsealed"), err)
	}
	if err := proxy.Close(ctx); err != nil {
		return zero, errors.Join(errors.New("provider proxy shutdown failed; turn remains unsealed"), err)
	}
	if err := proxy.Wait(); err != nil {
		return zero, errors.Join(errors.New("provider proxy exit failed; turn remains unsealed"), err)
	}
	select {
	case <-process.Done():
		return zero, errors.Join(errors.New("OpenCode root exited before owned shutdown; turn remains unsealed"), process.Wait())
	default:
	}
	closeErr := process.CloseContext(ctx)
	if errors.Is(closeErr, context.Canceled) || errors.Is(closeErr, context.DeadlineExceeded) {
		return zero, errors.Join(errors.New("OpenCode root process reap deadline exhausted; turn remains unsealed"), closeErr)
	}
	select {
	case <-process.Done():
	default:
		return zero, errors.Join(errors.New("OpenCode root process was not reaped; turn remains unsealed"), closeErr)
	}
	client.Close()
	if err := requireExactCompositeOpenState(dispatch, open, second.ReceiptHead, second.ReceiptID); err != nil {
		return zero, errors.Join(errors.New("composite journals changed during process shutdown; turn remains unsealed"), err)
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(errors.New("seal deadline exhausted before broker closure; turn remains unsealed"), err)
	}
	if err := broker.Close(); err != nil {
		return zero, errors.Join(errors.New("broker closure failed; turn remains unsealed"), err)
	}
	closed, err := contextbroker.Inspect(dispatch.BrokerPath)
	if err != nil {
		return zero, err
	}
	wantClosed := open
	wantClosed.Closed = true
	if !equalCanonical(closed, wantClosed) {
		return zero, errors.New("closed broker state mismatch; turn remains unsealed")
	}
	if err := requireExactReceiptState(dispatch.ReceiptPath, dispatch.Receipts, second.ReceiptHead, second.ReceiptID); err != nil {
		return zero, errors.Join(errors.New("receipt journal changed during teardown; turn remains unsealed"), err)
	}
	closedID, err := canonical.Hash("harness.opencode-composite-sealed-broker.v1", closed)
	if err != nil {
		return zero, err
	}
	receipt := CompositeToolTurnTerminalReceipt{
		Version: 1, IntentID: intent.ID, InvocationID: intent.Dispatch.Turn.Invocation.ID, ObservationSHA256: intent.ObservationSHA256,
		TranscriptSHA256: intent.TranscriptSHA256, OpenBrokerStateID: intent.OpenBrokerStateID,
		ClosedBrokerStateID: closedID, ReceiptBindingID: intent.ReceiptBindingID,
		ReceiptJournalHead: intent.OpenReceiptJournalHead, ReceiptStateSHA256: intent.OpenReceiptStateSHA256,
		ToolsConfigurationSHA256: intent.ToolsConfigurationSHA256, MCPStatusSHA256: intent.MCPStatusSHA256,
		ProcessID: intent.ProcessID, MCPHandlersStopped: true, ProviderProxyStopped: true,
		RootProcessReaped: true, BrokerClosed: true,
		ProviderProcessSHA256: expected.Provider.Process.SHA256, ProviderProxySHA256: expected.Provider.Proxy.SHA256,
	}
	if err := appendCompositeToolTurnSeal(sealPath, "opencode.composite-tool-turn-sealed.v1", receipt); err != nil {
		return zero, err
	}
	sealed = true
	return receipt, nil
}

func validateCompositeToolTurnSealLive(dispatch CompositeDispatchIntent, expected SynchronousToolTurnSealExpected, spec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker, verify CompositeBackendVerifier) (contextbroker.State, CompositeToolTurnObservation, int, error) {
	var empty contextbroker.State
	if !equalCanonical(expected.Dispatch, dispatch.Turn) || expected.Provider == nil || expected.Session.Validate() != nil || !equalSessionAndToolDispatch(expected.Session.Session, expected.Dispatch) {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("composite session and dispatch differ")
	}
	normalized, err := normalizeToolsConfiguration(spec)
	if err != nil || expected.Tools.MCPServer != ToolsMCPServerName || expected.Tools.Endpoint != normalized.endpoint || expected.Tools.TimeoutMillis != normalized.timeoutMillis || !slices.Equal(expected.Tools.ToolIDs, normalized.toolIDs) || !slices.Equal(expected.Tools.ToolIDs, mustToolPermissionIDs(expected.Session.ToolNames)) {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("composite session and host configuration differ")
	}
	if client == nil || client.http == nil || process == nil || process.command == nil || process.command.Process == nil || process.done == nil || process.cancel == nil || running == nil || broker == nil {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("incomplete composite lifecycle ownership")
	}
	select {
	case <-process.Done():
		return empty, CompositeToolTurnObservation{}, 0, errors.New("OpenCode root process already exited")
	default:
	}
	if broker.JournalPath() != dispatch.BrokerPath {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("foreign composite broker pointer")
	}
	open, err := contextbroker.Inspect(dispatch.BrokerPath)
	if err != nil || open.Closed || open.Pending != nil || validateSynchronousToolIntent(expected.Dispatch, open, false) != nil {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("context broker unavailable for composite sealing")
	}
	stored, err := RecoverCompositeToolTurn(dispatch.DispatchPath, dispatch, verify)
	if err != nil {
		return empty, CompositeToolTurnObservation{}, 0, errors.Join(errors.New("composite dispatch lacks exact returned observation"), err)
	}
	if err := running.ValidateRecorderOwner(broker, spec.Bearer, dispatch.ReceiptPath, dispatch.Receipts); err != nil {
		return empty, CompositeToolTurnObservation{}, 0, err
	}
	if running.URL() != spec.Endpoint || running.CatalogHash() != expected.Session.CatalogSHA256 || running.CatalogHash() != dispatch.Receipts.CatalogSHA256 {
		return empty, CompositeToolTurnObservation{}, 0, errors.New("foreign composite MCP listener identity")
	}
	if _, _, err := inspectTerminalReceiptState(dispatch.ReceiptPath, dispatch.Receipts); err != nil {
		return empty, CompositeToolTurnObservation{}, 0, err
	}
	pid, err := validateSynchronousToolProcessIdentity(client, process, expected, spec)
	return open, stored, pid, err
}

func readCompositeToolTurnSealSnapshot(ctx context.Context, dispatch CompositeDispatchIntent, expected SynchronousToolTurnSealExpected, spec ToolsConfigurationSpec, client *Client, want contextbroker.State, verify CompositeBackendVerifier) (compositeToolTurnSealSnapshot, error) {
	var result compositeToolTurnSealSnapshot
	before, err := contextbroker.Inspect(dispatch.BrokerPath)
	if err != nil || !equalCanonical(before, want) {
		return result, errors.New("broker changed before composite live read")
	}
	beforeHead, beforeID, err := inspectTerminalReceiptState(dispatch.ReceiptPath, dispatch.Receipts)
	if err != nil {
		return result, err
	}
	result.Observation, err = client.ReadCompositeToolTurnWithRuntimeMetadata(ctx, expected.Dispatch.Dispatch.Binding, expected.Dispatch.Dispatch.Text, dispatch.ReceiptPath, dispatch.Receipts, verify, expected.RuntimeMetadata)
	if err != nil {
		return result, err
	}
	result.Tools, err = client.ReadToolsConfigurationInDirectory(ctx, expected.Dispatch.Dispatch.Binding.Directory, spec)
	if err != nil || !equalCanonical(result.Tools, expected.Tools) {
		return result, errors.New("live tools configuration differs from composite admission")
	}
	result.Status, err = client.ReadMCPStatusInDirectory(ctx, expected.Dispatch.Dispatch.Binding.Directory)
	if err != nil {
		return result, err
	}
	afterHead, afterID, err := inspectTerminalReceiptState(dispatch.ReceiptPath, dispatch.Receipts)
	if err != nil || beforeHead != afterHead || beforeID != afterID || result.Observation.ReceiptJournalHead != afterHead || result.Observation.ReceiptStateSHA256 != afterID {
		return result, errors.New("receipt journal changed during composite live read")
	}
	after, err := contextbroker.Inspect(dispatch.BrokerPath)
	if err != nil || !equalCanonical(after, want) {
		return result, errors.New("broker changed during composite live read")
	}
	result.Broker, result.ReceiptHead, result.ReceiptID = want, afterHead, afterID
	return result, nil
}

func inspectTerminalReceiptState(path string, binding toolreceipts.Binding) (string, string, error) {
	state, head, err := toolreceipts.InspectWithHead(path)
	if err != nil || state.Binding == nil || !equalCanonical(*state.Binding, binding) {
		return "", "", errors.Join(errors.New("composite receipt binding changed"), err)
	}
	for _, call := range state.Calls {
		if call.Receipt == nil {
			return "", "", errors.New("composite receipt journal contains unresolved call")
		}
	}
	id, err := canonical.Hash("harness.opencode-composite-tool-receipt-state.v1", state)
	if err != nil {
		return "", "", err
	}
	return head, id, nil
}

func requireExactReceiptState(path string, binding toolreceipts.Binding, head, id string) error {
	gotHead, gotID, err := inspectTerminalReceiptState(path, binding)
	if err != nil || gotHead != head || gotID != id {
		return errors.New("composite receipt state mismatch")
	}
	return nil
}

func requireExactCompositeOpenState(dispatch CompositeDispatchIntent, broker contextbroker.State, head, id string) error {
	if err := requireExactBrokerState(dispatch.BrokerPath, broker); err != nil {
		return err
	}
	return requireExactReceiptState(dispatch.ReceiptPath, dispatch.Receipts, head, id)
}

func validateCompositeSealPaths(sealPath string, dispatch CompositeDispatchIntent) error {
	paths := []string{sealPath, dispatch.DispatchPath, dispatch.BrokerPath, dispatch.ReceiptPath}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("invalid composite seal journal path")
		}
	}
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			if strings.EqualFold(paths[i], paths[j]) {
				return errors.New("composite seal journal paths must be distinct")
			}
		}
	}
	return nil
}

func compositeToolTurnSealIntentID(intent CompositeToolTurnSealIntent) (string, error) {
	intent.ID = ""
	return canonical.Hash("harness.opencode-composite-tool-turn-seal-intent.v1", intent)
}

func validateCompositeToolTurnSealIntent(intent CompositeToolTurnSealIntent) error {
	if intent.Version != 1 || intent.Expected.Provider == nil || !equalCanonical(intent.Expected.Dispatch, intent.Dispatch.Turn) || safepath.RequireDigest(intent.ObservationSHA256) != nil || safepath.RequireDigest(intent.TranscriptSHA256) != nil || safepath.RequireDigest(intent.OpenBrokerStateID) != nil || safepath.RequireDigest(intent.ReceiptBindingID) != nil || safepath.RequireDigest(intent.OpenReceiptJournalHead) != nil || safepath.RequireDigest(intent.OpenReceiptStateSHA256) != nil || safepath.RequireDigest(intent.ToolsConfigurationSHA256) != nil || safepath.RequireDigest(intent.MCPStatusSHA256) != nil || intent.ProcessID <= 0 {
		return errors.New("invalid composite tool turn seal intent")
	}
	receiptBindingID, receiptErr := intent.Dispatch.Receipts.ID()
	if validateCompositeSealPaths(intent.SealPath, intent.Dispatch) != nil || intent.Dispatch.Version != 1 || receiptErr != nil || receiptBindingID != intent.Dispatch.Receipts.BindingID || intent.ReceiptBindingID != intent.Dispatch.Receipts.BindingID || intent.Dispatch.Receipts.InvocationID != intent.Dispatch.Turn.Invocation.ID || intent.Expected.Session.CatalogSHA256 != intent.Dispatch.Receipts.CatalogSHA256 || intent.ToolsConfigurationSHA256 != intent.Expected.Tools.SHA256 || intent.OpenReceiptStateSHA256 == intent.OpenBrokerStateID || !validExactLoopbackEndpoint(intent.OpenCodeEndpoint, "") || !validExactLoopbackEndpoint(intent.MCPEndpoint, "/mcp") {
		return errors.New("invalid composite tool turn seal binding")
	}
	base := SynchronousToolTurnSealIntent{Version: 1, Expected: intent.Expected, ObservationSHA256: intent.ObservationSHA256, TranscriptSHA256: intent.TranscriptSHA256, OpenBrokerStateID: intent.OpenBrokerStateID, ToolsConfigurationSHA256: intent.ToolsConfigurationSHA256, MCPStatusSHA256: intent.MCPStatusSHA256, ProcessID: intent.ProcessID, OpenCodeEndpoint: intent.OpenCodeEndpoint, MCPEndpoint: intent.MCPEndpoint}
	base.ID, _ = synchronousToolTurnSealIntentID(base)
	if validateSynchronousToolTurnSealIntent(base) != nil {
		return errors.New("invalid composite tool turn base binding")
	}
	id, err := compositeToolTurnSealIntentID(intent)
	if err != nil || id != intent.ID || safepath.RequireDigest(intent.ID) != nil {
		return errors.New("invalid composite tool turn seal identity")
	}
	return nil
}

func validateCompositeTerminalReceipt(receipt CompositeToolTurnTerminalReceipt, intent CompositeToolTurnSealIntent) error {
	if receipt.Version != 1 || receipt.IntentID != intent.ID || receipt.InvocationID != intent.Dispatch.Turn.Invocation.ID || receipt.ObservationSHA256 != intent.ObservationSHA256 || receipt.TranscriptSHA256 != intent.TranscriptSHA256 || receipt.OpenBrokerStateID != intent.OpenBrokerStateID || receipt.ReceiptBindingID != intent.ReceiptBindingID || receipt.ReceiptJournalHead != intent.OpenReceiptJournalHead || receipt.ReceiptStateSHA256 != intent.OpenReceiptStateSHA256 || receipt.ToolsConfigurationSHA256 != intent.ToolsConfigurationSHA256 || receipt.MCPStatusSHA256 != intent.MCPStatusSHA256 || receipt.ProcessID != intent.ProcessID || safepath.RequireDigest(receipt.ClosedBrokerStateID) != nil || !receipt.MCPHandlersStopped || !receipt.ProviderProxyStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed || receipt.ProviderProcessSHA256 != intent.Expected.Provider.Process.SHA256 || receipt.ProviderProxySHA256 != intent.Expected.Provider.Proxy.SHA256 {
		return errors.New("composite terminal receipt differs from seal intent")
	}
	return nil
}

func replayCompositeToolTurnSeal(events []journal.Event) (compositeToolTurnSealState, error) {
	state := compositeToolTurnSealState{}
	for _, event := range events {
		switch event.Kind {
		case "opencode.composite-tool-turn-seal-intent.v1":
			if state.Intent != nil {
				return compositeToolTurnSealState{}, errors.New("duplicate composite seal intent")
			}
			var intent CompositeToolTurnSealIntent
			if canonical.Decode(event.Payload, &intent) != nil || validateCompositeToolTurnSealIntent(intent) != nil {
				return compositeToolTurnSealState{}, errors.New("invalid composite seal intent")
			}
			state.Intent = &intent
		case "opencode.composite-tool-turn-sealed.v1":
			if state.Intent == nil || state.Receipt != nil {
				return compositeToolTurnSealState{}, errors.New("invalid composite terminal receipt")
			}
			var receipt CompositeToolTurnTerminalReceipt
			if canonical.Decode(event.Payload, &receipt) != nil || validateCompositeTerminalReceipt(receipt, *state.Intent) != nil {
				return compositeToolTurnSealState{}, errors.New("invalid composite terminal receipt")
			}
			state.Receipt = &receipt
		default:
			return compositeToolTurnSealState{}, errors.New("unknown composite seal event")
		}
	}
	return state, nil
}

func appendCompositeToolTurnSeal(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		_, err := replayCompositeToolTurnSeal(events)
		return err
	})
	return err
}

// RecoverCompositeToolTurnSeal is journal-only and performs no host lifecycle
// action. Only identities known before dispatch are caller supplied; the full
// post-effect seal intent is recovered from its journal.
func RecoverCompositeToolTurnSeal(path string, dispatch CompositeDispatchIntent, expected SynchronousToolTurnSealExpected, verify CompositeBackendVerifier) (CompositeToolTurnObservation, CompositeToolTurnTerminalReceipt, error) {
	var emptyObservation CompositeToolTurnObservation
	var emptyReceipt CompositeToolTurnTerminalReceipt
	dispatch.Receipts.Tools = append([]toolreceipts.ToolOwner(nil), dispatch.Receipts.Tools...)
	expected = snapshotSynchronousToolTurnSealExpected(expected)
	events, err := journal.Read(path)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	state, err := replayCompositeToolTurnSeal(events)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	if state.Intent == nil || state.Intent.SealPath != path || !equalCanonical(state.Intent.Dispatch, dispatch) || !equalCanonical(state.Intent.Expected, expected) {
		return emptyObservation, emptyReceipt, errors.New("composite tool turn seal intent mismatch")
	}
	if state.Receipt == nil {
		return emptyObservation, emptyReceipt, errors.New("composite tool turn remains unsealed")
	}
	intent := *state.Intent
	closed, err := contextbroker.Inspect(intent.Dispatch.BrokerPath)
	if err != nil || !closed.Closed {
		return emptyObservation, emptyReceipt, errors.New("sealed composite broker is not durably closed")
	}
	closedID, err := canonical.Hash("harness.opencode-composite-sealed-broker.v1", closed)
	if err != nil || closedID != state.Receipt.ClosedBrokerStateID {
		return emptyObservation, emptyReceipt, errors.New("sealed composite broker identity mismatch")
	}
	open := closed
	open.Closed = false
	openID, err := canonical.Hash("harness.opencode-tool-turn-open-broker.v1", open)
	if err != nil || openID != intent.OpenBrokerStateID {
		return emptyObservation, emptyReceipt, errors.New("sealed composite open broker identity mismatch")
	}
	if err := requireExactReceiptState(intent.Dispatch.ReceiptPath, intent.Dispatch.Receipts, intent.OpenReceiptJournalHead, intent.OpenReceiptStateSHA256); err != nil {
		return emptyObservation, emptyReceipt, err
	}
	observation, err := RecoverCompositeToolTurn(intent.Dispatch.DispatchPath, intent.Dispatch, verify)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	observationID, err := canonical.Hash("harness.opencode-composite-seal-observation.v1", observation)
	if err != nil || observationID != intent.ObservationSHA256 || observation.TranscriptSHA256 != intent.TranscriptSHA256 || observation.ReceiptJournalHead != intent.OpenReceiptJournalHead || observation.ReceiptStateSHA256 != intent.OpenReceiptStateSHA256 {
		return emptyObservation, emptyReceipt, errors.New("sealed composite observation mismatch")
	}
	return observation, *state.Receipt, nil
}
