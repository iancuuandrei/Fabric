package control

import (
	"errors"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/runtime"
)

// codexRuntimeUsagePolicy projects the immutable, role-scoped access
// reservation into a Codex adapter. UsageQualified is operator preflight
// evidence recorded in configuration; it is not an automatic capability proof.
func codexRuntimeUsagePolicy(s Snapshot, role string) (budget int64, requireLive, qualified, unlimited bool, err error) {
	c := s.Creation.Config.Codex
	if c == nil {
		return 0, false, false, false, errors.New("Codex runtime usage policy requires Codex configuration")
	}
	if s.Creation.Config.Access != nil {
		limit, ok := s.Creation.Config.Access.Invocations[role]
		if !ok || limit.UnlimitedTokens && limit.Tokens != 0 || !limit.UnlimitedTokens && limit.Tokens <= 0 {
			return 0, false, false, false, errors.New("Codex runtime usage policy lacks an authorized role reservation")
		}
		budget = limit.Tokens
		unlimited = limit.UnlimitedTokens
	}
	if c.RequireLiveUsage && budget <= 0 && !unlimited {
		return 0, false, false, false, errors.New("strict Codex live usage requires an authorized token reservation")
	}
	if c.RequireLiveUsage && !c.UsageQualified {
		return 0, false, false, false, errors.New("strict Codex live usage requires operator qualification")
	}
	return budget, c.RequireLiveUsage, c.UsageQualified, unlimited, nil
}

// deriveModelAccessIntent derives the controller-owned access intent for one
// exact runtime invocation. It is pure derivation: callers must separately
// persist the intent before dispatch and record a terminal receipt afterward.
func deriveModelAccessIntent(s Snapshot, invocation runtime.Invocation, attempt int) (access.Intent, error) {
	exact, err := runtime.NewInvocation(invocation.Profile, invocation.Input)
	if err != nil {
		return access.Intent{}, err
	}
	if invocation != exact {
		return access.Intent{}, errors.New("runtime invocation identity mismatch")
	}

	configured, err := s.Creation.Config.Route(invocation.Profile.Role)
	if err != nil {
		return access.Intent{}, err
	}
	if configured != invocation.Profile {
		return access.Intent{}, errors.New("runtime invocation differs from configured role")
	}

	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return access.Intent{}, err
	}
	policyID, err := policy.ID()
	if err != nil {
		return access.Intent{}, err
	}

	var selected access.Route
	routeCount := 0
	for _, route := range policy.Routes {
		if route.Role == invocation.Profile.Role {
			selected = route
			routeCount++
		}
	}
	if routeCount != 1 || selected.Runtime != invocation.Profile.Runtime || selected.Provider != invocation.Profile.Provider || selected.Model != invocation.Profile.Model || selected.Effort != invocation.Profile.Effort {
		return access.Intent{}, errors.New("access policy route differs from runtime invocation")
	}

	var selectedProfile access.Profile
	profileCount := 0
	for _, profile := range policy.Profiles {
		id, err := profile.ID()
		if err != nil {
			return access.Intent{}, err
		}
		if id == selected.AccessID {
			selectedProfile = profile
			profileCount++
		}
	}
	if profileCount != 1 {
		return access.Intent{}, errors.New("access policy route lacks an exact profile")
	}

	limit, ok := s.Creation.Config.Access.Invocations[invocation.Profile.Role]
	if !ok {
		return access.Intent{}, errors.New("role invocation limit missing")
	}
	var cost *int64
	if limit.CostMicroUSD != nil {
		value := *limit.CostMicroUSD
		cost = &value
	}
	inputID, err := access.InputID(invocation.Input)
	if err != nil {
		return access.Intent{}, err
	}
	intent := access.Intent{
		Attempt:   attempt,
		PolicyID:  policyID,
		InputHash: inputID,
		Route:     selected,
		Reservation: access.Reservation{
			Tokens:          limit.Tokens,
			UnlimitedTokens: limit.UnlimitedTokens,
			CostMicroUSD:    cost,
			BillingMode:     selectedProfile.Kind,
		},
	}
	intent.Reservation.InvocationID, err = intent.ID()
	if err != nil {
		return access.Intent{}, err
	}
	ledger, err := access.NewLedger(policy.Limits)
	if err != nil {
		return access.Intent{}, err
	}
	if err := ledger.Reserve(intent.Reservation); err != nil {
		return access.Intent{}, err
	}
	return intent, nil
}
