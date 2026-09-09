package control

import (
	"context"
	"errors"
	"harness.local/engorch/internal/codexruntime"
)

func selectRoleRuntimeLexical(ctx context.Context, path string, s Snapshot) (*codexruntime.LexicalBinding, error) {
	context, err := roleLexical(s)
	if err != nil || context == nil {
		return nil, err
	}
	if s.Workspace == nil {
		return nil, errors.New("lexical role workspace required")
	}
	binding, err := selectRuntimeLexicalLeased(ctx, path, context.Scope == "candidate", *s.Workspace)
	if err != nil {
		return nil, err
	}
	id, err := binding.Base.Build.ID()
	if err != nil {
		return nil, err
	}
	if id != context.BuildID {
		return nil, errors.New("role lexical build changed")
	}
	if binding.Overlay != nil {
		id, err := binding.Overlay.ID(binding.Base.Manifest)
		if err != nil {
			return nil, err
		}
		if id != context.OverlayID {
			return nil, errors.New("role lexical overlay changed")
		}
	}
	return &binding, nil
}

func validateRoleLexicalEvidence(s Snapshot, expected, actual *codexruntime.LexicalBinding, candidateID string) error {
	if (expected == nil) != (actual == nil) {
		return errors.New("runtime lexical binding mismatch")
	}
	if expected == nil {
		return nil
	}
	expectedID, err := expected.ID(s.Creation.Repository, candidateID)
	if err != nil {
		return err
	}
	actualID, err := actual.ID(s.Creation.Repository, candidateID)
	if err != nil {
		return err
	}
	if expectedID != actualID {
		return errors.New("runtime lexical evidence substituted")
	}
	return nil
}

func validateRoleLexicalState(s Snapshot, expected *codexruntime.LexicalBinding, actual codexruntime.State, candidateID string) error {
	if actual.Source == nil || *actual.Source != s.Creation.Repository {
		return errors.New("runtime lexical evidence source differs")
	}
	observedCandidate := ""
	if actual.Candidate != nil {
		var err error
		observedCandidate, err = actual.Candidate.Candidate.ID()
		if err != nil {
			return err
		}
	}
	if candidateID != observedCandidate {
		return errors.New("runtime lexical evidence candidate differs")
	}
	return actual.ValidateLexicalBinding(expected)
}

type roleLexicalContext struct {
	PlanID      string `json:"plan_id"`
	BuildID     string `json:"build_id"`
	OverlayID   string `json:"overlay_id"`
	Scope       string `json:"scope"`
	Instruction string `json:"instruction"`
}

// roleLexical derives invocation identity exclusively from journal metadata.
// Runtime selection separately verifies artifacts before any model dispatch.
func roleLexical(s Snapshot) (*roleLexicalContext, error) {
	if s.RILexical == nil {
		return nil, nil
	}
	if s.RILexical.Outcome != "CONFIRMED" || s.RILexical.Observation == nil || s.RILexical.Observation.Build == nil {
		return nil, errors.New("lexical indexing is unresolved")
	}
	plan, err := s.RILexical.Intent.Plan.ID()
	if err != nil {
		return nil, err
	}
	build, err := s.RILexical.Observation.Build.ID()
	if err != nil {
		return nil, err
	}
	c := &roleLexicalContext{PlanID: plan, BuildID: build, Scope: "base_commit", Instruction: "ri_search searches exact bytes in this fixed lexical scope. Results carry blob identities and byte ranges; lexical absence is not semantic absence. Base scope does not include candidate edits."}
	if s.RILexicalOverlay != nil {
		if s.RILexicalOverlay.Outcome != "CONFIRMED" || s.Candidate == nil {
			return nil, errors.New("lexical overlay is unresolved")
		}
		candidate, err := s.Candidate.ID()
		if err != nil {
			return nil, err
		}
		p := s.RILexicalOverlay.Intent.Prepared.Plan
		if p.CandidateID != candidate || p.BaseID != s.RILexical.Intent.Plan.ManifestID {
			return nil, errors.New("lexical overlay differs from role candidate or base")
		}
		c.OverlayID = p.OverlayID
		c.Scope = "candidate"
		c.Instruction = "ri_search searches this fixed admitted candidate overlay. Replaced and deleted base files are shadowed. Follow bound pagination; lexical absence is not semantic absence."
	}
	return c, nil
}
