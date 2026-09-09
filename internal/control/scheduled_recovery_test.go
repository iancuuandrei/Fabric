package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

func TestScheduledReconcileRefusesMissingRuntimeHistory(t *testing.T) {
	controllerPath, snapshot, invocation := agentDispatchFixture(t)
	binding, err := beginAgentDispatch(controllerPath, snapshot, invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := markAgentDispatchRunning(&binding); err != nil {
		t.Fatal(err)
	}
	if err := finishAgentDispatchUnknown(binding); err != nil {
		t.Fatal(err)
	}
	task, err := PrepareScheduledTask(controllerPath, taskscheduler.OperationPlanner, "")
	if err != nil {
		t.Fatal(err)
	}
	task.ID = strings.Repeat("b", 64)
	before, err := controllerJournalHead(controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: strings.Repeat("c", 64), Task: task, Generation: 1, ControllerHead: strings.Repeat("d", 64)}
	if _, err := ReconcileScheduledClaim(context.Background(), controllerPath, claim); err == nil {
		t.Fatal("UNKNOWN claim without runtime history was restarted")
	}
	after, err := controllerJournalHead(controllerPath)
	if err != nil || after != before {
		t.Fatal("rejected reconciliation changed controller history", before, after, err)
	}
}

func TestScheduledReconcileRejectsNonterminalCodexRuntime(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "codex-runtime.jsonl")
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "low", Role: "planner"}, "objective")
	if err != nil {
		t.Fatal(err)
	}
	intent := codexruntime.Intent{Invocation: invocation, Directory: filepath.Join(t.TempDir(), "workspace")}
	if _, err := journal.Append(runtimePath, "runtime.intent", intent, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := journal.Read(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireScheduledOfflineRuntime("codex-app-server", runtimePath); err == nil {
		t.Fatal("nonterminal Codex runtime was admitted for provider resume")
	}
	after, err := journal.Read(runtimePath)
	if err != nil || len(after) != len(before) || after[len(after)-1].Hash != before[len(before)-1].Hash {
		t.Fatal("rejected Codex reconciliation changed runtime history", err)
	}
}
