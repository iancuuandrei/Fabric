package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestScopedToolHandlerAndApprovalDenial(t *testing.T) {
	a, b := net.Pipe()
	called := 0
	c := NewWithToolHandler(a, func(ctx context.Context, p json.RawMessage) (json.RawMessage, error) {
		called++
		return json.RawMessage(`{"success":true,"contentItems":[]}`), nil
	})
	defer c.Close()
	done := make(chan []Message, 1)
	go func() {
		defer b.Close()
		r := bufio.NewReader(b)
		var replies []Message
		for _, method := range []string{"item/tool/call", "item/commandExecution/requestApproval"} {
			fmt.Fprintf(b, "{\"id\":\"request\",\"method\":%q,\"params\":{}}\n", method)
			line, e := r.ReadBytes('\n')
			if e != nil {
				break
			}
			m, e := Decode(line)
			if e != nil {
				break
			}
			replies = append(replies, m)
		}
		done <- replies
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m, e := c.Receive(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Completion(m, "thread", "turn"); e != nil {
		t.Fatal(e)
	}
	m, e = c.Receive(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := Completion(m, "thread", "turn"); e == nil {
		t.Fatal("approval was treated as handled tool")
	}
	replies := <-done
	if called != 1 || len(replies) != 2 || len(replies[0].Result) == 0 || len(replies[1].Error) == 0 {
		t.Fatal(called, replies)
	}
}
