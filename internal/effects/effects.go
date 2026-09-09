package effects

import (
	"errors"
	"strings"

	"harness.local/engorch/internal/canonical"
)

// Intent binds a proposed effect to its run, exact approval and input artifact.
// InputHash refers to a kind-specific immutable payload; this envelope alone
// cannot authorize execution without that payload and its concrete validator.
type Intent struct {
	Version      int    `json:"version"`
	RunID        string `json:"run_id"`
	PlanID       string `json:"plan_id"`
	RepositoryID string `json:"repository_id"`
	Kind         string `json:"kind"`
	InputHash    string `json:"input_hash"`
}

func hash(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }

// ID rejects unknown effect kinds and returns the exact intent identity.
func (i Intent) ID() (string, error) {
	if i.Version != 1 || !hash(i.RunID) || !hash(i.PlanID) || !hash(i.RepositoryID) || !hash(i.InputHash) {
		return "", errors.New("invalid effect binding")
	}
	switch i.Kind {
	case "filesystem", "ri_producer", "ri_import", "ri_lexical", "ri_lexical_overlay", "ri_publish", "ri_publish_recovery", "lease_recovery", "commit", "commit_recovery", "push", "draft_pr":
	default:
		return "", errors.New("unknown effect kind")
	}
	return canonical.Hash("harness.effect.v1", i)
}

// Authorization records explicit operator approval of one exact intent. It is
// an audit binding; authenticating the operator belongs to the host boundary.
type Authorization struct {
	IntentID string `json:"intent_id"`
	Actor    string `json:"actor"`
}

// Validate ensures approval applies to these exact inputs, not another effect.
func (a Authorization) Validate(i Intent) error {
	id, err := i.ID()
	if err != nil {
		return err
	}
	if a.IntentID != id || strings.TrimSpace(a.Actor) == "" || len(a.Actor) > 256 {
		return errors.New("effect approval mismatch")
	}
	return nil
}

// Receipt binds a classified outcome to observed evidence. The adapter must
// validate the observation itself; a hash is not proof that execution succeeded.
type Receipt struct {
	Version         int    `json:"version"`
	IntentID        string `json:"intent_id"`
	Outcome         string `json:"outcome"`
	ObservationHash string `json:"observation_hash"`
}

// Outcome returns UNKNOWN when a durable intent has no validated receipt.
// It never authorizes retry: reconciliation must establish actual external state.
func Outcome(i Intent, r *Receipt) (string, error) {
	id, err := i.ID()
	if err != nil {
		return "", err
	}
	if r == nil {
		return "UNKNOWN", nil
	}
	if r.Version != 1 || r.IntentID != id || !hash(r.ObservationHash) {
		return "", errors.New("effect receipt binding mismatch")
	}
	switch r.Outcome {
	case "CONFIRMED", "NOT_APPLIED", "UNKNOWN":
		return r.Outcome, nil
	default:
		return "", errors.New("invalid effect outcome")
	}
}
