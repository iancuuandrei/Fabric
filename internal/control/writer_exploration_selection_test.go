package control

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/worktree"
)

func TestExplorationContextSelectionBindsOmittedOrderAndContent(t *testing.T) {
	records := make([]ExplorerRecord, 24)
	for index := range records {
		records[index].Question = strings.Repeat("x", index+1)
	}
	selected, omitted, hash, err := selectExplorationRecords(records, 16)
	if err != nil || omitted != 8 || len(selected) != 16 || selected[0].Question != records[8].Question || selected[15].Question != records[23].Question || len(hash) != 64 {
		t.Fatal(selected, omitted, hash, err)
	}
	changed := append([]ExplorerRecord(nil), records...)
	changed[0].Question += "changed omitted evidence"
	_, _, changedHash, err := selectExplorationRecords(changed, 16)
	if err != nil || changedHash == hash {
		t.Fatal("omitted content not bound", err)
	}
	changed = append([]ExplorerRecord(nil), records...)
	changed[0], changed[1] = changed[1], changed[0]
	_, _, reorderedHash, err := selectExplorationRecords(changed, 16)
	if err != nil || reorderedHash == hash {
		t.Fatal("omitted order not bound", err)
	}
	all, omitted, hash, err := selectExplorationRecords(records[:16], 16)
	if err != nil || len(all) != 16 || omitted != 0 || hash != "" {
		t.Fatal("legacy selection changed", err)
	}
	before, err := canonical.Bytes(struct {
		Instruction string            `json:"instruction"`
		Records     []explorerExcerpt `json:"records"`
	}{"legacy", []explorerExcerpt{}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := canonical.Bytes(explorationContext{Instruction: "legacy", Records: []explorerExcerpt{}})
	if err != nil || string(before) != string(after) {
		t.Fatal("legacy context shape changed", err)
	}
}

func TestWriterProjectionUsesConfiguredContextWindow(t *testing.T) {
	candidate := &worktree.Candidate{Version: 1, WorktreeID: strings.Repeat("a", 64), Head: strings.Repeat("b", 40), IndexHash: strings.Repeat("c", 64), FilesHash: strings.Repeat("d", 64)}
	id, err := candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	state := Snapshot{Candidate: candidate, Explorations: make([]ExplorerRecord, 24)}
	state.Creation.Config.Exploration = &config.ExplorationPolicy{Version: 1, MaxRecords: 32, MaxContextRecords: 3}
	body, err := canonical.Bytes(Exploration{CandidateID: id, Summary: "advisory fixture", Paths: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	for index := range state.Explorations {
		state.Explorations[index].Question = strings.Repeat("q", index+1)
		state.Explorations[index].Result.Output = string(body)
	}
	projection, err := writerExplorationContext(state)
	if err != nil || len(projection.Records) != 3 || projection.OmittedRecords != 21 || projection.Records[0].Question != state.Explorations[21].Question {
		t.Fatal(projection, err)
	}
	if !projection.Records[0].Current || len(projection.OmittedEvidenceHash) != 64 {
		t.Fatal("projection lost evidence binding")
	}
	state.Creation.Config.Exploration = nil
	legacy, err := writerExplorationContext(state)
	if err != nil || len(legacy.Records) != 16 || legacy.OmittedRecords != 8 {
		t.Fatal("legacy default changed", legacy, err)
	}
}
