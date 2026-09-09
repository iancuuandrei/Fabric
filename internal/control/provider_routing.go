package control

import (
	"errors"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/runtime"
)

// ProviderRouting is the complete controller-selected public authority for one
// provider-backed invocation. CredentialEnvironment names a controller-only
// secret source; it never contains credential material.
type ProviderRouting struct {
	Profile               runtime.Profile
	Policy                access.Policy
	Intent                access.Intent
	ProviderRole          config.ResolvedProviderRole
	Gateway               providergateway.Binding
	RequestExpectation    providergateway.AdapterRequestExpectation
	CredentialEnvironment string
	OpenCode              *config.OpenCodeHost
}

// ResolveProviderRouting binds one configured role and input identity to its
// exact access reservation, provider contracts and finite adapter. It never
// searches for or substitutes another role, provider, model or protocol.
func ResolveProviderRouting(c config.Config, runID, role, inputHash string, attempt int) (ProviderRouting, error) {
	profile, err := c.Route(role)
	if err != nil {
		return ProviderRouting{}, err
	}
	if profile.Runtime != "provider-api" && profile.Runtime != "opencode-http" {
		return ProviderRouting{}, errors.New("role is not configured for a provider-backed runtime")
	}
	if c.Provider == nil || c.Access == nil {
		return ProviderRouting{}, errors.New("provider routing configuration unavailable")
	}
	policy, err := c.AccessPolicy(runID)
	if err != nil {
		return ProviderRouting{}, err
	}
	var route access.Route
	foundRoute := false
	for _, candidate := range policy.Routes {
		if candidate.Role == role {
			route, foundRoute = candidate, true
			break
		}
	}
	if !foundRoute {
		return ProviderRouting{}, errors.New("provider access route unavailable")
	}
	accessName, ok := c.Access.Roles[role]
	if !ok {
		return ProviderRouting{}, errors.New("provider access profile unavailable")
	}
	var billingMode string
	for _, candidate := range c.Access.Profiles {
		if candidate.Name == accessName {
			billingMode = candidate.Kind
			break
		}
	}
	limit, ok := c.Access.Invocations[role]
	if !ok || billingMode == "" {
		return ProviderRouting{}, errors.New("provider invocation reservation unavailable")
	}
	reservation := access.Reservation{Tokens: limit.Tokens, UnlimitedTokens: limit.UnlimitedTokens, CostMicroUSD: cloneOptionalInt64(limit.CostMicroUSD), BillingMode: billingMode}
	policyID, err := policy.ID()
	if err != nil {
		return ProviderRouting{}, err
	}
	intent := access.Intent{Attempt: attempt, PolicyID: policyID, InputHash: inputHash, Route: route, Reservation: reservation}
	invocationID, err := intent.ID()
	if err != nil {
		return ProviderRouting{}, err
	}
	intent.Reservation.InvocationID = invocationID
	resolved, expectation, err := ConfiguredProviderExpectation(c, role)
	if err != nil {
		return ProviderRouting{}, err
	}
	gateway, err := providergateway.BindingForAccess(policy, intent, resolved.Endpoint, resolved.Model)
	if err != nil {
		return ProviderRouting{}, err
	}
	if _, err := providergateway.AdapterRequestExpectationID(gateway, expectation); err != nil {
		return ProviderRouting{}, err
	}
	selected := ProviderRouting{Profile: profile, Policy: policy, Intent: intent, ProviderRole: resolved, Gateway: gateway, RequestExpectation: expectation, CredentialEnvironment: resolved.CredentialEnvironment}
	if profile.Runtime == "opencode-http" {
		host := *c.OpenCode
		selected.OpenCode = &host
	}
	return selected, nil
}

// ConfiguredProviderExpectation returns the exact role controls and capability
// requirement needed to derive a direct invocation input identity before access
// and gateway identities can be constructed.
func ConfiguredProviderExpectation(c config.Config, role string) (config.ResolvedProviderRole, providergateway.AdapterRequestExpectation, error) {
	profile, err := c.Route(role)
	if err != nil {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, err
	}
	if profile.Runtime != "provider-api" && profile.Runtime != "opencode-http" || c.Provider == nil {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, errors.New("role is not configured for a provider-backed runtime")
	}
	resolved, err := c.ResolveProviderRole(role)
	if err != nil {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, err
	}
	expectation := providergateway.AdapterRequestExpectation{MaxBytes: resolved.Model.MaxRequestBytes, MaxOutputTokens: resolved.Model.MaxOutputTokens, Controls: append([]byte(nil), resolved.AdapterControls...), RequiredCapabilities: cloneRequiredCapabilities(resolved.RequiredCapabilities), ResponseFraming: resolved.ResponseFraming}
	return resolved, expectation, nil
}

func cloneOptionalInt64(source *int64) *int64 {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneRequiredCapabilities(source *providergateway.RequiredCapabilities) *providergateway.RequiredCapabilities {
	if source == nil {
		return nil
	}
	result := *source
	result.Schema = append([]byte(nil), source.Schema...)
	return &result
}
