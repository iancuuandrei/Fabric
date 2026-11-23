package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/ri"
)

func TestReadGoSemanticQueryIsStrictAndBounded(t *testing.T) {
	for name, raw := range map[string][]byte{
		"duplicate field": []byte(`{"limit":1,"limit":2,"vocabulary":"path","path":"a.go"}`),
		"unknown field":   []byte(`{"limit":1,"vocabulary":"path","path":"a.go","candidate":"x"}`),
		"invalid JSON":    []byte(`{"limit":`),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "query.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGoSemanticQuery(path); err == nil {
				t.Fatal("invalid query JSON accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "oversized.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), goSemanticQueryMaxBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGoSemanticQuery(path); err == nil {
		t.Fatal("oversized query accepted")
	}
	prettyPath := filepath.Join(t.TempDir(), "pretty.json")
	if err := os.WriteFile(prettyPath, []byte("{\n  \"vocabulary\": \"path\", \"path\": \"pkg/a.go\", \"limit\": 4\n}"), 0600); err != nil {
		t.Fatal(err)
	}
	query, err := readGoSemanticQuery(prettyPath)
	if err != nil || query.Vocabulary != "path" || query.Path != "pkg/a.go" || query.Limit != 4 {
		t.Fatalf("valid bounded JSON rejected: query=%+v err=%v", query, err)
	}
}

func TestValidateGoSemanticQueryRejectsUnsupportedOrUnsafeSelectors(t *testing.T) {
	validPath := ri.SemanticQuery{Vocabulary: "path", Path: "pkg/a.go", Limit: 1}
	for name, query := range map[string]ri.SemanticQuery{
		"unsupported references":      {Vocabulary: "references", Path: "pkg/a.go", Limit: 1},
		"unknown vocabulary":          {Vocabulary: "nearby", Path: "pkg/a.go", Limit: 1},
		"unsupported symbol selector": {Vocabulary: "symbol", ImportPath: "example.invalid/pkg", Limit: 1},
		"unsafe exact path":           {Vocabulary: "path", Path: "../pkg/a.go", Limit: 1},
		"sensitive impact path":       {Vocabulary: "impact", Paths: []string{"secrets/token.go"}, Limit: 1},
		"duplicate impact path":       {Vocabulary: "impact", Paths: []string{"pkg/a.go", "pkg/a.go"}, Limit: 1},
		"zero limit":                  {Vocabulary: "path", Path: "pkg/a.go", Limit: 0},
		"too many seeds":              {Vocabulary: "impact", Paths: makeSemanticPaths(65), Limit: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateGoSemanticQuery(query); err == nil {
				t.Fatal("invalid semantic query accepted")
			}
		})
	}
	if err := validateGoSemanticQuery(validPath); err != nil {
		t.Fatalf("valid exact path query rejected: %v", err)
	}
}

func TestRIQueryReturnsBoundedPinnedGraphVocabularies(t *testing.T) {
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
	writeQueryFixtureExtras(t, root)
	commitRIModuleFixture(t, root)
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &output); err != nil {
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
	spec := goGraphSpec{
		Files: []goGraphFileSpec{
			{Path: "root.go"}, {Path: "nested/pkg/pkg.go"}, {Path: "app/use.go"},
			{Path: "pkg/generate.go"}, {Path: "pkg/generated.go"}, {Path: "pkg/external_test.go"},
		},
		Generators:      []ri.GoGeneratorBinding{generatorFixture("pkg/generate.go", "pkg/generated.go", "//go:generate gen")},
		ModuleInventory: &inventory,
	}
	specBytes, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "graph-spec.json")
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	queries := map[string]ri.SemanticQuery{
		"symbol":         {Vocabulary: "symbol", Path: "pkg/generate.go", Limit: 10},
		"symbol-bounded": {Vocabulary: "symbol", Kind: "function_declaration", Limit: 1},
		"imports":        {Vocabulary: "imports", Path: "app/use.go", Limit: 10},
		"calls":          {Vocabulary: "calls", Path: "app/use.go", Limit: 10},
		"tests":          {Vocabulary: "tests", Path: "pkg/external_test.go", Limit: 10},
		"generators":     {Vocabulary: "generators", Path: "pkg/generated.go", Limit: 10},
		"module":         {Vocabulary: "module", Path: "pkg/generate.go", Limit: 10},
		"path":           {Vocabulary: "path", Path: "pkg/generate.go", Limit: 10},
		"impact":         {Vocabulary: "impact", Paths: []string{"pkg/generate.go"}, MaxDepth: 2, Limit: 10},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			queryBytes, err := canonical.Bytes(query)
			if err != nil {
				t.Fatal(err)
			}
			queryPath := filepath.Join(root, "query-"+name+".json")
			if err := os.WriteFile(queryPath, queryBytes, 0600); err != nil {
				t.Fatal(err)
			}
			output.Reset()
			if err := Execute(context.Background(), []string{"ri", "query", executable, exeSHA, specPath, queryPath}, root, &output); err != nil {
				t.Fatal(err)
			}
			var envelope goSemanticQueryEnvelope
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Repository.RepositoryID != inventory.RepositoryID || envelope.Result.SourceID != inventory.RepositoryID || envelope.Result.GraphDigest == "" || envelope.Result.ProducerSHA256 != exeSHA || envelope.Result.Coverage != "PARTIAL" {
				t.Fatalf("query result lost graph/source/producer binding: %+v", envelope.Result)
			}
			if len(envelope.Sources) != len(spec.Files) || envelope.Result.CandidateID != "" || len(envelope.Result.Items) == 0 {
				t.Fatalf("query result is empty or not a committed base result: %+v", envelope)
			}
			switch name {
			case "symbol-bounded":
				if len(envelope.Result.Items) != 1 || !envelope.Result.Truncated {
					t.Fatalf("bounded symbol result did not report truncation: %+v", envelope.Result)
				}
			case "calls":
				for _, item := range envelope.Result.Items {
					if item.Resolution != "UNRESOLVED" {
						t.Fatalf("call evidence claimed resolution: %+v", item)
					}
				}
			case "module":
				if envelope.Result.Items[0].Resolution != "DECLARED" {
					t.Fatalf("module evidence lacks declared status: %+v", envelope.Result.Items[0])
				}
			}
		})
	}
}

func TestRIQueryRejectsInvalidSelectorBeforeParserAccess(t *testing.T) {
	root := t.TempDir()
	query := ri.SemanticQuery{Vocabulary: "references", Path: "pkg/a.go", Limit: 1}
	queryBytes, err := canonical.Bytes(query)
	if err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(root, "query.json")
	if err := os.WriteFile(queryPath, queryBytes, 0600); err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	if err := Execute(context.Background(), []string{"ri", "query", "missing-parser", strings.Repeat("0", 64), "missing-spec.json", queryPath}, root, output); err == nil || err != ri.ErrSemanticVocabularyUnsupported || output.Len() != 0 {
		t.Fatalf("unsupported graph vocabulary was not rejected before repository/parser access: err=%v output=%q", err, output.String())
	}
}

func writeQueryFixtureExtras(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"pkg/generate.go":      "package generated\n//go:generate gen\nfunc Local() {}\n",
		"pkg/generated.go":     "// Code generated by fixture; DO NOT EDIT.\npackage generated\nfunc Generated() {}\n",
		"pkg/external_test.go": "package generated_test\nimport \"example.invalid/root/pkg\"\nfunc TestLocal() { _ = pkg.Local }\n",
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

func makeSemanticPaths(count int) []string {
	paths := make([]string, count)
	for i := range paths {
		paths[i] = filepath.ToSlash(filepath.Join("pkg", "file"+strings.Repeat("a", i+1)+".go"))
	}
	return paths
}
