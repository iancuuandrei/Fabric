package control

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/controllerstate"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestLifecycleUnresolvedIncludesUnknownGraphIsolation(t *testing.T) {
	got := lifecycleUnresolved(Snapshot{GraphIsolations: map[string]GraphIsolationState{"impl": {Outcome: "UNKNOWN"}}})
	if len(got) != 1 || got[0] != "graph-isolation:impl" {
		t.Fatalf("unknown isolation omitted: %v", got)
	}
}

func TestIsolatedImplementationPolicyRequiresBoundedGraphRepair(t *testing.T) {
	legacyJSON, err := json.Marshal(ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 1})
	if err != nil || string(legacyJSON) != `{"mode":"autonomous-v1","max_repairs":1}` {
		t.Fatalf("legacy policy bytes changed: %s %v", legacyJSON, err)
	}
	base := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 1, GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, RepairPlanningVersion: 1, IsolatedImplementationVersion: 1}
	capacity := &engineeringplan.ResourceCapacity{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, TotalRuntimeSlots: 1, ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "p", Slots: 1}}, ModelSlots: []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "p", Model: "m"}, Slots: 1}}, RuntimeSlots: []engineeringplan.RuntimeSlotLimit{{Runtime: engineeringplan.RuntimeResourceKey{ProfileID: "x", Provider: "p", Model: "m"}, Slots: 1}}}
	base.IsolationCapacity, base.IsolationEstimate = capacity, &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ExecutionPolicy){
		"graph":    func(p *ExecutionPolicy) { p.GraphVersion = 0 },
		"repair":   func(p *ExecutionPolicy) { p.RepairPlanningVersion = 0 },
		"context":  func(p *ExecutionPolicy) { p.Context = "" },
		"version":  func(p *ExecutionPolicy) { p.IsolatedImplementationVersion = 4 },
		"parallel": func(p *ExecutionPolicy) { p.ParallelImplementationVersion = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			p := base
			change(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("invalid isolation policy admitted")
			}
		})
	}
	waves := base
	waves.IsolatedImplementationVersion = 2
	if err := waves.Validate(); err != nil {
		t.Fatalf("versioned wave policy rejected: %v", err)
	}
}

func TestIsolationEstimateAllowsZeroWriterVerificationSlots(t *testing.T) {
	valid := IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 0, RuntimeSlots: 1}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.VerificationSlots = -1
	if err := valid.Validate(); err == nil {
		t.Fatal("negative verification estimate admitted")
	}
}

func TestIsolatedCreationRequiresV7Contract(t *testing.T) {
	p := &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 1, GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, RepairPlanningVersion: 1, IsolatedImplementationVersion: 1, IsolationCapacity: &engineeringplan.ResourceCapacity{}, IsolationEstimate: &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, RuntimeSlots: 1}}
	for contract, want := range map[string]bool{"plan-graph-v7": true, plannerContractGraphV3: false, plannerContractGraphV4: false} {
		if err := validateRepairPlanningBinding(Creation{Config: config.Config{PlannerContract: contract}, Execution: p}); (err == nil) != want {
			t.Fatalf("contract %s admitted=%v err=%v", contract, err == nil, err)
		}
	}
	waves := *p
	waves.IsolatedImplementationVersion = 2
	if err := validateRepairPlanningBinding(Creation{Config: config.Config{PlannerContract: "plan-graph-v7"}, Execution: &waves}); err != nil {
		t.Fatalf("wave policy did not bind plan-graph-v7: %v", err)
	}
}

func TestCreateTaskIsolationUsesDistinctPristineTaskWorktree(t *testing.T) {
	c := graphCreation(t, 1)
	c.Execution.RepairPlanningVersion = 1
	c.Execution.IsolatedImplementationVersion = 1

	c.Config.PlannerContract = "plan-graph-v7"
	c.Config.WriterContract = "anchored-edits-v1"
	c.Config.ExplorerContract = "json-v2"

	profileID, err := canonical.Hash("harness.isolation-writer-profile.v1", *c.Config.Writer)
	if err != nil {
		t.Fatal(err)
	}
	route := engineeringplan.RuntimeResourceKey{ProfileID: profileID, Provider: "deterministic", Model: "explicit-writer"}
	c.Execution.IsolationEstimate = &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	c.Execution.IsolationCapacity = &engineeringplan.ResourceCapacity{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, TotalRuntimeSlots: 1, ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "deterministic", Slots: 1}}, ModelSlots: []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "deterministic", Model: "explicit-writer"}, Slots: 1}}, RuntimeSlots: []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 1}}}
	c.Config.ControllerStateRoot = t.TempDir()
	autonomousGitInit(t, c.Repository.Root)
	c.Repository, err = repository.Discover(context.Background(), c.Repository.Root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := canonical.Hash("harness.run.v1", c)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := controllerstate.Initialize(paths, c.Repository); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.Runs, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := paths.Run(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	graph := directGraphFixture()
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := plannerInvocationWithContextsAndRecipe(c.Config, c.Objective, nil, nil, c.Execution)
	if err != nil {
		t.Fatal(err)
	}
	model := inv.Profile.Model
	if err := Append(path, "plan.recorded", runtime.Result{Version: 1, InvocationID: inv.ID, Requested: inv.Profile, ObservedModel: &model, Output: string(raw)}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: machinePolicyID(t, s), Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	s, err = recordGraphFixtureForIsolation(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareGraphIsolationCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	first, err := CreateTaskIsolation(context.Background(), path, "one")
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != "CONFIRMED" || first.Binding == nil || first.Candidate == nil || first.Binding.Request.Path == s.Creation.Repository.Root {
		t.Fatalf("isolation not confirmed/distinct: %+v", first)
	}
	again, err := CreateTaskIsolation(context.Background(), path, "one")
	if err != nil || again.Intent.Request != first.Intent.Request {
		t.Fatalf("confirmed isolation not idempotently inspectable: %v", err)
	}
	current, err := Inspect(path)
	if err != nil || current.Workspace == nil {
		t.Fatal(err)
	}
	state := current.GraphIsolations["one"]
	dirty := *state.Candidate
	dirty.FilesHash = strings.Repeat("f", 64)
	forged := current
	forged.GraphIsolations = map[string]GraphIsolationState{"one": {Intent: state.Intent, Outcome: "UNKNOWN"}}
	payload, err := canonical.Bytes(GraphIsolationReceipt{Intent: state.Intent, Binding: *state.Binding, Candidate: dirty})
	if err != nil {
		t.Fatal(err)
	}
	if err := replayGraphIsolation(&forged, journal.Event{Kind: "graph.isolate-confirmed", Payload: payload}); err == nil {
		t.Fatal("same-commit dirty child receipt admitted")
	}
	if err := os.WriteFile(current.Workspace.Request.Path+string(os.PathSeparator)+"file.txt", []byte("later parent candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path); err != nil {
		t.Fatalf("historical isolation replay read live parent bytes: %v", err)
	}
}

func TestIsolationCohortSelectorPolicyValidation(t *testing.T) {
	legacyJSON, err := json.Marshal(ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 1})
	if err != nil || string(legacyJSON) != `{"mode":"autonomous-v1","max_repairs":1}` {
		t.Fatalf("legacy policy bytes changed: %s %v", legacyJSON, err)
	}
	base := ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 1, GraphVersion: 1, MaxParallel: 1, Context: taskContextBoundedV1, RepairPlanningVersion: 1, IsolatedImplementationVersion: 3}
	capacity := &engineeringplan.ResourceCapacity{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, TotalRuntimeSlots: 1, ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: "p", Slots: 1}}, ModelSlots: []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: "p", Model: "m"}, Slots: 1}}, RuntimeSlots: []engineeringplan.RuntimeSlotLimit{{Runtime: engineeringplan.RuntimeResourceKey{ProfileID: "x", Provider: "p", Model: "m"}, Slots: 1}}}
	base.IsolationCapacity, base.IsolationEstimate = capacity, &IsolationEstimateTemplate{CPUMilli: 1, MemoryMiB: 1, VerificationSlots: 1, RuntimeSlots: 1}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	lexicographic := base
	lexicographic.IsolationCohortSelectorVersion = 1
	if err := lexicographic.Validate(); err != nil {
		t.Fatalf("staged lexicographic selector rejected: %v", err)
	}
	couplingAware := base
	couplingAware.IsolationCohortSelectorVersion = 2
	if err := couplingAware.Validate(); err != nil {
		t.Fatalf("staged coupling-aware selector rejected: %v", err)
	}
	raw, err := json.Marshal(lexicographic)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip ExecutionPolicy
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.IsolationCohortSelectorVersion != 1 {
		t.Fatalf("selector did not survive serialization: %+v", roundTrip)
	}
	for name, change := range map[string]func(*ExecutionPolicy){
		"selector_range": func(p *ExecutionPolicy) { p.IsolationCohortSelectorVersion = 9 },
		"selector_serial": func(p *ExecutionPolicy) {
			p.IsolatedImplementationVersion = 0
			p.IsolationCapacity, p.IsolationEstimate = nil, nil
			p.IsolationCohortSelectorVersion = 1
		},
		"selector_waves": func(p *ExecutionPolicy) {
			p.IsolatedImplementationVersion = 2
			p.IsolationCohortSelectorVersion = 1
		},
		"selector_single": func(p *ExecutionPolicy) {
			p.IsolatedImplementationVersion = 1
			p.IsolationCohortSelectorVersion = 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := lexicographic
			change(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("invalid cohort selector policy admitted")
			}
		})
	}
	absent, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(absent), "cohort_selector") {
		t.Fatalf("absent selector changed legacy serialization: %s", absent)
	}
}

func recordGraphFixtureForIsolation(path string) (Snapshot, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if _, err := ensureGraphRecorded(path, s); err != nil {
		return s, err
	}
	return Inspect(path)
}
