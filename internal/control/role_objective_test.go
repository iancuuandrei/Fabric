package control

import (
	"context"
	"encoding/json"
	"harness.local/engorch/internal/runtime"
	"strings"
	"testing"
)

func TestWriterRoleContextDoesNotInheritPlannerProhibitions(t *testing.T) {
	c := creation(t)
	c.Config.WriterContract = "nonempty-v1"
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer", Effort: "medium", Role: "writer"}
	planner := "Planner phase: do not implement; read-only; do not edit; research only; do not commit."
	task := "Create the requested module. Preserve the user constraint: do not commit until review."
	raw, _ := json.Marshal(PhaseObjective{1, task, planner, json.RawMessage(`[]`)})
	c.Objective = string(raw)
	path, _ := approvedRepositoryCreation(t, c)
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	writer, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Objective, "do not implement") {
		t.Fatal("invalid planner fixture")
	}
	for _, prohibition := range []string{"do not implement", "read-only", "do not edit", "research only", "planner_instruction"} {
		if strings.Contains(writer.Input, prohibition) {
			t.Fatal("planner leakage", prohibition)
		}
	}
	if !strings.Contains(writer.Input, "ROLE: IMPLEMENTER") || !strings.Contains(writer.Input, "Produce the implementation") || !strings.Contains(writer.Input, "do not commit until review") {
		t.Fatal("lost role duty or shared constraint")
	}
}
