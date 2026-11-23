package ri

import (
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestGraphTypedCanonicalSealMatchesGenericHash(t *testing.T) {
	producer := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sourceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	source := []byte("package p\nfunc Value() {}\n")
	facts := makeCorpusTestFacts(t, "pkg/value.go", source, producer, nil)
	binding, err := SourceLocalGoPackageBinding(sourceID, "pkg/value.go", "p")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{{Facts: facts, Source: source, Package: binding}}, Generators: []GoGeneratorBinding{}})
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Hash("harness.ri.go-engineering-graph.v1", graph.content())
	if err != nil {
		t.Fatal(err)
	}
	if graph.Digest != want || ValidateGoEngineeringGraph(graph) != nil {
		t.Fatal("typed graph seal diverged from generic canonical hash")
	}
}

func TestGraphTypedSealAdmitsBoundedLargeClosedGraph(t *testing.T) {
	producer := strings.Repeat("a", 64)
	sourceID := strings.Repeat("b", 64)
	var source strings.Builder
	source.WriteString("package p\nfunc F() {\n")
	calls := make([]GoCall, 0, 3500)
	for range 3500 {
		start := source.Len()
		source.WriteString("call()\n")
		calls = append(calls, GoCall{Spelling: "call", Resolution: "UNRESOLVED", Range: GoRange{StartByte: start, EndByte: start + len("call")}})
	}
	source.WriteString("}\n")
	text := source.String()
	binding, err := SourceLocalGoPackageBinding(sourceID, "pkg/large.go", "p")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, CandidateID: strings.Repeat("c", 64), ProducerSHA256: producer, Files: []GoGraphFileInput{graphInput("pkg/large.go", text, binding, []GoSymbol{{Name: "F", Kind: "function_declaration", Range: graphSpan(text, "F", 0)}}, nil, calls, nil, producer)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := canonical.TypedGeneratedHash("harness.ri.go-engineering-graph.v1", graph.content()); err == nil || !strings.Contains(err.Error(), "JSON size") {
		t.Fatalf("generic one-megabyte seal error = %v", err)
	}
	if _, err := canonical.TypedGeneratedHashBounded("harness.ri.go-engineering-graph.v1", graph.content(), canonical.MaxTypedGeneratedBytes); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		t.Fatal(err)
	}
}
