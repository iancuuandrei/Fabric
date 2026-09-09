package opencode

import (
	"context"
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const synchronousToolTurnSealQuietInterval = 100 * time.Millisecond
const synchronousToolTurnCleanupTimeout = 2 * time.Second

// SynchronousToolTurnSealExpected binds the already durable synchronous turn
// to its exact tool session and admitted, credential-free host configuration.
type SynchronousToolTurnSealExpected struct {
	Dispatch         SynchronousToolDispatchIntent `json:"dispatch"`
	Session          ToolSessionBinding            `json:"session"`
	Tools            ToolsConfigurationReceipt     `json:"tools"`
	ExecutableSHA256 string                        `json:"executable_sha256"`
	// HostRoot is the private OpenCode state root. WorkingDirectory is the
	// admitted repository workspace. Empty WorkingDirectory is the legacy v1
	// representation where both identities equal HostRoot.
	HostRoot         string `json:"host_root"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	// MaxOutputTokens is the provider wire cap. RuntimeOutputTokens is the
	// text-output cap configured in OpenCode after protocol-specific reasoning
	// reservation. Legacy non-provider seals may leave RuntimeOutputTokens zero.
	MaxOutputTokens     int64                         `json:"max_output_tokens"`
	RuntimeOutputTokens int64                         `json:"runtime_output_tokens,omitempty"`
	Diagnostic          bool                          `json:"diagnostic"`
	Provider            *ProviderToolTurnSealExpected `json:"provider,omitempty"`
	RuntimeMetadata     *RuntimeMetadataExpectation   `json:"runtime_metadata,omitempty"`
}

// SynchronousToolTurnSealIntent is written only after two equal live reads.
// It deliberately excludes the MCP bearer and OpenCode credentials.
type SynchronousToolTurnSealIntent struct {
	Version                  int                             `json:"version"`
	ID                       string                          `json:"id"`
	Expected                 SynchronousToolTurnSealExpected `json:"expected"`
	ObservationSHA256        string                          `json:"observation_sha256"`
	TranscriptSHA256         string                          `json:"transcript_sha256"`
	OpenBrokerStateID        string                          `json:"open_broker_state_id"`
	ToolsConfigurationSHA256 string                          `json:"tools_configuration_sha256"`
	MCPStatusSHA256          string                          `json:"mcp_status_sha256"`
	ProcessID                int                             `json:"process_id"`
	OpenCodeEndpoint         string                          `json:"opencode_endpoint"`
	MCPEndpoint              string                          `json:"mcp_endpoint"`
}

// ToolTurnTerminalReceipt is durable evidence that the owned listener stopped,
// the owned OpenCode root process was reaped, and the broker was closed. It
// makes no descendant-sandbox or operating-system containment claim.
type ToolTurnTerminalReceipt struct {
	Version                  int    `json:"version"`
	IntentID                 string `json:"intent_id"`
	ObservationSHA256        string `json:"observation_sha256"`
	TranscriptSHA256         string `json:"transcript_sha256"`
	OpenBrokerStateID        string `json:"open_broker_state_id"`
	ClosedBrokerStateID      string `json:"closed_broker_state_id"`
	ToolsConfigurationSHA256 string `json:"tools_configuration_sha256"`
	MCPStatusSHA256          string `json:"mcp_status_sha256"`
	ProcessID                int    `json:"process_id"`
	MCPHandlersStopped       bool   `json:"mcp_handlers_stopped"`
	RootProcessReaped        bool   `json:"root_process_reaped"`
	BrokerClosed             bool   `json:"broker_closed"`
	ProviderProxyStopped     bool   `json:"provider_proxy_stopped,omitempty"`
	ProviderProcessSHA256    string `json:"provider_process_sha256,omitempty"`
	ProviderProxySHA256      string `json:"provider_proxy_sha256,omitempty"`
}

type synchronousToolTurnSealState struct {
	Intent  *SynchronousToolTurnSealIntent
	Receipt *ToolTurnTerminalReceipt
}

type synchronousToolTurnSealSnapshot struct {
	Observation ToolTurnObservation
	Broker      contextbroker.State
	Tools       ToolsConfigurationReceipt
	Status      MCPStatus
}

// SealSynchronousToolTurn terminalizes one returned synchronous tool turn. The
// caller must supply a deadline. Any failure after the seal intent is durable
// remains UNSEALED and this API must not be repeated automatically.
func SealSynchronousToolTurn(ctx context.Context, sealPath, dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected, toolsSpec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker) (ToolTurnTerminalReceipt, error) {
	return sealSynchronousToolTurn(ctx, sealPath, dispatchPath, brokerPath, expected, toolsSpec, client, process, running, broker, nil)
}

func sealSynchronousToolTurn(ctx context.Context, sealPath, dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected, toolsSpec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker, provider *providerSealLive) (ToolTurnTerminalReceipt, error) {
	var zero ToolTurnTerminalReceipt
	expected = snapshotSynchronousToolTurnSealExpected(expected)
	toolsSpec.ToolNames = append([]string(nil), toolsSpec.ToolNames...)
	if ctx == nil {
		return zero, errors.New("synchronous tool turn seal context required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return zero, errors.New("synchronous tool turn seal deadline required")
	}
	events, err := journal.Read(sealPath)
	if err != nil {
		return zero, err
	}
	if len(events) != 0 {
		return zero, errors.New("synchronous tool turn seal already attempted")
	}
	if (expected.Provider == nil) != (provider == nil) {
		return zero, errors.New("provider seal ownership differs from expectation")
	}
	open, stored, pid, err := validateSynchronousToolTurnSealLive(dispatchPath, brokerPath, expected, toolsSpec, client, process, running, broker)
	if err != nil {
		return zero, err
	}
	if provider != nil {
		if err := validateProviderSealLive(process, provider.proxy, provider.capability, *expected.Provider, expected); err != nil {
			return zero, err
		}
	}
	first, err := readSynchronousToolTurnSealSnapshot(ctx, brokerPath, client, expected, toolsSpec, open)
	if err != nil {
		return zero, err
	}
	if !equalCanonical(first.Observation, stored) {
		return zero, errors.New("live tool turn differs from durable synchronous observation")
	}
	timer := time.NewTimer(synchronousToolTurnSealQuietInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return zero, errors.Join(errors.New("synchronous tool turn quiet interval incomplete"), ctx.Err())
	case <-timer.C:
	}
	second, err := readSynchronousToolTurnSealSnapshot(ctx, brokerPath, client, expected, toolsSpec, open)
	if err != nil {
		return zero, err
	}
	if !equalCanonical(first, second) {
		return zero, errors.New("synchronous tool turn changed during quiet interval")
	}
	observationHash, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", second.Observation)
	if err != nil {
		return zero, err
	}
	intent := SynchronousToolTurnSealIntent{
		Version: 1, Expected: expected, ObservationSHA256: observationHash,
		TranscriptSHA256: second.Observation.TranscriptSHA256, OpenBrokerStateID: second.Observation.BrokerStateID,
		ToolsConfigurationSHA256: second.Tools.SHA256, MCPStatusSHA256: second.Status.SHA256,
		ProcessID: pid, OpenCodeEndpoint: client.base, MCPEndpoint: running.URL(),
	}
	intent.ID, err = synchronousToolTurnSealIntentID(intent)
	if err != nil {
		return zero, err
	}
	if err := appendSynchronousToolTurnSeal(sealPath, "opencode.tool-turn-seal-intent", intent); err != nil {
		return zero, err
	}
	sealed := false
	defer func() {
		if !sealed {
			bestEffortUnsealedCleanup(client, process, running, provider)
		}
	}()
	// The synchronous POST has returned and both decoded transcripts end in the
	// same tool-free stop. Shutdown now closes admission, cancels active handlers
	// and waits for HTTP handlers; the following broker comparison detects any
	// call which entered between the last snapshot and that shutdown boundary.
	if err := running.Close(ctx); err != nil {
		return zero, errors.Join(errors.New("synchronous tool listener shutdown failed; turn remains unsealed"), err)
	}
	if err := running.Wait(); err != nil {
		return zero, errors.Join(errors.New("synchronous tool listener exit failed; turn remains unsealed"), err)
	}
	if err := requireExactBrokerState(brokerPath, open); err != nil {
		return zero, errors.Join(errors.New("broker changed during listener shutdown; turn remains unsealed"), err)
	}
	providerStopped := false
	if provider != nil {
		if err := provider.proxy.Close(ctx); err != nil {
			return zero, errors.Join(errors.New("provider proxy shutdown failed; turn remains unsealed"), err)
		}
		if err := provider.proxy.Wait(); err != nil {
			return zero, errors.Join(errors.New("provider proxy exit failed; turn remains unsealed"), err)
		}
		providerStopped = true
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
	if err := requireExactBrokerState(brokerPath, open); err != nil {
		return zero, errors.Join(errors.New("broker changed during process shutdown; turn remains unsealed"), err)
	}
	if err := ctx.Err(); err != nil {
		return zero, errors.Join(errors.New("seal deadline exhausted before broker closure; turn remains unsealed"), err)
	}
	if err := broker.Close(); err != nil {
		return zero, errors.Join(errors.New("broker closure failed; turn remains unsealed"), err)
	}
	closed, err := contextbroker.Inspect(brokerPath)
	if err != nil {
		return zero, err
	}
	wantClosed := open
	wantClosed.Closed = true
	if !equalCanonical(closed, wantClosed) {
		return zero, errors.New("closed broker state mismatch; turn remains unsealed")
	}
	closedID, err := canonical.Hash("harness.opencode-tool-turn-sealed-broker.v1", closed)
	if err != nil {
		return zero, err
	}
	receipt := ToolTurnTerminalReceipt{
		Version: 1, IntentID: intent.ID, ObservationSHA256: intent.ObservationSHA256,
		TranscriptSHA256: intent.TranscriptSHA256, OpenBrokerStateID: intent.OpenBrokerStateID,
		ClosedBrokerStateID: closedID, ToolsConfigurationSHA256: intent.ToolsConfigurationSHA256,
		MCPStatusSHA256: intent.MCPStatusSHA256, ProcessID: intent.ProcessID,
		MCPHandlersStopped: true, RootProcessReaped: true, BrokerClosed: true,
	}
	if expected.Provider != nil {
		receipt.ProviderProxyStopped = providerStopped
		receipt.ProviderProcessSHA256 = expected.Provider.Process.SHA256
		receipt.ProviderProxySHA256 = expected.Provider.Proxy.SHA256
	}
	if err := appendSynchronousToolTurnSeal(sealPath, "opencode.tool-turn-sealed", receipt); err != nil {
		return zero, err
	}
	sealed = true
	return receipt, nil
}

// bestEffortUnsealedCleanup stops admission and tries to settle both listener
// families and reap the owned root under a fresh bounded context. It never
// closes the broker or writes a terminal receipt: either would turn uncertain
// post-intent cleanup into a false success claim.
func bestEffortUnsealedCleanup(client *Client, process *Process, running *contextmcp.OwnedRunning, provider *providerSealLive) {
	ctx, cancel := context.WithTimeout(context.Background(), synchronousToolTurnCleanupTimeout)
	defer cancel()
	// Initiate root cancellation before a listener shutdown can consume the
	// cleanup deadline; the final CloseContext below still waits for reap.
	if process != nil {
		kick, stop := context.WithCancel(context.Background())
		stop()
		_ = process.CloseContext(kick)
	}
	if running != nil {
		_ = running.Close(ctx)
	}
	if provider != nil && provider.proxy != nil {
		_ = provider.proxy.Close(ctx)
	}
	if process != nil {
		_ = process.CloseContext(ctx)
	}
	if running != nil {
		_ = waitLifecycleBounded(ctx, running.Wait)
	}
	if provider != nil && provider.proxy != nil {
		_ = waitLifecycleBounded(ctx, provider.proxy.Wait)
	}
	if client != nil {
		client.Close()
	}
}

func waitLifecycleBounded(ctx context.Context, wait func() error) error {
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecoverSynchronousToolTurnSeal is journal-only. It never reads the host and
// never repeats shutdown. Intent-only history returns an unsealed error.
func RecoverSynchronousToolTurnSeal(path, dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected) (ToolTurnTerminalReceipt, error) {
	_, receipt, err := RecoverSynchronousToolTurnSealEvidence(path, dispatchPath, brokerPath, expected)
	return receipt, err
}

// RecoverSynchronousToolTurnSealEvidence returns the exact stored tool-turn
// observation together with its terminal receipt. It reconstructs the
// pre-close broker state only after validating the closed broker and seal
// receipt, and performs no host read or lifecycle action.
func RecoverSynchronousToolTurnSealEvidence(path, dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected) (ToolTurnObservation, ToolTurnTerminalReceipt, error) {
	var emptyObservation ToolTurnObservation
	var emptyReceipt ToolTurnTerminalReceipt
	expected = snapshotSynchronousToolTurnSealExpected(expected)
	events, err := journal.Read(path)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	state, err := replaySynchronousToolTurnSeal(events)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	if state.Intent == nil || !equalCanonical(state.Intent.Expected, expected) {
		return emptyObservation, emptyReceipt, errors.New("synchronous tool turn seal intent mismatch")
	}
	if state.Receipt == nil {
		return emptyObservation, emptyReceipt, errors.New("synchronous tool turn remains unsealed")
	}
	closed, err := contextbroker.Inspect(brokerPath)
	if err != nil || !closed.Closed {
		return emptyObservation, emptyReceipt, errors.New("sealed context broker is not durably closed")
	}
	closedID, err := canonical.Hash("harness.opencode-tool-turn-sealed-broker.v1", closed)
	if err != nil || closedID != state.Receipt.ClosedBrokerStateID {
		return emptyObservation, emptyReceipt, errors.New("sealed context broker identity mismatch")
	}
	open := closed
	open.Closed = false
	dispatchEvents, err := journal.Read(dispatchPath)
	if err != nil {
		return emptyObservation, emptyReceipt, err
	}
	if err := requireReturnedSynchronousToolPost(dispatchEvents); err != nil {
		return emptyObservation, emptyReceipt, err
	}
	dispatch, err := replaySynchronousToolDispatch(dispatchEvents, open)
	if err != nil || dispatch.Intent == nil || !equalSynchronousToolIntent(*dispatch.Intent, expected.Dispatch) || dispatch.Observation == nil {
		return emptyObservation, emptyReceipt, errors.New("sealed synchronous tool evidence mismatch")
	}
	observationID, err := sealObservationHash(*dispatch.Observation, state.Intent.Expected.Provider == nil, state.Intent.ObservationSHA256)
	if err != nil || observationID != state.Intent.ObservationSHA256 || dispatch.Observation.TranscriptSHA256 != state.Intent.TranscriptSHA256 || dispatch.Observation.BrokerStateID != state.Intent.OpenBrokerStateID {
		return emptyObservation, emptyReceipt, errors.New("sealed synchronous tool observation mismatch")
	}
	return *dispatch.Observation, *state.Receipt, nil
}

// sealObservationHash accepts the former observation projection only while
// replaying a seal that has no provider identity. Provider-backed seals always
// bind the per-generation text digests used by gateway semantic matching.
func sealObservationHash(observation ToolTurnObservation, allowLegacy bool, want string) (string, error) {
	current, err := canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", observation)
	if err != nil || current == want || !allowLegacy {
		return current, err
	}
	return canonical.Hash("harness.opencode-tool-turn-seal-observation.v1", legacyToolTurnObservationV1(observation))
}

func validateSynchronousToolTurnSealLive(dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected, spec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker) (contextbroker.State, ToolTurnObservation, int, error) {
	var empty contextbroker.State
	if err := expected.Session.Validate(); err != nil || !equalSessionAndToolDispatch(expected.Session.Session, expected.Dispatch) || !equalOptionalStructuredOutputExpectation(expected.Session.StructuredOutput, expected.Dispatch.Dispatch.StructuredOutput) {
		return empty, ToolTurnObservation{}, 0, errors.New("tool session and synchronous dispatch differ")
	}
	normalized, err := normalizeToolsConfiguration(spec)
	if err != nil {
		return empty, ToolTurnObservation{}, 0, err
	}
	if expected.Tools.MCPServer != ToolsMCPServerName || expected.Tools.Endpoint != normalized.endpoint || expected.Tools.TimeoutMillis != normalized.timeoutMillis || !slices.Equal(expected.Tools.ToolIDs, normalized.toolIDs) || !slices.Equal(expected.Tools.ToolIDs, mustToolPermissionIDs(expected.Session.ToolNames)) {
		return empty, ToolTurnObservation{}, 0, errors.New("tool session and host configuration differ")
	}
	if client == nil || client.http == nil || process == nil || process.command == nil || process.command.Process == nil || process.done == nil || process.cancel == nil || running == nil || broker == nil {
		return empty, ToolTurnObservation{}, 0, errors.New("incomplete synchronous tool lifecycle ownership")
	}
	select {
	case <-process.Done():
		return empty, ToolTurnObservation{}, 0, errors.New("OpenCode root process already exited")
	default:
	}
	if broker.JournalPath() != brokerPath {
		return empty, ToolTurnObservation{}, 0, errors.New("foreign context broker pointer")
	}
	bindingID, err := broker.BindingID()
	if err != nil || bindingID != expected.Dispatch.BrokerBindingID {
		return empty, ToolTurnObservation{}, 0, errors.New("context broker pointer identity mismatch")
	}
	open, err := contextbroker.Inspect(brokerPath)
	if err != nil || open.Closed || open.Pending != nil || validateSynchronousToolIntent(expected.Dispatch, open, false) != nil {
		return empty, ToolTurnObservation{}, 0, errors.New("context broker unavailable for sealing")
	}
	dispatchEvents, err := journal.Read(dispatchPath)
	if err != nil {
		return empty, ToolTurnObservation{}, 0, err
	}
	if err := requireReturnedSynchronousToolPost(dispatchEvents); err != nil {
		return empty, ToolTurnObservation{}, 0, err
	}
	stored, err := replaySynchronousToolDispatch(dispatchEvents, open)
	if err != nil || stored.Intent == nil || !equalSynchronousToolIntent(*stored.Intent, expected.Dispatch) || stored.Observation == nil {
		return empty, ToolTurnObservation{}, 0, errors.New("synchronous tool dispatch lacks exact stored observation")
	}
	if err := running.ValidateOwner(broker, spec.Bearer); err != nil {
		return empty, ToolTurnObservation{}, 0, err
	}
	if running.URL() != spec.Endpoint || running.CatalogHash() != expected.Session.CatalogSHA256 {
		return empty, ToolTurnObservation{}, 0, errors.New("foreign MCP listener identity")
	}
	pid, err := validateSynchronousToolProcessIdentity(client, process, expected, spec)
	if err != nil {
		return empty, ToolTurnObservation{}, 0, err
	}
	return open, *stored.Observation, pid, nil
}

func readSynchronousToolTurnSealSnapshot(ctx context.Context, brokerPath string, client *Client, expected SynchronousToolTurnSealExpected, spec ToolsConfigurationSpec, want contextbroker.State) (synchronousToolTurnSealSnapshot, error) {
	var result synchronousToolTurnSealSnapshot
	before, err := contextbroker.Inspect(brokerPath)
	if err != nil || !equalCanonical(before, want) {
		return result, errors.New("broker changed before live seal read")
	}
	if expected.Dispatch.Dispatch.StructuredOutput != nil {
		result.Observation, err = client.ReadToolTurnWithStructuredOutputAndRuntimeMetadata(ctx, expected.Dispatch.Dispatch.Binding, expected.Dispatch.Dispatch.Text, want, *expected.Dispatch.Dispatch.StructuredOutput, expected.RuntimeMetadata)
	} else {
		result.Observation, err = client.ReadToolTurnWithRuntimeMetadata(ctx, expected.Dispatch.Dispatch.Binding, expected.Dispatch.Dispatch.Text, want, expected.RuntimeMetadata)
	}
	if err != nil {
		return result, err
	}
	result.Broker = want
	result.Tools, err = client.ReadToolsConfigurationInDirectory(ctx, expected.Dispatch.Dispatch.Binding.Directory, spec)
	if err != nil || !equalCanonical(result.Tools, expected.Tools) {
		return result, errors.New("live tools configuration differs from admitted receipt")
	}
	result.Status, err = client.ReadMCPStatusInDirectory(ctx, expected.Dispatch.Dispatch.Binding.Directory)
	if err != nil {
		return result, err
	}
	after, err := contextbroker.Inspect(brokerPath)
	if err != nil || !equalCanonical(after, want) {
		return result, errors.New("broker changed during live seal read")
	}
	return result, nil
}

func validateSynchronousToolProcessIdentity(client *Client, process *Process, expected SynchronousToolTurnSealExpected, spec ToolsConfigurationSpec) (int, error) {
	identity, ok := process.identity()
	if !ok || identity.Tools == nil || identity.AdmittedTools == nil || process.command == nil || process.command.Process == nil || process.command.Process.Pid <= 0 {
		return 0, errors.New("OpenCode process lacks immutable tools admission identity")
	}
	authSHA256, err := processServerAuthDigest(client.user, client.password)
	if err != nil {
		return 0, err
	}
	bearerSHA256, err := toolsBearerDigest(spec.Bearer)
	if err != nil {
		return 0, err
	}
	normalized, err := normalizeToolsConfiguration(spec)
	if err != nil {
		return 0, err
	}
	tools := identity.Tools
	runtimeOutputTokens := expected.RuntimeOutputTokens
	if runtimeOutputTokens == 0 && expected.Provider == nil {
		runtimeOutputTokens = expected.MaxOutputTokens
	}
	if identity.ExecutableSHA256 != expected.ExecutableSHA256 || identity.Root != expected.HostRoot || identity.WorkingDirectory != synchronousToolTurnWorkingDirectory(expected) || identity.Endpoint != client.base || identity.AuthSHA256 != authSHA256 || identity.MaxOutputTokens != runtimeOutputTokens || identity.Diagnostic != expected.Diagnostic || tools.Endpoint != normalized.endpoint || tools.BearerSHA256 != bearerSHA256 || !slices.Equal(tools.ToolIDs, normalized.toolIDs) || tools.TimeoutMillis != normalized.timeoutMillis || !equalCanonical(*identity.AdmittedTools, expected.Tools) {
		return 0, errors.New("OpenCode process immutable launch or admission identity mismatch")
	}
	return process.command.Process.Pid, nil
}

func equalSessionAndToolDispatch(session SessionBinding, intent SynchronousToolDispatchIntent) bool {
	b := intent.Dispatch.Binding
	return session.IntentID == intent.Invocation.ID && session.Directory == b.Directory && session.Agent == b.Agent && session.Provider == b.Provider && session.Model == b.Model && session.Variant == b.Variant
}

func equalOptionalStructuredOutputExpectation(left, right *StructuredOutputExpectation) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return equalCanonical(*left, *right)
}

func mustToolPermissionIDs(names []string) []string {
	ids, _ := ToolPermissionIDs(names)
	return ids
}

func snapshotSynchronousToolTurnSealExpected(expected SynchronousToolTurnSealExpected) SynchronousToolTurnSealExpected {
	if expected.Dispatch.Dispatch.StructuredOutput != nil {
		copy := *expected.Dispatch.Dispatch.StructuredOutput
		copy.Schema = append([]byte(nil), expected.Dispatch.Dispatch.StructuredOutput.Schema...)
		expected.Dispatch.Dispatch.StructuredOutput = &copy
	}
	expected.Session = snapshotToolSessionBinding(expected.Session)
	expected.Tools.ToolIDs = append([]string(nil), expected.Tools.ToolIDs...)
	expected.Provider = snapshotProviderToolTurnSealExpected(expected.Provider)
	if expected.RuntimeMetadata != nil {
		copy := *expected.RuntimeMetadata
		copy.KnownControllerPaths = append([]string(nil), expected.RuntimeMetadata.KnownControllerPaths...)
		expected.RuntimeMetadata = &copy
	}
	return expected
}

func requireExactBrokerState(path string, want contextbroker.State) error {
	got, err := contextbroker.Inspect(path)
	if err != nil || !equalCanonical(got, want) {
		return errors.New("context broker state mismatch")
	}
	return nil
}

func equalCanonical(left, right any) bool {
	a, aerr := canonical.Bytes(left)
	b, berr := canonical.Bytes(right)
	return aerr == nil && berr == nil && string(a) == string(b)
}

func requireReturnedSynchronousToolPost(events []journal.Event) error {
	for _, event := range events {
		if event.Kind != "opencode.sync-tool-observed" {
			continue
		}
		var record synchronousToolRecord
		if err := canonical.Decode(event.Payload, &record); err != nil || record.Response == "" {
			return errors.New("synchronous tool turn lacks returned POST evidence")
		}
		return nil
	}
	return errors.New("synchronous tool turn lacks returned POST evidence")
}

func synchronousToolTurnSealIntentID(intent SynchronousToolTurnSealIntent) (string, error) {
	intent.ID = ""
	return canonical.Hash("harness.opencode-tool-turn-seal-intent.v1", intent)
}

// synchronousToolTurnWorkingDirectory preserves the v1 seal representation,
// which used HostRoot for both state and workspace identity.
func synchronousToolTurnWorkingDirectory(expected SynchronousToolTurnSealExpected) string {
	if expected.WorkingDirectory != "" {
		return expected.WorkingDirectory
	}
	return expected.HostRoot
}

func validateSynchronousToolTurnSealIntent(intent SynchronousToolTurnSealIntent) error {
	if intent.Version != 1 || safepath.RequireDigest(intent.ObservationSHA256) != nil || safepath.RequireDigest(intent.TranscriptSHA256) != nil || safepath.RequireDigest(intent.OpenBrokerStateID) != nil || safepath.RequireDigest(intent.ToolsConfigurationSHA256) != nil || safepath.RequireDigest(intent.MCPStatusSHA256) != nil || intent.ProcessID <= 0 {
		return errors.New("invalid synchronous tool turn seal intent")
	}
	permissionIDs, permissionErr := intent.Expected.Session.permissionIDs()
	dispatch := intent.Expected.Dispatch
	workingDirectory := synchronousToolTurnWorkingDirectory(intent.Expected)
	expectedDispatch, dispatchErr := DispatchForInvocationWithStructuredOutput(dispatch.Invocation, dispatch.Dispatch.Binding.SessionID, dispatch.Dispatch.Binding.ParentID, dispatch.Dispatch.Binding.Agent, dispatch.Dispatch.Binding.Directory, dispatch.Dispatch.Binding.Root, dispatch.Dispatch.StructuredOutput)
	if err := intent.Expected.Session.Validate(); err != nil || permissionErr != nil || dispatchErr != nil || !equalCanonical(dispatch.Dispatch, expectedDispatch) || !equalSessionAndToolDispatch(intent.Expected.Session.Session, dispatch) || !equalOptionalStructuredOutputExpectation(intent.Expected.Session.StructuredOutput, dispatch.Dispatch.StructuredOutput) || intent.Expected.Tools.AllowStructuredOutput != (dispatch.Dispatch.StructuredOutput != nil) || !equalCanonical(intent.Expected.RuntimeMetadata, dispatch.RuntimeMetadata) || safepath.RequireDigest(intent.Expected.ExecutableSHA256) != nil || !filepath.IsAbs(intent.Expected.HostRoot) || filepath.Clean(intent.Expected.HostRoot) != intent.Expected.HostRoot || !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory || workingDirectory != intent.Expected.Session.Session.Directory || workingDirectory != dispatch.Dispatch.Binding.Directory || intent.Expected.MaxOutputTokens < 1 || intent.Expected.MaxOutputTokens > toolTurnMaximumExactInteger || intent.Expected.RuntimeOutputTokens < 0 || intent.Expected.RuntimeOutputTokens > intent.Expected.MaxOutputTokens || intent.Expected.Provider != nil && intent.Expected.RuntimeOutputTokens < 1 || safepath.RequireDigest(intent.Expected.Tools.SHA256) != nil || intent.Expected.Tools.MCPServer != ToolsMCPServerName || !slices.Equal(intent.Expected.Tools.ToolIDs, permissionIDs) || intent.ToolsConfigurationSHA256 != intent.Expected.Tools.SHA256 || intent.MCPEndpoint != intent.Expected.Tools.Endpoint || intent.Expected.Provider != nil && validateProviderToolTurnSealExpected(*intent.Expected.Provider, intent.Expected) != nil || intent.Expected.RuntimeMetadata != nil && intent.Expected.RuntimeMetadata.validate() != nil || intent.Expected.RuntimeMetadata != nil && intent.Expected.RuntimeMetadata.WorkspaceRoot != workingDirectory {
		return errors.New("invalid synchronous tool turn seal binding")
	}
	if !validExactLoopbackEndpoint(intent.OpenCodeEndpoint, "") || !validExactLoopbackEndpoint(intent.MCPEndpoint, "/mcp") {
		return errors.New("invalid synchronous tool turn seal endpoint")
	}
	id, err := synchronousToolTurnSealIntentID(intent)
	if err != nil || id != intent.ID || safepath.RequireDigest(intent.ID) != nil {
		return errors.New("invalid synchronous tool turn seal identity")
	}
	return nil
}

func validExactLoopbackEndpoint(endpoint, path string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Path != path || parsed.RawPath != "" || parsed.Hostname() != "127.0.0.1" || net.ParseIP(parsed.Hostname()) == nil {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535 && parsed.Host == "127.0.0.1:"+strconv.Itoa(port) && parsed.String() == endpoint
}

func replaySynchronousToolTurnSeal(events []journal.Event) (synchronousToolTurnSealState, error) {
	state := synchronousToolTurnSealState{}
	for _, event := range events {
		switch event.Kind {
		case "opencode.tool-turn-seal-intent":
			if state.Intent != nil {
				return synchronousToolTurnSealState{}, errors.New("duplicate synchronous tool turn seal intent")
			}
			var intent SynchronousToolTurnSealIntent
			if canonical.Decode(event.Payload, &intent) != nil || validateSynchronousToolTurnSealIntent(intent) != nil {
				return synchronousToolTurnSealState{}, errors.New("invalid synchronous tool turn seal intent")
			}
			state.Intent = &intent
		case "opencode.tool-turn-sealed":
			if state.Intent == nil || state.Receipt != nil {
				return synchronousToolTurnSealState{}, errors.New("invalid synchronous tool turn terminal receipt")
			}
			var receipt ToolTurnTerminalReceipt
			if canonical.Decode(event.Payload, &receipt) != nil || validateToolTurnTerminalReceipt(receipt, *state.Intent) != nil {
				return synchronousToolTurnSealState{}, errors.New("invalid synchronous tool turn terminal receipt")
			}
			state.Receipt = &receipt
		default:
			return synchronousToolTurnSealState{}, errors.New("unknown synchronous tool turn seal event")
		}
	}
	return state, nil
}

func validateToolTurnTerminalReceipt(receipt ToolTurnTerminalReceipt, intent SynchronousToolTurnSealIntent) error {
	if receipt.Version != 1 || receipt.IntentID != intent.ID || receipt.ObservationSHA256 != intent.ObservationSHA256 || receipt.TranscriptSHA256 != intent.TranscriptSHA256 || receipt.OpenBrokerStateID != intent.OpenBrokerStateID || receipt.ToolsConfigurationSHA256 != intent.ToolsConfigurationSHA256 || receipt.MCPStatusSHA256 != intent.MCPStatusSHA256 || receipt.ProcessID != intent.ProcessID || safepath.RequireDigest(receipt.ClosedBrokerStateID) != nil || !receipt.MCPHandlersStopped || !receipt.RootProcessReaped || !receipt.BrokerClosed {
		return errors.New("terminal receipt differs from seal intent")
	}
	if intent.Expected.Provider == nil {
		if receipt.ProviderProxyStopped || receipt.ProviderProcessSHA256 != "" || receipt.ProviderProxySHA256 != "" {
			return errors.New("legacy terminal receipt contains provider evidence")
		}
	} else if !receipt.ProviderProxyStopped || receipt.ProviderProcessSHA256 != intent.Expected.Provider.Process.SHA256 || receipt.ProviderProxySHA256 != intent.Expected.Provider.Proxy.SHA256 {
		return errors.New("provider terminal receipt differs from seal intent")
	}
	return nil
}

func appendSynchronousToolTurnSeal(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		_, err := replaySynchronousToolTurnSeal(events)
		return err
	})
	return err
}
