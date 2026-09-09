package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
)

func lifecycleCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count := 3
	if command == "settle-lifecycle" {
		count = 4
	}
	if len(args) != count {
		return errors.New("lifecycle command requires exact run, actor and request or settlement evidence arguments")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	snapshot, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if snapshot.RunID != args[0] || filepath.Clean(snapshot.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	switch command {
	case "pause":
		snapshot, err = control.RequestPause(path, args[1], args[2])
	case "cancel":
		snapshot, err = control.RequestCancel(path, args[1], args[2])
	case "resume":
		snapshot, err = control.Resume(path, args[1], args[2])
	case "settle-lifecycle":
		if args[3] != "workloads-stopped" {
			return errors.New("explicit workloads-stopped attestation required")
		}
		snapshot, err = control.SettleLifecycle(path, args[1], args[2], true)
	default:
		return errors.New("unsupported lifecycle command")
	}
	if err != nil {
		return err
	}
	return output(out, snapshot)
}
