// Package taskscheduler coordinates durable DAG selection across controller runs.
// It owns scheduling metadata only; controllers retain dispatch authority and
// taskpool retains capacity authority.
package taskscheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskpool"
)

// Operation names a finite controller role entry point.
type Operation string

const (
	// OperationPlanner schedules the controller's exact planner invocation.
	OperationPlanner Operation = "planner"
	// OperationExplorer schedules one exact bounded explorer question.
	OperationExplorer Operation = "explorer"
	// OperationWriter schedules the controller's exact writer invocation.
	OperationWriter Operation = "writer"
	// OperationReviewer schedules the controller's exact reviewer invocation.
	OperationReviewer Operation = "reviewer"
)

// Status is a replayed scheduling disposition.
type Status string

const (
	// StatusReady is eligible when dependency and controller evidence agree.
	StatusReady Status = "ready"
	// StatusBlocked awaits successful dependency evidence.
	StatusBlocked Status = "blocked"
	// StatusClaimed has a durable scheduler selection but no observation yet.
	StatusClaimed Status = "claimed"
	// StatusParked closed a claim before runtime work on a finite condition.
	StatusParked Status = "parked"
	// StatusRunning has exact controller admission and active work.
	StatusRunning Status = "running"
	// StatusUnknown retains admission and capacity pending reconciliation.
	StatusUnknown Status = "unknown"
	// StatusSucceeded has exact successful controller evidence.
	StatusSucceeded Status = "succeeded"
	// StatusFailed has exact terminal failure evidence.
	StatusFailed Status = "failed"
	// StatusCancelled has exact terminal cancellation evidence.
	StatusCancelled Status = "cancelled"
	// StatusPaused is controller preflight evidence that permits parking only.
	StatusPaused Status = "paused"
)

// ParkReason is a finite reason proven before new runtime work.
type ParkReason string

const (
	// ParkCapacity records atomic TaskPool capacity denial.
	ParkCapacity ParkReason = "capacity"
	// ParkLifecycle records an inactive controller lifecycle.
	ParkLifecycle ParkReason = "lifecycle"
	// ParkNoEffect records another explicitly proven pre-effect stop.
	ParkNoEffect ParkReason = "no-effect"
)

// TaskSpec binds one DAG node to an exact finite controller operation.
type TaskSpec struct {
	ID             string    `json:"id"`
	DependsOn      []string  `json:"depends_on,omitempty"`
	RunID          string    `json:"run_id"`
	ControllerPath string    `json:"controller_path"`
	Operation      Operation `json:"operation"`
	Input          string    `json:"input,omitempty"`
	InvocationID   string    `json:"invocation_id"`
}

// Definition is the immutable schedule input.
type Definition struct {
	Version int        `json:"version"`
	Nonce   string     `json:"nonce"`
	Tasks   []TaskSpec `json:"tasks"`
}

// DynamicTask binds a runtime-created agent turn into the same durable queue.
// TurnSequence is assigned by AddTask and is monotonic per agent.
type DynamicTask struct {
	Task          TaskSpec `json:"task"`
	ParentAgentID string   `json:"parent_agent_id"`
	AgentID       string   `json:"agent_id"`
	TurnID        string   `json:"turn_id"`
	TurnSequence  int      `json:"turn_sequence"`
}

// AgentTurnBinding is the exact AgentTree and mailbox identity carried into a
// dynamic scheduling claim.
type AgentTurnBinding struct {
	ParentAgentID string `json:"parent_agent_id"`
	AgentID       string `json:"agent_id"`
	TurnID        string `json:"turn_id"`
	TurnSequence  int    `json:"turn_sequence"`
}

// ID returns the exact schedule identity.
func (d Definition) ID() (string, error) {
	if err := d.validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.task-schedule.v1", d)
}

func (d Definition) validate() error {
	if d.Version != 1 || len(d.Nonce) == 0 || len(d.Nonce) > 512 || strings.TrimSpace(d.Nonce) != d.Nonce || !utf8.ValidString(d.Nonce) {
		return errors.New("invalid schedule definition")
	}
	graphTasks := make([]taskpool.Task, len(d.Tasks))
	seen := map[string]bool{}
	for i, task := range d.Tasks {
		if seen[task.ID] {
			return errors.New("duplicate scheduled task")
		}
		seen[task.ID] = true
		if safepath.RequireDigest(task.RunID) != nil || safepath.RequireDigest(task.InvocationID) != nil || !filepath.IsAbs(task.ControllerPath) || filepath.Clean(task.ControllerPath) != task.ControllerPath {
			return errors.New("invalid scheduled task identity")
		}
		if len(task.Input) > 4096 || !utf8.ValidString(task.Input) || strings.IndexByte(task.Input, 0) >= 0 {
			return errors.New("invalid scheduled task input")
		}
		switch task.Operation {
		case OperationExplorer:
			if len(task.Input) == 0 || strings.TrimSpace(task.Input) != task.Input {
				return errors.New("explorer input required")
			}
		case OperationPlanner, OperationWriter, OperationReviewer:
			if task.Input != "" {
				return errors.New("operation does not accept input")
			}
		default:
			return errors.New("invalid scheduled operation")
		}
		graphTasks[i] = taskpool.Task{ID: task.ID, DependsOn: append([]string(nil), task.DependsOn...)}
	}
	_, err := taskpool.NewGraph(graphTasks)
	return err
}

// Claim is one durable selection generation. Its controller head prevents a
// stale scheduling decision from crossing a concurrent controller mutation.
type Claim struct {
	Version        int               `json:"version"`
	ScheduleID     string            `json:"schedule_id"`
	Task           TaskSpec          `json:"task"`
	Generation     int               `json:"generation"`
	ControllerHead string            `json:"controller_head"`
	AgentTurn      *AgentTurnBinding `json:"agent_turn,omitempty"`
}

// ID returns the identity retained across crash recovery.
func (c Claim) ID() (string, error) {
	if c.Version != 1 || safepath.RequireDigest(c.ScheduleID) != nil || c.Generation < 1 || safepath.RequireDigest(c.ControllerHead) != nil {
		return "", errors.New("invalid scheduler claim")
	}
	if c.AgentTurn != nil && (safepath.RequireDigest(c.AgentTurn.ParentAgentID) != nil || safepath.RequireDigest(c.AgentTurn.AgentID) != nil || safepath.RequireDigest(c.AgentTurn.TurnID) != nil || c.AgentTurn.TurnID != c.Task.ID || c.AgentTurn.TurnSequence < 1) {
		return "", errors.New("invalid scheduler agent turn")
	}
	return canonical.Hash("harness.task-scheduler-claim.v1", c)
}

// Evidence is a controller projection for the exact run and invocation.
type Evidence struct {
	RunID          string `json:"run_id"`
	InvocationID   string `json:"invocation_id"`
	ControllerHead string `json:"controller_head"`
	AdmissionID    string `json:"admission_id,omitempty"`
	Status         Status `json:"status"`
}

// ProbeRequest carries the exact static task or dynamic agent-turn binding to
// the controller's observation-only adapter seam.
type ProbeRequest struct {
	Task      TaskSpec          `json:"task"`
	AgentTurn *AgentTurnBinding `json:"agent_turn,omitempty"`
}

func (e Evidence) validate(task TaskSpec) error {
	if e.RunID != task.RunID || e.InvocationID != task.InvocationID || safepath.RequireDigest(e.ControllerHead) != nil {
		return errors.New("scheduler evidence identity mismatch")
	}
	if e.AdmissionID != "" && safepath.RequireDigest(e.AdmissionID) != nil {
		return errors.New("invalid admission identity")
	}
	switch e.Status {
	case StatusReady, StatusPaused:
		if e.AdmissionID != "" {
			return errors.New("unadmitted evidence has admission")
		}
	case StatusRunning, StatusUnknown, StatusSucceeded, StatusFailed, StatusCancelled:
		if safepath.RequireDigest(e.AdmissionID) != nil {
			return errors.New("admitted evidence required")
		}
	default:
		return errors.New("invalid scheduler evidence status")
	}
	return nil
}

// Adapter is the only scheduler seam to controller-owned dispatch and observation.
type Adapter interface {
	Probe(context.Context, ProbeRequest) (Evidence, error)
	Dispatch(context.Context, Claim) (Evidence, error)
	Reconcile(context.Context, Claim) (Evidence, error)
}

// ParkError proves a finite pre-effect reason for parking a claim.
type ParkError struct{ Reason ParkReason }

// Error returns a bounded human-readable parking reason.
func (e *ParkError) Error() string { return "scheduled task parked: " + string(e.Reason) }

// TaskState is replayed scheduling metadata, not controller state authority.
type TaskState struct {
	Status     Status     `json:"status"`
	Generation int        `json:"generation"`
	Claim      *Claim     `json:"claim,omitempty"`
	Evidence   *Evidence  `json:"evidence,omitempty"`
	ParkReason ParkReason `json:"park_reason,omitempty"`
	BlockedBy  string     `json:"blocked_by,omitempty"`
}

// Snapshot is reconstructed from the complete scheduler journal.
type Snapshot struct {
	ScheduleID string               `json:"schedule_id"`
	Definition *Definition          `json:"definition,omitempty"`
	Dynamic    []DynamicTask        `json:"dynamic,omitempty"`
	Tasks      map[string]TaskState `json:"tasks"`
}

// Decision describes the one scheduling action attempted by Tick or RecoverClaim.
type Decision struct {
	ClaimID string `json:"claim_id,omitempty"`
	TaskID  string `json:"task_id,omitempty"`
	Status  Status `json:"status,omitempty"`
}

type boundEvent struct {
	Definition Definition `json:"definition"`
	ScheduleID string     `json:"schedule_id"`
}
type claimEvent struct {
	Claim   Claim  `json:"claim"`
	ClaimID string `json:"claim_id"`
}
type parkEvent struct {
	ClaimID string     `json:"claim_id"`
	Reason  ParkReason `json:"reason"`
}
type observedEvent struct {
	ClaimID  string   `json:"claim_id"`
	Evidence Evidence `json:"evidence"`
}
type addedEvent struct {
	Dynamic DynamicTask `json:"dynamic"`
}
type uncertainEvent struct {
	ClaimID        string `json:"claim_id"`
	ControllerHead string `json:"controller_head"`
}

// Bind creates the immutable schedule or verifies an exact existing binding.
func Bind(path string, definition Definition) (Snapshot, error) {
	id, err := definition.ID()
	if err != nil {
		return Snapshot{}, err
	}
	current, err := Inspect(path)
	if err != nil {
		return Snapshot{}, err
	}
	if current.Definition != nil {
		if current.ScheduleID != id {
			return Snapshot{}, errors.New("schedule definition changed")
		}
		return current, nil
	}
	_, err = journal.Append(path, "schedule.bound", boundEvent{Definition: definition, ScheduleID: id}, func(events []journal.Event) error { _, replayErr := Replay(events); return replayErr })
	if err != nil {
		return Snapshot{}, err
	}
	return Inspect(path)
}

// Inspect replays the durable schedule without dispatching work.
func Inspect(path string) (Snapshot, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, err
	}
	return Replay(events)
}

// AddTask appends one controller-derived child or follow-up turn. Exact TurnID
// retries are idempotent; a changed retry or duplicate task identity rejects.
func AddTask(path string, dynamic DynamicTask) (Snapshot, error) {
	if dynamic.TurnSequence != 0 {
		return Snapshot{}, errors.New("turn sequence is scheduler assigned")
	}
	for attempts := 0; attempts < 3; attempts++ {
		snapshot, err := Inspect(path)
		if err != nil || snapshot.Definition == nil {
			return Snapshot{}, errors.Join(err, errors.New("schedule not bound"))
		}
		if existing, ok := dynamicByTurn(snapshot, dynamic.TurnID); ok {
			wanted := dynamic
			wanted.TurnSequence = existing.TurnSequence
			if equalDynamic(existing, wanted) {
				return snapshot, nil
			}
			return Snapshot{}, errors.New("dynamic turn identity changed")
		}
		if err := validateDynamic(snapshot, dynamic); err != nil {
			return Snapshot{}, err
		}
		for _, existing := range snapshot.Dynamic {
			if existing.AgentID == dynamic.AgentID && existing.TurnSequence >= dynamic.TurnSequence {
				dynamic.TurnSequence = existing.TurnSequence + 1
			}
		}
		if dynamic.TurnSequence == 0 {
			dynamic.TurnSequence = 1
		}
		if err := appendValidated(path, "task.added", addedEvent{Dynamic: dynamic}); err == nil {
			return Inspect(path)
		}
		// A concurrent append may have assigned this agent's next sequence.
	}
	return Snapshot{}, errors.New("dynamic task changed concurrently")
}

// Replay validates scheduling records and deterministically derives dependency states.
func Replay(events []journal.Event) (Snapshot, error) {
	snapshot := Snapshot{Tasks: map[string]TaskState{}}
	for i, event := range events {
		switch event.Kind {
		case "schedule.bound":
			var payload boundEvent
			if i != 0 || canonical.Decode(event.Payload, &payload) != nil {
				return Snapshot{}, errors.New("invalid schedule binding")
			}
			id, err := payload.Definition.ID()
			if err != nil || id != payload.ScheduleID {
				return Snapshot{}, errors.New("invalid schedule identity")
			}
			snapshot.ScheduleID, snapshot.Definition = id, &payload.Definition
			for _, task := range payload.Definition.Tasks {
				snapshot.Tasks[task.ID] = TaskState{}
			}
		case "task.added":
			if snapshot.Definition == nil {
				return Snapshot{}, errors.New("dynamic task before schedule binding")
			}
			var payload addedEvent
			if canonical.Decode(event.Payload, &payload) != nil || validateDynamic(snapshot, payload.Dynamic) != nil {
				return Snapshot{}, errors.New("invalid dynamic task")
			}
			expected := 1
			for _, existing := range snapshot.Dynamic {
				if existing.AgentID == payload.Dynamic.AgentID {
					if existing.ParentAgentID != payload.Dynamic.ParentAgentID {
						return Snapshot{}, errors.New("dynamic agent parent changed")
					}
					expected = existing.TurnSequence + 1
				}
			}
			if payload.Dynamic.TurnSequence != expected {
				return Snapshot{}, errors.New("invalid dynamic turn sequence")
			}
			snapshot.Dynamic = append(snapshot.Dynamic, payload.Dynamic)
			snapshot.Tasks[payload.Dynamic.Task.ID] = TaskState{}
		case "task.claimed":
			if snapshot.Definition == nil {
				return Snapshot{}, errors.New("claim before schedule binding")
			}
			var payload claimEvent
			if canonical.Decode(event.Payload, &payload) != nil {
				return Snapshot{}, errors.New("invalid claim event")
			}
			claimID, err := payload.Claim.ID()
			task, ok := snapshotTaskByID(snapshot, payload.Claim.Task.ID)
			state := snapshot.Tasks[payload.Claim.Task.ID]
			if err != nil || claimID != payload.ClaimID || !ok || !equalTask(task, payload.Claim.Task) || !claimTurnMatches(snapshot, payload.Claim) || payload.Claim.ScheduleID != snapshot.ScheduleID || payload.Claim.Generation != state.Generation+1 || !claimable(state.Status) {
				return Snapshot{}, errors.New("invalid task claim")
			}
			state = TaskState{Status: StatusClaimed, Generation: payload.Claim.Generation, Claim: &payload.Claim}
			snapshot.Tasks[task.ID] = state
		case "task.parked":
			var payload parkEvent
			if canonical.Decode(event.Payload, &payload) != nil || !validParkReason(payload.Reason) {
				return Snapshot{}, errors.New("invalid parked event")
			}
			id, state, ok := stateByClaim(snapshot, payload.ClaimID)
			if !ok || state.Status != StatusClaimed {
				return Snapshot{}, errors.New("park lacks claimed task")
			}
			state.Status, state.ParkReason = StatusParked, payload.Reason
			snapshot.Tasks[id] = state
		case "task.observed":
			var payload observedEvent
			if canonical.Decode(event.Payload, &payload) != nil {
				return Snapshot{}, errors.New("invalid observation event")
			}
			id, state, ok := stateByClaim(snapshot, payload.ClaimID)
			if !ok || state.Claim == nil || payload.Evidence.validate(state.Claim.Task) != nil || !canObserve(state.Status, payload.Evidence.Status) {
				return Snapshot{}, errors.New("invalid task observation")
			}
			state.Status, state.Evidence, state.ParkReason = payload.Evidence.Status, &payload.Evidence, ""
			snapshot.Tasks[id] = state
		case "task.uncertain":
			var payload uncertainEvent
			if canonical.Decode(event.Payload, &payload) != nil || safepath.RequireDigest(payload.ControllerHead) != nil {
				return Snapshot{}, errors.New("invalid uncertainty event")
			}
			id, state, ok := stateByClaim(snapshot, payload.ClaimID)
			if !ok || state.Status != StatusClaimed && state.Status != StatusRunning && state.Status != StatusUnknown {
				return Snapshot{}, errors.New("uncertainty lacks claimed task")
			}
			state.Status = StatusUnknown
			snapshot.Tasks[id] = state
		default:
			return Snapshot{}, errors.New("unknown scheduler event")
		}
		if snapshot.Definition != nil {
			deriveDependencies(&snapshot)
		}
	}
	return snapshot, nil
}

func taskByID(d Definition, id string) (TaskSpec, bool) {
	for _, task := range d.Tasks {
		if task.ID == id {
			return task, true
		}
	}
	return TaskSpec{}, false
}

func snapshotTaskByID(snapshot Snapshot, id string) (TaskSpec, bool) {
	if snapshot.Definition != nil {
		if task, ok := taskByID(*snapshot.Definition, id); ok {
			return task, true
		}
	}
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.Task.ID == id {
			return dynamic.Task, true
		}
	}
	return TaskSpec{}, false
}

func allTasks(snapshot Snapshot) []TaskSpec {
	tasks := append([]TaskSpec(nil), snapshot.Definition.Tasks...)
	for _, dynamic := range snapshot.Dynamic {
		tasks = append(tasks, dynamic.Task)
	}
	return tasks
}

func dynamicByTurn(snapshot Snapshot, turnID string) (DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.TurnID == turnID {
			return dynamic, true
		}
	}
	return DynamicTask{}, false
}

func equalDynamic(a, b DynamicTask) bool {
	ah, ae := canonical.Hash("harness.dynamic-scheduled-task.v1", a)
	bh, be := canonical.Hash("harness.dynamic-scheduled-task.v1", b)
	return ae == nil && be == nil && ah == bh
}

func validateDynamic(snapshot Snapshot, dynamic DynamicTask) error {
	if safepath.RequireDigest(dynamic.ParentAgentID) != nil || safepath.RequireDigest(dynamic.AgentID) != nil || safepath.RequireDigest(dynamic.TurnID) != nil || dynamic.Task.ID != dynamic.TurnID || dynamic.TurnSequence < 0 {
		return errors.New("invalid dynamic agent turn identity")
	}
	if _, exists := snapshot.Tasks[dynamic.Task.ID]; exists {
		return errors.New("duplicate dynamic task identity")
	}
	tasks := allTasks(snapshot)
	tasks = append(tasks, dynamic.Task)
	return (Definition{Version: 1, Nonce: "dynamic-validation", Tasks: tasks}).validate()
}
func equalTask(a, b TaskSpec) bool {
	ah, ae := canonical.Hash("harness.task-spec.v1", a)
	bh, be := canonical.Hash("harness.task-spec.v1", b)
	return ae == nil && be == nil && ah == bh
}
func stateByClaim(s Snapshot, claimID string) (string, TaskState, bool) {
	for id, state := range s.Tasks {
		if state.Claim != nil {
			observed, err := state.Claim.ID()
			if err == nil && observed == claimID {
				return id, state, true
			}
		}
	}
	return "", TaskState{}, false
}
func validParkReason(r ParkReason) bool {
	return r == ParkCapacity || r == ParkLifecycle || r == ParkNoEffect
}
func claimable(s Status) bool { return s == StatusReady || s == StatusParked }
func canObserve(from, to Status) bool {
	switch from {
	case StatusClaimed:
		return to == StatusRunning || to == StatusUnknown || to == StatusSucceeded || to == StatusFailed || to == StatusCancelled
	case StatusRunning:
		return to == StatusRunning || to == StatusUnknown || to == StatusSucceeded || to == StatusFailed || to == StatusCancelled
	case StatusUnknown:
		return to == StatusUnknown || to == StatusSucceeded || to == StatusFailed || to == StatusCancelled
	}
	return false
}

func deriveDependencies(s *Snapshot) {
	changed := true
	for changed {
		changed = false
		for _, task := range allTasks(*s) {
			state := s.Tasks[task.ID]
			if state.Generation > 0 || state.Status == StatusSucceeded || state.Status == StatusFailed || state.Status == StatusCancelled {
				continue
			}
			next, blockedBy := StatusReady, ""
			for _, dep := range task.DependsOn {
				depState := s.Tasks[dep]
				if depState.Status == StatusFailed || depState.Status == StatusCancelled {
					next, blockedBy = StatusFailed, dep
					break
				}
				if depState.Status != StatusSucceeded {
					next = StatusBlocked
				}
			}
			if state.Status != next || state.BlockedBy != blockedBy {
				state.Status, state.BlockedBy = next, blockedBy
				s.Tasks[task.ID] = state
				changed = true
			}
		}
	}
}

func appendValidated(path, kind string, payload any) error {
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error { _, replayErr := Replay(events); return replayErr })
	return err
}

func appendObservation(path, claimID string, evidence Evidence) error {
	err := appendValidated(path, "task.observed", observedEvent{ClaimID: claimID, Evidence: evidence})
	if err == nil {
		return nil
	}
	latest, inspectErr := Inspect(path)
	if inspectErr == nil {
		_, state, ok := stateByClaim(latest, claimID)
		if ok && state.Evidence != nil && *state.Evidence == evidence {
			return nil
		}
	}
	return errors.Join(err, inspectErr)
}

// Tick claims and attempts at most one ready or parked task.
func Tick(ctx context.Context, path string, adapter Adapter) (Decision, error) {
	if ctx == nil || adapter == nil {
		return Decision{}, errors.New("scheduler context and adapter required")
	}
	snapshot, err := Inspect(path)
	if err != nil || snapshot.Definition == nil {
		return Decision{}, errors.Join(err, errors.New("schedule not bound"))
	}
	for _, state := range snapshot.Tasks {
		if state.Status != StatusRunning && state.Status != StatusUnknown {
			continue
		}
		evidence, probeErr := adapter.Probe(ctx, ProbeRequest{Task: state.Claim.Task, AgentTurn: state.Claim.AgentTurn})
		if probeErr != nil {
			return Decision{}, probeErr
		}
		if err := evidence.validate(state.Claim.Task); err != nil {
			return Decision{}, err
		}
		if canObserve(state.Status, evidence.Status) && evidence.Status != state.Status {
			claimID := mustClaimID(state)
			if err := appendObservation(path, claimID, evidence); err != nil {
				return Decision{}, err
			}
			snapshot, err = Inspect(path)
			if err != nil {
				return Decision{}, err
			}
		}
	}
	candidates := orderedCandidates(snapshot)
	for _, task := range candidates {
		evidence, probeErr := adapter.Probe(ctx, ProbeRequest{Task: task, AgentTurn: agentTurnForTask(snapshot, task.ID)})
		if probeErr != nil {
			return Decision{}, probeErr
		}
		if err := evidence.validate(task); err != nil {
			return Decision{}, err
		}
		state := snapshot.Tasks[task.ID]
		claim := Claim{Version: 1, ScheduleID: snapshot.ScheduleID, Task: task, Generation: state.Generation + 1, ControllerHead: evidence.ControllerHead, AgentTurn: agentTurnForTask(snapshot, task.ID)}
		claimID, err := claim.ID()
		if err != nil {
			return Decision{}, err
		}
		if err = appendValidated(path, "task.claimed", claimEvent{Claim: claim, ClaimID: claimID}); err != nil {
			latest, inspectErr := Inspect(path)
			if inspectErr == nil {
				if latestState := latest.Tasks[task.ID]; latestState.Generation >= claim.Generation {
					return Decision{mustClaimID(latestState), task.ID, latestState.Status}, nil
				}
			}
			return Decision{}, errors.Join(err, inspectErr)
		}
		if evidence.Status == StatusPaused {
			err = appendValidated(path, "task.parked", parkEvent{ClaimID: claimID, Reason: ParkLifecycle})
			return Decision{claimID, task.ID, StatusParked}, err
		}
		if evidence.Status != StatusReady {
			if !canObserve(StatusClaimed, evidence.Status) {
				return Decision{}, errors.New("controller evidence cannot be adopted")
			}
			if err = appendObservation(path, claimID, evidence); err != nil {
				return Decision{}, err
			}
			return Decision{claimID, task.ID, evidence.Status}, nil
		}
		return dispatchClaim(ctx, path, claim, adapter)
	}
	return Decision{}, nil
}

// RecoverClaim observes one exact open claim and uses the adapter's receipt-only
// reconciliation seam. It never assumes executor quiescence or dispatches work.
func RecoverClaim(ctx context.Context, path, claimID string, adapter Adapter) (Decision, error) {
	if ctx == nil || adapter == nil || safepath.RequireDigest(claimID) != nil {
		return Decision{}, errors.New("invalid scheduler recovery")
	}
	snapshot, err := Inspect(path)
	if err != nil {
		return Decision{}, err
	}
	_, state, ok := stateByClaim(snapshot, claimID)
	if !ok {
		return Decision{}, errors.New("scheduler claim not found")
	}
	if state.Status == StatusClaimed || state.Status == StatusRunning || state.Status == StatusUnknown {
		evidence, probeErr := adapter.Probe(ctx, ProbeRequest{Task: state.Claim.Task, AgentTurn: state.Claim.AgentTurn})
		if probeErr != nil {
			return Decision{}, probeErr
		}
		if err := evidence.validate(state.Claim.Task); err != nil {
			return Decision{}, err
		}
		if evidence.Status == StatusSucceeded || evidence.Status == StatusFailed || evidence.Status == StatusCancelled {
			if err := appendObservation(path, claimID, evidence); err != nil {
				return Decision{}, err
			}
			return Decision{claimID, state.Claim.Task.ID, evidence.Status}, nil
		}
		if state.Status == StatusClaimed {
			// A crash can occur after Adapter.Dispatch begins but before the
			// scheduler records its observation. Recovery therefore cannot call
			// Dispatch again, even when the controller still appears ready.
			if err := appendValidated(path, "task.uncertain", uncertainEvent{ClaimID: claimID, ControllerHead: state.Claim.ControllerHead}); err != nil {
				return Decision{}, err
			}
			state.Status = StatusUnknown
			if evidence.Status == StatusUnknown {
				if err := appendObservation(path, claimID, evidence); err != nil {
					return Decision{}, err
				}
			}
		}
		if evidence.Status != StatusRunning && evidence.Status != StatusUnknown {
			if state.Status != StatusUnknown {
				return Decision{claimID, state.Claim.Task.ID, state.Status}, nil
			}
		}
		if evidence.Status != state.Status && canObserve(state.Status, evidence.Status) {
			if err := appendObservation(path, claimID, evidence); err != nil {
				return Decision{}, err
			}
			state.Status = evidence.Status
		}
		if state.Status == StatusRunning {
			return Decision{claimID, state.Claim.Task.ID, StatusRunning}, nil
		}
		// UNKNOWN recovery has a separate adapter seam which may only inspect
		// and complete already durable runtime evidence. It cannot dispatch.
		reconciled, reconcileErr := adapter.Reconcile(ctx, *state.Claim)
		if reconcileErr != nil {
			return Decision{claimID, state.Claim.Task.ID, StatusUnknown}, reconcileErr
		}
		if err := reconciled.validate(state.Claim.Task); err != nil {
			return Decision{}, err
		}
		if !canObserve(StatusUnknown, reconciled.Status) {
			return Decision{}, errors.New("invalid UNKNOWN reconciliation")
		}
		if reconciled.Status != StatusUnknown {
			if err := appendObservation(path, claimID, reconciled); err != nil {
				return Decision{}, err
			}
		}
		return Decision{claimID, state.Claim.Task.ID, reconciled.Status}, nil
	}
	return Decision{claimID, state.Claim.Task.ID, state.Status}, nil
}

func dispatchClaim(ctx context.Context, path string, claim Claim, adapter Adapter) (Decision, error) {
	claimID, _ := claim.ID()
	evidence, err := adapter.Dispatch(ctx, claim)
	if err != nil {
		var parked *ParkError
		if errors.As(err, &parked) && validParkReason(parked.Reason) {
			appendErr := appendValidated(path, "task.parked", parkEvent{ClaimID: claimID, Reason: parked.Reason})
			return Decision{claimID, claim.Task.ID, StatusParked}, appendErr
		}
		appendErr := appendValidated(path, "task.uncertain", uncertainEvent{ClaimID: claimID, ControllerHead: claim.ControllerHead})
		return Decision{claimID, claim.Task.ID, StatusUnknown}, errors.Join(err, appendErr)
	}
	if err := evidence.validate(claim.Task); err != nil || evidence.Status == StatusReady || evidence.Status == StatusPaused {
		return Decision{}, errors.Join(err, errors.New("dispatch returned nonterminal preflight evidence"))
	}
	current, inspectErr := Inspect(path)
	if inspectErr != nil {
		return Decision{}, inspectErr
	}
	_, currentState, ok := stateByClaim(current, claimID)
	if !ok {
		return Decision{}, errors.New("dispatched scheduler claim disappeared")
	}
	if !canObserve(currentState.Status, evidence.Status) {
		return Decision{claimID, claim.Task.ID, currentState.Status}, nil
	}
	if err := appendObservation(path, claimID, evidence); err != nil {
		return Decision{}, err
	}
	return Decision{claimID, claim.Task.ID, evidence.Status}, nil
}

func mustClaimID(state TaskState) string {
	if state.Claim == nil {
		return ""
	}
	id, _ := state.Claim.ID()
	return id
}
func orderedCandidates(snapshot Snapshot) []TaskSpec {
	tasks := allTasks(snapshot)
	graphTasks := make([]taskpool.Task, len(tasks))
	states := map[string]string{}
	for i, task := range tasks {
		graphTasks[i] = taskpool.Task{ID: task.ID, DependsOn: task.DependsOn}
		status := snapshot.Tasks[task.ID].Status
		if status == StatusParked || status == StatusReady || status == StatusBlocked {
			status = "pending"
		} else if status == StatusClaimed {
			status = StatusRunning
		}
		states[task.ID] = string(status)
	}
	graph, _ := taskpool.NewGraph(graphTasks)
	ready, _ := graph.Ready(states)
	candidates := make([]TaskSpec, 0, len(ready))
	for _, id := range ready {
		task, _ := snapshotTaskByID(snapshot, id)
		candidates = append(candidates, task)
	}
	candidates = filterAgentFIFO(snapshot, candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		si, sj := snapshot.Tasks[candidates[i].ID], snapshot.Tasks[candidates[j].ID]
		return si.Status == StatusParked && sj.Status != StatusParked
	})
	return candidates
}

func filterAgentFIFO(snapshot Snapshot, candidates []TaskSpec) []TaskSpec {
	oldest := map[string]DynamicTask{}
	for _, dynamic := range snapshot.Dynamic {
		state := snapshot.Tasks[dynamic.Task.ID]
		if state.Status == StatusSucceeded || state.Status == StatusFailed || state.Status == StatusCancelled {
			continue
		}
		prior, ok := oldest[dynamic.AgentID]
		if !ok || dynamic.TurnSequence < prior.TurnSequence {
			oldest[dynamic.AgentID] = dynamic
		}
	}
	result := make([]TaskSpec, 0, len(candidates))
	for _, candidate := range candidates {
		dynamic, ok := dynamicTaskByID(snapshot, candidate.ID)
		if !ok || oldest[dynamic.AgentID].Task.ID == candidate.ID {
			result = append(result, candidate)
		}
	}
	return result
}

func dynamicTaskByID(snapshot Snapshot, taskID string) (DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.Task.ID == taskID {
			return dynamic, true
		}
	}
	return DynamicTask{}, false
}

func agentTurnForTask(snapshot Snapshot, taskID string) *AgentTurnBinding {
	dynamic, ok := dynamicTaskByID(snapshot, taskID)
	if !ok {
		return nil
	}
	return &AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
}

func claimTurnMatches(snapshot Snapshot, claim Claim) bool {
	want := agentTurnForTask(snapshot, claim.Task.ID)
	if want == nil || claim.AgentTurn == nil {
		return want == nil && claim.AgentTurn == nil
	}
	return *want == *claim.AgentTurn
}
