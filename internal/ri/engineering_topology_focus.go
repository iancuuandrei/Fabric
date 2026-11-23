package ri

import (
	"errors"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

const goFocusTopologyMaxBytes = 8 << 10

// GoFocusTopology is a compact, source-bound advisory projection rooted at
// objective contract excerpts. Its relationships describe observed graph
// facts; they do not authorize writes or establish task independence.
type GoFocusTopology struct {
	Version               int                     `json:"version"`
	SeedBasis             string                  `json:"seed_basis"`
	GraphDigest           string                  `json:"graph_digest"`
	SourceID              string                  `json:"source_id"`
	CandidateID           string                  `json:"candidate_id"`
	Coverage              string                  `json:"coverage"`
	FocusPaths            []string                `json:"focus_paths"`
	ObservedPackages      []GoFocusTopologyModule `json:"observed_packages"`
	Couplings             []GoTopologyCoupling    `json:"couplings"`
	PotentialTests        []string                `json:"potential_tests"`
	ReviewGroups          []GoTopologyReviewGroup `json:"review_groups"`
	ImpactTruncated       bool                    `json:"impact_truncated"`
	Truncated             bool                    `json:"truncated"`
	OmittedPackages       int                     `json:"omitted_packages"`
	OmittedCouplings      int                     `json:"omitted_couplings"`
	OmittedPotentialTests int                     `json:"omitted_potential_tests"`
	OmittedReviewGroups   int                     `json:"omitted_review_groups"`
	OmittedReviewPaths    int                     `json:"omitted_review_paths"`
	Digest                string                  `json:"digest"`
}

// GoFocusTopologyModule reports observed package degree for a package
// containing a focus or impact path. Degrees count distinct graph bindings.
type GoFocusTopologyModule struct {
	Label                string `json:"label"`
	ObservedImporters    int    `json:"observed_importers"`
	ObservedDependencies int    `json:"observed_dependencies"`
	SharedHub            bool   `json:"shared_hub"`
}

// QueryGoFocusTopology reuses the bounded topology query while labeling its
// seeds as objective contract paths, not changed files. The result is a
// deterministic model-sized projection with an explicit digest and omission
// counts. It performs no parsing, model calls, tests, or write admission.
func QueryGoFocusTopology(graph GoEngineeringGraph, focusPaths []string, maxGroupFiles int) (GoFocusTopology, error) {
	var out GoFocusTopology
	base, err := queryGoTopology(graph, focusPaths, maxGroupFiles, false)
	if err != nil {
		return out, err
	}
	selected := make(map[string]bool)
	for _, group := range base.ReviewGroups {
		for _, path := range group.Paths {
			selected[path] = true
		}
	}
	packages := make(map[string]bool)
	for _, file := range graph.Files {
		if selected[file.Facts.Path] {
			packages[graphPackageBindingNodeID(file.Package)] = true
		}
	}
	modulesByID := make(map[string]GoTopologyModule, len(base.Modules))
	for _, module := range base.Modules {
		modulesByID[module.ID] = module
	}
	moduleIDs := make([]string, 0, len(packages))
	for id := range packages {
		if _, exists := modulesByID[id]; exists {
			moduleIDs = append(moduleIDs, id)
		}
	}
	sort.Strings(moduleIDs)
	observed := make([]GoFocusTopologyModule, 0, len(moduleIDs))
	for _, id := range moduleIDs {
		module := modulesByID[id]
		observed = append(observed, GoFocusTopologyModule{Label: module.Label, ObservedImporters: module.ObservedImporters, ObservedDependencies: module.ObservedDependencies, SharedHub: module.SharedHub})
	}
	out = GoFocusTopology{
		Version: 1, SeedBasis: "objective_contract", GraphDigest: base.GraphDigest, SourceID: base.SourceID,
		CandidateID: base.CandidateID, Coverage: "PARTIAL", FocusPaths: append([]string(nil), base.ChangedPaths...),
		ObservedPackages: observed, Couplings: append([]GoTopologyCoupling(nil), base.Couplings...),
		PotentialTests: append([]string(nil), base.PotentialTests...), ReviewGroups: append([]GoTopologyReviewGroup(nil), base.ReviewGroups...),
		ImpactTruncated: base.ImpactTruncated,
	}
	if err := trimGoFocusTopology(&out); err != nil {
		return GoFocusTopology{}, err
	}
	out.Truncated = out.ImpactTruncated || out.OmittedPackages+out.OmittedCouplings+out.OmittedPotentialTests+out.OmittedReviewGroups+out.OmittedReviewPaths > 0
	out.Digest, err = canonical.Hash("harness.ri.go-focus-topology.v1", out.content())
	if err != nil {
		return GoFocusTopology{}, err
	}
	encoded, err := canonical.Bytes(out)
	if err != nil || len(encoded) > goFocusTopologyMaxBytes {
		return GoFocusTopology{}, errors.New("Go focus topology exceeds bounded projection size")
	}
	return out, nil
}

func (result GoFocusTopology) content() GoFocusTopology {
	result.Digest = ""
	return result
}

func trimGoFocusTopology(result *GoFocusTopology) error {
	for {
		encoded, err := canonical.Bytes(result)
		if err != nil {
			return err
		}
		// Reserve the digest field's canonical JSON size before hashing.
		if len(encoded)+64 <= goFocusTopologyMaxBytes {
			return nil
		}
		switch {
		case len(result.PotentialTests) > 0:
			result.PotentialTests = result.PotentialTests[:len(result.PotentialTests)-1]
			result.OmittedPotentialTests++
		case len(result.ObservedPackages) > 0:
			result.ObservedPackages = result.ObservedPackages[:len(result.ObservedPackages)-1]
			result.OmittedPackages++
		case len(result.Couplings) > 0:
			result.Couplings = result.Couplings[:len(result.Couplings)-1]
			result.OmittedCouplings++
		case len(result.ReviewGroups) > 0:
			last := len(result.ReviewGroups) - 1
			group := &result.ReviewGroups[last]
			if len(group.Paths) > 1 {
				group.Paths = group.Paths[:len(group.Paths)-1]
				result.OmittedReviewPaths++
			} else {
				result.ReviewGroups = result.ReviewGroups[:last]
				result.OmittedReviewGroups++
			}
		default:
			return errors.New("Go focus topology required fields exceed bounded projection size")
		}
	}
}
