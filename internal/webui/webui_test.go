package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMissionControlHandlerServesCSPHTMLAndJS(t *testing.T) {
	h := Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("unexpected content-type: %s", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("missing strict CSP script-src 'self': %s", csp)
	}
	if strings.Contains(rec.Body.String(), "onclick=") {
		t.Fatal("index.html must not contain inline onclick handlers")
	}
	if !strings.Contains(rec.Body.String(), "Mission Control") {
		t.Fatalf("missing Mission Control markup")
	}

	jsRec := httptest.NewRecorder()
	h.ServeHTTP(jsRec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if jsRec.Code != http.StatusOK || !strings.Contains(jsRec.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("GET /app.js failed: status=%d ct=%s", jsRec.Code, jsRec.Header().Get("Content-Type"))
	}

	rec404 := httptest.NewRecorder()
	h.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown path, got %d", rec404.Code)
	}
}
