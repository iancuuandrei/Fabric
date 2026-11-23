package ri

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestCollectCandidateGoCorpusActualPinnedRI(t *testing.T) {
	client, identity, binding, baseInventory, baseGraph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod":          "module example.test/root\n",
		"api/a.go":        "package api\nfunc A() {}\n",
		"gone.go":         "package root\nfunc Gone() {}\n",
		"nested/go.mod":   "module example.test/nested\n",
		"nested/pkg/p.go": "package pkg\nfunc P() {}\n",
	}, "update api")
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "api", "a.go"), []byte("package api\nfunc A() {}\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(binding.Request.Path, "added"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "added", "new.go"), []byte("package added\nfunc New() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(binding.Request.Path, "gone.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "nested", "go.mod"), []byte("module example.test/nestedchanged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(binding.Request.Path, "private"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "private", "hidden.go"), []byte("package private\nfunc Hidden() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, baseGraph, baseInventory, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateID == "" || got.CandidateFilesHash != expected.FilesHash || got.Graph.CandidateID != got.CandidateID || got.Graph.ModuleInventory == nil || got.Graph.ModuleInventory.Digest != got.ModuleInventory.Digest || got.Coverage != "PARTIAL" {
		t.Fatalf("candidate corpus lost bound graph evidence: %#v", got)
	}
	if !containsPath(got.ChangedPaths, "api/a.go") || !containsPath(got.AdmittedPaths, "added/new.go") || !containsPath(got.DeletedPaths, "gone.go") {
		t.Fatalf("candidate corpus did not report modified/add/delete paths: %#v", got)
	}
	if packageBindingForPath(got.Graph, "added/new.go").ImportPath != "example.test/root/added" || hasGoDeclaration(got.Graph, "gone.go", "Gone") {
		t.Fatalf("candidate graph retained deleted source or missed candidate ownership: %#v", got.Graph.Files)
	}
	if packageBindingForPath(got.Graph, "nested/pkg/p.go").ImportPath != "example.test/nestedchanged/pkg" {
		t.Fatal("candidate module inventory did not rebind retained nested package")
	}
	for _, omission := range got.Omissions {
		if omission.Path == "private/hidden.go" {
			t.Fatal("sensitive candidate path leaked into corpus omission")
		}
	}
	if err := ValidateGoEngineeringGraph(got.Graph); err != nil {
		t.Fatal(err)
	}
}

func TestCollectCandidateGoCorpusCandidateFactsCacheRevalidatesCandidateBindings(t *testing.T) {
	client, identity, binding, inventory, graph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod": "module example.test/cache\n",
		"a.go":   "package cache\nfunc Base() {}\n",
	}, "cache candidate facts")
	cacheDir := t.TempDir()
	collect := func(content string) GoCandidateCorpus {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binding.Request.Path, "a.go"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		expected, err := worktree.Fingerprint(context.Background(), binding)
		if err != nil {
			t.Fatal(err)
		}
		corpus, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, graph, inventory, client, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		if corpus.CandidateID == "" || corpus.Graph.CandidateID != corpus.CandidateID || corpus.CandidateFilesHash != expected.FilesHash || !hasGoDeclaration(corpus.Graph, "a.go", "ChangedA") && !hasGoDeclaration(corpus.Graph, "a.go", "ChangedB") {
			t.Fatal("candidate facts cache lost current candidate graph binding")
		}
		return corpus
	}
	a := "package cache\nfunc ChangedA() {}\n"
	b := "package cache\nfunc ChangedB() {}\n"
	first := collect(a)
	second := collect(b)
	third := collect(a)
	if first.CandidateID == second.CandidateID || first.CandidateID != third.CandidateID || first.CandidateFilesHash == second.CandidateFilesHash || first.Graph.Digest == second.Graph.Digest || first.Graph.Digest != third.Graph.Digest {
		t.Fatal("A/B/A candidate facts did not rebuild candidate-bound graph identity")
	}
	if first.CandidateFactCacheHits != 0 || first.CandidateFactCacheMisses != 1 || second.CandidateFactCacheHits != 0 || second.CandidateFactCacheMisses != 1 || third.CandidateFactCacheHits != 1 || third.CandidateFactCacheMisses != 0 {
		t.Fatalf("unexpected candidate fact cache diagnostics: first=%d/%d second=%d/%d third=%d/%d", first.CandidateFactCacheHits, first.CandidateFactCacheMisses, second.CandidateFactCacheHits, second.CandidateFactCacheMisses, third.CandidateFactCacheHits, third.CandidateFactCacheMisses)
	}
	if err := ValidateGoEngineeringGraph(third.Graph); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(third)
	if err != nil || bytes.Contains(encoded, []byte("candidate_fact_cache")) {
		t.Fatal("candidate cache diagnostics entered serialized corpus identity")
	}
}

func TestReadCandidateGoCorpusFileRejectsStaleExpectedCandidate(t *testing.T) {
	_, binding := candidateModuleFixture(t, map[string]string{"go.mod": "module example.test/root\n", "a.go": "package p\n"})
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	_, states, err := worktree.Capture(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	var state worktree.FileState
	for _, item := range states {
		if item.Path == "a.go" {
			state = item
		}
	}
	if state.Path == "" {
		t.Fatal("fixture state absent")
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "a.go"), []byte("package p\n// drift\n"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := expected.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readCandidateGoCorpusFile(context.Background(), binding, expected, id, state); err == nil {
		t.Fatal("stale candidate was read")
	}
}

func TestCollectCandidateGoCorpusActualPinnedRINoOp(t *testing.T) {
	client, identity, binding, baseInventory, baseGraph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod": "module example.test/root\n",
		"a.go":   "package root\nfunc A() {}\n",
	}, "inspect root")
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, baseGraph, baseInventory, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ChangedPaths) != 0 || len(got.DeletedPaths) != 0 || got.Graph.CandidateID != got.CandidateID || got.Graph.CandidateID == "" || !hasGoDeclaration(got.Graph, "a.go", "A") {
		t.Fatalf("no-op candidate did not rebuild bound retained facts: %#v", got)
	}
}

func TestCollectCandidateGoCorpusRejectsUnsafeCacheDirectory(t *testing.T) {
	client, identity, binding, inventory, graph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod": "module example.test/cachepath\n",
		"a.go":   "package cachepath\nfunc A() {}\n",
	}, "cache path")
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, graph, inventory, client, "relative-cache"); err == nil {
		t.Fatal("relative candidate cache directory accepted")
	}
}

func TestCandidateFactsCacheDoesNotReuseModuleOwnership(t *testing.T) {
	client, identity, binding, inventory, graph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod":   "module example.test/old\n",
		"pkg/a.go": "package pkg\nfunc Base() {}\n",
	}, "module ownership")
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "pkg", "a.go"), []byte("package pkg\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()
	collect := func() GoCandidateCorpus {
		t.Helper()
		expected, err := worktree.Fingerprint(context.Background(), binding)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, graph, inventory, client, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	first := collect()
	if first.CandidateFactCacheMisses != 1 || packageBindingForPath(first.Graph, "pkg/a.go").ImportPath != "example.test/old/pkg" {
		t.Fatal("initial candidate cache collection did not bind old module ownership")
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "go.mod"), []byte("module example.test/new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := collect()
	if second.CandidateFactCacheHits != 1 || second.CandidateID == first.CandidateID || packageBindingForPath(second.Graph, "pkg/a.go").ImportPath != "example.test/new/pkg" {
		t.Fatal("cached candidate facts reused stale module ownership")
	}
}

func TestCollectCandidateGoCorpusDropsInvalidChangedBaseFacts(t *testing.T) {
	client, identity, binding, baseInventory, baseGraph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod":  "module example.test/root\n",
		"a.go":    "package p\nfunc Old() {}\n",
		"keep.go": "package p\nfunc Kept() {}\n",
	}, "inspect")
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "a.go"), []byte("package\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, baseGraph, baseInventory, client, "")
	if err != nil {
		t.Fatal(err)
	}
	if hasGoDeclaration(got.Graph, "a.go", "Old") || got.OmittedCount == 0 {
		t.Fatalf("invalid changed base retained stale facts: %#v", got)
	}
	if !hasGoDeclaration(got.Graph, "keep.go", "Kept") {
		t.Fatal("unchanged admitted source disappeared with the omitted changed file")
	}
}

func TestCollectCandidateGoCorpusRejectsAllFactsOmitted(t *testing.T) {
	client, identity, binding, inventory, graph := candidateGoCorpusTestBase(t, map[string]string{
		"go.mod":  "module example.test/empty\n",
		"only.go": "package p\nfunc Old() {}\n",
	}, "inspect")
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "only.go"), []byte("package\n"), 0600); err != nil {
		t.Fatal(err)
	}
	expected, err := worktree.Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectCandidateGoCorpus(context.Background(), identity, binding, expected, graph, inventory, client, "")
	if err == nil || got.Graph.Digest != "" || len(got.Graph.Files) != 0 {
		t.Fatal("all omitted candidate facts must not return a usable old graph")
	}
}

// candidateGoCorpusTestBase keeps the same admitted committed fixture recipe
// shared by the independent candidate mutation assertions above.
func candidateGoCorpusTestBase(t *testing.T, files map[string]string, query string) (Client, repository.Identity, worktree.Binding, GoModuleInventory, GoEngineeringGraph) {
	t.Helper()
	client := actualGoCorpusClient(t)
	identity, binding := candidateModuleFixture(t, files)
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := CollectCommittedGoCorpusWithOptions(context.Background(), identity, client, "", query, GoCorpusOptions{ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: corpus.Source.RepositoryID, ProducerSHA256: client.ExecutableHash, Files: corpus.GraphInputs, Generators: corpus.Generators, ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	return client, identity, binding, inventory, graph
}
