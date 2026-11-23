package rewind

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/gitexec"
)

// Snapshot describes one lightweight Git checkpoint ref for a worktree.
type Snapshot struct {
	Step      int       `json:"step"`
	Ref       string    `json:"ref"`
	Commit    string    `json:"commit"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

func refPrefix(runID string) string {
	return "refs/fabric/snapshots/" + runID + "/"
}

// Create records the current worktree state (including untracked files) into an
// isolated Git snapshot ref without modifying HEAD or the index.
func Create(ctx context.Context, worktreeDir, runID string, step int, label string) (Snapshot, error) {
	if worktreeDir == "" || runID == "" || step < 0 {
		return Snapshot{}, errors.New("worktreeDir, runID, and non-negative step are required")
	}
	// Use a temporary index file outside the worktree so we never touch the real Git index.
	tmpIndex := filepath.Join(filepath.Dir(worktreeDir), fmt.Sprintf(".fabric-snap-%s-%d-%d", runID[:min(12, len(runID))], step, time.Now().UnixNano()))
	defer func() { _ = removeFile(tmpIndex) }()

	env := append(execEnv(), "GIT_INDEX_FILE="+tmpIndex)
	// Seed temporary index from HEAD if it exists, then add all working tree files.
	_, _ = runGitEnv(ctx, worktreeDir, env, "read-tree", "HEAD")
	if _, err := runGitEnv(ctx, worktreeDir, env, "add", "-A"); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot stage: %w", err)
	}
	tree, err := runGitEnv(ctx, worktreeDir, env, "write-tree")
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot write-tree: %w", err)
	}
	tree = strings.TrimSpace(tree)

	msg := strings.TrimSpace(label)
	if msg == "" {
		msg = fmt.Sprintf("checkpoint-%d", step)
	}
	commitArgs := []string{
		"-c", "user.name=Fabric Checkpoint",
		"-c", "user.email=checkpoint@fabric.local",
		"-c", "commit.gpgsign=false",
		"commit-tree", tree, "-m", msg,
	}
	if head, headErr := runGit(ctx, worktreeDir, "rev-parse", "--verify", "HEAD"); headErr == nil && strings.TrimSpace(head) != "" {
		commitArgs = append(commitArgs, "-p", strings.TrimSpace(head))
	}
	commit, err := runGit(ctx, worktreeDir, commitArgs...)
	if err != nil {
		return Snapshot{}, fmt.Errorf("snapshot commit-tree: %w", err)
	}
	commit = strings.TrimSpace(commit)
	ref := fmt.Sprintf("%s%04d", refPrefix(runID), step)
	if _, err := runGit(ctx, worktreeDir, "update-ref", ref, commit); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot update-ref: %w", err)
	}
	return Snapshot{
		Step:      step,
		Ref:       ref,
		Commit:    commit,
		Label:     msg,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// List returns all recorded checkpoints for runID sorted by step ascending.
func List(ctx context.Context, worktreeDir, runID string) ([]Snapshot, error) {
	prefix := refPrefix(runID)
	out, err := runGit(ctx, worktreeDir, "for-each-ref", "--format=%(refname)\t%(objectname)\t%(subject)\t%(committerdate:unix)", prefix)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		stepStr := strings.TrimPrefix(parts[0], prefix)
		step, err := strconv.Atoi(stepStr)
		if err != nil {
			continue
		}
		unixSec, _ := strconv.ParseInt(parts[3], 10, 64)
		snaps = append(snaps, Snapshot{
			Step:      step,
			Ref:       parts[0],
			Commit:    parts[1],
			Label:     parts[2],
			CreatedAt: time.Unix(unixSec, 0).UTC(),
		})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Step < snaps[j].Step })
	return snaps, nil
}

// Restore rewinds the worktree files to the exact tree recorded in step.
func Restore(ctx context.Context, worktreeDir, runID string, step int) (Snapshot, error) {
	snaps, err := List(ctx, worktreeDir, runID)
	if err != nil {
		return Snapshot{}, err
	}
	var target *Snapshot
	for i := range snaps {
		if snaps[i].Step == step {
			target = &snaps[i]
			break
		}
	}
	if target == nil {
		return Snapshot{}, fmt.Errorf("checkpoint step %d not found for run %s", step, runID)
	}
	// Clean untracked files and restore exact snapshot tree into worktree.
	if _, err := runGit(ctx, worktreeDir, "read-tree", "--reset", "-u", target.Commit); err != nil {
		return Snapshot{}, fmt.Errorf("restore read-tree: %w", err)
	}
	if _, err := runGit(ctx, worktreeDir, "clean", "-fd"); err != nil {
		return Snapshot{}, fmt.Errorf("restore clean: %w", err)
	}
	// Reset index back to HEAD so `git diff` still shows changes relative to base branch.
	_ = runGitNoOut(ctx, worktreeDir, "reset", "--mixed", "HEAD")
	return *target, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitEnv(ctx, dir, nil, args...)
}

func runGitNoOut(ctx context.Context, dir string, args ...string) error {
	_, err := runGit(ctx, dir, args...)
	return err
}

func runGitEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := gitexec.CommandContext(ctx, append([]string{"-C", dir}, args...)...)
	if len(env) > 0 {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func execEnv() []string { return osEnviron() }
