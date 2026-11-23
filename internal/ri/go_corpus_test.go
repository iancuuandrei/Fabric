package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestSelectGoCorpusCandidatesIsDeterministicAndPrioritizesRelatedTests(t *testing.T) {
	candidates := make([]goCorpusCandidate, 0, 40)
	for index := 0; index < 40; index++ {
		name := filepath.ToSlash(filepath.Join("pkg", "other", fmt.Sprintf("%03d.go", index)))
		candidates = append(candidates, goCorpusCandidate{entry: repository.SourceEntry{Path: name}, score: 0})
	}
	candidates = append(candidates,
		goCorpusCandidate{entry: repository.SourceEntry{Path: "pkg/target.go"}, score: 3},
		goCorpusCandidate{entry: repository.SourceEntry{Path: "pkg/target_test.go"}, score: 0},
		goCorpusCandidate{entry: repository.SourceEntry{Path: "pkg/caller.go"}, score: 0},
	)
	first := selectGoCorpusCandidates(candidates)
	second := selectGoCorpusCandidates(append([]goCorpusCandidate(nil), candidates...))
	if len(first) != goCorpusMaxFiles || len(second) != len(first) {
		t.Fatalf("unexpected selected size: %d / %d", len(first), len(second))
	}
	paths := make([]string, len(first))
	for i := range first {
		paths[i] = first[i].entry.Path
		if first[i] != second[i] {
			t.Fatalf("selection is unstable at %d: %+v != %+v", i, first[i], second[i])
		}
	}
	seen := map[string]bool{}
	for _, path := range paths {
		seen[path] = true
	}
	if !seen["pkg/target.go"] || !seen["pkg/target_test.go"] || !seen["pkg/caller.go"] {
		t.Fatalf("same-directory implementation/test/caller were not retained: %v", paths)
	}
}

func TestGoCorpusTermsAndScoringAreDeterministic(t *testing.T) {
	terms := goCorpusTerms("Generator helper GENERATOR, API!")
	if strings.Join(terms, ",") != "api,generator,helper" {
		t.Fatalf("unexpected normalized terms: %v", terms)
	}
	if got := goCorpusPathScore("internal/generator/helper.go", terms); got != 3 {
		t.Fatalf("unexpected objective path score: %d", got)
	}
}

func TestReceiverAwareCorpusSelectionPrefersExactReceiverDeclaration(t *testing.T) {
	targets := goCorpusReceiverTargets("Preserve Logger.WithValues behavior")
	if len(targets) != 1 || targets[0] != (goCorpusReceiverTarget{receiver: "Logger", method: "WithValues"}) {
		t.Fatalf("unexpected receiver target extraction: %#v", targets)
	}
	root := []byte("package logr\ntype Logger struct{}\nfunc (Logger) WithValues(values ...any) Logger { return Logger{} }\n")
	decoy := []byte("package examples\ntype tabLogger struct{}\nfunc (tabLogger) WithValues(values ...any) {}\nfunc ExampleLogger() { tabLogger{}.WithValues() }\n")
	if got, want := goCorpusReceiverAwareRank(root, targets), goCorpusReceiverAwareRank(decoy, targets); !got.betterThan(want) {
		t.Fatalf("exact receiver declaration did not beat example/other receiver: root=%+v decoy=%+v", got, want)
	}
	var repeated strings.Builder
	repeated.WriteString("package examples\n")
	for index := 0; index < 32; index++ {
		repeated.WriteString(fmt.Sprintf("type Other%d struct{}\nfunc (Other%d) WithValues(values ...any) {}\n", index, index))
	}
	if got, want := goCorpusReceiverAwareRank(root, targets), goCorpusReceiverAwareRank([]byte(repeated.String()), targets); !got.betterThan(want) {
		t.Fatalf("repeated unrelated receiver methods outranked exact declaration: root=%+v unrelated=%+v", got, want)
	}
	generic := []byte("package logr\ntype Logger[T any] struct{}\nfunc (l *Logger[T]) WithValues(values ...any) Logger[T] { return *l }\n")
	if got, want := goCorpusReceiverAwareRank(generic, targets), goCorpusReceiverAwareRank(decoy, targets); !got.betterThan(want) || !got.exact {
		t.Fatalf("generic pointer receiver was not ranked as exact: generic=%+v decoy=%+v", got, want)
	}
}

func TestReceiverAwareCorpusAdmitsRootDeclarationBeyondPathBudget(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	files := make(map[string][]byte, goCorpusMaxFiles+2)
	for index := 0; index < goCorpusMaxFiles+1; index++ {
		files[fmt.Sprintf("examples/logger_%02d.go", index)] = []byte(fmt.Sprintf("package examples\ntype Logger%d struct{}\nfunc (Logger%d) WithValues(values ...any) {}\n", index, index))
	}
	files["logr.go"] = []byte("package logr\ntype Logger struct{}\nfunc (Logger) WithValues(values ...any) Logger { return Logger{} }\n")
	initGoCorpusGit(t, root, files)
	identity, err := repository.Discover(context.Background(), root, "receiver-aware-corpus")
	if err != nil {
		t.Fatal(err)
	}
	objective := "Preserve Logger.WithValues behavior"
	legacy, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", objective)
	if err != nil {
		t.Fatal(err)
	}
	if corpusHasPath(legacy, "logr.go") {
		t.Fatal("legacy path-only corpus unexpectedly admitted root declaration")
	}
	updated, err := CollectCommittedGoCorpusWithOptions(context.Background(), identity, client, "", objective, GoCorpusOptions{SelectionVersion: GoCorpusSelectionReceiverAwareV2})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AttemptedFiles != goCorpusMaxFiles+2 || updated.ReadFiles != goCorpusMaxFiles || !corpusHasPath(updated, "logr.go") {
		t.Fatalf("receiver-aware corpus did not retain bounded root declaration: attempted=%d read=%d root=%t", updated.AttemptedFiles, updated.ReadFiles, corpusHasPath(updated, "logr.go"))
	}
	if !corpusHasOmission(updated, "examples/logger_", "declaration_file_budget") {
		t.Fatal("receiver-aware final corpus omission was not explicit")
	}
}

// TestReceiverAwareCorpusSelectsConfiguredCommittedRootAPI is an offline
// qualification hook for a public pinned repository. It does not read a
// working-tree overlay or call a provider.
func TestReceiverAwareCorpusSelectsConfiguredCommittedRootAPI(t *testing.T) {
	root := os.Getenv("ENGORCH_RECEIVER_AWARE_REPOSITORY")
	if root == "" {
		t.Skip("ENGORCH_RECEIVER_AWARE_REPOSITORY is not configured")
	}
	client := actualGoCorpusClient(t)
	identity, err := repository.Discover(context.Background(), root, "receiver-aware-qualified")
	if err != nil {
		t.Fatal(err)
	}
	if expected := os.Getenv("ENGORCH_RECEIVER_AWARE_COMMIT"); expected != "" && identity.Commit != expected {
		t.Fatalf("configured repository commit mismatch: got=%s", identity.Commit)
	}
	corpus, err := CollectCommittedGoCorpusWithOptions(context.Background(), identity, client, "", "Preserve Logger.WithValues behavior", GoCorpusOptions{SelectionVersion: GoCorpusSelectionReceiverAwareV2})
	if err != nil {
		t.Fatal(err)
	}
	if corpus.Unavailable != "" || !corpusHasPath(corpus, "logr.go") {
		t.Fatalf("receiver-aware corpus did not retain the configured root API: unavailable=%q read=%d", corpus.Unavailable, corpus.ReadFiles)
	}
	found := false
	for _, input := range corpus.GraphInputs {
		if input.Facts.Path != "logr.go" {
			continue
		}
		for _, declaration := range input.Facts.Declarations {
			found = found || declaration.Name == "WithValues" && declaration.Kind == "method_declaration"
		}
	}
	if !found {
		t.Fatal("configured root API declaration was absent from receiver-aware graph input")
	}
}

func corpusHasPath(corpus GoCommittedCorpus, path string) bool {
	for _, source := range corpus.Sources {
		if source.Path == path {
			return true
		}
	}
	return false
}

func corpusHasOmission(corpus GoCommittedCorpus, prefix, reason string) bool {
	for _, omission := range corpus.Omissions {
		if strings.HasPrefix(omission.Path, prefix) && omission.Reason == reason {
			return true
		}
	}
	return false
}

func TestGoCorpusGraphBudgetAdmitsSmallCandidateAndRejectsOversizedExtension(t *testing.T) {
	producer := strings.Repeat("a", 64)
	sourceID := strings.Repeat("b", 64)
	smallSource := []byte("package p\nfunc Small() {}\n")
	smallFacts := makeCorpusTestFacts(t, "pkg/small.go", smallSource, producer, nil)
	smallPackage, err := SourceLocalGoPackageBinding(sourceID, "pkg/small.go", "p")
	if err != nil {
		t.Fatal(err)
	}
	small := GoGraphFileInput{Facts: smallFacts, Source: smallSource, Package: smallPackage}
	if fits, err := goCorpusGraphFits(sourceID, producer, []GoGraphFileInput{small}); err != nil || !fits {
		t.Fatalf("small graph candidate rejected: fits=%t err=%v", fits, err)
	}

	var source strings.Builder
	source.WriteString("package p\n")
	declarations := make([]GoSymbol, 0, 1700)
	for index := 0; index < 1700; index++ {
		name := fmt.Sprintf("Function%04d", index)
		start := source.Len() + len("func ")
		source.WriteString("func " + name + "() {}\n")
		declarations = append(declarations, GoSymbol{Name: name, Kind: "function_declaration", Range: GoRange{StartByte: start, EndByte: start + len(name)}})
	}
	largeSource := []byte(source.String())
	largeFacts := makeCorpusTestFacts(t, "pkg/large.go", largeSource, producer, declarations)
	largePackage, err := SourceLocalGoPackageBinding(sourceID, "pkg/large.go", "p")
	if err != nil {
		t.Fatal(err)
	}
	large := GoGraphFileInput{Facts: largeFacts, Source: largeSource, Package: largePackage}
	if fits, err := goCorpusGraphFits(sourceID, producer, []GoGraphFileInput{small, large}); err != nil {
		t.Fatal(err)
	} else if fits {
		t.Fatal("graph exceeding the 512 KiB admission cap was accepted")
	}
	if len(largeSource) > goCorpusMaxTotalBytes {
		t.Fatal("oversize graph fixture exceeded the source corpus ceiling")
	}
}

func makeCorpusTestFacts(t *testing.T, path string, source []byte, producer string, declarations []GoSymbol) GoFileFacts {
	t.Helper()
	sourceHash := sha256.Sum256(source)
	sourceSHA := hex.EncodeToString(sourceHash[:])
	facts := fixtureGoFacts(path, sourceSHA, producer)
	facts.Declarations = declarations
	facts.Imports = []GoImport{}
	facts.Calls = []GoCall{}
	facts.GeneratedMarkers = []string{}
	facts.BodySHA256 = ""
	facts.Cache = ""
	facts.ParseCount = 0
	body, err := canonical.Bytes(facts)
	if err != nil {
		t.Fatal(err)
	}
	bodyHash := sha256.Sum256(body)
	facts.BodySHA256 = hex.EncodeToString(bodyHash[:])
	facts.Cache = "miss"
	facts.ParseCount = 1
	return facts
}

func TestCollectCommittedGoCorpusUsesCommittedSourceAndSourceLocalPackages(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	initGoCorpusGit(t, root, map[string][]byte{
		"alpha/alpha.go":            []byte("package alpha\n//go:generate go run ./cmd/gen\nfunc Helper() {}\n"),
		"alpha/alpha_test.go":       []byte("package alpha\nimport \"testing\"\nfunc TestHelper(t *testing.T) { Helper() }\n"),
		"alpha/external_test.go":    []byte("package alpha_test\nimport \"testing\"\nfunc TestExternal(t *testing.T) { _ = testing.Short() }\n"),
		"cmd/gen/main.go":           []byte("// Code generated by fixture. DO NOT EDIT.\npackage main\nfunc main() {}\n"),
		"secrets/token.go":          []byte("package token\nvar Token = \"committed-secret\"\n"),
		".github/workflows/tool.go": []byte("package tool\n"),
	})
	identity, err := repository.Discover(context.Background(), root, "go-corpus-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha", "alpha.go"), []byte("package dirty\nfunc Changed() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	corpus, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", "alpha helper generator")
	if err != nil {
		t.Fatal(err)
	}
	if corpus.Source.RepositoryID == "" || corpus.Source.Commit != identity.Commit || corpus.ReadFiles != 4 || corpus.AttemptedFiles != 4 || corpus.InventoryFiles != 6 || corpus.OmittedCount != 2 || corpus.Unavailable != "" {
		t.Fatalf("unexpected corpus accounting: %+v", corpus)
	}
	if len(corpus.Sources) != 4 || len(corpus.GraphInputs) != 4 || len(corpus.ContextFiles) != 4 || len(corpus.Generators) != 0 {
		t.Fatalf("unexpected corpus data sizes or inferred generator authority: sources=%d graph=%d context=%d generators=%d", len(corpus.Sources), len(corpus.GraphInputs), len(corpus.ContextFiles), len(corpus.Generators))
	}
	byPath := make(map[string]GoGraphFileInput, len(corpus.GraphInputs))
	contextByPath := make(map[string]string, len(corpus.ContextFiles))
	for i, source := range corpus.Sources {
		if source.RepositoryID != corpus.Source.RepositoryID || source.Commit != identity.Commit || source.Path != corpus.GraphInputs[i].Facts.Path || source.Blob == "" || source.SHA256 == "" {
			t.Fatalf("source digest is not bound to graph input: %+v / %+v", source, corpus.GraphInputs[i].Facts)
		}
		byPath[source.Path] = corpus.GraphInputs[i]
	}
	for _, file := range corpus.ContextFiles {
		contextByPath[file.Path] = string(file.Content)
	}
	if got := contextByPath["alpha/alpha.go"]; !strings.Contains(got, "func Helper()") || strings.Contains(got, "func Changed()") {
		t.Fatalf("corpus used worktree rather than committed source: %q", got)
	}
	base := byPath["alpha/alpha.go"].Package
	internal := byPath["alpha/alpha_test.go"].Package
	external := byPath["alpha/external_test.go"].Package
	if base.IdentityKind != goCorpusSourceLocalV1 || base.ImportPath != "" || base.PackageName != "alpha" || internal.PackageIdentity != base.PackageIdentity || internal.TestOfPackageIdentity != "" {
		t.Fatalf("production/internal-test package binding mismatch: base=%+v internal=%+v", base, internal)
	}
	if external.PackageName != "alpha_test" || external.PackageIdentity == base.PackageIdentity || external.TestOfPackageIdentity != base.PackageIdentity {
		t.Fatalf("external-test package identity is not distinct and linked: %+v base=%+v", external, base)
	}
	if len(byPath["alpha/alpha.go"].Facts.GeneratedMarkers) != 1 || !strings.Contains(byPath["alpha/alpha.go"].Facts.GeneratedMarkers[0], "//go:generate") {
		t.Fatal("go:generate directive observation was not retained")
	}
	if len(byPath["cmd/gen/main.go"].Facts.GeneratedMarkers) != 1 {
		t.Fatal("generated-file marker observation was not retained")
	}
}

func TestCollectCommittedGoCorpusOmitsOversizedAndNonUTF8WithoutTruncating(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	large := []byte("package large\n//" + strings.Repeat("x", goCorpusMaxFileBytes))
	requestBound := []byte("package request\n//" + strings.Repeat("x", goCorpusMaxFileBytes-len("package request\n//")))
	initGoCorpusGit(t, root, map[string][]byte{
		"a_large.go":   large,
		"b_binary.go":  append([]byte("package binary\n"), 0xff),
		"b_request.go": requestBound,
		"c_small.go":   []byte("package small\nfunc F() {}\n"),
	})
	identity, err := repository.Discover(context.Background(), root, "go-corpus-limits")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", "small")
	if err != nil {
		t.Fatal(err)
	}
	if corpus.ReadFiles != 1 || corpus.AttemptedFiles != 4 || corpus.OmittedCount != 3 || len(corpus.GraphInputs) != 1 || corpus.GraphInputs[0].Facts.Path != "c_small.go" {
		t.Fatalf("oversized/binary inputs were not omitted whole: %+v", corpus)
	}
	if string(corpus.ContextFiles[0].Content) != "package small\nfunc F() {}\n" {
		t.Fatalf("small committed source content changed: %q", corpus.ContextFiles[0].Content)
	}
}

func TestCollectCommittedGoCorpusEnforcesAggregateEightMiBSourceBound(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	const fileBytes = 512 << 10
	files := make(map[string][]byte, 17)
	content := []byte("package p\n//" + strings.Repeat("x", fileBytes-len("package p\n//")))
	if len(content) != fileBytes {
		t.Fatalf("fixture size is %d, want %d", len(content), fileBytes)
	}
	for index := 0; index < 17; index++ {
		files[fmt.Sprintf("pkg%02d/file.go", index)] = append([]byte(nil), content...)
	}
	initGoCorpusGit(t, root, files)
	identity, err := repository.Discover(context.Background(), root, "go-corpus-total-limit")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", "package p")
	if err != nil {
		t.Fatal(err)
	}
	if corpus.AttemptedFiles != 17 || corpus.ReadFiles != 16 || corpus.OmittedCount != 1 || len(corpus.GraphInputs) != 16 {
		t.Fatalf("aggregate byte ceiling was not applied to whole files: attempted=%d read=%d omitted=%d graph=%d", corpus.AttemptedFiles, corpus.ReadFiles, corpus.OmittedCount, len(corpus.GraphInputs))
	}
	for _, input := range corpus.GraphInputs {
		if len(input.Source) != fileBytes {
			t.Fatalf("corpus truncated a source file: %s bytes=%d", input.Facts.Path, len(input.Source))
		}
	}
}

func TestCollectCommittedGoCorpusReportsUnavailableWhenAllGoPathsAreIneligible(t *testing.T) {
	client := actualGoCorpusClient(t)
	root := t.TempDir()
	initGoCorpusGit(t, root, map[string][]byte{
		"secrets/token.go":          []byte("package token\n"),
		".github/workflows/tool.go": []byte("package tool\n"),
	})
	identity, err := repository.Discover(context.Background(), root, "go-corpus-unavailable")
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := CollectCommittedGoCorpus(context.Background(), identity, client, "", "inspect Go")
	if err != nil {
		t.Fatal(err)
	}
	if corpus.InventoryFiles != 2 || corpus.AttemptedFiles != 0 || corpus.ReadFiles != 0 || corpus.Unavailable != goCorpusNoEligibleCode || corpus.OmittedCount != 2 {
		t.Fatalf("ineligible corpus did not return explicit unavailable result: %+v", corpus)
	}
	for _, omission := range corpus.Omissions {
		if strings.Contains(omission.Path, "secret") {
			t.Fatalf("sensitive path leaked in omission: %+v", omission)
		}
	}
}

func actualGoCorpusClient(t *testing.T) Client {
	t.Helper()
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("set ENGORCH_RI_BINARY to built Rust RI")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	return Client{Executable: executable, ExecutableHash: hex.EncodeToString(hash[:])}
}

func initGoCorpusGit(t *testing.T, root string, files map[string][]byte) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q")
	for name, source := range files {
		fullPath := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "committed Go corpus fixture")
}
