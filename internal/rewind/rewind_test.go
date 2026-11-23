package rewind

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointCreateListAndRestore(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nconst V = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")

	ctx := context.Background()
	runID := "0123456789abcdef0123456789abcdef"

	// Step 0: base snapshot
	if _, err := Create(ctx, dir, runID, 0, "pre-write"); err != nil {
		t.Fatalf("Create step 0: %v", err)
	}
	// Mutate app.go + add new file -> Step 1
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\nconst V = 2\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package app\n"), 0o600)
	if _, err := Create(ctx, dir, runID, 1, "after-write"); err != nil {
		t.Fatalf("Create step 1: %v", err)
	}
	// Break app.go
	_ = os.WriteFile(filepath.Join(dir, "app.go"), []byte("BROKEN SYNTAX"), 0o600)

	snaps, err := List(ctx, dir, runID)
	if err != nil || len(snaps) != 2 {
		t.Fatalf("List failed: snaps=%+v err=%v", snaps, err)
	}

	// Rewind to step 1
	if _, err := Restore(ctx, dir, runID, 1); err != nil {
		t.Fatalf("Restore step 1: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "app.go"))
	if strings.ReplaceAll(string(got), "\r\n", "\n") != "package app\nconst V = 2\n" {
		t.Fatalf("unexpected content after restore step 1: %q", string(got))
	}

	// Rewind to step 0 (extra.go must disappear!)
	if _, err := Restore(ctx, dir, runID, 0); err != nil {
		t.Fatalf("Restore step 0: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dir, "app.go"))
	if strings.ReplaceAll(string(got), "\r\n", "\n") != "package app\nconst V = 1\n" {
		t.Fatalf("unexpected content after restore step 0: %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "extra.go")); !os.IsNotExist(err) {
		t.Fatal("expected extra.go to be removed after rewinding to step 0")
	}
}
