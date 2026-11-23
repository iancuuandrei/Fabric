package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func cliGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
}

func autonomousCLIFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cliGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", "source.txt")
	cliGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	mustExecuteCLI(t, root, "init")
	return root
}

// mustExecuteCLI runs one CLI invocation against root and fails the test on
// error, returning the captured stdout. It factors the repeated
// Execute-plus-buffer boilerplate shared by CLI fixtures.
func mustExecuteCLI(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := Execute(context.Background(), args, root, &b); err != nil {
		t.Fatalf("execute %q: %v", strings.Join(args, " "), err)
	}
	return b.Bytes()
}

// planAutonomousFailureFixture plans one objective and returns the durable run
// path, snapshot and a reset buffer ready for reportAutonomousFailure.
func planAutonomousFailureFixture(t *testing.T, objective string) (string, control.Snapshot, bytes.Buffer) {
	t.Helper()
	root := autonomousCLIFixture(t)
	var planOut bytes.Buffer
	if err := Execute(context.Background(), []string{"plan", objective}, root, &planOut); err != nil {
		t.Fatal(err)
	}
	var s control.Snapshot
	if err := json.Unmarshal(planOut.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".harness", "runs", s.RunID+".jsonl")
	var out bytes.Buffer
	return path, s, out
}

func TestRunAutonomousCreatesBoundPolicyAndReportsResumableBlocker(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--max-repairs", "3", "Make a bounded fixture change"}, root, &out)
	if err == nil {
		t.Fatal("fixture planner emits a non-graph response; autonomous run should retain its semantic blocker")
	}
	var result autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("failure omitted sanitized run summary: %s (%v)", out.String(), err)
	}

	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Objective != "Make a bounded fixture change" || s.Creation.Execution == nil || s.Creation.Execution.Mode != "autonomous-v1" || s.Creation.Execution.MaxRepairs != 3 {
		t.Fatalf("autonomous input was not durably bound: %#v", s.Creation)
	}
	if s.Creation.Execution.GraphVersion != 1 || s.Creation.Execution.MaxParallel != 3 || s.Creation.Execution.Context != "bounded-v1" || s.Creation.Execution.RepairPlanningVersion != 1 || s.Creation.Execution.ParallelImplementationVersion != 0 || s.Creation.Config.PlannerContract != "plan-graph-v5" {
		t.Fatalf("graph execution and bounded context not defaulted: %#v", s.Creation.Execution)
	}
	if s.Creation.Execution.PlannerContext != "" {
		t.Fatalf("planner context must remain disabled by default: %#v", s.Creation.Execution)
	}
	if s.State != "AWAITING_APPROVAL" || result.RunID != s.RunID || result.State != s.State || result.Phase != "approval" || result.BlockedReason != "semantic_correction_budget_exhausted" {
		t.Fatalf("failure summary or durable state mismatch: result=%#v snapshot=%#v", result, s)
	}
	if len(s.PlannerCorrections) != 2 || s.RepairAttempts != 0 || s.Workspace != nil || s.MachineApproval != nil {
		t.Fatal("malformed planner exhausted the wrong budget or advanced into implementation")
	}

	// The CLI resolves an omitted RUN only from a validated journal bound to
	// this repository, then resumes the same blocked run without losing ID.
	var resumeOut bytes.Buffer
	err = Execute(context.Background(), []string{"resume", "--autonomous"}, root, &resumeOut)
	if err == nil {
		t.Fatal("resume should retain the exhausted semantic-correction blocker")
	}
	var resumed autonomousFailure
	if err := json.Unmarshal(resumeOut.Bytes(), &resumed); err != nil || resumed.RunID != s.RunID || resumed.State != s.State {
		t.Fatalf("resume did not report the same durable run: %#v err=%v", resumed, err)
	}
	priorHead := s.ControllerHead
	s, err = control.Inspect(entries[0])
	if err != nil || s.State != "AWAITING_APPROVAL" || s.Creation.Execution.MaxRepairs != 3 || s.ControllerHead != priorHead || len(s.PlannerCorrections) != 2 {
		t.Fatalf("resume changed the immutable policy or state: %#v, %v", s, err)
	}
}

func TestAutonomousLatestSelectionIgnoresCanonicalProviderSidecars(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "Make a bounded fixture change"}, root, &out); err == nil {
		t.Fatal("fixture planner emits a non-graph response; autonomous run should retain its semantic blocker")
	}
	var blocked autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &blocked); err != nil {
		t.Fatalf("failure omitted sanitized run summary: %s (%v)", out.String(), err)
	}
	runs := filepath.Join(root, ".harness", "runs")
	turnID := strings.Repeat("b", 64)
	invocationID := strings.Repeat("c", 64)
	for _, sidecar := range []string{
		blocked.RunID + ".jsonl.model-access.jsonl",
		blocked.RunID + ".jsonl.planner.opencode-runtime.jsonl",
		blocked.RunID + ".jsonl.planner.provider-gateway.jsonl",
		blocked.RunID + ".jsonl.planner.opencode-runtime.jsonl.state-root.jsonl",
		blocked.RunID + ".jsonl.explorer.turn-" + turnID + ".opencode-runtime.jsonl",
		blocked.RunID + ".jsonl.explorer.turn-" + turnID + ".provider-gateway.jsonl",
		blocked.RunID + ".jsonl.planner.invocation-" + invocationID + ".opencode-runtime.jsonl",
	} {
		if err := os.WriteFile(filepath.Join(runs, sidecar), []byte("separate provider runtime event contract"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if latest, err := latestAutonomousRunID(root); err != nil || latest != blocked.RunID {
		t.Fatalf("autonomous latest selection missed the blocked run: %s %v", latest, err)
	}
	// Omitted-RUN resume still resolves the same blocked run without
	// dispatching new work; the exhausted blocker is retained.
	var resumeOut bytes.Buffer
	if err := Execute(context.Background(), []string{"resume", "--autonomous"}, root, &resumeOut); err == nil {
		t.Fatal("resume should retain the exhausted semantic-correction blocker")
	}
	var resumed autonomousFailure
	if err := json.Unmarshal(resumeOut.Bytes(), &resumed); err != nil || resumed.RunID != blocked.RunID {
		t.Fatalf("omitted-RUN resume did not resolve the blocked run: %#v err=%v", resumed, err)
	}
}

func TestAutonomousPlannerContextOptInIsIndependentOfSchedulerAndRoleContext(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--prepare-only", "--max-parallel", "1", "--planner-context", "source-bounded-v1", "A bounded fixture objective"}, root, &out)
	if err == nil {
		t.Fatal("fake planner's non-graph output should stop qualification without invoking a provider")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable autonomous run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil || policy.PlannerContext != "source-bounded-v1" || policy.Context != "bounded-v1" || policy.MaxParallel != 1 || policy.ParallelImplementationVersion != 0 {
		t.Fatalf("planner context opt-in changed unrelated scheduler or role-context policy: %#v", policy)
	}
}

func TestAutonomousGoSourcePlannerContextRequiresAndBindsPinnedParser(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parserBytes, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(parserBytes)
	parserHash := hex.EncodeToString(sum[:])
	if err := validateAutonomousPlannerContext(autonomousPlannerContextGoSourceV1, parser, parserHash); err != nil {
		t.Fatalf("valid pinned parser binding rejected: %v", err)
	}
	if err := validateAutonomousPlannerContext(autonomousPlannerContextGoSourceV2, parser, parserHash); err != nil {
		t.Fatalf("valid v2 pinned parser binding rejected: %v", err)
	}
	if err := validateAutonomousPlannerContext(autonomousPlannerContextGoContractV1, parser, parserHash); err != nil {
		t.Fatalf("valid contract-context pinned parser binding rejected: %v", err)
	}
	if err := validateAutonomousPlannerContext(autonomousPlannerContextGoContractV2, parser, parserHash); err != nil {
		t.Fatalf("valid contract-context v2 pinned parser binding rejected: %v", err)
	}
	if err := validateAutonomousPlannerContext(autonomousPlannerContextGoContractV3, parser, parserHash); err != nil {
		t.Fatalf("valid contract-context v3 pinned parser binding rejected: %v", err)
	}

	for _, test := range []struct {
		name, mode, path, hash string
	}{
		{name: "go context missing binding", mode: autonomousPlannerContextGoSourceV1},
		{name: "go context v2 missing binding", mode: autonomousPlannerContextGoSourceV2},
		{name: "contract context missing binding", mode: autonomousPlannerContextGoContractV1},
		{name: "contract context v2 missing binding", mode: autonomousPlannerContextGoContractV2},
		{name: "contract context v3 missing binding", mode: autonomousPlannerContextGoContractV3},
		{name: "go context missing hash", mode: autonomousPlannerContextGoSourceV1, path: parser},
		{name: "relative parser path", mode: autonomousPlannerContextGoSourceV1, path: "ri.exe", hash: parserHash},
		{name: "v2 relative parser path", mode: autonomousPlannerContextGoSourceV2, path: "ri.exe", hash: parserHash},
		{name: "contract relative parser path", mode: autonomousPlannerContextGoContractV1, path: "ri.exe", hash: parserHash},
		{name: "contract v2 relative parser path", mode: autonomousPlannerContextGoContractV2, path: "ri.exe", hash: parserHash},
		{name: "contract v3 relative parser path", mode: autonomousPlannerContextGoContractV3, path: "ri.exe", hash: parserHash},
		{name: "unclean parser path", mode: autonomousPlannerContextGoSourceV1, path: filepath.Dir(parser) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(parser), hash: parserHash},
		{name: "uppercase hash", mode: autonomousPlannerContextGoSourceV1, path: parser, hash: strings.ToUpper(parserHash)},
		{name: "binding with legacy mode", mode: autonomousPlannerContextSourceBoundedV1, path: parser, hash: parserHash},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAutonomousPlannerContext(test.mode, test.path, test.hash); err == nil {
				t.Fatal("invalid planner context binding was accepted")
			}
		})
	}

	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoSourceV1,
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"A bounded fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("fixture should stop before external role dispatch")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("failure did not identify the locally prepared run: %s (%v)", out.String(), decodeErr)
	}
	s, err := control.Inspect(filepath.Join(root, ".harness", "runs", failure.RunID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil ||
		policy.PlannerContext != autonomousPlannerContextGoSourceV1 ||
		policy.PlannerParseCacheVersion != 0 ||
		policy.PlannerContextRIExecutable != parser ||
		policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("explicit parser provenance was not bound unchanged in run creation: %#v", policy)
	}
	policyBytes, err := canonical.Bytes(policy)
	if err != nil || strings.Contains(string(policyBytes), "planner_parse_cache_version") {
		t.Fatalf("disabled parser cache changed the legacy policy encoding: %s (%v)", policyBytes, err)
	}

	v2Root := autonomousCLIFixture(t)
	var v2Out bytes.Buffer
	v2Err := Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoSourceV2,
		"--planner-context-parse-cache",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"A bounded v2 fixture objective",
	}, v2Root, &v2Out)
	if v2Err == nil {
		t.Fatal("fixture should stop before external role dispatch")
	}
	var v2Failure autonomousFailure
	if decodeErr := json.Unmarshal(v2Out.Bytes(), &v2Failure); decodeErr != nil || v2Failure.RunID == "" {
		t.Fatalf("v2 failure did not identify the locally prepared run: %s (%v)", v2Out.String(), decodeErr)
	}
	v2Snapshot, err := control.Inspect(filepath.Join(v2Root, ".harness", "runs", v2Failure.RunID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	v2Policy := v2Snapshot.Creation.Execution
	if v2Policy == nil || v2Policy.PlannerContext != autonomousPlannerContextGoSourceV2 || v2Policy.PlannerParseCacheVersion != 1 || v2Policy.PlannerContextRIExecutable != parser || v2Policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("v2 parser provenance was not bound unchanged in run creation: %#v", v2Policy)
	}
}

func TestAutonomousContractPlannerContextV3BindsNewRecordMode(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoContractV3,
		"--planner-context-parse-cache",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"--review-impact-context", "--review-impact-candidate-facts-cache",
		"A bounded contract v3 fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("test executable unexpectedly satisfied the RI protocol")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("contract v3 admission failure did not identify the prepared run: %s (%v)", out.String(), decodeErr)
	}
	snapshot, err := control.Inspect(filepath.Join(root, ".harness", "runs", failure.RunID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	policy := snapshot.Creation.Execution
	if policy == nil || policy.PlannerContext != autonomousPlannerContextGoContractV3 || policy.PlannerParseCacheVersion != 1 ||
		policy.ReviewImpactContextVersion != 1 || policy.CandidateFactsCacheVersion != 1 ||
		policy.PlannerContextRIExecutable != parser || policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("contract v3/cache/review/parser policy was not immutably bound: %#v", policy)
	}
}

func TestAutonomousContractPlannerContextBindsSeparateOptIn(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoContractV1,
		"--planner-context-parse-cache",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", hex.EncodeToString(sum[:]),
		"A bounded contract fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("fixture should stop before external role dispatch")
	}
	var result autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	path, err := runPath(root, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := snapshot.Creation.Execution
	if policy == nil || policy.PlannerContext != autonomousPlannerContextGoContractV1 || policy.PlannerParseCacheVersion != 1 || policy.PlannerContextRIExecutable != parser || policy.PlannerContextRIExecutableSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("contract planner policy was not immutably bound: %#v", policy)
	}
}

func TestAutonomousContractPlannerContextV2AllowsReviewAndParseCacheOptIns(t *testing.T) {
	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoContractV2,
		"--planner-context-parse-cache",
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"--review-impact-context", "--review-impact-candidate-facts-cache",
		"A bounded contract v2 review fixture objective",
	}, root, &out)
	if err == nil {
		t.Fatal("test executable unexpectedly satisfied the RI protocol")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("contract v2 admission failure did not identify the prepared run: %s (%v)", out.String(), decodeErr)
	}
	path, err := runPath(root, failure.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := snapshot.Creation.Execution
	if policy == nil || policy.PlannerContext != autonomousPlannerContextGoContractV2 || policy.PlannerParseCacheVersion != 1 ||
		policy.ReviewImpactContextVersion != 1 || policy.CandidateFactsCacheVersion != 1 ||
		policy.PlannerContextRIExecutable != parser || policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("contract v2/review/cache/parser policy was not immutably bound: %#v", policy)
	}
}

func TestAutonomousReviewImpactContextRequiresAndBindsPinnedContractMode(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--autonomous", "--review-impact-candidate-facts-cache", "objective"},
		{"run", "--autonomous", "--review-impact-context", "objective"},
		{"run", "--autonomous", "--review-impact-context", "--planner-context", autonomousPlannerContextGoSourceV1, "objective"},
		{"run", "--autonomous", "--review-impact-context", "--planner-context", autonomousPlannerContextGoContractV1, "objective"},
		{"run", "--autonomous", "--review-impact-context", "--planner-context", autonomousPlannerContextGoContractV2, "objective"},
	} {
		root := autonomousCLIFixture(t)
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err == nil {
			t.Fatalf("invalid review-impact invocation accepted: %v", args)
		}
		entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid review-impact invocation created a run: %v (%v)", entries, err)
		}
	}

	parser, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(parser)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	parserHash := hex.EncodeToString(sum[:])
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err = Execute(context.Background(), []string{
		"run", "--autonomous", "--prepare-only", "--max-parallel", "1",
		"--planner-context", autonomousPlannerContextGoContractV1,
		"--planner-context-ri-executable", parser,
		"--planner-context-ri-executable-sha256", parserHash,
		"--review-impact-context", "--review-impact-candidate-facts-cache", "A bounded contract review objective",
	}, root, &out)
	if err == nil {
		t.Fatal("test executable unexpectedly satisfied the RI protocol")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("failed local parser admission did not identify its run: %s (%v)", out.Bytes(), decodeErr)
	}
	path, err := runPath(root, failure.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := snapshot.Creation.Execution
	if policy == nil || policy.ReviewImpactContextVersion != 1 || policy.CandidateFactsCacheVersion != 1 || policy.PlannerContext != autonomousPlannerContextGoContractV1 || policy.PlannerContextRIExecutable != parser || policy.PlannerContextRIExecutableSHA256 != parserHash {
		t.Fatalf("review impact mode/parser were not bound in run creation: %#v", policy)
	}
}

func TestPlannerParseCacheFlagIsContextVersioned(t *testing.T) {
	for _, test := range []struct {
		mode    string
		version int
		wantErr bool
	}{
		{mode: "", version: 0},
		{mode: autonomousPlannerContextGoSourceV1, version: 0},
		{mode: autonomousPlannerContextGoSourceV2, version: 0},
		{mode: autonomousPlannerContextGoSourceV2, version: 1},
		{mode: autonomousPlannerContextGoContractV1, version: 1},
		{mode: autonomousPlannerContextGoContractV2, version: 1},
		{mode: autonomousPlannerContextGoSourceV1, version: 1, wantErr: true},
		{mode: autonomousPlannerContextSourceBoundedV1, version: 1, wantErr: true},
		{mode: autonomousPlannerContextGoSourceV2, version: 2, wantErr: true},
	} {
		err := validatePlannerParseCacheVersion(test.mode, test.version)
		if (err != nil) != test.wantErr {
			t.Fatalf("unexpected parse-cache validation for mode=%q version=%d: %v", test.mode, test.version, err)
		}
	}
}

func TestAutonomousPromptRecipeIsExplicitlyBoundInCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--prepare-only", "--max-parallel", "1", "--prompt-recipe", "cache-prefix-v1", "A bounded fixture objective"}, root, &out)
	if err == nil {
		t.Fatal("fake planner's non-graph output should stop qualification without invoking a provider")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable autonomous run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Execution == nil || s.Creation.Execution.PromptRecipe != "cache-prefix-v1" {
		t.Fatalf("prompt recipe was not bound to immutable creation metadata: %#v", s.Creation.Execution)
	}
}

func TestAutonomousRepairIntelligenceIsExplicitlyBoundInCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--prepare-only", "--repair-intelligence", "A bounded fixture objective"}, root, &out)
	if err == nil {
		t.Fatal("fake non-graph planner should stop before provider dispatch")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatal("missing immutable run", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil || s.Creation.Execution.RepairIntelligenceVersion != 1 {
		t.Fatal("repair intelligence was not bound at creation", err)
	}
}

func TestAutonomousCLIRejectsInvalidOptionsAndNonAutonomousResume(t *testing.T) {
	root := autonomousCLIFixture(t)
	for _, args := range [][]string{
		{"run", "--autonomous", "--max-repairs", "-1", "objective"},
		{"run", "--autonomous", "--max-repairs", "9", "objective"},
		{"run", "--autonomous", "--max-repairs", "not-a-number", "objective"},
		{"run", "--autonomous", "--max-parallel", "0", "objective"},
		{"run", "--autonomous", "--max-parallel", "9", "objective"},
		{"run", "--autonomous", "--max-parallel", "not-a-number", "objective"},
		{"run", "--autonomous", "--planner-context", "unsupported", "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextGoSourceV1, "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextGoContractV2, "objective"},
		{"run", "--autonomous", "--planner-context-parse-cache", "objective"},
		{"run", "--autonomous", "--planner-context", autonomousPlannerContextGoSourceV1, "--planner-context-parse-cache", "objective"},
		{"run", "--autonomous", "--planner-context-ri-executable", "C:\\tools\\ri.exe", "--planner-context-ri-executable-sha256", strings.Repeat("a", 64), "objective"},
		{"run", "--autonomous", "--prompt-recipe", "unsupported", "objective"},
		{"run", "--autonomous", "--auto-compact-token-limit", "0", "objective"},
		{"run", "--autonomous", "--auto-compact-token-limit", "-1", "objective"},
		{"run", "--autonomous", "--auto-compact-token-limit", "10000001", "objective"},
		{"run", "--autonomous"},
		{"resume", "--autonomous", "one", "two"},
	} {
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err == nil {
			t.Errorf("accepted invalid invocation %q", strings.Join(args, " "))
		}
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("invalid autonomous options created a run: %v, %v", entries, err)
	}

	var planOut bytes.Buffer
	if err := Execute(context.Background(), []string{"plan", "ordinary fixture"}, root, &planOut); err != nil {
		t.Fatal(err)
	}
	var ordinary control.Snapshot
	if err := json.Unmarshal(planOut.Bytes(), &ordinary); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"resume", "--autonomous", ordinary.RunID}, root, &out)
	if err == nil || !strings.Contains(err.Error(), "is not an autonomous run") {
		t.Fatalf("autonomous resume accepted an interactive run: %v", err)
	}
}

func TestAutonomousUncertaintySummaryIsSanitized(t *testing.T) {
	path, s, out := planAutonomousFailureFixture(t, "uncertainty fixture")
	secretDetail := "file effect UNKNOWN credential=must-not-be-printed"
	err := reportAutonomousFailure(&out, path, s.RunID, errors.Join(control.ErrAutonomousReconciliation, errors.New(secretDetail)))
	if err == nil {
		t.Fatal("sanitized boundary error missing")
	}
	if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), secretDetail) {
		t.Fatalf("boundary error leaked provider detail: %v", err)
	}
	if !strings.Contains(err.Error(), s.RunID) || !strings.Contains(err.Error(), "effect_requires_reconciliation") {
		t.Fatalf("boundary error lost coarse identity: %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("non-cancellation cause reports as cancellation: %v", err)
	}
	var summary autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatalf("invalid uncertainty summary %q: %v", out.String(), err)
	}
	if summary.RunID != s.RunID || summary.State != s.State || summary.Phase != "approval" || summary.BlockedReason != "effect_requires_reconciliation" || strings.Contains(out.String(), "credential") {
		t.Fatalf("uncertainty summary leaked details or lost state: %#v (%s)", summary, out.String())
	}
}

func TestAutonomousVerificationNotRunSummaryUsesTrustedSentinel(t *testing.T) {
	path, s, out := planAutonomousFailureFixture(t, "verification preflight fixture")
	trustedSecret := "trusted-provider-token-must-not-print"
	cause := errors.Join(errors.New("verification unavailable token="+trustedSecret), control.ErrAutonomousVerificationNotRun)
	err := reportAutonomousFailure(&out, path, s.RunID, cause)
	if err == nil || strings.Contains(err.Error(), trustedSecret) || strings.Contains(out.String(), trustedSecret) {
		t.Fatalf("trusted verification blocker leaked its cause: err=%v output=%s", err, out.String())
	}
	var summary autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &summary); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if summary.BlockedReason != "verification_not_run" {
		t.Fatalf("trusted verification blocker lost stable classification: %#v", summary)
	}

	ordinarySecret := "ordinary-provider-token-must-not-print"
	out.Reset()
	err = reportAutonomousFailure(&out, path, s.RunID, errors.New("verification_not_run unavailable token="+ordinarySecret))
	if err == nil || strings.Contains(err.Error(), ordinarySecret) || strings.Contains(out.String(), ordinarySecret) {
		t.Fatalf("ordinary error leaked its cause: err=%v output=%s", err, out.String())
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &summary); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if summary.BlockedReason != "operator_attention_required" {
		t.Fatalf("ordinary message spoofed trusted verification classification: %#v", summary)
	}
}

func TestAutonomousFailurePreservesCancellationWithoutLeakingDetail(t *testing.T) {
	path, s, out := planAutonomousFailureFixture(t, "cancellation fixture")
	cause := errors.Join(errors.New("provider TOKEN=secret-must-not-print"), context.Canceled)
	err := reportAutonomousFailure(&out, path, s.RunID, cause)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved through the sanitized boundary: %v", err)
	}
	if strings.Contains(err.Error(), "secret-must-not-print") {
		t.Fatalf("cancelled boundary error leaked provider detail: %v", err)
	}
	var summary autonomousFailure
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.BlockedReason != "execution_interrupted" || strings.Contains(out.String(), "secret") {
		t.Fatalf("cancelled summary leaked details or lost reason: %#v (%s)", summary, out.String())
	}
}

func TestAutonomousFailureReportsDispositionWithoutGrantingAcceptance(t *testing.T) {
	path, s, out := planAutonomousFailureFixture(t, "failure disposition fixture")
	for _, tc := range []struct {
		cause       error
		status      string
		disposition control.GateDisposition
		next        string
	}{
		{control.ErrScopeReplanRequired, "NEEDS_REPLAN", control.GateReplan, "request_candidate_bound_scope_replan"},
		{control.ErrAutonomousRepairBudget, "NEEDS_ATTENTION", control.GateAttention, "inspect_candidate_and_remaining_gates"},
		{control.ErrAutonomousReconciliation, "UNKNOWN", control.GateReconcile, "reconcile_existing_effect_without_resend"},
	} {
		out.Reset()
		if err := reportAutonomousFailure(&out, path, s.RunID, tc.cause); err == nil {
			t.Fatal("unaccepted run returned success")
		}
		var summary autonomousFailure
		if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Status != tc.status || summary.Disposition != tc.disposition || summary.NextAction != tc.next || summary.State != s.State || summary.CandidateRetained != (s.Candidate != nil) {
			t.Fatalf("incorrect disposition or durable state: %#v", summary)
		}
	}
}

func TestAutonomousObjectiveValidationRejectsBeforeDurableCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	invalid := []string{"   \n\t ", string([]byte{0xff, 0xfe})}
	for _, objective := range invalid {
		var out bytes.Buffer
		if err := Execute(context.Background(), []string{"run", "--autonomous", objective}, root, &out); err == nil {
			t.Fatalf("accepted invalid objective %q", objective)
		}
	}
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", strings.Repeat("x", (256<<10)+1)}, root, &out); err == nil {
		t.Fatal("accepted oversized objective")
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("invalid autonomous objective created a run: %v, %v", entries, err)
	}
	if err := validateAutonomousObjective("  "); err == nil {
		t.Fatal("whitespace-only objective admitted")
	}
	if err := validateAutonomousObjective(string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 objective admitted")
	}
	if err := validateAutonomousObjective(strings.Repeat("x", (256<<10)+1)); err == nil {
		t.Fatal("oversized objective admitted")
	}
}

func TestAutonomousFileGoalUsesPlanObjectiveConventions(t *testing.T) {
	root := autonomousCLIFixture(t)
	want := "# Obiectiv\r\nPăstrează textul exact.\n"
	goalPath := filepath.Join(root, "goal.md")
	if err := os.WriteFile(goalPath, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--max-repairs", "1", "--file", "goal.md"}, root, &out)
	if err == nil {
		t.Fatal("fixture config has no implementation roles; file-goal run should report its blocker")
	}
	if strings.Contains(err.Error(), want) {
		t.Fatalf("sanitized file-goal error echoed objective detail: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable file-goal run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Objective != want || s.Creation.Execution == nil || s.Creation.Execution.Mode != "autonomous-v1" {
		t.Fatalf("file-goal bytes were not preserved durably: %#v", s.Creation)
	}
	if err := os.WriteFile(goalPath, []byte(" \n"), 0600); err != nil {
		t.Fatal(err)
	}
	var rejected bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--file", "goal.md"}, root, &rejected); err == nil {
		t.Fatal("whitespace-only goal file admitted")
	}
	var both bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--file", "goal.md", "extra"}, root, &both); err == nil {
		t.Fatal("positional objective alongside --file admitted")
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 1 {
		t.Fatalf("invalid file-goal input created a run: %v, %v", entries, err)
	}
}

func TestAutonomousParallelFlagDefaultsSequentialOverride(t *testing.T) {
	root := autonomousCLIFixture(t)
	var seqOut bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--max-parallel", "1", "Sequential fixture"}, root, &seqOut); err == nil {
		t.Fatal("sequential fixture should still report its blocker")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one sequential run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Execution == nil || s.Creation.Execution.GraphVersion != 1 || s.Creation.Execution.MaxParallel != 1 || s.Creation.Execution.RepairPlanningVersion != 1 || s.Creation.Execution.ParallelImplementationVersion != 0 || s.Creation.Config.PlannerContract != "plan-graph-v5" {
		t.Fatalf("sequential override not bound: %#v", s.Creation.Execution)
	}
}

func TestAutonomousParallelWritersOptInBindsV4PolicyAndAllowsSerialScheduler(t *testing.T) {
	root := autonomousCLIFixture(t)
	configPath := filepath.Join(root, "harness.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(content), "\n[planner]", "\nwriter_contract = \"anchored-edits-v1\"\nexplorer_contract = \"json-v2\"\n\n[planner]", 1)
	text += `

[writer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "writer"

[explorer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "explorer"
`
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Execute(context.Background(), []string{"run", "--autonomous", "--parallel-writers", "--max-parallel", "1", "Parallel fixture"}, root, &out)
	if err == nil {
		t.Fatal("deterministic fake planner should not produce a production graph")
	}
	entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one durable opted-in run, entries=%v err=%v", entries, err)
	}
	s, err := control.Inspect(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	policy := s.Creation.Execution
	if policy == nil || policy.ParallelImplementationVersion != 1 || policy.GraphVersion != 1 || policy.RepairPlanningVersion != 1 || policy.Context != "bounded-v1" || policy.MaxParallel != 1 || s.Creation.Config.PlannerContract != "plan-graph-v6" {
		t.Fatalf("parallel writer policy or serial scheduler was not durably bound: %#v, %#v", policy, s.Creation.Config)
	}
}

func TestAutonomousParallelWritersRejectsUnsupportedRoutesBeforeRunCreation(t *testing.T) {
	root := autonomousCLIFixture(t)
	var out bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "--parallel-writers", "Parallel fixture"}, root, &out); err == nil {
		t.Fatal("parallel writer opt-in accepted a config without admitted writer/explorer routes")
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("unsupported parallel writer route created a run: %v, %v", entries, err)
	}
}

func isolatedWriterPolicyJSON() string {
	return `{"version":1,"capacity":{"cpu_milli":2000,"memory_mib":2048,"verification_slots":1,"total_runtime_slots":2,"provider_slots":2,"model_slots":2,"runtime_slots":2},"estimate":{"cpu_milli":500,"memory_mib":512,"verification_slots":0,"runtime_slots":1}}`
}

func configureIsolatedCLIRoutes(t *testing.T, root, controllerStateRoot string) string {
	t.Helper()
	configPath := filepath.Join(root, "harness.toml")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(content), "\n[planner]", "\nwriter_contract = \"anchored-edits-v1\"\nexplorer_contract = \"json-v2\"\n\n[planner]", 1)
	text += `

[writer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "writer"

[explorer]
runtime = "fake"
provider = "deterministic"
model = "fixture-v1"
effort = "none"
role = "explorer"
`
	if controllerStateRoot != "" {
		quoted, err := json.Marshal(controllerStateRoot)
		if err != nil {
			t.Fatal(err)
		}
		text = "controller_state_root = " + string(quoted) + "\n" + text
	}
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestIsolatedWritersFallbackForMissingStateButDenyUnsafeState(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(string) string
	}{
		{name: "missing", root: func(string) string { return "" }},
		{name: "inside checkout", root: func(root string) string { return filepath.Join(root, "controller-state") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := autonomousCLIFixture(t)
			configuredRoot := tc.root(root)
			configureIsolatedCLIRoutes(t, root, configuredRoot)
			policyPath := filepath.Join(root, "isolation-policy.json")
			if err := os.WriteFile(policyPath, []byte(isolatedWriterPolicyJSON()), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err := Execute(context.Background(), []string{"run", "--autonomous", "--isolated-writers", "--isolation-policy", policyPath, "A fixture objective"}, root, &out)
			if configuredRoot == "" {
				entries, globErr := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl"))
				if globErr != nil || len(entries) != 1 {
					t.Fatalf("serial fallback did not create one run: %v, %v", entries, globErr)
				}
				s, inspectErr := control.Inspect(entries[0])
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				p := s.Creation.Execution
				if p.IsolatedImplementationVersion != 0 || p.ParallelImplementationVersion != 0 || len(p.CapabilityFallbacks) != 1 || p.CapabilityFallbacks[0].Reason != "external_state_unavailable" {
					t.Fatalf("missing optional isolation did not bind serial fallback: %#v", p)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "controller_state_root") {
				t.Fatalf("isolated mode did not reject its invalid external state root: %v", err)
			}
			if entries, globErr := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); globErr != nil || len(entries) != 0 {
				t.Fatalf("invalid isolated state root created a run or provider intent: %v, %v", entries, globErr)
			}
			if configuredRoot != "" {
				if _, statErr := os.Stat(configuredRoot); !os.IsNotExist(statErr) {
					t.Fatalf("state-root validation created a directory: %v", statErr)
				}
			}
		})
	}
}

func TestReadIsolatedWriterPolicyStrictVersionedBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "isolation-policy.json")
	if err := os.WriteFile(path, []byte(isolatedWriterPolicyJSON()), 0600); err != nil {
		t.Fatal(err)
	}
	policy, err := readIsolatedWriterPolicy(path)
	if err != nil || policy.Version != 1 || policy.Capacity.ProviderSlots != 2 || policy.Estimate.VerificationSlots != 0 {
		t.Fatalf("valid explicit resource policy rejected: %#v, %v", policy, err)
	}

	for name, raw := range map[string]string{
		"duplicate member": strings.Replace(isolatedWriterPolicyJSON(), `"version":1,`, `"version":1,"version":1,`, 1),
		"unknown member":   strings.Replace(isolatedWriterPolicyJSON(), `"version":1,`, `"version":1,"host_cpu":4,`, 1),
		"missing slot":     strings.Replace(isolatedWriterPolicyJSON(), `,"runtime_slots":2`, ``, 1),
		"null estimate":    strings.Replace(isolatedWriterPolicyJSON(), `"estimate":{"cpu_milli":500,"memory_mib":512,"verification_slots":0,"runtime_slots":1}`, `"estimate":null`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readIsolatedWriterPolicy(path); err == nil {
				t.Fatal("invalid versioned isolation policy accepted")
			}
		})
	}
	if err := os.WriteFile(path, []byte(strings.Replace(isolatedWriterPolicyJSON(), `"version":1`, `"version":2`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIsolatedWriterPolicy(path); err == nil {
		t.Fatal("unsupported isolation policy version accepted")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(isolatedWriterPolicyJSON(), `"runtime_slots":1`, `"runtime_slots":0`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIsolatedWriterPolicy(path); err == nil {
		t.Fatal("zero per-task runtime estimate accepted")
	}
	if _, err := readIsolatedWriterPolicy(t.TempDir()); err == nil {
		t.Fatal("directory accepted as isolation policy")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", isolatedWriterPolicyMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readIsolatedWriterPolicy(path); err == nil {
		t.Fatal("oversized isolation policy accepted")
	}
}

func TestIsolatedWritersRequirePolicyAndRejectParallelWritersCombination(t *testing.T) {
	root := autonomousCLIFixture(t)
	policyPath := filepath.Join(root, "isolation-policy.json")
	if err := os.WriteFile(policyPath, []byte(isolatedWriterPolicyJSON()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", "--autonomous", "--isolated-writers", "objective"},
		{"run", "--autonomous", "--isolation-policy", policyPath, "objective"},
		{"run", "--autonomous", "--parallel-writers", "--isolated-writers", "--isolation-policy", policyPath, "objective"},
	} {
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err == nil {
			t.Errorf("accepted invalid isolated-writer invocation %q", strings.Join(args, " "))
		}
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("invalid isolated-writer arguments created runs: %v, %v", entries, err)
	}
}

func TestCohortSelectorRequiresStagedIsolation(t *testing.T) {
	if version, err := validateAutonomousCohortSelector("", false); err != nil || version != 0 {
		t.Fatalf("empty selector rejected: %d %v", version, err)
	}
	if version, err := validateAutonomousCohortSelector("lexicographic-v1", true); err != nil || version != 1 {
		t.Fatalf("staged lexicographic selector rejected: %d %v", version, err)
	}
	for _, tc := range []struct {
		name   string
		mode   string
		staged bool
	}{
		{"unknown", "lexicographic-v2", true},
		{"unversioned", "lexicographic", true},
		{"greedy_name", "greedy-v1", true},
		{"non_staged", "lexicographic-v1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := validateAutonomousCohortSelector(tc.mode, tc.staged); err == nil {
				t.Fatalf("invalid cohort selector %q admitted (staged=%v)", tc.mode, tc.staged)
			}
		})
	}
	if got := cohortSelectorFlag(1); got != "lexicographic-v1" {
		t.Fatalf("selector version did not render its flag spelling: %q", got)
	}
	if got := cohortSelectorFlag(0); got != "" {
		t.Fatalf("greedy selector changed legacy spelling: %q", got)
	}
	if _, err := validateAutonomousCohortSelector(cohortSelectorFlag(7), true); err == nil {
		t.Fatal("out-of-range selector version admitted")
	}

	root := autonomousCLIFixture(t)
	policyPath := filepath.Join(root, "isolation-policy.json")
	if err := os.WriteFile(policyPath, []byte(isolatedWriterPolicyJSON()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"run", "--autonomous", "--cohort-selector", "lexicographic-v1", "objective"},
		{"run", "--autonomous", "--isolated-writers", "--isolation-policy", policyPath, "--cohort-selector", "lexicographic-v1", "objective"},
		{"run", "--autonomous", "--isolated-writer-waves", "--isolation-policy", policyPath, "--cohort-selector", "lexicographic-v1", "objective"},
		{"run", "--autonomous", "--isolated-writer-staged", "--isolation-policy", policyPath, "--cohort-selector", "lexicographic-v2", "objective"},
		{"run", "--autonomous", "--parallel-writers", "--cohort-selector", "lexicographic-v1", "objective"},
	} {
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err == nil {
			t.Errorf("accepted invalid cohort-selector invocation %q", strings.Join(args, " "))
		}
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); err != nil || len(entries) != 0 {
		t.Fatalf("invalid cohort-selector arguments created runs: %v, %v", entries, err)
	}
}

func TestAutonomousIsolatedWritersBindExplicitCapacityAndDerivedWriterRoute(t *testing.T) {
	root := autonomousCLIFixture(t)
	controllerStateRoot := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-controller-state")
	configureIsolatedCLIRoutes(t, root, controllerStateRoot)
	var err error
	policyPath := filepath.Join(root, "isolation-policy.json")
	if err := os.WriteFile(policyPath, []byte(isolatedWriterPolicyJSON()), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Execute(context.Background(), []string{"run", "--autonomous", "--isolated-writers", "--isolation-policy", policyPath, "--max-parallel", "2", "--prepare-only", "Isolate the initial source tasks"}, root, &out)
	if err == nil {
		t.Fatal("fixture planner should stop before accepting a production task graph")
	}
	var failure autonomousFailure
	if decodeErr := json.Unmarshal(out.Bytes(), &failure); decodeErr != nil || failure.RunID == "" {
		t.Fatalf("isolated run failure omitted durable run identity: output=%s decode=%v execute=%v", out.String(), decodeErr, err)
	}
	cfg, err := configuration(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repository.Discover(context.Background(), root, cfg.Repository)
	if err != nil {
		t.Fatal(err)
	}
	statePaths, err := controllerstate.Resolve(controllerStateRoot, identity)
	if err != nil || !statePaths.External || !strings.HasPrefix(statePaths.Root, controllerStateRoot) {
		t.Fatalf("external controller namespace did not resolve: %#v %v", statePaths, err)
	}
	runPath, err := statePaths.Run(failure.RunID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := control.Inspect(runPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Creation.Config.ControllerStateRoot != controllerStateRoot {
		t.Fatalf("external controller root was not bound in immutable creation config: %q", s.Creation.Config.ControllerStateRoot)
	}
	if _, statErr := os.Stat(filepath.Join(statePaths.Root, "repository.json")); statErr != nil {
		t.Fatalf("external controller namespace was not initialized: %v", statErr)
	}
	if entries, globErr := filepath.Glob(filepath.Join(root, ".harness", "runs", "*.jsonl")); globErr != nil || len(entries) != 0 {
		t.Fatalf("isolated run unexpectedly used repository-local run storage: %v, %v", entries, globErr)
	}
	execution := s.Creation.Execution
	if execution == nil || execution.IsolatedImplementationVersion != 1 || execution.ParallelImplementationVersion != 0 || execution.MaxParallel != 2 || execution.IsolationCapacity == nil || execution.IsolationEstimate == nil {
		t.Fatalf("isolated resource policy not durably bound: %#v", execution)
	}
	capacity := execution.IsolationCapacity
	if capacity.CPUMilli != 2000 || capacity.MemoryMiB != 2048 || capacity.VerificationSlots != 1 || capacity.TotalRuntimeSlots != 2 || len(capacity.ProviderSlots) != 1 || len(capacity.ModelSlots) != 1 || len(capacity.RuntimeSlots) != 1 {
		t.Fatalf("capacity fields differ from explicit file: %#v", capacity)
	}
	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *s.Creation.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	route := capacity.RuntimeSlots[0].Runtime
	if capacity.ProviderSlots[0].Provider != s.Creation.Config.Writer.Provider || capacity.ProviderSlots[0].Slots != 2 || capacity.ModelSlots[0].Model.Provider != s.Creation.Config.Writer.Provider || capacity.ModelSlots[0].Model.Model != s.Creation.Config.Writer.Model || route.ProfileID != profileID || route.Provider != s.Creation.Config.Writer.Provider || route.Model != s.Creation.Config.Writer.Model || capacity.RuntimeSlots[0].Slots != 2 {
		t.Fatalf("capacity was not bound to the configured writer route: %#v", capacity)
	}
	if execution.IsolationEstimate.CPUMilli != 500 || execution.IsolationEstimate.MemoryMiB != 512 || execution.IsolationEstimate.VerificationSlots != 0 || execution.IsolationEstimate.RuntimeSlots != 1 {
		t.Fatalf("per-writer estimate differs from explicit file: %#v", execution.IsolationEstimate)
	}
	if s.Creation.Config.PlannerContract != "plan-graph-v7" {
		t.Fatalf("isolated mode did not bind the multi-implementation graph contract: %q", s.Creation.Config.PlannerContract)
	}

	// Reuse the recorded fake planner invocation identity in a fresh run, but
	// replace its deliberately invalid fixture text with a valid direct graph.
	// This drives the real controller cohort admission against the route derived
	// by the CLI without contacting a provider or creating writer worktrees.
	creation := s.Creation
	creation.Nonce = "cli-isolation-cohort-route-regression"
	cloneID, err := canonical.Hash("harness.run.v1", creation)
	if err != nil {
		t.Fatal(err)
	}
	clonePath, err := initializeRunPath(root, cloneID, creation.Config, creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(clonePath, "run.created", creation); err != nil {
		t.Fatal(err)
	}
	if err := control.Append(clonePath, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	plannerResult := *s.Plan
	if len(s.PlannerCorrections) != 0 {
		// The clone starts before correction admission, so use the original
		// identity rather than a successor bound to the first run's receipt.
		plannerResult = s.PlannerCorrections[0].Predecessor
	}
	plannerResult.Output = `{"version":1,"mode":"direct","summary":"isolated route regression","tasks":[{"id":"impl","kind":"implementation","title":"change fixture","scope_paths":["source.txt"],"write_paths":["source.txt"],"expected_evidence":[{"kind":"file","description":"source change"}],"estimated_seconds":10}]}`
	if err := control.Append(clonePath, "plan.recorded", plannerResult); err != nil {
		t.Fatal(err)
	}
	preparedSnapshot, err := control.PrepareAutonomous(context.Background(), clonePath)
	if err != nil {
		t.Fatalf("could not prepare the fixture graph: %v", err)
	}
	cohort, err := control.PrepareGraphIsolationCohort(context.Background(), clonePath)
	if err != nil {
		t.Fatalf("CLI-derived resource route did not admit the graph cohort: %v", err)
	}
	if len(cohort.Demands) != 1 || len(cohort.SelectedTaskIDs) != 1 || cohort.SelectedTaskIDs[0] != "impl" || cohort.Demands[0].Runtime.ProfileID != profileID || cohort.Demands[0].Runtime.Provider != preparedSnapshot.Creation.Config.Writer.Provider || cohort.Demands[0].Runtime.Model != preparedSnapshot.Creation.Config.Writer.Model {
		t.Fatalf("prepared cohort route differs from CLI-bound writer profile: cohort=%#v writer=%#v", cohort, preparedSnapshot.Creation.Config.Writer)
	}
}

func TestAutonomousResumeWithoutIDSelectsLatestAutonomousRun(t *testing.T) {
	root := autonomousCLIFixture(t)
	var autoOut bytes.Buffer
	if err := Execute(context.Background(), []string{"run", "--autonomous", "Make a bounded fixture change"}, root, &autoOut); err == nil {
		t.Fatal("expected autonomous blocker for fixture config")
	}
	var autoResult autonomousFailure
	if err := json.Unmarshal(autoOut.Bytes(), &autoResult); err != nil {
		t.Fatalf("missing autonomous failure summary: %v", err)
	}
	// A newer interactive run must not divert an omitted autonomous resume.
	var planOut bytes.Buffer
	if err := Execute(context.Background(), []string{"plan", "newer ordinary fixture"}, root, &planOut); err != nil {
		t.Fatal(err)
	}
	var ordinary control.Snapshot
	if err := json.Unmarshal(planOut.Bytes(), &ordinary); err != nil {
		t.Fatal(err)
	}
	if ordinary.RunID == autoResult.RunID {
		t.Fatal("fixture reused the autonomous run identity")
	}
	var resumeOut bytes.Buffer
	err := Execute(context.Background(), []string{"resume", "--autonomous"}, root, &resumeOut)
	if err == nil {
		t.Fatal("resume should retain the missing-role blocker")
	}
	var resumed autonomousFailure
	if err := json.Unmarshal(resumeOut.Bytes(), &resumed); err != nil || resumed.RunID != autoResult.RunID {
		t.Fatalf("resume did not select the latest autonomous run: %#v err=%v", resumed, err)
	}
}
