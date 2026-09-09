package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/worktree"
)

// PreparedFiles is an immutable proposal and its exact effect identity. It grants
// no write authority; ApplyFiles requires separate explicit authorization.
type PreparedFiles struct {
	Proposal fileeffects.Proposal `json:"proposal"`
	Intent   effects.Intent       `json:"intent"`
}

// FileIntent is persisted before mutation, binding proposal and operator approval.
type FileIntent struct {
	Prepared      PreparedFiles         `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// FileObservation records actual candidate evidence even when execution failed.
// A missing candidate requires an error and can only classify as UNKNOWN.
type FileObservation struct {
	Candidate *worktree.Candidate `json:"candidate"`
	Error     string              `json:"error"`
}

// FileReceipt binds effect outcome to the entire observed source state.
type FileReceipt struct {
	Receipt     effects.Receipt `json:"receipt"`
	Observation FileObservation `json:"observation"`
}

func preparedFiles(s Snapshot, p fileeffects.Proposal) (PreparedFiles, error) {
	if err := p.Validate(); err != nil {
		return PreparedFiles{}, err
	}
	if s.Workspace == nil || s.Candidate == nil || *s.Candidate != p.Before {
		return PreparedFiles{}, errors.New("proposal candidate is not current admitted state")
	}
	h, err := p.ID()
	if err != nil {
		return PreparedFiles{}, err
	}
	repo, err := s.Creation.Repository.ID()
	if err != nil {
		return PreparedFiles{}, err
	}
	return PreparedFiles{p, effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "filesystem", InputHash: h}}, nil
}

func filesAllowed(s Snapshot) error {
	if (s.State != "IMPLEMENTING" && s.State != "REPAIRING") || s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return errors.New("admitted writer workspace required")
	}
	if s.FileIntent != nil && s.FileOutcome == "UNKNOWN" {
		return errors.New("file effect UNKNOWN; reconcile before another effect")
	}
	return nil
}

// PrepareFiles captures a leased candidate and predicts exact file changes. It
// returns an approval target without changing source or recording an intent.
func PrepareFiles(ctx context.Context, journalPath string, changes []fileeffects.Change) (prepared PreparedFiles, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return prepared, err
	}
	if err = filesAllowed(s); err != nil {
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
	if err = filesAllowed(s); err != nil {
		return prepared, err
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return prepared, err
	}
	p, err := fileeffects.Prepare(ctx, *s.Workspace, hex.EncodeToString(nonce), changes)
	if err != nil {
		return prepared, err
	}
	if err = fileeffects.Preflight(ctx, *s.Workspace, p); err != nil {
		return prepared, err
	}
	return preparedFiles(s, p)
}

// ApplyFiles validates explicit authorization and current state under the writer
// lease, persists intent, performs changes, and records whole-state observation.
// Even an execution error is followed by an observation attempt; it never retries.
func ApplyFiles(ctx context.Context, journalPath string, p PreparedFiles, a effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err = filesAllowed(s); err != nil {
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
	if err = filesAllowed(s); err != nil {
		return s, err
	}
	expected, err := preparedFiles(s, p.Proposal)
	if err != nil {
		return s, err
	}
	if expected.Intent != p.Intent {
		return s, errors.New("file intent substitution")
	}
	if err = a.Validate(p.Intent); err != nil {
		return s, err
	}
	if s.Creation.Config.CandidateIdentity == "semantic-index-v2" {
		if _, err := observeCandidate(ctx, journalPath, s); err != nil {
			return s, err
		}
	}
	if err = fileeffects.Preflight(ctx, *s.Workspace, p.Proposal); err != nil {
		return s, err
	}
	if err = Append(journalPath, "files.intent", FileIntent{p, a}); err != nil {
		return s, err
	}
	applyErr := fileeffects.Apply(ctx, *s.Workspace, p.Proposal)
	observed, observeErr := observeFiles(ctx, *s.Workspace, p, applyErr)
	appendErr := Append(journalPath, "files.observed", observed)
	latest, readErr := Inspect(journalPath)
	if applyErr == nil && observeErr == nil && appendErr == nil && readErr == nil && latest.FileOutcome == "CONFIRMED" {
		latest, readErr = observeCandidateBaseline(ctx, journalPath)
	}
	return latest, errors.Join(applyErr, observeErr, appendErr, readErr)
}

func observeFiles(ctx context.Context, b worktree.Binding, p PreparedFiles, executionErr error) (FileReceipt, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	candidate, err := worktree.Fingerprint(ctx, b)
	observation := FileObservation{}
	if err == nil {
		observation.Candidate = &candidate
	}
	if combined := errors.Join(executionErr, err); combined != nil {
		observation.Error = strings.ToValidUTF8(combined.Error(), "�")
		if len(observation.Error) > 2048 {
			observation.Error = observation.Error[:2048]
			for !utf8.ValidString(observation.Error) {
				observation.Error = observation.Error[:len(observation.Error)-1]
			}
		}
	}
	h, hashErr := canonical.Hash("harness.file-observation.v1", observation)
	id, idErr := p.Intent.ID()
	receipt := effects.Receipt{Version: 1, IntentID: id, Outcome: fileeffects.Classify(p.Proposal, observation.Candidate), ObservationHash: h}
	return FileReceipt{receipt, observation}, errors.Join(err, hashErr, idErr)
}

// ReconcileFiles only observes a pending/UNKNOWN effect; it never completes,
// retries or rolls back writes. Exact before/after states close uncertainty.
func ReconcileFiles(ctx context.Context, journalPath string) (snapshot Snapshot, err error) {
	s, err := Inspect(journalPath)
	if err != nil {
		return s, err
	}
	if s.FileIntent == nil || s.FileOutcome != "UNKNOWN" || s.Workspace == nil {
		return s, errors.New("no unknown file effect")
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
	if s.FileIntent == nil || s.FileOutcome != "UNKNOWN" {
		return s, errors.New("file reconciliation state changed")
	}
	active := s.FileIntent.Prepared
	active.Intent = activeFileIntent(s)
	receipt, observeErr := observeFiles(ctx, *s.Workspace, active, nil)
	appendErr := Append(journalPath, "files.observed", receipt)
	latest, readErr := Inspect(journalPath)
	if receipt.Receipt.Outcome == "UNKNOWN" {
		observeErr = errors.Join(observeErr, errors.New("partial or unexpected state remains UNKNOWN"))
	}
	return latest, errors.Join(observeErr, appendErr, readErr)
}
