package codegraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildImpactAndReport(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.24\n"), 0o600)
	_ = os.MkdirAll(filepath.Join(dir, "pkg", "core"), 0o700)
	_ = os.MkdirAll(filepath.Join(dir, "cmd", "app"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "pkg", "core", "calc.go"), []byte("package core\n\nfunc ComputeTotal(x int) int { return x * 2 }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "cmd", "app", "main.go"), []byte("package main\n\nimport \"example.com/demo/pkg/core\"\n\nfunc main() { _ = core.ComputeTotal(5) }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte("# Arch\nCore lives in `pkg/core/calc.go`.\n"), 0o600)

	g, err := Build(dir)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if g.FilesCount != 3 {
		t.Fatalf("expected 3 indexed files, got %d", g.FilesCount)
	}
	if len(g.Edges) < 3 {
		t.Fatalf("expected DEFINES + IMPORTS + CALLS + DOCUMENTS edges, got %d", len(g.Edges))
	}

	imp := g.Impact("ComputeTotal", 2)
	if len(imp.MatchedIDs) == 0 || len(imp.Callers) == 0 {
		t.Fatalf("expected ComputeTotal callers in impact report: %+v", imp)
	}

	fileImp := g.Impact("pkg/core/calc.go", 2)
	if len(fileImp.Docs) == 0 {
		t.Fatalf("expected ARCHITECTURE.md linked as doc for calc.go: %+v", fileImp)
	}

	md := g.ReportMarkdown()
	if !strings.Contains(md, "Top Hub Nodes") || !strings.Contains(md, "calc.go") {
		t.Fatalf("unexpected report markdown:\n%s", md)
	}
	if _, _, err := g.Save(dir); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
}
