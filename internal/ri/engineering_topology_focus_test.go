package ri

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestGoFocusTopologyUsesObjectiveContractSeedsAndObservedRelations(t *testing.T) {
	graph := topologyFixture(t)
	before := graph.Digest
	out, err := QueryGoFocusTopology(graph, []string{"api/a.go"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out.SeedBasis != "objective_contract" || out.Coverage != "PARTIAL" || out.GraphDigest != graph.Digest || out.SourceID != graph.SourceID || out.CandidateID != graph.CandidateID || out.Digest == "" || graph.Digest != before {
		t.Fatalf("focus topology lost source binding or seed semantics: %+v", out)
	}
	if len(out.FocusPaths) != 1 || out.FocusPaths[0] != "api/a.go" {
		t.Fatalf("focus seed was not preserved: %#v", out.FocusPaths)
	}
	foundHub, foundGenerator, foundSamePackage, foundTest, foundOwner := false, false, false, false, false
	for _, module := range out.ObservedPackages {
		foundHub = foundHub || module.Label == "example.com/m/api" && module.SharedHub && module.ObservedImporters == 2
	}
	for _, coupling := range out.Couplings {
		foundGenerator = foundGenerator || coupling.Reason == "EXPLICIT_GENERATOR_OWNER" && coupling.From == "api/a.go" && coupling.To == "gen/main.go"
		foundSamePackage = foundSamePackage || coupling.Reason == "SAME_OBSERVED_PACKAGE" && coupling.From == "api/a.go" && coupling.To == "api/b.go"
	}
	for _, path := range out.PotentialTests {
		foundTest = foundTest || path == "api/a_test.go"
	}
	for _, group := range out.ReviewGroups {
		for _, path := range group.Paths {
			foundOwner = foundOwner || path == "gen/main.go"
		}
	}
	if !foundHub || !foundGenerator || !foundSamePackage || !foundTest || !foundOwner {
		t.Fatalf("focus projection lost hub/generator/package/test observations: hub=%v gen=%v package=%v test=%v owner=%v; %+v", foundHub, foundGenerator, foundSamePackage, foundTest, foundOwner, out)
	}
	encoded, err := canonical.Bytes(out)
	if err != nil || len(encoded) > goFocusTopologyMaxBytes {
		t.Fatalf("projection exceeded hard bound: bytes=%d err=%v", len(encoded), err)
	}
	again, err := QueryGoFocusTopology(graph, []string{"api/a.go"}, 2)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("focus topology is nondeterministic")
	}
}

func TestGoFocusTopologyIsBoundedAndContainsNoSourceBytes(t *testing.T) {
	const count = 64
	producer := strings.Repeat("b", 64)
	sourceID := strings.Repeat("0", 64)
	secret := "PRIVATE_SOURCE_SENTINEL_92e4e"
	files := make([]GoGraphFileInput, 0, count)
	focus := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("pkg/%s_%02d.go", strings.Repeat("d", 64), i)
		focus = append(focus, path)
		files = append(files, graphInput(path, "package shared\n// "+secret+"\n", GoPackageBinding{ImportPath: "example.com/m/shared", ModulePath: "example.com/m"}, nil, nil, nil, nil, producer))
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	out, err := QueryGoFocusTopology(graph, focus, 32)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canonical.Bytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > goFocusTopologyMaxBytes || !out.Truncated || out.OmittedReviewPaths+out.OmittedReviewGroups+out.OmittedCouplings+out.OmittedPackages+out.OmittedPotentialTests == 0 {
		t.Fatalf("large projection was not bounded with explicit omissions: bytes=%d result=%+v", len(encoded), out)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("focus projection disclosed source contents")
	}
	if len(out.FocusPaths) != count {
		t.Fatalf("projection dropped objective focus paths: got %d want %d", len(out.FocusPaths), count)
	}
	again, err := QueryGoFocusTopology(graph, focus, 32)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("bounded truncation is nondeterministic")
	}
}

func TestGoFocusTopologyUsesSeparateDigestDomain(t *testing.T) {
	graph := topologyFixture(t)
	focus, err := QueryGoFocusTopology(graph, []string{"api/a.go"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := canonical.Hash("harness.ri.go-focus-topology.v1", focus.content())
	if err != nil || focus.Digest != expected {
		t.Fatalf("focus topology digest mismatch: got=%s want=%s err=%v", focus.Digest, expected, err)
	}
	legacy, err := QueryGoTopology(graph, []string{"api/a.go"}, 2)
	if err != nil || legacy.Digest == focus.Digest {
		t.Fatal("focus topology reused legacy topology digest")
	}
}

func TestGoFocusTopologyPreservesCandidateBinding(t *testing.T) {
	sourceID := strings.Repeat("0", 64)
	candidateID := strings.Repeat("c", 64)
	producer := strings.Repeat("b", 64)
	binding, err := SourceLocalGoPackageBinding(sourceID, "pkg/a.go", "a")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, CandidateID: candidateID, ProducerSHA256: producer, Files: []GoGraphFileInput{graphInput("pkg/a.go", "package a\n", binding, nil, nil, nil, nil, producer)}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := QueryGoFocusTopology(graph, []string{"pkg/a.go"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out.SourceID != sourceID || out.CandidateID != candidateID || out.GraphDigest != graph.Digest {
		t.Fatalf("candidate binding was not preserved: %+v", out)
	}
}
