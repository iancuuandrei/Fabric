package toolbridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type asyncHTTPResult struct {
	response *http.Response
	err      error
}

func postToolCallAsync(t *testing.T, server *testRunning, id string) <-chan asyncHTTPResult {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"` + id + `","method":"tools/call","params":{"name":"wait","arguments":{}}}`
	request, err := http.NewRequest(http.MethodPost, server.running.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	result := make(chan asyncHTTPResult, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		result <- asyncHTTPResult{response: response, err: requestErr}
	}()
	return result
}

func awaitHTTPResult(t *testing.T, result <-chan asyncHTTPResult) *http.Response {
	t.Helper()
	select {
	case completed := <-result:
		if completed.err != nil {
			t.Fatal(completed.err)
		}
		return completed.response
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not complete")
		return nil
	}
}

func awaitStartedCall(t *testing.T, started <-chan string, want string) {
	t.Helper()
	select {
	case got := <-started:
		if got != want {
			t.Fatalf("callback order %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("callback %q did not start", want)
	}
}

func awaitQueuedCalls(t *testing.T, server *testRunning, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		server.bridge.mu.Lock()
		got := len(server.bridge.queuedCalls)
		server.bridge.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued calls did not reach %d", want)
}

func queueTestConfig(call CallFunc, queued int) Config {
	return Config{
		Token:              testToken,
		MaxConcurrentCalls: 1,
		MaxQueuedCalls:     queued,
		Catalog: func() ([]ToolDefinition, error) {
			return []ToolDefinition{{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil
		},
		Call: call,
	}
}

func TestQueuedCallsRunInValidatedAdmissionOrder(t *testing.T) {
	started := make(chan string, 3)
	releases := map[string]chan struct{}{
		`"first"`:  make(chan struct{}),
		`"second"`: make(chan struct{}),
		`"third"`:  make(chan struct{}),
	}
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, call Call) (Result, error) {
		id := string(call.RequestID)
		started <- id
		select {
		case <-releases[id]:
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, 2))

	first := postToolCallAsync(t, server, "first")
	awaitStartedCall(t, started, `"first"`)
	second := postToolCallAsync(t, server, "second")
	awaitQueuedCalls(t, server, 1)
	third := postToolCallAsync(t, server, "third")
	awaitQueuedCalls(t, server, 2)

	close(releases[`"first"`])
	assertStatus(t, awaitHTTPResult(t, first), http.StatusOK)
	awaitStartedCall(t, started, `"second"`)
	select {
	case id := <-started:
		t.Fatalf("later callback started before FIFO predecessor completed: %s", id)
	default:
	}
	close(releases[`"second"`])
	assertStatus(t, awaitHTTPResult(t, second), http.StatusOK)
	awaitStartedCall(t, started, `"third"`)
	close(releases[`"third"`])
	assertStatus(t, awaitHTTPResult(t, third), http.StatusOK)
}

func TestQueuedCallCapacityIsBounded(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, call Call) (Result, error) {
		started <- string(call.RequestID)
		select {
		case <-release:
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, 1))

	first := postToolCallAsync(t, server, "first")
	awaitStartedCall(t, started, `"first"`)
	second := postToolCallAsync(t, server, "second")
	awaitQueuedCalls(t, server, 1)
	third := server.post(t, `{"jsonrpc":"2.0","id":"third","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	assertStatus(t, third, http.StatusTooManyRequests)
	_ = readResponse(t, third)
	close(release)
	assertStatus(t, awaitHTTPResult(t, first), http.StatusOK)
	assertStatus(t, awaitHTTPResult(t, second), http.StatusOK)
}

func TestDuplicateQueuedRequestIDIsRejected(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, call Call) (Result, error) {
		started <- string(call.RequestID)
		select {
		case <-release:
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, 1))

	first := postToolCallAsync(t, server, "first")
	awaitStartedCall(t, started, `"first"`)
	queued := postToolCallAsync(t, server, "queued")
	awaitQueuedCalls(t, server, 1)
	duplicate := server.post(t, `{"jsonrpc":"2.0","id":"que\u0075ed","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	body := readResponse(t, duplicate)
	if duplicate.StatusCode != http.StatusOK || !strings.Contains(string(body), "duplicate active request id") {
		t.Fatal("queued duplicate was not rejected", duplicate.StatusCode, string(body))
	}
	close(release)
	assertStatus(t, awaitHTTPResult(t, first), http.StatusOK)
	assertStatus(t, awaitHTTPResult(t, queued), http.StatusOK)
}

func TestQueuedCancellationDoesNotInvokeCallback(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, call Call) (Result, error) {
		started <- string(call.RequestID)
		select {
		case <-release:
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, 1))

	first := postToolCallAsync(t, server, "first")
	awaitStartedCall(t, started, `"first"`)
	queued := postToolCallAsync(t, server, "queued")
	awaitQueuedCalls(t, server, 1)
	cancelled := server.post(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"queued"}}`, true)
	assertStatus(t, cancelled, http.StatusAccepted)
	_ = readResponse(t, cancelled)
	queuedResponse := awaitHTTPResult(t, queued)
	queuedBody := readResponse(t, queuedResponse)
	if queuedResponse.StatusCode != http.StatusOK || !strings.Contains(string(queuedBody), `"code":-32800`) {
		t.Fatal("queued cancellation response changed", queuedResponse.StatusCode, string(queuedBody))
	}
	select {
	case id := <-started:
		t.Fatalf("cancelled queued callback ran: %s", id)
	default:
	}
	close(release)
	assertStatus(t, awaitHTTPResult(t, first), http.StatusOK)
}

func TestCloseCancelsAndDrainsRunningAndQueuedCalls(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, _ Call) (Result, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}, 1))
	active := postToolCallAsync(t, server, "active")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active callback did not start")
	}
	queued := postToolCallAsync(t, server, "queued")
	awaitQueuedCalls(t, server, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.running.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, response := range []*http.Response{awaitHTTPResult(t, active), awaitHTTPResult(t, queued)} {
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"code":-32800`) {
			t.Fatal("shutdown did not cancel admitted request", response.StatusCode, string(body), err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("shutdown admitted queued callback", calls.Load())
	}
}

func TestQueuedDeadlineIncludesWaitingWithoutCallingBackend(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	config := queueTestConfig(func(context.Context, Call) (Result, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		// Keep the execution slot occupied even after its context expires.
		// The queued call must expire independently, without starting a backend.
		<-release
		return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
	}, 1)
	config.CallTimeout = 200 * time.Millisecond
	server := newTestServer(t, config)
	active := postToolCallAsync(t, server, "active")
	defer func() {
		close(release)
		_ = readResponse(t, awaitHTTPResult(t, active))
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active callback did not start")
	}
	queued := postToolCallAsync(t, server, "queued")
	response := awaitHTTPResult(t, queued)
	body := readResponse(t, response)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"code":-32800`) || calls.Load() != 1 {
		t.Fatal("queue wait escaped deadline or invoked backend", response.StatusCode, string(body), calls.Load())
	}
}

func TestMaxQueuedCallsZeroPreservesImmediateRejection(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := newTestServer(t, queueTestConfig(func(ctx context.Context, _ Call) (Result, error) {
		close(started)
		select {
		case <-release:
			return Result{JSON: json.RawMessage(`{"ok":true}`)}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, 0))
	active := postToolCallAsync(t, server, "active")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("active callback did not start")
	}
	rejected := server.post(t, `{"jsonrpc":"2.0","id":"rejected","method":"tools/call","params":{"name":"wait","arguments":{}}}`, true)
	assertStatus(t, rejected, http.StatusTooManyRequests)
	_ = readResponse(t, rejected)
	close(release)
	assertStatus(t, awaitHTTPResult(t, active), http.StatusOK)
}
