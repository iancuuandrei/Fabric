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

func TestRIOrientationQueryReaderIsStrictAndBounded(t *testing.T) {
	for name, raw := range map[string][]byte{
		"duplicate":    []byte(`{"objective":"Local","top_n":1,"top_n":2}`),
		"unknown":      []byte(`{"objective":"Local","top_n":1,"override":true}`),
		"array":        []byte(`[]`),
		"invalid utf8": {'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'},
		"oversized":    bytes.Repeat([]byte(" "), (32<<10)+1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "query.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readRIOrientationQuery[ri.GoEngineeringRankingQuery](path); err == nil {
				t.Fatal("invalid query accepted")
			}
		})
	}
	if _, err := readRIOrientationQuery[ri.SemanticSnapshotQuery](t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestRIOrientationRejectsInvalidSelectorsBeforeRepositoryDiscovery(t *testing.T) {
	for _, operation := range []string{"rank", "semantic"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			queryPath := filepath.Join(root, "query.json")
			if err := os.WriteFile(queryPath, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"ri", operation, "missing.exe", strings.Repeat("a", 64), "missing-spec.json", queryPath}
			if operation == "semantic" {
				args = []string{"ri", operation, "missing.exe", strings.Repeat("a", 64), "missing-snapshot", strings.Repeat("b", 64), queryPath}
			}
			var out bytes.Buffer
			err := Execute(context.Background(), args, root, &out)
			if err == nil || !strings.Contains(err.Error(), "query") || out.Len() != 0 {
				t.Fatalf("invalid selector crossed preflight: %v", err)
			}
		})
	}
}

func TestRIRankingUsesCommittedSourceWithActualRust(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires pinned Rust RI executable")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	root := t.TempDir()
	cliGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "api.go"), []byte("package api\nfunc Local() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "ranking fixture")
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &out); err != nil {
		t.Fatal(err)
	}
	spec, err := canonical.Bytes(goGraphSpec{Files: []goGraphFileSpec{{Path: "api.go", ImportPath: "example.invalid/api", ModulePath: "example.invalid/api"}}, Generators: []ri.GoGeneratorBinding{}})
	if err != nil {
		t.Fatal(err)
	}
	specPath, queryPath := filepath.Join(root, "spec.json"), filepath.Join(root, "query.json")
	if err := os.WriteFile(specPath, spec, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queryPath, []byte(`{"objective":"Local","top_n":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api.go"), []byte("package dirty\nfunc Other() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Execute(context.Background(), []string{"ri", "rank", executable, hex.EncodeToString(sum[:]), specPath, queryPath}, root, &out); err != nil {
		t.Fatal(err)
	}
	var result goRankingEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ranking.SourceID != result.Repository.RepositoryID || result.Ranking.Coverage != "PARTIAL" || len(result.Ranking.Items) != 1 || result.Ranking.Items[0].Path != "api.go" || result.Ranking.GraphDigest == "" {
		t.Fatal("ranking lost source binding or committed symbol")
	}
}
