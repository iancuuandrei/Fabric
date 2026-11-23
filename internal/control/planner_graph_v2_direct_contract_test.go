package control

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestPlannerGraphV2DirectModeOmitsNativeGateTasks(t *testing.T) {
	c := config.Config{
		PlannerContract: plannerContractGraphV2,
		Planner:         runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "high", Role: "planner"},
	}
	i, err := plannerInvocation(c, "objective")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"In direct mode, include exactly one task total: the implementation task.",
		"Do not include verification or review tasks; the runner performs those native gates.",
	} {
		if !strings.Contains(payload.Instruction, required) {
			t.Errorf("graph-v2 direct-mode instructions omit %q", required)
		}
	}
	if strings.Contains(plannerGraphAssignmentV1, "exactly one task total") {
		t.Fatal("graph-v1 assignment bytes were changed by graph-v2 clarification")
	}
}
