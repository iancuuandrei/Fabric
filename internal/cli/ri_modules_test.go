package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

func TestRIModulesReportsCommittedInventoryAndRejectsSubstitution(t *testing.T) {
	root := newRIModuleFixture(t)
	writeRIModuleFixture(t, root)
	commitRIModuleFixture(t, root)
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/dirty\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "modules"}, root, &output); err != nil {
		t.Fatal(err)
	}
	var inventory ri.GoModuleInventory
	if err := json.Unmarshal(output.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	cfg, err := configuration(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := ri.ValidateGoModuleInventory(inventory, identity); err != nil {
		t.Fatalf("module command returned an unbound inventory: %v", err)
	}
	rootModule := moduleObservation(t, inventory, "go.mod")
	nestedModule := moduleObservation(t, inventory, "nested/go.mod")
	if rootModule.ModulePath != "example.invalid/root" || nestedModule.ModulePath != "example.invalid/nested" || inventory.Coverage != "complete" {
		t.Fatalf("inventory used working-tree declarations or omitted nested module: root=%+v nested=%+v", rootModule, nestedModule)
	}

	spec := goGraphSpec{Files: []goGraphFileSpec{{Path: "root.go"}, {Path: "nested/pkg/pkg.go"}, {Path: "app/use.go"}}, Generators: []ri.GoGeneratorBinding{}, ModuleInventory: &inventory}
	if err := validateGoGraphSpec(spec); err != nil {
		t.Fatalf("module-backed graph spec with derived identities rejected: %v", err)
	}
	wrongSource := inventory
	wrongSource.RepositoryID = strings.Repeat("0", 64)
	wrongSource.Digest, err = canonical.Hash("harness.ri.go-module-inventory.v1", inventoryContentForDigest(wrongSource))
	if err != nil {
		t.Fatal(err)
	}
	spec.ModuleInventory = &wrongSource
	specBytes, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "substituted-spec.json")
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "graph", filepath.Join(root, "missing-parser"), strings.Repeat("a", 64), specPath}, root, &output); err == nil || !strings.Contains(err.Error(), "not bound to the configured committed source") || output.Len() != 0 {
		t.Fatalf("source-substituted inventory was not rejected before parser access: err=%v output=%q", err, output.String())
	}
	forged := inventory
	for i := range forged.Files {
		if forged.Files[i].Path == "go.mod" {
			forged.Files[i].ModulePath = "example.invalid/forged"
		}
	}
	forged.Digest, err = canonical.Hash("harness.ri.go-module-inventory.v1", inventoryContentForDigest(forged))
	if err != nil {
		t.Fatal(err)
	}
	spec.ModuleInventory = &forged
	specBytes, err = canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "graph", filepath.Join(root, "missing-parser"), strings.Repeat("a", 64), specPath}, root, &output); err == nil || !strings.Contains(err.Error(), "differs from the configured committed source") || output.Len() != 0 {
		t.Fatalf("self-rehashed manifest substitution was not rejected before parser access: err=%v output=%q", err, output.String())
	}
	malformed := inventory
	malformed.Digest = strings.Repeat("0", 64)
	spec.ModuleInventory = &malformed
	if err := validateGoGraphSpec(spec); err == nil {
		t.Fatal("malformed embedded inventory digest accepted")
	}
}

func TestRIModuleBackedGraphUsesRootAndNestedCommittedModules(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built Rust RI executable")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	exeSHA := hex.EncodeToString(digest[:])
	root := newRIModuleFixture(t)
	writeRIModuleFixture(t, root)
	commitRIModuleFixture(t, root)
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &output); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "go.mod"), []byte("module example.invalid/dirty-nested\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "modules"}, root, &output); err != nil {
		t.Fatal(err)
	}
	var inventory ri.GoModuleInventory
	if err := json.Unmarshal(output.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	spec := goGraphSpec{Files: []goGraphFileSpec{{Path: "root.go"}, {Path: "nested/pkg/pkg.go"}, {Path: "app/use.go"}}, Generators: []ri.GoGeneratorBinding{}, ModuleInventory: &inventory}
	specBytes, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "graph-spec.json")
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "graph", executable, exeSHA, specPath}, root, &output); err != nil {
		t.Fatal(err)
	}
	var result goGraphResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Graph.CandidateID != "" || result.Graph.ModuleInventory == nil || result.Graph.ModuleInventory.Digest != inventory.Digest {
		t.Fatalf("module-backed graph lost its base inventory binding: %+v", result.Graph)
	}
	for _, file := range []string{"root.go", "nested/pkg/pkg.go", "app/use.go"} {
		if !hasGraphEdgeForPath(result.Graph.Edges, "DECLARED_OWNERSHIP", file) {
			t.Fatalf("module ownership edge missing for %s", file)
		}
	}
	if !hasGraphEdgeTargetCLI(result.Graph.Edges, "IMPORTS", "app/use.go", packageNodeID("example.invalid/root")) || !hasGraphEdgeTargetCLI(result.Graph.Edges, "IMPORTS", "app/use.go", packageNodeID("example.invalid/nested/pkg")) {
		t.Fatal("root and nested module imports were not linked to declared package nodes")
	}
	if result.Repository.Commit != inventory.Commit || result.Repository.RepositoryID != inventory.RepositoryID {
		t.Fatal("graph source binding differs from module inventory")
	}
}

func newRIModuleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "-q")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}

func writeRIModuleFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod":            "module example.invalid/root\n\ngo 1.27\n",
		"root.go":           "package root\nfunc Root() {}\n",
		"nested/go.mod":     "module example.invalid/nested\n\ngo 1.27\n",
		"nested/pkg/pkg.go": "package nested\nfunc Nested() {}\n",
		"app/use.go":        "package app\nimport (\n \"example.invalid/root\"\n \"example.invalid/nested/pkg\"\n)\nfunc Use() { root.Root(); nested.Nested() }\n",
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func commitRIModuleFixture(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("add", ".")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "module fixture")
}

func moduleObservation(t *testing.T, inventory ri.GoModuleInventory, path string) ri.GoManifestObservation {
	t.Helper()
	for _, file := range inventory.Files {
		if file.Path == path {
			return file
		}
	}
	t.Fatalf("module inventory has no %s observation", path)
	return ri.GoManifestObservation{}
}

func inventoryContentForDigest(inventory ri.GoModuleInventory) any {
	inventory.Digest = ""
	return inventory
}

func hasGraphEdgeForPath(edges []ri.GoGraphEdge, relation, sourcePath string) bool {
	for _, edge := range edges {
		if edge.Relation == relation && edge.Path == sourcePath {
			return true
		}
	}
	return false
}

func packageNodeID(importPath string) string {
	id, _ := canonical.Hash("harness.ri.go-package-node.v1", importPath)
	return "package:" + id
}

func hasGraphEdgeTargetCLI(edges []ri.GoGraphEdge, relation, sourcePath, target string) bool {
	for _, edge := range edges {
		if edge.Relation == relation && edge.Path == sourcePath && edge.To == target {
			return true
		}
	}
	return false
}
