package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/agentcontext"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

const defaultAutonomousMaxRepairs = 2

const defaultAutonomousMaxParallel = 3

const autonomousPlannerContextSourceBoundedV1 = "source-bounded-v1"

const autonomousPlannerContextGoSourceV1 = "go-source-context-v1"

const autonomousPlannerContextGoSourceV2 = "go-source-context-v2"

const autonomousPlannerContextGoContractV1 = "go-contract-context-v1"

const autonomousPlannerContextGoContractV2 = "go-contract-context-v2"

const autonomousPlannerContextGoContractV3 = "go-contract-context-v3"

const isolatedWriterPolicyMaxBytes = 32 << 10

type isolatedWriterPolicyFile struct {
	Version  int                               `json:"version"`
	Capacity isolatedWriterLimits              `json:"capacity"`
	Estimate control.IsolationEstimateTemplate `json:"estimate"`
}

// These scalar ceilings are expanded to the exact configured writer provider,
// model and runtime keys after configuration is loaded. Callers never provide
// opaque profile IDs or route identities in this policy file.
type isolatedWriterLimits struct {
	CPUMilli          int64 `json:"cpu_milli"`
	MemoryMiB         int64 `json:"memory_mib"`
	VerificationSlots int   `json:"verification_slots"`
	TotalRuntimeSlots int   `json:"total_runtime_slots"`
	ProviderSlots     int   `json:"provider_slots"`
	ModelSlots        int   `json:"model_slots"`
	RuntimeSlots      int   `json:"runtime_slots"`
}

// runCommand retains the original `run RUN` operation and adds the explicit
// autonomous objective form. The latter creates the immutable execution policy
// before any provider dispatch is considered by the controller.
func runCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "--autonomous" {
		if len(args) != 1 {
			return errors.New("run requires one RUN or --autonomous OBJECTIVE")
		}
		return legacyRunCommand(ctx, root, args[0], out)
	}
	return autonomousRunCommand(ctx, root, args[1:], out)
}

func legacyRunCommand(ctx context.Context, root, id string, out io.Writer) error {
	p, err := runPath(root, id)
	if err != nil {
		return err
	}
	bound, err := control.Inspect(p)
	if err != nil {
		return err
	}
	if err := requireRunBinding(bound, root, id); err != nil {
		return err
	}
	s, err := control.StartWorkspace(ctx, p)
	if err != nil {
		return err
	}
	return output(out, s)
}

func autonomousRunCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run --autonomous", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	preset := fs.String("preset", "", "execution preset: standard, fast, or thorough")
	maxRepairs := fs.Int("max-repairs", defaultAutonomousMaxRepairs, "maximum bounded repair attempts")
	maxParallel := fs.Int("max-parallel", defaultAutonomousMaxParallel, "maximum bounded parallel task workers (1 sequential, 2..8 parallel)")
	parallelWriters := fs.Bool("parallel-writers", false, "allow two independent initial implementation tasks when justified")
	isolatedWriters := fs.Bool("isolated-writers", false, "run an explicitly resource-bounded initial implementation cohort in separate worktrees")
	isolatedWriterWaves := fs.Bool("isolated-writer-waves", false, "run resource-bounded initial writer waves on one pristine parent, aggregating once after all proposals")
	isolatedWriterStaged := fs.Bool("isolated-writer-staged", false, "run dependent hub-to-leaf staged isolated cohorts, each forked from its exact parent candidate with one aggregate per stage")
	isolationPolicyPath := fs.String("isolation-policy", "", "strict versioned JSON resource capacity and per-writer estimate file (required with --isolated-writers, --isolated-writer-waves or --isolated-writer-staged)")
	cohortSelector := fs.String("cohort-selector", "", "staged wave selector: empty (frozen greedy derivation), lexicographic-v1 (exact finite optimum over the same hard gates), coupling-aware-v1 (typed C1-C4 optimum with C4 hard gate; requires --isolated-writer-staged) or observed-coupling-v1 (planner advisory merged with source-observed C4/C2 at MAX severity; requires --isolated-writer-staged with optional Go planner context, absent RI falls back to advisory)")
	plannerContext := fs.String("planner-context", "", "planner evidence mode: source-bounded-v1 or pinned go-source-context-v1/go-source-context-v2/go-contract-context-v1/go-contract-context-v2/go-contract-context-v3")
	agentContext := fs.Bool("agent-context", true, "bind committed scope-aware AGENTS.md and role-aware skill workflows to the new run")
	workingContext := fs.Bool("working-context", false, "experimentally enable bounded editable context for dynamic explorer follow-ups")
	dynamicExplorers := fs.Bool("dynamic-explorers", false, "enable dynamic Codex explorer turns independently of experimental retention")
	repairIntelligence := fs.Bool("repair-intelligence", false, "bind structured repair evidence from admitted task context to new graph repair invocations")
	plannerContextRIExecutable := fs.String("planner-context-ri-executable", "", "absolute path to the pinned Go RI parser (required for Go source/contract contexts)")
	plannerContextRIExecutableSHA256 := fs.String("planner-context-ri-executable-sha256", "", "lowercase SHA-256 of the pinned Go-source RI parser")
	plannerContextParseCache := fs.Bool("planner-context-parse-cache", false, "reuse local Go parser facts for go-source-context-v2 or go-contract-context-v1/v2/v3")
	plannerPPR := fs.Bool("planner-ppr", false, "opt go-source-context-v2 planning into bounded Personalized PageRank hints over the admitted graph")
	reviewImpactContext := fs.Bool("review-impact-context", false, "attach bounded candidate Go topology to reviews (requires go-contract-context-v1/v2/v3 and pinned RI)")
	candidateFactsCache := fs.Bool("review-impact-candidate-facts-cache", false, "reuse local candidate Go syntax facts during review-impact collection")
	contextSelector := fs.String("context-selector", "", "task context selector: empty (legacy default) or rrf-coverage-v1 (experimental RRF plus coverage; requires bounded-v1)")
	promptRecipe := fs.String("prompt-recipe", "", "opt in to cache-prefix-v1 prompt ordering")
	autoCompactTokenLimit := fs.Int64("auto-compact-token-limit", 0, "opt in to Codex automatic in-turn compaction at this positive token threshold")
	prepareOnly := fs.Bool("prepare-only", false, "accept the graph and confirm its workspace, then return before explorer or writer dispatch")
	inspectPlan := fs.Bool("inspect-plan", false, "show effective options without creating a run or calling providers")
	evidencePolicyPath := fs.String("evidence-policy", "", "strict versioned finite EVC source acquisition template for one automatic serial-graph acquisition before the initial writer")
	goalFile := fs.String("file", "", "read objective from file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *preset {
	case "", "standard":
	case "fast":
		*maxRepairs = 1
		*maxParallel = 1
	case "thorough":
		*repairIntelligence = true
		if *maxRepairs < 3 {
			*maxRepairs = 3
		}
	default:
		return fmt.Errorf("unknown preset %q (use standard, fast, or thorough)", *preset)
	}
	if *workingContext {
		*dynamicExplorers = true
	}
	autoCompactFlagSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "auto-compact-token-limit" {
			autoCompactFlagSet = true
		}
	})
	if autoCompactFlagSet && (*autoCompactTokenLimit <= 0 || *autoCompactTokenLimit > runtime.MaxCodexAutoCompactTokenLimit) {
		return fmt.Errorf("auto-compact-token-limit must be between 1 and %d", runtime.MaxCodexAutoCompactTokenLimit)
	}
	if *maxRepairs < 0 || *maxRepairs > 8 {
		return errors.New("max-repairs must be between 0 and 8")
	}
	if *maxParallel < 1 || *maxParallel > 8 {
		return errors.New("max-parallel must be between 1 and 8")
	}
	if *parallelWriters && (*isolatedWriters || *isolatedWriterWaves || *isolatedWriterStaged) {
		return errors.New("parallel-writers and isolated-writers are mutually exclusive")
	}
	if (*isolatedWriters && *isolatedWriterWaves) || (*isolatedWriters && *isolatedWriterStaged) || (*isolatedWriterWaves && *isolatedWriterStaged) {
		return errors.New("isolated-writers and isolated-writer-waves are mutually exclusive")
	}
	if (*isolatedWriters || *isolatedWriterWaves || *isolatedWriterStaged) != (*isolationPolicyPath != "") {
		return errors.New("isolated-writers requires exactly one --isolation-policy PATH")
	}
	cohortSelectorVersion := 0
	switch *cohortSelector {
	case "":
	case autonomousCohortSelectorLexicographicV1:
		cohortSelectorVersion = 1
	case autonomousCohortSelectorCouplingAwareV1:
		cohortSelectorVersion = 2
	case autonomousCohortSelectorObservedCouplingV1:
		cohortSelectorVersion = 3
	default:
		return errors.New("cohort-selector must be empty, lexicographic-v1, coupling-aware-v1 or observed-coupling-v1")
	}
	if cohortSelectorVersion != 0 && !*isolatedWriterStaged {
		return errors.New("cohort-selector lexicographic-v1, coupling-aware-v1 or observed-coupling-v1 requires --isolated-writer-staged with --isolation-policy PATH")
	}
	var isolationPolicy *isolatedWriterPolicyFile
	if *isolatedWriters || *isolatedWriterWaves || *isolatedWriterStaged {
		policy, err := readIsolatedWriterPolicy(*isolationPolicyPath)
		if err != nil {
			return err
		}
		isolationPolicy = &policy
	}
	var evidencePolicy *control.EvidenceAutoPolicy
	if *evidencePolicyPath != "" {
		policy, err := readEvidenceAutoPolicy(*evidencePolicyPath)
		if err != nil {
			return err
		}
		if *parallelWriters || *isolatedWriters || *isolatedWriterWaves || *isolatedWriterStaged {
			return errors.New("evidence-policy requires serial graph writers; parallel-writers and isolated-writers are incompatible")
		}
		evidencePolicy = &policy
	}
	if err := validateAutonomousPlannerContext(*plannerContext, *plannerContextRIExecutable, *plannerContextRIExecutableSHA256); err != nil {
		return err
	}
	reviewImpactContextVersion := 0
	if *reviewImpactContext {
		if *plannerContext != autonomousPlannerContextGoContractV1 && *plannerContext != autonomousPlannerContextGoContractV2 && *plannerContext != autonomousPlannerContextGoContractV3 {
			return errors.New("review-impact-context requires --planner-context go-contract-context-v1, go-contract-context-v2, or go-contract-context-v3 and its pinned RI binding")
		}
		reviewImpactContextVersion = 1
	}
	candidateFactsCacheVersion := 0
	if *candidateFactsCache {
		if reviewImpactContextVersion != 1 {
			return errors.New("review-impact-candidate-facts-cache requires --review-impact-context")
		}
		candidateFactsCacheVersion = 1
	}
	plannerParseCacheVersion := 0
	if *plannerContextParseCache {
		if err := validatePlannerParseCacheVersion(*plannerContext, 1); err != nil {
			return err
		}
		plannerParseCacheVersion = 1
	}
	plannerPPRVersion := 0
	if *plannerPPR {
		if err := validatePlannerPPRVersion(*plannerContext, 1); err != nil {
			return err
		}
		plannerPPRVersion = 1
	}
	if *promptRecipe != "" && *promptRecipe != "cache-prefix-v1" {
		return errors.New("prompt-recipe must be empty or cache-prefix-v1")
	}
	if err := validateAutonomousContextSelector(*contextSelector); err != nil {
		return err
	}
	if *inspectPlan {
		if fs.NArg() != 0 || *goalFile != "" {
			return errors.New("inspect-plan takes run options without an objective")
		}
		options := autonomousCapabilities{parallel: *parallelWriters, isolation: isolationPolicy, isolationWaves: *isolatedWriterWaves, isolationStaged: *isolatedWriterStaged, cohortSelector: cohortSelectorVersion, agentContext: *agentContext, workingContext: *workingContext, dynamicExplorers: *dynamicExplorers,
			plannerContext: *plannerContext, parser: *plannerContextRIExecutable, parserHash: *plannerContextRIExecutableSHA256,
			parseCache: plannerParseCacheVersion, plannerPPR: plannerPPRVersion, reviewImpact: reviewImpactContextVersion, candidateCache: candidateFactsCacheVersion, autoCompact: *autoCompactTokenLimit, evidence: evidencePolicy, contextSelector: *contextSelector}
		return inspectAutonomousPlan(ctx, root, options, *maxParallel, out)
	}
	if *goalFile != "" {
		if fs.NArg() != 0 {
			return errors.New("run --autonomous --file takes no positional objective")
		}
		objective, err := planObjective(ctx, root, []string{"--file", *goalFile})
		if err != nil {
			return err
		}
		if err := validateAutonomousObjective(objective); err != nil {
			return err
		}
		return createAndRunAutonomous(ctx, root, objective, *maxRepairs, *maxParallel, *parallelWriters, isolationPolicy, *isolatedWriterWaves, *isolatedWriterStaged, cohortSelectorVersion, *plannerContext, plannerParseCacheVersion, plannerPPRVersion, reviewImpactContextVersion, candidateFactsCacheVersion, *plannerContextRIExecutable, *plannerContextRIExecutableSHA256, *promptRecipe, *autoCompactTokenLimit, *prepareOnly, *repairIntelligence, out, *agentContext, *workingContext, *dynamicExplorers, evidencePolicy, *contextSelector)
	}
	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return errors.New("run --autonomous requires one objective or --file PATH")
	}
	if err := validateAutonomousObjective(fs.Arg(0)); err != nil {
		return err
	}
	return createAndRunAutonomous(ctx, root, fs.Arg(0), *maxRepairs, *maxParallel, *parallelWriters, isolationPolicy, *isolatedWriterWaves, *isolatedWriterStaged, cohortSelectorVersion, *plannerContext, plannerParseCacheVersion, plannerPPRVersion, reviewImpactContextVersion, candidateFactsCacheVersion, *plannerContextRIExecutable, *plannerContextRIExecutableSHA256, *promptRecipe, *autoCompactTokenLimit, *prepareOnly, *repairIntelligence, out, *agentContext, *workingContext, *dynamicExplorers, evidencePolicy, *contextSelector)
}

func validatePlannerParseCacheVersion(plannerContext string, version int) error {
	if version == 0 {
		return nil
	}
	if version != 1 || plannerContext != autonomousPlannerContextGoSourceV2 && plannerContext != autonomousPlannerContextGoContractV1 && plannerContext != autonomousPlannerContextGoContractV2 && plannerContext != autonomousPlannerContextGoContractV3 {
		return errors.New("planner-context-parse-cache requires go-source-context-v2 or go-contract-context-v1/v2/v3")
	}
	return nil
}

// validatePlannerPPRVersion admits only the narrow PPR seam: version 1
// requires go-source-context-v2 (with its pinned RI binding validated
// separately). Zero preserves every historical planner input.
func validatePlannerPPRVersion(plannerContext string, version int) error {
	if version == 0 {
		return nil
	}
	if version != 1 || plannerContext != autonomousPlannerContextGoSourceV2 {
		return errors.New("planner-ppr requires --planner-context go-source-context-v2 and its pinned RI binding")
	}
	return nil
}

func readIsolatedWriterPolicy(path string) (isolatedWriterPolicyFile, error) {
	var policy isolatedWriterPolicyFile
	file, err := os.Open(path)
	if err != nil {
		return policy, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return policy, err
	}
	if !info.Mode().IsRegular() || info.Size() > isolatedWriterPolicyMaxBytes {
		return policy, errors.New("isolation policy must be a regular JSON file no larger than 32 KiB")
	}
	raw, err := io.ReadAll(io.LimitReader(file, isolatedWriterPolicyMaxBytes+1))
	if err != nil || len(raw) > isolatedWriterPolicyMaxBytes {
		return policy, errors.New("isolation policy exceeds 32 KiB or could not be read")
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return policy, fmt.Errorf("invalid isolation policy encoding: %w", err)
	}
	if err := canonical.Decode(normal, &policy); err != nil {
		return policy, fmt.Errorf("invalid isolation policy: %w", err)
	}
	if err := validateIsolatedWriterPolicy(policy); err != nil {
		return policy, err
	}
	return policy, nil
}

func validateIsolatedWriterPolicy(policy isolatedWriterPolicyFile) error {
	c := policy.Capacity
	e := policy.Estimate
	if policy.Version != 1 {
		return errors.New("isolation policy version must be 1")
	}
	if c.CPUMilli < 1 || c.CPUMilli > 1<<20 || c.MemoryMiB < 1 || c.MemoryMiB > 1<<30 ||
		c.VerificationSlots < 1 || c.VerificationSlots > 64 || c.TotalRuntimeSlots < 1 || c.TotalRuntimeSlots > 64 ||
		c.ProviderSlots < 1 || c.ProviderSlots > 64 || c.ModelSlots < 1 || c.ModelSlots > 64 || c.RuntimeSlots < 1 || c.RuntimeSlots > 64 {
		return errors.New("isolation capacity must provide positive bounded CPU, memory, verification, global runtime, provider, model and route limits")
	}
	if e.CPUMilli < 1 || e.CPUMilli > 1<<20 || e.MemoryMiB < 1 || e.MemoryMiB > 1<<30 ||
		e.VerificationSlots < 0 || e.VerificationSlots > 64 || e.RuntimeSlots < 1 || e.RuntimeSlots > 64 {
		return errors.New("per-writer isolation estimate must provide bounded positive CPU, memory and runtime slots; verification slots may be zero")
	}
	return nil
}

func validateAutonomousPlannerContext(mode, executable, executableSHA256 string) error {
	switch mode {
	case "", autonomousPlannerContextSourceBoundedV1:
		if executable != "" || executableSHA256 != "" {
			return errors.New("planner-context RI executable binding requires a Go planner context")
		}
	case autonomousPlannerContextGoSourceV1, autonomousPlannerContextGoSourceV2, autonomousPlannerContextGoContractV1, autonomousPlannerContextGoContractV2, autonomousPlannerContextGoContractV3:
		if executable == "" || executableSHA256 == "" {
			return errors.New("Go planner contexts require planner-context-ri-executable and planner-context-ri-executable-sha256")
		}
		if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
			return errors.New("planner-context-ri-executable must be an absolute clean path")
		}
		if !isLowerSHA256(executableSHA256) {
			return errors.New("planner-context-ri-executable-sha256 must be 64 lowercase hexadecimal characters")
		}
	default:
		return errors.New("planner-context must be empty, source-bounded-v1, go-source-context-v1, go-source-context-v2, go-contract-context-v1, go-contract-context-v2, or go-contract-context-v3")
	}
	return nil
}

const autonomousContextSelectorRRFCoverageV1 = "rrf-coverage-v1"

// autonomousCohortSelectorLexicographicV1 opts staged runs into the exact
// finite lexicographic wave optimum over the same hard gates. Empty
// preserves the frozen greedy derivation byte-for-byte.
const autonomousCohortSelectorLexicographicV1 = "lexicographic-v1"

// autonomousCohortSelectorCouplingAwareV1 opts staged runs into the typed
// coupling-aware optimum: C4 hard gate plus C3 risk before admitted count,
// then C2/C1 ordinal minimization before existing critical/resource packing.
// Couplings are planner-declared advisory only; requires plan-graph-v9.
const autonomousCohortSelectorCouplingAwareV1 = "coupling-aware-v1"

// autonomousCohortSelectorObservedCouplingV1 opts staged runs into the
// source-observed coupling optimum: planner advisory couplings merged with
// the controller-derived source-observed set (C4 same generation family,
// C2 same observed package) at MAX severity under plan-graph-v9. Missing or
// unavailable RI degrades to the planner advisory selection with no absence
// claim; invalid or forged bindings fail closed. Absent preserves the frozen
// greedy derivation byte-for-byte.
const autonomousCohortSelectorObservedCouplingV1 = "observed-coupling-v1"

// validateAutonomousCohortSelector rejects unknown staged wave selectors
// before any durable run is created. Empty preserves exact historical
// behavior; only lexicographic-v1, coupling-aware-v1 and observed-coupling-v1
// are admitted.
func validateAutonomousCohortSelector(mode string, staged bool) (int, error) {
	switch mode {
	case "":
		return 0, nil
	case autonomousCohortSelectorLexicographicV1:
		if !staged {
			return 0, errors.New("cohort-selector lexicographic-v1 requires --isolated-writer-staged with --isolation-policy PATH")
		}
		return 1, nil
	case autonomousCohortSelectorCouplingAwareV1:
		if !staged {
			return 0, errors.New("cohort-selector coupling-aware-v1 requires --isolated-writer-staged with --isolation-policy PATH")
		}
		return 2, nil
	case autonomousCohortSelectorObservedCouplingV1:
		if !staged {
			return 0, errors.New("cohort-selector observed-coupling-v1 requires --isolated-writer-staged with --isolation-policy PATH")
		}
		return 3, nil
	default:
		return 0, errors.New("cohort-selector must be empty, lexicographic-v1, coupling-aware-v1 or observed-coupling-v1")
	}
}

// validateAutonomousContextSelector rejects unknown selector modes before any
// durable run is created. Empty preserves exact historical/default behavior;
// only rrf-coverage-v1 is admitted. A malformed flag creates no run.
func validateAutonomousContextSelector(mode string) error {
	switch mode {
	case "", autonomousContextSelectorRRFCoverageV1:
		return nil
	default:
		return errors.New("context-selector must be empty or rrf-coverage-v1")
	}
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

// cohortSelectorFlag renders a resolved selector version back to its flag
// spelling for shared validation. Unknown versions fail closed.
func cohortSelectorFlag(version int) string {
	if version == 1 {
		return autonomousCohortSelectorLexicographicV1
	}
	if version == 2 {
		return autonomousCohortSelectorCouplingAwareV1
	}
	if version == 3 {
		return autonomousCohortSelectorObservedCouplingV1
	}
	if version != 0 {
		return "invalid"
	}
	return ""
}

// validateAutonomousObjective rejects whitespace-only, invalid UTF-8 and
// oversized objectives before any durable run is created. The 256 KiB bound
// and nonempty UTF-8 conventions match planObjective goal-file validation.
func validateAutonomousObjective(objective string) error {
	if strings.TrimSpace(objective) == "" {
		return errors.New("autonomous objective must be nonempty text")
	}
	if !utf8.ValidString(objective) {
		return errors.New("autonomous objective must be valid UTF-8")
	}
	if len(objective) > 256<<10 {
		return errors.New("autonomous objective exceeds 256 KiB")
	}
	return nil
}

func createAndRunAutonomous(ctx context.Context, root, objective string, maxRepairs, maxParallel int, parallelWriters bool, isolationPolicy *isolatedWriterPolicyFile, isolationWaves bool, isolationStaged bool, cohortSelectorVersion int, plannerContext string, plannerParseCacheVersion, plannerPPRVersion, reviewImpactContextVersion, candidateFactsCacheVersion int, plannerContextRIExecutable, plannerContextRIExecutableSHA256, promptRecipe string, autoCompactTokenLimit int64, prepareOnly, repairIntelligence bool, out io.Writer, agentContextEnabled, workingContextEnabled, dynamicExplorersEnabled bool, evidencePolicy *control.EvidenceAutoPolicy, contextSelector string) error {
	if err := validateAutonomousObjective(objective); err != nil {
		return err
	}
	if err := validateAutonomousContextSelector(contextSelector); err != nil {
		return err
	}
	if _, err := validateAutonomousCohortSelector(cohortSelectorFlag(cohortSelectorVersion), isolationStaged); err != nil {
		return err
	}
	if parallelWriters && (isolationPolicy != nil || isolationWaves || isolationStaged) {
		return errors.New("parallel-writers and isolated-writers are mutually exclusive")
	}
	if isolationWaves && isolationStaged {
		return errors.New("isolated-writers and isolated-writer-waves are mutually exclusive")
	}
	if isolationWaves && isolationPolicy == nil {
		return errors.New("isolated-writer-waves requires exactly one --isolation-policy PATH")
	}
	if isolationStaged && isolationPolicy == nil {
		return errors.New("isolated-writer-staged requires exactly one --isolation-policy PATH")
	}
	if evidencePolicy != nil {
		if err := control.ValidateEvidenceAutoPolicyTemplate(*evidencePolicy); err != nil {
			return err
		}
		if parallelWriters || isolationPolicy != nil || isolationWaves || isolationStaged {
			return errors.New("evidence-policy requires serial graph writers; parallel-writers and isolated-writers are incompatible")
		}
	}
	if err := validateAutonomousPlannerContext(plannerContext, plannerContextRIExecutable, plannerContextRIExecutableSHA256); err != nil {
		return err
	}
	if err := validatePlannerParseCacheVersion(plannerContext, plannerParseCacheVersion); err != nil {
		return err
	}
	if err := validatePlannerPPRVersion(plannerContext, plannerPPRVersion); err != nil {
		return err
	}
	if reviewImpactContextVersion != 0 && (reviewImpactContextVersion != 1 || plannerContext != autonomousPlannerContextGoContractV1 && plannerContext != autonomousPlannerContextGoContractV2 && plannerContext != autonomousPlannerContextGoContractV3 || plannerContextRIExecutable == "" || plannerContextRIExecutableSHA256 == "") {
		return errors.New("review impact context requires go-contract-context-v1/v2/v3 and a pinned RI parser")
	}
	if candidateFactsCacheVersion != 0 && (candidateFactsCacheVersion != 1 || reviewImpactContextVersion != 1) {
		return errors.New("review-impact-candidate-facts-cache requires review impact context")
	}
	if promptRecipe != "" && promptRecipe != "cache-prefix-v1" {
		return errors.New("prompt-recipe must be empty or cache-prefix-v1")
	}
	var autoCompact *runtime.CodexAutoCompactOptions
	if autoCompactTokenLimit != 0 {
		autoCompact = &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: autoCompactTokenLimit}
		if err := autoCompact.Validate(); err != nil {
			return err
		}
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	capabilities := autonomousCapabilities{parallel: parallelWriters, isolation: isolationPolicy, isolationWaves: isolationWaves, isolationStaged: isolationStaged, cohortSelector: cohortSelectorVersion, workingContext: workingContextEnabled, dynamicExplorers: dynamicExplorersEnabled,
		plannerContext: plannerContext, parser: plannerContextRIExecutable, parserHash: plannerContextRIExecutableSHA256,
		parseCache: plannerParseCacheVersion, plannerPPR: plannerPPRVersion, reviewImpact: reviewImpactContextVersion, candidateCache: candidateFactsCacheVersion, autoCompact: autoCompactTokenLimit, evidence: evidencePolicy}
	if err := capabilities.resolve(cfg); err != nil {
		return err
	}
	if evidencePolicy != nil {
		if capabilities.parallel || capabilities.isolation != nil || capabilities.isolationWaves || capabilities.isolationStaged {
			return errors.New("evidence-policy requires serial graph writers; capability fallback cannot silently drop this policy")
		}
		if capabilities.evidence == nil {
			return errors.New("evidence-policy was not retained through capability resolution")
		}
	}
	parallelWriters, isolationPolicy, isolationWaves, isolationStaged = capabilities.parallel, capabilities.isolation, capabilities.isolationWaves, capabilities.isolationStaged
	cohortSelectorVersion = capabilities.cohortSelector
	if cohortSelectorVersion != 0 && (!isolationStaged || isolationPolicy == nil) {
		return errors.New("cohort-selector lexicographic-v1, coupling-aware-v1 or observed-coupling-v1 requires --isolated-writer-staged with --isolation-policy PATH; capability fallback cannot silently drop this selector")
	}
	plannerContext, plannerContextRIExecutable, plannerContextRIExecutableSHA256 = capabilities.plannerContext, capabilities.parser, capabilities.parserHash
	plannerParseCacheVersion, plannerPPRVersion, reviewImpactContextVersion, candidateFactsCacheVersion = capabilities.parseCache, capabilities.plannerPPR, capabilities.reviewImpact, capabilities.candidateCache
	if err := validatePlannerPPRVersion(plannerContext, plannerPPRVersion); err != nil {
		return err
	}
	if capabilities.autoCompact == 0 {
		autoCompact = nil
	}
	// Fail fast for selected OpenCode routes when the private state root is
	// missing or unsafe, before creating a durable run or admitting provider
	// work. Resume of existing runs keeps controller-owned recovery semantics.
	if err := requireOpenCodeStateRoot(cfg); err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	if filepath.Clean(identity.Root) != filepath.Clean(root) {
		return errors.New("root must be repository top level")
	}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	// New autonomous runs use hierarchical task graph execution with bounded
	// task context and design-planned repairs. The planner contract reserves a
	// conservative initial implementation scope; repair design may refine only
	// never-started repair writes within that scope. Nil/old policies remain
	// compatible and replay with their historical repair behavior.
	cfg.PlannerContract = "plan-graph-v5"
	parallelImplementationVersion := 0
	isolatedImplementationVersion := 0
	var isolationCapacity *engineeringplan.ResourceCapacity
	var isolationEstimate *control.IsolationEstimateTemplate
	if parallelWriters {
		cfg.PlannerContract = "plan-graph-v6"
		parallelImplementationVersion = 1
		// Validate the opt-in against the configured implementation route before
		// creating a run or dispatching any planner intent.
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	if isolationPolicy != nil {
		if cfg.Writer == nil {
			return errors.New("isolated-writers requires an explicitly configured writer route")
		}
		if cfg.ControllerStateRoot == "" {
			return errors.New("isolated-writers requires an external controller_state_root in harness.toml")
		}
		statePaths, err := controllerstate.Resolve(cfg.ControllerStateRoot, identity)
		if err != nil {
			return fmt.Errorf("isolated-writers requires a valid external controller_state_root: %w", err)
		}
		if !statePaths.External {
			return errors.New("isolated-writers requires an external controller_state_root in harness.toml")
		}
		cfg.PlannerContract = "plan-graph-v7"
		if isolationStaged {
			cfg.PlannerContract = "plan-graph-v8"
			if cohortSelectorVersion == 2 || cohortSelectorVersion == 3 {
				cfg.PlannerContract = "plan-graph-v9"
			}
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		// Keep this identity domain and the complete bound profile in sync with
		// control's admission-time route derivation in graph_isolation.go.
		profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *cfg.Writer)
		if err != nil {
			return err
		}
		route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: cfg.Writer.Provider, Model: cfg.Writer.Model}
		limits := isolationPolicy.Capacity
		isolationCapacity = &engineeringplan.ResourceCapacity{
			CPUMilli: limits.CPUMilli, MemoryMiB: limits.MemoryMiB,
			VerificationSlots: limits.VerificationSlots, TotalRuntimeSlots: limits.TotalRuntimeSlots,
			ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: cfg.Writer.Provider, Slots: limits.ProviderSlots}},
			ModelSlots:    []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: cfg.Writer.Provider, Model: cfg.Writer.Model}, Slots: limits.ModelSlots}},
			RuntimeSlots:  []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: limits.RuntimeSlots}},
		}
		estimate := isolationPolicy.Estimate
		isolationEstimate = &estimate
		isolatedImplementationVersion = 1
		if isolationWaves {
			isolatedImplementationVersion = 2
		}
		if isolationStaged {
			isolatedImplementationVersion = 3
		}
		if cohortSelectorVersion != 0 && isolatedImplementationVersion != 3 {
			return errors.New("cohort-selector lexicographic-v1, coupling-aware-v1 or observed-coupling-v1 requires --isolated-writer-staged with --isolation-policy PATH")
		}
	}
	if cfg.Reviewer != nil {
		cfg.ReviewerContract = "json-v1"
	}
	creation := control.Creation{
		Version:    1,
		Nonce:      hex.EncodeToString(nonce),
		Repository: identity,
		Objective:  objective,
		Config:     cfg,
		Execution: &control.ExecutionPolicy{
			SemanticCorrectionVersion: 1,
			CapabilityFallbacks:       capabilities.fallbacks,
			Mode:                      "autonomous-v1", MaxRepairs: maxRepairs, PromptRecipe: promptRecipe, Context: "bounded-v1",
			ContextSelector: contextSelector,
			PlannerContext:  plannerContext, PlannerContextRIExecutable: plannerContextRIExecutable,
			PlannerContextRIExecutableSHA256: plannerContextRIExecutableSHA256,
			PlannerParseCacheVersion:         plannerParseCacheVersion,
			PlannerPPRVersion:                plannerPPRVersion,
			ReviewImpactContextVersion:       reviewImpactContextVersion,
			CandidateFactsCacheVersion:       candidateFactsCacheVersion,
			GraphVersion:                     1, MaxParallel: maxParallel, RepairPlanningVersion: 1,
			ParallelImplementationVersion: parallelImplementationVersion,
			IsolatedImplementationVersion: isolatedImplementationVersion, IsolationCapacity: isolationCapacity, IsolationEstimate: isolationEstimate,
			IsolationCohortSelectorVersion: cohortSelectorVersion,
			CodexAutoCompact:               autoCompact, EvidencePolicy: capabilities.evidence,
		},
	}
	if workingContextEnabled {
		creation.Config.ExplorerContract = "json-v2"
		creation.Execution.WorkingContextVersion = 1
	}
	if dynamicExplorersEnabled {
		creation.Config.ExplorerContract = "json-v2"
		creation.Execution.ScheduledExplorerDispatchVersion = 1
	}
	if repairIntelligence {
		creation.Execution.RepairIntelligenceVersion = 1
		if creation.Config.Reviewer != nil && creation.Config.ReviewerContract == "json-v1" {
			creation.Execution.ReviewRecheckVersion = 1
		}
	}
	configureAutonomousScopeReplan(&creation)
	if agentContextEnabled {
		creation.AgentContext, err = agentcontext.Capture(ctx, identity)
		if err != nil {
			return fmt.Errorf("capture committed agent context before run creation: %w", err)
		}
	}
	creation, err = bindCurrentHost(ctx, creation)
	if err != nil {
		return err
	}
	id, err := canonical.Hash("harness.run.v1", creation)
	if err != nil {
		return err
	}
	p, err := initializeRunPath(root, id, cfg, identity)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	if err = control.Append(p, "run.created", creation); err != nil {
		return err
	}
	if prepareOnly {
		s, err := control.PrepareAutonomous(ctx, p)
		if err != nil {
			return reportAutonomousFailure(out, p, id, err)
		}
		prepared, err := autonomousPreparedResult(s)
		if err != nil {
			return reportAutonomousFailure(out, p, id, err)
		}
		return output(out, prepared)
	}
	s, err := control.RunAutonomous(ctx, p)
	if err != nil {
		return reportAutonomousFailure(out, p, id, err)
	}
	return output(out, s)
}

func configureAutonomousScopeReplan(creation *control.Creation) {
	if creation.Execution == nil || creation.Config.Explorer == nil || creation.Config.Writer == nil {
		return
	}
	if creation.Execution.IsolatedImplementationVersion == 2 || creation.Execution.IsolatedImplementationVersion == 3 {
		return
	}
	version := 1
	if creation.Execution.ParallelImplementationVersion != 0 || creation.Execution.IsolatedImplementationVersion != 0 {
		version = 2
	}
	creation.Execution.ScopeReplanVersion = version
	creation.Execution.MaxScopeReplans = 2
	creation.Execution.ScopeReplanDesignVersion = version
}

type autonomousPrepared struct {
	Status        string `json:"status"`
	RunID         string `json:"run_id"`
	State         string `json:"state"`
	PlanID        string `json:"plan_id"`
	CandidateID   string `json:"candidate_id"`
	GraphDigest   string `json:"graph_digest"`
	GraphRevision int    `json:"graph_revision"`
}

func autonomousPreparedResult(s control.Snapshot) (autonomousPrepared, error) {
	if s.State != "IMPLEMENTING" || s.Workspace == nil || s.Candidate == nil || s.WorkspaceOutcome != "CONFIRMED" || s.Graph == nil || s.Graph.PlanID != s.PlanID || s.Graph.Revision != 1 {
		return autonomousPrepared{}, errors.New("autonomous preparation receipt is incomplete")
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		return autonomousPrepared{}, err
	}
	return autonomousPrepared{
		Status: "PREPARED", RunID: s.RunID, State: s.State, PlanID: s.PlanID,
		CandidateID: candidateID, GraphDigest: s.Graph.Digest, GraphRevision: s.Graph.Revision,
	}, nil
}

func autonomousResumeCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	for _, arg := range args {
		if arg == "--evidence-policy" || strings.HasPrefix(arg, "--evidence-policy=") {
			return errors.New("evidence-policy cannot be injected or changed on resume; it is frozen at run creation")
		}
	}
	if len(args) > 1 {
		return errors.New("resume --autonomous accepts at most one RUN")
	}
	id := ""
	if len(args) == 1 {
		id = args[0]
	} else {
		var err error
		id, err = latestAutonomousRunID(root)
		if err != nil {
			return err
		}
	}
	p, err := runPath(root, id)
	if err != nil {
		return err
	}
	s, err := control.Inspect(p)
	if err != nil {
		return err
	}
	if err := requireRunBinding(s, root, id); err != nil {
		return err
	}
	if s.Creation.Execution == nil || s.Creation.Execution.Mode != "autonomous-v1" {
		return fmt.Errorf("run %s is not an autonomous run", id)
	}
	s, err = control.RunAutonomous(ctx, p)
	if err != nil {
		return reportAutonomousFailure(out, p, id, err)
	}
	return output(out, s)
}

type autonomousFailure struct {
	RuntimeDiagnostic *opencode.DispatchDiagnostic `json:"runtime_diagnostic,omitempty"`
	Status            string                       `json:"status"`
	Disposition       control.GateDisposition      `json:"disposition"`
	NextAction        string                       `json:"next_action"`
	CandidateRetained bool                         `json:"candidate_retained"`
	RunID             string                       `json:"run_id"`
	State             string                       `json:"state"`
	Phase             string                       `json:"phase"`
	BlockedReason     string                       `json:"blocked_reason"`
}

// reportAutonomousFailure emits only the durable run identity and coarse
// controller state. Provider errors may carry credentials, so the JSON summary
// and the returned boundary error carry only the coarse blocked reason; the
// raw cause is never returned to the main stderr channel. Cancellation still
// reports through errors.Is by wrapping the corresponding sentinel.
func reportAutonomousFailure(out io.Writer, path, fallbackID string, cause error) error {
	if cause == nil {
		cause = errors.New("autonomous failure has no supplied outcome")
	}
	summary := autonomousFailure{RunID: fallbackID, State: "UNKNOWN", Phase: "blocked"}
	outcome := control.ClassifyAutonomousFailure(control.Snapshot{}, cause)
	if s, err := control.Inspect(path); err == nil {
		if s.RunID != "" {
			summary.RunID = s.RunID
		}
		summary.State = s.State
		summary.Phase = autonomousPhase(s.State)
		summary.CandidateRetained = s.Candidate != nil
		outcome = control.ClassifyAutonomousFailure(s, cause)
	} else {
		outcome = control.ClassifyAutonomousFailure(control.Snapshot{}, errors.Join(cause, err))
	}
	summary.RuntimeDiagnostic = opencode.DispatchDiagnosticFromError(cause)
	summary.BlockedReason, summary.Status = outcome.PublicReason(), outcome.Status()
	summary.Disposition, summary.NextAction = outcome.Disposition(), outcome.NextAction()
	boundary := sanitizedAutonomousError(summary, cause)
	if err := output(out, summary); err != nil {
		return errors.Join(boundary, err)
	}
	return boundary
}

// sanitizedAutonomousError binds only coarse durable identity to a useful
// message. It never wraps the raw provider cause, so credential-bearing detail
// cannot reach main stderr through the returned error.
func sanitizedAutonomousError(summary autonomousFailure, cause error) error {
	if errors.Is(cause, context.Canceled) {
		return fmt.Errorf("autonomous run %s blocked in %s (%s): %w", summary.RunID, summary.Phase, summary.BlockedReason, context.Canceled)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("autonomous run %s blocked in %s (%s): %w", summary.RunID, summary.Phase, summary.BlockedReason, context.DeadlineExceeded)
	}
	return fmt.Errorf("autonomous run %s blocked in %s (%s: %s)", summary.RunID, summary.Phase, summary.State, summary.BlockedReason)
}

func autonomousPhase(state string) string {
	switch state {
	case "OBJECTIVE", "PLANNING":
		return "planning"
	case "AWAITING_APPROVAL":
		return "approval"
	case "IMPLEMENTING":
		return "implementation"
	case "VERIFYING":
		return "verification"
	case "REVIEWING":
		return "review"
	case "REPAIRING":
		return "repair"
	case "READY":
		return "ready"
	default:
		return "blocked"
	}
}

func autonomousBlockReason(err error) string {
	if err == nil {
		return "unknown"
	}
	return control.ClassifyAutonomousFailure(control.Snapshot{}, err).PublicReason()
}

func requireRunBinding(s control.Snapshot, root, id string) error {
	if s.RunID != id || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.Join(control.ErrAutonomousUnsafe, errors.New("journal/run repository binding mismatch"))
	}
	return nil
}
