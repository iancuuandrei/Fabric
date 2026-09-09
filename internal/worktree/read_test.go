package worktree

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestCandidateReadTracksModifiedBytesAndDrift(t *testing.T) {
	_, request := fixture(t)
	lease, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	binding, err := Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte{'x', 0xff, 'y', '\n'}
	if err := os.WriteFile(filepath.Join(request.Path, "source.txt"), content, 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	first, err := ReadSource(context.Background(), binding, candidate, "source.txt", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContentBase64 != base64.StdEncoding.EncodeToString(content[:2]) || first.ContentUTF8 != nil || first.NextOffset == nil || *first.NextOffset != 2 {
		t.Fatal("binary page mismatch", first)
	}
	last, err := ReadSource(context.Background(), binding, candidate, "source.txt", 2, 2)
	if err != nil || last.ContentUTF8 == nil || *last.ContentUTF8 != "y\n" || last.NextOffset != nil || last.SHA256 != first.SHA256 {
		t.Fatal("final page mismatch", err)
	}
	for _, path := range []string{"../source.txt", ".git", "absent"} {
		if _, err := ReadSource(context.Background(), binding, candidate, path, 0, 2); err == nil {
			t.Fatal("invalid candidate path admitted", path)
		}
	}
	if _, err := ReadSource(context.Background(), binding, candidate, "source.txt", 5, 2); err == nil {
		t.Fatal("past-EOF offset admitted")
	}
	if err := os.WriteFile(filepath.Join(request.Path, "other.txt"), []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSource(context.Background(), binding, candidate, "source.txt", 0, 2); err == nil {
		t.Fatal("unrelated candidate drift admitted")
	}
}
