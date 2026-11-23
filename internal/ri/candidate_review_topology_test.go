package ri

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func TestGoCandidateReviewTopologyBindsChangedAddedDeletedAndImpact(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, true)
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	if out.Source.RepositoryID != base.SourceID || out.CandidateID != corpus.CandidateID || out.CandidateFilesHash != corpus.CandidateFilesHash || out.ProducerSHA256 != corpus.ProducerSHA256 || out.BaseGraphDigest != base.Digest || out.BaseModuleInventoryDigest != inventory.Digest || out.CandidateGraphDigest != corpus.Graph.Digest || out.CandidateModuleInventoryDigest != corpus.ModuleInventory.Digest || out.Coverage != "PARTIAL" {
		t.Fatalf("candidate topology lost exact bindings: %+v", out)
	}
	if out.ChangedPathCount != 3 || !reflect.DeepEqual(out.ChangedPaths, []string{"api/a.go", "api/generated.go"}) || out.UnrepresentedChangedPathCount != 1 || out.AdmittedPathCount != 1 || !reflect.DeepEqual(out.AdmittedPaths, []string{"api/new.go"}) || out.DeletedPathCount != 1 || !reflect.DeepEqual(out.DeletedPaths, []string{"api/deleted.go"}) || out.OmittedCount != 1 || len(out.Omissions) != 1 || out.Omissions[0].Path != "api/omitted.go" {
		t.Fatalf("changed/admitted/deleted/omitted classifications were conflated: %+v", out)
	}
	foundGenerator, foundTest, foundCall, foundGeneratorCoupling := false, false, false, false
	for _, relation := range out.BaseGeneratorHints {
		foundGenerator = foundGenerator || relation.GeneratorPath == "gen/main.go" && relation.GeneratedPath == "api/generated.go" && relation.Status == "base_relation_candidate_not_revalidated"
	}
	for _, relation := range out.Couplings {
		foundGeneratorCoupling = foundGeneratorCoupling || relation.Reason == "EXPLICIT_GENERATOR_OWNER"
	}
	for _, path := range out.PotentialTests {
		foundTest = foundTest || path == "api/a_test.go"
	}
	for _, hint := range out.PotentialCallHints {
		if hint.Path == "api/a.go" && hint.Spelling == "Local" {
			foundCall = hint.Resolution == "UNRESOLVED"
		}
		if hint.Resolution != "UNRESOLVED" {
			t.Fatalf("candidate call hint implied resolution: %+v", hint)
		}
	}
	foundHub := false
	for _, module := range out.ObservedPackages {
		foundHub = foundHub || module.ObservedImporters > 1
	}
	if !foundGenerator || foundGeneratorCoupling || !foundTest || !foundCall || !foundHub || len(out.ReviewGroups) == 0 {
		t.Fatalf("candidate impact evidence missing generator/test/call/group: generator=%v test=%v call=%v result=%+v", foundGenerator, foundTest, foundCall, out)
	}
	if len(out.PotentialCallHints) != goCandidateReviewCallHintLimit || out.OmittedCallHints == 0 || !out.Truncated {
		t.Fatalf("query-level call hint cap was not reflected in truncation: %+v", out)
	}
	encoded, err := canonical.Bytes(out)
	if err != nil || len(encoded) > goCandidateReviewTopologyMaxBytes {
		t.Fatalf("candidate reviewer projection exceeds cap: bytes=%d err=%v", len(encoded), err)
	}
	if strings.Contains(string(encoded), "candidate source sentinel") || strings.Contains(string(encoded), "CALLS_RESOLVED") || strings.Contains(string(encoded), "write_paths") {
		t.Fatalf("candidate topology disclosed bytes or authority/semantic claims: %s", encoded)
	}
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err != nil {
		t.Fatalf("candidate topology did not replay: %v", err)
	}
	again, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("candidate review topology is nondeterministic")
	}
}

func TestGoCandidateReviewTopologyDeletionOnlyIsExplicitlyUnavailable(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, false)
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	if out.UnavailableReason != "no_candidate_go_seeds" || out.DeletedPathCount != 1 || !reflect.DeepEqual(out.DeletedPaths, []string{"api/deleted.go"}) || len(out.ReviewGroups) != 0 || len(out.BaseGeneratorHints) != 0 || out.CandidateID != corpus.CandidateID {
		t.Fatalf("deletion-only evidence was misrepresented: %+v", out)
	}
	assertCandidateReviewTopologyArrays(t, out)
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err != nil {
		t.Fatal(err)
	}
}

func TestGoCandidateReviewTopologyPreservesEmptyOmissionsArray(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, true)
	corpus.Omissions = []taskcontext.Omission{}
	corpus.OmittedCount = 0
	corpus.OmissionsTrimmed = false
	corpus.ChangedPaths = []string{"api/a.go", "api/generated.go"}
	corpus.DeletedPaths = []string{"api/deleted.go", "api/omitted.go"}
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	if out.Omissions == nil || len(out.Omissions) != 0 {
		t.Fatalf("empty omissions became null or nonempty: %#v", out.Omissions)
	}
	assertCandidateReviewTopologyArrays(t, out)
	encoded, err := canonical.Bytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"omissions":[]`)) || bytes.Contains(encoded, []byte(`"omissions":null`)) {
		t.Fatalf("projection did not encode the required empty omissions array")
	}
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err != nil {
		t.Fatalf("empty omissions projection did not replay: %v", err)
	}
}

func TestGoCandidateReviewTopologyEncodesEmptyDeletedPathsAsArray(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, false)
	binding, err := DeclaredGoPackageBinding(corpus.ModuleInventory, "api/a.go", "api")
	if err != nil {
		t.Fatal(err)
	}
	changedSource := "package api\n// candidate change\n"
	replacement := graphInput("api/a.go", changedSource, binding, nil, nil, nil, nil, corpus.ProducerSHA256)
	graph, err := ApplyGoEngineeringOverlay(base, GoGraphOverlayInput{
		BaseDigest: base.Digest, CandidateID: corpus.CandidateID, ProducerSHA256: corpus.ProducerSHA256,
		Replacements: []GoGraphFileInput{replacement}, Generators: base.Generators, ModuleInventory: &corpus.ModuleInventory,
	})
	if err != nil {
		t.Fatal(err)
	}
	corpus.Graph = graph
	corpus.DeletedPaths = nil
	corpus.ChangedPaths = []string{"api/a.go"}
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	if out.DeletedPaths == nil {
		t.Fatal("empty deleted_paths must be a nonnil slice for the nonnullable projection field")
	}
	assertCandidateReviewTopologyArrays(t, out)
	encoded, err := canonical.Bytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"deleted_paths":[]`) || strings.Contains(string(encoded), `"deleted_paths":null`) {
		t.Fatalf("empty deleted_paths did not encode as an array: %s", encoded)
	}
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err != nil {
		t.Fatalf("empty deletion projection did not replay: %v", err)
	}
}

func TestGoCandidateReviewTopologyKeepsEmptyImpactArrays(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, false)
	binding, err := DeclaredGoPackageBinding(corpus.ModuleInventory, "api/a.go", "api")
	if err != nil {
		t.Fatal(err)
	}
	producer := corpus.ProducerSHA256
	source := "package api\nfunc Changed() {}\n"
	replacement := graphInput("api/a.go", source, binding, nil, nil, nil, nil, producer)
	deleted := make([]string, 0, len(base.Files)-1)
	for _, file := range base.Files {
		if file.Facts.Path != "api/a.go" {
			deleted = append(deleted, file.Facts.Path)
		}
	}
	sort.Strings(deleted)
	graph, err := ApplyGoEngineeringOverlay(base, GoGraphOverlayInput{
		BaseDigest: base.Digest, CandidateID: corpus.CandidateID, ProducerSHA256: producer,
		Replacements: []GoGraphFileInput{replacement}, Deleted: deleted,
		ModuleInventory: &corpus.ModuleInventory,
	})
	if err != nil {
		t.Fatal(err)
	}
	corpus.Graph = graph
	corpus.ChangedPaths = []string{"api/a.go"}
	corpus.AdmittedPaths = []string{}
	corpus.DeletedPaths = deleted
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	if out.Couplings == nil || len(out.Couplings) != 0 || out.PotentialTests == nil || len(out.PotentialTests) != 0 || out.BaseGeneratorHints == nil {
		t.Fatalf("empty impact collections must remain nonnil arrays: couplings=%#v tests=%#v generators=%#v", out.Couplings, out.PotentialTests, out.BaseGeneratorHints)
	}
	assertCandidateReviewTopologyArrays(t, out)
	encoded, err := canonical.Bytes(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"couplings", "potential_tests"} {
		if !bytes.Contains(encoded, []byte(`"`+name+`":[]`)) {
			t.Fatalf("empty %s did not encode as an array", name)
		}
	}
	if bytes.Contains(encoded, []byte(`"base_generator_hints":null`)) {
		t.Fatal("base_generator_hints encoded as null")
	}
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err != nil {
		t.Fatalf("empty impact projection did not replay: %v", err)
	}
}

func assertCandidateReviewTopologyArrays(t *testing.T, out GoCandidateReviewTopology) {
	t.Helper()
	for name, value := range map[string]any{
		"changed_paths":        out.ChangedPaths,
		"admitted_paths":       out.AdmittedPaths,
		"deleted_paths":        out.DeletedPaths,
		"omissions":            out.Omissions,
		"observed_packages":    out.ObservedPackages,
		"couplings":            out.Couplings,
		"potential_tests":      out.PotentialTests,
		"potential_call_hints": out.PotentialCallHints,
		"base_generator_hints": out.BaseGeneratorHints,
		"review_groups":        out.ReviewGroups,
	} {
		if reflect.ValueOf(value).IsNil() {
			t.Fatalf("%s must encode as an array, not null", name)
		}
	}
	encoded, err := canonical.Bytes(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"changed_paths", "admitted_paths", "deleted_paths", "omissions", "observed_packages", "couplings", "potential_tests", "potential_call_hints", "base_generator_hints", "review_groups"} {
		if bytes.Contains(encoded, []byte(`"`+name+`":null`)) {
			t.Fatalf("%s encoded as null", name)
		}
	}
}

func TestGoCandidateReviewTopologyRejectsStaleBindingsAndTampering(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, true)
	if _, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8); err != nil {
		t.Fatal(err)
	}
	stale := corpus
	stale.CandidateFilesHash = strings.Repeat("e", 64)
	if _, err := QueryGoCandidateReviewTopology(base, inventory, stale, 8); err == nil {
		t.Fatal("stale candidate files hash accepted")
	}
	stale = corpus
	stale.BaseGraphDigest = strings.Repeat("f", 64)
	if _, err := QueryGoCandidateReviewTopology(base, inventory, stale, 8); err == nil {
		t.Fatal("stale base graph digest accepted")
	}
	forged := corpus
	forgedGraph, err := buildGoEngineeringGraph(corpus.Graph.SourceID, corpus.Graph.CandidateID, corpus.Graph.ProducerSHA256, corpus.Graph.Files, base.Generators, &corpus.ModuleInventory)
	if err != nil {
		t.Fatal(err)
	}
	forged.Graph = forgedGraph
	if _, err := QueryGoCandidateReviewTopology(base, inventory, forged, 8); err == nil {
		t.Fatal("stale base generator relation accepted after its generated endpoint changed")
	}
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatal(err)
	}
	out.PotentialTests = append(out.PotentialTests, "forged_test.go")
	if err := ValidateGoCandidateReviewTopology(out, base, inventory, corpus, 8); err == nil {
		t.Fatal("tampered topology projection replayed")
	}
	if _, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 33); err == nil {
		t.Fatal("invalid group limit accepted")
	}
}

func TestGoCandidateReviewTopologyRequiresSensitiveOmissionsRedacted(t *testing.T) {
	base, inventory, corpus := candidateReviewTopologyFixture(t, true)
	corpus.Omissions = append(corpus.Omissions, taskcontext.Omission{Path: "private/credentials/token.go", Reason: "unsafe_or_sensitive_path"})
	corpus.OmittedCount++
	if _, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8); err == nil {
		t.Fatal("sensitive omission path was exposed")
	}
	corpus.Omissions[len(corpus.Omissions)-1].Path = "[redacted]"
	out, err := QueryGoCandidateReviewTopology(base, inventory, corpus, 8)
	if err != nil {
		t.Fatalf("properly redacted sensitive omission rejected: %v", err)
	}
	for _, omission := range out.Omissions {
		if strings.Contains(omission.Path, "credentials") || strings.Contains(omission.Path, "token") {
			t.Fatalf("sensitive path leaked in projection: %+v", omission)
		}
	}
}

func TestGoCandidateReviewTopologyTruncationIsDeterministicAndCounted(t *testing.T) {
	result := GoCandidateReviewTopology{Version: 1, Coverage: "PARTIAL", PotentialCallHints: []GoCandidateReviewCallHint{}, PotentialTests: []string{}, ObservedPackages: []GoFocusTopologyModule{}, Couplings: []GoTopologyCoupling{}, ReviewGroups: []GoTopologyReviewGroup{}}
	for i := 0; i < 60; i++ {
		result.PotentialCallHints = append(result.PotentialCallHints, GoCandidateReviewCallHint{Path: fmt.Sprintf("pkg/%03d.go", i), Spelling: strings.Repeat("Call", 32), StartByte: i, EndByte: i + 4, Resolution: "UNRESOLVED"})
	}
	first := result
	first.PotentialCallHints = append([]GoCandidateReviewCallHint(nil), result.PotentialCallHints...)
	if err := trimGoCandidateReviewTopology(&first); err != nil {
		t.Fatal(err)
	}
	second := result
	second.PotentialCallHints = append([]GoCandidateReviewCallHint(nil), result.PotentialCallHints...)
	if err := trimGoCandidateReviewTopology(&second); err != nil {
		t.Fatal(err)
	}
	if !first.Truncated || first.OmittedCallHints == 0 || !reflect.DeepEqual(first, second) {
		t.Fatalf("oversize projection did not truncate deterministically: %+v", first)
	}
	encoded, err := canonical.Bytes(first)
	if err != nil || len(encoded)+64 > goCandidateReviewTopologyMaxBytes {
		t.Fatalf("trimmed projection exceeds cap: bytes=%d err=%v", len(encoded), err)
	}
}

func candidateReviewTopologyFixture(t *testing.T, withChanges bool) (GoEngineeringGraph, GoModuleInventory, GoCandidateCorpus) {
	t.Helper()
	sourceID := strings.Repeat("0", 64)
	producer := strings.Repeat("b", 64)
	candidateID := strings.Repeat("c", 64)
	filesHash := strings.Repeat("d", 64)
	baseInventory := semanticModuleInventory(t, sourceID)
	packageBinding := func(path, packageName string) GoPackageBinding {
		binding, err := DeclaredGoPackageBinding(baseInventory, path, packageName)
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	apiSource := "package api\n// candidate source sentinel\nfunc Local() {}\nfunc Changed() { Local() }\n"
	generatedSource := "// Code generated by gen. DO NOT EDIT.\npackage api\n"
	ownerSource := "package gen\n//go:generate go run .\n"
	testSource := "package api_test\nfunc TestChanged() {}\n"
	consumerSource := "package consumer\nimport \"example.com/m/api\"\n"
	baseInputs := []GoGraphFileInput{
		graphInput("api/a.go", apiSource, packageBinding("api/a.go", "api"), []GoSymbol{{Name: "Local", Kind: "function_declaration", Range: graphSpan(apiSource, "Local", 0)}, {Name: "Changed", Kind: "function_declaration", Range: graphSpan(apiSource, "Changed", 0)}}, nil, []GoCall{{Spelling: "Local", Resolution: "UNRESOLVED", Range: graphSpan(apiSource, "Local", strings.Index(apiSource, "Changed"))}}, nil, producer),
		graphInput("api/generated.go", generatedSource, packageBinding("api/generated.go", "api"), nil, nil, nil, []string{"// Code generated by gen. DO NOT EDIT."}, producer),
		graphInput("api/a_test.go", testSource, packageBinding("api/a_test.go", "api_test"), nil, nil, nil, nil, producer),
		graphInput("api/deleted.go", "package api\nfunc Deleted() {}\n", packageBinding("api/deleted.go", "api"), nil, nil, nil, nil, producer),
		graphInput("api/omitted.go", "package api\nfunc Omitted() {}\n", packageBinding("api/omitted.go", "api"), nil, nil, nil, nil, producer),
		graphInput("consumer/a/a.go", consumerSource, packageBinding("consumer/a/a.go", "consumer_a"), nil, []GoImport{{Path: "example.com/m/api", Range: graphSpan(consumerSource, `"example.com/m/api"`, 0)}}, nil, nil, producer),
		graphInput("consumer/b/b.go", consumerSource, packageBinding("consumer/b/b.go", "consumer_b"), nil, []GoImport{{Path: "example.com/m/api", Range: graphSpan(consumerSource, `"example.com/m/api"`, 0)}}, nil, nil, producer),
		graphInput("gen/main.go", ownerSource, packageBinding("gen/main.go", "gen"), nil, nil, nil, []string{"//go:generate go run ."}, producer),
	}
	base, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: baseInputs, ModuleInventory: &baseInventory, Generators: []GoGeneratorBinding{{GeneratorPath: "gen/main.go", GeneratedPath: "api/generated.go", Directive: "//go:generate go run ."}}})
	if err != nil {
		t.Fatal(err)
	}
	candidateInventory := candidateInventoryForGraphTest(t, baseInventory, candidateID, filesHash, map[string]string{"go.mod": "example.com/m"})
	moduleBinding := func(path, packageName string) GoPackageBinding {
		binding, err := DeclaredGoPackageBinding(candidateInventory, path, packageName)
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	deletes := []string{}
	replacements := []GoGraphFileInput{}
	changedPaths, admittedPaths := []string{}, []string{}
	deletedPaths := []string{}
	omissions := []taskcontext.Omission{}
	if withChanges {
		changedSource := "package api\n// candidate source sentinel\nfunc Local() {}\nfunc Changed() { " + strings.Repeat("Local(); ", goCandidateReviewCallHintLimit+6) + "}\n"
		changedGeneratedSource := "// Code generated by gen. DO NOT EDIT.\npackage api\n// candidate update\n"
		calls := make([]GoCall, 0, goCandidateReviewCallHintLimit+6)
		searchFrom := strings.Index(changedSource, "func Changed()")
		for i := 0; i < goCandidateReviewCallHintLimit+6; i++ {
			span := graphSpan(changedSource, "Local", searchFrom)
			calls = append(calls, GoCall{Spelling: "Local", Resolution: "UNRESOLVED", Range: span})
			searchFrom = span.EndByte
		}
		replacements = append(replacements,
			graphInput("api/a.go", changedSource, moduleBinding("api/a.go", "api"), []GoSymbol{{Name: "Local", Kind: "function_declaration", Range: graphSpan(changedSource, "Local", 0)}, {Name: "Changed", Kind: "function_declaration", Range: graphSpan(changedSource, "Changed", 0)}}, nil, calls, nil, producer),
			graphInput("api/generated.go", changedGeneratedSource, moduleBinding("api/generated.go", "api"), nil, nil, nil, []string{"// Code generated by gen. DO NOT EDIT."}, producer),
			graphInput("api/new.go", "package api\nfunc Added() {}\n", moduleBinding("api/new.go", "api"), nil, nil, nil, nil, producer),
		)
		deletes = []string{"api/deleted.go", "api/omitted.go"}
		changedPaths = []string{"api/a.go", "api/generated.go", "api/omitted.go"}
		admittedPaths = []string{"api/new.go"}
		deletedPaths = []string{"api/deleted.go"}
		omissions = []taskcontext.Omission{{Path: "api/omitted.go", Reason: "candidate_read_failed"}}
	} else {
		deletes = []string{"api/deleted.go"}
		deletedPaths = []string{"api/deleted.go"}
	}
	generators := append([]GoGeneratorBinding(nil), base.Generators...)
	if withChanges {
		generators = nil // the generated endpoint changed, so the base relation is no longer current candidate evidence
	}
	candidateGraph, err := ApplyGoEngineeringOverlay(base, GoGraphOverlayInput{BaseDigest: base.Digest, CandidateID: candidateID, ProducerSHA256: producer, Replacements: replacements, Deleted: deletes, Generators: generators, ModuleInventory: &candidateInventory})
	if err != nil {
		t.Fatal(err)
	}
	return base, baseInventory, GoCandidateCorpus{
		Version:     goCandidateCorpusVersion,
		Source:      Source{RepositoryID: sourceID, ObjectFormat: "sha1", Commit: baseInventory.Commit, Tree: baseInventory.Tree},
		CandidateID: candidateID, CandidateFilesHash: filesHash, ProducerSHA256: producer,
		BaseGraphDigest: base.Digest, BaseModuleInventoryDigest: baseInventory.Digest,
		ModuleInventory: candidateInventory, Graph: candidateGraph,
		ChangedPaths: changedPaths, AdmittedPaths: admittedPaths, DeletedPaths: deletedPaths,
		OmittedCount: len(omissions), Omissions: omissions, Coverage: "PARTIAL",
	}
}
