package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/taskpool"
	"harness.local/engorch/internal/taskscheduler"
)

func schedulePath(root, id string) (string, error) {
	run, err := runPath(root, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(filepath.Dir(run)), "schedules", id+".journal"), nil
}

func validateScheduleRepository(root string, definition taskscheduler.Definition) error {
	for _, task := range definition.Tasks {
		path, err := runPath(root, task.RunID)
		if err != nil || path != task.ControllerPath {
			return errors.New("scheduled task must bind an exact run in the selected repository")
		}
		snapshot, err := control.Inspect(path)
		if err != nil {
			return err
		}
		if snapshot.RunID != task.RunID || filepath.Clean(snapshot.Creation.Repository.Root) != root {
			return errors.New("scheduled run repository identity differs")
		}
	}
	return nil
}

func scheduleCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if command == "schedule-task" {
		if len(args) != 3 && len(args) != 4 {
			return errors.New("schedule-task requires RUN TASK_ID OPERATION and optional INPUT")
		}
		if _, err := taskpool.NewGraph([]taskpool.Task{{ID: args[1]}}); err != nil {
			return err
		}
		path, err := runPath(root, args[0])
		if err != nil {
			return err
		}
		input := ""
		if len(args) == 4 {
			input = args[3]
		}
		task, err := control.PrepareScheduledTask(path, taskscheduler.Operation(args[2]), input)
		if err != nil {
			return err
		}
		task.ID = args[1]
		if err := validateScheduleRepository(root, taskscheduler.Definition{Tasks: []taskscheduler.TaskSpec{task}}); err != nil {
			return err
		}
		return output(out, task)
	}
	want := 1
	if command == "schedule-recover" {
		want = 2
	}
	workers := 1
	if (command == "schedule-tick" || command == "schedule-run") && len(args) == 2 {
		var err error
		workers, err = strconv.Atoi(args[1])
		if err != nil || workers < 1 || workers > 64 {
			return errors.New("schedule workers must be between 1 and 64")
		}
		want = 2
	}
	if len(args) != want {
		return errors.New("schedule command requires one definition path or schedule ID")
	}
	if command == "schedule-create" {
		input := args[0]
		if !filepath.IsAbs(input) {
			input = filepath.Join(root, input)
		}
		var definition taskscheduler.Definition
		if err := readJSON(input, &definition); err != nil {
			return err
		}
		id, err := definition.ID()
		if err != nil {
			return err
		}
		if err := validateScheduleRepository(root, definition); err != nil {
			return err
		}
		path, err := schedulePath(root, id)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		snapshot, err := taskscheduler.Bind(path, definition)
		if err != nil {
			return err
		}
		return output(out, snapshot)
	}
	path, err := schedulePath(root, args[0])
	if err != nil {
		return err
	}
	snapshot, err := taskscheduler.Inspect(path)
	if err != nil {
		return err
	}
	if snapshot.ScheduleID != args[0] || snapshot.Definition == nil {
		return errors.New("schedule identity unavailable or differs")
	}
	if err := validateScheduleRepository(root, *snapshot.Definition); err != nil {
		return err
	}
	for _, dynamic := range snapshot.Dynamic {
		if err := validateScheduleRepository(root, taskscheduler.Definition{Tasks: []taskscheduler.TaskSpec{dynamic.Task}}); err != nil {
			return err
		}
	}
	adapter := repositoryScheduleAdapter{root: root, journalPath: path}
	if command == "schedule-run" {
		// No completion output is inferred from cancellation or an empty queue.
		// Inspect the existing journals after this foreground process stops.
		return taskscheduler.Pump(ctx, path, adapter, taskscheduler.PumpOptions{Workers: workers})
	}
	if command == "schedule-tick" {
		if workers > 1 {
			return scheduleBatch(ctx, path, workers, adapter, out)
		}
		decision, err := taskscheduler.Tick(ctx, path, adapter)
		if err != nil {
			return err
		}
		return output(out, decision)
	}
	if command == "schedule-recover" {
		decision, err := taskscheduler.RecoverClaim(ctx, path, args[1], adapter)
		if err != nil {
			return err
		}
		return output(out, decision)
	}
	return output(out, snapshot)
}
