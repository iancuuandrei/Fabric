package cli

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"io"
	"strconv"
)

func riRelatedCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 10 && len(args) != 11 {
		return errors.New("usage: ri related EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID NODE RELATION DIRECTION PRODUCER LIMIT [CURSOR]")
	}
	limit, err := strconv.Atoi(args[9])
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
	if len(args) == 11 {
		after = &args[10]
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	page, err := client.Neighbors(ctx, ri.SnapshotRef{Path: artifact, ID: args[4], Source: source}, ri.NeighborQuery{Node: args[5], Relation: args[6], Direction: args[7], Producer: args[8], Limit: limit}, after)
	if err != nil {
		return err
	}
	return output(out, page)
}
