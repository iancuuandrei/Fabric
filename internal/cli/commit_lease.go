package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
)

type commitLeasePreview struct {
	Prepared control.PreparedCommitLease `json:"prepared"`
	IntentID string                      `json:"intent_id"`
}

func commitLeaseCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 3
	if command == "recover-commit-lease" {
		want = 4
	}
	if len(args) != want {
		return errors.New("prepare-commit-lease requires RUN EVIDENCE workloads-stopped; recover-commit-lease requires RUN PREVIEW_JSON INTENT_ID ACTOR")
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
	if command == "prepare-commit-lease" {
		if args[2] != "workloads-stopped" {
			return errors.New("explicit workloads-stopped attestation required")
		}
		prepared, err := control.PrepareCommitLeaseRecovery(path, args[1], true)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, commitLeasePreview{Prepared: prepared, IntentID: id})
	}
	var preview commitLeasePreview
	if err := readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit lease recovery approval differs from preview")
	}
	after, recoveryErr := control.RecoverCommitLease(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]})
	return errors.Join(recoveryErr, output(out, after))
}
