package fabric

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/cost"
)

type sdkStubModel struct {
	name  string
	steps []agentloop.ModelResponse
	idx   int
}

func (m *sdkStubModel) Name() string { return m.name }
func (m *sdkStubModel) Complete(_ context.Context, _ string, _ []agentloop.Message, _ []agentloop.ToolDef) (agentloop.ModelResponse, error) {
	if m.idx >= len(m.steps) {
		return agentloop.ModelResponse{Text: "done"}, nil
	}
	r := m.steps[m.idx]
	m.idx++
	return r, nil
}

func initSDKRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "val.go"), []byte("package app\nconst V = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	return dir
}

func TestClientAgentEpisodePacketRewindAndApply(t *testing.T) {
	dir := initSDKRepo(t)
	client, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	mdl := &sdkStubModel{
		name: "claude-sonnet-4-5",
		steps: []agentloop.ModelResponse{
			{
				ToolCalls: []agentloop.ToolCall{{
					ID:        "c1",
					Name:      "edit_file",
					Arguments: json.RawMessage(`{"path":"val.go","old_string":"const V = 1","new_string":"const V = 42"}`),
				}},
				Usage: cost.Usage{InputTokens: 400, OutputTokens: 60},
			},
			{
				Text:  "Updated V from 1 to 42.",
				Usage: cost.Usage{InputTokens: 300, OutputTokens: 40},
			},
		},
	}

	ep, err := client.RunAgent(ctx, AgentOptions{
		Objective:   "Change V to 42",
		CustomModel: mdl,
	})
	if err != nil {
		t.Fatalf("RunAgent failed: %v", err)
	}
	if ep.Status != "READY" {
		t.Fatalf("expected READY, got %s (%s)", ep.Status, ep.Reason)
	}

	eps, err := client.ListEpisodes()
	if err != nil || len(eps) != 1 || eps[0].RunID != ep.RunID {
		t.Fatalf("ListEpisodes unexpected: %+v err=%v", eps, err)
	}
	loaded, err := client.GetEpisode(ep.RunID)
	if err != nil || loaded.RunID != ep.RunID {
		t.Fatalf("GetEpisode failed: %v", err)
	}
	pkt, err := client.ReviewPacket(ep.RunID)
	if err != nil || !strings.Contains(pkt.Markdown(), "Fabric Review Packet") {
		t.Fatalf("ReviewPacket failed: %+v err=%v", pkt, err)
	}

	// Apply cleanly onto a new branch.
	if err := client.ApplyEpisode(ctx, ep.RunID, "sdk-apply"); err != nil {
		t.Fatalf("ApplyEpisode failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "val.go"))
	if !strings.Contains(string(got), "const V = 42") {
		t.Fatalf("expected applied content in repo, got %q", string(got))
	}

	// Rewind isolated worktree back to step 0 (base).
	if _, err := client.RewindEpisode(ctx, ep.RunID, 0); err != nil {
		t.Fatalf("RewindEpisode failed: %v", err)
	}
	wtGot, _ := os.ReadFile(filepath.Join(ep.WorkDir, "val.go"))
	if !strings.Contains(string(wtGot), "const V = 1") {
		t.Fatalf("expected rewound worktree content, got %q", string(wtGot))
	}
}
