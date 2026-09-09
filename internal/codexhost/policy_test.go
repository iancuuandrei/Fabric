package codexhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/runtime"
)

func TestHostPreparationRejectsReuseAndDrift(t *testing.T) {
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	l, err := Prepare(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.ID(); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(root, binary); err == nil {
		t.Fatal("reused populated control directory")
	}
	if err := os.WriteFile(filepath.Join(root, "home", "config.toml"), []byte("sandbox_mode = \"danger-full-access\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.Validate(); err == nil {
		t.Fatal("configuration drift admitted")
	}
}

func TestHostEnvironmentDoesNotInheritCredentialsOrOverrides(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fixture-secret")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("CODEX_HOME", t.TempDir())
	root := t.TempDir()
	for _, entry := range environment(root) {
		if strings.HasPrefix(entry, "OPENAI_API_KEY=") || strings.HasPrefix(entry, "GIT_") {
			t.Fatal("inherited unselected environment")
		}
		if strings.HasPrefix(entry, "CODEX_HOME=") && entry != "CODEX_HOME="+root {
			t.Fatal("inherited canonical Codex home")
		}
	}
}

func TestInstalledHostAdmission(t *testing.T) {
	binary := os.Getenv("ENGORCH_CODEX_PROBE_BINARY")
	if binary == "" {
		t.Skip("opt-in installed host admission; no model turn")
	}
	l, err := Prepare(t.TempDir(), binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h, err := Start(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if len(h.Receipt.DisabledFeatures) != len(disabled) {
		t.Fatal("incomplete feature admission")
	}
	t.Logf("host admission PASS: %d disabled features observed; no model turn", len(disabled))
	p := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "planner"}
	thread, err := h.Client.StartThread(ctx, p, filepath.Join(l.Root, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if thread.ThreadID == "" {
		t.Fatal("missing local thread identity")
	}
	t.Log("empty-environment thread creation accepted; no model turn dispatched")
}
