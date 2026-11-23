package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	agentruntime "github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestOutputStreamsLargeSnapshotWithoutDroppingBindings(t *testing.T) {
	snapshot := control.Snapshot{
		RunID:       strings.Repeat("a", 64),
		State:       "REPAIRING",
		PlanID:      strings.Repeat("b", 64),
		FileOutcome: "UNKNOWN",
		Creation: control.Creation{
			Version:   1,
			Nonce:     "bound-nonce",
			Objective: "bound objective",
			Repository: repository.Identity{
				Version:      1,
				Name:         "fixture",
				Root:         t.TempDir(),
				CommonDir:    t.TempDir(),
				ObjectFormat: "sha1",
				Commit:       strings.Repeat("c", 40),
				Tree:         strings.Repeat("d", 40),
			},
		},
		Candidate: &worktree.Candidate{
			Version:    1,
			WorktreeID: strings.Repeat("e", 64),
			Head:       strings.Repeat("f", 40),
			IndexHash:  strings.Repeat("1", 64),
			FilesHash:  strings.Repeat("2", 64),
			FileCount:  3,
		},
	}
	for i := 0; i < 96; i++ {
		record := control.ExplorerRecord{
			Question: strings.Repeat("q", 4096),
			Invocation: agentruntime.Invocation{
				Version: 1,
				ID:      fmt.Sprintf("invocation-%03d", i),
				Profile: agentruntime.Profile{Runtime: "fixture", Provider: "fixture", Model: "fixture", Effort: "low", Role: "explorer"},
				Input:   strings.Repeat("i", 4096),
			},
			Result: agentruntime.Result{
				Version:      1,
				InvocationID: fmt.Sprintf("invocation-%03d", i),
				Requested:    agentruntime.Profile{Runtime: "fixture", Provider: "fixture", Model: "fixture", Effort: "low", Role: "explorer"},
				Output:       strings.Repeat("o", 8192),
			},
		}
		encoded, err := canonical.Bytes(record)
		if err != nil {
			t.Fatalf("record %d canonical encoding: %v", i, err)
		}
		if len(encoded) >= canonical.MaxBytes {
			t.Fatalf("record %d is not individually bounded: %d bytes", i, len(encoded))
		}
		snapshot.Explorations = append(snapshot.Explorations, record)
	}
	if encoded, err := json.Marshal(snapshot); err != nil {
		t.Fatal(err)
	} else if len(encoded) <= canonical.MaxBytes {
		t.Fatalf("fixture snapshot did not exceed aggregate bound: %d bytes", len(encoded))
	}

	var out bytes.Buffer
	if err := output(&out, snapshot); err != nil {
		t.Fatalf("large snapshot output: %v", err)
	}
	if out.Len() <= canonical.MaxBytes || !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		t.Fatalf("snapshot output size or framing mismatch: %d bytes", out.Len())
	}
	var got control.Snapshot
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("snapshot output did not round-trip: %v", err)
	}
	if got.RunID != snapshot.RunID || got.State != snapshot.State || got.PlanID != snapshot.PlanID || got.FileOutcome != "UNKNOWN" {
		t.Fatalf("snapshot identity/state binding changed: run=%q state=%q plan=%q file_outcome=%q", got.RunID, got.State, got.PlanID, got.FileOutcome)
	}
	if got.Creation.Nonce != snapshot.Creation.Nonce || got.Creation.Objective != snapshot.Creation.Objective || got.Creation.Repository != snapshot.Creation.Repository || got.Candidate == nil || *got.Candidate != *snapshot.Candidate {
		t.Fatal("snapshot creation or candidate binding changed")
	}
	if got.Plan != nil || got.WorkspaceIntent != nil || got.FileIntent != nil || got.RIProducer != nil {
		t.Fatal("explicit null snapshot fields did not round-trip as nil")
	}
	if got.Explorations == nil || len(got.Explorations) != len(snapshot.Explorations) || got.Explorations[95].Question != snapshot.Explorations[95].Question || got.Explorations[95].Result.Output != snapshot.Explorations[95].Result.Output {
		t.Fatal("snapshot evidence records were dropped or changed")
	}
}

type failingOutputWriter struct{ err error }

func (w failingOutputWriter) Write([]byte) (int, error) { return 0, w.err }

func TestOutputSnapshotPropagatesWriterError(t *testing.T) {
	want := errors.New("fixture writer failure")
	if err := output(failingOutputWriter{err: want}, control.Snapshot{RunID: "fixture"}); !errors.Is(err, want) {
		t.Fatalf("writer error was not propagated: %v", err)
	}
}

func TestOutputOtherCanonicalValuesRemainBounded(t *testing.T) {
	var out bytes.Buffer
	err := output(&out, strings.Repeat("x", canonical.MaxBytes))
	if err == nil || !strings.Contains(err.Error(), "JSON size or UTF-8 invalid") {
		t.Fatalf("oversized non-snapshot output bypassed canonical bound: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("rejected canonical output wrote %d bytes", out.Len())
	}
}
