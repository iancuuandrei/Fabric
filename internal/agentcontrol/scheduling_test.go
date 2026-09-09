package agentcontrol

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/taskscheduler"
)

func TestScheduledExplorerSpawnAndFollowUpUseOneFIFOQueue(t *testing.T) {
	service, treePath, root, _ := controlFixture(t)
	directory := filepath.Dir(treePath)
	schedulePath := filepath.Join(directory, "schedule.db")
	controllerPath := filepath.Join(directory, "controller.db")
	definition := taskscheduler.Definition{Version: 1, Nonce: "fixture", Tasks: []taskscheduler.TaskSpec{{ID: strings.Repeat("1", 64), RunID: strings.Repeat("a", 64), ControllerPath: controllerPath, Operation: taskscheduler.OperationPlanner, InvocationID: strings.Repeat("2", 64)}}}
	if _, err := taskscheduler.Bind(schedulePath, definition); err != nil {
		t.Fatal(err)
	}
	scheduled, err := BindScheduled(treePath, filepath.Join(directory, "agent-control.db"), schedulePath)
	if err != nil || scheduled.Service != service && scheduled.treeID != service.treeID {
		t.Fatal("bind scheduled", err)
	}
	plan := SpawnPlan{
		ParentAgentID: root.AgentID,
		Name:          "researcher",
		Role:          "explorer",
		Authority:     agenttree.AuthorityReadOnly,
		InvocationID:  strings.Repeat("3", 64),
		ContextSHA256: strings.Repeat("4", 64),
		Nonce:         "spawn-1",
		Task:          taskscheduler.TaskSpec{RunID: strings.Repeat("a", 64), ControllerPath: controllerPath, Operation: taskscheduler.OperationExplorer, Input: "inspect exact source", InvocationID: strings.Repeat("3", 64)},
	}
	plan.TurnID, err = ExplorerSpawnTurnID(strings.Repeat("a", 64), root.AgentID, plan.Name, plan.Task.Input, plan.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	plan.Task.ID = plan.TurnID
	node, initial, err := scheduled.Spawn(context.Background(), plan)
	if err != nil || node.ParentAgentID != root.AgentID || initial.AgentID != node.AgentID || initial.TurnSequence != 1 {
		t.Fatal("spawn", node, initial, err)
	}
	retryNode, retryTurn, err := scheduled.Spawn(context.Background(), plan)
	if err != nil || retryNode != node || retryTurn.TurnID != initial.TurnID {
		t.Fatal("spawn retry", retryNode, retryTurn, err)
	}
	nextInput := "continue with second source"
	followRequest := MessageRequest{FromAgentID: root.AgentID, ToAgentID: node.AgentID, Nonce: "follow-1", Body: nextInput}
	followTurnID, err := ExplorerFollowUpTurnID(strings.Repeat("a", 64), followRequest)
	if err != nil {
		t.Fatal(err)
	}
	message, next, err := scheduled.FollowUpTurn(context.Background(), followRequest, taskscheduler.TaskSpec{ID: followTurnID, RunID: strings.Repeat("a", 64), ControllerPath: controllerPath, Operation: taskscheduler.OperationExplorer, Input: nextInput, InvocationID: strings.Repeat("5", 64)})
	if err != nil || !message.Message.Wake || next.AgentID != node.AgentID || next.TurnID == initial.TurnID {
		t.Fatal("followup", message, next, err)
	}
	snapshot, err := taskscheduler.Inspect(schedulePath)
	if err != nil || len(snapshot.Dynamic) != 2 || snapshot.Dynamic[0].TurnSequence != 1 || snapshot.Dynamic[1].TurnSequence != 2 || snapshot.Dynamic[0].AgentID != snapshot.Dynamic[1].AgentID {
		t.Fatal("dynamic FIFO", snapshot.Dynamic, err)
	}
	afterFollowUpNode, afterFollowUpInitial, err := scheduled.Spawn(context.Background(), plan)
	if err != nil || afterFollowUpNode != node || afterFollowUpInitial.TurnID != initial.TurnID || afterFollowUpInitial.TurnSequence != 1 {
		t.Fatal("initial retry after follow-up", afterFollowUpNode, afterFollowUpInitial, err)
	}
	duplicatePlan := plan
	duplicatePlan.Nonce = "different-initial-turn"
	if _, _, err := scheduled.Spawn(context.Background(), duplicatePlan); err == nil {
		t.Fatal("second initial turn admitted through spawn")
	}
}

func TestScheduledOperationsRejectCallerAuthorityAndUnconsumedFollowUp(t *testing.T) {
	_, treePath, root, _ := controlFixture(t)
	directory := filepath.Dir(treePath)
	schedulePath := filepath.Join(directory, "schedule.db")
	controllerPath := filepath.Join(directory, "controller.db")
	if _, err := taskscheduler.Bind(schedulePath, taskscheduler.Definition{Version: 1, Nonce: "fixture", Tasks: []taskscheduler.TaskSpec{{ID: strings.Repeat("1", 64), RunID: strings.Repeat("a", 64), ControllerPath: controllerPath, Operation: taskscheduler.OperationPlanner, InvocationID: strings.Repeat("2", 64)}}}); err != nil {
		t.Fatal(err)
	}
	scheduled, err := BindScheduled(treePath, filepath.Join(directory, "agent-control.db"), schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := SpawnPlan{ParentAgentID: root.AgentID, Name: "writer", Role: "writer", Authority: agenttree.AuthorityScopedWriter, InvocationID: strings.Repeat("3", 64), ContextSHA256: strings.Repeat("4", 64), Nonce: "spawn", Task: taskscheduler.TaskSpec{RunID: strings.Repeat("a", 64), ControllerPath: controllerPath, Operation: taskscheduler.OperationWriter, InvocationID: strings.Repeat("3", 64)}}
	unsafe.TurnID = strings.Repeat("7", 64)
	unsafe.Task.ID = unsafe.TurnID
	if _, _, err := scheduled.Spawn(context.Background(), unsafe); err == nil {
		t.Fatal("non-explorer dynamic controller seam accepted")
	}
}
