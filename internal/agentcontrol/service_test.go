package agentcontrol

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/taskscheduler"
)

func TestSendFollowUpAndRetrieveExactBodies(t *testing.T) {
	service, treePath, root, child := controlFixture(t)
	ctx := context.Background()
	sent, err := service.Send(ctx, MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Nonce: "note-1", Body: "inspect the exact receipt"})
	if err != nil || sent.Message.Wake || sent.Body != "inspect the exact receipt" {
		t.Fatal("send", sent, err)
	}
	followed, err := service.FollowUp(ctx, MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Nonce: "turn-2", Body: "continue after the receipt"})
	if err != nil || !followed.Message.Wake || followed.Message.Sequence != sent.Message.Sequence+1 {
		t.Fatal("followup", followed, err)
	}
	records, err := service.MessagesAfter(child.AgentID, 0, 10)
	if err != nil || len(records) != 2 || records[0] != sent || records[1] != followed {
		t.Fatal("messages", records, err)
	}
	state, err := Inspect(filepath.Join(filepath.Dir(treePath), "agent-control.db"))
	if err != nil || len(state.Activities) != 2 || state.Activities[0].Sequence != 1 || state.Activities[1].Sequence != 2 {
		t.Fatal("activities", state, err)
	}
}

func TestSendReconcilesEnvelopeAfterBodyJournalGap(t *testing.T) {
	service, treePath, root, child := controlFixture(t)
	request := MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Nonce: "recover", Body: "durable body"}
	messageID, bodySHA256, err := service.messageIdentity(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agenttree.Send(treePath, agenttree.MessageRequest{MessageID: messageID, FromAgentID: root.AgentID, ToAgentID: child.AgentID, BodySHA256: bodySHA256}); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Send(context.Background(), request)
	if err != nil || recovered.Message.MessageID != messageID || recovered.Body != request.Body {
		t.Fatal("recover", recovered, err)
	}
	retried, err := service.Send(context.Background(), request)
	if err != nil || retried != recovered {
		t.Fatal("retry", retried, err)
	}
}

func TestListAndWaitObserveLifecycleOnce(t *testing.T) {
	service, treePath, _, child := controlFixture(t)
	nodes, err := service.List(context.Background(), "", 10)
	if err != nil || len(nodes) != 2 {
		t.Fatal("list", nodes, err)
	}
	current, err := service.ActivitiesAfter(child.AgentID, 0, 10)
	if err != nil || len(current) != 1 || current[0].Status != agenttree.StatusQueued {
		t.Fatal("initial activity", current, err)
	}
	if _, err := service.List(context.Background(), "", 10); err != nil {
		t.Fatal(err)
	}
	repeated, err := service.ActivitiesAfter(child.AgentID, 0, 10)
	if err != nil || len(repeated) != 1 {
		t.Fatal("duplicate status activity", repeated, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = agenttree.ObserveStatus(treePath, child.AgentID, agenttree.StatusRunning)
		_ = agenttree.ObserveResult(treePath, child.AgentID, strings.Repeat("9", 64))
	}()
	observed, err := service.Wait(ctx, child.AgentID, current[0].Sequence, 10)
	if err == nil && len(observed) > 0 && observed[len(observed)-1].Status == agenttree.StatusRunning {
		observed, err = service.Wait(ctx, child.AgentID, observed[len(observed)-1].Sequence, 10)
	}
	if err != nil || len(observed) == 0 || observed[len(observed)-1].Status != agenttree.StatusSucceeded || observed[len(observed)-1].ResultSHA256 != strings.Repeat("9", 64) {
		t.Fatal("wait", observed, err)
	}
}

func TestBoundsAndReplayRejectTampering(t *testing.T) {
	service, _, root, child := controlFixture(t)
	if _, err := service.Send(context.Background(), MessageRequest{FromAgentID: root.AgentID, ToAgentID: child.AgentID, Nonce: "large", Body: strings.Repeat("x", MaximumBodyBytes+1)}); err == nil {
		t.Fatal("oversized body accepted")
	}
	if _, err := service.List(context.Background(), "/root/missing", 1); err == nil {
		t.Fatal("foreign list cursor accepted")
	}
	if _, err := service.Wait(context.Background(), child.AgentID, 0, 1); err == nil {
		t.Fatal("unbounded wait accepted")
	}
	path := filepath.Join(t.TempDir(), "tampered.db")
	_, err := journal.Append(path, "agentcontrol.bound", boundEvent{Version: 1, TreeID: strings.Repeat("a", 64)}, func([]journal.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	bad := Activity{AgentID: child.AgentID, Sequence: 2, Kind: "status", Status: agenttree.StatusQueued}
	_, err = journal.Append(path, "agentcontrol.activity", activityEvent{Version: 1, Activity: bad}, func([]journal.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path); err == nil {
		t.Fatal("non-monotonic activity accepted")
	}
}

func TestTurnActivityRequiresRunningAndAllowsUnknownReconciliation(t *testing.T) {
	service, _, root, child := controlFixture(t)
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: root.AgentID, AgentID: child.AgentID, TurnID: strings.Repeat("8", 64), TurnSequence: 2}
	result := strings.Repeat("9", 64)
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusSucceeded, result); err == nil {
		t.Fatal("terminal turn without running accepted")
	}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusRunning, ""); err != nil {
		t.Fatal("running retry", err)
	}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusUnknown, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusSucceeded, result); err != nil {
		t.Fatal(err)
	}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusRunning, ""); err == nil {
		t.Fatal("terminal turn revived")
	}
	activities, err := service.ActivitiesAfter(child.AgentID, 0, 10)
	if err != nil || len(activities) != 3 || activities[0].Status != agenttree.StatusRunning || activities[1].Status != agenttree.StatusUnknown || activities[2].Status != agenttree.StatusSucceeded {
		t.Fatal("turn activities", activities, err)
	}
}

func controlFixture(t *testing.T) (*Service, string, agenttree.Node, agenttree.Node) {
	t.Helper()
	directory := t.TempDir()
	treePath := filepath.Join(directory, "agent-tree.db")
	controlPath := filepath.Join(directory, "agent-control.db")
	treeID := strings.Repeat("a", 64)
	root, err := agenttree.Create(treePath, treeID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: strings.Repeat("b", 64), ContextSHA256: strings.Repeat("c", 64)})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := agenttree.ReserveChild(treePath, treeID, agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: "worker", Role: "writer", Authority: agenttree.AuthorityScopedWriter, InvocationID: strings.Repeat("d", 64), ContextSHA256: strings.Repeat("e", 64)})
	if err != nil {
		t.Fatal(err)
	}
	child, err := agenttree.CommitChild(treePath, reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Bind(treePath, controlPath)
	if err != nil {
		t.Fatal(err)
	}
	return service, treePath, root, child
}
