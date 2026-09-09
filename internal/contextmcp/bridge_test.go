package contextmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/toolbridge"
)

func TestDurableContextOverMCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	source, err := repository.Discover(ctx, root, "context-mcp-fixture")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(strings.Repeat("a", 64), source, nil, contextbroker.Limits{MaxCalls: 8, MaxRequestBytes: 4096, MaxResponseBytes: MaxContentBytes, MaxTotalResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context.jsonl")
	broker, err := contextbroker.Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	const token = "0123456789abcdef0123456789abcdef"
	t.Run("oversized broker rejected before transport", func(t *testing.T) {
		oversized := binding
		oversized.Limits.MaxResponseBytes = MaxContentBytes + 1
		oversizedPath := filepath.Join(t.TempDir(), "oversized.jsonl")
		other, err := contextbroker.Open(oversizedPath, oversized)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		if server, err := New(other, token, nil); err == nil || server != nil {
			t.Fatal("unframeable broker admitted")
		}
		state, err := contextbroker.Inspect(oversizedPath)
		if err != nil || state.Calls != 0 || state.Pending != nil {
			t.Fatal("configuration rejection executed a call", err)
		}
	})
	server, err := New(broker, token, nil)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if err := running.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	call := func(id, arguments string) (json.RawMessage, string, bool) {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"source_read","arguments":` + arguments + `}}`
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, running.URL, bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", toolbridge.ProtocolVersion)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("MCP response: %d %v", response.StatusCode, err)
		}
		var result struct {
			Error  json.RawMessage `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		text := ""
		if len(result.Result.Content) > 0 {
			text = result.Result.Content[0].Text
		}
		return result.Error, text, result.Result.IsError
	}
	args := `{"path":"source.txt","offset":0,"limit":32}`
	for _, id := range []string{`1`, `"1"`} {
		failure, text, isError := call(id, args)
		if len(failure) != 0 || isError || !strings.Contains(text, "committed bytes") {
			t.Fatal("source result not delivered", string(failure))
		}
		state, err := contextbroker.Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		exact, err := canonical.Bytes(state.Responses[len(state.Responses)-1])
		if err != nil || string(exact) != text {
			t.Fatal("delivered bytes differ from durable response", err)
		}
	}
	failure, text, isError := call(`2`, `{"path":"absent.txt","offset":0,"limit":32}`)
	if len(failure) != 0 || !isError {
		t.Fatal("domain failure lost MCP error flag")
	}
	state, err := contextbroker.Inspect(path)
	if err != nil || len(state.Responses) != 3 {
		t.Fatal("domain failure missing from journal", err)
	}
	exact, err := canonical.Bytes(state.Responses[2])
	if err != nil || string(exact) != text {
		t.Fatal("domain failure differs from journal", err)
	}
	if failure, _, _ := call(`1`, args); len(failure) == 0 {
		t.Fatal("duplicate request replayed")
	}
	state, err = contextbroker.Inspect(path)
	if err != nil || state.Calls != 3 {
		t.Fatal("duplicate request changed durable calls", err)
	}
}
