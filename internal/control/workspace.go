package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/worktree"
)

// WorkspaceReceipt binds the actual admitted workspace and pristine candidate.
// Reconciliation uses the same observation contract as initial confirmation.
type WorkspaceReceipt struct {
	Binding   worktree.Binding   `json:"binding"`
	Candidate worktree.Candidate `json:"candidate"`
}

// StartWorkspace persists exact intent before creating a worktree. A pending
// intent is UNKNOWN and blocks automatic recreation. It holds the writer lease
// until receipt persistence and validates state again after acquiring the lease.
func StartWorkspace(ctx context.Context, journalPath string) (snapshot Snapshot, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if s.State != "IMPLEMENTING" {
		return s, errors.New("approved implementation required")
	}
	r, err := worktree.Prepare(s.RunID, s.Creation.Repository)
	if err != nil {
		return s, err
	}
	r.CandidateIdentity = s.Creation.Config.CandidateIdentity
	r.ControllerStateRoot, err = controllerNamespace(s.Creation)
	if err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(r)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if s.Workspace != nil {
		current, err := worktree.Fingerprint(ctx, *s.Workspace)
		if err != nil {
			return s, err
		}
		if s.Candidate == nil || current != *s.Candidate {
			return s, errors.New("workspace candidate changed")
		}
		return s, nil
	}
	if s.WorkspaceIntent != nil {
		return s, errors.New("workspace outcome UNKNOWN; explicit reconciliation required")
	}
	if err = Append(journalPath, "workspace.intent", r); err != nil {
		return s, err
	}
	b, err := worktree.Create(ctx, r)
	if err != nil {
		return s, err
	}
	candidate, err := worktree.Pristine(ctx, b)
	if err != nil {
		return s, err
	}
	if err = Append(journalPath, "workspace.confirmed", WorkspaceReceipt{b, candidate}); err != nil {
		return s, err
	}
	return observeCandidateBaseline(ctx, journalPath)
}

// ReconcileWorkspace confirms only a registered, pristine workspace matching a
// pending intent. Partial, absent or changed state remains UNKNOWN without retry.
// A stale writer lease must be reviewed separately; this does not steal locks.
func ReconcileWorkspace(ctx context.Context, journalPath string) (snapshot Snapshot, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if s.WorkspaceIntent == nil || s.Workspace != nil {
		return s, errors.New("no pending workspace intent")
	}
	lease, err := worktree.Acquire(*s.WorkspaceIntent)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if s.WorkspaceIntent == nil || s.Workspace != nil {
		return s, errors.New("workspace reconciliation state changed")
	}
	b, err := worktree.Observe(ctx, *s.WorkspaceIntent)
	if err != nil {
		return s, err
	}
	candidate, err := worktree.Pristine(ctx, b)
	if err != nil {
		return s, err
	}
	if err = Append(journalPath, "workspace.confirmed", WorkspaceReceipt{b, candidate}); err != nil {
		return s, err
	}
	return observeCandidateBaseline(ctx, journalPath)
}
