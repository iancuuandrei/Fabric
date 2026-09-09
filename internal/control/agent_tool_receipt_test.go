package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

func TestAgentToolReceiptVerifiesAllOperationSourcesAndOlderPrefix(t *testing.T) {
	fixture := newAgentToolFixture(t)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}

	listCall := canonicalAgentToolCall(t, "list_agents", 301, map[string]any{"limit": 8})
	listResult := executeAgentToolCall(t, fixture.projection, listCall)
	assertAgentToolReceipt(t, verifier, listCall, listResult)

	waitCall := canonicalAgentToolCall(t, "wait_agent", 302, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 0, "limit": 8, "timeout_milliseconds": 5000,
	})
	waitResult := executeAgentToolCall(t, fixture.projection, waitCall)
	assertAgentToolReceipt(t, verifier, waitCall, waitResult)
	timeoutCall := canonicalAgentToolCall(t, "wait_agent", 308, map[string]any{
		"agent_id": fixture.binding.Node.AgentID, "after_sequence": 1000, "limit": 1, "timeout_milliseconds": 500,
	})
	timeoutResult := executeAgentToolCall(t, fixture.projection, timeoutCall)
	assertAgentToolReceipt(t, verifier, timeoutCall, timeoutResult)

	sendCall := canonicalAgentToolCall(t, "send_message", 303, map[string]any{
		"agent_id": fixture.binding.Node.ParentAgentID, "nonce": "receipt-message-one", "body": "First retained receipt body.",
	})
	sendResult := executeAgentToolCall(t, fixture.projection, sendCall)
	assertAgentToolReceipt(t, verifier, sendCall, sendResult)

	spawnCall := canonicalAgentToolCall(t, "spawn_agent", 304, map[string]any{
		"name": "receipt-child", "question": "Inspect receipt source semantics.", "nonce": "receipt-spawn-one",
	})
	spawnResult := executeAgentToolCall(t, fixture.projection, spawnCall)
	assertAgentToolReceipt(t, verifier, spawnCall, spawnResult)
	var spawnEnvelope struct {
		Result struct {
			AgentID string `json:"agent_id"`
			TurnID  string `json:"turn_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(spawnResult.JSON, &spawnEnvelope); err != nil {
		t.Fatal(err)
	}

	followCall := canonicalAgentToolCall(t, "followup_task", 305, map[string]any{
		"agent_id": spawnEnvelope.Result.AgentID, "nonce": "receipt-follow-one", "body": "Inspect the next receipt fact.",
	})
	followResult := executeAgentToolCall(t, fixture.projection, followCall)
	assertAgentToolReceipt(t, verifier, followCall, followResult)

	decision, err := taskscheduler.Tick(context.Background(), fixture.schedulerPath, fixture.interruptSchedulerAdapter)
	if err != nil || decision.TaskID != spawnEnvelope.Result.TurnID || decision.Status != taskscheduler.StatusRunning {
		t.Fatal("claim child for interrupt", decision, err)
	}
	interruptCall := canonicalAgentToolCall(t, "interrupt_agent", 306, map[string]any{
		"turn_id": spawnEnvelope.Result.TurnID, "nonce": "receipt-interrupt-one",
	})
	interruptResult := executeAgentToolCall(t, fixture.projection, interruptCall)
	assertAgentToolReceipt(t, verifier, interruptCall, interruptResult)

	// Append a later valid message to both AgentTree and AgentControl. The
	// earlier receipt remains valid because it selects its exact source prefix.
	laterCall := canonicalAgentToolCall(t, "send_message", 307, map[string]any{
		"agent_id": fixture.binding.Node.ParentAgentID, "nonce": "receipt-message-two", "body": "Later durable body.",
	})
	laterResult := executeAgentToolCall(t, fixture.projection, laterCall)
	assertAgentToolReceipt(t, verifier, laterCall, laterResult)
	assertAgentToolReceipt(t, verifier, sendCall, sendResult)
}

func TestAgentToolReceiptRejectsResignedSemanticSubstitution(t *testing.T) {
	fixture := newAgentToolFixture(t)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	call := canonicalAgentToolCall(t, "send_message", 401, map[string]any{
		"agent_id": fixture.binding.Node.ParentAgentID, "nonce": "semantic-substitution", "body": "Authoritative body.",
	})
	result := executeAgentToolCall(t, fixture.projection, call)
	assertAgentToolReceipt(t, verifier, call, result)

	var envelope agentToolResultEnvelope
	if err := canonical.Decode(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	var output struct {
		Message struct {
			MessageID   string `json:"message_id"`
			FromAgentID string `json:"from_agent_id"`
			ToAgentID   string `json:"to_agent_id"`
			BodySHA256  string `json:"body_sha256"`
			Sequence    int    `json:"sequence"`
			Wake        bool   `json:"wake"`
		} `json:"message"`
	}
	if err := canonical.Decode(envelope.Result, &output); err != nil {
		t.Fatal(err)
	}
	output.Message.ToAgentID = strings.Repeat("9", 64)
	envelope.Result, err = canonical.Bytes(output)
	if err != nil {
		t.Fatal(err)
	}
	tampered := resignAgentToolEnvelope(t, verifier, call, envelope)
	if err := verifier.Verify(call, tampered); err == nil {
		t.Fatal("re-signed result substitution passed source verification")
	}

	changedCall := call
	changedCall.Arguments, err = canonical.Bytes(map[string]any{
		"agent_id": fixture.binding.Node.ParentAgentID, "nonce": "semantic-substitution", "body": "Substituted body.",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := resignAgentToolEnvelope(t, verifier, changedCall, decodeAgentToolEnvelope(t, result))
	if err := verifier.Verify(changedCall, changed); err == nil {
		t.Fatal("re-signed argument substitution passed body evidence verification")
	}
}

func TestAgentToolReceiptRejectsCallerAndSourceHeadSubstitution(t *testing.T) {
	fixture := newAgentToolFixture(t)
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal(err)
	}
	substituted := fixture.binding
	substituted.AgentTurn = &taskscheduler.AgentTurnBinding{
		ParentAgentID: fixture.binding.AgentTurn.ParentAgentID,
		AgentID:       strings.Repeat("8", 64),
		TurnID:        fixture.binding.AgentTurn.TurnID,
		TurnSequence:  fixture.binding.AgentTurn.TurnSequence,
	}
	if _, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, substituted); err == nil {
		t.Fatal("substituted receipt caller admitted")
	}
	call := canonicalAgentToolCall(t, "list_agents", 501, map[string]any{"limit": 4})
	result := executeAgentToolCall(t, fixture.projection, call)
	envelope := decodeAgentToolEnvelope(t, result)
	envelope.Sources.TreeHead = strings.Repeat("7", 64)
	tampered := resignAgentToolEnvelope(t, verifier, call, envelope)
	if err := verifier.Verify(call, tampered); err == nil {
		t.Fatal("unavailable source prefix admitted")
	}
}

func TestAgentToolReceiptVerifierConstructsAfterCallerTerminal(t *testing.T) {
	fixture := newAgentToolFixture(t)
	call := canonicalAgentToolCall(t, "list_agents", 601, map[string]any{"limit": 4})
	result := executeAgentToolCall(t, fixture.projection, call)
	if err := finishAgentDispatch(fixture.binding, strings.Repeat("6", 64)); err != nil {
		t.Fatal(err)
	}
	verifier, err := newAgentToolReceiptVerifier(fixture.controllerPath, fixture.schedulerPath, fixture.binding)
	if err != nil {
		t.Fatal("terminal historical binding unavailable", err)
	}
	assertAgentToolReceipt(t, verifier, call, result)
}

func executeAgentToolCall(t *testing.T, projection toolbridge.Projection, call toolbridge.Call) toolbridge.Result {
	t.Helper()
	result, err := projection.Call(context.Background(), call)
	if err != nil {
		t.Fatal(call.Tool, err)
	}
	return result
}

func assertAgentToolReceipt(t *testing.T, verifier *agentToolReceiptVerifier, call toolbridge.Call, result toolbridge.Result) {
	t.Helper()
	if err := verifier.Verify(call, result); err != nil {
		t.Fatal(call.Tool, err)
	}
}

func decodeAgentToolEnvelope(t *testing.T, result toolbridge.Result) agentToolResultEnvelope {
	t.Helper()
	var envelope agentToolResultEnvelope
	if err := canonical.Decode(result.JSON, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func resignAgentToolEnvelope(t *testing.T, verifier *agentToolReceiptVerifier, call toolbridge.Call, envelope agentToolResultEnvelope) toolbridge.Result {
	t.Helper()
	call, err := normalizeAgentToolCall(call)
	if err != nil {
		t.Fatal(err)
	}
	envelope.ReceiptID, err = canonical.Hash("harness.agent-tool-source-receipt.v1", struct {
		Tool         string                  `json:"tool"`
		RequestID    json.RawMessage         `json:"request_id"`
		Arguments    json.RawMessage         `json:"arguments"`
		AgentID      string                  `json:"agent_id"`
		InvocationID string                  `json:"invocation_id"`
		TurnID       string                  `json:"turn_id"`
		Result       json.RawMessage         `json:"result"`
		Sources      agentToolSourceReceipts `json:"sources"`
	}{call.Tool, call.RequestID, call.Arguments, verifier.binding.Node.AgentID, verifier.binding.InvocationID, verifier.binding.AgentTurn.TurnID, envelope.Result, envelope.Sources})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.Bytes(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return toolbridge.Result{JSON: encoded}
}
