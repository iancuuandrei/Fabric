package config

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestCalibrationCandidateIsAdmittedOnlyForConfiguredFixerRoute(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Version = 2
	base := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "gpt-6-luna", Effort: "high", Role: "fixer"}
	c.Fixer = &base
	c.Access = routingFixtureAccess()
	calibration := routingFixtureCalibration()
	c.ModelPolicy = &modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 2, Calibration: &calibration,
		Profiles: []modelpolicy.Profile{
			calibration.Baseline,
			calibration.Candidate,
			{Name: "strong", Runtime: base.Runtime, Provider: base.Provider, Model: "gpt-6-astra", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{
			"fixer": {DefaultProfile: "luna", EscalatedProfile: "strong", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid calibration config rejected: %v", err)
	}
	candidate := runtime.Profile{Runtime: base.Runtime, Provider: base.Provider, Model: "gpt-6-sol", Effort: "high", Role: "fixer"}
	if !c.AllowsProfile(candidate) {
		t.Fatal("calibration candidate was not admitted for the exact fixer route")
	}
	choices := c.ModelChoices("fixer")
	if len(choices) != 2 || choices[0].Name != "strong" || choices[1].Name != "sol" {
		t.Fatalf("access choices do not expose both the static escalation and calibrated alternative: %#v", choices)
	}
	foreign := candidate
	foreign.Provider = "other"
	if c.AllowsProfile(foreign) {
		t.Fatal("calibration admitted a candidate from another provider")
	}
}

func TestCalibrationRequiresExplicitV2AndConfiguredBaseline(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Version = 2
	base := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "gpt-6-luna", Effort: "high", Role: "fixer"}
	c.Fixer = &base
	c.Access = routingFixtureAccess()
	calibration := routingFixtureCalibration()
	policy := &modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 1, Calibration: &calibration,
		Profiles: []modelpolicy.Profile{calibration.Baseline, calibration.Candidate, {Name: "strong", Runtime: base.Runtime, Provider: base.Provider, Model: "gpt-6-astra", Effort: "high"}},
		Rules:    map[string]modelpolicy.Rule{"fixer": {DefaultProfile: "luna", EscalatedProfile: "strong", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1}},
	}
	c.ModelPolicy = policy
	if err := c.Validate(); err == nil {
		t.Fatal("calibration accepted without decision evidence version 2")
	}
	policy.DecisionEvidenceVersion = 2
	policy.Rules["fixer"] = modelpolicy.Rule{DefaultProfile: "sol", EscalatedProfile: "strong", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1}
	if err := c.Validate(); err == nil {
		t.Fatal("calibration baseline differed from the configured default")
	}
}

func routingFixtureAccess() *Access {
	return &Access{
		Class:       access.Private,
		Limits:      access.Limits{Tokens: 2000, Concurrency: 1},
		Roles:       map[string]string{"planner": "fixture", "fixer": "fixture"},
		Invocations: map[string]InvocationLimit{"planner": {Tokens: 1000}, "fixer": {Tokens: 1000}},
		Profiles:    []access.Profile{{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture", RepositoryClasses: []access.Class{access.Private}}},
	}
}

func routingFixtureCalibration() modelpolicy.Calibration {
	baseline := modelpolicy.Profile{Name: "luna", Runtime: "fake", Provider: "deterministic", Model: "gpt-6-luna", Effort: "high"}
	candidate := modelpolicy.Profile{Name: "sol", Runtime: "fake", Provider: "deterministic", Model: "gpt-6-sol", Effort: "high"}
	objectiveTrain, objectiveHoldout := strings.Repeat("a", 64), strings.Repeat("c", 64)
	row := func(cohort, name string, objective string, profile modelpolicy.Profile, outcome string) modelpolicy.TaskPolicyOutcome {
		return modelpolicy.TaskPolicyOutcome{SourceID: "source-main", TaskID: cohort + "-task", RunID: cohort + "-" + name, ObjectiveHash: objective, NonFixerPolicyID: strings.Repeat("e", 64), AssignedProfile: profile, ObservedFixerProfiles: []modelpolicy.Profile{profile}, FixerInvocations: 1, Outcome: outcome, Repairs: 1}
	}
	return modelpolicy.Calibration{
		Version: modelpolicy.CalibrationVersion, FamilyID: "go-fixer", Role: "fixer", Objectives: []string{objectiveTrain, objectiveHoldout},
		Baseline: baseline, Candidate: candidate, MinimumPerArm: 1,
		Training: []modelpolicy.TaskPolicyOutcome{row("train", "luna", objectiveTrain, baseline, modelpolicy.OutcomeFailed), row("train", "sol", objectiveTrain, candidate, modelpolicy.OutcomeAccepted)},
		Holdout:  []modelpolicy.TaskPolicyOutcome{row("holdout", "luna", objectiveHoldout, baseline, modelpolicy.OutcomeFailed), row("holdout", "sol", objectiveHoldout, candidate, modelpolicy.OutcomeAccepted)},
	}
}
