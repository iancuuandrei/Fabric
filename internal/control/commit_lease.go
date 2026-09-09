package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PreparedCommitLease is the exact approval target for abandoned-lease recovery.
type PreparedCommitLease struct {
	Plan   worktree.LeaseRecovery `json:"plan"`
	Intent effects.Intent         `json:"intent"`
}

// CommitLeaseIntent records authority before attempting kernel lease adoption.
type CommitLeaseIntent struct {
	Prepared      PreparedCommitLease   `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// CommitLeaseState distinguishes release confirmation from commit completion.
type CommitLeaseState struct {
	Intent  CommitLeaseIntent `json:"intent"`
	Outcome string            `json:"outcome"`
	Receipt *effects.Receipt  `json:"receipt,omitempty"`
}

// CommitLeaseObservation binds a receipt to actual lease-release readback.
type CommitLeaseObservation struct {
	Receipt  effects.Receipt `json:"receipt"`
	Released bool            `json:"released"`
}

func expectedCommitLease(s Snapshot, plan worktree.LeaseRecovery) (effects.Intent, error) {
	if s.Commit == nil || (s.Commit.Outcome != "UNKNOWN" && (s.Commit.LeaseRecovery == nil || s.Commit.LeaseRecovery.Outcome != "UNKNOWN")) {
		return effects.Intent{}, errors.New("pending commit or unresolved lease recovery required")
	}
	previous := ""
	if s.Commit.LeaseRecovery != nil {
		var err error
		previous, err = s.Commit.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return effects.Intent{}, err
		}
	}
	if plan.PreviousIntentID != previous {
		return effects.Intent{}, errors.New("lease recovery predecessor mismatch")
	}
	if plan.Request != s.Commit.Intent.Prepared.Plan.Workspace.Request {
		return effects.Intent{}, errors.New("lease recovery workspace mismatch")
	}
	id, err := plan.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	repo, err := s.Creation.Repository.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	return effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "lease_recovery", InputHash: id}, nil
}

// PrepareCommitLeaseRecovery freezes token and quiescence evidence without adoption.
func PrepareCommitLeaseRecovery(path, evidence string, stopped bool) (PreparedCommitLease, error) {
	s, err := Inspect(path)
	if err != nil {
		return PreparedCommitLease{}, err
	}
	if s.Commit == nil {
		return PreparedCommitLease{}, errors.New("pending commit required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PreparedCommitLease{}, err
	}
	plan, err := worktree.PrepareLeaseRecovery(s.Commit.Intent.Prepared.Plan.Workspace.Request, hex.EncodeToString(nonce[:]), evidence, stopped)
	if err != nil {
		return PreparedCommitLease{}, err
	}
	if s.Commit.LeaseRecovery != nil {
		plan.PreviousIntentID, err = s.Commit.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return PreparedCommitLease{}, err
		}
	}
	intent, err := expectedCommitLease(s, plan)
	return PreparedCommitLease{Plan: plan, Intent: intent}, err
}

func replayCommitLease(s *Snapshot, e journal.Event) error {
	if e.Kind == "commit.lease-intent" {
		var event CommitLeaseIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		expected, err := expectedCommitLease(*s, event.Prepared.Plan)
		if err != nil {
			return err
		}
		if expected != event.Prepared.Intent {
			return errors.New("lease recovery effect mismatch")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		s.Commit.LeaseRecovery = &CommitLeaseState{Intent: event, Outcome: "UNKNOWN"}
		return nil
	}
	if s.Commit == nil || s.Commit.LeaseRecovery == nil || s.Commit.LeaseRecovery.Outcome != "UNKNOWN" {
		return errors.New("pending lease recovery required")
	}
	var event CommitLeaseObservation
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	hash, err := canonical.Hash("harness.commit-lease-observation.v1", event.Released)
	if err != nil {
		return err
	}
	outcome, err := effects.Outcome(s.Commit.LeaseRecovery.Intent.Prepared.Intent, &event.Receipt)
	if err != nil {
		return err
	}
	if hash != event.Receipt.ObservationHash || (event.Released && outcome != "CONFIRMED") || (!event.Released && outcome != "UNKNOWN") {
		return errors.New("lease recovery observation mismatch")
	}
	s.Commit.LeaseRecovery.Outcome, s.Commit.LeaseRecovery.Receipt = outcome, &event.Receipt
	return nil
}

// RecoverCommitLease records recovery authority before adopting the old lease,
// observes the commit without retrying its writes, then releases and observes the
// lease. A failed/interrupted recovery is not automatically retried.
func RecoverCommitLease(ctx context.Context, path string, p PreparedCommitLease, auth effects.Authorization) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	expected, err := expectedCommitLease(s, p.Plan)
	if err != nil {
		return s, err
	}
	if expected != p.Intent {
		return s, errors.New("lease recovery preview substitution")
	}
	if err := auth.Validate(expected); err != nil {
		return s, err
	}
	if err := Append(path, "commit.lease-intent", CommitLeaseIntent{Prepared: p, Authorization: auth}); err != nil {
		return s, err
	}
	lease, adoptErr := worktree.AdoptLease(p.Plan, p.Intent, auth)
	var observeErr, closeErr error
	released := false
	if adoptErr == nil {
		s, observeErr = Inspect(path)
		if observeErr == nil && s.Commit.Outcome == "UNKNOWN" {
			s, observeErr = observeCommitLocked(ctx, path, s)
		}
		closeErr = lease.Close()
		lock := worktree.LeasePath(p.Plan.Request)
		_, statErr := os.Lstat(lock)
		released = closeErr == nil && os.IsNotExist(statErr)
		if !released && closeErr == nil {
			closeErr = errors.New("lease release could not be verified")
		}
	}
	id, idErr := p.Intent.ID()
	hash, hashErr := canonical.Hash("harness.commit-lease-observation.v1", released)
	outcome := "UNKNOWN"
	if released {
		outcome = "CONFIRMED"
	}
	event := CommitLeaseObservation{Receipt: effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, Released: released}
	appendErr := Append(path, "commit.lease-observed", event)
	latest, readErr := Inspect(path)
	return latest, errors.Join(adoptErr, observeErr, closeErr, idErr, hashErr, appendErr, readErr)
}
