package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// taskContextBoundedV1 is the only task-context mode that enables native role
// context admission. Empty preserves legacy autonomous behavior byte-for-byte.
const taskContextBoundedV1 = "bounded-v1"

const (
	taskContextVersion                  = 1
	taskContextRIUnavailableVersion     = 2
	taskContextLexicalUnavailableReason = "candidate_lexical_search_unavailable"
	taskContextMaxReadFiles             = 24
	taskContextMaxFileBytes             = 32768
	taskContextMaxInput                 = 768 << 10
	taskContextQueryCap                 = 16 << 10
	// taskContextFullQueryMax aligns the full question/objective admission bound
	// with the CLI 256KiB limit. Selection text stays capped at 16KiB; the full
	// digest and length remain bound in the record. Serialized role invocation
	// input remains capped by runtime.NewInvocation (256KiB); builders fail
	// rather than silently trimming a mandatory objective, and no token
	// estimates are invented.
	taskContextFullQueryMax = 256 << 10
)

// Secret-redaction scope: path filtering prevents reads of common secret files
// (for example .env, secret/credential directories, private keys) and redacts
// their names in omissions, but it cannot guarantee that secrets embedded in
// ordinary source text are absent from selected excerpts. The mode stays
// opt-in via ExecutionPolicy.Context="bounded-v1" and makes no claim of
// universal secret redaction.

// TaskContextRecord is durable evidence that a bounded task-relevant text view
// was admitted for one role, source, candidate and query. Manifest.Selected
// retains the exact source text shown to the role; ManifestID binds the
// complete observed selection. Unavailable, when set, marks an explicit
// unavailable context with no eligible source text rather than a silent
// whole-repo scan.
type TaskContextRecord struct {
	// EvidenceDecisionID links an optional source acquisition to its admitted
	// finite decision. Empty preserves ordinary context records.
	EvidenceDecisionID string `json:"evidence_decision_id,omitempty"`
	Version            int    `json:"version"`
	Role               string `json:"role"`
	// IsolationTaskID binds writer context to one confirmed child candidate.
	// Empty preserves the historical record shape.
	IsolationTaskID          string                     `json:"isolation_task_id,omitempty"`
	SourceID                 string                     `json:"source_id"`
	CandidateID              string                     `json:"candidate_id"`
	Query                    string                     `json:"query"`
	QueryTruncated           bool                       `json:"query_truncated,omitempty"`
	QueryHash                string                     `json:"query_hash"`
	QueryLen                 int                        `json:"query_len"`
	Manifest                 taskcontext.Manifest       `json:"manifest"`
	ManifestID               string                     `json:"manifest_id,omitempty"`
	LexicalBuildID           string                     `json:"lexical_build_id,omitempty"`
	LexicalOverlayID         string                     `json:"lexical_overlay_id,omitempty"`
	LexicalSearches          []TaskContextLexicalSearch `json:"lexical_searches,omitempty"`
	LexicalSearchesID        string                     `json:"lexical_searches_id,omitempty"`
	LexicalUnavailableReason string                     `json:"lexical_unavailable_reason,omitempty"`
	Unavailable              string                     `json:"unavailable,omitempty"`
	RepairSpectrum           *RepairSpectrumEvidence    `json:"repair_spectrum,omitempty"`
}

// taskContextValidRole reports whether role is a native task-context consumer.
func taskContextValidRole(role string) bool {
	switch role {
	case "explorer", "writer", "fixer", "reviewer":
		return true
	default:
		return false
	}
}

// taskContextQueryHash binds the complete original query bytes.
func taskContextQueryHash(full string) string {
	sum := sha256.Sum256([]byte(full))
	return hex.EncodeToString(sum[:])
}

// truncateTaskQuery caps selection text to taskContextQueryCap bytes on a UTF-8
// boundary. It returns the bound text, whether truncation occurred, the full
// query digest and the original length.
func truncateTaskQuery(full string) (string, bool, string, int) {
	hash := taskContextQueryHash(full)
	orig := len(full)
	if len(full) <= taskContextQueryCap {
		return full, false, hash, orig
	}
	cut := taskContextQueryCap
	for cut > 0 && !utf8.RuneStart(full[cut]) {
		cut--
	}
	return full[:cut], true, hash, orig
}

// taskContextEnabled reports whether snapshot opted into bounded task context.
// Legacy empty context returns false without performing any reads or events.
func taskContextEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.Context == taskContextBoundedV1
}

// taskContextForRole returns the persisted record matching the exact current
// source, candidate, role and full query. It performs no filesystem reads and
// no selection; replay reconstructs invocations identically through this path.
//
// Disabled legacy (empty context) returns (nil, nil) so role builders omit the
// payload and retain byte-identical invocations. Bounded-v1 with no matching
// admission returns an error so Prepare builders fail rather than dispatching
// a provider with silently missing required context.
func taskContextForRole(s Snapshot, role, fullQuestion string) (*TaskContextRecord, error) {
	if !taskContextEnabled(s) {
		return nil, nil
	}
	if !taskContextValidRole(role) {
		return nil, errors.New("invalid task context role")
	}
	if strings.TrimSpace(fullQuestion) == "" || !utf8.ValidString(fullQuestion) || len(fullQuestion) > taskContextFullQueryMax {
		return nil, errors.New("task context question bound exceeded")
	}
	if s.Workspace == nil || s.Candidate == nil {
		return nil, errors.New("task context admission missing for current candidate")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return nil, err
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return nil, err
	}
	want := taskContextQueryHash(fullQuestion)
	for i := range s.TaskContexts {
		r := &s.TaskContexts[i]
		if r.IsolationTaskID == "" && r.Role == role && r.SourceID == sourceID && r.CandidateID == candidateID && r.QueryHash == want {
			out := *r
			return &out, nil
		}
	}
	return nil, errors.New("task context admission missing for current candidate and query")
}

func taskContextForIsolatedWriter(s Snapshot, role, fullQuestion string, binding isolatedWriterBinding) (*TaskContextRecord, error) {
	if !taskContextEnabled(s) || role != "writer" || binding.TaskID == "" {
		return nil, errors.New("isolated writer task context is unavailable")
	}
	if strings.TrimSpace(fullQuestion) == "" || !utf8.ValidString(fullQuestion) || len(fullQuestion) > taskContextFullQueryMax {
		return nil, errors.New("writer task context question bound exceeded")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return nil, err
	}
	candidateID, err := binding.Candidate.ID()
	if err != nil {
		return nil, err
	}
	want := taskContextQueryHash(fullQuestion)
	for i := range s.TaskContexts {
		r := &s.TaskContexts[i]
		if r.IsolationTaskID == binding.TaskID && r.Role == role && r.SourceID == sourceID && r.CandidateID == candidateID && r.QueryHash == want {
			copy := *r
			return &copy, nil
		}
	}
	return nil, errors.New("isolated writer task context admission missing")
}

// writerTaskRole derives the task-context role for writer invocations. Repair
// state uses the fixer role; all other states use writer.
func writerTaskRole(s Snapshot) string {
	if s.Creation.Config.Version == 2 && s.State == "REPAIRING" {
		return "fixer"
	}
	return "writer"
}

// maybeAdmitTaskContext admits bounded context before a native role dispatch.
// Legacy empty context performs no reads or events and returns nil.
func maybeAdmitTaskContext(ctx context.Context, path, role, question string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if !taskContextEnabled(s) {
		return nil
	}
	if _, err := AdmitTaskContext(ctx, path, role, question); err != nil {
		return err
	}
	return nil
}

func maybeAdmitIsolatedWriterTaskContext(ctx context.Context, path, taskID, question string) error {
	s, err := Inspect(path)
	if err != nil {
		return err
	}
	if !taskContextEnabled(s) {
		return nil
	}
	if _, err := admitTaskContext(ctx, path, "writer", question, taskID); err != nil {
		return err
	}
	return nil
}

// AdmitTaskContext captures a bounded observable text view for role and
// question and persists it as a task.context-admitted event before any runtime
// intent. It requires the immutable bounded-v1 policy and a resolved current
// workspace/candidate.
//
// It acquires the existing shared worktree lease, captures the exact current
// candidate, builds a small deterministic path inventory from already captured
// FileStates (path names only), prioritizes exploration and writer proposal
// paths plus task path terms, then reads at most 24 complete candidate regular
// files with worktree.ReadSource exact binding. Only complete files of at most
// 32768 bytes are admitted; larger files are omitted, never misrepresented as
// complete. Decoded bytes are hash-verified against the admitted FileState.
// Sensitive paths are never read, reusing the exact selector eligibility
// rules. Total input is capped at 768KiB; selection returns at most the default
// 48KiB view. The query bound for selection is capped at 16KiB UTF-8 with
// explicit truncation evidence while the full query digest remains bound.
// Omissions and inventory/read-budget limits are recorded; the manifest never
// claims the repository is complete. Empty eligible selection yields an
// explicit unavailable record, never a silent whole-repo scan.
//
// An already admitted identical context (same role, source, candidate and full
// query digest) is reused without new effects. Candidate drift requires a new
// context. The lease is released before appending via normal controller Append.
// Replay performs no filesystem reads and no selection.
func AdmitTaskContext(ctx context.Context, path, role, question string) (TaskContextRecord, error) {
	return admitTaskContext(ctx, path, role, question, "")
}

func admitTaskContext(ctx context.Context, path, role, question, isolationTaskID string) (TaskContextRecord, error) {
	return admitTaskContextWithSpectrum(ctx, path, role, question, isolationTaskID, nil)
}

func admitTaskContextWithSpectrum(ctx context.Context, path, role, question, isolationTaskID string, spectrum *RepairSpectrumEvidence) (TaskContextRecord, error) {
	return admitTaskContextBound(ctx, path, role, question, isolationTaskID, spectrum, nil)
}

func admitTaskContextBound(ctx context.Context, path, role, question, isolationTaskID string, spectrum *RepairSpectrumEvidence, decision *EvidenceContextDecision) (TaskContextRecord, error) {
	if !taskContextValidRole(role) {
		return TaskContextRecord{}, errors.New("invalid task context role")
	}
	if strings.TrimSpace(question) == "" || !utf8.ValidString(question) {
		return TaskContextRecord{}, errors.New("invalid task context question")
	}
	if len(question) > taskContextFullQueryMax {
		return TaskContextRecord{}, errors.New("task context question too large")
	}
	s, err := Inspect(path)
	if err != nil {
		return TaskContextRecord{}, err
	}
	if err := requireEvidenceContextBinding(s, role, question, decision); err != nil {
		return TaskContextRecord{}, err
	}
	if s.Creation.Execution == nil || s.Creation.Execution.Context != taskContextBoundedV1 {
		return TaskContextRecord{}, errors.New("task context not enabled for this run")
	}
	if err := s.Creation.Execution.Validate(); err != nil {
		return TaskContextRecord{}, err
	}
	if s.Workspace == nil || s.Candidate == nil {
		return TaskContextRecord{}, errors.New("task context requires a resolved workspace and candidate")
	}
	workspace, candidate := *s.Workspace, *s.Candidate
	var isolated *isolatedWriterBinding
	if isolationTaskID != "" {
		if role != "writer" {
			return TaskContextRecord{}, errors.New("isolated context is restricted to graph writers")
		}
		if stagedIsolationEnabled(s) {
			binding, err := stagedForkBindingForTask(s, isolationTaskID)
			if err != nil {
				return TaskContextRecord{}, err
			}
			isolated = &binding
			workspace, candidate = binding.Workspace, binding.Candidate
		} else {
			binding, err := isolatedWriterBindingForTask(s, isolationTaskID)
			if err != nil {
				return TaskContextRecord{}, err
			}
			isolated = &binding
			workspace, candidate = binding.Workspace, binding.Candidate
		}
	} else if isolatedImplementationEnabled(s) && s.State == "IMPLEMENTING" && role == "writer" {
		return TaskContextRecord{}, errors.New("isolated graph writer requires child-bound task context")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return TaskContextRecord{}, err
	}
	candidateID, err := candidate.ID()
	if err != nil {
		return TaskContextRecord{}, err
	}
	boundQuery, truncated, queryHash, queryLen := truncateTaskQuery(question)
	// Reuse without new effects: same role, source, candidate and full query.
	for _, existing := range s.TaskContexts {
		if existing.IsolationTaskID == isolationTaskID && existing.Role == role && existing.SourceID == sourceID && existing.CandidateID == candidateID && existing.QueryHash == queryHash {
			return reuseRepairSpectrumContext(existing, spectrum)
		}
	}
	lease, err := worktree.AcquireRead(workspace.Request)
	if err != nil {
		return TaskContextRecord{}, err
	}
	// Capture under the shared lease and require the exact current candidate.
	captured, fileStates, captureErr := worktree.Capture(ctx, workspace)
	if captureErr != nil {
		_ = lease.Close()
		return TaskContextRecord{}, captureErr
	}
	if captured != candidate {
		_ = lease.Close()
		return TaskContextRecord{}, errors.New("task context candidate drift before admission")
	}
	fresh, err := Inspect(path)
	if err != nil {
		_ = lease.Close()
		return TaskContextRecord{}, err
	}
	if err := requireEvidenceContextBinding(fresh, role, question, decision); err != nil {
		_ = lease.Close()
		return TaskContextRecord{}, err
	}
	if isolationTaskID == "" {
		if fresh.Workspace == nil || fresh.Candidate == nil || *fresh.Candidate != candidate {
			_ = lease.Close()
			return TaskContextRecord{}, errors.New("task context candidate changed before admission")
		}
	} else if stagedIsolationEnabled(fresh) {
		current, currentErr := stagedForkBindingForTask(fresh, isolationTaskID)
		if currentErr != nil || current.Workspace != workspace || current.Candidate != candidate {
			_ = lease.Close()
			return TaskContextRecord{}, errors.Join(errors.New("isolated task context candidate changed before admission"), currentErr)
		}
	} else {
		current, currentErr := isolatedWriterBindingForTask(fresh, isolationTaskID)
		if currentErr != nil || current.Workspace != workspace || current.Candidate != candidate {
			_ = lease.Close()
			return TaskContextRecord{}, errors.Join(errors.New("isolated task context candidate changed before admission"), currentErr)
		}
	}
	// Use the freshest journal metadata for hints without extra dispatch.
	s = fresh
	explorationPaths := taskContextExplorationPaths(s)
	evidencePaths := taskContextEvidencePathsForCandidate(s, sourceID, candidateID)
	combinedExploration := mergeTaskContextHintPaths(explorationPaths, evidencePaths)
	writerPaths := taskContextWriterPaths(s)
	var lexicalSearches []TaskContextLexicalSearch
	var lexicalPaths []string
	lexicalUnavailableReason := ""
	if isolated == nil {
		lexicalSearches, lexicalPaths, err = taskContextCandidateLexicalEvidence(ctx, path, s, boundQuery)
		if err != nil {
			if ri.IsProcessUnavailableOnly(err) {
				// Candidate identity and overlay selection have already been
				// checked. Only failure of the optional RI child process degrades
				// to the ordinary bounded source excerpts below.
				lexicalUnavailableReason = taskContextLexicalUnavailableReason
				lexicalSearches = nil
				lexicalPaths = nil
			} else {
				_ = lease.Close()
				return TaskContextRecord{}, err
			}
		}
	}
	selectorMode := taskContextSelectorMode(s)
	prioritized := prioritizeTaskPaths(fileStates, combinedExploration, writerPaths, lexicalPaths, boundQuery)
	if selectorMode == taskContextSelectorRRFCoverageV1 {
		prioritized = prioritizeTaskPathsRRF(fileStates, combinedExploration, writerPaths, lexicalPaths, boundQuery)
	}
	if isolated != nil {
		prioritized = prioritizeTaskPaths(fileStates, combinedExploration, isolated.Task.WritePaths, lexicalPaths, boundQuery)
		if selectorMode == taskContextSelectorRRFCoverageV1 {
			prioritized = prioritizeTaskPathsRRF(fileStates, combinedExploration, isolated.Task.WritePaths, lexicalPaths, boundQuery)
		}
	}
	type readFile struct {
		path    string
		hash    string
		content []byte
	}
	reads := []readFile{}
	preOmissions := []taskcontext.Omission{}
	total := 0
	attempts := 0
	readStates := []worktree.FileState{}
	// Lexical provenance binds any candidate-overlay search evidence below.
	var lexicalBuild, lexicalOverlay string
	if lex, lexErr := roleLexical(s); lexErr != nil {
		_ = lease.Close()
		return TaskContextRecord{}, lexErr
	} else if lex != nil && isolated == nil {
		lexicalBuild = lex.BuildID
		lexicalOverlay = lex.OverlayID
	}
	lexicalSearchesID, err := taskContextLexicalSearchesID(lexicalSearches)
	if err != nil {
		_ = lease.Close()
		return TaskContextRecord{}, err
	}
	for _, fs := range prioritized {
		if attempts >= taskContextMaxReadFiles {
			if taskcontext.EligiblePath(fs.Path) {
				appendTaskOmission(&preOmissions, 64, fs.Path, "read_budget")
			} else {
				appendTaskOmission(&preOmissions, 64, "[redacted]", "sensitive_path")
			}
			continue
		}
		if !taskcontext.EligiblePath(fs.Path) {
			if len(preOmissions) < 64 {
				preOmissions = append(preOmissions, taskcontext.Omission{Path: "[redacted]", Reason: "sensitive_path"})
			}
			continue
		}
		// ReadSources enforces the same protected-path floor as writer inputs.
		// Do not let one intentionally unreadable path (for example a GitHub
		// workflow) abort the entire bounded context batch; retain it as an
		// explicit omission and continue with eligible source files.
		if err := safepath.Writable(fs.Path); err != nil {
			appendTaskOmission(&preOmissions, 64, fs.Path, "unreadable")
			continue
		}
		attempts++
		readStates = append(readStates, fs)
	}
	readPaths := make([]string, len(readStates))
	for i, fs := range readStates {
		readPaths[i] = fs.Path
	}
	chunks := []worktree.SourceFile{}
	if len(readPaths) > 0 {
		var readErr error
		chunks, readErr = worktree.ReadSources(ctx, workspace, captured, readPaths, taskContextMaxFileBytes)
		if readErr != nil {
			_ = lease.Close()
			return TaskContextRecord{}, readErr
		}
	}
	if len(chunks) != len(readStates) {
		_ = lease.Close()
		return TaskContextRecord{}, errors.New("task context batch read count mismatch")
	}
	for i, fs := range readStates {
		chunk := chunks[i]
		if chunk.Err != nil {
			// Absent or changed file: record explicitly, never claim complete.
			appendTaskOmission(&preOmissions, 64, fs.Path, "unreadable")
			continue
		}
		if chunk.CandidateID != candidateID || chunk.Path != fs.Path {
			_ = lease.Close()
			return TaskContextRecord{}, errors.New("task context read binding mismatch")
		}
		if chunk.Size > taskContextMaxFileBytes || chunk.NextOffset != nil {
			appendTaskOmission(&preOmissions, 64, fs.Path, "file_too_large")
			continue
		}
		raw := chunk.Content
		if int64(len(raw)) != chunk.Size {
			_ = lease.Close()
			return TaskContextRecord{}, errors.New("task context chunk size mismatch")
		}
		sum := sha256.Sum256(raw)
		got := hex.EncodeToString(sum[:])
		if got != fs.Hash || got != chunk.SHA256 {
			_ = lease.Close()
			return TaskContextRecord{}, errors.New("task context content hash mismatch")
		}
		if total+len(raw) > taskContextMaxInput {
			appendTaskOmission(&preOmissions, 64, fs.Path, "input_limit")
			continue
		}
		total += len(raw)
		reads = append(reads, readFile{fs.Path, fs.Hash, raw})
	}
	// Reject drift observed during reads before persisting anything.
	after, fpErr := worktree.Fingerprint(ctx, workspace)
	if fpErr != nil {
		_ = lease.Close()
		return TaskContextRecord{}, fpErr
	}
	if after != captured {
		_ = lease.Close()
		return TaskContextRecord{}, errors.New("task context candidate changed during reads")
	}
	if err := lease.Close(); err != nil {
		return TaskContextRecord{}, err
	}
	if len(reads) == 0 {
		if spectrum != nil {
			return TaskContextRecord{}, errors.New("repair spectrum requires complete selected source bytes")
		}
		rec := TaskContextRecord{
			EvidenceDecisionID: evidenceContextDecisionID(decision),
			Version:            taskContextRecordVersion(lexicalUnavailableReason),
			Role:               role,
			IsolationTaskID:    isolationTaskID,
			SourceID:           sourceID,
			CandidateID:        candidateID,
			Query:              boundQuery,
			QueryTruncated:     truncated,
			QueryHash:          queryHash,
			QueryLen:           queryLen,
			Manifest: taskcontext.Manifest{
				Version:   1,
				Scope:     taskcontext.Scope{SourceID: sourceID, CandidateID: candidateID},
				InputHash: strings.Repeat("0", 64),
				Selected:  []taskcontext.SelectedFile{},
				Omissions: []taskcontext.Omission{},
				Selector:  selectorMode,
			},
			LexicalBuildID:           lexicalBuild,
			LexicalOverlayID:         lexicalOverlay,
			LexicalSearches:          lexicalSearches,
			LexicalSearchesID:        lexicalSearchesID,
			LexicalUnavailableReason: lexicalUnavailableReason,
			Unavailable:              "no_eligible_files",
		}
		if err := Append(path, "task.context-admitted", rec); err != nil {
			// Reuse on concurrent admission of the identical unavailable context.
			latest, inspectErr := Inspect(path)
			if inspectErr != nil {
				return TaskContextRecord{}, errors.Join(err, inspectErr)
			}
			for _, existing := range latest.TaskContexts {
				if existing.IsolationTaskID == isolationTaskID && existing.Role == role && existing.SourceID == sourceID && existing.CandidateID == candidateID && existing.QueryHash == queryHash {
					return existing, nil
				}
			}
			return TaskContextRecord{}, err
		}
		return rec, nil
	}
	files := make([]taskcontext.File, 0, len(reads))
	for _, r := range reads {
		files = append(files, taskcontext.File{Path: r.path, Hash: r.hash, Content: r.content})
	}
	changed, hints := taskContextSelectorHints(writerPaths, append(combinedExploration, lexicalPaths...))
	limits := taskcontext.DefaultLimits()
	limits.MaxInputBytes = taskContextMaxInput
	manifest, selErr := taskcontext.Select(taskcontext.Input{
		Version:      1,
		Scope:        taskcontext.Scope{SourceID: sourceID, CandidateID: candidateID},
		Objective:    boundQuery,
		Files:        files,
		ChangedPaths: changed,
		PathHints:    hints,
		Selector:     selectorMode,
		Limits:       limits,
	})
	if selErr != nil {
		return TaskContextRecord{}, selErr
	}
	// Record pre-selection omissions (sensitive, too-large, budget limits)
	// without claiming the repository is complete.
	for _, o := range preOmissions {
		if len(manifest.Omissions) < limits.MaxOmissions {
			manifest.Omissions = append(manifest.Omissions, o)
		}
	}
	manifest.OmittedCount += len(preOmissions)
	manifest.OmissionsTrimmed = manifest.OmittedCount > len(manifest.Omissions)
	manifestID, err := manifest.ID()
	if err != nil {
		return TaskContextRecord{}, err
	}
	rec := TaskContextRecord{
		EvidenceDecisionID:       evidenceContextDecisionID(decision),
		Version:                  taskContextRecordVersion(lexicalUnavailableReason),
		Role:                     role,
		IsolationTaskID:          isolationTaskID,
		SourceID:                 sourceID,
		CandidateID:              candidateID,
		Query:                    boundQuery,
		QueryTruncated:           truncated,
		QueryHash:                queryHash,
		QueryLen:                 queryLen,
		Manifest:                 manifest,
		ManifestID:               manifestID,
		LexicalBuildID:           lexicalBuild,
		LexicalOverlayID:         lexicalOverlay,
		LexicalSearches:          lexicalSearches,
		LexicalSearchesID:        lexicalSearchesID,
		LexicalUnavailableReason: lexicalUnavailableReason,
		RepairSpectrum:           spectrum,
	}
	if _, err := recordedRepairSpectrum(s, &rec); err != nil {
		return TaskContextRecord{}, err
	}
	if err := Append(path, "task.context-admitted", rec); err != nil {
		latest, inspectErr := Inspect(path)
		if inspectErr != nil {
			return TaskContextRecord{}, errors.Join(err, inspectErr)
		}
		for _, existing := range latest.TaskContexts {
			if existing.IsolationTaskID == isolationTaskID && existing.Role == role && existing.SourceID == sourceID && existing.CandidateID == candidateID && existing.QueryHash == queryHash && existing.ManifestID == manifestID {
				return reuseRepairSpectrumContext(existing, spectrum)
			}
		}
		return TaskContextRecord{}, err
	}
	return rec, nil
}

func taskContextRecordVersion(lexicalUnavailableReason string) int {
	if lexicalUnavailableReason == taskContextLexicalUnavailableReason {
		return taskContextRIUnavailableVersion
	}
	return taskContextVersion
}

func appendTaskOmission(out *[]taskcontext.Omission, cap int, path, reason string) {
	if len(*out) < cap {
		*out = append(*out, taskcontext.Omission{Path: path, Reason: reason})
	}
}

// taskContextExplorationPaths collects advisory exploration paths from journal
// metadata only. It performs no filesystem reads.
func taskContextExplorationPaths(s Snapshot) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, rec := range s.Explorations {
		var obs Exploration
		if err := canonical.Decode([]byte(rec.Result.Output), &obs); err != nil {
			continue
		}
		for _, p := range obs.Paths {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// taskContextEvidencePathsForCandidate collects advisory selected source paths
// from successful decision-associated explorer admissions. It performs no
// filesystem reads and grants no scope: selected paths only bias existing
// bounded prioritization and selector hints for subsequent roles.
//
// A path contributes only when all of the following hold for the exact current
// source and candidate: the decision is present in the replay-validated
// snapshot with a positive selected action, its selected query is bounded and
// matches an admitted explorer context for the same source, candidate and
// query, and that context carries complete selected source bytes (no
// unavailable marker). Stale candidate/source, wrong role or query, missing
// context, malformed linkage and unavailable contexts contribute nothing.
// Decisions never dispatch providers, inject manifests or change acceptance.
//
// Same-query reuse is honored without inferring semantic truth: a historical
// explorer context admitted before its decision (empty EvidenceDecisionID) may
// match one validated explicit decision only when its source, candidate, role
// and exact query equal that decision's selected query. Successful reads alone
// never create hints.
func taskContextEvidencePathsForCandidate(s Snapshot, sourceID, candidateID string) []string {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(candidateID) == "" {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for i := range s.EvidenceDecisions {
		decision := &s.EvidenceDecisions[i]
		if decision.Report.Selected == "" {
			continue
		}
		selectedQuery, ok := decision.Request.Queries[decision.Report.Selected]
		if !ok || strings.TrimSpace(selectedQuery) == "" || len(selectedQuery) > taskContextQueryCap || !utf8.ValidString(selectedQuery) {
			continue
		}
		if decision.Request.Model.Binding.SourceID != sourceID || decision.Request.Model.Binding.CandidateID != candidateID {
			continue
		}
		var matched *TaskContextRecord
		for j := range s.TaskContexts {
			ctx := &s.TaskContexts[j]
			if ctx.Role != "explorer" || ctx.IsolationTaskID != "" {
				continue
			}
			if ctx.SourceID != sourceID || ctx.CandidateID != candidateID {
				continue
			}
			if ctx.Unavailable != "" || len(ctx.Manifest.Selected) == 0 {
				continue
			}
			if ctx.EvidenceDecisionID != "" {
				if ctx.EvidenceDecisionID != decision.ID {
					continue
				}
				if ctx.Query != selectedQuery {
					continue
				}
				matched = ctx
				break
			}
			if ctx.Query != selectedQuery {
				continue
			}
			matched = ctx
			break
		}
		if matched == nil {
			continue
		}
		for _, sel := range matched.Manifest.Selected {
			if strings.TrimSpace(sel.Path) == "" {
				continue
			}
			if err := safepath.Relative(sel.Path); err != nil {
				continue
			}
			if !seen[sel.Path] {
				seen[sel.Path] = true
				out = append(out, sel.Path)
			}
		}
	}
	sort.Strings(out)
	return out
}

// mergeTaskContextHintPaths deduplicates and sorts advisory hint paths
// deterministically. It performs no filesystem reads.
func mergeTaskContextHintPaths(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range a {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range b {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// taskContextWriterPaths collects writer proposal change paths from journal
// metadata only. It performs no filesystem reads.
func taskContextWriterPaths(s Snapshot) []string {
	if s.WriterProposal == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, c := range s.WriterProposal.Prepared.Proposal.Changes {
		if !seen[c.Path] {
			seen[c.Path] = true
			out = append(out, c.Path)
		}
	}
	sort.Strings(out)
	return out
}

// taskContextSelectorHints bounds selector path evidence without extra reads.
func taskContextSelectorHints(writerPaths, explorationPaths []string) ([]string, []string) {
	changed := append([]string(nil), writerPaths...)
	hints := append([]string(nil), explorationPaths...)
	if len(changed) > 512 {
		changed = changed[:512]
	}
	if len(hints) > 512 {
		hints = hints[:512]
	}
	return changed, hints
}

// prioritizeTaskPaths ranks already captured FileState names only. Exploration
// and writer proposal paths plus task path terms rank first; ties break by
// path for determinism. It performs no file reads.
func prioritizeTaskPaths(states []worktree.FileState, explorationPaths, writerPaths, lexicalPaths []string, query string) []worktree.FileState {
	exploration := map[string]bool{}
	for _, p := range explorationPaths {
		exploration[p] = true
	}
	writer := map[string]bool{}
	for _, p := range writerPaths {
		writer[p] = true
	}
	lexical := map[string]bool{}
	for _, p := range lexicalPaths {
		lexical[p] = true
	}
	terms := taskQueryTerms(query)
	type scored struct {
		state worktree.FileState
		score int
	}
	scoredList := make([]scored, 0, len(states))
	for _, st := range states {
		score := 0
		if writer[st.Path] {
			score += 1000
		}
		if exploration[st.Path] {
			score += 800
		}
		if lexical[st.Path] {
			score += 700
		}
		lower := strings.ToLower(st.Path)
		for _, term := range terms {
			if strings.Contains(lower, term) {
				score += 80
			}
		}
		scoredList = append(scoredList, scored{st, score})
	}
	sort.Slice(scoredList, func(i, j int) bool {
		if scoredList[i].score != scoredList[j].score {
			return scoredList[i].score > scoredList[j].score
		}
		return scoredList[i].state.Path < scoredList[j].state.Path
	})
	out := make([]worktree.FileState, 0, len(scoredList))
	for _, sc := range scoredList {
		out = append(out, sc.state)
	}
	return out
}

// taskQueryTerms extracts bounded lowercase path terms from the bound query.
func taskQueryTerms(query string) []string {
	seen := map[string]bool{}
	lower := strings.ToLower(query)
	for _, part := range strings.FieldsFunc(lower, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r >= 'A' && r <= 'Z')
	}) {
		if len([]rune(part)) >= 2 {
			seen[part] = true
		}
	}
	out := make([]string, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	sort.Strings(out)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// replayTaskContext validates a persisted task context admission without
// reading the filesystem or running selection. It rejects duplicate and
// substituted records and requires exact source, candidate and query binding
// plus bounded selected bytes/counts, excerpt hashes/UTF-8/ranges and the
// manifest identity.
func replayTaskContext(s *Snapshot, e journal.Event) error {
	var rec TaskContextRecord
	if err := canonical.Decode(e.Payload, &rec); err != nil {
		return err
	}
	if rec.EvidenceDecisionID != "" {
		decision := findEvidenceContextDecision(*s, rec.EvidenceDecisionID)
		if decision == nil {
			return errors.New("task context evidence decision unavailable")
		}
		if err := requireEvidenceContextBinding(*s, rec.Role, rec.Query, decision); err != nil {
			return err
		}
		if rec.IsolationTaskID != "" || rec.RepairSpectrum != nil {
			return errors.New("source acquisition cannot admit writer or repair context")
		}
	}
	if (rec.Version != taskContextVersion && rec.Version != taskContextRIUnavailableVersion) || !taskContextValidRole(rec.Role) {
		return errors.New("invalid task context record")
	}
	if (rec.Version == taskContextVersion && rec.LexicalUnavailableReason != "") || (rec.Version == taskContextRIUnavailableVersion && rec.LexicalUnavailableReason != taskContextLexicalUnavailableReason) {
		return errors.New("invalid task context lexical fallback version")
	}
	if err := safepath.RequireDigest(rec.SourceID); err != nil {
		return errors.New("invalid task context source")
	}
	if err := safepath.RequireDigest(rec.CandidateID); err != nil {
		return errors.New("invalid task context candidate")
	}
	if rec.Query == "" || len(rec.Query) > taskContextQueryCap || !utf8.ValidString(rec.Query) {
		return errors.New("invalid task context query bound")
	}
	if err := safepath.RequireDigest(rec.QueryHash); err != nil {
		return errors.New("invalid task context query digest")
	}
	if rec.QueryLen < len(rec.Query) || rec.QueryLen <= 0 || rec.QueryLen > taskContextFullQueryMax {
		return errors.New("invalid task context query length")
	}
	if rec.QueryTruncated {
		if rec.QueryLen <= len(rec.Query) {
			return errors.New("task context truncation evidence mismatch")
		}
	} else {
		if rec.QueryLen != len(rec.Query) {
			return errors.New("task context truncation evidence mismatch")
		}
		if taskContextQueryHash(rec.Query) != rec.QueryHash {
			return errors.New("task context query substitution")
		}
	}
	if s.Creation.Execution == nil || s.Creation.Execution.Context != taskContextBoundedV1 {
		return errors.New("task context not enabled for this run")
	}
	if s.Workspace == nil || s.Candidate == nil {
		return errors.New("task context requires a resolved candidate")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil || sourceID != rec.SourceID {
		return errors.New("task context source substitution")
	}
	var candidate worktree.Candidate
	if rec.IsolationTaskID == "" {
		candidate = *s.Candidate
	} else {
		if rec.Role != "writer" || !isolatedImplementationEnabled(*s) {
			return errors.New("isolated task context is not admitted for this role")
		}
		if stagedIsolationEnabled(*s) {
			fork, ok := s.GraphStagedForks[rec.IsolationTaskID]
			if !ok || fork.Outcome != "CONFIRMED" || fork.Candidate == nil || fork.Binding == nil {
				return errors.New("isolated task context requires a confirmed child")
			}
			binding, err := stagedForkBindingForTask(*s, rec.IsolationTaskID)
			if err != nil || binding.Candidate != *fork.Candidate {
				return errors.Join(errors.New("isolated task context binding mismatch"), err)
			}
			candidate = *fork.Candidate
		} else {
			state, ok := s.GraphIsolations[rec.IsolationTaskID]
			if !ok || state.Outcome != "CONFIRMED" || state.Candidate == nil || state.Binding == nil {
				return errors.New("isolated task context requires a confirmed child")
			}
			binding, err := isolatedWriterBindingForTask(*s, rec.IsolationTaskID)
			if err != nil || binding.Candidate != *state.Candidate {
				return errors.Join(errors.New("isolated task context binding mismatch"), err)
			}
			candidate = *state.Candidate
		}
	}
	candidateID, err := candidate.ID()
	if err != nil || candidateID != rec.CandidateID {
		return errors.New("task context candidate substitution")
	}
	if rec.LexicalBuildID != "" {
		if err := safepath.RequireDigest(rec.LexicalBuildID); err != nil {
			return errors.New("invalid task context lexical build")
		}
	}
	if rec.LexicalOverlayID != "" {
		if err := safepath.RequireDigest(rec.LexicalOverlayID); err != nil {
			return errors.New("invalid task context lexical overlay")
		}
	}
	if err := validateTaskContextLexicalEvidence(*s, rec); err != nil {
		return err
	}
	if rec.Unavailable != "" {
		if rec.RepairSpectrum != nil {
			return errors.New("unavailable context cannot admit repair spectrum")
		}
		if len(rec.Unavailable) > 256 || !utf8.ValidString(rec.Unavailable) {
			return errors.New("invalid task context unavailable reason")
		}
		if len(rec.Manifest.Selected) != 0 || rec.ManifestID != "" {
			return errors.New("unavailable task context must not carry selected content")
		}
		if err := validateTaskContextSelectorBinding(*s, rec.Manifest); err != nil {
			return err
		}
		for _, existing := range s.TaskContexts {
			if existing.IsolationTaskID == rec.IsolationTaskID && existing.Role == rec.Role && existing.QueryHash == rec.QueryHash && existing.CandidateID == rec.CandidateID {
				return errors.New("duplicate task context")
			}
		}
		s.TaskContexts = append(s.TaskContexts, rec)
		return nil
	}
	if rec.Unavailable == "" && rec.ManifestID == "" {
		return errors.New("task context manifest identity missing")
	}
	m := rec.Manifest
	if m.Version != 1 {
		return errors.New("invalid task context manifest version")
	}
	if err := validateTaskContextSelectorBinding(*s, m); err != nil {
		return err
	}
	if m.Scope.SourceID != rec.SourceID || m.Scope.CandidateID != rec.CandidateID {
		return errors.New("task context manifest scope mismatch")
	}
	if err := safepath.RequireDigest(m.InputHash); err != nil {
		return errors.New("invalid task context input binding")
	}
	if m.InputBytes < 0 || m.InputBytes > taskContextMaxInput || m.SelectedBytes < 0 || m.SelectedBytes > 48<<10 || m.SelectedBytes > m.InputBytes {
		return errors.New("task context byte bound exceeded")
	}
	if len(m.Selected) == 0 || len(m.Selected) > 12 {
		return errors.New("task context selection count invalid")
	}
	if m.OmittedCount < 0 || m.OmittedCount > 4096+64 {
		return errors.New("invalid task context omission count")
	}
	if len(m.Omissions) > 64 {
		return errors.New("task context omission bound exceeded")
	}
	if m.OmissionsTrimmed {
		if m.OmittedCount <= len(m.Omissions) {
			return errors.New("task context omission trim mismatch")
		}
	} else if m.OmittedCount != len(m.Omissions) {
		return errors.New("task context omission count mismatch")
	}
	seen := map[string]bool{}
	for _, sel := range m.Selected {
		if err := safepath.Relative(sel.Path); err != nil {
			return errors.New("invalid task context excerpt path")
		}
		key := strings.ToLower(sel.Path)
		if seen[key] {
			return errors.New("task context path alias")
		}
		seen[key] = true
		if err := safepath.RequireDigest(sel.Hash); err != nil {
			return errors.New("invalid task context file digest")
		}
		if err := safepath.RequireDigest(sel.ExcerptHash); err != nil {
			return errors.New("invalid task context excerpt digest")
		}
		if sel.Start < 0 || sel.End <= sel.Start {
			return errors.New("invalid task context excerpt range")
		}
		if int64(len(sel.Content)) != sel.End-sel.Start {
			return errors.New("task context excerpt range mismatch")
		}
		if !utf8.ValidString(sel.Content) {
			return errors.New("task context excerpt not UTF-8")
		}
		sum := sha256.Sum256([]byte(sel.Content))
		if hex.EncodeToString(sum[:]) != sel.ExcerptHash {
			return errors.New("task context excerpt substitution")
		}
		if strings.TrimSpace(sel.Reason) == "" || len(sel.Reason) > 64 {
			return errors.New("invalid task context excerpt reason")
		}
	}
	gotID, err := m.ID()
	if err != nil || gotID != rec.ManifestID {
		return errors.New("task context manifest substitution")
	}
	if _, err := recordedRepairSpectrum(*s, &rec); err != nil {
		return err
	}
	for _, existing := range s.TaskContexts {
		// The manifest digest describes selected candidate text and query, not
		// the consumer role. A writer and reviewer may legitimately admit the
		// same manifest for the same candidate. Role is part of the full task
		// context identity, so only treat a same-role digest as a duplicate.
		if existing.IsolationTaskID == rec.IsolationTaskID && existing.Role == rec.Role && existing.ManifestID != "" && existing.ManifestID == rec.ManifestID {
			return errors.New("duplicate task context")
		}
		if existing.IsolationTaskID == rec.IsolationTaskID && existing.Role == rec.Role && existing.QueryHash == rec.QueryHash && existing.CandidateID == rec.CandidateID {
			return errors.New("duplicate task context")
		}
	}
	s.TaskContexts = append(s.TaskContexts, rec)
	return nil
}
