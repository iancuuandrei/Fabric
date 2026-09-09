package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/journal"
)

func TestSynchronousDispatchPersistsBeforeSinglePostAndValidatesReadback(t *testing.T) {
	intent, response, transcript := synchronousFixture()
	path := filepath.Join(t.TempDir(), "sync.jsonl")
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/session/ses_fixture/message":
			posts.Add(1)
			events, err := journal.Read(path)
			if err != nil {
				t.Error(err)
			}
			state, err := replaySynchronousDispatch(events)
			if err != nil || state.Intent == nil || *state.Intent != intent || state.Observation != nil {
				t.Error("POST was not preceded by exact durable intent")
			}
			writeSynchronousJSON(w, response)
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_fixture/message":
			gets.Add(1)
			writeSynchronousJSON(w, transcript)
		default:
			t.Error("unexpected synchronous request", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	observation, err := client.SubmitSynchronousTextTurn(ctx, path, intent)
	if err != nil || observation.Text != "result" || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("synchronous dispatch failed", err, posts.Load(), gets.Load())
	}
	raw, err := journal.ExportJSONL(path)
	if err != nil || !strings.Contains(string(raw), `0.0123`) {
		t.Fatal("raw decimal transcript evidence was not retained", err)
	}
	var offline *Client
	replayed, err := offline.RecoverSynchronousTextTurn(context.Background(), path, intent)
	if err != nil || replayed != observation || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("offline synchronous replay failed", err)
	}
}

func TestSynchronousDispatchUncertainPostRecoversGetOnly(t *testing.T) {
	intent, _, transcript := synchronousFixture()
	path := filepath.Join(t.TempDir(), "sync.jsonl")
	var posts, gets atomic.Int32
	releasePost := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			<-releasePost
			return
		}
		if r.Method == http.MethodGet {
			gets.Add(1)
			writeSynchronousJSON(w, transcript)
			return
		}
		t.Error("unexpected method", r.Method)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	short, stopShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = client.SubmitSynchronousTextTurn(short, path, intent)
	stopShort()
	close(releasePost)
	if err == nil || posts.Load() != 1 {
		t.Fatal("uncertain POST was admitted", err)
	}
	retry, stopRetry := context.WithTimeout(context.Background(), time.Second)
	if _, err := client.SubmitSynchronousTextTurn(retry, path, intent); err == nil || posts.Load() != 1 {
		t.Fatal("unresolved synchronous dispatch was reposted")
	}
	stopRetry()
	recoverContext, stopRecover := context.WithTimeout(context.Background(), time.Second)
	observation, err := client.RecoverSynchronousTextTurn(recoverContext, path, intent)
	stopRecover()
	if err != nil || observation.Text != "result" || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("GET-only recovery failed", err, posts.Load(), gets.Load())
	}
	var offline *Client
	if replayed, err := offline.RecoverSynchronousTextTurn(context.Background(), path, intent); err != nil || replayed != observation || gets.Load() != 1 {
		t.Fatal("recovered observation was not replayable offline", err)
	}
}

func TestSynchronousDispatchRejectsUnboundedAndDivergentEvidence(t *testing.T) {
	intent, response, transcript := synchronousFixture()
	path := filepath.Join(t.TempDir(), "sync.jsonl")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			writeSynchronousJSON(w, strings.Replace(response, `"text":"result"`, `"text":"other"`, 1))
			return
		}
		writeSynchronousJSON(w, transcript)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.SubmitSynchronousTextTurn(context.Background(), path, intent); err == nil || posts != 0 {
		t.Fatal("unbounded synchronous request mutated state or posted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unbounded request created journal", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.SubmitSynchronousTextTurn(ctx, path, intent); err == nil || posts != 1 {
		t.Fatal("divergent POST/readback evidence accepted")
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 1 || events[0].Kind != "opencode.sync-dispatch-intent" {
		t.Fatal("divergent evidence was journaled", err)
	}
	foreign := intent
	foreign.Text = "other prompt"
	recoverContext, stopRecover := context.WithTimeout(context.Background(), time.Second)
	defer stopRecover()
	if _, err := client.RecoverSynchronousTextTurn(recoverContext, path, foreign); err == nil {
		t.Fatal("foreign intent recovered")
	}
}

func TestSynchronousDispatchRejectsCombinedEvidenceBeyondJournalBound(t *testing.T) {
	intent, response, _ := synchronousFixture()
	padding := strings.Repeat("x", 520<<10)
	response = strings.Replace(response, `},"parts"`, `},"padding":"`+padding+`","parts"`, 1)
	_, _, baseTranscript := synchronousFixture()
	transcript := strings.Replace(baseTranscript, `},"parts"`, `},"padding":"`+padding+`","parts"`, 1)
	path := filepath.Join(t.TempDir(), "sync.jsonl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writeSynchronousJSON(w, response)
			return
		}
		writeSynchronousJSON(w, transcript)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := client.SubmitSynchronousTextTurn(ctx, path, intent); err == nil {
		t.Fatal("combined evidence larger than one journal event was admitted")
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 1 || events[0].Kind != "opencode.sync-dispatch-intent" {
		t.Fatal("oversized combined evidence changed durable state", err)
	}
}

func TestSynchronousRecoveryRejectsAsyncJournal(t *testing.T) {
	intent, _, _ := synchronousFixture()
	path := filepath.Join(t.TempDir(), "async.jsonl")
	if _, err := journal.Append(path, "opencode.dispatch-intent", intent, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var client *Client
	if _, err := client.RecoverSynchronousTextTurn(context.Background(), path, intent); err == nil {
		t.Fatal("async journal was treated as synchronous evidence")
	}
}

func synchronousFixture() (DispatchIntent, string, string) {
	binding := Binding{SessionID: "ses_fixture", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}
	intent := DispatchIntent{Binding: binding, Text: "task"}
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","agent":"build","model":{"providerID":"fixture","modelID":"model"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":"task"}]}`
	response := `{"info":{"id":"msg_assistant","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","cost":0.0123,"time":{"created":10,"completed":20},"path":{"cwd":"/candidate","root":"/candidate"},"tokens":{"total":15,"input":10,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_assistant","messageID":"msg_assistant","sessionID":"ses_fixture","type":"text","text":"result","time":{"start":11,"end":19}}]}`
	return intent, response, "[" + user + "," + response + "]"
}

func writeSynchronousJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}
