package control

import (
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

// newCompositeBackendVerifier constructs the finite journal-only verifier used
// by the runtime adapter. The transport's owner labels select a validated
// backend; they cannot supply a new callback or substitute caller identity.
func newCompositeBackendVerifier(controllerPath, schedulerPath, brokerPath string, caller agentDispatchBinding, expected contextbroker.Binding, receipts toolreceipts.Binding) (opencode.CompositeBackendVerifier, error) {
	callerID, err := agentToolCallerIdentity(controllerPath, schedulerPath, caller)
	if err != nil {
		return nil, err
	}
	id, err := receipts.ID()
	if err != nil || id != receipts.BindingID || receipts.CallerBindingSHA256 != callerID || receipts.InvocationID != caller.InvocationID || expected.InvocationID != caller.InvocationID {
		return nil, errors.New("composite backend caller binding differs")
	}
	// Copy nested repository/candidate state before retaining it in a closure.
	raw, err := canonical.Bytes(expected)
	if err != nil {
		return nil, err
	}
	var contextBinding contextbroker.Binding
	if err := canonical.Decode(raw, &contextBinding); err != nil {
		return nil, err
	}
	state, err := contextbroker.Inspect(brokerPath)
	if err != nil || state.Binding == nil || !sameCompositeBinding(*state.Binding, contextBinding) {
		return nil, errors.Join(errors.New("composite context backend differs"), err)
	}
	agentVerifier, err := newAgentToolReceiptVerifier(controllerPath, schedulerPath, caller)
	if err != nil {
		return nil, err
	}
	owners := make(map[string]toolreceipts.Owner, len(receipts.Tools))
	for _, tool := range receipts.Tools {
		owners[tool.Tool] = tool.Owner
	}
	return func(owner toolreceipts.Owner, call toolbridge.Call, result toolbridge.Result) error {
		if expectedOwner, found := owners[call.Tool]; !found || expectedOwner != owner {
			return errors.New("composite tool owner differs")
		}
		switch owner {
		case toolreceipts.OwnerContext:
			return contextmcp.VerifyBackendReceipt(brokerPath, contextBinding, call, result)
		case toolreceipts.OwnerAgent:
			return agentVerifier.Verify(call, result)
		default:
			return errors.New("unsupported composite backend")
		}
	}, nil
}

func sameCompositeBinding(left, right contextbroker.Binding) bool {
	a, aErr := canonical.Bytes(left)
	b, bErr := canonical.Bytes(right)
	return aErr == nil && bErr == nil && string(a) == string(b)
}
