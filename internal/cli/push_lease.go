package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
)

type pushLeasePreview struct {
	Prepared control.PreparedPushLease `json:"prepared"`
	IntentID string                    `json:"intent_id"`
}

func pushLeaseCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 3
	if command == "recover-push-lease" {
		want = 4
	}
	if len(args) != want && !(command == "recover-push-lease" && len(args) == want+1) {
		return errors.New("prepare-push-lease requires RUN EVIDENCE workloads-stopped; recover-push-lease requires RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]")
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
	if command == "prepare-push-lease" {
		if args[2] != "workloads-stopped" {
			return errors.New("explicit workloads-stopped attestation required")
		}
		prepared, err := control.PreparePushLeaseRecovery(path, args[1], true)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, pushLeasePreview{Prepared: prepared, IntentID: id})
	}
	var preview pushLeasePreview
	if err := readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit lease recovery approval differs from preview")
	}
	if s.Push == nil {
		return errors.New("pending push required")
	}
	credentials, err := pushCredentials(s.Push.Intent.Prepared.Plan.Destination, args[want:])
	if err != nil {
		return err
	}
	after, recoveryErr := control.RecoverPushLease(ctx, path, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]}, credentials...)
	return errors.Join(recoveryErr, output(out, after))
}
