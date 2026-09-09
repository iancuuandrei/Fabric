package control

import (
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/canonical"
	"strings"
)

// PhaseObjective explicitly separates phase-local instruction from shared task
// data. Projection never strips arbitrary natural-language safety constraints.
type PhaseObjective struct {
	RoleContextVersion int             `json:"role_context_version"`
	Task               string          `json:"task"`
	PlannerInstruction string          `json:"planner_instruction"`
	SourceState        json.RawMessage `json:"source_state"`
}

func mutationObjective(raw string) (string, error) {
	var marker struct {
		Version *int `json:"role_context_version"`
	}
	if json.Unmarshal([]byte(raw), &marker) != nil || marker.Version == nil {
		return raw, nil
	}
	var objective PhaseObjective
	if err := canonical.Decode([]byte(raw), &objective); err != nil {
		return "", err
	}
	if objective.RoleContextVersion != 1 || strings.TrimSpace(objective.Task) == "" {
		return "", errors.New("invalid phase objective")
	}
	projected, err := canonical.Bytes(struct {
		Task        string          `json:"task"`
		SourceState json.RawMessage `json:"source_state"`
	}{objective.Task, objective.SourceState})
	return string(projected), err
}
