package control

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func creation(t *testing.T) Creation {
	t.Helper()
	root := t.TempDir()
	cfg, err := config.Parse([]byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repository = "fixture"
	return Creation{Version: 1, Nonce: "fixture-run", Repository: repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}, Objective: "Add a tested greeting", Config: cfg}
}

func TestPlanApprovalReplayAndStaleAuthority(t *testing.T) {
	p := filepath.Join(t.TempDir(), "run.jsonl")
	c := creation(t)
	if err := Append(p, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "plan.approved", Approval{"unbound", "human"}); err == nil {
		t.Fatal("approved before planning")
	}
	if err := Append(p, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	i, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	fake := &runtime.Fake{}
	r, err := fake.Execute(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	bad := r
	other := "substituted"
	bad.ObservedModel = &other
	if err := Append(p, "plan.recorded", bad); err == nil {
		t.Fatal("model substitution admitted")
	}
	if err := Append(p, "plan.recorded", r); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(p)
	if err != nil || s.State != "AWAITING_APPROVAL" {
		t.Fatal(s, err)
	}
	if err := Append(p, "plan.approved", Approval{"old-plan", "human"}); err == nil {
		t.Fatal("stale approval admitted")
	}
	if err := Append(p, "plan.approved", Approval{s.PlanID, "human"}); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil || s.State != "IMPLEMENTING" || s.ApprovedBy != "human" {
		t.Fatal(s, err)
	}
	before, _ := os.ReadFile(p)
	if err := Append(p, "run.ready", struct{}{}); err == nil {
		t.Fatal("unimplemented transition admitted")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("rejected event changed journal")
	}
}

func TestRuntimeRejectsMutationAndCancellation(t *testing.T) {
	c := creation(t)
	i, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	f := &runtime.Fake{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Execute(ctx, i); err == nil {
		t.Fatal("ignored cancellation")
	}
	mutated := i
	mutated.Input = "new task"
	if _, err := f.Execute(context.Background(), mutated); err == nil {
		t.Fatal("input substitution")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Execute(context.Background(), i); err == nil {
		t.Fatal("executed closed runtime")
	}
}

func TestExecutionPolicyValidatesCodexAutoCompactOption(t *testing.T) {
	policy := ExecutionPolicy{Mode: "autonomous-v1", CodexAutoCompact: &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 64000}}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, option := range []*runtime.CodexAutoCompactOptions{
		{Version: 0, TokenLimit: 64000},
		{Version: runtime.CodexAutoCompactVersion, TokenLimit: 0},
		{Version: runtime.CodexAutoCompactVersion, TokenLimit: runtime.MaxCodexAutoCompactTokenLimit + 1},
	} {
		policy.CodexAutoCompact = option
		if err := policy.Validate(); err == nil {
			t.Fatalf("invalid auto-compaction policy admitted: %+v", option)
		}
	}
	policy.CodexAutoCompact = &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 64000}
	c := creation(t)
	c.Execution = &policy
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err == nil {
		t.Fatal("Codex compaction option admitted when no Codex role is configured")
	}
	compatible := config.Config{Planner: runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "model", Effort: "high", Role: "planner"}}
	if !creationSupportsCodexAutoCompact(compatible) {
		t.Fatal("all-Codex role configuration was rejected")
	}
	compatible.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "low", Role: "writer"}
	if creationSupportsCodexAutoCompact(compatible) {
		t.Fatal("mixed runtime role configuration silently admitted native Codex compaction")
	}
}
