package opencode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestMCPStatusRejectsUnadmittedServers(t *testing.T) {
	for _, raw := range []string{
		`{}`, `null`, `{"other":{"status":"connected"}}`,
		`{"engorch":{"status":"disabled"}}`,
		`{"engorch":{"status":"connected","error":"unexpected"}}`,
		`{"engorch":{"status":"connected"},"other":{"status":"connected"}}`,
		`{"engorch":{"status":"connected","status":"connected"}}`,
	} {
		if result, err := decodeMCPStatus([]byte(raw)); err == nil || result != (MCPStatus{}) {
			t.Fatalf("admitted invalid status %s", raw)
		}
	}
}

func TestMCPStatusReadback(t *testing.T) {
	directory := filepath.Clean(t.TempDir())
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, password, ok := r.BasicAuth()
		if r.Method != "GET" || r.URL.Path != "/mcp" || r.URL.Query().Get("directory") != directory || len(r.URL.Query()) != 1 || !ok || user != "fixture" || password != "secret" {
			t.Error("unexpected status request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"engorch":{"status":"connected"}}`))
	}))
	defer s.Close()
	c, err := NewClient(s.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	result, err := c.ReadMCPStatusInDirectory(context.Background(), directory)
	if err != nil || len(result.SHA256) != 64 || calls != 1 {
		t.Fatal("status readback failed", err)
	}
}

func TestMCPStatusDirectoryRejectsInvalidBeforeHTTP(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid directory reached HTTP")
	}))
	defer s.Close()
	c, err := NewClient(s.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if result, err := c.ReadMCPStatusInDirectory(context.Background(), "relative"); err == nil || result != (MCPStatus{}) {
		t.Fatal("invalid MCP directory admitted")
	}
}
