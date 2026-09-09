package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

func checkScheduleCLI(t *testing.T, root string, runState control.Snapshot, run func(...string) []byte) {
	t.Helper()
	creation := runState.Creation
	creation.Nonce += "-schedule-v2"
	creation.Config.Version = 2
	creation.Config.Access = &config.Access{
		Class:       access.Public,
		Limits:      access.Limits{Tokens: 1000, Concurrency: 1},
		Profiles:    []access.Profile{{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture-session", RepositoryClasses: []access.Class{access.Public}}},
		Roles:       map[string]string{"planner": "fixture"},
		Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 600}},
	}
	createdID, err := canonical.Hash("harness.run.v1", creation)
	if err != nil {
		t.Fatal(err)
	}
	createdPath, err := runPath(root, createdID)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(createdPath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run("resume", createdID), &runState); err != nil {
		t.Fatal(err)
	}
	checkAgentCLI(t, root, runState, run)
	checkAgentSchedulingCLI(t, root, runState, run)
	pendingCreation := creation
	pendingCreation.Nonce += "-dependent"
	pendingCreation.Objective = "Execute a dependent planner through the task schedule"
	pendingID, err := canonical.Hash("harness.run.v1", pendingCreation)
	if err != nil {
		t.Fatal(err)
	}
	pendingPath, err := runPath(root, pendingID)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(pendingPath, "run.created", pendingCreation); err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(runState.Creation.Config.Planner, runState.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	controllerPath, err := runPath(root, runState.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var task taskscheduler.TaskSpec
	if err := json.Unmarshal(run("schedule-task", runState.RunID, "plan", "planner"), &task); err != nil {
		t.Fatal(err)
	}
	if task.RunID != runState.RunID || task.InvocationID != invocation.ID || task.ControllerPath != controllerPath {
		t.Fatal("prepared task identity differs")
	}
	var dependent taskscheduler.TaskSpec
	if err := json.Unmarshal(run("schedule-task", pendingID, "dependent", "planner"), &dependent); err != nil {
		t.Fatal(err)
	}
	dependent.DependsOn = []string{"plan"}
	definition := taskscheduler.Definition{Version: 1, Nonce: "cli-fixture", Tasks: []taskscheduler.TaskSpec{task, dependent}}
	body, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "schedule.json")
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	created := run("schedule-create", input)
	id, err := definition.ID()
	if err != nil {
		t.Fatal(err)
	}
	if inspected := run("schedule-inspect", id); !bytes.Equal(created, inspected) {
		t.Fatal("schedule inspection changed bound definition")
	}
	before, err := journal.ExportJSONL(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	run("schedule-tick", id)
	var scheduled taskscheduler.Snapshot
	if err := json.Unmarshal(run("schedule-inspect", id), &scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled.Tasks["plan"].Status != taskscheduler.StatusSucceeded {
		t.Fatal("scheduler did not adopt completed planner result")
	}
	claim := scheduled.Tasks["plan"].Claim
	if claim == nil {
		t.Fatal("completed task lost exact claim")
	}
	claimID, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	var recovered taskscheduler.Decision
	if err := json.Unmarshal(run("schedule-recover", id, claimID), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Status != taskscheduler.StatusSucceeded || recovered.ClaimID != claimID {
		t.Fatal("recovery changed completed claim identity")
	}
	run("schedule-tick", id)
	finished, err := control.Inspect(pendingPath)
	if err != nil || finished.State != "AWAITING_APPROVAL" || finished.PlannerAccess == nil {
		t.Fatal("dependent planner was not executed with durable admission", err)
	}
	pendingBefore, err := journal.ExportJSONL(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	run("schedule-tick", id)
	pendingAfter, err := journal.ExportJSONL(pendingPath)
	if err != nil || !bytes.Equal(pendingBefore, pendingAfter) {
		t.Fatal("dependent planner was executed twice", err)
	}
	var batch []scheduleWorkerResult
	if err := json.Unmarshal(run("schedule-tick", id, "2"), &batch); err != nil || len(batch) != 2 {
		t.Fatal("parallel tick did not report each worker", err)
	}
	for _, result := range batch {
		if result.Failed {
			t.Fatal("completed schedule parallel observation failed")
		}
	}
	for _, invalid := range []string{"0", "65", "invalid"} {
		var denied bytes.Buffer
		if err := Execute(context.Background(), []string{"schedule-tick", id, invalid}, root, &denied); err == nil || denied.Len() != 0 {
			t.Fatal("invalid worker count admitted")
		}
		if err := Execute(context.Background(), []string{"schedule-run", id, invalid}, root, &denied); err == nil || denied.Len() != 0 {
			t.Fatal("invalid pump worker count admitted")
		}
	}
	pumpCtx, stopPump := context.WithTimeout(context.Background(), 200*time.Millisecond)
	var pumpOutput bytes.Buffer
	pumpErr := Execute(pumpCtx, []string{"schedule-run", id, "2"}, root, &pumpOutput)
	stopPump()
	if !errors.Is(pumpErr, context.DeadlineExceeded) || pumpOutput.Len() != 0 {
		t.Fatal("pump inferred completion or ignored cancellation", pumpErr)
	}
	after, err := journal.ExportJSONL(controllerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("scheduler redispatched completed planner", err)
	}
	definition.Tasks[0].ControllerPath = filepath.Join(t.TempDir(), runState.RunID+".jsonl")
	body, err = json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, body, 0600); err != nil {
		t.Fatal(err)
	}
	var denied bytes.Buffer
	if err := Execute(context.Background(), []string{"schedule-create", input}, root, &denied); err == nil || denied.Len() != 0 {
		t.Fatal("schedule accepted substituted controller path")
	}
}
