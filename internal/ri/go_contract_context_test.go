package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func TestCompileGoContractContextSelectsTargetAndExampleContract(t *testing.T) {
	producer, sourceID := strings.Repeat("a", 64), strings.Repeat("b", 64)
	mainSource := "package difflib\nfunc SplitLines(s string) []string { return nil }\nfunc WriteUnified(s string) string { return strings.Join(SplitLines(s), \"\\n\") }\n"
	testSource := "package difflib\nfunc ExampleSplitLines() { _ = SplitLines(\"x\") }\n"
	pkg := GoPackageBinding{ImportPath: "example.test/difflib", ModulePath: "example.test"}
	main := graphInput("diff.go", mainSource, pkg, []GoSymbol{{Name: "SplitLines", Kind: "function_declaration", Range: graphSpan(mainSource, "SplitLines", 0)}, {Name: "WriteUnified", Kind: "function_declaration", Range: graphSpan(mainSource, "WriteUnified", 0)}}, nil, []GoCall{{Spelling: "SplitLines", Resolution: "UNRESOLVED", Range: graphSpan(mainSource, "SplitLines", strings.Index(mainSource, "WriteUnified"))}}, nil, producer)
	testFile := graphInput("diff_test.go", testSource, GoPackageBinding{ImportPath: "example.test/difflib_test", ModulePath: "example.test", TestOfImportPath: "example.test/difflib"}, []GoSymbol{{Name: "ExampleSplitLines", Kind: "function_declaration", Test: true, Range: graphSpan(testSource, "ExampleSplitLines", 0)}}, nil, []GoCall{{Spelling: "SplitLines", Resolution: "UNRESOLVED", Range: graphSpan(testSource, "SplitLines", 0)}}, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{main, testFile}})
	if err != nil {
		t.Fatal(err)
	}
	input := GoContractContextInput{SourceID: sourceID, Graph: graph, Objective: "Fix SplitLines behavior", Files: []taskcontext.File{{Path: "diff.go", Hash: main.Facts.SourceSHA256, Content: main.Source}, {Path: "diff_test.go", Hash: testFile.Facts.SourceSHA256, Content: testFile.Source}}}
	first, err := CompileGoContractContext(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompileGoContractContext(input)
	if err != nil || first.Digest != second.Digest {
		t.Fatalf("nondeterministic contract context: %v", err)
	}
	if first.Coverage != "PARTIAL" || first.SourceBytes > goContractContextSourceLimit || len(first.Excerpts) == 0 || len(first.Excerpts) > goContractContextMaxExcerpts {
		t.Fatalf("bad context bounds: %#v", first)
	}
	foundTarget, foundExample, foundUnresolved := false, false, false
	for _, item := range first.Excerpts {
		foundTarget = foundTarget || strings.Contains(item.Content, "func SplitLines")
		foundExample = foundExample || strings.Contains(item.Content, "ExampleSplitLines")
	}
	for _, edge := range first.Relations {
		foundUnresolved = foundUnresolved || edge.Relation == "CALLS_UNRESOLVED" && edge.Resolution == "UNRESOLVED"
	}
	if !foundTarget || !foundExample || !foundUnresolved {
		t.Fatalf("target/test/unresolved evidence missing: %#v", first)
	}
	if err := ValidateGoContractContext(first, input); err != nil {
		t.Fatal(err)
	}
	if len(first.Relations) > 0 {
		forged := first
		forged.Relations = append([]GoGraphEdge(nil), first.Relations...)
		forged.Relations[0].Path = "foreign.go"
		if err := finalizeGoContractContext(&forged); err != nil {
			t.Fatal(err)
		}
		if err := ValidateGoContractContext(forged, input); err == nil {
			t.Fatal("self-rehashed foreign relation accepted")
		}
	}
}

func TestGoContractContextBindsGenerationTemplateAndModule(t *testing.T) {
	input, _ := goContractGenerationFixture(t, false)
	result, err := CompileGoContractContext(input)
	if err != nil {
		t.Fatal(err)
	}
	foundTemplate, foundModule := false, false
	for _, item := range result.Excerpts {
		foundTemplate = foundTemplate || item.Reason == "generation_template"
	}
	for _, module := range result.Modules {
		foundModule = foundModule || module.Ownership.Status == "declared_module"
	}
	if !foundTemplate || !foundModule || result.GenerationBytes > goContractContextGenerationCap {
		t.Fatal("missing precise generation or module evidence")
	}
}

func TestGoContractContextRejectsTamperedSourceAndBounds(t *testing.T) {
	input := contextFixture(t)
	input.ContextVersion = 0
	contractInput := GoContractContextInput{SourceID: input.SourceID, CandidateID: input.Graph.CandidateID, Graph: input.Graph, Objective: "Fix F", Files: input.Files}
	result, err := CompileGoContractContext(contractInput)
	if err != nil {
		t.Fatal(err)
	}
	tampered := result
	tampered.Excerpts = append([]taskcontext.SelectedFile(nil), result.Excerpts...)
	tampered.Excerpts[0].Content = "forged"
	if err := finalizeGoContractContext(&tampered); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoContractContext(tampered, contractInput); err == nil {
		t.Fatal("self-rehashed forged excerpt accepted")
	}
	if len(result.Relations) > 0 {
		tampered = result
		tampered.Relations = append([]GoGraphEdge(nil), result.Relations...)
		tampered.Relations[0].Path = "foreign.go"
		if err := finalizeGoContractContext(&tampered); err != nil {
			t.Fatal(err)
		}
		if err := ValidateGoContractContext(tampered, contractInput); err == nil {
			t.Fatal("self-rehashed foreign relation accepted")
		}
	}
	for name, mutate := range map[string]func(*GoContractContext){
		"truncation": func(value *GoContractContext) { value.Truncated = false },
		"arbitrary admitted span": func(value *GoContractContext) {
			item := &value.Excerpts[0]
			item.Start = 0
			item.End = 7
			item.Content = string(contractInput.Files[0].Content[:7])
			sum := sha256.Sum256([]byte(item.Content))
			item.ExcerptHash = hex.EncodeToString(sum[:])
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := result
			forged.Excerpts = append([]taskcontext.SelectedFile(nil), result.Excerpts...)
			forged.Omissions = append([]taskcontext.Omission(nil), result.Omissions...)
			mutate(&forged)
			if err := finalizeGoContractContext(&forged); err != nil {
				t.Fatal(err)
			}
			if err := ValidateGoContractContext(forged, contractInput); err == nil {
				t.Fatal("self-rehashed deterministic context substitution accepted")
			}
		})
	}
	emptyInput := contractInput
	emptyInput.Objective = "No matching identifier"
	empty, err := CompileGoContractContext(emptyInput)
	if err != nil {
		t.Fatal(err)
	}
	if empty.OmittedCount == 0 {
		t.Fatal("fixture did not produce deterministic omission")
	}
	empty.OmittedCount = 0
	empty.Omissions = []taskcontext.Omission{}
	empty.OmissionsTrimmed = false
	if err := finalizeGoContractContext(&empty); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGoContractContext(empty, emptyInput); err == nil {
		t.Fatal("self-rehashed omission substitution accepted")
	}
	contractInput.Files[0].Content = []byte("package p\nfunc F() { panic(\"substitution\") }\n")
	if _, err := CompileGoContractContext(contractInput); err == nil {
		t.Fatal("source substitution accepted")
	}
}

func TestGoContractContextEmptyFallbackAndPrivacy(t *testing.T) {
	input := contextFixture(t)
	contractInput := GoContractContextInput{SourceID: input.SourceID, CandidateID: input.Graph.CandidateID, Graph: input.Graph, Objective: "No matching identifier", Files: input.Files}
	result, err := CompileGoContractContext(contractInput)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Excerpts) != 0 || result.OmittedCount == 0 {
		t.Fatalf("empty fallback hid omission: %#v", result)
	}
}

func TestGoContractContextSelectsChangedDeclarationWithoutObjectiveName(t *testing.T) {
	producer, sourceID := strings.Repeat("9", 64), strings.Repeat("8", 64)
	source := "package pkg\nfunc ChangedImplementation() {}\n"
	file := graphInput("pkg/changed.go", source, GoPackageBinding{ImportPath: "example.test/pkg", ModulePath: "example.test"}, []GoSymbol{{Name: "ChangedImplementation", Kind: "function_declaration", Range: graphSpan(source, "ChangedImplementation", 0)}}, nil, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, CandidateID: strings.Repeat("7", 64), ProducerSHA256: producer, Files: []GoGraphFileInput{file}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CompileGoContractContext(GoContractContextInput{SourceID: sourceID, CandidateID: graph.CandidateID, Graph: graph, Objective: "Fix generic behavior", ChangedPaths: []string{"pkg/changed.go"}, Files: []taskcontext.File{{Path: file.Facts.Path, Hash: file.Facts.SourceSHA256, Content: file.Source}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Excerpts) != 1 || result.Excerpts[0].Reason != "impact_declaration" || !strings.Contains(result.Excerpts[0].Content, "ChangedImplementation") {
		t.Fatalf("changed declaration was not selected: %#v", result)
	}
}

func TestGoContractContextSelectsOneDeterministicTestExcerptPerPath(t *testing.T) {
	producer, sourceID := strings.Repeat("6", 64), strings.Repeat("5", 64)
	mainSource := "package pkg\nfunc Target() {}\n"
	testSource := "package pkg\nfunc TestTargetOne() { Target() }\nfunc ExampleTarget() { Target() }\n"
	main := graphInput("pkg/main.go", mainSource, GoPackageBinding{ImportPath: "example.test/pkg", ModulePath: "example.test"}, []GoSymbol{{Name: "Target", Kind: "function_declaration", Range: graphSpan(mainSource, "Target", 0)}}, nil, nil, nil, producer)
	testFile := graphInput("pkg/main_test.go", testSource, GoPackageBinding{ImportPath: "example.test/pkg_test", ModulePath: "example.test", TestOfImportPath: "example.test/pkg"}, []GoSymbol{{Name: "TestTargetOne", Kind: "function_declaration", Test: true, Range: graphSpan(testSource, "TestTargetOne", 0)}, {Name: "ExampleTarget", Kind: "function_declaration", Test: true, Range: graphSpan(testSource, "ExampleTarget", 0)}}, nil, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{main, testFile}})
	if err != nil {
		t.Fatal(err)
	}
	input := GoContractContextInput{SourceID: sourceID, Graph: graph, Objective: "Fix Target", Files: []taskcontext.File{{Path: main.Facts.Path, Hash: main.Facts.SourceSHA256, Content: main.Source}, {Path: testFile.Facts.Path, Hash: testFile.Facts.SourceSHA256, Content: testFile.Source}}}
	result, err := CompileGoContractContext(input)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range result.Excerpts {
		if item.Path == "pkg/main_test.go" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("test path should have one deterministic excerpt, got %#v", result.Excerpts)
	}
}

func TestGoContractContextUsesDeepestDeclaredModuleAndKeepsFallbackPartial(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{
		"go.mod":                 "module example.test/root\n\ngo 1.25\n",
		"nested/go.mod":          "module example.test/nested\n\ngo 1.25\n",
		"nested/pkg/contract.go": "package pkg\nfunc Contract() {}\n",
	})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	producer := strings.Repeat("d", 64)
	source := "package pkg\nfunc Contract() {}\n"
	binding, err := DeclaredGoPackageBinding(inventory, "nested/pkg/contract.go", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	file := graphInput("nested/pkg/contract.go", source, binding, []GoSymbol{{Name: "Contract", Kind: "function_declaration", Range: graphSpan(source, "Contract", 0)}}, nil, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{file}, ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CompileGoContractContext(GoContractContextInput{SourceID: sourceID, Graph: graph, ModuleInventory: &inventory, Objective: "Fix Contract", Files: []taskcontext.File{{Path: file.Facts.Path, Hash: file.Facts.SourceSHA256, Content: file.Source}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 1 || result.Modules[0].Ownership.ModuleRoot != "nested" || result.Modules[0].Ownership.ImportPath != "example.test/nested/pkg" {
		t.Fatalf("deepest module was not retained: %#v", result.Modules)
	}
}

func TestGoContractContextDoesNotInventAmbiguousModuleOwnership(t *testing.T) {
	identity := goModuleFixture(t, map[string]string{
		"go.mod":                 "module example.test/root\n\ngo 1.25\n",
		"nested/go.mod":          "module \n",
		"nested/pkg/contract.go": "package pkg\nfunc Contract() {}\n",
	})
	inventory, err := CollectGoModuleInventory(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := identity.ID()
	if err != nil {
		t.Fatal(err)
	}
	producer := strings.Repeat("1", 64)
	source := "package pkg\nfunc Contract() {}\n"
	binding, err := DeclaredGoPackageBinding(inventory, "nested/pkg/contract.go", "pkg")
	if err != nil {
		t.Fatal(err)
	}
	if binding.IdentityKind != "source_local_v1" {
		t.Fatalf("ambiguous manifest invented declared package binding: %#v", binding)
	}
	file := graphInput("nested/pkg/contract.go", source, binding, []GoSymbol{{Name: "Contract", Kind: "function_declaration", Range: graphSpan(source, "Contract", 0)}}, nil, nil, nil, producer)
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{file}, ModuleInventory: &inventory})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CompileGoContractContext(GoContractContextInput{SourceID: sourceID, Graph: graph, ModuleInventory: &inventory, Objective: "Fix Contract", Files: []taskcontext.File{{Path: file.Facts.Path, Hash: file.Facts.SourceSHA256, Content: file.Source}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Modules) != 0 {
		t.Fatalf("ambiguous module ownership was exposed: %#v", result.Modules)
	}
}

func TestGoContractContextEnforcesExcerptAndPrivacyBounds(t *testing.T) {
	producer, sourceID := strings.Repeat("e", 64), strings.Repeat("f", 64)
	inputs := make([]GoGraphFileInput, 0, 7)
	files := make([]taskcontext.File, 0, 7)
	for i := 0; i < 7; i++ {
		path := "pkg/f" + string(rune('a'+i)) + ".go"
		source := "package pkg\nfunc Target() {\n" + strings.Repeat("_ = \"padding\"\n", 300) + "}\n"
		in := graphInput(path, source, GoPackageBinding{ImportPath: "example.test/pkg", ModulePath: "example.test"}, []GoSymbol{{Name: "Target", Kind: "function_declaration", Range: graphSpan(source, "Target", 0)}}, nil, nil, nil, producer)
		inputs = append(inputs, in)
		files = append(files, taskcontext.File{Path: path, Hash: in.Facts.SourceSHA256, Content: in.Source})
	}
	graph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: inputs})
	if err != nil {
		t.Fatal(err)
	}
	result, err := CompileGoContractContext(GoContractContextInput{SourceID: sourceID, Graph: graph, Objective: "Fix Target", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceBytes > goContractContextSourceLimit || len(result.Excerpts) > goContractContextMaxExcerpts || len(result.Excerpts) > goContractContextMaxFiles || result.OmittedCount == 0 {
		t.Fatalf("contract bounds were not recorded: %#v", result)
	}

	secret := graphInput("secrets/private.go", "package secrets\nfunc Target() {}\n", GoPackageBinding{ImportPath: "example.test/secrets", ModulePath: "example.test"}, []GoSymbol{{Name: "Target", Kind: "function_declaration", Range: GoRange{StartByte: 21, EndByte: 27}}}, nil, nil, nil, producer)
	secretGraph, err := BuildGoEngineeringGraph(GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: producer, Files: []GoGraphFileInput{secret}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompileGoContractContext(GoContractContextInput{SourceID: sourceID, Graph: secretGraph, Objective: "Fix Target", Files: []taskcontext.File{{Path: secret.Facts.Path, Hash: secret.Facts.SourceSHA256, Content: secret.Source}}}); err == nil {
		t.Fatal("sensitive source was admitted to contract context")
	}
}
