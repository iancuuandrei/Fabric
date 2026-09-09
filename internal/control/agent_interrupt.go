package control

import (
	"context"
	"errors"
	"sync"
	"time"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/taskscheduler"
)

const scheduledInterruptPollInterval = 100 * time.Millisecond

// RequestAgentInterrupt durably requests interruption of one exact scheduled
// OpenCode explorer turn. The request contains no process identifier and does
// not itself claim that the running executor observed or completed the stop.
func RequestAgentInterrupt(ctx context.Context, controllerPath, schedulerPath, turnID, actor, nonce string) (agentcontrol.InterruptState, error) {
	if ctx == nil {
		return agentcontrol.InterruptState{}, errors.New("agent interrupt context required")
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil {
		return agentcontrol.InterruptState{}, err
	}
	dynamic, ok := scheduledDynamicTurn(scheduled, turnID)
	if !ok {
		return agentcontrol.InterruptState{}, errors.New("scheduled agent turn unavailable")
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	snapshot, _, invocation, err := scheduledInvocation(dynamic.Task, &turn)
	if err != nil {
		return agentcontrol.InterruptState{}, err
	}
	if dynamic.Task.ControllerPath != controllerPath || dynamic.Task.RunID != snapshot.RunID || dynamic.Task.InvocationID != invocation.ID {
		return agentcontrol.InterruptState{}, errors.New("scheduled agent interrupt identity changed")
	}
	if dynamic.Task.Operation != taskscheduler.OperationExplorer || invocation.Profile.Runtime != "opencode-http" {
		return agentcontrol.InterruptState{}, errors.New("scheduled agent runtime does not support interruption")
	}
	if err := validateScheduledAgentTurn(dynamic.Task, snapshot, invocation, &turn); err != nil {
		return agentcontrol.InterruptState{}, err
	}
	service, err := agentcontrol.BindScheduled(controllerPath+".agent-tree", controllerPath+".agent-control", schedulerPath)
	if err != nil {
		return agentcontrol.InterruptState{}, err
	}
	return service.RequestInterrupt(ctx, turnID, actor, nonce)
}

type scheduledInterruptWatcher struct {
	stop        context.CancelFunc
	cancel      context.CancelFunc
	done        <-chan error
	service     *agentcontrol.Service
	controlPath string
	claim       taskscheduler.Claim
	mu          sync.Mutex
	interrupt   *agentcontrol.InterruptState
}

type scheduledInterruptShutdownKey struct{}

func watchScheduledAgentInterrupt(ctx context.Context, claim taskscheduler.Claim) (context.Context, *scheduledInterruptWatcher, error) {
	if claim.AgentTurn == nil {
		return ctx, nil, nil
	}
	controlPath := claim.Task.ControllerPath + ".agent-control"
	service, err := agentcontrol.Bind(claim.Task.ControllerPath+".agent-tree", controlPath)
	if err != nil {
		return nil, nil, err
	}
	executionCtx, cancelExecution := context.WithCancel(ctx)
	watchCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	watcher := &scheduledInterruptWatcher{stop: stop, cancel: cancelExecution, done: done, service: service, controlPath: controlPath, claim: claim}
	executionCtx = context.WithValue(executionCtx, scheduledInterruptShutdownKey{}, opencoderuntime.InterruptShutdownLookup(watcher.lookup))
	go func() {
		ticker := time.NewTicker(scheduledInterruptPollInterval)
		defer ticker.Stop()
		for {
			interrupt, ok, interruptErr := exactScheduledInterrupt(controlPath, claim)
			if interruptErr != nil {
				cancelExecution()
				done <- interruptErr
				return
			}
			if ok && interrupt.Observation == nil {
				watcher.remember(interrupt)
				cancelExecution()
				done <- watcher.observe(interrupt, agentcontrol.InterruptSignalDelivered)
				return
			}
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-watchCtx.Done():
				done <- nil
				return
			case <-ticker.C:
			}
		}
	}()
	return executionCtx, watcher, nil
}

func (w *scheduledInterruptWatcher) remember(interrupt agentcontrol.InterruptState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	copy := interrupt
	w.interrupt = &copy
}

func (w *scheduledInterruptWatcher) lookup(intent opencoderuntime.Intent) (opencoderuntime.InterruptShutdownExpected, bool, error) {
	w.mu.Lock()
	remembered := w.interrupt
	w.mu.Unlock()
	var interrupt agentcontrol.InterruptState
	var ok bool
	var err error
	if remembered != nil {
		interrupt = *remembered
		ok = true
	} else {
		interrupt, ok, err = exactScheduledInterrupt(w.controlPath, w.claim)
	}
	if err != nil || !ok {
		return opencoderuntime.InterruptShutdownExpected{}, false, err
	}
	if intent.Invocation.ID != w.claim.Task.InvocationID || interrupt.Request.InvocationID != intent.Invocation.ID {
		return opencoderuntime.InterruptShutdownExpected{}, false, errors.New("scheduled interrupt runtime invocation differs")
	}
	return opencoderuntime.InterruptShutdownExpected{Version: 1, Runtime: intent, RequestID: interrupt.RequestID, Request: interrupt.Request}, true, nil
}

func (w *scheduledInterruptWatcher) finish() error {
	if w == nil {
		return nil
	}
	w.stop()
	w.cancel()
	if err := <-w.done; err != nil {
		return err
	}
	interrupt, ok, err := exactScheduledInterrupt(w.controlPath, w.claim)
	if err != nil {
		return err
	}
	if !ok || interrupt.Observation == nil || interrupt.Observation.Outcome != agentcontrol.InterruptSignalDelivered {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	evidence, err := (ScheduledDispatchAdapter{}).Probe(probeCtx, taskscheduler.ProbeRequest{Task: w.claim.Task, AgentTurn: w.claim.AgentTurn})
	if err != nil {
		return err
	}
	outcome := agentcontrol.InterruptUnknown
	if evidence.Status == taskscheduler.StatusSucceeded || evidence.Status == taskscheduler.StatusFailed || evidence.Status == taskscheduler.StatusCancelled {
		outcome = agentcontrol.InterruptAlreadyTerminal
	} else {
		snapshot, _, invocation, invocationErr := scheduledInvocation(w.claim.Task, w.claim.AgentTurn)
		if invocationErr != nil {
			return invocationErr
		}
		runtimePath, pathErr := scheduledRuntimeJournal(snapshot, invocation, w.claim.Task, w.claim.AgentTurn)
		if pathErr != nil {
			return pathErr
		}
		_, shutdownErr := opencoderuntime.InspectInterruptShutdownRequest(runtimePath, interrupt.RequestID, interrupt.Request)
		switch {
		case shutdownErr == nil:
			outcome = agentcontrol.InterruptConfirmedLocalStop
		case errors.Is(shutdownErr, opencoderuntime.ErrInterruptShutdownNotAttempted), errors.Is(shutdownErr, opencoderuntime.ErrInterruptShutdownUnconfirmed):
		default:
			return shutdownErr
		}
	}
	return w.observe(interrupt, outcome)
}

func scheduledInterruptShutdownLookup(ctx context.Context) opencoderuntime.InterruptShutdownLookup {
	if ctx == nil {
		return nil
	}
	lookup, _ := ctx.Value(scheduledInterruptShutdownKey{}).(opencoderuntime.InterruptShutdownLookup)
	return lookup
}

func (w *scheduledInterruptWatcher) observe(interrupt agentcontrol.InterruptState, outcome agentcontrol.InterruptOutcome) error {
	head, err := controllerJournalHead(w.claim.Task.ControllerPath)
	if err != nil {
		return err
	}
	evidence, err := interruptEvidence(interrupt.RequestID, outcome, head)
	if err != nil {
		return err
	}
	observeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = w.service.ObserveInterrupt(observeCtx, interrupt.RequestID, agentcontrol.InterruptObservation{Version: 1, Outcome: outcome, Evidence: evidence, ControllerHead: head})
	return err
}

func exactScheduledInterrupt(controlPath string, claim taskscheduler.Claim) (agentcontrol.InterruptState, bool, error) {
	state, err := agentcontrol.Inspect(controlPath)
	if err != nil {
		return agentcontrol.InterruptState{}, false, err
	}
	claimID, err := claim.ID()
	if err != nil {
		return agentcontrol.InterruptState{}, false, err
	}
	for _, interrupt := range state.Interrupts {
		request := interrupt.Request
		if request.AgentTurn.TurnID != claim.Task.ID {
			continue
		}
		if claim.AgentTurn == nil || request.TreeID != claim.Task.RunID || request.ScheduleID != claim.ScheduleID || request.AgentTurn != *claim.AgentTurn || request.InvocationID != claim.Task.InvocationID || request.ClaimID != claimID || request.ControllerHead != claim.ControllerHead {
			return agentcontrol.InterruptState{}, false, errors.New("scheduled interrupt request binding differs")
		}
		return interrupt, true, nil
	}
	return agentcontrol.InterruptState{}, false, nil
}

func scheduledDynamicTurn(snapshot taskscheduler.Snapshot, turnID string) (taskscheduler.DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.TurnID == turnID {
			return dynamic, true
		}
	}
	return taskscheduler.DynamicTask{}, false
}

func interruptEvidence(requestID string, outcome agentcontrol.InterruptOutcome, controllerHead string) (string, error) {
	return canonical.Hash("harness.control-agent-interrupt-observation.v1", struct {
		RequestID      string                        `json:"request_id"`
		Outcome        agentcontrol.InterruptOutcome `json:"outcome"`
		ControllerHead string                        `json:"controller_head"`
	}{requestID, outcome, controllerHead})
}
