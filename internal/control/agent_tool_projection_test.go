package control

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

type agentToolFixture struct {
	interruptSchedulerAdapter
	controllerPath string
	schedulerPath  string
	binding        agentDispatchBinding
	projection     toolbridge.Projection
}

func newAgentToolFixture(t *testing.T) agentToolFixture {
	t.Helper()
	fixture := newScheduledInterruptFixture(t)
	snapshot, _, invocation, err := scheduledInvocation(fixture.claim.Task, fixture.claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, snapshot, invocation, fixture.claim.AgentTurn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	projection, err := newAgentToolProjection(fixture.controllerPath, fixture.schedulerPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	return agentToolFixture{
		interruptSchedulerAdapter: interruptSchedulerAdapter{seedID: strings.Repeat("a", 64)},
		controllerPath:            fixture.controllerPath,
		schedulerPath:             fixture.schedulerPath,
		binding:                   binding,
		projection:                projection,
	}
}

func TestAgentToolProjectionCatalogAndCallerBoundMessage(t *testing.T) {
	fixture := newAgentToolFixture(t)
	catalog, err := fixture.projection.Catalog()
	if err != nil || len(catalog) != 6 {
		t.Fatal("catalog", catalog, err)
	}
	want := []string{"spawn_agent", "list_agents", "wait_agent", "send_message", "followup_task", "interrupt_agent"}
	for index, definition := range catalog {
		if definition.Name != want[index] || !json.Valid(definition.InputSchema) || !strings.Contains(string(definition.InputSchema), `"additionalProperties":false`) {
			t.Fatalf("catalog[%d] = %+v", index, definition)
		}
	}
	result, err := fixture.projection.Call(context.Background(), toolbridge.Call{
		RequestID: json.RawMessage(`1`), Tool: "send_message",
		Arguments: json.RawMessage("{\n  \"nonce\": \"message-once\", \"body\": \"A bounded observation for the parent.\",\n  \"agent_id\": \"" + fixture.binding.Node.ParentAgentID + "\"\n}"),
	})
	if err != nil {
		t.Fatal("valid noncanonical MCP arguments rejected", err)
	}
	retry, err := fixture.projection.Call(context.Background(), toolbridge.Call{
		RequestID: json.RawMessage(`1`), Tool: "send_message",
		Arguments: json.RawMessage(`{"agent_id":"` + fixture.binding.Node.ParentAgentID + `","body":"A bounded observation for the parent.","nonce":"message-once"}`),
	})
	if err != nil || !jsonEqual(result.JSON, retry.JSON) {
		t.Fatal("exact message retry changed source receipt", string(result.JSON), string(retry.JSON), err)
	}
	if strings.Contains(string(result.JSON), "bounded observation") {
		t.Fatal("tool result echoed message body", string(result.JSON))
	}
	var envelope struct {
		Result struct {
			Message struct {
				FromAgentID string `json:"from_agent_id"`
				ToAgentID   string `json:"to_agent_id"`
				MessageID   string `json:"message_id"`
			} `json:"message"`
		} `json:"result"`
		ReceiptID string `json:"receipt_id"`
		Sources   struct {
			Controller string `json:"controller_head"`
			Tree       string `json:"tree_head"`
			Control    string `json:"agent_control_head"`
			Scheduler  string `json:"scheduler_head"`
		} `json:"source_receipts"`
	}
	if err := json.Unmarshal(result.JSON, &envelope); err != nil || envelope.Result.Message.FromAgentID != fixture.binding.Node.AgentID || envelope.Result.Message.ToAgentID != fixture.binding.Node.ParentAgentID || envelope.Result.Message.MessageID == "" || envelope.ReceiptID == "" || envelope.Sources.Controller == "" || envelope.Sources.Tree == "" || envelope.Sources.Control == "" || envelope.Sources.Scheduler == "" {
		t.Fatal("caller-bound message result", envelope, err)
	}
	stored, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(stored.Messages) != 1 || stored.Messages[0].Message.FromAgentID != fixture.binding.Node.AgentID {
		t.Fatal("durable sender binding", stored.Messages, err)
	}
}

func TestAgentToolProjectionRejectsUnknownAndDuplicateArgumentsBeforeMutation(t *testing.T) {
	fixture := newAgentToolFixture(t)
	paths := []string{fixture.controllerPath, fixture.controllerPath + ".agent-tree", fixture.controllerPath + ".agent-control", fixture.schedulerPath}
	before := readAgentToolFiles(t, paths)
	for _, raw := range []string{
		`{"agent_id":"` + fixture.binding.Node.ParentAgentID + `","body":"do not store","nonce":"n","authority":"scoped-writer"}`,
		`{"agent_id":"` + fixture.binding.Node.ParentAgentID + `","body":"first","body":"second","nonce":"n"}`,
	} {
		if _, err := fixture.projection.Call(context.Background(), toolbridge.Call{RequestID: json.RawMessage(`2`), Tool: "send_message", Arguments: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid arguments admitted", raw)
		}
	}
	after := readAgentToolFiles(t, paths)
	for index := range paths {
		if string(before[index]) != string(after[index]) {
			t.Fatal("invalid call mutated journal", paths[index])
		}
	}
}

func TestAgentToolProjectionSpawnListFollowUpAndInterruptDirectChild(t *testing.T) {
	fixture := newAgentToolFixture(t)
	spawn := callAgentTool(t, fixture.projection, "spawn_agent", 3, map[string]any{
		"name": "nested-reader", "question": "Inspect the bounded nested concern.", "nonce": "spawn-once",
	})
	var spawned struct {
		Result struct {
			AgentID string `json:"agent_id"`
			TurnID  string `json:"turn_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(spawn.JSON, &spawned); err != nil || spawned.Result.AgentID == "" || spawned.Result.TurnID == "" {
		t.Fatal("spawn result", spawned, err)
	}
	listed := callAgentTool(t, fixture.projection, "list_agents", 4, map[string]any{"limit": 64})
	var page struct {
		Result struct {
			Agents []struct {
				AgentID string `json:"agent_id"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(listed.JSON, &page); err != nil || len(page.Result.Agents) != 2 || page.Result.Agents[0].AgentID != fixture.binding.Node.AgentID || page.Result.Agents[1].AgentID != spawned.Result.AgentID {
		t.Fatal("list escaped or omitted caller subtree", string(listed.JSON))
	}
	followed := callAgentTool(t, fixture.projection, "followup_task", 5, map[string]any{
		"agent_id": spawned.Result.AgentID, "body": "Inspect one later bounded concern.", "nonce": "follow-once",
	})
	if strings.Contains(string(followed.JSON), "later bounded concern") {
		t.Fatal("follow-up result echoed body", string(followed.JSON))
	}
	decision, err := taskscheduler.Tick(context.Background(), fixture.schedulerPath, fixture.interruptSchedulerAdapter)
	if err != nil || decision.TaskID != spawned.Result.TurnID || decision.Status != taskscheduler.StatusRunning {
		t.Fatal("child initial turn was not claimed", decision, err)
	}
	interrupted := callAgentTool(t, fixture.projection, "interrupt_agent", 6, map[string]any{"turn_id": spawned.Result.TurnID, "nonce": "interrupt-once"})
	if !strings.Contains(string(interrupted.JSON), spawned.Result.TurnID) || !strings.Contains(string(interrupted.JSON), string(agentcontrol.InterruptRequested)) {
		t.Fatal("interrupt result", string(interrupted.JSON))
	}
	interrupts, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(interrupts.Interrupts) != 1 {
		t.Fatal("interrupt not durable", interrupts.Interrupts, err)
	}
	for _, state := range interrupts.Interrupts {
		if state.Request.Actor != fixture.binding.Node.AgentID || state.Request.AgentTurn.AgentID != spawned.Result.AgentID {
			t.Fatal("interrupt caller or target substituted", state)
		}
	}
}

func TestAgentToolProjectionWaitIsFiniteAndTopologyBound(t *testing.T) {
	fixture := newAgentToolFixture(t)
	result := callAgentTool(t, fixture.projection, "wait_agent", 7, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 1000, "limit": 1, "timeout_milliseconds": 500,
	})
	if !strings.Contains(string(result.JSON), `"timed_out":true`) {
		t.Fatal("finite wait did not time out", string(result.JSON))
	}
	if _, err := fixture.projection.Call(context.Background(), canonicalAgentToolCall(t, "wait_agent", 8, map[string]any{
		"agent_id": fixture.binding.Node.ParentAgentID, "after_sequence": 0, "limit": 1, "timeout_milliseconds": 25,
	})); err == nil {
		t.Fatal("wait admitted caller parent")
	}
}

func TestAgentToolProjectionRejectsSubstitutedCallerBinding(t *testing.T) {
	fixture := newAgentToolFixture(t)
	substituted := fixture.binding
	substituted.Node.AgentID = strings.Repeat("9", 64)
	if _, err := newAgentToolProjection(fixture.controllerPath, fixture.schedulerPath, substituted); err == nil {
		t.Fatal("substituted caller binding admitted")
	}
	if _, err := newAgentToolProjection(fixture.controllerPath, fixture.schedulerPath+".other", fixture.binding); err == nil {
		t.Fatal("substituted scheduler admitted")
	}
}

func TestAgentToolProjectionCopiesCallerTurnAndSupportsConcurrentReads(t *testing.T) {
	fixture := newAgentToolFixture(t)
	projection := fixture.projection
	fixture.binding.AgentTurn.AgentID = strings.Repeat("8", 64)
	const callers = 8
	calls := make([]toolbridge.Call, callers)
	for index := range calls {
		calls[index] = canonicalAgentToolCall(t, "list_agents", 100+index, map[string]any{"limit": 4})
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func(requestID int) {
			defer wait.Done()
			_, err := projection.Call(context.Background(), calls[requestID])
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal("concurrent caller-bound read", err)
		}
	}
}

func TestAgentToolProjectionUnknownCallerCannotCreateWorkButCanRead(t *testing.T) {
	fixture := newAgentToolFixture(t)
	if err := finishAgentDispatchUnknown(fixture.binding); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projection.Call(context.Background(), canonicalAgentToolCall(t, "spawn_agent", 200, map[string]any{
		"name": "forbidden", "question": "Do not schedule from uncertainty.", "nonce": "unknown-spawn",
	})); err == nil {
		t.Fatal("UNKNOWN caller created new scheduled work")
	}
	if _, err := fixture.projection.Call(context.Background(), canonicalAgentToolCall(t, "list_agents", 201, map[string]any{"limit": 4})); err != nil {
		t.Fatal("UNKNOWN caller lost observation-only access", err)
	}
}

func TestAgentToolProjectionRejectsTerminalCallerWhenScheduleLags(t *testing.T) {
	fixture := newAgentToolFixture(t)
	if err := finishAgentDispatch(fixture.binding, strings.Repeat("7", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.projection.Call(context.Background(), canonicalAgentToolCall(t, "list_agents", 202, map[string]any{"limit": 4})); err == nil {
		t.Fatal("terminal controller dispatch retained tool authority")
	}
}

func callAgentTool(t *testing.T, projection toolbridge.Projection, name string, requestID int, args any) toolbridge.Result {
	t.Helper()
	result, err := projection.Call(context.Background(), canonicalAgentToolCall(t, name, requestID, args))
	if err != nil {
		t.Fatal(name, err)
	}
	return result
}

func canonicalAgentToolCall(t *testing.T, name string, requestID int, args any) toolbridge.Call {
	t.Helper()
	raw, err := canonical.Bytes(args)
	if err != nil {
		t.Fatal(err)
	}
	request, err := canonical.Bytes(requestID)
	if err != nil {
		t.Fatal(err)
	}
	return toolbridge.Call{RequestID: request, Tool: name, Arguments: raw}
}

func readAgentToolFiles(t *testing.T, paths []string) [][]byte {
	t.Helper()
	result := make([][]byte, len(paths))
	for index, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = content
	}
	return result
}

func jsonEqual(first, second []byte) bool {
	left, leftErr := canonical.Normalize(first)
	right, rightErr := canonical.Normalize(second)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}
