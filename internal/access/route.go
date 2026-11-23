package access

import (
	"errors"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// Route is an operator-selected inference route with an exact access-policy
// identity. Permission is a capability ceiling, not direct filesystem authority.
type Route struct {
	Version      int           `json:"version"`
	Role         string        `json:"role"`
	Runtime      string        `json:"runtime"`
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	Effort       string        `json:"effort"`
	AccessID     string        `json:"access_id"`
	Permission   string        `json:"permission"`
	ModelChoices []ModelChoice `json:"model_choices,omitempty"`
}

// ModelChoice is a single controller-configured alternative under one fixed
// runtime/provider/access route. It carries no credential or billing authority.
type ModelChoice struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
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
	if len(r.ModelChoices) > 32 {
		return errors.New("too many adaptive model choices")
	}
	choices := map[ModelChoice]bool{}
	for _, choice := range r.ModelChoices {
		if !validModelName(choice.Model) || !identifier(choice.Effort) || choice.Model == r.Model && choice.Effort == r.Effort || choices[choice] {
			return errors.New("invalid adaptive model choice")
		}
		choices[choice] = true
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

// AllowsModelChoice admits the default route when choice is nil, or one exact
// alternative listed by this route.
func (r Route) AllowsModelChoice(choice *ModelChoice) bool {
	if choice == nil {
		return true
	}
	for _, allowed := range r.ModelChoices {
		if allowed == *choice {
			return true
		}
	}
	return false
}

func validModelName(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
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
	policyID, policyErr := policy.ID()
	requestedID, requestedErr := requested.ID()
	if policyErr != nil || requestedErr != nil || policyID != requestedID {
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
