package ri

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

// pprFixtureIDs returns fixed source/producer identities for PPR fixtures.
func pprFixtureIDs() (sourceID, producer string) {
	return strings.Repeat("1", 64), strings.Repeat("2", 64)
}

func pprBinding(importPath string) GoPackageBinding {
	return GoPackageBinding{ImportPath: importPath, ModulePath: "example.com/m"}
}

// pprGraph builds one admitted graph from fixture inputs. Inputs may be
// supplied in any order; the graph canonicalizes them.
func pprGraph(t *testing.T, inputs []GoGraphFileInput) GoEngineeringGraph {
	t.Helper()
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("1", 64), ProducerSHA256: strings.Repeat("2", 64), Files: inputs})
	if err != nil {
		t.Fatal(err)
	}
	return graph
}

func pprTwoFileGraph(t *testing.T) GoEngineeringGraph {
	t.Helper()
	_, producer := pprFixtureIDs()
	aSource := "package pkg\nfunc Alpha() {}\n"
	bSource := "package pkg\nfunc Beta() {}\n"
	a := graphInput("pkg/a.go", aSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Alpha", Kind: "function_declaration", Range: graphSpan(aSource, "Alpha", 0)}}, nil, nil, nil, producer)
	b := graphInput("pkg/b.go", bSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Beta", Kind: "function_declaration", Range: graphSpan(bSource, "Beta", 0)}}, nil, nil, nil, producer)
	return pprGraph(t, []GoGraphFileInput{a, b})
}

func pprMass(t *testing.T, ranks []GoPPRFileRank, want int) {
	t.Helper()
	total := 0
	for _, rank := range ranks {
		if rank.Score < 0 || rank.Score > goPPRScale {
			t.Fatalf("rank score outside fixed-point range: %+v", rank)
		}
		total += rank.Score
	}
	if total != want {
		t.Fatalf("PPR mass not conserved: got %d want %d", total, want)
	}
}

// TestPPRLazyWalkMatchesAnalyticalStep checks one diffusion step against a
// hand-computed value: two connected files, seed mass S on a. Lazy keeps
// S/2 on a and sends S/2 to b; restart combination gives
// a = (3S + 17S/2)/20 = 575000, b = 17S/40 = 425000 at S = 1000000.
func TestPPRLazyWalkMatchesAnalyticalStep(t *testing.T) {
	files := []string{"pkg/a.go", "pkg/b.go"}
	adjacency := map[string][]string{"pkg/a.go": {"pkg/b.go"}, "pkg/b.go": {"pkg/a.go"}}
	restart := goPPRRestartDistribution(files, []string{"pkg/a.go"}, goPPRScale)
	if !reflect.DeepEqual(restart, []int{goPPRScale, 0}) {
		t.Fatalf("restart is not a point mass on the seed: %v", restart)
	}
	scores, used, converged := goPPRLazyWalk(files, adjacency, restart, DefaultGoPPRConfig(), 1)
	if used != 1 || converged {
		t.Fatalf("single step must run once without converging: used=%d converged=%t", used, converged)
	}
	if !reflect.DeepEqual(scores, []int{575000, 425000}) {
		t.Fatalf("analytical PPR step mismatch: %v", scores)
	}
}

// TestPPROrientationPrefersSeedAndConservesMass runs the full computation on
// the two-file clique: the seed must rank first and mass must be conserved.
func TestPPROrientationPrefersSeedAndConservesMass(t *testing.T) {
	graph := pprTwoFileGraph(t)
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Alpha behavior", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seeds.Files, []string{"pkg/a.go"}) {
		t.Fatalf("exact identifier seed mismatch: %+v", seeds)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "Fix Alpha behavior", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != 2 || provenance.Ranks[0].Path != "pkg/a.go" {
		t.Fatalf("seed orientation lost: %+v", provenance)
	}
	pprMass(t, provenance.Ranks, goPPRScale)
	if provenance.Iterations < 1 || provenance.Iterations > goPPRMaxIterations {
		t.Fatalf("iteration count outside hard bound: %+v", provenance)
	}
	if err := ValidateGoPPRProvenance(graph, "Fix Alpha behavior", provenance); err != nil {
		t.Fatalf("fresh provenance failed validation: %v", err)
	}
}

// TestPPRDanglingSeedKeepsMass covers a degenerate single-file graph: with
// no neighbors the lazy walk stays, so the only file retains full mass.
func TestPPRDanglingSeedKeepsMass(t *testing.T) {
	_, producer := pprFixtureIDs()
	source := "package lone\nfunc Lone() {}\n"
	input := graphInput("lone.go", source, pprBinding("example.com/m/lone"), []GoSymbol{{Name: "Lone", Kind: "function_declaration", Range: graphSpan(source, "Lone", 0)}}, nil, nil, nil, producer)
	graph := pprGraph(t, []GoGraphFileInput{input})
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Lone behavior", nil)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "Fix Lone behavior", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != 1 || provenance.Ranks[0].Score != goPPRScale {
		t.Fatalf("dangling seed did not retain full mass: %+v", provenance)
	}
	if !provenance.Converged || provenance.Iterations != 1 {
		t.Fatalf("degenerate walk must converge immediately: %+v", provenance)
	}
}

// TestPPRPermutationTiesRequireExactPathOrder builds a symmetric two-seed
// graph twice with reversed input order: scores stay tied and the exact path
// breaks the tie identically, with identical provenance bytes.
func TestPPRPermutationTiesRequireExactPathOrder(t *testing.T) {
	_, producer := pprFixtureIDs()
	aSource := "package pkg\nfunc Alpha() {}\n"
	bSource := "package pkg\nfunc Beta() {}\n"
	a := graphInput("pkg/a.go", aSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Alpha", Kind: "function_declaration", Range: graphSpan(aSource, "Alpha", 0)}}, nil, nil, nil, producer)
	b := graphInput("pkg/b.go", bSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Beta", Kind: "function_declaration", Range: graphSpan(bSource, "Beta", 0)}}, nil, nil, nil, producer)
	first := pprGraph(t, []GoGraphFileInput{a, b})
	second := pprGraph(t, []GoGraphFileInput{b, a})
	if first.Digest != second.Digest {
		t.Fatal("graph depends on input order")
	}
	seeds := GoPPRSeeds{Files: []string{"pkg/b.go", "pkg/a.go"}}
	firstRanks, err := ComputeGoPPR(first, seeds, "symmetric", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondRanks, err := ComputeGoPPR(second, GoPPRSeeds{Files: []string{"pkg/a.go", "pkg/b.go"}}, "symmetric", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstRanks, secondRanks) {
		t.Fatalf("PPR depends on seed input order: %+v vs %+v", firstRanks, secondRanks)
	}
	if len(firstRanks.Ranks) != 2 || firstRanks.Ranks[0].Score != firstRanks.Ranks[1].Score || firstRanks.Ranks[0].Path != "pkg/a.go" {
		t.Fatalf("symmetric tie not broken by exact path: %+v", firstRanks.Ranks)
	}
	pprMass(t, firstRanks.Ranks, goPPRScale)
}

// TestPPRSeedsRequireFullIdentifiers rejects substring, prefix and
// case-folded guessing while admitting exact identifier and exact path cues.
func TestPPRSeedsRequireFullIdentifiers(t *testing.T) {
	graph := pprTwoFileGraph(t)
	for _, test := range []struct {
		name      string
		objective string
		want      []string
	}{
		{"exact identifier", "Fix Alpha behavior", []string{"pkg/a.go"}},
		{"wrong case is not a seed", "fix alpha behavior", nil},
		{"prefix is not a seed", "Fix Alph now", nil},
		{"substring is not a seed", "Fix lpha now", nil},
		{"exact path cue", "See pkg/b.go for context", []string{"pkg/b.go"}},
		{"unrelated query", "unrelated task description", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			seeds, err := DeriveGoPPRSeeds(graph, test.objective, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(test.want) == 0 && len(seeds.Files) != 0 {
				t.Fatalf("guessed seeds for %q: %+v", test.objective, seeds.Files)
			}
			if len(test.want) != 0 && !reflect.DeepEqual(seeds.Files, test.want) {
				t.Fatalf("seed mismatch for %q: got %+v want %+v", test.objective, seeds.Files, test.want)
			}
		})
	}
	changed, err := DeriveGoPPRSeeds(graph, "unrelated task description", []string{"pkg/b.go"})
	if err != nil || !reflect.DeepEqual(changed.Files, []string{"pkg/b.go"}) {
		t.Fatalf("changed path did not seed: %+v err=%v", changed, err)
	}
	if _, err := DeriveGoPPRSeeds(graph, "unrelated task description", []string{"pkg/missing.go"}); err == nil {
		t.Fatal("absent changed path seeded silently")
	}
	if _, err := DeriveGoPPRSeeds(graph, "unrelated task description", []string{"pkg/b.go", "pkg/b.go"}); err == nil {
		t.Fatal("duplicate changed path accepted")
	}
}

// TestPPREmptySeedsReturnExplicitNoSignal verifies the safe fallback:
// no error, no ranks, explicit empty_seeds provenance.
func TestPPREmptySeedsReturnExplicitNoSignal(t *testing.T) {
	graph := pprTwoFileGraph(t)
	seeds, err := DeriveGoPPRSeeds(graph, "unrelated task description", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds.Files) != 0 {
		t.Fatalf("unexpected seeds: %+v", seeds)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "unrelated task description", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !provenance.NoSignal || provenance.FallbackReason != "empty_seeds" || len(provenance.Ranks) != 0 || provenance.WorkExhausted {
		t.Fatalf("no-signal fallback misreported: %+v", provenance)
	}
	if provenance.RankHash != goPPRRankHash(nil) || provenance.ProjectionHash == "" {
		t.Fatalf("no-signal provenance lacks identity: %+v", provenance)
	}
	if err := ValidateGoPPRProvenance(graph, "unrelated task description", provenance); err != nil {
		t.Fatalf("no-signal provenance failed validation: %v", err)
	}
}

// TestPPRDisconnectedSeedRetainsMajority checks an isolated seed with no
// shared relations: the walk is valid, mass conserved, seed first.
func TestPPRDisconnectedSeedRetainsMajority(t *testing.T) {
	_, producer := pprFixtureIDs()
	aSource := "package aaa\nfunc Aaa() {}\n"
	bSource := "package bbb\nfunc Bbb() {}\n"
	a := graphInput("aaa.go", aSource, pprBinding("example.com/m/aaa"), []GoSymbol{{Name: "Aaa", Kind: "function_declaration", Range: graphSpan(aSource, "Aaa", 0)}}, nil, nil, nil, producer)
	b := graphInput("bbb.go", bSource, pprBinding("example.com/m/bbb"), []GoSymbol{{Name: "Bbb", Kind: "function_declaration", Range: graphSpan(bSource, "Bbb", 0)}}, nil, nil, nil, producer)
	graph := pprGraph(t, []GoGraphFileInput{a, b})
	for _, edge := range graph.Edges {
		if edge.Relation != "IN_PACKAGE" && edge.Relation != "DECLARES" {
			t.Fatalf("unexpected cross-file relation in disconnected fixture: %+v", edge)
		}
	}
	provenance, err := ComputeGoPPR(graph, GoPPRSeeds{Files: []string{"aaa.go"}}, "Fix Aaa now", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != 2 || provenance.Ranks[0].Path != "aaa.go" {
		t.Fatalf("disconnected seed lost: %+v", provenance)
	}
	pprMass(t, provenance.Ranks, goPPRScale)
}

// TestPPRHubOutranksLeaves builds an importer hub over two packages and
// seeds the hub: the hub must rank first with mass conserved.
func TestPPRHubOutranksLeaves(t *testing.T) {
	_, producer := pprFixtureIDs()
	aSource := "package aaa\nfunc Aaa() {}\n"
	bSource := "package bbb\nfunc Bbb() {}\n"
	hubSource := "package hub\nimport \"example.com/m/aaa\"\nimport \"example.com/m/bbb\"\nfunc Hub() {}\n"
	a := graphInput("aaa.go", aSource, pprBinding("example.com/m/aaa"), []GoSymbol{{Name: "Aaa", Kind: "function_declaration", Range: graphSpan(aSource, "Aaa", 0)}}, nil, nil, nil, producer)
	b := graphInput("bbb.go", bSource, pprBinding("example.com/m/bbb"), []GoSymbol{{Name: "Bbb", Kind: "function_declaration", Range: graphSpan(bSource, "Bbb", 0)}}, nil, nil, nil, producer)
	hub := graphInput("hub.go", hubSource, pprBinding("example.com/m/hub"), []GoSymbol{{Name: "Hub", Kind: "function_declaration", Range: graphSpan(hubSource, "Hub", 0)}},
		[]GoImport{{Path: "example.com/m/aaa", Range: graphSpan(hubSource, `"example.com/m/aaa"`, 0)}, {Path: "example.com/m/bbb", Range: graphSpan(hubSource, `"example.com/m/bbb"`, 0)}}, nil, nil, producer)
	graph := pprGraph(t, []GoGraphFileInput{a, b, hub})
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Hub wiring", nil)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "Fix Hub wiring", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != 3 || provenance.Ranks[0].Path != "hub.go" {
		t.Fatalf("hub seed did not outrank leaves: %+v", provenance)
	}
	pprMass(t, provenance.Ranks, goPPRScale)
}

// TestPPRCyclePropagatesAroundRing checks a three-file package clique seeded
// once: the seed stays first, others tie-break by path, mass conserved.
func TestPPRCyclePropagatesAroundRing(t *testing.T) {
	_, producer := pprFixtureIDs()
	inputs := make([]GoGraphFileInput, 0, 3)
	for _, name := range []string{"C1", "C2", "C3"} {
		source := "package ring\nfunc " + name + "() {}\n"
		inputs = append(inputs, graphInput("ring/"+name+".go", source, pprBinding("example.com/m/ring"), []GoSymbol{{Name: name, Kind: "function_declaration", Range: graphSpan(source, name, 0)}}, nil, nil, nil, producer))
	}
	graph := pprGraph(t, inputs)
	provenance, err := ComputeGoPPR(graph, GoPPRSeeds{Files: []string{"ring/C2.go"}}, "ring", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != 3 || provenance.Ranks[0].Path != "ring/C2.go" {
		t.Fatalf("ring seed lost: %+v", provenance)
	}
	pprMass(t, provenance.Ranks, goPPRScale)
}

// TestPPRProjectionIgnoresUnresolvedCalls proves CALLS_UNRESOLVED containment
// and cross-file spelling coincidences never create projection edges.
func TestPPRProjectionIgnoresUnresolvedCalls(t *testing.T) {
	_, producer := pprFixtureIDs()
	callerSource := "package caller\nfunc Caller() { Use() }\n"
	targetSource := "package target\nfunc Use() {}\n"
	callerCall := graphSpan(callerSource, "Use", strings.Index(callerSource, "{"))
	caller := graphInput("caller.go", callerSource, pprBinding("example.com/m/caller"), []GoSymbol{{Name: "Caller", Kind: "function_declaration", Range: graphSpan(callerSource, "Caller", 0)}}, nil, []GoCall{{Spelling: "Use", Resolution: "UNRESOLVED", Range: callerCall}}, nil, producer)
	target := graphInput("target.go", targetSource, pprBinding("example.com/m/target"), []GoSymbol{{Name: "Use", Kind: "function_declaration", Range: graphSpan(targetSource, "Use", 0)}}, nil, nil, nil, producer)
	graph := pprGraph(t, []GoGraphFileInput{caller, target})
	found := false
	for _, edge := range graph.Edges {
		if edge.Relation == "CALLS_UNRESOLVED" && edge.Path == "caller.go" {
			found = true
		}
		if edge.Relation == "CALL_NAME_CANDIDATE" {
			t.Fatalf("cross-file spelling gained an advisory target: %+v", edge)
		}
	}
	if !found {
		t.Fatal("fixture lacks the UNRESOLVED call it claims to ignore")
	}
	files, adjacency := goPPRProjection(graph)
	if len(files) != 2 || len(adjacency["caller.go"]) != 0 || len(adjacency["target.go"]) != 0 {
		t.Fatalf("UNRESOLVED call fabricated a projection edge: %v %v", files, adjacency)
	}
}

// TestPPRBoundsAndWorkExhaustion checks hard iteration/rank caps and the
// explicit work-exhausted fallback with provenance.
func TestPPRBoundsAndWorkExhaustion(t *testing.T) {
	_, producer := pprFixtureIDs()
	inputs := make([]GoGraphFileInput, 0, 70)
	paths := make([]string, 0, 70)
	for i := 0; i < 70; i++ {
		path := "pkg/big" + itoa(i) + ".go"
		symbol := "Big" + itoa(i)
		source := "package big\nfunc " + symbol + "() {}\n"
		inputs = append(inputs, graphInput(path, source, pprBinding("example.com/m/big"), []GoSymbol{{Name: symbol, Kind: "function_declaration", Range: graphSpan(source, symbol, 0)}}, nil, nil, nil, producer))
		paths = append(paths, path)
	}
	graph := pprGraph(t, inputs)
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Big0 now", paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds.Files) != goEngineeringMaxSeeds || !seeds.Truncated {
		t.Fatalf("seed truncation misreported: %d truncated=%t", len(seeds.Files), seeds.Truncated)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "Fix Big0 now", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.NoSignal || len(provenance.Ranks) != goPPRMaxRanks || !provenance.RanksTruncated {
		t.Fatalf("rank truncation misreported: %+v", provenance)
	}
	// Truncated durable ranks cannot be assumed to sum to full scale: assert
	// the exact truncated shape instead (ordered, in range, positive partial
	// mass at most scale; equality holds when every dropped rank scores
	// zero, as with isolated files under a uniform seed split).
	// Full-distribution mass conservation is proven by the untruncated
	// orientation/dangling/permutation/disconnected/hub/cycle cases above.
	previous := goPPRScale + 1
	partial := 0
	for _, rank := range provenance.Ranks {
		if rank.Score < 0 || rank.Score > goPPRScale || rank.Score > previous {
			t.Fatalf("truncated ranks not ordered in range: %+v", provenance.Ranks[:4])
		}
		previous = rank.Score
		partial += rank.Score
	}
	if partial <= 0 || partial > goPPRScale {
		t.Fatalf("truncated partial mass outside (0, scale]: %d", partial)
	}
	if provenance.Iterations > goPPRMaxIterations {
		t.Fatalf("iteration bound exceeded: %+v", provenance)
	}
	if err := ValidateGoPPRProvenance(graph, "Fix Big0 now", provenance); err != nil {
		t.Fatalf("truncated provenance failed validation: %v", err)
	}
	exhausted, err := computeGoPPRWithBudget(graph, seeds, "Fix Big0 now", DefaultGoPPRConfig(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !exhausted.NoSignal || !exhausted.WorkExhausted || exhausted.FallbackReason != "work_exhausted" || len(exhausted.Ranks) != 0 {
		t.Fatalf("work exhaustion did not degrade with provenance: %+v", exhausted)
	}
}

// TestPPRProvenanceRejectsSubstitution mutates every bound input and
// requires validation to reject each forgery.
func TestPPRProvenanceRejectsSubstitution(t *testing.T) {
	graph := pprTwoFileGraph(t)
	query := "Fix Alpha behavior"
	seeds, err := DeriveGoPPRSeeds(graph, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := ComputeGoPPR(graph, seeds, query, DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*GoPPRProvenance){
		"rank score":   func(p *GoPPRProvenance) { p.Ranks[0].Score++ },
		"rank order":   func(p *GoPPRProvenance) { p.Ranks[0], p.Ranks[1] = p.Ranks[1], p.Ranks[0] },
		"rank hash":    func(p *GoPPRProvenance) { p.RankHash = strings.Repeat("0", 64) },
		"seeds":        func(p *GoPPRProvenance) { p.Seeds = []string{"pkg/b.go"} },
		"graph":        func(p *GoPPRProvenance) { p.GraphDigest = strings.Repeat("0", 64) },
		"query":        func(p *GoPPRProvenance) { p.QuerySHA256 = strings.Repeat("0", 64) },
		"projection":   func(p *GoPPRProvenance) { p.ProjectionHash = strings.Repeat("0", 64) },
		"iterations":   func(p *GoPPRProvenance) { p.Iterations++ },
		"parameters":   func(p *GoPPRProvenance) { p.AlphaNum = 1 },
		"no-signal":    func(p *GoPPRProvenance) { p.NoSignal = true },
		"fallback":     func(p *GoPPRProvenance) { p.FallbackReason = "forged" },
		"dropped rank": func(p *GoPPRProvenance) { p.Ranks = p.Ranks[:1] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			forged := provenance
			forged.Ranks = append([]GoPPRFileRank(nil), provenance.Ranks...)
			forged.Seeds = append([]string(nil), provenance.Seeds...)
			mutate(&forged)
			if err := ValidateGoPPRProvenance(graph, query, forged); err == nil {
				t.Fatalf("forged PPR provenance accepted (%s): %+v", name, forged)
			}
		})
	}
	if err := ValidateGoPPRProvenance(graph, "forged query", provenance); err == nil {
		t.Fatal("foreign query accepted")
	}
	// A graph with different files must not validate against this provenance.
	biggerSource := "package pkg\nfunc Alpha() {}\nfunc Extra() {}\n"
	_, producer := pprFixtureIDs()
	extra := graphInput("pkg/a.go", biggerSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Alpha", Kind: "function_declaration", Range: graphSpan(biggerSource, "Alpha", 0)}, {Name: "Extra", Kind: "function_declaration", Range: graphSpan(biggerSource, "Extra", strings.Index(biggerSource, "Extra"))}}, nil, nil, nil, producer)
	otherSource := "package pkg\nfunc Beta() {}\n"
	otherFile := graphInput("pkg/b.go", otherSource, pprBinding("example.com/m/pkg"), []GoSymbol{{Name: "Beta", Kind: "function_declaration", Range: graphSpan(otherSource, "Beta", 0)}}, nil, nil, nil, producer)
	foreign := pprGraph(t, []GoGraphFileInput{extra, otherFile})
	if err := ValidateGoPPRProvenance(foreign, query, provenance); err == nil {
		t.Fatal("foreign graph accepted")
	}
	if _, err := ComputeGoPPR(graph, seeds, query, GoPPRConfig{}); err == nil {
		t.Fatal("zero PPR parameters accepted")
	}
	if _, err := ComputeGoPPR(graph, GoPPRSeeds{Files: []string{"pkg/missing.go"}}, query, DefaultGoPPRConfig()); err == nil {
		t.Fatal("seed absent from projection accepted")
	}
	if _, err := DeriveGoPPRSeeds(graph, "", nil); err == nil {
		t.Fatal("empty query accepted")
	}
}

// pprFlipGraph builds a hub topology where two changed files tie on legacy
// selection scores: both carry changed_path weight with no lexical
// differentiator, so legacy selection breaks the tie by exact path and picks
// aaa.go. The PPR walk from the same changed seeds ranks the hub first.
func pprFlipGraph(t *testing.T) (GoEngineeringGraph, []taskcontext.File) {
	t.Helper()
	producer := strings.Repeat("7", 64)
	sourceID := strings.Repeat("8", 64)
	candidateID := strings.Repeat("9", 64)
	aSource := "package aaa\nfunc Aaa() {}\n"
	bSource := "package bbb\nfunc Bbb() {}\n"
	hubSource := "package hub\nimport \"example.com/m/aaa\"\nimport \"example.com/m/bbb\"\nfunc Hub() {}\n"
	a := graphInput("aaa.go", aSource, pprBinding("example.com/m/aaa"), []GoSymbol{{Name: "Aaa", Kind: "function_declaration", Range: graphSpan(aSource, "Aaa", 0)}}, nil, nil, nil, producer)
	b := graphInput("bbb.go", bSource, pprBinding("example.com/m/bbb"), []GoSymbol{{Name: "Bbb", Kind: "function_declaration", Range: graphSpan(bSource, "Bbb", 0)}}, nil, nil, nil, producer)
	hub := graphInput("hub.go", hubSource, pprBinding("example.com/m/hub"), []GoSymbol{{Name: "Hub", Kind: "function_declaration", Range: graphSpan(hubSource, "Hub", 0)}},
		[]GoImport{{Path: "example.com/m/aaa", Range: graphSpan(hubSource, `"example.com/m/aaa"`, 0)}, {Path: "example.com/m/bbb", Range: graphSpan(hubSource, `"example.com/m/bbb"`, 0)}}, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, CandidateID: candidateID, ProducerSHA256: producer, Files: []GoGraphFileInput{a, b, hub}})
	if err != nil {
		t.Fatal(err)
	}
	files := []taskcontext.File{}
	for _, in := range []GoGraphFileInput{a, b, hub} {
		files = append(files, taskcontext.File{Path: in.Facts.Path, Hash: in.Facts.SourceSHA256, Content: in.Source})
	}
	return graph, files
}

// TestPPRVisibleContextFollowsRankOrder compiles one hub graph twice under
// identical tight file/byte bounds: legacy selection breaks the changed-path
// tie lexically (aaa.go) while the PPR treatment breaks the same tie by
// advisory rank (hub.go). Only the treatment differs; excerpt bytes,
// symbol/span visibility and all bounds are unchanged.
func TestPPRVisibleContextFollowsRankOrder(t *testing.T) {
	graph, files := pprFlipGraph(t)
	limits := taskcontext.DefaultLimits()
	limits.MaxFiles = 1
	base := GoContextInput{ContextVersion: 2, SourceID: graph.SourceID, Graph: graph, Objective: "update wiring", Files: files, ChangedPaths: []string{"aaa.go", "hub.go"}, Limits: limits}
	legacy, err := CompileGoContext(base)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.PPR != nil {
		t.Fatal("legacy compilation carries PPR treatment")
	}
	if len(legacy.Selection.Selected) != 1 || legacy.Selection.Selected[0].Path != "aaa.go" {
		t.Fatalf("legacy tie not broken lexically: %+v", legacy.Selection.Selected)
	}
	withPPR := base
	withPPR.PPR = &GoPPRRequest{}
	ranked, err := CompileGoContext(withPPR)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.PPR == nil || ranked.PPR.NoSignal {
		t.Fatal("PPR treatment missing from ranked compilation")
	}
	if !reflect.DeepEqual(ranked.PPR.Seeds, []string{"aaa.go", "hub.go"}) {
		t.Fatalf("PPR seeds are not the observed changed paths: %+v", ranked.PPR.Seeds)
	}
	if len(ranked.PPR.Ranks) != 3 || ranked.PPR.Ranks[0].Path != "hub.go" {
		t.Fatalf("hub did not outrank the leaf: %+v", ranked.PPR.Ranks)
	}
	if len(ranked.Selection.Selected) != 1 || ranked.Selection.Selected[0].Path != "hub.go" {
		t.Fatalf("PPR rank order did not change the visible file: %+v", ranked.Selection.Selected)
	}
	// The treatment changes which bounded excerpt is visible, not the
	// excerpt mechanics: same reason family, safe pivots, same byte budget.
	if ranked.Selection.Selected[0].Reason != "changed_path" {
		t.Fatalf("PPR invented excerpt evidence: %+v", ranked.Selection.Selected[0])
	}
	if ranked.Selection.SelectedBytes > limits.MaxBytes {
		t.Fatal("PPR selection exceeded the identical byte bound")
	}
	again, err := CompileGoContext(withPPR)
	if err != nil || again.Digest != ranked.Digest {
		t.Fatal("PPR compilation is not deterministic")
	}
}

// TestPPRNoSignalKeepsLegacySelection proves an unrelated query keeps the
// current selector byte-for-byte while retaining explicit no-signal
// provenance: the treatment degrades, it never aborts or invents evidence.
func TestPPRNoSignalKeepsLegacySelection(t *testing.T) {
	graph, files := pprFlipGraph(t)
	base := GoContextInput{ContextVersion: 2, SourceID: graph.SourceID, Graph: graph, Objective: "unrelated task description", Files: files, Limits: taskcontext.DefaultLimits()}
	legacy, err := CompileGoContext(base)
	if err != nil {
		t.Fatal(err)
	}
	withPPR := base
	withPPR.PPR = &GoPPRRequest{}
	ranked, err := CompileGoContext(withPPR)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.PPR == nil || !ranked.PPR.NoSignal || ranked.PPR.FallbackReason != "empty_seeds" {
		t.Fatalf("no-signal provenance missing: %+v", ranked.PPR)
	}
	if !reflect.DeepEqual(ranked.Selection, legacy.Selection) {
		t.Fatal("no-signal PPR changed the legacy selection")
	}
}

// TestPPRObjectiveDerivedSeedsChangeVisibleSelection proves the planner seam
// (no changed paths, empty candidate ID) can change the visible excerpt
// through objective-derived seeds alone. Three files share one exact
// identifier Foo with identical lexical/path evidence, so legacy selection
// ties and picks aaa.go; the PPR walk from the same observed seeds ranks the
// import hub first and the same bounded selector then shows hub.go. Only the
// treatment differs; reasons stay in the same positive-evidence family and
// all file/byte bounds hold.
func TestPPRObjectiveDerivedSeedsChangeVisibleSelection(t *testing.T) {
	sourceID, producer := pprFixtureIDs()
	aSrc := "package aaa\nfunc Foo() {}\n"
	zSrc := "package zzz\nfunc Foo() {}\n"
	hubSrc := "package hub\nimport \"example.com/m/aaa\"\nimport \"example.com/m/zzz\"\nfunc Foo() {}\n"
	a := graphInput("aaa.go", aSrc, pprBinding("example.com/m/aaa"), []GoSymbol{{Name: "Foo", Kind: "function_declaration", Range: graphSpan(aSrc, "Foo", 0)}}, nil, nil, nil, producer)
	z := graphInput("zzz.go", zSrc, pprBinding("example.com/m/zzz"), []GoSymbol{{Name: "Foo", Kind: "function_declaration", Range: graphSpan(zSrc, "Foo", 0)}}, nil, nil, nil, producer)
	hub := graphInput("hub.go", hubSrc, pprBinding("example.com/m/hub"), []GoSymbol{{Name: "Foo", Kind: "function_declaration", Range: graphSpan(hubSrc, "Foo", 0)}},
		[]GoImport{{Path: "example.com/m/aaa", Range: graphSpan(hubSrc, `"example.com/m/aaa"`, 0)}, {Path: "example.com/m/zzz", Range: graphSpan(hubSrc, `"example.com/m/zzz"`, 0)}}, nil, nil, producer)
	graph := pprGraph(t, []GoGraphFileInput{a, z, hub})
	if graph.CandidateID != "" {
		t.Fatalf("planner-shaped graph must carry an empty candidate ID: %q", graph.CandidateID)
	}
	files := []taskcontext.File{}
	for _, in := range []GoGraphFileInput{a, z, hub} {
		files = append(files, taskcontext.File{Path: in.Facts.Path, Hash: in.Facts.SourceSHA256, Content: in.Source})
	}
	_ = sourceID
	objective := "Fix Foo wiring"
	derived, err := DeriveGoPPRSeeds(graph, objective, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(derived.Files, []string{"aaa.go", "hub.go", "zzz.go"}) || derived.Truncated {
		t.Fatalf("objective-derived seeds mismatch: %+v", derived)
	}
	limits := taskcontext.DefaultLimits()
	limits.MaxFiles = 1
	base := GoContextInput{ContextVersion: 2, SourceID: graph.SourceID, Graph: graph, Objective: objective, Files: files, Limits: limits}
	if len(base.ChangedPaths) != 0 {
		t.Fatal("planner seam must compile with no changed paths")
	}
	legacy, err := CompileGoContext(base)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.PPR != nil {
		t.Fatal("legacy compilation carries PPR treatment")
	}
	if len(legacy.Selection.Selected) != 1 || legacy.Selection.Selected[0].Path != "aaa.go" {
		t.Fatalf("legacy tie not broken lexically: %+v", legacy.Selection.Selected)
	}
	if legacy.Selection.Selected[0].Reason != "path_hint" {
		t.Fatalf("legacy tie lacks positive evidence: %+v", legacy.Selection.Selected[0])
	}
	withPPR := base
	withPPR.PPR = &GoPPRRequest{}
	ranked, err := CompileGoContext(withPPR)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.PPR == nil || ranked.PPR.NoSignal {
		t.Fatal("PPR treatment missing from ranked compilation")
	}
	if !reflect.DeepEqual(ranked.PPR.Seeds, []string{"aaa.go", "hub.go", "zzz.go"}) {
		t.Fatalf("ranked seeds are not the observed objective-derived set: %+v", ranked.PPR.Seeds)
	}
	if len(ranked.PPR.Ranks) != 3 || ranked.PPR.Ranks[0].Path != "hub.go" {
		t.Fatalf("hub did not outrank the leaves: %+v", ranked.PPR.Ranks)
	}
	pprMass(t, ranked.PPR.Ranks, goPPRScale)
	if len(ranked.Selection.Selected) != 1 || ranked.Selection.Selected[0].Path != "hub.go" {
		t.Fatalf("PPR rank order did not change the visible file: %+v", ranked.Selection.Selected)
	}
	if ranked.Selection.Selected[0].Path == legacy.Selection.Selected[0].Path {
		t.Fatal("visible selection did not change")
	}
	if ranked.Selection.Selected[0].Reason != legacy.Selection.Selected[0].Reason {
		t.Fatalf("PPR invented excerpt evidence: %q vs %q", ranked.Selection.Selected[0].Reason, legacy.Selection.Selected[0].Reason)
	}
	for _, selected := range append(append([]taskcontext.SelectedFile(nil), legacy.Selection.Selected...), ranked.Selection.Selected...) {
		if len(selected.Ranks) != 0 || len(selected.Covered) != 0 {
			t.Fatal("selection carries experimental selector provenance")
		}
	}
	if ranked.Selection.SelectedBytes > limits.MaxBytes || len(ranked.Selection.Selected) > limits.MaxFiles {
		t.Fatal("PPR selection exceeded the identical file/byte bounds")
	}
	again, err := CompileGoContext(withPPR)
	if err != nil || again.Digest != ranked.Digest {
		t.Fatal("PPR compilation is not deterministic")
	}
}

// TestPPRConvergenceFlagIsFiniteThreshold pins the converged flag to its
// honest meaning: L1 movement at or below epsilon when the walk stops. A
// single-iteration budget on the two-file clique still moves mass far above
// epsilon (unconverged), while the full fixed budget settles below it.
func TestPPRConvergenceFlagIsFiniteThreshold(t *testing.T) {
	graph := pprTwoFileGraph(t)
	query := "Fix Alpha behavior"
	seeds, err := DeriveGoPPRSeeds(graph, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	single, err := computeGoPPRWithBudget(graph, seeds, query, DefaultGoPPRConfig(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if single.WorkExhausted || single.NoSignal || single.Iterations != 1 || single.Converged {
		t.Fatalf("single step must run once unconverged: %+v", single)
	}
	full, err := ComputeGoPPR(graph, seeds, query, DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !full.Converged || full.Iterations < 1 || full.Iterations > goPPRMaxIterations {
		t.Fatalf("full walk did not report finite convergence: %+v", full)
	}
	pprMass(t, full.Ranks, goPPRScale)
}

// TestPPRSelfConsistentForgeKeepsDerivedSeeds shows the forge that planner
// admission must reject: a different valid seed set recomputed honestly
// passes self-consistency validation, yet its seeds differ from the observed
// query-derived seeds. Planner admission pins the derived set (see control
// validatePlannerGoPPRSeeds); this test fixes the forge shape it rejects.
func TestPPRSelfConsistentForgeKeepsDerivedSeeds(t *testing.T) {
	graph := pprTwoFileGraph(t)
	query := "Fix Alpha behavior"
	derived, err := DeriveGoPPRSeeds(graph, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(derived.Files, []string{"pkg/a.go"}) || derived.Truncated {
		t.Fatalf("observed seeds changed: %+v", derived)
	}
	forged, err := ComputeGoPPR(graph, GoPPRSeeds{Files: []string{"pkg/b.go"}}, query, DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Honestly recomputed from its own seeds: self-consistent by construction.
	if err := ValidateGoPPRProvenance(graph, query, forged); err != nil {
		t.Fatalf("honest recomputation failed self-consistency: %v", err)
	}
	if reflect.DeepEqual(forged.Seeds, derived.Files) {
		t.Fatal("forge accidentally matches the observed seeds")
	}
}

// TestPPROversizedPackageProjectsSparse proves package groups above the
// clique limit gain no intra-package edges: no synthetic hub, no fabricated
// equivalence. Members stay reachable only through other admitted relations.
func TestPPROversizedPackageProjectsSparse(t *testing.T) {
	_, producer := pprFixtureIDs()
	inputs := make([]GoGraphFileInput, 0, goPPRCliqueLimit+1)
	for i := 0; i <= goPPRCliqueLimit; i++ {
		path := "pkg/sparse" + itoa(i) + ".go"
		symbol := "Sparse" + itoa(i)
		source := "package big\nfunc " + symbol + "() {}\n"
		inputs = append(inputs, graphInput(path, source, pprBinding("example.com/m/big"), []GoSymbol{{Name: symbol, Kind: "function_declaration", Range: graphSpan(source, symbol, 0)}}, nil, nil, nil, producer))
	}
	graph := pprGraph(t, inputs)
	files, adjacency := goPPRProjection(graph)
	if len(files) != goPPRCliqueLimit+1 {
		t.Fatalf("sparse projection dropped files: %d", len(files))
	}
	for _, path := range files {
		if len(adjacency[path]) != 0 {
			t.Fatalf("oversized package member gained a projection edge: %s -> %v", path, adjacency[path])
		}
	}
	// Deterministic across input permutations and still a valid walk input.
	second := pprGraph(t, append(append([]GoGraphFileInput(nil), inputs[1:]...), inputs[0]))
	if graph.Digest != second.Digest {
		t.Fatal("sparse projection depends on input order")
	}
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Sparse0 now", []string{files[0]})
	if err != nil {
		t.Fatal(err)
	}
	provenance, err := ComputeGoPPR(graph, seeds, "Fix Sparse0 now", DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if provenance.Ranks[0].Path != files[0] {
		t.Fatalf("isolated seed lost under sparse projection: %+v", provenance.Ranks[:3])
	}
	if err := ValidateGoPPRProvenance(graph, "Fix Sparse0 now", provenance); err != nil {
		t.Fatalf("sparse provenance failed validation: %v", err)
	}
}

// TestPPRProjectionFileCapExhaustsHonestly proves the pre-adjacency file
// cap: a valid graph one file above goPPRMaxProjectionFiles returns
// explicit graph_size_exhausted no-signal provenance before any dense
// IMPORTS/TESTS neighbor allocation, with deterministic identity that
// admission recomputation validates. It carries zero ranks, mutates neither
// the graph nor the seed input, and keeps the current selector byte-for-byte
// when compiled with the treatment.
func TestPPRProjectionFileCapExhaustsHonestly(t *testing.T) {
	_, producer := pprFixtureIDs()
	count := goPPRMaxProjectionFiles + 1
	inputs := make([]GoGraphFileInput, 0, count)
	paths := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := "pkg/cap" + itoa(i) + ".go"
		symbol := "Cap" + itoa(i)
		source := "package big\nfunc " + symbol + "() {}\n"
		inputs = append(inputs, graphInput(path, source, pprBinding("example.com/m/big"), []GoSymbol{{Name: symbol, Kind: "function_declaration", Range: graphSpan(source, symbol, 0)}}, nil, nil, nil, producer))
		paths = append(paths, path)
	}
	graph := pprGraph(t, inputs)
	if len(graph.Files) != count {
		t.Fatalf("cap fixture lost files: %d", len(graph.Files))
	}
	query := "Fix Cap0 now"
	seeds := GoPPRSeeds{Files: []string{paths[0]}}
	seedsBefore := append([]string(nil), seeds.Files...)
	before, err := canonical.Bytes(graph)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest := graph.Digest
	provenance, err := ComputeGoPPR(graph, seeds, query, DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !provenance.NoSignal || !provenance.WorkExhausted || provenance.FallbackReason != "graph_size_exhausted" || len(provenance.Ranks) != 0 {
		t.Fatalf("size exhaustion misreported: %+v", provenance)
	}
	if provenance.GraphDigest != graph.Digest || provenance.QuerySHA256 == "" || provenance.AlphaNum != goPPRAlphaNum || provenance.AlphaDen != goPPRAlphaDen || provenance.Scale != goPPRScale || provenance.ProjectionHash == "" {
		t.Fatalf("size-exhausted provenance lacks deterministic identity: %+v", provenance)
	}
	if provenance.RankHash != goPPRRankHash(nil) || provenance.Iterations != 0 || provenance.Converged || provenance.RanksTruncated {
		t.Fatalf("size-exhausted outcome carries walk output: %+v", provenance)
	}
	if !reflect.DeepEqual(provenance.Seeds, []string{paths[0]}) {
		t.Fatalf("size-exhausted seeds not preserved: %+v", provenance.Seeds)
	}
	// Admission recomputation validates the fallback from the bound inputs.
	if err := ValidateGoPPRProvenance(graph, query, provenance); err != nil {
		t.Fatalf("size-exhausted provenance failed validation: %v", err)
	}
	// Deterministic across input permutations.
	reversed := append(append([]GoGraphFileInput(nil), inputs[1:]...), inputs[0])
	second := pprGraph(t, reversed)
	if second.Digest != graph.Digest {
		t.Fatal("cap fixture depends on input order")
	}
	again, err := ComputeGoPPR(second, seeds, query, DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, provenance) {
		t.Fatal("size-exhausted provenance is not deterministic")
	}
	// No input mutation.
	after, err := canonical.Bytes(graph)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Digest != beforeDigest || string(before) != string(after) {
		t.Fatal("size cap mutated the admitted graph")
	}
	if !reflect.DeepEqual(seeds.Files, seedsBefore) {
		t.Fatal("size cap mutated the seed input")
	}
	// Invalid graph identity still rejects instead of returning success.
	tampered := graph
	tampered.Digest = strings.Repeat("0", 64)
	if _, err := ComputeGoPPR(tampered, seeds, query, DefaultGoPPRConfig()); err == nil {
		t.Fatal("tampered graph returned size-exhausted success")
	}
	// The treatment degrades to the current selector: same bounded excerpts.
	files := make([]taskcontext.File, 0, len(inputs))
	for _, in := range inputs {
		files = append(files, taskcontext.File{Path: in.Facts.Path, Hash: in.Facts.SourceSHA256, Content: in.Source})
	}
	base := GoContextInput{ContextVersion: 2, SourceID: graph.SourceID, Graph: graph, Objective: query, Files: files, Limits: taskcontext.DefaultLimits()}
	legacy, err := CompileGoContext(base)
	if err != nil {
		t.Fatal(err)
	}
	withPPR := base
	withPPR.PPR = &GoPPRRequest{}
	ranked, err := CompileGoContext(withPPR)
	if err != nil {
		t.Fatal(err)
	}
	if ranked.PPR == nil || !ranked.PPR.NoSignal || !ranked.PPR.WorkExhausted || ranked.PPR.FallbackReason != "graph_size_exhausted" {
		t.Fatalf("compiled treatment lost size exhaustion: %+v", ranked.PPR)
	}
	if !reflect.DeepEqual(ranked.Selection, legacy.Selection) {
		t.Fatal("size-exhausted PPR changed the legacy selection")
	}
}

// TestPPRLeavesGraphAndSourceUntouched proves the walk never mutates its
// admitted inputs.
func TestPPRLeavesGraphAndSourceUntouched(t *testing.T) {
	graph := pprTwoFileGraph(t)
	before, err := canonical.Bytes(graph)
	if err != nil {
		t.Fatal(err)
	}
	beforeDigest := graph.Digest
	seeds, err := DeriveGoPPRSeeds(graph, "Fix Alpha behavior", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeGoPPR(graph, seeds, "Fix Alpha behavior", DefaultGoPPRConfig()); err != nil {
		t.Fatal(err)
	}
	after, err := canonical.Bytes(graph)
	if err != nil {
		t.Fatal(err)
	}
	if graph.Digest != beforeDigest || string(before) != string(after) {
		t.Fatal("PPR mutated the admitted graph")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
