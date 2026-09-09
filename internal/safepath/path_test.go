package safepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableWriterPaths(t *testing.T) {
	for _, p := range []string{"../escape", "/root", "a\\b", "x:y", "a/./b", "a//b", "a.", "a ", "NUL.txt", "COM1", "x/.git/config", "x/.harness/state", ".github/workflows/run.yml", "nested/AGENTS.md", "harness.toml"} {
		if err := Writable(p); err == nil {
			t.Fatal("admitted", p)
		}
	}
	if err := Writable("src/hello.go"); err != nil {
		t.Fatal(err)
	}
}

func TestHardlinkAndMissingAreDistinct(t *testing.T) {
	d := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(d, "alias")); err != nil {
		t.Fatal(err)
	}
	r, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, _, _, _, err := ReadRegular(r, "alias", 100); err == nil {
		t.Fatal("hardlink admitted")
	}
	if _, _, _, exists, err := ReadRegular(r, "missing", 100); err != nil || exists {
		t.Fatal(exists, err)
	}
	if err := os.WriteFile(filepath.Join(d, "empty"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if h, n, _, exists, err := ReadRegular(r, "empty", 100); err != nil || !exists || n != 0 || h == "" {
		t.Fatal("empty confused with absence", err)
	}
}

func TestSymlinkEscapeWhenPermitted(t *testing.T) {
	d := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(d, "link")); err != nil {
		t.Skipf("host cannot create symlink: %v", err)
	}
	r, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if err := Check(r, "link/file", true); err == nil {
		t.Fatal("link traversal admitted")
	}
}
