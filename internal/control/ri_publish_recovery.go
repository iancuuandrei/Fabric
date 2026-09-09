package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
)

// RIPublishRecovery authorizes one explicit completion of exact pending bytes.
type RIPublishRecovery struct {
	Intent        effects.Intent        `json:"intent"`
	Authorization effects.Authorization `json:"authorization"`
}

func publicationRecoveryIntent(s Snapshot) (effects.Intent, error) {
	if s.RIPublish == nil || s.RIPublish.Outcome != "UNKNOWN" || (s.RIPublish.Recovery != nil && !s.RIPublish.RecoveryObserved) {
		return effects.Intent{}, errors.New("no unattempted publication recovery")
	}
	id, err := s.RIPublish.Intent.Intent.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	previous := ""
	if s.RIPublish.Recovery != nil {
		previous, err = s.RIPublish.Recovery.Intent.ID()
		if err != nil {
			return effects.Intent{}, err
		}
	}
	hash, err := canonical.Hash("harness.ri.publication-recovery.v1", struct {
		PublicationID    string                `json:"publication_id"`
		PreviousRecovery string                `json:"previous_recovery"`
		Observation      *RIPublishObservation `json:"observation"`
	}{id, previous, s.RIPublish.Observation})
	if err != nil {
		return effects.Intent{}, err
	}
	result := s.RIPublish.Intent.Intent
	result.Kind = "ri_publish_recovery"
	result.InputHash = hash
	return result, nil
}

// PrepareRIPublishRecovery returns the exact recovery approval target without IO effects.
func PrepareRIPublishRecovery(path string) (effects.Intent, error) {
	s, err := Inspect(path)
	if err != nil {
		return effects.Intent{}, err
	}
	return publicationRecoveryIntent(s)
}

func replayRIPublishRecovery(s *Snapshot, event journal.Event, seen map[string]bool) error {
	var recovery RIPublishRecovery
	if err := canonical.Decode(event.Payload, &recovery); err != nil {
		return err
	}
	expected, err := publicationRecoveryIntent(*s)
	if err != nil {
		return err
	}
	if expected != recovery.Intent {
		return errors.New("publication recovery substitution")
	}
	if err := recovery.Authorization.Validate(expected); err != nil {
		return err
	}
	id, err := expected.ID()
	if err != nil {
		return err
	}
	if seen[id] {
		return errors.New("publication recovery already attempted")
	}
	seen[id] = true
	s.RIPublish.Recovery = &recovery
	s.RIPublish.RecoveryObserved = false
	return nil
}

// RecoverRIPublish journals recovery before completing a pending store publication.
// Partial or independent conflicting files remain untouched. Each publication
// requires a recorded observation before a separately authorized next attempt.
func RecoverRIPublish(ctx context.Context, path string, authorization effects.Authorization) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	intent, err := publicationRecoveryIntent(s)
	if err != nil {
		return s, err
	}
	if err := Append(path, "ri.publish-recovery-intent", RIPublishRecovery{intent, authorization}); err != nil {
		return s, err
	}
	store := ri.Store{Directory: s.RIPublish.Intent.Directory}
	_, recoveryErr := store.Reconcile(s.RIImport.Observation.Artifact.SnapshotID)
	latest, observeErr := ReconcileRIPublish(ctx, path)
	return latest, errors.Join(recoveryErr, observeErr)
}
