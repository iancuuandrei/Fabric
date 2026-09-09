package control

import (
	"context"
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/ri"
)

// SelectRuntimeRI revalidates the journal's confirmed publication for a runtime.
// It never imports, publishes, repairs, or selects an arbitrary artifact path.
// The runtime must persist this binding before starting its provider thread.
func SelectRuntimeRI(ctx context.Context, path string) (codexruntime.RIBinding, error) {
	s, err := Inspect(path)
	if err != nil {
		return codexruntime.RIBinding{}, err
	}
	binding, err := runtimeRISelection(s)
	if err != nil {
		return codexruntime.RIBinding{}, err
	}
	// Store.Read rejects unresolved pending publications even if final bytes exist.
	if _, err := (ri.Store{Directory: s.RIPublish.Intent.Directory}).Read(binding.Snapshot.ID); err != nil {
		return codexruntime.RIBinding{}, err
	}
	if _, err := (ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256}).Inspect(ctx, binding.Snapshot); err != nil {
		return codexruntime.RIBinding{}, err
	}
	return binding, nil
}

// runtimeRISelection reconstructs authority without filesystem reads during replay.
func runtimeRISelection(s Snapshot) (codexruntime.RIBinding, error) {
	if s.RIPublish == nil || s.RIPublish.Outcome != "CONFIRMED" || s.RIPublish.Observation == nil || s.RIPublish.Observation.Artifact == nil || s.RIImport == nil || s.RIImport.Outcome != "CONFIRMED" || s.RIImport.Observation == nil || s.RIImport.Observation.Artifact == nil {
		return codexruntime.RIBinding{}, errors.New("runtime RI requires a confirmed import and publication")
	}
	ref := *s.RIPublish.Observation.Artifact
	imported := s.RIImport.Observation.Artifact
	plan := s.RIImport.Intent.Plan
	importID, err := s.RIImport.Intent.Intent.ID()
	if err != nil {
		return codexruntime.RIBinding{}, err
	}
	publicationInput, err := canonical.Hash("harness.ri.publication.v1", struct {
		ImportID  string           `json:"import_id"`
		Directory string           `json:"directory"`
		Artifact  ri.ImportReceipt `json:"artifact"`
	}{importID, s.RIPublish.Intent.Directory, *imported})
	if err != nil {
		return codexruntime.RIBinding{}, err
	}
	if publicationInput != s.RIPublish.Intent.Intent.InputHash {
		return codexruntime.RIBinding{}, errors.New("runtime RI publication belongs to a different import intent")
	}
	if ref.ID != imported.SnapshotID || ref.Source != imported.Source || plan.Repository != s.Creation.Repository || ref.Path != filepath.Join(s.RIPublish.Intent.Directory, ref.ID+".jsonl") {
		return codexruntime.RIBinding{}, errors.New("runtime RI publication/import binding mismatch")
	}
	binding := codexruntime.RIBinding{Snapshot: ref, Executable: plan.Executable, ExecutableSHA256: plan.ExecutableSHA256}
	if err := binding.Validate(s.Creation.Repository); err != nil {
		return codexruntime.RIBinding{}, err
	}
	return binding, nil
}
