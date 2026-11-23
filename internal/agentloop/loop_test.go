package agentloop

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
)

type scriptedModel struct {
	name      string
	responses []ModelResponse
	seenMsgs  [][]Message
	idx       int
}

func (s *scriptedModel) Name() string { return s.name }

func (s *scriptedModel) Complete(_ context.Context, _ string, messages []Message, _ []ToolDef) (ModelResponse, error) {
	cp := make([]Message, len(messages))
	copy(cp, messages)
	s.seenMsgs = append(s.seenMsgs, cp)
	if s.idx >= len(s.responses) {
		return ModelResponse{Text: "done"}, nil
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

func makeGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	bigComment := strings.Repeat("// padding comment line for observation masking test\n", 20)
	_ = os.WriteFile(filepath.Join(dir, "math.go"), []byte("package math\n"+bigComment+"func Positive(n int) bool {\n\treturn n >= 0\n}\n"), 0o600)
	run("add", ".")
	run("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	return dir
}

func TestAgentLoopToolsObservationMaskingCheckpointAndReview(t *testing.T) {
	repo := makeGitRepo(t)
	model := &scriptedModel{
		name: "claude-sonnet-4-5",
		responses: []ModelResponse{
			{
				ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: json.RawMessage(`{"path":"math.go"}`)}},
				Usage:     cost.Usage{InputTokens: 500, OutputTokens: 40},
			},
			{
				ToolCalls: []ToolCall{{ID: "c2", Name: "search_text", Arguments: json.RawMessage(`{"query":"Positive"}`)}},
				Usage:     cost.Usage{InputTokens: 600, CachedTokens: 400, OutputTokens: 30},
			},
			{
				ToolCalls: []ToolCall{{ID: "c3", Name: "edit_file", Arguments: json.RawMessage(`{"path":"math.go","old_string":"return n >= 0","new_string":"return n > 0"}`)}},
				Usage:     cost.Usage{InputTokens: 700, CachedTokens: 500, OutputTokens: 50},
			},
			{
				Text:  "Fixed Positive(n) to require strictly n > 0.",
				Usage: cost.Usage{InputTokens: 750, CachedTokens: 600, OutputTokens: 25},
			},
		},
	}
	reviewer := &scriptedModel{
		name: "claude-sonnet-4-5",
		responses: []ModelResponse{{
			Text:  `{"verdict":"approve","notes":"Minimal root-cause fix verified."}`,
			Usage: cost.Usage{InputTokens: 300, OutputTokens: 20},
		}},
	}

	ep, err := Run(context.Background(), Config{
		RepoRoot:        repo,
		Objective:       "Fix Positive(n) so 0 returns false.",
		Model:           model,
		ReviewerModel:   reviewer,
		KeepRecentTurns: 1, // force observation masking on turn 3+
		Verification:    []config.Check{{Name: "git-ok", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}},
		AllowCommand:    true,
	})
	if err != nil {
		t.Fatalf("agentloop.Run failed: %v", err)
	}
	if ep.Status != "READY" {
		t.Fatalf("expected READY, got %s (%s)", ep.Status, ep.Reason)
	}
	if ep.MaskedBytesSaved <= 0 {
		t.Fatalf("expected observation masking to save bytes, got %d", ep.MaskedBytesSaved)
	}
	if len(ep.Checkpoints) < 2 {
		t.Fatalf("expected >=2 rewind checkpoints (base + pre-edit), got %d", len(ep.Checkpoints))
	}
	if ep.ReviewVerdict != "approve" {
		t.Fatalf("expected approve verdict, got %q", ep.ReviewVerdict)
	}
	if !ep.CostEstimate.KnownPricing || ep.CostEstimate.EstimatedUSD <= 0 {
		t.Fatalf("expected positive USD cost estimate, got %+v", ep.CostEstimate)
	}
	// Verify isolated worktree has the fix while repo root remains completely untouched!
	wtData, _ := os.ReadFile(filepath.Join(ep.WorkDir, "math.go"))
	if !strings.Contains(string(wtData), "return n > 0") {
		t.Fatalf("expected fix in worktree, got: %s", string(wtData))
	}
	rootData, _ := os.ReadFile(filepath.Join(repo, "math.go"))
	if !strings.Contains(string(rootData), "return n >= 0") {
		t.Fatal("user repo root was mutated! isolation violated")
	}
}

func TestAgentLoopBudgetCircuitBreakerAndWriteScope(t *testing.T) {
	repo := makeGitRepo(t)
	model := &scriptedModel{
		name: "claude-opus-4-5",
		responses: []ModelResponse{
			{
				ToolCalls: []ToolCall{{ID: "c1", Name: "write_file", Arguments: json.RawMessage(`{"path":"unauthorized.go","content":"package x\n"}`)}},
				Usage:     cost.Usage{InputTokens: 500_000, OutputTokens: 50_000}, // > $11 USD
			},
			{Text: "should never reach turn 2"},
		},
	}
	ep, err := Run(context.Background(), Config{
		RepoRoot:     repo,
		Objective:    "Test budget stop.",
		Model:        model,
		AllowedWrite: []string{"math.go"},
		Budget:       cost.Budget{MaxUSD: 1.00},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ep.Status != "BUDGET_EXCEEDED" {
		t.Fatalf("expected BUDGET_EXCEEDED, got %s (%s)", ep.Status, ep.Reason)
	}
	if _, err := os.Stat(filepath.Join(ep.WorkDir, "unauthorized.go")); !os.IsNotExist(err) {
		t.Fatal("write outside AllowedWrite was permitted!")
	}
}
