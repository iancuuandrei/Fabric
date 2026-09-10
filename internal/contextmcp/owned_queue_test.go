package contextmcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/toolbridge"
)

func TestNewOwnedWithQueueValidatesDepth(t *testing.T) {
	broker := bridgeFixture(t)
	bearer := strings.Repeat("o", 40)
	for _, depth := range []int{-1, 33, 100} {
		if _, err := NewOwnedWithQueue(broker, bearer, nil, depth); err == nil {
			t.Fatalf("queue depth %d admitted", depth)
		}
	}
	for _, depth := range []int{0, 1, 32} {
		server, err := NewOwnedWithQueue(broker, bearer, nil, depth)
		if err != nil {
			t.Fatalf("queue depth %d rejected: %v", depth, err)
		}
		running, err := server.Listen()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}
}

func TestQueuedOwnedServerKeepsExactCatalogAndOwner(t *testing.T) {
	broker := bridgeFixture(t)
	bearer := strings.Repeat("o", 40)
	strict, err := NewOwned(broker, bearer, nil)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := NewOwnedWithQueue(broker, bearer, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	strictRunning, err := strict.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = strictRunning.Close(ctx)
		cancel()
		_ = strictRunning.Wait()
	}()
	queuedRunning, err := queued.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = queuedRunning.Close(ctx)
		cancel()
		_ = queuedRunning.Wait()
	}()
	if strictRunning.CatalogHash() == "" || strictRunning.CatalogHash() != queuedRunning.CatalogHash() {
		t.Fatal("queue depth changed the served catalog identity")
	}
	if err := queuedRunning.ValidateOwner(broker, bearer); err != nil {
		t.Fatal("queued bridge lost constructor ownership", err)
	}
}

func postOwnedCall(t *testing.T, url, bearer, id string) *http.Response {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"` + id + `","method":"tools/call","params":{"name":"source_list","arguments":{"after":"","limit":1}}}`
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("MCP-Protocol-Version", toolbridge.ProtocolVersion)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestQueuedOwnedServerAdmitsSimultaneousBurst(t *testing.T) {
	broker := bridgeFixture(t)
	bearer := strings.Repeat("o", 40)
	server, err := NewOwnedWithQueue(broker, bearer, nil, 8)
	if err != nil {
		t.Fatal(err)
	}
	running, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = running.Close(ctx)
		cancel()
		_ = running.Wait()
	}()
	const burst = 8
	type outcome struct {
		status int
		body   string
	}
	results := make(chan outcome, burst)
	var group sync.WaitGroup
	for i := 0; i < burst; i++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			response := postOwnedCall(t, running.URL(), bearer, strings.Repeat("q", 60)+string(rune('a'+n)))
			defer response.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			results <- outcome{status: response.StatusCode, body: string(raw)}
		}(i)
	}
	group.Wait()
	close(results)
	for result := range results {
		if result.status != http.StatusOK {
			t.Fatalf("burst call rejected with status %d: %s", result.status, result.body)
		}
		if strings.Contains(result.body, "concurrency limit reached") {
			t.Fatalf("queued burst hit concurrency rejection: %s", result.body)
		}
	}
}
