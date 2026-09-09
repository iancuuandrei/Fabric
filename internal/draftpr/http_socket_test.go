package draftpr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
)

func TestTLSResponseBodyFailures(t *testing.T) {
	for _, scenario := range []string{"truncated-length", "truncated-chunk", "body-deadline"} {
		t.Run(scenario, func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				if scenario == "body-deadline" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				conn, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				wire := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 999\r\n\r\nfixture-secret"
				if scenario == "truncated-chunk" {
					wire = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n20\r\nfixture-secret"
				}
				_, _ = buffer.WriteString(wire)
				_ = buffer.Flush()
			}))
			defer server.Close()
			client, err := NewClient("fixture-secret")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			local, _ := url.Parse(server.URL)
			transport := server.Client().Transport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				copyReq := req.Clone(req.Context())
				copyReq.URL.Scheme, copyReq.URL.Host = local.Scheme, local.Host
				return transport.RoundTrip(copyReq)
			})
			// Exercise the client's whole-request timeout after response headers,
			// rather than relying on a context cancelled before dispatch.
			client.http.Timeout = 2 * time.Second
			raw, err := client.read(context.Background(), "/repos/fixture/project/pulls/7")
			if err == nil || len(raw) != 0 || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatalf("body failure admitted or exposed: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != 1 {
				t.Fatalf("body failure retried: %d requests", calls)
			}
		})
	}
}

func TestTLSLostCreationResponseAndReconciliation(t *testing.T) {
	plan := fixturePlan(t)
	payload, err := plan.Request()
	if err != nil {
		t.Fatal(err)
	}
	intent, _ := plan.Intent()
	id, _ := intent.ID()
	var mu sync.Mutex
	posts, branches, reads := 0, 0, 0
	created := false
	branch := func(ref, sha string) any {
		return map[string]any{"ref": ref, "sha": sha, "repo": map[string]any{"full_name": plan.Repository}}
	}
	full := map[string]any{"number": 7, "html_url": "https://github.com/fixture/project/pull/7", "state": "open", "draft": true, "title": payload.Title, "body": payload.Body, "head": branch(payload.Head, plan.Push.Candidate.Head), "base": branch(payload.Base, plan.BaseCommit)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("missing explicit authentication/version")
			http.Error(w, "bad request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/fixture/project/git/ref/") && r.Method == "GET":
			branches++
			ref := strings.TrimPrefix(r.URL.Path, "/repos/fixture/project/git/ref/")
			sha := plan.Push.Candidate.Head
			if ref == "heads/main" {
				sha = plan.BaseCommit
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/" + ref, "object": map[string]any{"type": "commit", "sha": sha}})
		case r.URL.Path == "/repos/fixture/project/pulls" && r.Method == "POST":
			posts++
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			var sent CreateRequest
			if err := canonical.Decode(raw, &sent); err != nil || sent != payload || branches != 2 {
				t.Error("POST payload or preflight mismatch")
				return
			}
			created = true // Host applied the request before the response is lost.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		case r.URL.Path == "/repos/fixture/project/pulls" && r.Method == "GET":
			rows := []any{}
			if created && r.URL.Query().Get("page") == "1" {
				rows = append(rows, full)
			}
			_ = json.NewEncoder(w).Encode(rows)
		case r.URL.Path == "/repos/fixture/project/pulls/7" && r.Method == "GET":
			reads++
			_ = json.NewEncoder(w).Encode(full)
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 400)
		}
	}))
	defer server.Close()
	client, err := NewClient("fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	localURL, _ := url.Parse(server.URL)
	transport := server.Client().Transport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	// Only the test transport rewrites the fixed API destination to the local
	// TLS fixture. Production has no configurable alternate host or TLS bypass.
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		copyReq := req.Clone(req.Context())
		copyURL := *req.URL
		if copyURL.Host != "api.github.com" {
			t.Fatal("production endpoint changed")
		}
		copyURL.Scheme, copyURL.Host = localURL.Scheme, localURL.Host
		copyReq.URL = &copyURL
		return transport.RoundTrip(copyReq)
	})
	o, err := client.Create(context.Background(), plan, intent, effects.Authorization{IntentID: id, Actor: "fixture"})
	if err == nil || o != (Observation{}) {
		t.Fatal("lost socket response confirmed creation")
	}
	o, err = client.Reconcile(context.Background(), plan)
	if err != nil || o.Number != 7 {
		t.Fatal("socket reconciliation failed", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || reads != 1 || !created {
		t.Fatalf("unexpected effects: posts=%d reads=%d created=%v", posts, reads, created)
	}
}
