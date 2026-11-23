package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
)

func TestModelPolicyInputRejectsMalformedAndOversizedJSON(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"version":1}`,
		`{"unexpected":true}`,
		`{} {}`,
		strings.Repeat(" ", modelPolicyMaxBytes+1),
	} {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readInitModelPolicy(path); err == nil {
			t.Fatal("invalid model policy input accepted")
		}
	}
	if _, err := readModelPolicyJSON(t.TempDir()); err == nil {
		t.Fatal("directory accepted as model policy input")
	}
}

func TestInitEmbedsExplicitModelPolicyAndDoesNotRereadItsFile(t *testing.T) {
	f := newFixerInitFixture(t)
	policy := modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 1,
		Profiles: []modelpolicy.Profile{
			{Name: "baseline", Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium"},
			{Name: "candidate", Runtime: "codex-app-server", Provider: "openai", Model: "strong-model", Effort: "high"},
		},
		Rules: map[string]modelpolicy.Rule{"fixer": {
			DefaultProfile: "baseline", EscalatedProfile: "candidate",
			ContextEscalationTokens: modelpolicy.MaxContextTokens,
			ContextEscalationBytes:  modelpolicy.MaxContextBytes, FailureEscalationCount: 3,
		}},
	}
	body, err := canonical.Bytes(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "model-policy.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := f.args("--fixer-model", "base-model", "--access-config", f.accessConfig(t), "--model-policy", path)
	if err := Execute(context.Background(), args, f.root, &out); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	configBytes, err := os.ReadFile(filepath.Join(f.root, "harness.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(configBytes)
	if err != nil || cfg.ModelPolicy == nil {
		t.Fatalf("embedded model policy unavailable: %v", err)
	}
	got, err := canonical.Bytes(cfg.ModelPolicy)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("model policy changed during init round trip: %v", err)
	}
}

func TestInitModelPolicyRequiresExplicitFixerAndAccessBeforeWrites(t *testing.T) {
	f := newFixerInitFixture(t)
	var out bytes.Buffer
	if err := Execute(context.Background(), f.args("--model-policy", "missing.json"), f.root, &out); err == nil {
		t.Fatal("model policy without fixer/access accepted")
	}
	f.assertNoWrites(t)
}

func TestCalibrateModelsRejectsArgumentsAndMalformedArtifactWithoutDispatch(t *testing.T) {
	var out bytes.Buffer
	if err := modelCalibrationCommand(nil, &out); err == nil {
		t.Fatal("missing artifact accepted")
	}
	path := filepath.Join(t.TempDir(), "calibration.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := modelCalibrationCommand([]string{path}, &out); err == nil || out.Len() != 0 {
		t.Fatal("malformed calibration emitted a recommendation")
	}
}

func TestCalibrateModelsReportsFallbackAndTypedUnknownCost(t *testing.T) {
	baseline := modelpolicy.Profile{Name: "luna", Runtime: "codex-app-server", Provider: "openai", Model: "gpt-6-luna", Effort: "high"}
	candidate := baseline
	candidate.Name, candidate.Model = "sol", "gpt-6.1-sol"
	artifact := cliCalibrationFixture(baseline, candidate)
	body, err := canonical.Bytes(artifact)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "calibration.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := modelCalibrationCommand([]string{path}, &out); err != nil {
		t.Fatal(err)
	}
	var result modelpolicy.CalibrationSelection
	if err := canonical.Decode(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Selected || result.Profile.Name != baseline.Name || result.Reason != "fallback_fixer_not_exercised" || result.Digest == "" || result.Profile.ExpectedCostMicroUSD != nil {
		t.Fatal("fallback or unknown cost was not preserved")
	}
}

func cliCalibrationFixture(baseline, candidate modelpolicy.Profile) modelpolicy.Calibration {
	row := func(task, run, objective string, profile modelpolicy.Profile) modelpolicy.TaskPolicyOutcome {
		return modelpolicy.TaskPolicyOutcome{
			SourceID: "fixture-source", TaskID: task, RunID: run, ObjectiveHash: objective,
			NonFixerPolicyID: strings.Repeat("c", 64), AssignedProfile: profile,
			Outcome: modelpolicy.OutcomeNotExercised,
		}
	}
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	return modelpolicy.Calibration{
		Version: 1, FamilyID: "fixture-family", Role: "fixer", Baseline: baseline, Candidate: candidate,
		MinimumPerArm: 1, Objectives: []string{a, b},
		Training: []modelpolicy.TaskPolicyOutcome{row("train", "train-luna", a, baseline), row("train", "train-sol", a, candidate)},
		Holdout:  []modelpolicy.TaskPolicyOutcome{row("holdout", "holdout-luna", b, baseline), row("holdout", "holdout-sol", b, candidate)},
	}
}

func TestInitEmbedsCalibrationV2WithExactDigestAndUnknownUsage(t *testing.T) {
	f := newFixerInitFixture(t)
	baseline := modelpolicy.Profile{Name: "luna", Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium"}
	candidate := baseline
	candidate.Name, candidate.Model = "sol", "strong-model"
	frontier := baseline
	frontier.Name, frontier.Model = "frontier", "frontier-model"
	calibration := cliCalibrationFixture(baseline, candidate)
	policy := modelpolicy.Policy{
		Version: 1, DecisionEvidenceVersion: 2, Calibration: &calibration,
		Profiles: []modelpolicy.Profile{baseline, candidate, frontier},
		Rules: map[string]modelpolicy.Rule{"fixer": {
			DefaultProfile: baseline.Name, EscalatedProfile: frontier.Name,
			ContextEscalationTokens: modelpolicy.MaxContextTokens,
			ContextEscalationBytes:  modelpolicy.MaxContextBytes, FailureEscalationCount: 3,
		}},
	}
	body, err := canonical.Bytes(policy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), f.args("--fixer-model", "base-model", "--access-config", f.accessConfig(t), "--model-policy", path), f.root, &out); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "harness.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil || cfg.ModelPolicy == nil || cfg.ModelPolicy.Calibration == nil {
		t.Fatalf("embedded calibration unavailable: %v", err)
	}
	before, err := calibration.Digest()
	if err != nil {
		t.Fatal(err)
	}
	after, err := cfg.ModelPolicy.Calibration.Digest()
	if err != nil || before != after || cfg.ModelPolicy.Calibration.Training[0].Usage != nil {
		t.Fatal("calibration digest or unavailable usage changed during TOML round trip")
	}
}
