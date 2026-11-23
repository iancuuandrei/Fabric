package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

type goCandidateQueryEnvelope struct {
	Version                          int                     `json:"version"`
	Coverage                         string                  `json:"coverage"`
	RunID                            string                  `json:"run_id"`
	Repository                       ri.Source               `json:"repository"`
	CandidateID                      string                  `json:"candidate_id"`
	CandidateHead                    string                  `json:"candidate_head"`
	CandidateFilesHash               string                  `json:"candidate_files_hash"`
	CandidateFileCount               int                     `json:"candidate_file_count"`
	BaseGraphDigest                  string                  `json:"base_graph_digest"`
	CandidateGraphDigest             string                  `json:"candidate_graph_digest"`
	BaseModuleInventoryDigest        string                  `json:"base_module_inventory_digest"`
	CandidateModuleInventoryDigest   string                  `json:"candidate_module_inventory_digest"`
	CandidateModuleInventoryCoverage string                  `json:"candidate_module_inventory_coverage"`
	CandidateManifestObservedCount   int                     `json:"candidate_manifest_observed_count"`
	CandidateManifestOmittedCount    int                     `json:"candidate_manifest_omitted_count"`
	CandidateManifestOmissions       []ri.GoManifestOmission `json:"candidate_manifest_omissions"`
	ChangedPathCount                 int                     `json:"changed_path_count"`
	AdmittedPathCount                int                     `json:"admitted_path_count"`
	DeletedPathCount                 int                     `json:"deleted_path_count"`
	OmittedCount                     int                     `json:"omitted_count"`
	OmissionsTrimmed                 bool                    `json:"omissions_trimmed"`
	Omissions                        []taskcontext.Omission  `json:"omissions"`
	Result                           ri.SemanticResult       `json:"result"`
}

func riGoCandidateQueryCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 6 && len(args) != 7 {
		return errors.New("usage: ri candidate-query RUN EXE EXE_SHA256 BASE_SPEC_JSON QUERY_JSON [CACHE_DIR]")
	}
	if len(args) == 7 && args[6] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	runID := args[1]
	queryPath, err := riAbsolutePath(root, args[5])
	if err != nil {
		return err
	}
	query, err := readGoSemanticQuery(queryPath)
	if err != nil {
		return err
	}
	if err := validateGoSemanticQuery(query); err != nil {
		return err
	}
	baseSpecPath, err := riAbsolutePath(root, args[4])
	if err != nil {
		return err
	}
	baseSpec, err := readGoGraphSpec(baseSpecPath)
	if err != nil {
		return err
	}
	if err := validateGoGraphSpec(baseSpec); err != nil {
		return err
	}
	if baseSpec.ModuleInventory == nil || baseSpec.ModuleInventory.Version != ri.GoModuleInventoryVersionCommitted {
		return errors.New("candidate query requires a committed v1 module inventory in the base graph spec")
	}
	journalPath, err := runPath(root, runID)
	if err != nil {
		return err
	}
	snapshot, err := control.Inspect(journalPath)
	if err != nil {
		return err
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	repositoryID, err := identity.ID()
	if err != nil {
		return err
	}
	candidateID, err := validateCandidateQuerySnapshot(snapshot, runID, identity)
	if err != nil {
		return err
	}
	built, err := buildGoGraph(ctx, root, args[2], args[3], args[4], cacheArgument(args, 6))
	if err != nil {
		return err
	}
	secondSpec, err := readGoGraphSpec(baseSpecPath)
	if err != nil || !reflect.DeepEqual(baseSpec, secondSpec) {
		if err != nil {
			return err
		}
		return errors.New("base graph spec changed while candidate query was being prepared")
	}
	if built.result.Repository.RepositoryID != repositoryID || built.result.Graph.ModuleInventory == nil || built.result.Graph.ModuleInventory.Digest != baseSpec.ModuleInventory.Digest || !candidateQueryBaseMatchesSpec(built.result.Graph, baseSpec) {
		return errors.New("base graph is not bound to the run source and committed module inventory")
	}
	executable, err := riAbsolutePath(root, args[2])
	if err != nil {
		return err
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[3]}
	corpus, err := ri.CollectCandidateGoCorpus(ctx, identity, *snapshot.Workspace, *snapshot.Candidate, built.result.Graph, *baseSpec.ModuleInventory, client, "")
	if err != nil {
		return err
	}
	if corpus.Source.RepositoryID != built.result.Repository.RepositoryID || corpus.CandidateID != candidateID || corpus.CandidateFilesHash != snapshot.Candidate.FilesHash || corpus.BaseGraphDigest != built.result.Graph.Digest || corpus.BaseModuleInventoryDigest != baseSpec.ModuleInventory.Digest || corpus.Graph.CandidateID != candidateID {
		return errors.New("candidate corpus binding differs from the admitted run snapshot")
	}
	if err := ri.ValidateCandidateGoModuleInventory(corpus.ModuleInventory, identity, *snapshot.Candidate, *baseSpec.ModuleInventory); err != nil {
		return fmt.Errorf("candidate module inventory binding is invalid: %w", err)
	}
	if err := ri.ValidateGoEngineeringGraph(corpus.Graph); err != nil {
		return fmt.Errorf("candidate graph validation failed: %w", err)
	}
	lease, err := worktree.AcquireRead(snapshot.Workspace.Request)
	if err != nil {
		return err
	}
	var result ri.SemanticResult
	checkErr := lease.WithOwnership(snapshot.Workspace.Request, func(worktree.LeaseIdentity) error {
		latest, err := control.Inspect(journalPath)
		if err != nil {
			return err
		}
		if latest.RunID != snapshot.RunID || latest.Workspace == nil || latest.Candidate == nil || latest.Workspace.Request != snapshot.Workspace.Request || *latest.Workspace != *snapshot.Workspace || *latest.Candidate != *snapshot.Candidate || latest.WorkspaceOutcome != "CONFIRMED" || latest.FileOutcome == "UNKNOWN" {
			return errors.New("run candidate changed during candidate query")
		}
		current, _, err := worktree.Capture(ctx, *latest.Workspace)
		if err != nil {
			return err
		}
		if current != *latest.Candidate {
			return errors.New("workspace candidate changed during candidate query")
		}
		currentIdentity, err := repository.Discover(ctx, root, cfg.Repository)
		if err != nil {
			return err
		}
		if currentIdentity != identity {
			return errors.New("configured repository source changed during candidate query")
		}
		result, err = ri.QueryGoSemantic(corpus.Graph, query)
		if err != nil {
			return err
		}
		if result.Coverage != "PARTIAL" || result.CandidateID != candidateID || result.SourceID != built.result.Repository.RepositoryID {
			return errors.New("candidate semantic result binding is invalid")
		}
		return nil
	})
	closeErr := lease.Close()
	if err := errors.Join(checkErr, closeErr); err != nil {
		return err
	}
	return output(out, goCandidateQueryEnvelope{
		Version: 1, Coverage: "PARTIAL", RunID: runID, Repository: corpus.Source,
		CandidateID: candidateID, CandidateHead: snapshot.Candidate.Head,
		CandidateFilesHash: snapshot.Candidate.FilesHash, CandidateFileCount: snapshot.Candidate.FileCount,
		BaseGraphDigest: built.result.Graph.Digest, CandidateGraphDigest: corpus.Graph.Digest,
		BaseModuleInventoryDigest:        corpus.BaseModuleInventoryDigest,
		CandidateModuleInventoryDigest:   corpus.ModuleInventory.Digest,
		CandidateModuleInventoryCoverage: corpus.ModuleInventory.Coverage,
		CandidateManifestObservedCount:   corpus.ModuleInventory.ObservedManifestCount,
		CandidateManifestOmittedCount:    corpus.ModuleInventory.TruncatedManifestCount + len(corpus.ModuleInventory.Omissions),
		CandidateManifestOmissions:       corpus.ModuleInventory.Omissions,
		ChangedPathCount:                 len(corpus.ChangedPaths), AdmittedPathCount: len(corpus.AdmittedPaths), DeletedPathCount: len(corpus.DeletedPaths),
		OmittedCount: corpus.OmittedCount, OmissionsTrimmed: corpus.OmissionsTrimmed,
		Omissions: corpus.Omissions, Result: result,
	})
}

func candidateQueryBaseMatchesSpec(graph ri.GoEngineeringGraph, spec goGraphSpec) bool {
	if len(graph.Files) != len(spec.Files) || !reflect.DeepEqual(graph.ModuleInventory, spec.ModuleInventory) {
		return false
	}
	byPath := make(map[string]goGraphFileSpec, len(spec.Files))
	for _, file := range spec.Files {
		byPath[file.Path] = file
	}
	for _, file := range graph.Files {
		want, ok := byPath[file.Facts.Path]
		if !ok || (want.ImportPath != "" && want.ImportPath != file.Package.ImportPath) ||
			(want.ModulePath != "" && want.ModulePath != file.Package.ModulePath) ||
			(want.TestOfImportPath != "" && want.TestOfImportPath != file.Package.TestOfImportPath) {
			return false
		}
		delete(byPath, file.Facts.Path)
	}
	if len(byPath) != 0 {
		return false
	}
	wantGenerators := append([]ri.GoGeneratorBinding(nil), spec.Generators...)
	gotGenerators := append([]ri.GoGeneratorBinding(nil), graph.Generators...)
	less := func(items []ri.GoGeneratorBinding) func(i, j int) bool {
		return func(i, j int) bool {
			if items[i].GeneratorPath != items[j].GeneratorPath {
				return items[i].GeneratorPath < items[j].GeneratorPath
			}
			if items[i].GeneratedPath != items[j].GeneratedPath {
				return items[i].GeneratedPath < items[j].GeneratedPath
			}
			return items[i].Directive < items[j].Directive
		}
	}
	sort.Slice(wantGenerators, less(wantGenerators))
	sort.Slice(gotGenerators, less(gotGenerators))
	return reflect.DeepEqual(wantGenerators, gotGenerators)
}

func validateCandidateQuerySnapshot(snapshot control.Snapshot, runID string, identity repository.Identity) (string, error) {
	if snapshot.RunID != runID {
		return "", errors.New("run journal identity mismatch")
	}
	if snapshot.Workspace == nil || snapshot.Candidate == nil || snapshot.WorkspaceOutcome != "CONFIRMED" || snapshot.FileOutcome == "UNKNOWN" {
		return "", errors.New("candidate query requires a confirmed workspace and candidate")
	}
	if identity != snapshot.Creation.Repository || snapshot.Workspace.Request.Source != identity || snapshot.Workspace.Request.RunID != snapshot.RunID {
		return "", errors.New("run source or workspace differs from the currently configured repository")
	}
	if err := snapshot.Candidate.ValidateBinding(*snapshot.Workspace); err != nil || snapshot.Candidate.Head != identity.Commit {
		return "", errors.New("run candidate is not bound to its confirmed source workspace")
	}
	return snapshot.Candidate.ID()
}
