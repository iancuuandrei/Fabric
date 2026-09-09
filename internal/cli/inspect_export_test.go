package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectExportRejectsMissingCorruptAndCancelledHistory(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	path, err := runPath(root, id)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"inspect", id, "--export-jsonl"}, root, &out); err == nil || out.Len() != 0 {
		t.Fatal("missing history was exported", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection created a journal", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	bad := []byte("{\"version\":1}\n")
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"inspect", id, "--export-jsonl"}, root, &out); err == nil || out.Len() != 0 {
		t.Fatal("corrupt history was exported", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Execute(ctx, []string{"inspect", id, "--export-jsonl"}, root, &out); err == nil || out.Len() != 0 {
		t.Fatal("cancelled export succeeded", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, bad) {
		t.Fatal("failed inspection changed evidence", err)
	}
}
