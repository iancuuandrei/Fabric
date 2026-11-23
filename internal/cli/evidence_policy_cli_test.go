package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/evidencevalue"
)

func evidencePolicyFile(t *testing.T, root string, mutate func(map[string]any)) string {
	t.Helper()
	cost := "1"
	model := map[string]any{
		"version": 1,
		"binding": map[string]any{"run_id": "", "source_id": "", "candidate_id": "", "journal_head": ""},
		"resources": []any{map[string]any{
			"name": "local_compute_ms", "limit": cost, "used": "0", "price": cost, "gradient_squares": cost,
		}},
		"actions": []any{map[string]any{
			"id":          "inspect-source",
			"kind":        "source_read",
			"reliability": map[string]any{"ordinal": 2, "successes": 0, "failures": 0},
			"outcomes": []any{
				map[string]any{"probability": "0.5", "utilities": []any{cost, "0"}},
				map[string]any{"probability": "0.5", "utilities": []any{"0", cost}},
			},
			"costs": map[string]any{"local_compute_ms": cost},
		}},
	}
	doc := map[string]any{"version": 1, "model": model, "queries": map[string]any{"inspect-source": "Explain source.txt base"}}
	if mutate != nil {
		mutate(doc)
	}
	raw, err := canonical.Bytes(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "evidence-policy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvidencePolicyFlagStrictRejectedPreRun(t *testing.T) {
	root := autonomousCLIFixture(t)
	valid := evidencePolicyFile(t, root, nil)
	_ = valid
	cases := map[string]func(map[string]any){
		"version": func(m map[string]any) { m["version"] = 2 },
		"binding-supplied": func(m map[string]any) {
			m["model"].(map[string]any)["binding"] = map[string]any{"run_id": strings.Repeat("a", 64), "source_id": "", "candidate_id": "", "journal_head": ""}
		},
		"non-source-kind": func(m map[string]any) {
			m["model"].(map[string]any)["actions"].([]any)[0].(map[string]any)["kind"] = "spawn_explorer"
		},
		"missing-query": func(m map[string]any) { m["queries"] = map[string]any{} },
		"unknown-field": func(m map[string]any) { m["extra"] = 1 },
	}
	for name, mutate := range cases {
		path := evidencePolicyFile(t, root, mutate)
		var out bytes.Buffer
		if err := Execute(context.Background(), []string{"run", "--autonomous", "--evidence-policy", path, "objective"}, root, &out); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	// Duplicate keys rejected before run creation.
	dupPath := filepath.Join(t.TempDir(), "dup.json")
	dup := `{"version":1,"version":1,"model":{"version":1,"binding":{"run_id":"","source_id":"","candidate_id":"","journal_head":""},"resources":[],"actions":[]},"queries":{}}`
	if err := os.WriteFile(dupPath, []byte(dup), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--evidence-policy", dupPath, "objective"}, root, &out); err == nil {
		t.Fatal("duplicate policy accepted")
	}
	// Incompatible parallel/isolated writers rejected explicitly, no fallback.
	isolationPath := filepath.Join(t.TempDir(), "isolation-policy.json")
	isolationRaw := `{"version":1,"capacity":{"cpu_milli":2000,"memory_mib":2048,"verification_slots":1,"total_runtime_slots":2,"provider_slots":2,"model_slots":2,"runtime_slots":2},"estimate":{"cpu_milli":500,"memory_mib":512,"verification_slots":0,"runtime_slots":1}}`
	if err := os.WriteFile(isolationPath, []byte(isolationRaw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", "--autonomous", "--evidence-policy", valid, "--parallel-writers", "objective"},
		{"run", "--autonomous", "--evidence-policy", valid, "--isolated-writers", "--isolation-policy", isolationPath, "objective"},
	} {
		var b bytes.Buffer
		if err := Execute(context.Background(), args, root, &b); err == nil {
			t.Fatalf("incompatible writers accepted: %v", args)
		}
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("strict rejection created runs: %v", entries)
	}
}

func TestEvidencePolicyBindsImmutableCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	policyPath := evidencePolicyFile(t, root, nil)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--max-parallel", "1", "--evidence-policy", policyPath, "A bounded fixture objective"}, root, &out)
	if err == nil {
		t.Fatal("fixture planner should stop before dispatch")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable run: %v", entries)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Execution == nil || s.Creation.Execution.EvidencePolicy == nil {
		t.Fatal("evidence policy not frozen at creation")
	}
	stored := *s.Creation.Execution.EvidencePolicy
	if stored.Version != 1 || stored.Model.Binding != (evidencevalue.Binding{}) {
		t.Fatal("stored template lost empty binding")
	}
	if err := control.ValidateEvidenceAutoPolicyTemplate(stored); err != nil {
		t.Fatal("stored template invalid", err)
	}
	hash, err := control.EvidenceAutoPolicyHash(stored)
	if err != nil || hash == "" {
		t.Fatal("policy hash missing", err)
	}
	// Legacy run without the flag preserves absent-field serialization.
	legacyRoot := autonomousCLIFixture(t)
	var legacyOut bytes.Buffer
	_ = Execute(context.Background(), []string{"run", "--autonomous", "--max-parallel", "1", "Legacy objective"}, legacyRoot, &legacyOut)
	legacyEntries, _ := filepath.Glob(filepath.Join(legacyRoot, ".harness", "runs", "*.jsonl"))
	if len(legacyEntries) != 1 {
		t.Fatal("legacy run missing")
	}
	legacy, err := control.Inspect(legacyEntries[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Bytes(legacy.Creation.Execution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "evidence_policy") {
		t.Fatal("absent policy changed legacy serialization")
	}
	// --file creation path binds the same immutable policy.
	goalPath := filepath.Join(root, "goal.md")
	if err := os.WriteFile(goalPath, []byte("File objective with evidence policy"), 0600); err != nil {
		t.Fatal(err)
	}
	var fileOut bytes.Buffer
	fileErr := Execute(context.Background(), []string{"run", "--autonomous", "--max-parallel", "1", "--evidence-policy", policyPath, "--file", "goal.md"}, root, &fileOut)
	if fileErr == nil {
		t.Fatal("file run should stop at fixture planner")
	}
	fileEntries, _ := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if len(fileEntries) != 2 {
		t.Fatalf("file creation did not bind a second run: %v", fileEntries)
	}
}

func TestEvidencePolicyInspectPlanAndResume(t *testing.T) {
	root := autonomousCLIFixture(t)
	policyPath := evidencePolicyFile(t, root, nil)
	raw := mustExecuteCLI(t, root, "run", "--autonomous", "--inspect-plan", "--max-parallel", "1", "--evidence-policy", policyPath)
	var plan map[string]any
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	execution, ok := plan["execution_plan"].(map[string]any)
	if !ok {
		t.Fatal("inspect-plan missing execution plan")
	}
	evidence, ok := execution["evidence_policy"].(map[string]any)
	if !ok || evidence["version"] != float64(1) || evidence["runtime_dispatch"] != "NOT_RUN" {
		t.Fatalf("inspect-plan omitted requested policy: %v", evidence)
	}
	// Incompatible inspect-plan rejects clearly before dispatch.
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan", "--evidence-policy", policyPath, "--parallel-writers"}, root, &out); err == nil {
		t.Fatal("inspect-plan accepted incompatible writers")
	}
	// Resume forbids injecting or changing the frozen policy.
	var runOut bytes.Buffer
	_ = Execute(context.Background(), []string{"run", "--autonomous", "--max-parallel", "1", "--evidence-policy", policyPath, "Resume fixture"}, root, &runOut)
	entries, _ := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if len(entries) == 0 {
		t.Fatal("policy run missing for resume")
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	var resumeOut bytes.Buffer
	if err := Execute(context.Background(), []string{"resume", "--autonomous", "--evidence-policy", policyPath}, root, &resumeOut); err == nil {
		t.Fatal("resume accepted policy injection")
	}
	var resumeOK bytes.Buffer
	_ = Execute(context.Background(), []string{"resume", "--autonomous", s.RunID}, root, &resumeOK)
	after, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, _ := canonical.Bytes(s.Creation.Execution)
	afterRaw, _ := canonical.Bytes(after.Creation.Execution)
	if !bytes.Equal(beforeRaw, afterRaw) {
		t.Fatal("resume changed the frozen policy")
	}
}
