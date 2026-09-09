package ri

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// PublishImport revalidates imported staging before content-addressed publication.
// Caller must journal publication intent first. Store errors can leave pending
// bytes requiring explicit recovery; this function never retries publication.
func PublishImport(ctx context.Context, plan ImportPlan, store Store) (SnapshotRef, error) {
	expected, err := ObserveImport(ctx, plan)
	if err != nil {
		return SnapshotRef{}, err
	}
	root, err := os.OpenRoot(filepath.Dir(plan.OutputPath))
	if err != nil {
		return SnapshotRef{}, err
	}
	data, readErr := stored(root, filepath.Base(plan.OutputPath), expected.SnapshotID)
	closeErr := root.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return SnapshotRef{}, err
	}
	path, err := store.Publish(expected.SnapshotID, data)
	if err != nil {
		return SnapshotRef{}, err
	}
	ref := SnapshotRef{Path: path, ID: expected.SnapshotID, Source: expected.Source}
	client := Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	if _, err := client.Inspect(ctx, ref); err != nil {
		return SnapshotRef{}, err
	}
	return ref, nil
}
