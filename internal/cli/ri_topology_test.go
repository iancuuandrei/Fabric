package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

func TestReadGoTopologyPathsStrictBoundedInput(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "wrong top level", raw: `{"paths":["pkg/a.go"]}`},
		{name: "non-string entry", raw: `["pkg/a.go",1]`},
		{name: "empty", raw: `[]`},
		{name: "null", raw: `null`},
		{name: "duplicate", raw: `["pkg/a.go","pkg/a.go"]`},
		{name: "traversal", raw: `["../pkg/a.go"]`},
		{name: "absolute", raw: `["C:\\src\\pkg\\a.go"]`},
		{name: "sensitive", raw: `["secrets/test.go"]`},
		{name: "non-Go", raw: `["README.md"]`},
		{name: "invalid syntax", raw: `["pkg/a.go",]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "changed.json")
			if err := os.WriteFile(path, []byte(test.raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGoTopologyPaths(path); err == nil {
				t.Fatalf("accepted invalid changed-path input %s", test.raw)
			}
		})
	}

	tooMany := make([]string, goTopologyMaxPaths+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("pkg/file%d.go", i)
	}
	tooManyPath := filepath.Join(t.TempDir(), "too-many.json")
	if err := os.WriteFile(tooManyPath, []byte(mustJSON(t, tooMany)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGoTopologyPaths(tooManyPath); err == nil {
		t.Fatal("accepted more than 64 changed paths")
	}

	largePath := filepath.Join(t.TempDir(), "large.json")
	if err := os.WriteFile(largePath, []byte(`["`+strings.Repeat("a", goTopologyPathsMaxBytes)+`.go"]`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGoTopologyPaths(largePath); err == nil {
		t.Fatal("accepted changed-path JSON larger than 32 KiB")
	}
	if _, err := readGoTopologyPaths(t.TempDir()); err == nil {
		t.Fatal("accepted a directory instead of a regular JSON file")
	}
}

func TestRITopologyCommandIsExposedWithBoundedUsage(t *testing.T) {
	var out strings.Builder
	err := riCommand(context.Background(), t.TempDir(), []string{"topology"}, &out)
	if err == nil || !strings.Contains(err.Error(), "ri topology EXE EXE_SHA256 SPEC_JSON CHANGED_PATHS_JSON MAX_GROUP_FILES") {
		t.Fatalf("topology command missing bounded usage response: %v", err)
	}
	err = riGoTopologyCommand(context.Background(), t.TempDir(), []string{"topology", "ri.exe", strings.Repeat("a", 64), "spec.json", "changed.json", "33"}, &out)
	if err == nil || !strings.Contains(err.Error(), "MAX_GROUP_FILES must be an integer from 1 to 32") {
		t.Fatalf("topology command did not reject an oversized group limit: %v", err)
	}
}

func TestTopologyResultKeepsGraphAndSourceBinding(t *testing.T) {
	sourceID, producer := strings.Repeat("0", 64), strings.Repeat("b", 64)
	path := "pkg/api.go"
	source := []byte("package api\n")
	sourceSum := sha256.Sum256(source)
	facts := ri.GoFileFacts{
		Schema: "engorch.go-file-facts.v1", Language: "go", ParserVersion: "tree-sitter-go-0.25.0",
		Path: path, SourceSHA256: hex.EncodeToString(sourceSum[:]), ProducerSHA256: producer,
		SyntaxErrors: false, Coverage: "PARTIAL", Declarations: []ri.GoSymbol{}, Imports: []ri.GoImport{}, Calls: []ri.GoCall{},
		GeneratedMarkers: []string{}, Cache: "miss", ParseCount: 1,
	}
	var err error
	facts.CacheKey, err = canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{
		"schema": facts.Schema, "language": facts.Language, "parser": facts.ParserVersion,
		"path": path, "source_sha256": facts.SourceSHA256, "producer_sha256": producer,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := facts
	body.Cache, body.ParseCount, body.BodySHA256 = "", 0, ""
	bodyBytes, err := canonical.Bytes(body)
	if err != nil {
		t.Fatal(err)
	}
	bodySum := sha256.Sum256(bodyBytes)
	facts.BodySHA256 = hex.EncodeToString(bodySum[:])
	packageBinding, err := ri.SourceLocalGoPackageBinding(sourceID, path, "api")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{
		SourceID: sourceID, ProducerSHA256: producer,
		Files: []ri.GoGraphFileInput{{Facts: facts, Source: source, Package: packageBinding}},
	})
	if err != nil {
		t.Fatal(err)
	}
	built := builtGoGraph{result: goGraphResult{
		Repository: ri.Source{RepositoryID: sourceID},
		Sources:    []goGraphSourceObservation{{Source: repository.SourceDigest{RepositoryID: sourceID, Path: path, SHA256: facts.SourceSHA256, Bytes: int64(len(source))}}},
		Graph:      graph,
	}}
	result, err := queryGoTopologyResult(built, []string{path}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if result.Topology.GraphDigest != graph.Digest || result.Topology.SourceID != sourceID || result.Topology.Coverage != "PARTIAL" || result.Repository.RepositoryID != sourceID || len(result.Sources) != 1 {
		t.Fatalf("topology output lost committed-source binding: %#v", result)
	}
	if result.Topology.CandidateID != "" || len(result.Topology.ChangedPaths) != 1 || result.Topology.ChangedPaths[0] != path {
		t.Fatalf("base topology unexpectedly claims a candidate or lost seeds: %#v", result.Topology)
	}
}

func TestRITopologyUsesPinnedCommittedCorpusDespiteDirtySource(t *testing.T) {
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
	cliGit(t, root, "init", "-q")
	files := map[string]string{
		"pkg/generate.go":      "package sample\n//go:generate go run ./cmd/gen\nfunc Local() {}\n",
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
	cliGit(t, root, "add", ".")
	cliGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "RI topology fixture")
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &out); err != nil {
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
	specJSON, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "spec.json")
	if err := os.WriteFile(specPath, specJSON, 0600); err != nil {
		t.Fatal(err)
	}
	changedPath := filepath.Join(root, "changed-paths.json")
	changedJSON, err := canonical.Bytes([]string{"pkg/zz_generated.go"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(changedPath, changedJSON, 0600); err != nil {
		t.Fatal(err)
	}
	// Dirty bytes are deliberately different from the committed parser input.
	if err := os.WriteFile(filepath.Join(root, "pkg", "generate.go"), []byte("package changed\nfunc Dirty() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(context.Background(), []string{"ri", "topology", executable, exeSHA, specPath, changedPath, "1"}, root, &out); err != nil {
		t.Fatal(err)
	}
	var result goTopologyResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Repository.RepositoryID == "" || result.Repository.Commit == "" || result.Topology.SourceID != result.Repository.RepositoryID || result.Topology.GraphDigest == "" || result.Topology.CandidateID != "" || result.Topology.Coverage != "PARTIAL" {
		t.Fatalf("topology output is not bound to the committed base: %+v", result)
	}
	if len(result.Sources) != 3 {
		t.Fatalf("expected all three explicit committed sources: %+v", result.Sources)
	}
	for _, source := range result.Sources {
		if source.Source.Commit != result.Repository.Commit || source.Source.RepositoryID != result.Repository.RepositoryID {
			t.Fatalf("source escaped its repository snapshot: %+v", source.Source)
		}
		if source.Source.Path == "pkg/generate.go" && source.Source.SHA256 == fmt.Sprintf("%x", sha256.Sum256([]byte("package changed\nfunc Dirty() {}\n"))) {
			t.Fatal("topology used dirty working-tree bytes")
		}
	}
	foundGeneratorCoupling, foundTest := false, false
	for _, coupling := range result.Topology.Couplings {
		foundGeneratorCoupling = foundGeneratorCoupling || coupling.Reason == "EXPLICIT_GENERATOR_OWNER" && coupling.From == "pkg/generate.go" && coupling.To == "pkg/zz_generated.go"
	}
	for _, path := range result.Topology.PotentialTests {
		foundTest = foundTest || path == "pkg/external_test.go"
	}
	if !foundGeneratorCoupling || !foundTest {
		t.Fatalf("topology omitted generator coupling or potential test: %#v", result.Topology)
	}
	components := make(map[string]string)
	for _, group := range result.Topology.ReviewGroups {
		if len(group.Paths) > 1 || !group.Split {
			t.Fatalf("review group ignored maximum size or split marker: %+v", group)
		}
		for _, path := range group.Paths {
			components[path] = group.ComponentID
		}
	}
	component := components["pkg/generate.go"]
	if component == "" || components["pkg/zz_generated.go"] != component || components["pkg/external_test.go"] != component {
		t.Fatalf("review groups did not preserve shared component identity: %v", components)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
