package agentloop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/directapi"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestLoadRepoConfigFallbackAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	// Fallback to harness.toml when fabric.toml is absent.
	if err := os.WriteFile(filepath.Join(dir, "harness.toml"), []byte(config.Example), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRepoConfig(dir)
	if err != nil {
		t.Fatalf("expected harness.toml fallback to load: %v", err)
	}
	if cfg.Repository != "local-project" {
		t.Fatalf("unexpected repository: %s", cfg.Repository)
	}

	// fabric.toml takes precedence over harness.toml.
	custom := `version = 1
repository = "fabric-primary"
base_branch = "main"

[planner]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "planner"

[[verification]]
name = "unit"
argv = ["go", "test", "./..."]
timeout_seconds = 60
`
	if err := os.WriteFile(filepath.Join(dir, "fabric.toml"), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadRepoConfig(dir)
	if err != nil {
		t.Fatalf("expected fabric.toml to load: %v", err)
	}
	if cfg.Repository != "fabric-primary" {
		t.Fatalf("expected fabric.toml precedence, got %s", cfg.Repository)
	}
}

func TestModelFromConfigPrecedence(t *testing.T) {
	cfg := &config.Config{
		Planner:   runtime.Profile{Role: "planner", Runtime: "direct-api", Provider: "anthropic", Model: "claude-sonnet-4-5", Effort: "medium"},
		DirectAPI: &config.DirectAPI{Dialect: directapi.DialectAnthropic, BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY"},
	}

	// 1. Explicit dialect override wins over config.
	m, err := ModelFromConfig(cfg, ModelOverrides{Dialect: "openai", Model: "gpt-5"})
	if err != nil {
		t.Fatalf("ModelFromConfig override: %v", err)
	}
	hm, ok := m.(*HTTPModel)
	if !ok || hm.Dialect != "openai" || hm.ModelName != "gpt-5" {
		t.Fatalf("unexpected override model: %#v", m)
	}

	// 2. DirectAPI config used when no override.
	m, err = ModelFromConfig(cfg, ModelOverrides{})
	if err != nil {
		t.Fatalf("ModelFromConfig direct-api: %v", err)
	}
	hm, ok = m.(*HTTPModel)
	if !ok || hm.Dialect != "anthropic" || hm.ModelName != "claude-sonnet-4-5" {
		t.Fatalf("unexpected direct-api model: %#v", m)
	}

	// 3. ClaudeCLI config used when DirectAPI is absent.
	claudeCfg := &config.Config{
		Planner: runtime.Profile{Role: "planner", Runtime: "claude-cli", Provider: "anthropic", Model: "claude-opus-4-1", Effort: "high"},
		Claude:  &config.ClaudeHost{Executable: "/usr/local/bin/claude"},
	}
	m, err = ModelFromConfig(claudeCfg, ModelOverrides{})
	if err != nil {
		t.Fatalf("ModelFromConfig claude-cli: %v", err)
	}
	cm, ok := m.(*ClaudeCLIModel)
	if !ok || cm.Executable != "/usr/local/bin/claude" || cm.ModelName != "claude-opus-4-1" {
		t.Fatalf("unexpected claude-cli model: %#v", m)
	}
}
