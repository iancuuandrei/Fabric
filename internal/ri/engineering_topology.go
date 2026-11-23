package ri

import (
	"errors"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

// GoTopologyModule describes a package in the observed corpus. Import degrees
// count distinct observed packages, not resolved calls or runtime coupling.
type GoTopologyModule struct {
	ID                   string   `json:"id"`
	Label                string   `json:"label"`
	Files                []string `json:"files"`
	Dependencies         []string `json:"dependencies"`
	Importers            []string `json:"importers"`
	ObservedImporters    int      `json:"observed_importers"`
	ObservedDependencies int      `json:"observed_dependencies"`
	SharedHub            bool     `json:"shared_hub"`
}

// GoTopologyCoupling is a source-bound advisory relation between files. Package
// membership and declared generation do not authorize writes or prove behavior.
type GoTopologyCoupling struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// GoTopologyReviewGroup is a deterministic bounded review partition. Splitting
// a component preserves its identity so a cross-cutting reviewer sees coupling.
type GoTopologyReviewGroup struct {
	ComponentID string   `json:"component_id"`
	Paths       []string `json:"paths"`
	Split       bool     `json:"split"`
}

// GoTopologyResult binds advisory topology to its immutable graph. Tests are
// potentially affected observations; they are never verification receipts.
type GoTopologyResult struct {
	Version         int                     `json:"version"`
	GraphDigest     string                  `json:"graph_digest"`
	SourceID        string                  `json:"source_id"`
	CandidateID     string                  `json:"candidate_id"`
	Coverage        string                  `json:"coverage"`
	Modules         []GoTopologyModule      `json:"modules"`
	Couplings       []GoTopologyCoupling    `json:"couplings"`
	ChangedPaths    []string                `json:"changed_paths"`
	PotentialTests  []string                `json:"potential_tests"`
	ReviewGroups    []GoTopologyReviewGroup `json:"review_groups"`
	ImpactTruncated bool                    `json:"impact_truncated"`
	Digest          string                  `json:"digest"`
}

// QueryGoTopology groups changed files and observed impact by package and
// explicit generator ownership. Hub degrees use only exact package bindings
// already present in the graph; source-local imports remain unresolved. This
// read-only query performs no parsing, model calls, tests, or write admission.
func QueryGoTopology(graph GoEngineeringGraph, changedPaths []string, maxGroupFiles int) (GoTopologyResult, error) {
	return queryGoTopology(graph, changedPaths, maxGroupFiles, true)
}

// queryGoTopology is shared by the full changed-path query and the compact
// objective-focus projection. The latter does not serialize the full module
// inventory or expose its legacy digest, so it need not inherit the 1 MiB
// public-result limit before compacting.
func queryGoTopology(graph GoEngineeringGraph, changedPaths []string, maxGroupFiles int, seal bool) (GoTopologyResult, error) {
	var out GoTopologyResult
	if maxGroupFiles < 1 || maxGroupFiles > 32 || len(changedPaths) == 0 || len(changedPaths) > goEngineeringMaxSeeds {
		return out, errors.New("Go topology query bounds are invalid")
	}
	impact, err := QueryGoImpact(graph, GoImpactQuery{Paths: changedPaths, MaxDepth: 3, Limit: 256})
	if err != nil {
		return out, err
	}
	changed := append([]string(nil), changedPaths...)
	sort.Strings(changed)
	out = GoTopologyResult{Version: 1, GraphDigest: graph.Digest, SourceID: graph.SourceID, CandidateID: graph.CandidateID, Coverage: "PARTIAL", ChangedPaths: changed, ImpactTruncated: impact.Truncated,
		Modules: []GoTopologyModule{}, Couplings: []GoTopologyCoupling{}, PotentialTests: []string{}, ReviewGroups: []GoTopologyReviewGroup{}}
	index := indexGoTopology(graph)
	out.Modules = index.modules(graph)
	paths := index.selectPaths(graph, changed, impact.Paths)
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			out.PotentialTests = append(out.PotentialTests, p)
		}
	}
	index.coupleFiles(graph, out.Modules)
	out.Couplings = index.couplings
	out.ReviewGroups, err = index.reviewGroups(graph.Digest, paths, maxGroupFiles)
	if err != nil {
		return GoTopologyResult{}, err
	}
	if seal {
		out.Digest, err = canonical.Hash("harness.ri.go-topology.v1", out)
		if err != nil {
			return GoTopologyResult{}, err
		}
		if encoded, encodeErr := canonical.Bytes(out); encodeErr != nil || len(encoded) > 1<<20 {
			return GoTopologyResult{}, errors.New("Go topology result exceeds bounded output")
		}
	}
	return out, nil
}

// goTopologyIndex uses linear-size membership edges rather than all file pairs.
type goTopologyIndex struct {
	packageFiles  map[string][]string
	filePackage   map[string]string
	dependencies  map[string]map[string]bool
	importers     map[string]map[string]bool
	selected      map[string]bool
	firstSelected map[string]string
	parent        map[string]string
	couplings     []GoTopologyCoupling
}

func indexGoTopology(graph GoEngineeringGraph) *goTopologyIndex {
	index := &goTopologyIndex{packageFiles: make(map[string][]string), filePackage: make(map[string]string), dependencies: make(map[string]map[string]bool), importers: make(map[string]map[string]bool), selected: make(map[string]bool), firstSelected: make(map[string]string), parent: make(map[string]string), couplings: []GoTopologyCoupling{}}
	for _, file := range graph.Files {
		id := graphPackageBindingNodeID(file.Package)
		index.packageFiles[id] = append(index.packageFiles[id], file.Facts.Path)
		index.filePackage[file.Facts.Path] = id
	}
	for _, edge := range graph.Edges {
		from := index.filePackage[edge.Path]
		if edge.Relation != "IMPORTS" || from == edge.To || len(index.packageFiles[edge.To]) == 0 {
			continue
		}
		addTopologyMembership(index.dependencies, from, edge.To)
		addTopologyMembership(index.importers, edge.To, from)
	}
	return index
}

func addTopologyMembership(groups map[string]map[string]bool, key, value string) {
	if groups[key] == nil {
		groups[key] = make(map[string]bool)
	}
	groups[key][value] = true
}

func (index *goTopologyIndex) modules(graph GoEngineeringGraph) []GoTopologyModule {
	modules := make([]GoTopologyModule, 0, len(index.packageFiles))
	for id, files := range index.packageFiles {
		modules = append(modules, GoTopologyModule{ID: id, Label: nodeLabel(graph.Nodes, id), Files: append([]string(nil), files...), Dependencies: topologyMembershipKeys(index.dependencies[id]), Importers: topologyMembershipKeys(index.importers[id]), ObservedImporters: len(index.importers[id]), ObservedDependencies: len(index.dependencies[id]), SharedHub: len(index.importers[id]) >= 2})
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].ID < modules[j].ID })
	return modules
}

func topologyMembershipKeys(members map[string]bool) []string {
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (index *goTopologyIndex) selectPaths(graph GoEngineeringGraph, changed, impact []string) []string {
	queue := append(append([]string(nil), changed...), impact...)
	generation := make(map[string][]string)
	for _, binding := range graph.Generators {
		generation[binding.GeneratorPath] = append(generation[binding.GeneratorPath], binding.GeneratedPath)
		generation[binding.GeneratedPath] = append(generation[binding.GeneratedPath], binding.GeneratorPath)
	}
	// Finite generator closure includes siblings sharing an owner. No command is executed.
	for head := 0; head < len(queue); head++ {
		p := queue[head]
		if index.selected[p] {
			continue
		}
		index.selected[p] = true
		queue = append(queue, generation[p]...)
	}
	paths := make([]string, 0, len(index.selected))
	for p := range index.selected {
		paths = append(paths, p)
		index.parent[p] = p
	}
	sort.Strings(paths)
	for _, p := range paths {
		pkg := index.filePackage[p]
		if index.firstSelected[pkg] == "" {
			index.firstSelected[pkg] = p
		}
	}
	return paths
}

func (index *goTopologyIndex) find(p string) string {
	for index.parent[p] != p {
		index.parent[p] = index.parent[index.parent[p]]
		p = index.parent[p]
	}
	return p
}

func (index *goTopologyIndex) join(a, b, reason string) {
	if !index.selected[a] || !index.selected[b] || a == b {
		return
	}
	if a > b {
		a, b = b, a
	}
	index.couplings = append(index.couplings, GoTopologyCoupling{From: a, To: b, Reason: reason})
	x, y := index.find(a), index.find(b)
	if x > y {
		x, y = y, x
	}
	index.parent[y] = x
}

func (index *goTopologyIndex) coupleFiles(graph GoEngineeringGraph, modules []GoTopologyModule) {
	for _, module := range modules {
		first := ""
		for _, p := range module.Files {
			if !index.selected[p] {
				continue
			}
			if first == "" {
				first = p
				continue
			}
			index.join(first, p, "SAME_OBSERVED_PACKAGE")
		}
	}
	for _, binding := range graph.Generators {
		index.join(binding.GeneratorPath, binding.GeneratedPath, "EXPLICIT_GENERATOR_OWNER")
	}
	for _, edge := range graph.Edges {
		index.coupleRelation(edge)
	}
	sort.Slice(index.couplings, func(i, j int) bool { return topologyCouplingLess(index.couplings[i], index.couplings[j]) })
	unique := index.couplings[:0]
	for _, c := range index.couplings {
		if len(unique) == 0 || unique[len(unique)-1] != c {
			unique = append(unique, c)
		}
	}
	index.couplings = unique
}

func (index *goTopologyIndex) coupleRelation(edge GoGraphEdge) {
	if !index.selected[edge.Path] {
		return
	}
	reason := ""
	switch edge.Relation {
	case "TESTS":
		reason = "POTENTIAL_PACKAGE_TEST"
	case "IMPORTS":
		reason = "OBSERVED_LOCAL_IMPORT"
	default:
		return
	}
	if p := index.firstSelected[edge.To]; p != "" {
		index.join(edge.Path, p, reason)
	}
}

func topologyCouplingLess(a, b GoTopologyCoupling) bool {
	if a.From != b.From {
		return a.From < b.From
	}
	if a.To != b.To {
		return a.To < b.To
	}
	return a.Reason < b.Reason
}

func (index *goTopologyIndex) reviewGroups(graphDigest string, paths []string, maxGroupFiles int) ([]GoTopologyReviewGroup, error) {
	components := make(map[string][]string)
	for _, p := range paths {
		id := index.find(p)
		components[id] = append(components[id], p)
	}
	roots := make([]string, 0, len(components))
	for root := range components {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	groups := []GoTopologyReviewGroup{}
	for _, root := range roots {
		files := components[root]
		id, err := canonical.Hash("harness.ri.go-review-component.v1", struct {
			Graph string   `json:"graph"`
			Paths []string `json:"paths"`
		}{graphDigest, files})
		if err != nil {
			return nil, err
		}
		for start := 0; start < len(files); start += maxGroupFiles {
			end := start + maxGroupFiles
			if end > len(files) {
				end = len(files)
			}
			groups = append(groups, GoTopologyReviewGroup{ComponentID: id, Paths: append([]string(nil), files[start:end]...), Split: len(files) > maxGroupFiles})
		}
	}
	return groups, nil
}
