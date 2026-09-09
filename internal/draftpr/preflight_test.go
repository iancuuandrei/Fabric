package draftpr

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBranchResponseAndEscapedRef(t *testing.T) {
	sha := strings.Repeat("a", 40)
	good := `{"ref":"refs/heads/feature/a#b%é","object":{"type":"commit","sha":"` + sha + `"}}`
	for _, raw := range []string{good, strings.Replace(good, `"commit"`, `"tag"`, 1), strings.Replace(good, sha, strings.Repeat("0", 40), 1), strings.Replace(good, sha, strings.Repeat("A", 40), 1), strings.Replace(good, `"sha"`, `"SHA"`, 1), strings.Replace(good, `"type":"commit"`, `"type":"commit","type":"tag"`, 1), `null`, `[]`, strings.Replace(good, "feature/a#b%é", "other", 1)} {
		c, err := NewClient("fixture")
		if err != nil {
			t.Fatal(err)
		}
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.EscapedPath() != "/repos/fixture/project/git/ref/heads/feature/a%23b%25%C3%A9" || req.URL.RawQuery != "" || req.URL.Fragment != "" {
				t.Fatal("ref escaped into another URL component")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(raw)), ContentLength: int64(len(raw)), Request: req}, nil
		})
		o, err := c.ReadBranch(context.Background(), "fixture/project", "refs/heads/feature/a#b%é")
		c.Close()
		if raw == good {
			if err != nil || o.Commit != sha {
				t.Fatalf("valid ref rejected: %v", err)
			}
		} else if err == nil || o != (BranchObservation{}) {
			t.Fatal("invalid ref admitted", raw)
		}
	}
}
