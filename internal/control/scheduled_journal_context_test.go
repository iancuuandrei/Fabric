package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/taskscheduler"
)

type scheduledJournalCaptureAdapter struct {
	path     string
	claim    taskscheduler.Claim
	observed string
}

func (a *scheduledJournalCaptureAdapter) Probe(_ context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	return taskscheduler.Evidence{RunID: request.Task.RunID, InvocationID: request.Task.InvocationID, ControllerHead: strings.Repeat("c", 64), Status: taskscheduler.StatusReady}, nil
}

func (a *scheduledJournalCaptureAdapter) Dispatch(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	a.claim = claim
	bound, err := (ScheduledDispatchAdapter{JournalPath: a.path}).bindJournalContext(ctx, claim)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	a.observed, _ = scheduledDispatchJournalPath(bound)
	return taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: claim.Task.InvocationID, ControllerHead: claim.ControllerHead, AdmissionID: strings.Repeat("d", 64), Status: taskscheduler.StatusSucceeded}, nil
}

func (*scheduledJournalCaptureAdapter) Reconcile(context.Context, taskscheduler.Claim) (taskscheduler.Evidence, error) {
	panic("unexpected reconciliation")
}

func TestScheduledDispatchJournalContextRequiresExactDurableClaim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedule.jsonl")
	task := taskscheduler.TaskSpec{
		ID: strings.Repeat("1", 64), RunID: strings.Repeat("2", 64),
		ControllerPath: filepath.Join(t.TempDir(), "run.jsonl"), Operation: taskscheduler.OperationExplorer,
		Input: "Inspect the exact source.", InvocationID: strings.Repeat("3", 64),
	}
	definition := taskscheduler.Definition{Version: 1, Nonce: "scheduled-journal-context", Tasks: []taskscheduler.TaskSpec{task}}
	if _, err := taskscheduler.Bind(path, definition); err != nil {
		t.Fatal(err)
	}
	adapter := &scheduledJournalCaptureAdapter{path: path}
	decision, err := taskscheduler.Tick(context.Background(), path, adapter)
	if err != nil || decision.Status != taskscheduler.StatusSucceeded || adapter.observed != path {
		t.Fatal("exact scheduled journal was not exposed during dispatch", decision, adapter.observed, err)
	}

	bound, err := (ScheduledDispatchAdapter{JournalPath: path}).bindJournalContext(context.Background(), adapter.claim)
	if err != nil {
		t.Fatal("terminal exact claim no longer supports read-only recovery binding", err)
	}
	if observed, ok := scheduledDispatchJournalPath(bound); !ok || observed != path {
		t.Fatal("exact terminal claim path unavailable", observed, ok)
	}

	pathless, err := (ScheduledDispatchAdapter{}).bindJournalContext(context.Background(), adapter.claim)
	if err != nil {
		t.Fatal(err)
	}
	if observed, ok := scheduledDispatchJournalPath(pathless); ok || observed != "" {
		t.Fatal("legacy pathless adapter exposed scheduler context", observed)
	}

	foreignPath := filepath.Join(t.TempDir(), "schedule.jsonl")
	if _, err := taskscheduler.Bind(foreignPath, definition); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		path  string
		claim taskscheduler.Claim
	}{
		{"foreign path without claim", foreignPath, adapter.claim},
		{"relative path", "schedule.jsonl", adapter.claim},
	}
	substitutedSchedule := adapter.claim
	substitutedSchedule.ScheduleID = strings.Repeat("4", 64)
	cases = append(cases, struct {
		name  string
		path  string
		claim taskscheduler.Claim
	}{"schedule identity", path, substitutedSchedule})
	substitutedTurn := adapter.claim
	substitutedTurn.AgentTurn = &taskscheduler.AgentTurnBinding{
		ParentAgentID: strings.Repeat("5", 64), AgentID: strings.Repeat("6", 64),
		TurnID: adapter.claim.Task.ID, TurnSequence: 1,
	}
	cases = append(cases, struct {
		name  string
		path  string
		claim taskscheduler.Claim
	}{"agent turn", path, substitutedTurn})
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (ScheduledDispatchAdapter{JournalPath: test.path}).bindJournalContext(context.Background(), test.claim); err == nil {
				t.Fatal("substituted scheduler context accepted")
			}
		})
	}
}
