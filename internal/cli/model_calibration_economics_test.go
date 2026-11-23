package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
)

// cliEconomicsV2Fixture reuses the frozen v1 shape from cliCalibrationFixture,
// then upgrades it to an explicitly opted-in version 2 calibration with
// complete measured costs: expensive baseline, strictly cheaper candidate,
// and qualifying ACCEPTED/FAILED outcomes in both cohorts.
func cliEconomicsV2Fixture(baseline, candidate modelpolicy.Profile) modelpolicy.Calibration {
	cal := cliCalibrationFixture(baseline, candidate)
	cal.Version = modelpolicy.CalibrationVersion2
	epsilon, floor := 0, 0
	cal.EpsilonPPM, cal.QualityFloorPPM = &epsilon, &floor
	fixtureID := strings.Repeat("e", 64)
	for _, cohort := range [][]modelpolicy.TaskPolicyOutcome{cal.Training, cal.Holdout} {
		for i := range cohort {
			cohort[i].NonFixerPolicyID = fixtureID
			cohort[i].ObservedFixerProfiles = []modelpolicy.Profile{cohort[i].AssignedProfile}
			cohort[i].FixerInvocations = 1
			cohort[i].Repairs = 1
			cost := int64(200)
			outcome := modelpolicy.OutcomeFailed
			if cohort[i].AssignedProfile.Name == candidate.Name {
				outcome = modelpolicy.OutcomeAccepted
				cost = 100
			}
			cohort[i].Outcome = outcome
			value := cost
			cohort[i].Usage = &modelpolicy.EmpiricalUsage{CostMicroUSD: &value}
		}
	}
	return cal
}

func cliEconomicsV2Profiles() (baseline, candidate modelpolicy.Profile) {
	baseline = modelpolicy.Profile{Name: "luna", Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium"}
	candidate = baseline
	candidate.Name, candidate.Model = "sol", "strong-model"
	return baseline, candidate
}

// writeTempJSON canonicalizes value once and stages it as a temp input file.
func writeTempJSON(t *testing.T, value any, name string) string {
	t.Helper()
	body, err := canonical.Bytes(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cliEconomicsPolicy binds a v2 calibration to the shared frontier topology.
func cliEconomicsPolicy(baseline, candidate modelpolicy.Profile, calibration *modelpolicy.Calibration) modelpolicy.Policy {
	frontier := baseline
	frontier.Name, frontier.Model = "frontier", "frontier-model"
	return modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 2, Calibration: calibration,
		Profiles: []modelpolicy.Profile{baseline, candidate, frontier},
		Rules: map[string]modelpolicy.Rule{"fixer": {
			DefaultProfile: baseline.Name, EscalatedProfile: frontier.Name,
			ContextEscalationTokens: modelpolicy.MaxContextTokens,
			ContextEscalationBytes:  modelpolicy.MaxContextBytes, FailureEscalationCount: 3,
		}},
	}
}

func TestCalibrateModelsV2ReportsCheaperReliableSelection(t *testing.T) {
	baseline, candidate := cliEconomicsV2Profiles()
	artifact := cliEconomicsV2Fixture(baseline, candidate)
	if artifact.Version != modelpolicy.CalibrationVersion2 || artifact.EpsilonPPM == nil || artifact.QualityFloorPPM == nil {
		t.Fatalf("economics v2 fixture lacks explicit version/epsilon/floor: %+v", artifact)
	}
	path := writeTempJSON(t, artifact, "calibration.json")
	var out bytes.Buffer
	if err := modelCalibrationCommand([]string{path}, &out); err != nil {
		t.Fatal(err)
	}
	var result modelpolicy.CalibrationSelection
	if err := canonical.Decode(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Selected || result.Profile.Name != candidate.Name || result.Reason != "candidate_cheaper_reliable_train_and_holdout" || result.Version != modelpolicy.CalibrationVersion2 {
		t.Fatalf("v2 cheaper candidate not reported: %+v", result)
	}
	if result.Economics == nil || result.Economics.EpsilonPPM != 0 || result.Economics.QualityFloorPPM != 0 || !result.Economics.Training.CostsComplete || !result.Economics.Holdout.CostsComplete {
		t.Fatalf("v2 epsilon/floor/costs not embedded immutable: %+v", result.Economics)
	}
	if result.Economics.Training.BaselineMeanCostMicroUSD != 200 || result.Economics.Training.CandidateMeanCostMicroUSD != 100 {
		t.Fatalf("v2 exact means not preserved: %+v", result.Economics.Training)
	}
	// Digest rederived through the real evaluation seam, not a byte comparison.
	expected, err := modelpolicy.EvaluateCalibration(artifact)
	if err != nil || expected.Digest != result.Digest {
		t.Fatalf("v2 digest not rederived from frozen artifact: %q %q %v", result.Digest, expected.Digest, err)
	}
}

func TestCalibrateModelsV2RejectsStrictInputWithoutDispatch(t *testing.T) {
	baseline, candidate := cliEconomicsV2Profiles()
	valid := cliEconomicsV2Fixture(baseline, candidate)
	negative, tooLarge := -1, modelpolicy.MaxQualityPPM+1
	cases := map[string]func(*modelpolicy.Calibration){
		"negative-epsilon":  func(c *modelpolicy.Calibration) { c.EpsilonPPM = &negative },
		"epsilon-too-large": func(c *modelpolicy.Calibration) { c.EpsilonPPM = &tooLarge },
		"missing-epsilon":   func(c *modelpolicy.Calibration) { c.EpsilonPPM = nil },
		"missing-floor":     func(c *modelpolicy.Calibration) { c.QualityFloorPPM = nil },
		"negative-floor":    func(c *modelpolicy.Calibration) { c.QualityFloorPPM = &negative },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			artifact := valid
			// Deep-copy pointer fields so table mutations do not leak.
			epsilonCopy, floorCopy := *valid.EpsilonPPM, *valid.QualityFloorPPM
			artifact.EpsilonPPM, artifact.QualityFloorPPM = &epsilonCopy, &floorCopy
			mutate(&artifact)
			path := writeTempJSON(t, artifact, "calibration.json")
			var out bytes.Buffer
			if err := modelCalibrationCommand([]string{path}, &out); err == nil || out.Len() != 0 {
				t.Fatalf("%s accepted with output %q", name, out.Bytes())
			}
		})
	}
}

func TestInitRejectsInvalidCalibrationV2ParamsBeforeWrites(t *testing.T) {
	baseline, candidate := cliEconomicsV2Profiles()
	negative := -1
	for _, name := range []string{"negative-epsilon", "missing-floor"} {
		t.Run(name, func(t *testing.T) {
			f := newFixerInitFixture(t)
			calibration := cliEconomicsV2Fixture(baseline, candidate)
			if name == "negative-epsilon" {
				calibration.EpsilonPPM = &negative
			} else {
				calibration.QualityFloorPPM = nil
			}
			policy := cliEconomicsPolicy(baseline, candidate, &calibration)
			path := writeTempJSON(t, policy, "model-policy.json")
			var out bytes.Buffer
			args := f.args("--fixer-model", "base-model", "--access-config", f.accessConfig(t), "--model-policy", path)
			if err := Execute(context.Background(), args, f.root, &out); err == nil {
				t.Fatalf("%s init accepted invalid v2 params", name)
			}
			f.assertNoWrites(t)
		})
	}
}

func TestInitEmbedsCalibrationV2EconomicsWithoutReread(t *testing.T) {
	f := newFixerInitFixture(t)
	baseline, candidate := cliEconomicsV2Profiles()
	calibration := cliEconomicsV2Fixture(baseline, candidate)
	policy := cliEconomicsPolicy(baseline, candidate, &calibration)
	path := writeTempJSON(t, policy, "model-policy.json")
	var out bytes.Buffer
	if err := Execute(context.Background(), f.args("--fixer-model", "base-model", "--access-config", f.accessConfig(t), "--model-policy", path), f.root, &out); err != nil {
		t.Fatal(err)
	}
	// Small clean seam: the frozen artifact is embedded once; later execution
	// never rereads the input file.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg := f.readConfig(t)
	if cfg.ModelPolicy == nil || cfg.ModelPolicy.Calibration == nil || cfg.ModelPolicy.Calibration.Version != modelpolicy.CalibrationVersion2 {
		t.Fatalf("embedded v2 calibration unavailable: %+v", cfg.ModelPolicy)
	}
	before, err := calibration.Digest()
	if err != nil {
		t.Fatal(err)
	}
	after, err := cfg.ModelPolicy.Calibration.Digest()
	if err != nil || before != after {
		t.Fatalf("v2 digest changed during TOML round trip: %q %q %v", before, after, err)
	}
	embedded := cfg.ModelPolicy.Calibration
	if embedded.EpsilonPPM == nil || *embedded.EpsilonPPM != 0 || embedded.QualityFloorPPM == nil || *embedded.QualityFloorPPM != 0 {
		t.Fatalf("embedded v2 epsilon/floor not retained: %+v", embedded)
	}
	if embedded.Training[0].Usage == nil || embedded.Training[0].Usage.CostMicroUSD == nil || *embedded.Training[0].Usage.CostMicroUSD != 200 {
		t.Fatalf("embedded measured cost not retained: %+v", embedded.Training[0].Usage)
	}
	selection, err := modelpolicy.EvaluateCalibration(*embedded)
	if err != nil || !selection.Selected || selection.Reason != "candidate_cheaper_reliable_train_and_holdout" {
		t.Fatalf("embedded v2 artifact does not reselect cheaper candidate: %+v %v", selection, err)
	}
}
