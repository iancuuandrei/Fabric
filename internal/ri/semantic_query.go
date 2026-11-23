package ri

import (
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/safepath"
)

// ErrSemanticVocabularyUnsupported means the requested semantic vocabulary is
// not evidenced by the supplied graph. It is deliberately distinct from an
// empty result, which remains PARTIAL evidence rather than a completeness or
// absence claim.
var ErrSemanticVocabularyUnsupported = errors.New("semantic vocabulary unsupported by graph evidence")

// SemanticQuery selects bounded evidence from one immutable Go engineering
// graph. References and implementations require a semantic producer and are
// explicitly unsupported by this graph-only adapter.
type SemanticQuery struct {
	Vocabulary string   `json:"vocabulary"`
	Path       string   `json:"path,omitempty"`
	NamePrefix string   `json:"name_prefix,omitempty"`
	ImportPath string   `json:"import_path,omitempty"`
	Kind       string   `json:"kind,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	MaxDepth   int      `json:"max_depth,omitempty"`
	Limit      int      `json:"limit"`
}

// SemanticItem is one graph-backed declaration, relation, or path. Resolution
// is UNRESOLVED for syntactic call evidence and DECLARED for explicit source
// relations; an empty value carries no semantic-resolution assertion.
type SemanticItem struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Path       string   `json:"path"`
	Label      string   `json:"label,omitempty"`
	Relation   string   `json:"relation,omitempty"`
	Target     string   `json:"target,omitempty"`
	Range      *GoRange `json:"range,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

// SemanticResult binds returned evidence to one source/candidate graph and its
// pinned local parser producer. Coverage is always PARTIAL, including an empty
// item list; Truncated means the configured result bound was reached.
type SemanticResult struct {
	Version        int            `json:"version"`
	Vocabulary     string         `json:"vocabulary"`
	SourceID       string         `json:"source_id"`
	CandidateID    string         `json:"candidate_id,omitempty"`
	GraphDigest    string         `json:"graph_digest"`
	ProducerSHA256 string         `json:"producer_sha256"`
	Coverage       string         `json:"coverage"`
	Items          []SemanticItem `json:"items"`
	Truncated      bool           `json:"truncated"`
}

// QueryGoSemantic exposes the graph's observed declaration, import, syntactic
// call, test, generator, module, path, and bounded-impact evidence through a
// common result shape. It never resolves calls, type implementations, or
// semantic references.
func QueryGoSemantic(graph GoEngineeringGraph, query SemanticQuery) (SemanticResult, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return SemanticResult{}, err
	}
	if query.Limit < 1 || query.Limit > goEngineeringMaxQuery {
		return SemanticResult{}, errors.New("semantic query limit is outside bounds")
	}
	if query.Path != "" && safepath.Relative(query.Path) != nil {
		return SemanticResult{}, errors.New("semantic query path is invalid")
	}
	result := SemanticResult{Version: 1, Vocabulary: query.Vocabulary, SourceID: graph.SourceID, CandidateID: graph.CandidateID, GraphDigest: graph.Digest, ProducerSHA256: graph.ProducerSHA256, Coverage: "PARTIAL", Items: []SemanticItem{}}
	switch query.Vocabulary {
	case "symbol":
		items, truncated, err := semanticSymbols(graph, query)
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "imports":
		items, truncated, err := semanticEdges(graph, query, "IMPORTS")
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "calls":
		items, truncated, err := semanticCalls(graph, query)
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "tests":
		items, truncated, err := semanticEdges(graph, query, "TESTS")
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "generators":
		items, truncated, err := semanticEdges(graph, query, "GENERATED_BY")
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "module":
		items, truncated, err := semanticEdges(graph, query, "DECLARED_OWNERSHIP")
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "path":
		items, err := semanticPath(graph, query)
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items = items
	case "impact":
		items, truncated, err := semanticImpact(graph, query)
		if err != nil {
			return SemanticResult{}, err
		}
		result.Items, result.Truncated = items, truncated
	case "references", "implementations":
		return SemanticResult{}, ErrSemanticVocabularyUnsupported
	default:
		return SemanticResult{}, errors.New("unknown semantic vocabulary")
	}
	return result, nil
}

func semanticSymbols(graph GoEngineeringGraph, query SemanticQuery) ([]SemanticItem, bool, error) {
	if query.ImportPath != "" || len(query.Paths) != 0 || query.MaxDepth != 0 || query.NamePrefix == "" && query.Path == "" && query.Kind == "" {
		return nil, false, errors.New("symbol query requires a name, path, or kind selector")
	}
	nodes, err := QueryGoSymbols(graph, GoSymbolQuery{NamePrefix: query.NamePrefix, Kind: query.Kind, Path: query.Path, Limit: goEngineeringMaxQuery})
	if err != nil {
		return nil, false, err
	}
	items := make([]SemanticItem, 0, min(query.Limit, len(nodes)))
	for _, node := range nodes {
		items = append(items, semanticNode(node, "symbol"))
	}
	items, truncated := semanticLimit(items, query.Limit)
	// The underlying graph query has the same hard upper bound and cannot
	// distinguish exactly-full from an omitted next result. Report the bound
	// conservatively so a caller never treats this result as exhaustive.
	truncated = truncated || len(nodes) == goEngineeringMaxQuery
	return items, truncated, nil
}

func semanticCalls(graph GoEngineeringGraph, query SemanticQuery) ([]SemanticItem, bool, error) {
	if query.ImportPath != "" || len(query.Paths) != 0 || query.Kind != "" || query.MaxDepth != 0 || query.NamePrefix == "" && query.Path == "" {
		return nil, false, errors.New("calls query requires a spelling or path selector")
	}
	edges, err := QueryGoCalls(graph, GoCallQuery{SpellingPrefix: query.NamePrefix, Path: query.Path, Limit: goEngineeringMaxQuery})
	if err != nil {
		return nil, false, err
	}
	items := make([]SemanticItem, 0, min(query.Limit, len(edges)))
	for _, edge := range edges {
		item := semanticEdge(graph, edge)
		item.Kind = "call"
		item.Resolution = "UNRESOLVED"
		items = append(items, item)
	}
	items, truncated := semanticLimit(items, query.Limit)
	// See semanticSymbols: an exactly-full underlying result is conservatively
	// marked truncated because the graph query does not expose a total count.
	truncated = truncated || len(edges) == goEngineeringMaxQuery
	return items, truncated, nil
}

func semanticEdges(graph GoEngineeringGraph, query SemanticQuery, relation string) ([]SemanticItem, bool, error) {
	if query.NamePrefix != "" || query.Kind != "" || len(query.Paths) != 0 || query.MaxDepth != 0 {
		return nil, false, errors.New("semantic relation query carries unsupported selectors")
	}
	if query.Path == "" && query.ImportPath == "" {
		return nil, false, errors.New("semantic relation query requires a path or import selector")
	}
	edges := make([]SemanticItem, 0)
	for _, edge := range graph.Edges {
		if edge.Relation != relation || query.Path != "" && edge.Path != query.Path {
			continue
		}
		item := semanticEdge(graph, edge)
		if query.ImportPath != "" && item.Target != query.ImportPath {
			continue
		}
		switch relation {
		case "IMPORTS":
			item.Kind = "import"
		case "TESTS":
			item.Kind = "test"
		case "GENERATED_BY":
			item.Kind = "generator"
		case "DECLARED_OWNERSHIP":
			item.Kind, item.Resolution = "module", "DECLARED"
		}
		edges = append(edges, item)
	}
	sortSemanticItems(edges)
	edges, truncated := semanticLimit(edges, query.Limit)
	return edges, truncated, nil
}

func semanticPath(graph GoEngineeringGraph, query SemanticQuery) ([]SemanticItem, error) {
	if query.Path == "" || query.NamePrefix != "" || query.ImportPath != "" || query.Kind != "" || len(query.Paths) != 0 || query.MaxDepth != 0 {
		return nil, errors.New("path query requires one exact path")
	}
	for _, file := range graph.Files {
		if file.Facts.Path == query.Path {
			_, label := graphPackageNodeKindLabel(file.Package)
			return []SemanticItem{{ID: graphFileNodeID(file.Facts.Path), Kind: "path", Path: file.Facts.Path, Label: label}}, nil
		}
	}
	return nil, errors.New("path is absent from graph")
}

func semanticImpact(graph GoEngineeringGraph, query SemanticQuery) ([]SemanticItem, bool, error) {
	if query.Path != "" || query.NamePrefix != "" || query.ImportPath != "" || query.Kind != "" || len(query.Paths) == 0 {
		return nil, false, errors.New("impact query requires exact source paths")
	}
	depth := query.MaxDepth
	if depth == 0 {
		depth = 1
	}
	impact, err := QueryGoImpact(graph, GoImpactQuery{Paths: query.Paths, MaxDepth: depth, Limit: query.Limit})
	if err != nil {
		return nil, false, err
	}
	items := make([]SemanticItem, 0, len(impact.Paths))
	for _, path := range impact.Paths {
		items = append(items, SemanticItem{ID: graphFileNodeID(path), Kind: "impact", Path: path})
	}
	return items, impact.Truncated, nil
}

func semanticNode(node GoGraphNode, kind string) SemanticItem {
	item := SemanticItem{ID: node.ID, Kind: kind, Path: node.Path, Label: node.Label, Resolution: node.Resolution}
	if node.Range != nil {
		rangeCopy := *node.Range
		item.Range = &rangeCopy
	}
	return item
}

func semanticEdge(graph GoEngineeringGraph, edge GoGraphEdge) SemanticItem {
	item := SemanticItem{ID: edge.From + ":" + edge.Relation + ":" + edge.To, Path: edge.Path, Relation: edge.Relation, Target: nodeLabel(graph.Nodes, edge.To), Resolution: edge.Resolution}
	if edge.Range != nil {
		rangeCopy := *edge.Range
		item.Range = &rangeCopy
	}
	return item
}

func semanticLimit(items []SemanticItem, limit int) ([]SemanticItem, bool) {
	sortSemanticItems(items)
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated
}

func sortSemanticItems(items []SemanticItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Path != items[j].Path {
			return items[i].Path < items[j].Path
		}
		if items[i].Relation != items[j].Relation {
			return items[i].Relation < items[j].Relation
		}
		if items[i].Target != items[j].Target {
			return items[i].Target < items[j].Target
		}
		return items[i].ID < items[j].ID
	})
}
