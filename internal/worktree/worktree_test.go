package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
)

func fixture(t testing.TB) (string, Request) {
	t.Helper()
	d := t.TempDir()
	gitTest(t, d, "init", "-q")
	if err := os.WriteFile(filepath.Join(d, "source.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, d, "add", "source.txt")
	gitTest(t, d, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	i, err := repository.Discover(context.Background(), d, "unrelated-fixture")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Prepare(strings.Repeat("a", 64), i)
	if err != nil {
		t.Fatal(err)
	}
	return d, r
}

func gitTest(t testing.TB, root string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
}

func TestIsolatedMaterializationPreservesDirtySource(t *testing.T) {
	d, r := fixture(t)
	if err := os.WriteFile(filepath.Join(d, "source.txt"), []byte("user dirty edits\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// A source checkout hook must not run during admitted materialization.
	hook := filepath.Join(r.Source.CommonDir, "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf unexpected > hook-fired\n"), 0700); err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if _, err := Acquire(r); err == nil {
		t.Fatal("second writer admitted")
	}
	b, err := Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(r.Path, "source.txt"))
	if err != nil || string(raw) != "committed\n" {
		t.Fatal("not exact blob", err)
	}
	source, err := os.ReadFile(filepath.Join(d, "source.txt"))
	if err != nil || string(source) != "user dirty edits\n" {
		t.Fatal("dirty source changed", err)
	}
	if _, err := os.Stat(filepath.Join(r.Path, "hook-fired")); !os.IsNotExist(err) {
		t.Fatal("checkout hook executed")
	}
	first, err := Fingerprint(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Fingerprint(context.Background(), b)
	if err != nil || again != first {
		t.Fatal("unstable fingerprint", err)
	}
	if err := os.WriteFile(filepath.Join(r.Path, "source.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := Fingerprint(context.Background(), b)
	if err != nil || changed.FilesHash == first.FilesHash {
		t.Fatal("candidate edit not observed", err)
	}
	if _, err := Create(context.Background(), r); err == nil {
		t.Fatal("automatic recreation admitted")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestObserveRejectsOtherWorktreeAndChangedHead(t *testing.T) {
	_, r := fixture(t)
	b, err := Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	forged := r
	forged.Path = r.Source.Root
	if _, err := Observe(context.Background(), forged); err == nil {
		t.Fatal("canonical checkout admitted as writer")
	}
	gitTest(t, r.Path, "checkout", "--detach")
	if _, err := Fingerprint(context.Background(), b); err == nil {
		t.Fatal("detached writer admitted")
	}
}

func TestCandidateRejectsHardlink(t *testing.T) {
	_, r := fixture(t)
	b, err := Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(outside, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(r.Path, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(context.Background(), b); err == nil {
		t.Fatal("hardlink candidate admitted")
	}
}

func TestLeaseOwnershipSubstitution(t *testing.T) {
	_, r := fixture(t)
	l, err := Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.path, []byte("different owner"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err == nil {
		t.Fatal("released changed owner")
	}
	if _, err := os.Stat(l.path); err != nil {
		t.Fatal("replacement lock removed", err)
	}
}

func TestRawGitSymlinkRejectedBeforeCreation(t *testing.T) {
	d, r := fixture(t)
	cmd := exec.Command("git", "-C", d, "hash-object", "-w", "--stdin")
	cmd.Stdin = strings.NewReader("../../outside")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	object := strings.TrimSpace(string(out))
	gitTest(t, d, "update-index", "--add", "--cacheinfo", "120000,"+object+",link")
	gitTest(t, d, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "symlink source")
	r.Source, err = repository.Discover(context.Background(), d, r.Source.Name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), r); err == nil {
		t.Fatal("source symlink admitted")
	}
	if _, err := os.Stat(r.Path); !os.IsNotExist(err) {
		t.Fatal("workspace created before source admission")
	}
}

func TestCreateLongBranchPathWithMaskedGlobalConfig(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Git for Windows long-path behavior")
	}

	root := t.TempDir()
	for len(filepath.Join(root, ".git", "refs", "heads", "harness", strings.Repeat("a", 64)+".lock")) <= 260 {
		root = filepath.Join(root, "x")
	}
	if len(root) >= 260 {
		t.Fatalf("fixture root unexpectedly exceeds classic path limit: %d", len(root))
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "source.txt")
	gitTest(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	gitTest(t, root, "config", "core.longpaths", "false")

	identity, err := repository.Discover(context.Background(), root, "longpath-fixture")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Prepare(strings.Repeat("a", 64), identity)
	if err != nil {
		t.Fatal(err)
	}
	refLock := filepath.Join(identity.CommonDir, "refs", "heads", "harness", r.RunID+".lock")
	if len(refLock) <= 260 {
		t.Fatalf("test does not cross classic ref-lock path limit: %d", len(refLock))
	}

	// The production command intentionally masks machine-wide Git config.
	// Verify the local override is in the actual argv, then prove Create can
	// register and materialize a normal fresh worktree despite local=false.
	cmd := command(context.Background(), root, "config", "--get", "core.longpaths")
	if !strings.Contains(strings.Join(cmd.Args, " "), "-c core.longpaths=true") {
		t.Fatalf("Git command lacks explicit long-path override: %q", cmd.Args)
	}
	globalMasked, noSystem := false, false
	for _, entry := range cmd.Env {
		if entry == "GIT_CONFIG_GLOBAL="+os.DevNull {
			globalMasked = true
		}
		if entry == "GIT_CONFIG_NOSYSTEM=1" {
			noSystem = true
		}
	}
	if !globalMasked || !noSystem {
		t.Fatal("test command did not retain masked global/system Git config")
	}
	if got, err := gitText(context.Background(), root, "config", "--get", "core.longpaths"); err != nil || got != "true" {
		t.Fatalf("explicit command-level config did not override repository=false: got %q, err %v", got, err)
	}

	binding, err := Create(context.Background(), r)
	if err != nil {
		t.Fatalf("Create failed with a >260-character branch ref lock path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(identity.CommonDir, "refs", "heads", r.Branch)); err != nil {
		t.Fatalf("fresh worktree branch ref was not created: %v", err)
	}
	if _, err := Observe(context.Background(), r); err != nil {
		t.Fatalf("fresh long-path worktree did not pass normal observation: %v", err)
	}
	if binding.Request != r {
		t.Fatal("created binding changed the admitted request")
	}
}
