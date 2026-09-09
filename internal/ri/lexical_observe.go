package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"harness.local/engorch/internal/canonical"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
)

// LexicalFile matches the Rust exact-byte manifest schema.
type LexicalFile struct {
	Path   string `json:"path"`
	Blob   string `json:"blob"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// LexicalManifest describes regular files in one immutable Git observation.
type LexicalManifest struct {
	Version int           `json:"version"`
	Source  Source        `json:"source"`
	Files   []LexicalFile `json:"files"`
}

// WriteRecords emits the bounded-record Rust JSONL transport. The caller owns
// fresh-file creation, synchronization and publication; failures may leave a tail.
func (m LexicalManifest) WriteRecords(out io.Writer) error {
	if out == nil {
		return errors.New("lexical manifest destination required")
	}
	if _, err := m.ID(); err != nil {
		return err
	}
	files := append([]LexicalFile(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	total := 0
	write := func(kind string, value any) error {
		raw, err := canonical.Bytes(map[string]any{"kind": kind, "value": value})
		if err != nil {
			return err
		}
		total += len(raw) + 1
		if total > 512<<20 {
			return errors.New("lexical manifest exceeds transport bound")
		}
		raw = append(raw, '\n')
		n, err := out.Write(raw)
		if err == nil && n != len(raw) {
			return io.ErrShortWrite
		}
		return err
	}
	if err := write("source", m.Source); err != nil {
		return err
	}
	for _, file := range files {
		if err := write("file", file); err != nil {
			return err
		}
	}
	return nil
}

// ID uses the same per-record identity encoding as Rust. Large scopes do not
// require serializing the entire manifest as one canonical protocol record.
func (m LexicalManifest) ID() (string, error) {
	length := 40
	if m.Source.ObjectFormat == "sha256" {
		length = 64
	} else if m.Source.ObjectFormat != "sha1" {
		return "", errors.New("invalid lexical object format")
	}
	if m.Version != 1 || len(m.Files) > 500000 || safepath.RequireDigest(m.Source.RepositoryID) != nil || len(m.Source.Commit) != length || len(m.Source.Tree) != length || strings.Trim(m.Source.Commit+m.Source.Tree, "0123456789abcdef") != "" {
		return "", errors.New("invalid lexical source")
	}
	files := append([]LexicalFile(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	hash := sha256.New()
	hash.Write([]byte("harness.ri.lexical-manifest.v1\x00"))
	raw, err := canonical.Bytes(m.Source)
	if err != nil {
		return "", err
	}
	hash.Write(raw)
	hash.Write([]byte{'\n'})
	for n, file := range files {
		if safepath.Relative(file.Path) != nil || len(file.Path) > 4096 || len(file.Blob) != length || strings.Trim(file.Blob, "0123456789abcdef") != "" || safepath.RequireDigest(file.SHA256) != nil || file.Bytes < 0 || file.Bytes > 9007199254740991 || (n > 0 && files[n-1].Path == file.Path) {
			return "", errors.New("invalid lexical file")
		}
		raw, err := canonical.Bytes(file)
		if err != nil {
			return "", err
		}
		hash.Write(raw)
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ValidateLexicalManifest verifies the Rust process computes the same scope
// identity. Inline manifests remain subject to the transport's 1MiB record limit.
func (c Client) ValidateLexicalManifest(ctx context.Context, m LexicalManifest) error {
	id, err := m.ID()
	if err != nil {
		return err
	}
	raw, err := c.Call(ctx, map[string]any{"operation": "lexical_manifest", "manifest": m, "source": m.Source})
	if err != nil {
		return err
	}
	var result struct {
		ID     string `json:"manifest_id"`
		Files  int    `json:"files"`
		Source Source `json:"source"`
	}
	if err := canonical.Decode(raw, &result); err != nil {
		return err
	}
	if result.ID != id || result.Files != len(m.Files) || result.Source != m.Source {
		return errors.New("Rust lexical identity differs from controller")
	}
	return nil
}

// ValidateLexicalArtifact reads a controller-owned JSONL artifact through Rust
// without putting its complete manifest in the bounded stdio request.
func (c Client) ValidateLexicalArtifact(ctx context.Context, path string, m LexicalManifest) error {
	if !filepath.IsAbs(path) {
		return errors.New("absolute lexical artifact path required")
	}
	id, err := m.ID()
	if err != nil {
		return err
	}
	raw, err := c.Call(ctx, map[string]any{"operation": "lexical_manifest_file", "path": path, "manifest_id": id, "source": m.Source})
	if err != nil {
		return err
	}
	var result struct {
		ID     string `json:"manifest_id"`
		Files  int    `json:"files"`
		Source Source `json:"source"`
	}
	if err := canonical.Decode(raw, &result); err != nil {
		return err
	}
	if result.ID != id || result.Files != len(m.Files) || result.Source != m.Source {
		return errors.New("Rust lexical artifact identity differs from controller")
	}
	return nil
}

// LexicalObservation keeps unsupported tree leaves visible outside the Rust
// regular-file manifest. It does not claim semantic or submodule coverage.
type LexicalObservation struct {
	Manifest LexicalManifest          `json:"manifest"`
	Excluded []repository.SourceEntry `json:"excluded"`
}

// ObserveLexical builds a complete regular-file scope using a single tree stream
// and a single batch blob process. It reads exact committed objects, not worktree
// content. Failures return no partial observation or apparent complete coverage.
func ObserveLexical(ctx context.Context, identity repository.Identity) (LexicalObservation, error) {
	observed, err := repository.DiscoverCommit(ctx, identity.Root, identity.Name, identity.Commit)
	if err != nil {
		return LexicalObservation{}, err
	}
	if observed != identity {
		return LexicalObservation{}, errors.New("lexical repository identity changed")
	}
	source, err := FromRepository(identity)
	if err != nil {
		return LexicalObservation{}, err
	}
	result := LexicalObservation{Manifest: LexicalManifest{Version: 1, Source: source, Files: []LexicalFile{}}, Excluded: []repository.SourceEntry{}}
	seen := make(map[string]bool)
	err = repository.VisitSourceDigests(ctx, identity, func(entry repository.SourceEntry, digest *repository.SourceDigest) error {
		if seen[entry.Path] {
			return errors.New("duplicate lexical tree path")
		}
		seen[entry.Path] = true
		if digest == nil {
			result.Excluded = append(result.Excluded, entry)
			return nil
		}
		if err := safepath.Relative(entry.Path); err != nil {
			return err
		}
		if digest.RepositoryID != source.RepositoryID || digest.Commit != source.Commit || digest.Path != entry.Path || digest.Blob != entry.Object {
			return errors.New("lexical digest binding mismatch")
		}
		result.Manifest.Files = append(result.Manifest.Files, LexicalFile{Path: digest.Path, Blob: digest.Blob, SHA256: digest.SHA256, Bytes: digest.Bytes})
		return nil
	})
	if err != nil {
		return LexicalObservation{}, err
	}
	sort.Slice(result.Manifest.Files, func(i, j int) bool { return result.Manifest.Files[i].Path < result.Manifest.Files[j].Path })
	sort.Slice(result.Excluded, func(i, j int) bool { return result.Excluded[i].Path < result.Excluded[j].Path })
	return result, nil
}
