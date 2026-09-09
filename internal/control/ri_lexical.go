package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
)

// RILexicalIntent binds staging to an approved run and exact executable/input plan.
type RILexicalIntent struct {
	Plan          ri.LexicalPlan        `json:"plan"`
	Intent        effects.Intent        `json:"intent"`
	Authorization effects.Authorization `json:"authorization"`
}

// RILexicalObservation carries confirmed staging evidence, or nil for UNKNOWN.
type RILexicalObservation struct {
	IntentID string           `json:"intent_id"`
	Build    *ri.LexicalBuild `json:"build"`
}

// RILexicalState is replayed solely from validated journal events.
type RILexicalState struct {
	Intent      RILexicalIntent       `json:"intent"`
	Observation *RILexicalObservation `json:"observation"`
	Outcome     string                `json:"outcome"`
}

func replayRILexical(s *Snapshot, e journal.Event, seen map[string]bool) error {
	switch e.Kind {
	case "ri.lexical-intent":
		if (s.State != "IMPLEMENTING" && s.State != "REPAIRING") || s.ApprovedBy == "" || s.FileOutcome == "UNKNOWN" || s.WorkspaceOutcome == "UNKNOWN" {
			return errors.New("approved resolved implementation required for lexical indexing")
		}
		var event RILexicalIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		if event.Plan.Repository != s.Creation.Repository {
			return errors.New("lexical repository substitution")
		}
		expected, err := event.Plan.Intent(s.RunID, s.PlanID)
		if err != nil {
			return err
		}
		if expected != event.Intent {
			return errors.New("lexical intent substitution")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		id, err := expected.ID()
		if err != nil {
			return err
		}
		if seen[id] {
			return errors.New("lexical intent already attempted")
		}
		seen[id] = true
		s.RILexical = &RILexicalState{Intent: event, Outcome: "UNKNOWN"}
	case "ri.lexical-observed":
		if s.RILexical == nil || s.RILexical.Outcome != "UNKNOWN" {
			return errors.New("no unresolved lexical effect")
		}
		var event RILexicalObservation
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		id, err := s.RILexical.Intent.Intent.ID()
		if err != nil {
			return err
		}
		if id != event.IntentID {
			return errors.New("lexical observation intent mismatch")
		}
		if event.Build != nil {
			if _, err := event.Build.ID(); err != nil {
				return err
			}
			p := s.RILexical.Intent.Plan
			count := 0
			for _, shard := range event.Build.Shards {
				if shard.Files > p.BatchFiles {
					return errors.New("lexical shard exceeds plan")
				}
				count += shard.Files
			}
			if count != p.Files || event.Build.ManifestID != p.ManifestID {
				return errors.New("lexical build scope mismatch")
			}
			s.RILexical.Outcome = "CONFIRMED"
		}
		s.RILexical.Observation = &event
	}
	return nil
}

// ExecuteRILexical persists the admitted intent before the first staging write.
// No automatic retry is possible: attempted intents and UNKNOWN states block it.
func ExecuteRILexical(ctx context.Context, path string, plan ri.LexicalPlan, manifest ri.LexicalManifest, authorization effects.Authorization) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if err := plan.ValidateManifest(manifest); err != nil {
		return s, err
	}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		return s, err
	}
	if err := Append(path, "ri.lexical-intent", RILexicalIntent{plan, intent, authorization}); err != nil {
		return s, err
	}
	ref, executionErr := ri.StageLexical(ctx, plan, manifest)
	id, _ := intent.ID()
	observation := RILexicalObservation{IntentID: id}
	if executionErr == nil {
		observation.Build = &ref.Build
	}
	appendErr := Append(path, "ri.lexical-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(executionErr, appendErr, readErr)
}

// ReconcileRILexical observes an unresolved staging attempt without repeating it.
// Failed observations leave the journal unchanged and the effect UNKNOWN.
func ReconcileRILexical(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.RILexical == nil || s.RILexical.Outcome != "UNKNOWN" {
		return s, errors.New("no unresolved lexical effect")
	}
	ref, err := ri.ObserveLexicalStage(ctx, s.RILexical.Intent.Plan)
	if err != nil {
		return s, err
	}
	id, err := s.RILexical.Intent.Intent.ID()
	if err != nil {
		return s, err
	}
	appendErr := Append(path, "ri.lexical-observed", RILexicalObservation{IntentID: id, Build: &ref.Build})
	latest, readErr := Inspect(path)
	return latest, errors.Join(appendErr, readErr)
}
