package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/draftpr"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PreparedDraftLease is the exact approval target for abandoned-lease recovery.
type PreparedDraftLease struct {
	Plan   worktree.LeaseRecovery `json:"plan"`
	Intent effects.Intent         `json:"intent"`
}

// DraftLeaseIntent records authority before attempting kernel lease adoption.
type DraftLeaseIntent struct {
	Prepared      PreparedDraftLease    `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// DraftLeaseState distinguishes release confirmation from draft completion.
type DraftLeaseState struct {
	Intent  DraftLeaseIntent `json:"intent"`
	Outcome string           `json:"outcome"`
	Receipt *effects.Receipt `json:"receipt,omitempty"`
}

// DraftLeaseObservation binds a receipt to actual lease-release readback.
type DraftLeaseObservation struct {
	Receipt  effects.Receipt `json:"receipt"`
	Released bool            `json:"released"`
}

func expectedDraftLease(s Snapshot, plan worktree.LeaseRecovery) (effects.Intent, error) {
	if s.Draft == nil || (s.Draft.Outcome != "UNKNOWN" && (s.Draft.LeaseRecovery == nil || s.Draft.LeaseRecovery.Outcome != "UNKNOWN")) {
		return effects.Intent{}, errors.New("pending draft or unresolved lease recovery required")
	}
	previous := ""
	if s.Draft.LeaseRecovery != nil {
		var err error
		previous, err = s.Draft.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return effects.Intent{}, err
		}
	}
	if plan.PreviousIntentID != previous {
		return effects.Intent{}, errors.New("lease recovery predecessor mismatch")
	}
	if plan.Request != s.Draft.Intent.Prepared.Plan.Push.Workspace.Request {
		return effects.Intent{}, errors.New("lease recovery workspace mismatch")
	}
	id, err := plan.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	// The draft lease binds the post-commit workspace source, whose commit/tree
	// differ from the original run creation. The request above is exact-bound.
	repo, err := plan.Request.Source.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	return effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "lease_recovery", InputHash: id}, nil
}

// PrepareDraftLeaseRecovery freezes token and quiescence evidence without adoption.
func PrepareDraftLeaseRecovery(path, evidence string, stopped bool) (PreparedDraftLease, error) {
	s, err := Inspect(path)
	if err != nil {
		return PreparedDraftLease{}, err
	}
	if s.Draft == nil {
		return PreparedDraftLease{}, errors.New("pending draft required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PreparedDraftLease{}, err
	}
	plan, err := worktree.PrepareLeaseRecovery(s.Draft.Intent.Prepared.Plan.Push.Workspace.Request, hex.EncodeToString(nonce[:]), evidence, stopped)
	if err != nil {
		return PreparedDraftLease{}, err
	}
	if s.Draft.LeaseRecovery != nil {
		plan.PreviousIntentID, err = s.Draft.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return PreparedDraftLease{}, err
		}
	}
	intent, err := expectedDraftLease(s, plan)
	return PreparedDraftLease{Plan: plan, Intent: intent}, err
}

func replayDraftLease(s *Snapshot, e journal.Event) error {
	if e.Kind == "draft.lease-intent" {
		var event DraftLeaseIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		expected, err := expectedDraftLease(*s, event.Prepared.Plan)
		if err != nil {
			return err
		}
		if expected != event.Prepared.Intent {
			return errors.New("lease recovery effect mismatch")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		s.Draft.LeaseRecovery = &DraftLeaseState{Intent: event, Outcome: "UNKNOWN"}
		return nil
	}
	if s.Draft == nil || s.Draft.LeaseRecovery == nil || s.Draft.LeaseRecovery.Outcome != "UNKNOWN" {
		return errors.New("pending lease recovery required")
	}
	var event DraftLeaseObservation
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	hash, err := canonical.Hash("harness.draft-lease-observation.v1", event.Released)
	if err != nil {
		return err
	}
	outcome, err := effects.Outcome(s.Draft.LeaseRecovery.Intent.Prepared.Intent, &event.Receipt)
	if err != nil {
		return err
	}
	if hash != event.Receipt.ObservationHash || (event.Released && outcome != "CONFIRMED") || (!event.Released && outcome != "UNKNOWN") {
		return errors.New("lease recovery observation mismatch")
	}
	s.Draft.LeaseRecovery.Outcome, s.Draft.LeaseRecovery.Receipt = outcome, &event.Receipt
	return nil
}

// RecoverDraftLease records recovery authority before adopting the old lease,
// observes the draft without retrying its writes, then releases and observes the
// lease. A failed/interrupted recovery is not automatically retried.
func RecoverDraftLease(ctx context.Context, path string, p PreparedDraftLease, auth effects.Authorization, client *draftpr.Client) (Snapshot, error) {
	return recoverDraftLease(ctx, path, p, auth, client)
}
func recoverDraftLease(ctx context.Context, path string, p PreparedDraftLease, auth effects.Authorization, host draftHost) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	expected, err := expectedDraftLease(s, p.Plan)
	if err != nil {
		return s, err
	}
	if expected != p.Intent {
		return s, errors.New("lease recovery preview substitution")
	}
	if err := auth.Validate(expected); err != nil {
		return s, err
	}
	if err := Append(path, "draft.lease-intent", DraftLeaseIntent{Prepared: p, Authorization: auth}); err != nil {
		return s, err
	}
	lease, adoptErr := worktree.AdoptLease(p.Plan, p.Intent, auth)
	var observeErr, closeErr error
	released := false
	if adoptErr == nil {
		s, observeErr = Inspect(path)
		if observeErr == nil && s.Draft.Outcome == "UNKNOWN" {
			s, observeErr = observeDraftLocked(ctx, path, s, host)
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
	hash, hashErr := canonical.Hash("harness.draft-lease-observation.v1", released)
	outcome := "UNKNOWN"
	if released {
		outcome = "CONFIRMED"
	}
	event := DraftLeaseObservation{Receipt: effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, Released: released}
	appendErr := Append(path, "draft.lease-observed", event)
	latest, readErr := Inspect(path)
	return latest, errors.Join(adoptErr, observeErr, closeErr, idErr, hashErr, appendErr, readErr)
}
