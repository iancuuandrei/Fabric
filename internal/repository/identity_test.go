package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverRealRepositoryAndIgnoresGitEnvironment(t *testing.T) {
	d := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", d}, args...)...)
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %s", e, b)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(d, "hello.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("add", "hello.txt")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "not-a-repo"))
	i, err := Discover(context.Background(), d, "unrelated-fixture")
	if err != nil {
		t.Fatal(err)
	}
	id, err := i.ID()
	if err != nil || len(id) != 64 {
		t.Fatal(id, err)
	}
	if err := os.WriteFile(filepath.Join(d, "hello.txt"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := Discover(context.Background(), d, "unrelated-fixture")
	if err != nil || j != i {
		t.Fatal("tracked dirty bytes must not impersonate a new commit", err)
	}
	for _, invalid := range []string{"HEAD", i.Commit[:12], i.Tree, "--help"} {
		if _, err := DiscoverCommit(context.Background(), d, "unrelated-fixture", invalid); err == nil {
			t.Fatal("invalid historical commit admitted", invalid)
		}
	}
	// Existing fake GIT_DIR stays excluded by the repository observation helper.
	historical, err := DiscoverCommit(context.Background(), d, "unrelated-fixture", i.Commit)
	if err != nil || historical != i {
		t.Fatal("historical identity mismatch", err)
	}
	contents, err := os.ReadFile(filepath.Join(d, "hello.txt"))
	if err != nil || string(contents) != "dirty\n" {
		t.Fatal("historical observation altered worktree", err)
	}
}

func TestUnbornRepositoryIsError(t *testing.T) {
	d := t.TempDir()
	if b, e := exec.Command("git", "init", "-q", d).CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	if _, e := Discover(context.Background(), d, "empty"); e == nil {
		t.Fatal("fabricated commit")
	}
}
