package doccheck

import (
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
