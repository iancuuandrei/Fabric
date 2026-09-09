package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PreparedCommitRecovery freezes a partial-state plan and its new approval target.
type PreparedCommitRecovery struct {
	Plan   gitlocal.RecoveryPlan `json:"plan"`
	Intent effects.Intent        `json:"intent"`
}

// CommitRecoveryIntent persists recovery inputs and authorization before writes.
type CommitRecoveryIntent struct {
	Prepared      PreparedCommitRecovery `json:"prepared"`
	Authorization effects.Authorization  `json:"authorization"`
}

// CommitRecoveryState binds a recovery attempt to the shared final observation.
type CommitRecoveryState struct {
	Intent  CommitRecoveryIntent `json:"intent"`
	Outcome string               `json:"outcome"`
	Receipt *effects.Receipt     `json:"receipt,omitempty"`
}

func commitRecoveryAllowed(s Snapshot) error {
	if s.Commit == nil || s.Commit.Outcome != "UNKNOWN" {
		return errors.New("pending commit required for recovery")
	}
	if s.Commit.LeaseRecovery != nil && s.Commit.LeaseRecovery.Outcome != "CONFIRMED" {
		return errors.New("unresolved lease recovery blocks commit recovery")
	}
	return nil
}

func validateCommitRecovery(s Snapshot, p PreparedCommitRecovery) error {
	if err := commitRecoveryAllowed(s); err != nil {
		return err
	}
	previous := ""
	if s.Commit.Recovery != nil {
		var err error
		previous, err = s.Commit.Recovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return err
		}
	}
	if p.Plan.PreviousIntentID != previous {
		return errors.New("commit recovery predecessor mismatch")
	}
	expected, err := p.Plan.Intent()
	if err != nil {
		return err
	}
	original := s.Commit.Intent
	id, err := original.Prepared.Plan.ID()
	if err != nil {
		return err
	}
	providedID, err := p.Plan.Commit.ID()
	if err != nil {
		return err
	}
	if expected != p.Intent || p.Plan.OriginalIntent != original.Prepared.Intent || id != providedID || p.Plan.TreeID != original.TreeID || p.Plan.CommitID != original.CommitID {
		return errors.New("commit recovery substitution")
	}
	return nil
}

// PrepareCommitRecovery observes a recognized partial state under lease without mutation.
func PrepareCommitRecovery(ctx context.Context, path, evidence string, stopped bool) (prepared PreparedCommitRecovery, err error) {
	s, err := Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := commitRecoveryAllowed(s); err != nil {
		return prepared, err
	}
	lease, err := worktree.Acquire(s.Commit.Intent.Prepared.Plan.Workspace.Request)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := commitRecoveryAllowed(s); err != nil {
		return prepared, err
	}
	original := s.Commit.Intent
	before, err := gitlocal.InspectRecovery(ctx, original.Prepared.Plan, original.TreeID, original.CommitID)
	if err != nil {
		return prepared, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return prepared, err
	}
	plan := gitlocal.RecoveryPlan{Version: 1, Commit: original.Prepared.Plan, OriginalIntent: original.Prepared.Intent, TreeID: original.TreeID, CommitID: original.CommitID, Before: before, Nonce: hex.EncodeToString(nonce[:]), Evidence: evidence, WorkloadsStopped: stopped}
	if s.Commit.Recovery != nil {
		plan.PreviousIntentID, err = s.Commit.Recovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return prepared, err
		}
	}
	intent, err := plan.Intent()
	return PreparedCommitRecovery{Plan: plan, Intent: intent}, err
}

func replayCommitRecovery(s *Snapshot, e journal.Event) error {
	var event CommitRecoveryIntent
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	if err := validateCommitRecovery(*s, event.Prepared); err != nil {
		return err
	}
	if err := event.Authorization.Validate(event.Prepared.Intent); err != nil {
		return err
	}
	s.Commit.Recovery = &CommitRecoveryState{Intent: event, Outcome: "UNKNOWN"}
	return nil
}

// RecoverCommit persists a newly authorized intent, executes once and observes afterward.
// Existing recovery intents cannot be retried through this entry point.
func RecoverCommit(ctx context.Context, path string, p PreparedCommitRecovery, auth effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err := validateCommitRecovery(s, p); err != nil {
		return s, err
	}
	if err := auth.Validate(p.Intent); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(p.Plan.Commit.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if err := validateCommitRecovery(s, p); err != nil {
		return s, err
	}
	before, err := gitlocal.InspectRecovery(ctx, p.Plan.Commit, p.Plan.TreeID, p.Plan.CommitID)
	if err != nil {
		return s, err
	}
	if before != p.Plan.Before {
		return s, errors.New("commit recovery preview is stale")
	}
	if err := Append(path, "commit.recovery-intent", CommitRecoveryIntent{Prepared: p, Authorization: auth}); err != nil {
		return s, err
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	_, recoveryErr := gitlocal.Recover(ctx, p.Plan, p.Intent, auth)
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	latest, observeErr := observeCommitLocked(observeCtx, path, s)
	return latest, errors.Join(recoveryErr, observeErr)
}
