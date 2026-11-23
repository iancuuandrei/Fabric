package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func sourceSpectrum(run, candidate string) faultlocalization.Spectrum {
	return faultlocalization.Spectrum{Version: 1, RunID: run, CandidateID: candidate,
		Sources: []faultlocalization.Source{{Path: "file.txt", ProfilePath: "fixture/file.txt", SHA256: *digestText("base\n")}},
		Tests:   []faultlocalization.Test{{ID: "failing", Outcome: "FAIL", Profile: "mode: set\nfixture/file.txt:1.1,1.5 1 1\n"}, {ID: "passing", Outcome: "PASS", Profile: "mode: set\nfixture/file.txt:1.1,1.5 1 0\n"}}}
}

func TestRepairSpectrumLeasedSourceBindingDriftAndNoJournalMutation(t *testing.T) {
	c := creation(t)
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _ := s.Candidate.ID()
	bundle := sourceSpectrum(s.RunID, candidate)
	before, _ := Inspect(path)
	base, _ := DiagnoseRepair(s)
	r, err := DiagnoseRepairSpectrum(context.Background(), path, s, bundle)
	if err != nil || r.Spectrum == nil || r.Spectrum.SourceStatus != "complete_candidate_bytes_observed" || len(r.Spectrum.Blocks) != 1 {
		t.Fatal("candidate bytes not observed", r, err)
	}
	projected := r
	projected.Spectrum = nil
	a, _ := canonical.Bytes(base)
	b, _ := canonical.Bytes(projected)
	if string(a) != string(b) {
		t.Fatal("ranking changed original findings or specifications")
	}
	after, _ := Inspect(path)
	if after.ControllerHead != before.ControllerHead {
		t.Fatal("read-only ranking appended a journal event")
	}
	for _, mutate := range []func(*faultlocalization.Spectrum){
		func(b *faultlocalization.Spectrum) { b.RunID = strings.Repeat("1", 64) },
		func(b *faultlocalization.Spectrum) { b.CandidateID = strings.Repeat("1", 64) },
	} {
		bad := sourceSpectrum(s.RunID, candidate)
		mutate(&bad)
		if _, err := DiagnoseRepairSpectrum(context.Background(), path, s, bad); err == nil {
			t.Fatal("foreign scope accepted")
		}
	}
	wrong := sourceSpectrum(s.RunID, candidate)
	wrong.Sources[0].SHA256 = strings.Repeat("1", 64)
	r, err = DiagnoseRepairSpectrum(context.Background(), path, s, wrong)
	if err != nil || r.Spectrum.SourceStatus != "unavailable" || len(r.Spectrum.Blocks) != 0 {
		t.Fatal("hash mismatch published rank", r, err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "file.txt"), []byte("unadmitted drift\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = DiagnoseRepairSpectrum(context.Background(), path, s, bundle)
	if err != nil || r.Spectrum.SourceStatus != "unavailable" || len(r.Spectrum.Blocks) != 0 {
		t.Fatal("candidate drift published rank", r, err)
	}
}

func TestRepairSpectrumSourceCoverageAndRangeValidation(t *testing.T) {
	s := sourceSpectrum(strings.Repeat("a", 64), strings.Repeat("b", 64))
	r, _ := faultlocalization.Analyze(s)
	f := anchorSource(s.CandidateID, "base\n")
	f.Path = "file.txt"
	if err := validateSpectrumSources(s, r, []worktree.SourceFile{f}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*worktree.SourceFile){
		"candidate": func(f *worktree.SourceFile) { f.CandidateID = "foreign" },
		"prefix":    func(f *worktree.SourceFile) { next := f.Size; f.NextOffset = &next },
		"size":      func(f *worktree.SourceFile) { f.Size++ },
		"path":      func(f *worktree.SourceFile) { f.Path = "other" },
		"content":   func(f *worktree.SourceFile) { f.Content = []byte("evil\n") },
		"utf8":      func(f *worktree.SourceFile) { f.Content = []byte{255}; f.Size = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := f
			mutate(&bad)
			if err := validateSpectrumSources(s, r, []worktree.SourceFile{bad}); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	r.Blocks[0].EndColumn = 100
	if err := validateSpectrumSources(s, r, []worktree.SourceFile{f}); err == nil {
		t.Fatal("range outside source accepted")
	}
}
