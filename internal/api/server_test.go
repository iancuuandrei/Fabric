package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/rewind"
	"github.com/iancuuandrei/fabric/pkg/fabric"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return root
}

func TestHTTPAPIServerEndpointsAndAuth(t *testing.T) {
	root := initTestRepo(t)
	client, err := fabric.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Init(t.Context(), fabric.InitOptions{APIDialect: "anthropic", Model: "claude-sonnet-4-5"}); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Seed one persisted episode + checkpoint to exercise Mission Control & episode routes.
	_ = os.WriteFile(filepath.Join(root, "app.go"), []byte("package app\nconst V = 1\n"), 0o600)
	_, _ = exec.Command("git", "-C", root, "add", "app.go").CombinedOutput()
	_, _ = exec.Command("git", "-C", root, "-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "add app").CombinedOutput()
	epID := "ep0123456789abcdef"
	wt := filepath.Join(root, ".harness", "worktrees", epID)
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "-b", "fabric/test-ep", wt, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	snap, err := rewind.Create(t.Context(), wt, epID, 0, "base")
	if err != nil {
		t.Fatalf("rewind.Create: %v", err)
	}
	ep := agentloop.Episode{
		RunID:        epID,
		Objective:    "Update constant",
		Model:        "claude-sonnet-4-5",
		Status:       "READY",
		WorkDir:      wt,
		Summary:      "Updated V",
		Checkpoints:  []rewind.Snapshot{snap},
		Usage:        cost.Usage{InputTokens: 500, OutputTokens: 100},
		CostEstimate: cost.Estimate{KnownPricing: true, EstimatedUSD: 0.003},
		StartedAt:    time.Now().UTC(),
		EndedAt:      time.Now().UTC(),
	}
	if err := agentloop.SaveEpisode(root, ep); err != nil {
		t.Fatalf("SaveEpisode: %v", err)
	}

	srv := NewServer(ServerConfig{Client: client, AuthToken: "secret-token", AllowCORS: true})

	// 1. Mission Control UI, Health & OpenAPI do not require auth.
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Mission Control") {
		t.Fatalf("GET / failed: status=%d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi status=%d", rec.Code)
	}
	var spec map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil || spec["openapi"] != "3.1.0" {
		t.Fatalf("invalid openapi spec: %v", err)
	}

	// 2. Protected routes reject missing token.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/episodes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	authReq := func(method, path string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer secret-token")
		r := httptest.NewRecorder()
		srv.ServeHTTP(r, req)
		return r
	}

	// 3. Protected classic & episode routes succeed with Bearer token.
	if r := authReq(http.MethodGet, "/v1/runs", ""); r.Code != http.StatusOK {
		t.Fatalf("GET /v1/runs status=%d body=%s", r.Code, r.Body.String())
	}
	if r := authReq(http.MethodGet, "/v1/episodes", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), epID) {
		t.Fatalf("GET /v1/episodes status=%d body=%s", r.Code, r.Body.String())
	}
	if r := authReq(http.MethodGet, "/v1/episodes/"+epID+"/packet?format=markdown", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Fabric Review Packet") {
		t.Fatalf("GET packet markdown status=%d body=%s", r.Code, r.Body.String())
	}
	if r := authReq(http.MethodPost, "/v1/episodes/"+epID+"/rewind", `{"step":0}`); r.Code != http.StatusOK {
		t.Fatalf("POST rewind status=%d body=%s", r.Code, r.Body.String())
	}
	if r := authReq(http.MethodGet, "/v1/episodes/"+epID+"/events", ""); r.Code != http.StatusOK || !strings.Contains(r.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("GET episode events SSE failed: status=%d ct=%s", r.Code, r.Header().Get("Content-Type"))
	}
	if r := authReq(http.MethodGet, "/v1/graph", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "app.go") {
		t.Fatalf("GET /v1/graph failed: status=%d body=%s", r.Code, r.Body.String())
	}
	if r := authReq(http.MethodGet, "/v1/graph/impact?target=app.go", ""); r.Code != http.StatusOK {
		t.Fatalf("GET /v1/graph/impact failed: status=%d body=%s", r.Code, r.Body.String())
	}
	// Steering a non-running episode returns 409 Conflict cleanly.
	if r := authReq(http.MethodPost, "/v1/episodes/"+epID+"/steer", `{"message":"focus on unit tests"}`); r.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when steering idle episode, got %d body=%s", r.Code, r.Body.String())
	}

	// 4. Integrations status & OpenAPI spec include external tracker/webhook routes.
	if r := authReq(http.MethodGet, "/v1/integrations", ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "jira") {
		t.Fatalf("GET /v1/integrations failed: status=%d body=%s", r.Code, r.Body.String())
	}
	specRec := httptest.NewRecorder()
	srv.ServeHTTP(specRec, httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil))
	if !strings.Contains(specRec.Body.String(), "/v1/webhooks/{provider}") || !strings.Contains(specRec.Body.String(), "/v1/tickets/run") {
		t.Fatalf("openapi spec missing integration routes")
	}

	// 5. Inbound webhooks fail closed when unconfigured (503).
	whRec := httptest.NewRecorder()
	srv.ServeHTTP(whRec, httptest.NewRequest(http.MethodPost, "/v1/webhooks/jira", bytes.NewBufferString(`{}`)))
	if whRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for unconfigured webhook, got %d body=%s", whRec.Code, whRec.Body.String())
	}

	// 6. Add [integrations.notify] + [integrations.webhooks] into fabric.toml and test notify + signed webhook filtering.
	var slackHits int
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slackHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer slackSrv.Close()
	t.Setenv("TEST_API_SLACK_HOOK", slackSrv.URL)
	t.Setenv("TEST_API_WEBHOOK_SEC", "wh-api-secret")

	cfgPath := filepath.Join(root, "fabric.toml")
	existingCfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	extraTOML := string(existingCfg) + `
[integrations.notify]
slack_webhook_env = "TEST_API_SLACK_HOOK"

[integrations.webhooks]
secret_env = "TEST_API_WEBHOOK_SEC"
trigger_label = "fabric"
`
	if err := os.WriteFile(cfgPath, []byte(extraTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Bad webhook secret -> 401.
	badReq := httptest.NewRequest(http.MethodPost, "/v1/webhooks/jira", bytes.NewBufferString(`{"webhookEvent":"jira:issue_updated"}`))
	badReq.Header.Set("X-Webhook-Secret", "wrong")
	badRec := httptest.NewRecorder()
	srv.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 on wrong webhook secret, got %d", badRec.Code)
	}

	// Valid secret without trigger_label -> 200 ignored cleanly.
	okReq := httptest.NewRequest(http.MethodPost, "/v1/webhooks/jira", bytes.NewBufferString(`{"webhookEvent":"jira:issue_updated","issue":{"key":"PAY-5","fields":{"summary":"Test","labels":["other"]}}}`))
	okReq.Header.Set("X-Webhook-Secret", "wh-api-secret")
	okRec := httptest.NewRecorder()
	srv.ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK || !strings.Contains(okRec.Body.String(), `"ignored"`) {
		t.Fatalf("expected 200 ignored webhook response, got %d body=%s", okRec.Code, okRec.Body.String())
	}

	// Notify Slack for episode -> 200 and Slack endpoint hit.
	if nRec := authReq(http.MethodPost, "/v1/episodes/"+epID+"/notify", `{"channel":"slack","ticket_key":"PAY-5"}`); nRec.Code != http.StatusOK || slackHits != 1 {
		t.Fatalf("POST notify failed: status=%d hits=%d body=%s", nRec.Code, slackHits, nRec.Body.String())
	}
}
