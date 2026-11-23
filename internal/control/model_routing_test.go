package control

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func adaptivePlannerPolicy(base runtime.Profile, bytes int64) *modelpolicy.Policy {
	return &modelpolicy.Policy{
		Version: 1,
		Profiles: []modelpolicy.Profile{
			{Name: "standard", Runtime: base.Runtime, Provider: base.Provider, Model: base.Model, Effort: base.Effort},
			{Name: "escalated", Runtime: base.Runtime, Provider: base.Provider, Model: "planner-strong", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{
			"planner": {DefaultProfile: "standard", EscalatedProfile: "escalated", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: bytes, FailureEscalationCount: 1},
		},
	}
}

func TestAdaptivePlannerInvocationRoutesFromExactFinalInputBytes(t *testing.T) {
	c := creation(t)
	c.Config.PlannerContract = plannerContractV1
	c.Config.ModelPolicy = adaptivePlannerPolicy(c.Config.Planner, 100)
	objective := strings.Repeat("x", 150)
	i, err := plannerInvocation(c.Config, objective)
	if err != nil {
		t.Fatal(err)
	}
	if i.Profile.Model != "planner-strong" || i.Profile.Effort != "high" {
		t.Fatalf("large final planner input did not select configured escalation: %+v", i.Profile)
	}
	if i.Input == objective || len(i.Input) < len(objective) {
		t.Fatal("planner contract wrapper was not included in byte-based selection")
	}

	legacy := c.Config
	legacy.ModelPolicy = nil
	legacy.PlannerContract = ""
	legacyInvocation, err := plannerInvocation(legacy, objective)
	if err != nil {
		t.Fatal(err)
	}
	want, err := runtime.NewInvocation(legacy.Planner, objective)
	if err != nil || legacyInvocation != want {
		t.Fatalf("nil model policy changed legacy planner input or identity: %v", err)
	}
}

func TestRoutingDecisionEvidenceIsOptInAndBindsExactInput(t *testing.T) {
	c := creation(t)
	c.Config.PlannerContract = plannerContractV1
	c.Config.ModelPolicy = adaptivePlannerPolicy(c.Config.Planner, 80)
	input := strings.Repeat("x", 90)

	legacy, evidence, err := modelSelectionForInput(c.Config, "planner", input, 0)
	if err != nil || legacy.Model != "planner-strong" || evidence != nil {
		t.Fatalf("default routing changed or unexpectedly emitted evidence: %+v %+v %v", legacy, evidence, err)
	}
	c.Config.ModelPolicy.DecisionEvidenceVersion = 1
	selected, evidence, err := modelSelectionForInput(c.Config, "planner", input, 0)
	if err != nil || selected != legacy || evidence == nil {
		t.Fatalf("opt-in evidence changed selection or was absent: %+v %+v %v", selected, evidence, err)
	}
	configID, err := c.Config.ID()
	if err != nil || evidence.ConfigID != configID || evidence.ProfileName != "escalated" || evidence.Model != selected.Model || evidence.Effort != selected.Effort || evidence.ContextBytes != int64(len(input)) || evidence.AcceptedFailures != 0 || evidence.Reason != "large-input-bytes" {
		t.Fatalf("routing evidence does not describe the exact configured decision: %+v %v", evidence, err)
	}
	snapshot := Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
	invocation, err := runtime.NewInvocation(selected, input)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := modelRoutingDecisionForInvocation(snapshot, invocation)
	if err != nil || !sameCanonical(derived, evidence) {
		t.Fatalf("controller invocation evidence was not reproducible: %+v %v", derived, err)
	}
}

func TestAdaptiveWriterRoutingIgnoresUnknownGraphAttempt(t *testing.T) {
	c := creation(t)
	base := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer-standard", Effort: "medium", Role: "writer"}
	c.Config.Writer = &base
	c.Config.ModelPolicy = &modelpolicy.Policy{
		Version: 1,
		Profiles: []modelpolicy.Profile{
			{Name: "standard", Runtime: base.Runtime, Provider: base.Provider, Model: base.Model, Effort: base.Effort},
			{Name: "escalated", Runtime: base.Runtime, Provider: base.Provider, Model: "writer-strong", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{
			"writer": {DefaultProfile: "standard", EscalatedProfile: "escalated", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1},
		},
	}
	graph := validGraphFixture()
	for i := range graph.Tasks {
		if graph.Tasks[i].ID == "impl" {
			graph.Tasks[i].Attempts = []engineeringplan.Attempt{{ID: "attempt-unknown", Outcome: engineeringplan.AttemptUnknown}}
		}
	}
	s := Snapshot{Creation: c, Graph: &GraphState{Graph: graph}}
	input := "final writer input"
	if got := modelFailureCount(s, "writer", input); got != 0 {
		t.Fatalf("unknown attempt counted as a completed failure: %d", got)
	}
	selected, err := modelProfileForSnapshot(s, "writer", input)
	if err != nil || selected != base {
		t.Fatalf("unknown attempt changed configured writer profile: %+v %v", selected, err)
	}

	for i := range s.Graph.Graph.Tasks {
		if s.Graph.Graph.Tasks[i].ID == "impl" {
			s.Graph.Graph.Tasks[i].Attempts = append(s.Graph.Graph.Tasks[i].Attempts, engineeringplan.Attempt{ID: "attempt-failed", Outcome: engineeringplan.AttemptFailed})
		}
	}
	if got := modelFailureCount(s, "writer", input); got != 1 {
		t.Fatalf("completed failed attempt was not counted: %d", got)
	}
	selected, err = modelProfileForSnapshot(s, "writer", input)
	if err != nil || selected.Model != "writer-strong" || selected.Effort != "high" {
		t.Fatalf("completed failure did not select configured escalation: %+v %v", selected, err)
	}
}

func TestConfigRejectsCheapProfileForWriteRole(t *testing.T) {
	c := creation(t)
	base := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer-standard", Effort: "medium", Role: "writer"}
	c.Config.Writer = &base
	c.Config.ModelPolicy = &modelpolicy.Policy{
		Version: 1,
		Profiles: []modelpolicy.Profile{
			{Name: "standard", Runtime: base.Runtime, Provider: base.Provider, Model: base.Model, Effort: base.Effort},
			{Name: "economy", Runtime: base.Runtime, Provider: base.Provider, Model: "writer-economy", Effort: "low"},
			{Name: "strong", Runtime: base.Runtime, Provider: base.Provider, Model: "writer-strong", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{
			"writer": {DefaultProfile: "standard", CheapProfile: "economy", CheapContextBytes: 1024, EscalatedProfile: "strong", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1},
		},
	}
	if err := c.Config.Validate(); err == nil {
		t.Fatal("configuration admitted an economy route for a mutating writer role")
	}
}

func TestCalibratedFixerRouteBindsObjectiveAndPreservesStaticEscalation(t *testing.T) {
	c := creation(t)
	c.Config.Version = 2
	baseline := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixer-luna", Effort: "high", Role: "fixer"}
	c.Config.Fixer = &baseline
	calibration := controlFixtureCalibration(baseline, c.Objective)
	c.Config.Access = &config.Access{
		Class:       access.Private,
		Limits:      access.Limits{Tokens: 2000, Concurrency: 1},
		Profiles:    []access.Profile{{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture", RepositoryClasses: []access.Class{access.Private}}},
		Roles:       map[string]string{"planner": "fixture", "fixer": "fixture"},
		Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 1000}, "fixer": {Tokens: 1000}},
	}
	c.Config.ModelPolicy = &modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 2, Calibration: &calibration,
		Profiles: []modelpolicy.Profile{
			calibration.Baseline,
			calibration.Candidate,
			{Name: "strong", Runtime: baseline.Runtime, Provider: baseline.Provider, Model: "fixer-strong", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{"fixer": {DefaultProfile: "luna", EscalatedProfile: "strong", ContextEscalationTokens: modelpolicy.MaxContextTokens, ContextEscalationBytes: modelpolicy.MaxContextBytes, FailureEscalationCount: 1}},
	}
	if err := c.Config.Validate(); err != nil {
		t.Fatalf("valid calibration configuration rejected: %v", err)
	}
	s := Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
	input := "fixer invocation input"
	selected, err := modelProfileForSnapshot(s, "fixer", input)
	if err != nil || selected.Model != calibration.Candidate.Model {
		t.Fatalf("in-scope candidate was not selected for static-default fixer: %+v %v", selected, err)
	}
	invocation, err := runtime.NewInvocation(selected, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := modelRoutingDecisionForInvocation(s, invocation)
	objectiveHash, hashErr := modelpolicy.ObjectiveDigest(c.Objective)
	if err != nil || hashErr != nil || evidence == nil || evidence.Version != 2 || evidence.ObjectiveHash != objectiveHash || evidence.Reason != "calibrated-candidate" || evidence.Calibration == nil || !evidence.Calibration.CandidateSelected || !evidence.Calibration.CandidateApplied || !evidence.Calibration.ScopeMatched {
		t.Fatalf("calibrated evidence is not bound to exact objective/selection: %+v %v %v", evidence, err, hashErr)
	}
	if evidence.Calibration.Training.Baseline.Assigned != 1 || evidence.Calibration.Training.Candidate.Accepted != 1 || evidence.Calibration.Holdout.Candidate.Accepted != 1 {
		t.Fatalf("compact calibration counts omitted outcomes: %+v", evidence.Calibration)
	}
	if err := validateRoutingDecisionForSnapshot(s, invocation, evidence); err != nil {
		t.Fatalf("exact recorded objective was rejected: %v", err)
	}
	plannerInvocation, err := runtime.NewInvocation(s.Creation.Config.Planner, "legacy planner input")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRoutingDecisionForSnapshot(s, plannerInvocation, nil); err != nil {
		t.Fatalf("unconfigured planner role was incorrectly made dependent on fixer calibration: %v", err)
	}
	wrongObjective := s
	wrongObjective.Creation.Objective = "unlisted objective"
	if err := validateRoutingDecisionForSnapshot(wrongObjective, invocation, evidence); err == nil {
		t.Fatal("recorded objective digest substitution was accepted")
	}

	outside := s
	outside.Creation.Objective = "unlisted objective"
	baselineSelected, outsideEvidence, err := modelSelectionForSnapshot(outside, "fixer", input, 0)
	if err != nil || baselineSelected != baseline || outsideEvidence == nil || outsideEvidence.Reason != "calibration-objective-out-of-scope" || outsideEvidence.Calibration == nil || outsideEvidence.Calibration.ScopeMatched || outsideEvidence.Calibration.CandidateApplied {
		t.Fatalf("out-of-scope task did not fall back to baseline: %+v %+v %v", baselineSelected, outsideEvidence, err)
	}
	failed, failedEvidence, err := modelSelectionForObjectiveHash(c.Config, "fixer", int64(len(input)), 1, objectiveHash)
	if err != nil || failed.Model != "fixer-strong" || failedEvidence == nil || failedEvidence.Reason != "prior-failures" || failedEvidence.Calibration.CandidateApplied {
		t.Fatalf("static prior-failure escalation did not take precedence: %+v %+v %v", failed, failedEvidence, err)
	}
}

func controlFixtureCalibration(baseline runtime.Profile, objective string) modelpolicy.Calibration {
	profile := func(name, model string) modelpolicy.Profile {
		return modelpolicy.Profile{Name: name, Runtime: baseline.Runtime, Provider: baseline.Provider, Model: model, Effort: baseline.Effort}
	}
	baseProfile, candidate := profile("luna", baseline.Model), profile("sol", "fixer-sol")
	trainHash, _ := modelpolicy.ObjectiveDigest(objective)
	holdoutHash, _ := modelpolicy.ObjectiveDigest(objective + " holdout")
	row := func(cohort, name, objectiveHash string, assigned modelpolicy.Profile, outcome string) modelpolicy.TaskPolicyOutcome {
		return modelpolicy.TaskPolicyOutcome{SourceID: "source-main", TaskID: cohort + "-task", RunID: cohort + "-" + name, ObjectiveHash: objectiveHash, NonFixerPolicyID: strings.Repeat("e", 64), AssignedProfile: assigned, ObservedFixerProfiles: []modelpolicy.Profile{assigned}, FixerInvocations: 1, Outcome: outcome, Repairs: 1}
	}
	return modelpolicy.Calibration{
		Version: modelpolicy.CalibrationVersion, FamilyID: "go-fixer", Role: "fixer", Objectives: []string{trainHash, holdoutHash},
		Baseline: baseProfile, Candidate: candidate, MinimumPerArm: 1,
		Training: []modelpolicy.TaskPolicyOutcome{row("train", "luna", trainHash, baseProfile, modelpolicy.OutcomeFailed), row("train", "sol", trainHash, candidate, modelpolicy.OutcomeAccepted)},
		Holdout:  []modelpolicy.TaskPolicyOutcome{row("holdout", "luna", holdoutHash, baseProfile, modelpolicy.OutcomeFailed), row("holdout", "sol", holdoutHash, candidate, modelpolicy.OutcomeAccepted)},
	}
}
