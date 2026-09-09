package agentcontrol

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
)

// InterruptOutcome is a finite executor observation. It is deliberately
// separate from scheduler and provider outcomes.
type InterruptOutcome string

const (
	// InterruptRequested means durable intent exists without executor observation.
	InterruptRequested InterruptOutcome = "requested"
	// InterruptSignalDelivered means the exact in-process cancel handle was invoked.
	InterruptSignalDelivered InterruptOutcome = "signal_delivered"
	// InterruptConfirmedLocalStop means teardown evidence proves owned resources stopped.
	InterruptConfirmedLocalStop InterruptOutcome = "confirmed_local_stop"
	// InterruptUnknown means local teardown could not be proved.
	InterruptUnknown InterruptOutcome = "unknown"
	// InterruptAlreadyTerminal means exact terminal evidence won the interrupt race.
	InterruptAlreadyTerminal InterruptOutcome = "already_terminal"
)

// InterruptRequest binds operator intent to one exact open scheduled turn.
// Every execution identity is derived from durable scheduler state.
type InterruptRequest struct {
	Version        int                            `json:"version"`
	TreeID         string                         `json:"tree_id"`
	ScheduleID     string                         `json:"schedule_id"`
	AgentTurn      taskscheduler.AgentTurnBinding `json:"agent_turn"`
	InvocationID   string                         `json:"invocation_id"`
	ClaimID        string                         `json:"claim_id"`
	ControllerHead string                         `json:"controller_head"`
	Actor          string                         `json:"actor"`
	Nonce          string                         `json:"nonce"`
}

// ID returns the immutable identity of one exact interrupt request.
func (r InterruptRequest) ID() (string, error) {
	if err := validateInterruptRequest(r); err != nil {
		return "", err
	}
	return canonical.Hash("harness.agentcontrol-interrupt.v1", r)
}

// InterruptObservation records executor evidence without asserting a provider,
// scheduler, TaskPool, or AgentTree terminal outcome.
type InterruptObservation struct {
	Version        int              `json:"version"`
	Outcome        InterruptOutcome `json:"outcome"`
	Evidence       string           `json:"evidence"`
	ControllerHead string           `json:"controller_head"`
}

// InterruptState is the replayed request and optional executor observation.
type InterruptState struct {
	RequestID   string                `json:"request_id"`
	Request     InterruptRequest      `json:"request"`
	Observation *InterruptObservation `json:"observation,omitempty"`
}

type interruptRequestedEvent struct {
	Version   int              `json:"version"`
	RequestID string           `json:"request_id"`
	Request   InterruptRequest `json:"request"`
	Activity  Activity         `json:"activity"`
}

type interruptObservedEvent struct {
	Version     int                  `json:"version"`
	RequestID   string               `json:"request_id"`
	Observation InterruptObservation `json:"observation"`
	Activity    Activity             `json:"activity"`
}

// RequestInterrupt appends operator intent for one exact dynamic task with an
// open scheduler claim. The caller supplies no runtime, path, PID, or model.
func (s *ScheduledService) RequestInterrupt(ctx context.Context, turnID, actor, nonce string) (InterruptState, error) {
	if err := s.validateScheduled(ctx); err != nil || safepath.RequireDigest(turnID) != nil || !boundedInterruptText(actor, 256) || !boundedInterruptText(nonce, 128) {
		return InterruptState{}, errors.Join(errors.New("invalid agent interrupt request"), err)
	}
	scheduled, err := taskscheduler.Inspect(s.schedulerPath)
	if err != nil || scheduled.ScheduleID != s.scheduleID {
		return InterruptState{}, errors.Join(errors.New("agent scheduler identity changed"), err)
	}
	dynamic, ok := dynamicTurn(scheduled, turnID)
	state := scheduled.Tasks[turnID]
	if !ok || dynamic.Task.ID != turnID || dynamic.Task.RunID != s.treeID || state.Claim == nil || state.Claim.AgentTurn == nil {
		return InterruptState{}, errors.New("scheduled agent turn has no interruptible claim")
	}
	tree, treeErr := agenttree.Inspect(s.treePath)
	node, nodeOK := nodeByID(tree, dynamic.AgentID)
	if treeErr != nil || tree.TreeID != s.treeID || !nodeOK || node.ParentAgentID != dynamic.ParentAgentID {
		return InterruptState{}, errors.Join(errors.New("scheduled interrupt agent binding differs"), treeErr)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	if *state.Claim.AgentTurn != turn || !reflect.DeepEqual(state.Claim.Task, dynamic.Task) || state.Claim.ScheduleID != s.scheduleID || state.Claim.Task.InvocationID == "" {
		return InterruptState{}, errors.New("scheduled agent interrupt binding differs")
	}
	claimID, err := state.Claim.ID()
	if err != nil {
		return InterruptState{}, err
	}
	request := InterruptRequest{Version: 1, TreeID: s.treeID, ScheduleID: s.scheduleID, AgentTurn: turn, InvocationID: dynamic.Task.InvocationID, ClaimID: claimID, ControllerHead: state.Claim.ControllerHead, Actor: actor, Nonce: nonce}
	requestID, err := request.ID()
	if err != nil {
		return InterruptState{}, err
	}
	current, err := Inspect(s.journalPath)
	if err != nil {
		return InterruptState{}, err
	}
	if prior, exists := current.Interrupts[requestID]; exists && prior.Request == request {
		return prior, nil
	}
	if state.Status != taskscheduler.StatusClaimed && state.Status != taskscheduler.StatusRunning && state.Status != taskscheduler.StatusUnknown {
		return InterruptState{}, errors.New("scheduled agent turn is not open")
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, inspectErr := Inspect(s.journalPath)
		if inspectErr != nil {
			return InterruptState{}, inspectErr
		}
		if prior, exists := current.Interrupts[requestID]; exists {
			if prior.Request == request {
				return prior, nil
			}
			return InterruptState{}, errors.New("agent interrupt request differs")
		}
		for _, prior := range current.Interrupts {
			if prior.Request.AgentTurn.TurnID == turnID {
				return InterruptState{}, errors.New("agent turn already has an interrupt request")
			}
		}
		activity := Activity{AgentID: turn.AgentID, Sequence: nextActivitySequence(current, turn.AgentID), Kind: "interrupt", TurnID: turn.TurnID, TurnSequence: turn.TurnSequence, InterruptID: requestID, Interrupt: InterruptRequested}
		_, appendErr := journal.Append(s.journalPath, "agentcontrol.interrupt-requested", interruptRequestedEvent{Version: 1, RequestID: requestID, Request: request, Activity: activity}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if appendErr == nil {
			return InterruptState{RequestID: requestID, Request: request}, nil
		}
	}
	return InterruptState{}, errors.New("agent interrupt request contention")
}

// Interrupt returns the exact request for a turn, if one exists.
func (s *ScheduledService) Interrupt(turnID string) (InterruptState, bool, error) {
	if err := validateService(s.Service); err != nil || safepath.RequireDigest(turnID) != nil {
		return InterruptState{}, false, errors.Join(errors.New("invalid agent interrupt lookup"), err)
	}
	scheduled, err := taskscheduler.Inspect(s.schedulerPath)
	if err != nil || scheduled.ScheduleID != s.scheduleID {
		return InterruptState{}, false, errors.Join(errors.New("agent scheduler identity changed"), err)
	}
	state, err := Inspect(s.journalPath)
	if err != nil {
		return InterruptState{}, false, err
	}
	for _, interrupt := range state.Interrupts {
		if interrupt.Request.AgentTurn.TurnID == turnID {
			return interrupt, true, nil
		}
	}
	return InterruptState{}, false, nil
}

// ObserveInterrupt appends executor evidence for an existing exact request.
func (s *Service) ObserveInterrupt(ctx context.Context, requestID string, observation InterruptObservation) (InterruptState, error) {
	if err := validateService(s); err != nil || ctx == nil || ctx.Err() != nil || safepath.RequireDigest(requestID) != nil || validateInterruptObservation(observation) != nil {
		return InterruptState{}, errors.Join(errors.New("invalid agent interrupt observation"), err, contextError(ctx))
	}
	for attempt := 0; attempt < 3; attempt++ {
		current, inspectErr := Inspect(s.journalPath)
		if inspectErr != nil {
			return InterruptState{}, inspectErr
		}
		prior, ok := current.Interrupts[requestID]
		if !ok {
			return InterruptState{}, errors.New("agent interrupt request unavailable")
		}
		if prior.Observation != nil {
			if *prior.Observation == observation {
				return prior, nil
			}
			if !canAdvanceInterrupt(prior.Observation.Outcome, observation.Outcome) {
				return InterruptState{}, errors.New("agent interrupt observation differs")
			}
		}
		turn := prior.Request.AgentTurn
		activity := Activity{AgentID: turn.AgentID, Sequence: nextActivitySequence(current, turn.AgentID), Kind: "interrupt", TurnID: turn.TurnID, TurnSequence: turn.TurnSequence, InterruptID: requestID, Interrupt: observation.Outcome}
		_, appendErr := journal.Append(s.journalPath, "agentcontrol.interrupt-observed", interruptObservedEvent{Version: 1, RequestID: requestID, Observation: observation, Activity: activity}, func(events []journal.Event) error {
			_, replayErr := Replay(events)
			return replayErr
		})
		if appendErr == nil {
			copy := observation
			prior.Observation = &copy
			return prior, nil
		}
	}
	return InterruptState{}, errors.New("agent interrupt observation contention")
}

func replayInterrupt(state *Snapshot, event journal.Event) error {
	switch event.Kind {
	case "agentcontrol.interrupt-requested":
		var payload interruptRequestedEvent
		if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 || validateInterruptRequest(payload.Request) != nil {
			return errors.New("invalid agent interrupt request")
		}
		requestID, err := payload.Request.ID()
		if err != nil || payload.Request.TreeID != state.TreeID || payload.RequestID != requestID || state.Interrupts[requestID].RequestID != "" || validateInterruptActivity(*state, payload.Activity, payload.Request, requestID, InterruptRequested) != nil {
			return errors.New("invalid agent interrupt request")
		}
		for _, prior := range state.Interrupts {
			if prior.Request.AgentTurn.TurnID == payload.Request.AgentTurn.TurnID {
				return errors.New("duplicate agent turn interrupt")
			}
		}
		state.Interrupts[requestID] = InterruptState{RequestID: requestID, Request: payload.Request}
		state.Activities = append(state.Activities, payload.Activity)
		state.Latest[payload.Activity.AgentID] = payload.Activity
	case "agentcontrol.interrupt-observed":
		var payload interruptObservedEvent
		if canonical.Decode(event.Payload, &payload) != nil || payload.Version != 1 {
			return errors.New("invalid agent interrupt observation")
		}
		prior, ok := state.Interrupts[payload.RequestID]
		if !ok || validateInterruptObservation(payload.Observation) != nil || prior.Observation != nil && !canAdvanceInterrupt(prior.Observation.Outcome, payload.Observation.Outcome) || validateInterruptActivity(*state, payload.Activity, prior.Request, payload.RequestID, payload.Observation.Outcome) != nil {
			return errors.New("invalid agent interrupt observation")
		}
		copy := payload.Observation
		prior.Observation = &copy
		state.Interrupts[payload.RequestID] = prior
		state.Activities = append(state.Activities, payload.Activity)
		state.Latest[payload.Activity.AgentID] = payload.Activity
	default:
		return errors.New("unknown agent interrupt event")
	}
	return nil
}

func validateInterruptActivity(state Snapshot, activity Activity, request InterruptRequest, requestID string, outcome InterruptOutcome) error {
	if validateActivity(state, activity) != nil || activity.Kind != "interrupt" || activity.AgentID != request.AgentTurn.AgentID || activity.TurnID != request.AgentTurn.TurnID || activity.TurnSequence != request.AgentTurn.TurnSequence || activity.InterruptID != requestID || activity.Interrupt != outcome {
		return errors.New("invalid agent interrupt activity")
	}
	return nil
}

func validateInterruptRequest(request InterruptRequest) error {
	turn := request.AgentTurn
	if request.Version != 1 || safepath.RequireDigest(request.TreeID) != nil || safepath.RequireDigest(request.ScheduleID) != nil || safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnSequence < 1 || safepath.RequireDigest(request.InvocationID) != nil || safepath.RequireDigest(request.ClaimID) != nil || safepath.RequireDigest(request.ControllerHead) != nil || !boundedInterruptText(request.Actor, 256) || !boundedInterruptText(request.Nonce, 128) {
		return errors.New("invalid agent interrupt request")
	}
	return nil
}

func validateInterruptObservation(observation InterruptObservation) error {
	if observation.Version != 1 || !validInterruptOutcome(observation.Outcome) || observation.Outcome == InterruptRequested || safepath.RequireDigest(observation.Evidence) != nil || safepath.RequireDigest(observation.ControllerHead) != nil {
		return errors.New("invalid agent interrupt observation")
	}
	return nil
}

func validInterruptOutcome(outcome InterruptOutcome) bool {
	switch outcome {
	case InterruptRequested, InterruptSignalDelivered, InterruptConfirmedLocalStop, InterruptUnknown, InterruptAlreadyTerminal:
		return true
	default:
		return false
	}
}

func canAdvanceInterrupt(from, to InterruptOutcome) bool {
	return from == InterruptSignalDelivered && (to == InterruptConfirmedLocalStop || to == InterruptUnknown || to == InterruptAlreadyTerminal) || from == InterruptUnknown && (to == InterruptConfirmedLocalStop || to == InterruptAlreadyTerminal)
}

func boundedInterruptText(value string, maximum int) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value && len(value) <= maximum && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}

func dynamicTurn(snapshot taskscheduler.Snapshot, turnID string) (taskscheduler.DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.TurnID == turnID {
			return dynamic, true
		}
	}
	return taskscheduler.DynamicTask{}, false
}
