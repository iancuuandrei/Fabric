package testsupport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// CandidateWorkspace creates a leased worktree, applies fixture files, and
// fingerprints the resulting candidate. The lease is released at test cleanup.
func CandidateWorkspace(t testing.TB, source repository.Identity, runID string, files map[string][]byte) (worktree.Binding, worktree.Candidate) {
	t.Helper()
	request, err := worktree.Prepare(runID, source)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lease.Close(); err != nil {
			t.Errorf("close candidate workspace lease: %v", err)
		}
	})
	workspace, err := worktree.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(request.Path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := worktree.Fingerprint(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	return workspace, candidate
}
