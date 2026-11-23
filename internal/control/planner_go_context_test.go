package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/taskcontext"
)

func plannerGoContextUnavailableFixture(t *testing.T) (string, Creation, PlannerGoContextRecord) {
	t.Helper()
	c := autonomousCreation(t, 0)
	c.Execution.PlannerContext = plannerContextGoSourceV1
	c.Execution.PlannerContextRIExecutable = filepath.Join(t.TempDir(), "engorch-ri.exe")
	c.Execution.PlannerContextRIExecutableSHA256 = strings.Repeat("a", 64)
	root := c.Repository.Root
	autonomousGitInit(t, root)
	var err error
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ri.FromRepository(c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	query, truncated, hash, length := truncateTaskQuery(c.Objective)
	record := PlannerGoContextRecord{
		Version: plannerGoContextVersion, Source: source, Query: query, QueryTruncated: truncated, QueryHash: hash, QueryLen: length,
		RIExecutableSHA256: c.Execution.PlannerContextRIExecutableSHA256, Unavailable: "no_eligible_complete_go_source",
	}
	record.RecordID, err = plannerGoContextRecordID(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	return path, c, record
}

func TestGoPlannerContextUnavailableAdmissionBindsPlannerInputAndReplays(t *testing.T) {
	path, _, record := plannerGoContextUnavailableFixture(t)
	if err := Append(path, "planner.go-context-admitted", record); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlannerGoContext == nil || s.PlannerGoContext.RecordID != record.RecordID || s.PlannerGoContext.Unavailable == "" {
		t.Fatalf("unavailable Go context was not replayed: %+v", s.PlannerGoContext)
	}
	invocation, err := plannerInvocationForSnapshot(s)
	if err != nil || !strings.Contains(invocation.Input, record.RecordID) {
		t.Fatalf("planner invocation lacks durable Go context: %v", err)
	}
}

func TestGoPlannerContextReplayRejectsRecordAndSourceSubstitution(t *testing.T) {
	_, c, record := plannerGoContextUnavailableFixture(t)
	for name, change := range map[string]func(*PlannerGoContextRecord){
		"record": func(r *PlannerGoContextRecord) { r.Unavailable = "forged" },
		"source": func(r *PlannerGoContextRecord) { r.Source.Tree = strings.Repeat("b", 64) },
		"parser": func(r *PlannerGoContextRecord) { r.RIExecutableSHA256 = strings.Repeat("b", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.jsonl")
			if err := Append(path, "run.created", c); err != nil {
				t.Fatal(err)
			}
			forged := record
			change(&forged)
			if err := Append(path, "planner.go-context-admitted", forged); err == nil {
				t.Fatal("forged Go planner context was admitted")
			}
		})
	}
}

func TestGoPlannerContextPolicyRequiresOnlyItsPinnedParser(t *testing.T) {
	base := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 0}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.PlannerContext = plannerContextGoSourceV1
	if err := bad.Validate(); err == nil {
		t.Fatal("Go planner context accepted without parser pin")
	}
	good := bad
	good.PlannerContextRIExecutable = filepath.Join(t.TempDir(), "engorch-ri.exe")
	good.PlannerContextRIExecutableSHA256 = strings.Repeat("a", 64)
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	contract := good
	contract.PlannerContext = plannerContextGoContractV1
	if err := contract.Validate(); err != nil {
		t.Fatalf("contract context with parser pin rejected: %v", err)
	}
	contract.PlannerParseCacheVersion = 1
	if err := contract.Validate(); err != nil {
		t.Fatalf("contract context rejected explicit parse cache: %v", err)
	}
	unsupported := contract
	unsupported.PlannerContext = plannerContextGoSourceV1
	if err := unsupported.Validate(); err == nil {
		t.Fatal("v1 context accepted unsupported parse cache")
	}
	legacyPlanner := base
	legacyPlanner.PlannerContextRIExecutable = good.PlannerContextRIExecutable
	legacyPlanner.PlannerContextRIExecutableSHA256 = good.PlannerContextRIExecutableSHA256
	if err := legacyPlanner.Validate(); err == nil {
		t.Fatal("legacy planner context accepted Go parser fields")
	}
}

// TestGoPlannerContextV2UnavailableAdmission keeps the no-Go-source path
// available without inventing an empty generation discovery record.
func TestGoPlannerContextV2UnavailableAdmission(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("ENGORCH_RI_BINARY is required for local RI fixture")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	c := autonomousCreation(t, 0)
	c.Execution.PlannerContext = plannerContextGoSourceV2
	c.Execution.PlannerContextRIExecutable = filepath.Clean(executable)
	c.Execution.PlannerContextRIExecutableSHA256 = hex.EncodeToString(digest[:])
	root := c.Repository.Root
	autonomousGitInit(t, root)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("no Go source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "No Go fixture"}} {
		if out, err := autonomousGitCmd(t, root, args); err != nil {
			t.Fatal(err, string(out))
		}
	}
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	record, err := AdmitPlannerGoContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if record.Version != 3 || record.Unavailable != "no_eligible_complete_go_source" || record.GenerationMetadata != nil || record.GenerationContext != nil {
		t.Fatalf("v2 unavailable record invented generation evidence: %+v", record)
	}
	invocation, err := plannerInvocationForSnapshot(mustInspect(t, path))
	if err != nil || !strings.Contains(invocation.Input, record.RecordID) {
		t.Fatalf("unavailable v2 planner prompt lacks compact durable record: %v", err)
	}
	persisted, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var replay PlannerGoContextRecord
	if err := json.Unmarshal(persisted, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.GenerationMetadata != nil || replay.GenerationContext != nil {
		t.Fatal("unavailable v2 durable record has generation data")
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("unavailable v2 replay failed: %v", err)
	}
	forged := record
	forged.GenerationMetadata = &ri.GoGenerationMetadata{}
	forged.RecordID, err = plannerGoContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "forged.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("unavailable v2 record accepted forged generation data")
	}
}

func mustInspect(t *testing.T, path string) Snapshot {
	t.Helper()
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPlannerGoContextV1IdentityGolden protects the established v1 record
// representation. v2-only generation fields must remain nil and omitted from
// historical canonical bytes and the v1 hash domain.
func TestPlannerGoContextV1IdentityGolden(t *testing.T) {
	record := PlannerGoContextRecord{
		Version: 2,
		Source:  ri.Source{RepositoryID: strings.Repeat("1", 64), ObjectFormat: "sha256", Commit: strings.Repeat("2", 64), Tree: strings.Repeat("3", 64)},
		Query:   "preserve legacy identity", QueryHash: strings.Repeat("4", 64), QueryLen: len("preserve legacy identity"),
		RIExecutableSHA256: strings.Repeat("5", 64), Unavailable: "no_eligible_complete_go_source",
	}
	bytes, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	const wantBytes = `{"attempted_files":0,"inventory_files":0,"omitted_count":0,"query":"preserve legacy identity","query_hash":"4444444444444444444444444444444444444444444444444444444444444444","query_len":24,"read_files":0,"record_id":"","ri_executable_sha256":"5555555555555555555555555555555555555555555555555555555555555555","source":{"commit":"2222222222222222222222222222222222222222222222222222222222222222","object_format":"sha256","repository_id":"1111111111111111111111111111111111111111111111111111111111111111","tree":"3333333333333333333333333333333333333333333333333333333333333333"},"unavailable":"no_eligible_complete_go_source","version":2}`
	if string(bytes) != wantBytes {
		t.Fatalf("v1 canonical bytes changed:\n got %s\nwant %s", bytes, wantBytes)
	}
	got, err := plannerGoContextRecordID(record)
	if err != nil {
		t.Fatal(err)
	}
	const wantID = "2234ad17cf940f78893a7e15ba9dcf7d0bdd7692f1428f2aeb7dffd37f0139bf"
	if got != wantID {
		t.Fatalf("v1 record ID changed: got %s want %s", got, wantID)
	}
}

// TestGoPlannerContextNonemptyAdmissionAndReplay uses the pinned local RI
// executable when supplied by the test environment. It creates a committed
// fixture and exercises only admission/replay: no planner, writer, or model
// runtime is started.
func TestGoPlannerContextNonemptyAdmissionAndReplay(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("ENGORCH_RI_BINARY is required for local RI fixture")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	c := autonomousCreation(t, 0)
	c.Objective = "Correct SplitLines semantics and document the example contract"
	c.Execution.PlannerContext = plannerContextGoSourceV1
	c.Execution.PlannerContextRIExecutable = filepath.Clean(executable)
	c.Execution.PlannerContextRIExecutableSHA256 = hex.EncodeToString(digest[:])
	root := c.Repository.Root
	autonomousGitInit(t, root)
	for path, content := range map[string]string{
		"difflib/difflib.go":      "package difflib\n\n// SplitLines preserves existing terminators.\nfunc SplitLines(input string) []string { return []string{input} }\n\nfunc GetContextDiffString(input string) string { return SplitLines(input)[0] }\n",
		"difflib/difflib_test.go": "package difflib\n\nimport \"fmt\"\n\nfunc ExampleGetContextDiffString() {\n\tfmt.Println(GetContextDiffString(\"line\\n\"))\n\t// Output: line\n}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "difflib"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "Go fixture"}} {
		if out, err := autonomousGitCmd(t, root, args); err != nil {
			t.Fatal(err, string(out))
		}
	}
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	record, err := AdmitPlannerGoContext(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if record.Unavailable != "" || record.Graph == nil || record.Context == nil || record.Context.Schema != "engorch.ri.go-context.v2" || len(record.Sources) == 0 {
		t.Fatalf("expected nonempty v2 evidence: %+v", record)
	}
	if err := boundedCanonical(*record.Graph, plannerGoContextMaxGraphBytes); err != nil {
		t.Fatal(err)
	}
	if err := boundedCanonical(record, plannerGoContextMaxRecord); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlannerGoContext == nil || s.PlannerAccess != nil || len(s.ModelAccess) != 0 || s.WriterProposal != nil || s.PlannerGoContext.RecordID != record.RecordID {
		t.Fatalf("admission created an unexpected effect or failed replay: %+v", s)
	}
	all := append(append([]taskcontext.SelectedFile(nil), record.Context.Selection.Selected...), record.Context.ContractExcerpts...)
	joined := ""
	for _, excerpt := range all {
		joined += string(excerpt.Content)
	}
	if !strings.Contains(joined, "SplitLines") || !strings.Contains(joined, "ExampleGetContextDiffString") {
		t.Fatalf("v2 context lacks target contract evidence: %q", joined)
	}
	forged := record
	forged.Source.Tree = strings.Repeat("f", len(forged.Source.Tree))
	forged.RecordID, err = plannerGoContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "forged.jsonl")
	if err := Append(badPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(badPath, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("self-rehashed v2 source substitution was admitted")
	}
}

// TestGoPlannerContextV2GenerationAdmissionAndReplay uses a committed fixture
// with a literal generator owner, embedded template, and marked output. It
// exercises the v2 pre-planning evidence only; no model or writer is called.
func TestGoPlannerContextV2GenerationAdmissionAndReplay(t *testing.T) {
	c, path, record := admitPlannerGoFixture(t, plannerContextGoSourceV2, "Update generated Bool behavior through its generator template and verify the public example", goGenerationFixtureFiles(), "Go generation fixture")

	if record.Version != 3 || record.GenerationMetadata == nil || len(record.GenerationMetadata.Bindings) == 0 || record.GenerationContext == nil || record.GenerationContext.SelectedBytes > 8<<10 {
		t.Fatalf("v2 generation evidence was not admitted: %+v", record)
	}
	contextBytes := record.Context.Selection.SelectedBytes
	for _, excerpt := range record.Context.ContractExcerpts {
		contextBytes += len(excerpt.Content)
	}
	if contextBytes > 24<<10 {
		t.Fatalf("v2 Go context exceeded its 24 KiB budget: %d", contextBytes)
	}
	if got := record.Graph.Generators; len(got) != 1 || got[0].GeneratorPath != "bool_ext.go" || got[0].GeneratedPath != "bool.go" {
		t.Fatalf("source-bound generation graph edge missing: %+v", got)
	}
	prompt := plannerGoContextPromptFor(&record)
	encoded, err := canonical.Bytes(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "generation_owner") || strings.Contains(string(encoded), "repository_id") || strings.Contains(string(encoded), "context_files") {
		t.Fatalf("generation prompt is not compact source-bound evidence: %s", encoded)
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("v2 generation replay failed: %v", err)
	}

	// Reconstruct the exact durable shape before forging: ContextFiles are
	// transient and must not be needed to reject a self-rehashed excerpt.
	persisted, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var forged PlannerGoContextRecord
	if err := json.Unmarshal(persisted, &forged); err != nil {
		t.Fatal(err)
	}
	if forged.GenerationMetadata.ContextFiles != nil || forged.GenerationContext.ContextFiles != nil {
		t.Fatal("transient generation source bytes leaked into durable record")
	}
	contextCopy := *forged.GenerationContext
	contextCopy.Selected = append([]taskcontext.SelectedFile(nil), contextCopy.Selected...)
	contextCopy.Selected[0].Content = "x" + contextCopy.Selected[0].Content[1:]
	sum := sha256.Sum256([]byte(contextCopy.Selected[0].Content))
	contextCopy.Selected[0].ExcerptHash = hex.EncodeToString(sum[:])
	body := contextCopy
	body.Digest = ""
	contextCopy.Digest, err = canonical.Hash("engorch.ri.go-generation-context.v1", body)
	if err != nil {
		t.Fatal(err)
	}
	forged.GenerationContext = &contextCopy
	forged.RecordID, err = plannerGoContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "tampered.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("self-rehashed generation content substitution was admitted")
	}

	seedForged := record
	metadataCopy := *record.GenerationMetadata
	metadataCopy.SeedPaths = append([]string(nil), metadataCopy.SeedPaths...)
	metadataCopy.SeedPaths[0] = "aaa.go"
	rehashGoGenerationMetadata(t, &metadataCopy)
	if err := ri.ValidateGoGenerationMetadata(metadataCopy); err != nil {
		t.Fatalf("self-rehashed alternate metadata should remain internally valid: %v", err)
	}
	seedForged.GenerationMetadata = &metadataCopy
	seedForged.GenerationContext = nil
	seedForged.RecordID, err = plannerGoContextRecordID(seedForged)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(t.TempDir(), "seed-substitution.jsonl")
	if err := Append(seedPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(seedPath, "planner.go-context-admitted", seedForged); err == nil {
		t.Fatal("self-rehashed generation seed substitution was admitted")
	}

	commitForged := record
	commitMetadata := copyGenerationMetadata(*record.GenerationMetadata)
	for i := range commitMetadata.Sources {
		commitMetadata.Sources[i].Commit = strings.Repeat("f", len(commitMetadata.Sources[i].Commit))
	}
	byPath := make(map[string]repository.SourceDigest, len(commitMetadata.Sources))
	for _, source := range commitMetadata.Sources {
		byPath[source.Path] = source
	}
	for i := range commitMetadata.Bindings {
		binding := &commitMetadata.Bindings[i]
		binding.DirectiveSource = byPath[binding.GeneratorPath]
		binding.OutputSource = byPath[binding.GeneratedPath]
		for j := range binding.ToolSources {
			binding.ToolSources[j].Source = byPath[binding.ToolSources[j].Path]
		}
	}
	rehashGoGenerationMetadata(t, &commitMetadata)
	if err := ri.ValidateGoGenerationMetadata(commitMetadata); err != nil {
		t.Fatalf("self-rehashed alternate commit metadata should remain internally valid: %v", err)
	}
	commitForged.GenerationMetadata = &commitMetadata
	commitForged.GenerationContext = nil
	commitForged.RecordID, err = plannerGoContextRecordID(commitForged)
	if err != nil {
		t.Fatal(err)
	}
	commitPath := filepath.Join(t.TempDir(), "commit-substitution.jsonl")
	if err := Append(commitPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(commitPath, "planner.go-context-admitted", commitForged); err == nil {
		t.Fatal("self-rehashed generation commit substitution was admitted")
	}
}

func copyGenerationMetadata(metadata ri.GoGenerationMetadata) ri.GoGenerationMetadata {
	copy := metadata
	copy.SeedPaths = append([]string{}, metadata.SeedPaths...)
	copy.Sources = append([]repository.SourceDigest{}, metadata.Sources...)
	copy.Bindings = append([]ri.GoGenerationBinding{}, metadata.Bindings...)
	for i := range copy.Bindings {
		copy.Bindings[i].ToolSources = append([]ri.GoGenerationSourceRef{}, metadata.Bindings[i].ToolSources...)
	}
	copy.Omissions = append([]taskcontext.Omission{}, metadata.Omissions...)
	copy.ContextFiles = append([]taskcontext.File{}, metadata.ContextFiles...)
	return copy
}

func rehashGoGenerationMetadata(t *testing.T, metadata *ri.GoGenerationMetadata) {
	t.Helper()
	seed, err := canonical.Hash("harness.ri.go-generation.seeds.v1", struct {
		SourceID string   `json:"source_id"`
		Recipe   string   `json:"recipe"`
		Seeds    []string `json:"seeds"`
	}{metadata.SourceID, "sibling-go-v1", metadata.SeedPaths})
	if err != nil {
		t.Fatal(err)
	}
	metadata.SeedDigest = seed
	body := *metadata
	body.Digest = ""
	bytes, err := canonical.Bytes(body)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bytes)
	metadata.Digest = hex.EncodeToString(digest[:])
}

// TestGoContractPlannerContextAdmissionAndReplay exercises the separate v1
// contract prompt view with the pinned RI parser. It performs no model call.
func TestGoContractPlannerContextAdmissionAndReplay(t *testing.T) {
	c, path, record := admitPlannerGoFixture(t, plannerContextGoContractV1, "Correct SplitLines behavior and preserve its example contract", map[string]string{
		"go.mod":                  "module example.test/difflib\n\ngo 1.25\n",
		"difflib/difflib.go":      "package difflib\nfunc SplitLines(input string) []string { return []string{input} }\nfunc WriteUnified(input string) string { return SplitLines(input)[0] }\n",
		"difflib/difflib_test.go": "package difflib\nfunc ExampleSplitLines() { _ = SplitLines(\"line\\n\") }\n",
	}, "contract context fixture")

	if record.Version != 4 || record.Context != nil || record.GenerationContext != nil || record.ContractContext == nil || record.GenerationMetadata == nil || record.Graph == nil || record.Graph.ModuleInventory == nil || len(record.ContractSources) != record.ReadFiles || record.ContractContext.SourceBytes > 12<<10 || record.ContractContext.GenerationBytes > 4<<10 {
		t.Fatalf("contract evidence was not separately admitted: %+v", record)
	}
	prompt := plannerGoContextPromptFor(&record)
	if prompt.Context != nil || prompt.Generation != nil || prompt.ContractContext == nil {
		t.Fatalf("contract prompt stacked v2 evidence: %#v", prompt)
	}
	encoded, err := canonical.Bytes(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "contract_context") || strings.Contains(string(encoded), "\"generation\":") || strings.Contains(string(encoded), "context_files") {
		t.Fatalf("contract prompt is not compact durable evidence: %s", encoded)
	}
	persisted, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var replay PlannerGoContextRecord
	if err := json.Unmarshal(persisted, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.GenerationMetadata.ContextFiles != nil {
		t.Fatal("transient generation bytes leaked into contract record")
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("contract context replay failed: %v", err)
	}
	forgedSource := record
	forgedSource.ContractSources = append([]PlannerGoContractSource(nil), record.ContractSources...)
	forgedSource.ContractSources[0].Content = "package forged\n"
	forgedSource.RecordID, err = plannerGoContextRecordID(forgedSource)
	if err != nil {
		t.Fatal(err)
	}
	badSource := filepath.Join(t.TempDir(), "forged-source.jsonl")
	if err := Append(badSource, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(badSource, "planner.go-context-admitted", forgedSource); err == nil {
		t.Fatal("self-rehashed contract source substitution was admitted")
	}

	forged := record
	contract := *record.ContractContext
	contract.GraphDigest = strings.Repeat("f", 64)
	body := contract
	body.Digest = ""
	contract.Digest, err = canonical.Hash("harness.ri.go-contract-context.v1", body)
	if err != nil {
		t.Fatal(err)
	}
	forged.ContractContext = &contract
	forged.RecordID, err = plannerGoContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "forged.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("self-rehashed contract graph substitution was admitted")
	}
}
