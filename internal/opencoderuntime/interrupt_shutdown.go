package opencoderuntime

import (
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

var (
	// ErrInterruptShutdownNotAttempted means no exact local-shutdown intent exists.
	ErrInterruptShutdownNotAttempted = errors.New("interrupt shutdown not attempted")
	// ErrInterruptShutdownUnconfirmed means teardown began but has no complete receipt.
	ErrInterruptShutdownUnconfirmed = errors.New("interrupt shutdown remains unconfirmed")
)

const (
	interruptShutdownIntentEvent  = "opencode-runtime.interrupt-shutdown-intent"
	interruptShutdownReceiptEvent = "opencode-runtime.interrupt-shutdown-receipt"
	interruptShutdownSuffix       = ".interrupt-shutdown.jsonl"
)

// InterruptShutdownExpected binds local teardown evidence to one exact runtime
// intent and one exact durable agent interrupt request.
type InterruptShutdownExpected struct {
	Version   int                           `json:"version"`
	Runtime   Intent                        `json:"runtime"`
	RequestID string                        `json:"request_id"`
	Request   agentcontrol.InterruptRequest `json:"request"`
}

// InterruptShutdownResources identifies the already admitted local resources
// which an interrupt teardown must stop. It contains no bearer or credential.
type InterruptShutdownResources struct {
	Version               int    `json:"version"`
	ProcessID             int    `json:"process_id"`
	ProviderProcessSHA256 string `json:"provider_process_sha256"`
	ProviderProxySHA256   string `json:"provider_proxy_sha256"`
	MCPServerSHA256       string `json:"mcp_server_sha256"`
	BrokerPath            string `json:"broker_path"`
	BrokerBindingID       string `json:"broker_binding_id"`
	BrokerOpenStateID     string `json:"broker_open_state_id"`
	BrokerOpenHead        string `json:"broker_open_head"`
}

// InterruptShutdownIntent is durable before teardown begins. RuntimeHead is
// the exact validated runtime-journal prefix observed at that boundary.
type InterruptShutdownIntent struct {
	Version     int                        `json:"version"`
	ID          string                     `json:"id"`
	Expected    InterruptShutdownExpected  `json:"expected"`
	RuntimeHead string                     `json:"runtime_head"`
	Resources   InterruptShutdownResources `json:"resources"`
}

// InterruptShutdownReceipt proves only that the exact owned local resources
// named by its intent stopped. It is not a model, provider, scheduler, access,
// TaskPool, or AgentTree terminal receipt.
type InterruptShutdownReceipt struct {
	Version                 int    `json:"version"`
	IntentID                string `json:"intent_id"`
	RuntimeHead             string `json:"runtime_head"`
	ProcessID               int    `json:"process_id"`
	ProviderProcessSHA256   string `json:"provider_process_sha256"`
	ProviderProxySHA256     string `json:"provider_proxy_sha256"`
	MCPServerSHA256         string `json:"mcp_server_sha256"`
	BrokerBindingID         string `json:"broker_binding_id"`
	BrokerOpenStateID       string `json:"broker_open_state_id"`
	BrokerOpenHead          string `json:"broker_open_head"`
	BrokerSettledStateID    string `json:"broker_settled_state_id"`
	BrokerSettledHead       string `json:"broker_settled_head"`
	BrokerClosedStateID     string `json:"broker_closed_state_id"`
	BrokerClosedHead        string `json:"broker_closed_head"`
	MCPHandlersStopped      bool   `json:"mcp_handlers_stopped"`
	ProviderProxyStopped    bool   `json:"provider_proxy_stopped"`
	ProviderHandlersSettled bool   `json:"provider_handlers_settled"`
	RootProcessReaped       bool   `json:"root_process_reaped"`
	ProcessWaitErrorSHA256  string `json:"process_wait_error_sha256,omitempty"`
	BrokerClosed            bool   `json:"broker_closed"`
}

type interruptShutdownState struct {
	Intent  *InterruptShutdownIntent
	Receipt *InterruptShutdownReceipt
}

// verifiedInterruptShutdown is an in-package capability. Live integration may
// construct it only after waiting on the owned MCP server, provider proxy and
// root process and after reading back the exact closed broker. There is no
// exported receipt writer or boolean-based constructor.
type verifiedInterruptShutdown struct {
	seal    *interruptShutdownVerificationSeal
	receipt InterruptShutdownReceipt
}

type interruptShutdownVerificationSeal struct{}

// InspectInterruptShutdown validates a stored local-shutdown receipt against
// the exact runtime intent and interrupt request. It is journal-only and never
// repeats teardown, provider work, or any other effect.
func InspectInterruptShutdown(runtimePath string, expected InterruptShutdownExpected) (InterruptShutdownReceipt, error) {
	var zero InterruptShutdownReceipt
	if err := validateInterruptShutdownExpected(expected); err != nil {
		return zero, err
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		return zero, err
	}
	state, err := replay(runtimeEvents)
	if err != nil || state.Intent == nil || !equalCanonical(*state.Intent, normalizedIntent(expected.Runtime)) {
		return zero, errors.Join(errors.New("interrupt shutdown runtime intent mismatch"), err)
	}
	events, err := journal.Read(interruptShutdownPath(runtimePath))
	if err != nil {
		return zero, err
	}
	shutdown, err := replayInterruptShutdown(events)
	if err != nil {
		return zero, err
	}
	if shutdown.Intent == nil {
		return zero, ErrInterruptShutdownNotAttempted
	}
	if !equalCanonical(shutdown.Intent.Expected, expected) {
		return zero, errors.New("interrupt shutdown expectation mismatch")
	}
	if shutdown.Receipt == nil {
		return zero, ErrInterruptShutdownUnconfirmed
	}
	startIndex := eventIndex(runtimeEvents, shutdown.Intent.RuntimeHead)
	stopIndex := eventIndex(runtimeEvents, shutdown.Receipt.RuntimeHead)
	if startIndex < 0 || stopIndex < startIndex {
		return zero, errors.New("interrupt shutdown runtime prefix unavailable")
	}
	if state.Bound == nil || state.Bound.Paths.Broker != shutdown.Intent.Resources.BrokerPath {
		return zero, errors.New("interrupt shutdown broker path mismatch")
	}
	brokerEvents, err := journal.Read(shutdown.Intent.Resources.BrokerPath)
	if err != nil {
		return zero, err
	}
	openIndex := eventIndex(brokerEvents, shutdown.Receipt.BrokerOpenHead)
	settledIndex := eventIndex(brokerEvents, shutdown.Receipt.BrokerSettledHead)
	closedIndex := eventIndex(brokerEvents, shutdown.Receipt.BrokerClosedHead)
	if openIndex < 0 || settledIndex < openIndex || closedIndex <= settledIndex || closedIndex != len(brokerEvents)-1 {
		return zero, errors.New("interrupt shutdown broker prefixes unavailable")
	}
	closed, err := contextbroker.Inspect(shutdown.Intent.Resources.BrokerPath)
	if err != nil || !closed.Closed {
		return zero, errors.Join(errors.New("interrupt shutdown broker is not durably closed"), err)
	}
	closedID, err := interruptBrokerStateID(closed)
	if err != nil || closedID != shutdown.Receipt.BrokerClosedStateID {
		return zero, errors.Join(errors.New("interrupt shutdown closed broker identity mismatch"), err)
	}
	return *shutdown.Receipt, nil
}

// InspectInterruptShutdownRequest finds and validates shutdown evidence for one
// exact interrupt request. The stored expectation remains authoritative for the
// runtime intent; callers cannot substitute or omit that intent projection.
func InspectInterruptShutdownRequest(runtimePath, requestID string, request agentcontrol.InterruptRequest) (InterruptShutdownReceipt, error) {
	var zero InterruptShutdownReceipt
	exactID, err := request.ID()
	if err != nil || requestID != exactID {
		return zero, errors.New("invalid interrupt shutdown request")
	}
	events, err := journal.Read(interruptShutdownPath(runtimePath))
	if err != nil {
		return zero, err
	}
	shutdown, err := replayInterruptShutdown(events)
	if err != nil {
		return zero, err
	}
	if shutdown.Intent == nil {
		return zero, ErrInterruptShutdownNotAttempted
	}
	if shutdown.Intent.Expected.RequestID != requestID || !equalCanonical(shutdown.Intent.Expected.Request, request) {
		return zero, errors.New("interrupt shutdown request mismatch")
	}
	return InspectInterruptShutdown(runtimePath, shutdown.Intent.Expected)
}

func recordInterruptShutdownIntent(runtimePath string, expected InterruptShutdownExpected, resources InterruptShutdownResources) (InterruptShutdownIntent, error) {
	var zero InterruptShutdownIntent
	if err := validateInterruptShutdownExpected(expected); err != nil || validateInterruptShutdownResources(resources, expected.Runtime) != nil {
		return zero, errors.Join(errors.New("invalid interrupt shutdown intent"), err)
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		return zero, err
	}
	state, err := replay(runtimeEvents)
	if err != nil || state.Intent == nil || !equalCanonical(*state.Intent, normalizedIntent(expected.Runtime)) || len(runtimeEvents) == 0 {
		return zero, errors.Join(errors.New("interrupt shutdown runtime intent mismatch"), err)
	}
	intent := InterruptShutdownIntent{Version: 1, Expected: expected, RuntimeHead: runtimeEvents[len(runtimeEvents)-1].Hash, Resources: resources}
	intent.ID, err = interruptShutdownIntentID(intent)
	if err != nil {
		return zero, err
	}
	path := interruptShutdownPath(runtimePath)
	events, err := journal.Read(path)
	if err != nil {
		return zero, err
	}
	prior, err := replayInterruptShutdown(events)
	if err != nil {
		return zero, err
	}
	if prior.Intent != nil {
		if equalCanonical(*prior.Intent, intent) {
			return *prior.Intent, nil
		}
		return zero, errors.New("interrupt shutdown already differs")
	}
	if _, err := journal.Append(path, interruptShutdownIntentEvent, intent, validateInterruptShutdownEvents); err != nil {
		return zero, err
	}
	return intent, nil
}

func recordInterruptShutdownReceipt(runtimePath string, intent InterruptShutdownIntent, proof verifiedInterruptShutdown) (InterruptShutdownReceipt, error) {
	var zero InterruptShutdownReceipt
	if proof.seal == nil || validateInterruptShutdownReceipt(proof.receipt, intent) != nil {
		return zero, errors.New("verified interrupt shutdown required")
	}
	runtimeEvents, err := journal.Read(runtimePath)
	if err != nil {
		return zero, err
	}
	if eventIndex(runtimeEvents, intent.RuntimeHead) < 0 || eventIndex(runtimeEvents, proof.receipt.RuntimeHead) < eventIndex(runtimeEvents, intent.RuntimeHead) {
		return zero, errors.New("interrupt shutdown runtime prefix unavailable")
	}
	path := interruptShutdownPath(runtimePath)
	events, err := journal.Read(path)
	if err != nil {
		return zero, err
	}
	state, err := replayInterruptShutdown(events)
	if err != nil || state.Intent == nil || !equalCanonical(*state.Intent, intent) {
		return zero, errors.Join(errors.New("interrupt shutdown intent mismatch"), err)
	}
	if state.Receipt != nil {
		if equalCanonical(*state.Receipt, proof.receipt) {
			return *state.Receipt, nil
		}
		return zero, errors.New("interrupt shutdown receipt already differs")
	}
	if _, err := journal.Append(path, interruptShutdownReceiptEvent, proof.receipt, validateInterruptShutdownEvents); err != nil {
		return zero, err
	}
	return proof.receipt, nil
}

func validateInterruptShutdownExpected(expected InterruptShutdownExpected) error {
	runtimeIntent := normalizedIntent(expected.Runtime)
	requestID, requestErr := expected.Request.ID()
	if expected.Version != 1 || runtimeIntent.Version == 0 || requestErr != nil || expected.RequestID != requestID || expected.Request.InvocationID != runtimeIntent.Invocation.ID || expected.Request.AgentTurn.TurnID == "" {
		return errors.New("invalid interrupt shutdown expectation")
	}
	return nil
}

func validateInterruptShutdownResources(resources InterruptShutdownResources, runtimeIntent Intent) error {
	bindingID, err := runtimeIntent.Context.ID()
	if resources.Version != 1 || resources.ProcessID <= 0 || err != nil || resources.BrokerBindingID != bindingID || !cleanAbsolute(resources.BrokerPath) {
		return errors.New("invalid interrupt shutdown resources")
	}
	for _, digest := range []string{resources.ProviderProcessSHA256, resources.ProviderProxySHA256, resources.MCPServerSHA256, resources.BrokerOpenStateID, resources.BrokerOpenHead} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid interrupt shutdown resources")
		}
	}
	return nil
}

func interruptShutdownIntentID(intent InterruptShutdownIntent) (string, error) {
	if intent.Version != 1 || validateInterruptShutdownExpected(intent.Expected) != nil || validateInterruptShutdownResources(intent.Resources, intent.Expected.Runtime) != nil || safepath.RequireDigest(intent.RuntimeHead) != nil {
		return "", errors.New("invalid interrupt shutdown intent")
	}
	intent.ID = ""
	return canonical.Hash("harness.opencode-runtime-interrupt-shutdown-intent.v1", intent)
}

func validateInterruptShutdownReceipt(receipt InterruptShutdownReceipt, intent InterruptShutdownIntent) error {
	intentID, err := interruptShutdownIntentID(intent)
	resources := intent.Resources
	if err != nil || receipt.Version != 1 || receipt.IntentID != intentID || receipt.ProcessID != resources.ProcessID || receipt.ProviderProcessSHA256 != resources.ProviderProcessSHA256 || receipt.ProviderProxySHA256 != resources.ProviderProxySHA256 || receipt.MCPServerSHA256 != resources.MCPServerSHA256 || receipt.BrokerBindingID != resources.BrokerBindingID || receipt.BrokerOpenStateID != resources.BrokerOpenStateID || receipt.BrokerOpenHead != resources.BrokerOpenHead {
		return errors.New("invalid interrupt shutdown receipt")
	}
	for _, digest := range []string{receipt.RuntimeHead, receipt.BrokerSettledStateID, receipt.BrokerSettledHead, receipt.BrokerClosedStateID, receipt.BrokerClosedHead} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid interrupt shutdown receipt")
		}
	}
	if receipt.ProcessWaitErrorSHA256 != "" && safepath.RequireDigest(receipt.ProcessWaitErrorSHA256) != nil {
		return errors.New("invalid interrupt shutdown receipt")
	}
	if !receipt.MCPHandlersStopped || !receipt.ProviderProxyStopped || !receipt.ProviderHandlersSettled || !receipt.RootProcessReaped || !receipt.BrokerClosed || receipt.BrokerClosedStateID == receipt.BrokerSettledStateID || receipt.BrokerClosedHead == receipt.BrokerSettledHead {
		return errors.New("invalid interrupt shutdown receipt")
	}
	return nil
}

func replayInterruptShutdown(events []journal.Event) (interruptShutdownState, error) {
	var state interruptShutdownState
	for _, event := range events {
		switch event.Kind {
		case interruptShutdownIntentEvent:
			if state.Intent != nil || state.Receipt != nil {
				return interruptShutdownState{}, errors.New("duplicate interrupt shutdown intent")
			}
			var intent InterruptShutdownIntent
			if canonical.Decode(event.Payload, &intent) != nil {
				return interruptShutdownState{}, errors.New("invalid interrupt shutdown intent")
			}
			id, err := interruptShutdownIntentID(intent)
			if err != nil || intent.ID != id {
				return interruptShutdownState{}, errors.New("invalid interrupt shutdown intent")
			}
			copy := intent
			state.Intent = &copy
		case interruptShutdownReceiptEvent:
			if state.Intent == nil || state.Receipt != nil {
				return interruptShutdownState{}, errors.New("invalid interrupt shutdown receipt order")
			}
			var receipt InterruptShutdownReceipt
			if canonical.Decode(event.Payload, &receipt) != nil || validateInterruptShutdownReceipt(receipt, *state.Intent) != nil {
				return interruptShutdownState{}, errors.New("invalid interrupt shutdown receipt")
			}
			copy := receipt
			state.Receipt = &copy
		default:
			return interruptShutdownState{}, errors.New("unknown interrupt shutdown event")
		}
	}
	return state, nil
}

func validateInterruptShutdownEvents(events []journal.Event) error {
	_, err := replayInterruptShutdown(events)
	return err
}

func interruptShutdownPath(runtimePath string) string {
	if !cleanAbsolute(runtimePath) {
		return ""
	}
	path := runtimePath + interruptShutdownSuffix
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 {
		return ""
	}
	return path
}

func eventIndex(events []journal.Event, head string) int {
	for index := range events {
		if events[index].Hash == head {
			return index
		}
	}
	return -1
}
