package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/faultlocalization"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// DiagnoseRepairSpectrum adds an advisory imported Ochiai ranking only after
// complete candidate source hashes/ranges are observed under the existing read
// lease. It preserves findings/specifications and performs no journal writes,
// test execution or provider effects. Test labels/mappings remain untrusted.
func DiagnoseRepairSpectrum(ctx context.Context, path string, s Snapshot, spectrum faultlocalization.Spectrum) (RepairDiagnosis, error) {
	r, err := DiagnoseRepair(s)
	if err != nil {
		return r, err
	}
	ranking, err := faultlocalization.Analyze(spectrum)
	if err != nil {
		return r, err
	}
	if spectrum.RunID != s.RunID || spectrum.CandidateID != r.CandidateID {
		return r, errors.New("spectrum run/candidate binding mismatch")
	}
	// Keep totals/provenance when source observation is unavailable, but never
	// publish an unvalidated location as a candidate-bound ranking.
	unavailable := ranking
	unavailable.SourceStatus = "unavailable"
	unavailable.Blocks = []faultlocalization.Block{}
	r.Spectrum = &unavailable
	if s.Workspace == nil || s.Candidate == nil {
		return r, nil
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return r, nil
	}
	defer lease.Close()
	err = lease.WithOwnership(s.Workspace.Request, func(worktree.LeaseIdentity) error {
		fresh, err := Inspect(path)
		if err != nil || fresh.ControllerHead != s.ControllerHead {
			return errors.New("spectrum journal head changed")
		}
		paths := make([]string, 0, len(spectrum.Sources))
		for _, source := range spectrum.Sources {
			paths = append(paths, source.Path)
		}
		sort.Strings(paths)
		files, err := worktree.ReadSources(ctx, *s.Workspace, *s.Candidate, paths, 32768)
		if err != nil {
			return err
		}
		return validateSpectrumSources(spectrum, ranking, files)
	})
	if err != nil {
		return r, nil
	}
	fresh, err := Inspect(path)
	if err != nil || fresh.ControllerHead != s.ControllerHead {
		return r, nil
	}
	return finishRepairSpectrum(r, ranking, lease.Close()), nil
}

func finishRepairSpectrum(r RepairDiagnosis, ranking faultlocalization.Report, closeErr error) RepairDiagnosis {
	if closeErr != nil {
		return r
	}
	ranking.SourceStatus = "complete_candidate_bytes_observed"
	r.Spectrum = &ranking
	return r
}

func validateSpectrumSources(s faultlocalization.Spectrum, r faultlocalization.Report, files []worktree.SourceFile) error {
	observed := map[string]worktree.SourceFile{}
	lineWidths := map[string][]int{}
	for _, file := range files {
		observed[file.Path] = file
	}
	for _, source := range s.Sources {
		f, ok := observed[source.Path]
		if !ok || !completeSpectrumFile(f, source, s.CandidateID) {
			return errors.New("spectrum source bytes unavailable or changed")
		}
		for _, line := range bytes.Split(f.Content, []byte{'\n'}) {
			lineWidths[source.Path] = append(lineWidths[source.Path], len(line))
		}
	}
	for _, block := range r.Blocks {
		widths := lineWidths[block.Path]
		if !spectrumRangeFits(block, widths) {
			return errors.New("spectrum range outside source bytes")
		}
	}
	return nil
}

func completeSpectrumFile(f worktree.SourceFile, source faultlocalization.Source, candidate string) bool {
	if f.Err != nil || f.CandidateID != candidate || f.SHA256 != source.SHA256 || f.NextOffset != nil || f.Size != int64(len(f.Content)) || len(f.Content) > 32768 || !utf8.Valid(f.Content) {
		return false
	}
	digest := sha256.Sum256(f.Content)
	return hex.EncodeToString(digest[:]) == source.SHA256
}

func spectrumRangeFits(b faultlocalization.Block, widths []int) bool {
	if b.StartLine < 1 || b.EndLine < 1 || b.StartLine > len(widths) || b.EndLine > len(widths) {
		return false
	}
	return b.StartColumn > 0 && b.EndColumn > 0 && b.StartColumn <= widths[b.StartLine-1]+1 && b.EndColumn <= widths[b.EndLine-1]+1
}
