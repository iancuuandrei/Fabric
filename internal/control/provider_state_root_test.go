package control

import (
	"path"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestProviderOpenCodeStateRootBindingIsCompactDurableAndDistinct(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	runID := strings.Repeat("a", 64)
	invocationID := strings.Repeat("b", 64)
	segment, err := bindProviderOpenCodeStateRoot(runtimePath, false, runID, "explorer", invocationID)
	if err != nil || len(segment) != 64 || filepath.Base(segment) != segment {
		t.Fatal("fresh state root is not one full digest segment", segment, err)
	}
	again, err := bindProviderOpenCodeStateRoot(runtimePath, true, runID, "explorer", invocationID)
	if err != nil || again != segment {
		t.Fatal("partial compact runtime did not recover its durable namespace", again, err)
	}
	other, err := bindProviderOpenCodeStateRoot(filepath.Join(t.TempDir(), "runtime.jsonl"), false, runID, "explorer", strings.Repeat("c", 64))
	if err != nil || other == segment {
		t.Fatal("distinct invocation reused compact state root", other, err)
	}
	otherRun, err := bindProviderOpenCodeStateRoot(filepath.Join(t.TempDir(), "runtime.jsonl"), false, strings.Repeat("d", 64), "explorer", invocationID)
	if err != nil || otherRun == segment {
		t.Fatal("distinct run reused compact state root", otherRun, err)
	}
}

func TestProviderOpenCodeStateRootPreservesOnlyUnboundHistoricalRuntime(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	runID := strings.Repeat("a", 64)
	invocationID := strings.Repeat("b", 64)
	legacy, err := bindProviderOpenCodeStateRoot(runtimePath, true, runID, "explorer", invocationID)
	want := path.Join(runID, "explorer-"+invocationID)
	if err != nil || legacy != want {
		t.Fatal("historical runtime state root changed", legacy, want, err)
	}
	events, err := journal.Read(runtimePath + ".state-root.jsonl")
	if err != nil || len(events) != 0 {
		t.Fatal("historical runtime was backfilled ambiguously", events, err)
	}
}

func TestProviderOpenCodeStateRootRejectsSubstitution(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	if _, err := bindProviderOpenCodeStateRoot(runtimePath, false, strings.Repeat("a", 64), "explorer", strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := bindProviderOpenCodeStateRoot(runtimePath, false, strings.Repeat("a", 64), "reviewer", strings.Repeat("b", 64)); err == nil {
		t.Fatal("substituted role reused state root binding")
	}
}
