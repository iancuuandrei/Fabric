package control

import (
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

const plannerContractV1 = "plan-v1"

const plannerAssignmentV1 = "As planner, produce an implementation plan using read-only source tools. Editing and testing belong to later roles. Read-only access is expected and is not a blocker. Return the plan without requesting additional capability."

// plannerInvocation binds the configured planner contract to the exact
// objective. The empty contract deliberately retains the historical raw input
// identity so journals created before planner contracts remain replayable.
func plannerInvocation(c config.Config, objective string) (runtime.Invocation, error) {
	input := objective
	if c.PlannerContract != "" {
		if c.PlannerContract != plannerContractV1 {
			return runtime.Invocation{}, errors.New("unsupported planner contract")
		}
		wrapped, err := canonical.Bytes(struct {
			Role        string `json:"role"`
			Instruction string `json:"instruction"`
			Objective   string `json:"objective"`
		}{"planner", plannerAssignmentV1, objective})
		if err != nil {
			return runtime.Invocation{}, err
		}
		input = string(wrapped)
	}
	return runtime.NewInvocation(c.Planner, input)
}
