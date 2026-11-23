package control

import (
	"context"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func checkRepairSpectrumAdmission(t *testing.T, ctx context.Context, path string, s Snapshot) {
	t.Helper()
	// Existing file.txt is complete candidate evidence but deliberately outside
	// the repaired bool_ext.go write scope. The fixture does not claim real Go
	// test execution; actual per-test Go profiles are tested in faultlocalization.
	id, _ := s.Candidate.ID()
	file := anchorSource(id, "initial implementation\n")
	spectrum := faultlocalization.Spectrum{Version: 1, RunID: s.RunID, CandidateID: id,
		Sources: []faultlocalization.Source{{Path: "file.txt", ProfilePath: "fixture/file.txt", SHA256: file.SHA256}},
		Tests:   []faultlocalization.Test{{ID: "asserted-failure", Outcome: "FAIL", Profile: "mode: set\nfixture/file.txt:1.1,1.5 1 1\n"}}}
	before := s.ControllerHead
	foreign := spectrum
	foreign.CandidateID = strings.Repeat("0", 64)
	if _, err := AdmitRepairSpectrumContext(ctx, path, "", foreign); err == nil {
		t.Fatal("foreign candidate admitted")
	}
	unchanged, err := Inspect(path)
	if err != nil || unchanged.ControllerHead != before {
		t.Fatal("rejected import changed journal", err)
	}
	rec, err := AdmitRepairSpectrumContext(ctx, path, "", spectrum)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.RepairSpectrum == nil || rec.RepairSpectrum.TaskID != "impl-repair-1" || len(after.TaskContexts) != len(s.TaskContexts)+1 {
		t.Fatal("missing admitted spectrum")
	}
	// Reuse exact admission, reject changed content, never replace an input.
	if _, err := AdmitRepairSpectrumContext(ctx, path, "", spectrum); err != nil {
		t.Fatal(err)
	}
	changed := spectrum
	changed.Tests = append([]faultlocalization.Test(nil), spectrum.Tests...)
	changed.Tests[0].ID = "different-test"
	if _, err := AdmitRepairSpectrumContext(ctx, path, "", changed); err == nil {
		t.Fatal("frozen context replaced")
	}
	latest, err := Inspect(path)
	if err != nil || latest.ControllerHead != after.ControllerHead {
		t.Fatal("reuse/rejection appended an event", err)
	}
	visible := writerVisibleTaskContext(&rec)
	if visible.RepairSpectrum != nil || rec.RepairSpectrum == nil {
		t.Fatal("raw strip mutated durable admission")
	}
	// Replay validation is pure and rejects malformed bindings/preimages.
	for _, mutate := range []func(*TaskContextRecord){
		func(r *TaskContextRecord) { r.RepairSpectrum.TaskID = "foreign" },
		func(r *TaskContextRecord) { r.RepairSpectrum.GateID = "foreign" },
		func(r *TaskContextRecord) { r.RepairSpectrum.Spectrum.RunID = strings.Repeat("0", 64) },
		func(r *TaskContextRecord) { r.RepairSpectrum.Spectrum.Sources[0].SHA256 = strings.Repeat("0", 64) },
		func(r *TaskContextRecord) { r.Role = "reviewer" },
		func(r *TaskContextRecord) { r.Unavailable = "unavailable" },
		func(r *TaskContextRecord) { r.Manifest.Selected = nil },
	} {
		raw, _ := canonical.Bytes(rec)
		var bad TaskContextRecord
		if err := canonical.Decode(raw, &bad); err != nil {
			t.Fatal(err)
		}
		mutate(&bad)
		raw, _ = canonical.Bytes(bad)
		copy := s
		if err := replayTaskContext(&copy, journal.Event{Payload: raw}); err == nil {
			t.Fatal("invalid spectrum replayed")
		}
	}
}

func TestRepairSpectrumContextBounds(t *testing.T) {
	s := faultlocalization.Spectrum{Version: 1, RunID: strings.Repeat("a", 64), CandidateID: strings.Repeat("b", 64), Sources: []faultlocalization.Source{{Path: "a.go", ProfilePath: "fixture/a.go", SHA256: strings.Repeat("c", 64)}}}
	profile := "mode: count\nfixture/a.go:1.1,1.2 1 " + strings.Repeat(" ", 60000) + "1\n"
	for _, id := range []string{"a", "b", "c"} {
		s.Tests = append(s.Tests, faultlocalization.Test{ID: id, Outcome: "FAIL", Profile: profile})
	}
	if _, err := faultlocalization.Analyze(s); err != nil {
		t.Fatal("base spectrum should fit read-only bound", err)
	}
	if _, err := boundedContextSpectrum(s); err == nil {
		t.Fatal("oversized admitted spectrum accepted")
	}
}

func TestRepairSpectrumContextCompactScopedProjection(t *testing.T) {
	s, task, rec := recordedRepairFixture(t)
	s.RunID = strings.Repeat("a", 64)
	task.WritePaths = []string{"src/bool.go"}
	rec.Role = writerTaskRole(s)
	rec.Query = s.Creation.Objective
	rec.QueryHash = taskContextQueryHash(rec.Query)
	rec.QueryLen = len(rec.Query)
	other := rec.Manifest.Selected[0]
	other.Path = "src/other.go"
	rec.Manifest.Selected = append(rec.Manifest.Selected, other)
	node, _ := s.Graph.Graph.Task(task.ID)
	profile := "mode: set\nfixture/bool.go:2.1,2.5 1 1\nfixture/other.go:2.1,2.5 1 1\n"
	rec.RepairSpectrum = &RepairSpectrumEvidence{TaskID: task.ID, GateID: node.ParentID, Spectrum: faultlocalization.Spectrum{
		Version: 1, RunID: s.RunID, CandidateID: rec.CandidateID,
		Sources: []faultlocalization.Source{{Path: "src/bool.go", ProfilePath: "fixture/bool.go", SHA256: other.Hash}, {Path: "src/other.go", ProfilePath: "fixture/other.go", SHA256: other.Hash}},
		Tests:   []faultlocalization.Test{{ID: "asserted-failure", Outcome: "FAIL", Profile: profile}},
	}}
	r, err := writerRepairIntelligence(s, task, rec)
	if err != nil || r.Spectrum == nil || len(r.Spectrum.Blocks) != 1 || r.Spectrum.Blocks[0].Path != "src/bool.go" || r.Spectrum.OmittedBlocks != 1 || r.Spectrum.Provenance != "caller_supplied_untrusted_test_spectra" {
		t.Fatal("scoped ranking missing", r, err)
	}
	raw, _ := canonical.Bytes(r)
	if strings.Contains(string(raw), "mode: set") {
		t.Fatal("raw profile leaked")
	}
	for _, taskBound := range []bool{false, true} {
		invocationTaskID := ""
		if taskBound {
			invocationTaskID = task.ID
		}
		rec.RepairSpectrum.InvocationTaskID = invocationTaskID
		question, err := writerTaskContextQuestion(s, invocationTaskID)
		if err != nil {
			t.Fatal(err)
		}
		rec.Query = question
		rec.QueryHash = taskContextQueryHash(question)
		rec.QueryLen = len(question)
		if _, err := recordedRepairSpectrum(s, rec); err != nil {
			t.Fatal("valid writer route rejected", taskBound, err)
		}
	}
	frozen := *rec
	frozen.RepairSpectrum = nil
	if _, err := reuseRepairSpectrumContext(frozen, rec.RepairSpectrum); err == nil {
		t.Fatal("ordinary frozen context retrofitted")
	}
	if _, err := reuseRepairSpectrumContext(*rec, nil); err != nil {
		t.Fatal("generic writer cannot reuse admitted spectrum", err)
	}
	full := anchorSource(rec.CandidateID, strings.Repeat("x", 9<<10))
	large := *rec
	large.Manifest.Selected = []taskcontext.SelectedFile{{Path: "src/bool.go", Hash: full.SHA256, Start: 0, End: 8 << 10, Content: string(full.Content[:8<<10])}}
	large.RepairSpectrum = &RepairSpectrumEvidence{TaskID: task.ID, GateID: node.ParentID, Spectrum: faultlocalization.Spectrum{
		Version: 1, RunID: s.RunID, CandidateID: rec.CandidateID,
		Sources: []faultlocalization.Source{{Path: "src/bool.go", ProfilePath: "fixture/bool.go", SHA256: full.SHA256}},
		Tests:   []faultlocalization.Test{{ID: "asserted-failure", Outcome: "FAIL", Profile: "mode: set\nfixture/bool.go:1.1,1.5 1 1\n"}},
	}}
	large.Query = s.Creation.Objective
	large.QueryHash = taskContextQueryHash(large.Query)
	large.QueryLen = len(large.Query)
	if _, err := recordedRepairSpectrum(s, &large); err == nil {
		t.Fatal("8 KiB prefix authenticated a larger source")
	}
	// Same retained prefix is insufficient when the complete source hash differs.
	rec.Manifest.Selected = []taskcontext.SelectedFile{rec.Manifest.Selected[0]}
	if _, err := recordedRepairSpectrum(s, rec); err == nil {
		t.Fatal("missing complete source accepted")
	}
}
