package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
)

type draftLeasePreview struct {
	Prepared control.PreparedDraftLease `json:"prepared"`
	IntentID string                     `json:"intent_id"`
}

func draftLeaseCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 3
	if command == "recover-draft-lease" {
		want = 5
	}
	if len(args) != want {
		return errors.New("prepare-draft-lease requires RUN EVIDENCE workloads-stopped; recover-draft-lease requires RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV")
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
	if command == "prepare-draft-lease" {
		if args[2] != "workloads-stopped" {
			return errors.New("explicit workloads-stopped attestation required")
		}
		prepared, err := control.PrepareDraftLeaseRecovery(path, args[1], true)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, draftLeasePreview{Prepared: prepared, IntentID: id})
	}
	var preview draftLeasePreview
	if err := readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit lease recovery approval differs from preview")
	}
	client, err := draftClient(args[4])
	if err != nil {
		return err
	}
	defer client.Close()
	after, recoveryErr := control.RecoverDraftLease(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]}, client)
	return errors.Join(recoveryErr, output(out, after))
}
