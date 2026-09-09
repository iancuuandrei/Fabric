package control

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

func modelAccessSnapshot(t *testing.T, kind string) Snapshot {
	t.Helper()
	c := creation(t)
	c.Config.Version = 2
	roles := []string{"planner", "explorer", "writer", "fixer", "reviewer"}
	profiles := make(map[string]runtime.Profile, len(roles))
	accessRoles := make(map[string]string, len(roles))
	limits := make(map[string]config.InvocationLimit, len(roles))
	accessProfiles := make([]access.Profile, 0, len(roles))
	var totalCost int64
	for n, role := range roles {
		profile := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: role + "-model", Effort: "effort-" + role, Role: role}
		profiles[role] = profile
		name := role + "-access"
		accessRoles[role] = name
		limit := config.InvocationLimit{Tokens: int64((n + 1) * 100)}
		ap := access.Profile{Version: 1, Name: name, Kind: kind, Runtime: "fake", Provider: "deterministic", RepositoryClasses: []access.Class{access.Private}}
		if kind == "api" {
			cost := int64((n + 1) * 10)
			limit.CostMicroUSD = &cost
			totalCost += cost
			ap.CredentialRef = "FIXTURE_KEY"
		} else {
			ap.AuthMode = "fixture-session"
		}
		limits[role] = limit
		accessProfiles = append(accessProfiles, ap)
	}
	c.Config.Planner = profiles["planner"]
	c.Config.Explorer = modelAccessProfilePointer(profiles["explorer"])
	c.Config.Writer = modelAccessProfilePointer(profiles["writer"])
	c.Config.Fixer = modelAccessProfilePointer(profiles["fixer"])
	c.Config.Reviewer = modelAccessProfilePointer(profiles["reviewer"])
	runLimits := access.Limits{Tokens: 2000, Concurrency: 5}
	if kind == "api" {
		runLimits.CostMicroUSD = &totalCost
	}
	c.Config.Access = &config.Access{Class: access.Private, Limits: runLimits, Profiles: accessProfiles, Roles: accessRoles, Invocations: limits}
	if err := c.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	return Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
}

func modelAccessProfilePointer(profile runtime.Profile) *runtime.Profile { return &profile }

func TestDeriveModelAccessIntentAllRoles(t *testing.T) {
	s := modelAccessSnapshot(t, "subscription")
	for n, role := range []string{"planner", "explorer", "writer", "fixer", "reviewer"} {
		t.Run(role, func(t *testing.T) {
			profile, err := s.Creation.Config.Route(role)
			if err != nil {
				t.Fatal(err)
			}
			input := "exact input for " + role
			invocation, err := runtime.NewInvocation(profile, input)
			if err != nil {
				t.Fatal(err)
			}
			intent, err := deriveModelAccessIntent(s, invocation, n+1)
			if err != nil {
				t.Fatal(err)
			}
			inputID, _ := access.InputID(input)
			intentID, idErr := intent.ID()
			if idErr != nil || intent.Reservation.InvocationID != intentID || intent.InputHash != inputID || intent.Attempt != n+1 {
				t.Fatal("intent does not bind exact input, attempt, and identity", idErr)
			}
			if intent.Route.Role != role || intent.Route.Runtime != profile.Runtime || intent.Route.Provider != profile.Provider || intent.Route.Model != profile.Model || intent.Route.Effort != profile.Effort {
				t.Fatal("intent route differs from configured runtime profile")
			}
			if intent.Reservation.Tokens != int64((n+1)*100) || intent.Reservation.CostMicroUSD != nil || intent.Reservation.BillingMode != "subscription" {
				t.Fatal("intent reservation differs from configured role limit or billing")
			}
		})
	}
}

func TestCodexRuntimeUsagePolicyUsesAuthorizedRoleReservation(t *testing.T) {
	s := modelAccessSnapshot(t, "subscription")
	s.Creation.Config.Codex = &config.Codex{RequireLiveUsage: true, UsageQualified: true}
	for n, role := range []string{"planner", "explorer", "writer", "fixer", "reviewer"} {
		budget, strict, qualified, unlimited, err := codexRuntimeUsagePolicy(s, role)
		if err != nil || budget != int64((n+1)*100) || !strict || !qualified || unlimited {
			t.Fatal("Codex adapter policy differs from the authorized role reservation", role, budget, strict, qualified, unlimited, err)
		}
	}

	unqualified := s
	unqualified.Creation.Config.Codex = &config.Codex{RequireLiveUsage: true}
	if _, _, _, _, err := codexRuntimeUsagePolicy(unqualified, "planner"); err == nil {
		t.Fatal("strict Codex live usage admitted without operator qualification")
	}

	missing := s
	missing.Creation.Config.Access = nil
	if _, _, _, _, err := codexRuntimeUsagePolicy(missing, "planner"); err == nil {
		t.Fatal("strict Codex live usage admitted without an authorized reservation")
	}
}

func TestCodexRuntimeUsagePolicyPropagatesExplicitUnlimitedReservation(t *testing.T) {
	s := modelAccessSnapshot(t, "subscription")
	s.Creation.Config.Codex = &config.Codex{RequireLiveUsage: true, UsageQualified: true}
	s.Creation.Config.Access.Limits = access.Limits{UnlimitedTokens: true, Concurrency: 5}
	limit := s.Creation.Config.Access.Invocations["planner"]
	limit.Tokens = 0
	limit.UnlimitedTokens = true
	s.Creation.Config.Access.Invocations["planner"] = limit

	budget, strict, qualified, unlimited, err := codexRuntimeUsagePolicy(s, "planner")
	if err != nil || budget != 0 || !strict || !qualified || !unlimited {
		t.Fatal("explicit unlimited reservation was not projected to Codex", budget, strict, qualified, unlimited, err)
	}
	// The shared access fixture uses deterministic fake routes. Remove the
	// helper-only Codex policy before validating the independent access intent.
	s.Creation.Config.Codex = nil
	profile, err := s.Creation.Config.Route("planner")
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(profile, "unlimited exact input")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := deriveModelAccessIntent(s, invocation, 1)
	if err != nil || intent.Reservation.Tokens != 0 || !intent.Reservation.UnlimitedTokens {
		t.Fatal("controller intent lost unlimited token authority", intent.Reservation, err)
	}
}

func TestDeriveModelAccessIntentRejectsMutatedInvocation(t *testing.T) {
	s := modelAccessSnapshot(t, "subscription")
	profile, _ := s.Creation.Config.Route("writer")
	original, _ := runtime.NewInvocation(profile, "write exact candidate")
	mutations := map[string]func(runtime.Invocation) runtime.Invocation{
		"version": func(i runtime.Invocation) runtime.Invocation { i.Version++; return i },
		"id":      func(i runtime.Invocation) runtime.Invocation { i.ID = strings.Repeat("b", 64); return i },
		"input":   func(i runtime.Invocation) runtime.Invocation { i.Input += " changed"; return i },
		"profile": func(i runtime.Invocation) runtime.Invocation { i.Profile.Model = "other-model"; return i },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := deriveModelAccessIntent(s, mutate(original), 1); err == nil {
				t.Fatal("mutated invocation admitted")
			}
		})
	}
	changed := profile
	changed.Model = "other-model"
	rebound, _ := runtime.NewInvocation(changed, original.Input)
	if _, err := deriveModelAccessIntent(s, rebound, 1); err == nil {
		t.Fatal("valid invocation outside configured role route admitted")
	}
	for _, attempt := range []int{0, 1000001} {
		if _, err := deriveModelAccessIntent(s, original, attempt); err == nil {
			t.Fatal("invalid attempt admitted", attempt)
		}
	}
}

func TestDeriveModelAccessIntentRejectsMissingPolicyBindings(t *testing.T) {
	base := modelAccessSnapshot(t, "subscription")
	planner, _ := base.Creation.Config.Route("planner")
	invocation, _ := runtime.NewInvocation(planner, "plan")

	tests := map[string]func(*Snapshot){
		"absent route":               func(s *Snapshot) { s.Creation.Config.Access.Roles["planner"] = "missing" },
		"mismatched profile runtime": func(s *Snapshot) { s.Creation.Config.Access.Profiles[0].Runtime = "codex-app-server" },
		"absent limit":               func(s *Snapshot) { delete(s.Creation.Config.Access.Invocations, "planner") },
		"limit beyond run budget": func(s *Snapshot) {
			s.Creation.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: 2001}
		},
		"subscription monetary limit": func(s *Snapshot) {
			cost := int64(1)
			s.Creation.Config.Access.Invocations["planner"] = config.InvocationLimit{Tokens: 100, CostMicroUSD: &cost}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := modelAccessSnapshot(t, "subscription")
			mutate(&s)
			if _, err := deriveModelAccessIntent(s, invocation, 1); err == nil {
				t.Fatal("invalid access binding admitted")
			}
		})
	}

	absent := base
	absent.Creation.Config.Explorer = nil
	explorerProfile := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explorer-model", Effort: "effort-explorer", Role: "explorer"}
	explorer, _ := runtime.NewInvocation(explorerProfile, "explore")
	if _, err := deriveModelAccessIntent(absent, explorer, 1); err == nil {
		t.Fatal("absent configured role admitted")
	}
}

func TestDeriveModelAccessIntentPreservesAPIBillingCeiling(t *testing.T) {
	s := modelAccessSnapshot(t, "api")
	profile, _ := s.Creation.Config.Route("reviewer")
	invocation, _ := runtime.NewInvocation(profile, "review exact candidate")
	intent, err := deriveModelAccessIntent(s, invocation, 7)
	if err != nil {
		t.Fatal(err)
	}
	if intent.Reservation.BillingMode != "api" || intent.Reservation.CostMicroUSD == nil || *intent.Reservation.CostMicroUSD != 50 {
		t.Fatal("configured API billing ceiling was not preserved")
	}
	*s.Creation.Config.Access.Invocations["reviewer"].CostMicroUSD = 999
	if *intent.Reservation.CostMicroUSD != 50 {
		t.Fatal("derived intent aliases mutable configuration cost")
	}

	missingCost := modelAccessSnapshot(t, "api")
	missingCost.Creation.Config.Access.Invocations["reviewer"] = config.InvocationLimit{Tokens: 500}
	if _, err := deriveModelAccessIntent(missingCost, invocation, 1); err == nil {
		t.Fatal("API route without configured cost ceiling admitted")
	}
}
