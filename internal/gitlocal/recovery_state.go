package gitlocal

import (
	"context"
	"errors"
	"strings"

	"harness.local/engorch/internal/worktree"
)

// RecoveryState describes an exact observation, not permission to retry writes.
type RecoveryState struct {
	Stage     string             `json:"stage"`
	Candidate worktree.Candidate `json:"candidate"`
}

// InspectRecovery admits only the original candidate before ref advancement,
// or the verified new commit with its original or completed index. It requires
// a caller-held lease and makes no process-quiescence assertion.
func InspectRecovery(ctx context.Context, plan CommitPlan, treeID, commitID string) (RecoveryState, error) {
	_, expected, err := CommitObject(plan, treeID)
	if err != nil {
		return RecoveryState{}, err
	}
	if expected != commitID {
		return RecoveryState{}, errors.New("recovery prediction mismatch")
	}
	headBytes, err := gitObjectCommand(ctx, plan.Workspace.Request.Path, nil, 128, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return RecoveryState{}, err
	}
	head := strings.TrimSpace(string(headBytes))
	if head == plan.Candidate.Head {
		objects, err := ReadCandidateObjects(ctx, plan)
		if err != nil {
			return RecoveryState{}, err
		}
		if objects.CommitID != commitID || objects.Trees[len(objects.Trees)-1].ObjectID != treeID {
			return RecoveryState{}, errors.New("recovery candidate predicts different objects")
		}
		return RecoveryState{Stage: "BEFORE_REF", Candidate: plan.Candidate}, nil
	}
	if head != commitID {
		return RecoveryState{}, errors.New("unrecognized commit recovery HEAD")
	}
	candidate, err := observePostCommit(ctx, plan, treeID, commitID, false)
	if err != nil {
		return RecoveryState{}, err
	}
	_, indexErr := gitObjectCommand(ctx, plan.Workspace.Request.Path, nil, 1024, "diff-index", "--cached", "--quiet", "--no-ext-diff", commitID, "--")
	stage := "FINALIZED"
	if indexErr != nil {
		if candidate.IndexHash != plan.Candidate.IndexHash {
			return RecoveryState{}, errors.New("unrecognized partial commit index")
		}
		stage = "AFTER_REF"
	}
	updated := plan.Workspace
	updated.Request.Source.Commit, updated.Request.Source.Tree = commitID, treeID
	after, err := worktree.Fingerprint(ctx, updated)
	if err != nil {
		return RecoveryState{}, err
	}
	if candidate != after {
		return RecoveryState{}, errors.New("commit recovery state changed during observation")
	}
	return RecoveryState{Stage: stage, Candidate: candidate}, nil
}
