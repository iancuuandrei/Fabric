package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/control"
)

func TestInspectReusesBoundSnapshotWithoutReload(t *testing.T) {
	bound := control.Snapshot{RunID: strings.Repeat("a", 64), State: "AWAITING_APPROVAL"}
	got, err := snapshotForCommand("inspect", bound, t.TempDir()+"\\missing.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != bound.RunID || got.State != bound.State {
		t.Fatalf("inspect did not return its validated snapshot: got=%+v want=%+v", got, bound)
	}
}

func TestOtherReadFallbackStillInspectsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("not a journal"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := snapshotForCommand("other", control.Snapshot{}, path)
	if err == nil {
		t.Fatal("non-inspect fallback skipped the journal read")
	}
}
