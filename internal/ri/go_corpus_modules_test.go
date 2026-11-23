package ri

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestGoCommittedCorpusExplicitModulesPreservesLegacyAndNestedOwnership(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	initGoCorpusGit(t, root, map[string][]byte{
		"go.mod":          []byte("module example.com/root\ngo 1.20\n"),
		"api/value.go":    []byte("package api\nfunc Value() int { return 1 }\n"),
		"consumer/use.go": []byte("package consumer\nimport \"example.com/root/api\"\nfunc Value() int { return api.Value() }\n"),
		"tools/go.mod":    []byte("module example.com/tools\ngo 1.20\n"),
		"tools/main.go":   []byte("package main\nfunc main() {}\n"),
		"broken/go.mod":   []byte("module\n"),
		"broken/value.go": []byte("package broken\nfunc Value() int { return 0 }\n"),
	})
	identity, err := repository.Discover(context.Background(), root, "module-corpus-fixture")
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	// Inventory and corpus must both read the committed manifests, even after a
	// working-tree edit gives the root a different declared module name.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/dirty\ngo 1.20\n"), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", "Value implementation")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.ModuleInventory != nil {
		t.Fatal("legacy collection opted into module ownership")
	}
	for _, file := range legacy.GraphInputs {
		if file.Package.IdentityKind != goCorpusSourceLocalV1 || file.Package.ImportPath != "" {
			t.Fatal("legacy package identity changed")
		}
	}
	corpus, err := CollectCommittedGoCorpusWithOptions(context.Background(), identity, client, "", "Value implementation", GoCorpusOptions{ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	if corpus.ModuleInventory == nil || corpus.ModuleInventory.Digest != inventory.Digest {
		t.Fatal("module inventory lost")
	}
	seen := map[string]bool{}
	for _, file := range corpus.GraphInputs {
		switch file.Facts.Path {
		case "api/value.go":
			if file.Package.ImportPath != "example.com/root/api" {
				t.Fatal("committed root ownership lost")
			}
		case "tools/main.go":
			if file.Package.ImportPath != "example.com/tools" {
				t.Fatal("nested module inherited parent ownership")
			}
		case "broken/value.go":
			if file.Package.IdentityKind != goCorpusSourceLocalV1 || file.Package.ImportPath != "" {
				t.Fatal("malformed nested manifest did not shadow parent")
			}
		}
		seen[file.Facts.Path] = true
	}
	if !seen["api/value.go"] || !seen["tools/main.go"] || !seen["broken/value.go"] {
		t.Fatal("fixture corpus omitted required paths")
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: inventory.RepositoryID, ProducerSHA256: client.ExecutableHash, Files: corpus.GraphInputs, ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		t.Fatal(err)
	}
}
