package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// LexicalShard binds the three reader input files in a generated shard.
type LexicalShard struct {
	Directory string    `json:"directory"`
	Files     int       `json:"files"`
	Hashes    [3]string `json:"hashes"`
}

// LexicalBuild is staging evidence, not a published or immutable-store guarantee.
type LexicalBuild struct {
	ManifestID string         `json:"manifest_id"`
	Shards     []LexicalShard `json:"shards"`
}

// ID reproduces the Rust receipt identity after checking descriptor bounds.
func (b LexicalBuild) ID() (string, error) {
	if safepath.RequireDigest(b.ManifestID) != nil || len(b.Shards) > 500000 {
		return "", errors.New("invalid lexical build scope")
	}
	hash := sha256.New()
	hash.Write([]byte("harness.ri.lexical-disk-build.v1\n"))
	hash.Write([]byte(b.ManifestID))
	hash.Write([]byte{'\n'})
	for n, shard := range b.Shards {
		if shard.Directory != fmt.Sprintf("shard-%06d", n) || shard.Files < 1 || shard.Files > 10000 {
			return "", errors.New("invalid lexical shard descriptor")
		}
		for _, value := range shard.Hashes {
			if safepath.RequireDigest(value) != nil {
				return "", errors.New("invalid lexical shard hash")
			}
		}
		raw, err := canonical.Bytes(shard)
		if err != nil {
			return "", err
		}
		hash.Write(raw)
		hash.Write([]byte{'\n'})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// BuildLexical invokes one staged build and validates exact manifest/count/ID
// bindings. The caller must persist its effect intent and own staging isolation.
// Errors leave output unresolved and must never trigger an automatic rebuild.
func (c Client) BuildLexical(ctx context.Context, m LexicalManifest, manifestPath, sourceRoot, outputPath string, batchBytes int64, batchFiles int) (LexicalBuild, error) {
	id, err := m.ID()
	if err != nil {
		return LexicalBuild{}, err
	}
	for _, path := range []string{manifestPath, sourceRoot, outputPath} {
		if !filepath.IsAbs(path) {
			return LexicalBuild{}, errors.New("absolute lexical staging paths required")
		}
	}
	if batchBytes < 1 || batchBytes > 64<<20 || batchFiles < 1 || batchFiles > 10000 {
		return LexicalBuild{}, errors.New("invalid lexical batch bounds")
	}
	for _, file := range m.Files {
		if file.Bytes > batchBytes {
			return LexicalBuild{}, errors.New("lexical file exceeds batch ceiling")
		}
	}
	raw, err := c.Call(ctx, map[string]any{"operation": "lexical_build", "manifest_path": manifestPath, "manifest_id": id, "source": m.Source, "source_root": sourceRoot, "output_path": outputPath, "batch_bytes": batchBytes, "batch_files": batchFiles})
	if err != nil {
		return LexicalBuild{}, err
	}
	var result struct {
		ID    string       `json:"build_id"`
		Build LexicalBuild `json:"build"`
	}
	if err := canonical.Decode(raw, &result); err != nil {
		return LexicalBuild{}, err
	}
	actual, err := result.Build.ID()
	if err != nil {
		return LexicalBuild{}, err
	}
	total := 0
	for _, shard := range result.Build.Shards {
		if shard.Files > batchFiles {
			return LexicalBuild{}, errors.New("lexical shard exceeds admitted file count")
		}
		total += shard.Files
	}
	if result.ID != actual || result.Build.ManifestID != id || total != len(m.Files) {
		return LexicalBuild{}, errors.New("lexical build response differs from admission")
	}
	return result.Build, nil
}
