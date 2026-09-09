package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/taskscheduler"
)

// SpawnExplorerAgent derives a read-only explorer child and its initial turn
// from the current controller configuration, then appends it to TaskScheduler.
func SpawnExplorerAgent(ctx context.Context, controllerPath, schedulerPath, parentAgentID, name, question, nonce string) (agenttree.Node, taskscheduler.DynamicTask, error) {
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	turnID, err := agentcontrol.ExplorerSpawnTurnID(snapshot.RunID, parentAgentID, name, question, nonce)
	if err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	_, task, contextHash, err := prepareExplorerAgentTurn(ctx, controllerPath, question, turnID)
	if err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	service, err := agentcontrol.BindScheduled(controllerPath+".agent-tree", controllerPath+".agent-control", schedulerPath)
	if err != nil {
		return agenttree.Node{}, taskscheduler.DynamicTask{}, err
	}
	return service.Spawn(ctx, agentcontrol.SpawnPlan{
		ParentAgentID: parentAgentID,
		Name:          name,
		Role:          "explorer",
		Authority:     agenttree.AuthorityReadOnly,
		InvocationID:  task.InvocationID,
		ContextSHA256: contextHash,
		TurnID:        turnID,
		Nonce:         nonce,
		Task:          task,
	})
}

// FollowUpExplorerAgent stores a waking message and appends a new explorer turn
// for an existing child. TaskScheduler retains FIFO and dispatch authority.
func FollowUpExplorerAgent(ctx context.Context, controllerPath, schedulerPath string, request agentcontrol.MessageRequest, question string) (agentcontrol.MessageRecord, taskscheduler.DynamicTask, error) {
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		return agentcontrol.MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	turnID, err := agentcontrol.ExplorerFollowUpTurnID(snapshot.RunID, request)
	if err != nil {
		return agentcontrol.MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	_, task, _, err := prepareExplorerAgentTurn(ctx, controllerPath, question, turnID)
	if err != nil {
		return agentcontrol.MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	service, err := agentcontrol.BindScheduled(controllerPath+".agent-tree", controllerPath+".agent-control", schedulerPath)
	if err != nil {
		return agentcontrol.MessageRecord{}, taskscheduler.DynamicTask{}, err
	}
	return service.FollowUpTurn(ctx, request, task)
}

func prepareExplorerAgentTurn(ctx context.Context, controllerPath, question, turnID string) (Snapshot, taskscheduler.TaskSpec, string, error) {
	if ctx == nil || ctx.Err() != nil {
		if ctx == nil {
			return Snapshot{}, taskscheduler.TaskSpec{}, "", errors.New("agent scheduling context required")
		}
		return Snapshot{}, taskscheduler.TaskSpec{}, "", errors.Join(errors.New("agent scheduling context required"), ctx.Err())
	}
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", err
	}
	if lifecycleStatus(snapshot) != LifecycleActive {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", errors.New("agent scheduling requires active run")
	}
	if err := requireCurrentHostAdmission(ctx, snapshot); err != nil {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", err
	}
	task, err := PrepareDynamicScheduledTask(controllerPath, taskscheduler.OperationExplorer, question, turnID)
	if err != nil {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", err
	}
	base, err := explorerInvocation(snapshot, question)
	if err != nil {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", err
	}
	invocation, err := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, turnID)
	if err != nil || invocation.ID != task.InvocationID {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", errors.Join(errors.New("explorer turn invocation changed"), err)
	}
	contextHash, err := access.InputID(invocation.Input)
	if err != nil {
		return Snapshot{}, taskscheduler.TaskSpec{}, "", err
	}
	return snapshot, task, contextHash, nil
}
