package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func contextFixture(t *testing.T) GoContextInput {
	t.Helper()
	producer := strings.Repeat("a", 64)
	source := []byte("package p\nfunc F() {}\n")
	hash := sha256.Sum256(source)
	facts := fixtureGoFacts("pkg/file.go", hex.EncodeToString(hash[:]), producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("c", 64), CandidateID: strings.Repeat("b", 64), ProducerSHA256: producer, Files: []GoGraphFileInput{{Facts: facts, Source: source, Package: GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return GoContextInput{SourceID: strings.Repeat("c", 64), Graph: graph, Objective: "Fix F", Files: []taskcontext.File{{Path: facts.Path, Hash: facts.SourceSHA256, Content: source}}, Limits: taskcontext.DefaultLimits()}
}

func TestCompileGoContextBindsExactGraphSourceAndQuery(t *testing.T) {
	input := contextFixture(t)
	first, err := CompileGoContext(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompileGoContext(input)
	if err != nil || first.Digest != second.Digest {
		t.Fatalf("nondeterministic manifest: %v", err)
	}
	if first.Coverage != "PARTIAL" || first.GraphDigest != input.Graph.Digest || len(first.Selection.Selected) != 1 || len(first.Symbols) != 1 || first.Symbols[0].Label != "F" {
		t.Fatalf("incorrect evidence: %+v", first)
	}
	if first.Digest != "b5fce9e45c2899d7f0b1f15045527efde6299d7917f5741142712d786cfb922a" {
		t.Fatalf("legacy context fixture digest changed: %s", first.Digest)
	}
	before := input.Objective
	input.Objective = "Explain F"
	changed, err := CompileGoContext(input)
	if err != nil || first.QuerySHA256 == changed.QuerySHA256 || first.Digest == changed.Digest {
		t.Fatalf("query not bound: %v", err)
	}
	input.Objective = before
	input.Files[0].Content = []byte("dirty working tree")
	if _, err := CompileGoContext(input); err == nil {
		t.Fatal("unbound source accepted")
	}
}

func TestCompileGoContextRejectsMissingDuplicateCorruptAndForeignEvidence(t *testing.T) {
	for _, which := range []string{"source ID", "missing", "duplicate", "graph digest", "foreign changed path"} {
		t.Run(which, func(t *testing.T) {
			input := contextFixture(t)
			switch which {
			case "source ID":
				input.SourceID = "unbound"
			case "missing":
				input.Files = nil
			case "duplicate":
				input.Files = append(input.Files, input.Files[0])
			case "graph digest":
				input.Graph.Digest = strings.Repeat("d", 64)
			case "foreign changed path":
				input.ChangedPaths = []string{"foreign.go"}
			}
			if _, err := CompileGoContext(input); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
}

func TestGoContextDigestAndReturnedValuesAreIndependent(t *testing.T) {
	input := contextFixture(t)
	manifest, err := CompileGoContext(input)
	if err != nil {
		t.Fatal(err)
	}
	digest := manifest.Digest
	manifest.Digest = ""
	calculated, err := canonical.Hash("harness.ri.go-context.v1", manifest)
	if err != nil || calculated != digest {
		t.Fatalf("manifest digest mismatch: %v", err)
	}
	manifest.Symbols[0].Range.StartByte++
	if err := ValidateGoEngineeringGraph(input.Graph); err != nil {
		t.Fatalf("returned ranges mutated graph: %v", err)
	}
}

func TestCompileGoContextRetrievesNamedDeclarationAheadOfLexicalNoise(t *testing.T) {
	producer := strings.Repeat("a", 64)
	wanted := "package p\nfunc Target() {}\n"
	noise := "package p\n// Target Target Target Target\nfunc Other() {}\n"
	inputs := []GoGraphFileInput{
		graphInput("src/target.go", wanted, GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}, []GoSymbol{{Name: "Target", Kind: "function_declaration", Range: graphSpan(wanted, "Target", 0)}}, nil, nil, nil, producer),
		graphInput("src/noise.go", noise, GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}, []GoSymbol{{Name: "Other", Kind: "function_declaration", Range: graphSpan(noise, "Other", 0)}}, nil, nil, nil, producer),
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("c", 64), ProducerSHA256: producer, Files: inputs})
	if err != nil {
		t.Fatal(err)
	}
	files := []taskcontext.File{}
	for _, in := range inputs {
		files = append(files, taskcontext.File{Path: in.Facts.Path, Hash: in.Facts.SourceSHA256, Content: in.Source})
	}
	limits := taskcontext.DefaultLimits()
	limits.MaxFiles = 1
	input := GoContextInput{SourceID: graph.SourceID, Graph: graph, Objective: "Fix Target", Files: files, Limits: limits}
	manifest, err := CompileGoContext(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Selection.Selected) != 1 || manifest.Selection.Selected[0].Path != "src/target.go" || len(manifest.Symbols) != 1 || manifest.Symbols[0].Label != "Target" || manifest.Selection.OmittedCount != 1 {
		t.Fatalf("retrieval ground truth mismatch: %+v", manifest)
	}
	input.Files[0], input.Files[1] = input.Files[1], input.Files[0]
	reordered, err := CompileGoContext(input)
	if err != nil || reordered.Digest != manifest.Digest {
		t.Fatalf("corpus order changed selection: %v", err)
	}
	if manifest.Selection.Scope.CandidateID != "" {
		t.Fatal("base context falsely claims candidate identity")
	}
}

func TestCompileGoContextAnchorsLateUnicodeDeclaration(t *testing.T) {
	producer := strings.Repeat("a", 64)
	source := "package p\n" + strings.Repeat("// unrelated padding\n", 3000) + "func Cafés() {}\n"
	file := graphInput("src/late.go", source, GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}, []GoSymbol{{Name: "Cafés", Kind: "function_declaration", Range: graphSpan(source, "Cafés", 0)}}, nil, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("c", 64), ProducerSHA256: producer, Files: []GoGraphFileInput{file}})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := CompileGoContext(GoContextInput{SourceID: graph.SourceID, Graph: graph, Objective: "Fix Cafés", Files: []taskcontext.File{{Path: file.Facts.Path, Hash: file.Facts.SourceSHA256, Content: file.Source}}, Limits: taskcontext.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Selection.Selected) != 1 || manifest.Selection.Selected[0].Start <= 32<<10 || !strings.Contains(manifest.Selection.Selected[0].Content, "func Cafés") || len(manifest.Symbols) != 1 {
		t.Fatalf("late declaration not visible in compiled context: %+v", manifest)
	}
}

func TestCompileGoContextTruncatesExcessDeclarationHints(t *testing.T) {
	producer := strings.Repeat("a", 64)
	source := []byte("package p\nfunc F() {}\n")
	sum := sha256.Sum256(source)
	files := make([]GoGraphFileInput, 0, 513)
	corpus := make([]taskcontext.File, 0, 513)
	for index := 0; index < 513; index++ {
		path := fmt.Sprintf("src/f%03d.go", index)
		fact := fixtureGoFacts(path, hex.EncodeToString(sum[:]), producer)
		files = append(files, GoGraphFileInput{Facts: fact, Source: source, Package: GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}})
		corpus = append(corpus, taskcontext.File{Path: path, Hash: fact.SourceSHA256, Content: source})
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("c", 64), ProducerSHA256: producer, Files: files})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := CompileGoContext(GoContextInput{SourceID: graph.SourceID, Graph: graph, Objective: "Fix F", Files: corpus, Limits: taskcontext.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.HintsTruncated || len(manifest.Selection.Selected) != 12 {
		t.Fatal("hint overflow was not bounded and recorded")
	}
}

func TestCompileGoContextV2IncludesBoundedCallerChainExcerpts(t *testing.T) {
	producer := strings.Repeat("a", 64)
	var source strings.Builder
	source.WriteString("package p\nfunc SplitLines() {}\n")
	for _, function := range []struct{ name, body string }{
		{"WriteUnifiedDiff", "SplitLines()"},
		{"GetUnifiedDiffString", "WriteUnifiedDiff()"},
		{"ExampleGetUnifiedDiffCode", "GetUnifiedDiffString()"},
	} {
		source.WriteString(strings.Repeat("// unrelated padding keeps caller spans separate\n", 300))
		source.WriteString("func " + function.name + "() { " + function.body + " }\n")
	}
	source.WriteString(strings.Repeat("// another separated wrapper\n", 300))
	source.WriteString("func ExampleOuterWrapper() { ExampleGetUnifiedDiffCode() }\n")
	text := source.String()
	declarations := []GoSymbol{
		{Name: "SplitLines", Kind: "function_declaration", Range: graphSpan(text, "SplitLines", 0)},
		{Name: "WriteUnifiedDiff", Kind: "function_declaration", Range: graphSpan(text, "WriteUnifiedDiff", 0)},
		{Name: "GetUnifiedDiffString", Kind: "function_declaration", Range: graphSpan(text, "GetUnifiedDiffString", 0)},
		{Name: "ExampleGetUnifiedDiffCode", Kind: "function_declaration", Range: graphSpan(text, "ExampleGetUnifiedDiffCode", 0)},
	}
	calls := []GoCall{
		{Spelling: "SplitLines", Resolution: "UNRESOLVED", Range: graphSpan(text, "SplitLines", strings.Index(text, "func WriteUnifiedDiff"))},
		{Spelling: "WriteUnifiedDiff", Resolution: "UNRESOLVED", Range: graphSpan(text, "WriteUnifiedDiff", strings.Index(text, "func GetUnifiedDiffString"))},
		{Spelling: "GetUnifiedDiffString", Resolution: "UNRESOLVED", Range: graphSpan(text, "GetUnifiedDiffString", strings.Index(text, "func ExampleGetUnifiedDiffCode"))},
	}
	file := graphInput("src/diff.go", text, GoPackageBinding{ImportPath: "example/p", ModulePath: "example"}, declarations, nil, calls, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: strings.Repeat("c", 64), ProducerSHA256: producer, Files: []GoGraphFileInput{file}})
	if err != nil {
		t.Fatal(err)
	}
	input := GoContextInput{SourceID: graph.SourceID, Graph: graph, Objective: "Fix SplitLines", Files: []taskcontext.File{{Path: file.Facts.Path, Hash: file.Facts.SourceSHA256, Content: file.Source}}, Limits: taskcontext.DefaultLimits()}
	v1, err := CompileGoContext(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(v1.ContractExcerpts) != 0 {
		t.Fatal("legacy context unexpectedly gained extra excerpts")
	}
	input.ContextVersion = goContextV2
	v2, err := CompileGoContext(input)
	if err != nil {
		t.Fatal(err)
	}
	v2Again, err := CompileGoContext(input)
	if err != nil || v2Again.Digest != v2.Digest {
		t.Fatalf("v2 caller context is nondeterministic: %v", err)
	}
	totalBytes := v2.Selection.SelectedBytes
	foundExample := false
	for _, excerpt := range v2.ContractExcerpts {
		totalBytes += int(excerpt.End - excerpt.Start)
		if excerpt.Reason != "graph_anchor" && excerpt.Reason != "graph_caller" {
			t.Fatalf("unexpected excerpt reason %q", excerpt.Reason)
		}
		if strings.Contains(excerpt.Content, "ExampleGetUnifiedDiffCode") {
			foundExample = true
		}
	}
	if v2.Schema != "engorch.ri.go-context.v2" || !foundExample || len(v2.ContractExcerpts) < 2 || totalBytes > goContextV2MaxPromptBytes || len(v2.ContractExcerpts) > goContextV2MaxExtraExcerpts {
		t.Fatalf("v2 caller chain/budget mismatch: foundExample=%v bytes=%d excerpts=%d", foundExample, totalBytes, len(v2.ContractExcerpts))
	}
	if !v2.FactsTruncated {
		t.Fatal("v2 caller-depth limit did not report a potentially omitted fourth-hop caller")
	}
	for _, relation := range v2.Relations {
		if relation.Range == nil {
			t.Fatal("v2 exposed a relation without a source range")
		}
	}
	for _, symbol := range v2.Symbols {
		if symbol.Label == "ExampleGetUnifiedDiffCode" {
			return
		}
	}
	t.Fatal("v2 did not expose a graph symbol whose range is inside the caller excerpt")
}
