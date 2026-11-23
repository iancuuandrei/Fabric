package ri

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
)

func goModuleFixture(t *testing.T, files map[string]string) repository.Identity {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
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
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "module fixture")
	identity, err := repository.Discover(context.Background(), root, "go-module-fixture")
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestGoModuleInventoryParsesDeclarationsAndSelectsDeepestModule(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{
		"go.mod":                    "module example.test/root\n\ngo 1.25\nrequire example.test/dep v1.2.3\nreplace example.test/dep => ./fork\n",
		"go.work":                   "go 1.25\n\nuse ./nested\nreplace example.test/dep => ./workspace-fork\n",
		"nested/go.mod":             "module example.test/nested\n\ngo 1.24\n",
		"nested/pkg/file.go":        "package pkg\n",
		"nested/vendor/modules.txt": "# example.test/vendored v1.2.0\n",
	})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventory(inventory, identity); err != nil {
		t.Fatal(err)
	}
	if inventory.Coverage != "complete" || inventory.ObservedManifestCount != 4 || len(inventory.Files) != 4 {
		t.Fatalf("unexpected inventory coverage: %#v", inventory)
	}
	root := goManifestByPath(t, inventory, "go.mod")
	if root.ModulePath != "example.test/root" || root.GoVersion != "1.25" || len(root.Requires) != 1 || root.Requires[0] != (GoModuleRequirement{Path: "example.test/dep", Version: "v1.2.3"}) || len(root.Replaces) != 1 || !root.Replaces[0].Local || root.Replaces[0].NewPath != "./fork" || root.Replaces[0].LocalRoot != "fork" || root.Replaces[0].LocalStatus != "declared_repository_path" {
		t.Fatalf("go.mod declarations not preserved: %#v", root)
	}
	work := goManifestByPath(t, inventory, "go.work")
	if len(work.Uses) != 1 || work.Uses[0] != (GoWorkspaceUse{DeclaredPath: "./nested", Root: "nested", Status: "declared_repository_path"}) || len(work.Replaces) != 1 || work.Replaces[0].NewPath != "./workspace-fork" {
		t.Fatalf("go.work declarations not preserved: %#v", work)
	}
	vendor := goManifestByPath(t, inventory, "nested/vendor/modules.txt")
	if vendor.Status != "parsed" || vendor.SHA256 == "" || vendor.Bytes == 0 || vendor.ModulePath != "" {
		t.Fatalf("vendor evidence should be presence and digest only: %#v", vendor)
	}
	owner, err := GoModuleOwnershipForPath(inventory, "nested/pkg/file.go")
	if err != nil || owner.Status != "declared_module" || owner.ModuleRoot != "nested" || owner.ModulePath != "example.test/nested" || owner.ImportPath != "example.test/nested/pkg" {
		t.Fatalf("wrong deepest module ownership: %#v, %v", owner, err)
	}
	rootOwner, err := GoModuleOwnershipForPath(inventory, "rootpkg/file.go")
	if err != nil || rootOwner.Status != "declared_module" || rootOwner.ImportPath != "example.test/root/rootpkg" {
		t.Fatalf("wrong root module ownership: %#v, %v", rootOwner, err)
	}
}

func TestGoModuleInventoryNoManifestPreservesSourceLocalFallback(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{"README.md": "no Go module manifest\n"})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Coverage != "complete" || inventory.ObservedManifestCount != 0 || len(inventory.Files) != 0 {
		t.Fatalf("unexpected empty manifest inventory: %#v", inventory)
	}
	if err := ValidateGoModuleInventory(inventory, identity); err != nil {
		t.Fatal(err)
	}
	owner, err := GoModuleOwnershipForPath(inventory, "pkg/file.go")
	if err != nil || owner.Status != "source_local_fallback" {
		t.Fatalf("missing module inventory must preserve fallback: %#v, %v", owner, err)
	}
}

func TestGoModuleInventoryOmitsSensitiveAndProtectedManifestsBeforeReading(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{
		"go.mod":                    "module example.test/root\n",
		"private/go.mod":            "module secret.private.example/private\n",
		"credentials/go.mod":        "module secret.credentials.example/private\n",
		".env.production/go.mod":    "module secret.env.example/private\n",
		".harness/go.mod":           "module example.test/protected-control\n",
		".harness/settings/go.work": "use ../secret-workspace\n",
	})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventory(inventory, identity); err != nil {
		t.Fatal(err)
	}
	if inventory.Coverage != "partial" || len(inventory.Omissions) != 5 || inventory.ObservedManifestCount != len(inventory.Files)+len(inventory.Omissions)+inventory.TruncatedManifestCount {
		t.Fatalf("sensitive/protected manifest omissions are not bounded: %#v", inventory)
	}
	for _, omission := range inventory.Omissions {
		if omission.Reason == "sensitive_path" && omission.Path != "[redacted]" {
			t.Fatalf("sensitive manifest path leaked: %#v", omission)
		}
	}
	serialized, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"private/go.mod", "credentials/go.mod", ".env.production/go.mod",
		"secret.private.example", "secret.credentials.example", "secret.env.example",
		"protected-control", "secret-workspace",
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("omitted manifest content/path leaked %q in %s", forbidden, serialized)
		}
	}
	protected := false
	for _, omission := range inventory.Omissions {
		if omission.Path == ".harness/go.mod" && omission.Reason == "protected_path" {
			protected = true
		}
	}
	if !protected {
		t.Fatalf("protected manifest was not retained as path-only omission: %#v", inventory.Omissions)
	}
	owner, err := GoModuleOwnershipForPath(inventory, "any/pkg/file.go")
	if err != nil || owner.Status != "ambiguous" || owner.Reason != "module_manifest_omitted" {
		t.Fatalf("omitted module manifest must prevent false parent ownership: %#v, %v", owner, err)
	}
}

func TestGoModuleInventoryIncompleteDeepestManifestShadowsParent(t *testing.T) {
	for name, nested := range map[string]string{
		"malformed": "module\n",
		"oversized": "//" + strings.Repeat("x", goModuleMaxFileBytes),
	} {
		t.Run(name, func(t *testing.T) {
			identity := goModuleFixture(t, map[string]string{
				"go.mod":        "module example.test/root\n",
				"deep/go.mod":   nested,
				"deep/pkg/a.go": "package pkg\n",
			})
			inventory, err := CollectGoModuleInventory(context.Background(), identity)
			if err != nil {
				t.Fatal(err)
			}
			owner, err := GoModuleOwnershipForPath(inventory, "deep/pkg/a.go")
			if err != nil || owner.Status != "ambiguous" {
				t.Fatalf("incomplete nested manifest fell back to parent: %#v, %v", owner, err)
			}
			if err := ValidateGoModuleInventory(inventory, identity); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGoModuleInventoryWorkspaceOutsideAndMalformedAreExplicit(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{
		"go.work": "go 1.25\nuse ../outside\n",
		"go.mod":  "module example.test/root\n",
	})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	work := goManifestByPath(t, inventory, "go.work")
	if work.Status != "parsed" || len(work.Uses) != 1 || work.Uses[0].Status != "external_or_invalid_path" || work.Uses[0].Root != "" {
		t.Fatalf("outside workspace path was not explicitly rejected: %#v", work)
	}
	if owner, err := GoModuleOwnershipForPath(inventory, "unrelated/a.go"); err != nil || owner.Status != "declared_module" {
		t.Fatalf("workspace declaration incorrectly changed module ownership: %#v, %v", owner, err)
	}

	badIdentity := goModuleFixture(t, map[string]string{"go.work": "use (\n", "go.mod": "module example.test/root\n"})
	badInventory, err := CollectGoModuleInventory(context.Background(), badIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if goManifestByPath(t, badInventory, "go.work").Status != "invalid" || badInventory.Coverage != "partial" {
		t.Fatalf("malformed workspace did not produce partial evidence: %#v", badInventory)
	}
}

func TestGoModuleInventoryBoundsAndTruncationPreventFalseOwnership(t *testing.T) {
	files := map[string]string{"go.mod": "module example.test/root\n"}
	for i := 0; i < goModuleMaxRecords; i++ {
		files[fmt.Sprintf("nested-%03d/go.mod", i)] = fmt.Sprintf("module example.test/nested%d\n", i)
	}
	identity := goModuleFixture(t, files)
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.TruncatedManifestCount != 1 || inventory.Coverage != "partial" {
		t.Fatalf("manifest count bound not represented: observed=%d retained=%d truncated=%d", inventory.ObservedManifestCount, len(inventory.Files), inventory.TruncatedManifestCount)
	}
	owner, err := GoModuleOwnershipForPath(inventory, "rootpkg/file.go")
	if err != nil || owner.Status != "ambiguous" || owner.Reason != "manifest_inventory_truncated" {
		t.Fatalf("truncated tree produced a confident owner: %#v, %v", owner, err)
	}
}

func TestGoModuleInventoryTotalByteBudgetMarksNestedRootAmbiguous(t *testing.T) {
	files := map[string]string{"go.mod": "module example.test/root\n"}
	content := "module example.test/nested\n//" + strings.Repeat("x", (120<<10)-len("module example.test/nested\n//"))
	for i := 0; i < 9; i++ {
		files[fmt.Sprintf("m%02d/go.mod", i)] = content
		files[fmt.Sprintf("m%02d/pkg/a.go", i)] = "package pkg\n"
	}
	identity := goModuleFixture(t, files)
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	var omitted string
	for _, file := range inventory.Files {
		if file.Status == "budget_omitted" {
			omitted = strings.TrimSuffix(file.Path, "/go.mod")
			break
		}
	}
	if omitted == "" || inventory.Coverage != "partial" {
		t.Fatalf("total byte budget did not remain explicit: %#v", inventory)
	}
	owner, err := GoModuleOwnershipForPath(inventory, omitted+"/pkg/a.go")
	if err != nil || owner.Status != "ambiguous" {
		t.Fatalf("budget-omitted nested module fell back to ancestor: %#v, %v", owner, err)
	}
}

func TestGoModuleInventoryRejectsDuplicateAndTamperedRecords(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := inventory
	duplicate.Files = append(append([]GoManifestObservation(nil), inventory.Files...), inventory.Files[0])
	duplicate.ObservedManifestCount++
	duplicate.TruncatedManifestCount = 0
	if err := finalizeGoModuleInventory(&duplicate); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventoryRecord(duplicate); err == nil {
		t.Fatal("duplicate path record accepted")
	}
	tampered := inventory
	tampered.Files = append([]GoManifestObservation(nil), inventory.Files...)
	tampered.Files[0].ModulePath = "example.test/forged"
	if err := ValidateGoModuleInventoryRecord(tampered); err == nil {
		t.Fatal("record mutation accepted without digest update")
	}
	tampered = inventory
	tampered.Files = append([]GoManifestObservation(nil), inventory.Files...)
	tampered.Files[0].Path = "../go.mod"
	if err := finalizeGoModuleInventory(&tampered); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventoryRecord(tampered); err == nil {
		t.Fatal("unsafe manifest path accepted")
	}
	tampered = inventory
	tampered.Omissions = []GoManifestOmission{{Kind: "go_mod", Path: "[redacted]", Reason: "unsafe_path"}}
	tampered.ObservedManifestCount++
	tampered.Coverage = "partial"
	if err := finalizeGoModuleInventory(&tampered); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventoryRecord(tampered); err != nil {
		t.Fatal("explicit unsafe-path omission should remain valid evidence", err)
	}
	owner, err := GoModuleOwnershipForPath(tampered, "pkg/file.go")
	if err != nil || owner.Status != "ambiguous" {
		t.Fatalf("unsafe go.mod path should block confident ownership: %#v, %v", owner, err)
	}
}

func goManifestByPath(t *testing.T, inventory GoModuleInventory, name string) GoManifestObservation {
	t.Helper()
	for _, file := range inventory.Files {
		if file.Path == name {
			return file
		}
	}
	t.Fatalf("manifest %q not found", name)
	return GoManifestObservation{}
}
