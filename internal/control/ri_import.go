package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
)

// RIImportIntent binds the complete import payload to exact effect authorization.
type RIImportIntent struct {
	Plan          ri.ImportPlan         `json:"plan"`
	Intent        effects.Intent        `json:"intent"`
	Authorization effects.Authorization `json:"authorization"`
}

// RIImportObservation records verified staging evidence or unresolved execution.
type RIImportObservation struct {
	IntentID string            `json:"intent_id"`
	Artifact *ri.ImportReceipt `json:"artifact"`
}

// RIImportState is reconstructed from intent and validated observation events.
type RIImportState struct {
	Intent      RIImportIntent       `json:"intent"`
	Observation *RIImportObservation `json:"observation"`
	Outcome     string               `json:"outcome"`
}

func replayRIImport(s *Snapshot, e journal.Event, seen map[string]bool) error {
	switch e.Kind {
	case "ri.import-intent":
		if (s.State != "IMPLEMENTING" && s.State != "REPAIRING") || s.ApprovedBy == "" {
			return errors.New("approved implementation plan required for RI import")
		}
		if s.FileOutcome == "UNKNOWN" || s.WorkspaceOutcome == "UNKNOWN" {
			return errors.New("unresolved writer effect blocks RI import")
		}
		var event RIImportIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		if event.Plan.Repository != s.Creation.Repository {
			return errors.New("RI import repository mismatch")
		}
		if err := validateProducerImport(*s, event.Plan); err != nil {
			return err
		}
		expected, err := event.Plan.Intent(s.RunID, s.PlanID)
		if err != nil {
			return err
		}
		if expected != event.Intent {
			return errors.New("RI import intent substitution")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		id, err := expected.ID()
		if err != nil {
			return err
		}
		if seen[id] {
			return errors.New("RI import intent already attempted")
		}
		seen[id] = true
		s.RIImport = &RIImportState{Intent: event, Outcome: "UNKNOWN"}
	case "ri.import-observed":
		if s.RIImport == nil || s.RIImport.Outcome != "UNKNOWN" {
			return errors.New("no unresolved RI import")
		}
		var event RIImportObservation
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		id, err := s.RIImport.Intent.Intent.ID()
		if err != nil {
			return err
		}
		if event.IntentID != id {
			return errors.New("RI observation intent mismatch")
		}
		if a := event.Artifact; a != nil {
			p := s.RIImport.Intent.Plan
			if a.Source != p.Request.Source || a.OutputPath != p.OutputPath || a.Bytes < 1 || a.Bytes > 64<<20 {
				return errors.New("RI artifact observation mismatch")
			}
			if err := safepath.RequireDigest(a.SnapshotID); err != nil {
				return err
			}
			s.RIImport.Outcome = "CONFIRMED"
		}
		s.RIImport.Observation = &event
	}
	return nil
}

// ExecuteRIImport persists intent before dispatch and records verified staging.
// Missing/error results remain UNKNOWN. It never retries or deletes staging data.
func ExecuteRIImport(ctx context.Context, path string, plan ri.ImportPlan, authorization effects.Authorization) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		return s, err
	}
	if _, err := ri.VerifyCommittedSources(ctx, plan); err != nil {
		return s, err
	}
	if err := Append(path, "ri.import-intent", RIImportIntent{plan, intent, authorization}); err != nil {
		return s, err
	}
	client := ri.Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	var artifact ri.ImportReceipt
	var executionErr error
	if plan.MaterializeSources {
		_, executionErr = ri.MaterializeSources(ctx, plan)
	}
	if executionErr == nil {
		artifact, executionErr = client.Import(ctx, plan.Request, plan.Repository, plan.OutputPath)
	}
	id, _ := intent.ID()
	observation := RIImportObservation{IntentID: id}
	if executionErr == nil {
		observation.Artifact = &artifact
	}
	appendErr := Append(path, "ri.import-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(executionErr, appendErr, readErr)
}

// ReconcileRIImport observes an unresolved import without retrying staging writes.
// Recomputing import identity runs no producer. Failed observation stays UNKNOWN.
func ReconcileRIImport(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.RIImport == nil || s.RIImport.Outcome != "UNKNOWN" {
		return s, errors.New("no unresolved RI import")
	}
	var artifact ri.ImportReceipt
	_, observeErr := ri.VerifyCommittedSources(ctx, s.RIImport.Intent.Plan)
	if observeErr == nil {
		artifact, observeErr = ri.ObserveImport(ctx, s.RIImport.Intent.Plan)
	}
	id, err := s.RIImport.Intent.Intent.ID()
	if err != nil {
		return s, err
	}
	observation := RIImportObservation{IntentID: id}
	if observeErr == nil {
		observation.Artifact = &artifact
	}
	appendErr := Append(path, "ri.import-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(observeErr, appendErr, readErr)
}
