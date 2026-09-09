package ri

import (
	"context"
	"crypto/sha256"
	"fmt"
	"harness.local/engorch/internal/repository"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActualRustImportFromGo(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built RI executable")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{executable, fmt.Sprintf("%x", sha256.Sum256(binary))}
	root := t.TempDir()
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	source, err := FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	// Complete empty SCIP index: metadata tool_info, project_root and UTF8 encoding.
	field := func(tag byte, value []byte) []byte { return append([]byte{tag, byte(len(value))}, value...) }
	tool := append(field(10, []byte("fixture")), field(18, []byte("1"))...)
	meta := append(field(18, tool), field(26, []byte("file:///fixture"))...)
	index := field(10, append(meta, 32, 1))
	indexPath := filepath.Join(root, "index.scip")
	if err := os.WriteFile(indexPath, index, 0600); err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{IndexPath: indexPath, Source: source, Producer: "p", ProjectRoot: "file:///fixture", Sources: map[string]string{}, Policy: "strict", Manifest: Manifest{Format: 1, Source: source, Producers: []Producer{{ID: "p", Name: "fixture", Version: "1", ArtifactSHA256: strings.Repeat("d", 64), Inputs: []Input{{Name: "scip:index", SHA256: fmt.Sprintf("%x", sha256.Sum256(index))}}}}}}
	output := filepath.Join(root, "snapshot.staging")
	receipt, err := client.Import(context.Background(), request, identity, output)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SnapshotID != SnapshotID(data) || receipt.Bytes != len(data) {
		t.Fatal("incorrect receipt")
	}
	if _, err := client.Import(context.Background(), request, identity, output); err == nil {
		t.Fatal("repeated staging admitted")
	}
	request.Source.Commit = strings.Repeat("f", 40)
	if _, err := client.Import(context.Background(), request, identity, filepath.Join(root, "other")); err == nil {
		t.Fatal("foreign source admitted")
	}
}
