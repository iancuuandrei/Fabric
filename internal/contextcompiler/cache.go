package contextcompiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/codegraph"
)

const graphCacheVersion = 1

type cachedGraphRecord struct {
	Version     int              `json:"version"`
	Fingerprint string           `json:"fingerprint"`
	Graph       *codegraph.Graph `json:"graph"`
}

// LoadOrBuildGraph returns the Code Knowledge Graph for repoRoot. When useCache
// is true, it checks a SHA-256 content-addressed cache under
// <repoRoot>/.harness/cache/context/graph-v1.json and returns (graph, cacheHit, err).
func LoadOrBuildGraph(repoRoot string, useCache bool) (*codegraph.Graph, bool, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, false, err
	}
	if !useCache {
		g, err := codegraph.Build(absRoot)
		return g, false, err
	}

	fp := computeRepoFingerprint(absRoot)
	cachePath := filepath.Join(absRoot, ".harness", "cache", "context", "graph-v1.json")
	if raw, err := os.ReadFile(cachePath); err == nil && fp != "" {
		var rec cachedGraphRecord
		if json.Unmarshal(raw, &rec) == nil && rec.Version == graphCacheVersion && rec.Fingerprint == fp && rec.Graph != nil {
			return rec.Graph, true, nil
		}
	}

	g, err := codegraph.Build(absRoot)
	if err != nil {
		return nil, false, err
	}
	writeCachedGraph(cachePath, fp, g)
	return g, false, nil
}

func computeRepoFingerprint(absRoot string) string {
	var paths []string
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", ".harness", ".fabric", "node_modules", "target", "vendor", "dist", "build", ".next", "__pycache__", ".venv", "venv":
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !isFingerprintExt(ext, name) {
			return nil
		}
		if len(paths) < 2500 {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)

	h := sha256.New()
	for _, p := range paths {
		rel, err := filepath.Rel(absRoot, p)
		if err != nil {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		_, _ = h.Write([]byte{0})
		sum := sha256.Sum256(raw)
		_, _ = h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func isFingerprintExt(ext, name string) bool {
	if name == "go.mod" {
		return true
	}
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".py", ".rs", ".java", ".cs", ".md":
		return true
	}
	return false
}

func writeCachedGraph(cachePath, fp string, g *codegraph.Graph) {
	if fp == "" || g == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(cachedGraphRecord{
		Version:     graphCacheVersion,
		Fingerprint: fp,
		Graph:       g,
	})
	if err != nil {
		return
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		_ = os.Remove(tmp)
	}
}
