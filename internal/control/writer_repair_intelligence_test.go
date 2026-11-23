package control

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func recordedRepairFixture(t *testing.T) (Snapshot, *writerImplementationContext, *TaskContextRecord) {
	t.Helper()
	s := diagnosisFixture(t)
	s.Creation.Execution.RepairPlanningVersion = 1
	s.Creation.Execution.RepairIntelligenceVersion = 1
	s.Creation.Execution.Context = taskContextBoundedV1
	s.Verification.Observations[0].Result.Stderr.Excerpt = "src/bool.go:2:5: compile error"
	id, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	file := anchorSource(id, "package example\nvar count = 1\n")
	context := &TaskContextRecord{SourceID: "source", CandidateID: id, Manifest: taskcontext.Manifest{Selected: []taskcontext.SelectedFile{{Path: "src/bool.go", Hash: file.SHA256, Start: 0, End: file.Size, Content: string(file.Content)}}}}
	task := &writerImplementationContext{ID: "impl-repair-1"}
	return s, task, context
}

func TestWriterRepairIntelligenceOversizedProjectionRetainsOmissionIdentity(t *testing.T) {
	s, task, context := recordedRepairFixture(t)
	first := s.Verification.Observations[0]
	s.Verification.Observations = nil
	for i := 0; i < 128; i++ {
		observation := first
		observation.Start.Index = i
		observation.Result.Stderr = verification.Stream{Excerpt: "src/bool.go:2:5: compile error"}
		s.Verification.Observations = append(s.Verification.Observations, observation)
	}
	r, err := writerRepairIntelligence(s, task, context)
	if err != nil || r == nil || r.AnchoringStatus != "projection_budget_omitted" || r.OmittedFindings != 128 || len(r.ProjectionHash) != 64 || len(r.Findings) != 0 || len(r.Specifications) != 0 {
		t.Fatalf("oversized optional projection did not degrade explicitly: %+v %v", r, err)
	}
	raw, err := canonical.Bytes(r)
	if err != nil || len(raw) > 32<<10 {
		t.Fatal("omission record exceeds its bound", err)
	}
	firstHash := r.ProjectionHash
	s.Verification.Observations[127].Result.Error = "different retained evidence"
	r, err = writerRepairIntelligence(s, task, context)
	if err != nil || r.ProjectionHash == firstHash {
		t.Fatal("omitted evidence lost its identity", err)
	}
}

func TestWriterRepairIntelligenceBindsRecordedPreimageAndReplayStableCursor(t *testing.T) {
	s, task, context := recordedRepairFixture(t)
	s.ControllerHead = strings.Repeat("1", 64)
	r, err := writerRepairIntelligence(s, task, context)
	if err != nil || r == nil || len(r.Specifications) != 1 || r.Specifications[0].SuggestedStrategy != "localized_llm" || len(r.Anchors) != 1 || r.Anchors[0].Code != "" || r.ControllerHead != "" {
		t.Fatalf("recorded repair context lost: %+v %v", r, err)
	}
	anchorID, err := repairAnchorID(r.Anchors[0])
	if err != nil || anchorID != r.Anchors[0].ID {
		t.Fatal("compact anchor has inconsistent content identity", err)
	}
	first, err := canonical.Bytes(r)
	if err != nil {
		t.Fatal(err)
	}
	s.ControllerHead = strings.Repeat("2", 64)
	again, err := writerRepairIntelligence(s, task, context)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := canonical.Bytes(again)
	if string(first) != string(second) {
		t.Fatal("host journal appends changed replay-derived repair input")
	}
	s.Creation.Execution.RepairIntelligenceVersion = 0
	off, err := writerRepairIntelligence(s, task, context)
	if err != nil || off != nil {
		t.Fatal("legacy policy gained repair input", err)
	}
}

func TestWriterRepairIntelligenceIncompleteOrForeignContextCannotValidatePreimage(t *testing.T) {
	for _, change := range []func(*TaskContextRecord){
		func(r *TaskContextRecord) { r.CandidateID = "foreign" },
		func(r *TaskContextRecord) { r.Manifest.Selected[0].Start = 1 },
		func(r *TaskContextRecord) { r.Manifest.Selected[0].Content += "altered"; r.Manifest.Selected[0].End++ },
		func(r *TaskContextRecord) { r.Manifest.Selected[0].Hash = strings.Repeat("0", 64) },
	} {
		s, task, context := recordedRepairFixture(t)
		change(context)
		r, err := writerRepairIntelligence(s, task, context)
		if err != nil || r == nil || r.Specifications[0].SuggestedStrategy != "localization_required" {
			t.Fatal("incomplete/foreign evidence implied exact preimage", r, err)
		}
	}
}

func TestWriterRepairIntelligenceRelocatesOnlyAdmittedOldContext(t *testing.T) {
	s, task, context := recordedRepairFixture(t)
	old := strings.Repeat("9", 64)
	s.Verification.Observations[0].Result.CandidateID = old
	previous := *context
	previous.CandidateID = old
	s.TaskContexts = []TaskContextRecord{previous}
	current := anchorSource(context.CandidateID, "// header\npackage example\nvar count = 1\n")
	context.Manifest.Selected[0].Hash = current.SHA256
	context.Manifest.Selected[0].Content = string(current.Content)
	context.Manifest.Selected[0].End = current.Size
	// Keep the prior admission immutable rather than sharing selected-file storage.
	s.TaskContexts[0].Manifest.Selected = []taskcontext.SelectedFile{{Path: "src/bool.go", Hash: anchorSource(old, "package example\nvar count = 1\n").SHA256, End: 30, Content: "package example\nvar count = 1\n"}}
	s.TaskContexts[0].Manifest.Selected[0].End = int64(len(s.TaskContexts[0].Manifest.Selected[0].Content))
	r, err := writerRepairIntelligence(s, task, context)
	if err != nil || len(r.Anchors) != 1 || r.Anchors[0].Status != "code_reanchored" || r.Anchors[0].Line != 3 || r.Findings[0].CandidateBinding != "stale_candidate" || len(r.Specifications) != 1 {
		t.Fatalf("admitted context relocation failed: %+v %v", r, err)
	}
	s.TaskContexts[0].SourceID = "foreign-source"
	r, err = writerRepairIntelligence(s, task, context)
	if err != nil || r.Anchors[0].Status != "stale_unlocalized" || len(r.Specifications) != 0 {
		t.Fatal("foreign admission relocated finding", r, err)
	}
}

func TestRepairIntelligencePolicyIsExplicitAndRequiresExistingGraphContext(t *testing.T) {
	p := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 1, RepairPlanningVersion: 1, Context: taskContextBoundedV1, RepairIntelligenceVersion: 1}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ExecutionPolicy){func(p *ExecutionPolicy) { p.RepairIntelligenceVersion = 2 }, func(p *ExecutionPolicy) { p.Context = "" }, func(p *ExecutionPolicy) { p.RepairPlanningVersion = 0 }, func(p *ExecutionPolicy) { p.GraphVersion = 0 }} {
		bad := p
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("unsupported repair policy admitted", bad)
		}
	}
	p.RepairIntelligenceVersion = 0
	raw, err := canonical.Bytes(p)
	if err != nil || strings.Contains(string(raw), "repair_intelligence_version") {
		t.Fatal("absent policy changed legacy serialization", err)
	}
}
