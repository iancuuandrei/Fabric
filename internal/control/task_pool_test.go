package control

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/providerruntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
)

func TestTaskPoolExactAttemptRecoveryAndOneTimeSettlement(t *testing.T) {
	c := providerRoutingConfig("provider-api", "openai")
	c.TaskPool = &config.TaskPool{Version: 1, Path: filepath.Join(t.TempDir(), "shared-pool.jsonl"), Limits: taskpool.Limits{Total: 1}}
	runID := strings.Repeat("a", 64)
	invocation, err := runtime.NewInvocation(c.Planner, "bounded plan")
	if err != nil {
		t.Fatal(err)
	}
	_, expectation, err := ConfiguredProviderExpectation(c, "planner")
	if err != nil {
		t.Fatal(err)
	}
	direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
	inputHash, err := direct.InputHash(expectation)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := ResolveProviderRouting(c, runID, "planner", inputHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ensureTaskPool(c, runID, selected.Intent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureTaskPool(c, runID, selected.Intent)
	if err != nil || first != second {
		t.Fatal("exact active task-pool attempt was not recovered", err)
	}
	receiptHash := strings.Repeat("b", 64)
	if err := settleTaskPool(c, runID, selected.Intent, receiptHash); err != nil {
		t.Fatal(err)
	}
	if err := settleTaskPool(c, runID, selected.Intent, receiptHash); err != nil {
		t.Fatal("exact settlement was not idempotent", err)
	}
	if _, err := ensureTaskPool(c, runID, selected.Intent); err == nil {
		t.Fatal("settled attempt was reacquired")
	}
	state, err := taskpool.Inspect(c.TaskPool.Path)
	if err != nil || len(state.Active) != 0 || state.Settled[first.ID].ReceiptSHA256 != receiptHash {
		t.Fatal("unexpected shared pool accounting", state, err)
	}
}
