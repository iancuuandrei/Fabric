package agentcontrol

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/taskscheduler"
)

func TestAcceptedResultIndexIsBodyFreeExactAndIdempotent(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	before, err := Inspect(service.journalPath)
	if err != nil || len(before.Results) != 0 {
		t.Fatal("runtime-only turn observation synthesized acceptance", before.Results, err)
	}
	indexed, err := service.IndexAcceptedResult(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := reference.ID()
	if err != nil || indexed.ResultID != wantID || indexed.Reference != reference {
		t.Fatal("accepted result identity changed", indexed, wantID, err)
	}
	encoded, err := json.Marshal(indexed)
	if err != nil || strings.Contains(string(encoded), `"body"`) || strings.Contains(string(encoded), "accepted exploration body") {
		t.Fatal("accepted result index retained a body", string(encoded), err)
	}
	state, err := Inspect(service.journalPath)
	if err != nil || len(state.Results) != 1 || state.Results[0] != indexed || len(state.Activities) != 4 {
		t.Fatal("accepted result absent", state, err)
	}
	for index, agentID := range []string{reference.ParentAgentID, reference.AgentID} {
		activity := state.Activities[len(state.Activities)-2+index]
		if activity.AgentID != agentID || activity.Kind != "result" || activity.ResultID != wantID || activity.TurnID != reference.TurnID || activity.TurnSequence != reference.TurnSequence || activity.Status != agenttree.StatusSucceeded || activity.ResultSHA256 != reference.ResultSHA256 {
			t.Fatal("accepted result audience activity changed", activity)
		}
	}
	events, err := journal.Read(service.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	head := events[len(events)-1].Hash
	retried, err := service.IndexAcceptedResult(context.Background(), reference)
	if err != nil || retried != indexed {
		t.Fatal("exact accepted result retry changed", retried, err)
	}
	events, err = journal.Read(service.journalPath)
	if err != nil || events[len(events)-1].Hash != head {
		t.Fatal("exact retry appended another event", err)
	}
}

func TestAcceptedResultRemainsVisibleAfterChildTerminalCursor(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	before, err := service.ActivitiesAfterWithHead(reference.AgentID, 0, 256)
	if err != nil || len(before.Activities) == 0 {
		t.Fatal("terminal child page unavailable", err)
	}
	terminal := before.Activities[len(before.Activities)-1]
	if terminal.Kind != "turn" || terminal.Status != agenttree.StatusSucceeded {
		t.Fatal("fixture has no terminal turn", terminal)
	}
	indexed, err := service.IndexAcceptedResult(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ActivitiesAfterWithHead(reference.AgentID, terminal.Sequence, 256)
	if err != nil || len(page.Activities) != 1 || page.JournalHead == before.JournalHead {
		t.Fatal("accepted result lost after terminal cursor", page, err)
	}
	if page.Activities[0].Kind != "result" || page.Activities[0].ResultID != indexed.ResultID || page.Activities[0].Sequence != terminal.Sequence+1 {
		t.Fatal("accepted result cursor differs", page.Activities)
	}
	parent, err := service.ActivitiesAfterWithHead(reference.ParentAgentID, 0, 256)
	if err != nil || len(parent.Activities) != 1 || parent.Activities[0].ResultID != indexed.ResultID || parent.JournalHead != page.JournalHead {
		t.Fatal("parent notification differs from child notification", parent, err)
	}
}

func TestAcceptedResultRejectsChangedTurnAndTopology(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	if _, err := service.IndexAcceptedResult(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	changed := reference
	changed.ControllerEventHash = strings.Repeat("9", 64)
	if _, err := service.IndexAcceptedResult(context.Background(), changed); err == nil {
		t.Fatal("changed accepted result retry admitted")
	}
	foreignParent := reference
	foreignParent.ParentAgentID = strings.Repeat("8", 64)
	foreignParent.TreeID = reference.TreeID
	if _, err := service.IndexAcceptedResult(context.Background(), foreignParent); err == nil {
		t.Fatal("foreign parent topology admitted")
	}
	foreignRole := reference
	foreignRole.Role = "writer"
	foreignRole.ResultKind = "proposal"
	if _, err := service.IndexAcceptedResult(context.Background(), foreignRole); err == nil {
		t.Fatal("unsupported accepted result kind admitted")
	}
	state, err := Inspect(service.journalPath)
	if err != nil || len(state.Results) != 1 {
		t.Fatal("rejected result changed index", state.Results, err)
	}
}

func TestAcceptedResultRequiresExplicitSuccessfulTurnObservation(t *testing.T) {
	for _, status := range []agenttree.Status{agenttree.StatusRunning, agenttree.StatusUnknown} {
		t.Run(string(status), func(t *testing.T) {
			service, reference := acceptedResultFixture(t, status)
			if _, err := service.IndexAcceptedResult(context.Background(), reference); err == nil {
				t.Fatal("unaccepted turn indexed", status)
			}
			state, err := Inspect(service.journalPath)
			if err != nil || len(state.Results) != 0 {
				t.Fatal("failed index mutated results", state.Results, err)
			}
		})
	}
}

func TestAcceptedResultReplayRejectsSecondReferenceForTurn(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	if _, err := service.IndexAcceptedResult(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
	changed := reference
	changed.ControllerEventHash = strings.Repeat("9", 64)
	changedID, err := changed.ID()
	if err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(service.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	activities := []Activity{
		acceptedResultActivity(changed.ParentAgentID, nextActivitySequence(state, changed.ParentAgentID), changed, changedID),
		acceptedResultActivity(changed.AgentID, nextActivitySequence(state, changed.AgentID), changed, changedID),
	}
	_, err = journal.Append(service.journalPath, "agentcontrol.result-accepted", acceptedResultEvent{Version: 1, Result: AcceptedResult{ResultID: changedID, Reference: changed}, Activities: activities}, func([]journal.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(service.journalPath); err == nil {
		t.Fatal("changed second accepted result replayed")
	}
}

func TestConcurrentAcceptedResultRetriesConvergeOnce(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	wantID, err := reference.ID()
	if err != nil {
		t.Fatal(err)
	}
	const callers = 8
	results := make(chan AcceptedResult, callers)
	errorsSeen := make(chan error, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			result, indexErr := service.IndexAcceptedResult(context.Background(), reference)
			results <- result
			errorsSeen <- indexErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsSeen)
	for indexErr := range errorsSeen {
		if indexErr != nil {
			t.Fatal("identical concurrent retry failed", indexErr)
		}
	}
	for result := range results {
		if result.ResultID != wantID || result.Reference != reference {
			t.Fatal("concurrent retry returned different result", result)
		}
	}
	state, err := Inspect(service.journalPath)
	if err != nil || len(state.Results) != 1 || len(state.Activities) != 4 {
		t.Fatal("concurrent retries did not converge once", len(state.Results), len(state.Activities), err)
	}
}

func TestConcurrentChangedAcceptedResultCannotCreateSecondIndex(t *testing.T) {
	service, reference := acceptedResultFixture(t, agenttree.StatusSucceeded)
	changed := reference
	changed.ControllerEventHash = strings.Repeat("9", 64)
	start := make(chan struct{})
	type outcome struct {
		result AcceptedResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for _, candidate := range []AcceptedResultReference{reference, changed} {
		candidate := candidate
		go func() {
			<-start
			result, err := service.IndexAcceptedResult(context.Background(), candidate)
			outcomes <- outcome{result: result, err: err}
		}()
	}
	close(start)
	succeeded := 0
	failed := 0
	for index := 0; index < 2; index++ {
		observed := <-outcomes
		if observed.err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	state, err := Inspect(service.journalPath)
	if err != nil || succeeded != 1 || failed != 1 || len(state.Results) != 1 || len(state.Activities) != 4 {
		t.Fatal("changed reference race created ambiguous acceptance", succeeded, failed, len(state.Results), len(state.Activities), err)
	}
}

func acceptedResultFixture(t *testing.T, terminal agenttree.Status) (*Service, AcceptedResultReference) {
	t.Helper()
	directory := t.TempDir()
	treePath := filepath.Join(directory, "agent-tree.db")
	controlPath := filepath.Join(directory, "agent-control.db")
	treeID := strings.Repeat("1", 64)
	root, err := agenttree.Create(treePath, treeID, agenttree.NodeSpec{Name: "root", Role: "planner", Authority: agenttree.AuthorityReadOnly, InvocationID: strings.Repeat("2", 64), ContextSHA256: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := agenttree.ReserveChild(treePath, treeID, agenttree.NodeSpec{ParentAgentID: root.AgentID, Name: "explorer", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: strings.Repeat("4", 64), ContextSHA256: strings.Repeat("5", 64)})
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
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: root.AgentID, AgentID: child.AgentID, TurnID: strings.Repeat("6", 64), TurnSequence: 1}
	if err := service.ObserveTurn(context.Background(), turn, agenttree.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	resultHash := strings.Repeat("7", 64)
	if terminal == agenttree.StatusSucceeded {
		if err := service.ObserveTurn(context.Background(), turn, terminal, resultHash); err != nil {
			t.Fatal(err)
		}
	} else if terminal == agenttree.StatusUnknown {
		if err := service.ObserveTurn(context.Background(), turn, terminal, ""); err != nil {
			t.Fatal(err)
		}
	}
	reference := AcceptedResultReference{Version: 1, TreeID: treeID, ParentAgentID: root.AgentID, AgentID: child.AgentID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence, InvocationID: child.InvocationID, AdmissionID: strings.Repeat("8", 64), ControllerEventHash: strings.Repeat("a", 64), Role: AcceptedResultRoleExplorer, ResultKind: AcceptedResultKindExploration, ResultSHA256: resultHash}
	return service, reference
}
