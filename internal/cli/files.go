package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
)

type filePreview struct {
	Prepared control.PreparedFiles `json:"prepared"`
	IntentID string                `json:"intent_id"`
}

type recoveryPreview struct {
	Prepared control.PreparedRecovery `json:"prepared"`
	IntentID string                   `json:"intent_id"`
}

func recoveryCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	want := 1
	if command == "recover-files" {
		want = 4
	}
	if len(args) != want {
		return errors.New("prepare-recovery requires RUN; recover-files requires RUN PREVIEW_JSON INTENT_ID ACTOR")
	}
	p, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	s, err := control.Inspect(p)
	if err != nil {
		return err
	}
	if s.RunID != args[0] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	if command == "prepare-recovery" {
		prepared, err := control.PrepareFileRecovery(ctx, p)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, recoveryPreview{prepared, id})
	}
	var preview recoveryPreview
	if err = readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit recovery approval differs from preview")
	}
	after, recoverErr := control.RecoverFiles(ctx, p, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]})
	return errors.Join(recoverErr, output(out, after))
}

func readJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, canonical.MaxBytes+1))
	if err != nil {
		return err
	}
	return canonical.Decode(b, dst)
}

func fileCommand(ctx context.Context, root, command string, args []string, out io.Writer) error {
	count := 2
	if command == "apply-files" {
		count = 4
	}
	if len(args) != count {
		return errors.New("prepare-files requires RUN CHANGES_JSON; apply-files requires RUN PREVIEW_JSON INTENT_ID ACTOR")
	}
	p, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	s, err := control.Inspect(p)
	if err != nil {
		return err
	}
	if s.RunID != args[0] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	if command == "prepare-files" {
		var changes []fileeffects.Change
		if err = readJSON(args[1], &changes); err != nil {
			return err
		}
		prepared, err := control.PrepareFiles(ctx, p, changes)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, filePreview{prepared, id})
	}
	var preview filePreview
	if err = readJSON(args[1], &preview); err != nil {
		return err
	}
	if args[2] != preview.IntentID {
		return errors.New("explicit approval differs from preview intent")
	}
	after, applyErr := control.ApplyFiles(ctx, p, preview.Prepared, effects.Authorization{IntentID: args[2], Actor: args[3]})
	return errors.Join(applyErr, output(out, after))
}
