package worktree

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// Candidate binds observed file bytes, modes, HEAD and index to one workspace.
// It includes ignored files; this is source-state evidence, not hermetic build proof.
type Candidate struct {
	Version    int    `json:"version"`
	WorktreeID string `json:"worktree_id"`
	Head       string `json:"head"`
	IndexHash  string `json:"index_hash"`
	FilesHash  string `json:"files_hash"`
	FileCount  int    `json:"file_count"`
}

// IndexObservation retains the raw index tamper signal alongside its semantic
// staged-entry and index-flag identity. RawHash is evidence, not the v2 verdict.
type IndexObservation struct {
	RawHash      string `json:"raw_hash"`
	SemanticHash string `json:"semantic_hash"`
}

// ID validates and hashes a candidate observation; it does not refresh the files.
func (c Candidate) ID() (string, error) {
	if (c.Version != 1 && c.Version != 2) || c.FileCount < 0 || c.FileCount > 4096 {
		return "", errors.New("invalid candidate")
	}
	for _, h := range []string{c.WorktreeID, c.IndexHash, c.FilesHash} {
		if err := safepath.RequireDigest(h); err != nil {
			return "", err
		}
	}
	if len(c.Head) != 40 && len(c.Head) != 64 || strings.Trim(c.Head, "0123456789abcdef") != "" {
		return "", errors.New("invalid candidate HEAD")
	}
	domain := "harness.candidate.v1"
	if c.Version == 2 {
		domain = "harness.candidate.v2"
	}
	return canonical.Hash(domain, c)
}

// ValidateBinding rejects mixing legacy and semantic candidates with a binding
// configured for the other identity contract.
func (c Candidate) ValidateBinding(b Binding) error {
	id, err := b.ID()
	if err != nil {
		return err
	}
	wantVersion := 1
	if b.Request.CandidateIdentity == "semantic-index-v2" {
		wantVersion = 2
	}
	if c.Version != wantVersion || c.WorktreeID != id {
		return errors.New("candidate identity contract differs from worktree binding")
	}
	_, err = c.ID()
	return err
}

type semanticIndex struct {
	StagedEntries []byte `json:"staged_entries"`
	TrackedFlags  []byte `json:"tracked_flags"`
}

// IndexEvidence observes the exact admitted HEAD and returns bounded raw and
// semantic index identities. It fails closed if Git rewrites the index while
// staged entries or visibility/assume flags are being read.
func IndexEvidence(ctx context.Context, b Binding) (IndexObservation, error) {
	observed, err := Observe(ctx, b.Request)
	if err != nil {
		return IndexObservation{}, err
	}
	if observed != b {
		return IndexObservation{}, errors.New("worktree binding changed")
	}
	return indexEvidence(ctx, b)
}

func indexEvidence(ctx context.Context, b Binding) (IndexObservation, error) {
	if filepath.Clean(b.GitDir) == filepath.Clean(b.Request.Source.CommonDir) {
		return IndexObservation{}, errors.New("canonical index cannot be writer index")
	}
	gitRoot, err := os.OpenRoot(b.GitDir)
	if err != nil {
		return IndexObservation{}, err
	}
	defer gitRoot.Close()
	readRaw := func() (string, error) {
		hash, _, _, exists, err := safepath.ReadRegular(gitRoot, "index", 64<<20)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", errors.New("worktree index missing")
		}
		return hash, nil
	}
	before, err := readRaw()
	if err != nil {
		return IndexObservation{}, err
	}
	staged, err := git(ctx, b.Request.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return IndexObservation{}, err
	}
	flags, err := git(ctx, b.Request.Path, "ls-files", "-v", "-z")
	if err != nil {
		return IndexObservation{}, err
	}
	after, err := readRaw()
	if err != nil {
		return IndexObservation{}, err
	}
	if after != before {
		return IndexObservation{}, errors.New("worktree index changed during semantic observation")
	}
	semantic, err := canonical.Hash("harness.index.semantic.v2", semanticIndex{StagedEntries: staged, TrackedFlags: flags})
	if err != nil {
		return IndexObservation{}, err
	}
	return IndexObservation{RawHash: before, SemanticHash: semantic}, nil
}

// FileState is one observed regular file in the candidate source tree.
// It carries byte identity and executable mode, without assigning task relevance.
type FileState struct {
	Path       string `json:"path"`
	Hash       string `json:"hash"`
	Executable bool   `json:"executable"`
}

// Pristine compares an admitted candidate with raw source blobs, using one
// bounded Git batch. It rejects extra or partial files during reconciliation.
func Pristine(ctx context.Context, b Binding) (Candidate, error) {
	candidate, err := Fingerprint(ctx, b)
	if err != nil {
		return candidate, err
	}
	items, err := inventory(ctx, b.Request)
	if err != nil {
		return candidate, err
	}
	expected := []FileState{}
	err = streamBlobs(ctx, b.Request, items, func(item blob, r io.Reader, n int64) error {
		h := sha256.New()
		if _, err := io.CopyN(h, r, n); err != nil {
			return err
		}
		expected = append(expected, FileState{item.name, hex.EncodeToString(h.Sum(nil)), item.mode == "100755" && runtime.GOOS != "windows"})
		return nil
	})
	if err != nil {
		return candidate, err
	}
	sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
	hash, err := FilesID(expected)
	if err != nil {
		return candidate, err
	}
	if candidate.FilesHash != hash || candidate.FileCount != len(expected) {
		return candidate, errors.New("workspace is not exact pristine source")
	}
	index, err := git(ctx, b.Request.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return candidate, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	var expectedIndex strings.Builder
	for _, item := range items {
		expectedIndex.WriteString(item.mode + " " + item.object + " 0\t" + item.name + "\x00")
	}
	if !bytes.Equal(index, []byte(expectedIndex.String())) {
		return candidate, errors.New("workspace index is not pristine source")
	}
	return candidate, nil
}

// Fingerprint admits the workspace then hashes a bounded sorted file observation.
// Callers must hold the writer lease and compare before/after operation results.
func Fingerprint(ctx context.Context, b Binding) (Candidate, error) {
	c, _, err := Capture(ctx, b)
	return c, err
}

// Capture returns the candidate and its exact file manifest from one observation.
// Callers must hold the writer lease; the manifest is used to predict file effects.
func Capture(ctx context.Context, b Binding) (Candidate, []FileState, error) {
	observed, err := Observe(ctx, b.Request)
	if err != nil {
		return Candidate{}, nil, err
	}
	if observed != b {
		return Candidate{}, nil, errors.New("worktree binding changed")
	}
	id, err := b.ID()
	if err != nil {
		return Candidate{}, nil, err
	}
	root, err := os.OpenRoot(b.Request.Path)
	if err != nil {
		return Candidate{}, nil, err
	}
	defer root.Close()
	files := []FileState{}
	total := int64(0)
	seen := map[string]bool{}
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if p == ".git" {
			if d.IsDir() {
				return errors.New("workspace Git marker changed to directory")
			}
			return nil
		}
		if err := safepath.Check(root, p, false); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		key := strings.ToLower(p)
		if seen[key] {
			return errors.New("case-colliding candidate paths")
		}
		seen[key] = true
		h, n, x, exists, err := safepath.ReadRegular(root, p, 64<<20)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("file disappeared during fingerprint")
		}
		total += n
		if total > 512<<20 || len(files) >= 4096 {
			return errors.New("candidate bounds exceeded")
		}
		files = append(files, FileState{p, h, x})
		return nil
	})
	if err != nil {
		return Candidate{}, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	filesHash, err := FilesID(files)
	if err != nil {
		return Candidate{}, nil, err
	}
	version, indexHash := 1, ""
	if b.Request.CandidateIdentity == "semantic-index-v2" {
		index, err := indexEvidence(ctx, b)
		if err != nil {
			return Candidate{}, nil, err
		}
		version, indexHash = 2, index.SemanticHash
	} else {
		gitRoot, err := os.OpenRoot(b.GitDir)
		if err != nil {
			return Candidate{}, nil, err
		}
		defer gitRoot.Close()
		var exists bool
		indexHash, _, _, exists, err = safepath.ReadRegular(gitRoot, "index", 64<<20)
		if err != nil {
			return Candidate{}, nil, err
		}
		if !exists {
			return Candidate{}, nil, errors.New("worktree index missing")
		}
		if filepath.Clean(b.GitDir) == filepath.Clean(b.Request.Source.CommonDir) {
			return Candidate{}, nil, errors.New("canonical index cannot be writer index")
		}
	}
	c := Candidate{Version: version, WorktreeID: id, Head: b.Request.Source.Commit, IndexHash: indexHash, FilesHash: filesHash, FileCount: len(files)}
	return c, files, c.ValidateBinding(b)
}

// FilesID validates a manifest and hashes a sorted copy. It never changes caller
// ordering and rejects aliases so a predicted tree has one meaning on every host.
func FilesID(files []FileState) (string, error) {
	if files == nil || len(files) > 4096 {
		return "", errors.New("invalid file manifest")
	}
	ordered := append([]FileState{}, files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	seen := map[string]bool{}
	for _, f := range ordered {
		if err := safepath.Relative(f.Path); err != nil {
			return "", err
		}
		if err := safepath.RequireDigest(f.Hash); err != nil {
			return "", err
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return "", errors.New("manifest path alias")
		}
		seen[key] = true
	}
	for _, f := range ordered {
		parts := strings.Split(strings.ToLower(f.Path), "/")
		for i := 1; i < len(parts); i++ {
			if seen[strings.Join(parts[:i], "/")] {
				return "", errors.New("file/directory collision")
			}
		}
	}
	return canonical.Hash("harness.files.v1", ordered)
}
