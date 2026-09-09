package ri

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/safepath"
)

// LexicalManifestRef is a compact immutable identity, not a hydrated file list.
// Artifact selection remains controller authority; hydration verifies its bytes.
type LexicalManifestRef struct {
	Path   string `json:"path"`
	ID     string `json:"id"`
	Source Source `json:"source"`
	Files  int    `json:"files"`
}

// Validate checks metadata without opening or trusting the referenced artifact.
func (r LexicalManifestRef) Validate() error {
	if !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Files < 0 || r.Files > 500000 {
		return errors.New("invalid lexical manifest reference")
	}
	if err := safepath.Relative(filepath.Base(r.Path)); err != nil {
		return err
	}
	if err := safepath.RequireDigest(r.ID); err != nil {
		return err
	}
	_, err := (LexicalManifest{Version: 1, Source: r.Source, Files: []LexicalFile{}}).ID()
	return err
}

// Reference derives compact metadata from an observed manifest without writing.
func (m LexicalManifest) Reference(path string) (LexicalManifestRef, error) {
	id, err := m.ID()
	if err != nil {
		return LexicalManifestRef{}, err
	}
	ref := LexicalManifestRef{Path: path, ID: id, Source: m.Source, Files: len(m.Files)}
	if err := ref.Validate(); err != nil {
		return LexicalManifestRef{}, err
	}
	return ref, nil
}

// Hydrate reads a bounded regular singly-linked artifact under an opened root.
// It checks source, count and logical identity and returns no partial manifest.
// The caller must own the artifact lifetime; this does not make paths immutable.
func (r LexicalManifestRef) Hydrate(ctx context.Context) (LexicalManifest, error) {
	if err := ctx.Err(); err != nil {
		return LexicalManifest{}, err
	}
	if err := r.Validate(); err != nil {
		return LexicalManifest{}, err
	}
	parent := filepath.Dir(r.Path)
	if err := safepath.Directory(parent); err != nil {
		return LexicalManifest{}, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return LexicalManifest{}, err
	}
	defer root.Close()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, _, _, exists, err := safepath.CopyRegular(root, filepath.Base(r.Path), 512<<20, writer)
		if err == nil && !exists {
			err = errors.New("lexical manifest artifact missing")
		}
		_ = writer.CloseWithError(err)
		done <- err
	}()
	stop := context.AfterFunc(ctx, func() { _ = reader.CloseWithError(ctx.Err()) })
	manifest, readErr := ReadLexicalRecords(ctx, reader, r.Source, r.ID)
	_ = reader.Close()
	copyErr := <-done
	stop()
	if err := errors.Join(readErr, copyErr, ctx.Err()); err != nil {
		return LexicalManifest{}, err
	}
	if len(manifest.Files) != r.Files {
		return LexicalManifest{}, errors.New("lexical manifest count differs")
	}
	return manifest, nil
}
