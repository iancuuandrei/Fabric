package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goContractContextSchema        = "engorch.ri.go-contract-context.v1"
	goContractContextSourceLimit   = 12 << 10
	goContractContextGenerationCap = 4 << 10
	goContractContextMaxExcerpts   = 12
	goContractContextMaxFiles      = 6
	goContractContextMaxExcerpt    = 2 << 10
	goContractContextMaxRelations  = 32
	goContractContextMaxOmissions  = 64
)

// GoContractContextInput supplies already-admitted source and syntax evidence.
// It performs no filesystem, provider, generator, or scanner operation.
type GoContractContextInput struct {
	SourceID        string
	CandidateID     string
	Graph           GoEngineeringGraph
	ModuleInventory *GoModuleInventory
	Generation      *GoGenerationMetadata
	// GenerationFiles optionally supplies the exact source-bound subset used
	// by literal generator bindings. Nil retains the transient metadata corpus.
	GenerationFiles []taskcontext.File
	Objective       string
	Files           []taskcontext.File
	ChangedPaths    []string
}

// GoContractModuleEvidence is declared module ownership for one selected path.
// It is not active Go build resolution.
type GoContractModuleEvidence struct {
	Path      string            `json:"path"`
	Ownership GoModuleOwnership `json:"ownership"`
}

// GoContractContext is compact, source-bound planning evidence. It is always
// PARTIAL: unresolved calls, omitted sources, and absent anchors prove nothing.
type GoContractContext struct {
	Schema                   string                     `json:"schema"`
	Version                  int                        `json:"version"`
	SourceID                 string                     `json:"source_id"`
	CandidateID              string                     `json:"candidate_id,omitempty"`
	GraphDigest              string                     `json:"graph_digest"`
	ProducerSHA256           string                     `json:"producer_sha256"`
	ModuleInventoryDigest    string                     `json:"module_inventory_digest,omitempty"`
	GenerationMetadataDigest string                     `json:"generation_metadata_digest,omitempty"`
	QuerySHA256              string                     `json:"query_sha256"`
	Coverage                 string                     `json:"coverage"`
	Excerpts                 []taskcontext.SelectedFile `json:"excerpts"`
	SourceBytes              int                        `json:"source_bytes"`
	GenerationBytes          int                        `json:"generation_bytes"`
	Relations                []GoGraphEdge              `json:"relations"`
	Modules                  []GoContractModuleEvidence `json:"modules"`
	OmittedCount             int                        `json:"omitted_count"`
	Omissions                []taskcontext.Omission     `json:"omissions"`
	OmissionsTrimmed         bool                       `json:"omissions_trimmed"`
	Truncated                bool                       `json:"truncated"`
	Digest                   string                     `json:"digest"`
}

type goContractAnchor struct {
	path   string
	start  int
	end    int
	reason string
	rank   int
}

// CompileGoContractContext selects exact enclosing declaration and test bodies
// from one already-admitted graph corpus. It deliberately replaces broad prompt
// excerpts; it is not an additional source-context layer.
func CompileGoContractContext(input GoContractContextInput) (GoContractContext, error) {
	return compileGoContractContext(input, true)
}

// compileGoContractContext is the deterministic, side-effect-free selection
// routine. validateResult is false only when validation recomputes the exact
// expected result; keeping that path separate prevents Compile/Validate
// recursion while requiring every output field to be reproduced.
func compileGoContractContext(input GoContractContextInput, validateResult bool) (GoContractContext, error) {
	if err := validateGoContractInput(input); err != nil {
		return GoContractContext{}, err
	}
	terms := contractTerms(input.Objective)
	files := map[string]taskcontext.File{}
	for _, file := range input.Files {
		files[file.Path] = file
	}
	anchors, omissions := contractAnchors(input.Graph, files, terms, input.ChangedPaths)
	result := GoContractContext{
		Schema: goContractContextSchema, Version: 1, SourceID: input.SourceID, CandidateID: input.CandidateID,
		GraphDigest: input.Graph.Digest, ProducerSHA256: input.Graph.ProducerSHA256, Coverage: "PARTIAL",
		Excerpts: []taskcontext.SelectedFile{}, Relations: []GoGraphEdge{}, Modules: []GoContractModuleEvidence{}, Omissions: []taskcontext.Omission{},
	}
	query := sha256.Sum256([]byte(input.Objective))
	result.QuerySHA256 = hex.EncodeToString(query[:])
	if input.ModuleInventory != nil {
		result.ModuleInventoryDigest = input.ModuleInventory.Digest
	}
	if input.Generation != nil {
		result.GenerationMetadataDigest = input.Generation.Digest
	}
	if len(anchors) == 0 {
		omissions = append(omissions, taskcontext.Omission{Path: "[context]", Reason: "no_contract_anchor"})
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].rank != anchors[j].rank {
			return anchors[i].rank < anchors[j].rank
		}
		if anchors[i].path != anchors[j].path {
			return anchors[i].path < anchors[j].path
		}
		return anchors[i].start < anchors[j].start
	})
	usedPaths := map[string]bool{}
	for _, anchor := range anchors {
		if len(result.Excerpts) >= goContractContextMaxExcerpts || len(usedPaths) >= goContractContextMaxFiles {
			omissions = append(omissions, taskcontext.Omission{Path: anchor.path, Reason: "contract_excerpt_limit"})
			continue
		}
		file := files[anchor.path]
		if usedPaths[anchor.path] {
			continue
		}
		budget := min(goContractContextMaxExcerpt, goContractContextSourceLimit-result.SourceBytes)
		if budget < 1 {
			omissions = append(omissions, taskcontext.Omission{Path: anchor.path, Reason: "contract_source_budget"})
			continue
		}
		end := min(anchor.end, anchor.start+budget)
		end = contractUTF8End(file.Content, end)
		if end <= anchor.start {
			omissions = append(omissions, taskcontext.Omission{Path: anchor.path, Reason: "contract_invalid_span"})
			continue
		}
		entry := contractExcerpt(file, anchor.start, end, anchor.reason)
		result.Excerpts = append(result.Excerpts, entry)
		result.SourceBytes += int(entry.End - entry.Start)
		usedPaths[anchor.path] = true
	}
	generationFiles := make(map[string]taskcontext.File)
	if input.Generation != nil {
		files := input.GenerationFiles
		if files == nil {
			files = input.Generation.ContextFiles
		}
		for _, file := range files {
			generationFiles[file.Path] = file
		}
	}
	generation, generationOmissions := contractGenerationExcerpts(input.Generation, generationFiles, usedPaths, terms, result.Excerpts)
	for _, item := range generation {
		if len(result.Excerpts) >= goContractContextMaxExcerpts || len(usedPaths) >= goContractContextMaxFiles {
			omissions = append(omissions, taskcontext.Omission{Path: item.Path, Reason: "generation_excerpt_limit"})
			continue
		}
		result.Excerpts = append(result.Excerpts, item)
		result.GenerationBytes += int(item.End - item.Start)
		usedPaths[item.Path] = true
	}
	omissions = append(omissions, generationOmissions...)
	result.Relations = contractRelations(input.Graph, result.Excerpts)
	result.Modules = contractModules(input.ModuleInventory, result.Excerpts)
	result.OmittedCount, result.Omissions, result.OmissionsTrimmed = contractOmissions(omissions)
	result.Truncated = result.OmittedCount > 0 || input.Graph.Coverage == "PARTIAL" || input.Generation != nil && (input.Generation.Truncated || input.Generation.OmissionsTrimmed)
	if err := finalizeGoContractContext(&result); err != nil {
		return GoContractContext{}, err
	}
	if validateResult {
		if err := ValidateGoContractContext(result, input); err != nil {
			return GoContractContext{}, err
		}
	}
	return result, nil
}

func validateGoContractInput(input GoContractContextInput) error {
	if input.SourceID != input.Graph.SourceID || input.CandidateID != input.Graph.CandidateID || safepath.RequireDigest(input.SourceID) != nil || (input.CandidateID != "" && safepath.RequireDigest(input.CandidateID) != nil) || !utf8.ValidString(input.Objective) || strings.TrimSpace(input.Objective) == "" || len(input.Objective) > 16<<10 {
		return errors.New("invalid contract context identity or objective")
	}
	if len(input.Files) != len(input.Graph.Files) || len(input.Files) == 0 {
		return fmt.Errorf("contract context requires complete graph corpus: files=%d graph_files=%d", len(input.Files), len(input.Graph.Files))
	}
	seen := map[string]bool{}
	facts := map[string]GoGraphFile{}
	for _, file := range input.Graph.Files {
		facts[file.Facts.Path] = file
	}
	for _, file := range input.Files {
		sum := sha256.Sum256(file.Content)
		bound, ok := facts[file.Path]
		if !ok || seen[file.Path] || safepath.Relative(file.Path) != nil || !taskcontext.EligiblePath(file.Path) || file.Hash != hex.EncodeToString(sum[:]) || file.Hash != bound.Facts.SourceSHA256 || !utf8.Valid(file.Content) {
			return errors.New("contract context source differs from admitted graph")
		}
		seen[file.Path] = true
	}
	for _, changed := range input.ChangedPaths {
		if !seen[changed] {
			return errors.New("changed path absent from contract source")
		}
	}
	if input.ModuleInventory != nil {
		if err := ValidateGoModuleInventoryRecord(*input.ModuleInventory); err != nil {
			return fmt.Errorf("invalid module inventory: %w", err)
		}
		if input.Graph.ModuleInventory == nil || input.Graph.ModuleInventory.Digest != input.ModuleInventory.Digest {
			return errors.New("module inventory differs from graph")
		}
	}
	if input.Generation != nil {
		if err := ValidateGoGenerationMetadata(*input.Generation); err != nil {
			return fmt.Errorf("invalid generation metadata: %w", err)
		}
		if input.Generation.SourceID != input.SourceID {
			return errors.New("generation metadata source differs from graph")
		}
		if err := validateContractGenerationFiles(input.GenerationFiles, *input.Generation); err != nil {
			return err
		}
	} else if len(input.GenerationFiles) != 0 {
		return errors.New("generation source input requires metadata")
	}
	if err := ValidateGoEngineeringGraph(input.Graph); err != nil {
		return fmt.Errorf("invalid graph: %w", err)
	}
	return nil
}

func validateContractGenerationFiles(files []taskcontext.File, metadata GoGenerationMetadata) error {
	if files == nil {
		return nil // Legacy transient metadata input is validated separately.
	}
	if len(files) > goGenerationMaxFiles {
		return errors.New("contract generation source count exceeds bound")
	}
	sources := make(map[string]repository.SourceDigest, len(metadata.Sources))
	for _, source := range metadata.Sources {
		sources[source.Path] = source
	}
	bound := make(map[string]bool)
	for _, binding := range metadata.Bindings {
		bound[binding.GeneratorPath], bound[binding.GeneratedPath] = true, true
		for _, source := range binding.ToolSources {
			bound[source.Path] = true
		}
	}
	seen := make(map[string]bool, len(files))
	total := 0
	for _, file := range files {
		source, ok := sources[file.Path]
		sum := sha256.Sum256(file.Content)
		if !ok || !bound[file.Path] || seen[file.Path] || file.Hash != source.SHA256 || file.Hash != hex.EncodeToString(sum[:]) || int64(len(file.Content)) != source.Bytes || len(file.Content) > goGenerationMaxFileBytes || !utf8.Valid(file.Content) {
			return errors.New("contract generation source differs from admitted metadata")
		}
		seen[file.Path] = true
		total += len(file.Content)
	}
	if total > goGenerationMaxTotalBytes {
		return errors.New("contract generation source bytes exceed bound")
	}
	for _, binding := range metadata.Bindings {
		if !seen[binding.GeneratorPath] || !seen[binding.GeneratedPath] {
			return errors.New("contract generation binding source input is missing")
		}
		for _, source := range binding.ToolSources {
			if !seen[source.Path] {
				return errors.New("contract generation tool source input is missing")
			}
		}
	}
	return nil
}

func contractTerms(objective string) map[string]bool {
	terms := map[string]bool{}
	for _, item := range strings.FieldsFunc(objective, func(r rune) bool { return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') }) {
		if len([]rune(item)) >= 1 {
			terms[strings.ToLower(item)] = true
		}
	}
	return terms
}

func contractAnchors(graph GoEngineeringGraph, files map[string]taskcontext.File, terms map[string]bool, changed []string) ([]goContractAnchor, []taskcontext.Omission) {
	anchors, omissions := []goContractAnchor{}, []taskcontext.Omission{}
	seedPaths := map[string]bool{}
	for _, file := range graph.Files {
		for _, decl := range file.Facts.Declarations {
			if terms[strings.ToLower(decl.Name)] {
				seedPaths[file.Facts.Path] = true
			}
		}
	}
	for _, path := range changed {
		seedPaths[path] = true
	}
	if len(seedPaths) > 0 {
		paths := make([]string, 0, len(seedPaths))
		for p := range seedPaths {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		impact, err := QueryGoImpact(graph, GoImpactQuery{Paths: paths, MaxDepth: 1, Limit: 32})
		if err == nil {
			for _, p := range impact.Paths {
				seedPaths[p] = true
			}
			if impact.Truncated {
				omissions = append(omissions, taskcontext.Omission{Path: "[graph]", Reason: "impact_truncated"})
			}
		}
	}
	packageByPath := map[string]string{}
	for _, f := range graph.Files {
		packageByPath[f.Facts.Path] = graphPackageBindingNodeID(f.Package)
	}
	testPaths := map[string]bool{}
	for _, edge := range graph.Edges {
		if edge.Relation == "TESTS" && seedPaths[edge.Path] {
			continue
		}
		if edge.Relation == "TESTS" {
			for path, pkg := range packageByPath {
				if seedPaths[path] && edge.To == pkg {
					testPaths[edge.Path] = true
				}
			}
		}
	}
	for path := range seedPaths {
		if strings.HasSuffix(path, "_test.go") {
			testPaths[path] = true
		}
	}
	paths := make([]string, 0, len(seedPaths)+len(testPaths))
	seen := map[string]bool{}
	for p := range seedPaths {
		seen[p] = true
	}
	for p := range testPaths {
		seen[p] = true
	}
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file, ok := files[path]
		if !ok {
			omissions = append(omissions, taskcontext.Omission{Path: path, Reason: "contract_source_missing"})
			continue
		}
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, path, file.Content, parser.AllErrors)
		if err != nil {
			omissions = append(omissions, taskcontext.Omission{Path: path, Reason: "contract_syntax_error"})
			continue
		}
		for _, decl := range parsed.Decls {
			start, end, name, isTest, ok := contractDeclRange(set, decl)
			if !ok {
				continue
			}
			objectiveMatch := terms[strings.ToLower(name)]
			match := objectiveMatch || isTest && testPaths[path] || seedPaths[path]
			if !match {
				continue
			}
			reason, rank := "objective_declaration", 0
			if isTest {
				reason, rank = "test_contract", 2
			} else if !objectiveMatch {
				reason, rank = "impact_declaration", 1
			}
			anchors = append(anchors, goContractAnchor{path: path, start: start, end: end, reason: reason, rank: rank})
		}
	}
	return anchors, omissions
}

func contractDeclRange(set *token.FileSet, decl ast.Decl) (int, int, string, bool, bool) {
	switch value := decl.(type) {
	case *ast.FuncDecl:
		if value.Name == nil {
			return 0, 0, "", false, false
		}
		return set.Position(value.Pos()).Offset, set.Position(value.End()).Offset, value.Name.Name, strings.HasPrefix(value.Name.Name, "Test") || strings.HasPrefix(value.Name.Name, "Example"), true
	case *ast.GenDecl:
		if len(value.Specs) != 1 {
			return 0, 0, "", false, false
		}
		switch spec := value.Specs[0].(type) {
		case *ast.TypeSpec:
			return set.Position(value.Pos()).Offset, set.Position(value.End()).Offset, spec.Name.Name, false, true
		case *ast.ValueSpec:
			if len(spec.Names) == 1 {
				return set.Position(value.Pos()).Offset, set.Position(value.End()).Offset, spec.Names[0].Name, false, true
			}
		}
	}
	return 0, 0, "", false, false
}

func contractExcerpt(file taskcontext.File, start, end int, reason string) taskcontext.SelectedFile {
	content := file.Content[start:end]
	sum := sha256.Sum256(content)
	return taskcontext.SelectedFile{Path: file.Path, Hash: file.Hash, Start: int64(start), End: int64(end), ExcerptHash: hex.EncodeToString(sum[:]), Reason: reason, Content: string(content)}
}
func contractUTF8End(content []byte, end int) int {
	if end > len(content) {
		end = len(content)
	}
	for end > 0 && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return end
}

func contractGenerationExcerpts(metadata *GoGenerationMetadata, files map[string]taskcontext.File, used map[string]bool, terms map[string]bool, existing []taskcontext.SelectedFile) ([]taskcontext.SelectedFile, []taskcontext.Omission) {
	if metadata == nil {
		return nil, nil
	}
	selectedPaths := map[string]bool{}
	for _, item := range existing {
		selectedPaths[item.Path] = true
	}
	type candidate struct {
		path, reason string
		pivot, rank  int
	}
	candidates := []candidate{}
	for _, binding := range metadata.Bindings {
		relevant := selectedPaths[binding.GeneratorPath] || selectedPaths[binding.GeneratedPath] || terms[strings.ToLower(filepath.Base(binding.GeneratorPath))] || terms[strings.ToLower(filepath.Base(binding.GeneratedPath))]
		if !relevant {
			continue
		}
		for _, ref := range binding.ToolSources {
			if ref.Role == "generator_template" {
				candidates = append(candidates, candidate{ref.Path, "generation_template", 0, 0})
			}
		}
		candidates = append(candidates, candidate{binding.GeneratorPath, "generation_owner", binding.DirectiveRange.StartByte, 1}, candidate{binding.GeneratedPath, "generation_output", 0, 3})
		for _, ref := range binding.ToolSources {
			if ref.Role == "generator_source" {
				candidates = append(candidates, candidate{ref.Path, "generation_tool", 0, 2})
			}
			if ref.Role == "build_definition" {
				candidates = append(candidates, candidate{ref.Path, "generation_build", 0, 2})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].path < candidates[j].path
	})
	out, omissions := []taskcontext.SelectedFile{}, []taskcontext.Omission{}
	remain := goContractContextGenerationCap
	seen := map[string]bool{}
	for _, c := range candidates {
		if seen[c.path] || used[c.path] {
			continue
		}
		seen[c.path] = true
		file, ok := files[c.path]
		if !ok {
			omissions = append(omissions, taskcontext.Omission{Path: c.path, Reason: "generation_source_not_admitted"})
			continue
		}
		if remain < 1 {
			omissions = append(omissions, taskcontext.Omission{Path: c.path, Reason: "generation_source_budget"})
			continue
		}
		start := min(c.pivot, len(file.Content))
		end := contractUTF8End(file.Content, min(len(file.Content), start+min(goContractContextMaxExcerpt, remain)))
		if end <= start {
			omissions = append(omissions, taskcontext.Omission{Path: c.path, Reason: "generation_invalid_span"})
			continue
		}
		out = append(out, contractExcerpt(file, start, end, c.reason))
		remain -= end - start
	}
	return out, omissions
}

func contractRelations(graph GoEngineeringGraph, excerpts []taskcontext.SelectedFile) []GoGraphEdge {
	visible := map[string][]taskcontext.SelectedFile{}
	for _, e := range excerpts {
		visible[e.Path] = append(visible[e.Path], e)
	}
	out := []GoGraphEdge{}
	for _, edge := range graph.Edges {
		spans := visible[edge.Path]
		if len(spans) == 0 {
			continue
		}
		if edge.Range != nil {
			found := false
			for _, s := range spans {
				if int64(edge.Range.StartByte) >= s.Start && int64(edge.Range.EndByte) <= s.End {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		out = append(out, cloneGoGraphEdge(edge))
		if len(out) == goContractContextMaxRelations {
			break
		}
	}
	return out
}
func contractModules(inv *GoModuleInventory, excerpts []taskcontext.SelectedFile) []GoContractModuleEvidence {
	if inv == nil {
		return []GoContractModuleEvidence{}
	}
	out := []GoContractModuleEvidence{}
	seen := map[string]bool{}
	for _, e := range excerpts {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		o, err := GoModuleOwnershipForPath(*inv, e.Path)
		if err == nil && o.Status == "declared_module" {
			out = append(out, GoContractModuleEvidence{Path: e.Path, Ownership: o})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func contractOmissions(items []taskcontext.Omission) (int, []taskcontext.Omission, bool) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		return items[i].Reason < items[j].Reason
	})
	out := items
	trimmed := len(out) > goContractContextMaxOmissions
	if trimmed {
		out = out[:goContractContextMaxOmissions]
	}
	return len(items), out, trimmed
}
func finalizeGoContractContext(result *GoContractContext) error {
	body := *result
	body.Digest = ""
	digest, err := canonical.Hash("harness.ri.go-contract-context.v1", body)
	if err != nil {
		return err
	}
	result.Digest = digest
	return nil
}

// ValidateGoContractContext verifies a compiled result against its admitted input.
func ValidateGoContractContext(result GoContractContext, input GoContractContextInput) error {
	if err := validateGoContractInput(input); err != nil {
		return err
	}
	expected, err := compileGoContractContext(input, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(result, expected) {
		gotExcerpts, _ := canonical.Hash("harness.ri.go-contract-excerpts.v1", result.Excerpts)
		wantExcerpts, _ := canonical.Hash("harness.ri.go-contract-excerpts.v1", expected.Excerpts)
		gotRelations, _ := canonical.Hash("harness.ri.go-contract-relations.v1", result.Relations)
		wantRelations, _ := canonical.Hash("harness.ri.go-contract-relations.v1", expected.Relations)
		gotOmissions, _ := canonical.Hash("harness.ri.go-contract-omissions.v1", result.Omissions)
		wantOmissions, _ := canonical.Hash("harness.ri.go-contract-omissions.v1", expected.Omissions)
		return fmt.Errorf("contract context differs from deterministic admitted evidence: got=%s want=%s excerpts=%d/%d:%s/%s relations=%d/%d:%s/%s omissions=%d/%d:%s/%s", result.Digest, expected.Digest, len(result.Excerpts), len(expected.Excerpts), gotExcerpts, wantExcerpts, len(result.Relations), len(expected.Relations), gotRelations, wantRelations, result.OmittedCount, expected.OmittedCount, gotOmissions, wantOmissions)
	}
	return nil
}
func validContractReason(reason string) bool {
	switch reason {
	case "objective_declaration", "impact_declaration", "test_contract", "generation_owner", "generation_template", "generation_tool", "generation_build", "generation_output":
		return true
	}
	return false
}
func contractRelationInGraph(graph GoEngineeringGraph, want GoGraphEdge) bool {
	for _, got := range graph.Edges {
		if reflect.DeepEqual(got, want) {
			return true
		}
	}
	return false
}

func contractRelationVisible(edge GoGraphEdge, excerpts []taskcontext.SelectedFile) bool {
	for _, excerpt := range excerpts {
		if excerpt.Path != edge.Path {
			continue
		}
		if edge.Range == nil || int64(edge.Range.StartByte) >= excerpt.Start && int64(edge.Range.EndByte) <= excerpt.End {
			return true
		}
	}
	return false
}
