package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/journal"
)

func inspectExport(ctx context.Context, root, id string, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := runPath(root, id)
	if err != nil {
		return err
	}
	events, err := journal.ReadContext(ctx, path)
	if err != nil {
		return err
	}
	// Validate and serialize the same snapshot; a second read could admit a
	// different history between semantic validation and export.
	snapshot, err := control.Replay(events)
	if err != nil {
		return err
	}
	if snapshot.RunID != id || filepath.Clean(snapshot.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	var data bytes.Buffer
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := canonical.Bytes(event)
		if err != nil {
			return err
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	_, err = io.Copy(out, &data)
	return err
}
