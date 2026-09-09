package providerruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/worktree"
)

const (
	boundEvent  = "provider-runtime.bound"
	resultEvent = "provider-runtime.result"
)

// State is reconstructed exclusively from the validated direct-runtime journal.
type State struct {
	Binding *InvocationRecord `json:"binding,omitempty"`
	Result  *Result           `json:"result,omitempty"`
}

// Inspect validates the complete direct-runtime journal without egress.
func Inspect(path string) (State, error) {
	events, err := journal.Read(path)
	if err != nil {
		return State{}, err
	}
	return replay(events)
}

func replay(events []journal.Event) (State, error) {
	var state State
	for _, event := range events {
		switch event.Kind {
		case boundEvent:
			if state.Binding != nil {
				return State{}, errors.New("provider runtime already bound")
			}
			var binding InvocationRecord
			if canonical.Decode(event.Payload, &binding) != nil || validateBindingRecord(binding) != nil {
				return State{}, errors.New("invalid provider runtime binding")
			}
			state.Binding = &binding
		case resultEvent:
			if state.Binding == nil || state.Result != nil {
				return State{}, errors.New("invalid provider runtime result transition")
			}
			var result Result
			if canonical.Decode(event.Payload, &result) != nil || validateResult(*state.Binding, result) != nil {
				return State{}, errors.New("invalid provider runtime result")
			}
			state.Result = &result
		default:
			return State{}, errors.New("unknown provider runtime event")
		}
	}
	return cloneState(state), nil
}

func validateBindingRecord(b InvocationRecord) error {
	if b.Version != 1 || !validDigest(b.InvocationID) || !validDigest(b.RouteID) || !validDigest(b.GatewayBindingID) || !validDigest(b.RequestExpectationID) || !validDigest(b.CredentialBindingID) || b.WorkspaceLeaseID != "" && !validDigest(b.WorkspaceLeaseID) || b.Invocation.validate() != nil || !json.Valid(b.RequestBody) {
		return ErrRejected
	}
	expectation := b.Expectation.gatewayExpectation()
	inputHash, inputErr := b.Invocation.InputHash(expectation)
	invocationID, invocationErr := b.AccessIntent.ID()
	routeID, routeErr := b.AccessIntent.Route.ID()
	gatewayID, gatewayErr := b.GatewayBinding.ID()
	expectationID, expectationErr := providergateway.AdapterRequestExpectationID(b.GatewayBinding, expectation)
	credentialID, credentialErr := b.CredentialBinding.ID()
	if inputErr != nil || invocationErr != nil || routeErr != nil || gatewayErr != nil || expectationErr != nil || credentialErr != nil ||
		inputHash != b.AccessIntent.InputHash || invocationID != b.InvocationID || invocationID != b.AccessIntent.Reservation.InvocationID ||
		routeID != b.RouteID || gatewayID != b.GatewayBindingID || expectationID != b.RequestExpectationID || credentialID != b.CredentialBindingID ||
		b.GatewayBinding.AccessInvocationID != b.InvocationID || b.GatewayBinding.RouteID != b.RouteID ||
		!directStructuredRequirement(expectation.RequiredCapabilities) {
		return ErrRejected
	}
	if _, err := providergateway.ValidateAdapterRequest(b.RequestBody, b.GatewayBinding, expectation); err != nil {
		return ErrRejected
	}
	if b.WorkspaceRequest == nil {
		if b.WorkspaceLeaseID != "" {
			return ErrRejected
		}
	} else {
		identity := worktree.LeaseIdentity{Version: 1, Request: *b.WorkspaceRequest}
		id, err := identity.ID()
		if err != nil || id != b.WorkspaceLeaseID {
			return ErrRejected
		}
	}
	return nil
}

func validateResult(binding InvocationRecord, result Result) error {
	if result.Version != 1 || result.Receipt.InvocationID != binding.InvocationID || result.Receipt.BindingID != binding.GatewayBindingID || result.Receipt.RequestExpectationID != binding.RequestExpectationID || result.Receipt.Finish != "stop" || result.Receipt.Semantic == nil || len(result.Receipt.Semantic.ToolCalls) != 0 {
		return ErrUnresolved
	}
	parsed, digest, err := validateOutput(binding.Invocation.Output, result.Text)
	textDigest := sha256.Sum256([]byte(result.Text))
	if err != nil || parsed != result.JSON || digest != result.JSONSHA256 || result.Receipt.Semantic.OutputTextSHA256 != hex.EncodeToString(textDigest[:]) ||
		!result.Receipt.StreamComplete || !result.Receipt.UsageComplete || result.Receipt.ResponseSHA256 == "" {
		return ErrUnresolved
	}
	return nil
}

func appendEvent(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, err := replay(events); return err })
	return err
}

func cloneState(source State) State {
	var result State
	raw, err := canonical.Bytes(source)
	if err == nil {
		_ = canonical.Decode(raw, &result)
	}
	return result
}

func sameBinding(left, right InvocationRecord) bool { return reflect.DeepEqual(left, right) }
