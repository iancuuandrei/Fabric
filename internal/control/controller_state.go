package control

import (
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/controllerstate"
)

func controllerNamespace(c Creation) (string, error) {
	if c.Config.ControllerStateRoot == "" {
		return "", nil
	}
	paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
	if err != nil {
		return "", err
	}
	return paths.Root, nil
}

func validateControllerJournalPath(path string, c Creation, runID string) error {
	if c.Config.ControllerStateRoot == "" {
		return nil
	}
	paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
	if err != nil {
		return err
	}
	expected, err := paths.Run(runID)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != expected {
		return errors.New("controller journal path differs from bound external state")
	}
	return controllerstate.Validate(paths, c.Repository)
}

func validateControllerCreationPath(path string, payload any) error {
	raw, err := canonical.Bytes(payload)
	if err != nil {
		return err
	}
	var c Creation
	if err := canonical.Decode(raw, &c); err != nil {
		return err
	}
	if c.Config.ControllerStateRoot == "" {
		return nil
	}
	id, err := canonical.Hash("harness.run.v1", c)
	if err != nil {
		return err
	}
	return validateControllerJournalPath(path, c, id)
}
