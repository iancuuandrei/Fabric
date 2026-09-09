package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/ri"
)

type lexicalPreview struct {
	Plan     ri.LexicalPlan `json:"plan"`
	IntentID string         `json:"intent_id"`
}

type overlayPreview struct {
	Plan     control.LexicalOverlayPlan `json:"plan"`
	Intent   effects.Intent             `json:"intent"`
	IntentID string                     `json:"intent_id"`
}

func riLexicalCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	counts := map[string]int{"prepare-lexical": 7, "lexical": 5, "lexical-ref": 2, "prepare-overlay": 3, "overlay": 5, "overlay-ref": 2}
	if len(args) == 0 || len(args) != counts[args[0]] {
		return errors.New("invalid lexical lifecycle arguments; see harness help")
	}
	path, err := runPath(root, args[1])
	if err != nil {
		return err
	}
	s, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if s.RunID != args[1] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	switch args[0] {
	case "prepare-overlay":
		destination, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		prepared, err := control.PrepareLexicalOverlay(ctx, path, destination)
		if err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		return output(out, overlayPreview{prepared.Plan, prepared.Intent, id})
	case "overlay":
		previewPath, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		var prepared overlayPreview
		if err := readJSON(previewPath, &prepared); err != nil {
			return err
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			return err
		}
		if id != prepared.IntentID || id != args[3] {
			return errors.New("overlay approval differs from preview")
		}
		latest, executionErr := control.ExecuteLexicalOverlay(ctx, path, control.PreparedLexicalOverlay{Plan: prepared.Plan, Intent: prepared.Intent}, effects.Authorization{IntentID: args[3], Actor: args[4]})
		return errors.Join(executionErr, output(out, latest))
	case "overlay-ref":
		ref, err := control.SelectLexicalOverlay(ctx, path)
		if err != nil {
			return err
		}
		return output(out, ref)
	}
	if args[0] == "lexical-ref" {
		if s.RILexical == nil || s.RILexical.Outcome != "CONFIRMED" {
			return errors.New("confirmed lexical indexing required")
		}
		ref, err := ri.ObserveLexicalStage(ctx, s.RILexical.Intent.Plan)
		if err != nil {
			return err
		}
		observedID, err := ref.Build.ID()
		if err != nil {
			return err
		}
		confirmedID, err := s.RILexical.Observation.Build.ID()
		if err != nil {
			return err
		}
		if observedID != confirmedID {
			return errors.New("lexical build differs from journal")
		}
		compact, err := ref.Compact()
		if err != nil {
			return err
		}
		return output(out, compact)
	}
	if args[0] == "prepare-lexical" {
		executable, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		stage, err := riAbsolutePath(root, args[4])
		if err != nil {
			return err
		}
		batchBytes, err := strconv.ParseInt(args[5], 10, 64)
		if err != nil {
			return err
		}
		batchFiles, err := strconv.Atoi(args[6])
		if err != nil {
			return err
		}
		observed, err := ri.ObserveLexical(ctx, s.Creation.Repository)
		if err != nil {
			return err
		}
		manifestID, err := observed.Manifest.ID()
		if err != nil {
			return err
		}
		plan := ri.LexicalPlan{Version: 1, Repository: s.Creation.Repository, ManifestID: manifestID, Files: len(observed.Manifest.Files), Executable: executable, ExecutableSHA256: args[3], StageRoot: stage, BatchBytes: batchBytes, BatchFiles: batchFiles}
		if err := plan.ValidateManifest(observed.Manifest); err != nil {
			return err
		}
		intent, err := plan.Intent(s.RunID, s.PlanID)
		if err != nil {
			return err
		}
		id, err := intent.ID()
		if err != nil {
			return err
		}
		return output(out, lexicalPreview{plan, id})
	}
	previewPath, err := riAbsolutePath(root, args[2])
	if err != nil {
		return err
	}
	var prepared lexicalPreview
	if err := readJSON(previewPath, &prepared); err != nil {
		return err
	}
	if prepared.IntentID != args[3] || prepared.Plan.Repository != s.Creation.Repository {
		return errors.New("lexical approval or repository differs from preview")
	}
	observed, err := ri.ObserveLexical(ctx, s.Creation.Repository)
	if err != nil {
		return err
	}
	latest, executionErr := control.ExecuteRILexical(ctx, path, prepared.Plan, observed.Manifest, effects.Authorization{IntentID: args[3], Actor: args[4]})
	return errors.Join(executionErr, output(out, latest))
}
