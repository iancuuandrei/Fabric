package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/worktree"
	"path/filepath"
	"strings"
)

// RIProducerIntent records the complete process plan and exact authorization.
type RIProducerIntent struct {
	Plan          ri.ProducerPlan       `json:"plan"`
	Intent        effects.Intent        `json:"intent"`
	Authorization effects.Authorization `json:"authorization"`
}

// RIProducerObservation retains process facts when available; nil is uncertainty.
type RIProducerObservation struct {
	Before   *worktree.Candidate `json:"before"`
	After    *worktree.Candidate `json:"after"`
	IntentID string              `json:"intent_id"`
	Result   *ri.ProducerResult  `json:"result"`
}

// RIProducerState is reconstructed from durable producer events.
type RIProducerState struct {
	Closure     *RIProducerClosure     `json:"closure"`
	Intent      RIProducerIntent       `json:"intent"`
	Observation *RIProducerObservation `json:"observation"`
	Outcome     string                 `json:"outcome"`
}

func replayRIProducer(s *Snapshot, event journal.Event, seen map[string]bool) error {
	if event.Kind == "ri.producer-closed" {
		return replayRIProducerClosure(s, event)
	}
	if event.Kind == "ri.producer-intent" {
		if (s.State != "IMPLEMENTING" && s.State != "REPAIRING") || s.ApprovedBy == "" || s.FileOutcome == "UNKNOWN" || s.WorkspaceOutcome == "UNKNOWN" {
			return errors.New("approved implementation without unresolved writer required")
		}
		if s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" {
			return errors.New("producer requires admitted isolated workspace")
		}
		var p RIProducerIntent
		if err := canonical.Decode(event.Payload, &p); err != nil {
			return err
		}
		if p.Plan.Repository != s.Creation.Repository {
			return errors.New("producer repository mismatch")
		}
		if p.Plan.Invocation.Directory != s.Workspace.Request.Path {
			return errors.New("producer directory differs from owned workspace")
		}
		rel, err := filepath.Rel(p.Plan.Invocation.Directory, p.Plan.OutputPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("producer output must be outside source workspace")
		}
		expected, err := p.Plan.Intent(s.RunID, s.PlanID)
		if err != nil {
			return err
		}
		if expected != p.Intent {
			return errors.New("producer intent mismatch")
		}
		if err := p.Authorization.Validate(expected); err != nil {
			return err
		}
		id, err := expected.ID()
		if err != nil {
			return err
		}
		if seen[id] {
			return errors.New("producer effect already attempted")
		}
		seen[id] = true
		s.RIProducer = &RIProducerState{Intent: p, Outcome: "UNKNOWN"}
		return nil
	}
	if s.RIProducer == nil || s.RIProducer.Outcome != "UNKNOWN" || s.RIProducer.Closure != nil {
		return errors.New("no unresolved producer effect")
	}
	var observed RIProducerObservation
	if err := canonical.Decode(event.Payload, &observed); err != nil {
		return err
	}
	id, err := s.RIProducer.Intent.Intent.ID()
	if err != nil {
		return err
	}
	if observed.IntentID != id {
		return errors.New("producer observation mismatch")
	}
	for _, candidate := range []*worktree.Candidate{observed.Before, observed.After} {
		if candidate == nil {
			continue
		}
		if _, err := candidate.ID(); err != nil {
			return err
		}
		workspaceID, err := s.Workspace.ID()
		if err != nil {
			return err
		}
		if candidate.WorktreeID != workspaceID || candidate.Head != s.Creation.Repository.Commit {
			return errors.New("producer source observation mismatch")
		}
	}
	if observed.Result != nil {
		if err := ri.ValidateProducerResult(s.RIProducer.Intent.Plan, *observed.Result); err != nil {
			return err
		}
		if observed.Result.Artifact != nil && observed.Before != nil && observed.After != nil && *observed.Before == *observed.After {
			s.RIProducer.Outcome = "CONFIRMED"
		}
		if observed.Result.Process.Status == "NOT_RUN" && observed.Result.Artifact == nil && observed.Before != nil && observed.After != nil && *observed.Before == *observed.After {
			s.RIProducer.Outcome = "NOT_APPLIED"
		}
	}
	s.RIProducer.Observation = &observed
	return nil
}

// ExecuteRIProducer journals intent before dispatch. It observes repository
// identity but the caller must qualify and isolate the selected source directory.
// Failure remains UNKNOWN; no producer process is automatically retried.
func ExecuteRIProducer(ctx context.Context, path string, plan ri.ProducerPlan, authorization effects.Authorization) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if err = requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	if s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" {
		return s, errors.New("producer requires admitted isolated workspace")
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	before, err := worktree.Pristine(ctx, *s.Workspace)
	if err != nil {
		return s, err
	}
	observed, err := repository.DiscoverCommit(ctx, plan.Repository.Root, plan.Repository.Name, plan.Repository.Commit)
	if err != nil {
		return s, err
	}
	if observed != plan.Repository {
		return s, errors.New("producer repository identity mismatch")
	}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		return s, err
	}
	if err := Append(path, "ri.producer-intent", RIProducerIntent{plan, intent, authorization}); err != nil {
		return s, err
	}
	result, executionErr := ri.ExecuteProducer(ctx, plan)
	id, _ := intent.ID()
	observation := RIProducerObservation{IntentID: id, Before: &before}
	after, sourceErr := worktree.Pristine(ctx, *s.Workspace)
	if sourceErr == nil {
		observation.After = &after
	}
	if ri.ValidateProducerResult(plan, result) == nil {
		observation.Result = &result
	}
	appendErr := Append(path, "ri.producer-observed", observation)
	latest, readErr := Inspect(path)
	return latest, errors.Join(executionErr, sourceErr, appendErr, readErr)
}
