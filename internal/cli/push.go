package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
)

type pushPreview struct {
	Prepared control.PreparedPush `json:"prepared"`
	IntentID string               `json:"intent_id"`
}

func pushCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 3
	if command == "push" {
		want = 4
	}
	if command == "reconcile-push" {
		want = 1
	}
	if len(args) != want && len(args) != want+1 {
		return errors.New("prepare-push requires RUN DESTINATION TARGET_REF [TOKEN_ENV]; push requires RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]; reconcile-push requires RUN [TOKEN_ENV]")
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
	if command == "prepare-push" {
		credentials, err := pushCredentials(args[1], args[want:])
		if err != nil {
			return err
		}
		prepared, err := control.PreparePush(ctx, path, args[1], args[2], credentials...)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, pushPreview{prepared, id})
	}
	if command == "reconcile-push" {
		if s.Push == nil {
			return errors.New("pending push required")
		}
		credentials, err := pushCredentials(s.Push.Intent.Prepared.Plan.Destination, args[want:])
		if err != nil {
			return err
		}
		after, err := control.ReconcilePush(ctx, path, credentials...)
		return errors.Join(err, output(out, after))
	}
	var preview pushPreview
	if err := readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit push approval differs from preview")
	}
	credentials, err := pushCredentials(preview.Prepared.Plan.Destination, args[want:])
	if err != nil {
		return err
	}
	after, executionErr := control.ExecutePush(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]}, credentials...)
	return errors.Join(executionErr, output(out, after))
}
