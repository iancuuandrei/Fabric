package control

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// observedCouplingReason names the two trustworthy derivation classes. C1/C3
// are never derived; import/call relations stay deferred because UNRESOLVED
// syntax facts are not direct API proof.
const (
	observedReasonGenerationFamily = "same_generation_family"
	observedReasonSamePackage      = "same_observed_package"
)

// deriveObservedCouplings is the pure controller derivation for CURRENT ready
// implementations over the replay-validated PlannerGoContext. It performs no
// filesystem I/O and appends no journal event: callers recompute it entirely
// from the snapshot on preparation and replay.
//
// Contract:
//   - PlannerGoContext absent or Unavailable degrades to planner advisory
//     selection: returns haveRI=false with no error and no absence claim.
//   - Invalid or forged source/producer/graph/digest bindings fail closed
//     with an error, never silent fallback.
//   - WritePaths expand over admitted graph files only (exact path or
//     directory prefix with slash boundary); ScopePaths never confer
//     ownership. New, omitted or changed paths yield no observed relation;
//     PARTIAL coverage is preserved and C0 is never emitted.
//   - Every source path changed by the recorded staged parent delta is
//     excluded, including generator/config antecedents not owned by future
//     tasks. A parent-delta change to an admitted module manifest, or an
//     added/changed nested go.mod/go.work, additionally makes every
//     declared-module package observation derived from that old inventory
//     ineligible even when the endpoint Go bytes are unchanged; source-local
//     membership stays eligible only when its own file bytes are proven
//     unchanged. A lineage-incompatible base candidate fails closed rather
//     than using detached facts.
//   - C4 covers the same explicit generation family (generator->output and
//     two outputs of one generator) only when the generator and both endpoint
//     facts are unchanged with exact SourceSHA256 matches. C2 covers the same
//     observed package from exact package identity/import-path bindings, never
//     directory guesses and never C3.
func deriveObservedCouplings(s Snapshot, ready []engineeringplan.Task) ([]engineeringplan.TaskCoupling, bool, error) {
	rec := s.PlannerGoContext
	if rec == nil || rec.Unavailable != "" {
		return nil, false, nil
	}
	if len(ready) == 0 || len(ready) > 8 {
		return nil, false, errors.New("observed derivation requires one to eight ready implementations")
	}
	for _, task := range ready {
		if task.Kind != engineeringplan.Implementation {
			return nil, false, errors.New("observed derivation requires implementation tasks")
		}
	}
	if s.Creation.Execution == nil {
		return nil, false, errors.New("observed derivation requires an execution policy")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return nil, false, err
	}
	if rec.Source.RepositoryID != sourceID || rec.Source.Commit != s.Creation.Repository.Commit || rec.Source.Tree != s.Creation.Repository.Tree || rec.Source.ObjectFormat != s.Creation.Repository.ObjectFormat {
		return nil, false, errors.New("observed source binding mismatch")
	}
	if safepath.RequireDigest(rec.RIExecutableSHA256) != nil || rec.RIExecutableSHA256 != s.Creation.Execution.PlannerContextRIExecutableSHA256 {
		return nil, false, errors.New("observed producer binding mismatch")
	}
	if rec.Graph == nil {
		return nil, false, errors.New("observed graph is missing")
	}
	if err := ri.ValidateGoEngineeringGraph(*rec.Graph); err != nil {
		return nil, false, err
	}
	if rec.Graph.SourceID != sourceID || rec.Graph.ProducerSHA256 != rec.RIExecutableSHA256 || rec.Graph.CandidateID != "" || rec.Graph.Coverage != "PARTIAL" {
		return nil, false, errors.New("observed graph binding mismatch")
	}
	if err := validateObservedSourceCorpus(*rec, sourceID); err != nil {
		return nil, false, err
	}
	if err := validateObservedGenerationBindings(*rec, sourceID); err != nil {
		return nil, false, err
	}
	id, err := plannerGoContextRecordID(*rec)
	if err != nil || id != rec.RecordID || safepath.RequireDigest(rec.RecordID) != nil {
		return nil, false, errors.New("observed record substitution")
	}
	// Base candidate lineage: when prior staged cohorts exist, the current
	// parent candidate must chain to the archived candidate. Otherwise the
	// admitted facts are detached from this parent and must not be used.
	if len(s.GraphStagedCohorts) > 0 {
		if s.Candidate == nil {
			return nil, false, errors.New("observed derivation requires a parent candidate")
		}
		currentID, err := s.Candidate.ID()
		if err != nil {
			return nil, false, err
		}
		last := s.GraphStagedCohorts[len(s.GraphStagedCohorts)-1]
		if currentID != last.CandidateID {
			return nil, false, errors.New("observed base candidate lineage mismatch")
		}
	}
	delta, err := stagedParentDelta(s)
	if err != nil {
		return nil, false, err
	}
	changed := make(map[string]bool, len(delta))
	for _, change := range delta {
		changed[change.Path] = true
	}
	// Declared-module package identity depends on ManifestPath/InventoryDigest
	// (ri.DeclaredGoPackageBinding), so module ownership derived from the old
	// inventory cannot survive a manifest change. The controller never
	// synthesizes an overlay or reparses ownership; it conservatively treats
	// every declared-module observation as stale instead. Generator (C4)
	// relations are textual and stay eligible when their own generator and
	// endpoint antecedents are unchanged.
	manifestStale := observedModuleInventoryStale(rec.Graph.ModuleInventory, delta)
	digestByPath := make(map[string]string, len(rec.Sources))
	for _, source := range rec.Sources {
		digestByPath[source.Path] = source.SHA256
	}
	fileByPath := make(map[string]ri.GoGraphFile, len(rec.Graph.Files))
	for _, file := range rec.Graph.Files {
		fileByPath[file.Facts.Path] = file
	}
	// Exact bounded WritePaths expansion over admitted graph files. A file is
	// valid observed evidence only when its fact SHA exactly matches the
	// admitted source digest and its path was not changed by the recorded
	// parent delta. ScopePaths are never consulted here.
	validByTask := make(map[string]map[string]ri.GoGraphFile, len(ready))
	for _, task := range ready {
		valid := map[string]ri.GoGraphFile{}
		for _, write := range task.WritePaths {
			if write == "" {
				continue
			}
			for _, file := range rec.Graph.Files {
				path := file.Facts.Path
				if path != write && !strings.HasPrefix(path, write+"/") {
					continue
				}
				if changed[path] {
					continue
				}
				want, ok := digestByPath[path]
				if !ok || want == "" || want != file.Facts.SourceSHA256 {
					continue
				}
				valid[path] = file
			}
		}
		validByTask[task.ID] = valid
	}
	pairs := map[string]engineeringplan.TaskCoupling{}
	emit := func(a, b string, level, reason, provenance, evidence string) {
		if a == b {
			return
		}
		from, to := a, b
		if from > to {
			from, to = to, from
		}
		key := from + "\x00" + to
		if existing, ok := pairs[key]; ok {
			if severityOf(level) < severityOf(existing.Level) {
				return
			}
			if severityOf(level) == severityOf(existing.Level) {
				// Deterministic lexicographic tie-break over
				// reason/provenance/evidence so replays never depend on
				// traversal or map order when families share a pair.
				if reason > existing.Reason {
					return
				}
				if reason == existing.Reason {
					if provenance > existing.Provenance {
						return
					}
					if provenance == existing.Provenance && evidence >= existing.Evidence {
						return
					}
				}
			}
		}
		pairs[key] = engineeringplan.TaskCoupling{From: from, To: to, Level: level, Reason: reason, Provenance: provenance, Evidence: evidence}
	}
	// Sorted generator traversal so derivation is independent of admission or
	// input order; ties keep the lexicographic winner via emit.
	sortedGenerators := append([]ri.GoGeneratorBinding(nil), rec.Graph.Generators...)
	sort.Slice(sortedGenerators, func(i, j int) bool {
		if sortedGenerators[i].GeneratorPath != sortedGenerators[j].GeneratorPath {
			return sortedGenerators[i].GeneratorPath < sortedGenerators[j].GeneratorPath
		}
		if sortedGenerators[i].GeneratedPath != sortedGenerators[j].GeneratedPath {
			return sortedGenerators[i].GeneratedPath < sortedGenerators[j].GeneratedPath
		}
		return sortedGenerators[i].Directive < sortedGenerators[j].Directive
	})
	// C4: same explicit generation family. Each binding is eligible only when
	// the generator and the generated endpoint facts are unchanged with exact
	// source digests.
	for _, binding := range sortedGenerators {
		if changed[binding.GeneratorPath] || changed[binding.GeneratedPath] {
			continue
		}
		genFile, genOK := fileByPath[binding.GeneratorPath]
		outFile, outOK := fileByPath[binding.GeneratedPath]
		if !genOK || !outOK {
			continue
		}
		if digestByPath[binding.GeneratorPath] != genFile.Facts.SourceSHA256 || digestByPath[binding.GeneratedPath] != outFile.Facts.SourceSHA256 {
			continue
		}
		genOwners := ownersOf(validByTask, binding.GeneratorPath)
		outOwners := ownersOf(validByTask, binding.GeneratedPath)
		for _, a := range genOwners {
			for _, b := range outOwners {
				if a == b {
					continue
				}
				evidence := observedEvidence("generator", binding.GeneratorPath, binding.GeneratedPath, rec.Graph.Digest)
				emit(a, b, engineeringplan.CouplingC4, observedReasonGenerationFamily, engineeringplan.CouplingProvenanceGeneratorObserved, evidence)
			}
		}
	}
	// C4: two outputs of one generator. Group generated paths by generator and
	// relate distinct owners of different outputs in the same family.
	outputsByGenerator := map[string][]string{}
	for _, binding := range sortedGenerators {
		if changed[binding.GeneratorPath] || changed[binding.GeneratedPath] {
			continue
		}
		genFile, genOK := fileByPath[binding.GeneratorPath]
		outFile, outOK := fileByPath[binding.GeneratedPath]
		if !genOK || !outOK {
			continue
		}
		if digestByPath[binding.GeneratorPath] != genFile.Facts.SourceSHA256 || digestByPath[binding.GeneratedPath] != outFile.Facts.SourceSHA256 {
			continue
		}
		outputsByGenerator[binding.GeneratorPath] = append(outputsByGenerator[binding.GeneratorPath], binding.GeneratedPath)
	}
	generators := make([]string, 0, len(outputsByGenerator))
	for generator := range outputsByGenerator {
		generators = append(generators, generator)
	}
	sort.Strings(generators)
	for _, generator := range generators {
		outputs := append([]string(nil), outputsByGenerator[generator]...)
		sort.Strings(outputs)
		if len(outputs) < 2 {
			continue
		}
		for i := range outputs {
			for j := i + 1; j < len(outputs); j++ {
				ownersA := ownersOf(validByTask, outputs[i])
				ownersB := ownersOf(validByTask, outputs[j])
				for _, a := range ownersA {
					for _, b := range ownersB {
						if a == b {
							continue
						}
						// Canonical-sort the two output witnesses so the
						// family evidence path never depends on input order.
						lo, hi := outputs[i], outputs[j]
						if hi < lo {
							lo, hi = hi, lo
						}
						evidence := observedEvidence("generator", generator, lo+"|"+hi, rec.Graph.Digest)
						emit(a, b, engineeringplan.CouplingC4, observedReasonGenerationFamily, engineeringplan.CouplingProvenanceGeneratorObserved, evidence)
					}
				}
			}
		}
	}
	// C2: same observed package from exact bindings only. Never C3 from same
	// package; import/call edges stay deferred.
	ids := make([]string, 0, len(ready))
	for _, task := range ready {
		ids = append(ids, task.ID)
	}
	sort.Strings(ids)
	for i := range ids {
		for _, other := range ids[i+1:] {
			filesA := validByTask[ids[i]]
			filesB := validByTask[other]
			if len(filesA) == 0 || len(filesB) == 0 {
				continue
			}
			matched, evidencePath := sameObservedPackagePair(filesA, filesB, manifestStale)
			if !matched {
				continue
			}
			evidence := observedEvidence("package", evidencePath, ids[i]+"|"+other, rec.Graph.Digest)
			emit(ids[i], other, engineeringplan.CouplingC2, observedReasonSamePackage, engineeringplan.CouplingProvenanceTopologyObserved, evidence)
		}
	}
	out := make([]engineeringplan.TaskCoupling, 0, len(pairs))
	for _, c := range pairs {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	if len(out) > engineeringplan.MaxTaskCouplings {
		return nil, false, errors.New("observed coupling set exceeds 28 relationships")
	}
	if err := engineeringplan.ValidateObservedTaskCouplings(out); err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// validateObservedSourceCorpus replays the exact source/digest binding between
// admitted sources and graph facts without filesystem access.
func validateObservedSourceCorpus(rec PlannerGoContextRecord, sourceID string) error {
	if rec.Graph == nil || len(rec.Graph.Files) != len(rec.Sources) {
		return errors.New("observed source corpus mismatch")
	}
	bound := make(map[string]string, len(rec.Sources))
	for _, source := range rec.Sources {
		if safepath.Relative(source.Path) != nil || source.RepositoryID != sourceID || source.Commit != rec.Source.Commit || safepath.RequireDigest(source.SHA256) != nil {
			return errors.New("observed source binding mismatch")
		}
		bound[source.Path] = source.SHA256
	}
	for _, file := range rec.Graph.Files {
		want, ok := bound[file.Facts.Path]
		if !ok || want != file.Facts.SourceSHA256 {
			return errors.New("observed graph source mismatch")
		}
	}
	return nil
}

// validateObservedGenerationBindings requires the admitted graph generator
// set to equal the metadata-derived bindings over the admitted corpus. A
// graph carrying generators without provable metadata fails closed.
func validateObservedGenerationBindings(rec PlannerGoContextRecord, sourceID string) error {
	if rec.Graph == nil {
		return errors.New("observed graph is missing")
	}
	if rec.GenerationMetadata == nil {
		if len(rec.Graph.Generators) != 0 {
			return errors.New("observed generator binding lacks metadata proof")
		}
		return nil
	}
	if rec.GenerationMetadata.SourceID != sourceID || ri.ValidateGoGenerationMetadata(*rec.GenerationMetadata) != nil {
		return errors.New("observed generation metadata mismatch")
	}
	if !reflect.DeepEqual(rec.GenerationMetadata.SeedPaths, sourcePaths(rec.Sources)) {
		return errors.New("observed generation seed substitution")
	}
	if !reflect.DeepEqual(rec.Graph.Generators, admittedGoGenerationBindings(*rec.GenerationMetadata, rec.Sources)) {
		return errors.New("observed generation graph substitution")
	}
	return nil
}

// ownersOf returns sorted task IDs whose valid observed expansion contains path.
func ownersOf(validByTask map[string]map[string]ri.GoGraphFile, path string) []string {
	var owners []string
	for taskID, files := range validByTask {
		if _, ok := files[path]; ok {
			owners = append(owners, taskID)
		}
	}
	sort.Strings(owners)
	return owners
}

// sameObservedPackagePair reports whether any valid file pair across two
// tasks shares the exact observed package binding, returning the
// lexicographically smallest witness path for bounded evidence. Sorted
// traversal keeps the witness independent of map order when several files
// share one package. Directory proximity is never consulted. When the
// admitted module inventory is stale, declared-module bindings are skipped
// without fallback: source-local membership stays eligible only through the
// caller's unchanged-byte checks.
func sameObservedPackagePair(a, b map[string]ri.GoGraphFile, manifestStale bool) (bool, string) {
	pathsA := make([]string, 0, len(a))
	for path := range a {
		pathsA = append(pathsA, path)
	}
	sort.Strings(pathsA)
	pathsB := make([]string, 0, len(b))
	for path := range b {
		pathsB = append(pathsB, path)
	}
	sort.Strings(pathsB)
	best := ""
	for _, pathA := range pathsA {
		if !observedPackageBindingEligible(a[pathA].Package, manifestStale) {
			continue
		}
		for _, pathB := range pathsB {
			if !observedPackageBindingEligible(b[pathB].Package, manifestStale) {
				continue
			}
			if sameObservedPackage(a[pathA].Package, b[pathB].Package) {
				witness := pathA
				if pathB < witness {
					witness = pathB
				}
				if best == "" || witness < best {
					best = witness
				}
			}
		}
	}
	if best == "" {
		return false, ""
	}
	return true, best
}

// sameObservedPackage compares exact existing bindings only: matching
// non-empty package identities, or matching import/module/inventory bindings.
// Empty or mixed binding modes never match.
func sameObservedPackage(a, b ri.GoPackageBinding) bool {
	if a.PackageIdentity != "" && b.PackageIdentity != "" {
		return a.PackageIdentity == b.PackageIdentity
	}
	if a.ImportPath != "" && b.ImportPath != "" {
		if a.ImportPath != b.ImportPath || a.ModulePath != b.ModulePath {
			return false
		}
		return a.InventoryDigest == b.InventoryDigest
	}
	return false
}

// observedPackageBindingEligible keeps source-local membership usable after a
// manifest change only when the caller's own unchanged-byte checks pass;
// declared-module bindings (ManifestPath/InventoryDigest) go stale with the
// inventory and are never reused detached.
func observedPackageBindingEligible(binding ri.GoPackageBinding, manifestStale bool) bool {
	if manifestStale && binding.IdentityKind == "declared_module_v1" {
		return false
	}
	return true
}

// observedModuleInventoryStale reports whether the recorded parent delta may
// have changed module ownership derived from the admitted inventory: a change
// to any admitted manifest file, or any added/changed manifest-kind path
// (nested go.mod/go.work or vendor modules) that the old inventory cannot
// describe. It uses only existing manifest binding fields, grants no
// authority, and never substitutes for inventory ownership.
func observedModuleInventoryStale(inventory *ri.GoModuleInventory, delta []fileeffects.Change) bool {
	if inventory == nil {
		return false
	}
	admitted := make(map[string]bool, len(inventory.Files))
	for _, file := range inventory.Files {
		admitted[file.Path] = true
	}
	for _, change := range delta {
		if admitted[change.Path] {
			return true
		}
		if isManifestKindPath(change.Path) {
			return true
		}
	}
	return false
}

// isManifestKindPath mirrors the RI manifest classification (go.mod, go.work,
// vendor modules.txt) for staleness only. It never confers ownership.
func isManifestKindPath(p string) bool {
	portable := strings.ReplaceAll(p, "\\", "/")
	base := portable
	if i := strings.LastIndex(portable, "/"); i >= 0 {
		base = portable[i+1:]
	}
	if base == "go.mod" || base == "go.work" {
		return true
	}
	return strings.HasSuffix(portable, "/vendor/modules.txt") || portable == "vendor/modules.txt"
}

// severityOf orders coupling levels for MAX-severity merges.
func severityOf(level string) int {
	switch level {
	case engineeringplan.CouplingC1:
		return 1
	case engineeringplan.CouplingC2:
		return 2
	case engineeringplan.CouplingC3:
		return 3
	case engineeringplan.CouplingC4:
		return 4
	default:
		return 0
	}
}

// observedEvidence renders a bounded human-auditable pointer with digest
// references and a truncated safe path witness. Output stays within the 256
// byte evidence bound, valid UTF-8 without control characters.
func observedEvidence(kind, witness, pair, graphDigest string) string {
	short := graphDigest
	if len(short) > 12 {
		short = short[:12]
	}
	evidence := fmt.Sprintf("observed %s %q pair %s graph %s PARTIAL", kind, witness, pair, short)
	if len(evidence) > 256 {
		// Truncate the witness first to preserve the digest reference.
		over := len(evidence) - 256
		if over < len(witness) {
			witness = witness[:len(witness)-over]
		} else {
			witness = ""
		}
		evidence = fmt.Sprintf("observed %s %q pair %s graph %s PARTIAL", kind, witness, pair, short)
		if len(evidence) > 256 {
			evidence = evidence[:256]
		}
	}
	return evidence
}
