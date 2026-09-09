package cli

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"io"
	"strconv"
)

func riChangedCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 9 && len(args) != 10 && len(args) != 11 {
		return errors.New("usage: ri changed EXE EXE_SHA256 BEFORE_SNAPSHOT BEFORE_ID BEFORE_COMMIT AFTER_SNAPSHOT AFTER_ID AFTER_COMMIT")
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	beforeIdentity, err := repository.DiscoverCommit(ctx, root, cfg.Repository, args[5])
	if err != nil {
		return err
	}
	afterIdentity, err := repository.DiscoverCommit(ctx, root, cfg.Repository, args[8])
	if err != nil {
		return err
	}
	beforeSource, err := ri.FromRepository(beforeIdentity)
	if err != nil {
		return err
	}
	afterSource, err := ri.FromRepository(afterIdentity)
	if err != nil {
		return err
	}
	executable, err := riAbsolutePath(root, args[1])
	if err != nil {
		return err
	}
	beforePath, err := riAbsolutePath(root, args[3])
	if err != nil {
		return err
	}
	afterPath, err := riAbsolutePath(root, args[6])
	if err != nil {
		return err
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	if len(args) >= 10 {
		limit, err := strconv.Atoi(args[9])
		if err != nil {
			return err
		}
		var cursor *string
		if len(args) == 11 {
			cursor = &args[10]
		}
		page, err := client.ChangedPage(ctx, ri.SnapshotRef{Path: beforePath, ID: args[4], Source: beforeSource}, ri.SnapshotRef{Path: afterPath, ID: args[7], Source: afterSource}, beforeIdentity, afterIdentity, limit, cursor)
		if err != nil {
			return err
		}
		return output(out, page)
	}
	result, err := client.Changed(ctx, ri.SnapshotRef{Path: beforePath, ID: args[4], Source: beforeSource}, ri.SnapshotRef{Path: afterPath, ID: args[7], Source: afterSource}, beforeIdentity, afterIdentity)
	if err != nil {
		return err
	}
	return output(out, result)
}
