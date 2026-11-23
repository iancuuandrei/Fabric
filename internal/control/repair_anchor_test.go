package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func anchorSource(candidate, text string) worktree.SourceFile {
	digest := sha256.Sum256([]byte(text))
	return worktree.SourceFile{CandidateID: candidate, Path: "file.go", SHA256: hex.EncodeToString(digest[:]), Content: []byte(text), Size: int64(len(text))}
}

func TestRepairAnchorCurrentRangeAndUniqueRelocation(t *testing.T) {
	oldID, currentID := strings.Repeat("1", 64), strings.Repeat("2", 64)
	f := RepairFinding{ID: "finding", CandidateID: oldID, Path: "file.go", Line: 2, Column: 5}
	original := resolveRepairAnchor(f, oldID, anchorSource(oldID, "package example\nvar count = 1\n"), nil)
	if original.Status != "range_verified" || original.Code != "var count = 1" || original.FileHash == "" {
		t.Fatalf("range not anchored: %+v", original)
	}
	original.ID, _ = repairAnchorID(original)
	hints := repairAnchorHints([]RepairAnchor{original})
	current := anchorSource(currentID, "// added header\npackage example\nvar count = 1\n")
	relocated := resolveRepairAnchor(f, currentID, current, hints[f.ID])
	if relocated.Status != "code_reanchored" || relocated.Line != 3 || relocated.Column != 5 || relocated.Provenance != "provided_previous_report" || relocated.CandidateID != currentID || relocated.SourceCandidateID != oldID {
		t.Fatalf("relocation lost identity: %+v", relocated)
	}
	if f.Line != 2 || f.CandidateID != oldID {
		t.Fatal("original finding rewritten")
	}
	relocated.ID, _ = repairAnchorID(relocated)
	thirdID := strings.Repeat("3", 64)
	third := resolveRepairAnchor(f, thirdID, anchorSource(thirdID, "// second header\n// added header\npackage example\nvar count = 1\n"), repairAnchorHints([]RepairAnchor{relocated})[f.ID])
	if third.Status != "code_reanchored" || third.Line != 4 || third.SourceCandidateID != oldID || third.CandidateID != thirdID {
		t.Fatal("prior-report chain lost original finding binding", third)
	}
}

func TestRepairAnchorRelocationRejectsMissingAmbiguousAndPartialEvidence(t *testing.T) {
	oldID, newID := strings.Repeat("1", 64), strings.Repeat("2", 64)
	f := RepairFinding{ID: "finding", CandidateID: oldID, Path: "file.go", Line: 1, Column: 1}
	hint := resolveRepairAnchor(f, oldID, anchorSource(oldID, "var count = 1\n"), nil)
	for _, test := range []struct{ text, status string }{
		{"var count = 2\n", "anchor_missing"},
		{"var count = 1\nvar count = 1\n", "ambiguous"},
		{"// var count = 1\n", "anchor_missing"},
		{"var count = 1 + 1\n", "anchor_missing"},
	} {
		a := resolveRepairAnchor(f, newID, anchorSource(newID, test.text), &hint)
		if a.Status != test.status || a.Code != "" {
			t.Fatalf("guessed %q: %+v", test.text, a)
		}
	}
	partial := anchorSource(newID, "var count = 1\n")
	next := partial.Size
	partial.NextOffset = &next
	a := resolveRepairAnchor(f, newID, partial, &hint)
	if a.Status != "coverage_incomplete" {
		t.Fatal("prefix implied unique full-file match", a)
	}
	hint.Path = "different.go"
	a = resolveRepairAnchor(f, newID, anchorSource(newID, "var count = 1\n"), &hint)
	if a.Status != "stale_unlocalized" {
		t.Fatal("foreign path hint accepted", a)
	}
}

func TestRepairAnchorHintsRejectTamperingAndDuplicateIdentity(t *testing.T) {
	old := strings.Repeat("1", 64)
	f := RepairFinding{ID: "finding", CandidateID: old, Path: "file.go", Line: 1}
	a := resolveRepairAnchor(f, old, anchorSource(old, "var count = 1\n"), nil)
	a.ID, _ = repairAnchorID(a)
	if len(repairAnchorHints([]RepairAnchor{a})) != 1 {
		t.Fatal("valid hint dropped")
	}
	changed := a
	changed.Code = "var count = 2"
	if len(repairAnchorHints([]RepairAnchor{changed})) != 0 {
		t.Fatal("modified code retained old anchor identity")
	}
	if len(repairAnchorHints([]RepairAnchor{a, a, a})) != 0 {
		t.Fatal("ambiguous prior hints selected arbitrarily")
	}
}

func TestRepairAnchorRangeBoundsAndOptionalCoverage(t *testing.T) {
	id := strings.Repeat("1", 64)
	f := RepairFinding{ID: "finding", CandidateID: id, Path: "file.go", Line: 1}
	for _, text := range []string{"", strings.Repeat("x", 4097), string([]byte{0xff})} {
		a := resolveRepairAnchor(f, id, anchorSource(id, text), nil)
		if a.Status != "range_unavailable" || a.Code != "" {
			t.Fatalf("invalid bounded range anchored: %+v", a)
		}
	}
	file := anchorSource(id, "var count")
	next := file.Size
	file.NextOffset = &next
	a := resolveRepairAnchor(f, id, file, nil)
	if a.Status != "range_unavailable" {
		t.Fatal("incomplete last line accepted", a)
	}
	f.Column = 100
	a = resolveRepairAnchor(f, id, anchorSource(id, "var count = 1\n"), nil)
	if a.Status != "range_unavailable" {
		t.Fatal("column beyond line accepted", a)
	}
}

func TestRepairAnchorRequiresExactReadyOwnershipForLocalizedStrategy(t *testing.T) {
	r := RepairDiagnosis{CandidateID: "candidate", Specifications: []RepairSpecification{{FindingIDs: []string{"finding"}, AllowedWritePaths: []string{"src/file.go"}, SuggestedStrategy: "localization_required"}}, Anchors: []RepairAnchor{{ID: "anchor", FindingID: "finding", CandidateID: "candidate", Path: "src/file.go", Status: "range_verified"}}}
	r.attachRepairAnchors()
	if r.Specifications[0].SuggestedStrategy != "localized_llm" || len(r.Specifications[0].AnchorIDs) != 1 {
		t.Fatal("verified scope not reflected", r)
	}
	r.Specifications[0].SuggestedStrategy = "localization_required"
	r.Specifications[0].AnchorIDs = nil
	r.Anchors[0].Path = "src/file.go.extra"
	r.attachRepairAnchors()
	if r.Specifications[0].SuggestedStrategy != "localization_required" || len(r.Specifications[0].AnchorIDs) != 0 {
		t.Fatal("similar path expanded ownership", r)
	}
}

func TestRepairAnchoredDiagnosisRetainsReportOnActualCandidateDrift(t *testing.T) {
	c := creation(t)
	c.Config.Verification = []config.Check{{Name: "failure", Argv: []string{"git", "--engorch-invalid-option"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	s, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("unadmitted drift\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := DiagnoseRepairAnchored(context.Background(), path, s, nil)
	if err != nil || r.AnchoringStatus != "unavailable" || len(r.Findings) != 1 || len(r.Anchors) != 0 {
		t.Fatalf("optional drift erased useful evidence: %+v %v", r, err)
	}
}

func TestRepairAnchorCloseOwnershipFailureWithholdsObservedEvidence(t *testing.T) {
	c := creation(t)
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worktree.LeaseGuardPath(s.Workspace.Request), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	closeErr := lease.Close()
	if closeErr == nil {
		t.Fatal("fixture did not cause real guard ownership failure")
	}
	r, err := DiagnoseRepair(s)
	if err != nil {
		t.Fatal(err)
	}
	r.AnchoringStatus = "unavailable"
	proposed := []RepairAnchor{{ID: "not-admitted", Status: "range_verified"}}
	result, err := finishRepairAnchoring(s, r, proposed, closeErr)
	if err != nil || result.AnchoringStatus != "unavailable" || len(result.Anchors) != 0 {
		t.Fatal("ownership failure published verified source", result, err)
	}
	unavailable := faultlocalization.Report{SourceStatus: "unavailable", Blocks: []faultlocalization.Block{}}
	r.Spectrum = &unavailable
	rank := faultlocalization.Report{Blocks: []faultlocalization.Block{{Path: "file.txt"}}}
	if got := finishRepairSpectrum(r, rank, closeErr); got.Spectrum.SourceStatus != "unavailable" || len(got.Spectrum.Blocks) != 0 {
		t.Fatal("guard failure published spectrum ranking")
	}
}
