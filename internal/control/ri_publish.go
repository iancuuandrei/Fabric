package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"path/filepath"
)

// RIPublishIntent binds the confirmed import to one local artifact store.
type RIPublishIntent struct {
	Directory     string                `json:"directory"`
	Intent        effects.Intent        `json:"intent"`
	Authorization effects.Authorization `json:"authorization"`
}

// RIPublishObservation records final artifact admission; nil remains UNKNOWN.
type RIPublishObservation struct {
	IntentID string          `json:"intent_id"`
	Artifact *ri.SnapshotRef `json:"artifact"`
}

// RIPublishState tracks one publication from the journal.
type RIPublishState struct {
	RecoveryObserved bool                  `json:"recovery_observed"`
	Recovery         *RIPublishRecovery    `json:"recovery"`
	Intent           RIPublishIntent       `json:"intent"`
	Observation      *RIPublishObservation `json:"observation"`
	Outcome          string                `json:"outcome"`
}

// PrepareRIPublish returns an approval target without creating or writing files.
func PrepareRIPublish(path, directory string) (effects.Intent, error) {
	s, err := Inspect(path)
	if err != nil {
		return effects.Intent{}, err
	}
	return publishIntent(s, directory)
}

func publishIntent(s Snapshot, directory string) (effects.Intent, error) {
	if (s.State != "IMPLEMENTING" && s.State != "REPAIRING") || s.FileOutcome == "UNKNOWN" || s.WorkspaceOutcome == "UNKNOWN" {
		return effects.Intent{}, errors.New("publication requires an implementation state without unresolved writer effects")
	}
	if s.RIImport == nil || s.RIImport.Outcome != "CONFIRMED" || s.RIImport.Observation == nil || s.RIImport.Observation.Artifact == nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return effects.Intent{}, errors.New("confirmed import and absolute store required")
	}
	importID, err := s.RIImport.Intent.Intent.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	hash, err := canonical.Hash("harness.ri.publication.v1", struct {
		ImportID  string           `json:"import_id"`
		Directory string           `json:"directory"`
		Artifact  ri.ImportReceipt `json:"artifact"`
	}{importID, directory, *s.RIImport.Observation.Artifact})
	if err != nil {
		return effects.Intent{}, err
	}
	result := s.RIImport.Intent.Intent
	result.Kind = "ri_publish"
	result.InputHash = hash
	return result, nil
}

func replayRIPublish(s *Snapshot, event journal.Event, seen map[string]bool) error {
	if event.Kind == "ri.publish-recovery-intent" {
		return replayRIPublishRecovery(s, event, seen)
	}
	if event.Kind == "ri.publish-intent" {
		var p RIPublishIntent
		if err := canonical.Decode(event.Payload, &p); err != nil {
			return err
		}
		expected, err := publishIntent(*s, p.Directory)
		if err != nil {
			return err
		}
		if expected != p.Intent {
			return errors.New("publication intent mismatch")
		}
		if err := p.Authorization.Validate(expected); err != nil {
			return err
		}
		id, err := expected.ID()
		if err != nil {
			return err
		}
		if seen[id] {
			return errors.New("publication already attempted")
		}
		seen[id] = true
		s.RIPublish = &RIPublishState{Intent: p, Outcome: "UNKNOWN"}
		return nil
	}
	if s.RIPublish == nil || s.RIPublish.Outcome != "UNKNOWN" {
		return errors.New("no unresolved publication")
	}
	var observed RIPublishObservation
	if err := canonical.Decode(event.Payload, &observed); err != nil {
		return err
	}
	id, err := s.RIPublish.Intent.Intent.ID()
	if err != nil {
		return err
	}
	if observed.IntentID != id {
		return errors.New("publication observation mismatch")
	}
	if ref := observed.Artifact; ref != nil {
		imported := s.RIImport.Observation.Artifact
		if ref.ID != imported.SnapshotID || ref.Source != imported.Source || ref.Path != filepath.Join(s.RIPublish.Intent.Directory, ref.ID+".jsonl") {
			return errors.New("published artifact mismatch")
		}
		s.RIPublish.Outcome = "CONFIRMED"
	}
	s.RIPublish.Observation = &observed
	if s.RIPublish.Recovery != nil {
		s.RIPublish.RecoveryObserved = true
	}
	return nil
}

// ExecuteRIPublish journals intent before publishing and records admitted output.
// Failures remain UNKNOWN and never cause automatic recovery or retry.
func ExecuteRIPublish(ctx context.Context, path, directory string, authorization effects.Authorization) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	intent, err := publishIntent(s, directory)
	if err != nil {
		return s, err
	}
	if err := Append(path, "ri.publish-intent", RIPublishIntent{directory, intent, authorization}); err != nil {
		return s, err
	}
	ref, executionErr := ri.PublishImport(ctx, s.RIImport.Intent.Plan, ri.Store{Directory: directory})
	id, _ := intent.ID()
	observation := RIPublishObservation{IntentID: id}
	if executionErr == nil {
		observation.Artifact = &ref
	}
	appendErr := Append(path, "ri.publish-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(executionErr, appendErr, readErr)
}

// ReconcileRIPublish observes a completed publication without writing files.
// Missing or pending publication remains UNKNOWN; completing a pending hard link
// requires a separate explicitly journaled recovery action.
func ReconcileRIPublish(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.RIPublish == nil || s.RIPublish.Outcome != "UNKNOWN" {
		return s, errors.New("no unresolved publication")
	}
	imported := s.RIImport.Observation.Artifact
	store := ri.Store{Directory: s.RIPublish.Intent.Directory}
	_, observeErr := store.Read(imported.SnapshotID)
	ref := ri.SnapshotRef{Path: filepath.Join(store.Directory, imported.SnapshotID+".jsonl"), ID: imported.SnapshotID, Source: imported.Source}
	if observeErr == nil {
		p := s.RIImport.Intent.Plan
		_, observeErr = (ri.Client{Executable: p.Executable, ExecutableHash: p.ExecutableSHA256}).Inspect(ctx, ref)
	}
	id, _ := s.RIPublish.Intent.Intent.ID()
	observation := RIPublishObservation{IntentID: id}
	if observeErr == nil {
		observation.Artifact = &ref
	}
	appendErr := Append(path, "ri.publish-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(observeErr, appendErr, readErr)
}
