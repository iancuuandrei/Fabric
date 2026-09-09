package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

// This exercises CLI queue admission against a real controller and workspace.
// The fake profile is deliberately not claimed as an executed child runtime.
func checkAgentSchedulingCLI(t *testing.T, root string, state control.Snapshot, run func(...string) []byte) {
	t.Helper()
	creation := state.Creation
	creation.Nonce += "-agent-scheduling"
	accessConfig := *creation.Config.Access
	accessConfig.Roles = maps.Clone(accessConfig.Roles)
	accessConfig.Invocations = maps.Clone(accessConfig.Invocations)
	accessConfig.Roles["explorer"] = accessConfig.Roles["planner"]
	accessConfig.Invocations["explorer"] = accessConfig.Invocations["planner"]
	creation.Config.Access = &accessConfig
	creation.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture-explorer", Effort: "low", Role: "explorer"}
	runID, err := canonical.Hash("harness.run.v1", creation)
	if err != nil {
		t.Fatal(err)
	}
	controllerPath, err := runPath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run("resume", runID), &state); err != nil {
		t.Fatal(err)
	}
	run("approve", runID, state.PlanID, "fixture-human")
	run("run", runID)
	invocation, err := runtime.NewInvocation(creation.Config.Planner, creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	inputID, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := agenttree.Create(controllerPath+".agent-tree", runID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: invocation.ID, ContextSHA256: inputID})
	if err != nil {
		t.Fatal(err)
	}
	var anchor taskscheduler.TaskSpec
	if err := json.Unmarshal(run("schedule-task", runID, "anchor", "planner"), &anchor); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) string {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	definition := taskscheduler.Definition{Version: 1, Nonce: "agent-cli", Tasks: []taskscheduler.TaskSpec{anchor}}
	var scheduled taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-create", write("schedule.json", definition)), &scheduled); err != nil {
		t.Fatal(err)
	}
	spawn := explorerSpawnRequest{ParentAgentID: parent.AgentID, Name: "reader", Question: "Inspect exact source", Nonce: "first"}
	spawnPath := write("spawn.json", spawn)
	firstBytes := run("agent-spawn", runID, scheduled.ScheduleID, spawnPath)
	var first agentQueuedResult
	if err := json.Unmarshal(firstBytes, &first); err != nil || first.AgentID == "" || first.TurnID == "" {
		t.Fatal(first, err)
	}
	if !bytes.Equal(firstBytes, run("agent-spawn", runID, scheduled.ScheduleID, spawnPath)) {
		t.Fatal("spawn retry changed identity")
	}
	message := agentcontrol.MessageRequest{FromAgentID: parent.AgentID, ToAgentID: first.AgentID, Body: "Explain missing evidence", Nonce: "next"}
	messagePath := write("message.json", message)
	followBytes := run("agent-followup", runID, scheduled.ScheduleID, messagePath)
	var follow agentQueuedResult
	if err := json.Unmarshal(followBytes, &follow); err != nil || follow.AgentID != first.AgentID || follow.TurnID == first.TurnID || follow.Message == nil || !follow.Message.Wake {
		t.Fatal(follow, err)
	}
	if bytes.Contains(followBytes, []byte(message.Body)) {
		t.Fatal("queue response echoed body")
	}
	if !bytes.Equal(followBytes, run("agent-followup", runID, scheduled.ScheduleID, messagePath)) {
		t.Fatal("followup retry changed identity")
	}
	if !bytes.Equal(firstBytes, run("agent-spawn", runID, scheduled.ScheduleID, spawnPath)) {
		t.Fatal("initial spawn retry changed after followup")
	}
	queuePath, err := schedulePath(root, scheduled.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	journalPaths := []string{controllerPath, controllerPath + ".agent-tree", controllerPath + ".agent-control", queuePath}
	before := make(map[string][]byte)
	for _, path := range journalPaths {
		before[path], err = journal.ExportJSONL(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"authority", "invocation_id", "model", "controller_path"} {
		request := map[string]any{"parent_agent_id": parent.AgentID, "name": "override", "question": "Inspect source", "nonce": "override", field: "untrusted-override"}
		var denied bytes.Buffer
		if err := Execute(context.Background(), []string{"agent-spawn", runID, scheduled.ScheduleID, write("override.json", request)}, root, &denied); err == nil || denied.Len() != 0 {
			t.Fatalf("caller authority override accepted: %s", field)
		}
	}
	// This queued fake-runtime turn is ineligible for interruption. Rejection
	// must precede any durable request or lifecycle mutation.
	for _, turnID := range []string{first.TurnID, strings.Repeat("f", 64)} {
		var denied bytes.Buffer
		if err := Execute(context.Background(), []string{"agent-interrupt", runID, scheduled.ScheduleID, turnID, "operator", "interrupt-queued"}, root, &denied); err == nil || denied.Len() != 0 {
			t.Fatal("ineligible interrupt accepted", turnID)
		}
	}
	for _, path := range journalPaths {
		after, err := journal.ExportJSONL(path)
		if err != nil || !bytes.Equal(before[path], after) {
			t.Fatalf("rejected authority override changed journal %s: %v", path, err)
		}
	}
	message.Nonce = "same-question-new-turn"
	var repeated agentQueuedResult
	if err := json.Unmarshal(run("agent-followup", runID, scheduled.ScheduleID, write("repeat.json", message)), &repeated); err != nil || repeated.TurnID == follow.TurnID || repeated.TurnSequence != 3 {
		t.Fatal("new request for same question reused turn", repeated, err)
	}
	if err := json.Unmarshal(run("schedule-inspect", scheduled.ScheduleID), &scheduled); err != nil || len(scheduled.Dynamic) != 3 {
		t.Fatal(scheduled, err)
	}
	if scheduled.Dynamic[0].TurnSequence != 1 || scheduled.Dynamic[1].TurnSequence != 2 {
		t.Fatal("turn order differs")
	}
	if scheduled.Dynamic[1].Task.InvocationID == scheduled.Dynamic[2].Task.InvocationID {
		t.Fatal("distinct turns for identical question reused invocation")
	}
	// Exercise CLI observation of an explicitly injected lifecycle fixture;
	// this does not claim the queued fake runtime was executed.
	service, err := agentcontrol.Bind(controllerPath+".agent-tree", controllerPath+".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	run("agent-list", runID)
	prior, err := service.ActivitiesAfter(first.AgentID, 0, 256)
	if err != nil || len(prior) == 0 {
		t.Fatal(prior, err)
	}
	cursor := prior[len(prior)-1].Sequence
	turn := scheduled.Dynamic[1]
	binding := taskscheduler.AgentTurnBinding{ParentAgentID: turn.ParentAgentID, AgentID: turn.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence}
	for _, status := range []agenttree.Status{agenttree.StatusRunning, agenttree.StatusUnknown, agenttree.StatusSucceeded} {
		resultHash := ""
		if status == agenttree.StatusSucceeded {
			resultHash = strings.Repeat("e", 64)
		}
		if err := service.ObserveTurn(context.Background(), binding, status, resultHash); err != nil {
			t.Fatal(err)
		}
	}
	var observed []agentcontrol.Activity
	if err := json.Unmarshal(run("agent-wait", runID, first.AgentID, fmt.Sprint(cursor), "10", "1000"), &observed); err != nil || len(observed) != 3 {
		t.Fatal(observed, err)
	}
	for _, event := range observed {
		if event.Kind != "turn" || event.TurnID != binding.TurnID || event.TurnSequence != 2 {
			t.Fatal("CLI lost turn identity", event)
		}
	}
	if observed[2].Status != agenttree.StatusSucceeded || observed[2].ResultSHA256 != strings.Repeat("e", 64) {
		t.Fatal("CLI lost terminal turn evidence")
	}
	tree, err := agenttree.Inspect(controllerPath + ".agent-tree")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range tree.Nodes {
		if node.Status != agenttree.StatusQueued {
			t.Fatal("queue admission dispatched a runtime")
		}
	}
}
