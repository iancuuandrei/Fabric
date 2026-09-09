package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/worktree"
)

// PreparedPushLease is the exact approval target for abandoned-lease recovery.
type PreparedPushLease struct {
	Plan   worktree.LeaseRecovery `json:"plan"`
	Intent effects.Intent         `json:"intent"`
}

// PushLeaseIntent records authority before attempting kernel lease adoption.
type PushLeaseIntent struct {
	Prepared      PreparedPushLease     `json:"prepared"`
	Authorization effects.Authorization `json:"authorization"`
}

// PushLeaseState distinguishes release confirmation from push completion.
type PushLeaseState struct {
	Intent  PushLeaseIntent  `json:"intent"`
	Outcome string           `json:"outcome"`
	Receipt *effects.Receipt `json:"receipt,omitempty"`
}

// PushLeaseObservation binds a receipt to actual lease-release readback.
type PushLeaseObservation struct {
	Receipt  effects.Receipt `json:"receipt"`
	Released bool            `json:"released"`
}

func expectedPushLease(s Snapshot, plan worktree.LeaseRecovery) (effects.Intent, error) {
	if s.Push == nil || (s.Push.Outcome != "UNKNOWN" && (s.Push.LeaseRecovery == nil || s.Push.LeaseRecovery.Outcome != "UNKNOWN")) {
		return effects.Intent{}, errors.New("pending push or unresolved lease recovery required")
	}
	previous := ""
	if s.Push.LeaseRecovery != nil {
		var err error
		previous, err = s.Push.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return effects.Intent{}, err
		}
	}
	if plan.PreviousIntentID != previous {
		return effects.Intent{}, errors.New("lease recovery predecessor mismatch")
	}
	if plan.Request != s.Push.Intent.Prepared.Plan.Workspace.Request {
		return effects.Intent{}, errors.New("lease recovery workspace mismatch")
	}
	id, err := plan.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	// The push lease binds the post-commit workspace source, whose commit/tree
	// differ from the original run creation. The request above is exact-bound.
	repo, err := plan.Request.Source.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	return effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "lease_recovery", InputHash: id}, nil
}

// PreparePushLeaseRecovery freezes token and quiescence evidence without adoption.
func PreparePushLeaseRecovery(path, evidence string, stopped bool) (PreparedPushLease, error) {
	s, err := Inspect(path)
	if err != nil {
		return PreparedPushLease{}, err
	}
	if s.Push == nil {
		return PreparedPushLease{}, errors.New("pending push required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return PreparedPushLease{}, err
	}
	plan, err := worktree.PrepareLeaseRecovery(s.Push.Intent.Prepared.Plan.Workspace.Request, hex.EncodeToString(nonce[:]), evidence, stopped)
	if err != nil {
		return PreparedPushLease{}, err
	}
	if s.Push.LeaseRecovery != nil {
		plan.PreviousIntentID, err = s.Push.LeaseRecovery.Intent.Prepared.Intent.ID()
		if err != nil {
			return PreparedPushLease{}, err
		}
	}
	intent, err := expectedPushLease(s, plan)
	return PreparedPushLease{Plan: plan, Intent: intent}, err
}

func replayPushLease(s *Snapshot, e journal.Event) error {
	if e.Kind == "push.lease-intent" {
		var event PushLeaseIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		expected, err := expectedPushLease(*s, event.Prepared.Plan)
		if err != nil {
			return err
		}
		if expected != event.Prepared.Intent {
			return errors.New("lease recovery effect mismatch")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		s.Push.LeaseRecovery = &PushLeaseState{Intent: event, Outcome: "UNKNOWN"}
		return nil
	}
	if s.Push == nil || s.Push.LeaseRecovery == nil || s.Push.LeaseRecovery.Outcome != "UNKNOWN" {
		return errors.New("pending lease recovery required")
	}
	var event PushLeaseObservation
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	hash, err := canonical.Hash("harness.push-lease-observation.v1", event.Released)
	if err != nil {
		return err
	}
	outcome, err := effects.Outcome(s.Push.LeaseRecovery.Intent.Prepared.Intent, &event.Receipt)
	if err != nil {
		return err
	}
	if hash != event.Receipt.ObservationHash || (event.Released && outcome != "CONFIRMED") || (!event.Released && outcome != "UNKNOWN") {
		return errors.New("lease recovery observation mismatch")
	}
	s.Push.LeaseRecovery.Outcome, s.Push.LeaseRecovery.Receipt = outcome, &event.Receipt
	return nil
}

// RecoverPushLease records recovery authority before adopting the old lease,
// observes the push without retrying its writes, then releases and observes the
// lease. A failed/interrupted recovery is not automatically retried.
func RecoverPushLease(ctx context.Context, path string, p PreparedPushLease, auth effects.Authorization, credentials ...*gitpush.Credential) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	expected, err := expectedPushLease(s, p.Plan)
	if err != nil {
		return s, err
	}
	if expected != p.Intent {
		return s, errors.New("lease recovery preview substitution")
	}
	if err := auth.Validate(expected); err != nil {
		return s, err
	}
	if _, err := pushCredential(s.Push.Intent.Prepared.Plan.Destination, credentials); err != nil {
		return s, err
	}
	if err := Append(path, "push.lease-intent", PushLeaseIntent{Prepared: p, Authorization: auth}); err != nil {
		return s, err
	}
	lease, adoptErr := worktree.AdoptLease(p.Plan, p.Intent, auth)
	var observeErr, closeErr error
	released := false
	if adoptErr == nil {
		s, observeErr = Inspect(path)
		if observeErr == nil && s.Push.Outcome == "UNKNOWN" {
			s, observeErr = observePushLocked(ctx, path, s, credentials...)
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
	hash, hashErr := canonical.Hash("harness.push-lease-observation.v1", released)
	outcome := "UNKNOWN"
	if released {
		outcome = "CONFIRMED"
	}
	event := PushLeaseObservation{Receipt: effects.Receipt{Version: 1, IntentID: id, Outcome: outcome, ObservationHash: hash}, Released: released}
	appendErr := Append(path, "push.lease-observed", event)
	latest, readErr := Inspect(path)
	return latest, errors.Join(adoptErr, observeErr, closeErr, idErr, hashErr, appendErr, readErr)
}
