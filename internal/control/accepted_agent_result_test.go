package control

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

type acceptedExplorerFixture struct {
	controllerPath string
	root           agenttree.Node
	child          agenttree.Node
	question       string
	invocation     runtime.Invocation
	turn           taskscheduler.AgentTurnBinding
	task           taskscheduler.TaskSpec
	claim          taskscheduler.Claim
	record         ExplorerRecord
}

func newAcceptedExplorerFixture(t *testing.T) acceptedExplorerFixture {
	t.Helper()
	creation := creation(t)
	creation.Config = modelAccessSnapshot(t, "subscription").Creation.Config
	command := exec.Command("git", "-C", creation.Repository.Root, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	if err := os.WriteFile(filepath.Join(creation.Repository.Root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "file.txt"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base"}} {
		command = exec.Command("git", append([]string{"-C", creation.Repository.Root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	var err error
	creation.Repository, err = repository.Discover(context.Background(), creation.Repository.Root, creation.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	controllerPath := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(controllerPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	planned, err := ResumePlanning(context.Background(), controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(controllerPath, "plan.approved", Approval{PlanID: planned.PlanID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := StartWorkspace(context.Background(), controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := bootstrapAgentTreeRoot(controllerPath+".agent-tree", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	root, ok := agentNodeByID(tree, tree.RootAgentID)
	if !ok {
		t.Fatal("fixture root unavailable")
	}
	fixture := acceptedExplorerFixture{controllerPath: controllerPath, root: root, question: "Index the accepted fixture exploration."}
	fixture = addAcceptedExplorerTurn(t, fixture, agenttree.Node{}, strings.Repeat("6", 64), 1, "SENSITIVE-ACCEPTED-EXPLORATION-BODY")
	return fixture
}

func addAcceptedExplorerTurn(t *testing.T, fixture acceptedExplorerFixture, existing agenttree.Node, turnID string, sequence int, summary string) acceptedExplorerFixture {
	t.Helper()
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	base, err := explorerInvocation(snapshot, fixture.question)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, turnID)
	if err != nil {
		t.Fatal(err)
	}
	child := existing
	if child.AgentID == "" {
		contextHash, hashErr := access.InputID(invocation.Input)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		reservation, reserveErr := agenttree.ReserveChild(fixture.controllerPath+".agent-tree", snapshot.RunID, agenttree.NodeSpec{ParentAgentID: fixture.root.AgentID, Name: "accepted-explorer", Role: "explorer", Authority: agenttree.AuthorityReadOnly, InvocationID: invocation.ID, ContextSHA256: contextHash})
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
		child, err = agenttree.CommitChild(fixture.controllerPath+".agent-tree", reservation.ReservationID)
		if err != nil {
			t.Fatal(err)
		}
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: fixture.root.AgentID, AgentID: child.AgentID, TurnID: turnID, TurnSequence: sequence}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, snapshot, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	candidateID, err := snapshot.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	output, err := canonical.Bytes(Exploration{CandidateID: candidateID, Summary: summary, Paths: []string{"file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	model := invocation.Profile.Model
	record := ExplorerRecord{Question: fixture.question, Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(output)}}
	resultHash, err := canonical.Hash("harness.explorer-result.v1", record.Result)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(binding, resultHash); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordExploration(fixture.controllerPath, record); err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: turnID, RunID: snapshot.RunID, ControllerPath: fixture.controllerPath, Operation: taskscheduler.OperationExplorer, Input: fixture.question, InvocationID: invocation.ID}
	head, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.child, fixture.invocation, fixture.turn, fixture.task, fixture.record = child, invocation, turn, task, record
	fixture.claim = taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: task, Generation: 1, ControllerHead: head, AgentTurn: &fixture.turn}
	return fixture
}

func TestAcceptedExplorerResultIndexesInitialAndFollowUpTurns(t *testing.T) {
	fixture := newAcceptedExplorerFixture(t)
	snapshot, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := scheduledEvidenceWithAcceptedResult(fixture.controllerPath, snapshot, head, fixture.invocation, fixture.task, &fixture.turn)
	if err != nil || evidence.Status != taskscheduler.StatusUnknown {
		t.Fatal("unindexed accepted exploration was presented as terminal", evidence, err)
	}
	if _, _, err := acceptedExplorerResultReference(fixture.controllerPath, strings.Repeat("f", 64), snapshot, fixture.invocation, &fixture.turn, &fixture.record); err == nil {
		t.Fatal("accepted exploration reference ignored a substituted controller prefix")
	}
	indexed, err := ensureAcceptedExplorerResult(context.Background(), fixture.controllerPath, fixture.claim, fixture.invocation, &fixture.record)
	if err != nil || !indexed {
		t.Fatal("initial accepted exploration was not indexed", indexed, err)
	}
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(state.Results) != 1 || state.Results[0].Reference.TurnID != fixture.turn.TurnID || state.Results[0].Reference.TurnSequence != 1 || state.Results[0].Reference.AgentID != fixture.child.AgentID || state.Results[0].Reference.ParentAgentID != fixture.root.AgentID {
		t.Fatal("initial accepted result reference differs", err, state.Results)
	}
	eventsBeforeRetry, err := journal.Read(fixture.controllerPath + ".agent-control")
	if err != nil {
		t.Fatal(err)
	}
	if indexed, err = ensureAcceptedExplorerResult(context.Background(), fixture.controllerPath, fixture.claim, fixture.invocation, &fixture.record); err != nil || !indexed {
		t.Fatal("exact accepted result retry changed outcome", indexed, err)
	}
	eventsAfterRetry, err := journal.Read(fixture.controllerPath + ".agent-control")
	if err != nil || len(eventsAfterRetry) != len(eventsBeforeRetry) || eventsAfterRetry[len(eventsAfterRetry)-1].Hash != eventsBeforeRetry[len(eventsBeforeRetry)-1].Hash {
		t.Fatal("exact accepted result retry appended another event", err)
	}
	journalBytes, err := os.ReadFile(fixture.controllerPath + ".agent-control")
	if err != nil || bytes.Contains(journalBytes, []byte("SENSITIVE-ACCEPTED-EXPLORATION-BODY")) {
		t.Fatal("accepted result index retained exploration body", err)
	}

	first := fixture
	fixture = addAcceptedExplorerTurn(t, fixture, fixture.child, strings.Repeat("8", 64), 2, "SENSITIVE-FOLLOWUP-EXPLORATION-BODY")
	if indexed, err = ensureAcceptedExplorerResult(context.Background(), fixture.controllerPath, fixture.claim, fixture.invocation, &fixture.record); err != nil || !indexed {
		t.Fatal("follow-up accepted exploration was not indexed", indexed, err)
	}
	state, err = agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(state.Results) != 2 || state.Results[0].Reference.TurnID != first.turn.TurnID || state.Results[1].Reference.TurnID != fixture.turn.TurnID || state.Results[1].Reference.TurnSequence != 2 || state.Results[1].Reference.AgentID != first.child.AgentID {
		t.Fatal("follow-up accepted result reference differs", err, state.Results)
	}
	journalBytes, err = os.ReadFile(fixture.controllerPath + ".agent-control")
	if err != nil || bytes.Contains(journalBytes, []byte("SENSITIVE-FOLLOWUP-EXPLORATION-BODY")) {
		t.Fatal("follow-up accepted result index retained exploration body", err)
	}
}

func TestUnknownScheduledExplorationRepairsAcceptedIndexWithoutRuntimeSend(t *testing.T) {
	fixture := newAcceptedExplorerFixture(t)
	schedulerPath := filepath.Join(t.TempDir(), "scheduler.jsonl")
	seed, err := PrepareScheduledTask(fixture.controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("5", 64)
	if _, err := taskscheduler.Bind(schedulerPath, taskscheduler.Definition{Version: 1, Nonce: "accepted-result-recovery", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	adapter := ScheduledDispatchAdapter{JournalPath: schedulerPath}
	seedDecision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter)
	if err != nil || seedDecision.TaskID != seed.ID || seedDecision.Status != taskscheduler.StatusSucceeded {
		t.Fatal("accepted result recovery seed did not settle", seedDecision, err)
	}
	if _, err := taskscheduler.AddTask(schedulerPath, taskscheduler.DynamicTask{Task: fixture.task, ParentAgentID: fixture.turn.ParentAgentID, AgentID: fixture.turn.AgentID, TurnID: fixture.turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	decision, err := taskscheduler.Tick(context.Background(), schedulerPath, adapter)
	if err != nil || decision.Status != taskscheduler.StatusUnknown {
		t.Fatal("accepted result gap did not remain UNKNOWN for recovery", decision, err)
	}
	beforeRuntime, err := filepath.Glob(fixture.controllerPath + ".explorer.turn-*.opencode-runtime.jsonl")
	if err != nil || len(beforeRuntime) != 0 {
		t.Fatal("fixture unexpectedly has provider runtime history", beforeRuntime, err)
	}
	recovered, err := taskscheduler.RecoverClaim(context.Background(), schedulerPath, decision.ClaimID, adapter)
	if err != nil || recovered.Status != taskscheduler.StatusSucceeded || recovered.ClaimID != decision.ClaimID {
		t.Fatal("UNKNOWN accepted result index was not repaired", recovered, err)
	}
	afterRuntime, err := filepath.Glob(fixture.controllerPath + ".explorer.turn-*.opencode-runtime.jsonl")
	if err != nil || len(afterRuntime) != 0 {
		t.Fatal("accepted result repair dispatched a provider runtime", afterRuntime, err)
	}
	state, err := agentcontrol.Inspect(fixture.controllerPath + ".agent-control")
	if err != nil || len(state.Results) != 1 || state.Results[0].Reference.TurnID != fixture.turn.TurnID {
		t.Fatal("recovered accepted result index unavailable", err, state.Results)
	}
}
