package tournament

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
)

type stubModel struct {
	name  string
	steps []agentloop.ModelResponse
	i     int
}

func (m *stubModel) Name() string { return m.name }
func (m *stubModel) Complete(_ context.Context, _ string, _ []agentloop.Message, _ []agentloop.ToolDef) (agentloop.ModelResponse, error) {
	if m.i >= len(m.steps) {
		return agentloop.ModelResponse{Text: "done"}, nil
	}
	r := m.steps[m.i]
	m.i++
	return r, nil
}

func TestTournamentSelectsMinimalDiffParetoWinner(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nconst A = 1\nconst B = 2\n"), 0o600)
	run("add", ".")
	run("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")

	bloated := &stubModel{
		name: "gpt-5",
		steps: []agentloop.ModelResponse{
			{ToolCalls: []agentloop.ToolCall{{ID: "1", Name: "edit_file", Arguments: json.RawMessage(`{"path":"app.go","old_string":"const A = 1\nconst B = 2","new_string":"// extra comment\nconst A = 10\n// another comment\nconst B = 2"}`)}}},
			{Text: "updated A with comments", Usage: cost.Usage{InputTokens: 2000, OutputTokens: 200}},
		},
	}
	surgical := &stubModel{
		name: "claude-sonnet-4-5",
		steps: []agentloop.ModelResponse{
			{ToolCalls: []agentloop.ToolCall{{ID: "1", Name: "edit_file", Arguments: json.RawMessage(`{"path":"app.go","old_string":"const A = 1","new_string":"const A = 10"}`)}}},
			{Text: "surgical update to A", Usage: cost.Usage{InputTokens: 1000, OutputTokens: 100}},
		},
	}

	rep, err := Run(context.Background(), Config{
		RepoRoot:  dir,
		Objective: "Change A to 10.",
		Contenders: []Contender{
			{Label: "bloated-gpt", Model: bloated},
			{Label: "surgical-claude", Model: surgical},
		},
		Verification: []config.Check{{Name: "ok", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}},
	})
	if err != nil {
		t.Fatalf("tournament failed: %v", err)
	}
	if rep.Winner == nil || rep.Winner.Label != "surgical-claude" {
		t.Fatalf("expected surgical-claude Pareto winner, got: %+v", rep.Winner)
	}
}
