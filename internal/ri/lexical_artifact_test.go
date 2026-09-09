package ri

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLargeLexicalArtifactThroughActualRust(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("explicit Rust binary required")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{Executable: executable, ExecutableHash: fmt.Sprintf("%x", sha256.Sum256(binary))}
	m := LexicalManifest{Version: 1, Source: Source{strings.Repeat("a", 64), "sha1", strings.Repeat("b", 40), strings.Repeat("c", 40)}}
	for n := 0; n < 6000; n++ {
		m.Files = append(m.Files, LexicalFile{Path: fmt.Sprintf("src/%06d.rs", n), Blob: strings.Repeat("d", 40), SHA256: strings.Repeat("e", 64), Bytes: 3})
	}
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.WriteRecords(file); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() <= 1<<20 {
		t.Fatal("fixture must exceed inline protocol bound", err)
	}
	if err := client.ValidateLexicalArtifact(context.Background(), path, m); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	if client.ValidateLexicalArtifact(context.Background(), path, m) == nil {
		t.Fatal("torn artifact accepted")
	}
}
