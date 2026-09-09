package config

import (
	"errors"
	"harness.local/engorch/internal/runtime"
)

// Route returns only the explicitly configured role; it never borrows another
// role's model. Optional roles remain unavailable until configured by the user.
func (c Config) Route(role string) (runtime.Profile, error) {
	if err := c.Validate(); err != nil {
		return runtime.Profile{}, err
	}
	var profile *runtime.Profile
	switch role {
	case "planner":
		profile = &c.Planner
	case "writer":
		profile = c.Writer
	case "fixer":
		profile = c.Fixer
	case "explorer":
		profile = c.Explorer
	case "reviewer":
		profile = c.Reviewer
	default:
		return runtime.Profile{}, errors.New("unknown routing role")
	}
	if profile == nil {
		return runtime.Profile{}, errors.New("role has no explicit runtime configuration")
	}
	return *profile, nil
}
