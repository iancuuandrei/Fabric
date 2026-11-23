package control

import (
	"context"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

// Reserve the exact final record-ID width before hashing or journal admission.
func plannerGoContractRecordFits(record PlannerGoContextRecord) bool {
	record.RecordID = strings.Repeat("0", 64)
	return boundedCanonical(record, plannerGoContextMaxRecord) == nil
}

// retainPlannerGoContractRecord chooses the longest prefix of the already
// relevance-ranked, parsed corpus whose complete replay input fits the journal.
// It never reparses sources or calls a model. Generation discovery reads only
// the same immutable Git tree, with exact retained seed paths for each trial.
// Legacy source-context admission does not use this recipe.
func retainPlannerGoContractRecord(ctx context.Context, identity repository.Identity, sourceID string, corpus ri.GoCommittedCorpus, full PlannerGoContextRecord) (PlannerGoContextRecord, error) {
	for count := len(corpus.GraphInputs) - 1; count > 0; count-- {
		if err := ctx.Err(); err != nil {
			return PlannerGoContextRecord{}, err
		}
		record := full
		record.RecordID = ""
		record.Sources = nil
		record.Omissions = append([]taskcontext.Omission(nil), full.Omissions...)
		retained := make(map[string]bool, count)
		for _, input := range corpus.GraphInputs[:count] {
			retained[input.Facts.Path] = true
		}
		for _, source := range corpus.Sources {
			if retained[source.Path] {
				record.Sources = append(record.Sources, source)
			}
		}
		sort.Slice(record.Sources, func(i, j int) bool { return record.Sources[i].Path < record.Sources[j].Path })
		record.ReadFiles = len(record.Sources)
		for _, input := range corpus.GraphInputs[count:] {
			record.OmittedCount++
			if len(record.Omissions) < 64 {
				record.Omissions = append(record.Omissions, taskcontext.Omission{Path: input.Facts.Path, Reason: "contract_record_budget"})
			} else {
				record.OmissionsTrimmed = true
			}
		}
		metadata, err := ri.DiscoverCommittedGoGenerators(ctx, identity, sourcePaths(record.Sources))
		if err != nil {
			return PlannerGoContextRecord{}, err
		}
		graph, err := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{
			SourceID: sourceID, ProducerSHA256: full.RIExecutableSHA256,
			Files: corpus.GraphInputs[:count], Generators: admittedGoGenerationBindings(metadata, record.Sources), ModuleInventory: corpus.ModuleInventory,
		})
		if err != nil {
			return PlannerGoContextRecord{}, err
		}
		if boundedCanonical(graph, plannerGoContextMaxGraphBytes) != nil {
			continue
		}
		files := make([]taskcontext.File, 0, count)
		for _, file := range corpus.ContextFiles {
			if retained[file.Path] {
				files = append(files, file)
			}
		}
		contract, err := ri.CompileGoContractContext(ri.GoContractContextInput{
			SourceID: sourceID, Graph: graph, ModuleInventory: corpus.ModuleInventory,
			Generation: &metadata, GenerationFiles: plannerGoContractGenerationFiles(&metadata), Objective: full.Query, Files: clonePlannerGoContractFiles(files),
		})
		if err != nil {
			return PlannerGoContextRecord{}, err
		}
		record.Graph, record.GenerationMetadata, record.ContractContext = &graph, &metadata, &contract
		record.ContractSources = plannerGoContractSources(files)
		record.ContractGenerationSources = plannerGoContractSources(plannerGoContractGenerationFiles(&metadata))
		if plannerGoContractRecordFits(record) {
			return record, nil
		}
	}
	return plannerGoContractUnavailable(full, "contract_record_budget"), nil
}

// Discovery may read sibling Go files merely to find directives. Those bytes
// are not compiler inputs unless a literal binding references them; retaining
// them again would duplicate the ordinary corpus without adding evidence.
func plannerGoContractGenerationFiles(metadata *ri.GoGenerationMetadata) []taskcontext.File {
	files := make([]taskcontext.File, 0)
	if metadata == nil {
		return files
	}
	bound := make(map[string]bool)
	for _, binding := range metadata.Bindings {
		bound[binding.GeneratorPath], bound[binding.GeneratedPath] = true, true
		for _, source := range binding.ToolSources {
			bound[source.Path] = true
		}
	}
	for _, file := range metadata.ContextFiles {
		if bound[file.Path] {
			files = append(files, file)
		}
	}
	return files
}
