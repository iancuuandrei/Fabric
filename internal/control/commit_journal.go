package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// CommitIntent fixes execution inputs before any object or ref mutation.
// Derived object IDs are predictions, not evidence of stored objects.
type CommitIntent struct {
	Prepared      PreparedCommit        `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
	TreeID        string                `json:"tree_id"`
	CommitID      string                `json:"commit_id"`
}

// CommitState replays the original attempt and separately authorized recoveries.
type CommitState struct {
	Recovery      *CommitRecoveryState `json:"recovery,omitempty"`
	LeaseRecovery *CommitLeaseState    `json:"lease_recovery,omitempty"`
	Intent        CommitIntent         `json:"intent"`
	Outcome       string               `json:"outcome"`
	Observation   *CommitReceipt       `json:"observation,omitempty"`
}

func validatePreparedCommit(s Snapshot, p PreparedCommit) error {
	if s.State != "READY" || s.ApprovedBy == "" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" || s.Commit != nil {
		return errors.New("ready candidate required for new commit intent")
	}
	if p.Plan.Workspace != *s.Workspace || p.Plan.Candidate != *s.Candidate {
		return errors.New("commit plan workspace/candidate substitution")
	}
	id, err := p.Plan.ID()
	if err != nil {
		return err
	}
	repo, err := s.Creation.Repository.ID()
	if err != nil {
		return err
	}
	expected := effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "commit", InputHash: id}
	if p.Intent != expected {
		return errors.New("commit effect substitution")
	}
	return nil
}

func replayCommitIntent(s *Snapshot, e journal.Event) error {
	var event CommitIntent
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	if err := validatePreparedCommit(*s, event.Prepared); err != nil {
		return err
	}
	if err := event.Authorization.Validate(event.Prepared.Intent); err != nil {
		return err
	}
	_, id, err := gitlocal.CommitObject(event.Prepared.Plan, event.TreeID)
	if err != nil {
		return err
	}
	if id != event.CommitID {
		return errors.New("predicted commit identity mismatch")
	}
	s.Commit = &CommitState{Intent: event, Outcome: "UNKNOWN"}
	s.State = "COMMITTING"
	return nil
}

// RecordCommitIntent refreshes the candidate under lease, derives object IDs,
// and durably records exact authorization. It never writes objects or refs.
// A recorded intent must be reconciled; calling this again is not a retry path.
func RecordCommitIntent(ctx context.Context, path string, p PreparedCommit, auth effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
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
	objects, err := gitlocal.ReadCandidateObjects(ctx, p.Plan)
	if err != nil {
		return s, err
	}
	event := CommitIntent{Prepared: p, Authorization: auth, TreeID: objects.Trees[len(objects.Trees)-1].ObjectID, CommitID: objects.CommitID}
	if err := Append(path, "commit.intent", event); err != nil {
		return s, err
	}
	return Inspect(path)
}
