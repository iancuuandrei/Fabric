package ri

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestCandidateModuleFieldsOmitAndPreserveCommittedInventoryBytes(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	type legacyInventory struct {
		Version                int                     `json:"version"`
		RepositoryID           string                  `json:"repository_id"`
		Commit                 string                  `json:"commit"`
		Tree                   string                  `json:"tree"`
		Coverage               string                  `json:"coverage"`
		ObservedManifestCount  int                     `json:"observed_manifest_count"`
		TruncatedManifestCount int                     `json:"truncated_manifest_count"`
		Files                  []GoManifestObservation `json:"files"`
		Omissions              []GoManifestOmission    `json:"omissions"`
		Digest                 string                  `json:"digest"`
	}
	legacy, err := canonical.Bytes(legacyInventory{
		Version: inventory.Version, RepositoryID: inventory.RepositoryID, Commit: inventory.Commit,
		Tree: inventory.Tree, Coverage: inventory.Coverage, ObservedManifestCount: inventory.ObservedManifestCount,
		TruncatedManifestCount: inventory.TruncatedManifestCount, Files: inventory.Files,
		Omissions: inventory.Omissions, Digest: inventory.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := canonical.Bytes(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, legacy) {
		t.Fatalf("v2 optional fields changed committed v1 canonical bytes\nlegacy=%s\ncurrent=%s", legacy, current)
	}
}

func candidateModuleFixture(t *testing.T, files map[string]string) (repository.Identity, worktree.Binding) {
	t.Helper()
	identity := goModuleFixture(t, files)
	request, err := worktree.Prepare(strings.Repeat("b", 64), identity)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := worktree.Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	binding, createErr := worktree.Create(context.Background(), request)
	closeErr := lease.Close()
	if err := errors.Join(createErr, closeErr); err != nil {
		t.Fatal(err)
	}
	return identity, binding
}

func TestCandidateGoModuleInventoryBindsAddedChangedAndDeletedManifestClosure(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{
		"go.mod":             "module example.test/root\n",
		"nested/go.mod":      "module example.test/nested\n",
		"nested/pkg/file.go": "package pkg\n",
	})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(binding.Request.Path, "nested", "go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(binding.Request.Path, "added"), 0700); err != nil {
		t.Fatal(err)
	}
	addedManifest := "module example.test/added\n\ngo 1.25\n" + strings.Repeat("// bounded page fixture\n", 1700)
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "added", "go.mod"), []byte(addedManifest), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := CollectCandidateGoModuleInventory(context.Background(), identity, binding, expected, base)
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Version != GoModuleInventoryVersionCandidate || inventory.CandidateFilesHash != expected.FilesHash || inventory.BaseInventoryDigest != base.Digest || inventory.CandidateID == "" || inventory.Coverage != "complete" {
		t.Fatalf("candidate inventory header is not bound: %+v", inventory)
	}
	if err := ValidateCandidateGoModuleInventory(inventory, identity, expected, base); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoModuleInventory(inventory, identity); err == nil {
		t.Fatal("candidate inventory accepted as committed-tree evidence")
	}
	if got := goManifestByPath(t, inventory, "added/go.mod"); got.Status != "parsed" || got.ModulePath != "example.test/added" || got.Blob != "" || got.Bytes != int64(len(addedManifest)) || got.SHA256 != expectedCandidateFileHash(t, binding, expected, "added/go.mod") {
		t.Fatalf("added candidate manifest was not parsed from exact candidate bytes: %+v", got)
	}
	if _, found := candidateGoManifestByPath(inventory, "nested/go.mod"); found {
		t.Fatal("deleted candidate manifest remained in inventory")
	}
	owner, err := GoModuleOwnershipForPath(inventory, "nested/pkg/file.go")
	if err != nil || owner.Status != "declared_module" || owner.ModulePath != "example.test/root" {
		t.Fatalf("deleted nested manifest did not reveal candidate ancestor ownership: %+v, %v", owner, err)
	}
	addedOwner, err := GoModuleOwnershipForPath(inventory, "added/pkg/file.go")
	if err != nil || addedOwner.Status != "declared_module" || addedOwner.ModulePath != "example.test/added" {
		t.Fatalf("new candidate module did not own its package: %+v, %v", addedOwner, err)
	}
}

func TestCandidateGoModuleInventoryMalformedAndOmittedNestedManifestsStayAmbiguous(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n", "pkg/file.go": "package pkg\n"})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		path  string
		value string
	}{
		{name: "malformed", path: "deep/go.mod", value: "module\n"},
		{name: "sensitive", path: "private/go.mod", value: "module secret.private.example/deep\n"},
		{name: "protected", path: ".harness/go.mod", value: "module example.test/protected\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(binding.Request.Path, filepath.FromSlash(tc.path))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.value), 0600); err != nil {
				t.Fatal(err)
			}
			expected, err := worktree.Fingerprint(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := CollectCandidateGoModuleInventory(context.Background(), identity, binding, expected, base)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCandidateGoModuleInventory(inventory, identity, expected, base); err != nil {
				t.Fatal(err)
			}
			owner, err := GoModuleOwnershipForPath(inventory, "deep/pkg/file.go")
			if err != nil || owner.Status != "ambiguous" {
				t.Fatalf("incomplete or hidden candidate module fell back to parent: %+v, %v", owner, err)
			}
			if tc.name == "malformed" {
				file, ok := candidateGoManifestByPath(inventory, tc.path)
				if !ok || file.Status != "invalid" || file.ModulePath != "" || file.Blob != "" {
					t.Fatalf("malformed manifest observation is not explicit/content-bound: %+v", file)
				}
			} else {
				serialized, err := canonical.Bytes(inventory)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(serialized), tc.value) || tc.name == "sensitive" && strings.Contains(string(serialized), "private/go.mod") {
					t.Fatal("omitted candidate manifest leaked its path or declarations")
				}
			}
		})
	}
}

func TestCandidateGoModuleInventoryRejectsStaleCandidateAndTampering(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := CollectCandidateGoModuleInventory(context.Background(), identity, binding, expected, base)
	if err != nil {
		t.Fatal(err)
	}
	changed := expected
	changed.FilesHash = strings.Repeat("c", 64)
	if err := ValidateCandidateGoModuleInventory(inventory, identity, changed, base); err == nil {
		t.Fatal("candidate inventory accepted another complete candidate")
	}
	tampered := inventory
	tampered.CandidateID = strings.Repeat("d", 64)
	if err := finalizeGoModuleInventory(&tampered); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCandidateGoModuleInventory(tampered, identity, expected, base); err == nil {
		t.Fatal("candidate inventory substitution accepted")
	}
}

func TestCandidateGoModuleInventoryRejectsStaleCurrentWorktree(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "go.mod"), []byte("module example.test/changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectCandidateGoModuleInventory(context.Background(), identity, binding, expected, base); err == nil {
		t.Fatal("stale candidate was read into a module inventory")
	}
}

func candidateGoManifestByPath(inventory GoModuleInventory, path string) (GoManifestObservation, bool) {
	for _, file := range inventory.Files {
		if file.Path == path {
			return file, true
		}
	}
	return GoManifestObservation{}, false
}

func expectedCandidateFileHash(t *testing.T, binding worktree.Binding, expected worktree.Candidate, path string) string {
	t.Helper()
	chunk, err := worktree.ReadSource(context.Background(), binding, expected, path, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	return chunk.SHA256
}

func TestCandidateGoModuleInventoryRejectsCanceledCapture(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectCandidateGoModuleInventory(ctx, identity, binding, expected, base); err == nil {
		t.Fatal("canceled candidate inventory admitted")
	}
}

func TestCandidateGoModuleInventoryOversizedManifestUsesCandidateHashWithoutContent(t *testing.T) {
	identity, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n"})
	base, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	content := "module example.test/large\n" + strings.Repeat("// padded\n", int(goModuleMaxFileBytes/10)+1)
	if err := os.MkdirAll(filepath.Join(binding.Request.Path, "large"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "large", "go.mod"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := CollectCandidateGoModuleInventory(context.Background(), identity, binding, expected, base)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := candidateGoManifestByPath(inventory, "large/go.mod")
	if !ok || file.Status != "oversized" || file.Blob != "" || file.Bytes != int64(len(content)) || file.SHA256 != expectedCandidateFileHash(t, binding, expected, "large/go.mod") || file.ModulePath != "" {
		t.Fatalf("oversized candidate manifest leaked/forged evidence: %+v", file)
	}
	owner, err := GoModuleOwnershipForPath(inventory, "large/pkg/file.go")
	if err != nil || owner.Status != "ambiguous" {
		t.Fatalf("oversized nested module did not shadow its parent: %+v, %v", owner, err)
	}
}
