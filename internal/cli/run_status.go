package cli

import "harness.local/engorch/internal/control"

// runStatus is an incidental reporting projection. Exact inputs, messages and
// evidence bodies remain available through explicit run inspection.
type runStatus struct {
	RunID     string `json:"run_id"`
	State     string `json:"state"`
	PlanID    string `json:"plan_id,omitempty"`
	Lifecycle string `json:"lifecycle"`
}

func summarizeRun(state control.Snapshot) runStatus {
	lifecycle := state.Lifecycle.Status
	if lifecycle == "" {
		lifecycle = control.LifecycleActive
	}
	return runStatus{RunID: state.RunID, State: state.State, PlanID: state.PlanID, Lifecycle: lifecycle}
}
