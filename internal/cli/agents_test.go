package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/runtime"
)

func checkAgentCLI(t *testing.T, root string, state control.Snapshot, run func(...string) []byte) {
	t.Helper()
	path, err := runPath(root, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(state.Creation.Config.Planner, state.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	inputHash, err := access.InputID(invocation.Input)
	if err != nil {
		t.Fatal(err)
	}
	treePath := path + ".agent-tree"
	parent, err := agenttree.Create(treePath, state.RunID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: "read-only", InvocationID: invocation.ID, ContextSHA256: inputHash})
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := agenttree.ReserveChild(treePath, state.RunID, agenttree.NodeSpec{ParentAgentID: parent.AgentID, Name: "reader", Role: "explorer", Authority: "read-only", InvocationID: strings.Repeat("d", 64), ContextSHA256: inputHash})
	if err != nil {
		t.Fatal(err)
	}
	child, err := agenttree.CommitChild(treePath, reserved.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"agent-list", state.RunID, "", "257"},
		{"agent-messages", state.RunID, child.AgentID, "-1", "10"},
		{"agent-wait", state.RunID, child.AgentID, "0", "10", "0"},
		{"agent-send", state.RunID, filepath.Join(t.TempDir(), "missing.json")},
	} {
		var denied bytes.Buffer
		if err := Execute(context.Background(), args, root, &denied); err == nil || denied.Len() != 0 {
			t.Fatal("invalid agent input accepted")
		}
	}
	if _, err := os.Stat(path + ".agent-control"); !os.IsNotExist(err) {
		t.Fatal("invalid agent input created service journal", err)
	}
	var nodes []agenttree.Node
	if err := json.Unmarshal(run("agent-list", state.RunID), &nodes); err != nil || len(nodes) != 2 {
		t.Fatal("agent listing differs", err)
	}
	request := agentcontrol.MessageRequest{FromAgentID: parent.AgentID, ToAgentID: child.AgentID, Nonce: "cli-message", Body: "Exact bounded message body"}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "message.json")
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	if sent := run("agent-send", state.RunID, input); bytes.Contains(sent, []byte(request.Body)) {
		t.Fatal("send receipt echoed message body")
	}
	var messages []agentcontrol.MessageRecord
	if err := json.Unmarshal(run("agent-messages", state.RunID, child.AgentID, "0", "10"), &messages); err != nil || len(messages) != 1 || messages[0].Body != request.Body {
		t.Fatal("message bytes not retrievable", err)
	}
	var activity []agentcontrol.Activity
	if err := json.Unmarshal(run("agent-wait", state.RunID, child.AgentID, "0", "10", "1000"), &activity); err != nil || len(activity) == 0 {
		t.Fatal("message activity not observable", err)
	}
	tree, err := agenttree.Inspect(treePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range tree.Nodes {
		if node.Status != agenttree.StatusQueued {
			t.Fatal("message operation woke runtime", node.Status)
		}
	}
	// A completed turn does not close the persistent agent mailbox. These
	// lifecycle facts are injected fixtures, not evidence of model execution.
	if err := agenttree.ObserveStatus(treePath, child.AgentID, agenttree.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := agenttree.ObserveResult(treePath, child.AgentID, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	request.Nonce, request.Body = "after-completion", "Context for a later turn"
	body, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	var envelope agenttree.Message
	if err := json.Unmarshal(run("agent-send", state.RunID, input), &envelope); err != nil || envelope.Wake {
		t.Fatal("completed-turn message unexpectedly woke agent", err)
	}
	if err := json.Unmarshal(run("agent-messages", state.RunID, child.AgentID, "0", "10"), &messages); err != nil || len(messages) != 2 || messages[1].Body != request.Body {
		t.Fatal("completed-turn mailbox unavailable", messages, err)
	}
	tree, err = agenttree.Inspect(treePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range tree.Nodes {
		if node.AgentID == child.AgentID && node.Status != agenttree.StatusSucceeded {
			t.Fatal("non-waking message changed terminal turn")
		}
	}
}
