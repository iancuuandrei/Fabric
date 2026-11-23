package control

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

const (
	observedFactsSchema = "engorch.go-file-facts.v1"
	observedParser      = "tree-sitter-go-0.25.0"
)

// observedFixtureIdentity returns a minimal valid repository identity for
// pure derivation tests. Root/CommonDir are portable absolute paths under
// t.TempDir() (never read) so filepath.IsAbs holds on Windows and Unix.
func observedFixtureIdentity(t *testing.T) repository.Identity {
	t.Helper()
	root := t.TempDir()
	return repository.Identity{
		Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"),
		ObjectFormat: "sha256", Commit: strings.Repeat("a", 64), Tree: strings.Repeat("b", 64),
	}
}

func observedGraphInput(path, source string, pkg ri.GoPackageBinding, markers []string, producer string) ri.GoGraphFileInput {
	content := []byte(source)
	sum := sha256.Sum256(content)
	facts := ri.GoFileFacts{
		Schema: observedFactsSchema, Language: "go", ParserVersion: observedParser,
		Path: path, SourceSHA256: hex.EncodeToString(sum[:]), ProducerSHA256: producer,
		Coverage: "PARTIAL", GeneratedMarkers: markers, Cache: "miss", ParseCount: 1,
	}
	key, err := canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{
		"schema": observedFactsSchema, "language": "go", "parser": observedParser,
		"path": path, "source_sha256": facts.SourceSHA256, "producer_sha256": producer,
	})
	if err != nil {
		panic(err)
	}
	facts.CacheKey = key
	// The body digest mirrors internal/ri/go_facts.go: Cache and ParseCount
	// stay actual response fields (miss/1) but hash as zero values.
	body := facts
	body.BodySHA256 = ""
	body.Cache = ""
	body.ParseCount = 0
	bodyBytes, err := canonical.Bytes(body)
	if err != nil {
		panic(err)
	}
	bodySum := sha256.Sum256(bodyBytes)
	facts.BodySHA256 = hex.EncodeToString(bodySum[:])
	return ri.GoGraphFileInput{Facts: facts, Source: content, Package: pkg}
}

func observedFixtureSources(identity repository.Identity, sourceID string, files map[string]string) []repository.SourceDigest {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	out := make([]repository.SourceDigest, 0, len(paths))
	for _, p := range paths {
		sum := sha256.Sum256([]byte(files[p]))
		// Deterministic fixture-only blob identifier: valid lower hex of the
		// exact commit length. Fixture blobs never claim real Git proof;
		// genuine blob identities come from repository.Discover in the
		// integration test, which stays untouched.
		blobSum := sha256.Sum256([]byte("fixture-blob:" + p))
		out = append(out, repository.SourceDigest{
			RepositoryID: sourceID, Commit: identity.Commit, Path: p,
			Blob:  hex.EncodeToString(blobSum[:]),
			Bytes: int64(len(files[p])), SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return out
}

func observedTasksByWrite(tasks []engineeringplan.Task) map[string]engineeringplan.Task {
	byWrite := make(map[string]engineeringplan.Task, len(tasks))
	for _, task := range tasks {
		byWrite[task.WritePaths[0]] = task
	}
	return byWrite
}

func observedSamePackagePair() (map[string]string, map[string]ri.GoPackageBinding) {
	files := map[string]string{
		"pkg/a.go": "package pkg\nfunc A() {}\n",
		"pkg/b.go": "package pkg\nfunc B() {}\n",
	}
	pkgs := map[string]ri.GoPackageBinding{
		"pkg/a.go": {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"pkg/b.go": {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
	}
	return files, pkgs
}

func observedSourceByPath(sources []repository.SourceDigest) map[string]repository.SourceDigest {
	byPath := make(map[string]repository.SourceDigest, len(sources))
	for _, source := range sources {
		byPath[source.Path] = source
	}
	return byPath
}

func observedSeedDigest(t *testing.T, sourceID string, seedPaths []string) string {
	t.Helper()
	seedDigest, err := canonical.Hash("harness.ri.go-generation.seeds.v1", struct {
		SourceID string   `json:"source_id"`
		Recipe   string   `json:"recipe"`
		Seeds    []string `json:"seeds"`
	}{sourceID, "sibling-go-v1", seedPaths})
	if err != nil {
		t.Fatal(err)
	}
	return seedDigest
}

func observedSealGenerationMetadata(t *testing.T, metadata *ri.GoGenerationMetadata) {
	t.Helper()
	body := *metadata
	body.Digest = ""
	encoded, err := canonical.Bytes(body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	metadata.Digest = hex.EncodeToString(sum[:])
}

func observedFixtureSnapshot(t *testing.T, files map[string]string, pkgs map[string]ri.GoPackageBinding, markers map[string][]string, generators []ri.GoGeneratorBinding, metadata *ri.GoGenerationMetadata) (Snapshot, []engineeringplan.Task) {
	t.Helper()
	return observedFixtureSnapshotWithIdentity(t, observedFixtureIdentity(t), files, pkgs, markers, generators, metadata)
}

func observedFixtureSnapshotWithIdentity(t *testing.T, identity repository.Identity, files map[string]string, pkgs map[string]ri.GoPackageBinding, markers map[string][]string, generators []ri.GoGeneratorBinding, metadata *ri.GoGenerationMetadata) (Snapshot, []engineeringplan.Task) {
	t.Helper()
	return observedFixtureSnapshotWithInventory(t, identity, files, pkgs, markers, generators, metadata, nil)
}

func observedFixtureSnapshotWithInventory(t *testing.T, identity repository.Identity, files map[string]string, pkgs map[string]ri.GoPackageBinding, markers map[string][]string, generators []ri.GoGeneratorBinding, metadata *ri.GoGenerationMetadata, inventory *ri.GoModuleInventory) (Snapshot, []engineeringplan.Task) {
	t.Helper()
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	producer := strings.Repeat("c", 64)
	inputs := make([]ri.GoGraphFileInput, 0, len(files))
	for path, content := range files {
		inputs = append(inputs, observedGraphInput(path, content, pkgs[path], markers[path], producer))
	}
	for i := 0; i < len(inputs); i++ {
		for j := i + 1; j < len(inputs); j++ {
			if inputs[j].Facts.Path < inputs[i].Facts.Path {
				inputs[i], inputs[j] = inputs[j], inputs[i]
			}
		}
	}
	graph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: inputs, Generators: generators, ModuleInventory: inventory})
	if err != nil {
		t.Fatalf("fixture graph rejected: %v", err)
	}
	sources := observedFixtureSources(identity, sourceID, files)
	rec := PlannerGoContextRecord{
		Version: 2, Source: ri.Source{RepositoryID: sourceID, ObjectFormat: "sha256", Commit: identity.Commit, Tree: identity.Tree},
		Query: "fixture", QueryHash: strings.Repeat("d", 64), QueryLen: len("fixture"),
		RIExecutableSHA256: producer, InventoryFiles: len(files), AttemptedFiles: len(files), ReadFiles: len(files),
		OmittedCount: 0, Sources: sources, Graph: &graph,
		GenerationMetadata: metadata,
	}
	rec.RecordID, err = plannerGoContextRecordID(rec)
	if err != nil {
		t.Fatal(err)
	}
	exec := &ExecutionPolicy{
		Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 2,
		PlannerContext:             plannerContextGoSourceV1,
		PlannerContextRIExecutable: "/bin/ri", PlannerContextRIExecutableSHA256: producer,
	}
	s := Snapshot{Creation: Creation{Version: 1, Nonce: "fixture", Repository: identity, Objective: "fixture", Execution: exec}, PlannerGoContext: &rec}
	ready := make([]engineeringplan.Task, 0, len(files))
	for path := range files {
		id := "impl-" + strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(path, ".go"), "/", "-"), "_", "-")
		ready = append(ready, engineeringplan.Task{ID: id, Kind: engineeringplan.Implementation, Title: "Task " + id, ScopePaths: []string{"."}, WritePaths: []string{path}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "output"}}, EstimatedSeconds: 10})
	}
	for i := 0; i < len(ready); i++ {
		for j := i + 1; j < len(ready); j++ {
			if ready[j].ID < ready[i].ID {
				ready[i], ready[j] = ready[j], ready[i]
			}
		}
	}
	return s, ready
}

func TestDeriveObservedC2SamePackage(t *testing.T) {
	files := map[string]string{
		"pkg/a.go":   "package pkg\nfunc A() {}\n",
		"pkg/b.go":   "package pkg\nfunc B() {}\n",
		"other/c.go": "package other\nfunc C() {}\n",
	}
	pkgs := map[string]ri.GoPackageBinding{
		"pkg/a.go":   {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"pkg/b.go":   {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"other/c.go": {ImportPath: "example.com/m/other", ModulePath: "example.com/m"},
	}
	s, ready := observedFixtureSnapshot(t, files, pkgs, nil, nil, nil)
	byWrite := observedTasksByWrite(ready)
	subset := []engineeringplan.Task{byWrite["pkg/a.go"], byWrite["pkg/b.go"], byWrite["other/c.go"]}
	subset[0].ID, subset[1].ID, subset[2].ID = "impl-a", "impl-b", "impl-c"
	observed, haveRI, err := deriveObservedCouplings(s, subset)
	if err != nil || !haveRI {
		t.Fatalf("C2 derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(observed) != 1 || observed[0].Level != engineeringplan.CouplingC2 || observed[0].Provenance != engineeringplan.CouplingProvenanceTopologyObserved {
		t.Fatalf("expected one C2 same-package pair, got %+v", observed)
	}
	if observed[0].From != "impl-a" || observed[0].To != "impl-b" {
		t.Fatalf("C2 pair endpoints wrong: %+v", observed[0])
	}
	reversed := []engineeringplan.Task{subset[2], subset[1], subset[0]}
	again, _, err := deriveObservedCouplings(s, reversed)
	if err != nil || len(again) != 1 || again[0] != observed[0] {
		t.Fatalf("derivation not deterministic: %+v vs %+v err=%v", observed, again, err)
	}
}

func TestDeriveObservedC4GeneratorFamily(t *testing.T) {
	directive := "//go:generate go run ./cmd/gen -file=output.go"
	files := map[string]string{
		"gen/tool.go":   "package gen\n" + directive + "\nfunc Tool() {}\n",
		"gen/output.go": "// Code generated by command. DO NOT EDIT.\npackage gen\n",
		"pkg/a.go":      "package a\nfunc A() {}\n",
	}
	pkgs := map[string]ri.GoPackageBinding{
		"gen/tool.go":   {ImportPath: "example.com/m/gen", ModulePath: "example.com/m"},
		"gen/output.go": {ImportPath: "example.com/m/other", ModulePath: "example.com/m"},
		"pkg/a.go":      {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
	}
	markers := map[string][]string{
		"gen/tool.go":   {directive},
		"gen/output.go": {"// Code generated by command. DO NOT EDIT."},
	}
	identity := observedFixtureIdentity(t)
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	sources := observedFixtureSources(identity, sourceID, files)
	byPath := observedSourceByPath(sources)
	directiveRange := ri.GoRange{StartByte: 13, EndByte: 13 + len(directive)}
	seedPaths := []string{"gen/output.go", "gen/tool.go", "pkg/a.go"}
	seedDigest := observedSeedDigest(t, sourceID, seedPaths)
	metadata := &ri.GoGenerationMetadata{
		Schema: "engorch.ri.go-generation.v1", Version: 1, SourceID: sourceID,
		SeedPaths: seedPaths, SeedDigest: seedDigest, Coverage: "PARTIAL",
		Bindings: []ri.GoGenerationBinding{{
			GeneratorPath: "gen/tool.go", GeneratedPath: "gen/output.go", Directive: directive,
			DirectiveRange: directiveRange, Command: "go",
			DirectiveSource: byPath["gen/tool.go"], OutputSource: byPath["gen/output.go"],
		}},
		Sources: sources, Omissions: make([]taskcontext.Omission, 0),
		InventoryFiles: len(sources), AttemptedDirectiveFiles: 1,
	}
	observedSealGenerationMetadata(t, metadata)
	if err := ri.ValidateGoGenerationMetadata(*metadata); err != nil {
		t.Fatalf("fixture metadata invalid: %v", err)
	}
	s, ready := observedFixtureSnapshotWithIdentity(t, identity, files, pkgs, markers, []ri.GoGeneratorBinding{{GeneratorPath: "gen/tool.go", GeneratedPath: "gen/output.go", Directive: directive}}, metadata)
	byWrite := observedTasksByWrite(ready)
	subset := []engineeringplan.Task{byWrite["gen/tool.go"], byWrite["gen/output.go"], byWrite["pkg/a.go"]}
	subset[0].ID, subset[1].ID, subset[2].ID = "impl-gen", "impl-out", "impl-other"
	observed, haveRI, err := deriveObservedCouplings(s, subset)
	if err != nil || !haveRI {
		t.Fatalf("C4 derivation failed: %v haveRI=%v", err, haveRI)
	}
	foundC4 := false
	for _, c := range observed {
		if c.Level == engineeringplan.CouplingC3 {
			t.Fatalf("same-package evidence became C3: %+v", c)
		}
		if c.Level == engineeringplan.CouplingC4 && c.Provenance == engineeringplan.CouplingProvenanceGeneratorObserved {
			foundC4 = true
		}
	}
	if !foundC4 {
		t.Fatalf("expected C4 generator family, got %+v", observed)
	}
	graph := engineeringplan.Graph{Version: engineeringplan.Version, Mode: engineeringplan.ModeGraph, Summary: "observed C4", Tasks: subset}
	demands := []engineeringplan.TaskResourceDemand{
		{TaskID: subset[0].ID, CPUMilli: 1, MemoryMiB: 1, RuntimeSlots: 1},
		{TaskID: subset[1].ID, CPUMilli: 1, MemoryMiB: 1, RuntimeSlots: 1},
		{TaskID: subset[2].ID, CPUMilli: 1, MemoryMiB: 1, RuntimeSlots: 1},
	}
	capacity := engineeringplan.ResourceCapacity{CPUMilli: 10, MemoryMiB: 10, VerificationSlots: 3, TotalRuntimeSlots: 3}
	if _, err := engineeringplan.SelectResourceWavesWithObservedCouplings(graph, subset, demands, capacity, 3, nil, observed); err == nil {
		t.Fatal("observed C4 split admitted without dependency")
	}
}

func TestDeriveObservedMissingFallsBack(t *testing.T) {
	s := Snapshot{Creation: Creation{Repository: observedFixtureIdentity(t)}}
	ready := []engineeringplan.Task{{ID: "impl-a", Kind: engineeringplan.Implementation, Title: "a", ScopePaths: []string{"."}, WritePaths: []string{"pkg/a.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "o"}}, EstimatedSeconds: 5}}
	if _, haveRI, err := deriveObservedCouplings(s, ready); err != nil || haveRI {
		t.Fatalf("missing RI must degrade without error: err=%v haveRI=%v", err, haveRI)
	}
	s.PlannerGoContext = &PlannerGoContextRecord{Unavailable: "no_eligible_complete_go_source"}
	if _, haveRI, err := deriveObservedCouplings(s, ready); err != nil || haveRI {
		t.Fatalf("unavailable RI must degrade without error: err=%v haveRI=%v", err, haveRI)
	}
}

func TestDeriveObservedForgedFailsClosed(t *testing.T) {
	files, pkgs := observedSamePackagePair()
	s, ready := observedFixtureSnapshot(t, files, pkgs, nil, nil, nil)
	subset := ready[:2]
	subset[0].ID, subset[1].ID = "impl-a", "impl-b"
	forged := s
	forgedRec := *s.PlannerGoContext
	forgedRec.RIExecutableSHA256 = strings.Repeat("f", 64)
	forged.PlannerGoContext = &forgedRec
	if _, _, err := deriveObservedCouplings(forged, subset); err == nil {
		t.Fatal("forged producer admitted")
	}
	forgedSource := s
	forgedRec2 := *s.PlannerGoContext
	forgedSources := append([]repository.SourceDigest(nil), forgedRec2.Sources...)
	forgedSources[0].SHA256 = strings.Repeat("e", 64)
	forgedRec2.Sources = forgedSources
	forgedRec2.RecordID, _ = plannerGoContextRecordID(forgedRec2)
	forgedSource.PlannerGoContext = &forgedRec2
	if _, _, err := deriveObservedCouplings(forgedSource, subset); err == nil {
		t.Fatal("forged source digest admitted")
	}
}

func TestDeriveObservedDeltaExcludesStaleAndNewPathNoSignal(t *testing.T) {
	files, pkgs := observedSamePackagePair()
	s, ready := observedFixtureSnapshot(t, files, pkgs, nil, nil, nil)
	byWrite := observedTasksByWrite(ready)
	subset := []engineeringplan.Task{byWrite["pkg/a.go"], byWrite["pkg/b.go"]}
	subset[0].ID, subset[1].ID = "impl-a", "impl-b"
	// Baseline derives one C2 pair.
	baseline, haveRI, err := deriveObservedCouplings(s, subset)
	if err != nil || !haveRI || len(baseline) != 1 {
		t.Fatalf("baseline C2 derivation failed: %v haveRI=%v %+v", err, haveRI, baseline)
	}
	// Prior staged delta changed pkg/b.go with a lineage-compatible parent:
	// the stale same-package relation must be excluded, not used detached.
	s = observedSnapshotWithParentDelta(t, s, "pkg/b.go")
	stale, haveRI, err := deriveObservedCouplings(s, subset)
	if err != nil || !haveRI {
		t.Fatalf("delta derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(stale) != 0 {
		t.Fatalf("changed prior-hub path yielded stale signal: %+v", stale)
	}
	// A task owning only a new path absent from the admitted graph yields no
	// observed relation and keeps PARTIAL with no C0 proof.
	fresh := Snapshot{Creation: s.Creation, PlannerGoContext: s.PlannerGoContext}
	newTask := engineeringplan.Task{ID: "impl-new", Kind: engineeringplan.Implementation, Title: "new", ScopePaths: []string{"."}, WritePaths: []string{"absent/fresh.go"}, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "o"}}, EstimatedSeconds: 5}
	mixed := []engineeringplan.Task{subset[0], newTask}
	observed, haveRI, err := deriveObservedCouplings(fresh, mixed)
	if err != nil || !haveRI {
		t.Fatalf("new-path derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(observed) != 0 {
		t.Fatalf("new/omitted path yielded signal: %+v", observed)
	}
}

func TestDeriveObservedDeterministicAcrossFamiliesAndFiles(t *testing.T) {
	// Two generator families yield the same C4 pair (impl-a, impl-b) through
	// generator->output edges and a two-outputs family edge; four same-package
	// files yield one C2 pair (impl-c, impl-d). Repeated derivation over the
	// same snapshot with permuted ready order must produce byte-identical
	// output: no arbitrary first-map win for the C4 evidence and the canonical
	// smallest witness for the C2 evidence.
	directive1a := "//go:generate go run ./cmd/gen1 -file=out1.go"
	directive1b := "//go:generate go run ./cmd/gen1 -file=out2.go"
	directive2 := "//go:generate go run ./cmd/gen2 -file=out.go"
	generated := "// Code generated by command. DO NOT EDIT."
	files := map[string]string{
		"gen1/tool.go": "package gen1\n" + directive1a + "\n" + directive1b + "\nfunc Tool1() {}\n",
		"gen1/out1.go": generated + "\npackage gen1out1\n",
		"gen1/out2.go": generated + "\npackage gen1out2\n",
		"gen2/tool.go": "package gen2\n" + directive2 + "\nfunc Tool2() {}\n",
		"gen2/out.go":  generated + "\npackage gen2out\n",
		"pkg/a1.go":    "package pkg\nfunc A1() {}\n",
		"pkg/a2.go":    "package pkg\nfunc A2() {}\n",
		"pkg/b1.go":    "package pkg\nfunc B1() {}\n",
		"pkg/b2.go":    "package pkg\nfunc B2() {}\n",
	}
	pkgs := map[string]ri.GoPackageBinding{
		"gen1/tool.go": {ImportPath: "example.com/m/gen1", ModulePath: "example.com/m"},
		"gen1/out1.go": {ImportPath: "example.com/m/gen1out1", ModulePath: "example.com/m"},
		"gen1/out2.go": {ImportPath: "example.com/m/gen1out2", ModulePath: "example.com/m"},
		"gen2/tool.go": {ImportPath: "example.com/m/gen2", ModulePath: "example.com/m"},
		"gen2/out.go":  {ImportPath: "example.com/m/gen2out", ModulePath: "example.com/m"},
		"pkg/a1.go":    {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"pkg/a2.go":    {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"pkg/b1.go":    {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
		"pkg/b2.go":    {ImportPath: "example.com/m/pkg", ModulePath: "example.com/m"},
	}
	markers := map[string][]string{
		"gen1/tool.go": {directive1a, directive1b},
		"gen1/out1.go": {generated},
		"gen1/out2.go": {generated},
		"gen2/tool.go": {directive2},
		"gen2/out.go":  {generated},
	}
	identity := observedFixtureIdentity(t)
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	sources := observedFixtureSources(identity, sourceID, files)
	byPath := observedSourceByPath(sources)
	seedPaths := []string{"gen1/out1.go", "gen1/out2.go", "gen1/tool.go", "gen2/out.go", "gen2/tool.go", "pkg/a1.go", "pkg/a2.go", "pkg/b1.go", "pkg/b2.go"}
	seedDigest := observedSeedDigest(t, sourceID, seedPaths)
	range1a := ri.GoRange{StartByte: 13, EndByte: 13 + len(directive1a)}
	range1b := ri.GoRange{StartByte: 13 + len(directive1a) + 1, EndByte: 13 + len(directive1a) + 1 + len(directive1b)}
	range2 := ri.GoRange{StartByte: 13, EndByte: 13 + len(directive2)}
	metadata := &ri.GoGenerationMetadata{
		Schema: "engorch.ri.go-generation.v1", Version: 1, SourceID: sourceID,
		SeedPaths: seedPaths, SeedDigest: seedDigest, Coverage: "PARTIAL",
		Bindings: []ri.GoGenerationBinding{
			{GeneratorPath: "gen1/tool.go", GeneratedPath: "gen1/out1.go", Directive: directive1a, DirectiveRange: range1a, Command: "go", DirectiveSource: byPath["gen1/tool.go"], OutputSource: byPath["gen1/out1.go"]},
			{GeneratorPath: "gen1/tool.go", GeneratedPath: "gen1/out2.go", Directive: directive1b, DirectiveRange: range1b, Command: "go", DirectiveSource: byPath["gen1/tool.go"], OutputSource: byPath["gen1/out2.go"]},
			{GeneratorPath: "gen2/tool.go", GeneratedPath: "gen2/out.go", Directive: directive2, DirectiveRange: range2, Command: "go", DirectiveSource: byPath["gen2/tool.go"], OutputSource: byPath["gen2/out.go"]},
		},
		Sources: sources, Omissions: make([]taskcontext.Omission, 0),
		InventoryFiles: len(sources), AttemptedDirectiveFiles: 2,
	}
	observedSealGenerationMetadata(t, metadata)
	if err := ri.ValidateGoGenerationMetadata(*metadata); err != nil {
		t.Fatalf("fixture metadata invalid: %v", err)
	}
	// Pass generators in reverse order to prove traversal sorting.
	reversedGenerators := []ri.GoGeneratorBinding{
		{GeneratorPath: "gen2/tool.go", GeneratedPath: "gen2/out.go", Directive: directive2},
		{GeneratorPath: "gen1/tool.go", GeneratedPath: "gen1/out2.go", Directive: directive1b},
		{GeneratorPath: "gen1/tool.go", GeneratedPath: "gen1/out1.go", Directive: directive1a},
	}
	s, _ := observedFixtureSnapshotWithIdentity(t, identity, files, pkgs, markers, reversedGenerators, metadata)
	implTask := func(id string, writes ...string) engineeringplan.Task {
		return engineeringplan.Task{ID: id, Kind: engineeringplan.Implementation, Title: "Task " + id, ScopePaths: []string{"."}, WritePaths: writes, ExpectedEvidence: []engineeringplan.Evidence{{Kind: "file", Description: "output"}}, EstimatedSeconds: 10}
	}
	ready := []engineeringplan.Task{
		implTask("impl-a", "gen1/tool.go", "gen2/tool.go"),
		implTask("impl-b", "gen1/out1.go", "gen1/out2.go", "gen2/out.go"),
		implTask("impl-c", "pkg/a1.go", "pkg/a2.go"),
		implTask("impl-d", "pkg/b1.go", "pkg/b2.go"),
	}
	first, haveRI, err := deriveObservedCouplings(s, ready)
	if err != nil || !haveRI {
		t.Fatalf("deterministic derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(first) != 2 {
		t.Fatalf("expected one C4 and one C2 pair, got %+v", first)
	}
	byPair := map[string]engineeringplan.TaskCoupling{}
	for _, c := range first {
		byPair[c.From+"\x00"+c.To] = c
	}
	c4, ok := byPair["impl-a\x00impl-b"]
	if !ok || c4.Level != engineeringplan.CouplingC4 || c4.Provenance != engineeringplan.CouplingProvenanceGeneratorObserved {
		t.Fatalf("expected C4 generator pair impl-a/impl-b, got %+v", first)
	}
	c2, ok := byPair["impl-c\x00impl-d"]
	if !ok || c2.Level != engineeringplan.CouplingC2 || c2.Provenance != engineeringplan.CouplingProvenanceTopologyObserved {
		t.Fatalf("expected C2 topology pair impl-c/impl-d, got %+v", first)
	}
	// Canonical witnesses: smallest package file and canonical family order.
	if !strings.Contains(c2.Evidence, "pkg/a1.go") {
		t.Fatalf("C2 evidence witness is not canonical: %q", c2.Evidence)
	}
	if !strings.Contains(c4.Evidence, "gen1/tool.go") {
		t.Fatalf("C4 evidence did not keep the deterministic winner: %q", c4.Evidence)
	}
	firstRaw, err := canonical.Bytes(first)
	if err != nil {
		t.Fatal(err)
	}
	firstSum := sha256.Sum256(firstRaw)
	for i := 0; i < 25; i++ {
		rotated := append([]engineeringplan.Task(nil), ready[(i%len(ready)):]...)
		rotated = append(rotated, ready[:(i%len(ready))]...)
		got, haveRI, err := deriveObservedCouplings(s, rotated)
		if err != nil || !haveRI {
			t.Fatalf("replay %d failed: %v haveRI=%v", i, err, haveRI)
		}
		if len(got) != len(first) {
			t.Fatalf("replay %d changed pair count: %+v vs %+v", i, got, first)
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("replay %d changed output (no deterministic tie-break): %+v vs %+v", i, got, first)
			}
		}
		raw, err := canonical.Bytes(got)
		if err != nil {
			t.Fatal(err)
		}
		if sha256.Sum256(raw) != firstSum {
			t.Fatalf("replay %d changed output hash", i)
		}
	}
}

func observedSnapshotWithParentDelta(t *testing.T, s Snapshot, changedPaths ...string) Snapshot {
	t.Helper()
	parent := worktree.Candidate{Version: 1, WorktreeID: strings.Repeat("1", 64), Head: s.Creation.Repository.Commit, IndexHash: strings.Repeat("2", 64), FilesHash: strings.Repeat("3", 64), FileCount: len(changedPaths)}
	parentID, err := parent.ID()
	if err != nil {
		t.Fatal(err)
	}
	changes := make([]fileeffects.Change, 0, len(changedPaths))
	for _, p := range changedPaths {
		changes = append(changes, fileeffects.Change{Path: p})
	}
	s.Candidate = &parent
	s.GraphStagedCohorts = []StagedCohortArchive{{
		CohortIndex: 0, PreparationID: "prep", BaseCandidateID: "base", CandidateID: parentID, TaskIDs: []string{"impl-hub"},
		Batch: GraphWriterBatchRecord{Prepared: PreparedFiles{Proposal: fileeffects.Proposal{Changes: changes}}},
	}}
	return s
}

func TestDeriveObservedManifestChangeStalesDeclaredPackages(t *testing.T) {
	// Declared-module package identity depends on ManifestPath/InventoryDigest
	// (ri.DeclaredGoPackageBinding): a prior staged change to an admitted
	// manifest, or a new nested go.mod absent from the old inventory, makes
	// every declared-module package observation derived from that inventory
	// ineligible even when the endpoint Go bytes are unchanged. Source-local
	// membership stays eligible when its own file bytes are proven unchanged.
	// No C0 is emitted and no absence is claimed: derivation keeps PARTIAL
	// with the smaller eligible set.
	goMod := "module example.com/m\n"
	goModSum := sha256.Sum256([]byte(goMod))
	blobSum := sha256.Sum256([]byte("fixture-manifest-blob:go.mod"))
	identity := observedFixtureIdentity(t)
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	inventory := ri.GoModuleInventory{
		Version: 1, RepositoryID: sourceID, Commit: identity.Commit, Tree: identity.Tree,
		Coverage: "complete", ObservedManifestCount: 1,
		Files: []ri.GoManifestObservation{{
			Kind: "go_mod", Path: "go.mod",
			Blob:  hex.EncodeToString(blobSum[:]),
			Bytes: int64(len(goMod)), SHA256: hex.EncodeToString(goModSum[:]),
			Status: "parsed", ModulePath: "example.com/m",
		}},
	}
	inventory.Digest, err = canonical.Hash("harness.ri.go-module-inventory.v1", inventory)
	if err != nil {
		t.Fatal(err)
	}
	if err := ri.ValidateGoModuleInventoryRecord(inventory); err != nil {
		t.Fatalf("fixture inventory invalid: %v", err)
	}
	declaredA, err := ri.DeclaredGoPackageBinding(inventory, "pkg/a.go", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	declaredB, err := ri.DeclaredGoPackageBinding(inventory, "pkg/b.go", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	if declaredA.IdentityKind != "declared_module_v1" || declaredB.IdentityKind != "declared_module_v1" || declaredA.InventoryDigest != inventory.Digest || declaredA.ManifestPath != "go.mod" {
		t.Fatalf("fixture bindings are not declared-module bound: %+v %+v", declaredA, declaredB)
	}
	localX, err := ri.SourceLocalGoPackageBinding(sourceID, "solo/x.go", "solo")
	if err != nil {
		t.Fatal(err)
	}
	localY, err := ri.SourceLocalGoPackageBinding(sourceID, "solo/y.go", "solo")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"pkg/a.go":  "package pkg\nfunc A() {}\n",
		"pkg/b.go":  "package pkg\nfunc B() {}\n",
		"solo/x.go": "package solo\nfunc X() {}\n",
		"solo/y.go": "package solo\nfunc Y() {}\n",
	}
	pkgs := map[string]ri.GoPackageBinding{
		"pkg/a.go": declaredA, "pkg/b.go": declaredB, "solo/x.go": localX, "solo/y.go": localY,
	}
	s, ready := observedFixtureSnapshotWithInventory(t, identity, files, pkgs, nil, nil, nil, &inventory)
	byWrite := observedTasksByWrite(ready)
	pkgA, pkgB, soloX, soloY := byWrite["pkg/a.go"], byWrite["pkg/b.go"], byWrite["solo/x.go"], byWrite["solo/y.go"]
	pkgA.ID, pkgB.ID, soloX.ID, soloY.ID = "impl-a", "impl-b", "impl-c", "impl-d"
	subset := []engineeringplan.Task{pkgA, pkgB, soloX, soloY}
	baseline, haveRI, err := deriveObservedCouplings(s, subset)
	if err != nil || !haveRI {
		t.Fatalf("manifest baseline derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(baseline) != 2 {
		t.Fatalf("expected declared and source-local C2 pairs, got %+v", baseline)
	}
	// A changed declared manifest stales the declared pair while the proven
	// unchanged source-local pair remains; derivation stays PARTIAL with no
	// C0 and no absence claim.
	changed := observedSnapshotWithParentDelta(t, s, "go.mod")
	stale, haveRI, err := deriveObservedCouplings(changed, subset)
	if err != nil || !haveRI {
		t.Fatalf("changed-manifest derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(stale) != 1 || stale[0].Level != engineeringplan.CouplingC2 || stale[0].From != "impl-c" || stale[0].To != "impl-d" || stale[0].Provenance != engineeringplan.CouplingProvenanceTopologyObserved {
		t.Fatalf("changed manifest did not stale only the declared pair: %+v", stale)
	}
	// A new nested manifest absent from the old inventory stales declared
	// ownership the same way: the old inventory cannot describe it, so no
	// overlay is synthesized and the declared pair is excluded.
	nested := observedSnapshotWithParentDelta(t, s, "nested/go.mod")
	added, haveRI, err := deriveObservedCouplings(nested, subset)
	if err != nil || !haveRI {
		t.Fatalf("nested-manifest derivation failed: %v haveRI=%v", err, haveRI)
	}
	if len(added) != 1 || added[0] != stale[0] {
		t.Fatalf("new nested manifest did not stale the declared pair: %+v", added)
	}
}
