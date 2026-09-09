package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionReconciliationReadOnly(t *testing.T) {
	id := strings.Repeat("a", 64)
	b := SessionBinding{IntentID: id, ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
	row := `{"id":"ses_fixture","projectID":"project","directory":"/candidate","agent":"build","title":"engorch:` + id + `","metadata":{"engorch_intent_id":"` + id + `"},"model":{"id":"model","providerID":"fixture"}}`
	for _, mode := range []string{"found", "absent", "duplicate", "changed", "missing-permission", "allow-permission", "extra-permission"} {
		reads := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reads++
			if r.URL.Query().Get("directory") != b.Directory {
				t.Error("reconciliation lost exact directory")
			}
			if r.Method != "GET" {
				t.Error("reconciliation mutated host")
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/session" {
				if r.URL.Query().Get("search") != "engorch:"+id || r.URL.Query().Get("limit") != "2" {
					t.Error("unbounded search")
				}
				body := "[" + row + "]"
				if mode == "absent" {
					body = "[]"
				}
				if mode == "duplicate" {
					body = "[" + row + "," + row + "]"
				}
				_, _ = w.Write([]byte(body))
				return
			}
			body := row
			permission := `[{"permission":"*","pattern":"*","action":"deny"}]`
			if mode == "allow-permission" {
				permission = `[{"permission":"*","pattern":"*","action":"allow"}]`
			}
			if mode == "extra-permission" {
				permission = `[{"permission":"*","pattern":"*","action":"deny"},{"permission":"bash","pattern":"*","action":"allow"}]`
			}
			if mode != "missing-permission" {
				body = strings.TrimSuffix(body, "}") + `,"permission":` + permission + `}`
			}
			if mode == "changed" {
				body = strings.Replace(body, "ses_fixture", "ses_other", 1)
			}
			_, _ = w.Write([]byte(body))
		}))
		c, err := NewClient(server.URL, "fixture", "secret")
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.ReconcileSession(context.Background(), b)
		c.Close()
		server.Close()
		if mode == "found" {
			if err != nil || got != "ses_fixture" || reads != 2 {
				t.Fatal(err)
			}
		} else if err == nil || got != "" {
			t.Fatal("uncertain creation admitted")
		}
	}
}
