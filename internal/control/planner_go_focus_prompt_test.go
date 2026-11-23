package control

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func TestPlannerGoContextV4AddsBoundedFocusTopologyFromContractExcerptPaths(t *testing.T) {
	record := plannerFocusPromptRecord(t)
	prompt := plannerGoContextPromptFor(&record)
	if prompt.FocusTopology == nil || prompt.FocusTopologyUnavailableReason != "" {
		t.Fatalf("v4 focus topology was not available: %#v", prompt)
	}
	topology := prompt.FocusTopology
	if topology.SeedBasis != "objective_contract" || topology.Coverage != "PARTIAL" || topology.SourceID != record.Graph.SourceID || topology.CandidateID != "" || topology.GraphDigest != record.Graph.Digest || topology.FocusPaths[0] != "api/a.go" {
		t.Fatalf("focus topology lost source/seed binding: %+v", topology)
	}
	if len(topology.FocusPaths) != 1 {
		t.Fatalf("non-graph template excerpt became a focus seed: %#v", topology.FocusPaths)
	}
	var foundHub, foundGenerator, foundPotentialTest bool
	for _, module := range topology.ObservedPackages {
		foundHub = foundHub || module.Label == "example.test/m/api" && module.SharedHub && module.ObservedImporters == 2
	}
	for _, coupling := range topology.Couplings {
		foundGenerator = foundGenerator || coupling.Reason == "EXPLICIT_GENERATOR_OWNER"
	}
	for _, path := range topology.PotentialTests {
		foundPotentialTest = foundPotentialTest || path == "api/a_test.go"
	}
	if !foundHub || !foundGenerator || !foundPotentialTest {
		t.Fatalf("focus hint omitted observed hub/generator/test: hub=%v generator=%v test=%v; %+v", foundHub, foundGenerator, foundPotentialTest, topology)
	}
	encoded, err := canonical.Bytes(topology)
	if err != nil || len(encoded) > 8<<10 {
		t.Fatalf("model topology exceeds byte bound: bytes=%d err=%v", len(encoded), err)
	}
	if strings.Contains(string(encoded), "PRIVATE_PLANNER_TOPOLOGY_SOURCE") || strings.Contains(string(encoded), "changed_paths") {
		t.Fatalf("topology leaked source bytes or mislabeled objective seeds: %s", encoded)
	}
	if strings.Contains(string(encoded), "write_paths") || strings.Contains(string(encoded), "independent") {
		t.Fatalf("topology gained write authority or independence claims: %s", encoded)
	}
	again := plannerGoContextPromptFor(&record)
	if !reflect.DeepEqual(prompt.FocusTopology, again.FocusTopology) {
		t.Fatal("recomputed topology differs for the same durable graph and contract context")
	}
}

func TestPlannerGoContextV4FocusTopologyHasExplicitUnavailableReasons(t *testing.T) {
	record := plannerFocusPromptRecord(t)
	missing := record
	contract := *record.ContractContext
	contract.Excerpts = []taskcontext.SelectedFile{{Path: "template/not-in-go-graph.tmpl"}}
	missing.ContractContext = &contract
	prompt := plannerGoContextPromptFor(&missing)
	if prompt.FocusTopology != nil || prompt.FocusTopologyUnavailableReason != "no_graph_contract_excerpt_paths" {
		t.Fatalf("zero eligible seeds were not explicit: %#v", prompt)
	}
	badGraph := record
	badGraph.Graph = cloneFocusPromptGraph(record.Graph)
	badGraph.Graph.Digest = "invalid"
	badContract := *record.ContractContext
	badContract.GraphDigest = "invalid"
	badGraph.ContractContext = &badContract
	prompt = plannerGoContextPromptFor(&badGraph)
	if prompt.FocusTopology != nil || prompt.FocusTopologyUnavailableReason != "focus_topology_query_failed" {
		t.Fatalf("query failure was not explicit: %#v", prompt)
	}
	badBinding := record
	badBinding.Graph = cloneFocusPromptGraph(record.Graph)
	badBinding.Graph.CandidateID = strings.Repeat("c", 64)
	prompt = plannerGoContextPromptFor(&badBinding)
	if prompt.FocusTopology != nil || prompt.FocusTopologyUnavailableReason != "contract_graph_binding_mismatch" {
		t.Fatalf("candidate was silently promoted into committed planner facts: %#v", prompt)
	}
}

func TestPlannerGoContextV1V2NilTopologyKeepsLegacyPromptBytes(t *testing.T) {
	type legacyPrompt struct {
		RecordID           string                     `json:"record_id"`
		Coverage           string                     `json:"coverage"`
		RIExecutableSHA256 string                     `json:"ri_executable_sha256"`
		GraphDigest        string                     `json:"graph_digest,omitempty"`
		Context            *ri.GoContextManifest      `json:"context,omitempty"`
		ContractContext    *ri.GoContractContext      `json:"contract_context,omitempty"`
		Unavailable        string                     `json:"unavailable,omitempty"`
		Generation         *plannerGoGenerationPrompt `json:"generation,omitempty"`
	}
	for _, version := range []int{1, 2} {
		record := PlannerGoContextRecord{Version: version, RecordID: "record-id", RIExecutableSHA256: strings.Repeat("a", 64)}
		prompt := plannerGoContextPromptFor(&record)
		got, err := canonical.Bytes(prompt)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canonical.Bytes(legacyPrompt{RecordID: record.RecordID, Coverage: "PARTIAL", RIExecutableSHA256: record.RIExecutableSHA256})
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("version %d legacy prompt bytes changed: got=%s want=%s err=%v", version, got, want, err)
		}
	}
}

func plannerFocusPromptRecord(t *testing.T) PlannerGoContextRecord {
	t.Helper()
	producer := strings.Repeat("b", 64)
	sourceID := strings.Repeat("0", 64)
	ownerSource := "package gen\n//go:generate go run .\n"
	generatedSource := "// Code generated by gen. DO NOT EDIT.\npackage api\n// PRIVATE_PLANNER_TOPOLOGY_SOURCE\n"
	testSource := "package api_test\nfunc TestA() {}\n"
	consumerSource := "package consumer\nimport \"example.test/m/api\"\n"
	files := []ri.GoGraphFileInput{
		focusPromptGraphInput("gen/main.go", ownerSource, ri.GoPackageBinding{ImportPath: "example.test/m/gen", ModulePath: "example.test/m"}, nil, []string{"//go:generate go run ."}, producer),
		focusPromptGraphInput("api/a.go", generatedSource, ri.GoPackageBinding{ImportPath: "example.test/m/api", ModulePath: "example.test/m"}, nil, []string{"// Code generated by gen. DO NOT EDIT."}, producer),
		focusPromptGraphInput("api/b.go", generatedSource, ri.GoPackageBinding{ImportPath: "example.test/m/api", ModulePath: "example.test/m"}, nil, []string{"// Code generated by gen. DO NOT EDIT."}, producer),
		focusPromptGraphInput("api/a_test.go", testSource, ri.GoPackageBinding{ImportPath: "example.test/m/api_test", ModulePath: "example.test/m", TestOfImportPath: "example.test/m/api"}, nil, nil, producer),
		focusPromptGraphInput("consumer/a.go", consumerSource, ri.GoPackageBinding{ImportPath: "example.test/m/consumer-a", ModulePath: "example.test/m"}, []ri.GoImport{{Path: "example.test/m/api", Range: ri.GoRange{StartByte: strings.Index(consumerSource, `"example.test/m/api"`), EndByte: strings.Index(consumerSource, `"example.test/m/api"`) + len(`"example.test/m/api"`)}}}, nil, producer),
		focusPromptGraphInput("consumer/b.go", consumerSource, ri.GoPackageBinding{ImportPath: "example.test/m/consumer-b", ModulePath: "example.test/m"}, []ri.GoImport{{Path: "example.test/m/api", Range: ri.GoRange{StartByte: strings.Index(consumerSource, `"example.test/m/api"`), EndByte: strings.Index(consumerSource, `"example.test/m/api"`) + len(`"example.test/m/api"`)}}}, nil, producer),
	}
	graph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: files, Generators: []ri.GoGeneratorBinding{{GeneratorPath: "gen/main.go", GeneratedPath: "api/a.go", Directive: "//go:generate go run ."}, {GeneratorPath: "gen/main.go", GeneratedPath: "api/b.go", Directive: "//go:generate go run ."}}})
	if err != nil {
		t.Fatal(err)
	}
	contract := &ri.GoContractContext{SourceID: sourceID, GraphDigest: graph.Digest, ProducerSHA256: producer, Coverage: "PARTIAL", Excerpts: []taskcontext.SelectedFile{{Path: "api/a.go", Content: "PRIVATE_PLANNER_TOPOLOGY_SOURCE"}, {Path: "template/task.tmpl", Content: "template bytes"}}}
	return PlannerGoContextRecord{Version: 4, RecordID: "contract-record", Source: ri.Source{RepositoryID: sourceID}, RIExecutableSHA256: producer, Graph: &graph, ContractContext: contract}
}

func focusPromptGraphInput(path, source string, pkg ri.GoPackageBinding, imports []ri.GoImport, markers []string, producer string) ri.GoGraphFileInput {
	content := []byte(source)
	sourceHash := sha256.Sum256(content)
	facts := ri.GoFileFacts{Schema: "engorch.go-file-facts.v1", Language: "go", ParserVersion: "tree-sitter-go-0.25.0", Path: path, SourceSHA256: hex.EncodeToString(sourceHash[:]), ProducerSHA256: producer, SyntaxErrors: false, Coverage: "PARTIAL", Declarations: []ri.GoSymbol{}, Imports: imports, Calls: []ri.GoCall{}, GeneratedMarkers: markers, Cache: "miss", ParseCount: 1}
	var err error
	facts.CacheKey, err = canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{"schema": facts.Schema, "language": facts.Language, "parser": facts.ParserVersion, "path": path, "source_sha256": facts.SourceSHA256, "producer_sha256": producer})
	if err != nil {
		panic(err)
	}
	body := facts
	body.BodySHA256, body.Cache, body.ParseCount = "", "", 0
	bodyBytes, err := canonical.Bytes(body)
	if err != nil {
		panic(err)
	}
	bodyHash := sha256.Sum256(bodyBytes)
	facts.BodySHA256 = hex.EncodeToString(bodyHash[:])
	return ri.GoGraphFileInput{Facts: facts, Source: content, Package: pkg}
}

func cloneFocusPromptGraph(graph *ri.GoEngineeringGraph) *ri.GoEngineeringGraph {
	clone := *graph
	return &clone
}
