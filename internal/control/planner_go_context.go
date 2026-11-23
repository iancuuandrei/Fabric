package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

const (
	plannerContextGoSourceV1   = "go-source-context-v1"
	plannerContextGoSourceV2   = "go-source-context-v2"
	plannerContextGoContractV1 = "go-contract-context-v1"
	plannerContextGoContractV2 = "go-contract-context-v2"
	plannerContextGoContractV3 = "go-contract-context-v3"
)

const (
	plannerGoContextVersion       = 2
	plannerGoContextMaxFiles      = 24
	plannerGoContextMaxFileBytes  = 1 << 20
	plannerGoContextMaxInputBytes = 8 << 20
	plannerGoContextMaxGraphBytes = 512 << 10
	plannerGoContextMaxRecord     = 768 << 10
)

// PlannerGoContextRecord is immutable, pre-planning engineering evidence for
// one committed Go corpus. It is partial by design: omitted paths, unresolved
// calls, and absent files never make an absence or semantic-correctness claim.
// The parser is a pinned local executable, not a model/provider invocation.
type PlannerGoContextRecord struct {
	Version int `json:"version"`
	// CorpusSelectionVersion binds the v5 receiver-aware selection recipe.
	// Empty preserves every earlier record representation.
	CorpusSelectionVersion int                       `json:"corpus_selection_version,omitempty"`
	Source                 ri.Source                 `json:"source"`
	Query                  string                    `json:"query"`
	QueryTruncated         bool                      `json:"query_truncated,omitempty"`
	QueryHash              string                    `json:"query_hash"`
	QueryLen               int                       `json:"query_len"`
	RIExecutableSHA256     string                    `json:"ri_executable_sha256"`
	InventoryFiles         int                       `json:"inventory_files"`
	AttemptedFiles         int                       `json:"attempted_files"`
	ReadFiles              int                       `json:"read_files"`
	OmittedCount           int                       `json:"omitted_count"`
	Omissions              []taskcontext.Omission    `json:"omissions,omitempty"`
	OmissionsTrimmed       bool                      `json:"omissions_trimmed,omitempty"`
	Sources                []repository.SourceDigest `json:"sources,omitempty"`
	Graph                  *ri.GoEngineeringGraph    `json:"graph,omitempty"`
	Context                *ri.GoContextManifest     `json:"context,omitempty"`
	// PPR binds the opt-in Personalized PageRank treatment over the already
	// admitted graph for go-source-context-v2 planning. It carries fixed
	// parameters, exact seeds and the independent advisory file ranks with
	// their projection/hash provenance. Nil preserves every earlier record
	// representation byte-for-byte.
	PPR                *ri.GoPPRProvenance      `json:"ppr,omitempty"`
	Unavailable        string                   `json:"unavailable,omitempty"`
	RecordID           string                   `json:"record_id"`
	GenerationMetadata *ri.GoGenerationMetadata `json:"generation_metadata,omitempty"`
	GenerationContext  *ri.GoGenerationContext  `json:"generation_context,omitempty"`
	// ContractContext is the compact, source-bound v1 contract evidence view.
	// It replaces the v2 broad context and generation prompt text for this mode.
	ContractContext *ri.GoContractContext `json:"contract_context,omitempty"`
	// ContractSources retains the complete admitted source corpus only for v4
	// replay. It is never copied into the model-visible planner prompt.
	ContractSources           []PlannerGoContractSource `json:"contract_sources,omitempty"`
	ContractGenerationSources []PlannerGoContractSource `json:"contract_generation_sources,omitempty"`
}

// PlannerGoContractSource is v4-only durable compiler input. Content is a
// string so canonical journal JSON retains the exact UTF-8 source bytes.
type PlannerGoContractSource struct {
	Path    string `json:"path"`
	Hash    string `json:"hash"`
	Content string `json:"content"`
}

// plannerGoContextPrompt is the model-visible view of a larger durable
// record. It excludes the full graph/fact corpus while retaining the exact
// selected evidence, its graph digest, and the immutable record identity.
type plannerGoContextPrompt struct {
	RecordID                       string                             `json:"record_id"`
	Coverage                       string                             `json:"coverage"`
	RIExecutableSHA256             string                             `json:"ri_executable_sha256"`
	GraphDigest                    string                             `json:"graph_digest,omitempty"`
	Context                        *ri.GoContextManifest              `json:"context,omitempty"`
	ContractContext                *ri.GoContractContext              `json:"contract_context,omitempty"`
	FocusTopology                  *ri.GoFocusTopology                `json:"focus_topology,omitempty"`
	FocusTopologyUnavailableReason string                             `json:"focus_topology_unavailable_reason,omitempty"`
	Unavailable                    string                             `json:"unavailable,omitempty"`
	Generation                     *plannerGoGenerationPrompt         `json:"generation,omitempty"`
	ContractGeneration             *plannerGoContractGenerationPrompt `json:"contract_generation,omitempty"`
}

// plannerGoContractGenerationPrompt is a compact, source-bound projection of
// generator chains visible in a v6 contract context. It carries no source
// text, command execution result, or write authority.
type plannerGoContractGenerationPrompt struct {
	Schema                string                               `json:"schema"`
	Version               int                                  `json:"version"`
	SourceID              string                               `json:"source_id"`
	GraphDigest           string                               `json:"graph_digest"`
	MetadataDigest        string                               `json:"metadata_digest"`
	Coverage              string                               `json:"coverage"`
	Bindings              []plannerGoContractGenerationBinding `json:"bindings"`
	KnownBindingCount     int                                  `json:"known_binding_count"`
	FilteredBindingCount  int                                  `json:"filtered_binding_count"`
	DiscoveryOmittedCount int                                  `json:"discovery_omitted_count"`
	OmittedCount          int                                  `json:"omitted_count"`
	Truncated             bool                                 `json:"truncated"`
}

type plannerGoContractGenerationBinding struct {
	GeneratorPath           string                              `json:"generator_path"`
	GeneratorRole           string                              `json:"generator_role"`
	GeneratorSHA            string                              `json:"generator_sha256"`
	GeneratedPath           string                              `json:"generated_path"`
	GeneratedRole           string                              `json:"generated_role"`
	GeneratedSHA            string                              `json:"generated_sha256"`
	Directive               string                              `json:"directive"`
	DirectiveRange          ri.GoRange                          `json:"directive_range"`
	ToolProvenanceComplete  bool                                `json:"tool_provenance_complete"`
	ToolSources             []plannerGoContractGenerationSource `json:"tool_sources"`
	ToolSourcesOmittedCount int                                 `json:"tool_sources_omitted_count,omitempty"`
}

type plannerGoContractGenerationSource struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	SHA256 string `json:"sha256"`
}

const plannerGoContractGenerationPromptBindings = 8
const plannerGoContractGenerationPromptSources = 8
const plannerGoContractGenerationPromptMaxBytes = 16 << 10

// plannerGoGenerationPrompt is the compact planner view of source-bound
// generation evidence. The durable record retains full metadata; this view
// deliberately excludes source inventories and retained source bytes.
type plannerGoGenerationPrompt struct {
	MetadataDigest string                     `json:"metadata_digest"`
	SourceID       string                     `json:"source_id"`
	Coverage       string                     `json:"coverage"`
	Bindings       []plannerGoGenerationChain `json:"bindings,omitempty"`
	Context        *ri.GoGenerationContext    `json:"context,omitempty"`
	Truncated      bool                       `json:"truncated,omitempty"`
	OmittedCount   int                        `json:"omitted_count,omitempty"`
}

type plannerGoGenerationChain struct {
	OwnerPath  string   `json:"owner_path"`
	OutputPath string   `json:"output_path"`
	Directive  string   `json:"directive"`
	ToolPaths  []string `json:"tool_paths,omitempty"`
	ToolRoles  []string `json:"tool_roles,omitempty"`
	ToolSHA256 []string `json:"tool_sha256,omitempty"`
}

func plannerGoGenerationPromptFor(metadata *ri.GoGenerationMetadata, generation *ri.GoGenerationContext) *plannerGoGenerationPrompt {
	if metadata == nil {
		return nil
	}
	view := &plannerGoGenerationPrompt{
		MetadataDigest: metadata.Digest,
		SourceID:       metadata.SourceID,
		Coverage:       metadata.Coverage,
		Context:        generation,
		Truncated:      metadata.Truncated,
		OmittedCount:   metadata.OmittedCount,
	}
	for _, binding := range metadata.Bindings {
		chain := plannerGoGenerationChain{OwnerPath: binding.GeneratorPath, OutputPath: binding.GeneratedPath, Directive: binding.Directive}
		for _, tool := range binding.ToolSources {
			chain.ToolPaths = append(chain.ToolPaths, tool.Path)
			chain.ToolRoles = append(chain.ToolRoles, tool.Role)
			chain.ToolSHA256 = append(chain.ToolSHA256, tool.Source.SHA256)
		}
		view.Bindings = append(view.Bindings, chain)
	}
	return view
}

func plannerGoContractGenerationPromptFor(record *PlannerGoContextRecord) *plannerGoContractGenerationPrompt {
	if record == nil || record.Graph == nil || record.ContractContext == nil || record.GenerationMetadata == nil {
		return nil
	}
	metadata := record.GenerationMetadata
	visiblePaths := make(map[string]bool)
	for _, excerpt := range record.ContractContext.Excerpts {
		visiblePaths[excerpt.Path] = true
	}
	visibleOutputs := make(map[string]bool)
	for _, relation := range record.ContractContext.Relations {
		if relation.Relation == "GENERATED_BY" && relation.Resolution == "EXPLICIT_SOURCE_BOUND" && visiblePaths[relation.Path] {
			visibleOutputs[relation.Path] = true
		}
	}
	graphBindings := make(map[string]bool, len(record.Graph.Generators))
	for _, binding := range record.Graph.Generators {
		graphBindings[binding.GeneratorPath+"\x00"+binding.GeneratedPath+"\x00"+binding.Directive] = true
	}
	sourceHashes := make(map[string]string, len(metadata.Sources))
	for _, source := range metadata.Sources {
		sourceHashes[source.Path] = source.SHA256
	}
	result := &plannerGoContractGenerationPrompt{
		Schema: "engorch.control.planner-go-contract-generation.v1", Version: 1,
		SourceID: metadata.SourceID, GraphDigest: record.Graph.Digest, MetadataDigest: metadata.Digest,
		Coverage: "PARTIAL", Bindings: []plannerGoContractGenerationBinding{},
		KnownBindingCount: len(metadata.Bindings), DiscoveryOmittedCount: metadata.OmittedCount,
		Truncated: metadata.Truncated || metadata.OmittedCount > 0 || metadata.OmissionsTrimmed,
	}
	for _, binding := range metadata.Bindings {
		key := binding.GeneratorPath + "\x00" + binding.GeneratedPath + "\x00" + binding.Directive
		if !visibleOutputs[binding.GeneratedPath] || !graphBindings[key] {
			result.FilteredBindingCount++
			continue
		}
		if len(result.Bindings) >= plannerGoContractGenerationPromptBindings {
			result.OmittedCount++
			result.Truncated = true
			continue
		}
		if sourceHashes[binding.GeneratorPath] == "" || sourceHashes[binding.GeneratedPath] == "" || binding.DirectiveSource.SHA256 != sourceHashes[binding.GeneratorPath] || binding.OutputSource.SHA256 != sourceHashes[binding.GeneratedPath] {
			// Durable record validation rejects this mismatch before prompt
			// construction; fail closed if the helper is called independently.
			return nil
		}
		chain := plannerGoContractGenerationBinding{
			GeneratorPath: binding.GeneratorPath, GeneratorRole: "directive_owner", GeneratorSHA: sourceHashes[binding.GeneratorPath],
			GeneratedPath: binding.GeneratedPath, GeneratedRole: "generated_output", GeneratedSHA: sourceHashes[binding.GeneratedPath],
			Directive: binding.Directive, DirectiveRange: binding.DirectiveRange,
			ToolProvenanceComplete: binding.ToolProvenanceComplete,
			ToolSources:            []plannerGoContractGenerationSource{},
		}
		for _, source := range binding.ToolSources {
			if source.Source.SHA256 == "" || sourceHashes[source.Path] != source.Source.SHA256 {
				return nil
			}
			if len(chain.ToolSources) >= plannerGoContractGenerationPromptSources {
				chain.ToolSourcesOmittedCount++
				result.Truncated = true
				continue
			}
			chain.ToolSources = append(chain.ToolSources, plannerGoContractGenerationSource{Path: source.Path, Role: source.Role, SHA256: source.Source.SHA256})
		}
		result.Bindings = append(result.Bindings, chain)
		if encoded, err := canonical.Bytes(result); err != nil || len(encoded) > plannerGoContractGenerationPromptMaxBytes {
			result.Bindings = result.Bindings[:len(result.Bindings)-1]
			result.OmittedCount++
			result.Truncated = true
		}
	}
	result.Truncated = result.Truncated || result.FilteredBindingCount > 0
	return result
}

func plannerGoContextPromptFor(record *PlannerGoContextRecord) *plannerGoContextPrompt {
	if record == nil {
		return nil
	}
	prompt := &plannerGoContextPrompt{RecordID: record.RecordID, Coverage: "PARTIAL", RIExecutableSHA256: record.RIExecutableSHA256, Unavailable: record.Unavailable}
	if record.Unavailable == "" && record.Graph != nil && record.Context != nil {
		prompt.GraphDigest = record.Graph.Digest
		context := *record.Context
		prompt.Context = &context
	}
	if record.Unavailable == "" && record.Graph != nil && record.ContractContext != nil {
		prompt.GraphDigest = record.Graph.Digest
		contract := *record.ContractContext
		prompt.ContractContext = &contract
	}
	if record.Version == 4 || record.Version == 5 || record.Version == 6 {
		prompt.FocusTopology, prompt.FocusTopologyUnavailableReason = plannerGoFocusTopologyFor(record)
	}
	if record.Version == 3 && record.GenerationMetadata != nil {
		prompt.Generation = plannerGoGenerationPromptFor(record.GenerationMetadata, record.GenerationContext)
	}
	if record.Version == 6 {
		prompt.ContractGeneration = plannerGoContractGenerationPromptFor(record)
	}
	return prompt
}

func plannerGoFocusTopologyFor(record *PlannerGoContextRecord) (*ri.GoFocusTopology, string) {
	if record == nil || record.Graph == nil || record.ContractContext == nil || record.Unavailable != "" {
		return nil, "contract_context_unavailable"
	}
	graph, contract := record.Graph, record.ContractContext
	if graph.SourceID != record.Source.RepositoryID || graph.SourceID != contract.SourceID || graph.CandidateID != contract.CandidateID || graph.Digest != contract.GraphDigest || graph.ProducerSHA256 != contract.ProducerSHA256 {
		return nil, "contract_graph_binding_mismatch"
	}
	pathsPresent := make(map[string]bool, len(graph.Files))
	for _, file := range graph.Files {
		pathsPresent[file.Facts.Path] = true
	}
	seen := make(map[string]bool, len(contract.Excerpts))
	focusPaths := make([]string, 0, len(contract.Excerpts))
	for _, excerpt := range contract.Excerpts {
		if !pathsPresent[excerpt.Path] || seen[excerpt.Path] {
			continue
		}
		seen[excerpt.Path] = true
		focusPaths = append(focusPaths, excerpt.Path)
	}
	sort.Strings(focusPaths)
	if len(focusPaths) == 0 {
		return nil, "no_graph_contract_excerpt_paths"
	}
	topology, err := ri.QueryGoFocusTopology(*graph, focusPaths, 8)
	if err != nil {
		return nil, "focus_topology_query_failed"
	}
	return &topology, ""
}

func plannerGoContextEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && (s.Creation.Execution.PlannerContext == plannerContextGoSourceV1 || s.Creation.Execution.PlannerContext == plannerContextGoSourceV2 || s.Creation.Execution.PlannerContext == plannerContextGoContractV1 || s.Creation.Execution.PlannerContext == plannerContextGoContractV2 || s.Creation.Execution.PlannerContext == plannerContextGoContractV3)
}

func plannerGoContextRecordID(rec PlannerGoContextRecord) (string, error) {
	rec.RecordID = ""
	domain := "harness.control.planner-go-context.v1"
	if rec.Version == 3 {
		domain = "harness.control.planner-go-context.v2"
	}
	if rec.Version == 4 {
		domain = "harness.control.planner-go-context.v3"
	}
	if rec.Version == 5 {
		domain = "harness.control.planner-go-context.v4"
	}
	if rec.Version == 6 {
		domain = "harness.control.planner-go-context.v5"
	}
	return canonical.Hash(domain, rec)
}

// plannerGoContractUnavailable removes v4 evidence that cannot fit the strict
// journal ceiling. It records an unavailable admission rather than retaining a
// partial compiler input that replay could not reproduce exactly.
func plannerGoContractUnavailable(rec PlannerGoContextRecord, reason string) PlannerGoContextRecord {
	rec.Unavailable = reason
	rec.ReadFiles = 0
	rec.Sources = nil
	rec.Graph = nil
	rec.Context = nil
	rec.GenerationMetadata = nil
	rec.GenerationContext = nil
	rec.ContractContext = nil
	rec.ContractSources = nil
	rec.ContractGenerationSources = nil
	rec.OmittedCount++
	if len(rec.Omissions) < 64 {
		rec.Omissions = append(rec.Omissions, taskcontext.Omission{Path: "[context]", Reason: reason})
	} else {
		rec.OmissionsTrimmed = true
	}
	return rec
}

// AdmitPlannerGoContext gathers one bounded, committed Go corpus and compiles
// it into read-only planning evidence before a planner runtime intent exists.
// Parser failures stop preparation; this function never falls back to a model
// or silently substitutes working-tree bytes.
func AdmitPlannerGoContext(ctx context.Context, path string) (PlannerGoContextRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return PlannerGoContextRecord{}, err
	}
	if s.State != "OBJECTIVE" || !plannerGoContextEnabled(s) {
		return PlannerGoContextRecord{}, errors.New("Go planner context is not enabled before planning")
	}
	if s.PlannerGoContext != nil {
		return *s.PlannerGoContext, nil
	}
	if s.PlannerContext != nil {
		return PlannerGoContextRecord{}, errors.New("conflicting planner context admission")
	}
	policy := s.Creation.Execution
	if policy == nil || policy.PlannerContextRIExecutable == "" || safepath.RequireDigest(policy.PlannerContextRIExecutableSHA256) != nil || !filepath.IsAbs(policy.PlannerContextRIExecutable) || filepath.Clean(policy.PlannerContextRIExecutable) != policy.PlannerContextRIExecutable {
		return PlannerGoContextRecord{}, errors.New("Go planner context requires an absolute pinned RI executable")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return PlannerGoContextRecord{}, err
	}
	source, err := ri.FromRepository(s.Creation.Repository)
	if err != nil {
		return PlannerGoContextRecord{}, err
	}
	query, truncated, queryHash, queryLen := truncateTaskQuery(s.Creation.Objective)
	client := ri.Client{Executable: policy.PlannerContextRIExecutable, ExecutableHash: policy.PlannerContextRIExecutableSHA256}
	options := ri.GoCorpusOptions{}
	if policy.PlannerContext == plannerContextGoContractV1 || policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
		inventory, inventoryErr := ri.CollectGoModuleInventory(ctx, s.Creation.Repository)
		if inventoryErr != nil {
			return PlannerGoContextRecord{}, inventoryErr
		}
		options.ModuleInventory = &inventory
	}
	if policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
		options.SelectionVersion = ri.GoCorpusSelectionReceiverAwareV2
	}
	var corpus ri.GoCommittedCorpus
	if policy.PlannerParseCacheVersion == 1 {
		cacheDir, cacheErr := ensurePlannerParseCacheDir(s.Creation.Repository, policy.PlannerContextRIExecutableSHA256)
		cacheDir, cacheErr = optionalCacheDirectory(cacheDir, cacheErr)
		if cacheErr != nil {
			return PlannerGoContextRecord{}, cacheErr
		}
		options.EnableParseCache = cacheDir != ""
		corpus, err = ri.CollectCommittedGoCorpusWithOptions(ctx, s.Creation.Repository, client, cacheDir, query, options)
	} else {
		corpus, err = ri.CollectCommittedGoCorpusWithOptions(ctx, s.Creation.Repository, client, "", query, options)
	}
	if err != nil {
		return PlannerGoContextRecord{}, err
	}
	record := PlannerGoContextRecord{
		Version: plannerGoContextVersion, Source: source, Query: query, QueryTruncated: truncated, QueryHash: queryHash, QueryLen: queryLen,
		RIExecutableSHA256: policy.PlannerContextRIExecutableSHA256, InventoryFiles: corpus.InventoryFiles, AttemptedFiles: corpus.AttemptedFiles,
		ReadFiles: corpus.ReadFiles, OmittedCount: corpus.OmittedCount, Omissions: append([]taskcontext.Omission(nil), corpus.Omissions...),
		OmissionsTrimmed: corpus.OmissionsTrimmed, Sources: append([]repository.SourceDigest(nil), corpus.Sources...),
	}
	if policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
		record.CorpusSelectionVersion = ri.GoCorpusSelectionReceiverAwareV2
	}
	// Corpus selection is relevance-ranked while durable source bindings are
	// path ordered. Discovery seeds this canonical admitted list so replay can
	// prove it was not pointed at a different generator search set.
	sort.Slice(record.Sources, func(i, j int) bool { return record.Sources[i].Path < record.Sources[j].Path })
	// An unavailable corpus has no admitted seed path. Preserve the legacy
	// unavailable representation rather than asking generation discovery to
	// manufacture an empty seed record.
	if policy.PlannerContext == plannerContextGoSourceV2 || policy.PlannerContext == plannerContextGoContractV1 || policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
		if policy.PlannerContext == plannerContextGoSourceV2 {
			record.Version = 3
		} else if policy.PlannerContext == plannerContextGoContractV1 {
			record.Version = 4
		} else if policy.PlannerContext == plannerContextGoContractV2 {
			record.Version = 5
		} else {
			record.Version = 6
		}
		if record.ReadFiles > 0 {
			metadata, discoverErr := ri.DiscoverCommittedGoGenerators(ctx, s.Creation.Repository, sourcePaths(record.Sources))
			if discoverErr != nil {
				return PlannerGoContextRecord{}, discoverErr
			}
			if err := ri.ValidateGoGenerationMetadata(metadata); err != nil {
				return PlannerGoContextRecord{}, err
			}
			record.GenerationMetadata = &metadata
			if policy.PlannerContext == plannerContextGoSourceV2 && len(metadata.Bindings) > 0 {
				generation, compileErr := ri.CompileGoGenerationContext(metadata, query)
				if compileErr != nil {
					return PlannerGoContextRecord{}, compileErr
				}
				record.GenerationContext = &generation
			}
		}
	}
	if record.ReadFiles == 0 {
		record.Unavailable = corpus.Unavailable
		if record.Unavailable == "" {
			record.Unavailable = "no_eligible_complete_go_source"
		}
	} else {
		generators := append([]ri.GoGeneratorBinding(nil), corpus.Generators...)
		if record.GenerationMetadata != nil {
			generators = admittedGoGenerationBindings(*record.GenerationMetadata, record.Sources)
		}
		graph, buildErr := ri.BuildGoEngineeringGraph(ri.GoGraphSnapshotInput{SourceID: sourceID, ProducerSHA256: policy.PlannerContextRIExecutableSHA256, Files: corpus.GraphInputs, Generators: generators, ModuleInventory: corpus.ModuleInventory})
		if buildErr != nil {
			return PlannerGoContextRecord{}, buildErr
		}
		if err := boundedCanonical(graph, plannerGoContextMaxGraphBytes); err != nil {
			return PlannerGoContextRecord{}, errors.New("Go planner graph exceeds bounded admission size")
		}
		if policy.PlannerContext == plannerContextGoContractV1 || policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
			compilerFiles := clonePlannerGoContractFiles(corpus.ContextFiles)
			durableSources := plannerGoContractSources(corpus.ContextFiles)
			generationFiles := plannerGoContractGenerationFiles(record.GenerationMetadata)
			contract, compileErr := ri.CompileGoContractContext(ri.GoContractContextInput{SourceID: sourceID, Graph: graph, ModuleInventory: corpus.ModuleInventory, Generation: record.GenerationMetadata, GenerationFiles: generationFiles, Objective: query, Files: compilerFiles})
			if compileErr != nil {
				return PlannerGoContextRecord{}, compileErr
			}
			record.Graph, record.ContractContext, record.ContractSources = &graph, &contract, durableSources
			record.ContractGenerationSources = plannerGoContractSources(generationFiles)
		} else {
			limits := taskcontext.DefaultLimits()
			if policy.PlannerContext == plannerContextGoSourceV2 {
				limits.MaxBytes = 24 << 10
			}
			var pprRequest *ri.GoPPRRequest
			if policy.PlannerPPRVersion == 1 {
				if policy.PlannerContext != plannerContextGoSourceV2 {
					return PlannerGoContextRecord{}, errors.New("Go planner PPR requires go-source-context-v2")
				}
				pprRequest = &ri.GoPPRRequest{}
			}
			manifest, compileErr := ri.CompileGoContext(ri.GoContextInput{ContextVersion: 2, SourceID: sourceID, Graph: graph, Objective: query, Files: corpus.ContextFiles, Limits: limits, PPR: pprRequest})
			if compileErr != nil {
				return PlannerGoContextRecord{}, compileErr
			}
			record.Graph, record.Context = &graph, &manifest
			if pprRequest != nil {
				if manifest.PPR == nil {
					return PlannerGoContextRecord{}, errors.New("Go planner PPR treatment missing from compiled context")
				}
				ppr := *manifest.PPR
				record.PPR = &ppr
			}
		}
	}
	if policy.PlannerContext == plannerContextGoContractV1 || policy.PlannerContext == plannerContextGoContractV2 || policy.PlannerContext == plannerContextGoContractV3 {
		if !plannerGoContractRecordFits(record) {
			record, err = retainPlannerGoContractRecord(ctx, s.Creation.Repository, sourceID, corpus, record)
			if err != nil {
				return PlannerGoContextRecord{}, err
			}
		}
	}
	record.RecordID, err = plannerGoContextRecordID(record)
	if err != nil {
		return PlannerGoContextRecord{}, err
	}
	if err := validatePlannerGoContextRecord(s, record); err != nil {
		return PlannerGoContextRecord{}, err
	}
	if err := boundedCanonical(record, plannerGoContextMaxRecord); err != nil {
		if policy.PlannerContext != plannerContextGoContractV1 && policy.PlannerContext != plannerContextGoContractV2 && policy.PlannerContext != plannerContextGoContractV3 {
			return PlannerGoContextRecord{}, errors.New("Go planner context record exceeds bounded journal size")
		}
		record = plannerGoContractUnavailable(record, "contract_record_budget")
		record.RecordID, err = plannerGoContextRecordID(record)
		if err != nil {
			return PlannerGoContextRecord{}, err
		}
		if err := validatePlannerGoContextRecord(s, record); err != nil {
			return PlannerGoContextRecord{}, err
		}
		if err := boundedCanonical(record, plannerGoContextMaxRecord); err != nil {
			return PlannerGoContextRecord{}, errors.New("unavailable Go planner contract record exceeds bounded journal size")
		}
	}
	if err := Append(path, "planner.go-context-admitted", record); err != nil {
		return PlannerGoContextRecord{}, err
	}
	return record, nil
}

func clonePlannerGoContractFiles(files []taskcontext.File) []taskcontext.File {
	clone := make([]taskcontext.File, len(files))
	for index, file := range files {
		clone[index] = file
		clone[index].Content = append([]byte(nil), file.Content...)
	}
	return clone
}

func plannerGoContractSources(files []taskcontext.File) []PlannerGoContractSource {
	sources := make([]PlannerGoContractSource, len(files))
	for index, file := range files {
		sources[index] = PlannerGoContractSource{Path: file.Path, Hash: file.Hash, Content: string(file.Content)}
	}
	return sources
}

func plannerGoContractFiles(sources []PlannerGoContractSource) []taskcontext.File {
	files := make([]taskcontext.File, len(sources))
	for index, source := range sources {
		files[index] = taskcontext.File{Path: source.Path, Hash: source.Hash, Content: []byte(source.Content)}
	}
	return files
}

func plannerGoContractGenerationMetadata(metadata *ri.GoGenerationMetadata) *ri.GoGenerationMetadata {
	if metadata == nil {
		return nil
	}
	clone := *metadata
	clone.ContextFiles = nil
	return &clone
}

func boundedCanonical(value any, maximum int) error {
	encoded, err := canonical.Bytes(value)
	if err != nil || len(encoded) > maximum {
		return errors.New("canonical value exceeds bound")
	}
	return nil
}

func sourcePaths(sources []repository.SourceDigest) []string {
	paths := make([]string, len(sources))
	for i, source := range sources {
		paths[i] = source.Path
	}
	return paths
}

// admittedGoGenerationBindings exposes a generation edge only when both ends
// are in the already admitted Go corpus and retain the exact committed digest
// observed by discovery. Tool provenance remains prompt-only evidence: no
// inferred generation edge is added for a supporting source.
func admittedGoGenerationBindings(metadata ri.GoGenerationMetadata, admitted []repository.SourceDigest) []ri.GoGeneratorBinding {
	byPath := make(map[string]repository.SourceDigest, len(admitted))
	for _, source := range admitted {
		byPath[source.Path] = source
	}
	bindings := make([]ri.GoGeneratorBinding, 0, len(metadata.Bindings))
	for _, binding := range metadata.Bindings {
		owner, ownerOK := byPath[binding.GeneratorPath]
		output, outputOK := byPath[binding.GeneratedPath]
		if !ownerOK || !outputOK || owner != binding.DirectiveSource || output != binding.OutputSource {
			continue
		}
		bindings = append(bindings, ri.GoGeneratorBinding{GeneratorPath: binding.GeneratorPath, GeneratedPath: binding.GeneratedPath, Directive: binding.Directive})
	}
	return bindings
}

func replayPlannerGoContext(s *Snapshot, rec PlannerGoContextRecord) error {
	if err := validatePlannerGoContextRecord(*s, rec); err != nil {
		return err
	}
	copy := rec
	copy.Sources = append([]repository.SourceDigest(nil), rec.Sources...)
	copy.Omissions = append([]taskcontext.Omission(nil), rec.Omissions...)
	s.PlannerGoContext = &copy
	return nil
}

func validatePlannerGoContextRecord(s Snapshot, rec PlannerGoContextRecord) error {
	wantVersion := plannerGoContextVersion
	if s.Creation.Execution != nil && s.Creation.Execution.PlannerContext == plannerContextGoSourceV2 {
		wantVersion = 3
	}
	if s.Creation.Execution != nil && s.Creation.Execution.PlannerContext == plannerContextGoContractV1 {
		wantVersion = 4
	}
	if s.Creation.Execution != nil && s.Creation.Execution.PlannerContext == plannerContextGoContractV2 {
		wantVersion = 5
	}
	if s.Creation.Execution != nil && s.Creation.Execution.PlannerContext == plannerContextGoContractV3 {
		wantVersion = 6
	}
	if s.State != "OBJECTIVE" || !plannerGoContextEnabled(s) || s.PlannerGoContext != nil || s.PlannerContext != nil || rec.Version != wantVersion || safepath.RequireDigest(rec.RIExecutableSHA256) != nil || s.Creation.Execution == nil || rec.RIExecutableSHA256 != s.Creation.Execution.PlannerContextRIExecutableSHA256 {
		return errors.New("Go planner context transition rejected")
	}
	wantPPR := s.Creation.Execution.PlannerPPRVersion == 1
	if wantPPR && s.Creation.Execution.PlannerContext != plannerContextGoSourceV2 {
		return errors.New("Go planner PPR requires go-source-context-v2")
	}
	if !wantPPR && rec.PPR != nil {
		return errors.New("legacy Go planner context carries PPR treatment")
	}
	if !wantPPR && rec.Context != nil && rec.Context.PPR != nil {
		return errors.New("legacy Go planner context carries nested PPR treatment")
	}
	if wantPPR && rec.Unavailable == "" && rec.PPR == nil {
		return errors.New("Go planner PPR treatment is missing")
	}
	if wantPPR && rec.Unavailable == "" && (rec.Context == nil || rec.Context.PPR == nil) {
		return errors.New("Go planner PPR nested treatment is missing")
	}
	if rec.PPR != nil && rec.Version != 3 {
		return errors.New("Go planner PPR treatment requires record version 3")
	}
	sourceID, err := s.Creation.Repository.ID()
	if err != nil {
		return err
	}
	if rec.Source.RepositoryID != sourceID || rec.Source.ObjectFormat != s.Creation.Repository.ObjectFormat || rec.Source.Commit != s.Creation.Repository.Commit || rec.Source.Tree != s.Creation.Repository.Tree {
		return errors.New("Go planner context source substitution")
	}
	query, truncated, queryHash, queryLen := truncateTaskQuery(s.Creation.Objective)
	if rec.Query != query || rec.QueryTruncated != truncated || rec.QueryHash != queryHash || rec.QueryLen != queryLen {
		return errors.New("Go planner context query substitution")
	}
	attemptedLimit := plannerGoContextMaxFiles
	if rec.Version == 5 || rec.Version == 6 {
		attemptedLimit = 48
	}
	if rec.InventoryFiles < 0 || rec.InventoryFiles > 500000 || rec.AttemptedFiles < 0 || rec.AttemptedFiles > attemptedLimit || rec.AttemptedFiles > rec.InventoryFiles || rec.ReadFiles < 0 || rec.ReadFiles > plannerGoContextMaxFiles || rec.ReadFiles > rec.AttemptedFiles || rec.ReadFiles != len(rec.Sources) || rec.OmittedCount < 0 || rec.OmittedCount < len(rec.Omissions) || len(rec.Omissions) > 64 || (rec.OmissionsTrimmed && rec.OmittedCount <= len(rec.Omissions)) || (!rec.OmissionsTrimmed && rec.OmittedCount != len(rec.Omissions)) {
		return errors.New("invalid Go planner context inventory")
	}
	for _, omission := range rec.Omissions {
		if (omission.Path != "[redacted]" && safepath.Relative(omission.Path) != nil) || strings.TrimSpace(omission.Reason) == "" || len(omission.Reason) > 64 {
			return errors.New("invalid Go planner context omission")
		}
	}
	if rec.Version == 5 || rec.Version == 6 {
		if rec.CorpusSelectionVersion != ri.GoCorpusSelectionReceiverAwareV2 {
			return errors.New("receiver-aware Go planner context selection binding mismatch")
		}
	} else if rec.CorpusSelectionVersion != 0 {
		return errors.New("legacy Go planner context carries selection version")
	}
	if rec.Unavailable != "" {
		validUnavailable := rec.Unavailable == "no_eligible_complete_go_source" || (rec.Version == 4 || rec.Version == 5 || rec.Version == 6) && rec.Unavailable == "contract_record_budget"
		if !validUnavailable || rec.ReadFiles != 0 || len(rec.Sources) != 0 || rec.Graph != nil || rec.Context != nil || rec.PPR != nil || rec.ContractContext != nil || len(rec.ContractSources) != 0 || len(rec.ContractGenerationSources) != 0 {
			return errors.New("invalid unavailable Go planner context")
		}
	} else {
		if rec.ReadFiles == 0 {
			return errors.New("empty Go planner corpus")
		}
		if err := validatePlannerGoSources(rec, sourceID); err != nil {
			return err
		}
		if rec.Graph == nil || rec.Context == nil && rec.ContractContext == nil {
			return errors.New("Go planner graph or context is missing")
		}
		if err := ri.ValidateGoEngineeringGraph(*rec.Graph); err != nil || rec.Graph.SourceID != sourceID || rec.Graph.CandidateID != "" || rec.Graph.ProducerSHA256 != rec.RIExecutableSHA256 {
			return errors.New("invalid Go planner graph")
		}
		if rec.Version == 2 && len(rec.Graph.Generators) != 0 {
			return errors.New("legacy Go planner graph carries generation bindings")
		}
		if err := boundedCanonical(*rec.Graph, plannerGoContextMaxGraphBytes); err != nil {
			return errors.New("Go planner graph exceeds bound")
		}
		if rec.Version == 4 || rec.Version == 5 || rec.Version == 6 {
			if rec.Context != nil || rec.ContractContext == nil || len(rec.ContractSources) == 0 {
				return errors.New("Go planner contract context is missing or mixed")
			}
			metadata := plannerGoContractGenerationMetadata(rec.GenerationMetadata)
			if err := validatePlannerGoContractContext(*rec.ContractContext, *rec.Graph, rec.Sources, rec.Source, rec.Query, metadata, plannerGoContractFiles(rec.ContractSources), plannerGoContractFiles(rec.ContractGenerationSources)); err != nil {
				return err
			}
		} else {
			contextBudget := 48 << 10
			if rec.Version == 3 {
				contextBudget = 24 << 10
			}
			if rec.Context == nil || rec.ContractContext != nil {
				return errors.New("Go planner context is missing or mixed")
			}
			if err := validatePlannerGoManifest(*rec.Context, *rec.Graph, rec.Sources, rec.Query, contextBudget); err != nil {
				return err
			}
		}
	}
	if rec.Version == 2 && (rec.GenerationMetadata != nil || rec.GenerationContext != nil || rec.ContractContext != nil || rec.PPR != nil || len(rec.ContractSources) != 0 || len(rec.ContractGenerationSources) != 0) {
		return errors.New("legacy Go planner context carries generation data")
	}
	if rec.Version == 3 {
		if len(rec.ContractSources) != 0 || len(rec.ContractGenerationSources) != 0 {
			return errors.New("v2 Go planner context carries contract source input")
		}
		if rec.Unavailable != "" {
			if rec.GenerationMetadata != nil || rec.GenerationContext != nil {
				return errors.New("unavailable Go planner context carries generation data")
			}
			id, err := plannerGoContextRecordID(rec)
			if err != nil || id != rec.RecordID || safepath.RequireDigest(rec.RecordID) != nil {
				return errors.New("Go planner context record substitution")
			}
			return boundedCanonical(rec, plannerGoContextMaxRecord)
		}
		if rec.GenerationMetadata == nil || rec.GenerationMetadata.SourceID != sourceID || ri.ValidateGoGenerationMetadata(*rec.GenerationMetadata) != nil {
			return errors.New("invalid Go generation metadata")
		}
		if !reflect.DeepEqual(rec.GenerationMetadata.SeedPaths, sourcePaths(rec.Sources)) {
			return errors.New("Go generation metadata seed substitution")
		}
		for _, source := range rec.GenerationMetadata.Sources {
			if source.RepositoryID != sourceID || source.Commit != rec.Source.Commit {
				return errors.New("Go generation metadata source substitution")
			}
		}
		if rec.Graph != nil && !reflect.DeepEqual(rec.Graph.Generators, admittedGoGenerationBindings(*rec.GenerationMetadata, rec.Sources)) {
			return errors.New("Go generation graph binding substitution")
		}
		if len(rec.GenerationMetadata.Bindings) > 0 && rec.GenerationContext == nil {
			return errors.New("Go generation context is missing")
		}
		if rec.GenerationContext != nil {
			if rec.GenerationContext.SourceID != sourceID || ri.ValidateGoGenerationContext(*rec.GenerationContext, *rec.GenerationMetadata) != nil {
				return errors.New("invalid Go generation context")
			}
		}
		if rec.PPR != nil {
			if rec.Context == nil || rec.Context.PPR == nil || !reflect.DeepEqual(*rec.Context.PPR, *rec.PPR) {
				return errors.New("Go planner PPR treatment mismatch")
			}
			if err := ri.ValidateGoPPRProvenance(*rec.Graph, rec.Query, *rec.PPR); err != nil {
				return errors.New("Go planner PPR provenance substitution")
			}
			if err := validatePlannerGoPPRSeeds(*rec.Graph, rec.Query, *rec.PPR); err != nil {
				return err
			}
		}
	}
	if rec.Version == 4 || rec.Version == 5 || rec.Version == 6 {
		if rec.Context != nil || rec.GenerationContext != nil {
			return errors.New("contract Go planner context carries v2 prompt evidence")
		}
		if rec.Unavailable != "" {
			if rec.GenerationMetadata != nil || rec.ContractContext != nil || len(rec.ContractSources) != 0 || len(rec.ContractGenerationSources) != 0 {
				return errors.New("unavailable contract Go planner context carries evidence")
			}
		} else if rec.GenerationMetadata == nil || rec.GenerationMetadata.SourceID != sourceID || ri.ValidateGoGenerationMetadata(*rec.GenerationMetadata) != nil {
			return errors.New("invalid contract Go generation metadata")
		} else if rec.Version == 6 {
			if !reflect.DeepEqual(rec.Graph.Generators, admittedGoGenerationBindings(*rec.GenerationMetadata, rec.Sources)) || plannerGoContractGenerationPromptFor(&rec) == nil {
				return errors.New("contract v3 generation prompt projection binding mismatch")
			}
		}
	}
	id, err := plannerGoContextRecordID(rec)
	if err != nil || id != rec.RecordID || safepath.RequireDigest(rec.RecordID) != nil {
		return errors.New("Go planner context record substitution")
	}
	return boundedCanonical(rec, plannerGoContextMaxRecord)
}

// validatePlannerGoPPRSeeds binds stored PPR seeds to the observed planner
// inputs. Planner admission compiles with no changed paths, so the stored
// seeds and truncation must exactly equal the query-derived seeds for the
// bound graph and query. Seeds are observed inputs, not free knobs: a
// self-consistent recomputation from a different valid seed set (rehashed to
// match itself) is still a substitution here and is rejected. Primitive
// callers that compile with explicit changed paths bind those paths at their
// own layer; ValidateGoPPRProvenance alone only proves self-consistency.
func validatePlannerGoPPRSeeds(graph ri.GoEngineeringGraph, query string, provenance ri.GoPPRProvenance) error {
	observed, err := ri.DeriveGoPPRSeeds(graph, query, nil)
	if err != nil {
		return errors.New("Go planner PPR seed derivation failed")
	}
	if !equalPPRSeedFiles(observed.Files, provenance.Seeds) || observed.Truncated != provenance.SeedsTruncated {
		return errors.New("Go planner PPR seed substitution")
	}
	return nil
}

// equalPPRSeedFiles compares seed file sets semantically: nil and empty
// non-nil slices are both the observed empty-seed set. DeriveGoPPRSeeds and
// ComputeGoPPR both encode empty seeds as [] (not null), so the semantic
// comparison keeps a valid empty_seeds fallback replaying exactly across
// admission and JSON roundtrip. Nonempty sets still require exact sorted
// equality; no encoding or identity is changed.
func equalPPRSeedFiles(observed, stored []string) bool {
	if len(observed) == 0 && len(stored) == 0 {
		return true
	}
	return reflect.DeepEqual(observed, stored)
}

func validatePlannerGoSources(rec PlannerGoContextRecord, sourceID string) error {
	seen := map[string]bool{}
	total := int64(0)
	for index, source := range rec.Sources {
		if safepath.Relative(source.Path) != nil || filepath.Ext(source.Path) != ".go" || source.RepositoryID != sourceID || source.Commit != rec.Source.Commit || safepath.RequireDigest(source.SHA256) != nil || source.Bytes < 0 || source.Bytes > plannerGoContextMaxFileBytes || (index > 0 && rec.Sources[index-1].Path >= source.Path) || seen[strings.ToLower(source.Path)] {
			return errors.New("invalid Go planner source binding")
		}
		seen[strings.ToLower(source.Path)] = true
		total += source.Bytes
	}
	if total > plannerGoContextMaxInputBytes || rec.Graph == nil || len(rec.Graph.Files) != len(rec.Sources) {
		return errors.New("Go planner source corpus exceeds bound")
	}
	bound := make(map[string]string, len(rec.Sources))
	for _, source := range rec.Sources {
		bound[source.Path] = source.SHA256
	}
	for _, file := range rec.Graph.Files {
		if bound[file.Facts.Path] == "" || bound[file.Facts.Path] != file.Facts.SourceSHA256 {
			return errors.New("Go planner graph source mismatch")
		}
	}
	return nil
}

func validatePlannerGoManifest(manifest ri.GoContextManifest, graph ri.GoEngineeringGraph, sources []repository.SourceDigest, query string, contextBudget int) error {
	if manifest.Schema != "engorch.ri.go-context.v2" || manifest.GraphDigest != graph.Digest || manifest.ProducerSHA256 != graph.ProducerSHA256 || manifest.Coverage != "PARTIAL" || safepath.RequireDigest(manifest.Digest) != nil {
		return errors.New("invalid Go planner context manifest")
	}
	querySum := sha256.Sum256([]byte(query))
	selection := manifest.Selection
	if contextBudget < 256 || contextBudget > 48<<10 || manifest.QuerySHA256 != hex.EncodeToString(querySum[:]) || selection.Scope.SourceID != graph.SourceID || selection.Scope.CandidateID != "" || len(selection.Selected) == 0 || len(selection.Selected) > 12 || selection.SelectedBytes < 0 || selection.SelectedBytes > contextBudget || selection.InputBytes < 0 || selection.SelectedBytes > selection.InputBytes || selection.Version != 1 || safepath.RequireDigest(selection.InputHash) != nil {
		return errors.New("Go planner context selection mismatch")
	}
	if _, err := selection.ID(); err != nil {
		return errors.New("invalid Go planner context selection identity")
	}
	files := make(map[string]ri.GoGraphFile, len(graph.Files))
	sizes := make(map[string]int64, len(sources))
	for _, file := range graph.Files {
		files[file.Facts.Path] = file
	}
	inputBytes := int64(0)
	for _, source := range sources {
		inputBytes += source.Bytes
		sizes[source.Path] = source.Bytes
	}
	if inputBytes > plannerGoContextMaxInputBytes || int64(selection.InputBytes) != inputBytes {
		return errors.New("Go planner context input byte mismatch")
	}
	if selection.OmittedCount < 0 || selection.OmittedCount > 24 || len(selection.Omissions) > 64 || (selection.OmissionsTrimmed && selection.OmittedCount <= len(selection.Omissions)) || (!selection.OmissionsTrimmed && selection.OmittedCount != len(selection.Omissions)) {
		return errors.New("invalid Go planner context selection omissions")
	}
	for _, omission := range selection.Omissions {
		if (omission.Path != "[redacted]" && safepath.Relative(omission.Path) != nil) || strings.TrimSpace(omission.Reason) == "" || len(omission.Reason) > 64 {
			return errors.New("invalid Go planner context selection omission")
		}
	}
	allExcerpts := append(append([]taskcontext.SelectedFile(nil), selection.Selected...), manifest.ContractExcerpts...)
	if len(manifest.ContractExcerpts) > 32 {
		return errors.New("Go planner context contract excerpts exceed bounds")
	}
	ranges := make(map[string][]taskcontext.SelectedFile, len(allExcerpts))
	selectedBytes := 0
	for _, selected := range selection.Selected {
		file, ok := files[selected.Path]
		if !ok || safepath.RequireDigest(selected.Hash) != nil || selected.Hash != file.Facts.SourceSHA256 || safepath.RequireDigest(selected.ExcerptHash) != nil || selected.Start < 0 || selected.End <= selected.Start || selected.End > sizes[selected.Path] || int64(len(selected.Content)) != selected.End-selected.Start || !utf8.ValidString(selected.Content) || !plannerGoSelectedReason(selected.Reason) || plannerGoOverlaps(selected, ranges[selected.Path]) {
			return errors.New("invalid Go planner context selected excerpt")
		}
		ranges[selected.Path] = append(ranges[selected.Path], selected)
		sum := sha256.Sum256([]byte(selected.Content))
		if hex.EncodeToString(sum[:]) != selected.ExcerptHash {
			return errors.New("Go planner context excerpt substitution")
		}
		selectedBytes += len(selected.Content)
	}
	if selectedBytes != selection.SelectedBytes {
		return errors.New("Go planner context selected byte mismatch")
	}
	contractBytes := 0
	for _, selected := range manifest.ContractExcerpts {
		file, ok := files[selected.Path]
		if !ok || safepath.RequireDigest(selected.Hash) != nil || selected.Hash != file.Facts.SourceSHA256 || safepath.RequireDigest(selected.ExcerptHash) != nil || selected.Start < 0 || selected.End <= selected.Start || selected.End > sizes[selected.Path] || int64(len(selected.Content)) != selected.End-selected.Start || !utf8.ValidString(selected.Content) || !plannerGoSelectedReason(selected.Reason) || plannerGoOverlaps(selected, ranges[selected.Path]) {
			return errors.New("invalid Go planner context contract excerpt")
		}
		ranges[selected.Path] = append(ranges[selected.Path], selected)
		sum := sha256.Sum256([]byte(selected.Content))
		if hex.EncodeToString(sum[:]) != selected.ExcerptHash {
			return errors.New("Go planner context contract excerpt substitution")
		}
		contractBytes += len(selected.Content)
	}
	if selectedBytes+contractBytes > contextBudget {
		return errors.New("Go planner context combined excerpt budget exceeded")
	}
	if len(manifest.Symbols) > 128 || len(manifest.Relations) > 128 {
		return errors.New("Go planner context facts exceed bounds")
	}
	for _, symbol := range manifest.Symbols {
		if symbol.Kind != "declaration" || !plannerGoVisibleNode(symbol, allExcerpts) || !plannerGoGraphHasNode(graph, symbol) {
			return errors.New("Go planner context symbol is not visible graph evidence")
		}
	}
	for _, relation := range manifest.Relations {
		if !plannerGoVisibleEdge(relation, allExcerpts) || !plannerGoGraphHasEdge(graph, relation) {
			return errors.New("Go planner context relation is not visible graph evidence")
		}
	}
	if manifest.PPR != nil {
		if err := ri.ValidateGoPPRProvenance(graph, query, *manifest.PPR); err != nil {
			return errors.New("Go planner context PPR substitution")
		}
		if err := validatePlannerGoPPRSeeds(graph, query, *manifest.PPR); err != nil {
			return err
		}
	}
	copy := manifest
	copy.Digest = ""
	id, err := canonical.Hash("harness.ri.go-context.v2", copy)
	if err != nil || id != manifest.Digest {
		return errors.New("Go planner context digest mismatch")
	}
	if err := boundedCanonical(manifest, 128<<10); err != nil {
		return errors.New("Go planner context manifest exceeds bound")
	}
	return nil
}

// validatePlannerGoContractContext replays the compact v1 contract view from
// durable graph, source-digest and generation metadata. The journal stores
// selected bytes, their hashes and full-source bindings; it never needs to
// reopen a repository or infer unresolved semantics during replay.
func validatePlannerGoContractContext(contract ri.GoContractContext, graph ri.GoEngineeringGraph, sources []repository.SourceDigest, sourceIdentity ri.Source, query string, metadata *ri.GoGenerationMetadata, contractFiles, generationFiles []taskcontext.File) error {
	if contract.Schema != "engorch.ri.go-contract-context.v1" || contract.Version != 1 || contract.SourceID != graph.SourceID || contract.CandidateID != "" || contract.GraphDigest != graph.Digest || contract.ProducerSHA256 != graph.ProducerSHA256 || contract.Coverage != "PARTIAL" || safepath.RequireDigest(contract.Digest) != nil || safepath.RequireDigest(contract.QuerySHA256) != nil || len(contract.Excerpts) > 12 || len(contract.Relations) > 32 || contract.SourceBytes < 0 || contract.SourceBytes > 12<<10 || contract.GenerationBytes < 0 || contract.GenerationBytes > 4<<10 || contract.OmittedCount < len(contract.Omissions) || len(contract.Omissions) > 64 || (contract.OmissionsTrimmed && contract.OmittedCount <= len(contract.Omissions)) || (!contract.OmissionsTrimmed && contract.OmittedCount != len(contract.Omissions)) || metadata == nil {
		return errors.New("invalid Go planner contract context header")
	}
	if (graph.ModuleInventory == nil && contract.ModuleInventoryDigest != "") || (graph.ModuleInventory != nil && (contract.ModuleInventoryDigest != graph.ModuleInventory.Digest || ri.ValidateGoModuleInventoryRecord(*graph.ModuleInventory) != nil)) || contract.GenerationMetadataDigest != metadata.Digest || ri.ValidateGoGenerationMetadata(*metadata) != nil {
		return errors.New("Go planner contract context provenance mismatch")
	}
	if err := ri.ValidateGoContractContext(contract, ri.GoContractContextInput{SourceID: graph.SourceID, Graph: graph, ModuleInventory: graph.ModuleInventory, Generation: metadata, GenerationFiles: generationFiles, Objective: query, Files: clonePlannerGoContractFiles(contractFiles)}); err != nil {
		return fmt.Errorf("Go planner contract context does not reproduce from durable source input: %w", err)
	}
	querySum := sha256.Sum256([]byte(query))
	if contract.QuerySHA256 != hex.EncodeToString(querySum[:]) || metadata.SourceID != graph.SourceID || !reflect.DeepEqual(metadata.SeedPaths, sourcePaths(sources)) {
		return errors.New("Go planner contract context query or seed substitution")
	}
	for _, source := range metadata.Sources {
		if source.RepositoryID != graph.SourceID || source.Commit != sourceIdentity.Commit {
			return errors.New("Go planner contract generation source substitution")
		}
	}
	if !reflect.DeepEqual(graph.Generators, admittedGoGenerationBindings(*metadata, sources)) {
		return errors.New("Go planner contract generation graph substitution")
	}
	graphFiles := make(map[string]ri.GoGraphFile, len(graph.Files))
	sizes := make(map[string]int64, len(sources))
	for _, source := range sources {
		sizes[source.Path] = source.Bytes
	}
	for _, file := range graph.Files {
		graphFiles[file.Facts.Path] = file
	}
	generation := make(map[string]repository.SourceDigest, len(metadata.Sources))
	for _, source := range metadata.Sources {
		generation[source.Path] = source
	}
	seen := map[string]bool{}
	sourceBytes, generationBytes := 0, 0
	for _, selected := range contract.Excerpts {
		if seen[selected.Path] || safepath.Relative(selected.Path) != nil || safepath.RequireDigest(selected.Hash) != nil || safepath.RequireDigest(selected.ExcerptHash) != nil || selected.Start < 0 || selected.End <= selected.Start || int64(len(selected.Content)) != selected.End-selected.Start || !utf8.ValidString(selected.Content) || !plannerGoContractReason(selected.Reason) {
			return errors.New("invalid Go planner contract excerpt")
		}
		seen[selected.Path] = true
		if strings.HasPrefix(selected.Reason, "generation_") {
			source, ok := generation[selected.Path]
			if !ok || selected.Hash != source.SHA256 || selected.End > source.Bytes {
				return errors.New("Go planner contract generation excerpt substitution")
			}
			generationBytes += len(selected.Content)
		} else {
			file, ok := graphFiles[selected.Path]
			if !ok || selected.Hash != file.Facts.SourceSHA256 || selected.End > sizes[selected.Path] {
				return errors.New("Go planner contract source excerpt substitution")
			}
			sourceBytes += len(selected.Content)
		}
		sum := sha256.Sum256([]byte(selected.Content))
		if selected.ExcerptHash != hex.EncodeToString(sum[:]) {
			return errors.New("Go planner contract excerpt hash substitution")
		}
	}
	if sourceBytes != contract.SourceBytes || generationBytes != contract.GenerationBytes {
		return errors.New("Go planner contract excerpt byte mismatch")
	}
	for _, omission := range contract.Omissions {
		if (omission.Path != "[redacted]" && safepath.Relative(omission.Path) != nil) || strings.TrimSpace(omission.Reason) == "" || len(omission.Reason) > 64 {
			return errors.New("invalid Go planner contract omission")
		}
	}
	for _, relation := range contract.Relations {
		if !plannerGoGraphHasEdge(graph, relation) || !plannerGoContractVisibleEdge(relation, contract.Excerpts) {
			return errors.New("Go planner contract relation is not visible graph evidence")
		}
	}
	for _, module := range contract.Modules {
		if !seen[module.Path] {
			return errors.New("Go planner contract module is not selected")
		}
		ownership, err := ri.GoModuleOwnershipForPath(*graph.ModuleInventory, module.Path)
		if err != nil || ownership != module.Ownership || ownership.Status != "declared_module" {
			return errors.New("Go planner contract module substitution")
		}
	}
	body := contract
	body.Digest = ""
	id, err := canonical.Hash("harness.ri.go-contract-context.v1", body)
	if err != nil || id != contract.Digest {
		return errors.New("Go planner contract context digest mismatch")
	}
	return nil
}

func plannerGoContractReason(reason string) bool {
	switch reason {
	case "objective_declaration", "impact_declaration", "test_contract", "generation_owner", "generation_template", "generation_tool", "generation_build", "generation_output":
		return true
	default:
		return false
	}
}

func plannerGoContractVisibleEdge(edge ri.GoGraphEdge, selected []taskcontext.SelectedFile) bool {
	for _, file := range selected {
		if file.Path != edge.Path {
			continue
		}
		if edge.Range == nil || int64(edge.Range.StartByte) >= file.Start && int64(edge.Range.EndByte) <= file.End {
			return true
		}
	}
	return false
}

func plannerGoOverlaps(candidate taskcontext.SelectedFile, existing []taskcontext.SelectedFile) bool {
	for _, prior := range existing {
		if candidate.Start < prior.End && prior.Start < candidate.End {
			return true
		}
	}
	return false
}

func plannerGoSelectedReason(reason string) bool {
	switch reason {
	case "changed_path", "path_hint", "path_match", "lexical_match", "deterministic_fallback", "graph_anchor", "graph_caller":
		return true
	default:
		return false
	}
}

func plannerGoVisibleNode(node ri.GoGraphNode, selected []taskcontext.SelectedFile) bool {
	if node.Path == "" || node.Range == nil {
		return false
	}
	for _, file := range selected {
		if file.Path == node.Path && int64(node.Range.StartByte) >= file.Start && int64(node.Range.EndByte) <= file.End {
			return true
		}
	}
	return false
}

func plannerGoVisibleEdge(edge ri.GoGraphEdge, selected []taskcontext.SelectedFile) bool {
	if edge.Path == "" || edge.Range == nil {
		return false
	}
	for _, file := range selected {
		if file.Path == edge.Path && int64(edge.Range.StartByte) >= file.Start && int64(edge.Range.EndByte) <= file.End {
			return true
		}
	}
	return false
}

func plannerGoGraphHasNode(graph ri.GoEngineeringGraph, node ri.GoGraphNode) bool {
	for _, candidate := range graph.Nodes {
		if reflect.DeepEqual(candidate, node) {
			return true
		}
	}
	return false
}

func plannerGoGraphHasEdge(graph ri.GoEngineeringGraph, edge ri.GoGraphEdge) bool {
	for _, candidate := range graph.Edges {
		if reflect.DeepEqual(candidate, edge) {
			return true
		}
	}
	return false
}
