package diagnostics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckFileDetectsGoAndJSONSyntaxErrors(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Valid Go file (without go.mod -> instant pure go/parser pass, no slow toolchain call).
	validGo := filepath.Join(dir, "ok.go")
	if err := os.WriteFile(validGo, []byte("package demo\n\nfunc Ok() int { return 1 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if diags := CheckFile(ctx, dir, "ok.go"); len(diags) != 0 {
		t.Fatalf("expected 0 diagnostics for valid Go file, got: %+v", diags)
	}

	// Broken Go syntax must report go/parser diagnostic with line number.
	badGo := filepath.Join(dir, "bad.go")
	if err := os.WriteFile(badGo, []byte("package demo\n\nfunc Broken( {\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diags := CheckFile(ctx, dir, "bad.go")
	if len(diags) == 0 || diags[0].Source != "go/parser" || diags[0].Line != 3 {
		t.Fatalf("expected go/parser line 3 error, got: %+v", diags)
	}

	// Valid & broken JSON files.
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"ok":true}`), 0o600)
	if d := CheckFile(ctx, dir, "good.json"); len(d) != 0 {
		t.Fatalf("expected 0 diagnostics for valid JSON, got: %+v", d)
	}
	_ = os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"ok": `), 0o600)
	jDiags := CheckFile(ctx, dir, "bad.json")
	if len(jDiags) == 0 || jDiags[0].Source != "json" || !strings.Contains(strings.ToLower(jDiags[0].Message), "json") {
		t.Fatalf("expected json diagnostic, got: %+v", jDiags)
	}
}
