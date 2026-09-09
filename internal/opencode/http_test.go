package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadbackHTTPProjection(t *testing.T) {
	b := Binding{SessionID: "ses_fixture", ParentID: "msg_user", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate"}
	const valid = `{"info":{"id":"msg_assistant","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","time":{"created":10,"completed":20},"path":{"cwd":"/candidate","root":"/candidate"},"cost":0.0123,"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}},"parts":[{"id":"p1","sessionID":"ses_fixture","messageID":"msg_assistant","type":"text","text":"verified output"}]}`
	for _, scenario := range []string{"success", "substituted-message", "truncated", "wrong-type", "oversized", "duplicate-info"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/session/ses_fixture/message/msg_assistant" {
					t.Error("readback path mismatch")
				}
				body := valid
				if scenario == "substituted-message" {
					body = strings.ReplaceAll(body, "msg_assistant", "msg_other")
				}
				if scenario == "duplicate-info" {
					body = strings.Replace(body, `"id":"msg_assistant"`, `"id":"msg_assistant","id":"msg_other"`, 1)
				}
				if scenario == "truncated" {
					conn, buf, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body)+10, body)
					_ = buf.Flush()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario == "wrong-type" {
					w.Header().Set("Content-Type", "text/plain")
				}
				if scenario == "oversized" {
					body = strings.Repeat(" ", 1<<20) + body
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "fixture", "secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			a, text, err := client.ReadMessage(context.Background(), b, "msg_assistant")
			if scenario == "success" {
				if err != nil || text != "verified output" || a.ID != "msg_assistant" || a.InputTokens != 10 {
					t.Fatal("valid readback failed", err)
				}
			} else if err == nil || text != "" || a != (Assistant{}) {
				t.Fatal("invalid readback admitted")
			}
			if calls != 1 {
				t.Fatal("unexpected read retry")
			}
		})
	}
}

func TestReadbackHTTPFailureDoesNotRedirect(t *testing.T) {
	for _, status := range []int{302, 401, 500} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			user, password, ok := r.BasicAuth()
			if !ok || user != "fixture" || password != "secret" || r.URL.Path != "/session/ses/message/msg" {
				t.Error("request mismatch")
			}
			w.Header().Set("Location", "http://127.0.0.1:1/leak")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("secret"))
		}))
		c, err := NewClient(server.URL, "fixture", "secret")
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = c.ReadMessage(context.Background(), Binding{SessionID: "ses"}, "msg")
		c.Close()
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret") || calls != 1 {
			t.Fatal("unsafe error/retry behavior", err)
		}
	}
}

func TestLocalEndpointRestrictions(t *testing.T) {
	for _, endpoint := range []string{"http://localhost:1234", "http://example.com:1234", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?x=1", "http://user@127.0.0.1:1234", "https://127.0.0.1:1234"} {
		if c, err := NewClient(endpoint, "fixture", "secret"); err == nil {
			c.Close()
			t.Fatal("unqualified endpoint accepted")
		}
	}
}
