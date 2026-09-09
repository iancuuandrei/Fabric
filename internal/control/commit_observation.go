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

// CommitObservation contains a verified final candidate or an unresolved read error.
type CommitObservation struct {
	Candidate *worktree.Candidate `json:"candidate"`
	Error     string              `json:"error"`
}

// CommitReceipt binds the original effect outcome to its final-state observation.
type CommitReceipt struct {
	Receipt     effects.Receipt   `json:"receipt"`
	Observation CommitObservation `json:"observation"`
}

func replayCommitObservation(s *Snapshot, e journal.Event) error {
	if s.Commit == nil || s.Commit.Outcome != "UNKNOWN" || s.State != "COMMITTING" {
		return errors.New("pending commit required for observation")
	}
	var event CommitReceipt
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	hash, err := canonical.Hash("harness.commit-observation.v1", event.Observation)
	if err != nil {
		return err
	}
	if event.Receipt.ObservationHash != hash {
		return errors.New("commit observation hash mismatch")
	}
	outcome, err := effects.Outcome(s.Commit.Intent.Prepared.Intent, &event.Receipt)
	if err != nil {
		return err
	}
	updated := s.Commit.Intent.Prepared.Plan.Workspace
	updated.Request.Source.Commit = s.Commit.Intent.CommitID
	updated.Request.Source.Tree = s.Commit.Intent.TreeID
	if event.Observation.Candidate == nil {
		if outcome != "UNKNOWN" || event.Observation.Error != "commit final state could not be verified" {
			return errors.New("unverified commit outcome mismatch")
		}
	} else {
		candidate := *event.Observation.Candidate
		if _, err := candidate.ID(); err != nil {
			return err
		}
		bindingID, err := updated.ID()
		if err != nil {
			return err
		}
		before := s.Commit.Intent.Prepared.Plan.Candidate
		if outcome != "CONFIRMED" || event.Observation.Error != "" || candidate.WorktreeID != bindingID || candidate.Head != s.Commit.Intent.CommitID || candidate.FilesHash != before.FilesHash || candidate.FileCount != before.FileCount {
			return errors.New("commit final candidate mismatch")
		}
		s.Workspace = &updated
		s.Candidate = &candidate
		s.State = "COMMITTED"
	}
	s.Commit.Observation = &event
	s.Commit.Outcome = outcome
	if recovery := s.Commit.Recovery; recovery != nil {
		id, err := recovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return err
		}
		recovery.Outcome = outcome
		recovery.Receipt = &effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}
	}
	return nil
}

// ReconcileCommit only reads Git/workspace state and records observation. It
// never retries an effect or infers NOT_APPLIED from incomplete final state.
func ReconcileCommit(ctx context.Context, path string) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Commit == nil || s.Commit.Outcome != "UNKNOWN" {
		return s, errors.New("pending commit required")
	}
	lease, err := worktree.Acquire(s.Commit.Intent.Prepared.Plan.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Commit == nil || s.Commit.Outcome != "UNKNOWN" {
		return s, errors.New("commit changed before observation")
	}
	return observeCommitLocked(ctx, path, s)
}

func observeCommitLocked(ctx context.Context, path string, s Snapshot) (Snapshot, error) {
	intent := s.Commit.Intent
	candidate, observeErr := gitlocal.ObserveFinalized(ctx, intent.Prepared.Plan, intent.TreeID, intent.CommitID)
	observation := CommitObservation{}
	outcome := "UNKNOWN"
	if observeErr != nil {
		observation.Error = "commit final state could not be verified"
	} else {
		observation.Candidate = &candidate
		outcome = "CONFIRMED"
	}
	hash, err := canonical.Hash("harness.commit-observation.v1", observation)
	if err != nil {
		return s, err
	}
	intentID, err := intent.Prepared.Intent.ID()
	if err != nil {
		return s, err
	}
	event := CommitReceipt{Receipt: effects.Receipt{Version: 1, IntentID: intentID, Outcome: outcome, ObservationHash: hash}, Observation: observation}
	appendErr := Append(path, "commit.observed", event)
	latest, readErr := Inspect(path)
	return latest, errors.Join(observeErr, appendErr, readErr)
}
