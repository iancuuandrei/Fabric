package cli

import (
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutonomousCapabilitiesFallBackBeforeDispatch(t *testing.T) {
	cfg := config.Config{Planner: runtime.Profile{Runtime: "opencode-http"}, Writer: &runtime.Profile{Runtime: "opencode-http"}}
	o := autonomousCapabilities{parallel: true, plannerContext: autonomousPlannerContextGoContractV3,
		parser: filepath.Join(t.TempDir(), "absent.exe"), parserHash: "retained-pin", parseCache: 1, plannerPPR: 1, reviewImpact: 1, candidateCache: 1, autoCompact: 10000}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.parallel || o.plannerContext != autonomousPlannerContextSourceBoundedV1 || o.parser != "" || o.parserHash != "" || o.parseCache != 0 || o.plannerPPR != 0 || o.reviewImpact != 0 || o.candidateCache != 0 || o.autoCompact != 0 || len(o.fallbacks) != 3 {
		t.Fatalf("capabilities did not degrade: %#v", o)
	}
	for _, f := range o.fallbacks {
		if f.Validate() != nil || f.Disposition != control.GateFallback {
			t.Fatal("invalid recorded fallback", f)
		}
	}
}

func TestAutonomousCapabilitiesPreserveExistingParserPin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ri.exe")
	if err := os.WriteFile(p, []byte("wrong binary"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Planner: runtime.Profile{Runtime: "codex-app-server"}, Writer: &runtime.Profile{Runtime: "codex-app-server"}}
	o := autonomousCapabilities{parallel: true, parser: p, parserHash: "must-remain-pinned", plannerContext: autonomousPlannerContextGoContractV3, autoCompact: 10000}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.parser != p || o.parserHash != "must-remain-pinned" || !o.parallel || o.autoCompact != 10000 || len(o.fallbacks) != 0 {
		t.Fatal("an existing parser pin was silently substituted")
	}
}

func TestAutonomousIsolationUnavailableFallsBackToSerial(t *testing.T) {
	cfg := config.Config{Writer: &runtime.Profile{Runtime: "codex-app-server"}}
	o := autonomousCapabilities{isolation: &isolatedWriterPolicyFile{Capacity: isolatedWriterLimits{CPUMilli: 1, MemoryMiB: 1, TotalRuntimeSlots: 1, RuntimeSlots: 1}, Estimate: control.IsolationEstimateTemplate{CPUMilli: 2, MemoryMiB: 2, RuntimeSlots: 1}}}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.isolation != nil || len(o.fallbacks) != 1 || o.fallbacks[0].Reason != "capacity_insufficient" {
		t.Fatal("insufficient optional isolation did not degrade")
	}
	o = autonomousCapabilities{isolation: &isolatedWriterPolicyFile{}}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.isolation != nil || o.fallbacks[0].Reason != "external_state_unavailable" {
		t.Fatal("missing external state did not select serial execution")
	}
}

func TestAutonomousCompactionFallsBackForMixedRuntimes(t *testing.T) {
	cfg := config.Config{Planner: runtime.Profile{Runtime: "codex-app-server"}, Writer: &runtime.Profile{Runtime: "opencode-http"}}
	o := autonomousCapabilities{autoCompact: 10000}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.autoCompact != 0 || len(o.fallbacks) != 1 || o.fallbacks[0].Capability != "auto_compaction" {
		t.Fatal("mixed runtimes retained an unsupported global compaction option")
	}
}

func TestAutonomousIsolationAdmitsOpenCodeWriter(t *testing.T) {
	cfg := config.Config{
		Writer:              &runtime.Profile{Runtime: "opencode-http"},
		ControllerStateRoot: t.TempDir(),
		OpenCode:            &config.OpenCodeHost{Version: 1, Executable: `D:\tools\opencode.exe`, ExecutableHash: strings.Repeat("e", 64), StateRoot: t.TempDir()},
		Provider:            &config.Provider{Version: 1},
		Access:              &config.Access{},
	}
	o := autonomousCapabilities{isolation: &isolatedWriterPolicyFile{Capacity: isolatedWriterLimits{CPUMilli: 4000, MemoryMiB: 8192, TotalRuntimeSlots: 4, RuntimeSlots: 2}, Estimate: control.IsolationEstimateTemplate{CPUMilli: 1000, MemoryMiB: 2048, RuntimeSlots: 1}}}
	if err := o.resolve(cfg); err != nil {
		t.Fatal(err)
	}
	if o.isolation == nil {
		t.Fatalf("opt-in isolated opencode-http writer was not admitted: fallbacks=%+v", o.fallbacks)
	}
	bad := cfg
	bad.OpenCode = nil
	bad.Provider = nil
	bad.Access = nil
	o = autonomousCapabilities{isolation: &isolatedWriterPolicyFile{Capacity: isolatedWriterLimits{CPUMilli: 4000, MemoryMiB: 8192, TotalRuntimeSlots: 4, RuntimeSlots: 2}, Estimate: control.IsolationEstimateTemplate{CPUMilli: 1000, MemoryMiB: 2048, RuntimeSlots: 1}}}
	if err := o.resolve(bad); err != nil {
		t.Fatal(err)
	}
	if o.isolation != nil || len(o.fallbacks) != 1 {
		t.Fatal("isolated opencode-http without provider state was not degraded to serial")
	}
}
