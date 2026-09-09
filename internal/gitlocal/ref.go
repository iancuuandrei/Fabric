package gitlocal

import (
	"bytes"
	"context"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

// AdvanceRef atomically verifies the branch's old commit before advancing only
// that branch. HEAD's symbolic target is checked before/after, not atomically.
// Requires persisted intent and a
// caller-held lease. Any error requires reconciliation, never automatic retry.
// The index is unchanged; this is not a complete controller commit transition.
func AdvanceRef(ctx context.Context, plan CommitPlan, objects CandidateObjects, intent effects.Intent, authorization effects.Authorization) error {
	if err := validateAuthorization(plan, intent, authorization); err != nil {
		return err
	}
	return advanceRef(ctx, plan, objects)
}

func advanceRef(ctx context.Context, plan CommitPlan, objects CandidateObjects) error {
	if err := objects.Validate(plan); err != nil {
		return err
	}
	before, err := worktree.Fingerprint(ctx, plan.Workspace)
	if err != nil {
		return err
	}
	if before != plan.Candidate {
		return errors.New("candidate changed before ref update")
	}
	stored, err := gitObjectCommand(ctx, plan.Workspace.Request.Path, nil, len(objects.CommitData)+1, "cat-file", "commit", objects.CommitID)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, objects.CommitData) {
		return errors.New("stored commit differs before ref update")
	}
	ref := "refs/heads/" + plan.Workspace.Request.Branch
	if _, err := gitObjectCommand(ctx, plan.Workspace.Request.Path, nil, 1024, "update-ref", "--no-deref", ref, objects.CommitID, plan.Candidate.Head); err != nil {
		return err
	}
	updated := plan.Workspace
	updated.Request.Source.Commit = objects.CommitID
	updated.Request.Source.Tree = objects.Trees[len(objects.Trees)-1].ObjectID
	after, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return err
	}
	if after.Head != objects.CommitID || after.IndexHash != before.IndexHash || after.FilesHash != before.FilesHash || after.FileCount != before.FileCount {
		return errors.New("workspace changed during ref update")
	}
	return nil
}
