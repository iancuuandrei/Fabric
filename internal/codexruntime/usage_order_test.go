package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/codexrpc"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageBeforeTurnResponseStopsBeforeToolReply(t *testing.T) {
	root := t.TempDir()
	left, right := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer right.Close()
		r := bufio.NewReader(right)
		read := func() (codexrpc.Message, error) {
			b, e := r.ReadBytes('\n')
			if e != nil {
				return codexrpc.Message{}, e
			}
			return codexrpc.Decode(b)
		}
		m, e := read()
		if e != nil {
			done <- e
			return
		}
		b, _ := json.Marshal(threadResponse(root, "explicit-model"))
		fmt.Fprintf(right, "{\"id\":%s,\"result\":%s}\n", m.ID, b)
		m, e = read()
		if e != nil {
			done <- e
			return
		}
		// All three messages may already be buffered. Usage must gate processing
		// before either a tool reply or the turn/start response is admitted.
		messages := []any{
			map[string]any{"method": "thread/tokenUsage/updated", "params": usageParams(105)},
			map[string]any{"id": 99, "method": "item/tool/call", "params": map[string]any{"threadId": "thread-1", "turnId": "turn-1", "callId": "forbidden", "tool": "source_list", "arguments": map[string]any{}}},
			map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}},
		}
		var wire []byte
		for _, message := range messages {
			b, _ := json.Marshal(message)
			wire = append(wire, append(b, '\n')...)
		}
		if _, e = right.Write(wire); e != nil {
			done <- e
			return
		}
		m, e = read()
		if e != nil || m.Method != "turn/interrupt" {
			done <- fmt.Errorf("tool reply or missing interrupt: %s %v", m.Method, e)
			return
		}
		done <- nil
	}()
	a := &Adapter{Client: codexrpc.New(left), JournalPath: filepath.Join(root, "usage.db"), Directory: root, UsageBudget: 100, RequireLiveUsage: true, UsageQualified: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := a.Execute(ctx, invocation(t))
	if err == nil || !strings.Contains(err.Error(), "BUDGET_EXHAUSTED") {
		t.Fatal(err)
	}
	s, err := Inspect(a.JournalPath)
	if err != nil || s.UsageTurnID != "turn-1" || !s.UsageInterrupt || s.PendingTool != nil || len(s.ToolResponses) != 0 {
		t.Fatal(s, err)
	}
	a.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
