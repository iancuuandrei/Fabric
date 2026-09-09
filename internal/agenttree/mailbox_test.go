package agenttree

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSendAndFollowUpPreserveFIFOAndWakeSemantics(t *testing.T) {
	journalPath, root, child := mailboxFixture(t)
	first, err := Send(journalPath, messageRequest(root.AgentID, child.AgentID, '1', 'a'))
	if err != nil {
		t.Fatal(err)
	}
	second, err := FollowUp(journalPath, messageRequest(root.AgentID, child.AgentID, '2', 'b'))
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 || first.Wake || second.Sequence != 2 || !second.Wake {
		t.Fatalf("messages = %+v, %+v", first, second)
	}
	state, err := Inspect(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	page, err := MessagesAfter(state, child.AgentID, 1, 1)
	if err != nil || len(page) != 1 || page[0] != second {
		t.Fatalf("page = %+v: %v", page, err)
	}
	if _, err := Send(journalPath, messageRequest(root.AgentID, child.AgentID, '2', 'c')); err == nil {
		t.Fatal("duplicate message identity accepted")
	}
}

func TestConcurrentMessagesUseOneMonotonicRecipientSequence(t *testing.T) {
	journalPath, root, child := mailboxFixture(t)
	start := make(chan struct{})
	results := make(chan Message, 2)
	errorsSeen := make(chan error, 2)
	var group sync.WaitGroup
	for index, marker := range []rune{'3', '4'} {
		group.Add(1)
		go func(index int, marker rune) {
			defer group.Done()
			<-start
			message, err := Send(journalPath, messageRequest(root.AgentID, child.AgentID, marker, rune('d'+index)))
			if err != nil {
				errorsSeen <- err
				return
			}
			results <- message
		}(index, marker)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsSeen)
	if len(errorsSeen) != 1 || len(results) != 1 {
		t.Fatalf("successful=%d failed=%d, want one each", len(results), len(errorsSeen))
	}
	state, err := Inspect(journalPath)
	if err != nil || len(state.Messages) != 1 || state.Messages[0].Sequence != 1 {
		t.Fatalf("state = %+v: %v", state, err)
	}
}

func TestWaitDoesNotMissMessageBetweenInspectionAndPolling(t *testing.T) {
	journalPath, root, child := mailboxFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan []Message, 1)
	errResult := make(chan error, 1)
	go func() {
		messages, err := Wait(ctx, journalPath, child.AgentID, 0, 8)
		result <- messages
		errResult <- err
	}()
	message, err := FollowUp(journalPath, messageRequest(root.AgentID, child.AgentID, '5', 'f'))
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := <-errResult; waitErr != nil {
		t.Fatal(waitErr)
	}
	messages := <-result
	if len(messages) != 1 || messages[0] != message {
		t.Fatalf("wait messages = %+v", messages)
	}
}

func TestPersistentMailboxAcceptsTerminalSendButUnknownRejectsWake(t *testing.T) {
	journalPath, root, child := mailboxFixture(t)
	if err := ObserveStatus(journalPath, child.AgentID, StatusCanceled); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(journalPath, messageRequest(root.AgentID, child.AgentID, '6', 'a')); err != nil {
		t.Fatal("idle persistent agent rejected ordinary message", err)
	}
	if _, err := FollowUp(journalPath, messageRequest(root.AgentID, child.AgentID, '7', 'b')); err != nil {
		t.Fatal("finite terminal recipient rejected follow-up", err)
	}
	unknownPath, unknownRoot, unknownChild := mailboxFixture(t)
	if err := ObserveStatus(unknownPath, unknownChild.AgentID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	if err := ObserveStatus(unknownPath, unknownChild.AgentID, StatusUnknown); err != nil {
		t.Fatal(err)
	}
	if _, err := FollowUp(unknownPath, messageRequest(unknownRoot.AgentID, unknownChild.AgentID, '8', 'c')); err == nil {
		t.Fatal("unknown recipient accepted follow-up")
	}
	if _, err := Send(unknownPath, messageRequest(unknownRoot.AgentID, unknownChild.AgentID, '9', 'd')); err != nil {
		t.Fatal("unknown recipient rejected non-waking message", err)
	}
	if _, err := Wait(context.Background(), journalPath, child.AgentID, 0, 1); err == nil {
		t.Fatal("unbounded mailbox wait accepted")
	}
}

func mailboxFixture(t *testing.T) (string, Node, Node) {
	t.Helper()
	journalPath := filepath.Join(t.TempDir(), "agents.db")
	treeID := strings.Repeat("a", 64)
	root, err := Create(journalPath, treeID, nodeSpec("", "root", "orchestrator", 'b', 'c'))
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ReserveChild(journalPath, treeID, nodeSpec(root.AgentID, "worker", "writer", 'd', 'e'))
	if err != nil {
		t.Fatal(err)
	}
	child, err := CommitChild(journalPath, reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	return journalPath, root, child
}

func messageRequest(from, to string, messageByte, bodyByte rune) MessageRequest {
	return MessageRequest{MessageID: strings.Repeat(string(messageByte), 64), FromAgentID: from, ToAgentID: to, BodySHA256: strings.Repeat(string(bodyByte), 64)}
}
