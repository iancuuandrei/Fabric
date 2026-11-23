package control

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestIsolatedPlannerReceivesBoundedWholeCohortConstraints(t *testing.T) {
	c := config.Config{PlannerContract: plannerContractGraphV7, Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	route := engineeringplan.RuntimeResourceKey{ProfileID: "fixture", Provider: "deterministic", Model: "fixture"}
	capacity := &engineeringplan.ResourceCapacity{CPUMilli: 3000, MemoryMiB: 1536, VerificationSlots: 1, TotalRuntimeSlots: 3,
		ProviderSlots: []engineeringplan.ProviderSlotLimit{{Provider: route.Provider, Slots: 3}}, ModelSlots: []engineeringplan.ModelSlotLimit{{Model: engineeringplan.ProviderModelKey{Provider: route.Provider, Model: route.Model}, Slots: 3}}, RuntimeSlots: []engineeringplan.RuntimeSlotLimit{{Runtime: route, Slots: 3}}}
	policy := &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 3, Context: taskContextBoundedV1, RepairPlanningVersion: 1, IsolatedImplementationVersion: 1,
		IsolationCapacity: capacity, IsolationEstimate: &IsolationEstimateTemplate{CPUMilli: 1000, MemoryMiB: 512, VerificationSlots: 0, RuntimeSlots: 1}}
	i, err := plannerInvocationWithContextsAndRecipe(c, "implement independent changes", nil, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string                       `json:"instruction"`
		Objective   string                       `json:"objective"`
		Isolation   *plannerIsolationConstraints `json:"isolation"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Isolation == nil || payload.Isolation.MaxParallel != 3 || !reflect.DeepEqual(payload.Isolation.Capacity, capacity) || !reflect.DeepEqual(payload.Isolation.Estimate, policy.IsolationEstimate) {
		t.Fatal("planner did not receive exact bounded resource policy")
	}
	for _, requirement := range []string{"at most 3 initial implementation tasks", "cannot depend on each other", "complete initial cohort must fit", "generator tools/templates", "every initial implementation", "Preserve public API"} {
		if !strings.Contains(payload.Instruction, requirement) {
			t.Fatalf("missing isolated planning requirement: %s", requirement)
		}
	}
	if payload.Objective != "implement independent changes" {
		t.Fatal("objective rewritten")
	}
	for _, invalid := range []*ExecutionPolicy{nil, {Mode: "autonomous-v1", MaxRepairs: 2, GraphVersion: 1, MaxParallel: 3}} {
		if _, err := plannerInvocationWithContextsAndRecipe(c, "objective", nil, nil, invalid); err == nil {
			t.Fatal("isolated contract without isolated policy admitted")
		}
	}
}

func TestLegacyPlannerContractOmitsNewIsolationInput(t *testing.T) {
	c := config.Config{PlannerContract: plannerContractGraphV5, Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "none", Role: "planner"}}
	i, err := plannerInvocationWithContextsAndRecipe(c, "legacy objective", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(i.Input, `"isolation"`) {
		t.Fatal("new constraints changed legacy planner input")
	}
}
