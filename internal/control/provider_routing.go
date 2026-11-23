package control

import (
	"errors"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/providergateway"
	"github.com/iancuuandrei/fabric/internal/providerruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
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
func ResolveProviderRouting(c config.Config, runID, role, inputHash string, attempt int, selectedProfiles ...runtime.Profile) (ProviderRouting, error) {
	return resolveProviderRouting(c, runID, role, inputHash, attempt, -1, nil, selectedProfiles...)
}

// resolveProviderRoutingForSnapshot binds an already-built runtime invocation
// to a routing observation rederived from the exact current controller state.
// It is the only production path that emits versioned routing evidence.
func resolveProviderRoutingForSnapshot(s Snapshot, invocation runtime.Invocation, attempt int) (ProviderRouting, error) {
	if err := invocation.Validate(); err != nil {
		return ProviderRouting{}, err
	}
	c := s.Creation.Config
	inputHash, err := providerInputIdentity(c, invocation)
	if err != nil {
		return ProviderRouting{}, err
	}
	evidence, err := modelRoutingDecisionForInvocation(s, invocation)
	if err != nil {
		return ProviderRouting{}, err
	}
	return resolveProviderRouting(c, s.RunID, invocation.Profile.Role, inputHash, attempt, int64(len(invocation.Input)), evidence, invocation.Profile)
}

// resolveProviderRoutingForRecordedEvidence validates the exact decision
// persisted with an already-dispatched provider receipt. It intentionally does
// not recount later controller failures, which may have changed after this
// invocation was admitted.
func resolveProviderRoutingForRecordedEvidence(c config.Config, runID string, invocation runtime.Invocation, attempt int, evidence *access.RoutingDecision) (ProviderRouting, error) {
	if err := invocation.Validate(); err != nil {
		return ProviderRouting{}, err
	}
	inputHash, err := providerInputIdentity(c, invocation)
	if err != nil {
		return ProviderRouting{}, err
	}
	return resolveProviderRouting(c, runID, invocation.Profile.Role, inputHash, attempt, int64(len(invocation.Input)), evidence, invocation.Profile)
}

// validateRoutingDecisionForSnapshot is used while replaying an accepted
// provider receipt. Version 2 binds the exact immutable creation objective;
// the accepted failure count remains the one persisted with that intent and
// is not recomputed from later replay state.
func validateRoutingDecisionForSnapshot(s Snapshot, invocation runtime.Invocation, evidence *access.RoutingDecision) error {
	policy := s.Creation.Config.ModelPolicy
	if policy == nil || policy.DecisionEvidenceVersion != 2 {
		return nil
	}
	if _, configured := policy.Rules[invocation.Profile.Role]; !configured {
		if evidence != nil {
			return errors.New("routing evidence is not enabled for this role")
		}
		return nil
	}
	if evidence == nil {
		return errors.New("version 2 routing evidence is missing during replay")
	}
	objectiveHash, err := modelpolicy.ObjectiveDigest(s.Creation.Objective)
	if err != nil || objectiveHash != evidence.ObjectiveHash {
		return errors.New("recorded calibration objective differs from immutable creation")
	}
	selected, expected, err := modelSelectionForObjectiveHash(s.Creation.Config, invocation.Profile.Role, int64(len(invocation.Input)), evidence.AcceptedFailures, objectiveHash)
	if err != nil || selected != invocation.Profile || !sameCanonical(expected, evidence) {
		return errors.New("recorded calibration routing decision differs from immutable policy")
	}
	return nil
}

func providerInputIdentity(c config.Config, invocation runtime.Invocation) (string, error) {
	var inputHash string
	var err error
	if invocation.Profile.Runtime == "provider-api" {
		_, expectation, resolveErr := ConfiguredProviderExpectation(c, invocation.Profile.Role, invocation.Profile)
		if resolveErr != nil {
			return "", resolveErr
		}
		direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
		inputHash, err = direct.InputHash(expectation)
	} else {
		inputHash, err = access.InputID(invocation.Input)
	}
	if err != nil {
		return "", err
	}
	return inputHash, nil
}

func resolveProviderRouting(c config.Config, runID, role, inputHash string, attempt int, inputBytes int64, routingDecision *access.RoutingDecision, selectedProfiles ...runtime.Profile) (ProviderRouting, error) {
	profile, err := c.Route(role)
	if err != nil {
		return ProviderRouting{}, err
	}
	if len(selectedProfiles) > 1 {
		return ProviderRouting{}, errors.New("one exact selected model profile expected")
	}
	if len(selectedProfiles) == 1 {
		profile = selectedProfiles[0]
		if profile.Role != role || !c.AllowsProfile(profile) {
			return ProviderRouting{}, errors.New("selected model profile is not admitted")
		}
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
	modelChoice, err := modelChoiceForProfile(c, role, profile)
	if err != nil {
		return ProviderRouting{}, err
	}
	if !route.AllowsModelChoice(modelChoice) {
		return ProviderRouting{}, errors.New("selected model choice is not in the access route")
	}
	if err := validateRoutingDecisionForProfile(c, profile, route, modelChoice, routingDecision, inputBytes); err != nil {
		return ProviderRouting{}, err
	}
	intent := access.Intent{Attempt: attempt, PolicyID: policyID, InputHash: inputHash, Route: route, ModelChoice: modelChoice, RoutingDecision: routingDecision, Reservation: reservation}
	invocationID, err := intent.ID()
	if err != nil {
		return ProviderRouting{}, err
	}
	intent.Reservation.InvocationID = invocationID
	resolved, expectation, err := ConfiguredProviderExpectation(c, role, profile)
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

func validateRoutingDecisionForProfile(c config.Config, profile runtime.Profile, route access.Route, choice *access.ModelChoice, evidence *access.RoutingDecision, inputBytes int64) error {
	policy := c.ModelPolicy
	if policy == nil || policy.Rules[profile.Role].DefaultProfile == "" || policy.DecisionEvidenceVersion == 0 {
		if evidence != nil {
			return errors.New("routing decision evidence is not enabled by configuration")
		}
		return nil
	}
	if evidence == nil {
		return errors.New("opted-in routing decision evidence is missing")
	}
	configID, err := c.ID()
	if err != nil || configID != evidence.ConfigID {
		return errors.New("routing decision configuration identity mismatch")
	}
	if err := evidence.Validate(route, choice); err != nil {
		return err
	}
	if inputBytes >= 0 && evidence.ContextBytes != inputBytes {
		return errors.New("routing decision input size differs from exact invocation")
	}
	if policy.DecisionEvidenceVersion == 2 {
		want, expected, err := modelSelectionForObjectiveHash(c, profile.Role, evidence.ContextBytes, evidence.AcceptedFailures, evidence.ObjectiveHash)
		if err != nil || want != profile || !sameCanonical(expected, evidence) {
			return errors.New("version 2 routing evidence differs from frozen calibration decision")
		}
		return nil
	}
	decision, err := modelpolicy.Select(*policy, modelpolicy.Request{
		Role: profile.Role, ReadOnly: profile.Role == "explorer", Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelMedium,
		Uncertainty: modelpolicy.LevelMedium, ContextBytes: evidence.ContextBytes, Failures: evidence.AcceptedFailures,
	})
	if err != nil {
		return err
	}
	want := runtime.Profile{Runtime: decision.Profile.Runtime, Provider: decision.Profile.Provider, Model: decision.Profile.Model, Effort: decision.Profile.Effort, Role: profile.Role}
	if decision.Profile.Name != evidence.ProfileName || decision.Reason != evidence.Reason || want != profile || decision.Profile.Model != evidence.Model || decision.Profile.Effort != evidence.Effort {
		return errors.New("routing decision evidence differs from deterministic policy selection")
	}
	return nil
}

// ConfiguredProviderExpectation returns the exact role controls and capability
// requirement needed to derive a direct invocation input identity before access
// and gateway identities can be constructed.
func ConfiguredProviderExpectation(c config.Config, role string, selectedProfiles ...runtime.Profile) (config.ResolvedProviderRole, providergateway.AdapterRequestExpectation, error) {
	profile, err := c.Route(role)
	if err != nil {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, err
	}
	if len(selectedProfiles) > 1 {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, errors.New("one exact selected model profile expected")
	}
	if len(selectedProfiles) == 1 {
		profile = selectedProfiles[0]
		if profile.Role != role || !c.AllowsProfile(profile) {
			return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, errors.New("selected model profile is not admitted")
		}
	}
	if profile.Runtime != "provider-api" && profile.Runtime != "opencode-http" || c.Provider == nil {
		return config.ResolvedProviderRole{}, providergateway.AdapterRequestExpectation{}, errors.New("role is not configured for a provider-backed runtime")
	}
	resolved, err := c.ResolveProviderRoleProfile(role, profile)
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
