package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfigValidationAndSecretsHygiene(t *testing.T) {
	good := &Config{
		Jira:     &JiraConfig{BaseURL: "https://acme.atlassian.net", EmailEnv: "JIRA_EMAIL", TokenEnv: "JIRA_TOKEN", APIVersion: "3"},
		Linear:   &LinearConfig{TokenEnv: "LINEAR_KEY"},
		GitLab:   &GitLabConfig{Project: "acme/api", TokenEnv: "GITLAB_TOKEN"},
		Notify:   &NotifyConfig{SlackWebhookEnv: "SLACK_HOOK", WebhookURL: "https://hooks.example.com/fabric", WebhookSecretEnv: "HOOK_SEC"},
		Webhooks: &WebhookConfig{SecretEnv: "INBOUND_SEC", DefaultMaxUSD: 2.0, DefaultMaxTurns: 20},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}

	for _, tc := range []struct {
		name string
		cfg  *Config
	}{
		{"raw URL in slack_webhook_env", &Config{Notify: &NotifyConfig{SlackWebhookEnv: "https://hooks.slack.com/services/a/b/c"}}},
		{"raw token with colon in token_env", &Config{Jira: &JiraConfig{BaseURL: "https://acme.atlassian.net", TokenEnv: "user:secret"}}},
		{"non-loopback http base_url", &Config{Jira: &JiraConfig{BaseURL: "http://jira.example.com"}}},
		{"userinfo in url", &Config{Jira: &JiraConfig{BaseURL: "https://user:pass@acme.atlassian.net"}}},
		{"gitlab missing project", &Config{GitLab: &GitLabConfig{}}},
	} {
		if err := tc.cfg.Validate(); err == nil {
			t.Fatalf("case %q should have failed validation", tc.name)
		}
	}
}

func TestParseReferenceAndBranchSlug(t *testing.T) {
	cases := []struct {
		raw      string
		wantProv string
		wantKey  string
		wantProj string
	}{
		{"https://acme.atlassian.net/browse/PAY-123", "jira", "PAY-123", ""},
		{"https://linear.app/acme/issue/ENG-42/fix-race", "linear", "ENG-42", ""},
		{"https://gitlab.com/org/team/app/-/issues/99", "gitlab", "99", "org/team/app"},
		{"https://github.com/org/repo/issues/17", "github", "17", ""},
		{"#55", "github", "55", ""},
		{"CORE-7", "", "CORE-7", ""},
	}
	for _, tc := range cases {
		p, k, proj := ParseReference(tc.raw)
		if p != tc.wantProv || k != tc.wantKey || proj != tc.wantProj {
			t.Fatalf("ParseReference(%q) = (%q,%q,%q), want (%q,%q,%q)", tc.raw, p, k, proj, tc.wantProv, tc.wantKey, tc.wantProj)
		}
	}
	if _, err := ValidateTicketRef("../etc/passwd"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if slug := BranchSlug(Ticket{Provider: "Jira", Key: "PAY-123"}); slug != "fabric/jira-pay-123" {
		t.Fatalf("unexpected slug %q", slug)
	}
}

func TestJiraCloudAndServerTrackers(t *testing.T) {
	t.Setenv("TEST_JIRA_EMAIL", "eng@acme.test")
	t.Setenv("TEST_JIRA_TOKEN", "jira-secret")

	var gotCommentBody map[string]any
	var gotTransition map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("eng@acme.test:jira-secret"))
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/PAY-10":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"key": "PAY-10",
				"fields": map[string]any{
					"summary": "Fix idempotency key check",
					"labels":  []string{"fabric", "backend"},
					"status":  map[string]any{"name": "To Do"},
					"description": map[string]any{
						"type": "doc",
						"content": []any{
							map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Line one"}}},
							map[string]any{"type": "paragraph", "content": []any{map[string]any{"type": "text", "text": "Line two"}}},
						},
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/PAY-10/comment":
			_ = json.NewDecoder(r.Body).Decode(&gotCommentBody)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/api/3/issue/PAY-10/transitions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"transitions": []map[string]any{
					{"id": "31", "name": "Start Review", "to": map[string]string{"name": "In Review"}},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/rest/api/3/issue/PAY-10/transitions":
			_ = json.NewDecoder(r.Body).Decode(&gotTransition)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tr, err := NewJiraTracker(JiraConfig{BaseURL: srv.URL, EmailEnv: "TEST_JIRA_EMAIL", TokenEnv: "TEST_JIRA_TOKEN", APIVersion: "3"}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	tk, err := tr.FetchTicket(context.Background(), "PAY-10")
	if err != nil || tk.Title != "Fix idempotency key check" || !strings.Contains(tk.Description, "Line one\n\nLine two") {
		t.Fatalf("unexpected Jira Cloud ticket: %+v err=%v", tk, err)
	}
	if err := tr.PostComment(context.Background(), "PAY-10", "Packet ready"); err != nil {
		t.Fatalf("PostComment failed: %v", err)
	}
	if bodyMap, ok := gotCommentBody["body"].(map[string]any); !ok || bodyMap["type"] != "doc" {
		t.Fatalf("expected ADF comment body, got %#v", gotCommentBody)
	}
	if err := tr.Transition(context.Background(), "PAY-10", "In Review"); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	if m, _ := gotTransition["transition"].(map[string]any); m["id"] != "31" {
		t.Fatalf("expected transition id 31, got %#v", gotTransition)
	}

	// Jira Server / Data Center v2 PAT Bearer auth
	var serverV2Body string
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pat-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/rest/api/2/issue/OPS-3/comment" {
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			serverV2Body = m["body"]
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv2.Close()
	t.Setenv("TEST_JIRA_PAT", "pat-token")
	tr2, err := NewJiraTracker(JiraConfig{BaseURL: srv2.URL, TokenEnv: "TEST_JIRA_PAT", APIVersion: "2"}, srv2.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr2.PostComment(context.Background(), "OPS-3", "Server v2 check"); err != nil || serverV2Body != "Server v2 check" {
		t.Fatalf("Jira v2 failed: %v body=%q", err, serverV2Body)
	}
}

func TestLinearAndGitLabTrackers(t *testing.T) {
	t.Setenv("TEST_LINEAR_KEY", "lin_api_test")
	t.Setenv("TEST_GITLAB_TOKEN", "glpat-test")

	linSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "lin_api_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		q := string(raw)
		switch {
		case strings.Contains(q, "issue(id:"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"issue": map[string]any{
						"id": "uuid-1", "identifier": "ENG-9", "title": "Fix cache race", "description": "Details",
						"url": "https://linear.app/acme/issue/ENG-9", "state": map[string]string{"name": "Todo"},
						"team": map[string]string{"id": "team-1"},
					},
				},
			})
		case strings.Contains(q, "commentCreate"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]bool{"success": true}}})
		case strings.Contains(q, "states"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"team": map[string]any{"states": map[string]any{"nodes": []map[string]string{{"id": "st-2", "name": "Done"}}}}},
			})
		case strings.Contains(q, "issueUpdate"):
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueUpdate": map[string]bool{"success": true}}})
		}
	}))
	defer linSrv.Close()

	lt, err := NewLinearTracker(LinearConfig{BaseURL: linSrv.URL, TokenEnv: "TEST_LINEAR_KEY"}, linSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	tk, err := lt.FetchTicket(context.Background(), "ENG-9")
	if err != nil || tk.Title != "Fix cache race" {
		t.Fatalf("Linear FetchTicket failed: %+v %v", tk, err)
	}
	if err := lt.PostComment(context.Background(), "ENG-9", "Verified"); err != nil {
		t.Fatalf("Linear PostComment failed: %v", err)
	}
	if err := lt.Transition(context.Background(), "ENG-9", "Done"); err != nil {
		t.Fatalf("Linear Transition failed: %v", err)
	}

	var glPutState string
	glSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "glpat-test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.RequestURI, "/projects/acme%2Fapi/issues/7"):
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 7, "title": "Timeout leak", "state": "opened"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/notes"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut:
			var m map[string]string
			_ = json.NewDecoder(r.Body).Decode(&m)
			glPutState = m["state_event"]
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer glSrv.Close()

	gt, err := NewGitLabTracker(GitLabConfig{BaseURL: glSrv.URL, Project: "acme/api", TokenEnv: "TEST_GITLAB_TOKEN"}, glSrv.Client())
	if err != nil {
		t.Fatal(err)
	}
	gtk, err := gt.FetchTicket(context.Background(), "7")
	if err != nil || gtk.Key != "7" || gtk.Title != "Timeout leak" {
		t.Fatalf("GitLab FetchTicket failed: %+v %v", gtk, err)
	}
	if err := gt.PostComment(context.Background(), "7", "Looks good"); err != nil {
		t.Fatalf("GitLab PostComment failed: %v", err)
	}
	if err := gt.Transition(context.Background(), "7", "closed"); err != nil || glPutState != "close" {
		t.Fatalf("GitLab Transition failed: %v state=%q", err, glPutState)
	}
}

func TestNotifyAllAndHMACSignedWebhook(t *testing.T) {
	seen := map[string]int{}
	var gotSig string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen[r.URL.Path]++
		if r.URL.Path == "/webhook" {
			gotSig = r.Header.Get("X-Fabric-Signature-256")
			gotBody = b
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("TEST_SLACK_URL", srv.URL+"/slack")
	t.Setenv("TEST_DISCORD_URL", srv.URL+"/discord")
	t.Setenv("TEST_TEAMS_URL", srv.URL+"/teams")
	t.Setenv("TEST_HOOK_SECRET", "super-secret-hmac")

	cfg := &NotifyConfig{
		SlackWebhookEnv:   "TEST_SLACK_URL",
		DiscordWebhookEnv: "TEST_DISCORD_URL",
		TeamsWebhookEnv:   "TEST_TEAMS_URL",
		WebhookURL:        srv.URL + "/webhook",
		WebhookSecretEnv:  "TEST_HOOK_SECRET",
		Events:            []string{"READY"},
	}
	if !ShouldNotify(cfg, "READY") || ShouldNotify(cfg, "FAILED") {
		t.Fatal("ShouldNotify did not honor Events filter")
	}
	if EventNameForStatus("READY") != "episode.ready" || EventNameForStatus("") != "episode.completed" {
		t.Fatal("unexpected EventNameForStatus mapping")
	}
	n := Notification{
		Status:       "READY",
		RunID:        "abcdef123456",
		Objective:    "Fix deadlock",
		Summary:      "Added mutex ordering",
		TicketKey:    "PAY-1",
		TicketURL:    "https://acme.atlassian.net/browse/PAY-1",
		PRURL:        "https://github.com/org/repo/pull/5",
		ChangedFiles: []string{"lock.go"},
		CostUSD:      0.1234,
		MutationKill: "4/4 (100%)",
	}
	res := NotifyAll(context.Background(), cfg, n, "all", srv.Client())
	if len(res) != 4 {
		t.Fatalf("expected 4 delivery results, got %d: %+v", len(res), res)
	}
	for _, r := range res {
		if !r.Sent || r.Error != "" {
			t.Fatalf("channel %s failed: %+v", r.Channel, r)
		}
	}
	if !VerifyHMACSHA256("super-secret-hmac", gotBody, gotSig) {
		t.Fatalf("outbound webhook HMAC verification failed: sig=%q", gotSig)
	}
}

func TestParseInboundWebhookFailClosedAndVerification(t *testing.T) {
	cfg := &WebhookConfig{SecretEnv: "TEST_INBOUND_WH_SECRET", TriggerLabel: "fabric"}
	if _, err := ParseInboundWebhook("jira", http.Header{}, []byte(`{}`), cfg); err != ErrWebhookNotConfigured {
		t.Fatalf("expected ErrWebhookNotConfigured when env missing, got %v", err)
	}

	t.Setenv("TEST_INBOUND_WH_SECRET", "wh-secret-42")
	badHdr := http.Header{"X-Webhook-Secret": {"wrong"}}
	if _, err := ParseInboundWebhook("jira", badHdr, []byte(`{}`), cfg); err != ErrWebhookUnauthorized {
		t.Fatalf("expected ErrWebhookUnauthorized, got %v", err)
	}

	jiraBody := []byte(`{"webhookEvent":"jira:issue_updated","issue":{"key":"PAY-88","fields":{"summary":"Fix crash","labels":["backend"]}}}`)
	hdr := http.Header{"X-Webhook-Secret": {"wh-secret-42"}}
	tr, err := ParseInboundWebhook("jira", hdr, jiraBody, cfg)
	if err != nil || !tr.Ignored {
		t.Fatalf("expected ignored trigger without matching label, got %+v err=%v", tr, err)
	}

	jiraReady := []byte(`{"webhookEvent":"jira:issue_updated","issue":{"key":"PAY-88","fields":{"summary":"Fix crash","labels":["fabric"]}}}`)
	tr2, err := ParseInboundWebhook("jira", hdr, jiraReady, cfg)
	if err != nil || tr2.Ignored || tr2.TicketKey != "PAY-88" {
		t.Fatalf("expected active Jira trigger, got %+v err=%v", tr2, err)
	}

	// Verify GitHub & Linear HMAC headers too.
	linBody := []byte(`{"action":"create","type":"Issue","data":{"identifier":"ENG-1","title":"Add tests","labels":[{"name":"fabric"}]}}`)
	linHdr := http.Header{"Linear-Signature": {hmacHex("wh-secret-42", linBody)}}
	trLin, err := ParseInboundWebhook("linear", linHdr, linBody, cfg)
	if err != nil || trLin.Ignored || trLin.TicketKey != "ENG-1" {
		t.Fatalf("expected active Linear HMAC trigger, got %+v err=%v", trLin, err)
	}
}

func hmacHex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
