package cli

import (
	"context"
	"errors"
	"io"

	"harness.local/engorch/internal/control"
)

func agentInterruptCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) != 5 {
		return errors.New("agent-interrupt requires RUN SCHEDULE_ID TURN_ID ACTOR NONCE")
	}
	controllerPath, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	path, err := schedulePath(root, args[1])
	if err != nil {
		return err
	}
	if err := validateAgentSchedule(root, args[0], args[1], controllerPath, path); err != nil {
		return err
	}
	state, err := control.RequestAgentInterrupt(ctx, controllerPath, path, args[2], args[3], args[4])
	if err != nil {
		return err
	}
	return output(out, state)
}
