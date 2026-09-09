package ri

import (
	"context"
	"harness.local/engorch/internal/repository"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLexicalMaterializationUsesCommittedDeduplicatedBytes(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	git("init", "-q")
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("committed\x00bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	identity, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveLexical(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := observed.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan := LexicalPlan{Version: 1, Repository: identity, ManifestID: manifestID, Files: len(observed.Manifest.Files), Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("a", 64), StageRoot: filepath.Join(root, "stage"), BatchBytes: 1048576, BatchFiles: 100}
	if err := plan.ValidateManifest(observed.Manifest); err != nil {
		t.Fatal(err)
	}
	intent, err := plan.Intent(strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil || intent.Kind != "ri_lexical" {
		t.Fatal(err)
	}
	changed := plan
	changed.StageRoot = filepath.Join(root, "other")
	other, err := changed.Intent(intent.RunID, intent.PlanID)
	if err != nil || other.InputHash == intent.InputHash {
		t.Fatal("staging substitution not bound", err)
	}
	changed = plan
	changed.Files++
	if changed.ValidateManifest(observed.Manifest) == nil {
		t.Fatal("manifest count substitution accepted")
	}
	changed = plan
	changed.BatchBytes = 1
	if changed.ValidateManifest(observed.Manifest) == nil {
		t.Fatal("oversized source accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "sources")
	if err := MaterializeLexical(context.Background(), identity, observed.Manifest, output); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 1 {
		t.Fatal("duplicate content not deduplicated", err)
	}
	raw, err := os.ReadFile(filepath.Join(output, observed.Manifest.Files[0].SHA256))
	if err != nil || string(raw) != "committed\x00bytes" {
		t.Fatal("materialized wrong bytes", err)
	}
	if MaterializeLexical(context.Background(), identity, observed.Manifest, output) == nil {
		t.Fatal("existing staging reused")
	}
	bad := observed.Manifest
	bad.Files = append([]LexicalFile(nil), bad.Files...)
	bad.Files[0].SHA256 = bad.Source.RepositoryID
	if MaterializeLexical(context.Background(), identity, bad, filepath.Join(t.TempDir(), "bad")) == nil {
		t.Fatal("substituted expected bytes accepted")
	}
}
