package cli

import (
	"context"
	"errors"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/ri"
	"io"
	"path/filepath"
)

type producerPreview struct {
	Plan     ri.ProducerPlan `json:"plan"`
	IntentID string          `json:"intent_id"`
}

func riLifecycleCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	counts := map[string]int{"prepare-import": 3, "import": 5, "prepare-publish": 3, "publish": 5, "prepare-publication-recovery": 2, "recover-publication": 4}
	counts["prepare-producer"] = 4
	counts["produce"] = 5
	counts["bind-import"] = 3
	counts["close-producer"] = 6
	counts["runtime-binding"] = 2
	if len(args) == 0 || len(args) != counts[args[0]] {
		return errors.New("invalid RI lifecycle arguments; see harness help")
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
	preview := func(intent effects.Intent, err error) error {
		if err != nil {
			return err
		}
		id, err := intent.ID()
		if err != nil {
			return err
		}
		return output(out, struct {
			Intent   effects.Intent `json:"intent"`
			IntentID string         `json:"intent_id"`
		}{intent, id})
	}
	switch args[0] {
	case "runtime-binding":
		binding, err := control.SelectRuntimeRI(ctx, path)
		if err != nil {
			return err
		}
		return output(out, binding)
	case "close-producer":
		if args[5] != "workloads-stopped" {
			return errors.New("explicit workloads-stopped attestation required")
		}
		latest, closeErr := control.CloseRIProducer(ctx, path, args[2], args[3], args[4], true)
		return errors.Join(closeErr, output(out, latest))
	case "prepare-producer":
		if s.Workspace == nil || s.WorkspaceOutcome != "CONFIRMED" {
			return errors.New("admitted workspace required")
		}
		checkPath, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		var check config.Check
		if err := readJSON(checkPath, &check); err != nil {
			return err
		}
		destination, err := riAbsolutePath(root, args[3])
		if err != nil {
			return err
		}
		plan, err := ri.PrepareProducer(s.Creation.Repository, s.Workspace.Request.Path, check, destination)
		if err != nil {
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
		return output(out, producerPreview{plan, id})
	case "produce":
		previewPath, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		var prepared producerPreview
		if err := readJSON(previewPath, &prepared); err != nil {
			return err
		}
		if args[3] != prepared.IntentID {
			return errors.New("producer approval differs from preview")
		}
		latest, executionErr := control.ExecuteRIProducer(ctx, path, prepared.Plan, effects.Authorization{IntentID: args[3], Actor: args[4]})
		return errors.Join(executionErr, output(out, latest))
	case "bind-import":
		planPath, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		var plan ri.ImportPlan
		if err := readJSON(planPath, &plan); err != nil {
			return err
		}
		bound, err := control.BindRIProducerImport(path, plan)
		if err != nil {
			return err
		}
		return output(out, bound)
	case "prepare-import", "import":
		planPath, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		var plan ri.ImportPlan
		if err := readJSON(planPath, &plan); err != nil {
			return err
		}
		if plan.Repository != s.Creation.Repository {
			return errors.New("import plan repository mismatch")
		}
		if args[0] == "prepare-import" {
			return preview(plan.Intent(s.RunID, s.PlanID))
		}
		latest, executionErr := control.ExecuteRIImport(ctx, path, plan, effects.Authorization{IntentID: args[3], Actor: args[4]})
		return errors.Join(executionErr, output(out, latest))
	case "prepare-publish", "publish":
		directory, err := riAbsolutePath(root, args[2])
		if err != nil {
			return err
		}
		if args[0] == "prepare-publish" {
			return preview(control.PrepareRIPublish(path, directory))
		}
		latest, executionErr := control.ExecuteRIPublish(ctx, path, directory, effects.Authorization{IntentID: args[3], Actor: args[4]})
		return errors.Join(executionErr, output(out, latest))
	case "prepare-publication-recovery":
		return preview(control.PrepareRIPublishRecovery(path))
	case "recover-publication":
		latest, executionErr := control.RecoverRIPublish(ctx, path, effects.Authorization{IntentID: args[2], Actor: args[3]})
		return errors.Join(executionErr, output(out, latest))
	}
	return errors.New("unknown RI lifecycle operation")
}
