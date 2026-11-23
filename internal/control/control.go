package control

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agentcontext"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/effects"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// Creation binds immutable inputs. Nonce distinguishes repeated human objectives.
type Creation struct {
	Version       int                 `json:"version"`
	Nonce         string              `json:"nonce"`
	Repository    repository.Identity `json:"repository"`
	Objective     string              `json:"objective"`
	Config        config.Config       `json:"config"`
	HostAdmission *HostAdmission      `json:"host_admission,omitempty"`
	// Execution is absent for the interactive v1 workflow. When present it is
	// immutable run input which allows the controller to make the narrowly
	// defined machine approvals below.
	Execution *ExecutionPolicy `json:"execution,omitempty"`
	// AgentContext retains exact committed guidance for new role invocations.
	// Absence preserves historical inputs and replay without filesystem reads.
	AgentContext *agentcontext.Bundle `json:"agent_context,omitempty"`
}

// ExecutionPolicy is an explicit, immutable opt-in to the bounded autonomous
// workflow. It never grants authority for network publication or other
// externally visible effects; its only machine authorization is an exact plan
// and candidate-bound regular-file proposal.
//
// Context is an optional immutable task-context mode. Empty preserves the
// legacy autonomous workflow byte-for-byte; "bounded-v1" enables bounded
// observable task context admission for native role execution. No other value
// is admitted.
//
// GraphVersion selects hierarchical task graph execution. Zero preserves the
// legacy sequential workflow byte-for-byte; 1 enables autonomous graph
// execution with bounded parallel read-only explorers. MaxParallel bounds
// explorer concurrency between 1 and 8 inclusive; zero preserves legacy
// identity and means sequential (1) for old runs.
type ExecutionPolicy struct {
	// SemanticCorrectionVersion admits at most two new output-correction calls.
	// Omitted retains historical stop-on-rejection behavior.
	SemanticCorrectionVersion int `json:"semantic_correction_version,omitempty"`
	// CapabilityFallbacks records pre-dispatch choices. These observations do
	// not grant authority or change any previously bound run configuration.
	CapabilityFallbacks []CapabilityFallback `json:"capability_fallbacks,omitempty"`
	Mode                string               `json:"mode"`
	MaxRepairs          int                  `json:"max_repairs"`
	// PromptRecipe selects a versioned serialization recipe for new model
	// invocations. Empty preserves every historical prompt byte.
	PromptRecipe string `json:"prompt_recipe,omitempty"`
	Context      string `json:"context,omitempty"`
	// ContextSelector opts bounded task context into experimental
	// rrf-coverage-v1 ranking. Empty preserves exact historical/default
	// selection; only "rrf-coverage-v1" is admitted and it requires
	// Context "bounded-v1". No default promotion.
	ContextSelector string `json:"context_selector,omitempty"`
	// PlannerContext opts planning into a bounded committed-source manifest.
	// Empty retains historic planner input identity exactly.
	PlannerContext string `json:"planner_context,omitempty"`
	// PlannerParseCacheVersion opts versioned Go planner contexts into local reuse of
	// validated, content-addressed source syntax facts. Zero preserves the
	// existing collection behavior; cache observations grant no authority.
	PlannerParseCacheVersion int `json:"planner_parse_cache_version,omitempty"`
	// ReviewImpactContextVersion opts reviews into a candidate-bound, bounded
	// source-topology projection. Zero preserves historical reviewer invocations.
	ReviewImpactContextVersion int `json:"review_impact_context_version,omitempty"`
	// CandidateFactsCacheVersion opts review-impact collection into local,
	// content-addressed candidate syntax-fact reuse. Cache observations are not
	// durable evidence and never authorize an effect.
	CandidateFactsCacheVersion int `json:"candidate_facts_cache_version,omitempty"`
	// PlannerContextRIExecutable and PlannerContextRIExecutableSHA256 pin the
	// local read-only parser used only by Go planner-context admission. They
	// are immutable run inputs and do not authorize a model or repository effect.
	PlannerContextRIExecutable       string `json:"planner_context_ri_executable,omitempty"`
	PlannerContextRIExecutableSHA256 string `json:"planner_context_ri_executable_sha256,omitempty"`
	// PlannerPPRVersion opts go-source-context-v2 planning into the bounded
	// Personalized PageRank treatment over the already admitted engineering
	// graph. Zero preserves exact historical planner input identity; 1 binds
	// the fixed PPR rank provenance into the admitted record. No other value
	// is admitted and no default promotion occurs.
	PlannerPPRVersion int `json:"planner_ppr_version,omitempty"`
	GraphVersion      int `json:"graph_version,omitempty"`
	MaxParallel       int `json:"max_parallel,omitempty"`
	// RepairPlanningVersion opts new graph runs into candidate-bound design
	// tasks that refine never-started repair write paths within original scope.
	RepairPlanningVersion int `json:"repair_planning_version,omitempty"`
	// RepairIntelligenceVersion derives bounded fixer evidence from existing
	// recorded task context. Zero preserves historical invocation bytes.
	RepairIntelligenceVersion int `json:"repair_intelligence_version,omitempty"`
	// ReviewRecheckVersion binds explicit reviewer answers to prior concern IDs.
	// Absent policy preserves historical review invocation bytes.
	ReviewRecheckVersion int `json:"review_recheck_version,omitempty"`
	// WorkingContextVersion enables bounded model-authored context on dynamic
	// explorer turns only. Zero preserves historical invocation bytes.
	WorkingContextVersion int `json:"working_context_version,omitempty"`
	// ScheduledExplorerDispatchVersion independently enables dynamic Codex/fake
	// explorer execution. Retention remains a separate experimental policy.
	ScheduledExplorerDispatchVersion int `json:"scheduled_explorer_dispatch_version,omitempty"`
	// ParallelImplementationVersion opts new graph runs into a bounded static
	// cohort of at most two independent implementation writers. Their proposals
	// are collected on one candidate and applied through one aggregate effect.
	ParallelImplementationVersion int `json:"parallel_implementation_version,omitempty"`
	// IsolatedImplementationVersion opts ready graph implementation tasks into
	// separate pristine source worktrees. It carries no writer authority.
	IsolatedImplementationVersion int                               `json:"isolated_implementation_version,omitempty"`
	IsolationCapacity             *engineeringplan.ResourceCapacity `json:"isolation_capacity,omitempty"`
	IsolationEstimate             *IsolationEstimateTemplate        `json:"isolation_estimate,omitempty"`
	// IsolationCohortSelectorVersion opts staged runs into an exact finite
	// wave optimum. Zero preserves the frozen greedy derivation byte-for-byte;
	// 1 selects the exhaustive lexicographic optimum over the same hard gates
	// (frozen v1.1.39 behavior: admitted count, then critical/resource
	// packing); 2 selects the coupling-aware optimum, which shares those hard
	// gates plus the C4 hard-coupling gate and minimizes C3 risk before
	// admitted count, then C2, then C1 co-scheduling before existing
	// critical/resource packing; 3 selects the source-observed coupling
	// optimum, which merges planner advisory couplings with the
	// controller-derived source-observed set (C4 generation family, C2 same
	// package) at MAX severity over the same hard gates. Only staged
	// isolation admits a nonzero selector; 2 and 3 additionally
	// require plan-graph-v9. C0 (absent) never proves independence.
	IsolationCohortSelectorVersion int `json:"isolation_cohort_selector_version,omitempty"`
	// ScopeReplanVersion permits a confirmed writer proposal to request a
	// bounded WritePaths refinement, only within its immutable ScopePaths.
	ScopeReplanVersion       int                              `json:"scope_replan_version,omitempty"`
	ScopeReplanDesignVersion int                              `json:"scope_replan_design_version,omitempty"`
	MaxScopeReplans          int                              `json:"max_scope_replans,omitempty"`
	CodexAutoCompact         *runtime.CodexAutoCompactOptions `json:"codex_auto_compact,omitempty"`
	// EvidencePolicy opts a new serial graph run into one automatic finite
	// source acquisition before the initial writer. Absent preserves exact
	// historical wire, prompt and replay behavior.
	EvidencePolicy *EvidenceAutoPolicy `json:"evidence_policy,omitempty"`
}

func codexAutoCompactForExecution(policy *ExecutionPolicy, profile runtime.Profile) *runtime.CodexAutoCompactOptions {
	if policy == nil || policy.CodexAutoCompact == nil || profile.Runtime != "codex-app-server" {
		return nil
	}
	options := *policy.CodexAutoCompact
	return &options
}

// Validate admits only the bounded autonomous workflow with a repair budget
// between 0 and 8 inclusive. Context, when present, must be "bounded-v1".
// GraphVersion must be 0 (legacy) or 1 (graph); MaxParallel must be 0
// (legacy/unspecified) or 1..8. Graph version 1 requires an explicit 1..8
// bound; legacy runs keep MaxParallel 0 for identical canonical identity.
// RepairPlanningVersion 1 requires graph execution and a matching graph-v3
// planner contract at creation replay; zero preserves the prior repair recipe.
func (p ExecutionPolicy) Validate() error {
	if p.ScheduledExplorerDispatchVersion != 0 && (p.ScheduledExplorerDispatchVersion != 1 || p.GraphVersion != 1 || p.Context != taskContextBoundedV1) {
		return errors.New("unsupported scheduled explorer dispatch policy")
	}
	if p.WorkingContextVersion != 0 && (p.WorkingContextVersion != 1 || p.ScheduledExplorerDispatchVersion != 1) {
		return errors.New("unsupported working context policy")
	}
	if p.ReviewRecheckVersion != 0 && (p.ReviewRecheckVersion != 1 || p.RepairIntelligenceVersion != 1) {
		return errors.New("unsupported review recheck policy")
	}
	if p.RepairIntelligenceVersion != 0 && (p.RepairIntelligenceVersion != 1 || p.GraphVersion != 1 || p.RepairPlanningVersion != 1 || p.Context != taskContextBoundedV1) {
		return errors.New("unsupported repair intelligence policy")
	}
	if p.ScopeReplanDesignVersion != 0 && (p.ScopeReplanDesignVersion < 1 || p.ScopeReplanDesignVersion > 2 || p.ScopeReplanDesignVersion != p.ScopeReplanVersion) {
		return errors.New("unsupported scope replan design policy")
	}
	if p.SemanticCorrectionVersion != 0 && (p.SemanticCorrectionVersion != 1 || p.GraphVersion != 1) {
		return errors.New("unsupported semantic correction policy")
	}
	if len(p.CapabilityFallbacks) > 3 {
		return errors.New("too many capability fallbacks")
	}
	seenFallbacks := map[string]bool{}
	for _, fallback := range p.CapabilityFallbacks {
		if fallback.Validate() != nil || seenFallbacks[fallback.Capability] {
			return errors.New("invalid capability fallback")
		}
		seenFallbacks[fallback.Capability] = true
		switch fallback.Capability {
		case "planner_context":
			if p.PlannerContext != "source-bounded-v1" {
				return errors.New("context fallback differs from selected capability")
			}
		case "parallel_writers":
			if p.ParallelImplementationVersion != 0 || p.IsolatedImplementationVersion != 0 {
				return errors.New("writer fallback differs from selected capability")
			}
		case "auto_compaction":
			if p.CodexAutoCompact != nil {
				return errors.New("compaction fallback differs from selected capability")
			}
		}
	}
	if p.Mode != "autonomous-v1" || p.MaxRepairs < 0 || p.MaxRepairs > 8 {
		return errors.New("invalid execution policy")
	}
	if p.CodexAutoCompact != nil {
		if err := p.CodexAutoCompact.Validate(); err != nil {
			return err
		}
	}
	if p.PromptRecipe != "" && p.PromptRecipe != promptRecipeCachePrefixV1 {
		return errors.New("invalid execution prompt recipe")
	}
	if p.Context != "" && p.Context != taskContextBoundedV1 {
		return errors.New("invalid execution task context")
	}
	if p.ContextSelector != "" && p.ContextSelector != taskContextSelectorRRFCoverageV1 {
		return errors.New("invalid execution context selector")
	}
	if p.ContextSelector != "" && p.Context != taskContextBoundedV1 {
		return errors.New("context selector requires bounded task context")
	}
	if p.PlannerContext != "" && p.PlannerContext != plannerContextSourceBoundedV1 && p.PlannerContext != plannerContextGoSourceV1 && p.PlannerContext != plannerContextGoSourceV2 && p.PlannerContext != plannerContextGoContractV1 && p.PlannerContext != plannerContextGoContractV2 && p.PlannerContext != plannerContextGoContractV3 {
		return errors.New("invalid execution planner context")
	}
	if p.PlannerContext == plannerContextGoSourceV1 || p.PlannerContext == plannerContextGoSourceV2 || p.PlannerContext == plannerContextGoContractV1 || p.PlannerContext == plannerContextGoContractV2 || p.PlannerContext == plannerContextGoContractV3 {
		if p.PlannerContextRIExecutable == "" || !filepath.IsAbs(p.PlannerContextRIExecutable) || filepath.Clean(p.PlannerContextRIExecutable) != p.PlannerContextRIExecutable || safepath.RequireDigest(p.PlannerContextRIExecutableSHA256) != nil {
			return errors.New("Go planner context requires a pinned RI executable")
		}
	} else if p.PlannerContextRIExecutable != "" || p.PlannerContextRIExecutableSHA256 != "" {
		return errors.New("RI planner context binding requires Go planner context")
	}
	if p.PlannerParseCacheVersion != 0 && p.PlannerParseCacheVersion != 1 {
		return errors.New("invalid planner parse-cache version")
	}
	if p.PlannerParseCacheVersion == 1 && p.PlannerContext != plannerContextGoSourceV2 && p.PlannerContext != plannerContextGoContractV1 && p.PlannerContext != plannerContextGoContractV2 && p.PlannerContext != plannerContextGoContractV3 {
		return errors.New("planner parse cache requires go-source-context-v2 or go-contract-context-v1/v2/v3")
	}
	if p.PlannerPPRVersion != 0 && p.PlannerPPRVersion != 1 {
		return errors.New("invalid planner PPR version")
	}
	if p.PlannerPPRVersion == 1 && p.PlannerContext != plannerContextGoSourceV2 {
		return errors.New("planner PPR requires go-source-context-v2")
	}
	if p.ReviewImpactContextVersion != 0 && p.ReviewImpactContextVersion != 1 {
		return errors.New("invalid review impact context version")
	}
	if p.ReviewImpactContextVersion == 1 && ((p.PlannerContext != plannerContextGoContractV1 && p.PlannerContext != plannerContextGoContractV2 && p.PlannerContext != plannerContextGoContractV3) || p.PlannerContextRIExecutable == "" || safepath.RequireDigest(p.PlannerContextRIExecutableSHA256) != nil) {
		return errors.New("review impact context requires go-contract-context-v1/v2/v3 and a pinned RI parser")
	}
	if p.CandidateFactsCacheVersion != 0 && p.CandidateFactsCacheVersion != 1 {
		return errors.New("invalid candidate facts cache version")
	}
	if p.CandidateFactsCacheVersion == 1 && p.ReviewImpactContextVersion != 1 {
		return errors.New("candidate facts cache requires review impact context")
	}
	if p.GraphVersion != 0 && p.GraphVersion != 1 {
		return errors.New("invalid execution graph version")
	}
	if p.MaxParallel < 0 || p.MaxParallel > 8 {
		return errors.New("invalid execution parallelism")
	}
	if p.GraphVersion == 1 && (p.MaxParallel < 1 || p.MaxParallel > 8) {
		return errors.New("graph execution requires max parallel 1..8")
	}
	if p.GraphVersion == 0 && p.MaxParallel > 1 {
		return errors.New("sequential execution cannot request parallelism")
	}
	if p.RepairPlanningVersion != 0 && p.RepairPlanningVersion != 1 {
		return errors.New("invalid repair planning version")
	}
	if p.RepairPlanningVersion == 1 && p.GraphVersion != 1 {
		return errors.New("repair planning requires graph execution")
	}
	if p.ParallelImplementationVersion != 0 && p.ParallelImplementationVersion != 1 {
		return errors.New("invalid parallel implementation version")
	}
	if p.ParallelImplementationVersion == 1 && (p.GraphVersion != 1 || p.RepairPlanningVersion != 1 || p.Context != taskContextBoundedV1) {
		return errors.New("parallel implementation requires graph execution, bounded task context, and repair planning")
	}
	if p.IsolatedImplementationVersion != 0 && p.IsolatedImplementationVersion != 1 && p.IsolatedImplementationVersion != 2 && p.IsolatedImplementationVersion != 3 {
		return errors.New("invalid isolated implementation version")
	}
	if (p.IsolatedImplementationVersion == 1 || p.IsolatedImplementationVersion == 2 || p.IsolatedImplementationVersion == 3) && (p.GraphVersion != 1 || p.RepairPlanningVersion != 1 || p.Context != taskContextBoundedV1) {
		return errors.New("isolated implementation requires graph execution, bounded task context, and repair planning")
	}
	if (p.IsolatedImplementationVersion == 1 || p.IsolatedImplementationVersion == 2 || p.IsolatedImplementationVersion == 3) && (p.IsolationCapacity == nil || p.IsolationEstimate == nil || p.IsolationEstimate.Validate() != nil) {
		return errors.New("isolated implementation requires capacity and estimate template")
	}
	if p.IsolatedImplementationVersion == 0 && (p.IsolationCapacity != nil || p.IsolationEstimate != nil) {
		return errors.New("isolation capacity requires isolated implementation")
	}
	if p.IsolationCohortSelectorVersion != 0 && p.IsolationCohortSelectorVersion != 1 && p.IsolationCohortSelectorVersion != 2 && p.IsolationCohortSelectorVersion != 3 {
		return errors.New("invalid isolation cohort selector version")
	}
	if p.IsolationCohortSelectorVersion != 0 && p.IsolatedImplementationVersion != 3 {
		return errors.New("isolation cohort selector requires staged isolation")
	}
	if (p.IsolatedImplementationVersion == 1 || p.IsolatedImplementationVersion == 2 || p.IsolatedImplementationVersion == 3) && p.ParallelImplementationVersion == 1 {
		return errors.New("parallel aggregate and isolated implementation modes are exclusive")
	}
	if p.ScopeReplanVersion < 0 || p.ScopeReplanVersion > 2 {
		return errors.New("invalid scope replan version")
	}
	if p.IsolatedImplementationVersion == 2 && p.ScopeReplanVersion != 0 {
		return errors.New("isolated waves do not support scope replanning")
	}
	if p.IsolatedImplementationVersion == 3 && p.ScopeReplanVersion != 0 {
		return errors.New("staged isolated cohorts do not support scope replanning")
	}
	if p.ScopeReplanVersion == 1 {
		if p.GraphVersion != 1 || p.RepairPlanningVersion != 1 || p.MaxScopeReplans < 1 || p.MaxScopeReplans > 2 || p.ParallelImplementationVersion != 0 || p.IsolatedImplementationVersion != 0 || p.ScopeReplanDesignVersion > 1 {
			return errors.New("scope replanning requires serial graph repair planning and a budget of one or two")
		}
	} else if p.ScopeReplanVersion == 2 {
		if p.IsolatedImplementationVersion == 2 || p.IsolatedImplementationVersion == 3 {
			return errors.New("isolated waves do not support scope replanning")
		}
		cohortMode := (p.ParallelImplementationVersion == 1) != (p.IsolatedImplementationVersion == 1)
		if p.GraphVersion != 1 || p.RepairPlanningVersion != 1 || p.MaxScopeReplans < 1 || p.MaxScopeReplans > 2 || !cohortMode || p.ScopeReplanDesignVersion != 2 {
			return errors.New("cohort scope replanning requires one bounded parallel or isolated graph mode and candidate-bound design v2")
		}
	} else if p.MaxScopeReplans != 0 {
		return errors.New("scope replan budget requires a scope replan version")
	}
	if p.EvidencePolicy != nil {
		if err := ValidateEvidenceAutoPolicyTemplate(*p.EvidencePolicy); err != nil {
			return err
		}
		if p.Context != taskContextBoundedV1 || p.GraphVersion != 1 {
			return errors.New("evidence policy requires bounded task context and graph execution")
		}
		if p.ParallelImplementationVersion != 0 || p.IsolatedImplementationVersion != 0 {
			return errors.New("evidence policy incompatible with parallel or isolated writers")
		}
	}
	return nil
}

// EffectiveMaxParallel returns the bounded explorer concurrency for graph
// runs. Legacy (GraphVersion 0) is always sequential. Graph runs default to
// the validated MaxParallel bound.
func (p ExecutionPolicy) EffectiveMaxParallel() int {
	if p.GraphVersion != 1 {
		return 1
	}
	if p.MaxParallel < 1 {
		return 1
	}
	if p.MaxParallel > 8 {
		return 8
	}
	return p.MaxParallel
}

// GraphEnabled reports whether hierarchical task graph execution applies.
// Nil or legacy policies remain sequential.
func (p *ExecutionPolicy) GraphEnabled() bool {
	return p != nil && p.GraphVersion == 1
}

func parallelImplementationEnabled(s Snapshot) bool {
	return s.Creation.Execution != nil && s.Creation.Execution.ParallelImplementationVersion == 1
}

// MachineApproval records that the immutable execution policy, rather than a
// human, admitted the exact planner result.
type MachineApproval struct {
	PlanID   string `json:"plan_id"`
	PolicyID string `json:"policy_id"`
	Actor    string `json:"actor"`
}

// Snapshot is reconstructed state, never independent authority to append effects.
type Snapshot struct {
	// EvidenceDecisions are replay-validated acquisition models and selections;
	// they cannot change authority, budgets or acceptance.
	EvidenceDecisions []EvidenceContextDecision `json:"evidence_decisions,omitempty"`
	// ControllerHead and ControllerSequence are populated only after Inspect
	// validates history and containment. They are not serialized run inputs.
	ControllerHead            string                             `json:"-"`
	ControllerSequence        int                                `json:"-"`
	CandidateIndex            *CandidateIndexObservation         `json:"candidate_index,omitempty"`
	PlannerAccess             *access.Intent                     `json:"planner_access,omitempty"`
	ModelAccess               []ModelAccessState                 `json:"model_access,omitempty"`
	Draft                     *DraftState                        `json:"draft,omitempty"`
	Push                      *PushState                         `json:"push,omitempty"`
	Explorations              []ExplorerRecord                   `json:"explorations,omitempty"`
	ExplorerHost              *ExplorerHostState                 `json:"explorer_host,omitempty"`
	ExplorerRuns              map[string]ExplorerHostState       `json:"explorer_runs,omitempty"`
	Commit                    *CommitState                       `json:"commit,omitempty"`
	ReviewHost                *ReviewHostState                   `json:"review_host,omitempty"`
	ScheduledReviewHosts      map[string]ReviewHostState         `json:"scheduled_review_hosts,omitempty"`
	Review                    *ReviewRecord                      `json:"review,omitempty"`
	ReviewRecheckHistory      *ReviewRecheckHistory              `json:"review_recheck_history,omitempty"`
	WorkingContextHistory     []ExplorerWorkingContextRecord     `json:"working_context_history,omitempty"`
	WriterHost                *WriterHostState                   `json:"writer_host,omitempty"`
	WriterProposal            *WriterRecord                      `json:"writer_proposal,omitempty"`
	GraphWriterHosts          map[string]WriterHostState         `json:"graph_writer_hosts,omitempty"`
	GraphWriterResults        map[string]GraphWriterRecord       `json:"graph_writer_results,omitempty"`
	GraphWriterBatch          *GraphWriterBatchRecord            `json:"graph_writer_batch,omitempty"`
	RIProducer                *RIProducerState                   `json:"ri_producer"`
	RIPublish                 *RIPublishState                    `json:"ri_publish"`
	RIImport                  *RIImportState                     `json:"ri_import"`
	RILexical                 *RILexicalState                    `json:"ri_lexical,omitempty"`
	RILexicalOverlay          *RILexicalOverlayState             `json:"ri_lexical_overlay,omitempty"`
	RunID                     string                             `json:"run_id"`
	State                     string                             `json:"state"`
	Creation                  Creation                           `json:"creation"`
	PlanID                    string                             `json:"plan_id"`
	Plan                      *runtime.Result                    `json:"plan"`
	ApprovedBy                string                             `json:"approved_by"`
	MachineApproval           *MachineApproval                   `json:"machine_approval,omitempty"`
	RepairAttempts            int                                `json:"repair_attempts,omitempty"`
	RepairCandidateID         string                             `json:"repair_candidate_id,omitempty"`
	WorkspaceIntent           *worktree.Request                  `json:"workspace_intent"`
	Workspace                 *worktree.Binding                  `json:"workspace"`
	Candidate                 *worktree.Candidate                `json:"candidate"`
	WorkspaceOutcome          string                             `json:"workspace_outcome"`
	FileIntent                *FileIntent                        `json:"file_intent"`
	FileReceipt               *FileReceipt                       `json:"file_receipt"`
	FileOutcome               string                             `json:"file_outcome"`
	FileRecovery              *RecoveryIntent                    `json:"file_recovery"`
	Verification              *VerificationState                 `json:"verification"`
	PlannerHost               *codexhost.Launch                  `json:"planner_host"`
	PlannerHostReady          bool                               `json:"planner_host_ready"`
	PlannerHostReceipt        *codexhost.Receipt                 `json:"planner_host_receipt"`
	PlannerReceipt            *PlannerReceipt                    `json:"planner_receipt"`
	PlannerCorrections        []PlannerSemanticCorrection        `json:"planner_corrections,omitempty"`
	RoleCorrections           []RoleSemanticCorrection           `json:"role_corrections,omitempty"`
	PlannerProvider           *providerDispatchReceipt           `json:"planner_provider,omitempty"`
	ProviderRuntime           map[string]providerDispatchReceipt `json:"provider_runtime,omitempty"`
	AgentDispatch             map[string]AgentDispatchState      `json:"agent_dispatch,omitempty"`
	TaskContexts              []TaskContextRecord                `json:"task_contexts,omitempty"`
	PlannerContext            *PlannerContextRecord              `json:"planner_context,omitempty"`
	PlannerGoContext          *PlannerGoContextRecord            `json:"planner_go_context,omitempty"`
	ReviewImpactContexts      []ReviewImpactContextRecord        `json:"review_impact_contexts,omitempty"`
	Graph                     *GraphState                        `json:"graph,omitempty"`
	GraphIsolations           map[string]GraphIsolationState     `json:"graph_isolations,omitempty"`
	GraphIsolationPreparation *GraphIsolationPreparation         `json:"graph_isolation_preparation,omitempty"`
	GraphStagedCohorts        []StagedCohortArchive              `json:"graph_staged_cohorts,omitempty"`
	GraphStagedForks          map[string]StagedForkState         `json:"graph_staged_forks,omitempty"`
	GraphMemoryAdmission      *GraphMemoryAdmissionState         `json:"graph_memory_admission,omitempty"`
	ScopeReplans              []ScopeReplanRecord                `json:"scope_replans,omitempty"`
	ScopeReplanRequests       []ScopeReplanRequest               `json:"scope_replan_requests,omitempty"`
	Lifecycle                 LifecycleState                     `json:"lifecycle"`
}

// Approval is explicit human input for one exact plan, not a model decision.
type Approval struct {
	PlanID string `json:"plan_id"`
	Actor  string `json:"actor"`
}

// Replay establishes all transition invariants from the complete journal. It
// rejects unknown events rather than returning a permissive partial state.
func Replay(events []journal.Event) (Snapshot, error) {
	s := Snapshot{}
	seenEffects := map[string]bool{}
	for _, e := range events {
		if err := admitLifecycleEvent(s, e.Kind); err != nil {
			return s, err
		}
		if !lifecycleControlEvent(e.Kind) && s.Draft != nil && s.Draft.LeaseRecovery != nil && s.Draft.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "draft.observed" && e.Kind != "draft.lease-intent" && e.Kind != "draft.lease-observed" {
			return s, errors.New("draft lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Draft != nil && s.Draft.Outcome == "UNKNOWN" && e.Kind != "draft.observed" && e.Kind != "draft.lease-intent" && e.Kind != "draft.lease-observed" {
			return s, errors.New("draft UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Push != nil && s.Push.LeaseRecovery != nil && s.Push.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "push.observed" && e.Kind != "push.lease-intent" && e.Kind != "push.lease-observed" {
			return s, errors.New("push lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Push != nil && s.Push.Outcome == "UNKNOWN" && e.Kind != "push.observed" && e.Kind != "push.lease-intent" && e.Kind != "push.lease-observed" {
			return s, errors.New("push UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Commit != nil && s.Commit.LeaseRecovery != nil && s.Commit.LeaseRecovery.Outcome == "UNKNOWN" && e.Kind != "commit.observed" && e.Kind != "commit.lease-observed" && e.Kind != "commit.lease-intent" {
			return s, errors.New("lease recovery UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.Commit != nil && s.Commit.Outcome == "UNKNOWN" && e.Kind != "commit.observed" && e.Kind != "commit.lease-intent" && e.Kind != "commit.lease-observed" && e.Kind != "commit.recovery-intent" {
			return s, errors.New("commit UNKNOWN; reconciliation required before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIProducer != nil && s.RIProducer.Outcome == "UNKNOWN" && s.RIProducer.Closure == nil && e.Kind != "ri.producer-observed" && e.Kind != "ri.producer-closed" {
			return s, errors.New("RI producer UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIPublish != nil && s.RIPublish.Outcome == "UNKNOWN" && e.Kind != "ri.publish-observed" && e.Kind != "ri.publish-recovery-intent" {
			return s, errors.New("RI publication UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RIImport != nil && s.RIImport.Outcome == "UNKNOWN" && e.Kind != "ri.import-observed" {
			return s, errors.New("RI import UNKNOWN; reconcile before further events")
		}
		if !lifecycleControlEvent(e.Kind) && s.RILexical != nil && s.RILexical.Outcome == "UNKNOWN" && e.Kind != "ri.lexical-observed" {
			return s, errors.New("RI lexical UNKNOWN; reconciliation required")
		}
		if !lifecycleControlEvent(e.Kind) && s.RILexicalOverlay != nil && s.RILexicalOverlay.Outcome == "UNKNOWN" && e.Kind != "ri.overlay-observed" {
			return s, errors.New("RI overlay UNKNOWN; reconciliation required")
		}
		switch e.Kind {
		case "agent.dispatch-admitted", "agent.dispatch-observed":
			if err := replayAgentDispatch(&s, e); err != nil {
				return s, err
			}
		case "ri.overlay-intent", "ri.overlay-observed":
			if err := replayLexicalOverlay(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.lexical-intent", "ri.lexical-observed":
			if err := replayRILexical(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "planning.access-intent":
			if err := replayPlanningAccess(&s, e); err != nil {
				return s, err
			}
		case "model.access-intent", "model.access-receipt", "model.access-semantic-pending":
			if err := replayModelAccess(&s, e); err != nil {
				return s, err
			}
		case "draft.lease-intent", "draft.lease-observed":
			if err := replayDraftLease(&s, e); err != nil {
				return s, err
			}
		case "draft.intent", "draft.observed":
			if err := replayDraft(&s, e); err != nil {
				return s, err
			}
		case "push.lease-intent", "push.lease-observed":
			if err := replayPushLease(&s, e); err != nil {
				return s, err
			}
		case "push.intent", "push.observed":
			if err := replayPush(&s, e); err != nil {
				return s, err
			}
		case "explorer.host-intent", "explorer.host-ready", "explorer.host-observed", "explorer.runtime-observed":
			if err := replayExplorerHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "explorer.runtime-observed" {
				var receipt ExplorerRuntimeReceipt
				if err := canonical.Decode(e.Payload, &receipt); err != nil {
					return s, err
				}
				host, ok := explorerRunForInvocation(s, receipt.InvocationID)
				if !ok || host.RuntimeReceipt == nil {
					return s, errors.New("exact explorer runtime receipt required")
				}
				r := host.RuntimeReceipt
				if err := requireModelAccessResult(s, host.Intent.Invocation, r.JournalHead, r.ResultHash, r.ThreadID, r.TurnID, r.UsagePending); err != nil {
					return s, err
				}
			}
		case "evidence.context-decided":
			if err := replayEvidenceContextDecision(&s, e); err != nil {
				return s, err
			}
		case "explorer.recorded":
			var record ExplorerRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := replayExplorer(&s, record); err != nil {
				return s, err
			}
			if err := recordExplorerWorkingContext(&s, record, e.Hash); err != nil {
				return s, err
			}
		case "commit.recovery-intent":
			if err := replayCommitRecovery(&s, e); err != nil {
				return s, err
			}
		case "commit.lease-intent", "commit.lease-observed":
			if err := replayCommitLease(&s, e); err != nil {
				return s, err
			}
		case "commit.observed":
			if err := replayCommitObservation(&s, e); err != nil {
				return s, err
			}
		case "commit.intent":
			if err := replayCommitIntent(&s, e); err != nil {
				return s, err
			}
		case "review.host-intent", "review.host-ready", "review.host-observed", "review.runtime-observed":
			if err := replayReviewHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "review.runtime-observed" {
				r := s.ReviewHost.RuntimeReceipt
				if err := requireModelAccessResult(s, s.ReviewHost.Intent.Invocation, r.JournalHead, r.ResultHash, r.ThreadID, r.TurnID, r.UsagePending); err != nil {
					return s, err
				}
			}
		case "scheduled.review.host-intent", "scheduled.review.host-ready", "scheduled.review.host-observed", "scheduled.review.runtime-observed":
			if err := replayScheduledReviewHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "scheduled.review.runtime-observed" {
				var event ScheduledReviewHostEvent
				if err := canonical.Decode(e.Payload, &event); err != nil {
					return s, err
				}
				host := s.ScheduledReviewHosts[event.TaskID]
				if host.RuntimeReceipt == nil {
					return s, errors.New("exact scheduled review runtime receipt required")
				}
				if err := requireModelAccessResult(s, host.Intent.Invocation, host.RuntimeReceipt.JournalHead, host.RuntimeReceipt.ResultHash, host.RuntimeReceipt.ThreadID, host.RuntimeReceipt.TurnID, host.RuntimeReceipt.UsagePending); err != nil {
					return s, err
				}
			}
		case "review.recorded":
			if err := replayReview(&s, e); err != nil {
				return s, err
			}
		case "writer.host-intent", "writer.host-ready", "writer.host-observed", "writer.runtime-observed":
			if err := replayWriterHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "writer.runtime-observed" {
				r := s.WriterHost.RuntimeReceipt
				if err := requireModelAccessResult(s, s.WriterHost.Intent.Invocation, r.JournalHead, r.ResultHash, r.ThreadID, r.TurnID, r.UsagePending); err != nil {
					return s, err
				}
			}
		case "writer.proposed":
			if err := replayWriterProposal(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "graph.writer.host-intent", "graph.writer.host-ready", "graph.writer.host-observed", "graph.writer.runtime-observed":
			if err := replayGraphWriterHost(&s, e); err != nil {
				return s, err
			}
			if e.Kind == "graph.writer.runtime-observed" {
				var event GraphWriterHostEvent
				if err := canonical.Decode(e.Payload, &event); err != nil {
					return s, err
				}
				host, ok := s.GraphWriterHosts[event.TaskID]
				if !ok || host.RuntimeReceipt == nil {
					return s, errors.New("exact graph writer runtime receipt required")
				}
				if err := requireModelAccessResult(s, host.Intent.Invocation, host.RuntimeReceipt.JournalHead, host.RuntimeReceipt.ResultHash, host.RuntimeReceipt.ThreadID, host.RuntimeReceipt.TurnID, host.RuntimeReceipt.UsagePending); err != nil {
					return s, err
				}
			}
		case "graph.writer.proposed":
			if err := replayGraphWriterProposal(&s, e, seenEffects); err != nil {
				return s, err
			}
			var record GraphWriterRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := settleGraphMemoryAdmissionProposal(&s, record); err != nil {
				return s, err
			}
		case "graph.writer.batch-proposed":
			if err := replayGraphWriterBatch(&s, e); err != nil {
				return s, err
			}
		case "ri.producer-intent", "ri.producer-observed", "ri.producer-closed":
			if err := replayRIProducer(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.publish-intent", "ri.publish-observed", "ri.publish-recovery-intent":
			if err := replayRIPublish(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "ri.import-intent", "ri.import-observed":
			if err := replayRIImport(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "run.created":
			if s.State != "" {
				return s, errors.New("run already exists")
			}
			var c Creation
			if err := canonical.Decode(e.Payload, &c); err != nil {
				return s, err
			}
			if c.Version != 1 || strings.TrimSpace(c.Nonce) == "" || len(c.Nonce) > 128 || c.Config.Planner.Role != "planner" {
				return s, errors.New("invalid creation")
			}
			if c.Execution != nil {
				if err := c.Execution.Validate(); err != nil {
					return s, err
				}
				if c.Execution.ReviewRecheckVersion == 1 && (c.Config.Reviewer == nil || c.Config.ReviewerContract != "json-v1") {
					return s, errors.New("review rechecks require structured configured review")
				}
				if c.Execution.ScheduledExplorerDispatchVersion == 1 && (c.Config.Version != 2 || c.Config.Explorer == nil || c.Config.ExplorerContract != "json-v2" || c.Config.Explorer.Runtime != "codex-app-server" && c.Config.Explorer.Runtime != "fake") {
					return s, errors.New("working context requires configuration v2 with structured explorer")
				}
			}
			if c.AgentContext != nil {
				sourceID, err := c.Repository.ID()
				if err != nil || c.AgentContext.Validate() != nil || c.AgentContext.SourceID != sourceID || c.AgentContext.SourceCommit != c.Repository.Commit || c.Execution == nil || c.Execution.GraphVersion != 1 {
					return s, errors.New("agent context differs from immutable run source or graph policy")
				}
			}
			if err := validateRepairPlanningBinding(c); err != nil {
				return s, err
			}
			if err := c.Repository.Validate(); err != nil {
				return s, err
			}
			if err := c.Config.Validate(); err != nil {
				return s, err
			}
			if c.Execution != nil && c.Execution.CodexAutoCompact != nil && !creationSupportsCodexAutoCompact(c.Config) {
				return s, errors.New("Codex auto-compaction requires every configured role to use the Codex app-server runtime")
			}
			if err := validateCreationHostAdmission(c); err != nil {
				return s, err
			}
			if c.Config.Repository != c.Repository.Name {
				return s, errors.New("configuration binding mismatch")
			}
			if c.Config.PlannerContract == "plan-graph-v7" || c.Config.PlannerContract == plannerContractGraphV8 || c.Config.PlannerContract == plannerContractGraphV9 {
				if _, err := plannerInvocationWithContextsAndRecipe(c.Config, c.Objective, nil, nil, c.Execution); err != nil {
					return s, err
				}
			} else if _, err := plannerInvocation(c.Config, c.Objective); err != nil {
				return s, err
			}
			id, err := canonical.Hash("harness.run.v1", c)
			if err != nil {
				return s, err
			}
			s = Snapshot{RunID: id, State: "OBJECTIVE", Creation: c, WorkspaceOutcome: "NOT_RUN", Lifecycle: LifecycleState{Status: LifecycleActive}}
		case "run.pause-requested", "run.paused", "run.resumed", "run.cancel-requested", "run.cancelled":
			if err := replayLifecycle(&s, e); err != nil {
				return s, err
			}
		case "planning.started":
			if s.State != "OBJECTIVE" {
				return s, errors.New("planning transition rejected")
			}
			var empty struct{}
			if err := canonical.Decode(e.Payload, &empty); err != nil {
				return s, err
			}
			s.State = "PLANNING"
		case "plan.recorded":
			if s.State != "PLANNING" {
				return s, errors.New("plan transition rejected")
			}
			var r runtime.Result
			if err := canonical.Decode(e.Payload, &r); err != nil {
				return s, err
			}
			i, err := plannerInvocationForSnapshot(s)
			if err != nil {
				return s, err
			}
			if s.PlannerReceipt != nil && s.PlannerReceipt.FailureCode != "" {
				return s, errors.New("capacity failure receipt cannot authorize a plan")
			}
			if err = runtime.ValidateResult(i, r, true); err != nil {
				return s, err
			}
			if s.Creation.Config.Version == 2 && s.Creation.Config.Planner.Runtime == "fake" {
				if err := validatePlanningAccessResult(s, r); err != nil {
					return s, err
				}
			}
			if s.Creation.Config.Planner.Runtime == "codex-app-server" {
				hash, err := canonical.Hash("harness.planner-result.v1", r)
				if err != nil {
					return s, err
				}
				if s.PlannerReceipt == nil || s.PlannerReceipt.ResultHash != hash {
					return s, errors.New("plan lacks exact runtime receipt")
				}
			}
			if s.Creation.Config.Planner.Runtime == "provider-api" || s.Creation.Config.Planner.Runtime == "opencode-http" {
				hash, err := canonical.Hash("harness.planner-result.v1", r)
				if err != nil {
					return s, err
				}
				if s.PlannerProvider == nil || s.PlannerProvider.ResultHash != hash || !sameCanonical(s.PlannerProvider.Result, r) {
					return s, errors.New("plan lacks exact direct provider receipt")
				}
			}
			id, err := canonical.Hash("harness.plan.v1", r)
			if err != nil {
				return s, err
			}
			s.Plan = &r
			s.PlanID = id
			s.State = "AWAITING_APPROVAL"
		case "plan.approved":
			if s.State != "AWAITING_APPROVAL" {
				return s, errors.New("approval transition rejected")
			}
			var a Approval
			if err := canonical.Decode(e.Payload, &a); err != nil {
				return s, err
			}
			if a.PlanID != s.PlanID || strings.TrimSpace(a.Actor) == "" || len(a.Actor) > 256 {
				return s, errors.New("approval does not bind exact plan and actor")
			}
			s.ApprovedBy = a.Actor
			s.State = "IMPLEMENTING"
		case "plan.autonomous-authorized":
			if s.State != "AWAITING_APPROVAL" || s.Creation.Execution == nil {
				return s, errors.New("autonomous approval transition rejected")
			}
			var a MachineApproval
			if err := canonical.Decode(e.Payload, &a); err != nil {
				return s, err
			}
			policyID, err := canonical.Hash("harness.execution-policy.v1", *s.Creation.Execution)
			if err != nil {
				return s, err
			}
			if a.PlanID != s.PlanID || a.PolicyID != policyID || a.Actor != "fabric:autonomous" {
				return s, errors.New("machine approval binding mismatch")
			}
			s.MachineApproval = &a
			s.ApprovedBy = a.Actor
			s.State = "IMPLEMENTING"
		case "autonomous.repair-started":
			if s.State != "REPAIRING" || s.Creation.Execution == nil {
				return s, errors.New("autonomous repair transition rejected")
			}
			var repair AutonomousRepair
			if err := canonical.Decode(e.Payload, &repair); err != nil {
				return s, err
			}
			if s.Candidate == nil {
				return s, errors.New("autonomous repair requires a candidate")
			}
			candidateID, err := s.Candidate.ID()
			if err != nil || repair.CandidateID != candidateID || repair.Attempt != s.RepairAttempts+1 || repair.Attempt > s.Creation.Execution.MaxRepairs || repair.CandidateID == s.RepairCandidateID {
				return s, errors.New("autonomous repair bound rejected")
			}
			s.RepairAttempts = repair.Attempt
			s.RepairCandidateID = repair.CandidateID
		case "candidate.index-observed":
			if err := replayCandidateIndex(&s, e.Payload); err != nil {
				return s, err
			}
		case "workspace.intent":
			if s.State != "IMPLEMENTING" || s.WorkspaceIntent != nil || s.Workspace != nil {
				return s, errors.New("workspace intent transition rejected")
			}
			var r worktree.Request
			if err := canonical.Decode(e.Payload, &r); err != nil {
				return s, err
			}
			if err := r.Validate(); err != nil {
				return s, err
			}
			expectedStateRoot, err := controllerNamespace(s.Creation)
			if err != nil || r.RunID != s.RunID || r.Source != s.Creation.Repository || r.CandidateIdentity != s.Creation.Config.CandidateIdentity || r.ControllerStateRoot != expectedStateRoot {
				return s, errors.New("workspace intent binding mismatch")
			}
			s.WorkspaceIntent = &r
			s.WorkspaceOutcome = "UNKNOWN"
		case "workspace.confirmed":
			if s.State != "IMPLEMENTING" || s.WorkspaceIntent == nil || s.Workspace != nil {
				return s, errors.New("workspace receipt transition rejected")
			}
			var receipt WorkspaceReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return s, err
			}
			if receipt.Binding.Request != *s.WorkspaceIntent {
				return s, errors.New("workspace receipt substitution")
			}
			id, err := receipt.Binding.ID()
			if err != nil {
				return s, err
			}
			if _, err := receipt.Candidate.ID(); err != nil {
				return s, err
			}
			expectedVersion := 1
			if s.Creation.Config.CandidateIdentity == "semantic-index-v2" {
				expectedVersion = 2
			}
			if receipt.Candidate.Version != expectedVersion {
				return s, errors.New("candidate identity version differs from authorized config")
			}
			if receipt.Candidate.WorktreeID != id || receipt.Candidate.Head != s.Creation.Repository.Commit {
				return s, errors.New("workspace candidate binding mismatch")
			}
			s.Workspace = &receipt.Binding
			s.Candidate = &receipt.Candidate
			s.WorkspaceOutcome = "CONFIRMED"
		case "files.intent":
			if err := filesAllowed(s); err != nil {
				return s, err
			}
			if stagedIsolationEnabled(s) && s.FileIntent != nil {
				return s, errors.New("staged cohort requires advance before another file effect")
			}
			var intent FileIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return s, err
			}
			expected, err := preparedFiles(s, intent.Prepared.Proposal)
			if err != nil {
				return s, err
			}
			if expected.Intent != intent.Prepared.Intent {
				return s, errors.New("file effect intent binding mismatch")
			}
			if err = validateFileAuthorization(s, intent.Authorization, expected.Intent); err != nil {
				return s, err
			}
			id, err := expected.Intent.ID()
			if err != nil {
				return s, err
			}
			if seenEffects[id] {
				return s, errors.New("effect intent already attempted")
			}
			seenEffects[id] = true
			s.FileIntent = &intent
			s.FileRecovery = nil
			s.FileReceipt = nil
			s.FileOutcome = "UNKNOWN"
		case "files.recovery-intent":
			var intent RecoveryIntent
			if err := canonical.Decode(e.Payload, &intent); err != nil {
				return s, err
			}
			expected, err := preparedRecovery(s, intent.Prepared.Recovery)
			if err != nil {
				return s, err
			}
			if expected.Intent != intent.Prepared.Intent {
				return s, errors.New("recovery effect binding mismatch")
			}
			if err = intent.Authorization.Validate(expected.Intent); err != nil {
				return s, err
			}
			id, err := expected.Intent.ID()
			if err != nil {
				return s, err
			}
			if seenEffects[id] {
				return s, errors.New("recovery already attempted")
			}
			if s.FileReceipt == nil || s.FileReceipt.Observation.Candidate == nil || *s.FileReceipt.Observation.Candidate != intent.Prepared.Recovery.Observed {
				return s, errors.New("fresh reconciliation receipt required before recovery")
			}
			seenEffects[id] = true
			s.FileRecovery = &intent
			s.FileReceipt = nil
		case "files.observed":
			if s.FileIntent == nil || s.FileOutcome != "UNKNOWN" {
				return s, errors.New("no pending file effect")
			}
			var receipt FileReceipt
			if err := canonical.Decode(e.Payload, &receipt); err != nil {
				return s, err
			}
			if len(receipt.Observation.Error) > 2048 || receipt.Observation.Candidate == nil && receipt.Observation.Error == "" {
				return s, errors.New("invalid file observation")
			}
			if receipt.Observation.Candidate != nil {
				if _, err := receipt.Observation.Candidate.ID(); err != nil {
					return s, err
				}
			}
			h, err := canonical.Hash("harness.file-observation.v1", receipt.Observation)
			if err != nil {
				return s, err
			}
			if h != receipt.Receipt.ObservationHash {
				return s, errors.New("file observation hash mismatch")
			}
			outcome, err := effects.Outcome(activeFileIntent(s), &receipt.Receipt)
			if err != nil {
				return s, err
			}
			if outcome != fileeffects.Classify(s.FileIntent.Prepared.Proposal, receipt.Observation.Candidate) {
				return s, errors.New("file outcome not established by observation")
			}
			s.FileReceipt = &receipt
			s.FileOutcome = outcome
			if outcome == "CONFIRMED" {
				after := s.FileIntent.Prepared.Proposal.After
				s.Candidate = &after
			}
		case "planning.semantic-correction":
			if err := replayPlannerSemanticCorrection(&s, e); err != nil {
				return s, err
			}
		case "role.semantic-correction":
			if err := replayRoleSemanticCorrection(&s, e); err != nil {
				return s, err
			}
		case "planning.host-intent", "planning.host-ready", "planning.host-observed", "planning.runtime-observed":
			if err := replayPlanner(&s, e); err != nil {
				return s, err
			}
		case "planning.provider-observed":
			if err := replayPlannerProvider(&s, e); err != nil {
				return s, err
			}
		case "role.provider-observed":
			if err := replayRoleProvider(&s, e); err != nil {
				return s, err
			}
		case "verification.planned", "verification.started", "verification.observed", "verification.closed":
			if err := replayVerification(&s, e, seenEffects); err != nil {
				return s, err
			}
		case "task.context-admitted":
			if err := replayTaskContext(&s, e); err != nil {
				return s, err
			}
		case "planner.context-admitted":
			var record PlannerContextRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := replayPlannerContext(&s, record); err != nil {
				return s, err
			}
		case "planner.go-context-admitted":
			var record PlannerGoContextRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := replayPlannerGoContext(&s, record); err != nil {
				return s, err
			}
		case "review.impact-context-admitted":
			var record ReviewImpactContextRecord
			if err := canonical.Decode(e.Payload, &record); err != nil {
				return s, err
			}
			if err := replayReviewImpactContext(&s, record); err != nil {
				return s, err
			}
		case "graph.recorded", "graph.progress", "graph.revised":
			if err := replayGraph(&s, e); err != nil {
				return s, err
			}
		case "graph.isolation-prepared", "graph.isolate-intent", "graph.isolate-confirmed":
			if err := replayGraphIsolation(&s, e); err != nil {
				return s, err
			}
		case "graph.staged-prepared":
			if err := replayStagedPreparation(&s, e); err != nil {
				return s, err
			}
		case "graph.staged-fork-intent", "graph.staged-fork-confirmed":
			if err := replayStagedFork(&s, e); err != nil {
				return s, err
			}
		case "graph.staged-advanced":
			if err := replayStagedAdvance(&s, e); err != nil {
				return s, err
			}
		case "graph.writer.memory-admitted", "graph.writer.memory-released":
			if err := replayGraphMemoryAdmission(&s, e); err != nil {
				return s, err
			}
		case "graph.scope-replan-requested":
			if err := replayGraphScopeReplanRequest(&s, e); err != nil {
				return s, err
			}
		case "graph.scope-replanned":
			if err := replayGraphScopeReplan(&s, e); err != nil {
				return s, err
			}
		default:
			return s, errors.New("unknown controller event")
		}
	}
	return s, nil
}

func creationSupportsCodexAutoCompact(c config.Config) bool {
	if c.Planner.Runtime != "codex-app-server" {
		return false
	}
	for _, profile := range []*runtime.Profile{c.Writer, c.Fixer, c.Explorer, c.Reviewer} {
		if profile != nil && profile.Runtime != "codex-app-server" {
			return false
		}
	}
	return true
}

func validateRepairPlanningBinding(c Creation) error {
	version := 0
	if c.Execution != nil {
		version = c.Execution.RepairPlanningVersion
	}
	parallelVersion := 0
	isolationVersion := 0
	if c.Execution != nil {
		parallelVersion = c.Execution.ParallelImplementationVersion
		isolationVersion = c.Execution.IsolatedImplementationVersion
	}
	serialContract := c.Config.PlannerContract == plannerContractGraphV3 || c.Config.PlannerContract == plannerContractGraphV5
	parallelContract := c.Config.PlannerContract == plannerContractGraphV4 || c.Config.PlannerContract == plannerContractGraphV6
	isolationContract := c.Config.PlannerContract == "plan-graph-v7"
	stagedContract := c.Config.PlannerContract == plannerContractGraphV8
	stagedCouplingContract := c.Config.PlannerContract == plannerContractGraphV9
	if isolationVersion == 1 || isolationVersion == 2 {
		if version != 1 || parallelVersion != 0 || !isolationContract {
			return errors.New("isolated implementation policy requires plan-graph-v7")
		}
		return nil
	}
	if isolationVersion == 3 {
		selector := 0
		if c.Execution != nil {
			selector = c.Execution.IsolationCohortSelectorVersion
		}
		if selector == 2 || selector == 3 {
			if version != 1 || parallelVersion != 0 || !stagedCouplingContract {
				return errors.New("coupling-aware staged policy requires plan-graph-v9")
			}
			return nil
		}
		if version != 1 || parallelVersion != 0 || !stagedContract {
			return errors.New("staged isolated policy requires plan-graph-v8")
		}
		return nil
	}
	if isolationContract {
		return errors.New("plan-graph-v7 requires isolated implementation policy")
	}
	if stagedContract {
		return errors.New("plan-graph-v8 requires staged isolated policy")
	}
	if stagedCouplingContract {
		return errors.New("plan-graph-v9 requires coupling-aware staged policy")
	}
	if version == 1 && !(serialContract && parallelVersion == 0 || parallelContract && parallelVersion == 1) ||
		version == 0 && (serialContract || parallelContract || parallelVersion != 0) {
		return errors.New("repair planning policy and planner contract must be enabled together")
	}
	if (parallelVersion == 1) != parallelContract {
		return errors.New("parallel implementation policy and planner contract must be enabled together")
	}
	return nil
}

// Append validates semantic state inside journal.Append's exclusive lock. This
// prevents two callers from both authorizing transitions against a stale read.
func Append(path, kind string, payload any) error {
	if kind == "model.access-receipt" {
		return errors.New("model access receipts require runtime journal reconciliation")
	}
	if kind == "model.access-semantic-pending" {
		return errors.New("semantic usage pending proofs require runtime journal reconciliation")
	}
	if kind == "run.created" {
		if err := validateControllerCreationPath(path, payload); err != nil {
			return err
		}
	}
	_, err := journal.Append(path, kind, payload, func(events []journal.Event) error {
		s, err := Replay(events)
		if err != nil {
			return err
		}
		return validateControllerJournalPath(path, s.Creation, s.RunID)
	})
	return err
}

// Inspect validates integrity and semantic history before returning a snapshot.
func Inspect(path string) (Snapshot, error) {
	events, err := journal.Read(path)
	if err != nil {
		return Snapshot{}, errors.Join(ErrAutonomousUnsafe, err)
	}
	s, err := Replay(events)
	if err != nil {
		return s, errors.Join(ErrAutonomousUnsafe, err)
	}
	if err := validateControllerJournalPath(path, s.Creation, s.RunID); err != nil {
		return s, errors.Join(ErrAutonomousUnsafe, err)
	}
	if len(events) > 0 {
		s.ControllerHead = events[len(events)-1].Hash
		s.ControllerSequence = events[len(events)-1].Sequence
	}
	return s, nil
}
