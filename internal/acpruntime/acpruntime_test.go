package acpruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestACPHelperProcess(t *testing.T) {
	if os.Getenv("FABRIC_ACP_TEST_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req rpcMessage
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1}}`+"\n", string(req.ID))
		case "session/new":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"acp-sess-1"}}`+"\n", string(req.ID))
		case "session/prompt":
			// First try an unauthorized write request; client must deny it.
			fmt.Println(`{"jsonrpc":"2.0","id":99,"method":"fs/write_text_file","params":{"path":"x"}}`)
			sc.Scan() // read denial
			// Stream two message chunks.
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"{\"plan\":"}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"\"ok\"}"}}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", string(req.ID))
		}
	}
	os.Exit(0)
}

func TestACPStdioExecuteAndJournalReplay(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FABRIC_ACP_TEST_HELPER", "1")

	inv, err := runtime.NewInvocation(runtime.Profile{
		Runtime:  RuntimeName,
		Provider: "acp-agent",
		Model:    "default",
		Effort:   "medium",
		Role:     "planner",
	}, "Produce a plan.")
	if err != nil {
		t.Fatal(err)
	}

	journalPath := filepath.Join(t.TempDir(), "planner.acp-runtime.jsonl")
	cfg := Config{
		Executable:     exe,
		ExecutableHash: hash,
		Args:           []string{"-test.run=^TestACPHelperProcess$"},
		JournalPath:    journalPath,
	}

	res, head1, err := Execute(context.Background(), cfg, inv)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if res.Output != `{"plan":"ok"}` {
		t.Fatalf("unexpected output: %q", res.Output)
	}
	if head1 == "" {
		t.Fatal("expected non-empty journal head")
	}

	// Second call must replay from journal without invoking the agent again.
	t.Setenv("FABRIC_ACP_TEST_HELPER", "0")
	res2, head2, err := Execute(context.Background(), cfg, inv)
	if err != nil {
		t.Fatalf("replay Execute failed: %v", err)
	}
	if res2.Output != res.Output || head2 != head1 {
		t.Fatalf("replay mismatch: %q vs %q", res2.Output, res.Output)
	}
}
