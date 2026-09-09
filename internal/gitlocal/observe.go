package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/worktree"
)

// ObserveFinalized reads exact post-commit state without writing objects, refs or
// index. The caller holds the workspace lease. Failure proves no outcome: even a
// missing object or mismatched index can follow a successfully advanced ref.
func ObserveFinalized(ctx context.Context, plan CommitPlan, treeID, commitID string) (worktree.Candidate, error) {
	return observePostCommit(ctx, plan, treeID, commitID, true)
}

func observePostCommit(ctx context.Context, plan CommitPlan, treeID, commitID string, requireFinalIndex bool) (worktree.Candidate, error) {
	data, expectedID, err := CommitObject(plan, treeID)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if expectedID != commitID {
		return worktree.Candidate{}, errors.New("commit observation prediction mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	updated := plan.Workspace
	updated.Request.Source.Commit, updated.Request.Source.Tree = commitID, treeID
	before, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if before.FilesHash != plan.Candidate.FilesHash || before.FileCount != plan.Candidate.FileCount {
		return worktree.Candidate{}, errors.New("committed candidate files differ")
	}
	// Reconstruct blobs and trees from the post-commit files. The temporary plan's
	// newly encoded child commit is unused; only its candidate objects are needed.
	postPlan := plan
	postPlan.Workspace, postPlan.Candidate = updated, before
	objects, err := ReadCandidateObjects(ctx, postPlan)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if objects.Trees[len(objects.Trees)-1].ObjectID != treeID {
		return worktree.Candidate{}, errors.New("committed tree differs from candidate bytes")
	}
	check := func(kind, id string, expected []byte) error {
		actual, err := gitObjectCommand(ctx, updated.Request.Path, nil, len(expected)+1, "cat-file", kind, id)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			return errors.New("committed object readback differs")
		}
		return nil
	}
	for _, blob := range objects.Blobs {
		if err := check("blob", blob.ObjectID, blob.Data); err != nil {
			return worktree.Candidate{}, err
		}
	}
	for _, tree := range objects.Trees {
		if err := check("tree", tree.ObjectID, tree.Data); err != nil {
			return worktree.Candidate{}, err
		}
	}
	if err := check("commit", commitID, data); err != nil {
		return worktree.Candidate{}, err
	}
	if requireFinalIndex {
		if _, err := gitObjectCommand(ctx, updated.Request.Path, nil, 1024, "diff-index", "--cached", "--quiet", "--no-ext-diff", commitID, "--"); err != nil {
			return worktree.Candidate{}, err
		}
	}
	after, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if after != before {
		return worktree.Candidate{}, errors.New("commit observation changed during readback")
	}
	return after, nil
}
