package control

import (
	"context"
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/safepath"
)

// LexicalOverlayPlan binds an immutable capture and fresh destination.
type LexicalOverlayPlan struct {
	CandidateID string `json:"candidate_id"`
	BaseID      string `json:"base_id"`
	ChangedID   string `json:"changed_id"`
	OverlayID   string `json:"overlay_id"`
	StageRoot   string `json:"stage_root"`
}

// ID validates the complete overlay materialization plan.
func (p LexicalOverlayPlan) ID() (string, error) {
	for _, id := range []string{p.CandidateID, p.BaseID, p.ChangedID, p.OverlayID} {
		if err := safepath.RequireDigest(id); err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(p.StageRoot) || filepath.Clean(p.StageRoot) != p.StageRoot {
		return "", errors.New("normalized absolute overlay destination required")
	}
	return canonical.Hash("harness.ri.lexical-overlay-plan.v1", p)
}

func overlayPlan(c LexicalCandidate, destination string) (LexicalOverlayPlan, error) {
	candidate, err := c.Candidate.ID()
	if err != nil {
		return LexicalOverlayPlan{}, err
	}
	base, err := c.Base.ID()
	if err != nil {
		return LexicalOverlayPlan{}, err
	}
	changed, err := c.Changed.ID()
	if err != nil {
		return LexicalOverlayPlan{}, err
	}
	ref := overlayRef(c, destination, candidate)
	id, err := ref.ID(c.Base)
	if err != nil {
		return LexicalOverlayPlan{}, err
	}
	p := LexicalOverlayPlan{candidate, base, changed, id, destination}
	_, err = p.ID()
	return p, err
}

func overlayRef(c LexicalCandidate, destination, candidate string) ri.LexicalOverlayRef {
	return ri.LexicalOverlayRef{Candidate: candidate, ManifestPath: filepath.Join(destination, "manifest.jsonl"), SourceRoot: filepath.Join(destination, "sources"), Manifest: c.Changed, Deleted: c.Deleted}
}

func overlayIntent(s Snapshot, p LexicalOverlayPlan) (effects.Intent, error) {
	h, err := p.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	repo, err := s.Creation.Repository.ID()
	if err != nil {
		return effects.Intent{}, err
	}
	i := effects.Intent{Version: 1, RunID: s.RunID, PlanID: s.PlanID, RepositoryID: repo, Kind: "ri_lexical_overlay", InputHash: h}
	_, err = i.ID()
	return i, err
}

// PreparedLexicalOverlay is the exact authorization target, with no source bytes.
type PreparedLexicalOverlay struct {
	Plan   LexicalOverlayPlan `json:"plan"`
	Intent effects.Intent     `json:"intent"`
}

// PrepareLexicalOverlay captures admitted state without writing staging.
func PrepareLexicalOverlay(ctx context.Context, path, destination string) (PreparedLexicalOverlay, error) {
	c, err := ObserveLexicalCandidate(ctx, path)
	if err != nil {
		return PreparedLexicalOverlay{}, err
	}
	p, err := overlayPlan(c, destination)
	if err != nil {
		return PreparedLexicalOverlay{}, err
	}
	s, err := Inspect(path)
	if err != nil {
		return PreparedLexicalOverlay{}, err
	}
	if s.Candidate == nil || *s.Candidate != c.Candidate {
		return PreparedLexicalOverlay{}, errors.New("overlay candidate changed during preparation")
	}
	i, err := overlayIntent(s, p)
	return PreparedLexicalOverlay{p, i}, err
}

// RILexicalOverlayIntent precedes any materialization write.
type RILexicalOverlayIntent struct {
	Prepared      PreparedLexicalOverlay `json:"prepared"`
	Authorization effects.Authorization  `json:"authorization"`
}

// RILexicalOverlayObservation is nil evidence for UNKNOWN, or the verified scope ID.
type RILexicalOverlayObservation struct {
	IntentID  string  `json:"intent_id"`
	OverlayID *string `json:"overlay_id"`
}

// RILexicalOverlayState retains the durable pending or confirmed effect.
type RILexicalOverlayState struct {
	Intent      RILexicalOverlayIntent       `json:"intent"`
	Observation *RILexicalOverlayObservation `json:"observation"`
	Outcome     string                       `json:"outcome"`
}

func replayLexicalOverlay(s *Snapshot, e journal.Event, seen map[string]bool) error {
	if e.Kind == "ri.overlay-intent" {
		if err := filesAllowed(*s); err != nil {
			return err
		}
		var event RILexicalOverlayIntent
		if err := canonical.Decode(e.Payload, &event); err != nil {
			return err
		}
		if s.Candidate == nil {
			return errors.New("overlay requires admitted candidate")
		}
		candidate, err := s.Candidate.ID()
		if err != nil {
			return err
		}
		if candidate != event.Prepared.Plan.CandidateID {
			return errors.New("overlay candidate substitution")
		}
		expected, err := overlayIntent(*s, event.Prepared.Plan)
		if err != nil {
			return err
		}
		if expected != event.Prepared.Intent {
			return errors.New("overlay intent substitution")
		}
		if err := event.Authorization.Validate(expected); err != nil {
			return err
		}
		id, err := expected.ID()
		if err != nil {
			return err
		}
		if seen[id] {
			return errors.New("overlay intent already attempted")
		}
		seen[id] = true
		s.RILexicalOverlay = &RILexicalOverlayState{Intent: event, Outcome: "UNKNOWN"}
		return nil
	}
	if s.RILexicalOverlay == nil || s.RILexicalOverlay.Outcome != "UNKNOWN" {
		return errors.New("no unresolved lexical overlay")
	}
	var event RILexicalOverlayObservation
	if err := canonical.Decode(e.Payload, &event); err != nil {
		return err
	}
	id, err := s.RILexicalOverlay.Intent.Prepared.Intent.ID()
	if err != nil {
		return err
	}
	if event.IntentID != id {
		return errors.New("overlay observation intent mismatch")
	}
	if event.OverlayID != nil {
		if *event.OverlayID != s.RILexicalOverlay.Intent.Prepared.Plan.OverlayID {
			return errors.New("overlay observation scope mismatch")
		}
		s.RILexicalOverlay.Outcome = "CONFIRMED"
	}
	s.RILexicalOverlay.Observation = &event
	return nil
}

// ExecuteLexicalOverlay re-captures exact inputs before journaling and staging.
// Captured bytes represent that immutable candidate, not subsequent worktree edits.
func ExecuteLexicalOverlay(ctx context.Context, path string, prepared PreparedLexicalOverlay, authorization effects.Authorization) (Snapshot, error) {
	snapshot, err := Inspect(path)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireCurrentHostAdmission(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	c, err := ObserveLexicalCandidate(ctx, path)
	if err != nil {
		return Snapshot{}, err
	}
	p, err := overlayPlan(c, prepared.Plan.StageRoot)
	if err != nil {
		return Snapshot{}, err
	}
	if p != prepared.Plan {
		return Snapshot{}, errors.New("overlay capture differs from approved plan")
	}
	if err := Append(path, "ri.overlay-intent", RILexicalOverlayIntent{prepared, authorization}); err != nil {
		return Snapshot{}, err
	}
	_, executionErr := ri.StageLexicalOverlay(ctx, c.Base, p.CandidateID, c.Changed, c.Deleted, c.Bytes, p.StageRoot)
	id, _ := prepared.Intent.ID()
	event := RILexicalOverlayObservation{IntentID: id}
	if executionErr == nil {
		event.OverlayID = &p.OverlayID
	}
	appendErr := Append(path, "ri.overlay-observed", event)
	s, readErr := Inspect(path)
	return s, errors.Join(executionErr, appendErr, readErr)
}

// ReconcileLexicalOverlay verifies existing staging without repeating writes.
// A changed live candidate or damaged artifact leaves the journal UNKNOWN.
func ReconcileLexicalOverlay(ctx context.Context, path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.RILexicalOverlay == nil || s.RILexicalOverlay.Outcome != "UNKNOWN" {
		return s, errors.New("no unresolved lexical overlay")
	}
	c, err := ObserveLexicalCandidate(ctx, path)
	if err != nil {
		return s, err
	}
	prepared := s.RILexicalOverlay.Intent.Prepared
	p, err := overlayPlan(c, prepared.Plan.StageRoot)
	if err != nil {
		return s, err
	}
	if p != prepared.Plan {
		return s, errors.New("recovery overlay scope changed")
	}
	id, err := ri.ObserveLexicalOverlay(ctx, c.Base, overlayRef(c, p.StageRoot, p.CandidateID))
	if err != nil {
		return s, err
	}
	intentID, _ := prepared.Intent.ID()
	appendErr := Append(path, "ri.overlay-observed", RILexicalOverlayObservation{IntentID: intentID, OverlayID: &id})
	latest, readErr := Inspect(path)
	return latest, errors.Join(appendErr, readErr)
}

// SelectLexicalOverlay returns a confirmed artifact only for the current admitted
// candidate. Its snapshot identity remains explicit if the workspace later moves.
func SelectLexicalOverlay(ctx context.Context, path string) (ri.LexicalOverlayRef, error) {
	return selectLexicalOverlay(ctx, path, ObserveLexicalCandidate)
}

func selectLexicalOverlay(ctx context.Context, path string, capture func(context.Context, string) (LexicalCandidate, error)) (ri.LexicalOverlayRef, error) {
	s, err := Inspect(path)
	if err != nil {
		return ri.LexicalOverlayRef{}, err
	}
	if s.RILexicalOverlay == nil || s.RILexicalOverlay.Outcome != "CONFIRMED" {
		return ri.LexicalOverlayRef{}, errors.New("confirmed lexical overlay required")
	}
	c, err := capture(ctx, path)
	if err != nil {
		return ri.LexicalOverlayRef{}, err
	}
	expected := s.RILexicalOverlay.Intent.Prepared.Plan
	p, err := overlayPlan(c, expected.StageRoot)
	if err != nil {
		return ri.LexicalOverlayRef{}, err
	}
	if p != expected {
		return ri.LexicalOverlayRef{}, errors.New("confirmed overlay differs from current candidate")
	}
	ref := overlayRef(c, p.StageRoot, p.CandidateID)
	id, err := ri.ObserveLexicalOverlay(ctx, c.Base, ref)
	if err != nil {
		return ri.LexicalOverlayRef{}, err
	}
	if id != p.OverlayID {
		return ri.LexicalOverlayRef{}, errors.New("confirmed overlay artifact substitution")
	}
	return ref, nil
}
