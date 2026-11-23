package control

import (
	"context"
	"errors"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/contextbroker"
	"github.com/iancuuandrei/fabric/internal/contextmcp"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/opencoderuntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/toolbridge"
	"github.com/iancuuandrei/fabric/internal/toolreceipts"
)

// The bound transport remains serial; this is waiting HTTP requests, not agent
// execution slots or permission for additional tool effects.
const compositeToolQueueLimit = contextmcp.InvocationToolCallBudget - 1

func prepareProviderComposite(ctx context.Context, controllerPath, runtimePath string, invocation runtime.Invocation, broker *contextbroker.Broker, contextBinding contextbroker.Binding, events []journal.Event) (*contextmcp.RecorderOwnedConfig, *toolreceipts.Binding, opencode.CompositeBackendVerifier, error) {
	schedulerPath, boundSchedule := scheduledDispatchJournalPath(ctx)
	recovering := len(events) != 0
	maxCalls := contextBinding.Limits.MaxCalls
	if maxCalls > contextmcp.InvocationToolCallBudget {
		maxCalls = contextmcp.InvocationToolCallBudget
	}
	maxQueuedCalls := maxCalls - 1
	queueWaitTimeout, callbackTimeout := time.Duration(maxQueuedCalls)*contextmcp.RecorderOwnedBridgeCallTimeout, contextmcp.RecorderOwnedBridgeCallTimeout
	if maxQueuedCalls < 1 {
		maxQueuedCalls, queueWaitTimeout, callbackTimeout = 0, 0, 0
	}
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
		queueWaitTimeout = time.Duration(prior.ToolReceipts.QueueWaitMillis) * time.Millisecond
		callbackTimeout = time.Duration(prior.ToolReceipts.CallbackTimeoutMillis) * time.Millisecond
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
	var binding toolreceipts.Binding
	if queueWaitTimeout > 0 || callbackTimeout > 0 {
		binding, err = contextmcp.PrepareRecorderBindingWithTimeouts(broker, invocation.ID, callerID, projection, maxQueuedCalls, queueWaitTimeout, callbackTimeout)
	} else {
		binding, err = contextmcp.PrepareRecorderBindingWithQueue(broker, invocation.ID, callerID, projection, maxQueuedCalls)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	verify, err := newCompositeBackendVerifier(controllerPath, schedulerPath, broker.JournalPath(), caller, contextBinding, binding)
	if err != nil {
		return nil, nil, nil, err
	}
	config := &contextmcp.RecorderOwnedConfig{Path: runtimePath + ".tool-receipts", InvocationID: invocation.ID, CallerBindingSHA256: callerID, CatalogSHA256: binding.CatalogSHA256, AgentProjection: projection, MaxQueuedCalls: maxQueuedCalls, QueueWaitTimeout: queueWaitTimeout, CallbackTimeout: callbackTimeout}
	return config, &binding, verify, nil
}
