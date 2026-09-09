package control

import (
	"context"
	"errors"
	"time"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

// ExecuteCommit executes a new, explicitly authorized local commit attempt.
// A lease spans intent, object/ref/index mutation and final observation. Existing
// intents are never resumed here; uncertain attempts require reconciliation.
func ExecuteCommit(ctx context.Context, path string, p PreparedCommit, auth effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err := validatePreparedCommit(s, p); err != nil {
		return s, err
	}
	if err := auth.Validate(p.Intent); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(p.Plan.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if err := validatePreparedCommit(s, p); err != nil {
		return s, err
	}
	if s.Creation.Config.CandidateIdentity == "semantic-index-v2" {
		if _, err := observeCandidate(ctx, path, s); err != nil {
			return s, err
		}
	}
	objects, err := gitlocal.ReadCandidateObjects(ctx, p.Plan)
	if err != nil {
		return s, err
	}
	event := CommitIntent{Prepared: p, Authorization: auth, TreeID: objects.Trees[len(objects.Trees)-1].ObjectID, CommitID: objects.CommitID}
	if err := Append(path, "commit.intent", event); err != nil {
		return s, err
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	executionErr := gitlocal.StoreObjects(ctx, p.Plan, objects, p.Intent, auth)
	if executionErr == nil {
		executionErr = gitlocal.AdvanceRef(ctx, p.Plan, objects, p.Intent, auth)
	}
	if executionErr == nil {
		_, executionErr = gitlocal.FinalizeIndex(ctx, p.Plan, objects, p.Intent, auth)
	}
	// Observe even after cancellation or an execution error, with a separate bound.
	observationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	latest, observeErr := observeCommitLocked(observationCtx, path, s)
	return latest, errors.Join(executionErr, observeErr)
}
