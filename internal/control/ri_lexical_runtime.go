package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/worktree"
)

// SelectRuntimeLexical selects only controller-confirmed artifacts and rechecks
// them before runtime binding. includeOverlay requires the current admitted
// candidate's confirmed overlay; it never silently falls back to base search.
func SelectRuntimeLexical(ctx context.Context, path string, includeOverlay bool) (codexruntime.LexicalBinding, error) {
	return selectRuntimeLexical(ctx, path, includeOverlay, ObserveLexicalCandidate)
}

// selectRuntimeLexicalLeased is for role execution holding this workspace lease.
func selectRuntimeLexicalLeased(ctx context.Context, path string, includeOverlay bool, workspace worktree.Binding) (codexruntime.LexicalBinding, error) {
	return selectRuntimeLexical(ctx, path, includeOverlay, func(ctx context.Context, path string) (LexicalCandidate, error) {
		return observeLexicalCandidateLeased(ctx, path, workspace)
	})
}

func selectRuntimeLexical(ctx context.Context, path string, includeOverlay bool, capture func(context.Context, string) (LexicalCandidate, error)) (codexruntime.LexicalBinding, error) {
	s, err := Inspect(path)
	if err != nil {
		return codexruntime.LexicalBinding{}, err
	}
	if s.RILexical == nil || s.RILexical.Outcome != "CONFIRMED" || s.RILexical.Observation == nil || s.RILexical.Observation.Build == nil {
		return codexruntime.LexicalBinding{}, errors.New("confirmed lexical build required")
	}
	plan := s.RILexical.Intent.Plan
	base, err := ri.ObserveLexicalStage(ctx, plan)
	if err != nil {
		return codexruntime.LexicalBinding{}, err
	}
	id, err := base.Build.ID()
	if err != nil {
		return codexruntime.LexicalBinding{}, err
	}
	expected, err := s.RILexical.Observation.Build.ID()
	if err != nil {
		return codexruntime.LexicalBinding{}, err
	}
	if id != expected {
		return codexruntime.LexicalBinding{}, errors.New("runtime lexical build differs from journal")
	}
	binding := codexruntime.LexicalBinding{Base: base, Executable: plan.Executable, ExecutableSHA256: plan.ExecutableSHA256}
	candidate := ""
	if includeOverlay {
		if s.RILexicalOverlay == nil || s.RILexicalOverlay.Intent.Prepared.Plan.BaseID != base.Build.ManifestID {
			return codexruntime.LexicalBinding{}, errors.New("runtime overlay base mismatch")
		}
		overlay, err := selectLexicalOverlay(ctx, path, capture)
		if err != nil {
			return codexruntime.LexicalBinding{}, err
		}
		binding.Overlay = &overlay
		candidate = overlay.Candidate
	}
	if _, err := binding.Record(s.Creation.Repository, candidate); err != nil {
		return codexruntime.LexicalBinding{}, err
	}
	return binding, nil
}
