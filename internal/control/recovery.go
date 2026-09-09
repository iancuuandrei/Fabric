package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/worktree"
)

// PreparedRecovery is a fresh approval target for an observed partial file effect.
// It does not inherit the original operator's authorization for a new attempt.
type PreparedRecovery struct {
	Recovery fileeffects.Recovery `json:"recovery"`
	Intent   effects.Intent       `json:"intent"`
}

// RecoveryIntent binds an explicit recovery approval persisted before cleanup/write.
type RecoveryIntent struct {
	Prepared      PreparedRecovery      `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

func recoveryAllowed(s Snapshot) error {
	if s.FileIntent == nil || s.FileOutcome != "UNKNOWN" || s.Workspace == nil {
		return errors.New("unknown file effect required for recovery")
	}
	return nil
}

func preparedRecovery(s Snapshot, r fileeffects.Recovery) (PreparedRecovery, error) {
	if err := recoveryAllowed(s); err != nil {
		return PreparedRecovery{}, err
	}
	h, err := r.ID(s.FileIntent.Prepared.Proposal)
	if err != nil {
		return PreparedRecovery{}, err
	}
	intent := s.FileIntent.Prepared.Intent
	intent.InputHash = h
	return PreparedRecovery{r, intent}, nil
}

func activeFileIntent(s Snapshot) effects.Intent {
	if s.FileRecovery != nil {
		return s.FileRecovery.Prepared.Intent
	}
	return s.FileIntent.Prepared.Intent
}

// PrepareFileRecovery captures partial state under the writer lease and returns
// a new immutable approval target. It neither removes temporary files nor writes.
func PrepareFileRecovery(ctx context.Context, journalPath string) (prepared PreparedRecovery, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return prepared, err
	}
	if err = recoveryAllowed(s); err != nil {
		return prepared, err
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(journalPath)
	if err != nil {
		return prepared, err
	}
	if err = recoveryAllowed(s); err != nil {
		return prepared, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return prepared, err
	}
	r, err := fileeffects.PrepareRecovery(ctx, *s.Workspace, s.FileIntent.Prepared.Proposal, hex.EncodeToString(nonce))
	if err != nil {
		return prepared, err
	}
	return preparedRecovery(s, r)
}

// RecoverFiles records fresh reconciliation evidence and a separately approved
// recovery intent before continuing only recognized remaining writes. Any new
// interruption is observed and remains subject to the same UNKNOWN rules.
func RecoverFiles(ctx context.Context, journalPath string, p PreparedRecovery, a effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err = recoveryAllowed(s); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if err = recoveryAllowed(s); err != nil {
		return s, err
	}
	expected, err := preparedRecovery(s, p.Recovery)
	if err != nil {
		return s, err
	}
	if expected.Intent != p.Intent {
		return s, errors.New("recovery intent substitution")
	}
	if err = a.Validate(p.Intent); err != nil {
		return s, err
	}
	if err = s.FileIntent.Prepared.Proposal.AdmitHost(); err != nil {
		return s, err
	}
	observed, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return s, err
	}
	if observed != p.Recovery.Observed {
		return s, errors.New("recovery preview is stale")
	}
	prior := s.FileIntent.Prepared
	prior.Intent = activeFileIntent(s)
	receipt, observeErr := observeFiles(ctx, *s.Workspace, prior, nil)
	if observeErr != nil {
		return s, observeErr
	}
	if receipt.Observation.Candidate == nil || *receipt.Observation.Candidate != p.Recovery.Observed {
		return s, errors.New("recovery state changed during reconciliation")
	}
	if err = Append(journalPath, "files.observed", receipt); err != nil {
		return s, err
	}
	if err = Append(journalPath, "files.recovery-intent", RecoveryIntent{p, a}); err != nil {
		return s, err
	}
	recoverErr := fileeffects.Recover(ctx, *s.Workspace, s.FileIntent.Prepared.Proposal, p.Recovery)
	attempt := s.FileIntent.Prepared
	attempt.Intent = p.Intent
	result, observeErr := observeFiles(ctx, *s.Workspace, attempt, recoverErr)
	appendErr := Append(journalPath, "files.observed", result)
	latest, readErr := Inspect(journalPath)
	return latest, errors.Join(recoverErr, observeErr, appendErr, readErr)
}
