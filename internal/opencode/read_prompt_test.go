package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptReadbackRejectsSubstitution(t *testing.T) {
	raw := `{"info":{"id":"msg_fixture","sessionID":"ses_fixture","role":"user","agent":"build","model":{"providerID":"fixture","modelID":"model","variant":"low"}},"parts":[{"id":"prt_fixture","sessionID":"ses_fixture","messageID":"msg_fixture","type":"text","text":"task"}]}`
	cases := []struct{ name, old, replacement string }{
		{name: "exact"},
		{"text", `"text":"task"`, `"text":"other"`},
		{"model", `"modelID":"model"`, `"modelID":"other"`},
		{"variant", `"variant":"low"`, `"variant":"high"`},
		{"session", `"sessionID":"ses_fixture"`, `"sessionID":"ses_other"`},
		{"synthetic", `"type":"text"`, `"type":"text","synthetic":true`},
		{"context", `"role":"user"`, `"role":"user","system":"other"`},
		{"duplicate", `"text":"task"`, `"text":"task","text":"task"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := raw
			if tc.old != "" {
				body = strings.Replace(body, tc.old, tc.replacement, 1)
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/session/ses_fixture/message/msg_fixture" {
					t.Error("unexpected recovery request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			c, err := NewClient(server.URL, "fixture", "secret")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			b := Binding{SessionID: "ses_fixture", ParentID: "msg_fixture", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate", Variant: "low"}
			err = c.ReadPrompt(context.Background(), b, "task")
			if (err == nil) != (tc.name == "exact") || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}
