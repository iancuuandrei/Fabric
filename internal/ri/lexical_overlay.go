package ri

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// LexicalOverlayRef describes immutable changed bytes admitted by the controller.
// The candidate fingerprint must come from its governed candidate observation;
// this transport descriptor does not itself establish candidate authority.
type LexicalOverlayRef struct {
	Candidate    string          `json:"candidate"`
	ManifestPath string          `json:"manifest_path"`
	SourceRoot   string          `json:"source_root"`
	Manifest     LexicalManifest `json:"manifest"`
	Deleted      []string        `json:"deleted"`
}

// ID validates the overlay scope and returns its shared Rust/Go identity.
func (o LexicalOverlayRef) ID(base LexicalManifest) (string, error) {
	id, _, err := o.scope(base)
	return id, err
}

func (o LexicalOverlayRef) scope(base LexicalManifest) (string, []LexicalFile, error) {
	baseID, err := base.ID()
	if err != nil {
		return "", nil, err
	}
	changedID, err := o.Manifest.ID()
	if err != nil {
		return "", nil, err
	}
	if err := safepath.RequireDigest(o.Candidate); err != nil {
		return "", nil, err
	}
	if o.Manifest.Source != base.Source || !filepath.IsAbs(o.ManifestPath) || !filepath.IsAbs(o.SourceRoot) || o.Deleted == nil {
		return "", nil, errors.New("invalid lexical overlay source or artifact binding")
	}
	files := make(map[string]LexicalFile, len(base.Files))
	for _, file := range base.Files {
		files[file.Path] = file
	}
	deleted := append([]string{}, o.Deleted...)
	sort.Strings(deleted)
	for n, path := range deleted {
		if _, ok := files[path]; !ok || (n > 0 && path == deleted[n-1]) {
			return "", nil, errors.New("invalid lexical tombstone")
		}
		delete(files, path)
	}
	var total int64
	for _, file := range o.Manifest.Files {
		i := sort.SearchStrings(deleted, file.Path)
		if i < len(deleted) && deleted[i] == file.Path {
			return "", nil, errors.New("changed lexical path is also deleted")
		}
		if file.Bytes > (64<<20)-total {
			return "", nil, errors.New("overlay exceeds resident source ceiling")
		}
		total += file.Bytes
		files[file.Path] = file
	}
	id, err := canonical.Hash("harness.ri.lexical-overlay.v1", map[string]any{"base": baseID, "candidate": o.Candidate, "changed": changedID, "deleted": deleted})
	if err != nil {
		return "", nil, err
	}
	merged := make([]LexicalFile, 0, len(files))
	for _, file := range files {
		merged = append(merged, file)
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Path < merged[j].Path })
	return id, merged, nil
}

// SearchLexicalOverlay validates returned hits against the merged, shadowed scope.
func (c Client) SearchLexicalOverlay(ctx context.Context, base LexicalRef, overlay LexicalOverlayRef, query LexicalQuery, limit int, after *LexicalCursor) (LexicalResult, error) {
	return c.searchLexical(ctx, base, &overlay, query, limit, after)
}
