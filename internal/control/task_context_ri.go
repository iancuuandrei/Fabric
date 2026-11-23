package control

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

const (
	taskContextLexicalMaxQueries = 2
	taskContextLexicalPageSize   = 8
	taskContextLexicalMaxBytes   = 64 << 10
)

var taskContextLexicalWord = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{2,}`)

// TaskContextLexicalSearch retains one exact question and the bounded result
// returned by the pinned Rust reader. Empty matches remain an observation,
// never a completeness or semantic-absence claim.
type TaskContextLexicalSearch struct {
	Query  ri.LexicalQuery  `json:"query"`
	Result ri.LexicalResult `json:"result"`
}

type taskContextLexicalSearcher interface {
	SearchLexicalOverlay(context.Context, ri.LexicalRef, ri.LexicalOverlayRef, ri.LexicalQuery, int, *ri.LexicalCursor) (ri.LexicalResult, error)
}

// taskContextCandidateLexicalEvidence searches only a controller-confirmed
// candidate overlay, reselected under the caller's existing read lease. Base
// lexical indexes and semantic RI stay available through the agent tools but
// are not mislabeled as candidate evidence here.
func taskContextCandidateLexicalEvidence(ctx context.Context, path string, s Snapshot, question string) ([]TaskContextLexicalSearch, []string, error) {
	lex, err := roleLexical(s)
	if err != nil {
		return nil, nil, err
	}
	if lex == nil || lex.Scope != "candidate" {
		return nil, nil, nil
	}
	if s.Workspace == nil || s.Candidate == nil {
		return nil, nil, errors.New("candidate lexical context requires a resolved workspace")
	}
	binding, err := selectRoleRuntimeLexical(ctx, path, s)
	if err != nil {
		return nil, nil, err
	}
	if binding.Overlay == nil {
		return nil, nil, errors.New("candidate lexical context lost its confirmed overlay")
	}
	searches, paths, err := searchTaskContextLexical(ctx, *binding, question, ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256})
	if err != nil {
		return nil, nil, err
	}
	return searches, paths, nil
}

// searchTaskContextLexical performs a small number of exact fixed-string
// searches through the pinned Rust overlay reader. The caller supplies a
// binding already revalidated against the current candidate.
func searchTaskContextLexical(ctx context.Context, binding codexruntime.LexicalBinding, question string, searcher taskContextLexicalSearcher) ([]TaskContextLexicalSearch, []string, error) {
	if binding.Overlay == nil || searcher == nil {
		return nil, nil, errors.New("candidate lexical search binding required")
	}
	queries := taskContextLexicalQueries(question)
	if len(queries) == 0 {
		return nil, nil, nil
	}
	searches := make([]TaskContextLexicalSearch, 0, len(queries))
	pathSet := map[string]bool{}
	for _, query := range queries {
		result, err := searcher.SearchLexicalOverlay(ctx, binding.Base, *binding.Overlay, query, taskContextLexicalPageSize, nil)
		if err != nil {
			return nil, nil, err
		}
		search := TaskContextLexicalSearch{Query: query, Result: result}
		if err := validateTaskContextLexicalSearch(binding, search, true); err != nil {
			return nil, nil, err
		}
		searches = append(searches, search)
		for _, hit := range result.Matches {
			pathSet[hit.Path] = true
		}
	}
	if _, err := taskContextLexicalSearchesID(searches); err != nil {
		return nil, nil, err
	}
	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return searches, paths, nil
}

// taskContextLexicalQueries extracts concrete identifiers from the task text.
// If it contains no code-shaped term, it falls back to the first two distinct
// longer words in appearance order. Queries are exact recorded inputs, not
// inferred symbols or fabricated search results.
func taskContextLexicalQueries(question string) []ri.LexicalQuery {
	words := taskContextLexicalWord.FindAllString(question, -1)
	selected := make([]string, 0, taskContextLexicalMaxQueries)
	seen := map[string]bool{}
	for _, word := range words {
		if !taskContextCodeShapedWord(word) || len(word) > 128 {
			continue
		}
		key := strings.ToLower(word)
		if seen[key] {
			continue
		}
		seen[key] = true
		selected = append(selected, word)
		if len(selected) == taskContextLexicalMaxQueries {
			break
		}
	}
	if len(selected) == 0 {
		for _, word := range words {
			if len([]rune(word)) < 6 || len(word) > 128 {
				continue
			}
			key := strings.ToLower(word)
			if seen[key] {
				continue
			}
			seen[key] = true
			selected = append(selected, word)
			if len(selected) == taskContextLexicalMaxQueries {
				break
			}
		}
	}
	queries := make([]ri.LexicalQuery, 0, len(selected))
	for _, word := range selected {
		queries = append(queries, ri.LexicalQuery{Pattern: word, Fixed: true})
	}
	return queries
}

func taskContextCodeShapedWord(word string) bool {
	if strings.ContainsAny(word, "_0123456789") {
		return true
	}
	for _, r := range word[1:] {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

func taskContextLexicalSearchesID(searches []TaskContextLexicalSearch) (string, error) {
	if len(searches) == 0 {
		return "", nil
	}
	raw, err := canonical.Bytes(searches)
	if err != nil {
		return "", err
	}
	if len(raw) > taskContextLexicalMaxBytes {
		return "", errors.New("task context lexical search evidence exceeded byte bound")
	}
	return canonical.Hash("harness.task-context.lexical-searches.v1", searches)
}

func validateTaskContextLexicalEvidence(s Snapshot, record TaskContextRecord) error {
	if len(record.LexicalSearches) == 0 {
		if record.LexicalSearchesID != "" {
			return errors.New("task context lexical search identity without searches")
		}
		if record.LexicalUnavailableReason != "" {
			if record.Version != taskContextRIUnavailableVersion || record.LexicalUnavailableReason != taskContextLexicalUnavailableReason || s.Candidate == nil {
				return errors.New("invalid unavailable candidate lexical evidence")
			}
			candidateID, err := s.Candidate.ID()
			if err != nil || candidateID != record.CandidateID {
				return errors.New("unavailable lexical evidence candidate binding mismatch")
			}
			if safepath.RequireDigest(record.LexicalBuildID) != nil || safepath.RequireDigest(record.LexicalOverlayID) != nil {
				return errors.New("unavailable lexical evidence binding missing")
			}
			lex, err := roleLexical(s)
			if err != nil || lex == nil || lex.Scope != "candidate" || lex.BuildID != record.LexicalBuildID || lex.OverlayID != record.LexicalOverlayID {
				return errors.New("unavailable lexical evidence is not bound to the current candidate overlay")
			}
		} else if record.Version == taskContextRIUnavailableVersion {
			return errors.New("RI-unavailable task context lacks fallback evidence")
		}
		return nil
	}
	if record.LexicalUnavailableReason != "" || record.Version != taskContextVersion {
		return errors.New("task context mixes lexical fallback and search evidence")
	}
	if len(record.LexicalSearches) > taskContextLexicalMaxQueries {
		return errors.New("task context lexical search count exceeded")
	}
	if s.Candidate == nil {
		return errors.New("task context lexical search requires a current candidate")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil || candidateID != record.CandidateID {
		return errors.New("task context lexical candidate binding mismatch")
	}
	id, err := taskContextLexicalSearchesID(record.LexicalSearches)
	if err != nil || id != record.LexicalSearchesID {
		return errors.New("task context lexical search identity mismatch")
	}
	if err := safepath.RequireDigest(record.LexicalBuildID); err != nil || safepath.RequireDigest(record.LexicalOverlayID) != nil {
		return errors.New("task context lexical search binding missing")
	}
	lex, err := roleLexical(s)
	if err != nil || lex == nil || lex.Scope != "candidate" || lex.BuildID != record.LexicalBuildID || lex.OverlayID != record.LexicalOverlayID {
		return errors.New("task context lexical search is not bound to the current candidate overlay")
	}
	for _, search := range record.LexicalSearches {
		if err := validateTaskContextLexicalSearchIDs(search, record.CandidateID, record.LexicalOverlayID); err != nil {
			return err
		}
	}
	raw, err := canonical.Bytes(record.LexicalSearches)
	if err != nil || len(raw) > taskContextLexicalMaxBytes {
		return errors.New("task context lexical search evidence exceeded byte bound")
	}
	return nil
}

func validateTaskContextLexicalSearch(binding codexruntime.LexicalBinding, search TaskContextLexicalSearch, validateHits bool) error {
	candidate, err := binding.Overlay.ID(binding.Base.Manifest)
	if err != nil {
		return err
	}
	if err := validateTaskContextLexicalSearchIDs(search, binding.Overlay.Candidate, candidate); err != nil {
		return err
	}
	if !validateHits {
		return nil
	}
	files := taskContextOverlayFiles(binding.Base.Manifest, *binding.Overlay)
	for _, hit := range search.Result.Matches {
		file, ok := files[hit.Path]
		if !ok || file.Blob != hit.Blob || hit.Range[0] < 0 || hit.Range[1] < hit.Range[0] || hit.Range[1] > file.Bytes {
			return errors.New("task context lexical hit differs from candidate overlay")
		}
	}
	if search.Result.CandidateFiles > len(files) || (search.Result.FullScan && search.Result.CandidateFiles != len(files)) {
		return errors.New("task context lexical scan count differs from candidate overlay")
	}
	raw, err := canonical.Bytes([]TaskContextLexicalSearch{search})
	if err != nil || len(raw) > taskContextLexicalMaxBytes {
		return errors.New("task context lexical search result exceeded byte bound")
	}
	return nil
}

func validateTaskContextLexicalSearchIDs(search TaskContextLexicalSearch, candidateID, overlayID string) error {
	queryID, err := search.Query.ID()
	if err != nil {
		return err
	}
	result := search.Result
	if result.QueryID != queryID || result.ManifestID != overlayID || len(result.Matches) > taskContextLexicalPageSize || result.CandidateFiles < 0 || result.CandidateFiles > 500000 || result.SearchedFiles < 0 || result.SearchedFiles > result.CandidateFiles || result.Truncated != (result.Next != nil) || (result.Truncated && len(result.Matches) != taskContextLexicalPageSize) || (len(result.Matches) > 0 && result.SearchedFiles == 0) || (!result.Truncated && result.SearchedFiles != result.CandidateFiles) {
		return errors.New("invalid task context lexical result scope or bounds")
	}
	var previous *ri.LexicalHit
	for i := range result.Matches {
		hit := result.Matches[i]
		if safepath.Relative(hit.Path) != nil || (len(hit.Blob) != 40 && len(hit.Blob) != 64) || strings.Trim(hit.Blob, "0123456789abcdef") != "" || hit.Range[0] < 0 || hit.Range[1] < hit.Range[0] || hit.Range[1] > 64<<20 {
			return errors.New("invalid task context lexical hit")
		}
		if search.Query.Path != "" && hit.Path != search.Query.Path && !strings.HasPrefix(hit.Path, strings.TrimSuffix(search.Query.Path, "/")+"/") {
			return errors.New("task context lexical hit outside query path")
		}
		if previous != nil && !taskContextLexicalBefore(previous.Path, previous.Range, hit.Path, hit.Range) {
			return errors.New("task context lexical hit ordering invalid")
		}
		previous = &hit
	}
	if result.Next != nil {
		if len(result.Matches) == 0 || *result.Next != (ri.LexicalCursor{ManifestID: overlayID, QueryID: queryID, Path: result.Matches[len(result.Matches)-1].Path, Range: result.Matches[len(result.Matches)-1].Range}) {
			return errors.New("task context lexical continuation mismatch")
		}
	}
	if safepath.RequireDigest(candidateID) != nil {
		return errors.New("task context lexical candidate missing")
	}
	return nil
}

// taskContextOverlayFiles reconstructs only the exact path/blob/length map
// already bound by the selected RI artifact descriptors; the Rust API remains
// the authority for search matching.
func taskContextOverlayFiles(base ri.LexicalManifest, overlay ri.LexicalOverlayRef) map[string]ri.LexicalFile {
	files := make(map[string]ri.LexicalFile, len(base.Files)+len(overlay.Manifest.Files))
	for _, file := range base.Files {
		files[file.Path] = file
	}
	for _, path := range overlay.Deleted {
		delete(files, path)
	}
	for _, file := range overlay.Manifest.Files {
		files[file.Path] = file
	}
	return files
}

func taskContextLexicalBefore(aPath string, aRange [2]int64, bPath string, bRange [2]int64) bool {
	return aPath < bPath || (aPath == bPath && (aRange[0] < bRange[0] || (aRange[0] == bRange[0] && aRange[1] < bRange[1])))
}
