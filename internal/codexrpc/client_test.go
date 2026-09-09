package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestBoundedProviderEnvelopes(t *testing.T) {
	for _, b := range []string{
		`{"id":1,"result":{"fraction":0.25}}`,
		`{"method":"turn/completed","params":{"threadId":"x"}}`,
		`{"method":"turn/completed","params":{},"emittedAtMs":12345}`,
		`{"jsonrpc":"2.0","id":"approval","method":"item/commandExecution/requestApproval","params":{}}`,
	} {
		if _, err := Decode([]byte(b)); err != nil {
			t.Fatal(b, err)
		}
	}
	for _, b := range []string{
		`{"id":1,"method":"item/tool/call","params":{},"toolHandled":true}`,
		`{"id":1,"id":2,"result":{}}`,
		`{"id":1,"result":{"nested":{"x":1,"x":2}}}`,
		`{"id":1,"result":{},"error":{}}`,
		`{"id":null,"result":{}}`,
		`{"id":1.5,"result":{}}`,
		`{"method":"x","emittedAtMs":null}`,
		`{"id":1,"result":{},"emittedAtMs":10}`,
		`{"id":1,"result":{},"method":"evil"}`,
		`{"id":1,"result":{},"unknown":true}`,
		`{"method":null,"params":{}}`,
		`{"method":"x","params":[]}`,
		`{"method":"x"} {"method":"y"}`,
		strings.Repeat("[", 66) + strings.Repeat("]", 66),
		strings.Repeat(" ", MaxMessage+1),
	} {
		if _, err := Decode([]byte(b)); err == nil {
			t.Fatal("accepted", b[:min(len(b), 100)])
		}
	}
}

func TestCallRetainsNotificationsAndRejectsServerApproval(t *testing.T) {
	clientStream, server := net.Pipe()
	c := New(clientStream)
	defer c.Close()
	serverDone := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		line, err := r.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		request, err := Decode(line)
		if err != nil {
			serverDone <- err
			return
		}
		fmt.Fprintln(server, `{"method":"thread/started","params":{"thread":{"id":"t1"}}}`)
		fmt.Fprintln(server, `{"id":"approve-1","method":"item/commandExecution/requestApproval","params":{}}`)
		line, err = r.ReadBytes('\n')
		if err != nil {
			serverDone <- err
			return
		}
		denial, err := Decode(line)
		if err != nil || string(denial.ID) != `"approve-1"` || len(denial.Error) == 0 {
			serverDone <- errors.New("server request was not denied")
			return
		}
		fmt.Fprintf(server, "{\"id\":%s,\"result\":{\"thread\":{\"id\":\"t1\"}}}\n", request.ID)
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, events, err := c.Call(ctx, "thread/start", map[string]any{"model": "explicit-model"})
	if err != nil || len(response.Result) == 0 || len(events) != 2 {
		t.Fatal(response, events, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCancellationUnblocksReadAndWriteWithoutRetry(t *testing.T) {
	for _, read := range []bool{true, false} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			clientStream, server := net.Pipe()
			defer server.Close()
			c := New(clientStream)
			defer c.Close()
			if read {
				go func() { _, _ = bufio.NewReader(server).ReadBytes('\n') }()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, _, err := c.Call(ctx, "initialize", map[string]any{})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if err := c.Notify(context.Background(), "initialized", map[string]any{}); err == nil {
				t.Fatal("reused uncertain connection")
			}
		})
	}
}

func TestMismatchAndUnboundedMessagesCloseConnection(t *testing.T) {
	for _, payload := range []string{`{"id":999,"result":{}}`, strings.Repeat("x", MaxMessage+1), `{"id":1,"result":{},"result":{}}`} {
		clientStream, server := net.Pipe()
		c := New(clientStream)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			_, _ = bufio.NewReader(server).ReadBytes('\n')
			_, _ = fmt.Fprintln(server, payload)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err := c.Call(ctx, "initialize", map[string]any{})
		cancel()
		if err == nil {
			t.Fatal("accepted invalid peer")
		}
		if err := c.Notify(context.Background(), "initialized", json.RawMessage(`{}`)); err == nil {
			t.Fatal("connection survived protocol failure")
		}
		_ = c.Close()
		<-done
	}
}
