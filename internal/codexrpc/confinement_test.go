package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/runtime"
)

func constrainedProfile() runtime.Profile {
	return runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-sol", Effort: "medium", Role: "planner"}
}

func TestConstrainedClientRejectsRawLifecycleCallsBeforeWrite(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := New(a)
	defer c.Close()
	if err := c.ConstrainThread(constrainedProfile(), `C:\workspace`, []any{}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"thread/start", "thread/resume", "turn/start", "review/start", "turn/steer", "thread/compact/start", "thread/queue/start", "thread/realtime/start", "thread/fork", "config/write", "account/logout"} {
		if _, _, err := c.Call(context.Background(), method, map[string]any{}); err == nil || !strings.Contains(err.Error(), "raw Codex method") {
			t.Fatalf("%s was not rejected: %v", method, err)
		}
	}
	if err := c.Notify(context.Background(), "turn/start", map[string]any{}); err == nil || !strings.Contains(err.Error(), "raw Codex notification") {
		t.Fatal("raw lifecycle notification was not rejected", err)
	}
	_ = b.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	buffer := make([]byte, 1)
	if n, err := b.Read(buffer); n != 0 || err == nil {
		t.Fatal("rejected raw lifecycle call wrote provider bytes")
	}
}

func TestConstrainedClientRejectsForeignThreadToolRequest(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	handled := false
	c := NewWithToolHandler(a, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		handled = true
		return json.RawMessage(`{"ok":true}`), nil
	})
	defer c.Close()
	if err := c.ConstrainThread(constrainedProfile(), `C:\workspace`, []any{}, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := c.bindConstrainedThread("root-thread"); err != nil {
		t.Fatal(err)
	}
	m := Message{ID: json.RawMessage(`1`), Method: "item/tool/call", Params: json.RawMessage(`{"threadId":"child-thread","turnId":"turn"}`)}
	if err := c.answer(context.Background(), &m); err == nil || !strings.Contains(err.Error(), "foreign Codex thread") {
		t.Fatal("foreign thread tool request was not rejected", err)
	}
	if handled {
		t.Fatal("foreign thread tool request reached handler")
	}
}

func TestConstrainedClientBindsProfileToolsWorkspaceAndExpiry(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := New(a)
	defer c.Close()
	profile := constrainedProfile()
	tools := []any{map[string]any{"name": "source_list"}}
	if err := c.ConstrainThread(profile, `C:\workspace`, tools, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := c.validateThreadConstraint(profile, `C:\workspace`, tools); err != nil {
		t.Fatal(err)
	}
	tools[0].(map[string]any)["name"] = "changed_after_binding"
	if err := c.validateThreadConstraint(profile, `C:\workspace`, []any{map[string]any{"name": "source_list"}}); err != nil {
		t.Fatal("constraint did not retain immutable hash snapshot", err)
	}
	if err := c.validateThreadConstraint(profile, `C:\other`, []any{map[string]any{"name": "source_list"}}); err == nil {
		t.Fatal("workspace substitution accepted")
	}
	if err := c.validateThreadConstraint(profile, `C:\workspace`, tools); err == nil {
		t.Fatal("dynamic tool substitution accepted")
	}
	c.constraint.expires = time.Now().Add(-time.Second)
	if err := c.validateThreadConstraint(profile, `C:\workspace`, []any{map[string]any{"name": "source_list"}}); err == nil {
		t.Fatal("expired constraint accepted")
	}
}

func TestConstrainedThreadStartsOnlyInExactChildDirectory(t *testing.T) {
	clientStream, server := net.Pipe()
	defer server.Close()
	c := New(clientStream)
	defer c.Close()
	profile := constrainedProfile()
	tools := []any{map[string]any{"name": "source_list"}}
	child := `C:\private\tasks\task-1\workspace`
	parent := `C:\private\workspace`
	if err := c.ConstrainThread(profile, child, tools, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartThreadWithTools(context.Background(), profile, parent, tools); err == nil {
		t.Fatal("parent workspace passed child-bound capability constraint")
	}
	_ = server.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := bufio.NewReader(server).ReadByte(); err == nil {
		t.Fatal("rejected parent workspace wrote a thread/start request")
	}
	_ = server.SetReadDeadline(time.Time{})
	serverDone := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(server).ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		var request Message
		if request, err = Decode(line); err != nil {
			serverDone <- err
			return
		}
		var params struct {
			CWD string `json:"cwd"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.CWD != child {
			serverDone <- fmt.Errorf("thread/start cwd=%q err=%v", params.CWD, err)
			return
		}
		_, err = fmt.Fprintf(server, `{"jsonrpc":"2.0","id":%s,"result":{"thread":{"id":"child-thread"},"model":"gpt-5.6-sol","modelProvider":"openai","reasoningEffort":"medium","cwd":%q,"approvalPolicy":"never","sandbox":{"type":"readOnly","networkAccess":false}}}`+"\n", request.ID, child)
		serverDone <- err
	}()
	settings, err := c.StartThreadWithTools(context.Background(), profile, child, tools)
	if err != nil || settings.ThreadID != "child-thread" || settings.Directory != child {
		t.Fatal("exact child workspace failed to start", settings, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
