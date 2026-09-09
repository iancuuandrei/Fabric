package access

import (
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

// Route is an operator-selected inference route with an exact access-policy
// identity. Permission is a capability ceiling, not direct filesystem authority.
type Route struct {
	Version    int    `json:"version"`
	Role       string `json:"role"`
	Runtime    string `json:"runtime"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	AccessID   string `json:"access_id"`
	Permission string `json:"permission"`
}

// Validate rejects malformed routing and write capabilities on read-only roles.
// Model names are opaque printable identifiers; availability is runtime evidence.
func (r Route) Validate() error {
	if r.Version != 1 || !identifier(r.Runtime) || !identifier(r.Provider) || !identifier(r.Effort) || len(r.Model) == 0 || len(r.Model) > 256 {
		return errors.New("invalid model route")
	}
	for _, c := range r.Model {
		if c < 33 || c > 126 {
			return errors.New("invalid model identifier")
		}
	}
	if err := safepath.RequireDigest(r.AccessID); err != nil {
		return errors.New("invalid route access identity")
	}
	switch r.Role {
	case "planner", "explorer", "reviewer":
		if r.Permission != "read-only" {
			return errors.New("role requires read-only capability")
		}
	case "writer", "fixer":
		if r.Permission != "workspace-write" && r.Permission != "read-only" {
			return errors.New("invalid writer capability")
		}
	default:
		return errors.New("unknown route role")
	}
	return nil
}

// ID binds all route fields, including access policy and requested capability.
func (r Route) ID() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("harness.model-route.v1", r)
}

// AdmitRoute checks a requested route against controller-selected policy and
// access identity. The caller supplies trusted policy and classification; worker
// input must never populate these arguments. This pure check does not reserve
// budget, persist intent, or authorize runtime dispatch on its own.
func AdmitRoute(policy, requested Route, profile Profile, class Class) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if err := requested.Validate(); err != nil {
		return err
	}
	if policy != requested {
		return errors.New("requested route differs from operator policy")
	}
	id, err := profile.ID()
	if err != nil {
		return err
	}
	if id != policy.AccessID {
		return errors.New("route access profile mismatch")
	}
	return profile.Allows(policy.Runtime, policy.Provider, class)
}
