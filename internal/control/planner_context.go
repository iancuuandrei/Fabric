package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const plannerContextSourceBoundedV1 = "source-bounded-v1"

const (
	plannerContextVersion   = 1
	plannerContextReadFiles = 24
	plannerContextReadBytes = 32 << 10
)

// PlannerContextRecord is a bounded view of the immutable source commit shown
// to the planner before workspace creation. It is partial coverage only; no RI
// query was performed and omitted paths make no absence claim.
type PlannerContextRecord struct {
	Version        int                  `json:"version"`
	SourceID       string               `json:"source_id"`
	Commit         string               `json:"commit"`
	Tree           string               `json:"tree"`
	Query          string               `json:"query"`
	QueryTruncated bool                 `json:"query_truncated,omitempty"`
	QueryHash      string               `json:"query_hash"`
	QueryLen       int                  `json:"query_len"`
	Manifest       taskcontext.Manifest `json:"manifest"`
	ManifestID     string               `json:"manifest_id,omitempty"`
	InventoryFiles int                  `json:"inventory_files"`
	AttemptedFiles int                  `json:"attempted_files"`
	ReadFiles      int                  `json:"read_files"`
	Coverage       string               `json:"coverage"`
	Unavailable    string               `json:"unavailable,omitempty"`
}

func plannerContextEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.PlannerContext == plannerContextSourceBoundedV1
}

func plannerContextTerms(query string) []string {
	return strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_')
	})
}

func plannerContextPathScoreTerms(path string, terms []string) int {
	score := 0
	folded := strings.ToLower(path)
	for _, term := range terms {
		if len(term) > 1 && strings.Contains(folded, term) {
			score++
		}
		// These are path hints only. A selected generated/template file still
		// contributes only its directly observed committed bytes.
		if (term == "generate" || term == "generated" || term == "generator") && (strings.Contains(folded, "gen") || strings.Contains(folded, "template")) {
			score++
		}
		if term == "template" && (strings.Contains(folded, "tmpl") || strings.Contains(folded, "template")) {
			score++
		}
	}
	return score
}

// plannerContextPathScore remains a pure convenience wrapper for tests.
func plannerContextPathScore(path, query string) int {
	return plannerContextPathScoreTerms(path, plannerContextTerms(query))
}

// AdmitPlannerContext records a small deterministic committed-source selection.
// It reads only pinned blobs and performs no workspace, RI, or provider action.
func AdmitPlannerContext(ctx context.Context, path string) (PlannerContextRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return PlannerContextRecord{}, err
	}
	if s.State != "OBJECTIVE" || !plannerContextEnabled(s) {
		return PlannerContextRecord{}, errors.New("planner context is not enabled before planning")
	}
	if s.PlannerContext != nil {
		return *s.PlannerContext, nil
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return PlannerContextRecord{}, err
	}
	query, truncated, queryHash, queryLen := truncateTaskQuery(s.Creation.Objective)
	terms := plannerContextTerms(query)
	type candidate struct {
		path  string
		score int
	}
	candidates := []candidate{}
	better := func(left, right candidate) bool {
		return left.score > right.score || left.score == right.score && left.path < right.path
	}
	inventory := 0
	err = repository.VisitSource(ctx, s.Creation.Repository, func(entry repository.SourceEntry) error {
		if entry.Kind != "file" || !taskcontext.EligiblePath(entry.Path) {
			return nil
		}
		inventory++
		next := candidate{entry.Path, plannerContextPathScoreTerms(entry.Path, terms)}
		if len(candidates) < plannerContextReadFiles {
			candidates = append(candidates, next)
			sort.Slice(candidates, func(i, j int) bool { return better(candidates[i], candidates[j]) })
		} else if better(next, candidates[len(candidates)-1]) {
			candidates[len(candidates)-1] = next
			sort.Slice(candidates, func(i, j int) bool { return better(candidates[i], candidates[j]) })
		}
		return nil
	})
	if err != nil {
		return PlannerContextRecord{}, err
	}
	files := []taskcontext.File{}
	attempted := 0
	for _, entry := range candidates {
		if attempted >= plannerContextReadFiles {
			break
		}
		attempted++
		chunk, readErr := repository.ReadSource(ctx, s.Creation.Repository, entry.path, 0, plannerContextReadBytes)
		if readErr != nil || chunk.NextOffset != nil || chunk.ContentUTF8 == nil || chunk.TotalBytes > plannerContextReadBytes {
			continue
		}
		files = append(files, taskcontext.File{Path: entry.path, Hash: chunk.ChunkHash, Content: []byte(*chunk.ContentUTF8)})
	}
	rec := PlannerContextRecord{Version: plannerContextVersion, SourceID: sourceID, Commit: s.Creation.Repository.Commit, Tree: s.Creation.Repository.Tree, Query: query, QueryTruncated: truncated, QueryHash: queryHash, QueryLen: queryLen, InventoryFiles: inventory, AttemptedFiles: attempted, ReadFiles: len(files), Coverage: "partial_committed_source_no_ri"}
	if len(files) == 0 {
		rec.Unavailable = "no_eligible_complete_utf8_source_files"
	} else {
		limits := taskcontext.DefaultLimits()
		limits.MaxInputBytes = taskContextMaxInput
		manifest, selectErr := taskcontext.Select(taskcontext.Input{Version: 1, Scope: taskcontext.Scope{SourceID: sourceID}, Objective: query, Files: files, Limits: limits})
		if selectErr != nil {
			return PlannerContextRecord{}, selectErr
		}
		manifestID, idErr := manifest.ID()
		if idErr != nil {
			return PlannerContextRecord{}, idErr
		}
		rec.Manifest, rec.ManifestID = manifest, manifestID
	}
	if err := Append(path, "planner.context-admitted", rec); err != nil {
		return PlannerContextRecord{}, err
	}
	return rec, nil
}

func plannerContextSelectedReason(reason string) bool {
	switch reason {
	case "changed_path", "path_hint", "path_match", "lexical_match", "deterministic_fallback":
		return true
	default:
		return false
	}
}

func emptyPlannerManifest(m taskcontext.Manifest) bool {
	return m.Version == 0 && m.Scope.SourceID == "" && m.Scope.CandidateID == "" && m.InputHash == "" && m.InputBytes == 0 && m.SelectedBytes == 0 && len(m.Selected) == 0 && len(m.Omissions) == 0 && m.OmittedCount == 0 && !m.OmissionsTrimmed
}

func replayPlannerContext(s *Snapshot, rec PlannerContextRecord) error {
	if s.State != "OBJECTIVE" || !plannerContextEnabled(*s) || s.PlannerContext != nil || rec.Version != plannerContextVersion || rec.Coverage != "partial_committed_source_no_ri" || rec.InventoryFiles < 0 || rec.InventoryFiles > 500000 || rec.AttemptedFiles < 0 || rec.AttemptedFiles > plannerContextReadFiles || rec.AttemptedFiles > rec.InventoryFiles || rec.ReadFiles < 0 || rec.ReadFiles > rec.AttemptedFiles {
		return errors.New("planner context transition rejected")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil || rec.SourceID != sourceID || rec.Commit != s.Creation.Repository.Commit || rec.Tree != s.Creation.Repository.Tree {
		return errors.New("planner context source substitution")
	}
	query, truncated, queryHash, queryLen := truncateTaskQuery(s.Creation.Objective)
	if rec.Query != query || rec.QueryTruncated != truncated || rec.QueryHash != queryHash || rec.QueryLen != queryLen {
		return errors.New("planner context query substitution")
	}
	if rec.Unavailable != "" {
		if rec.Unavailable != "no_eligible_complete_utf8_source_files" || rec.ReadFiles != 0 || rec.ManifestID != "" || !emptyPlannerManifest(rec.Manifest) {
			return errors.New("invalid unavailable planner context")
		}
	} else {
		m := rec.Manifest
		if m.Version != 1 || m.Scope.SourceID != rec.SourceID || m.Scope.CandidateID != "" || len(m.Selected) == 0 || len(m.Selected) > 12 || m.InputBytes < 0 || m.InputBytes > taskContextMaxInput || m.SelectedBytes < 0 || m.SelectedBytes > 48<<10 || m.SelectedBytes > m.InputBytes {
			return errors.New("invalid planner context manifest")
		}
		if m.OmittedCount < 0 || m.OmittedCount > plannerContextReadFiles || len(m.Omissions) > 64 || (m.OmissionsTrimmed && m.OmittedCount <= len(m.Omissions)) || (!m.OmissionsTrimmed && m.OmittedCount != len(m.Omissions)) {
			return errors.New("invalid planner context omissions")
		}
		seen := map[string]bool{}
		for _, omission := range m.Omissions {
			if (omission.Path != "[redacted]" && safepath.Relative(omission.Path) != nil) || strings.TrimSpace(omission.Reason) == "" || len(omission.Reason) > 64 {
				return errors.New("invalid planner context omission")
			}
		}
		selectedBytes := 0
		for _, selected := range m.Selected {
			key := strings.ToLower(selected.Path)
			if seen[key] {
				return errors.New("planner context path alias")
			}
			seen[key] = true
			if len(selected.Content) > 8<<10 || !plannerContextSelectedReason(selected.Reason) {
				return errors.New("invalid planner context selection")
			}
			selectedBytes += len(selected.Content)
		}
		if selectedBytes != m.SelectedBytes {
			return errors.New("planner context selected byte mismatch")
		}
		id, idErr := m.ID()
		if idErr != nil || id != rec.ManifestID {
			return errors.New("planner context manifest substitution")
		}
		for _, selected := range m.Selected {
			if safepath.Relative(selected.Path) != nil || safepath.RequireDigest(selected.Hash) != nil || safepath.RequireDigest(selected.ExcerptHash) != nil || selected.Start < 0 || selected.End <= selected.Start || int64(len(selected.Content)) != selected.End-selected.Start || !utf8.ValidString(selected.Content) {
				return errors.New("invalid planner context excerpt")
			}
			sum := sha256.Sum256([]byte(selected.Content))
			if hex.EncodeToString(sum[:]) != selected.ExcerptHash {
				return errors.New("planner context excerpt substitution")
			}
		}
	}
	copy := rec
	s.PlannerContext = &copy
	return nil
}
