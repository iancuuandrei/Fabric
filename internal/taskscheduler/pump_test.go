package taskscheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPumpDispatchesLateChildWhileParentWaits(t *testing.T) {
	parent := TaskSpec{ID: "parent", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	child := TaskSpec{ID: digest('2'), RunID: parent.RunID, ControllerPath: parent.ControllerPath, Operation: OperationExplorer, InvocationID: digest('3'), Input: "inspect"}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, parent)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started, childDone := make(chan struct{}), make(chan struct{})
	adapter := &fakeAdapter{evidence: map[string]Evidence{parent.ID: readyEvidence(parent, 'b'), child.ID: readyEvidence(child, 'c')}, dispatches: map[string]int{}}
	adapter.dispatch = func(claim Claim) (Evidence, error) {
		if claim.Task.ID == parent.ID {
			close(started)
			select {
			case <-childDone:
			case <-ctx.Done():
				return Evidence{}, ctx.Err()
			}
		} else {
			close(childDone)
		}
		evidence := admittedEvidence(claim.Task, StatusSucceeded, 'e')
		adapter.set(claim.Task.ID, evidence)
		return evidence, nil
	}
	finished := make(chan error, 1)
	go func() {
		finished <- Pump(ctx, path, adapter, PumpOptions{Workers: 2, PollInterval: 10 * time.Millisecond})
	}()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop")
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("parent did not start")
	}
	// The second worker must remain alive after finding no eligible work.
	time.Sleep(30 * time.Millisecond)
	if _, err := AddTask(path, DynamicTask{Task: child, ParentAgentID: digest('c'), AgentID: digest('d'), TurnID: child.ID}); err != nil {
		t.Fatal(err)
	}
	for {
		snapshot, err := Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Tasks[parent.ID].Status == StatusSucceeded && snapshot.Tasks[child.ID].Status == StatusSucceeded {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("late child did not unblock parent")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if adapter.count(parent.ID) != 1 || adapter.count(child.ID) != 1 {
		t.Fatal("duplicate dispatch")
	}
}

func TestPumpRejectsInvalidOptionsBeforeJournalAccess(t *testing.T) {
	for _, options := range []PumpOptions{{Workers: 0}, {Workers: 65}, {Workers: 1, PollInterval: time.Nanosecond}} {
		if err := Pump(context.Background(), "absent", &fakeAdapter{}, options); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}

func TestPumpStopsAllWorkersOnProbeFailure(t *testing.T) {
	task := TaskSpec{ID: "task", RunID: digest('a'), ControllerPath: filepath.Join(t.TempDir(), "run"), Operation: OperationPlanner, InvocationID: digest('1')}
	path := filepath.Join(t.TempDir(), "schedule")
	if _, err := Bind(path, scheduleDefinition(t, task)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	adapter := &fakeAdapter{evidence: map[string]Evidence{}, dispatches: map[string]int{}}
	if err := Pump(ctx, path, adapter, PumpOptions{Workers: 4}); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("probe error did not stop pump", err)
	}
	snapshot, err := Inspect(path)
	if err != nil || snapshot.Tasks[task.ID].Claim != nil || adapter.count(task.ID) != 0 {
		t.Fatal("failed probe admitted work", snapshot, err)
	}
}
