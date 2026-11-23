package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

// TestPlannerPPRPolicyVersionBinding pins the narrow PPR seam at policy
// validation: only version 1 with go-source-context-v2 is admitted.
func TestPlannerPPRPolicyVersionBinding(t *testing.T) {
	base := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 0}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	pinned := base
	pinned.PlannerContext = plannerContextGoSourceV2
	pinned.PlannerContextRIExecutable = filepath.Join(t.TempDir(), "engorch-ri.exe")
	pinned.PlannerContextRIExecutableSHA256 = strings.Repeat("a", 64)
	if err := pinned.Validate(); err != nil {
		t.Fatalf("v2 policy without PPR rejected: %v", err)
	}
	enabled := pinned
	enabled.PlannerPPRVersion = 1
	if err := enabled.Validate(); err != nil {
		t.Fatalf("v2 policy rejected explicit PPR opt-in: %v", err)
	}
	for name, mutate := range map[string]func(*ExecutionPolicy){
		"version 2":      func(p *ExecutionPolicy) { p.PlannerPPRVersion = 2 },
		"negative":       func(p *ExecutionPolicy) { p.PlannerPPRVersion = -1 },
		"source v1":      func(p *ExecutionPolicy) { p.PlannerContext = plannerContextGoSourceV1 },
		"contract v1":    func(p *ExecutionPolicy) { p.PlannerContext = plannerContextGoContractV1 },
		"contract v2":    func(p *ExecutionPolicy) { p.PlannerContext = plannerContextGoContractV2 },
		"contract v3":    func(p *ExecutionPolicy) { p.PlannerContext = plannerContextGoContractV3 },
		"source bounded": func(p *ExecutionPolicy) { p.PlannerContext = "source-bounded-v1" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := enabled
			mutate(&bad)
			if err := bad.Validate(); err == nil {
				t.Fatalf("invalid PPR policy accepted: %#v", bad)
			}
		})
	}
}

// admitPlannerGoPPRFixture commits a two-file Go corpus and admits a v2
// planner record with the PPR treatment enabled. It exercises admission only:
// no planner, writer or model runtime is started.
func admitPlannerGoPPRFixture(t *testing.T) (Creation, string, PlannerGoContextRecord) {
	t.Helper()
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
	c.Execution.PlannerContext = plannerContextGoSourceV2
	c.Execution.PlannerPPRVersion = 1
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
	return c, path, record
}

func mustReID(t *testing.T, record PlannerGoContextRecord) PlannerGoContextRecord {
	t.Helper()
	var err error
	record.RecordID, err = plannerGoContextRecordID(record)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func pprRoundTrip(t *testing.T, record PlannerGoContextRecord) PlannerGoContextRecord {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var out PlannerGoContextRecord
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPlannerPPRAdmissionAndReplay admits one bounded v2 corpus with the
// opt-in treatment and proves the durable record carries both PPR copies,
// replays without creating a runtime intent, and projects ranks into the
// model-visible prompt.
func TestPlannerPPRAdmissionAndReplay(t *testing.T) {
	_, path, record := admitPlannerGoPPRFixture(t)
	if record.Version != 3 || record.Unavailable != "" || record.Graph == nil || record.Context == nil {
		t.Fatalf("PPR record lacks v2 evidence: %+v", record)
	}
	if record.PPR == nil || record.Context.PPR == nil || !reflect.DeepEqual(*record.Context.PPR, *record.PPR) {
		t.Fatal("PPR treatment is not bound in both record copies")
	}
	if record.PPR.NoSignal || len(record.PPR.Seeds) == 0 || len(record.PPR.Ranks) == 0 {
		t.Fatalf("PPR treatment carries no advisory ranks: %+v", record.PPR)
	}
	if err := ri.ValidateGoPPRProvenance(*record.Graph, record.Query, *record.PPR); err != nil {
		t.Fatalf("admitted PPR provenance does not validate: %v", err)
	}
	observed, err := ri.DeriveGoPPRSeeds(*record.Graph, record.Query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed.Files, record.PPR.Seeds) || observed.Truncated != record.PPR.SeedsTruncated {
		t.Fatalf("admitted PPR seeds are not the observed derivation: %+v vs %+v", record.PPR.Seeds, observed.Files)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlannerGoContext == nil || s.PlannerAccess != nil || len(s.ModelAccess) != 0 || s.WriterProposal != nil || s.PlannerGoContext.RecordID != record.RecordID {
		t.Fatalf("PPR admission created an unexpected effect or failed replay: %+v", s)
	}
	prompt := plannerGoContextPromptFor(&record)
	if prompt == nil || prompt.Context == nil || prompt.Context.PPR == nil || len(prompt.Context.PPR.Ranks) != len(record.PPR.Ranks) {
		t.Fatal("model-visible prompt dropped the PPR ranks")
	}
	encoded, err := canonical.Bytes(prompt)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "rank_hash") {
		t.Fatal("model-visible prompt lacks PPR rank identity")
	}
}

// TestPlannerPPRLegacyAdmissionPreservesExactShape admits the same corpus
// shape without the opt-in and proves no PPR copy or wire key appears.
func TestPlannerPPRLegacyAdmissionPreservesExactShape(t *testing.T) {
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
	c.Execution.PlannerContext = plannerContextGoSourceV2
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
	if record.PPR != nil || record.Context == nil || record.Context.PPR != nil {
		t.Fatal("legacy admission carries PPR treatment")
	}
	encoded, err := canonical.Bytes(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"ppr"`) {
		t.Fatal("legacy record encoding carries a PPR key")
	}
}

// TestPlannerPPRLegacyPolicyRejectsTreatment replays a PPR record against a
// creation that never opted in: legacy-off versus treatment-on substitution
// is rejected.
func TestPlannerPPRLegacyPolicyRejectsTreatment(t *testing.T) {
	c, _, record := admitPlannerGoPPRFixture(t)
	off := c
	off.Execution.PlannerPPRVersion = 0
	bad := filepath.Join(t.TempDir(), "legacy-off.jsonl")
	if err := Append(bad, "run.created", off); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", record); err == nil {
		t.Fatal("legacy policy admitted a PPR treatment record")
	}
}

// TestPlannerPPRNestedOnlyRejected drops the record-level copy while keeping
// the nested manifest copy: a nested-only treatment is rejected even though
// the nested provenance is self-consistent.
func TestPlannerPPRNestedOnlyRejected(t *testing.T) {
	c, _, record := admitPlannerGoPPRFixture(t)
	forged := pprRoundTrip(t, record)
	forged.PPR = nil
	forged = mustReID(t, forged)
	bad := filepath.Join(t.TempDir(), "nested-only.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("nested-only PPR treatment was admitted")
	}
}

// TestPlannerPPRRecordOnlyRejected drops the nested manifest copy while
// keeping the record-level copy: a record-only treatment is rejected.
func TestPlannerPPRRecordOnlyRejected(t *testing.T) {
	c, _, record := admitPlannerGoPPRFixture(t)
	forged := pprRoundTrip(t, record)
	forged.Context.PPR = nil
	forged.Context.Digest = ""
	digest, err := canonical.Hash("harness.ri.go-context.v2", *forged.Context)
	if err != nil {
		t.Fatal(err)
	}
	forged.Context.Digest = digest
	forged = mustReID(t, forged)
	bad := filepath.Join(t.TempDir(), "record-only.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("record-only PPR treatment was admitted")
	}
}

// TestPlannerPPRSelfConsistentSeedForgeRejected recomputes honest provenance
// from a different valid seed set and rehashes both copies: self-consistent
// yet bound to unobserved seeds, so planner admission rejects it.
func TestPlannerPPRSelfConsistentSeedForgeRejected(t *testing.T) {
	c, _, record := admitPlannerGoPPRFixture(t)
	alternate := ""
	for _, file := range record.Graph.Files {
		seen := false
		for _, seed := range record.PPR.Seeds {
			if seed == file.Facts.Path {
				seen = true
			}
		}
		if !seen {
			alternate = file.Facts.Path
		}
	}
	if alternate == "" {
		t.Fatal("fixture leaves no unseeded file for the seed forge")
	}
	forgedProvenance, err := ri.ComputeGoPPR(*record.Graph, ri.GoPPRSeeds{Files: []string{alternate}}, record.Query, ri.DefaultGoPPRConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Honest recomputation from its own seeds: self-consistent by
	// construction, so primitive validation passes.
	if err := ri.ValidateGoPPRProvenance(*record.Graph, record.Query, forgedProvenance); err != nil {
		t.Fatalf("forge setup failed self-consistency: %v", err)
	}
	forged := pprRoundTrip(t, record)
	forged.PPR = &forgedProvenance
	contextCopy := *forged.Context
	contextCopy.PPR = &forgedProvenance
	contextCopy.Digest = ""
	digest, err := canonical.Hash("harness.ri.go-context.v2", contextCopy)
	if err != nil {
		t.Fatal(err)
	}
	contextCopy.Digest = digest
	forged.Context = &contextCopy
	forged = mustReID(t, forged)
	bad := filepath.Join(t.TempDir(), "seed-forge.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("self-consistent unobserved-seed PPR provenance was admitted")
	}
}

// TestPlannerPPREmptySeedsAdmissionAndReplay admits a valid nonempty Go
// corpus under the opt-in where the objective derives NO seeds. It proves
// the explicit empty_seeds fallback survives admission and JSON
// roundtrip/replay, leaves the legacy-selected excerpts byte-identical, and
// creates no runtime intent. Before the semantic seed-equality repair,
// admission rejected this shape because DeriveGoPPRSeeds returns an empty
// non-nil slice while ComputeGoPPR stored nil seeds; both now encode empty
// seeds as [].
func TestPlannerPPREmptySeedsAdmissionAndReplay(t *testing.T) {
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
	c.Objective = "Repair unrelated bookkeeping chore"
	c.Execution.PlannerContext = plannerContextGoSourceV2
	c.Execution.PlannerPPRVersion = 1
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
		t.Fatalf("empty-seeds PPR admission rejected a valid nonempty corpus: %v", err)
	}
	if record.Version != 3 || record.Unavailable != "" || record.Graph == nil || record.Context == nil || record.PPR == nil || record.Context.PPR == nil {
		t.Fatalf("empty-seeds record lacks v2 evidence with treatment: %+v", record)
	}
	if !reflect.DeepEqual(*record.Context.PPR, *record.PPR) {
		t.Fatal("empty-seeds treatment copies differ")
	}
	if !record.PPR.NoSignal || record.PPR.FallbackReason != "empty_seeds" || len(record.PPR.Seeds) != 0 || record.PPR.SeedsTruncated || len(record.PPR.Ranks) != 0 || record.PPR.WorkExhausted {
		t.Fatalf("empty-seeds fallback misreported: %+v", record.PPR)
	}
	observed, err := ri.DeriveGoPPRSeeds(*record.Graph, record.Query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Files) != 0 || observed.Truncated {
		t.Fatalf("fixture objective unexpectedly derives seeds: %+v", observed)
	}
	if err := ri.ValidateGoPPRProvenance(*record.Graph, record.Query, *record.PPR); err != nil {
		t.Fatalf("empty-seeds provenance does not validate: %v", err)
	}
	if err := validatePlannerGoPPRSeeds(*record.Graph, record.Query, *record.PPR); err != nil {
		t.Fatalf("empty-seeds planner seed binding rejected: %v", err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.PlannerGoContext == nil || s.PlannerAccess != nil || len(s.ModelAccess) != 0 || s.WriterProposal != nil || s.PlannerGoContext.RecordID != record.RecordID {
		t.Fatalf("empty-seeds admission created an unexpected effect or failed replay: %+v", s)
	}
	// JSON roundtrip preserves the empty seed shape: stored [] seeds encode
	// as [] and must still replay exactly.
	roundTripped := pprRoundTrip(t, record)
	if len(roundTripped.PPR.Seeds) != 0 || !roundTripped.PPR.NoSignal || roundTripped.PPR.FallbackReason != "empty_seeds" {
		t.Fatalf("JSON roundtrip lost empty-seeds provenance: %+v", roundTripped.PPR)
	}
	replayPath := filepath.Join(t.TempDir(), "replay.jsonl")
	if err := Append(replayPath, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(replayPath, "planner.go-context-admitted", roundTripped); err != nil {
		t.Fatalf("JSON roundtripped empty-seeds record failed replay: %v", err)
	}
	replayed, err := Inspect(replayPath)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.PlannerGoContext == nil || replayed.PlannerGoContext.RecordID != record.RecordID {
		t.Fatal("roundtrip replay lost the record identity")
	}
	// Same corpus and objective without the opt-in must select the exact
	// same excerpts: the no-signal treatment degrades to the current
	// selector without changing visible evidence.
	legacyCreation := c
	legacyCreation.Execution.PlannerPPRVersion = 0
	legacyPath := filepath.Join(t.TempDir(), "legacy.jsonl")
	if err := Append(legacyPath, "run.created", legacyCreation); err != nil {
		t.Fatal(err)
	}
	legacyRecord, err := AdmitPlannerGoContext(context.Background(), legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacyRecord.PPR != nil || legacyRecord.Context == nil || legacyRecord.Context.PPR != nil {
		t.Fatal("legacy empty-seeds admission carries PPR treatment")
	}
	if !reflect.DeepEqual(legacyRecord.Context.Selection.Selected, record.Context.Selection.Selected) {
		t.Fatalf("empty-seeds PPR changed legacy excerpts: %+v vs %+v", legacyRecord.Context.Selection.Selected, record.Context.Selection.Selected)
	}
	if legacyRecord.Context.Selection.InputHash != record.Context.Selection.InputHash || legacyRecord.Context.Selection.SelectedBytes != record.Context.Selection.SelectedBytes {
		t.Fatal("empty-seeds PPR changed legacy selection identity or byte cost")
	}
}

// TestPlannerPPRUnavailableAdmission keeps the no-Go-source path available
// under the opt-in: an unavailable corpus has no graph to rank, so no
// treatment is bound and replay stays exact.
func TestPlannerPPRUnavailableAdmission(t *testing.T) {
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
	c.Execution.PlannerPPRVersion = 1
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
	if record.Version != 3 || record.Unavailable != "no_eligible_complete_go_source" || record.PPR != nil || record.Context != nil || record.Graph != nil {
		t.Fatalf("unavailable PPR record invented treatment evidence: %+v", record)
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("unavailable PPR replay failed: %v", err)
	}
	// An unavailable record must never carry a treatment, even forged.
	forged := record
	treatment := ri.GoPPRProvenance{}
	forged.PPR = &treatment
	forged.RecordID, err = plannerGoContextRecordID(forged)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "unavailable-forged.jsonl")
	if err := Append(bad, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(bad, "planner.go-context-admitted", forged); err == nil {
		t.Fatal("unavailable record admitted a PPR treatment")
	}
}
