package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/iancuuandrei/fabric/internal/acpruntime"
	"github.com/iancuuandrei/fabric/internal/clauderuntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/directapi"
	"github.com/iancuuandrei/fabric/internal/hostenvironment"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/verification"
)

type hostObserver func(context.Context) (hostenvironment.Observation, error)

func bindCurrentHost(ctx context.Context, creation control.Creation) (control.Creation, error) {
	observation, err := hostenvironment.ObserveDefault(ctx)
	if err != nil {
		return control.Creation{}, err
	}
	policy := control.DefaultHostPolicy()
	if creation.Config.HostPolicy != nil {
		policy = *creation.Config.HostPolicy
	}
	return control.BindHostAdmission(creation, observation, policy)
}

func writeDoctor(ctx context.Context, out io.Writer, identity repository.Identity, policy *hostenvironment.Policy, configurations ...config.Config) error {
	return writeDoctorWithObserver(ctx, out, identity, hostenvironment.ObserveDefault, policy, configurations...)
}

func writeDoctorWithObserver(ctx context.Context, out io.Writer, identity repository.Identity, observe hostObserver, policy *hostenvironment.Policy, configurations ...config.Config) error {
	host, err := observe(ctx)
	if err != nil {
		return err
	}
	selected := control.DefaultHostPolicy()
	if policy != nil {
		selected = *policy
	}
	if err := hostenvironment.Admit(selected, host); err != nil {
		return err
	}
	report := map[string]any{
		"status":           "PASS",
		"repository":       identity,
		"host_environment": host,
		"runtime_dispatch": "NOT_RUN",
		"verification":     "NOT_RUN",
	}
	if len(configurations) != 0 {
		plan, err := doctorExecutionPlan(identity, configurations[0])
		if err != nil {
			return err
		}
		report["execution_plan"] = plan
	}
	return output(out, report)
}

func doctorExecutionPlan(identity repository.Identity, cfg config.Config) (map[string]any, error) {
	if err := requireOpenCodeStateRoot(cfg); err != nil {
		return nil, err
	}
	id, err := identity.ID()
	if err != nil {
		return nil, err
	}
	roles := map[string]*runtime.Profile{"planner": &cfg.Planner, "writer": cfg.Writer, "fixer": cfg.Fixer, "explorer": cfg.Explorer, "reviewer": cfg.Reviewer}
	compactSupported := true
	for _, profile := range roles {
		if profile != nil && profile.Runtime != "codex-app-server" {
			compactSupported = false
		}
	}
	parallelSupported := cfg.Writer != nil && (cfg.Writer.Runtime == "codex-app-server" || cfg.Writer.Runtime == "fake")
	resources := doctorMemoryResources(memoryadmission.Observe())
	resources["shared_task_pool"] = "NOT_CONFIGURED"
	if cfg.TaskPool != nil {
		resources["shared_task_pool"] = "CONFIGURED_NOT_ADMITTED"
		resources["limits"] = cfg.TaskPool.Limits
	}
	checks := []map[string]string{}
	for _, check := range cfg.Verification {
		invocation, err := verification.Prepare(id, identity.Root, check)
		if err != nil {
			return nil, err
		}
		readiness := "AVAILABLE_NOT_RUN"
		if invocation.Executable == nil {
			readiness = "UNAVAILABLE"
		}
		checks = append(checks, map[string]string{"name": check.Name, "readiness": readiness})
	}
	plan := map[string]any{"roles": roles, "verification_checks": checks,
		"scope_replan": map[string]any{"configured_roles_available": cfg.Writer != nil && cfg.Explorer != nil, "default_topology": "serial_writer", "serial_policy_version": 1, "cohort_policy_version": 2, "max_replans": 2, "runtime_execution": "NOT_RUN"},
		"topology":     "serial_default", "context": "source_bounded_default",
		"run_options": "NOT_SELECTED", "provider_qualification": "NOT_RUN",
		"runtime_support": map[string]bool{"parallel_writers": parallelSupported, "auto_compaction": compactSupported},
		"resources":       resources, "cache": "RUN_OPTIONS_NOT_SELECTED"}
	if readiness := doctorExternalRuntimes(cfg); len(readiness) > 0 {
		plan["external_runtimes"] = readiness
	}
	if len(cfg.Hooks) > 0 {
		hookChecks := make([]map[string]string, 0, len(cfg.Hooks))
		for _, h := range cfg.Hooks {
			status := "AVAILABLE_NOT_RUN"
			if _, lookErr := exec.LookPath(h.Argv[0]); lookErr != nil {
				status = "UNAVAILABLE"
			}
			hookChecks = append(hookChecks, map[string]string{"name": h.Name, "event": h.Event, "readiness": status})
		}
		plan["hooks"] = hookChecks
	}
	return plan, nil
}

func doctorExternalRuntimes(cfg config.Config) map[string]any {
	out := map[string]any{}
	if cfg.Claude != nil {
		status := "READY"
		if hash, err := clauderuntime.HashFile(cfg.Claude.Executable); err != nil {
			status = "EXECUTABLE_UNAVAILABLE"
		} else if cfg.Claude.ExecutableHash != "" && hash != cfg.Claude.ExecutableHash {
			status = "EXECUTABLE_HASH_MISMATCH"
		}
		out["claude-cli"] = map[string]any{"executable": cfg.Claude.Executable, "status": status}
	}
	if cfg.ACP != nil {
		status := "READY"
		if hash, err := acpruntime.HashFile(cfg.ACP.Executable); err != nil {
			status = "EXECUTABLE_UNAVAILABLE"
		} else if cfg.ACP.ExecutableHash != "" && hash != cfg.ACP.ExecutableHash {
			status = "EXECUTABLE_HASH_MISMATCH"
		}
		out["acp-stdio"] = map[string]any{"executable": cfg.ACP.Executable, "status": status}
	}
	if cfg.DirectAPI != nil {
		keyEnv := strings.TrimSpace(cfg.DirectAPI.APIKeyEnv)
		if keyEnv == "" {
			keyEnv = directapi.DefaultAPIKeyEnv(cfg.DirectAPI.Dialect)
		}
		baseURL := strings.TrimSpace(cfg.DirectAPI.BaseURL)
		if baseURL == "" {
			baseURL = directapi.DefaultBaseURL(cfg.DirectAPI.Dialect)
		}
		hasKey := os.Getenv(keyEnv) != ""
		status := "READY"
		lower := strings.ToLower(baseURL)
		loopback := strings.HasPrefix(lower, "http://127.0.0.1") || strings.HasPrefix(lower, "http://localhost") || strings.HasPrefix(lower, "http://[::1]")
		if !hasKey && !loopback {
			status = "API_KEY_MISSING"
		}
		out["direct-api"] = map[string]any{
			"dialect":        cfg.DirectAPI.Dialect,
			"base_url":       baseURL,
			"api_key_env":    keyEnv,
			"api_key_present": hasKey,
			"status":         status,
		}
	}
	return out
}

func doctorMemoryResources(observation memoryadmission.Observation) map[string]any {
	return map[string]any{
		"live_memory_pressure": observation.Status,
		"memory_observation":   observation,
		"memory_admission":     "NOT_ADMITTED",
		"worker_memory_usage":  "NOT_MEASURED",
	}
}

func inspectAutonomousPlan(ctx context.Context, root string, options autonomousCapabilities, maxParallel int, out io.Writer) error {
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	if err := options.resolve(cfg); err != nil {
		return err
	}
	// Shared preflight also runs inside doctorExecutionPlan below; this early
	// check keeps inspect-plan read-only without repository side effects.
	if err := requireOpenCodeStateRoot(cfg); err != nil {
		return err
	}
	if options.parser != "" {
		parser := ri.Client{Executable: options.parser, ExecutableHash: options.parserHash}
		if err := parser.ValidateExecutable(); err != nil {
			return err
		}
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	plan, err := doctorExecutionPlan(identity, cfg)
	if err != nil {
		return err
	}
	contextMode := options.plannerContext
	if contextMode == "" {
		contextMode = autonomousPlannerContextSourceBoundedV1
	}
	topology := "serial_writer"
	if options.parallel {
		topology = "parallel_writers"
	}
	if options.isolation != nil {
		topology = "isolated_writers"
		if options.isolationWaves {
			topology = "isolated_writer_waves"
		}
		if options.isolationStaged {
			topology = "isolated_writer_staged"
		}
		plan["isolation_capacity"] = options.isolation.Capacity
		plan["writer_estimate"] = options.isolation.Estimate
		plan["cohort_selector"] = cohortSelectorName(options.cohortSelector)
		resources := plan["resources"].(map[string]any)
		observation := resources["memory_observation"].(memoryadmission.Observation)
		decision, decisionErr := memoryadmission.Next(nil, observation, maxParallel, options.isolation.Estimate.MemoryMiB, memoryadmission.DefaultReserveMiB, memoryadmission.DefaultHysteresisMiB)
		if decisionErr != nil {
			return decisionErr
		}
		resources["memory_admission_preview"] = decision
		resources["memory_admission_preview_status"] = "ESTIMATED_NOT_ADMITTED"
		resources["memory_admission_recheck"] = "BEFORE_FUTURE_ISOLATED_WRITER_CLAIMS"
	}
	if options.evidence != nil {
		if err := control.ValidateEvidenceAutoPolicyTemplate(*options.evidence); err != nil {
			return err
		}
		if options.parallel || options.isolation != nil || options.isolationWaves || options.isolationStaged {
			return errors.New("evidence-policy requires serial graph writers; parallel-writers and isolated-writers are incompatible")
		}
		if len(options.fallbacks) != 0 {
			for _, fallback := range options.fallbacks {
				if fallback.Capability == "parallel_writers" {
					return errors.New("evidence-policy requires serial graph writers; capability fallback cannot silently drop this policy")
				}
			}
		}
		plan["evidence_policy"] = map[string]any{"version": options.evidence.Version, "actions": len(options.evidence.Model.Actions), "queries": len(options.evidence.Queries), "scope": "one_automatic_serial_graph_acquisition_before_initial_writer", "estimates": "caller_supplied_unvalidated_advisory", "runtime_dispatch": "NOT_RUN"}
	} else {
		plan["evidence_policy"] = map[string]any{"enabled": false, "runtime_dispatch": "NOT_RUN"}
	}
	plan["context"] = contextMode
	plan["context_selector"] = options.contextSelector
	plan["planner_ppr"] = options.plannerPPR
	plan["agent_context"] = map[string]any{"enabled": options.agentContext, "source": "COMMITTED_TREE", "skills": "ROLE_FILTERED_METADATA_AND_TASK_SELECTED_BODIES", "runtime_execution": "NOT_RUN"}
	plan["working_context"] = map[string]any{"enabled": options.workingContext, "role": "DYNAMIC_EXPLORER_FOLLOW_UP", "authority": "NONE", "max_content_bytes": 16384, "runtime_execution": "NOT_RUN"}
	plan["dynamic_explorers"] = map[string]any{"enabled": options.dynamicExplorers, "runtime_execution": "NOT_RUN"}
	plan["topology"] = topology
	scopeVersion := 1
	scopeEnabled := cfg.Writer != nil && cfg.Explorer != nil
	if topology != "serial_writer" {
		scopeVersion = 2
	}
	if topology == "isolated_writer_waves" {
		scopeVersion = 0
		scopeEnabled = false
	}
	plan["scope_replan"] = map[string]any{"enabled": scopeEnabled, "policy_version": scopeVersion, "max_replans": 2, "ownership_ceiling": "IMMUTABLE_SCOPE_PATHS", "design": "READ_ONLY_MODEL_DECISION", "runtime_execution": "NOT_RUN"}
	plan["max_parallel"] = maxParallel
	plan["run_options"] = "SELECTED_NOT_DISPATCHED"
	plan["cache"] = map[string]int{"parser_facts": options.parseCache, "candidate_facts": options.candidateCache}
	plan["auto_compact_token_limit"] = options.autoCompact
	plan["fallbacks"] = options.fallbacks
	return output(out, map[string]any{"execution_plan": plan, "runtime_dispatch": "NOT_RUN", "verification": "NOT_RUN"})
}

// cohortSelectorName renders the staged wave selector for inspect-plan
// display. Empty preserves the frozen greedy derivation; lexicographic-v1
// is the exact finite optimum over the same hard gates; coupling-aware-v1
// is the typed C1-C4 optimum with the C4 hard gate (plan-graph-v9);
// observed-coupling-v1 is the source-observed optimum merged at MAX severity
// under plan-graph-v9.
func cohortSelectorName(version int) string {
	if version == 1 {
		return autonomousCohortSelectorLexicographicV1
	}
	if version == 2 {
		return autonomousCohortSelectorCouplingAwareV1
	}
	if version == 3 {
		return autonomousCohortSelectorObservedCouplingV1
	}
	return ""
}
