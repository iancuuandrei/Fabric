package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/modelpolicy"
)

const modelPolicyMaxBytes = 128 << 10

func modelCalibrationCommand(args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("calibrate-models requires one task-policy calibration JSON file")
	}
	raw, err := readModelPolicyJSON(args[0])
	if err != nil {
		return err
	}
	var calibration modelpolicy.Calibration
	if err := canonical.Decode(raw, &calibration); err != nil {
		return fmt.Errorf("invalid calibration JSON: %w", err)
	}
	selection, err := modelpolicy.EvaluateCalibration(calibration)
	if err != nil {
		return err
	}
	return output(out, selection)
}

func readInitModelPolicy(path string) (*modelpolicy.Policy, error) {
	raw, err := readModelPolicyJSON(path)
	if err != nil {
		return nil, err
	}
	var policy modelpolicy.Policy
	if err := canonical.Decode(raw, &policy); err != nil {
		return nil, fmt.Errorf("invalid model policy JSON: %w", err)
	}
	return &policy, nil
}

func readModelPolicyJSON(path string) ([]byte, error) {
	return readRegularPolicyJSON(path, modelPolicyMaxBytes, "model policy input", "128 KiB")
}

func readRegularPolicyJSON(path string, maxBytes int64, label, sizeLabel string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s must be an existing regular file no larger than %s", label, sizeLabel)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s could not be opened", label)
	}
	opened, statErr := f.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(f, maxBytes+1))
	closeErr := f.Close()
	if errors.Join(statErr, readErr, closeErr) != nil {
		return nil, fmt.Errorf("%s could not be read completely", label)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) || int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%s must remain the same regular file no larger than %s", label, sizeLabel)
	}
	return raw, nil
}
