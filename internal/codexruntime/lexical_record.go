package codexruntime

import (
	"context"
	"errors"
	"path/filepath"
	"sort"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
)

// LexicalOverlayRecord retains changed-manifest identity and candidate tombstones.
// Tombstone membership is checked after hydration, not by metadata-only replay.
type LexicalOverlayRecord struct {
	Candidate  string                `json:"candidate"`
	Manifest   ri.LexicalManifestRef `json:"manifest"`
	SourceRoot string                `json:"source_root"`
	Deleted    []string              `json:"deleted"`
}

// LexicalRecord is the compact runtime representation with no inline file list.
// Its validation performs no filesystem reads; Hydrate verifies selected inputs.
type LexicalRecord struct {
	Version          int                   `json:"version"`
	Base             ri.LexicalArtifactRef `json:"base"`
	Overlay          *LexicalOverlayRecord `json:"overlay"`
	Executable       string                `json:"executable"`
	ExecutableSHA256 string                `json:"executable_sha256"`
}

// Validate checks source, candidate and artifact metadata without disk access.
func (r LexicalRecord) Validate(source repository.Identity, candidate string) error {
	expected, err := ri.FromRepository(source)
	if err != nil {
		return err
	}
	if r.Version != 1 || r.Base.Manifest.Source != expected {
		return errors.New("compact lexical source/version mismatch")
	}
	if err := r.Base.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(r.Executable) || filepath.Clean(r.Executable) != r.Executable {
		return errors.New("normalized lexical executable required")
	}
	if err := safepath.RequireDigest(r.ExecutableSHA256); err != nil {
		return err
	}
	if o := r.Overlay; o != nil {
		if candidate == "" || candidate != o.Candidate || safepath.RequireDigest(candidate) != nil {
			return errors.New("compact lexical candidate mismatch")
		}
		if err := o.Manifest.Validate(); err != nil {
			return err
		}
		if o.Manifest.Source != expected || o.Deleted == nil || len(o.Deleted) > r.Base.Manifest.Files {
			return errors.New("compact lexical overlay scope mismatch")
		}
		if !filepath.IsAbs(o.SourceRoot) || filepath.Clean(o.SourceRoot) != o.SourceRoot {
			return errors.New("normalized overlay source root required")
		}
		for n, path := range o.Deleted {
			if safepath.Relative(path) != nil || (n > 0 && o.Deleted[n-1] >= path) {
				return errors.New("invalid compact lexical tombstones")
			}
		}
	}
	return nil
}

// Record converts an admitted binding into a compact canonical record.
func (b LexicalBinding) Record(source repository.Identity, candidate string) (LexicalRecord, error) {
	if err := b.Validate(source, candidate); err != nil {
		return LexicalRecord{}, err
	}
	base, err := b.Base.Compact()
	if err != nil {
		return LexicalRecord{}, err
	}
	r := LexicalRecord{Version: 1, Base: base, Executable: b.Executable, ExecutableSHA256: b.ExecutableSHA256}
	if o := b.Overlay; o != nil {
		manifest, err := o.Manifest.Reference(o.ManifestPath)
		if err != nil {
			return LexicalRecord{}, err
		}
		deleted := append([]string{}, o.Deleted...)
		sort.Strings(deleted)
		r.Overlay = &LexicalOverlayRecord{Candidate: o.Candidate, Manifest: manifest, SourceRoot: o.SourceRoot, Deleted: deleted}
	}
	if err := r.Validate(source, candidate); err != nil {
		return LexicalRecord{}, err
	}
	if _, err := canonical.Bytes(r); err != nil {
		return LexicalRecord{}, err
	}
	return r, nil
}

// Hydrate verifies manifest files, then validates complete overlay membership and
// source-byte budgets. Rust still verifies index/source bytes during each search.
func (r LexicalRecord) Hydrate(ctx context.Context, source repository.Identity, candidate string) (LexicalBinding, error) {
	if err := r.Validate(source, candidate); err != nil {
		return LexicalBinding{}, err
	}
	base, err := r.Base.Hydrate(ctx)
	if err != nil {
		return LexicalBinding{}, err
	}
	b := LexicalBinding{Base: base, Executable: r.Executable, ExecutableSHA256: r.ExecutableSHA256}
	if o := r.Overlay; o != nil {
		manifest, err := o.Manifest.Hydrate(ctx)
		if err != nil {
			return LexicalBinding{}, err
		}
		b.Overlay = &ri.LexicalOverlayRef{Candidate: o.Candidate, ManifestPath: o.Manifest.Path, SourceRoot: o.SourceRoot, Manifest: manifest, Deleted: append([]string{}, o.Deleted...)}
	}
	if err := b.Validate(source, candidate); err != nil {
		return LexicalBinding{}, err
	}
	return b, nil
}
