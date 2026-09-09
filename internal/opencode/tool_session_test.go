package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/journal"
)

func toolSessionFixture() ToolSessionBinding {
	return ToolSessionBinding{
		Session: SessionBinding{
			IntentID: strings.Repeat("a", 64), ProjectID: "project", Directory: "/candidate",
			Agent: "build", Provider: "fixture", Model: "model",
		},
		ToolNames:     []string{"ri_search", "source_read"},
		CatalogSHA256: strings.Repeat("b", 64),
	}
}

func toolSessionJSON(binding ToolSessionBinding, permissions string) string {
	return `{"id":"ses_fixture","projectID":"project","directory":"/candidate","agent":"build","title":"engorch:` + binding.Session.IntentID + `","metadata":{"engorch_intent_id":"` + binding.Session.IntentID + `","engorch_catalog_sha256":"` + binding.CatalogSHA256 + `"},"model":{"id":"model","providerID":"fixture"},"permission":` + permissions + `}`
}

func expectedToolPermissions() string {
	return `[{"permission":"*","pattern":"*","action":"deny"},{"permission":"engorch_ri_search","pattern":"*","action":"allow"},{"permission":"engorch_source_read","pattern":"*","action":"allow"}]`
}

func TestToolSessionRejectsInvalidStructuredPermissionBeforeTransport(t *testing.T) {
	binding := toolSessionFixture()
	binding.StructuredOutput = &StructuredOutputExpectation{}
	if _, err := binding.permissions(); err == nil {
		t.Fatal("invalid structured output expectation produced a session permission")
	}
}

func TestCreateToolSessionJournalsBeforeOnePostAndReadsBackExactRules(t *testing.T) {
	binding := toolSessionFixture()
	intended := snapshotToolSessionBinding(binding)
	journalPath := filepath.Join(t.TempDir(), "session.jsonl")
	posts, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			posts++
			events, err := journal.Read(journalPath)
			if err != nil || len(events) != 1 || events[0].Kind != "opencode.tool-session-intent" {
				t.Error("POST preceded durable exact intent", err)
			}
			var request map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if string(request["permission"]) != expectedToolPermissions() {
				t.Errorf("permission precedence differs: %s", request["permission"])
			}
			metadata, err := wireObject(request["metadata"])
			if err != nil || !toolsExactKeys(metadata, "engorch_intent_id", "engorch_catalog_sha256") {
				t.Error("catalog metadata missing", err)
			}
			// Mutate the caller-owned backing array during the network callback.
			// Later readback and observation must use the entry snapshot.
			binding.ToolNames[0] = "mutated"
		} else if r.Method == http.MethodGet && r.URL.Path == "/session/ses_fixture" {
			reads++
		} else {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(toolSessionJSON(binding, expectedToolPermissions())))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "session-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id, err := client.CreateToolSession(context.Background(), journalPath, binding)
	if err != nil || id != "ses_fixture" || posts != 1 || reads != 1 {
		t.Fatal("tool session creation failed", id, err, posts, reads)
	}
	events, err := journal.Read(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := replayToolSessionCreation(events)
	if err != nil || state.Binding == nil || !equalToolSessionBinding(*state.Binding, intended) || state.ID != id {
		t.Fatal("durable tool session binding differs", err)
	}
	if binding.ToolNames[0] != "mutated" {
		t.Fatal("mutation fixture did not execute")
	}
	raw, err := journal.ExportJSONL(journalPath)
	if err != nil || bytes.Contains(raw, []byte("session-secret")) || bytes.Contains(raw, []byte("Bearer")) {
		t.Fatal("credential entered tool session journal", err)
	}
	if _, err := client.CreateToolSession(context.Background(), journalPath, binding); err == nil || posts != 1 {
		t.Fatal("duplicate creation repeated POST")
	}
}

func TestRecoverToolSessionIsReadOnlyAndBindsAllowlistCatalog(t *testing.T) {
	binding := toolSessionFixture()
	journalPath := filepath.Join(t.TempDir(), "session.jsonl")
	mode := "uncertain"
	posts, reads := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reads++
		w.Header().Set("Content-Type", "application/json")
		if mode != "recover" {
			t.Error("unexpected recovery read")
		}
		row := toolSessionJSON(binding, expectedToolPermissions())
		if r.URL.Path == "/session" {
			_, _ = w.Write([]byte("[" + row + "]"))
			return
		}
		_, _ = w.Write([]byte(row))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if id, err := client.CreateToolSession(context.Background(), journalPath, binding); err == nil || id != "" || posts != 1 {
		t.Fatal("uncertain POST admitted or repeated", id, err, posts)
	}
	changed := binding
	changed.CatalogSHA256 = strings.Repeat("c", 64)
	if id, err := client.RecoverToolSession(context.Background(), journalPath, changed); err == nil || id != "" || reads != 0 {
		t.Fatal("changed catalog reached recovery host", id, err, reads)
	}
	changed = binding
	changed.ToolNames = []string{"source_read"}
	if id, err := client.RecoverToolSession(context.Background(), journalPath, changed); err == nil || id != "" || reads != 0 {
		t.Fatal("changed allowlist reached recovery host", id, err, reads)
	}
	mode = "recover"
	id, err := client.RecoverToolSession(context.Background(), journalPath, binding)
	if err != nil || id != "ses_fixture" || posts != 1 || reads != 2 {
		t.Fatal("GET-only recovery failed", id, err, posts, reads)
	}
	reads = 0
	id, err = client.RecoverToolSession(context.Background(), journalPath, binding)
	if err != nil || id != "ses_fixture" || reads != 0 || posts != 1 {
		t.Fatal("cached observation contacted host", id, err, posts, reads)
	}
}

func TestToolSessionRejectsInvalidBindingsAndReadbackSubstitution(t *testing.T) {
	valid := toolSessionFixture()
	for name, mutate := range map[string]func(*ToolSessionBinding){
		"unsorted":  func(b *ToolSessionBinding) { b.ToolNames = []string{"source_read", "ri_search"} },
		"duplicate": func(b *ToolSessionBinding) { b.ToolNames = []string{"ri_search", "ri_search"} },
		"unsafe":    func(b *ToolSessionBinding) { b.ToolNames = []string{"ri/search"} },
		"empty":     func(b *ToolSessionBinding) { b.ToolNames = nil },
		"catalog":   func(b *ToolSessionBinding) { b.CatalogSHA256 = strings.Repeat("B", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			binding := valid
			mutate(&binding)
			if err := binding.Validate(); err == nil {
				t.Fatal("invalid tool session binding admitted")
			}
		})
	}
	for name, mutate := range map[string]func(string) string{
		"deny-last": func(row string) string {
			return strings.Replace(row, expectedToolPermissions(), `[{"permission":"engorch_ri_search","pattern":"*","action":"allow"},{"permission":"engorch_source_read","pattern":"*","action":"allow"},{"permission":"*","pattern":"*","action":"deny"}]`, 1)
		},
		"rule-extra": func(row string) string {
			return strings.Replace(row, `"action":"allow"`, `"action":"allow","extra":true`, 1)
		},
		"wrong-tool": func(row string) string {
			return strings.Replace(row, "engorch_ri_search", "engorch_other", 1)
		},
		"wrong-pattern": func(row string) string {
			return strings.Replace(row, `"pattern":"*","action":"allow"`, `"pattern":"argument","action":"allow"`, 1)
		},
		"metadata-extra": func(row string) string {
			return strings.Replace(row, `"engorch_catalog_sha256":"`+valid.CatalogSHA256+`"`, `"engorch_catalog_sha256":"`+valid.CatalogSHA256+`","extra":"x"`, 1)
		},
		"wrong-catalog": func(row string) string {
			return strings.Replace(row, valid.CatalogSHA256, strings.Repeat("c", 64), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			row := mutate(toolSessionJSON(valid, expectedToolPermissions()))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/session" {
					_, _ = w.Write([]byte("[" + toolSessionJSON(valid, expectedToolPermissions()) + "]"))
					return
				}
				_, _ = w.Write([]byte(row))
			}))
			client, err := NewClient(server.URL, "fixture", "secret")
			if err != nil {
				t.Fatal(err)
			}
			id, err := client.ReconcileToolSession(context.Background(), valid)
			client.Close()
			server.Close()
			if err == nil || id != "" {
				t.Fatal("substituted tool session readback admitted")
			}
		})
	}
}
