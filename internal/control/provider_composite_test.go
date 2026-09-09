package control

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/opencoderuntime"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/toolbridge"
)

func TestProviderCompositeRequiresScheduleAndRecoveryCannotCallTools(t *testing.T) {
	testProviderCompositeRecoveryQueue(t, compositeToolQueueLimit)
}

func TestProviderCompositeRecoveryPreservesLegacyZeroQueue(t *testing.T) {
	testProviderCompositeRecoveryQueue(t, 0)
}

func testProviderCompositeRecoveryQueue(t *testing.T, recordedQueue int) {
	t.Helper()
	f := newAgentToolFixture(t)
	snapshot, err := Inspect(f.controllerPath)
	if err != nil {
		t.Fatal(err)
	}
	invocation := snapshot.AgentDispatch[f.binding.InvocationID].Admission.Invocation
	scheduled, err := taskscheduler.Inspect(f.schedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := scheduled.Tasks[f.binding.AgentTurn.TurnID].Claim
	if claim == nil {
		t.Fatal("fixture claim missing")
	}
	ctx, err := (ScheduledDispatchAdapter{JournalPath: f.schedulerPath}).bindJournalContext(context.Background(), *claim)
	if err != nil {
		t.Fatal(err)
	}
	ctx = withScheduledAgentTurn(ctx, claim.AgentTurn)
	binding, err := contextbroker.NewBinding(invocation.ID, snapshot.Creation.Repository, nil, contextbroker.Limits{MaxCalls: 4, MaxRequestBytes: 4096, MaxResponseBytes: contextmcp.MaxContentBytes, MaxTotalResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := contextbroker.Open(filepath.Join(t.TempDir(), "broker"), binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	runtimePath := filepath.Join(t.TempDir(), "runtime")
	pathless, _, _, err := prepareProviderComposite(context.Background(), f.controllerPath, runtimePath, invocation, broker, binding, nil)
	if err != nil || pathless != nil {
		t.Fatal("pathless runtime gained tools", err)
	}
	config, receipts, verify, err := prepareProviderComposite(ctx, f.controllerPath, runtimePath, invocation, broker, binding, nil)
	if err != nil || config == nil || receipts == nil || verify == nil {
		t.Fatal("exact caller rejected", err)
	}
	if config.MaxQueuedCalls != compositeToolQueueLimit || receipts.MaxQueuedCalls != compositeToolQueueLimit || receipts.QueuePolicy != "serial-fifo-v1" || receipts.Version != 2 {
		t.Fatal("fresh composite queue is not durably bound")
	}
	if recordedQueue == 0 {
		legacy, err := contextmcp.PrepareRecorderBinding(broker, invocation.ID, config.CallerBindingSHA256, config.AgentProjection)
		if err != nil {
			t.Fatal(err)
		}
		receipts = &legacy
	}
	names := make([]string, len(receipts.Tools))
	for i, tool := range receipts.Tools {
		names[i] = tool.Tool
	}
	sort.Strings(names)
	root := snapshot.Creation.Repository.Root
	intent := opencoderuntime.Intent{Version: 3, Invocation: invocation, Directory: root, Project: opencode.ProjectExpectation{Directory: root, Mode: opencode.ProjectModeGit, Worktree: root}, Context: binding, Session: opencode.ToolSessionBinding{ToolNames: []string{}}, SessionPlan: &opencoderuntime.SessionPlan{Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort, ToolNames: names, CatalogSHA256: receipts.CatalogSHA256}, ToolReceipts: receipts}
	if err := opencoderuntime.RecordIntent(runtimePath, intent); err != nil {
		t.Fatal(err)
	}
	events, err := journal.Read(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareProviderComposite(context.Background(), f.controllerPath, runtimePath, invocation, broker, binding, events); err == nil {
		t.Fatal("composite recovery lost schedule authority")
	}
	recovered, recoveredBinding, _, err := prepareProviderComposite(ctx, f.controllerPath, runtimePath, invocation, broker, binding, events)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.MaxQueuedCalls != recordedQueue || recoveredBinding.BindingID != receipts.BindingID {
		t.Fatal("recovery changed recorded queue policy")
	}
	if _, err := recovered.AgentProjection.Call(ctx, toolbridge.Call{RequestID: []byte(`1`), Tool: "list_agents", Arguments: []byte(`{}`)}); !errors.Is(err, opencoderuntime.ErrRecoveryRequired) {
		t.Fatal("recovery callback acquired execution authority", err)
	}
	transportEvents, err := journal.Read(config.Path)
	if err != nil || len(transportEvents) != 0 {
		t.Fatal("preparation or recovery created transport effects", err)
	}
}
