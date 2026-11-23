package control

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

type frozenV5PlannerGoContextPrompt struct {
	RecordID                       string                     `json:"record_id"`
	Coverage                       string                     `json:"coverage"`
	RIExecutableSHA256             string                     `json:"ri_executable_sha256"`
	GraphDigest                    string                     `json:"graph_digest,omitempty"`
	Context                        *ri.GoContextManifest      `json:"context,omitempty"`
	ContractContext                *ri.GoContractContext      `json:"contract_context,omitempty"`
	FocusTopology                  *ri.GoFocusTopology        `json:"focus_topology,omitempty"`
	FocusTopologyUnavailableReason string                     `json:"focus_topology_unavailable_reason,omitempty"`
	Unavailable                    string                     `json:"unavailable,omitempty"`
	Generation                     *plannerGoGenerationPrompt `json:"generation,omitempty"`
}

// TestGoContractPlannerContextV3ProjectsBoundGeneratorChain exercises the
// new contract-context prompt recipe against a committed generator fixture.
// It does not execute the generator or call a model.
func TestGoContractPlannerContextV3ProjectsBoundGeneratorChain(t *testing.T) {
	objective := "Update generated Bool behavior through its generator template and verify the public example"
	files := goGenerationFixtureFiles()
	_, v2Path, v2 := admitPlannerGoFixture(t, plannerContextGoContractV2, objective, files, "contract v2 legacy projection")
	if v2.Version != 5 {
		t.Fatalf("legacy contract v2 record version changed: %d", v2.Version)
	}
	v2ForLegacyID := v2
	v2ForLegacyID.RecordID = ""
	legacyID, err := canonical.Hash("harness.control.planner-go-context.v4", v2ForLegacyID)
	if err != nil || legacyID != v2.RecordID {
		t.Fatalf("v5 record identity no longer uses the frozen hash domain: got=%s want=%s err=%v", v2.RecordID, legacyID, err)
	}
	v2Bytes, err := canonical.Bytes(plannerGoContextPromptFor(&v2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v2Bytes), "contract_generation") {
		t.Fatalf("legacy v5 prompt acquired v6 generation evidence: %s", v2Bytes)
	}
	v2Prompt := plannerGoContextPromptFor(&v2)
	frozenV2, err := canonical.Bytes(frozenV5PlannerGoContextPrompt{
		RecordID: v2Prompt.RecordID, Coverage: v2Prompt.Coverage, RIExecutableSHA256: v2Prompt.RIExecutableSHA256,
		GraphDigest: v2Prompt.GraphDigest, Context: v2Prompt.Context, ContractContext: v2Prompt.ContractContext,
		FocusTopology: v2Prompt.FocusTopology, FocusTopologyUnavailableReason: v2Prompt.FocusTopologyUnavailableReason,
		Unavailable: v2Prompt.Unavailable, Generation: v2Prompt.Generation,
	})
	if err != nil || string(v2Bytes) != string(frozenV2) {
		t.Fatalf("v5 prompt JSON changed from its pre-v3 field set: got=%s want=%s err=%v", v2Bytes, frozenV2, err)
	}
	v2Snapshot, err := Inspect(v2Path)
	if err != nil {
		t.Fatalf("legacy v5 journal replay failed: %v", err)
	}
	replayedV2, err := json.Marshal(v2Snapshot.PlannerGoContext)
	if err != nil {
		t.Fatal(err)
	}
	durableV2, err := json.Marshal(v2)
	if err != nil || string(replayedV2) != string(durableV2) {
		t.Fatalf("v5 durable record changed across replay: got=%s want=%s err=%v", replayedV2, durableV2, err)
	}

	c, path, record := admitPlannerGoFixture(t, plannerContextGoContractV3, objective, files, "contract v3 generator projection")
	if record.Version != 6 || record.GenerationMetadata == nil || record.ContractContext == nil || record.Graph == nil {
		t.Fatalf("contract v3 context was not admitted: version=%d metadata=%t", record.Version, record.GenerationMetadata != nil)
	}
	prompt := plannerGoContextPromptFor(&record)
	chain := prompt.ContractGeneration
	if chain == nil || chain.Schema != "engorch.control.planner-go-contract-generation.v1" || chain.Version != 1 || chain.Coverage != "PARTIAL" {
		t.Fatalf("bounded generator projection is missing or misbound: %#v", chain)
	}
	if chain.SourceID != record.Source.RepositoryID || chain.GraphDigest != record.Graph.Digest || chain.MetadataDigest != record.GenerationMetadata.Digest {
		t.Fatalf("generator projection is not bound to the admitted evidence: %#v", chain)
	}
	if chain.KnownBindingCount != len(record.GenerationMetadata.Bindings) || chain.FilteredBindingCount+len(chain.Bindings)+chain.OmittedCount != chain.KnownBindingCount {
		t.Fatalf("generator projection counts do not account for known bindings: %#v", chain)
	}
	found := false
	for _, binding := range chain.Bindings {
		if binding.GeneratorPath != "bool_ext.go" || binding.GeneratedPath != "bool.go" {
			continue
		}
		found = true
		if binding.GeneratorRole != "directive_owner" || binding.GeneratedRole != "generated_output" || binding.GeneratorSHA == "" || binding.GeneratedSHA == "" || binding.DirectiveRange.StartByte < 0 || binding.DirectiveRange.EndByte <= binding.DirectiveRange.StartByte {
			t.Fatalf("generation endpoints or directive range were not source-bound: %#v", binding)
		}
		roles := map[string]bool{}
		for _, source := range binding.ToolSources {
			if source.SHA256 == "" {
				t.Fatalf("tool source lacks a source hash: %#v", source)
			}
			roles[source.Role] = true
		}
		if !roles["generator_source"] || !roles["generator_template"] || !roles["build_definition"] || !binding.ToolProvenanceComplete {
			t.Fatalf("known generator tool/template provenance was omitted: %#v", binding)
		}
	}
	if !found {
		t.Fatalf("visible committed generated output has no projected chain: %#v", chain)
	}
	encoded, err := canonical.Bytes(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > plannerGoContractGenerationPromptMaxBytes || strings.Contains(string(encoded), "context_files") {
		t.Fatalf("v3 projection exceeded its bound or included transient source content: %d bytes", len(encoded))
	}

	// The prompt is reconstructible from durable, hashed journal evidence alone.
	// Verify JSON replay retains the exact projection without transient RI bytes.
	durable, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var replay PlannerGoContextRecord
	if err := json.Unmarshal(durable, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.GenerationMetadata.ContextFiles != nil {
		t.Fatal("transient generation source bytes leaked into the v6 record")
	}
	replayedPrompt, err := canonical.Bytes(plannerGoContextPromptFor(&replay))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(replayedPrompt) {
		t.Fatal("v3 generation projection changed after durable JSON roundtrip")
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("v3 planner context journal replay failed: %v", err)
	}

	// A forged tool-source digest cannot be turned into a prompt projection.
	for name, mutate := range map[string]func(*ri.GoGenerationMetadata){
		"owner path":  func(metadata *ri.GoGenerationMetadata) { metadata.Bindings[0].GeneratorPath = "other_owner.go" },
		"output path": func(metadata *ri.GoGenerationMetadata) { metadata.Bindings[0].GeneratedPath = "other_output.go" },
		"tool path": func(metadata *ri.GoGenerationMetadata) {
			metadata.Bindings[0].ToolSources[0].Path = "internal/other.go"
		},
		"tool hash": func(metadata *ri.GoGenerationMetadata) {
			fakeHash := strings.Repeat("f", 64)
			metadata.Bindings[0].ToolSources[0].Source.SHA256 = fakeHash
			for index := range metadata.Sources {
				if metadata.Sources[index].Path == metadata.Bindings[0].ToolSources[0].Path {
					metadata.Sources[index].SHA256 = fakeHash
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := replay
			metadata := *replay.GenerationMetadata
			metadata.Bindings = append([]ri.GoGenerationBinding(nil), replay.GenerationMetadata.Bindings...)
			metadata.Bindings[0].ToolSources = append([]ri.GoGenerationSourceRef(nil), replay.GenerationMetadata.Bindings[0].ToolSources...)
			metadata.Sources = append([]repository.SourceDigest(nil), replay.GenerationMetadata.Sources...)
			mutate(&metadata)
			rehashGoGenerationMetadata(t, &metadata)
			forged.GenerationMetadata = &metadata
			forged.RecordID, err = plannerGoContextRecordID(forged)
			if err != nil {
				t.Fatal(err)
			}
			badPath := filepath.Join(t.TempDir(), "forged-"+name+".jsonl")
			if err := Append(badPath, "run.created", c); err != nil {
				t.Fatal(err)
			}
			if err := Append(badPath, "planner.go-context-admitted", forged); err == nil {
				t.Fatalf("self-rehashed %s substitution was admitted", name)
			}
		})
	}

	filtered := record
	contractCopy := *record.ContractContext
	contractCopy.Excerpts = nil
	contractCopy.Relations = nil
	filtered.ContractContext = &contractCopy
	filteredProjection := plannerGoContractGenerationPromptFor(&filtered)
	if filteredProjection == nil || len(filteredProjection.Bindings) != 0 || filteredProjection.FilteredBindingCount != filteredProjection.KnownBindingCount || !filteredProjection.Truncated {
		t.Fatalf("unselected output was incorrectly projected as relevant: %#v", filteredProjection)
	}

	// Exercise both prompt caps without requiring another parser/provider run.
	capped := record
	graphCopy := *record.Graph
	graphCopy.Generators = append([]ri.GoGeneratorBinding(nil), record.Graph.Generators...)
	contextCopy := *record.ContractContext
	contextCopy.Excerpts = append([]taskcontext.SelectedFile(nil), record.ContractContext.Excerpts...)
	contextCopy.Relations = append([]ri.GoGraphEdge(nil), record.ContractContext.Relations...)
	metadataCopy := *record.GenerationMetadata
	metadataCopy.Bindings = append([]ri.GoGenerationBinding(nil), record.GenerationMetadata.Bindings...)
	metadataCopy.Sources = append([]repository.SourceDigest(nil), record.GenerationMetadata.Sources...)
	first := metadataCopy.Bindings[0]
	first.ToolSources = append([]ri.GoGenerationSourceRef(nil), first.ToolSources...)
	toolAppendCount := plannerGoContractGenerationPromptSources - len(first.ToolSources) + 1
	for index := 0; index < toolAppendCount; index++ {
		toolPath := fmt.Sprintf("internal/extra-tool-%02d.go", index)
		source := first.ToolSources[0].Source
		source.Path = toolPath
		first.ToolSources = append(first.ToolSources, ri.GoGenerationSourceRef{Path: toolPath, Role: "generator_source", Source: source})
		metadataCopy.Sources = append(metadataCopy.Sources, source)
	}
	metadataCopy.Bindings[0] = first
	for index := 1; index < plannerGoContractGenerationPromptBindings+2; index++ {
		owner := fmt.Sprintf("owner-%02d.go", index)
		output := fmt.Sprintf("output-%02d.go", index)
		binding := metadataCopy.Bindings[0]
		binding.GeneratorPath, binding.GeneratedPath = owner, output
		binding.Directive = fmt.Sprintf("//go:generate tool -file=%s", output)
		binding.DirectiveSource.Path, binding.DirectiveSource.SHA256 = owner, strings.Repeat(fmt.Sprintf("%x", index+1), 64)[:64]
		binding.OutputSource.Path, binding.OutputSource.SHA256 = output, strings.Repeat(fmt.Sprintf("%x", index+11), 64)[:64]
		metadataCopy.Bindings = append(metadataCopy.Bindings, binding)
		metadataCopy.Sources = append(metadataCopy.Sources, binding.DirectiveSource, binding.OutputSource)
		graphCopy.Generators = append(graphCopy.Generators, ri.GoGeneratorBinding{GeneratorPath: owner, GeneratedPath: output, Directive: binding.Directive})
		contextCopy.Excerpts = append(contextCopy.Excerpts, taskcontext.SelectedFile{Path: output})
		contextCopy.Relations = append(contextCopy.Relations, ri.GoGraphEdge{Relation: "GENERATED_BY", Resolution: "EXPLICIT_SOURCE_BOUND", Path: output})
	}
	capped.Graph, capped.ContractContext = &graphCopy, &contextCopy
	capped.GenerationMetadata = &metadataCopy
	projection := plannerGoContractGenerationPromptFor(&capped)
	if projection == nil || len(projection.Bindings) != plannerGoContractGenerationPromptBindings || projection.OmittedCount != 2 || !projection.Truncated || projection.Bindings[0].ToolSourcesOmittedCount != 1 || len(projection.Bindings[0].ToolSources) != plannerGoContractGenerationPromptSources {
		t.Fatalf("generator projection caps/truncation are not reported accurately: %#v", projection)
	}
}
