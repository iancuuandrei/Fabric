package ri

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

func TestGoEngineeringRankingCombinesLexicalEvidenceAndImportDegree(t *testing.T) {
	graph := rankingFixture(t, "")
	query := GoEngineeringRankingQuery{Objective: "special ParseHeader", TopN: 2}
	result, err := QueryGoEngineeringRanking(graph, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].Path != "special/parse_header.go" || result.Items[0].LexicalScore != 3 {
		t.Fatalf("lexical match did not outrank structural hub: %#v", result.Items)
	}
	if result.Items[0].NameMatches[0] != "parseheader" || result.Items[0].PathMatches[0] != "special" {
		t.Fatalf("ranking omitted lexical evidence: %#v", result.Items[0])
	}

	degreeQuery := GoEngineeringRankingQuery{Objective: "ParseHeader", TopN: 2}
	degreeResult, err := QueryGoEngineeringRanking(graph, degreeQuery)
	if err != nil {
		t.Fatal(err)
	}
	if degreeResult.Items[0].Path != "hub/parse.go" || degreeResult.Items[0].ObservedImporters != 2 || degreeResult.Items[0].ObservedImportDegreeCentrality != 2 {
		t.Fatalf("observed import degree did not break equal lexical scores: %#v", degreeResult.Items)
	}
	if degreeResult.Items[0].CommunityID == "" || degreeResult.Items[1].CommunityID == "" || degreeResult.Items[0].CommunityID == degreeResult.Items[1].CommunityID {
		t.Fatalf("expected query-scoped distinct topology communities: %#v", degreeResult.Items)
	}
	if result.Coverage != "PARTIAL" || result.CommunityScope != goEngineeringRankingCommunityScope || result.Digest == "" || result.GraphDigest != graph.Digest || result.SourceID != graph.SourceID || result.ProducerSHA256 != graph.ProducerSHA256 {
		t.Fatalf("ranking lost graph/source binding or partial coverage: %#v", result)
	}
	if err := ValidateGoEngineeringRanking(graph, query, result); err != nil {
		t.Fatalf("valid ranking did not replay: %v", err)
	}
	again, err := QueryGoEngineeringRanking(graph, query)
	if err != nil || !reflect.DeepEqual(result, again) {
		t.Fatalf("ranking is nondeterministic: err=%v", err)
	}
}

func TestGoEngineeringRankingReusesTopologyGeneratorAndPackageComponents(t *testing.T) {
	graph := topologyFixture(t)
	result, err := QueryGoEngineeringRanking(graph, GoEngineeringRankingQuery{Objective: "api", TopN: 8})
	if err != nil {
		t.Fatal(err)
	}
	components := map[string]string{}
	for _, item := range result.Items {
		if item.Path == "api/a.go" || item.Path == "api/b.go" || item.Path == "api/a_test.go" {
			if item.CommunityID == "" {
				t.Fatalf("missing scoped community for %s", item.Path)
			}
			if components["api"] == "" {
				components["api"] = item.CommunityID
			} else if components["api"] != item.CommunityID {
				t.Fatalf("existing topology couplings were not reused: %#v", result.Items)
			}
		}
	}
	if components["api"] == "" {
		t.Fatalf("expected API package results: %#v", result.Items)
	}
	for _, item := range result.Items {
		if item.Path == "gen/main.go" {
			t.Fatal("generator closure path without lexical match was incorrectly returned as a ranked hit")
		}
	}
}

func TestGoEngineeringRankingIsBoundedAndPartialOnNoMatches(t *testing.T) {
	graph := rankingFixture(t, "")
	result, err := QueryGoEngineeringRanking(graph, GoEngineeringRankingQuery{Objective: "does not exist", TopN: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage != "PARTIAL" || result.Items == nil || len(result.Items) != 0 || result.Truncated || result.OmittedMatches != 0 {
		t.Fatalf("empty observed result was not explicit and bounded: %#v", result)
	}
	truncated, err := QueryGoEngineeringRanking(graph, GoEngineeringRankingQuery{Objective: "ParseHeader", TopN: 1})
	if err != nil || !truncated.Truncated || truncated.OmittedMatches != 1 || len(truncated.Items) != 1 {
		t.Fatalf("top-N truncation was not reported: %#v err=%v", truncated, err)
	}
	tooManyTerms := make([]string, goEngineeringRankingMaxTerms+1)
	for i := range tooManyTerms {
		tooManyTerms[i] = fmt.Sprintf("term%d", i)
	}
	for _, query := range []GoEngineeringRankingQuery{
		{Objective: " ", TopN: 1},
		{Objective: strings.Repeat("x", goEngineeringRankingMaxQuery+1), TopN: 1},
		{Objective: strings.Join(tooManyTerms, " "), TopN: 1},
		{Objective: "x", TopN: 0},
		{Objective: "x", TopN: goEngineeringRankingMaxTopN + 1},
	} {
		if _, err := QueryGoEngineeringRanking(graph, query); err == nil {
			t.Fatalf("accepted unbounded query: %#v", query)
		}
	}
}

func TestGoEngineeringRankingRejectsGraphAndResultSubstitution(t *testing.T) {
	base := rankingFixture(t, "")
	candidate := rankingFixture(t, strings.Repeat("c", 64))
	query := GoEngineeringRankingQuery{Objective: "ParseHeader", TopN: 2}
	result, err := QueryGoEngineeringRanking(base, query)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoEngineeringRanking(candidate, query, result); err == nil {
		t.Fatal("ranking was replayed against a different candidate graph")
	}
	result.Items[0].ObservedImporters++
	if err := ValidateGoEngineeringRanking(base, query, result); err == nil {
		t.Fatal("altered ranking fields passed replay validation")
	}
	result, err = QueryGoEngineeringRanking(base, query)
	if err != nil {
		t.Fatal(err)
	}
	result.Items[0].ObservedImporters++
	result.Digest, err = canonical.Hash("harness.ri.go-engineering-ranking.v1", result.content())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoEngineeringRanking(base, query, result); err == nil {
		t.Fatal("self-rehashed fabricated centrality passed replay validation")
	}
}

func TestGoEngineeringRankingDoesNotInferCallReachability(t *testing.T) {
	graph := rankingFixtureWithCallFacts(t, "", true)
	withoutCalls := rankingFixture(t, "")
	result, err := QueryGoEngineeringRanking(graph, GoEngineeringRankingQuery{Objective: "ParseHeader", TopN: 2})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := QueryGoEngineeringRanking(withoutCalls, GoEngineeringRankingQuery{Objective: "ParseHeader", TopN: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range graph.Edges {
		if item.Relation == "CALL_NAME_CANDIDATE" || item.Relation == "CALLS_UNRESOLVED" {
			if item.Resolution != "UNRESOLVED" {
				t.Fatal("fixture call unexpectedly resolved")
			}
		}
	}
	for i := range baseline.Items {
		baseline.Items[i].CommunityID = result.Items[i].CommunityID
	}
	if !reflect.DeepEqual(result.Items, baseline.Items) {
		t.Fatalf("unresolved call facts changed structural ranking: with=%#v without=%#v", result.Items, baseline.Items)
	}
}

func rankingFixture(t *testing.T, candidateID string) GoEngineeringGraph {
	return rankingFixtureWithCallFacts(t, candidateID, false)
}

func rankingFixtureWithCallFacts(t *testing.T, candidateID string, includeCalls bool) GoEngineeringGraph {
	t.Helper()
	producer := strings.Repeat("b", 64)
	hubSource := "package hub\nfunc ParseHeader() {}\n"
	hub := graphInput("hub/parse.go", hubSource, GoPackageBinding{ImportPath: "example.test/m/hub", ModulePath: "example.test/m"}, []GoSymbol{{Name: "ParseHeader", Kind: "function_declaration", Range: graphSpan(hubSource, "ParseHeader", 0)}}, nil, nil, nil, producer)
	specialSource := "package special\nfunc ParseHeader() {}\n"
	special := graphInput("special/parse_header.go", specialSource, GoPackageBinding{ImportPath: "example.test/m/special", ModulePath: "example.test/m"}, []GoSymbol{{Name: "ParseHeader", Kind: "function_declaration", Range: graphSpan(specialSource, "ParseHeader", 0)}}, nil, nil, nil, producer)
	consumer := func(path, importPath string) GoGraphFileInput {
		source := "package consumer\nimport \"" + importPath + "\"\nfunc Use() { hub.ParseHeader() }\n"
		var calls []GoCall
		if includeCalls && importPath == "example.test/m/hub" {
			callAt := strings.Index(source, "hub.ParseHeader")
			calls = []GoCall{{Spelling: "hub.ParseHeader", Resolution: "UNRESOLVED", Range: graphSpan(source, "hub.ParseHeader", callAt)}}
		}
		return graphInput(path, source, GoPackageBinding{ImportPath: "example.test/m/consumer/" + strings.TrimSuffix(path, ".go"), ModulePath: "example.test/m"}, nil, []GoImport{{Path: importPath, Range: graphSpan(source, `"`+importPath+`"`, 0)}}, calls, nil, producer)
	}
	files := []GoGraphFileInput{hub, special, consumer("c1/use.go", "example.test/m/hub"), consumer("c2/use.go", "example.test/m/hub")}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("0", 64), CandidateID: candidateID, ProducerSHA256: producer, Files: files, Generators: []GoGeneratorBinding{}})
	if err != nil {
		t.Fatal(err)
	}
	return graph
}
