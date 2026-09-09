package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidSessionContextNeverContactsHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid session context reached host")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, field := range []string{"project", "directory", "agent", "provider", "model", "variant", "marker"} {
		b := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
		switch field {
		case "project":
			b.ProjectID = " "
		case "directory":
			b.Directory = ""
		case "agent":
			b.Agent = string([]byte{0xff})
		case "provider":
			b.Provider = ""
		case "model":
			b.Model = strings.Repeat("m", 4097)
		case "variant":
			b.Variant = string([]byte{0xff})
		case "marker":
			b.IntentID = "invalid"
		}
		if id, err := c.CreateSessionOnce(context.Background(), b); err == nil || id != "" {
			t.Fatalf("create accepted invalid %s", field)
		}
		if id, err := c.ReconcileSession(context.Background(), b); err == nil || id != "" {
			t.Fatalf("reconcile accepted invalid %s", field)
		}
	}
}

func TestCreateSessionBindsDirectoryOnWriteAndReadback(t *testing.T) {
	b := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: `C:\candidate space\a&b+é`, Agent: "build", Provider: "fixture", Model: "model"}
	row, err := json.Marshal(map[string]any{"id": "ses_fixture", "projectID": b.ProjectID, "directory": b.Directory, "agent": b.Agent, "title": "engorch:" + b.IntentID, "metadata": map[string]string{"engorch_intent_id": b.IntentID}, "model": map[string]string{"id": b.Model, "providerID": b.Provider}, "permission": denySessionPermissions})
	if err != nil {
		t.Fatal(err)
	}
	methods := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Query().Get("directory") != b.Directory || len(r.URL.Query()) != 1 {
			t.Error("session route lost exact directory")
		}
		if r.URL.Path != "/session" && r.URL.Path != "/session/ses_fixture" {
			t.Error("unexpected session route")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(row)
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, err := c.CreateSessionOnce(context.Background(), b)
	if err != nil || id != "ses_fixture" || len(methods) != 2 || methods[0] != "POST" || methods[1] != "GET" {
		t.Fatal(id, methods, err)
	}
}

func TestCreateSessionDoesNotRetryUncertainResponse(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/session" {
					t.Error("unexpected request")
				}
				w.Header().Set("Location", "/session")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, "fixture", "secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			b := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
			if id, err := client.CreateSessionOnce(context.Background(), b); err == nil || id != "" {
				t.Fatal("uncertain creation accepted")
			}
			if calls != 1 {
				t.Fatalf("creation attempted %d times", calls)
			}
		})
	}
}

func TestCreateSessionDoesNotRetryMismatchedReadback(t *testing.T) {
	b := SessionBinding{IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: "/candidate", Agent: "build", Provider: "fixture", Model: "model"}
	posts, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directory := b.Directory
		if r.Method == http.MethodPost {
			posts++
		} else if r.Method == http.MethodGet {
			reads++
			directory = "/different-candidate"
		} else {
			t.Error("unexpected method")
		}
		if r.URL.Query().Get("directory") != b.Directory {
			t.Error("unbound directory selector")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_fixture", "projectID": b.ProjectID, "directory": directory, "agent": b.Agent, "title": "engorch:" + b.IntentID, "metadata": map[string]string{"engorch_intent_id": b.IntentID}, "model": map[string]string{"id": b.Model, "providerID": b.Provider}, "permission": denySessionPermissions})
	}))
	defer server.Close()
	c, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if id, err := c.CreateSessionOnce(context.Background(), b); err == nil || id != "" {
		t.Fatal("substituted readback accepted", id, err)
	}
	if posts != 1 || reads != 1 {
		t.Fatal("uncertain creation retried", posts, reads)
	}
}
