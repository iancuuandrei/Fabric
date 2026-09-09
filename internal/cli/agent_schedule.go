package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/taskscheduler"
)

type explorerSpawnRequest struct {
	ParentAgentID string `json:"parent_agent_id"`
	Name          string `json:"name"`
	Question      string `json:"question"`
	Nonce         string `json:"nonce"`
}

type agentQueuedResult struct {
	AgentID      string             `json:"agent_id"`
	TaskID       string             `json:"task_id"`
	TurnID       string             `json:"turn_id"`
	TurnSequence int                `json:"turn_sequence"`
	Message      *agenttree.Message `json:"message,omitempty"`
}

func agentScheduleCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) != 3 {
		return errors.New("agent scheduling requires RUN SCHEDULE_ID REQUEST_JSON")
	}
	controllerPath, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	path, err := schedulePath(root, args[1])
	if err != nil {
		return err
	}
	input := args[2]
	if !filepath.IsAbs(input) {
		input = filepath.Join(root, input)
	}
	var spawn explorerSpawnRequest
	var message agentcontrol.MessageRequest
	switch command {
	case "agent-spawn":
		err = readJSON(input, &spawn)
	case "agent-followup":
		err = readJSON(input, &message)
	default:
		return errors.New("unsupported agent scheduling operation")
	}
	if err != nil {
		return err
	}
	if err := validateAgentSchedule(root, args[0], args[1], controllerPath, path); err != nil {
		return err
	}
	if command == "agent-spawn" {
		node, turn, err := control.SpawnExplorerAgent(ctx, controllerPath, path, spawn.ParentAgentID, spawn.Name, spawn.Question, spawn.Nonce)
		if err != nil {
			return err
		}
		return output(out, agentQueuedResult{AgentID: node.AgentID, TaskID: turn.Task.ID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence})
	}
	record, turn, err := control.FollowUpExplorerAgent(ctx, controllerPath, path, message, message.Body)
	if err != nil {
		return err
	}
	return output(out, agentQueuedResult{AgentID: message.ToAgentID, TaskID: turn.Task.ID, TurnID: turn.TurnID, TurnSequence: turn.TurnSequence, Message: &record.Message})
}

func validateAgentSchedule(root, runID, scheduleID, controllerPath, path string) error {
	if err := validateScheduleRepository(root, taskscheduler.Definition{Tasks: []taskscheduler.TaskSpec{{RunID: runID, ControllerPath: controllerPath}}}); err != nil {
		return err
	}
	snapshot, err := taskscheduler.Inspect(path)
	if err != nil {
		return err
	}
	if snapshot.ScheduleID != scheduleID || snapshot.Definition == nil {
		return errors.New("agent schedule identity unavailable or differs")
	}
	if err := validateScheduleRepository(root, *snapshot.Definition); err != nil {
		return err
	}
	for _, dynamic := range snapshot.Dynamic {
		if err := validateScheduleRepository(root, taskscheduler.Definition{Tasks: []taskscheduler.TaskSpec{dynamic.Task}}); err != nil {
			return err
		}
	}
	return nil
}
