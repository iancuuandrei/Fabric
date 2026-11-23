package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agentcontext"
	"github.com/iancuuandrei/fabric/internal/agentcontrol"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/workingcontext"
)

func TestExplorerWorkingContextCodexSettledResultUsesCachedReceipt(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableHash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	authSource := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authSource, []byte(`{"tokens":{"access_token":"fixture-token","account_id":"fixture-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1}
	fixture := newExplorerFixtureWithConfig(t, policy, func(c *Creation) {
		c.Config.ReviewerContract = "json-v1" // Structured planner envelope, as on autonomous runs.
		c.AgentContext, err = agentcontext.Capture(context.Background(), c.Repository)
		if err != nil {
			t.Fatal(err)
		}
		c.Config.Explorer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explorer-fixture", Effort: "high", Role: "explorer"}
		c.Config.Codex = &config.Codex{Executable: executable, ExecutableHash: executableHash, StateRoot: stateRoot, AuthSource: authSource, UsageQualified: true}
		c.Config.Access.Profiles = append(c.Config.Access.Profiles, access.Profile{Version: 1, Name: "chatgpt", Kind: "subscription", Runtime: "codex-app-server", Provider: "openai", AuthMode: "chatgpt-session", RepositoryClasses: []access.Class{access.Private}})
		c.Config.Access.Roles["explorer"] = "chatgpt"
	}, false)
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := filepath.Join(t.TempDir(), "scheduler.jsonl")
	seed := taskscheduler.TaskSpec{ID: "seed", RunID: s.RunID, ControllerPath: fixture.controllerPath, Operation: taskscheduler.OperationExplorer, Input: "seed", InvocationID: strings.Repeat("a", 64)}
	if _, err := taskscheduler.Bind(scheduler, taskscheduler.Definition{Version: 1, Nonce: "cached-context", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	_, dynamic, err := SpawnExplorerAgent(context.Background(), fixture.controllerPath, scheduler, fixture.root.AgentID, "cached-explorer", fixture.question, "cached-receipt")
	if err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	invocation, err := scheduledInvocationFromSnapshot(s, dynamic.Task, &turn)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, s, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := expectedExplorerHostForInvocation(s, fixture.question, invocation)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executeExplorer(context.Background(), fixture.controllerPath, s, expected)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := canonical.Hash("harness.explorer-result.v1", result)
	if err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatch(binding, hash); err != nil {
		t.Fatal(err)
	}
	// The provider completed, but the controller has not admitted the observation.
	// Recover this durable prefix using its cached runtime receipt only.
	methods := filepath.Join(expected.Launch.Root, "home", "fixture-methods.log")
	before, err := os.ReadFile(methods)
	if err != nil {
		t.Fatal(err)
	}
	head, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("c", 64), Task: dynamic.Task, Generation: 1, ControllerHead: head, AgentTurn: &turn}
	evidence, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, claim)
	if err != nil || evidence.Status != taskscheduler.StatusSucceeded {
		t.Fatal("cached Codex result was not admitted", evidence, err)
	}
	after, err := os.ReadFile(methods)
	if err != nil || string(after) != string(before) {
		t.Fatal("cached recovery made new app-server calls", err)
	}
	admitted, err := Inspect(fixture.controllerPath)
	if err != nil || len(admitted.Explorations) != 1 || !sameCanonical(admitted.Explorations[0].Result, result) || len(admitted.WorkingContextHistory) != 1 {
		t.Fatal("cached recovery changed result or lost context", err)
	}
}

func TestExplorerWorkingContextCompletedTurnKeepsFrozenGuidanceScope(t *testing.T) {
	for _, version := range []int{0, 1} {
		t.Run(string(rune('A'+version)), func(t *testing.T) {
			policy := &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, ScheduledExplorerDispatchVersion: 1, WorkingContextVersion: version}
			fixture := newExplorerFixtureWithConfig(t, policy, func(c *Creation) {
				c.Config.ReviewerContract = "json-v1"
				var err error
				c.AgentContext, err = agentcontext.Capture(context.Background(), c.Repository)
				if err != nil {
					t.Fatal(err)
				}
			}, true)
			s, err := Inspect(fixture.controllerPath)
			if err != nil {
				t.Fatal(err)
			}
			base := mustExplorerBase(t, s, fixture.question)
			rederived, err := scopedScheduledInvocation(s, base, fixture.task.Operation, &fixture.turn, "")
			if err != nil || rederived.ID == fixture.invocation.ID {
				t.Fatal("fixture failed to change guidance scope after observation", err)
			}
			got, err := scheduledInvocationFromSnapshot(s, fixture.task, &fixture.turn)
			if err != nil || got != fixture.invocation {
				t.Fatal("completed turn lost its frozen input", err)
			}
			if repeated, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, fixture.claim); err != nil || repeated.Status != taskscheduler.StatusSucceeded {
				t.Fatal("completed scope-drift turn was not observed", err)
			}
			for _, mutation := range []string{"question", "invocation", "turn"} {
				task, turn := fixture.task, fixture.turn
				switch mutation {
				case "question":
					task.Input += " changed"
				case "invocation":
					task.InvocationID = strings.Repeat("f", 64)
				case "turn":
					turn.AgentID = strings.Repeat("e", 64)
				}
				if _, ok := acceptedScheduledExplorerInvocation(s, task, &turn); ok {
					t.Fatal("accepted result granted a different frozen task", mutation)
				}
			}
		})
	}
}

func TestExplorerWorkingContextUsageKeepsCanonicalIntegerMean(t *testing.T) {
	s := Snapshot{WorkingContextHistory: []ExplorerWorkingContextRecord{
		{Status: "updated", Projection: &workingcontext.Context{SizeBytes: 1}},
		{Status: "updated", Projection: &workingcontext.Context{SizeBytes: 2}},
	}}
	u := measureWorkingContextUsage(s)
	if u.AverageVersionBytes != 1 || u.TotalVersionBytes != 3 || u.AcceptedRewrites != 2 {
		t.Fatal("integer mean lost exact numerator/count", u)
	}
	if _, err := canonical.Bytes(u); err != nil {
		t.Fatal("working context usage broke canonical CLI output", err)
	}
}

func TestExplorerWorkingContextRetainsOwnNotesAndFrozenReplay(t *testing.T) {
	policy := &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1}
	fixture := newAcceptedExplorerFixtureWithPolicy(t, policy)
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.WorkingContextHistory) != 1 || s.WorkingContextHistory[0].Status != "updated" {
		t.Fatal("initial result did not seed context", s.WorkingContextHistory)
	}
	first := *s.WorkingContextHistory[0].Projection
	if first.Binding.AgentID != fixture.child.AgentID || first.Binding.TaskID != fixture.child.InvocationID || first.Binding.JournalHead != s.WorkingContextHistory[0].EventHash {
		t.Fatal("projection lacks exact agent/task/event provenance", first.Binding)
	}
	initial := fixture
	fixture = addAcceptedExplorerTurn(t, fixture, fixture.child, strings.Repeat("8", 64), 2, "Retain file.txt; previous hypothesis resolved.")
	var input workingContextTurnEnvelope
	if err := canonical.Decode([]byte(fixture.invocation.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.Context == nil || *input.Context != first || input.ContextAgentID != fixture.child.AgentID || input.ExpectedID != first.ID {
		t.Fatal("follow-up did not receive its own exact context", input)
	}
	s, err = Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.WorkingContextHistory) != 2 || s.WorkingContextHistory[1].Projection.Content != "Retain file.txt; previous hypothesis resolved." {
		t.Fatal("replacement did not retain new reasoning")
	}
	stats := measureWorkingContextUsage(s)
	if stats.AcceptedRewrites != 2 || stats.RejectedUpdates != 0 || stats.TotalVersionBytes != first.SizeBytes+s.WorkingContextHistory[1].Projection.SizeBytes {
		t.Fatal("projection accounting is inaccurate", stats)
	}
	for _, frozen := range []acceptedExplorerFixture{initial, fixture} {
		got, err := scheduledInvocationFromSnapshot(s, frozen.task, &frozen.turn)
		if err != nil || got != frozen.invocation {
			t.Fatal("later rewrite changed frozen invocation", err)
		}
		resolved, err := resolveScheduledInvocationID(s, mustExplorerBase(t, s, frozen.question), frozen.invocation.ID)
		if err != nil || resolved != frozen.invocation {
			t.Fatal("replay replaced admitted input", err)
		}
	}
	handoff, err := writerExplorationContext(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range handoff.Records {
		if len(item.Observation.WorkingContextUpdate) != 0 {
			t.Fatal("writer inherited private retention state")
		}
	}
	base := mustExplorerBase(t, s, fixture.question)
	other, err := scopedExplorerContextInvocation(s, base, strings.Repeat("9", 64), strings.Repeat("a", 64), "")
	if err != nil {
		t.Fatal(err)
	}
	input = workingContextTurnEnvelope{}
	if err := canonical.Decode([]byte(other.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.Context != nil || input.ExpectedID != "" {
		t.Fatal("another agent inherited notes")
	}
	// Corruption of a disposable projection cannot make new work fail. Existing
	// frozen claims are not replaced: absent preimage means dispatch is denied.
	s.WorkingContextHistory[1].Projection.ContentHash = strings.Repeat("f", 64)
	if _, err := scopedExplorerContextInvocation(s, base, strings.Repeat("b", 64), fixture.child.AgentID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := scopedExplorerContextInvocation(s, base, fixture.turn.TurnID, fixture.child.AgentID, strings.Repeat("c", 64)); err == nil {
		t.Fatal("missing frozen identity silently recreated")
	}
}

func TestExplorerWorkingContextRejectsBoundedOrStaleNotesWithoutAuthorityChange(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1})
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{strings.Repeat("x", workingcontext.MaxContentBytes+1), "invalid\x00note"} {
		copy := s
		copy.WorkingContextHistory = nil // Exact empty preimage tests content bounds, not a stale CAS.
		record := fixture.record
		var observation Exploration
		if err := canonical.Decode([]byte(record.Result.Output), &observation); err != nil {
			t.Fatal(err)
		}
		observation.WorkingContextUpdate, _ = canonical.Bytes(workingcontext.Replacement{ExpectedContentHash: emptyWorkingContextHash, Content: content})
		body, _ := canonical.Bytes(observation)
		record.Result.Output = string(body)
		if err := recordExplorerWorkingContext(&copy, record, strings.Repeat("d", 64)); err != nil {
			t.Fatal(err)
		}
		last := copy.WorkingContextHistory[len(copy.WorkingContextHistory)-1]
		if last.Status != "rejected_update" || last.Projection != nil || copy.State != s.State || copy.Candidate != s.Candidate || copy.RunID != s.RunID {
			t.Fatal("invalid note altered authority")
		}
	}
	if len(compatibleExplorerContexts(s, fixture.child.AgentID)) != 1 {
		t.Fatal("valid prior context lost")
	}
	changed := s
	changed.RunID = strings.Repeat("e", 64)
	if len(compatibleExplorerContexts(changed, fixture.child.AgentID)) != 0 {
		t.Fatal("detached run projection admitted")
	}
}

func mustExplorerBase(t *testing.T, s Snapshot, question string) runtime.Invocation {
	t.Helper()
	base, err := explorerInvocation(s, question)
	if err != nil {
		t.Fatal(err)
	}
	return base
}

func TestExplorerWorkingContextDefaultPreservesInvocation(t *testing.T) {
	fixture := newAcceptedExplorerFixture(t)
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	base := mustExplorerBase(t, s, fixture.question)
	got, err := scopedExplorerContextInvocation(s, base, fixture.turn.TurnID, fixture.child.AgentID, "")
	want, legacyErr := scheduledTurnInvocation(base, taskscheduler.OperationExplorer, fixture.turn.TurnID)
	if err != nil || legacyErr != nil || got != want || len(s.WorkingContextHistory) != 0 {
		t.Fatal("default contract changed", err, legacyErr)
	}
}

func TestExplorerWorkingContextFollowUpFactoryUsesAcceptedProjection(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1})
	seed, err := PrepareScheduledTask(fixture.controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	seed.ID = strings.Repeat("a", 64)
	path := filepath.Join(t.TempDir(), "schedule.jsonl")
	if _, err := taskscheduler.Bind(path, taskscheduler.Definition{Version: 1, Nonce: "working-context", Tasks: []taskscheduler.TaskSpec{seed}}); err != nil {
		t.Fatal(err)
	}
	// Include the initial child's exact scheduled turn so the follow-up is turn 2.
	if _, err := taskscheduler.AddTask(path, taskscheduler.DynamicTask{Task: fixture.task, ParentAgentID: fixture.root.AgentID, AgentID: fixture.child.AgentID, TurnID: fixture.turn.TurnID}); err != nil {
		t.Fatal(err)
	}
	question := "Follow up the accepted observation without repeating earlier source reads."
	_, dynamic, err := FollowUpExplorerAgent(context.Background(), fixture.controllerPath, path, agentcontrol.MessageRequest{FromAgentID: fixture.root.AgentID, ToAgentID: fixture.child.AgentID, Nonce: "follow-up", Body: question}, question)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	invocation, err := scheduledInvocationFromSnapshot(s, dynamic.Task, &turn)
	if err != nil {
		t.Fatal(err)
	}
	var envelope workingContextTurnEnvelope
	if err := canonical.Decode([]byte(invocation.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Context == nil || envelope.Context.ID != s.WorkingContextHistory[0].Projection.ID || envelope.Context.Content != "SENSITIVE-ACCEPTED-EXPLORATION-BODY" {
		t.Fatal("real follow-up factory omitted accepted notes")
	}
	head, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("7", 64), Task: dynamic.Task, Generation: 1, ControllerHead: head, AgentTurn: &turn}
	evidence, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, claim)
	if err != nil || evidence.Status != taskscheduler.StatusSucceeded {
		t.Fatal("real dispatcher did not execute managed explorer", evidence, err)
	}
	admitted, err := Inspect(fixture.controllerPath)
	if err != nil || len(admitted.WorkingContextHistory) != 2 || admitted.WorkingContextHistory[1].Status != "updated" || admitted.WorkingContextHistory[1].Projection.Content != question {
		t.Fatal("runtime result did not replace context", err)
	}
	beforeRepeat, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if repeated, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, claim); err != nil || repeated.Status != taskscheduler.StatusSucceeded {
		t.Fatal("completed replay changed result", err)
	}
	afterRepeat, err := controllerJournalHead(fixture.controllerPath)
	if err != nil || afterRepeat != beforeRepeat {
		t.Fatal("completed turn was resent", err)
	}
}

func TestExplorerWorkingContextQueuedBeforeAcceptanceRejectsStaleNotes(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1})
	events, err := journal.Read(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Kind != "explorer.recorded" {
		t.Fatal("unexpected fixture order")
	}
	// Derive the queued follow-up from the exact prefix before initial notes
	// were admitted. Its immutable preimage is deliberately empty.
	before, err := Replay(events[:len(events)-1])
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := scopedExplorerContextInvocation(before, mustExplorerBase(t, before, fixture.question), strings.Repeat("e", 64), fixture.child.AgentID, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: fixture.root.AgentID, AgentID: fixture.child.AgentID, TurnID: strings.Repeat("e", 64), TurnSequence: 2}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, s, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	candidate, _ := s.Candidate.ID()
	update, _ := canonical.Bytes(workingcontext.Replacement{ExpectedContentHash: emptyWorkingContextHash, Content: "A stale rewrite must not replace the accepted first notes."})
	body, _ := canonical.Bytes(Exploration{CandidateID: candidate, Summary: "Useful second observation remains admitted.", Paths: []string{"file.txt"}, WorkingContextUpdate: update})
	model := invocation.Profile.Model
	record := ExplorerRecord{Question: fixture.question, Invocation: invocation, Result: runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(body)}}
	hash, _ := canonical.Hash("harness.explorer-result.v1", record.Result)
	if err := finishAgentDispatch(binding, hash); err != nil {
		t.Fatal(err)
	}
	admitted, err := RecordExploration(fixture.controllerPath, record)
	if err != nil {
		t.Fatal(err)
	}
	if len(admitted.Explorations) != 2 || len(admitted.WorkingContextHistory) != 2 || admitted.WorkingContextHistory[1].Status != "rejected_update" || admitted.WorkingContextHistory[1].Projection != nil {
		t.Fatal("stale note did not degrade independently")
	}
	if got := compatibleExplorerContexts(admitted, fixture.child.AgentID); len(got) != 1 || got[0].Content != "SENSITIVE-ACCEPTED-EXPLORATION-BODY" {
		t.Fatal("stale CAS replaced earlier notes")
	}
	resolved, err := resolveScheduledInvocationID(admitted, mustExplorerBase(t, admitted, fixture.question), invocation.ID)
	if err != nil || resolved != invocation {
		t.Fatal("stale update changed frozen invocation", err)
	}
}

func TestExplorerWorkingContextUnknownRetainsHistoryAndRefusesResend(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1})
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: fixture.root.AgentID, AgentID: fixture.child.AgentID, TurnID: strings.Repeat("f", 64), TurnSequence: 2}
	invocation, err := scopedExplorerContextInvocation(s, mustExplorerBase(t, s, fixture.question), turn.TurnID, turn.AgentID, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, s, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatchUnknown(binding); err != nil {
		t.Fatal(err)
	}
	before, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: turn.TurnID, RunID: s.RunID, ControllerPath: fixture.controllerPath, Operation: taskscheduler.OperationExplorer, Input: fixture.question, InvocationID: invocation.ID}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("c", 64), Task: task, Generation: 1, ControllerHead: before, AgentTurn: &turn}
	if _, err := ReconcileScheduledClaim(context.Background(), fixture.controllerPath, claim); err == nil {
		t.Fatal("unknown turn with no runtime receipt was resent")
	}
	after, err := controllerJournalHead(fixture.controllerPath)
	if err != nil || after != before {
		t.Fatal("unknown recovery mutated durable state", err)
	}
	fresh, err := Inspect(fixture.controllerPath)
	if err != nil || len(fresh.WorkingContextHistory) != 1 || fresh.WorkingContextHistory[0].Projection.ID != s.WorkingContextHistory[0].Projection.ID {
		t.Fatal("unknown turn changed context", err)
	}
}

func TestExplorerWorkingContextSettledResultBeforeRecordRecoversExactly(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, WorkingContextVersion: 1, ScheduledExplorerDispatchVersion: 1})
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: fixture.root.AgentID, AgentID: fixture.child.AgentID, TurnID: strings.Repeat("d", 64), TurnSequence: 2}
	invocation, err := scopedExplorerContextInvocation(s, mustExplorerBase(t, s, fixture.question), turn.TurnID, turn.AgentID, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := beginAgentDispatchForTurn(fixture.controllerPath, s, invocation, &turn)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	var envelope workingContextTurnEnvelope
	if err := canonical.Decode([]byte(invocation.Input), &envelope); err != nil {
		t.Fatal(err)
	}
	candidate, _ := s.Candidate.ID()
	update, _ := canonical.Bytes(workingcontext.Replacement{ExpectedID: envelope.ExpectedID, ExpectedContentHash: envelope.ExpectedHash, Content: fixture.question})
	body, _ := canonical.Bytes(Exploration{CandidateID: candidate, Summary: fixture.question, Paths: []string{}, WorkingContextUpdate: update})
	model := invocation.Profile.Model
	result := runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(body)}
	hash, _ := canonical.Hash("harness.explorer-result.v1", result)
	if err := finishAgentDispatch(binding, hash); err != nil {
		t.Fatal(err)
	}
	// Simulate the durable prefix after result settlement, before exploration
	// admission. No fresh provider attempt is permitted to bridge this gap.
	head, err := controllerJournalHead(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: turn.TurnID, RunID: s.RunID, ControllerPath: fixture.controllerPath, Operation: taskscheduler.OperationExplorer, Input: fixture.question, InvocationID: invocation.ID}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("c", 64), Task: task, Generation: 1, ControllerHead: head, AgentTurn: &turn}
	evidence, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, claim)
	if err != nil || evidence.Status != taskscheduler.StatusSucceeded {
		t.Fatal("settled result could not finish exact admission", evidence, err)
	}
	admitted, err := Inspect(fixture.controllerPath)
	if err != nil || len(admitted.Explorations) != 2 || !sameCanonical(admitted.Explorations[1].Result, result) {
		t.Fatal("recovery invented a different result", err)
	}
}

func TestExplorerDispatchBaselineKeepsLegacyInputWithoutRetention(t *testing.T) {
	fixture := newAcceptedExplorerFixtureWithPolicy(t, &ExecutionPolicy{Mode: "autonomous-v1", GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, ScheduledExplorerDispatchVersion: 1})
	s, err := Inspect(fixture.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: fixture.root.AgentID, AgentID: fixture.child.AgentID, TurnID: strings.Repeat("b", 64), TurnSequence: 2}
	invocation, err := scheduledTurnInvocation(mustExplorerBase(t, s, fixture.question), taskscheduler.OperationExplorer, turn.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	task := taskscheduler.TaskSpec{ID: turn.TurnID, RunID: s.RunID, ControllerPath: fixture.controllerPath, Operation: taskscheduler.OperationExplorer, Input: fixture.question, InvocationID: invocation.ID}
	head, _ := controllerJournalHead(fixture.controllerPath)
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("c", 64), Task: task, Generation: 1, ControllerHead: head, AgentTurn: &turn}
	evidence, err := ExecuteScheduledClaim(context.Background(), fixture.controllerPath, claim)
	if err != nil || evidence.Status != taskscheduler.StatusSucceeded {
		t.Fatal("baseline could not use matched executor", evidence, err)
	}
	admitted, err := Inspect(fixture.controllerPath)
	if err != nil || len(admitted.WorkingContextHistory) != 0 || len(admitted.Explorations) != 2 || admitted.Explorations[1].Invocation != invocation {
		t.Fatal("baseline acquired working context or changed legacy input", err)
	}
	base := mustExplorerBase(t, s, fixture.question)
	drifted, err := runtime.NewInvocationWithCodexAutoCompact(base.Profile, base.Input+" ", base.CodexAutoCompactOption())
	if err != nil {
		t.Fatal(err)
	}
	ctx := withScheduledAgentTurn(context.Background(), &turn)
	ctx = context.WithValue(ctx, workingContextInvocationKey{}, invocation.ID)
	if _, err := scheduledExplorerInvocationFromContext(ctx, s, drifted); err == nil {
		t.Fatal("baseline ignored frozen claim identity after input drift")
	}
}
