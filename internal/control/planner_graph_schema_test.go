package control

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestPlannerGraphV2BindsSchemaWithoutChangingLegacyRecipes(t *testing.T) {
	c := config.Config{Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "high", Role: "planner"}}
	for _, contract := range []string{plannerContractV1, plannerContractGraphV1} {
		c.PlannerContract = contract
		instruction := plannerAssignmentV1
		if contract == plannerContractGraphV1 {
			instruction = plannerGraphAssignmentV1 + fmt.Sprintf(" Use at most %d research/design tasks in total. Keep the initial plan to at most 40 tasks, leaving task slots for bounded repairs.", c.MaxExplorationRecords())
		}
		oldInput, err := canonical.Bytes(struct {
			Role        string `json:"role"`
			Instruction string `json:"instruction"`
			Objective   string `json:"objective"`
		}{"planner", instruction, "objective"})
		if err != nil {
			t.Fatal(err)
		}
		expected, err := runtime.NewInvocation(c.Planner, string(oldInput))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := plannerInvocation(c, "objective")
		if err != nil || actual != expected {
			t.Fatal("legacy planner recipe changed", contract, err)
		}
	}
	c.PlannerContract = plannerContractGraphV2
	i, err := plannerInvocation(c, "objective")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Schema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	got, _ := canonical.Hash("schema", payload.Schema)
	want, _ := canonical.Hash("schema", engineeringplan.PlannerJSONSchema())
	if got != want {
		t.Fatal("strict graph schema not bound to planner invocation")
	}
	c.PlannerContract = plannerContractGraphV5
	i, err = plannerInvocation(c, "objective")
	if err != nil {
		t.Fatal(err)
	}
	var repairPayload struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &repairPayload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(repairPayload.Instruction, "maximum repair area") || !strings.Contains(repairPayload.Instruction, "generated-source directives") || !strings.Contains(repairPayload.Instruction, "Generated outputs alone are not independent") {
		t.Fatal("repair planner contract omitted conservative-scope and generated-source guidance")
	}
	got, _ = canonical.Hash("schema", repairPayload.Schema)
	if got != want {
		t.Fatal("repair planner contract schema differs from strict graph schema")
	}
}

func TestPlannerGraphV6BindsParallelImplementationContract(t *testing.T) {
	c := config.Config{
		PlannerContract: plannerContractGraphV6,
		Planner:         runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "high", Role: "planner"},
	}
	i, err := plannerInvocation(c, "objective")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Instruction string          `json:"instruction"`
		Schema      json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal([]byte(i.Input), &payload); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"one initial implementation task",
		"two initial implementation tasks only when the work can be split into genuinely independent paths",
		"concrete, disjoint write_paths",
		"Include native verification and review gates in graph mode; both gates must depend on both implementation tasks when there are two",
		"In direct mode include exactly one implementation",
		"Do not duplicate work to fill a slot",
		"maximum repair area",
		"generated-source directives",
		"Generated outputs alone are not independent",
	} {
		if !strings.Contains(payload.Instruction, required) {
			t.Errorf("parallel planner contract omitted %q", required)
		}
	}
	got, _ := canonical.Hash("schema", payload.Schema)
	want, _ := canonical.Hash("schema", engineeringplan.PlannerJSONSchema())
	if got != want {
		t.Fatal("parallel planner contract schema differs from strict graph schema")
	}
}

func TestPlannerGraphV3V4HistoricInputsAreFrozen(t *testing.T) {
	const oldV3 = `As planner, produce a bounded engineering task graph as strict JSON matching the engineeringplan v1 schema (version 1, mode direct|graph, summary, tasks with id/kind/title/scope_paths/write_paths/expected_evidence/estimated_seconds and optional parent_id/dependencies). Use kinds research, design, implementation, verification, review. For graph mode include exactly one implementation with concrete write_paths within scope, research/design dependencies, and verification/review gates depending on the implementation. For direct mode include exactly one implementation with concrete write_paths. Never include completed or attempts; the runner owns progress. Return only the JSON object. Use at most 8 research/design tasks in total. Keep the initial plan to at most 32 tasks and at most 8 research/design tasks, reserving worst-case capacity for eight four-task repair slots. In direct mode, include exactly one task total: the implementation task. Do not include verification or review tasks; the runner performs those native gates. expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them.`
	const oldV4 = `As planner, produce a bounded engineering task graph as strict JSON matching the engineeringplan v1 schema (version 1, mode direct|graph, summary, tasks with id/kind/title/scope_paths/write_paths/expected_evidence/estimated_seconds and optional parent_id/dependencies). Use kinds research, design, implementation, verification, review. In graph mode, plan one initial implementation task for work that should remain together. Use two initial implementation tasks only when the work can be split into genuinely independent paths after any shared research/design: each task must own concrete, disjoint write_paths within scope and depend on the shared research/design it needs. Do not duplicate work to fill a slot. Include native verification and review gates in graph mode; both gates must depend on both implementation tasks when there are two, and on the implementation when there is one. In direct mode include exactly one implementation with concrete write_paths and no verification or review tasks; the runner performs native gates. Never include completed or attempts; the runner owns progress. Return only the JSON object. Use at most 8 research/design tasks in total. Keep the initial plan to at most 32 tasks and at most 8 research/design tasks, reserving worst-case capacity for eight four-task repair slots. expected_evidence MUST contain objects with nonempty kind and description strings, never plain strings. scope_paths may use a single dot for the repository root; write_paths must name concrete relative files or directories, never dot or parent paths. The review gate must depend on verification. For the initial implementation, treat scope_paths as the maximum repair area and write_paths as only the initial change. Inspect generated-source directives, templates, and extension points, and include relevant files in scope_paths even when they are not initially changed; do not add them to write_paths unless the first implementation edits them.`
	for _, tc := range []struct {
		contract    string
		instruction string
	}{{plannerContractGraphV3, oldV3}, {plannerContractGraphV4, oldV4}} {
		c := config.Config{PlannerContract: tc.contract, Planner: runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixture", Effort: "high", Role: "planner"}}
		actual, err := plannerInvocation(c, "historic objective")
		if err != nil {
			t.Fatal(err)
		}
		input, err := canonical.Bytes(struct {
			OutputSchema json.RawMessage `json:"output_schema,omitempty"`
			Role         string          `json:"role"`
			Instruction  string          `json:"instruction"`
			Objective    string          `json:"objective"`
			Checks       []config.Check  `json:"verification_checks,omitempty"`
		}{engineeringplan.PlannerJSONSchema(), "planner", tc.instruction, "historic objective", nil})
		if err != nil {
			t.Fatal(err)
		}
		want, err := runtime.NewInvocation(c.Planner, string(input))
		if err != nil || actual != want {
			t.Fatalf("historic %s planner input changed: %v", tc.contract, err)
		}
	}
}
