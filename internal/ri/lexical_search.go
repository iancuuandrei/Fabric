package ri

import (
	"context"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LexicalQuery binds exact matcher options; limit is a page size, not semantics.
type LexicalQuery struct {
	Pattern         string `json:"pattern"`
	Fixed           bool   `json:"fixed"`
	CaseInsensitive bool   `json:"case_insensitive"`
	Path            string `json:"path,omitempty"`
	Type            string `json:"type,omitempty"`
}

// ID is shared with the Rust matcher profile.
func (q LexicalQuery) ID() (string, error) {
	if len(q.Pattern) > 16<<10 || !utf8.ValidString(q.Pattern) {
		return "", errors.New("invalid lexical query")
	}
	if err := validateLexicalFilters(q.Path, q.Type); err != nil {
		return "", err
	}
	if q.Path == "" && q.Type == "" {
		return canonical.Hash("harness.ri.lexical-query.v1", map[string]any{"pattern": q.Pattern, "fixed": q.Fixed, "case_insensitive": q.CaseInsensitive, "profile": "rust-regex-bytes-v1-tgrep-e2007b52"})
	}
	var path, fileType any
	if q.Path != "" {
		path = q.Path
	}
	if q.Type != "" {
		fileType = q.Type
	}
	return canonical.Hash("harness.ri.lexical-query.v2", map[string]any{"pattern": q.Pattern, "fixed": q.Fixed, "case_insensitive": q.CaseInsensitive, "path": path, "type": fileType, "profile": "rust-regex-bytes-v1-tgrep-e2007b52-path-type-v1"})
}

func validateLexicalFilters(path, fileType string) error {
	if path != "" {
		if len(path) > 4096 || !utf8.ValidString(path) || strings.ContainsAny(path, "\\:") {
			return errors.New("invalid lexical path filter")
		}
		for _, r := range path {
			if unicode.IsControl(r) {
				return errors.New("invalid lexical path filter")
			}
		}
		for _, part := range strings.Split(path, "/") {
			if part == "" || part == "." || part == ".." {
				return errors.New("invalid lexical path filter")
			}
		}
	}
	if fileType != "" {
		if len(fileType) > 32 || !utf8.ValidString(fileType) {
			return errors.New("invalid lexical type filter")
		}
		for n, b := range []byte(fileType) {
			if !((b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (n > 0 && (b == '_' || b == '+' || b == '-'))) {
				return errors.New("invalid lexical type filter")
			}
		}
	}
	return nil
}

// LexicalCursor resumes one exact match in an immutable query scope.
type LexicalCursor struct {
	ManifestID string   `json:"manifest_id"`
	QueryID    string   `json:"query_id"`
	Path       string   `json:"path"`
	Range      [2]int64 `json:"range"`
}

// LexicalHit carries an admitted source locator and half-open byte interval.
type LexicalHit struct {
	Path  string   `json:"path"`
	Blob  string   `json:"blob"`
	Range [2]int64 `json:"range"`
}

// LexicalResult reports bounded evidence over the explicit manifest scope.
type LexicalResult struct {
	QueryID        string         `json:"query_id"`
	Next           *LexicalCursor `json:"next"`
	ManifestID     string         `json:"manifest_id"`
	FullScan       bool           `json:"full_scan"`
	CandidateFiles int            `json:"candidate_files"`
	SearchedFiles  int            `json:"searched_files"`
	Truncated      bool           `json:"truncated"`
	Matches        []LexicalHit   `json:"matches"`
}

// LexicalRef is controller-retained staging context, not authority supplied by a worker.
type LexicalRef struct {
	ManifestPath string          `json:"manifest_path"`
	SourceRoot   string          `json:"source_root"`
	IndexPath    string          `json:"index_path"`
	Manifest     LexicalManifest `json:"manifest"`
	Build        LexicalBuild    `json:"build"`
}

func lexicalBefore(aPath string, a [2]int64, bPath string, b [2]int64) bool {
	return aPath < bPath || (aPath == bPath && (a[0] < b[0] || (a[0] == b[0] && a[1] < b[1])))
}

// SearchLexical invokes the exact matcher and validates returned scope/locators.
// Completeness and matcher correctness depend on the admitted Rust executable;
// this projection does not independently reproduce regex evaluation in Go.
func (c Client) SearchLexical(ctx context.Context, ref LexicalRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	return c.searchLexical(ctx, ref, nil, query, limit, after)
}

func (c Client) searchLexical(ctx context.Context, ref LexicalRef, overlay *LexicalOverlayRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	return searchLexical(ctx, c.Call, ref, overlay, query, limit, after)
}

// SearchLexical reuses the owned process with the same admission and result
// validation as the one-shot client. Artifacts are still verified per request.
func (s *Stream) SearchLexical(ctx context.Context, ref LexicalRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	return searchLexical(ctx, s.Call, ref, nil, query, limit, after)
}

// SearchLexicalOverlay searches a candidate overlay through the owned process,
// validating hits and pagination against the complete shadowed scope.
func (s *Stream) SearchLexicalOverlay(ctx context.Context, ref LexicalRef, overlay LexicalOverlayRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	return searchLexical(ctx, s.Call, ref, &overlay, query, limit, after)
}

func searchLexical(ctx context.Context, call func(context.Context, any) (json.RawMessage, error), ref LexicalRef, overlay *LexicalOverlayRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	var result LexicalResult
	manifestID, err := ref.Manifest.ID()
	if err != nil {
		return result, err
	}
	queryID, err := query.ID()
	if err != nil {
		return result, err
	}
	buildID, err := ref.Build.ID()
	if err != nil {
		return result, err
	}
	if ref.Build.ManifestID != manifestID || limit < 1 || limit > 10000 {
		return result, errors.New("invalid lexical search admission")
	}
	for _, path := range []string{ref.ManifestPath, ref.SourceRoot, ref.IndexPath} {
		if !filepath.IsAbs(path) {
			return result, errors.New("absolute lexical artifact paths required")
		}
	}
	request := map[string]any{"operation": "lexical_search", "manifest_path": ref.ManifestPath, "manifest_id": manifestID, "source": ref.Manifest.Source, "source_root": ref.SourceRoot, "index_path": ref.IndexPath, "build": ref.Build, "build_id": buildID, "pattern": query.Pattern, "fixed": query.Fixed, "case_insensitive": query.CaseInsensitive, "limit": limit, "after": after}
	if query.Path != "" {
		request["path_filter"] = query.Path
	}
	if query.Type != "" {
		request["type_filter"] = query.Type
	}
	if overlay != nil {
		var files []LexicalFile
		manifestID, files, err = overlay.scope(ref.Manifest)
		if err != nil {
			return result, err
		}
		changedID, _ := overlay.Manifest.ID()
		request["overlay"] = map[string]any{"candidate": overlay.Candidate, "manifest_path": overlay.ManifestPath, "manifest_id": changedID, "source_root": overlay.SourceRoot, "deleted": overlay.Deleted}
		ref.Manifest.Files = files
	}
	if after != nil && (after.ManifestID != manifestID || after.QueryID != queryID) {
		return result, errors.New("lexical cursor scope mismatch")
	}
	raw, err := call(ctx, request)
	if err != nil {
		return result, err
	}
	if err := canonical.Decode(raw, &result); err != nil {
		return LexicalResult{}, err
	}
	if result.QueryID != queryID || result.ManifestID != manifestID || result.CandidateFiles < 0 || result.CandidateFiles > len(ref.Manifest.Files) || result.SearchedFiles < 0 || result.SearchedFiles > result.CandidateFiles || len(result.Matches) > limit || result.Truncated != (result.Next != nil) || (result.Truncated && len(result.Matches) != limit) {
		return LexicalResult{}, errors.New("invalid lexical result scope or bounds")
	}
	files := map[string]LexicalFile{}
	if (len(result.Matches) > 0 && result.SearchedFiles == 0) || (!result.Truncated && result.SearchedFiles != result.CandidateFiles) || (result.FullScan && result.CandidateFiles != len(ref.Manifest.Files)) {
		return LexicalResult{}, errors.New("inconsistent lexical search counts")
	}
	for _, file := range ref.Manifest.Files {
		files[file.Path] = file
	}
	for n, hit := range result.Matches {
		file, ok := files[hit.Path]
		if !ok || hit.Blob != file.Blob || hit.Range[0] < 0 || hit.Range[1] < hit.Range[0] || hit.Range[1] > file.Bytes {
			return LexicalResult{}, errors.New("invalid lexical source hit")
		}
		if n > 0 {
			previous := result.Matches[n-1]
			if !lexicalBefore(previous.Path, previous.Range, hit.Path, hit.Range) {
				return LexicalResult{}, errors.New("unordered lexical matches")
			}
		}
		if after != nil && !lexicalBefore(after.Path, after.Range, hit.Path, hit.Range) {
			return LexicalResult{}, errors.New("lexical page did not advance")
		}
	}
	if result.Next != nil {
		last := result.Matches[len(result.Matches)-1]
		if *result.Next != (LexicalCursor{manifestID, queryID, last.Path, last.Range}) {
			return LexicalResult{}, errors.New("lexical continuation differs from last hit")
		}
	}
	return result, nil
}
