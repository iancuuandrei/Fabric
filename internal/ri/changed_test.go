package ri

import (
	"context"
	"harness.local/engorch/internal/repository"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangedRejectsForeignRepositoryBeforeDispatch(t *testing.T) {
	root := t.TempDir()
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	source, err := FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	ref := SnapshotRef{Source: source}
	foreign := identity
	foreign.Root = t.TempDir()
	foreignSource, err := FromRepository(foreign)
	if err != nil {
		t.Fatal(err)
	}
	_, err = (Client{}).Changed(context.Background(), ref, SnapshotRef{Source: foreignSource}, identity, foreign)
	if err == nil || !strings.Contains(err.Error(), "authority mismatch") {
		t.Fatal("foreign repository reached dispatch", err)
	}
	substituted := ref
	substituted.Source.Commit = strings.Repeat("c", 40)
	_, err = (Client{}).Changed(context.Background(), ref, substituted, identity, identity)
	if err == nil || !strings.Contains(err.Error(), "authority mismatch") {
		t.Fatal("substituted binding reached dispatch", err)
	}
}
