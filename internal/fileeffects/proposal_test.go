package fileeffects

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func ptr(s string) *string { return &s }
func TestPredictionPreservesUntouchedFiles(t *testing.T) {
	hash := strings.Repeat("a", 64)
	files := []worktree.FileState{{Path: "keep", Hash: hash}, {Path: "replace", Hash: hash}}
	id, err := worktree.FilesID(files)
	if err != nil {
		t.Fatal(err)
	}
	before := worktree.Candidate{Version: 1, WorktreeID: hash, Head: strings.Repeat("b", 40), IndexHash: hash, FilesHash: id, FileCount: 2}
	change := Change{Path: "replace", BeforeHash: &hash, ContentBase64: ptr("eA==")}
	after, err := predict(before, files, []Change{change})
	if err != nil {
		t.Fatal(err)
	}
	p := Proposal{Version: 1, Nonce: "test", Before: before, BeforeFiles: files, Changes: []Change{change}, After: after}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.After.FileCount = 1
	if err := p.Validate(); err == nil {
		t.Fatal("unrelated file removal admitted")
	}
	p.After = after
	p.Changes[0].Path = ".git/config"
	if err := p.Validate(); err == nil {
		t.Fatal("control path admitted")
	}
}

func TestInterruptedApplyLeavesPartialState(t *testing.T) {
	d := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d}, args...)...)
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(d, "a"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "a")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	repo, err := repository.Discover(context.Background(), d, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	request, err := worktree.Prepare(strings.Repeat("a", 64), repo)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := worktree.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("before"))
	p, err := Prepare(context.Background(), binding, "fault", []Change{{Path: "a", BeforeHash: ptr(hex.EncodeToString(h[:])), ContentBase64: ptr(base64.StdEncoding.EncodeToString([]byte("after")))}, {Path: "b", ContentBase64: ptr("eA==")}})
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("injected stop after first file")
	err = apply(context.Background(), binding, p, func(i int) error {
		if i == 1 {
			return crash
		}
		return nil
	})
	if !errors.Is(err, crash) {
		t.Fatal(err)
	}
	observed, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if Classify(p, &observed) != "UNKNOWN" {
		t.Fatal("partial candidate accepted")
	}
	if err := Apply(context.Background(), binding, p); err == nil {
		t.Fatal("blind partial retry admitted")
	}
}
