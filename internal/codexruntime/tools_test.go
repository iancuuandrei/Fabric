package codexruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/repository"
)

func sourceAdapter(t *testing.T) *Adapter {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if b, e := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); e != nil {
			t.Fatal(e, string(b))
		}
	}
	run("init", "-q")
	if e := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed"), 0600); e != nil {
		t.Fatal(e)
	}
	run("add", "source.txt")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	source, e := repository.Discover(context.Background(), root, "broker-fixture")
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, "source.txt"), []byte("dirty"), 0600); e != nil {
		t.Fatal(e)
	}
	a := &Adapter{JournalPath: filepath.Join(t.TempDir(), "runtime.jsonl"), Directory: root, Source: &source}
	i := invocation(t)
	effort := i.Profile.Effort
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", Intent{i, root}},
		{"runtime.source", source},
		{"runtime.thread", codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &effort, Directory: root, Approval: "never", Sandbox: "readOnly"}},
		{"runtime.turn-intent", map[string]any{"invocation_id": i.ID}},
	} {
		if e := appendEvent(a.JournalPath, event.kind, event.payload); e != nil {
			t.Fatal(e)
		}
	}
	return a
}

func TestSourceToolsVisibleCatalogUnchanged(t *testing.T) {
	adapter := &Adapter{Source: &repository.Identity{}}
	got, err := canonical.Bytes(adapter.SourceTools())
	if err != nil {
		t.Fatal(err)
	}
	objectSchema := func(properties map[string]any, required []string) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	want, err := canonical.Bytes([]any{
		map[string]any{"type": "function", "deferLoading": false, "name": "source_list", "description": "List paths from the fixed source commit. limit must be 1 to 128; use after empty string initially. Follow next_after until null; includes symlinks/submodules as explicit kinds. No relevance filtering.", "inputSchema": objectSchema(map[string]any{"after": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 128}}, []string{"after", "limit"})},
		map[string]any{"type": "function", "deferLoading": false, "name": "source_read", "description": "Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. Returns content_utf8 when valid UTF-8 and always content_base64, with chunk hash. Follow next_offset until null. Working tree changes do not affect this source.", "inputSchema": objectSchema(map[string]any{"path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "integer", "minimum": 0}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 32768}}, []string{"path", "offset", "limit"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("provider-visible source catalog changed\n got: %s\nwant: %s", got, want)
	}
}

func TestSourceBrokerImmutableReadAndScope(t *testing.T) {
	a := sourceAdapter(t)
	request := ToolRequest{Arguments: json.RawMessage(`{"path":"source.txt","offset":0,"limit":32}`), CallID: "read-1", ThreadID: "thread-1", TurnID: "turn-1", Tool: "source_read"}
	raw, e := canonical.Bytes(request)
	if e != nil {
		t.Fatal(e)
	}
	response, e := a.HandleTool(context.Background(), raw)
	if e != nil {
		t.Fatal(e)
	}
	var wire struct {
		Success      bool `json:"success"`
		ContentItems []struct {
			Text string `json:"text"`
		} `json:"contentItems"`
	}
	if e := json.Unmarshal(response, &wire); e != nil {
		t.Fatal(e)
	}
	if !wire.Success || len(wire.ContentItems) != 1 {
		t.Fatal(string(response))
	}
	var chunk repository.SourceChunk
	if e := canonical.Decode([]byte(wire.ContentItems[0].Text), &chunk); e != nil {
		t.Fatal(e)
	}
	data, e := base64.StdEncoding.DecodeString(chunk.ContentBase64)
	if e != nil || string(data) != "committed" {
		t.Fatal(string(data), e)
	}
	s, e := Inspect(a.JournalPath)
	if e != nil || s.PendingTool != nil || len(s.ToolResponses) != 1 || s.ToolResponses[0].Content != wire.ContentItems[0].Text {
		t.Fatal(s, e)
	}
	if _, e := a.HandleTool(context.Background(), raw); e == nil {
		t.Fatal("duplicate call admitted")
	}
	request.CallID = "foreign"
	request.TurnID = "turn-2"
	raw, _ = canonical.Bytes(request)
	if _, e := a.HandleTool(context.Background(), raw); e == nil {
		t.Fatal("foreign turn admitted")
	}
	if e := appendEvent(a.JournalPath, "runtime.turn", map[string]any{"id": "turn-2"}); e == nil {
		t.Fatal("dispatch turn mismatch admitted")
	}
	if e := appendEvent(a.JournalPath, "runtime.turn", map[string]any{"id": "turn-1"}); e != nil {
		t.Fatal(e)
	}
}

func TestSourceBrokerPendingAndInvalidArguments(t *testing.T) {
	a := sourceAdapter(t)
	r := ToolRequest{Arguments: json.RawMessage(`{"path":"../escape","offset":0,"limit":1}`), CallID: "invalid", ThreadID: "thread-1", TurnID: "turn-1", Tool: "source_read"}
	raw, _ := canonical.Bytes(r)
	if _, e := a.HandleTool(context.Background(), raw); e != nil {
		t.Fatal(e)
	}
	s, e := Inspect(a.JournalPath)
	if e != nil || s.ToolResponses[0].Success {
		t.Fatal(s, e)
	}
	r.CallID = "pending"
	if e := appendEvent(a.JournalPath, "runtime.tool-request", r); e != nil {
		t.Fatal(e)
	}
	if _, e := a.HandleTool(context.Background(), raw); e == nil {
		t.Fatal("pending request retried")
	}
	if e := appendEvent(a.JournalPath, "runtime.turn", map[string]any{"id": "turn-1"}); e != nil {
		t.Fatal(e)
	}
	if e := appendEvent(a.JournalPath, "runtime.turn-status", TurnStatus{"turn-1", "completed"}); e == nil {
		t.Fatal("unanswered tool admitted at completion")
	}
}
