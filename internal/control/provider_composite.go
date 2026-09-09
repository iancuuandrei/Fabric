package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

// The bound transport remains serial; this is waiting HTTP requests, not agent
// execution slots or permission for additional tool effects.
const compositeToolQueueLimit = 32

func prepareProviderComposite(ctx context.Context, controllerPath, runtimePath string, invocation runtime.Invocation, broker *contextbroker.Broker, contextBinding contextbroker.Binding, events []journal.Event) (*contextmcp.RecorderOwnedConfig, *toolreceipts.Binding, opencode.CompositeBackendVerifier, error) {
	schedulerPath, boundSchedule := scheduledDispatchJournalPath(ctx)
	recovering := len(events) != 0
	maxQueuedCalls := compositeToolQueueLimit
	if recovering {
		var prior opencoderuntime.Intent
		if err := canonical.Decode(events[0].Payload, &prior); err != nil {
			return nil, nil, nil, err
		}
		id, err := prior.ID()
		if err != nil || id != prior.IntentID || prior.Invocation != invocation {
			return nil, nil, nil, errors.New("composite recovery runtime intent differs")
		}
		if prior.Version != 3 {
			return nil, nil, nil, nil
		}
		// Recovery preserves the original bound admission policy, including the
		// legacy zero-queue binding. It never upgrades a recorded invocation.
		maxQueuedCalls = prior.ToolReceipts.MaxQueuedCalls
		if !boundSchedule {
			return nil, nil, nil, errors.New("composite recovery requires exact scheduler context")
		}
	}
	if !boundSchedule || scheduledAgentTurn(ctx) == nil || invocation.Profile.Role != "explorer" {
		if recovering {
			return nil, nil, nil, errors.New("composite recovery caller unavailable")
		}
		return nil, nil, nil, nil
	}
	current, err := Inspect(controllerPath)
	if err != nil {
		return nil, nil, nil, err
	}
	dispatch, found := current.AgentDispatch[invocation.ID]
	admissionID, err := dispatch.Admission.ID()
	if !found || err != nil || dispatch.Admission.Invocation != invocation || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *scheduledAgentTurn(ctx) {
		return nil, nil, nil, errors.New("composite runtime admission differs")
	}
	caller := agentDispatchBinding{JournalPath: controllerPath + ".agent-tree", ControllerPath: controllerPath, AdmissionID: admissionID, Node: dispatch.Admission.Node, InvocationID: invocation.ID, AgentTurn: dispatch.Admission.AgentTurn, Recovering: recovering}
	callerID, err := agentToolCallerIdentity(controllerPath, schedulerPath, caller)
	if err != nil {
		return nil, nil, nil, err
	}
	var projection toolbridge.Projection
	if recovering {
		// Recovery needs the immutable catalog but grants no tool execution.
		projection = toolbridge.Projection{Catalog: agentToolCatalog, Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{}, opencoderuntime.ErrRecoveryRequired
		}}
	} else {
		projection, err = newAgentToolProjection(controllerPath, schedulerPath, caller)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	binding, err := contextmcp.PrepareRecorderBindingWithQueue(broker, invocation.ID, callerID, projection, maxQueuedCalls)
	if err != nil {
		return nil, nil, nil, err
	}
	verify, err := newCompositeBackendVerifier(controllerPath, schedulerPath, broker.JournalPath(), caller, contextBinding, binding)
	if err != nil {
		return nil, nil, nil, err
	}
	config := &contextmcp.RecorderOwnedConfig{Path: runtimePath + ".tool-receipts", InvocationID: invocation.ID, CallerBindingSHA256: callerID, CatalogSHA256: binding.CatalogSHA256, AgentProjection: projection, MaxQueuedCalls: maxQueuedCalls}
	return config, &binding, verify, nil
}
