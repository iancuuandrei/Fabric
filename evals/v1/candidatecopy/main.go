// Command candidatecopy is a standalone evaluation observation/copy helper.
//
// It binds one finished Fabric candidate to its reviewed identity and copies
// precisely the captured FileStates to a fresh external destination. It never
// substitutes another workspace or the latest run, never follows symlinks, and
// never deletes computed paths. A failed copy is retained for BLOCKED triage.
//
// Usage:
//
//	candidatecopy --snapshot <six-field snapshot projection JSON> --expected-candidate <64hex> --destination <fresh absolute path>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

type output struct {
	CandidateID string `json:"candidate_id"`
	FilesHash   string `json:"files_hash"`
	FileCount   int    `json:"file_count"`
	Destination string `json:"destination"`
	Workspace   string `json:"workspace"`
	WorktreeID  string `json:"worktree_id"`
}

// candidateCopySnapshot is the bounded, authority-relevant projection emitted
// by the v1 evaluation runner. Keep this schema separate from control.Snapshot:
// the complete snapshot contains unrelated host and execution evidence that
// must not be copied into the helper input. Required pointer fields deliberately
// have no omitempty so canonical.Decode requires their explicit presence (null
// is valid for review when no review record exists).
type candidateCopySnapshot struct {
	RunID        string                     `json:"run_id"`
	State        string                     `json:"state"`
	Workspace    *worktree.Binding          `json:"workspace"`
	Candidate    *worktree.Candidate        `json:"candidate"`
	Verification *control.VerificationState `json:"verification"`
	Review       *control.ReviewRecord      `json:"review"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "candidatecopy BLOCKED:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	fset := flag.NewFlagSet("candidatecopy", flag.ContinueOnError)
	snapshotPath := fset.String("snapshot", "", "six-field projection of actual fabric inspect JSON")
	expected := fset.String("expected-candidate", "", "reviewed candidate digest (64 hex)")
	destination := fset.String("destination", "", "fresh absolute external path")
	if err := fset.Parse(argv); err != nil {
		return err
	}
	if *snapshotPath == "" || *expected == "" || *destination == "" {
		return errors.New("snapshot, expected-candidate and destination are required")
	}
	if err := safepath.RequireDigest(*expected); err != nil {
		return fmt.Errorf("invalid expected candidate: %w", err)
	}
	snap, err := loadSnapshot(*snapshotPath)
	if err != nil {
		return err
	}
	if err := validateSnapshot(snap, *expected); err != nil {
		return err
	}
	dest := filepath.Clean(*destination)
	if !filepath.IsAbs(dest) {
		return errors.New("destination must be absolute")
	}
	src := filepath.Clean(snap.Workspace.Request.Path)
	if err := rejectNesting(src, dest); err != nil {
		return err
	}
	parent := filepath.Dir(dest)
	if err := safepath.Directory(parent); err != nil {
		return fmt.Errorf("destination parent rejected: %w", err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		if err == nil {
			return errors.New("destination already exists")
		}
		return fmt.Errorf("destination inspection failed: %w", err)
	}
	lease, err := worktree.AcquireRead(snap.Workspace.Request)
	if err != nil {
		return fmt.Errorf("read lease unavailable: %w", err)
	}
	var result output
	observeErr := lease.WithOwnership(snap.Workspace.Request, func(worktree.LeaseIdentity) error {
		res, err := copyUnderLease(context.Background(), snap, *expected, src, dest)
		if err != nil {
			return err
		}
		result = *res
		return nil
	})
	if err := errors.Join(observeErr, lease.Close()); err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

func loadSnapshot(p string) (control.Snapshot, error) {
	var projection candidateCopySnapshot
	var snap control.Snapshot
	info, err := os.Stat(p)
	if err != nil {
		return snap, err
	}
	if info.Size() <= 0 || info.Size() > 8<<20 {
		return snap, errors.New("snapshot size out of bound")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return snap, err
	}
	if len(raw) == 0 || len(raw) > 8<<20 {
		return snap, errors.New("snapshot size out of bound")
	}
	if err := canonical.Decode(raw, &projection); err != nil {
		return snap, fmt.Errorf("snapshot decode failed: %w", err)
	}
	snap = control.Snapshot{
		RunID:        projection.RunID,
		State:        projection.State,
		Workspace:    projection.Workspace,
		Candidate:    projection.Candidate,
		Verification: projection.Verification,
		Review:       projection.Review,
	}
	return snap, nil
}

func validateSnapshot(snap control.Snapshot, expected string) error {
	if snap.Workspace == nil {
		return errors.New("snapshot has no workspace")
	}
	if snap.Candidate == nil {
		return errors.New("snapshot has no candidate")
	}
	if snap.Verification == nil {
		return errors.New("snapshot has no verification")
	}
	if err := snap.Workspace.Request.Validate(); err != nil {
		return fmt.Errorf("workspace request invalid: %w", err)
	}
	if snap.Workspace.Request.RunID != snap.RunID {
		return errors.New("workspace request is not bound to snapshot run")
	}
	if snap.State != "READY" {
		return fmt.Errorf("snapshot state is %s, not READY", snap.State)
	}
	if snap.Verification.Pending {
		return errors.New("verification pending")
	}
	if len(snap.Verification.Plan.Invocations) == 0 {
		return errors.New("verification plan is empty")
	}
	if len(snap.Verification.Observations) != len(snap.Verification.Plan.Invocations) {
		return errors.New("verification observations do not cover all planned checks")
	}
	if snap.Verification.Plan.CandidateID != expected {
		return errors.New("verification plan candidate does not match expected")
	}
	for _, o := range snap.Verification.Observations {
		if o.Result.CandidateID != expected {
			return errors.New("verification result candidate binding mismatch")
		}
		if o.Result.Status != "PASS" {
			return fmt.Errorf("verification observation status is %s, not PASS", o.Result.Status)
		}
	}
	id, err := snap.Candidate.ID()
	if err != nil {
		return fmt.Errorf("snapshot candidate invalid: %w", err)
	}
	if id != expected {
		return errors.New("snapshot candidate ID does not match expected")
	}
	if err := snap.Candidate.ValidateBinding(*snap.Workspace); err != nil {
		return fmt.Errorf("candidate/workspace binding invalid: %w", err)
	}
	if snap.Review != nil {
		var verdict control.ReviewVerdict
		if err := canonical.Decode([]byte(snap.Review.Result.Output), &verdict); err != nil {
			return fmt.Errorf("review verdict decode failed: %w", err)
		}
		if verdict.CandidateID != expected {
			return errors.New("review candidate does not match expected")
		}
		if verdict.VerificationPlanID != snap.Verification.PlanID {
			return errors.New("review verification plan mismatch")
		}
		if verdict.Decision != "approve" || len(verdict.Findings) != 0 {
			return errors.New("review did not approve with zero findings")
		}
	}
	return nil
}

func rejectNesting(src, dest string) error {
	if src == dest {
		return errors.New("destination overlaps source")
	}
	within := func(base, target string) bool {
		rel, err := filepath.Rel(base, target)
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	if within(src, dest) || within(dest, src) {
		return errors.New("destination overlaps source")
	}
	return nil
}

func copyUnderLease(ctx context.Context, snap control.Snapshot, expected, src, dest string) (*output, error) {
	binding := *snap.Workspace
	before, files, err := worktree.Capture(ctx, binding)
	if err != nil {
		return nil, fmt.Errorf("initial capture failed: %w", err)
	}
	if before != *snap.Candidate {
		return nil, errors.New("actual candidate differs from snapshot candidate before copy")
	}
	beforeID, err := before.ID()
	if err != nil || beforeID != expected {
		return nil, errors.New("actual candidate ID does not match expected before copy")
	}
	if len(files) > 4096 {
		return nil, errors.New("candidate bounds exceeded")
	}
	if err := os.Mkdir(dest, 0755); err != nil {
		return nil, fmt.Errorf("destination creation failed: %w", err)
	}
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return nil, fmt.Errorf("source root unavailable: %w", err)
	}
	defer srcRoot.Close()
	destRoot, err := os.OpenRoot(dest)
	if err != nil {
		return nil, fmt.Errorf("destination root unavailable: %w", err)
	}
	defer destRoot.Close()
	var total int64
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := safepath.Relative(f.Path); err != nil {
			return nil, fmt.Errorf("invalid candidate path: %w", err)
		}
		parent := path.Dir(f.Path)
		if parent != "." {
			if err := destRoot.MkdirAll(parent, 0755); err != nil {
				return nil, fmt.Errorf("destination directory failed for %s: %w", f.Path, err)
			}
		}
		if err := safepath.Check(destRoot, f.Path, true); err != nil {
			return nil, fmt.Errorf("destination path rejected for %s: %w", f.Path, err)
		}
		mode := os.FileMode(0644)
		if f.Executable {
			mode = 0755
		}
		w, err := destRoot.OpenFile(f.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return nil, fmt.Errorf("destination creation failed for %s: %w", f.Path, err)
		}
		hash, size, exec, exists, copyErr := safepath.CopyRegular(srcRoot, f.Path, 64<<20, w)
		syncErr := w.Sync()
		closeErr := w.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return nil, fmt.Errorf("copy failed for %s: %w", f.Path, err)
		}
		if !exists || hash != f.Hash || exec != f.Executable {
			return nil, fmt.Errorf("copied content mismatch for %s", f.Path)
		}
		total += size
		if total > 512<<20 {
			return nil, errors.New("candidate aggregate bound exceeded during copy")
		}
	}
	if err := verifyDestination(destRoot, files, before); err != nil {
		return nil, err
	}
	after, _, err := worktree.Capture(ctx, binding)
	if err != nil {
		return nil, fmt.Errorf("final capture failed: %w", err)
	}
	if after != *snap.Candidate {
		return nil, errors.New("source candidate changed during copy")
	}
	afterID, err := after.ID()
	if err != nil || afterID != expected {
		return nil, errors.New("final candidate ID does not match expected")
	}
	worktreeID, err := binding.ID()
	if err != nil {
		return nil, err
	}
	return &output{
		CandidateID: expected,
		FilesHash:   before.FilesHash,
		FileCount:   len(files),
		Destination: dest,
		Workspace:   src,
		WorktreeID:  worktreeID,
	}, nil
}

func verifyDestination(destRoot *os.Root, expected []worktree.FileState, candidate worktree.Candidate) error {
	var found []worktree.FileState
	err := fs.WalkDir(destRoot.FS(), ".", func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == "." {
			return nil
		}
		if p == ".git" {
			return errors.New("destination contains Git control path")
		}
		if err := safepath.Check(destRoot, p, false); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		h, _, x, exists, err := safepath.ReadRegular(destRoot, p, 64<<20)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("destination file disappeared during verification")
		}
		found = append(found, worktree.FileState{Path: p, Hash: h, Executable: x})
		return nil
	})
	if err != nil {
		return fmt.Errorf("destination walk failed: %w", err)
	}
	if len(found) != len(expected) {
		return fmt.Errorf("destination file count %d differs from source %d", len(found), len(expected))
	}
	want := map[string]worktree.FileState{}
	for _, f := range expected {
		want[f.Path] = f
	}
	for _, f := range found {
		w, ok := want[f.Path]
		if !ok {
			return fmt.Errorf("destination has extra file %s", f.Path)
		}
		if w.Hash != f.Hash || w.Executable != f.Executable {
			return fmt.Errorf("destination file mismatch %s", f.Path)
		}
	}
	hash, err := worktree.FilesID(found)
	if err != nil {
		return fmt.Errorf("destination manifest invalid: %w", err)
	}
	if hash != candidate.FilesHash {
		return errors.New("destination manifest does not match source files hash")
	}
	return nil
}
