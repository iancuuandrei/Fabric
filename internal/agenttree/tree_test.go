package agenttree

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestReserveAndCommitChildBindsOpaqueIdentityToCanonicalPath(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "agents.db")
	treeID := strings.Repeat("a", 64)
	root, err := Create(journalPath, treeID, nodeSpec("", "root", "orchestrator", 'b', 'c'))
	if err != nil {
		t.Fatal(err)
	}
	request := nodeSpec(root.AgentID, "reviewer", "reviewer", 'd', 'e')
	reservation, err := ReserveChild(journalPath, treeID, request)
	if err != nil {
		t.Fatal(err)
	}
	before, err := Inspect(journalPath)
	if err != nil || len(before.Nodes) != 1 || len(before.Reservations) != 1 {
		t.Fatalf("reservation state = %+v: %v", before, err)
	}
	child, err := CommitChild(journalPath, reservation.ReservationID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Path != "/root/reviewer" || child.AgentID == child.Path || child.ParentAgentID != root.AgentID || child.Depth != 1 {
		t.Fatalf("child identity = %+v", child)
	}
	after, err := Inspect(journalPath)
	if err != nil || len(after.Nodes) != 2 || len(after.Reservations) != 0 {
		t.Fatalf("committed state = %+v: %v", after, err)
	}
	if _, err := CommitChild(journalPath, reservation.ReservationID); err == nil {
		t.Fatal("reservation committed twice")
	}
}

func TestConcurrentDuplicateReservationCommitsExactlyOneChild(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "agents.db")
	treeID := strings.Repeat("1", 64)
	root, err := Create(journalPath, treeID, nodeSpec("", "root", "orchestrator", '2', '3'))
	if err != nil {
		t.Fatal(err)
	}
	request := nodeSpec(root.AgentID, "worker", "writer", '4', '5')
	type result struct {
		reservation Reservation
		err         error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			reservation, reserveErr := ReserveChild(journalPath, treeID, request)
			results <- result{reservation, reserveErr}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	succeeded := 0
	var reservation Reservation
	for result := range results {
		if result.err == nil {
			succeeded++
			reservation = result.reservation
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful reservations = %d, want 1", succeeded)
	}
	if _, err := CommitChild(journalPath, reservation.ReservationID); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(journalPath)
	if err != nil || len(state.Nodes) != 2 || len(state.Reservations) != 0 {
		t.Fatalf("state = %+v: %v", state, err)
	}
}

func TestFiniteLifecycleCannotReviveTerminalNode(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "agents.db")
	root, err := Create(journalPath, strings.Repeat("6", 64), nodeSpec("", "root", "orchestrator", '7', '8'))
	if err != nil {
		t.Fatal(err)
	}
	if err := ObserveStatus(journalPath, root.AgentID, StatusRunning); err != nil {
		t.Fatal(err)
	}
	resultHash := strings.Repeat("0", 64)
	if err := ObserveResult(journalPath, root.AgentID, resultHash); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(journalPath)
	if err != nil || state.Nodes[0].ResultSHA256 != resultHash {
		t.Fatalf("result observation = %+v: %v", state, err)
	}
	if err := ObserveStatus(journalPath, root.AgentID, StatusRunning); err == nil {
		t.Fatal("terminal node revived")
	}
	reservation, err := ReserveChild(journalPath, strings.Repeat("6", 64), nodeSpec(root.AgentID, "late", "writer", '9', 'a'))
	if err != nil {
		t.Fatalf("terminal parent topology reservation: %v", err)
	}
	if _, err := CommitChild(journalPath, reservation.ReservationID); err != nil {
		t.Fatal(err)
	}
}

func TestPathsAndDepthAreBounded(t *testing.T) {
	valid := []struct{ current, target, want string }{
		{"/root", "child", "/root/child"},
		{"/root/child", "leaf", "/root/child/leaf"},
		{"/root/child", "/root/peer", "/root/peer"},
	}
	for _, item := range valid {
		got, err := ResolveTarget(item.current, item.target)
		if err != nil || got != item.want {
			t.Errorf("ResolveTarget(%q, %q) = %q, %v", item.current, item.target, got, err)
		}
	}
	for _, target := range []string{"", "../peer", "/root/../peer", "/root/Upper", "Upper", strings.Repeat("x", 64)} {
		if _, err := ResolveTarget("/root", target); err == nil {
			t.Errorf("invalid target %q accepted", target)
		}
	}

	journalPath := filepath.Join(t.TempDir(), "agents.db")
	treeID := strings.Repeat("b", 64)
	parent, err := Create(journalPath, treeID, nodeSpec("", "root", "orchestrator", 'c', 'd'))
	if err != nil {
		t.Fatal(err)
	}
	for depth := 1; depth <= MaximumDepth; depth++ {
		name := "child" + string(rune('a'+depth))
		reservation, reserveErr := ReserveChild(journalPath, treeID, nodeSpec(parent.AgentID, name, "worker", 'e', 'f'))
		if reserveErr != nil {
			t.Fatalf("depth %d: %v", depth, reserveErr)
		}
		parent, err = CommitChild(journalPath, reservation.ReservationID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReserveChild(journalPath, treeID, nodeSpec(parent.AgentID, "too-deep", "worker", 'e', 'f')); err == nil {
		t.Fatal("node beyond maximum depth accepted")
	}
}

func TestReplayRejectsLifecycleHistorySubstitution(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "agents.db")
	root, err := Create(journalPath, strings.Repeat("e", 64), nodeSpec("", "root", "orchestrator", 'f', '1'))
	if err != nil {
		t.Fatal(err)
	}
	payload := statusEvent{Version: 1, AgentID: root.AgentID, From: StatusRunning, To: StatusSucceeded}
	if _, err := journal.Append(journalPath, eventStatus, payload, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(journalPath); err == nil {
		t.Fatal("substituted lifecycle history accepted")
	}
}

func nodeSpec(parent, name, role string, invocationByte, contextByte rune) NodeSpec {
	return NodeSpec{
		ParentAgentID: parent,
		Name:          name,
		Role:          role,
		Authority:     authorityForRole(role),
		InvocationID:  strings.Repeat(string(invocationByte), 64),
		ContextSHA256: strings.Repeat(string(contextByte), 64),
	}
}

func authorityForRole(role string) Authority {
	if role == "writer" || role == "fixer" || role == "worker" {
		return AuthorityScopedWriter
	}
	return AuthorityReadOnly
}
