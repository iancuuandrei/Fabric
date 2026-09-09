package draftpr

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPClientIgnoresGlobalTransport(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, ambient := range []http.RoundTripper{
		&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("ambient transport invoked")
			return nil, errors.New("unexpected transport")
		}),
	} {
		http.DefaultTransport = ambient
		c, err := NewClient("fixture")
		if err != nil {
			t.Fatal(err)
		}
		transport, ok := c.http.Transport.(*http.Transport)
		if !ok || transport == ambient || transport.Proxy != nil ||
			(transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify) {
			t.Fatal("client inherited ambient transport policy")
		}
		c.Close()
	}
}

func TestHTTPReadBoundaries(t *testing.T) {
	const raw = `{"number":7,"html_url":"https://github.com/fixture/project/pull/7","state":"open","draft":true,"title":"test","body":"body","head":{"ref":"initial","sha":"abc","repo":{"full_name":"fixture/project"}},"base":{"ref":"main","sha":"def","repo":{"full_name":"fixture/project"}}}`
	for _, scenario := range []string{"success", "redirect", "unauthorized", "wrong-type", "oversized", "short", "transport-error", "cancelled", "different-number", "different-repository"} {
		t.Run(scenario, func(t *testing.T) {
			c, err := NewClient("fixture-secret")
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			calls := 0
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.String() != "https://api.github.com/repos/fixture/project/pulls/7" || req.Header.Get("Authorization") != "Bearer fixture-secret" || req.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
					t.Fatal("request binding mismatch")
				}
				if scenario == "transport-error" {
					return nil, errors.New("fixture-secret")
				}
				if scenario == "cancelled" {
					return nil, req.Context().Err()
				}
				res := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(raw)), ContentLength: int64(len(raw)), Request: req}
				switch scenario {
				case "redirect":
					res.StatusCode = 302
					res.Header.Set("Location", "https://other.example/secret")
				case "unauthorized":
					res.StatusCode = 401
				case "wrong-type":
					res.Header.Set("Content-Type", "text/html")
				case "oversized":
					res.ContentLength = -1
					res.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", canonical.MaxBytes+1)))
				case "short":
					res.ContentLength++
				case "different-number":
					other := strings.ReplaceAll(strings.Replace(raw, `"number":7`, `"number":8`, 1), "/pull/7", "/pull/8")
					res.Body = io.NopCloser(strings.NewReader(other))
					res.ContentLength = int64(len(other))
				case "different-repository":
					other := strings.ReplaceAll(raw, "fixture/project", "foreign/project")
					res.Body = io.NopCloser(strings.NewReader(other))
					res.ContentLength = int64(len(other))
				}
				return res, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "cancelled" {
				cancel()
			}
			o, err := c.Read(ctx, "fixture/project", 7)
			if scenario == "success" {
				if err != nil || o.Number != 7 {
					t.Fatalf("read failed: %v", err)
				}
			} else if err == nil || o != (Observation{}) {
				t.Fatal("invalid response admitted")
			}
			if err != nil && strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("credential leaked")
			}
			if calls != 1 {
				t.Fatalf("unexpected retry/redirect count: %d", calls)
			}
		})
	}
}

func TestHTTPClientRejectsInvalidCredentialsAndLocators(t *testing.T) {
	for _, token := range []string{"", " space", "line\n", "nonasciié", strings.Repeat("x", 4097)} {
		if _, err := NewClient(token); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	c, err := NewClient("fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("invalid locator dispatched"); return nil, nil })
	if _, err := c.Read(context.Background(), "../other/repo", 7); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if _, err := c.Read(context.Background(), "fixture/project", 0); err == nil {
		t.Fatal("invalid number accepted")
	}
}

func TestGoGitHubRateLimitErrorIsSanitizedWithoutRetry(t *testing.T) {
	c, err := NewClient("fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	calls := 0
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		raw := `{"message":"fixture-secret rate limited","documentation_url":"https://docs.github.com/rest"}`
		return &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{
			"Content-Type":          []string{"application/json"},
			"X-Ratelimit-Limit":     []string{"1"},
			"X-Ratelimit-Remaining": []string{"0"},
			"X-Ratelimit-Reset":     []string{"4102444800"},
		}, Body: io.NopCloser(strings.NewReader(raw)), ContentLength: int64(len(raw)), Request: req}, nil
	})
	if _, err := c.Read(context.Background(), "fixture/project", 7); err == nil || err.Error() != "GitHub primary rate limit reached" || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("typed primary rate-limit response was not safely classified", err)
	}
	if calls != 1 {
		t.Fatal("rate-limited request was retried", calls)
	}
}
