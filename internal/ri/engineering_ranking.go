package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goEngineeringRankingSchema         = "engorch.ri.go-engineering-ranking.v1"
	goEngineeringRankingMaxTopN        = 32
	goEngineeringRankingMaxQuery       = 2048
	goEngineeringRankingMaxTerms       = 64
	goEngineeringRankingMaxBytes       = 64 << 10
	goEngineeringRankingMaxSeedPath    = 64
	goEngineeringRankingCommunityScope = "top_n_matches_plus_observed_topology_couplings_and_generator_closure"
)

// GoEngineeringRankingQuery asks for lexical and observed-structure orientation
// over one already-built partial Go graph. Objective matching is exact token
// overlap, not semantic search or name resolution. Degree centrality is the
// sum of distinct observed local-package importers and dependencies.
type GoEngineeringRankingQuery struct {
	Objective string `json:"objective"`
	TopN      int    `json:"top_n"`
}

// GoEngineeringRankingItem is one source-bound file suggestion. Import degrees
// count distinct observed local package bindings; CommunityID describes only
// the bounded topology closure for this query's returned items.
type GoEngineeringRankingItem struct {
	Path                           string   `json:"path"`
	PackageLabel                   string   `json:"package_label"`
	LexicalScore                   int      `json:"lexical_score"`
	NameMatches                    []string `json:"name_matches"`
	PathMatches                    []string `json:"path_matches"`
	ObservedImporters              int      `json:"observed_importers"`
	ObservedDependencies           int      `json:"observed_dependencies"`
	ObservedImportDegreeCentrality int      `json:"observed_import_degree_centrality"`
	ObservedPackageFileCount       int      `json:"observed_package_file_count"`
	ObservedGeneratorLinks         int      `json:"observed_generator_links"`
	CommunityID                    string   `json:"community_id,omitempty"`
}

// GoEngineeringRanking is a deterministic advisory view over an immutable
// partial graph. It does not establish active build resolution, call
// reachability, test execution, task independence, or write authority.
type GoEngineeringRanking struct {
	Schema         string                     `json:"schema"`
	Version        int                        `json:"version"`
	Recipe         string                     `json:"recipe"`
	SourceID       string                     `json:"source_id"`
	CandidateID    string                     `json:"candidate_id,omitempty"`
	ProducerSHA256 string                     `json:"producer_sha256"`
	GraphDigest    string                     `json:"graph_digest"`
	QuerySHA256    string                     `json:"query_sha256"`
	Coverage       string                     `json:"coverage"`
	TopN           int                        `json:"top_n"`
	CommunityScope string                     `json:"community_scope"`
	Items          []GoEngineeringRankingItem `json:"items"`
	Truncated      bool                       `json:"truncated"`
	OmittedMatches int                        `json:"omitted_matches"`
	Digest         string                     `json:"digest"`
}

type goEngineeringRankingContent struct {
	Schema         string                     `json:"schema"`
	Version        int                        `json:"version"`
	Recipe         string                     `json:"recipe"`
	SourceID       string                     `json:"source_id"`
	CandidateID    string                     `json:"candidate_id,omitempty"`
	ProducerSHA256 string                     `json:"producer_sha256"`
	GraphDigest    string                     `json:"graph_digest"`
	QuerySHA256    string                     `json:"query_sha256"`
	Coverage       string                     `json:"coverage"`
	TopN           int                        `json:"top_n"`
	CommunityScope string                     `json:"community_scope"`
	Items          []GoEngineeringRankingItem `json:"items"`
	Truncated      bool                       `json:"truncated"`
	OmittedMatches int                        `json:"omitted_matches"`
}

// QueryGoEngineeringRanking combines exact objective-token matches in file
// paths and declaration names with observed package import-degree centrality.
// Lexical score is 2 per distinct declaration-name match plus 1 per distinct
// path-token match; structural degree breaks equal lexical scores. Ties are
// resolved by path. Components reuse the existing topology coupling algorithm
// over the returned paths and their explicit generator closure only.
func QueryGoEngineeringRanking(graph GoEngineeringGraph, query GoEngineeringRankingQuery) (GoEngineeringRanking, error) {
	if err := ValidateGoEngineeringGraph(graph); err != nil {
		return GoEngineeringRanking{}, err
	}
	terms, err := validateGoEngineeringRankingQuery(query)
	if err != nil {
		return GoEngineeringRanking{}, err
	}
	index := indexGoTopology(graph)
	modules := index.modules(graph)
	moduleByID := make(map[string]GoTopologyModule, len(modules))
	for _, module := range modules {
		moduleByID[module.ID] = module
	}
	generatorLinks := make(map[string]int)
	for _, relation := range graph.Generators {
		generatorLinks[relation.GeneratorPath]++
		generatorLinks[relation.GeneratedPath]++
	}

	items := make([]GoEngineeringRankingItem, 0, min(query.TopN, len(graph.Files)))
	for _, file := range graph.Files {
		path := file.Facts.Path
		if !taskcontext.EligiblePath(path) {
			continue
		}
		pathTerms := goRankingTokens(strings.TrimSuffix(path, ".go"))
		nameTerms := make(map[string]bool)
		for _, declaration := range file.Facts.Declarations {
			for term := range goRankingTokens(declaration.Name) {
				nameTerms[term] = true
			}
		}
		nameMatches := make([]string, 0)
		pathMatches := make([]string, 0)
		for _, term := range terms {
			if nameTerms[term] {
				nameMatches = append(nameMatches, term)
			}
			if pathTerms[term] {
				pathMatches = append(pathMatches, term)
			}
		}
		if len(nameMatches)+len(pathMatches) == 0 {
			continue
		}
		packageID := index.filePackage[path]
		module := moduleByID[packageID]
		items = append(items, GoEngineeringRankingItem{
			Path: path, PackageLabel: module.Label,
			LexicalScore: 2*len(nameMatches) + len(pathMatches),
			NameMatches:  nameMatches, PathMatches: pathMatches,
			ObservedImporters:              module.ObservedImporters,
			ObservedDependencies:           module.ObservedDependencies,
			ObservedImportDegreeCentrality: module.ObservedImporters + module.ObservedDependencies,
			ObservedPackageFileCount:       len(module.Files),
			ObservedGeneratorLinks:         generatorLinks[path],
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].LexicalScore != items[j].LexicalScore {
			return items[i].LexicalScore > items[j].LexicalScore
		}
		leftDegree := items[i].ObservedImportDegreeCentrality
		rightDegree := items[j].ObservedImportDegreeCentrality
		if leftDegree != rightDegree {
			return leftDegree > rightDegree
		}
		if len(items[i].NameMatches) != len(items[j].NameMatches) {
			return len(items[i].NameMatches) > len(items[j].NameMatches)
		}
		return items[i].Path < items[j].Path
	})
	matchCount := len(items)
	truncated := matchCount > query.TopN
	if truncated {
		items = items[:query.TopN]
	}
	focusPaths := make([]string, len(items))
	for i := range items {
		focusPaths[i] = items[i].Path
	}
	if len(focusPaths) > goEngineeringRankingMaxSeedPath {
		focusPaths = focusPaths[:goEngineeringRankingMaxSeedPath]
		truncated = true
	}
	if len(focusPaths) > 0 {
		selected := index.selectPaths(graph, focusPaths, nil)
		index.coupleFiles(graph, modules)
		groups, err := index.reviewGroups(graph.Digest, selected, goEngineeringRankingMaxTopN)
		if err != nil {
			return GoEngineeringRanking{}, err
		}
		communityByPath := make(map[string]string)
		for _, group := range groups {
			for _, path := range group.Paths {
				communityByPath[path] = group.ComponentID
			}
		}
		for i := range items {
			items[i].CommunityID = communityByPath[items[i].Path]
		}
	}
	queryDigest := sha256.Sum256([]byte(query.Objective))
	result := GoEngineeringRanking{
		Schema: goEngineeringRankingSchema, Version: 1, Recipe: "objective-token-import-degree-v1",
		SourceID: graph.SourceID, CandidateID: graph.CandidateID, ProducerSHA256: graph.ProducerSHA256,
		GraphDigest: graph.Digest, QuerySHA256: hex.EncodeToString(queryDigest[:]), Coverage: "PARTIAL",
		TopN: query.TopN, CommunityScope: goEngineeringRankingCommunityScope,
		Items: items, Truncated: truncated, OmittedMatches: matchCount - len(items),
	}
	content := result.content()
	result.Digest, err = canonical.Hash("harness.ri.go-engineering-ranking.v1", content)
	if err != nil {
		return GoEngineeringRanking{}, err
	}
	encoded, err := canonical.Bytes(result)
	if err != nil || len(encoded) > goEngineeringRankingMaxBytes {
		return GoEngineeringRanking{}, errors.New("Go engineering ranking exceeds bounded output")
	}
	return result, nil
}

// ValidateGoEngineeringRanking recomputes the ranking from the bound graph and
// exact query; altered evidence or a self-recomputed digest is rejected.
func ValidateGoEngineeringRanking(graph GoEngineeringGraph, query GoEngineeringRankingQuery, result GoEngineeringRanking) error {
	expected, err := QueryGoEngineeringRanking(graph, query)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, result) {
		return errors.New("Go engineering ranking differs from its bound graph and query")
	}
	return nil
}

func validateGoEngineeringRankingQuery(query GoEngineeringRankingQuery) ([]string, error) {
	if len(query.Objective) == 0 || len(query.Objective) > goEngineeringRankingMaxQuery || !utf8.ValidString(query.Objective) || strings.TrimSpace(query.Objective) == "" || query.TopN < 1 || query.TopN > goEngineeringRankingMaxTopN {
		return nil, errors.New("Go engineering ranking query exceeds bounds")
	}
	set := goRankingTokens(query.Objective)
	if len(set) == 0 || len(set) > goEngineeringRankingMaxTerms {
		return nil, errors.New("Go engineering ranking objective has no bounded search terms")
	}
	terms := make([]string, 0, len(set))
	for term := range set {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms, nil
}

// ValidateGoEngineeringRankingQuery rejects queries that cannot be ranked
// within this package's deterministic objective and output bounds.
func ValidateGoEngineeringRankingQuery(query GoEngineeringRankingQuery) error {
	_, err := validateGoEngineeringRankingQuery(query)
	return err
}

func goRankingTokens(value string) map[string]bool {
	result := make(map[string]bool)
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return !(r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r))
	}) {
		token = strings.ToLower(token)
		if token != "" {
			result[token] = true
		}
	}
	return result
}

func (result GoEngineeringRanking) content() goEngineeringRankingContent {
	return goEngineeringRankingContent{
		Schema: result.Schema, Version: result.Version, Recipe: result.Recipe,
		SourceID: result.SourceID, CandidateID: result.CandidateID, ProducerSHA256: result.ProducerSHA256,
		GraphDigest: result.GraphDigest, QuerySHA256: result.QuerySHA256, Coverage: result.Coverage,
		TopN: result.TopN, CommunityScope: result.CommunityScope,
		Items: result.Items, Truncated: result.Truncated, OmittedMatches: result.OmittedMatches,
	}
}
