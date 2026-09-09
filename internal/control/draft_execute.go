package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

type draftHost interface {
	ReadBranch(context.Context, string, string) (draftpr.BranchObservation, error)
	Preflight(context.Context, draftpr.Plan) error
	Create(context.Context, draftpr.Plan, effects.Intent, effects.Authorization) (draftpr.Observation, error)
	Reconcile(context.Context, draftpr.Plan) (draftpr.Observation, error)
}

// PrepareDraft freezes text and the currently observed base commit under the
// workspace lease. It neither persists a creation intent nor sends a POST.
func PrepareDraft(ctx context.Context, path, repository, baseRef, title, body string, client *draftpr.Client) (PreparedDraft, error) {
	return prepareDraft(ctx, path, repository, baseRef, title, body, client)
}

func prepareDraft(ctx context.Context, path, repository, baseRef, title, body string, host draftHost) (prepared PreparedDraft, err error) {
	s, err := Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := draftAllowed(s); err != nil {
		return prepared, err
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return prepared, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return prepared, err
	}
	if err := draftAllowed(s); err != nil {
		return prepared, err
	}
	candidate, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil || candidate != *s.Candidate {
		return prepared, errors.Join(errors.New("draft candidate changed"), err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return prepared, err
	}
	p := draftpr.Plan{Version: 1, Nonce: hex.EncodeToString(nonce[:]), Push: s.Push.Intent.Prepared.Plan, PushIntent: s.Push.Intent.Prepared.Intent, Repository: repository, BaseRef: baseRef, BaseCommit: candidate.Head, Title: title, Body: body}
	// Validate destination and text before any host request. This temporary base
	// commit is replaced by the read below before a preview identity is returned.
	if _, err := p.ID(); err != nil {
		return prepared, err
	}
	base, err := host.ReadBranch(ctx, repository, baseRef)
	if err != nil {
		return prepared, err
	}
	p.BaseCommit = base.Commit
	intent, err := p.Intent()
	if err != nil {
		return prepared, err
	}
	if err := host.Preflight(ctx, p); err != nil {
		return prepared, err
	}
	return PreparedDraft{Plan: p, Intent: intent}, nil
}

// ExecuteDraft journals exact authority before calling the host once. It always
// attempts read-only reconciliation afterward, including after a lost response.
// An existing draft intent cannot be repeated through this entry point.
func ExecuteDraft(ctx context.Context, path string, p PreparedDraft, auth effects.Authorization, client *draftpr.Client) (Snapshot, error) {
	return executeDraft(ctx, path, p, auth, client)
}

func executeDraft(ctx context.Context, path string, p PreparedDraft, auth effects.Authorization, host draftHost) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err := validatePreparedDraft(s, p); err != nil {
		return s, err
	}
	if err := auth.Validate(p.Intent); err != nil {
		return s, err
	}
	lease, err := worktree.Acquire(p.Plan.Push.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if err := validatePreparedDraft(s, p); err != nil {
		return s, err
	}
	candidate, err := worktree.Pristine(ctx, p.Plan.Push.Workspace)
	if err != nil || candidate != p.Plan.Push.Candidate {
		return s, errors.Join(errors.New("draft candidate changed before intent"), err)
	}
	if err := Append(path, "draft.intent", DraftIntent{Prepared: p, Authorization: auth}); err != nil {
		return s, err
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	_, executionErr := host.Create(ctx, p.Plan, p.Intent, auth)
	latest, observeErr := observeDraftLocked(context.WithoutCancel(ctx), path, s, host)
	return latest, errors.Join(executionErr, observeErr)
}

// ReconcileDraft observes a pending draft without repeating creation.
func ReconcileDraft(ctx context.Context, path string, client *draftpr.Client) (Snapshot, error) {
	return reconcileDraft(ctx, path, client)
}

func reconcileDraft(ctx context.Context, path string, host draftHost) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Draft == nil || s.Draft.Outcome != "UNKNOWN" {
		return s, errors.New("pending draft required")
	}
	lease, err := worktree.Acquire(s.Draft.Intent.Prepared.Plan.Push.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.Draft == nil || s.Draft.Outcome != "UNKNOWN" {
		return s, errors.New("draft changed before observation")
	}
	return observeDraftLocked(ctx, path, s, host)
}

func observeDraftLocked(ctx context.Context, path string, s Snapshot, host draftHost) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	p := s.Draft.Intent.Prepared
	o := DraftObservation{}
	remote, remoteErr := host.Reconcile(ctx, p.Plan)
	if remoteErr == nil {
		remoteErr = remote.Validate(p.Plan)
		if remoteErr == nil {
			o.Remote = &remote
		}
	}
	candidate, candidateErr := worktree.Pristine(ctx, p.Plan.Push.Workspace)
	if candidateErr == nil && candidate == p.Plan.Push.Candidate {
		o.Candidate = &candidate
	}
	outcome := "UNKNOWN"
	if o.Remote != nil && o.Candidate != nil {
		outcome = "CONFIRMED"
	} else {
		o.Error = "draft final state could not be verified"
	}
	hash, err := canonical.Hash("harness.draft-observation.v1", o)
	if err != nil {
		return s, err
	}
	id, err := p.Intent.ID()
	if err != nil {
		return s, err
	}
	appendErr := Append(path, "draft.observed", DraftReceipt{Receipt: effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, Observation: o})
	latest, readErr := Inspect(path)
	if outcome == "UNKNOWN" {
		return latest, errors.Join(errors.New(o.Error), remoteErr, candidateErr, appendErr, readErr)
	}
	return latest, errors.Join(appendErr, readErr)
}
