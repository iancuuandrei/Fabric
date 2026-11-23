package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func plannerContextFixture(t *testing.T, objective string) (string, Creation) {
	t.Helper()
	c := autonomousCreation(t, 0)
	c.Execution.PlannerContext = plannerContextSourceBoundedV1
	c.Objective = objective
	root := c.Repository.Root
	autonomousGitInit(t, root)
	for name, content := range map[string]string{
		"greeting_guide.txt":                  "tested greeting implementation guidance\n",
		"internal/difflib/pivot.go":           "package difflib\n// target diff implementation\n",
		"internal/gen_atomicint/wrapper.tmpl": "// Code generated template input; edit generator source with care.\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	autonomousGitCmd(t, root, []string{"add", "greeting_guide.txt", "internal/difflib/pivot.go", "internal/gen_atomicint/wrapper.tmpl"})
	if out, err := autonomousGitCmd(t, root, []string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "guide"}); err != nil {
		t.Fatal(err, string(out))
	}
	var err error
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	return path, c
}

func TestPlannerContextAdmittedBeforePlanningAndBoundIntoPlannerInput(t *testing.T) {
	path, _ := plannerContextFixture(t, "Add a tested greeting")
	admitted, err := AdmitPlannerContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Coverage != "partial_committed_source_no_ri" || admitted.InventoryFiles < 1 || admitted.AttemptedFiles < admitted.ReadFiles || admitted.ReadFiles < 1 || admitted.ManifestID == "" || len(admitted.Manifest.Selected) == 0 {
		t.Fatalf("incomplete planner context: %+v", admitted)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPlannerContext(invocation.Input, admitted.ManifestID) {
		t.Fatal("planner invocation lacks durable source manifest")
	}
	if err := os.WriteFile(filepath.Join(s.Creation.Repository.Root, "file.txt"), []byte("uncommitted mutation\n"), 0600); err != nil {
		t.Fatal(err)
	}
	replayed, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := plannerInvocationForSnapshot(replayed)
	if err != nil || after != invocation {
		t.Fatalf("mutable checkout changed admitted planner input: %v", err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	fake := &runtime.Fake{}
	result, err := fake.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerContextReplayRejectsInvalidUnavailableAndDuplicateManifestPaths(t *testing.T) {
	path, c := plannerContextFixture(t, "Add a tested greeting")
	record, err := AdmitPlannerContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.jsonl")
	if err := Append(invalidPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	unavailable := record
	unavailable.Unavailable = "no_eligible_complete_utf8_source_files"
	unavailable.ReadFiles = 0
	unavailable.Manifest = taskcontext.Manifest{}
	unavailable.ManifestID = ""
	unavailable.AttemptedFiles = 1
	if err := Append(invalidPath, "planner.context-admitted", unavailable); err == nil {
		t.Fatal("unavailable context accepted nonempty inventory/read telemetry")
	}
	duplicatePath := filepath.Join(t.TempDir(), "duplicate.jsonl")
	if err := Append(duplicatePath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	duplicate := record
	duplicate.Manifest.Selected = append(duplicate.Manifest.Selected, duplicate.Manifest.Selected[0])
	if id, err := duplicate.Manifest.ID(); err != nil {
		t.Fatal(err)
	} else {
		duplicate.ManifestID = id
	}
	if err := Append(duplicatePath, "planner.context-admitted", duplicate); err == nil {
		t.Fatal("duplicate selected path accepted")
	}
	forgedPath := filepath.Join(t.TempDir(), "forged.jsonl")
	if err := Append(forgedPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	forged := record
	forged.Manifest.SelectedBytes++
	if id, err := forged.Manifest.ID(); err != nil {
		t.Fatal(err)
	} else {
		forged.ManifestID = id
	}
	if err := Append(forgedPath, "planner.context-admitted", forged); err == nil {
		t.Fatal("forged selected byte total accepted")
	}
	reasonPath := filepath.Join(t.TempDir(), "reason.jsonl")
	if err := Append(reasonPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	badReason := record
	badReason.Manifest.Selected[0].Reason = "forged_reason"
	if id, err := badReason.Manifest.ID(); err != nil {
		t.Fatal(err)
	} else {
		badReason.ManifestID = id
	}
	if err := Append(reasonPath, "planner.context-admitted", badReason); err == nil {
		t.Fatal("forged selected reason accepted")
	}
}

func TestPlannerContextSelectsObjectiveAndGenerationPathPivots(t *testing.T) {
	path, _ := plannerContextFixture(t, "Repair difflib and update generated template ownership")
	record, err := AdmitPlannerContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, selected := range record.Manifest.Selected {
		seen[selected.Path] = true
	}
	if !seen["internal/difflib/pivot.go"] || !seen["internal/gen_atomicint/wrapper.tmpl"] {
		t.Fatalf("objective/template path hints did not select direct source evidence: %+v", record.Manifest.Selected)
	}
}

func TestResumePlanningAdmitsPlannerContextBeforeFakePlanner(t *testing.T) {
	path, _ := plannerContextFixture(t, "Add a tested greeting")
	s, err := ResumePlanning(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlannerContext == nil || s.Plan == nil || s.State != "AWAITING_APPROVAL" {
		t.Fatalf("planner context was not admitted before planner: %+v", s)
	}
}

func TestPlannerContextPolicyRequiresDurableAdmission(t *testing.T) {
	path, _ := plannerContextFixture(t, "Add a tested greeting")
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plannerInvocationForSnapshot(s); err == nil {
		t.Fatal("opt-in planner invocation accepted without durable context")
	}
}

func TestLegacyPlannerInvocationRemainsExactWithoutPlannerContext(t *testing.T) {
	c := creation(t)
	legacy, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	got, err := plannerInvocationForSnapshot(Snapshot{Creation: c})
	if err != nil || got != legacy {
		t.Fatalf("legacy planner identity changed: %v", err)
	}
}

func containsPlannerContext(input, manifestID string) bool {
	return manifestID != "" && strings.Contains(input, manifestID)
}

func BenchmarkPlannerContextPathScoreTerms(b *testing.B) {
	objective := "Repair difflib generated template ownership and regression coverage"
	paths := make([]string, 4096)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/pkg%04d/generated_wrapper_template.go", i)
	}
	b.Run("reference_retokenizes", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, path := range paths {
				_ = plannerContextPathScore(path, objective)
			}
		}
	})
	terms := plannerContextTerms(objective)
	b.Run("pretokenized", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			for _, path := range paths {
				_ = plannerContextPathScoreTerms(path, terms)
			}
		}
	})
}
