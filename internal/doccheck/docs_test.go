package doccheck

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func root(t *testing.T) string {
	t.Helper()
	r, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDocumentationLinksAndADRs(t *testing.T) {
	r := root(t)
	link := regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
	adr := regexp.MustCompile(`^(\d{4})-`)
	seen := map[string]bool{}
	err := filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".local" || d.Name() == "target" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := string(b)
		for _, m := range link.FindAllStringSubmatch(s, -1) {
			href := m[1]
			if strings.Contains(href, "://") || strings.HasPrefix(href, "#") {
				continue
			}
			href, _, _ = strings.Cut(href, "#")
			target := filepath.Clean(filepath.Join(filepath.Dir(p), filepath.FromSlash(href)))
			rel, err := filepath.Rel(r, target)
			if err != nil || strings.HasPrefix(rel, "..") {
				t.Errorf("%s link escapes repository: %s", p, href)
				continue
			}
			if _, err := os.Stat(target); err != nil {
				t.Errorf("%s broken link %s", p, href)
			}
		}
		if filepath.Base(filepath.Dir(p)) == "adr" {
			match := adr.FindStringSubmatch(d.Name())
			if len(match) != 2 {
				t.Errorf("invalid ADR name %s", p)
				return nil
			}
			if seen[match[1]] {
				t.Errorf("duplicate ADR %s", match[1])
			}
			seen[match[1]] = true
			if !regexp.MustCompile(`(?m)^Status: (PROPOSED|ACCEPTED|SUPERSEDED|REJECTED)$`).MatchString(s) {
				t.Errorf("invalid ADR status %s", p)
			}
			if !strings.Contains(s, "## References") || len(link.FindAllString(s, -1)) == 0 {
				t.Errorf("missing ADR references %s", p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestMermaidDiagramsHaveSafeIdentifiers(t *testing.T) {
	r := root(t)
	reserved := map[string]bool{
		"end": true, "graph": true, "subgraph": true, "direction": true,
		"class": true, "classdef": true, "style": true, "linkstyle": true,
		"click": true, "default": true, "call": true, "href": true,
		"interpolate": true, "flowchart": true,
	}
	classDefRe := regexp.MustCompile(`(?i)^\s*classDef\s+([A-Za-z0-9_-]+)`)
	subgraphRe := regexp.MustCompile(`(?i)^\s*subgraph\s+([A-Za-z0-9_-]+)`)
	nodeIDRe := regexp.MustCompile(`(?:^|-->|-->\|[^|]*\||-\.[^.]*\.->|\s)([A-Za-z_][A-Za-z0-9_-]*)(?:[\[\(\{])`)

	err := filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".local" || d.Name() == "target" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		lines := strings.Split(string(b), "\n")
		inBlock := false
		startLine := 0
		var block []string
		for i, raw := range lines {
			line := strings.TrimRight(raw, "\r")
			trimmed := strings.TrimSpace(line)
			if !inBlock && strings.EqualFold(trimmed, "```mermaid") {
				inBlock = true
				startLine = i + 1
				block = nil
				continue
			}
			if inBlock && trimmed == "```" {
				inBlock = false
				nodeIDs := map[string]bool{}
				classNames := map[string]int{}
				for j, ml := range block {
					ln := startLine + j + 1
					if m := classDefRe.FindStringSubmatch(ml); len(m) == 2 {
						name := m[1]
						if reserved[strings.ToLower(name)] {
							t.Errorf("%s:%d Mermaid classDef uses reserved keyword %q", p, ln, name)
						}
						classNames[name] = ln
					}
					if m := subgraphRe.FindStringSubmatch(ml); len(m) == 2 {
						name := m[1]
						if reserved[strings.ToLower(name)] {
							t.Errorf("%s:%d Mermaid subgraph uses reserved keyword %q", p, ln, name)
						}
					}
					for _, nm := range nodeIDRe.FindAllStringSubmatch(ml, -1) {
						id := nm[1]
						if strings.EqualFold(id, "subgraph") {
							continue
						}
						nodeIDs[id] = true
						if reserved[strings.ToLower(id)] {
							t.Errorf("%s:%d Mermaid node ID uses reserved keyword %q", p, ln, id)
						}
					}
				}
				for name, ln := range classNames {
					if nodeIDs[name] {
						t.Errorf("%s:%d Mermaid classDef %q collides with node ID of the same name", p, ln, name)
					}
				}
				continue
			}
			if inBlock {
				block = append(block, line)
			}
		}
		if inBlock {
			t.Errorf("%s:%d unterminated mermaid code fence", p, startLine)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModelAllocationCodexAccessExampleUsesSupportedAuthMode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(root(t), "docs", "guides", "model-allocation.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "For a Codex subscription route"
	_, after, ok := strings.Cut(string(raw), marker)
	if !ok {
		t.Fatal("Codex subscription example is missing")
	}
	_, after, ok = strings.Cut(after, "```json\n")
	if !ok {
		t.Fatal("Codex subscription JSON example is missing")
	}
	jsonText, _, ok := strings.Cut(after, "\n```")
	if !ok {
		t.Fatal("Codex subscription JSON example is unterminated")
	}
	var example struct {
		Profiles []struct {
			AuthMode string `json:"auth_mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(jsonText), &example); err != nil {
		t.Fatalf("decode Codex subscription example: %v", err)
	}
	if len(example.Profiles) != 1 || example.Profiles[0].AuthMode != "chatgpt-session" {
		t.Fatalf("Codex subscription example must use chatgpt-session auth mode: %+v", example.Profiles)
	}
}

func TestExportedGoAPIsHaveDocumentation(t *testing.T) {
	r := root(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(r, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			switch v := decl.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(v.Name.Name) && v.Doc == nil {
					t.Errorf("%s missing docs for %s", p, v.Name.Name)
				}
			case *ast.GenDecl:
				for _, spec := range v.Specs {
					switch item := spec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(item.Name.Name) && item.Doc == nil && v.Doc == nil {
							t.Errorf("%s missing docs for %s", p, item.Name.Name)
						}
					case *ast.ValueSpec:
						for _, name := range item.Names {
							if ast.IsExported(name.Name) && item.Doc == nil && v.Doc == nil {
								t.Errorf("%s missing docs for %s", p, name.Name)
							}
						}
					}
				}
			}
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), "doc.go")); err != nil {
			t.Errorf("missing package documentation: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
