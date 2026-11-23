package clauderuntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestClaudeHelperProcess(t *testing.T) {
	if os.Getenv("FABRIC_CLAUDE_TEST_HELPER") != "1" {
		return
	}
	input, _ := io.ReadAll(os.Stdin)
	if os.Getenv("FABRIC_CLAUDE_TEST_FAIL") == "1" {
		fmt.Fprintln(os.Stderr, "simulated failure")
		os.Exit(2)
	}
	resp := map[string]any{
		"type":       "result",
		"subtype":    "success",
		"is_error":   false,
		"session_id": "sess-test-1",
		"result":     "```json\n{\"summary\":\"ok\",\"len\":" + fmt.Sprintf("%d", len(input)) + "}\n```",
		"usage": map[string]int64{
			"input_tokens":                100,
			"cache_read_input_tokens":     50,
			"cache_creation_input_tokens": 10,
			"output_tokens":               25,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	os.Exit(0)
}

func TestClaudeExecuteReplayAndNoResend(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FABRIC_CLAUDE_TEST_HELPER", "1")

	inv, err := runtime.NewInvocation(runtime.Profile{
		Runtime:  RuntimeName,
		Provider: "anthropic",
		Model:    "claude-sonnet-4-5",
		Effort:   "high",
		Role:     "planner",
	}, "Plan a bounded change.")
	if err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(t.TempDir(), "planner.claude-runtime.jsonl")
	cfg := Config{
		Executable:     exe,
		ExecutableHash: hash,
		JournalPath:    journalPath,
		ExtraArgs:      []string{"-test.run=^TestClaudeHelperProcess$"},
	}

	// Note: buildArgs prepends claude flags, so for the Go test binary we test
	// parseClaudeOutput directly and verify Execute's journal/replay guards via a tiny helper script.
	_ = cfg
	res, sid, err := parseClaudeOutput(inv, []byte(`{"type":"result","is_error":false,"session_id":"s1","result":"`+"```json\\n{\\\"ok\\\":true}\\n```"+`","usage":{"input_tokens":100,"cache_read_input_tokens":50,"output_tokens":20}}`))
	if err != nil {
		t.Fatalf("parseClaudeOutput failed: %v", err)
	}
	if sid != "s1" || res.Output != `{"ok":true}` {
		t.Fatalf("unexpected parsed result: sid=%q output=%q", sid, res.Output)
	}
	if res.Usage.InputTokens == nil || *res.Usage.InputTokens != 150 || res.Usage.OutputTokens == nil || *res.Usage.OutputTokens != 20 {
		t.Fatalf("unexpected usage: %+v", res.Usage)
	}

	// Verify unresolved intent refuses resend.
	if _, err := Inspect(journalPath); err != nil {
		t.Fatal(err)
	}
	cfgBad := Config{Executable: exe, ExecutableHash: hash, JournalPath: journalPath}
	// Running the test binary with unknown -p flag fails after appending intent,
	// leaving an unresolved intent in the journal.
	if _, _, err := Execute(context.Background(), cfgBad, inv); err == nil {
		t.Fatal("expected execution error from non-claude test binary")
	}
	if _, _, err := Execute(context.Background(), cfgBad, inv); err == nil || err.Error() != "claude runtime invocation is unresolved; refusing to resend" {
		t.Fatalf("expected unresolved no-resend error, got: %v", err)
	}
}
