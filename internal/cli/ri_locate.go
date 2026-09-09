package cli

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"io"
	"strconv"
)

func riLocateCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 9 && len(args) != 10 {
		return errors.New("usage: ri locate EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FILE OFFSET PRODUCER LIMIT [CURSOR]")
	}
	offset, err := strconv.Atoi(args[6])
	if err != nil {
		return err
	}
	limit, err := strconv.Atoi(args[8])
	if err != nil {
		return err
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	source, err := ri.FromRepository(identity)
	if err != nil {
		return err
	}
	executable, err := riAbsolutePath(root, args[1])
	if err != nil {
		return err
	}
	artifact, err := riAbsolutePath(root, args[3])
	if err != nil {
		return err
	}
	var after *string
	if len(args) == 10 {
		after = &args[9]
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	page, err := client.Locate(ctx, ri.SnapshotRef{Path: artifact, ID: args[4], Source: source}, ri.LocateQuery{Path: args[5], Offset: offset, Producer: args[7], Limit: limit}, after)
	if err != nil {
		return err
	}
	return output(out, page)
}
