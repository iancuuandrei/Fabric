package control

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

const plannerContractV1 = "plan-v1"

const plannerContractGraphV1 = "plan-graph-v1"
const plannerContractGraphV2 = "plan-graph-v2"
const plannerContractGraphV3 = "plan-graph-v3"
const plannerContractGraphV4 = "plan-graph-v4"
const plannerContractGraphV5 = "plan-graph-v5"
const plannerContractGraphV6 = "plan-graph-v6"
const plannerContractGraphV7 = "plan-graph-v7"
const plannerContractGraphV8 = "plan-graph-v8"
const plannerContractGraphV9 = "plan-graph-v9"

const plannerAssignmentV1 = "As planner, produce an implementation plan using read-only source tools. Editing and testing belong to later roles. Read-only access is expected and is not a blocker. Return the plan without requesting additional capability."

const plannerGraphAssignmentV1 = "As planner, produce a bounded engineering task graph as strict JSON matching the engineeringplan v1 schema (version 1, mode direct|graph, summary, tasks with id/kind/title/scope_paths/write_paths/expected_evidence/estimated_seconds and optional parent_id/dependencies). Use kinds research, design, implementation, verification, review. For graph mode include exactly one implementation with concrete write_paths within scope, research/design dependencies, and verification/review gates depending on the implementation. For direct mode include exactly one implementation with concrete write_paths. Never include completed or attempts; the runner owns progress. Return only the JSON object."

const plannerGraphAssignmentV4 = "As planner, produce a bounded engineering task graph as strict JSON matching the engineeringplan v1 schema (version 1, mode direct|graph, summary, tasks with id/kind/title/scope_paths/write_paths/expected_evidence/estimated_seconds and optional parent_id/dependencies). Use kinds research, design, implementation, verification, review. In graph mode, plan one initial implementation task for work that should remain together. Use two initial implementation tasks only when the work can be split into genuinely independent paths after any shared research/design: each task must own concrete, disjoint write_paths within scope and depend on the shared research/design it needs. Do not duplicate work to fill a slot. Include native verification and review gates in graph mode; both gates must depend on both implementation tasks when there are two, and on the implementation when there is one. In direct mode include exactly one implementation with concrete write_paths and no verification or review tasks; the runner performs native gates. Never include completed or attempts; the runner owns progress. Return only the JSON object."

// plannerInvocation binds the configured planner contract to the exact
// objective. The empty contract deliberately retains the historical raw input
// identity so journals created before planner contracts remain replayable.
// plan-graph-v1 binds the strict autonomous graph instruction and still
// hashes the exact planner result as the PlanID; no extra approval authority
// is introduced.
func plannerInvocation(c config.Config, objective string) (runtime.Invocation, error) {
	return plannerInvocationWithContext(c, objective, nil)
}

// plannerInvocationForSnapshot binds an admitted planner context only for the
// explicit opt-in. Empty policy retains the historic builder byte-for-byte.
func plannerInvocationForSnapshot(s Snapshot) (runtime.Invocation, error) {
	if len(s.PlannerCorrections) != 0 {
		invocation := s.PlannerCorrections[len(s.PlannerCorrections)-1].Invocation
		return invocation, invocation.Validate()
	}
	if !plannerContextEnabled(s) && !plannerGoContextEnabled(s) {
		if s.PlannerContext != nil || s.PlannerGoContext != nil {
			return runtime.Invocation{}, errors.New("planner context present without policy")
		}
		base, err := plannerInvocationWithContextsAndRecipe(s.Creation.Config, s.Creation.Objective, nil, nil, s.Creation.Execution)
		return plannerAgentContextInvocation(s, base, err)
	}
	if plannerContextEnabled(s) && s.PlannerContext == nil {
		return runtime.Invocation{}, errors.New("planner context admission missing")
	}
	if plannerGoContextEnabled(s) && s.PlannerGoContext == nil {
		return runtime.Invocation{}, errors.New("Go planner context admission missing")
	}
	base, err := plannerInvocationWithContextsAndRecipe(s.Creation.Config, s.Creation.Objective, s.PlannerContext, s.PlannerGoContext, s.Creation.Execution)
	return plannerAgentContextInvocation(s, base, err)
}

func plannerInvocationWithContext(c config.Config, objective string, plannerContext *PlannerContextRecord) (runtime.Invocation, error) {
	return plannerInvocationWithContextsAndRecipe(c, objective, plannerContext, nil, nil)
}

func plannerInvocationWithContextAndRecipe(c config.Config, objective string, plannerContext *PlannerContextRecord, recipe *ExecutionPolicy) (runtime.Invocation, error) {
	return plannerInvocationWithContextsAndRecipe(c, objective, plannerContext, nil, recipe)
}

func plannerInvocationWithContextsAndRecipe(c config.Config, objective string, plannerContext *PlannerContextRecord, plannerGoContext *PlannerGoContextRecord, recipe *ExecutionPolicy) (runtime.Invocation, error) {
	input := objective
	if c.PlannerContract != "" || c.ReviewerContract == "json-v1" || plannerContext != nil || plannerGoContext != nil || recipe != nil && recipe.PromptRecipe != "" {
		assignment := plannerAssignmentV1
		var schema json.RawMessage
		var verificationChecks []config.Check
		var isolatedConstraints *plannerIsolationConstraints
		switch c.PlannerContract {
		case "":
		case plannerContractV1:
			assignment = plannerAssignmentV1
		case plannerContractGraphV1:
			assignment = plannerGraphAssignmentV1 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 40 tasks, leaving task slots for bounded repairs.", c.MaxExplorationRecords())
		case plannerContractGraphV2:
			assignment = plannerGraphAssignmentV1 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 40 tasks, leaving task slots for bounded repairs.", c.MaxExplorationRecords()) + " In direct mode, include exactly one task total: the implementation task. Do not include verification or review tasks; the runner performs those native gates. expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification."
			schema = engineeringplan.PlannerJSONSchema()
		case plannerContractGraphV3:
			readBudget := c.MaxExplorationRecords() - 8
			if readBudget < 0 {
				readBudget = 0
			}
			assignment = plannerGraphAssignmentV1 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 32 tasks and at most %d research/design tasks, reserving worst-case capacity for eight four-task repair slots.", readBudget, readBudget) + " In direct mode, include exactly one task total: the implementation task. Do not include verification or review tasks; the runner performs those native gates. expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them."
			schema = engineeringplan.PlannerJSONSchema()
		case plannerContractGraphV4:
			readBudget := c.MaxExplorationRecords() - 8
			if readBudget < 0 {
				readBudget = 0
			}
			assignment = plannerGraphAssignmentV4 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 32 tasks and at most %d research/design tasks, reserving worst-case capacity for eight four-task repair slots.", readBudget, readBudget) + " expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them."
			schema = engineeringplan.PlannerJSONSchema()
		case plannerContractGraphV5:
			readBudget := c.MaxExplorationRecords() - 8
			if readBudget < 0 {
				readBudget = 0
			}
			assignment = plannerGraphAssignmentV1 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 32 tasks and at most %d research/design tasks, reserving worst-case capacity for eight four-task repair slots.", readBudget, readBudget) + " In direct mode, include exactly one task total: the implementation task. Do not include verification or review tasks; the runner performs those native gates. expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them. Generated outputs alone are not independent: assign a generator/template and its generated outputs to one owner, or add an explicit dependency. Verify decomposition assumptions."
			schema = engineeringplan.PlannerJSONSchema()
		case plannerContractGraphV6:
			readBudget := c.MaxExplorationRecords() - 8
			if readBudget < 0 {
				readBudget = 0
			}
			assignment = plannerGraphAssignmentV4 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 32 tasks and at most %d research/design tasks, reserving worst-case capacity for eight four-task repair slots.", readBudget, readBudget) + " expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them. Generated outputs alone are not independent: assign a generator/template and its generated outputs to one owner, or add an explicit dependency. Verify decomposition assumptions."
			schema = engineeringplan.PlannerJSONSchema()
		case plannerContractGraphV7:
			if recipe == nil || (recipe.IsolatedImplementationVersion != 1 && recipe.IsolatedImplementationVersion != 2) || recipe.Validate() != nil {
				return runtime.Invocation{}, errors.New("isolated planner contract requires a valid isolated execution policy")
			}
			readBudget := max(c.MaxExplorationRecords()-8, 0)
			if recipe.IsolatedImplementationVersion == 2 {
				assignment = fmt.Sprintf("As planner, return only strict engineeringplan v1 JSON with version, mode direct|graph, summary and tasks. Use kinds research, design, implementation, verification and review; expected_evidence contains objects with nonempty kind and description. Plan at most 8 initial implementation tasks in total across serial resource-bounded initial waves (each wave at most %d concurrent tasks), at most %d research/design tasks and at most 32 tasks total. Use one implementation for a coupled change; split only genuinely independent concrete disjoint write_paths. Every initial implementation must be ready after shared read-only research/design tasks; initial implementations cannot depend on each other. Assign shared hubs, generator tools/templates and their outputs to one owner. Inspect actual ownership before partitioning, and do not invent files or split solely to occupy capacity. scope_paths bound future repairs; write_paths describe only actual initial edits and must be concrete relative paths, never dot or parents. Each task consumes the declared per-writer resource estimate; each resource-bounded initial wave must fit every supplied capacity before execution. Both verification and review must depend transitively on every initial implementation, with review depending on verification. Direct mode contains exactly one implementation and no gate tasks; the runner adds native gates. Never emit completed or attempts. Preserve public API representations and behavior outside the requested change, including return collection shape and line-ending conventions; expose any ambiguous assumption for verification rather than silently changing it.", recipe.EffectiveMaxParallel(), readBudget)
			} else {
				assignment = fmt.Sprintf("As planner, return only strict engineeringplan v1 JSON with version, mode direct|graph, summary and tasks. Use kinds research, design, implementation, verification and review; expected_evidence contains objects with nonempty kind and description. Plan at most %d initial implementation tasks, at most %d research/design tasks and at most 32 tasks total. Use one implementation for a coupled change; split only genuinely independent concrete disjoint write_paths. Every initial implementation must be ready after shared read-only research/design tasks; initial implementations cannot depend on each other. Assign shared hubs, generator tools/templates and their outputs to one owner. Inspect actual ownership before partitioning, and do not invent files or split solely to occupy capacity. scope_paths bound future repairs; write_paths describe only actual initial edits and must be concrete relative paths, never dot or parents. Each task consumes the declared per-writer resource estimate; the complete initial cohort must fit every supplied capacity before execution. Both verification and review must depend transitively on every initial implementation, with review depending on verification. Direct mode contains exactly one implementation and no gate tasks; the runner adds native gates. Never emit completed or attempts. Preserve public API representations and behavior outside the requested change, including return collection shape and line-ending conventions; expose any ambiguous assumption for verification rather than silently changing it.", recipe.EffectiveMaxParallel(), readBudget)
			}
			schema = engineeringplan.PlannerJSONSchema()
			isolatedConstraints = &plannerIsolationConstraints{MaxParallel: recipe.EffectiveMaxParallel(), Capacity: recipe.IsolationCapacity, Estimate: recipe.IsolationEstimate}
			if recipe.IsolatedImplementationVersion == 2 {
				isolatedConstraints.IsolatedImplementationVersion = 2
			}
		case plannerContractGraphV8:
			if recipe == nil || recipe.IsolatedImplementationVersion != 3 || recipe.Validate() != nil {
				return runtime.Invocation{}, errors.New("staged planner contract requires a valid staged isolated execution policy")
			}
			readBudget := max(c.MaxExplorationRecords()-8, 0)
			assignment = fmt.Sprintf("As planner, return only strict engineeringplan v1 JSON with version, mode direct|graph, summary and tasks. Use kinds research, design, implementation, verification and review; expected_evidence contains objects with nonempty kind and description. Plan at most 8 implementation tasks in total across dependent hub-to-leaf stages (each stage cohort at most %d concurrent tasks), at most %d research/design tasks and at most 32 tasks total. Use one hub implementation for shared work; leaves may depend on completed hub implementations with concrete disjoint write_paths. Every stage cohort must be ready together after its dependencies complete; staged implementations cannot form cycles. Assign shared hubs, generator tools/templates and their outputs to one owner; C3 and lower coupling is advisory only and never grants ownership. Inspect actual ownership before partitioning, and do not invent files or split solely to occupy capacity. scope_paths bound future repairs; write_paths describe only actual edits and must be concrete relative paths, never dot or parents. Each task consumes the declared per-writer resource estimate; each staged cohort wave must fit every supplied capacity before execution. Both verification and review must depend transitively on every implementation, with review depending on verification. Direct mode contains exactly one implementation and no gate tasks; the runner adds native gates. Never emit completed or attempts. Preserve public API representations and behavior outside the requested change; expose any ambiguous assumption for verification rather than silently changing it.", recipe.EffectiveMaxParallel(), readBudget)
			schema = engineeringplan.PlannerJSONSchema()
			isolatedConstraints = &plannerIsolationConstraints{MaxParallel: recipe.EffectiveMaxParallel(), Capacity: recipe.IsolationCapacity, Estimate: recipe.IsolationEstimate, IsolatedImplementationVersion: 3}
		case plannerContractGraphV9:
			if recipe == nil || recipe.IsolatedImplementationVersion != 3 || recipe.Validate() != nil {
				return runtime.Invocation{}, errors.New("coupling-aware staged contract requires a valid staged isolated execution policy")
			}
			if recipe.IsolationCohortSelectorVersion != 2 && recipe.IsolationCohortSelectorVersion != 3 {
				return runtime.Invocation{}, errors.New("plan-graph-v9 requires the coupling-aware cohort selector")
			}
			readBudget := max(c.MaxExplorationRecords()-8, 0)
			assignment = fmt.Sprintf("As planner, return only strict engineeringplan v1 JSON with version, mode direct|graph, summary, tasks and optional typed couplings. Use kinds research, design, implementation, verification and review; expected_evidence contains objects with nonempty kind and description. Plan at most 8 implementation tasks in total across dependent hub-to-leaf stages (each stage cohort at most %d concurrent tasks), at most %d research/design tasks and at most 32 tasks total. Use one hub implementation for shared work; leaves may depend on completed hub implementations with concrete disjoint write_paths. Every stage cohort must be ready together after its dependencies complete; staged implementations cannot form cycles. Assign shared hubs, generator tools/templates and their outputs to one owner. Optionally declare at most 28 typed couplings between implementation tasks with canonical from<to order, level C1 (weak graph proximity/import neighborhood), C2 (moderate same package/shared fixture), C3 (strong direct API/shared config), C4 (hard same generated family/shared mutation), plus bounded reason, planner_declared_advisory provenance and evidence. Observed generator/topology labels are not admitted on this wire; source-derived generator admissibility remains broader Phase E pending, and hashing binds declared risk inputs without proving a cited fact. C4 requires one owner or an explicit implementation dependency and rejects an unsafe split; serial waves from the same parent do not make it safe. C3/C2/C1 are advisory concurrency risk only and never grant readiness, ownership or write authority; absent coupling (C0) never proves independence. Different files do not prove independence. Partial evidence never proves absence. Inspect actual ownership before partitioning, and do not invent files or split solely to occupy capacity. scope_paths bound future repairs; write_paths describe only actual edits and must be concrete relative paths, never dot or parents. Each task consumes the declared per-writer resource estimate; each staged cohort wave must fit every supplied capacity before execution. Both verification and review must depend transitively on every implementation, with review depending on verification. Direct mode contains exactly one implementation and no gate tasks; the runner adds native gates. Never emit completed or attempts. Preserve public API representations and behavior outside the requested change; expose any ambiguous assumption for verification rather than silently changing it.", recipe.EffectiveMaxParallel(), readBudget)
			schema = engineeringplan.PlannerJSONSchemaWithCouplings()
			isolatedConstraints = &plannerIsolationConstraints{MaxParallel: recipe.EffectiveMaxParallel(), Capacity: recipe.IsolationCapacity, Estimate: recipe.IsolationEstimate, IsolatedImplementationVersion: 3}
		default:
			return runtime.Invocation{}, errors.New("unsupported planner contract")
		}
		if recipe != nil && recipe.PlannerContext == plannerContextGoSourceV2 {
			assignment += " Preserve observable public API behavior and representations outside the requested change. Resolve ambiguous compatibility assumptions from existing callers and tests before editing. Generated code must follow its observed directive, tool sources and templates; plan one owner for that chain and do not invent an inactive generator. Partial evidence never proves absence."
		}
		if c.ReviewerContract == "json-v1" {
			verificationChecks = append([]config.Check(nil), c.Verification...)
			assignment += " The verification gate may claim only the configured verification checks supplied in verification_checks. Treat additional checks as planned recommendations only; never imply they ran or passed without recorded observations."
			assignment += " Prefer direct mode for localized changes with known scope. Use graph mode for substantial or dependent work; independent read-only tasks may run concurrently, and research/design tasks are not mandatory for every small change."
		}
		wrapped, err := promptRecipeBytes(recipe, struct {
			OutputSchema       json.RawMessage              `json:"output_schema,omitempty"`
			Role               string                       `json:"role"`
			Instruction        string                       `json:"instruction"`
			Objective          string                       `json:"objective"`
			VerificationChecks []config.Check               `json:"verification_checks,omitempty"`
			PlannerContext     *PlannerContextRecord        `json:"planner_context,omitempty"`
			PlannerGoContext   *plannerGoContextPrompt      `json:"planner_go_context,omitempty"`
			Isolation          *plannerIsolationConstraints `json:"isolation,omitempty"`
		}{schema, "planner", assignment, objective, verificationChecks, plannerContext, plannerGoContextPromptFor(plannerGoContext), isolatedConstraints})
		if err != nil {
			return runtime.Invocation{}, err
		}
		input = string(wrapped)
	}
	profile, err := modelProfileForInput(c, "planner", input, 0)
	if err != nil {
		return runtime.Invocation{}, err
	}
	return runtime.NewInvocationWithCodexAutoCompact(profile, input, codexAutoCompactForExecution(recipe, profile))
}

type plannerIsolationConstraints struct {
	MaxParallel                   int                               `json:"max_parallel"`
	Capacity                      *engineeringplan.ResourceCapacity `json:"capacity"`
	Estimate                      *IsolationEstimateTemplate        `json:"estimate"`
	IsolatedImplementationVersion int                               `json:"isolated_implementation_version,omitempty"`
}
