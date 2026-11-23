package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/faultlocalization"
)

func diagnoseCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("diagnose requires RUN")
	}
	flags := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	anchor := flags.Bool("anchor", false, "validate current candidate bytes")
	closure := flags.Bool("closure", false, "inspect historical findings and exact native recheck receipts")
	previous := flags.String("previous", "", "use prior report anchors as untrusted localization hints")
	spectrum := flags.String("spectrum", "", "rank supplied per-test Go coverage after candidate source validation")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*previous != "" && !*anchor) || (*closure && (*anchor || *previous != "")) || (*spectrum != "" && (*anchor || *closure || *previous != "")) {
		return errors.New("diagnose expects RUN [--anchor [--previous REPORT_JSON] | --closure | --spectrum SPECTRUM_JSON]")
	}
	path, err := runPath(root, args[0])
	if err != nil {
		return err
	}
	if *closure {
		r, err := control.ReadRepairClosure(path, root, args[0])
		if err != nil {
			return err
		}
		return output(out, r)
	}
	s, err := control.Inspect(path)
	if err != nil {
		return err
	}
	if s.RunID != args[0] || filepath.Clean(s.Creation.Repository.Root) != filepath.Clean(root) {
		return errors.New("journal/run repository binding mismatch")
	}
	if *spectrum != "" {
		bundle, err := readRepairSpectrum(*spectrum)
		if err != nil {
			return err
		}
		r, err := control.DiagnoseRepairSpectrum(ctx, path, s, bundle)
		if err != nil {
			return err
		}
		return output(out, r)
	}
	if !*anchor {
		r, err := control.DiagnoseRepair(s)
		if err != nil {
			return err
		}
		return output(out, r)
	}
	hints, err := previousRepairAnchors(*previous, s.RunID)
	if err != nil {
		return err
	}
	r, err := control.DiagnoseRepairAnchored(ctx, path, s, hints)
	if err != nil {
		return err
	}
	return output(out, r)
}

func readRepairSpectrum(path string) (faultlocalization.Spectrum, error) {
	var s faultlocalization.Spectrum
	f, err := os.Open(path)
	if err != nil {
		return s, errors.New("repair spectrum unavailable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, faultlocalization.MaxEncodedBytes+1))
	if err != nil || len(raw) > faultlocalization.MaxEncodedBytes {
		return s, errors.New("repair spectrum outside read bounds")
	}
	if err := canonical.Decode(raw, &s); err != nil {
		return s, errors.New("invalid repair spectrum")
	}
	return s, nil
}

func repairContextCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("repair-context requires RUN SPECTRUM_JSON [--task TASK_ID]")
	}
	flags := flag.NewFlagSet("repair-context", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	task := flags.String("task", "", "select task-bound writer instead of the single serial writer")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("repair-context expects RUN SPECTRUM_JSON [--task TASK_ID]")
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
	spectrum, err := readRepairSpectrum(args[1])
	if err != nil {
		return err
	}
	rec, err := control.AdmitRepairSpectrumContext(ctx, path, *task, spectrum)
	if err != nil {
		return err
	}
	ranking, err := faultlocalization.Analyze(rec.RepairSpectrum.Spectrum)
	if err != nil {
		return err
	}
	return output(out, struct {
		TaskID      string `json:"task_id"`
		CandidateID string `json:"candidate_id"`
		ManifestID  string `json:"manifest_id"`
		SpectrumID  string `json:"spectrum_input_hash"`
	}{rec.RepairSpectrum.TaskID, rec.CandidateID, rec.ManifestID, ranking.InputHash})
}

func previousRepairAnchors(path, runID string) ([]control.RepairAnchor, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("previous repair report unavailable")
	}
	defer f.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("previous repair report outside read bounds")
	}
	var r control.RepairDiagnosis
	if err := canonical.Decode(raw, &r); err != nil {
		return nil, errors.New("invalid previous repair report")
	}
	if r.Version != 1 || r.RunID != runID || len(r.Anchors) > 128 {
		return nil, errors.New("previous repair report binding mismatch")
	}
	for _, a := range r.Anchors {
		if a.CandidateID != r.CandidateID {
			return nil, errors.New("previous repair anchor candidate mismatch")
		}
	}
	return r.Anchors, nil
}
