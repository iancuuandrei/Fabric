package verification

import (
	"errors"
	"reflect"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/safepath"
)

// Plan freezes the complete ordered check set for one run and candidate.
// Nonce distinguishes explicitly requested attempts; it does not grant retry authority.
type Plan struct {
	Version     int          `json:"version"`
	RunID       string       `json:"run_id"`
	Nonce       string       `json:"nonce"`
	CandidateID string       `json:"candidate_id"`
	Invocations []Invocation `json:"invocations"`
}

// PreparePlan resolves every required check before the first command is dispatched.
func PreparePlan(runID, nonce, candidateID, directory string, checks []config.Check) (Plan, error) {
	p := Plan{Version: 1, RunID: runID, Nonce: nonce, CandidateID: candidateID, Invocations: []Invocation{}}
	for _, check := range checks {
		i, err := Prepare(candidateID, directory, check)
		if err != nil {
			return p, err
		}
		p.Invocations = append(p.Invocations, i)
	}
	return p, p.Validate(checks, directory)
}

// Validate rejects omitted, reordered or substituted required checks.
func (p Plan) Validate(checks []config.Check, directory string) error {
	if p.Version != 1 || len(p.Nonce) < 1 || len(p.Nonce) > 128 || len(checks) < 1 || len(checks) > 64 || len(p.Invocations) != len(checks) {
		return errors.New("invalid verification plan shape")
	}
	if err := safepath.RequireDigest(p.RunID); err != nil {
		return err
	}
	if err := safepath.RequireDigest(p.CandidateID); err != nil {
		return err
	}
	seen := map[string]bool{}
	for n, i := range p.Invocations {
		if err := i.Validate(); err != nil {
			return err
		}
		if i.CandidateID != p.CandidateID || i.Directory != directory || !reflect.DeepEqual(i.Check, checks[n]) || seen[i.Check.Name] {
			return errors.New("verification plan does not bind exact required checks")
		}
		seen[i.Check.Name] = true
	}
	return nil
}

// ID returns a canonical attempt identity. Validate against trusted configuration first.
func (p Plan) ID() (string, error) { return canonical.Hash("harness.verification-plan.v1", p) }
