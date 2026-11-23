package cli

import (
	"context"
	"errors"
	"io"

	"github.com/iancuuandrei/fabric/internal/control"
)

// checkpointCommand is the read-only `checkpoint RUN` command handler. The
// shared command catalogue/dispatch is wired separately after this slice is
// frozen.
func checkpointCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("checkpoint requires one run ID")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	checkpoint, err := control.ReadCheckpoint(path)
	if err != nil {
		return err
	}
	if checkpoint.RunID != args[0] {
		return errors.New("checkpoint run ID binding mismatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return output(out, checkpoint)
}
