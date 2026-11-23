package cli

import (
	"errors"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"os"
)

type autonomousCapabilities struct {
	dynamicExplorers                                     bool
	agentContext                                         bool
	workingContext                                       bool
	parallel                                             bool
	isolation                                            *isolatedWriterPolicyFile
	isolationWaves                                       bool
	isolationStaged                                      bool
	cohortSelector                                       int
	plannerContext, parser, parserHash                   string
	parseCache, plannerPPR, reviewImpact, candidateCache int
	autoCompact                                          int64
	evidence                                             *control.EvidenceAutoPolicy
	contextSelector                                      string
	fallbacks                                            []control.CapabilityFallback
}

// isolatedOpenCodeWriterConfigured reports whether the configured writer is
// the opt-in isolated/staged OpenCode route. Shared-workspace parallel
// writers stay Codex-only; isolation admission here never widens them.
func isolatedOpenCodeWriterConfigured(cfg config.Config) bool {
	return cfg.Writer != nil && cfg.Writer.Runtime == "opencode-http"
}

// Resolve preferences only before run creation. Invalid policies, mismatched
// binary hashes, unsafe state paths and all existing-run inputs stay strict.
func (o *autonomousCapabilities) resolve(cfg config.Config) error {
	if (o.workingContext || o.dynamicExplorers) && (cfg.Version != 2 || cfg.Explorer == nil || cfg.Explorer.Runtime != "codex-app-server" && cfg.Explorer.Runtime != "fake") {
		return errors.New("working-context requires configuration v2 with a Codex or fixture explorer")
	}
	add := func(capability, reason, selected string) {
		o.fallbacks = append(o.fallbacks, control.CapabilityFallback{Capability: capability, Disposition: control.GateFallback, Reason: reason, Selected: selected})
	}
	if o.parser != "" {
		if _, err := os.Stat(o.parser); errors.Is(err, os.ErrNotExist) {
			add("planner_context", "parser_unavailable", autonomousPlannerContextSourceBoundedV1)
			o.plannerContext, o.parser, o.parserHash = autonomousPlannerContextSourceBoundedV1, "", ""
			o.parseCache, o.plannerPPR, o.reviewImpact, o.candidateCache = 0, 0, 0, 0
		} else if err != nil {
			return err
		}
	}
	if o.parallel || o.isolation != nil || o.isolationWaves || o.isolationStaged {
		reason := ""
		isolationRequested := o.isolation != nil || o.isolationWaves || o.isolationStaged
		if cfg.Writer != nil {
			switch writerRuntime := cfg.Writer.Runtime; {
			case writerRuntime == "codex-app-server" || writerRuntime == "fake":
			case writerRuntime == "opencode-http" && isolationRequested:
				// Opt-in isolated/staged cohorts may use the configured
				// OpenCode writer; shared-workspace parallel writers remain
				// Codex-only and are still rejected at RunWriter dispatch.
			default:
				reason = "runtime_unsupported"
			}
		}
		if isolationRequested {
			if isolatedOpenCodeWriterConfigured(cfg) && (cfg.OpenCode == nil || cfg.Provider == nil || cfg.Access == nil) {
				reason = "external_state_unavailable"
			}
			if o.isolation == nil {
				reason = "external_state_unavailable"
			} else {
				if cfg.ControllerStateRoot == "" {
					reason = "external_state_unavailable"
				}
				c, e := o.isolation.Capacity, o.isolation.Estimate
				if e.CPUMilli > c.CPUMilli || e.MemoryMiB > c.MemoryMiB || e.VerificationSlots > c.VerificationSlots || e.RuntimeSlots > c.TotalRuntimeSlots || e.RuntimeSlots > c.RuntimeSlots {
					reason = "capacity_insufficient"
				}
			}
		}
		if reason != "" {
			add("parallel_writers", reason, "serial_writer")
			o.parallel, o.isolation, o.isolationWaves, o.isolationStaged = false, nil, false, false
			o.cohortSelector = 0
		}
	}
	if o.autoCompact != 0 {
		supportsCompaction := cfg.Planner.Runtime == "codex-app-server"
		for _, p := range []*runtime.Profile{cfg.Writer, cfg.Fixer, cfg.Explorer, cfg.Reviewer} {
			if p != nil && p.Runtime != "codex-app-server" {
				supportsCompaction = false
			}
		}
		if !supportsCompaction {
			add("auto_compaction", "runtime_unsupported", "disabled")
			o.autoCompact = 0
		}
	}
	return nil
}
