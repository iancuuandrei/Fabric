package ri

import (
	"context"
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
)

// LexicalArtifactRef retains the manifest identity instead of its complete file
// list. Build receipts remain inline and subject to the canonical record bound.
type LexicalArtifactRef struct {
	Manifest   LexicalManifestRef `json:"manifest"`
	SourceRoot string             `json:"source_root"`
	IndexPath  string             `json:"index_path"`
	Build      LexicalBuild       `json:"build"`
}

// Validate binds artifact metadata without performing filesystem observation.
func (r LexicalArtifactRef) Validate() error {
	if err := r.Manifest.Validate(); err != nil {
		return err
	}
	if _, err := r.Build.ID(); err != nil {
		return err
	}
	if r.Build.ManifestID != r.Manifest.ID {
		return errors.New("lexical artifact build identity mismatch")
	}
	count := 0
	for _, shard := range r.Build.Shards {
		count += shard.Files
	}
	if count != r.Manifest.Files {
		return errors.New("lexical artifact build coverage mismatch")
	}
	for _, path := range []string{r.SourceRoot, r.IndexPath} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("normalized absolute lexical artifact path required")
		}
	}
	return nil
}

// ID seals all compact artifact metadata under the bounded canonical protocol.
func (r LexicalArtifactRef) ID() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.ri.lexical-artifact-ref.v1", r)
}

// Compact derives a record-sized descriptor from an observed lexical reference.
// It performs no disk writes and does not claim fresh artifact observation.
func (r LexicalRef) Compact() (LexicalArtifactRef, error) {
	manifest, err := r.Manifest.Reference(r.ManifestPath)
	if err != nil {
		return LexicalArtifactRef{}, err
	}
	ref := LexicalArtifactRef{Manifest: manifest, SourceRoot: r.SourceRoot, IndexPath: r.IndexPath, Build: r.Build}
	if _, err := ref.ID(); err != nil {
		return LexicalArtifactRef{}, err
	}
	return ref, nil
}

// Hydrate verifies the selected manifest and returns metadata for typed queries.
// The Rust query still verifies the build and source bytes before returning hits.
func (r LexicalArtifactRef) Hydrate(ctx context.Context) (LexicalRef, error) {
	if _, err := r.ID(); err != nil {
		return LexicalRef{}, err
	}
	manifest, err := r.Manifest.Hydrate(ctx)
	if err != nil {
		return LexicalRef{}, err
	}
	return LexicalRef{ManifestPath: r.Manifest.Path, SourceRoot: r.SourceRoot, IndexPath: r.IndexPath, Manifest: manifest, Build: r.Build}, nil
}
