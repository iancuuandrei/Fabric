package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeaseKernelExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease.lock")
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := lockLeaseFile(first); err != nil {
		t.Fatal(err)
	}
	if err := lockLeaseFile(second); err == nil {
		t.Fatal("second owner acquired held kernel lock")
	}
	if token, err := os.ReadFile(path); err != nil || len(token) != 64 {
		t.Fatal("kernel lock prevented token observation", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lockLeaseFile(second); err != nil {
		t.Fatal("closed owner retained kernel exclusion", err)
	}
}

func TestLeaseKernelSharedAndExclusiveModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "guard.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	first, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	writer, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := lockLeaseFileShared(first); err != nil {
		t.Fatal(err)
	}
	if err := lockLeaseFileShared(second); err != nil {
		t.Fatal("second reader did not share guard", err)
	}
	if err := lockLeaseFile(writer); err == nil {
		t.Fatal("writer acquired guard while readers were active")
	}
}
