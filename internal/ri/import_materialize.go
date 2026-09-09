package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
	"os"
	"path/filepath"
)

// MaterializeSources creates exact committed input copies at planned locations.
// Caller must journal intent and own destination directories before invoking.
// All destinations must be new; failures leave partial evidence without retry,
// deletion, directory creation or rollback. Returned success covers every copy.
func MaterializeSources(ctx context.Context, plan ImportPlan) ([]repository.SourceDigest, error) {
	expected, err := VerifyCommittedSources(ctx, plan)
	if err != nil {
		return nil, err
	}
	for _, source := range expected {
		destination := plan.Request.Sources[source.Path]
		if err := safepath.Directory(filepath.Dir(destination)); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			return nil, errors.New("source copy destination must be new")
		}
	}
	copied := make([]repository.SourceDigest, 0, len(expected))
	for _, source := range expected {
		destination := plan.Request.Sources[source.Path]
		root, err := os.OpenRoot(filepath.Dir(destination))
		if err != nil {
			return nil, err
		}
		file, err := root.OpenFile(filepath.Base(destination), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			root.Close()
			return nil, err
		}
		observed, copyErr := repository.CopySource(ctx, plan.Repository, source.Path, file)
		syncErr := file.Sync()
		closeErr := file.Close()
		rootErr := root.Close()
		if err := errors.Join(copyErr, syncErr, closeErr, rootErr); err != nil {
			return nil, err
		}
		if observed != source {
			return nil, errors.New("materialized source differs from committed evidence")
		}
		copied = append(copied, observed)
	}
	return copied, nil
}
