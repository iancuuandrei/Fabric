package control

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// RepairAnchor records current-candidate bytes separately from the original
// finding. Previous-report code is an untrusted localization hint, not authority
// or proof that the original defect persists.
type RepairAnchor struct {
	ID                string `json:"id"`
	FindingID         string `json:"finding_id"`
	SourceCandidateID string `json:"source_candidate_id"`
	CandidateID       string `json:"candidate_id"`
	Path              string `json:"path,omitempty"`
	FileHash          string `json:"file_hash,omitempty"`
	Line              int    `json:"line,omitempty"`
	Column            int    `json:"column,omitempty"`
	Code              string `json:"code,omitempty"`
	Status            string `json:"status"`
	Provenance        string `json:"provenance"`
}

// DiagnoseRepairAnchored adds bounded leased file observations to a validated
// snapshot's pure diagnosis. It never appends an event, dispatches a runtime or
// grants effect authority. Optional observation failure preserves the base
// diagnosis with unavailable anchoring. Previous anchors are untrusted hints;
// only a unique exact match in the current observed file is accepted.
func DiagnoseRepairAnchored(ctx context.Context, path string, s Snapshot, previous []RepairAnchor) (RepairDiagnosis, error) {
	r, err := DiagnoseRepair(s)
	if err != nil {
		return r, err
	}
	if len(previous) > 128 {
		return r, errors.New("previous repair anchors exceed bound")
	}
	r.AnchoringStatus = "unavailable"
	if s.Workspace == nil || s.Candidate == nil {
		return r, nil
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return r, nil
	}
	defer lease.Close()
	var files map[string]worktree.SourceFile
	err = lease.WithOwnership(s.Workspace.Request, func(worktree.LeaseIdentity) error {
		var readErr error
		files, readErr = repairAnchorSources(ctx, path, s)
		return readErr
	})
	if err != nil {
		return r, nil
	}
	hints := repairAnchorHints(previous)
	anchors := make([]RepairAnchor, 0, len(r.Findings))
	for _, finding := range r.Findings {
		file, ok := files[finding.Path]
		if !ok {
			file.Err = errors.New("source unavailable")
		}
		anchor := resolveRepairAnchor(finding, r.CandidateID, file, hints[finding.ID])
		anchor.ID, err = repairAnchorID(anchor)
		if err != nil {
			return r, err
		}
		anchors = append(anchors, anchor)
	}
	// The journal is bracketed as well as the filesystem; a graph revision
	// cannot silently relabel the admitted write scope during inspection.
	fresh, err := Inspect(path)
	if err != nil || fresh.ControllerHead != s.ControllerHead {
		return r, nil
	}
	return finishRepairAnchoring(s, r, anchors, lease.Close())
}

func finishRepairAnchoring(s Snapshot, r RepairDiagnosis, anchors []RepairAnchor, closeErr error) (RepairDiagnosis, error) {
	// Close validates exclusion-guard ownership before releasing it. An
	// ownership failure must not publish verified bytes or strategy hints.
	if closeErr != nil {
		return r, nil
	}
	r.AnchoringStatus, r.Anchors = "observed", anchors
	r.Specifications = []RepairSpecification{}
	if err := r.addReadySpecifications(s); err != nil {
		return r, err
	}
	r.attachRepairAnchors()
	return r, nil
}

func repairAnchorSources(ctx context.Context, path string, s Snapshot) (map[string]worktree.SourceFile, error) {
	fresh, err := Inspect(path)
	if err != nil || fresh.ControllerHead != s.ControllerHead {
		return nil, errors.New("diagnosis head changed")
	}
	candidate, states, err := worktree.Capture(ctx, *s.Workspace)
	if err != nil || candidate != *s.Candidate {
		return nil, errors.New("diagnosis candidate changed")
	}
	exists := make(map[string]bool, len(states))
	for _, state := range states {
		exists[state.Path] = true
	}
	r, err := DiagnoseRepair(s)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, f := range r.Findings {
		if exists[f.Path] && safepath.Writable(f.Path) == nil && !seen[f.Path] {
			seen[f.Path] = true
			paths = append(paths, f.Path)
		}
	}
	sort.Strings(paths)
	if len(paths) > 24 {
		paths = paths[:24]
	}
	files := map[string]worktree.SourceFile{}
	if len(paths) == 0 {
		return files, nil
	}
	read, err := worktree.ReadSources(ctx, *s.Workspace, *s.Candidate, paths, 32768)
	if err != nil {
		return nil, err
	}
	for _, file := range read {
		files[file.Path] = file
	}
	return files, nil
}

func repairAnchorID(a RepairAnchor) (string, error) {
	a.ID = ""
	return canonical.Hash("harness.repair-anchor.v1", a)
}

func repairAnchorHints(previous []RepairAnchor) map[string]*RepairAnchor {
	hints := map[string]*RepairAnchor{}
	duplicate := map[string]bool{}
	for i := range previous {
		a := previous[i]
		if !repairAnchorHintBounded(a) {
			continue
		}
		id, err := repairAnchorID(a)
		if err != nil || id != a.ID || duplicate[a.FindingID] {
			continue
		}
		if _, exists := hints[a.FindingID]; exists {
			delete(hints, a.FindingID)
			duplicate[a.FindingID] = true
			continue
		}
		hints[a.FindingID] = &a
	}
	return hints
}

func repairAnchorHintBounded(a RepairAnchor) bool {
	if len(a.FindingID) > 64 || len(a.Code) > 4096 || safepath.Writable(a.Path) != nil || !repairAnchorVerified(a.Status) {
		return false
	}
	if safepath.RequireDigest(a.ID) != nil || safepath.RequireDigest(a.FileHash) != nil || safepath.RequireDigest(a.CandidateID) != nil || safepath.RequireDigest(a.SourceCandidateID) != nil {
		return false
	}
	return a.Provenance == "current_candidate_observation" || a.Provenance == "provided_previous_report"
}

func resolveRepairAnchor(f RepairFinding, candidateID string, file worktree.SourceFile, previous *RepairAnchor) RepairAnchor {
	a := RepairAnchor{FindingID: f.ID, SourceCandidateID: f.CandidateID, CandidateID: candidateID,
		Path: f.Path, Status: "unavailable", Provenance: "current_candidate_observation"}
	if file.Err != nil || file.CandidateID != candidateID || file.Path != f.Path || safepath.RequireDigest(file.SHA256) != nil {
		return a
	}
	a.FileHash = file.SHA256
	if f.CandidateID != candidateID {
		return relocateRepairAnchor(a, file, previous)
	}
	if f.Line == 0 {
		a.Status = "file_verified"
		return a
	}
	code, ok := repairAnchorLine(file, f.Line)
	if !ok || f.Column > len(code)+1 {
		a.Status = "range_unavailable"
		return a
	}
	a.Line, a.Column, a.Code, a.Status = f.Line, f.Column, code, "range_verified"
	return a
}

func repairAnchorLine(file worktree.SourceFile, line int) (string, bool) {
	lines := bytes.Split(file.Content, []byte{'\n'})
	if line < 1 || line > len(lines) || (line == len(lines) && file.NextOffset != nil) {
		return "", false
	}
	code := string(bytes.TrimSuffix(lines[line-1], []byte{'\r'}))
	return code, code != "" && len(code) <= 4096 && utf8.ValidString(code)
}

func relocateRepairAnchor(a RepairAnchor, file worktree.SourceFile, previous *RepairAnchor) RepairAnchor {
	a.Status = "stale_unlocalized"
	if !repairAnchorHintMatches(a, previous) {
		return a
	}
	if file.NextOffset != nil {
		a.Status = "coverage_incomplete"
		return a
	}
	for i, line := range bytes.Split(file.Content, []byte{'\n'}) {
		if string(bytes.TrimSuffix(line, []byte{'\r'})) != previous.Code {
			continue
		}
		if a.Line != 0 {
			a.Line = 0
			a.Status = "ambiguous"
			return a
		}
		a.Line = i + 1
	}
	if a.Line == 0 {
		a.Status = "anchor_missing"
		return a
	}
	a.Column = previous.Column
	a.Code, a.Status, a.Provenance = previous.Code, "code_reanchored", "provided_previous_report"
	return a
}

func repairAnchorHintMatches(a RepairAnchor, previous *RepairAnchor) bool {
	if previous == nil || previous.FindingID != a.FindingID || previous.SourceCandidateID != a.SourceCandidateID || previous.Path != a.Path {
		return false
	}
	return repairAnchorVerified(previous.Status) && previous.Code != "" && len(previous.Code) <= 4096 && utf8.ValidString(previous.Code) && !strings.ContainsAny(previous.Code, "\r\n") && previous.Column >= 0 && previous.Column <= len(previous.Code)+1
}

func (r *RepairDiagnosis) findingMatchesCandidate(f RepairFinding) bool {
	if f.CandidateBinding == "matching_recorded_candidate" {
		return true
	}
	for _, a := range r.Anchors {
		if a.FindingID == f.ID && a.CandidateID == r.CandidateID && repairAnchorVerified(a.Status) {
			return true
		}
	}
	return false
}

func repairAnchorVerified(status string) bool {
	return status == "file_verified" || status == "range_verified" || status == "code_reanchored"
}

func (r *RepairDiagnosis) attachRepairAnchors() {
	byFinding := map[string]RepairAnchor{}
	for _, a := range r.Anchors {
		if a.CandidateID == r.CandidateID && repairAnchorVerified(a.Status) {
			byFinding[a.FindingID] = a
		}
	}
	for i := range r.Specifications {
		spec := &r.Specifications[i]
		complete := true
		for _, id := range spec.FindingIDs {
			a, exists := byFinding[id]
			if !exists || !repairAnchorOwned(a.Path, spec.AllowedWritePaths) {
				complete = false
				continue
			}
			spec.AnchorIDs = append(spec.AnchorIDs, a.ID)
		}
		if complete {
			spec.SuggestedStrategy = "localized_llm"
		}
	}
}

func repairAnchorOwned(path string, writePaths []string) bool {
	for _, allowed := range writePaths {
		if path == allowed || strings.HasPrefix(path, allowed+"/") {
			return true
		}
	}
	return false
}
