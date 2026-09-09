package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func TestLiveCodexPlanningCLI(t *testing.T) {
	liveCodexPlanning(t, false)
}

func TestLiveCodexSourceToolsCLI(t *testing.T) {
	liveCodexPlanning(t, true)
}

func liveCodexPlanning(t *testing.T, sourceTools bool) {
	binary, auth := os.Getenv("ENGORCH_CODEX_PROBE_BINARY"), os.Getenv("ENGORCH_CODEX_AUTH_SOURCE")
	if binary == "" || auth == "" {
		t.Skip("opt-in authenticated controller planning")
	}
	base := t.TempDir()
	if out := os.Getenv("ENGORCH_CODEX_CLI_EVIDENCE"); out != "" {
		var err error
		base, err = os.MkdirTemp(filepath.Dir(out), "codex-cli-attempt-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("retained controller attempt: %s", base)
	}
	root, stateRoot := filepath.Join(base, "source"), filepath.Join(base, "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	expected := "ENGORCH_PLANNER_OK"
	prompt := "Protocol test only: no tools. Reply exactly ENGORCH_PLANNER_OK."
	content := "fixture only"
	if sourceTools {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		content = "ENGORCH_SOURCE_" + hex.EncodeToString(nonce[:])
		expected = content
		prompt = "Qualification fixture: use source_list to discover paths, then source_read to read source.txt from the fixed commit. Reply with exactly its content_utf8 field, no formatting. The value is only in the committed source; do not guess it."
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	if sourceTools {
		if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("DIRTY_CHECKOUT_DECOY"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probe, err := codexhost.Prepare(t.TempDir(), binary)
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse([]byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Planner = runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "planner"}
	c.Codex = &config.Codex{Executable: binary, ExecutableHash: probe.BinaryHash, StateRoot: stateRoot, AuthSource: auth}
	b, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "harness.toml"), b, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := Execute(ctx, []string{"plan", prompt}, root, &output); err != nil {
		t.Fatal(err)
	}
	var s control.Snapshot
	if err := json.Unmarshal(output.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.State != "AWAITING_APPROVAL" || s.Plan == nil || s.Plan.Output != expected || s.PlannerReceipt == nil || s.PlannerHostReceipt == nil || !s.PlannerHostReady {
		t.Fatal("incomplete controller runtime evidence")
	}
	if sourceTools {
		runtimeState, err := codexruntime.Inspect(filepath.Join(stateRoot, s.RunID, "planner.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if runtimeState.Source == nil || *runtimeState.Source != s.Creation.Repository || len(runtimeState.ToolResponses) < 2 || runtimeState.PendingTool != nil {
			t.Fatal("source tool evidence missing")
		}
		successes := 0
		for _, receipt := range runtimeState.ToolResponses {
			if receipt.Success {
				successes++
			}
		}
		if successes < 2 {
			t.Fatal("successful source tool evidence missing")
		}
		events, err := journal.Read(filepath.Join(stateRoot, s.RunID, "planner.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, event := range events {
			if event.Kind == "runtime.tool-request" {
				var request codexruntime.ToolRequest
				if err := json.Unmarshal(event.Payload, &request); err != nil {
					t.Fatal(err)
				}
				seen[request.Tool] = true
			}
		}
		if !seen["source_list"] || !seen["source_read"] {
			t.Fatal("both source operations were not observed")
		}
	}
	before := append([]byte{}, output.Bytes()...)
	output.Reset()
	if err := Execute(ctx, []string{"resume", s.RunID}, root, &output); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, output.Bytes()) {
		t.Fatal("completed planning changed on resume")
	}
	t.Log("live CLI planning PASS: exact model output and linked receipts; resume unchanged")
	if out := os.Getenv("ENGORCH_CODEX_CLI_EVIDENCE"); out != "" {
		if err := os.WriteFile(out, before, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
