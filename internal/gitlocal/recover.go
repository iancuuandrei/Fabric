package gitlocal

import (
	"context"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

// Recover executes a separately authorized recovery from its exact observed
// partial state. The caller holds the lease and has persisted the new intent.
// Errors can follow mutations and never authorize repeating this attempt.
func Recover(ctx context.Context, plan RecoveryPlan, intent effects.Intent, auth effects.Authorization) (worktree.Candidate, error) {
	expected, err := plan.Intent()
	if err != nil {
		return worktree.Candidate{}, err
	}
	if expected != intent {
		return worktree.Candidate{}, errors.New("commit recovery effect substitution")
	}
	if err := auth.Validate(intent); err != nil {
		return worktree.Candidate{}, err
	}
	current, err := InspectRecovery(ctx, plan.Commit, plan.TreeID, plan.CommitID)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if current != plan.Before {
		return worktree.Candidate{}, errors.New("commit recovery preview is stale")
	}
	readPlan := plan.Commit
	if current.Stage == "AFTER_REF" {
		readPlan.Workspace.Request.Source.Commit, readPlan.Workspace.Request.Source.Tree = plan.CommitID, plan.TreeID
		readPlan.Candidate = current.Candidate
	}
	objects, err := ReadCandidateObjects(ctx, readPlan)
	if err != nil {
		return worktree.Candidate{}, err
	}
	objects.CommitData, objects.CommitID, err = CommitObject(plan.Commit, objects.Trees[len(objects.Trees)-1].ObjectID)
	if err != nil {
		return worktree.Candidate{}, err
	}
	if objects.CommitID != plan.CommitID {
		return worktree.Candidate{}, errors.New("recovery objects changed")
	}
	if err := objects.Validate(plan.Commit); err != nil {
		return worktree.Candidate{}, err
	}
	if current.Stage == "BEFORE_REF" {
		if err := storeObjects(ctx, plan.Commit, objects); err != nil {
			return worktree.Candidate{}, err
		}
		if err := advanceRef(ctx, plan.Commit, objects); err != nil {
			return worktree.Candidate{}, err
		}
	}
	if _, err := finalizeIndex(ctx, plan.Commit, objects); err != nil {
		return worktree.Candidate{}, err
	}
	return ObserveFinalized(ctx, plan.Commit, plan.TreeID, plan.CommitID)
}
