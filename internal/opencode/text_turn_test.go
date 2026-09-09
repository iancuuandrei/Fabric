package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTextTurnRequiresExactCompleteHistory(t *testing.T) {
	b := Binding{SessionID: "ses_fixture", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","agent":"build","model":{"providerID":"fixture","modelID":"model"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":"task"}]}`
	assistant := `{"info":{"id":"msg_assistant","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","cost":0.0123,"time":{"created":10,"completed":20},"path":{"cwd":"/candidate","root":"/candidate"},"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}},"parts":[{"id":"prt_assistant","messageID":"msg_assistant","sessionID":"ses_fixture","type":"text","text":"result"}]}`
	valid := "[" + user + "," + assistant + "]"
	path := filepath.Join(t.TempDir(), "turn.jsonl")
	intent := DispatchIntent{Binding: b, Text: "task"}
	if err := appendDispatch(path, "opencode.dispatch-intent", intent); err != nil {
		t.Fatal(err)
	}
	record := struct {
		Transcript string `json:"transcript"`
	}{valid}
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.Method != "GET" || r.URL.Path != "/session/ses_fixture/message" || r.URL.RawQuery != "" {
			t.Error("unexpected transcript request")
		}
		w.Header().Set("Content-Type", "application/json")
		if reads == 1 {
			_, _ = w.Write([]byte("[" + user + "]"))
			return
		}
		_, _ = w.Write([]byte(valid))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.ObserveTextTurn(context.Background(), path, intent); err == nil {
		t.Fatal("unfinished turn persisted")
	}
	observed, err := client.ObserveTextTurn(context.Background(), path, intent)
	if err != nil || observed.Text != "result" || reads != 2 {
		t.Fatal("HTTP observation failed", err)
	}
	server.Close()
	var offline *Client
	observation, err := offline.ObserveTextTurn(context.Background(), path, intent)
	if err != nil || observation.Text != "result" {
		t.Fatal("offline observation replay", err)
	}
	if err := appendDispatch(path, "opencode.text-turn-observed", record); err == nil {
		t.Fatal("duplicate observation admitted")
	}
	foreign := intent
	foreign.Text = "other"
	if _, err := offline.ObserveTextTurn(context.Background(), path, foreign); err == nil {
		t.Fatal("foreign offline replay admitted")
	}
	if a, text, err := decodeTextTurn([]byte(valid), b, "task"); err != nil || text != "result" || a.ID != "msg_assistant" {
		t.Fatal("valid turn rejected", err)
	}
	for _, raw := range []string{
		"[" + user + "]", "[" + assistant + "," + user + "]", "[" + user + "," + assistant + "," + user + "]",
		strings.Replace(valid, `"finish":"stop"`, `"finish":"tool-calls"`, 1),
		strings.Replace(valid, `"text":"task"`, `"text":"other"`, 1),
		strings.Replace(valid, `"completed":20`, `"incomplete":20`, 1),
	} {
		if a, text, err := decodeTextTurn([]byte(raw), b, "task"); err == nil || text != "" || a != (Assistant{}) {
			t.Fatal("invalid turn returned output")
		}
	}
}
