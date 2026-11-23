package ri

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	goCandidateReviewTopologyMaxBytes = 8 << 10
	goCandidateReviewTopologyMaxList  = 12
	goCandidateReviewCallHintLimit    = 24
)

// GoCandidateReviewCallHint is a syntax-only call site in a selected impact
// group. Resolution is always UNRESOLVED; it is not a dependency assertion.
type GoCandidateReviewCallHint struct {
	Path       string `json:"path"`
	Spelling   string `json:"spelling"`
	StartByte  int    `json:"start_byte"`
	EndByte    int    `json:"end_byte"`
	Resolution string `json:"resolution"`
}

// GoCandidateReviewGeneratorHint preserves a committed generator relation
// touching candidate changes as historical evidence only.
type GoCandidateReviewGeneratorHint struct {
	GeneratorPath string `json:"generator_path"`
	GeneratedPath string `json:"generated_path"`
	Status        string `json:"status"`
}

// GoCandidateReviewTopology is an 8 KiB candidate-bound advisory projection.
// It contains no source bytes, execution receipts, or write authority.
type GoCandidateReviewTopology struct {
	Version                        int                              `json:"version"`
	Source                         Source                           `json:"source"`
	CandidateID                    string                           `json:"candidate_id"`
	CandidateFilesHash             string                           `json:"candidate_files_hash"`
	ProducerSHA256                 string                           `json:"producer_sha256"`
	BaseGraphDigest                string                           `json:"base_graph_digest"`
	BaseModuleInventoryDigest      string                           `json:"base_module_inventory_digest"`
	CandidateGraphDigest           string                           `json:"candidate_graph_digest"`
	CandidateModuleInventoryDigest string                           `json:"candidate_module_inventory_digest"`
	Coverage                       string                           `json:"coverage"`
	ChangedPathCount               int                              `json:"changed_path_count"`
	ChangedPaths                   []string                         `json:"changed_paths"`
	UnlistedChangedPathCount       int                              `json:"unlisted_changed_path_count"`
	AdmittedPathCount              int                              `json:"admitted_path_count"`
	AdmittedPaths                  []string                         `json:"admitted_paths"`
	UnlistedAdmittedPathCount      int                              `json:"unlisted_admitted_path_count"`
	DeletedPathCount               int                              `json:"deleted_path_count"`
	DeletedPaths                   []string                         `json:"deleted_paths"`
	UnlistedDeletedPathCount       int                              `json:"unlisted_deleted_path_count"`
	OmittedCount                   int                              `json:"omitted_count"`
	Omissions                      []taskcontext.Omission           `json:"omissions"`
	OmissionsTrimmed               bool                             `json:"omissions_trimmed"`
	UnrepresentedChangedPathCount  int                              `json:"unrepresented_changed_path_count"`
	UnrepresentedAdmittedPathCount int                              `json:"unrepresented_admitted_path_count"`
	ObservedPackages               []GoFocusTopologyModule          `json:"observed_packages"`
	Couplings                      []GoTopologyCoupling             `json:"couplings"`
	PotentialTests                 []string                         `json:"potential_tests"`
	PotentialCallHints             []GoCandidateReviewCallHint      `json:"potential_call_hints"`
	BaseGeneratorHints             []GoCandidateReviewGeneratorHint `json:"base_generator_hints"`
	UnlistedBaseGeneratorHintCount int                              `json:"unlisted_base_generator_hint_count"`
	ReviewGroups                   []GoTopologyReviewGroup          `json:"review_groups"`
	ImpactTruncated                bool                             `json:"impact_truncated"`
	Truncated                      bool                             `json:"truncated"`
	UnavailableReason              string                           `json:"unavailable_reason,omitempty"`
	OmittedPackages                int                              `json:"omitted_packages"`
	OmittedCouplings               int                              `json:"omitted_couplings"`
	OmittedPotentialTests          int                              `json:"omitted_potential_tests"`
	OmittedCallHints               int                              `json:"omitted_call_hints"`
	OmittedReviewGroups            int                              `json:"omitted_review_groups"`
	OmittedReviewPaths             int                              `json:"omitted_review_paths"`
	OmittedCorpusOmissions         int                              `json:"omitted_corpus_omissions"`
	Digest                         string                           `json:"digest"`
}

// QueryGoCandidateReviewTopology projects a collected candidate corpus for a
// reviewer. Changed and admitted paths are separate; only paths still present
// in the candidate graph seed impact traversal. A deletion-only candidate
// returns an explicit unavailable reason while preserving deletion evidence.
func QueryGoCandidateReviewTopology(baseGraph GoEngineeringGraph, baseInventory GoModuleInventory, corpus GoCandidateCorpus, maxGroupFiles int) (GoCandidateReviewTopology, error) {
	var out GoCandidateReviewTopology
	if maxGroupFiles < 1 || maxGroupFiles > 32 {
		return out, errors.New("candidate review topology group limit is invalid")
	}
	if encoded, err := canonical.Bytes(corpus); err != nil || len(encoded) > 2<<20 {
		return out, errors.New("candidate review corpus exceeds bounded input size")
	}
	if err := validateGoCandidateReviewCorpus(baseGraph, baseInventory, corpus); err != nil {
		return out, err
	}
	graphPaths := make(map[string]bool, len(corpus.Graph.Files))
	for _, file := range corpus.Graph.Files {
		graphPaths[file.Facts.Path] = true
	}
	changed, missingChanged := candidateReviewPresentPaths(corpus.ChangedPaths, graphPaths)
	admitted, missingAdmitted := candidateReviewPresentPaths(corpus.AdmittedPaths, graphPaths)
	deleted := append([]string(nil), corpus.DeletedPaths...)
	seeds := uniqueSortedCandidatePaths(append(append([]string(nil), changed...), admitted...))
	if len(seeds) > goEngineeringMaxSeeds {
		return out, errors.New("candidate review topology seed count exceeds bound")
	}
	listedChanged := candidateReviewBoundedPaths(changed, goCandidateReviewTopologyMaxList)
	listedAdmitted := candidateReviewBoundedPaths(admitted, goCandidateReviewTopologyMaxList)
	listedDeleted := candidateReviewBoundedPaths(deleted, goCandidateReviewTopologyMaxList)
	// Omissions is required by the projection schema. Preserve an empty JSON
	// array rather than turning a nonnil empty input into null.
	listedOmissions := append([]taskcontext.Omission{}, corpus.Omissions...)
	if len(listedOmissions) > goCandidateReviewTopologyMaxList {
		listedOmissions = listedOmissions[:goCandidateReviewTopologyMaxList]
	}
	out = GoCandidateReviewTopology{
		Version: 1, Source: corpus.Source, CandidateID: corpus.CandidateID, CandidateFilesHash: corpus.CandidateFilesHash,
		ProducerSHA256: corpus.ProducerSHA256, BaseGraphDigest: corpus.BaseGraphDigest,
		BaseModuleInventoryDigest: corpus.BaseModuleInventoryDigest, CandidateGraphDigest: corpus.Graph.Digest,
		CandidateModuleInventoryDigest: corpus.ModuleInventory.Digest, Coverage: "PARTIAL",
		ChangedPathCount: len(corpus.ChangedPaths), ChangedPaths: listedChanged,
		UnlistedChangedPathCount: len(corpus.ChangedPaths) - len(listedChanged),
		AdmittedPathCount:        len(corpus.AdmittedPaths), AdmittedPaths: listedAdmitted,
		UnlistedAdmittedPathCount: len(corpus.AdmittedPaths) - len(listedAdmitted),
		DeletedPathCount:          len(deleted), DeletedPaths: listedDeleted,
		UnlistedDeletedPathCount: len(deleted) - len(listedDeleted),
		OmittedCount:             corpus.OmittedCount, Omissions: listedOmissions, OmissionsTrimmed: corpus.OmissionsTrimmed || len(corpus.Omissions) > len(listedOmissions),
		UnrepresentedChangedPathCount: missingChanged, UnrepresentedAdmittedPathCount: missingAdmitted,
		ObservedPackages: []GoFocusTopologyModule{}, Couplings: []GoTopologyCoupling{}, PotentialTests: []string{},
		PotentialCallHints: []GoCandidateReviewCallHint{}, BaseGeneratorHints: candidateReviewBaseGeneratorHints(baseGraph, corpus), ReviewGroups: []GoTopologyReviewGroup{},
		OmittedCorpusOmissions: max(0, corpus.OmittedCount-len(listedOmissions)),
	}
	if len(out.BaseGeneratorHints) > goCandidateReviewTopologyMaxList {
		out.UnlistedBaseGeneratorHintCount = len(out.BaseGeneratorHints) - goCandidateReviewTopologyMaxList
		out.BaseGeneratorHints = out.BaseGeneratorHints[:goCandidateReviewTopologyMaxList]
		out.Truncated = true
	}
	if len(seeds) == 0 {
		out.UnavailableReason = "no_candidate_go_seeds"
	} else {
		base, err := queryGoTopology(corpus.Graph, seeds, maxGroupFiles, false)
		if err != nil {
			return GoCandidateReviewTopology{}, err
		}
		out.ImpactTruncated = base.ImpactTruncated
		// These are required array fields in the projection. Copy into empty,
		// nonnil slices so an empty query result remains [] in JSON, not null.
		out.Couplings = append([]GoTopologyCoupling{}, base.Couplings...)
		out.PotentialTests = append([]string{}, base.PotentialTests...)
		out.ReviewGroups = append([]GoTopologyReviewGroup{}, base.ReviewGroups...)
		selected := make(map[string]bool)
		for _, group := range base.ReviewGroups {
			for _, path := range group.Paths {
				selected[path] = true
			}
		}
		out.ObservedPackages = candidateReviewObservedPackages(corpus.Graph, base.Modules, selected)
		callHints := candidateReviewCallHints(corpus.Graph, selected)
		out.PotentialCallHints = callHints
		if len(out.PotentialCallHints) > goCandidateReviewCallHintLimit {
			out.OmittedCallHints = len(out.PotentialCallHints) - goCandidateReviewCallHintLimit
			out.PotentialCallHints = out.PotentialCallHints[:goCandidateReviewCallHintLimit]
		}
	}
	out.Truncated = out.ImpactTruncated || corpus.OmittedCount > 0 || missingChanged+missingAdmitted > 0 || out.UnlistedChangedPathCount+out.UnlistedAdmittedPathCount+out.UnlistedDeletedPathCount+out.UnlistedBaseGeneratorHintCount+out.OmittedCallHints > 0
	if err := trimGoCandidateReviewTopology(&out); err != nil {
		return GoCandidateReviewTopology{}, err
	}
	digest, err := canonical.Hash("harness.ri.go-candidate-review-topology.v1", out.content())
	if err != nil {
		return GoCandidateReviewTopology{}, err
	}
	out.Digest = digest
	encoded, err := canonical.Bytes(out)
	if err != nil || len(encoded) > goCandidateReviewTopologyMaxBytes {
		return GoCandidateReviewTopology{}, errors.New("candidate review topology exceeds bounded output size")
	}
	return out, nil
}

// ValidateGoCandidateReviewTopology recomputes the bounded output and rejects
// altered evidence, bindings, ordering, counts, or truncation claims.
func ValidateGoCandidateReviewTopology(result GoCandidateReviewTopology, baseGraph GoEngineeringGraph, baseInventory GoModuleInventory, corpus GoCandidateCorpus, maxGroupFiles int) error {
	expected, err := QueryGoCandidateReviewTopology(baseGraph, baseInventory, corpus, maxGroupFiles)
	if err != nil || !reflect.DeepEqual(expected, result) {
		return errors.New("candidate review topology does not reproduce from bound corpus")
	}
	return nil
}

func (result GoCandidateReviewTopology) content() GoCandidateReviewTopology {
	result.Digest = ""
	return result
}

func validateGoCandidateReviewCorpus(baseGraph GoEngineeringGraph, baseInventory GoModuleInventory, corpus GoCandidateCorpus) error {
	if err := ValidateGoEngineeringGraph(baseGraph); err != nil {
		return errors.New("candidate review topology base graph is invalid")
	}
	if err := ValidateGoEngineeringGraph(corpus.Graph); err != nil {
		return errors.New("candidate review topology candidate graph is invalid")
	}
	if err := ValidateGoModuleInventoryRecord(baseInventory); err != nil || baseInventory.Version != GoModuleInventoryVersionCommitted {
		return errors.New("candidate review topology base module inventory is invalid")
	}
	if err := ValidateGoModuleInventoryRecord(corpus.ModuleInventory); err != nil || corpus.ModuleInventory.Version != GoModuleInventoryVersionCandidate {
		return errors.New("candidate review topology candidate module inventory is invalid")
	}
	if corpus.Version != goCandidateCorpusVersion || corpus.Coverage != "PARTIAL" || safepath.RequireDigest(corpus.CandidateID) != nil || safepath.RequireDigest(corpus.CandidateFilesHash) != nil || safepath.RequireDigest(corpus.ProducerSHA256) != nil || safepath.RequireDigest(corpus.BaseGraphDigest) != nil || safepath.RequireDigest(corpus.BaseModuleInventoryDigest) != nil || corpus.Source.RepositoryID == "" || corpus.Source.Commit == "" || corpus.Source.Tree == "" || corpus.Source.RepositoryID != baseGraph.SourceID || corpus.Source.RepositoryID != baseInventory.RepositoryID || corpus.Source.Commit != baseInventory.Commit || corpus.Source.Tree != baseInventory.Tree || corpus.BaseGraphDigest != baseGraph.Digest || corpus.BaseModuleInventoryDigest != baseInventory.Digest || baseGraph.ModuleInventory == nil || baseGraph.ModuleInventory.Digest != baseInventory.Digest || corpus.Graph.SourceID != corpus.Source.RepositoryID || corpus.Graph.CandidateID != corpus.CandidateID || corpus.Graph.ProducerSHA256 != corpus.ProducerSHA256 || corpus.ProducerSHA256 != baseGraph.ProducerSHA256 || corpus.ModuleInventory.RepositoryID != corpus.Source.RepositoryID || corpus.ModuleInventory.Commit != corpus.Source.Commit || corpus.ModuleInventory.Tree != corpus.Source.Tree || corpus.ModuleInventory.CandidateID != corpus.CandidateID || corpus.ModuleInventory.CandidateFilesHash != corpus.CandidateFilesHash || corpus.ModuleInventory.BaseInventoryDigest != baseInventory.Digest || corpus.Graph.ModuleInventory == nil || !reflect.DeepEqual(*corpus.Graph.ModuleInventory, corpus.ModuleInventory) || baseGraph.CandidateID != "" || !reflect.DeepEqual(*baseGraph.ModuleInventory, baseInventory) {
		return errors.New("candidate review topology corpus binding mismatch")
	}
	if corpus.OmittedCount < len(corpus.Omissions) || corpus.OmittedCount < 0 || corpus.OmittedCount > 1<<20 || corpus.OmissionsTrimmed != (corpus.OmittedCount > len(corpus.Omissions)) || len(corpus.Omissions) > 64 {
		return errors.New("candidate review topology omission counts are invalid")
	}
	for _, omission := range corpus.Omissions {
		if omission.Path == "[redacted]" {
			if omission.Reason != "unsafe_or_sensitive_path" {
				return errors.New("candidate review topology omission is invalid")
			}
		} else if safepath.Relative(omission.Path) != nil || !taskcontext.EligiblePath(omission.Path) || omission.Reason == "unsafe_or_sensitive_path" || !candidateReviewOmissionReason(omission.Reason) {
			return errors.New("candidate review topology omission is invalid")
		}
	}
	basePaths := make(map[string]GoGraphFile, len(baseGraph.Files))
	for _, file := range baseGraph.Files {
		if !taskcontext.EligiblePath(file.Facts.Path) {
			return errors.New("candidate review topology base graph contains a protected path")
		}
		basePaths[file.Facts.Path] = file
	}
	candidatePaths := make(map[string]GoGraphFile, len(corpus.Graph.Files))
	for _, file := range corpus.Graph.Files {
		if !taskcontext.EligiblePath(file.Facts.Path) {
			return errors.New("candidate review topology candidate graph contains a protected path")
		}
		candidatePaths[file.Facts.Path] = file
	}
	if !validCandidateReviewPathList(corpus.ChangedPaths) || !validCandidateReviewPathList(corpus.AdmittedPaths) || !validCandidateReviewPathList(corpus.DeletedPaths) {
		return errors.New("candidate review topology path lists are invalid")
	}
	changedSet, admittedSet, deletedSet := make(map[string]bool), make(map[string]bool), make(map[string]bool)
	for _, path := range corpus.ChangedPaths {
		if _, exists := basePaths[path]; !exists {
			return errors.New("candidate review changed path is absent from base graph")
		}
		changedSet[path] = true
	}
	for _, path := range corpus.AdmittedPaths {
		if _, exists := basePaths[path]; exists {
			return errors.New("candidate review admitted path already exists in base graph")
		}
		if _, exists := candidatePaths[path]; !exists {
			return errors.New("candidate review admitted path is absent from candidate graph")
		}
		admittedSet[path] = true
	}
	for _, path := range corpus.DeletedPaths {
		if _, exists := basePaths[path]; !exists {
			return errors.New("candidate review deleted path is absent from base graph")
		}
		if _, exists := candidatePaths[path]; exists {
			return errors.New("candidate review deleted path remains in candidate graph")
		}
		deletedSet[path] = true
	}
	for path, base := range basePaths {
		candidate, exists := candidatePaths[path]
		switch {
		case exists && base.Facts.SourceSHA256 != candidate.Facts.SourceSHA256:
			if !changedSet[path] {
				return errors.New("candidate review corpus omitted a changed base path")
			}
		case exists:
			if changedSet[path] || deletedSet[path] {
				return errors.New("candidate review corpus change classification disagrees with graph")
			}
		case deletedSet[path]:
		case changedSet[path] && candidateReviewHasOmission(corpus.Omissions, path):
		default:
			return errors.New("candidate review graph dropped an unaccounted base path")
		}
	}
	for path := range candidatePaths {
		if _, exists := basePaths[path]; !exists && !admittedSet[path] {
			return errors.New("candidate review corpus omitted an admitted path")
		}
	}
	for path := range changedSet {
		if admittedSet[path] || deletedSet[path] {
			return errors.New("candidate review path has conflicting classifications")
		}
	}
	for path := range admittedSet {
		if deletedSet[path] {
			return errors.New("candidate review path has conflicting classifications")
		}
	}
	changedEndpoints := make(map[string]bool, len(changedSet)+len(admittedSet)+len(deletedSet))
	for path := range changedSet {
		changedEndpoints[path] = true
	}
	for path := range admittedSet {
		changedEndpoints[path] = true
	}
	for path := range deletedSet {
		changedEndpoints[path] = true
	}
	expectedGenerators := make([]GoGeneratorBinding, 0, len(baseGraph.Generators))
	for _, generator := range baseGraph.Generators {
		if !changedEndpoints[generator.GeneratorPath] && !changedEndpoints[generator.GeneratedPath] {
			expectedGenerators = append(expectedGenerators, generator)
		}
	}
	if !reflect.DeepEqual(expectedGenerators, corpus.Graph.Generators) {
		return errors.New("candidate review generator relations disagree with changed endpoints")
	}
	return nil
}

func candidateReviewBaseGeneratorHints(baseGraph GoEngineeringGraph, corpus GoCandidateCorpus) []GoCandidateReviewGeneratorHint {
	touched := make(map[string]bool, len(corpus.ChangedPaths)+len(corpus.AdmittedPaths)+len(corpus.DeletedPaths))
	for _, paths := range [][]string{corpus.ChangedPaths, corpus.AdmittedPaths, corpus.DeletedPaths} {
		for _, path := range paths {
			touched[path] = true
		}
	}
	out := make([]GoCandidateReviewGeneratorHint, 0)
	for _, generator := range baseGraph.Generators {
		if touched[generator.GeneratorPath] || touched[generator.GeneratedPath] {
			out = append(out, GoCandidateReviewGeneratorHint{
				GeneratorPath: generator.GeneratorPath,
				GeneratedPath: generator.GeneratedPath,
				Status:        "base_relation_candidate_not_revalidated",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].GeneratorPath != out[j].GeneratorPath {
			return out[i].GeneratorPath < out[j].GeneratorPath
		}
		return out[i].GeneratedPath < out[j].GeneratedPath
	})
	return out
}

func candidateReviewPresentPaths(paths []string, graphPaths map[string]bool) ([]string, int) {
	present := make([]string, 0, len(paths))
	missing := 0
	for _, path := range paths {
		if graphPaths[path] {
			present = append(present, path)
		} else {
			missing++
		}
	}
	return present, missing
}

func candidateReviewBoundedPaths(paths []string, maxPaths int) []string {
	if len(paths) > maxPaths {
		paths = paths[:maxPaths]
	}
	return append([]string{}, paths...)
}

func uniqueSortedCandidatePaths(paths []string) []string {
	sort.Strings(paths)
	out := paths[:0]
	for _, path := range paths {
		if len(out) == 0 || out[len(out)-1] != path {
			out = append(out, path)
		}
	}
	return out
}

func candidateReviewObservedPackages(graph GoEngineeringGraph, modules []GoTopologyModule, selected map[string]bool) []GoFocusTopologyModule {
	selectedPackages := make(map[string]bool)
	for _, file := range graph.Files {
		if selected[file.Facts.Path] {
			selectedPackages[graphPackageBindingNodeID(file.Package)] = true
		}
	}
	out := make([]GoFocusTopologyModule, 0, len(selectedPackages))
	for _, module := range modules {
		if selectedPackages[module.ID] {
			out = append(out, GoFocusTopologyModule{Label: module.Label, ObservedImporters: module.ObservedImporters, ObservedDependencies: module.ObservedDependencies, SharedHub: module.SharedHub})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func candidateReviewCallHints(graph GoEngineeringGraph, selected map[string]bool) []GoCandidateReviewCallHint {
	out := make([]GoCandidateReviewCallHint, 0)
	for _, edge := range graph.Edges {
		if edge.Relation != "CALLS_UNRESOLVED" || !selected[edge.Path] || edge.Resolution != "UNRESOLVED" || edge.Range == nil {
			continue
		}
		out = append(out, GoCandidateReviewCallHint{Path: edge.Path, Spelling: nodeLabel(graph.Nodes, edge.To), StartByte: edge.Range.StartByte, EndByte: edge.Range.EndByte, Resolution: "UNRESOLVED"})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		if out[i].StartByte != out[j].StartByte {
			return out[i].StartByte < out[j].StartByte
		}
		if out[i].EndByte != out[j].EndByte {
			return out[i].EndByte < out[j].EndByte
		}
		return out[i].Spelling < out[j].Spelling
	})
	return out
}

func validCandidateReviewPathList(paths []string) bool {
	for index, path := range paths {
		if safepath.Relative(path) != nil || !taskcontext.EligiblePath(path) || filepath.Ext(path) != ".go" || index > 0 && paths[index-1] >= path {
			return false
		}
	}
	return true
}

func candidateReviewOmissionReason(reason string) bool {
	switch reason {
	case "unsafe_or_sensitive_path", "file_budget", "candidate_read_failed", "file_byte_limit", "invalid_package_clause":
		return true
	default:
		return false
	}
}

func candidateReviewHasOmission(omissions []taskcontext.Omission, path string) bool {
	for _, omission := range omissions {
		if omission.Path == path || omission.Path == "[redacted]" {
			return true
		}
	}
	return false
}

func trimGoCandidateReviewTopology(result *GoCandidateReviewTopology) error {
	for {
		encoded, err := canonical.Bytes(result)
		if err != nil {
			return err
		}
		if len(encoded)+64 <= goCandidateReviewTopologyMaxBytes {
			return nil
		}
		result.Truncated = true
		switch {
		case len(result.PotentialCallHints) > 0:
			result.PotentialCallHints = result.PotentialCallHints[:len(result.PotentialCallHints)-1]
			result.OmittedCallHints++
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
		case len(result.Omissions) > 0:
			result.Omissions = result.Omissions[:len(result.Omissions)-1]
			result.OmittedCorpusOmissions++
			result.OmissionsTrimmed = true
		case len(result.DeletedPaths) > 0:
			result.DeletedPaths = result.DeletedPaths[:len(result.DeletedPaths)-1]
			result.UnlistedDeletedPathCount++
		case len(result.AdmittedPaths) > 0:
			result.AdmittedPaths = result.AdmittedPaths[:len(result.AdmittedPaths)-1]
			result.UnlistedAdmittedPathCount++
		case len(result.ChangedPaths) > 0:
			result.ChangedPaths = result.ChangedPaths[:len(result.ChangedPaths)-1]
			result.UnlistedChangedPathCount++
		default:
			return errors.New("candidate review topology bindings exceed bounded output size")
		}
	}
}
