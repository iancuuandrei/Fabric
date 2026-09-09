package agentcontrol

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
)

// SpawnPlan is a controller-derived child and initial-turn admission. Callers
// must derive authority, invocation, operation, paths, and dependencies from
// trusted run configuration rather than model input.
type SpawnPlan struct {
	ParentAgentID string                 `json:"parent_agent_id"`
	Name          string                 `json:"name"`
	Role          string                 `json:"role"`
	Authority     agenttree.Authority    `json:"authority"`
	InvocationID  string                 `json:"invocation_id"`
	ContextSHA256 string                 `json:"context_sha256"`
	TurnID        string                 `json:"turn_id"`
	Nonce         string                 `json:"nonce"`
	Task          taskscheduler.TaskSpec `json:"task"`
}

// ScheduledService binds AgentControl mutations to one pre-existing scheduler.
type ScheduledService struct {
	*Service
	schedulerPath string
	scheduleID    string
}

// BindScheduled opens an AgentControl service and fixes the scheduler journal
// used for every later spawn or follow-up admission.
func BindScheduled(treePath, controlPath, schedulerPath string) (*ScheduledService, error) {
	if !exactAbsolutePath(schedulerPath) || schedulerPath == treePath || schedulerPath == controlPath {
		return nil, errors.New("invalid agent scheduler path")
	}
	scheduled, err := taskscheduler.Inspect(schedulerPath)
	if err != nil || scheduled.Definition == nil || safepath.RequireDigest(scheduled.ScheduleID) != nil {
		return nil, errors.Join(errors.New("agent scheduler unavailable"), err)
	}
	service, err := Bind(treePath, controlPath)
	if err != nil {
		return nil, err
	}
	return &ScheduledService{Service: service, schedulerPath: schedulerPath, scheduleID: scheduled.ScheduleID}, nil
}

// ExplorerSpawnTurnID derives the initial turn before its invocation and node
// are created, avoiding an identity cycle while binding exact caller intent.
func ExplorerSpawnTurnID(treeID, parentAgentID, name, question, nonce string) (string, error) {
	if safepath.RequireDigest(treeID) != nil || safepath.RequireDigest(parentAgentID) != nil || name == "" || question == "" || nonce == "" {
		return "", errors.New("invalid explorer spawn turn intent")
	}
	return canonical.Hash("harness.agentcontrol-explorer-spawn-turn.v1", struct {
		TreeID        string `json:"tree_id"`
		ParentAgentID string `json:"parent_agent_id"`
		Name          string `json:"name"`
		Question      string `json:"question"`
		Nonce         string `json:"nonce"`
	}{treeID, parentAgentID, name, question, nonce})
}

// ExplorerFollowUpTurnID derives one later turn from the exact waking message.
func ExplorerFollowUpTurnID(treeID string, request MessageRequest) (string, error) {
	if safepath.RequireDigest(treeID) != nil || safepath.RequireDigest(request.FromAgentID) != nil || safepath.RequireDigest(request.ToAgentID) != nil || request.Nonce == "" || request.Body == "" {
		return "", errors.New("invalid explorer follow-up turn intent")
	}
	return canonical.Hash("harness.agentcontrol-explorer-followup-turn.v1", struct {
		TreeID      string `json:"tree_id"`
		FromAgentID string `json:"from_agent_id"`
		ToAgentID   string `json:"to_agent_id"`
		Nonce       string `json:"nonce"`
		Question    string `json:"question"`
	}{treeID, request.FromAgentID, request.ToAgentID, request.Nonce, request.Body})
}

// Spawn commits one exact child and appends its initial turn to TaskScheduler.
// A crash between those journal writes leaves a visible queued child; replaying
// the same plan finishes the missing append without creating another node.
func (s *ScheduledService) Spawn(ctx context.Context, plan SpawnPlan) (agenttree.Node, taskscheduler.DynamicTask, error) {
	if err := s.validateScheduled(ctx); err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	if err := validateSpawnPlan(s.treeID, plan); err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil || tree.TreeID != s.treeID {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, errors.Join(errors.New("agent tree identity changed"), err)
	}
	parent, ok := nodeByID(tree, plan.ParentAgentID)
	if !ok {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, errors.New("agent tree parent unavailable")
	}
	spec := agenttree.NodeSpec{ParentAgentID: parent.AgentID, Name: plan.Name, Role: plan.Role, Authority: plan.Authority, InvocationID: plan.InvocationID, ContextSHA256: plan.ContextSHA256}
	node, err := ensureScheduledChild(s.treePath, s.treeID, tree, parent, spec)
	if err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	turnID := plan.TurnID
	task := plan.Task
	dynamic := taskscheduler.DynamicTask{Task: task, ParentAgentID: parent.AgentID, AgentID: node.AgentID, TurnID: turnID}
	currentSchedule, err := taskscheduler.Inspect(s.schedulerPath)
	if err != nil {
		return node, dynamic, err
	}
	for _, existing := range currentSchedule.Dynamic {
		if existing.AgentID == node.AgentID && existing.TurnSequence == 1 && existing.TurnID != turnID {
			return node, dynamic, errors.New("agent already has an initial turn")
		}
	}
	persisted, err := taskscheduler.AddTask(s.schedulerPath, dynamic)
	if err != nil {
		return node, dynamic, err
	}
	stored, ok := scheduledTurn(persisted, turnID)
	if !ok {
		return node, dynamic, errors.New("scheduled agent turn unavailable after append")
	}
	return node, stored, nil
}

// FollowUpTurn stores one waking message and appends its exact next turn to
// TaskScheduler. It does not dispatch or run inference inside AgentControl.
func (s *ScheduledService) FollowUpTurn(ctx context.Context, request MessageRequest, task taskscheduler.TaskSpec) (MessageRecord, taskscheduler.DynamicTask, error) {
	if err := s.validateScheduled(ctx); err != nil {
		return MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	tree, err := agenttree.Inspect(s.treePath)
	if err != nil || tree.TreeID != s.treeID {
		return MessageRecord{}, taskscheduler.DynamicTask{}, errors.Join(errors.New("agent tree identity changed"), err)
	}
	node, ok := nodeByID(tree, request.ToAgentID)
	if !ok || node.ParentAgentID == "" || node.Role != "explorer" || task.Operation != taskscheduler.OperationExplorer || task.Input != request.Body || safepath.RequireDigest(task.ID) != nil || task.RunID != s.treeID || task.InvocationID == node.InvocationID || !taskMatchesNode(task, node) {
		return MessageRecord{}, taskscheduler.DynamicTask{}, errors.New("invalid agent follow-up turn")
	}
	expectedTurnID, err := ExplorerFollowUpTurnID(s.treeID, request)
	if err != nil || task.ID != expectedTurnID {
		return MessageRecord{}, taskscheduler.DynamicTask{}, errors.Join(errors.New("agent follow-up turn identity mismatch"), err)
	}
	record, err := s.FollowUp(ctx, request)
	if err != nil {
		return MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	turnID := task.ID
	dynamic := taskscheduler.DynamicTask{Task: task, ParentAgentID: node.ParentAgentID, AgentID: node.AgentID, TurnID: turnID}
	persisted, err := taskscheduler.AddTask(s.schedulerPath, dynamic)
	if err != nil {
		return record, dynamic, err
	}
	stored, ok := scheduledTurn(persisted, turnID)
	if !ok {
		return record, dynamic, errors.New("scheduled agent turn unavailable after append")
	}
	return record, stored, nil
}

func (s *ScheduledService) validateScheduled(ctx context.Context) error {
	if s == nil || s.Service == nil || ctx == nil || ctx.Err() != nil || !exactAbsolutePath(s.schedulerPath) || safepath.RequireDigest(s.scheduleID) != nil {
		return errors.Join(errors.New("invalid scheduled agent control service"), contextError(ctx))
	}
	current, err := taskscheduler.Inspect(s.schedulerPath)
	if err != nil || current.ScheduleID != s.scheduleID {
		return errors.Join(errors.New("agent scheduler identity changed"), err)
	}
	return nil
}

func validateSpawnPlan(treeID string, plan SpawnPlan) error {
	if safepath.RequireDigest(treeID) != nil || safepath.RequireDigest(plan.ParentAgentID) != nil || safepath.RequireDigest(plan.InvocationID) != nil || safepath.RequireDigest(plan.ContextSHA256) != nil || safepath.RequireDigest(plan.TurnID) != nil || plan.Task.ID != plan.TurnID || plan.Task.RunID != treeID || plan.Task.InvocationID != plan.InvocationID || !exactAbsolutePath(plan.Task.ControllerPath) || len(plan.Nonce) < 1 || len(plan.Nonce) > 128 || strings.TrimSpace(plan.Nonce) != plan.Nonce || !utf8.ValidString(plan.Nonce) || strings.IndexByte(plan.Nonce, 0) >= 0 {
		return errors.New("invalid agent spawn plan")
	}
	if plan.Role != "explorer" || plan.Task.Operation != taskscheduler.OperationExplorer || !roleTaskAuthority(plan.Role, plan.Task.Operation, plan.Authority) {
		return errors.New("agent spawn role authority mismatch")
	}
	expectedTurnID, err := ExplorerSpawnTurnID(treeID, plan.ParentAgentID, plan.Name, plan.Task.Input, plan.Nonce)
	if err != nil || expectedTurnID != plan.TurnID {
		return errors.Join(errors.New("agent spawn turn identity mismatch"), err)
	}
	return nil
}

func taskMatchesNode(task taskscheduler.TaskSpec, node agenttree.Node) bool {
	return task.RunID != "" && task.InvocationID != "" && task.ControllerPath != "" && roleTaskAuthority(node.Role, task.Operation, node.Authority)
}

func roleTaskAuthority(role string, operation taskscheduler.Operation, authority agenttree.Authority) bool {
	switch role {
	case "explorer":
		return operation == taskscheduler.OperationExplorer && authority == agenttree.AuthorityReadOnly
	case "reviewer":
		return operation == taskscheduler.OperationReviewer && authority == agenttree.AuthorityReadOnly
	case "writer", "fixer":
		return operation == taskscheduler.OperationWriter && authority == agenttree.AuthorityScopedWriter
	default:
		return false
	}
}

func ensureScheduledChild(treePath, treeID string, tree agenttree.Snapshot, parent agenttree.Node, spec agenttree.NodeSpec) (agenttree.Node, error) {
	wantedPath, err := agenttree.ChildPath(parent.Path, spec.Name)
	if err != nil {
		return agenttree.Node{}, err
	}
	for _, node := range tree.Nodes {
		if node.Path == wantedPath {
			if exactScheduledNode(node, parent.AgentID, wantedPath, spec) {
				return node, nil
			}
			return agenttree.Node{}, errors.New("agent child path already bound")
		}
	}
	for _, reservation := range tree.Reservations {
		if reservation.Node.Path == wantedPath {
			if !exactScheduledNode(reservation.Node, parent.AgentID, wantedPath, spec) {
				return agenttree.Node{}, errors.New("agent child reservation differs")
			}
			return agenttree.CommitChild(treePath, reservation.ReservationID)
		}
	}
	reservation, err := agenttree.ReserveChild(treePath, treeID, spec)
	if err != nil {
		return agenttree.Node{}, err
	}
	return agenttree.CommitChild(treePath, reservation.ReservationID)
}

func exactScheduledNode(node agenttree.Node, parentID, wantedPath string, spec agenttree.NodeSpec) bool {
	return node.ParentAgentID == parentID && node.Path == wantedPath && node.Name == spec.Name && node.Role == spec.Role && node.Authority == spec.Authority && node.InvocationID == spec.InvocationID && node.ContextSHA256 == spec.ContextSHA256
}

func nodeByID(tree agenttree.Snapshot, id string) (agenttree.Node, bool) {
	for _, node := range tree.Nodes {
		if node.AgentID == id {
			return node, true
		}
	}
	return agenttree.Node{}, false
}

func scheduledTurn(snapshot taskscheduler.Snapshot, turnID string) (taskscheduler.DynamicTask, bool) {
	for _, dynamic := range snapshot.Dynamic {
		if dynamic.TurnID == turnID {
			return dynamic, true
		}
	}
	return taskscheduler.DynamicTask{}, false
}
