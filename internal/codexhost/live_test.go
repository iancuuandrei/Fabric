package codexhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/runtime"
)

func TestLiveModelRoundTrip(t *testing.T) {
	binary, source := os.Getenv("ENGORCH_CODEX_PROBE_BINARY"), os.Getenv("ENGORCH_CODEX_AUTH_SOURCE")
	if binary == "" || source == "" {
		t.Skip("opt-in authenticated model test")
	}
	root := t.TempDir()
	if out := os.Getenv("ENGORCH_CODEX_EVIDENCE"); out != "" {
		var err error
		root, err = os.MkdirTemp(filepath.Dir(out), "codex-live-attempt-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("durable local attempt: %s", root)
	}
	l, err := Prepare(root, binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h, err := Start(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := h.LoginChatGPT(ctx, source); err != nil {
		t.Fatal(err)
	}
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "planner"}, "Protocol qualification only. Do not use any tools. Reply with exactly ENGORCH_RUNTIME_OK.")
	if err != nil {
		t.Fatal(err)
	}
	a := &codexruntime.Adapter{Client: h.Client, JournalPath: filepath.Join(l.Root, "runtime.jsonl"), Directory: filepath.Join(l.Root, "workspace")}
	r, err := a.Execute(ctx, i)
	if err != nil {
		if state, inspectErr := codexruntime.Inspect(a.JournalPath); inspectErr == nil {
			t.Logf("observed terminal status=%s; turn handle recorded=%t", state.TurnStatus, state.TurnID != "")
		}
		t.Fatal(err)
	}
	if r.Output != "ENGORCH_RUNTIME_OK" {
		t.Fatalf("unexpected qualification response %q", r.Output)
	}
	s, err := codexruntime.Inspect(a.JournalPath)
	if err != nil || s.Result == nil || s.TurnStatus != "completed" {
		t.Fatal("runtime result not durably admitted", err)
	}
	if _, err := os.Stat(filepath.Join(l.Root, "home", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("external authentication unexpectedly persisted an auth file")
	}
	t.Logf("live model PASS: requested=%s observed=%s; final output and durable receipt verified", i.Profile.Model, *r.ObservedModel)
	if out := os.Getenv("ENGORCH_CODEX_EVIDENCE"); out != "" {
		if !filepath.IsAbs(out) {
			t.Fatal("absolute evidence output required")
		}
		b, err := json.MarshalIndent(struct {
			Launch  Launch             `json:"launch"`
			Receipt Receipt            `json:"host_receipt"`
			State   codexruntime.State `json:"runtime_state"`
		}{l, h.Receipt, s}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
