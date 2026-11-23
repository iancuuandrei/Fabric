package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
)

func TestSmartInitClaudeACPDirectAPIAndLanguageDetection(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	newRepo := func(manifestFile string) string {
		root := t.TempDir()
		if manifestFile != "" {
			if err := os.WriteFile(filepath.Join(root, manifestFile), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{
			{"init", "-q"},
			{"add", "."},
			{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "init"},
		} {
			if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		return root
	}

	// 1. Claude CLI init on a Node/TypeScript repo
	tsRoot := newRepo("package.json")
	if err := Execute(context.Background(), []string{"init", "--claude-bin", exe, "--model", "claude-sonnet-4-5"}, tsRoot, &bytes.Buffer{}); err != nil {
		t.Fatalf("claude init failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(tsRoot, "fabric.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Planner.Runtime != "claude-cli" || cfg.Claude == nil || cfg.Verification[0].Argv[0] != "npm" {
		t.Fatalf("unexpected claude/ts config: %+v", cfg)
	}

	// 2. ACP init on a Python repo
	pyRoot := newRepo("pyproject.toml")
	if err := Execute(context.Background(), []string{"init", "--acp", exe, "--acp-args", "--stdio"}, pyRoot, &bytes.Buffer{}); err != nil {
		t.Fatalf("acp init failed: %v", err)
	}
	raw, _ = os.ReadFile(filepath.Join(pyRoot, "fabric.toml"))
	cfg, err = config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Planner.Runtime != "acp-stdio" || cfg.ACP == nil || cfg.Verification[0].Argv[0] != "pytest" {
		t.Fatalf("unexpected acp/py config: %+v", cfg)
	}

	// 3. Direct API init on a Rust repo
	rsRoot := newRepo("Cargo.toml")
	if err := Execute(context.Background(), []string{"init", "--api", "anthropic", "--model", "claude-sonnet-4-5"}, rsRoot, &bytes.Buffer{}); err != nil {
		t.Fatalf("direct-api init failed: %v", err)
	}
	raw, _ = os.ReadFile(filepath.Join(rsRoot, "fabric.toml"))
	cfg, err = config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Planner.Runtime != "direct-api" || cfg.DirectAPI == nil || cfg.DirectAPI.Dialect != "anthropic" || cfg.Verification[0].Argv[0] != "cargo" {
		t.Fatalf("unexpected direct-api/rust config: %+v", cfg)
	}
}
