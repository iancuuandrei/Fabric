package control

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestPlannerInvocationContractPreservesLegacyInput(t *testing.T) {
	c := config.Config{Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	objective := "exact objective bytes\nwith newlines"
	legacy, err := plannerInvocation(c, objective)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Input != objective {
		t.Fatalf("legacy planner input changed: %q", legacy.Input)
	}
	expected, err := runtime.NewInvocation(c.Planner, objective)
	if err != nil || legacy != expected {
		t.Fatalf("legacy planner identity changed: %v", err)
	}
}

func TestPlannerInvocationContractSeparatesPlannerAssignment(t *testing.T) {
	c := config.Config{PlannerContract: plannerContractV1, Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	objective := "preserve this objective exactly"
	i, err := plannerInvocation(c, objective)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Role        string `json:"role"`
		Instruction string `json:"instruction"`
		Objective   string `json:"objective"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Role != "planner" || payload.Objective != objective || !strings.Contains(payload.Instruction, "read-only source tools") || !strings.Contains(payload.Instruction, "later roles") || !strings.Contains(payload.Instruction, "not a blocker") {
		t.Fatalf("planner assignment payload incomplete: %+v", payload)
	}
	legacy, err := runtime.NewInvocation(c.Planner, objective)
	if err != nil || i.ID == legacy.ID {
		t.Fatalf("planner contract did not bind a distinct invocation identity: %v", err)
	}
}

func TestPlannerInvocationRejectsUnknownContract(t *testing.T) {
	c := config.Config{PlannerContract: "unknown", Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	if _, err := plannerInvocation(c, "objective"); err == nil {
		t.Fatal("unknown planner contract admitted")
	}
}

func TestReviewerContractFreezesConfiguredVerificationInPlannerInput(t *testing.T) {
	c := creation(t)
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.ReviewerContract = "json-v1"
	i, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction        string         `json:"instruction"`
		VerificationChecks []config.Check `json:"verification_checks"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.VerificationChecks) != len(c.Config.Verification) || payload.VerificationChecks[0].Name != c.Config.Verification[0].Name || !strings.Contains(payload.Instruction, "configured verification checks") || !strings.Contains(payload.Instruction, "recorded observations") || !strings.Contains(payload.Instruction, "Prefer direct mode for localized changes") || !strings.Contains(payload.Instruction, "independent read-only tasks may run concurrently") {
		t.Fatalf("planner did not receive frozen verification scope: %+v", payload)
	}
	legacy := c.Config
	legacy.ReviewerContract = ""
	legacyInvocation, err := plannerInvocation(legacy, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	want, err := runtime.NewInvocation(legacy.Planner, c.Objective)
	if err != nil || legacyInvocation != want {
		t.Fatalf("empty reviewer contract changed legacy planner identity: %v", err)
	}
}

func TestPlannerContractIsEnforcedByJournalReplay(t *testing.T) {
	c := creation(t)
	c.Config.PlannerContract = plannerContractV1
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal("contract-bound planner result was rejected", err)
	}
	s, err := Inspect(path)
	if err != nil || s.Plan == nil || s.Plan.InvocationID != invocation.ID {
		t.Fatal("contract-bound planner result did not replay", err)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy-result.jsonl")
	if err := Append(legacyPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(legacyPath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	legacyInvocation, err := runtime.NewInvocation(c.Config.Planner, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	legacyResult, err := (&runtime.Fake{}).Execute(context.Background(), legacyInvocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(legacyPath, "plan.recorded", legacyResult); err == nil {
		t.Fatal("raw objective planner result bypassed the configured contract")
	}
}

func TestPlannerContractDoesNotContaminateWriterContext(t *testing.T) {
	c := creation(t)
	c.Config.PlannerContract = plannerContractV1
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "medium", Role: "writer"}
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	writer, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string `json:"instruction"`
		Objective   string `json:"objective"`
	}
	if err := json.Unmarshal([]byte(writer.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Objective != c.Objective {
		t.Fatalf("writer objective was changed by planner contract: %q", payload.Objective)
	}
	if strings.Contains(writer.Input, plannerAssignmentV1) || strings.Contains(payload.Instruction, "As planner") {
		t.Fatal("planner assignment contaminated writer context")
	}
}

func TestPlannerInvocationCarriesFrozenCodexAutoCompactOption(t *testing.T) {
	c := config.Config{Planner: runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "model", Effort: "high", Role: "planner"}}
	policy := &ExecutionPolicy{CodexAutoCompact: &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 50000}}
	i, err := plannerInvocationWithContextsAndRecipe(c, "objective", nil, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	if i.CodexAutoCompactOption() == nil || *i.CodexAutoCompactOption() != *policy.CodexAutoCompact {
		t.Fatalf("planner invocation did not retain frozen threshold: %+v", i.CodexAutoCompactOption())
	}
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	legacy, err := plannerInvocation(c, "objective")
	if err != nil || legacy.CodexAutoCompactOption() != nil {
		t.Fatalf("legacy planner invocation changed: %+v, %v", legacy, err)
	}
}
