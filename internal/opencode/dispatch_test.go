package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/journal"
)

func TestDurableDispatchRecoversWithoutReposting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch.jsonl")
	intent := DispatchIntent{Binding: Binding{SessionID: "ses_fixture", ParentID: "msg_fixture", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}, Text: "task"}
	posts, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posts++
			events, err := journal.Read(path)
			if err != nil {
				t.Error(err)
			}
			state, err := replayDispatch(events)
			if err != nil || state.Intent == nil || *state.Intent != intent {
				t.Error("POST preceded durable intent")
			}
			// Simulate persistence followed by an uncertain response.
			w.WriteHeader(500)
			return
		}
		reads++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"info":{"id":"msg_fixture","sessionID":"ses_fixture","role":"user","agent":"build","model":{"providerID":"fixture","modelID":"model"}},"parts":[{"id":"prt_fixture","sessionID":"ses_fixture","messageID":"msg_fixture","type":"text","text":"task"}]}`))
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.SubmitPrompt(ctx, path, intent); err == nil {
		t.Fatal("uncertain dispatch accepted")
	}
	if err := c.SubmitPrompt(ctx, path, intent); err == nil || posts != 1 {
		t.Fatal("dispatch repeated")
	}
	foreign := intent
	foreign.Text = "other"
	if err := c.RecoverPrompt(ctx, path, foreign); err == nil || reads != 0 {
		t.Fatal("foreign recovery admitted")
	}
	if err := c.RecoverPrompt(ctx, path, intent); err != nil {
		t.Fatal(err)
	}
	if err := c.RecoverPrompt(ctx, path, intent); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || posts != 1 {
		t.Fatalf("posts=%d reads=%d", posts, reads)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := replayDispatch(events)
	if err != nil || !state.Observed || state.Accepted {
		t.Fatal("incorrect recovered state", err)
	}
}
