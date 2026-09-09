package gitlocal

import (
	"context"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

// FinalizeIndex replaces only the isolated worktree's index after its authorized
// ref advancement. Caller must hold the lease and persist intent before mutation.
// This is not retry-safe: errors can follow a successful index replacement.
func FinalizeIndex(ctx context.Context, plan CommitPlan, objects CandidateObjects, intent effects.Intent, authorization effects.Authorization) (worktree.Candidate, error) {
	if err := validateAuthorization(plan, intent, authorization); err != nil {
		return worktree.Candidate{}, err
	}
	return finalizeIndex(ctx, plan, objects)
}

func finalizeIndex(ctx context.Context, plan CommitPlan, objects CandidateObjects) (worktree.Candidate, error) {
	if err := objects.Validate(plan); err != nil {
		return worktree.Candidate{}, err
	}
	updated := plan.Workspace
	updated.Request.Source.Commit = objects.CommitID
	updated.Request.Source.Tree = objects.Trees[len(objects.Trees)-1].ObjectID
	before, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if before.IndexHash != plan.Candidate.IndexHash || before.FilesHash != plan.Candidate.FilesHash || before.FileCount != plan.Candidate.FileCount {
		return worktree.Candidate{}, errors.New("candidate/index changed before index finalization")
	}
	if _, err := gitObjectCommand(ctx, updated.Request.Path, nil, 1024, "read-tree", updated.Request.Source.Tree); err != nil {
		return worktree.Candidate{}, err
	}
	if _, err := gitObjectCommand(ctx, updated.Request.Path, nil, 1024, "diff-index", "--cached", "--quiet", "--no-ext-diff", objects.CommitID, "--"); err != nil {
		return worktree.Candidate{}, err
	}
	after, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if after.FilesHash != before.FilesHash || after.FileCount != before.FileCount || after.Head != objects.CommitID {
		return worktree.Candidate{}, errors.New("candidate files changed during index finalization")
	}
	return after, nil
}
