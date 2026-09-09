package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestSessionCreationIntentPreventsAutomaticRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	b := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		events, err := journal.Read(path)
		if err != nil {
			t.Error(err)
		}
		state, err := replaySessionCreation(events)
		if err != nil || state.Binding == nil || *state.Binding != b {
			t.Error("creation preceded durable intent")
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 2; i++ {
		if id, err := c.CreateSession(context.Background(), path, b); err == nil || id != "" {
			t.Fatal("uncertain session admitted")
		}
	}
	if calls != 1 {
		t.Fatal("creation retried")
	}
	foreign := b
	foreign.Model = "other"
	if id, err := c.RecoverSession(context.Background(), path, foreign); err == nil || id != "" || calls != 1 {
		t.Fatal("foreign recovery admitted")
	}
	// A recorded intent with no observed locator stays unresolved after a failed
	// lookup. It must never become permission to create a replacement session.
	if id, err := c.RecoverSession(context.Background(), path, b); err == nil || id != "" {
		t.Fatal("failed recovery admitted")
	}
	if id, err := c.CreateSession(context.Background(), path, b); err == nil || id != "" || calls != 2 {
		t.Fatal("recovery authorized recreation")
	}
}
