package config

import (
	"errors"
	"fmt"
	"sort"

	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/runtime"
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

func (c Config) configuredRoute(role string) (runtime.Profile, bool) {
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
		return runtime.Profile{}, false
	}
	if profile == nil {
		return runtime.Profile{}, false
	}
	return *profile, true
}

// AllowsProfile admits the immutable configured route or one exact alternative
// named by this role's optional model policy. Runtime and provider are fixed by
// the configured route; models and effort are never borrowed across roles.
func (c Config) AllowsProfile(profile runtime.Profile) bool {
	base, ok := c.configuredRoute(profile.Role)
	if !ok {
		return false
	}
	if base == profile {
		return true
	}
	if c.ModelPolicy == nil || profile.Runtime != base.Runtime || profile.Provider != base.Provider {
		return false
	}
	rule, ok := c.ModelPolicy.Rules[profile.Role]
	if !ok {
		return false
	}
	names := []string{rule.CheapProfile, rule.DefaultProfile, rule.EscalatedProfile}
	if profile.Role == "fixer" && c.ModelPolicy.DecisionEvidenceVersion == 2 && c.ModelPolicy.Calibration != nil {
		names = append(names, c.ModelPolicy.Calibration.Candidate.Name)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, choice := range c.ModelPolicy.Profiles {
			if choice.Name == name && choice.Model == profile.Model && choice.Effort == profile.Effort && choice.Runtime == profile.Runtime && choice.Provider == profile.Provider {
				return true
			}
		}
	}
	return false
}

// ModelChoices returns the exact non-default choices the access route must
// authorize. The base/default choice remains represented by the legacy fields.
func (c Config) ModelChoices(role string) []modelpolicy.Profile {
	if c.ModelPolicy == nil {
		return nil
	}
	rule, ok := c.ModelPolicy.Rules[role]
	if !ok {
		return nil
	}
	base, ok := c.configuredRoute(role)
	if !ok {
		return nil
	}
	profiles := map[string]modelpolicy.Profile{}
	for _, profile := range c.ModelPolicy.Profiles {
		profiles[profile.Name] = profile
	}
	seen := map[string]bool{}
	var choices []modelpolicy.Profile
	names := []string{rule.CheapProfile, rule.EscalatedProfile}
	if role == "fixer" && c.ModelPolicy.DecisionEvidenceVersion == 2 && c.ModelPolicy.Calibration != nil {
		names = append(names, c.ModelPolicy.Calibration.Candidate.Name)
	}
	for _, name := range names {
		choice, ok := profiles[name]
		if !ok || choice.Runtime != base.Runtime || choice.Provider != base.Provider {
			continue
		}
		key := choice.Model + "\x00" + choice.Effort
		if key == base.Model+"\x00"+base.Effort || seen[key] {
			continue
		}
		seen[key] = true
		choices = append(choices, choice)
	}
	sort.Slice(choices, func(i, j int) bool {
		if choices[i].Model != choices[j].Model {
			return choices[i].Model < choices[j].Model
		}
		return choices[i].Effort < choices[j].Effort
	})
	return choices
}

func (c Config) validateModelPolicy() error {
	if c.ModelPolicy == nil {
		return nil
	}
	policy := *c.ModelPolicy
	if policy.Version != 1 || len(policy.Rules) == 0 {
		return errors.New("invalid adaptive model policy")
	}
	used := map[string]bool{}
	if policy.Calibration != nil {
		base, ok := c.configuredRoute("fixer")
		calibration := policy.Calibration
		if !ok || calibration.Role != "fixer" || calibration.Baseline.Name != policy.Rules["fixer"].DefaultProfile || calibration.Candidate.Model == calibration.Baseline.Model && calibration.Candidate.Effort == calibration.Baseline.Effort || calibration.Baseline.Runtime != base.Runtime || calibration.Candidate.Runtime != base.Runtime || calibration.Baseline.Provider != base.Provider || calibration.Candidate.Provider != base.Provider {
			return errors.New("calibration must use exact same-runtime/provider fixer profiles and a distinct candidate")
		}
		used[calibration.Baseline.Name] = true
		used[calibration.Candidate.Name] = true
	}
	for role, rule := range policy.Rules {
		base, ok := c.configuredRoute(role)
		if !ok {
			return fmt.Errorf("adaptive model policy names unconfigured role %q", role)
		}
		if rule.ContextEscalationBytes < 1 || rule.ContextEscalationBytes > modelpolicy.MaxContextBytes {
			return errors.New("adaptive model policy requires a bounded byte threshold")
		}
		if rule.CheapProfile != "" && (role != "explorer" || rule.CheapContextBytes < 1 || rule.CheapContextBytes > modelpolicy.MaxContextBytes) {
			return fmt.Errorf("adaptive cheap profile for %q requires an explorer route and a bounded byte threshold", role)
		}
		decision, err := modelpolicy.Select(policy, modelpolicy.Request{Role: role, Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium, Uncertainty: modelpolicy.LevelMedium})
		if err != nil {
			return errors.New("invalid adaptive model policy")
		}
		if decision.Profile.Name != rule.DefaultProfile || decision.Profile.Runtime != base.Runtime || decision.Profile.Provider != base.Provider || decision.Profile.Model != base.Model || decision.Profile.Effort != base.Effort {
			return fmt.Errorf("adaptive default for %q must match its configured route", role)
		}
		names := []string{rule.DefaultProfile, rule.CheapProfile, rule.EscalatedProfile}
		if role == "fixer" && policy.Calibration != nil {
			names = append(names, policy.Calibration.Candidate.Name)
		}
		for _, name := range names {
			if name == "" {
				continue
			}
			used[name] = true
			var choice *modelpolicy.Profile
			for i := range policy.Profiles {
				if policy.Profiles[i].Name == name {
					choice = &policy.Profiles[i]
					break
				}
			}
			if choice == nil || choice.Runtime != base.Runtime || choice.Provider != base.Provider {
				return fmt.Errorf("adaptive choice for %q changes runtime or provider", role)
			}
			selected := runtime.Profile{Runtime: choice.Runtime, Provider: choice.Provider, Model: choice.Model, Effort: choice.Effort, Role: role}
			if err := selected.Validate(); err != nil {
				return fmt.Errorf("invalid adaptive profile for %q", role)
			}
			if selected.Runtime == "provider-api" || selected.Runtime == "opencode-http" {
				resolved, err := c.Provider.ResolveRoleProfile(role, selected)
				if err != nil {
					return fmt.Errorf("adaptive provider profile for %q is not configured: %w", role, err)
				}
				reservedTokens, reservedCost, err := resolved.Model.ConservativeReservation()
				if err != nil {
					return err
				}
				limit := c.Access.Invocations[role]
				if limit.UnlimitedTokens && limit.Tokens != 0 || !limit.UnlimitedTokens && limit.Tokens < reservedTokens {
					return fmt.Errorf("adaptive provider token reservation for %q is insufficient", role)
				}
				accessName := c.Access.Roles[role]
				for _, accessProfile := range c.Access.Profiles {
					if accessProfile.Name == accessName && accessProfile.Kind == "api" && (limit.CostMicroUSD == nil || reservedCost == nil || *limit.CostMicroUSD < *reservedCost) {
						return fmt.Errorf("adaptive provider cost reservation for %q is insufficient", role)
					}
				}
			}
		}
	}
	for _, profile := range policy.Profiles {
		if !used[profile.Name] {
			return errors.New("adaptive model profile is not referenced by a routing rule")
		}
	}
	return nil
}
