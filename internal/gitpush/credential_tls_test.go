package gitpush

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActualGitCredentialOverLocalTLS(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "advertisement"
		if reject {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			credential, err := NewCredential("https://github.com/fixture/project.git", "fixture-secret")
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != strings.TrimPrefix(credential.header, "Authorization: ") {
					t.Error("Git omitted scoped authentication")
					http.Error(w, "missing credential", 403)
					return
				}
				if reject {
					http.Error(w, "fixture-secret "+credential.header, 403)
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				switch r.URL.Path {
				case "/fixture/project.git/info/refs":
					_, _ = w.Write([]byte(strings.Repeat("a", 40) + "\trefs/heads/main\n"))
				case "/fixture/project.git/HEAD":
					_, _ = w.Write([]byte("ref: refs/heads/main\n"))
				default:
					t.Error("unexpected Git request", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			// Test-only destination replacement and CA trust let real Git use a
			// local TLS endpoint. The public credential constructor stays fixed.
			credential.destination = server.URL + "/fixture/project.git"
			directory := t.TempDir()
			cert := filepath.Join(directory, "fixture-ca.pem")
			if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = gitInTemporaryCredential(ctx, directory, "", credential, "-c", "http.sslBackend=openssl", "-c", "http.sslCAInfo="+cert, "ls-remote", "--refs", "--", credential.destination, "refs/heads/main")
			if requests.Load() == 0 {
				t.Fatal("Git did not reach TLS fixture", err)
			}
			if !reject && err != nil {
				t.Fatal("authenticated advertisement failed", err)
			}
			if reject && (err == nil || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), credential.header)) {
				t.Fatal("rejected authentication leaked or succeeded")
			}
		})
	}
}
