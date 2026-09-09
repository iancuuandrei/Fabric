package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/agenttree"
	"harness.local/engorch/internal/control"
)

func agentCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	valid := command == "agent-list" && (len(args) == 1 || len(args) == 3) || command == "agent-send" && len(args) == 2 || command == "agent-messages" && len(args) == 4 || command == "agent-wait" && len(args) == 5
	if !valid {
		return errors.New("invalid agent command arguments")
	}
	after, limit, milliseconds := 0, 100, 0
	var parseErr error
	if command == "agent-list" && len(args) == 3 {
		limit, parseErr = strconv.Atoi(args[2])
	}
	if command == "agent-messages" || command == "agent-wait" {
		after, parseErr = strconv.Atoi(args[2])
		if parseErr == nil {
			limit, parseErr = strconv.Atoi(args[3])
		}
	}
	if parseErr != nil || after < 0 || limit < 1 || limit > 256 {
		return errors.New("invalid agent page: sequence must be nonnegative and limit 1..256")
	}
	if command == "agent-wait" {
		milliseconds, parseErr = strconv.Atoi(args[4])
		if parseErr != nil || milliseconds < 1 || milliseconds > 60000 {
			return errors.New("agent wait timeout must be 1..60000 milliseconds")
		}
	}
	var request agentcontrol.MessageRequest
	if command == "agent-send" {
		input := args[1]
		if !filepath.IsAbs(input) {
			input = filepath.Join(root, input)
		}
		if err := readJSON(input, &request); err != nil {
			return err
		}
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if snapshot.RunID != args[0] || filepath.Clean(snapshot.Creation.Repository.Root) != root {
		return errors.New("agent run repository identity differs")
	}
	tree, err := agenttree.Inspect(path + ".agent-tree")
	if err != nil {
		return err
	}
	if tree.TreeID != snapshot.RunID {
		return errors.New("registered agent tree unavailable for run")
	}
	service, err := agentcontrol.Bind(path+".agent-tree", path+".agent-control")
	if err != nil {
		return err
	}
	switch command {
	case "agent-list":
		cursor := ""
		if len(args) == 3 {
			cursor = args[1]
		}
		nodes, err := service.List(ctx, cursor, limit)
		if err != nil {
			return err
		}
		return output(out, nodes)
	case "agent-send":
		record, err := service.Send(ctx, request)
		if err != nil {
			return err
		}
		return output(out, record.Message)
	case "agent-messages", "agent-wait":
		if command == "agent-messages" {
			messages, err := service.MessagesAfter(args[1], after, limit)
			if err != nil {
				return err
			}
			return output(out, messages)
		}
		waitCtx, cancel := context.WithTimeout(ctx, time.Duration(milliseconds)*time.Millisecond)
		defer cancel()
		activities, err := service.Wait(waitCtx, args[1], after, limit)
		if err != nil {
			return err
		}
		return output(out, activities)
	}
	return errors.New("unsupported agent command")
}
