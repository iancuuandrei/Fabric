package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
)

type commitMetadata struct {
	Author    gitlocal.Identity `json:"author"`
	Committer gitlocal.Identity `json:"committer"`
	Message   string            `json:"message"`
}

type commitPreview struct {
	Prepared control.PreparedCommit `json:"prepared"`
	IntentID string                 `json:"intent_id"`
}

func commitCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 2
	if command == "commit" {
		want = 4
	}
	if len(args) != want {
		return errors.New("prepare-commit requires RUN METADATA_JSON; commit requires RUN PREVIEW_JSON INTENT_ID ACTOR")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	s, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if s.RunID != args[0] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	if command == "prepare-commit" {
		var metadata commitMetadata
		if err := readJSON(args[1], &metadata); err != nil {
			return err
		}
		prepared, err := control.PrepareCommit(ctx, path, metadata.Author, metadata.Committer, metadata.Message)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, commitPreview{Prepared: prepared, IntentID: id})
	}
	var preview commitPreview
	if err := readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit commit approval differs from preview")
	}
	after, executionErr := control.ExecuteCommit(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]})
	return errors.Join(executionErr, output(out, after))
}
