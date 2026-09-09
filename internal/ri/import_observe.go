package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"os"
	"path/filepath"
)

// ObserveImport recomputes expected bytes from exact bound inputs without writing.
// It confirms staging only when its bytes match the deterministic import result.
// Missing/changed inputs or staging produce errors, never permission to retry.
func ObserveImport(ctx context.Context, plan ImportPlan) (ImportReceipt, error) {
	if _, err := plan.ID(); err != nil {
		return ImportReceipt{}, err
	}
	client := Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	result, err := client.Call(ctx, map[string]any{"operation": "scip_import_expected", "request": plan.Request})
	if err != nil {
		return ImportReceipt{}, err
	}
	var expected struct {
		SnapshotID string `json:"snapshot_id"`
		Bytes      int    `json:"bytes"`
		Source     Source `json:"source"`
	}
	if err := canonical.Decode(result, &expected); err != nil {
		return ImportReceipt{}, err
	}
	if expected.Source != plan.Request.Source || expected.Bytes < 1 || expected.Bytes > 64<<20 {
		return ImportReceipt{}, errors.New("import expected result mismatch")
	}
	if err := safepath.RequireDigest(expected.SnapshotID); err != nil {
		return ImportReceipt{}, err
	}
	if err := safepath.Directory(filepath.Dir(plan.OutputPath)); err != nil {
		return ImportReceipt{}, err
	}
	root, err := os.OpenRoot(filepath.Dir(plan.OutputPath))
	if err != nil {
		return ImportReceipt{}, err
	}
	defer root.Close()
	bytes, err := stored(root, filepath.Base(plan.OutputPath), expected.SnapshotID)
	if err != nil {
		return ImportReceipt{}, err
	}
	if len(bytes) != expected.Bytes {
		return ImportReceipt{}, errors.New("staging size differs from expected import")
	}
	return ImportReceipt{SnapshotID: expected.SnapshotID, Bytes: expected.Bytes, Source: expected.Source, OutputPath: plan.OutputPath}, nil
}
