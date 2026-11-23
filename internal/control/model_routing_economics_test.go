package control

import (
	"sort"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

// economicsV2Calibration reuses the frozen v1 shape from
// controlFixtureCalibration, then upgrades it to an explicitly opted-in
// version 2 calibration bound to the given creation objective. Baseline rows
// carry a complete measured expensive cost; candidate rows carry a strictly
// cheaper measured cost. Outcomes keep the candidate reliability bound above
// required/floor.
func economicsV2Calibration(baseline runtime.Profile, objective string) modelpolicy.Calibration {
	cal := controlFixtureCalibration(baseline, objective)
	cal.Version = modelpolicy.CalibrationVersion2
	epsilon, floor := 0, 0
	cal.EpsilonPPM, cal.QualityFloorPPM = &epsilon, &floor
	sort.Strings(cal.Objectives)
	for _, cohort := range [][]modelpolicy.TaskPolicyOutcome{cal.Training, cal.Holdout} {
		for i := range cohort {
			cost := int64(200)
			if cohort[i].AssignedProfile.Name == "sol" {
				cost = 100
			}
			value := cost
			cohort[i].Usage = &modelpolicy.EmpiricalUsage{CostMicroUSD: &value}
		}
	}
	return cal
}

func economicsV2Config(t *testing.T, objective string) (Creation, modelpolicy.Calibration) {
	t.Helper()
	c := creation(t)
	c.Config.Version = 2
	baseline := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixer-luna", Effort: "high", Role: "fixer"}
	c.Config.Fixer = &baseline
	c.Objective = objective
	calibration := economicsV2Calibration(baseline, objective)
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
		t.Fatalf("valid economics v2 configuration rejected: %v", err)
	}
	return c, calibration
}

func TestModelRoutingEconomicsV2SelectsCheaperCandidateEndToEnd(t *testing.T) {
	c, calibration := economicsV2Config(t, "Add a tested greeting")
	if calibration.Version != modelpolicy.CalibrationVersion2 || calibration.EpsilonPPM == nil || calibration.QualityFloorPPM == nil {
		t.Fatalf("economics fixture is not explicit version 2 with frozen epsilon/floor: %+v", calibration)
	}
	selection, err := modelpolicy.EvaluateCalibration(calibration)
	if err != nil || !selection.Selected || selection.Profile.Name != "sol" || selection.Reason != "candidate_cheaper_reliable_train_and_holdout" {
		t.Fatalf("v2 economics did not select qualifying cheaper candidate: %+v %v", selection, err)
	}
	if selection.Version != modelpolicy.CalibrationVersion2 || selection.Economics == nil || selection.Economics.EpsilonPPM != 0 || selection.Economics.QualityFloorPPM != 0 {
		t.Fatalf("v2 epsilon/floor not embedded immutable in selection: %+v", selection.Economics)
	}
	second, err := modelpolicy.EvaluateCalibration(calibration)
	if err != nil || second.Digest != selection.Digest {
		t.Fatalf("v2 digest not deterministic: %q %q %v", selection.Digest, second.Digest, err)
	}
	s := Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
	input := "fixer invocation input"
	selected, err := modelProfileForSnapshot(s, "fixer", input)
	if err != nil || selected.Model != "fixer-sol" {
		t.Fatalf("in-scope cheap candidate not routed: %+v %v", selected, err)
	}
	invocation, err := runtime.NewInvocation(selected, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := modelRoutingDecisionForInvocation(s, invocation)
	objectiveHash, hashErr := modelpolicy.ObjectiveDigest(c.Objective)
	if err != nil || hashErr != nil || evidence == nil || evidence.Version != 2 || evidence.ObjectiveHash != objectiveHash || evidence.Reason != "calibrated-candidate" {
		t.Fatalf("calibrated evidence not bound to exact objective/selection: %+v %v %v", evidence, err, hashErr)
	}
	if evidence.Calibration == nil || !evidence.Calibration.CandidateSelected || !evidence.Calibration.CandidateApplied || !evidence.Calibration.ScopeMatched || evidence.Calibration.Digest != selection.Digest || evidence.Calibration.SelectionReason != "candidate_cheaper_reliable_train_and_holdout" {
		t.Fatalf("calibrated evidence does not carry exact v2 decision: %+v", evidence.Calibration)
	}
	if err := validateRoutingDecisionForSnapshot(s, invocation, evidence); err != nil {
		t.Fatalf("exact recorded v2 routing evidence rejected on replay: %v", err)
	}
	// Rederived evidence from the immutable snapshot must match the recorded
	// evidence through the controller seam, not a byte comparison.
	rederived, err := modelRoutingDecisionForInvocation(s, invocation)
	if err != nil || !sameCanonical(rederived, evidence) {
		t.Fatalf("controller evidence not reproducible from frozen config: %+v %v", rederived, err)
	}
}

func TestModelRoutingEconomicsV2StaticEscalationWins(t *testing.T) {
	c, _ := economicsV2Config(t, "Add a tested greeting")
	objectiveHash, err := modelpolicy.ObjectiveDigest(c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	input := "fixer invocation input"
	// Prior failures escalate to the static frontier even though the cheap
	// candidate qualifies on quality and cost.
	failed, failedEvidence, err := modelSelectionForObjectiveHash(c.Config, "fixer", int64(len(input)), 1, objectiveHash)
	if err != nil || failed.Model != "fixer-strong" || failedEvidence == nil || failedEvidence.Reason != "prior-failures" || failedEvidence.Calibration.CandidateApplied {
		t.Fatalf("static prior-failure escalation did not win: %+v %+v %v", failed, failedEvidence, err)
	}
	// Large context/input bytes escalate the same way.
	big, bigEvidence, err := modelSelectionForObjectiveHash(c.Config, "fixer", modelpolicy.MaxContextBytes, 0, objectiveHash)
	if err != nil || big.Model != "fixer-strong" || bigEvidence == nil || bigEvidence.Calibration.CandidateApplied {
		t.Fatalf("static large-context escalation did not win: %+v %+v %v", big, bigEvidence, err)
	}
	if bigEvidence.Reason != "large-input-bytes" && bigEvidence.Reason != "large-context" {
		t.Fatalf("unexpected static context reason: %+v", bigEvidence)
	}
	// High-risk policy input escalates at the policy seam; calibrated routing
	// only substitutes on the default-configured profile, so high risk wins.
	policy := *c.Config.ModelPolicy
	decision, err := modelpolicy.Select(policy, modelpolicy.Request{Role: "fixer", Complexity: modelpolicy.LevelMedium, Risk: modelpolicy.LevelHigh, Uncertainty: modelpolicy.LevelMedium})
	if err != nil || decision.Profile.Name != "strong" || decision.Reason != "high-risk" {
		t.Fatalf("high-risk static escalation changed: %+v %v", decision, err)
	}
}

func TestModelRoutingEconomicsV2OutOfScopeKeepsBaseline(t *testing.T) {
	c, _ := economicsV2Config(t, "Add a tested greeting")
	s := Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
	outside := s
	outside.Creation.Objective = "unlisted objective"
	input := "fixer invocation input"
	selected, evidence, err := modelSelectionForSnapshot(outside, "fixer", input, 0)
	if err != nil || selected.Model != "fixer-luna" || evidence == nil || evidence.Reason != "calibration-objective-out-of-scope" || evidence.Calibration == nil || evidence.Calibration.ScopeMatched || evidence.Calibration.CandidateApplied {
		t.Fatalf("out-of-scope task did not fall back to baseline: %+v %+v %v", selected, evidence, err)
	}
	invocation, err := runtime.NewInvocation(selected, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRoutingDecisionForSnapshot(outside, invocation, evidence); err != nil {
		t.Fatalf("out-of-scope baseline evidence rejected: %v", err)
	}
}

func TestModelRoutingEconomicsV2SubstitutionRejectedByRederivedEvidence(t *testing.T) {
	c, calibration := economicsV2Config(t, "Add a tested greeting")
	s := Snapshot{RunID: strings.Repeat("a", 64), Creation: c}
	input := "fixer invocation input"
	selected, err := modelProfileForSnapshot(s, "fixer", input)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(selected, input)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := modelRoutingDecisionForInvocation(s, invocation)
	if err != nil {
		t.Fatal(err)
	}
	// Substituted digest rejected by replay against frozen configuration.
	substituted := *evidence
	substitutedDigest := strings.Repeat("f", 64)
	if substitutedDigest == evidence.Calibration.Digest {
		substitutedDigest = strings.Repeat("0", 64)
	}
	substituted.Calibration = &access.CalibrationDecision{
		Digest: substitutedDigest, FamilyID: evidence.Calibration.FamilyID,
		BaselineProfile: evidence.Calibration.BaselineProfile, CandidateProfile: evidence.Calibration.CandidateProfile,
		SelectionReason: evidence.Calibration.SelectionReason, ScopeMatched: evidence.Calibration.ScopeMatched,
		CandidateSelected: evidence.Calibration.CandidateSelected, CandidateApplied: evidence.Calibration.CandidateApplied,
		Training: evidence.Calibration.Training, Holdout: evidence.Calibration.Holdout,
	}
	if err := validateRoutingDecisionForSnapshot(s, invocation, &substituted); err == nil {
		t.Fatal("substituted calibration digest accepted by replay")
	}
	// Changed epsilon parameter rejected: rederived evidence from the mutated
	// frozen policy differs from the recorded evidence.
	mutated := c
	mutatedCalibration := calibration
	changed := *mutatedCalibration.EpsilonPPM + 1
	if changed > modelpolicy.MaxQualityPPM {
		changed = *mutatedCalibration.EpsilonPPM - 1
	}
	mutatedCalibration.EpsilonPPM = &changed
	mutated.Config.ModelPolicy.Calibration = &mutatedCalibration
	if err := mutated.Config.Validate(); err != nil {
		t.Fatalf("changed-epsilon config unexpectedly invalid: %v", err)
	}
	objectiveHash, err := modelpolicy.ObjectiveDigest(c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	_, expected, err := modelSelectionForObjectiveHash(mutated.Config, "fixer", int64(len(input)), evidence.AcceptedFailures, objectiveHash)
	if err != nil {
		t.Fatal(err)
	}
	if sameCanonical(expected, evidence) {
		t.Fatal("changed epsilon parameter reproduced identical routing evidence")
	}
	if err := validateRoutingDecisionForSnapshot(Snapshot{RunID: s.RunID, Creation: mutated}, invocation, evidence); err == nil {
		t.Fatal("recorded evidence accepted under changed epsilon parameter")
	}
	// Wrong-objective replay rejected by the same seam.
	wrong := s
	wrong.Creation.Objective = "unlisted objective"
	if err := validateRoutingDecisionForSnapshot(wrong, invocation, evidence); err == nil {
		t.Fatal("recorded objective substitution accepted by replay")
	}
}

func TestModelRoutingEconomicsV2PreservesV1Legacy(t *testing.T) {
	baseline := runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixer-luna", Effort: "high", Role: "fixer"}
	legacy := controlFixtureCalibration(baseline, "Add a tested greeting")
	if legacy.Version != modelpolicy.CalibrationVersion {
		t.Fatalf("legacy fixture version changed: %d", legacy.Version)
	}
	selection, err := modelpolicy.EvaluateCalibration(legacy)
	if err != nil || !selection.Selected || selection.Economics != nil || selection.Reason != "candidate_quality_improved_train_and_holdout" {
		t.Fatalf("v1 legacy selection changed: %+v %v", selection, err)
	}
}
