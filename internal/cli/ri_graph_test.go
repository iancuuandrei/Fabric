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
	"github.com/iancuuandrei/fabric/internal/ri"
)

func TestValidateGoGraphSpecRejectsUnsafeOrUnboundInputs(t *testing.T) {
	base := func() goGraphSpec {
		return goGraphSpec{Files: []goGraphFileSpec{{Path: "pkg/a.go", ImportPath: "example.invalid/mod/pkg", ModulePath: "example.invalid/mod"}}, Generators: []ri.GoGeneratorBinding{}}
	}
	for name, mutate := range map[string]func(*goGraphSpec){
		"sensitive":      func(spec *goGraphSpec) { spec.Files[0].Path = "secrets/a.go" },
		"traversal":      func(spec *goGraphSpec) { spec.Files[0].Path = "../a.go" },
		"duplicate":      func(spec *goGraphSpec) { spec.Files = append(spec.Files, spec.Files[0]) },
		"outside module": func(spec *goGraphSpec) { spec.Files[0].ImportPath = "other.invalid/pkg" },
		"generator outside corpus": func(spec *goGraphSpec) {
			spec.Generators = append(spec.Generators, generatorFixture("pkg/a.go", "pkg/missing.go", "//go:generate gen"))
		},
		"empty test package": func(spec *goGraphSpec) { spec.Files[0].TestOfImportPath = "example.invalid/mod/pkg" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := base()
			mutate(&spec)
			if err := validateGoGraphSpec(spec); err == nil {
				t.Fatal("invalid spec accepted")
			}
		})
	}
	spec := base()
	spec.Files = make([]goGraphFileSpec, goGraphMaxFiles+1)
	for i := range spec.Files {
		spec.Files[i] = goGraphFileSpec{Path: filepath.ToSlash(filepath.Join("pkg", string(rune('a'+i%26))+string(rune('A'+i/26))+".go")), ImportPath: "example.invalid/mod/pkg", ModulePath: "example.invalid/mod"}
	}
	if err := validateGoGraphSpec(spec); err == nil {
		t.Fatal("oversized path set accepted")
	}
}

func TestReadGoGraphSpecRequiresCanonicalUniqueJSON(t *testing.T) {
	for name, raw := range map[string][]byte{
		"duplicate":           []byte(`{"files":[],"files":[],"generators":[]}`),
		"unknown scope field": []byte(`{"changed_paths":[],"files":[{"import_path":"example.invalid/mod","module_path":"example.invalid/mod","path":"a.go"}],"generators":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGoGraphSpec(path); err == nil {
				t.Fatal("noncanonical or ambiguous spec accepted")
			}
		})
	}
	valid := goGraphSpec{Files: []goGraphFileSpec{{Path: "a.go", ImportPath: "example.invalid/mod", ModulePath: "example.invalid/mod"}}, Generators: []ri.GoGeneratorBinding{}}
	raw, err := canonical.Bytes(valid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spec.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGoGraphSpec(path); err != nil {
		t.Fatalf("canonical spec rejected: %v", err)
	}
	pretty := []byte("{\n  \"generators\": [],\n  \"files\": [{\"path\": \"a.go\", \"module_path\": \"example.invalid/mod\", \"import_path\": \"example.invalid/mod\"}]\n}\n")
	if err := os.WriteFile(path, pretty, 0600); err != nil {
		t.Fatal(err)
	}
	prettySpec, err := readGoGraphSpec(path)
	if err != nil || len(prettySpec.Files) != 1 || prettySpec.Files[0].Path != "a.go" {
		t.Fatalf("pretty strict JSON did not decode to the same spec: spec=%+v err=%v", prettySpec, err)
	}
}

func TestRIGraphRejectsSensitiveSpecBeforeRepositoryOrProducerAccess(t *testing.T) {
	spec := goGraphSpec{Files: []goGraphFileSpec{{Path: "secrets/token.go", ImportPath: "example.invalid/mod", ModulePath: "example.invalid/mod"}}, Generators: []ri.GoGeneratorBinding{}}
	raw, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir() // Deliberately not a repository and has no harness configuration.
	specPath := filepath.Join(root, "spec.json")
	if err := os.WriteFile(specPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "graph", "missing-ri", strings.Repeat("0", 64), specPath}, root, &output); err == nil || output.Len() != 0 {
		t.Fatalf("sensitive source scope was not rejected before setup: err=%v output=%q", err, output.String())
	}
}

func TestGoGraphSourceBufferEnforcesAggregateBound(t *testing.T) {
	buffer := &goGraphSourceBuffer{limit: 3}
	if n, err := buffer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("exact bound rejected: n=%d err=%v", n, err)
	}
	if _, err := buffer.Write([]byte("d")); err == nil {
		t.Fatal("oversized aggregate write accepted")
	}
}

func TestRIGraphAndContextUseCommittedBoundedGoCorpus(t *testing.T) {
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
	exeHash := sha256.Sum256(binary)
	exeSHA := hex.EncodeToString(exeHash[:])
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	files := map[string]string{
		"pkg/generate.go":      "package sample\n//go:generate go run ./cmd/gen\ntype Thing struct{}\nfunc Local() { helper() }\nfunc helper() {}\n",
		"pkg/zz_generated.go":  "// Code generated by fixture; DO NOT EDIT.\npackage sample\nfunc Generated() {}\n",
		"pkg/external_test.go": "package sample_test\nimport sample \"example.invalid/mod/pkg\"\nfunc TestExternal() { sample.Local() }\n",
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
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "RI graph fixture")
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &output); err != nil {
		t.Fatal(err)
	}
	spec := goGraphSpec{
		Files: []goGraphFileSpec{
			{Path: "pkg/generate.go", ImportPath: "example.invalid/mod/pkg", ModulePath: "example.invalid/mod"},
			{Path: "pkg/zz_generated.go", ImportPath: "example.invalid/mod/pkg", ModulePath: "example.invalid/mod"},
			{Path: "pkg/external_test.go", ImportPath: "example.invalid/mod/pkg_test", ModulePath: "example.invalid/mod", TestOfImportPath: "example.invalid/mod/pkg"},
		},
		Generators: []ri.GoGeneratorBinding{generatorFixture("pkg/generate.go", "pkg/zz_generated.go", "//go:generate go run ./cmd/gen")},
	}
	specBytes, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "graph-spec.json")
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	// Dirty source must not change graph facts or context input.
	if err := os.WriteFile(filepath.Join(root, "pkg", "generate.go"), []byte("package wrong\nfunc Dirty() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "graph", executable, exeSHA, specPath}, root, &output); err != nil {
		t.Fatal(err)
	}
	var graphResult goGraphResult
	if err := json.Unmarshal(output.Bytes(), &graphResult); err != nil {
		t.Fatal(err)
	}
	if graphResult.Repository.RepositoryID != graphResult.Graph.SourceID || graphResult.Repository.Commit == "" || graphResult.Graph.CandidateID != "" || graphResult.Graph.Coverage != "PARTIAL" || len(graphResult.Sources) != 3 || len(graphResult.Graph.Files) != 3 {
		t.Fatalf("graph response binding or completeness mismatch: %+v", graphResult.Graph)
	}
	if len(graphResult.Graph.Generators) != 1 || graphResult.Graph.Generators[0].GeneratorPath != "pkg/generate.go" {
		t.Fatalf("explicit generator relation missing: %+v", graphResult.Graph.Generators)
	}
	hasImport, hasTest, hasGenerated, hasCall := false, false, false, false
	for _, edge := range graphResult.Graph.Edges {
		switch edge.Relation {
		case "IMPORTS":
			hasImport = hasImport || edge.Path == "pkg/external_test.go"
		case "TESTS":
			hasTest = hasTest || edge.Path == "pkg/external_test.go"
		case "GENERATED_BY":
			hasGenerated = true
		case "CALLS_UNRESOLVED":
			hasCall = true
		}
	}
	if !hasImport || !hasTest || !hasGenerated || !hasCall {
		t.Fatalf("expected import/test/generator/call relations: import=%t test=%t generated=%t call=%t", hasImport, hasTest, hasGenerated, hasCall)
	}
	for _, source := range graphResult.Sources {
		if source.Source.Commit != graphResult.Repository.Commit || source.Source.RepositoryID != graphResult.Repository.RepositoryID {
			t.Fatalf("source observation is not bound to repository snapshot: %+v", source)
		}
	}

	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "context", executable, exeSHA, specPath, "Update Local generated tests"}, root, &output); err != nil {
		t.Fatal(err)
	}
	var contextResult goContextResult
	if err := json.Unmarshal(output.Bytes(), &contextResult); err != nil {
		t.Fatal(err)
	}
	var contextShape map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &contextShape); err != nil {
		t.Fatal(err)
	}
	if _, duplicateGraph := contextShape["graph"]; duplicateGraph {
		t.Fatal("context response redundantly included the complete graph")
	}
	if len(contextResult.Context.GraphDigest) != 64 || contextResult.Context.Coverage != "PARTIAL" || contextResult.Context.Selection.Scope.SourceID != contextResult.Repository.RepositoryID || len(contextResult.Sources) != 3 {
		t.Fatalf("context lacks exact graph/source binding: %+v", contextResult.Context)
	}
	selected := false
	for _, file := range contextResult.Context.Selection.Selected {
		if file.Path == "pkg/generate.go" && strings.Contains(file.Content, "func Local") && !strings.Contains(file.Content, "Dirty") {
			selected = true
		}
	}
	if !selected {
		t.Fatalf("context did not use objective-matched committed source: %+v", contextResult.Context.Selection.Selected)
	}

	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "graph", executable, strings.Repeat("0", 64), specPath}, root, &output); err == nil || output.Len() != 0 {
		t.Fatal("bad binary hash succeeded or emitted output")
	}
}

func generatorFixture(generatorPath, generatedPath, directive string) ri.GoGeneratorBinding {
	return ri.GoGeneratorBinding{GeneratorPath: generatorPath, GeneratedPath: generatedPath, Directive: directive}
}
