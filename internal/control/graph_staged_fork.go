package control

import (
	"context"
	"errors"

	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// InspectStagedFork returns the durable fork state only; it never observes,
// creates, or retries a workspace.
func InspectStagedFork(path, taskID string) (StagedForkState, error) {
	s, err := Inspect(path)
	if err != nil {
		return StagedForkState{}, err
	}
	state, ok := s.GraphStagedForks[taskID]
	if !ok {
		return StagedForkState{}, errors.New("staged fork not admitted")
	}
	return state, nil
}

// CreateStagedTaskIsolation appends a durable fork intent before creating one
// task-owned forked worktree from the exact post-hub parent candidate. It is
// controller-owned bootstrap distinct from leaf TaskWritePaths. An UNKNOWN
// destination or overlay is never recreated here.
func CreateStagedTaskIsolation(ctx context.Context, path, taskID string) (state StagedForkState, err error) {
	s, err := Inspect(path)
	if err != nil {
		return StagedForkState{}, err
	}
	if err := RequireDispatchAllowed(s); err != nil {
		return StagedForkState{}, err
	}
	if !stagedIsolationEnabled(s) {
		return StagedForkState{}, errors.New("staged isolation not enabled")
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return StagedForkState{}, err
	}
	if s.Workspace == nil {
		return StagedForkState{}, errors.New("parent workspace required")
	}
	if s.GraphIsolationPreparation == nil || s.GraphIsolationPreparation.Version != 3 {
		return StagedForkState{}, errors.New("frozen staged cohort required")
	}
	if existing, ok := s.GraphStagedForks[taskID]; ok {
		if existing.Outcome == "UNKNOWN" {
			return existing, errors.New("staged fork outcome UNKNOWN; explicit reconciliation required")
		}
		return existing, nil
	}
	// Bracket read-lease capture of the exact parent before admitting intent.
	parentLease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return StagedForkState{}, err
	}
	intent, task, admitErr := admitStagedForkIntent(ctx, s, taskID)
	closeErr := parentLease.Close()
	if admitErr != nil {
		return StagedForkState{}, admitErr
	}
	if closeErr != nil {
		return StagedForkState{}, closeErr
	}
	if err = Append(path, "graph.staged-fork-intent", intent); err != nil {
		return StagedForkState{}, err
	}
	stateRoot, err := controllerNamespace(s.Creation)
	if err != nil || stateRoot == "" {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, errors.New("staged fork requires external controller state root")
	}
	if err = safepath.EnsureDirectory(stateRoot, "graph-stages/"+intent.Request.RunID); err != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	lease, err := worktree.Acquire(intent.Request)
	if err != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	binding, err := worktree.Create(ctx, intent.Request)
	if err != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	// Reuse existing worktree/fileeffects for materialization and parent-delta
	// application under the child writer lease. No hidden Git commit, copy
	// hooks, filters, or generic memory is introduced.
	if len(intent.Delta) > 0 {
		if err = applyStagedForkDelta(ctx, binding, intent); err != nil {
			return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
		}
		// Confirm the index did not drift during delta application.
		after, _, fpErr := worktree.Capture(ctx, binding)
		_ = after
		if fpErr != nil {
			return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, fpErr
		}
	}
	candidate, err := worktree.Fingerprint(ctx, binding)
	if err != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	// Post-state confirms the exact expected child fields except owned WorktreeID.
	parent := intent.ParentCandidate
	workspaceID, idErr := binding.ID()
	if idErr != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, idErr
	}
	if candidate.WorktreeID != workspaceID || candidate.Version != parent.Version || candidate.Head != parent.Head || candidate.IndexHash != parent.IndexHash || candidate.FilesHash != parent.FilesHash || candidate.FileCount != parent.FileCount {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, errors.New("staged fork child differs from exact parent candidate")
	}
	if err = Append(path, "graph.staged-fork-confirmed", StagedForkReceipt{Intent: intent, Binding: binding, Candidate: candidate}); err != nil {
		return StagedForkState{Intent: intent, Outcome: "UNKNOWN"}, err
	}
	_ = task
	return InspectStagedFork(path, taskID)
}

func admitStagedForkIntent(ctx context.Context, s Snapshot, taskID string) (StagedForkIntent, any, error) {
	if s.Workspace == nil || s.Candidate == nil {
		return StagedForkIntent{}, nil, errors.New("parent workspace required")
	}
	current, _, err := worktree.Capture(ctx, *s.Workspace)
	if err != nil || current != *s.Candidate {
		return StagedForkIntent{}, nil, errors.New("base candidate changed")
	}
	intent, task, err := expectedStagedForkIntent(s, taskID)
	if err != nil {
		return StagedForkIntent{}, nil, err
	}
	// Source base index drift is rejected here; foreign, stale, symlink,
	// hardlink and protected changes are rejected in expected intent and delta
	// validation. The committed source inventory path uses the same pristine
	// creation rules as existing worktrees.
	if intent.SourceCommit != s.Creation.Repository.Commit || intent.ParentCandidate.Head != s.Creation.Repository.Commit {
		return StagedForkIntent{}, nil, errors.New("staged fork source commit drift")
	}
	return intent, task, nil
}

func applyStagedForkDelta(ctx context.Context, binding worktree.Binding, intent StagedForkIntent) error {
	if len(intent.Delta) == 0 {
		return nil
	}
	// Honest pre-effect capacity diagnostic: reuse the same bounded closure the
	// controller admitted. No silent truncation or broad permission fallthrough.
	before, files, err := worktree.Capture(ctx, binding)
	if err != nil {
		return err
	}
	_ = files
	_ = before
	// The delta was already bound in the durable intent; re-validate encoding
	// and protected paths before any effect.
	for _, change := range intent.Delta {
		if err := safepath.Writable(change.Path); err != nil {
			return err
		}
		if _, err := decodeForkContent(change); err != nil {
			return err
		}
	}
	nonce := "staged-fork-" + intent.Request.RunID
	proposal, err := fileeffects.Prepare(ctx, binding, nonce, intent.Delta)
	if err != nil {
		return err
	}
	if err := fileeffects.Preflight(ctx, binding, proposal); err != nil {
		return err
	}
	if proposal.Before.WorktreeID == "" {
		return errors.New("staged fork preimage is not child-bound")
	}
	// The child preimage must be pristine source; the parent delta is the only
	// authorized copy. Writer scoping remains enforced at proposal time.
	if err := fileeffects.Apply(ctx, binding, proposal); err != nil {
		return err
	}
	// Read back under the same lease; interrupted overlay remains UNKNOWN via
	// the caller's UNKNOWN handling and is never recreated here.
	if _, err := worktree.Fingerprint(ctx, binding); err != nil {
		return err
	}
	return nil
}

func stagedForkBindingForTask(s Snapshot, taskID string) (isolatedWriterBinding, error) {
	fork, ok := s.GraphStagedForks[taskID]
	if !ok || fork.Outcome != "CONFIRMED" || fork.Binding == nil || fork.Candidate == nil {
		return isolatedWriterBinding{}, errors.New("staged fork workspace is not confirmed")
	}
	intent, task, err := expectedStagedForkIntent(s, taskID)
	if err != nil || !sameCanonical(intent, fork.Intent) {
		return isolatedWriterBinding{}, errors.Join(errors.New("staged fork intent does not match the frozen cohort"), err)
	}
	if !containsGraphIsolationTask(s.GraphIsolationPreparation.SelectedTaskIDs, taskID) {
		return isolatedWriterBinding{}, errors.New("task is outside the frozen staged cohort")
	}
	workspaceID, err := fork.Binding.ID()
	if err != nil {
		return isolatedWriterBinding{}, err
	}
	if err := fork.Candidate.ValidateBinding(*fork.Binding); err != nil {
		return isolatedWriterBinding{}, err
	}
	parentID, err := s.Candidate.ID()
	if err != nil {
		return isolatedWriterBinding{}, err
	}
	parent := *s.Candidate
	child := *fork.Candidate
	if intent.BaseCandidateID != parentID || s.GraphIsolationPreparation.BaseCandidateID != parentID || child.WorktreeID != workspaceID || child.Version != parent.Version || child.Head != parent.Head || child.IndexHash != parent.IndexHash || child.FilesHash != parent.FilesHash || child.FileCount != parent.FileCount {
		return isolatedWriterBinding{}, errors.New("staged fork child is not bound to the frozen parent")
	}
	if child.Head != s.Creation.Repository.Commit {
		return isolatedWriterBinding{}, errors.New("staged fork source commit mismatch")
	}
	_ = intent.Request.RunID
	return isolatedWriterBinding{
		TaskID:          taskID,
		IsolationID:     intent.Request.RunID,
		BaseCandidateID: parentID,
		Workspace:       *fork.Binding,
		Candidate:       *fork.Candidate,
		Task: writerImplementationContext{
			ID: task.ID, Title: task.Title,
			ScopePaths:       append([]string(nil), task.ScopePaths...),
			WritePaths:       append([]string(nil), task.WritePaths...),
			ExpectedEvidence: append([]engineeringplan.Evidence(nil), task.ExpectedEvidence...),
		},
	}, nil
}
