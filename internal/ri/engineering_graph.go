package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/token"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

const (
	goEngineeringGraphSchema = "engorch.ri.go-engineering-graph.v1"
	goEngineeringMaxFiles    = 4096
	goEngineeringMaxBytes    = 32 << 20
	goEngineeringMaxFacts    = 100_000
	goEngineeringMaxNodes    = 200_000
	goEngineeringMaxEdges    = 300_000
	goEngineeringMaxSeeds    = 64
	goEngineeringMaxQuery    = 1000
)

// GoGraphFileInput pairs validated syntax facts with the exact source bytes
// and explicit package identity used to validate and construct a graph.
type GoGraphFileInput struct {
	Facts   GoFileFacts      `json:"facts"`
	Source  []byte           `json:"source"`
	Package GoPackageBinding `json:"package"`
}

// GoPackageBinding is explicit repository/package metadata. ImportPath is
// matched exactly; declared_module_v1 bindings additionally require a matching
// committed module inventory. Neither mode establishes active build resolution.
// TestOfImportPath is explicit metadata for an external-test package; an
// internal *_test.go file is associated with its own supplied ImportPath.
type GoPackageBinding struct {
	ImportPath            string `json:"import_path"`
	ModulePath            string `json:"module_path"`
	TestOfImportPath      string `json:"test_of_import_path,omitempty"`
	IdentityKind          string `json:"identity_kind,omitempty"`
	SourceID              string `json:"source_id,omitempty"`
	SourceDirectory       string `json:"source_directory,omitempty"`
	PackageName           string `json:"package_name,omitempty"`
	PackageIdentity       string `json:"package_identity,omitempty"`
	TestOfPackageIdentity string `json:"test_of_package_identity,omitempty"`
	ModuleRoot            string `json:"module_root,omitempty"`
	ManifestPath          string `json:"manifest_path,omitempty"`
	InventoryDigest       string `json:"inventory_digest,omitempty"`
}

// DeclaredGoPackageBinding derives a package binding from a validated
// committed module inventory. It falls back to the established source-local
// identity whenever the inventory cannot establish one unique deepest module.
// It records declarations only and does not claim active Go build resolution.
func DeclaredGoPackageBinding(inventory GoModuleInventory, sourcePath, packageName string) (GoPackageBinding, error) {
	if err := ValidateGoModuleInventoryRecord(inventory); err != nil {
		return GoPackageBinding{}, err
	}
	ownership, err := GoModuleOwnershipForPath(inventory, sourcePath)
	if err != nil {
		return GoPackageBinding{}, err
	}
	if ownership.Status != "declared_module" {
		return SourceLocalGoPackageBinding(inventory.RepositoryID, sourcePath, packageName)
	}
	if !declaredModulePathUnique(inventory, ownership.ModulePath) {
		return SourceLocalGoPackageBinding(inventory.RepositoryID, sourcePath, packageName)
	}
	if !validGoPackageName(packageName) || !validGoImportPath(ownership.ModulePath) || !validGoImportPath(ownership.ImportPath) || safepath.Relative(sourcePath) != nil {
		return GoPackageBinding{}, errors.New("invalid declared Go package ownership")
	}
	manifestPath := path.Join(ownership.ModuleRoot, "go.mod")
	if ownership.ModuleRoot == "" {
		manifestPath = "go.mod"
	}
	binding := GoPackageBinding{
		ImportPath: ownership.ImportPath, ModulePath: ownership.ModulePath,
		IdentityKind: "declared_module_v1", SourceID: inventory.RepositoryID,
		SourceDirectory: path.Dir(sourcePath), PackageName: packageName,
		ModuleRoot: ownership.ModuleRoot, ManifestPath: manifestPath, InventoryDigest: inventory.Digest,
	}
	if strings.HasSuffix(sourcePath, "_test.go") && strings.HasSuffix(packageName, "_test") {
		binding.TestOfImportPath = ownership.ImportPath
		identity, err := declaredGoPackageIdentity(binding)
		if err != nil {
			return GoPackageBinding{}, err
		}
		binding.PackageIdentity = identity
	}
	return binding, nil
}

func declaredModulePathUnique(inventory GoModuleInventory, modulePath string) bool {
	count := 0
	for _, file := range inventory.Files {
		if file.Kind == "go_mod" && file.Status == "parsed" && file.ModulePath == modulePath {
			count++
		}
	}
	return count == 1
}

// SourceLocalGoPackageBinding creates a stable package identity from an
// immutable repository source ID, exact source file path, and parsed package
// clause. External *_test.go package clauses receive a distinct identity and
// a syntactic test-of-package relation. No Go import path is inferred.
func SourceLocalGoPackageBinding(sourceID, sourcePath, packageName string) (GoPackageBinding, error) {
	if safepath.RequireDigest(sourceID) != nil || safepath.Relative(sourcePath) != nil || filepath.Ext(sourcePath) != ".go" || !validGoPackageName(packageName) {
		return GoPackageBinding{}, errors.New("invalid source-local Go package input")
	}
	directory := path.Dir(sourcePath)
	identity, err := sourceLocalPackageIdentity(sourceID, directory, packageName)
	if err != nil {
		return GoPackageBinding{}, err
	}
	binding := GoPackageBinding{IdentityKind: "source_local_v1", SourceID: sourceID, SourceDirectory: directory, PackageName: packageName, PackageIdentity: identity}
	if strings.HasSuffix(sourcePath, "_test.go") && strings.HasSuffix(packageName, "_test") {
		testTargetName := strings.TrimSuffix(packageName, "_test")
		if !validGoPackageName(testTargetName) {
			return GoPackageBinding{}, errors.New("invalid external Go test package name")
		}
		binding.TestOfPackageIdentity, err = sourceLocalPackageIdentity(sourceID, directory, testTargetName)
		if err != nil {
			return GoPackageBinding{}, err
		}
	}
	return binding, nil
}

// GoGeneratorBinding is an explicit, source-bound relation. It records a
// declared go:generate directive and generated output; it does not execute or
// authorize the command.
type GoGeneratorBinding struct {
	GeneratorPath string `json:"generator_path"`
	GeneratedPath string `json:"generated_path"`
	Directive     string `json:"directive"`
}

// GoGraphSnapshotInput binds one immutable committed source identity and its
// RI producer. CandidateID is optional for a base graph and set for candidates.
type GoGraphSnapshotInput struct {
	SourceID        string               `json:"source_id"`
	CandidateID     string               `json:"candidate_id"`
	ProducerSHA256  string               `json:"producer_sha256"`
	Files           []GoGraphFileInput   `json:"files"`
	Generators      []GoGeneratorBinding `json:"generators"`
	ModuleInventory *GoModuleInventory   `json:"module_inventory,omitempty"`
}

// GoGraphOverlayInput describes replacement and deletion facts for a new
// candidate. Package bindings are carried by each replacement; Generators is
// the complete explicit relation set for the resulting snapshot.
type GoGraphOverlayInput struct {
	BaseDigest     string               `json:"base_digest"`
	SourceID       string               `json:"source_id,omitempty"`
	CandidateID    string               `json:"candidate_id"`
	ProducerSHA256 string               `json:"producer_sha256"`
	Replacements   []GoGraphFileInput   `json:"replacements"`
	Deleted        []string             `json:"deleted"`
	Generators     []GoGeneratorBinding `json:"generators"`
	// ModuleInventory closes declared-module ownership over a candidate
	// overlay. It is optional to preserve legacy source-local overlays.
	ModuleInventory *GoModuleInventory `json:"module_inventory,omitempty"`
}

// GoGraphFile retains syntax facts and explicit package identity, but not
// source text. Cache-hit/miss counters are normalized out of graph identity.
type GoGraphFile struct {
	Facts   GoFileFacts      `json:"facts"`
	Package GoPackageBinding `json:"package"`
}

// GoGraphNode is a deterministic file, package, declaration, import, or call
// node. A call node remains syntactic and does not claim Go name resolution.
type GoGraphNode struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Label      string   `json:"label"`
	Path       string   `json:"path,omitempty"`
	Range      *GoRange `json:"range,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// GoGraphEdge is one named relation with the originating byte range where
// applicable. TESTS is a syntactic file/package association, not proof that a
// test is buildable or executed. Resolution is UNRESOLVED for syntax-only calls.
type GoGraphEdge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Relation   string   `json:"relation"`
	Path       string   `json:"path,omitempty"`
	Range      *GoRange `json:"range,omitempty"`
	Alias      *string  `json:"alias,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// GoEngineeringGraph is a deterministic, partial source graph. Its digest
// binds committed source identity, optional candidate identity, producer,
// source hashes, facts, explicit package bindings, nodes, and edges. Callers
// receive fresh values; overlay application never mutates the supplied base.
type GoEngineeringGraph struct {
	Schema          string               `json:"schema"`
	SourceID        string               `json:"source_id"`
	CandidateID     string               `json:"candidate_id"`
	ProducerSHA256  string               `json:"producer_sha256"`
	SourceDigest    string               `json:"source_digest"`
	Coverage        string               `json:"coverage"`
	Files           []GoGraphFile        `json:"files"`
	Generators      []GoGeneratorBinding `json:"generators"`
	Nodes           []GoGraphNode        `json:"nodes"`
	Edges           []GoGraphEdge        `json:"edges"`
	ModuleInventory *GoModuleInventory   `json:"module_inventory,omitempty"`
	Digest          string               `json:"digest"`
}

type goEngineeringGraphContent struct {
	Schema          string               `json:"schema"`
	SourceID        string               `json:"source_id"`
	CandidateID     string               `json:"candidate_id"`
	ProducerSHA256  string               `json:"producer_sha256"`
	SourceDigest    string               `json:"source_digest"`
	Coverage        string               `json:"coverage"`
	Files           []GoGraphFile        `json:"files"`
	Generators      []GoGeneratorBinding `json:"generators"`
	Nodes           []GoGraphNode        `json:"nodes"`
	Edges           []GoGraphEdge        `json:"edges"`
	ModuleInventory *GoModuleInventory   `json:"module_inventory,omitempty"`
}

// BuildGoEngineeringGraph validates exact source/fact bindings and builds a
// deterministic partial graph. Import paths and generator links are accepted
// only from the explicit records in input.
func BuildGoEngineeringGraph(input GoGraphSnapshotInput) (GoEngineeringGraph, error) {
	if !lowerDigest(input.SourceID) || (input.CandidateID != "" && !lowerDigest(input.CandidateID)) || !lowerDigest(input.ProducerSHA256) {
		return GoEngineeringGraph{}, errors.New("Go graph source, candidate, or producer digest is malformed")
	}
	inventory, err := validateGoGraphModuleInventory(input.ModuleInventory, input.SourceID, input.CandidateID)
	if err != nil {
		return GoEngineeringGraph{}, err
	}
	files, err := validateGoGraphInputs(input.Files, input.ProducerSHA256, input.SourceID, inventory)
	if err != nil {
		return GoEngineeringGraph{}, err
	}
	return buildGoEngineeringGraph(input.SourceID, input.CandidateID, input.ProducerSHA256, files, input.Generators, inventory)
}

// ApplyGoEngineeringOverlay applies candidate-bound replacements/deletions to
// a new graph value. It rejects stale base digests, duplicate paths, producer
// substitutions, and candidate identity reuse.
func ApplyGoEngineeringOverlay(base GoEngineeringGraph, overlay GoGraphOverlayInput) (GoEngineeringGraph, error) {
	if err := ValidateGoEngineeringGraph(base); err != nil {
		return GoEngineeringGraph{}, fmt.Errorf("invalid base Go graph: %w", err)
	}
	if overlay.BaseDigest != base.Digest || (overlay.SourceID != "" && overlay.SourceID != base.SourceID) || !lowerDigest(overlay.CandidateID) || (base.CandidateID != "" && overlay.CandidateID == base.CandidateID) || overlay.ProducerSHA256 != base.ProducerSHA256 {
		return GoEngineeringGraph{}, errors.New("Go graph overlay binding mismatch")
	}
	inventory, err := validateGoGraphOverlayModuleInventory(base, overlay)
	if err != nil {
		return GoEngineeringGraph{}, err
	}
	var replacements []GoGraphFile
	if len(overlay.Replacements) > 0 {
		if inventory != nil {
			replacements, err = rebindGoGraphInputs(overlay.Replacements, overlay.ProducerSHA256, base.SourceID, inventory)
		} else {
			replacements, err = validateGoGraphInputs(overlay.Replacements, overlay.ProducerSHA256, base.SourceID, nil)
		}
		if err != nil {
			return GoEngineeringGraph{}, err
		}
	}
	filesByPath := make(map[string]GoGraphFile, len(base.Files)+len(replacements))
	for _, file := range base.Files {
		filesByPath[file.Facts.Path] = cloneGoGraphFile(file)
	}
	changed := make(map[string]struct{}, len(replacements)+len(overlay.Deleted))
	for _, file := range replacements {
		path := file.Facts.Path
		if _, duplicate := changed[path]; duplicate {
			return GoEngineeringGraph{}, errors.New("Go graph overlay repeats a changed path")
		}
		changed[path] = struct{}{}
		filesByPath[path] = cloneGoGraphFile(file)
	}
	deleted := append([]string(nil), overlay.Deleted...)
	sort.Strings(deleted)
	for i, path := range deleted {
		if err := safepath.Relative(path); err != nil || filepath.Ext(path) != ".go" || (i > 0 && deleted[i-1] == path) {
			return GoEngineeringGraph{}, errors.New("Go graph overlay has invalid or duplicate deletion path")
		}
		if _, duplicate := changed[path]; duplicate {
			return GoEngineeringGraph{}, errors.New("Go graph overlay both replaces and deletes a path")
		}
		changed[path] = struct{}{}
		if _, exists := filesByPath[path]; !exists {
			return GoEngineeringGraph{}, errors.New("Go graph overlay deletes a path absent from base")
		}
		delete(filesByPath, path)
	}
	if len(changed) == 0 {
		return GoEngineeringGraph{}, errors.New("Go graph overlay has no source changes")
	}
	files := make([]GoGraphFile, 0, len(filesByPath))
	for _, file := range filesByPath {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Facts.Path < files[j].Facts.Path })
	if inventory != nil {
		files, err = rebindGoGraphFiles(files, base.SourceID, inventory)
		if err != nil {
			return GoEngineeringGraph{}, err
		}
	}
	return buildGoEngineeringGraph(base.SourceID, overlay.CandidateID, overlay.ProducerSHA256, files, overlay.Generators, inventory)
}

// ValidateGoEngineeringGraph checks graph structure and its deterministic
// digest before the graph is queried or used as an overlay base.
func ValidateGoEngineeringGraph(graph GoEngineeringGraph) error {
	if graph.Schema != goEngineeringGraphSchema || graph.Coverage != "PARTIAL" || !lowerDigest(graph.SourceID) || (graph.CandidateID != "" && !lowerDigest(graph.CandidateID)) || !lowerDigest(graph.ProducerSHA256) || !lowerDigest(graph.SourceDigest) || !lowerDigest(graph.Digest) {
		return errors.New("Go engineering graph header is invalid")
	}
	if len(graph.Files) > goEngineeringMaxFiles || len(graph.Nodes) > goEngineeringMaxNodes || len(graph.Edges) > goEngineeringMaxEdges {
		return errors.New("Go engineering graph exceeds structural bounds")
	}
	totalFacts := 0
	for i, file := range graph.Files {
		if err := validateGoGraphFile(file, graph.ProducerSHA256, graph.SourceID, graph.ModuleInventory); err != nil {
			return err
		}
		if i > 0 && graph.Files[i-1].Facts.Path >= file.Facts.Path {
			return errors.New("Go engineering graph files are not strictly ordered")
		}
		totalFacts += len(file.Facts.Declarations) + len(file.Facts.Imports) + len(file.Facts.Calls) + len(file.Facts.GeneratedMarkers)
		if totalFacts > goEngineeringMaxFacts {
			return errors.New("Go engineering graph exceeds aggregate fact budget")
		}
	}
	for i := 1; i < len(graph.Nodes); i++ {
		if graph.Nodes[i-1].ID >= graph.Nodes[i].ID {
			return errors.New("Go engineering graph nodes are not strictly ordered")
		}
	}
	for i := 1; i < len(graph.Edges); i++ {
		if !goGraphEdgeLess(graph.Edges[i-1], graph.Edges[i]) {
			return errors.New("Go engineering graph edges are not strictly ordered")
		}
	}
	// Reconstruction computes both canonical digests once. Compare the entire
	// supplied content as well: matching only the supplied digest would permit
	// altered nodes/edges with an unchanged digest. No validation result is cached
	// across mutable caller-owned graphs.
	inventory, err := validateGoGraphModuleInventory(graph.ModuleInventory, graph.SourceID, graph.CandidateID)
	if err != nil {
		return err
	}
	expected, err := buildGoEngineeringGraph(graph.SourceID, graph.CandidateID, graph.ProducerSHA256, graph.Files, graph.Generators, inventory)
	if err != nil || expected.Digest != graph.Digest || !reflect.DeepEqual(expected.content(), graph.content()) {
		return errors.New("Go engineering graph relations do not match its bound facts")
	}
	return nil
}

// GoSymbolQuery selects declarations by exact name or prefix, optional kind
// and source path. Limit is required and must be within the public query cap.
type GoSymbolQuery struct {
	NamePrefix string `json:"name_prefix"`
	Kind       string `json:"kind,omitempty"`
	Path       string `json:"path,omitempty"`
	Limit      int    `json:"limit"`
}

// GoImportQuery selects exact imported path relations, optionally by importer.
type GoImportQuery struct {
	ImportPath string `json:"import_path"`
	Path       string `json:"path,omitempty"`
	Limit      int    `json:"limit"`
}

// GoCallQuery selects syntactic call relations. Results remain UNRESOLVED;
// a name-candidate relation is only an advisory same-file spelling match.
type GoCallQuery struct {
	SpellingPrefix string `json:"spelling_prefix"`
	Path           string `json:"path,omitempty"`
	Limit          int    `json:"limit"`
}

// GoImpactQuery bounds reverse dependency traversal from exact source paths.
type GoImpactQuery struct {
	Paths    []string `json:"paths"`
	MaxDepth int      `json:"max_depth"`
	Limit    int      `json:"limit"`
}

// GoImpactResult reports a bounded syntactic impact closure. Partial graph
// coverage means absence from the result is not evidence of no impact.
type GoImpactResult struct {
	Coverage  string   `json:"coverage"`
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated"`
}

// QueryGoSymbols returns bounded, deterministically ordered declaration nodes.
func QueryGoSymbols(graph GoEngineeringGraph, query GoSymbolQuery) ([]GoGraphNode, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return nil, err
	}
	if err := validateGoQuery(query.Limit, query.Path); err != nil {
		return nil, err
	}
	if len(query.NamePrefix) > 4096 || (query.NamePrefix == "" && query.Path == "" && query.Kind == "") || (query.Kind != "" && !validGoSymbolKind(query.Kind)) {
		return nil, errors.New("Go symbol query must be bounded by name, path, or kind")
	}
	var kindByID map[string]string
	if query.Kind != "" {
		kindByID = make(map[string]string)
		for _, file := range graph.Files {
			for _, symbol := range file.Facts.Declarations {
				kindByID[graphSymbolNodeID(file.Facts.Path, symbol)] = symbol.Kind
			}
		}
	}
	var result []GoGraphNode
	for _, node := range graph.Nodes {
		if node.Kind != "declaration" || !strings.HasPrefix(node.Label, query.NamePrefix) || (query.Path != "" && node.Path != query.Path) {
			continue
		}
		if query.Kind != "" && kindByID[node.ID] != query.Kind {
			continue
		}
		result = append(result, cloneGoGraphNode(node))
		if len(result) == query.Limit {
			break
		}
	}
	return result, nil
}

// QueryGoImports returns bounded exact import relations.
func QueryGoImports(graph GoEngineeringGraph, query GoImportQuery) ([]GoGraphEdge, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return nil, err
	}
	if err := validateGoQuery(query.Limit, query.Path); err != nil {
		return nil, err
	}
	if query.ImportPath == "" || len(query.ImportPath) > 4096 {
		return nil, errors.New("Go import query requires an exact import path")
	}
	var result []GoGraphEdge
	for _, edge := range graph.Edges {
		if edge.Relation != "IMPORTS" || (query.Path != "" && edge.Path != query.Path) {
			continue
		}
		if nodeLabel(graph.Nodes, edge.To) == query.ImportPath {
			result = append(result, cloneGoGraphEdge(edge))
			if len(result) == query.Limit {
				break
			}
		}
	}
	return result, nil
}

// QueryGoCalls returns syntactic calls and their explicit UNRESOLVED status.
func QueryGoCalls(graph GoEngineeringGraph, query GoCallQuery) ([]GoGraphEdge, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return nil, err
	}
	if err := validateGoQuery(query.Limit, query.Path); err != nil {
		return nil, err
	}
	if query.SpellingPrefix == "" && query.Path == "" {
		return nil, errors.New("Go call query must be bounded by spelling or path")
	}
	if len(query.SpellingPrefix) > 4096 {
		return nil, errors.New("Go call query spelling exceeds bound")
	}
	var result []GoGraphEdge
	for _, edge := range graph.Edges {
		if edge.Relation != "CALLS_UNRESOLVED" || (query.Path != "" && edge.Path != query.Path) {
			continue
		}
		if strings.HasPrefix(nodeLabel(graph.Nodes, edge.To), query.SpellingPrefix) {
			result = append(result, cloneGoGraphEdge(edge))
			if len(result) == query.Limit {
				break
			}
		}
	}
	return result, nil
}

// QueryGoImpact returns files syntactically downstream of the supplied paths.
// Unresolved calls and absent partial facts cannot establish completeness.
func QueryGoImpact(graph GoEngineeringGraph, query GoImpactQuery) (GoImpactResult, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return GoImpactResult{}, err
	}
	if query.Limit < 1 || query.Limit > goEngineeringMaxQuery || query.MaxDepth < 1 || query.MaxDepth > 16 || len(query.Paths) == 0 || len(query.Paths) > goEngineeringMaxSeeds {
		return GoImpactResult{}, errors.New("Go impact query exceeds bounds")
	}
	fileSet := make(map[string]struct{}, len(graph.Files))
	packageByPath := make(map[string]string, len(graph.Files))
	for _, file := range graph.Files {
		fileSet[file.Facts.Path] = struct{}{}
		packageByPath[file.Facts.Path] = graphPackageBindingNodeID(file.Package)
	}
	starts := append([]string(nil), query.Paths...)
	sort.Strings(starts)
	for i, path := range starts {
		if err := safepath.Relative(path); err != nil || (i > 0 && starts[i-1] == path) {
			return GoImpactResult{}, errors.New("Go impact query has invalid or duplicate paths")
		}
		if _, exists := fileSet[path]; !exists {
			return GoImpactResult{}, errors.New("Go impact query path is absent from graph")
		}
	}
	// Build compact traversal indexes from the explicit graph relations.
	packageImporters := make(map[string][]string)
	callers := make(map[string][]string)
	tests := make(map[string][]string)
	generated := make(map[string][]string)
	declaredSymbols := make(map[string][]string)
	for _, edge := range graph.Edges {
		switch edge.Relation {
		case "IMPORTS":
			packageImporters[edge.To] = append(packageImporters[edge.To], edge.Path)
		case "CALL_NAME_CANDIDATE":
			callers[edge.To] = append(callers[edge.To], edge.Path)
		case "TESTS":
			tests[edge.To] = append(tests[edge.To], edge.Path)
		case "GENERATED_BY":
			generated[edge.To] = append(generated[edge.To], edge.Path)
		case "DECLARES":
			declaredSymbols[edge.From] = append(declaredSymbols[edge.From], edge.To)
		}
	}
	for _, groups := range [][][]string{packageImportersValues(packageImporters), callerValues(callers), callerValues(tests), callerValues(generated), callerValues(declaredSymbols)} {
		for _, items := range groups {
			sort.Strings(items)
		}
	}
	type pending struct {
		path  string
		depth int
	}
	queue := make([]pending, 0, len(starts))
	for _, path := range starts {
		queue = append(queue, pending{path: path})
	}
	seen := make(map[string]int, len(starts))
	for _, path := range starts {
		seen[path] = 0
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= query.MaxDepth {
			continue
		}
		neighbors := make([]string, 0)
		pkg := packageByPath[current.path]
		neighbors = append(neighbors, packageImporters[pkg]...)
		neighbors = append(neighbors, tests[pkg]...)
		neighbors = append(neighbors, generated[graphFileNodeID(current.path)]...)
		for _, symbol := range declaredSymbols[graphFileNodeID(current.path)] {
			neighbors = append(neighbors, callers[symbol]...)
		}
		sort.Strings(neighbors)
		for _, neighbor := range neighbors {
			if _, exists := fileSet[neighbor]; !exists {
				continue
			}
			if _, exists := seen[neighbor]; exists {
				continue
			}
			seen[neighbor] = current.depth + 1
			queue = append(queue, pending{path: neighbor, depth: current.depth + 1})
		}
	}
	type impacted struct {
		path  string
		depth int
	}
	items := make([]impacted, 0, len(seen))
	for path, depth := range seen {
		if depth > 0 {
			items = append(items, impacted{path, depth})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].depth != items[j].depth {
			return items[i].depth < items[j].depth
		}
		return items[i].path < items[j].path
	})
	truncated := len(items) > query.Limit
	if truncated {
		items = items[:query.Limit]
	}
	paths := make([]string, len(items))
	for i := range items {
		paths[i] = items[i].path
	}
	return GoImpactResult{Coverage: "PARTIAL", Paths: paths, Truncated: truncated}, nil
}

func validateGoGraphModuleInventory(inventory *GoModuleInventory, sourceID, candidateID string) (*GoModuleInventory, error) {
	if inventory == nil {
		return nil, nil
	}
	if inventory.RepositoryID != sourceID || ValidateGoModuleInventoryRecord(*inventory) != nil {
		return nil, errors.New("Go graph module inventory is invalid or source-substituted")
	}
	if candidateID == "" {
		if inventory.Version != GoModuleInventoryVersionCommitted || inventory.CandidateID != "" || inventory.CandidateFilesHash != "" || inventory.BaseInventoryDigest != "" {
			return nil, errors.New("base Go graph requires a committed module inventory")
		}
	} else if inventory.Version != GoModuleInventoryVersionCandidate || inventory.CandidateID != candidateID || !lowerDigest(inventory.CandidateFilesHash) || !lowerDigest(inventory.BaseInventoryDigest) {
		return nil, errors.New("candidate Go graph module inventory is invalid or candidate-substituted")
	}
	copy := cloneGoModuleInventory(*inventory)
	return &copy, nil
}

// validateGoGraphOverlayModuleInventory admits only an explicit v2 candidate
// inventory over a committed v1 base. A committed inventory cannot be reused
// for modified candidate source because go.mod/go.work ownership may differ.
func validateGoGraphOverlayModuleInventory(base GoEngineeringGraph, overlay GoGraphOverlayInput) (*GoModuleInventory, error) {
	if overlay.ModuleInventory == nil {
		if base.ModuleInventory != nil {
			return nil, errors.New("module-backed Go graph overlays require a candidate manifest inventory")
		}
		return nil, nil
	}
	if base.CandidateID != "" || base.ModuleInventory == nil || base.ModuleInventory.Version != GoModuleInventoryVersionCommitted {
		return nil, errors.New("candidate module inventory requires a committed module-backed base graph")
	}
	inventory, err := validateGoGraphModuleInventory(overlay.ModuleInventory, base.SourceID, overlay.CandidateID)
	if err != nil {
		return nil, err
	}
	if inventory.BaseInventoryDigest != base.ModuleInventory.Digest || inventory.Commit != base.ModuleInventory.Commit || inventory.Tree != base.ModuleInventory.Tree {
		return nil, errors.New("candidate module inventory is not bound to the base inventory")
	}
	return inventory, nil
}

func cloneGoModuleInventory(inventory GoModuleInventory) GoModuleInventory {
	copy := inventory
	if inventory.Files != nil {
		copy.Files = append([]GoManifestObservation{}, inventory.Files...)
	}
	if inventory.Omissions != nil {
		copy.Omissions = append([]GoManifestOmission{}, inventory.Omissions...)
	}
	for i := range copy.Files {
		if inventory.Files[i].Requires != nil {
			copy.Files[i].Requires = append([]GoModuleRequirement{}, inventory.Files[i].Requires...)
		}
		if inventory.Files[i].Replaces != nil {
			copy.Files[i].Replaces = append([]GoModuleReplacement{}, inventory.Files[i].Replaces...)
		}
		if inventory.Files[i].Uses != nil {
			copy.Files[i].Uses = append([]GoWorkspaceUse{}, inventory.Files[i].Uses...)
		}
	}
	return copy
}

func validateGoGraphInputs(inputs []GoGraphFileInput, producer, sourceID string, inventory *GoModuleInventory) ([]GoGraphFile, error) {
	if len(inputs) == 0 || len(inputs) > goEngineeringMaxFiles {
		return nil, errors.New("Go graph file count is outside bounds")
	}
	files := make([]GoGraphFile, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	totalBytes, totalFacts := 0, 0
	for _, input := range inputs {
		facts := input.Facts
		if facts.ProducerSHA256 != producer || facts.Path == "" || filepath.Ext(facts.Path) != ".go" || len(input.Source) > goFactsMaxSource || !utf8.Valid(input.Source) {
			return nil, errors.New("Go graph file input binding or source is invalid")
		}
		if _, duplicate := seen[facts.Path]; duplicate {
			return nil, errors.New("Go graph repeats a file path")
		}
		seen[facts.Path] = struct{}{}
		totalBytes += len(input.Source)
		totalFacts += len(facts.Declarations) + len(facts.Imports) + len(facts.Calls) + len(facts.GeneratedMarkers)
		if totalBytes > goEngineeringMaxBytes || totalFacts > goEngineeringMaxFacts {
			return nil, errors.New("Go graph input exceeds aggregate bounds")
		}
		if err := validateGoFileFacts(facts, facts.Path, facts.SourceSHA256, producer, input.Source); err != nil {
			return nil, fmt.Errorf("invalid Go facts for %s: %w", facts.Path, err)
		}
		if err := validateGoPackageBinding(input.Package, facts.Path, sourceID, inventory); err != nil {
			return nil, err
		}
		files = append(files, GoGraphFile{Facts: normalizeGoGraphFacts(facts), Package: input.Package})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Facts.Path < files[j].Facts.Path })
	return files, nil
}

// rebindGoGraphInputs ignores caller-supplied module binding fields and derives
// fresh package ownership from a candidate inventory plus the replacement's
// explicit package clause. Facts still bind each replacement to exact source.
func rebindGoGraphInputs(inputs []GoGraphFileInput, producer, sourceID string, inventory *GoModuleInventory) ([]GoGraphFile, error) {
	bound := append([]GoGraphFileInput(nil), inputs...)
	for i := range bound {
		binding, err := DeclaredGoPackageBinding(*inventory, bound[i].Facts.Path, bound[i].Package.PackageName)
		if err != nil {
			return nil, err
		}
		bound[i].Package = binding
	}
	return validateGoGraphInputs(bound, producer, sourceID, inventory)
}

// rebindGoGraphFiles derives fresh ownership for retained facts. Their package
// clauses were already source-bound by the immutable base graph; no stale base
// module mapping survives a candidate manifest change.
func rebindGoGraphFiles(files []GoGraphFile, sourceID string, inventory *GoModuleInventory) ([]GoGraphFile, error) {
	bound := cloneGoGraphFiles(files)
	for i := range bound {
		binding, err := DeclaredGoPackageBinding(*inventory, bound[i].Facts.Path, bound[i].Package.PackageName)
		if err != nil {
			return nil, err
		}
		bound[i].Package = binding
		if err := validateGoGraphFile(bound[i], bound[i].Facts.ProducerSHA256, sourceID, inventory); err != nil {
			return nil, err
		}
	}
	return bound, nil
}

func validateGoGraphFile(file GoGraphFile, producer, sourceID string, inventory *GoModuleInventory) error {
	if file.Facts.ProducerSHA256 != producer || file.Facts.Path == "" || filepath.Ext(file.Facts.Path) != ".go" || file.Facts.Coverage != "PARTIAL" || file.Facts.Schema != goFactsSchema || file.Facts.Language != "go" || file.Facts.ParserVersion != goFactsParserVersion || !lowerDigest(file.Facts.SourceSHA256) || !lowerDigest(file.Facts.BodySHA256) || !lowerDigest(file.Facts.CacheKey) {
		return errors.New("Go graph contains an invalid file fact binding")
	}
	if err := safepath.Relative(file.Facts.Path); err != nil {
		return errors.New("Go graph contains an invalid file path")
	}
	if err := validateGoPackageBinding(file.Package, file.Facts.Path, sourceID, inventory); err != nil {
		return err
	}
	if err := validateNormalizedGoGraphFacts(file.Facts); err != nil {
		return err
	}
	for _, symbol := range file.Facts.Declarations {
		if symbol.Name == "" || !validGoSymbolKind(symbol.Kind) || symbol.Range.StartByte < 0 || symbol.Range.EndByte <= symbol.Range.StartByte {
			return errors.New("Go graph contains invalid declaration fact")
		}
	}
	for _, imported := range file.Facts.Imports {
		if imported.Path == "" || len(imported.Path) > 4096 || imported.Range.StartByte < 0 || imported.Range.EndByte <= imported.Range.StartByte {
			return errors.New("Go graph contains invalid import fact")
		}
	}
	for _, call := range file.Facts.Calls {
		if call.Spelling == "" || call.Resolution != "UNRESOLVED" || call.Range.StartByte < 0 || call.Range.EndByte <= call.Range.StartByte {
			return errors.New("Go graph contains invalid call fact")
		}
	}
	return nil
}

func validateNormalizedGoGraphFacts(facts GoFileFacts) error {
	if facts.Cache != "" || facts.ParseCount != 0 {
		return errors.New("Go graph cache metrics were not normalized")
	}
	cacheKey, err := canonical.Hash("harness.ri.go-file-facts.v1", map[string]any{
		"schema": goFactsSchema, "language": "go", "parser": goFactsParserVersion,
		"path": facts.Path, "source_sha256": facts.SourceSHA256, "producer_sha256": facts.ProducerSHA256,
	})
	if err != nil || cacheKey != facts.CacheKey {
		return errors.New("Go graph file cache-key binding mismatch")
	}
	body := facts
	body.BodySHA256 = ""
	encoded, err := canonical.Bytes(body)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if facts.BodySHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("Go graph file fact digest mismatch")
	}
	return nil
}

func validateGoPackageBinding(binding GoPackageBinding, file, sourceID string, inventory *GoModuleInventory) error {
	if err := safepath.Relative(file); err != nil || filepath.Ext(file) != ".go" {
		return errors.New("Go graph package binding has an invalid source path")
	}
	switch binding.IdentityKind {
	case "":
		if binding.SourceID != "" || binding.SourceDirectory != "" || binding.PackageIdentity != "" || len(binding.ImportPath) == 0 || len(binding.ImportPath) > 4096 || len(binding.ModulePath) == 0 || len(binding.ModulePath) > 4096 || !validGoImportPath(binding.ImportPath) || !validGoImportPath(binding.ModulePath) {
			return errors.New("Go graph package binding is invalid")
		}
		if binding.ImportPath != binding.ModulePath && !strings.HasPrefix(binding.ImportPath, strings.TrimSuffix(binding.ModulePath, "/")+"/") {
			return errors.New("Go package import path is outside its explicit module path")
		}
	case "source_local_v1":
		if binding.ImportPath != "" || binding.ModulePath != "" || binding.TestOfImportPath != "" || binding.SourceID != sourceID || safepath.RequireDigest(binding.SourceID) != nil || binding.SourceDirectory != path.Dir(file) || (binding.SourceDirectory != "." && safepath.Relative(binding.SourceDirectory) != nil) || !validGoPackageName(binding.PackageName) {
			return errors.New("Go source-local package binding differs from source identity or directory")
		}
		expected, err := sourceLocalPackageIdentity(binding.SourceID, binding.SourceDirectory, binding.PackageName)
		if err != nil || binding.PackageIdentity != expected {
			return errors.New("Go source-local package identity is invalid")
		}
		testTargetName := ""
		if strings.HasSuffix(file, "_test.go") && strings.HasSuffix(binding.PackageName, "_test") {
			testTargetName = strings.TrimSuffix(binding.PackageName, "_test")
		}
		if testTargetName == "" {
			if binding.TestOfPackageIdentity != "" {
				return errors.New("unexpected Go source-local test target")
			}
		} else {
			if !validGoPackageName(testTargetName) {
				return errors.New("invalid external Go test package name")
			}
			expectedTestTarget, err := sourceLocalPackageIdentity(binding.SourceID, binding.SourceDirectory, testTargetName)
			if err != nil || binding.TestOfPackageIdentity != expectedTestTarget {
				return errors.New("Go source-local test target identity is invalid")
			}
		}
	case "declared_module_v1":
		if inventory == nil || binding.SourceID != sourceID || binding.InventoryDigest != inventory.Digest || binding.SourceDirectory != path.Dir(file) || (binding.SourceDirectory != "." && safepath.Relative(binding.SourceDirectory) != nil) || binding.ManifestPath == "" || binding.ManifestPath != path.Join(binding.ModuleRoot, "go.mod") || binding.ModuleRoot != "" && safepath.Relative(binding.ModuleRoot) != nil || !validGoPackageName(binding.PackageName) || !validGoImportPath(binding.ModulePath) || !validGoImportPath(binding.ImportPath) {
			return errors.New("Go declared-module package binding is invalid")
		}
		ownership, err := GoModuleOwnershipForPath(*inventory, file)
		if err != nil || ownership.Status != "declared_module" || binding.ModuleRoot != ownership.ModuleRoot || binding.ModulePath != ownership.ModulePath || binding.ImportPath != ownership.ImportPath {
			return errors.New("Go declared-module package ownership differs from inventory")
		}
		if binding.TestOfImportPath == "" {
			if binding.PackageIdentity != "" {
				return errors.New("unexpected declared-module package identity")
			}
		} else {
			if !strings.HasSuffix(file, "_test.go") || !strings.HasSuffix(binding.PackageName, "_test") || binding.TestOfImportPath != ownership.ImportPath {
				return errors.New("invalid declared-module external test binding")
			}
			expected, err := declaredGoPackageIdentity(binding)
			if err != nil || binding.PackageIdentity != expected {
				return errors.New("declared-module external test identity is invalid")
			}
		}
	default:
		return errors.New("unsupported Go package identity kind")
	}
	if binding.TestOfImportPath != "" && (!validGoImportPath(binding.TestOfImportPath) || !strings.HasSuffix(file, "_test.go")) {
		return errors.New("Go graph test package binding is invalid")
	}
	return nil
}

func validGoImportPath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\:\t\r\n ") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validGoPackageName(value string) bool { return len(value) <= 4096 && token.IsIdentifier(value) }

func sourceLocalPackageIdentity(sourceID, directory, packageName string) (string, error) {
	digest, err := canonical.Hash("harness.ri.go-source-local-package.v1", struct {
		SourceID    string `json:"source_id"`
		Directory   string `json:"directory"`
		PackageName string `json:"package_name"`
	}{sourceID, directory, packageName})
	if err != nil {
		return "", err
	}
	return "source_local_v1:" + digest, nil
}

func buildGoEngineeringGraph(sourceID, candidateID, producer string, files []GoGraphFile, generators []GoGeneratorBinding, inventory *GoModuleInventory) (GoEngineeringGraph, error) {
	if len(files) == 0 || len(files) > goEngineeringMaxFiles || len(generators) > goEngineeringMaxFacts {
		return GoEngineeringGraph{}, errors.New("Go graph build input exceeds bounds")
	}
	files = cloneGoGraphFiles(files)
	fileByPath := make(map[string]GoGraphFile, len(files))
	packageFiles := make(map[string][]string)
	totalFacts := 0
	for _, file := range files {
		if err := validateGoGraphFile(file, producer, sourceID, inventory); err != nil {
			return GoEngineeringGraph{}, err
		}
		if _, exists := fileByPath[file.Facts.Path]; exists {
			return GoEngineeringGraph{}, errors.New("Go graph repeats a file path")
		}
		totalFacts += len(file.Facts.Declarations) + len(file.Facts.Imports) + len(file.Facts.Calls) + len(file.Facts.GeneratedMarkers)
		if totalFacts > goEngineeringMaxFacts {
			return GoEngineeringGraph{}, errors.New("Go graph build exceeds aggregate fact budget")
		}
		fileByPath[file.Facts.Path] = cloneGoGraphFile(file)
		if file.Package.IdentityKind == "" || file.Package.IdentityKind == "declared_module_v1" {
			packageFiles[file.Package.ImportPath] = append(packageFiles[file.Package.ImportPath], file.Facts.Path)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Facts.Path < files[j].Facts.Path })
	generators = append([]GoGeneratorBinding(nil), generators...)
	// Go does not order structs; sort generator records explicitly.
	sort.Slice(generators, func(i, j int) bool {
		if generators[i].GeneratedPath != generators[j].GeneratedPath {
			return generators[i].GeneratedPath < generators[j].GeneratedPath
		}
		if generators[i].GeneratorPath != generators[j].GeneratorPath {
			return generators[i].GeneratorPath < generators[j].GeneratorPath
		}
		return generators[i].Directive < generators[j].Directive
	})
	nodesByID := make(map[string]GoGraphNode)
	edges := make([]GoGraphEdge, 0)
	addNode := func(node GoGraphNode) error {
		if prior, exists := nodesByID[node.ID]; exists && (prior.Kind != node.Kind || prior.Label != node.Label || prior.Path != node.Path) {
			return fmt.Errorf("Go graph node identity collision for %q (%s/%s, %s/%s)", node.ID, prior.Kind, prior.Label, node.Kind, node.Label)
		}
		nodesByID[node.ID] = node
		return nil
	}
	addEdge := func(edge GoGraphEdge) { edges = append(edges, edge) }
	for _, file := range files {
		path := file.Facts.Path
		fileID := graphFileNodeID(path)
		if err := addNode(GoGraphNode{ID: fileID, Kind: "file", Label: path, Path: path}); err != nil {
			return GoEngineeringGraph{}, err
		}
		pkgID := graphPackageBindingNodeID(file.Package)
		pkgKind, pkgLabel := graphPackageNodeKindLabel(file.Package)
		if err := addNode(GoGraphNode{ID: pkgID, Kind: pkgKind, Label: pkgLabel}); err != nil {
			return GoEngineeringGraph{}, err
		}
		addEdge(GoGraphEdge{From: fileID, To: pkgID, Relation: "IN_PACKAGE", Path: path})
		if file.Package.IdentityKind == "declared_module_v1" {
			addEdge(GoGraphEdge{From: fileID, To: pkgID, Relation: "DECLARED_OWNERSHIP", Path: path, Resolution: "DECLARED_MODULE_V1"})
		}
		if file.Package.IdentityKind == "source_local_v1" {
			if file.Package.TestOfPackageIdentity != "" {
				testPkgID := graphSourceLocalPackageNodeID(file.Package.TestOfPackageIdentity)
				if err := addNode(GoGraphNode{ID: testPkgID, Kind: "source_local_package", Label: file.Package.TestOfPackageIdentity}); err != nil {
					return GoEngineeringGraph{}, err
				}
				addEdge(GoGraphEdge{From: fileID, To: testPkgID, Relation: "TESTS", Path: path})
			} else if strings.HasSuffix(path, "_test.go") {
				addEdge(GoGraphEdge{From: fileID, To: pkgID, Relation: "TESTS", Path: path})
			}
		} else {
			testTarget := file.Package.TestOfImportPath
			if testTarget == "" && strings.HasSuffix(path, "_test.go") {
				testTarget = file.Package.ImportPath
			}
			if testTarget != "" {
				testPkgID := graphPackageNodeID(testTarget)
				if err := addNode(GoGraphNode{ID: testPkgID, Kind: "package", Label: testTarget}); err != nil {
					return GoEngineeringGraph{}, err
				}
				addEdge(GoGraphEdge{From: fileID, To: testPkgID, Relation: "TESTS", Path: path})
			}
		}
		declByName := make(map[string][]GoSymbol)
		for _, symbol := range file.Facts.Declarations {
			r := symbol.Range
			symbolID := graphSymbolNodeID(path, symbol)
			rCopy := r
			if err := addNode(GoGraphNode{ID: symbolID, Kind: "declaration", Label: symbol.Name, Path: path, Range: &rCopy}); err != nil {
				return GoEngineeringGraph{}, err
			}
			addEdge(GoGraphEdge{From: fileID, To: symbolID, Relation: "DECLARES", Path: path, Range: &rCopy})
			if symbol.Kind == "function_declaration" {
				declByName[symbol.Name] = append(declByName[symbol.Name], symbol)
			}
		}
		for _, imported := range file.Facts.Imports {
			importID := graphPackageNodeID(imported.Path)
			kind := "package"
			if _, local := packageFiles[imported.Path]; !local {
				kind = "import"
			}
			if err := addNode(GoGraphNode{ID: importID, Kind: kind, Label: imported.Path}); err != nil {
				return GoEngineeringGraph{}, err
			}
			r := imported.Range
			addEdge(GoGraphEdge{From: fileID, To: importID, Relation: "IMPORTS", Path: path, Range: &r, Alias: cloneStringPointer(imported.Alias)})
		}
		for _, call := range file.Facts.Calls {
			r := call.Range
			callID := graphCallNodeID(path, call)
			if err := addNode(GoGraphNode{ID: callID, Kind: "call", Label: call.Spelling, Path: path, Range: &r, Resolution: "UNRESOLVED"}); err != nil {
				return GoEngineeringGraph{}, err
			}
			addEdge(GoGraphEdge{From: fileID, To: callID, Relation: "CALLS_UNRESOLVED", Path: path, Range: &r, Resolution: "UNRESOLVED"})
			if isGoIdentifier(call.Spelling) && len(declByName[call.Spelling]) == 1 {
				symbol := declByName[call.Spelling][0]
				addEdge(GoGraphEdge{From: fileID, To: graphSymbolNodeID(path, symbol), Relation: "CALL_NAME_CANDIDATE", Path: path, Range: &r, Resolution: "UNRESOLVED"})
			}
		}
	}
	seenGenerators := make(map[string]struct{}, len(generators))
	for _, relation := range generators {
		key, _ := canonical.Hash("harness.ri.go-generator-relation.v1", relation)
		if _, duplicate := seenGenerators[key]; duplicate {
			return GoEngineeringGraph{}, errors.New("Go graph repeats a generator relation")
		}
		seenGenerators[key] = struct{}{}
		generator, ok := fileByPath[relation.GeneratorPath]
		if !ok || relation.GeneratorPath == relation.GeneratedPath || !strings.HasPrefix(relation.Directive, "//go:generate") || len(relation.Directive) > 4096 || !containsExact(generator.Facts.GeneratedMarkers, relation.Directive) {
			return GoEngineeringGraph{}, errors.New("Go generator relation lacks an exact source directive binding")
		}
		generated, ok := fileByPath[relation.GeneratedPath]
		if !ok || !hasGeneratedCodeMarker(generated.Facts.GeneratedMarkers) {
			return GoEngineeringGraph{}, errors.New("Go generator relation target lacks generated marker")
		}
		addEdge(GoGraphEdge{From: graphFileNodeID(relation.GeneratedPath), To: graphFileNodeID(relation.GeneratorPath), Relation: "GENERATED_BY", Path: relation.GeneratedPath, Resolution: "EXPLICIT_SOURCE_BOUND"})
	}
	nodes := make([]GoGraphNode, 0, len(nodesByID))
	for _, node := range nodesByID {
		nodes = append(nodes, cloneGoGraphNode(node))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return goGraphEdgeLess(edges[i], edges[j]) })
	if len(nodes) > goEngineeringMaxNodes || len(edges) > goEngineeringMaxEdges {
		return GoEngineeringGraph{}, errors.New("Go engineering graph exceeds node or edge budget")
	}
	// Remove duplicate exact relations but retain distinct source ranges.
	deduped := edges[:0]
	for _, edge := range edges {
		if len(deduped) == 0 || !equalGoGraphEdge(deduped[len(deduped)-1], edge) {
			deduped = append(deduped, edge)
		}
	}
	edges = deduped
	sourcePairs := make([]map[string]string, 0, len(files))
	for _, file := range files {
		sourcePairs = append(sourcePairs, map[string]string{"path": file.Facts.Path, "source_sha256": file.Facts.SourceSHA256})
	}
	sourceDigest, err := canonical.Hash("harness.ri.go-engineering-source.v1", sourcePairs)
	if err != nil {
		return GoEngineeringGraph{}, err
	}
	graph := GoEngineeringGraph{Schema: goEngineeringGraphSchema, SourceID: sourceID, CandidateID: candidateID, ProducerSHA256: producer, SourceDigest: sourceDigest, Coverage: "PARTIAL", Files: files, Generators: append([]GoGeneratorBinding{}, generators...), Nodes: nodes, Edges: edges}
	if inventory != nil {
		copy := cloneGoModuleInventory(*inventory)
		graph.ModuleInventory = &copy
	}
	graph.Digest, err = canonical.TypedGeneratedHashBounded("harness.ri.go-engineering-graph.v1", graph.content(), goEngineeringMaxBytes)
	if err != nil {
		return GoEngineeringGraph{}, err
	}
	return graph, nil
}

func (g GoEngineeringGraph) content() goEngineeringGraphContent {
	return goEngineeringGraphContent{Schema: g.Schema, SourceID: g.SourceID, CandidateID: g.CandidateID, ProducerSHA256: g.ProducerSHA256, SourceDigest: g.SourceDigest, Coverage: g.Coverage, Files: g.Files, Generators: g.Generators, Nodes: g.Nodes, Edges: g.Edges, ModuleInventory: g.ModuleInventory}
}

func normalizeGoGraphFacts(facts GoFileFacts) GoFileFacts {
	facts.Cache = ""
	facts.ParseCount = 0
	if facts.Declarations != nil {
		facts.Declarations = append([]GoSymbol{}, facts.Declarations...)
	}
	if facts.Imports != nil {
		facts.Imports = append([]GoImport{}, facts.Imports...)
	}
	for i := range facts.Imports {
		facts.Imports[i].Alias = cloneStringPointer(facts.Imports[i].Alias)
	}
	if facts.Calls != nil {
		facts.Calls = append([]GoCall{}, facts.Calls...)
	}
	if facts.GeneratedMarkers != nil {
		facts.GeneratedMarkers = append([]string{}, facts.GeneratedMarkers...)
	}
	return facts
}

func cloneGoGraphFile(file GoGraphFile) GoGraphFile {
	file.Facts = normalizeGoGraphFacts(file.Facts)
	return file
}
func cloneGoGraphFiles(files []GoGraphFile) []GoGraphFile {
	out := make([]GoGraphFile, len(files))
	for i := range files {
		out[i] = cloneGoGraphFile(files[i])
	}
	return out
}
func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneGoGraphNode(node GoGraphNode) GoGraphNode {
	if node.Range != nil {
		r := *node.Range
		node.Range = &r
	}
	return node
}
func cloneGoGraphEdge(edge GoGraphEdge) GoGraphEdge {
	if edge.Range != nil {
		r := *edge.Range
		edge.Range = &r
	}
	edge.Alias = cloneStringPointer(edge.Alias)
	return edge
}

func graphFileNodeID(path string) string {
	id, _ := canonical.Hash("harness.ri.go-file-node.v1", path)
	return "file:" + id
}
func graphPackageNodeID(importPath string) string {
	id, _ := canonical.Hash("harness.ri.go-package-node.v1", importPath)
	return "package:" + id
}
func graphPackageBindingNodeID(binding GoPackageBinding) string {
	if binding.IdentityKind == "source_local_v1" {
		return graphSourceLocalPackageNodeID(binding.PackageIdentity)
	}
	if binding.IdentityKind == "declared_module_v1" && binding.PackageIdentity != "" {
		return graphDeclaredModulePackageNodeID(binding.PackageIdentity)
	}
	return graphPackageNodeID(binding.ImportPath)
}
func graphSourceLocalPackageNodeID(identity string) string {
	return "source-package:" + strings.TrimPrefix(identity, "source_local_v1:")
}
func graphDeclaredModulePackageNodeID(identity string) string {
	return "declared-package:" + strings.TrimPrefix(identity, "declared_module_v1:")
}
func graphPackageNodeKindLabel(binding GoPackageBinding) (string, string) {
	if binding.IdentityKind == "source_local_v1" {
		return "source_local_package", binding.PackageIdentity
	}
	if binding.IdentityKind == "declared_module_v1" && binding.PackageIdentity != "" {
		return "declared_module_external_test_package", binding.PackageIdentity
	}
	return "package", binding.ImportPath
}

func declaredGoPackageIdentity(binding GoPackageBinding) (string, error) {
	digest, err := canonical.Hash("harness.ri.declared-go-external-test-package.v1", struct {
		SourceID        string `json:"source_id"`
		InventoryDigest string `json:"inventory_digest"`
		ModuleRoot      string `json:"module_root"`
		Directory       string `json:"directory"`
		PackageName     string `json:"package_name"`
	}{binding.SourceID, binding.InventoryDigest, binding.ModuleRoot, binding.SourceDirectory, binding.PackageName})
	if err != nil {
		return "", err
	}
	return "declared_module_v1:" + digest, nil
}
func graphSymbolNodeID(path string, symbol GoSymbol) string {
	id, _ := canonical.Hash("harness.ri.go-symbol-node.v1", map[string]any{"path": path, "name": symbol.Name, "kind": symbol.Kind, "range": symbol.Range, "test": symbol.Test})
	return "symbol:" + id
}
func graphCallNodeID(path string, call GoCall) string {
	id, _ := canonical.Hash("harness.ri.go-call-node.v1", map[string]any{"path": path, "spelling": call.Spelling, "range": call.Range})
	return "call:" + id
}
func isGoIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (i == 0 && !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')) || (i > 0 && !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
func hasGeneratedCodeMarker(markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(marker, "Code generated") {
			return true
		}
	}
	return false
}
func containsExact(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func validateGoQuery(limit int, path string) error {
	if limit < 1 || limit > goEngineeringMaxQuery {
		return errors.New("Go query limit is outside bounds")
	}
	if path != "" {
		if err := safepath.Relative(path); err != nil || filepath.Ext(path) != ".go" {
			return errors.New("Go query path is invalid")
		}
	}
	return nil
}
func nodeLabel(nodes []GoGraphNode, id string) string {
	i := sort.Search(len(nodes), func(i int) bool { return nodes[i].ID >= id })
	if i < len(nodes) && nodes[i].ID == id {
		return nodes[i].Label
	}
	return ""
}
func goGraphEdgeLess(a, b GoGraphEdge) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.Relation != b.Relation {
		return a.Relation < b.Relation
	}
	if a.To != b.To {
		return a.To < b.To
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	ar, br := GoRange{}, GoRange{}
	if a.Range != nil {
		ar = *a.Range
	}
	if b.Range != nil {
		br = *b.Range
	}
	if ar.StartByte != br.StartByte {
		return ar.StartByte < br.StartByte
	}
	if ar.EndByte != br.EndByte {
		return ar.EndByte < br.EndByte
	}
	if ptrValue(a.Alias) != ptrValue(b.Alias) {
		return ptrValue(a.Alias) < ptrValue(b.Alias)
	}
	return a.Resolution < b.Resolution
}
func equalGoGraphEdge(a, b GoGraphEdge) bool { return !goGraphEdgeLess(a, b) && !goGraphEdgeLess(b, a) }
func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func packageImportersValues(values map[string][]string) [][]string {
	out := make([][]string, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}
func callerValues(values map[string][]string) [][]string {
	out := make([][]string, 0, len(values))
	for _, v := range values {
		out = append(out, v)
	}
	return out
}
