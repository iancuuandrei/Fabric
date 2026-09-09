package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sort"

	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/worktree"
)

// LexicalCandidate is a leased observation, not a persisted staging receipt.
// Bytes contains only changed/created sources. The caller must govern any later
// materialization and revalidate the candidate before using it as current state.
type LexicalCandidate struct {
	Candidate worktree.Candidate
	Base      ri.LexicalManifest
	Changed   ri.LexicalManifest
	Deleted   []string
	Bytes     map[string][]byte
}

// lexicalCaptureAllowed grants observation, not file-effect authority. Review
// and ready candidates remain readable after implementation has finished.
func lexicalCaptureAllowed(s Snapshot) error {
	if s.State != "IMPLEMENTING" && s.State != "REPAIRING" && s.State != "REVIEWING" && s.State != "READY" {
		return errors.New("admitted candidate phase required for lexical observation")
	}
	if s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" || s.Candidate == nil || s.FileOutcome == "UNKNOWN" {
		return errors.New("resolved workspace and candidate required for lexical observation")
	}
	return nil
}

// ObserveLexicalCandidate derives overlay inputs from the journal's admitted
// candidate, never from a worker-supplied fingerprint or changed-file list.
func ObserveLexicalCandidate(ctx context.Context, path string) (result LexicalCandidate, err error) {
	s, err := Inspect(path)
	if err != nil {
		return result, err
	}
	if err := lexicalCaptureAllowed(s); err != nil {
		return result, err
	}
	leasedWorkspace := *s.Workspace
	lease, err := worktree.Acquire(leasedWorkspace.Request)
	if err != nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, lease.Close())
		if err != nil {
			result = LexicalCandidate{}
		}
	}()
	return observeLexicalCandidateLeased(ctx, path, leasedWorkspace)
}

// observeLexicalCandidateLeased is internal to controller flows that already
// hold the workspace writer lease throughout capture and subsequent use.
func observeLexicalCandidateLeased(ctx context.Context, path string, leasedWorkspace worktree.Binding) (result LexicalCandidate, err error) {
	defer func() {
		if err != nil {
			result = LexicalCandidate{}
		}
	}()
	s, err := Inspect(path)
	if err != nil {
		return result, err
	}
	if err := lexicalCaptureAllowed(s); err != nil {
		return result, err
	}
	if *s.Workspace != leasedWorkspace {
		return result, errors.New("lexical workspace changed during lease acquisition")
	}
	if s.Candidate == nil {
		return result, errors.New("admitted lexical candidate required")
	}
	before, files, err := worktree.Capture(ctx, *s.Workspace)
	if err != nil {
		return result, err
	}
	if before != *s.Candidate {
		return result, errors.New("lexical candidate differs from journal")
	}
	base, err := ri.ObserveLexical(ctx, s.Creation.Repository)
	if err != nil {
		return result, err
	}
	if len(base.Excluded) != 0 {
		return result, errors.New("candidate lexical base contains unsupported entries")
	}
	root, err := os.OpenRoot(s.Workspace.Request.Path)
	if err != nil {
		return result, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
		if err != nil {
			result = LexicalCandidate{}
		}
	}()
	remaining := int64(64 << 20)
	baseFiles := map[string]ri.LexicalFile{}
	for _, file := range base.Manifest.Files {
		baseFiles[file.Path] = file
	}
	result = LexicalCandidate{Candidate: before, Base: base.Manifest, Changed: ri.LexicalManifest{Version: 1, Source: base.Manifest.Source, Files: []ri.LexicalFile{}}, Deleted: []string{}, Bytes: map[string][]byte{}}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return LexicalCandidate{}, err
		}
		original, exists := baseFiles[file.Path]
		delete(baseFiles, file.Path)
		if exists && original.SHA256 == file.Hash {
			continue
		}
		var data bytes.Buffer
		digest, size, executable, exists, err := safepath.CopyRegular(root, file.Path, remaining, &data)
		if err != nil {
			return LexicalCandidate{}, err
		}
		if !exists || digest != file.Hash || executable != file.Executable {
			return LexicalCandidate{}, errors.New("lexical candidate bytes changed")
		}
		remaining -= size
		blob, err := gitlocal.ObjectID(base.Manifest.Source.ObjectFormat, "blob", data.Bytes())
		if err != nil {
			return LexicalCandidate{}, err
		}
		result.Changed.Files = append(result.Changed.Files, ri.LexicalFile{Path: file.Path, Blob: blob, SHA256: digest, Bytes: size})
		result.Bytes[file.Path] = data.Bytes()
	}
	for path := range baseFiles {
		result.Deleted = append(result.Deleted, path)
	}
	sort.Strings(result.Deleted)
	if _, err := result.Changed.ID(); err != nil {
		return LexicalCandidate{}, err
	}
	after, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return LexicalCandidate{}, err
	}
	if before != after {
		return LexicalCandidate{}, errors.New("lexical candidate changed during capture")
	}
	return result, nil
}
