package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromptAcceptanceAndUncertainDispatch(t *testing.T) {
	for _, status := range []int{204, 200, 307, 500} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != "POST" || r.URL.Path != "/session/ses_fixture/prompt_async" {
				t.Error("wrong dispatch target")
			}
			var body struct {
				MessageID string
				Agent     string
				Variant   string
				Model     map[string]string
				Parts     []map[string]string
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.MessageID != "msg_fixture" || body.Agent != "build" || body.Variant != "low" || body.Model["modelID"] != "model" || body.Model["providerID"] != "fixture" || len(body.Parts) != 1 || body.Parts[0]["text"] != "task" {
				t.Error("prompt binding lost")
			}
			w.Header().Set("Location", "/redirect")
			w.WriteHeader(status)
		}))
		c, err := NewClient(server.URL, "fixture", "secret")
		if err != nil {
			t.Fatal(err)
		}
		b := Binding{SessionID: "ses_fixture", ParentID: "msg_fixture", Provider: "fixture", Model: "model", Agent: "build", Directory: "/candidate", Root: "/candidate", Variant: "low"}
		err = c.PromptOnce(context.Background(), b, "task")
		c.Close()
		server.Close()
		if (err == nil) != (status == 204) || calls != 1 {
			t.Fatalf("status %d: calls=%d err=%v", status, calls, err)
		}
	}
}
