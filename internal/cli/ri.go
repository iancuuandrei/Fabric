package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strconv"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

func riCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) < 1 {
		return errors.New("ri requires facts, modules, graph, query, rank, semantic, candidate-query, context, topology, status, coverage, deps, rdeps or path")
	}
	operation := args[0]
	if operation == "modules" {
		return riGoModulesCommand(ctx, root, args, out)
	}
	if operation == "facts" {
		return riGoFactsCommand(ctx, root, args, out)
	}
	if operation == "graph" {
		return riGoGraphCommand(ctx, root, args, out)
	}
	if operation == "query" {
		return riGoQueryCommand(ctx, root, args, out)
	}
	if operation == "rank" {
		return riGoRankingCommand(ctx, root, args, out)
	}
	if operation == "candidate-query" {
		return riGoCandidateQueryCommand(ctx, root, args, out)
	}
	if operation == "context" {
		return riGoContextCommand(ctx, root, args, out)
	}
	if operation == "topology" {
		return riGoTopologyCommand(ctx, root, args, out)
	}
	if operation == "search" {
		return riSearchCommand(ctx, root, args, out)
	}
	switch operation {
	case "prepare-lexical", "lexical", "lexical-ref", "prepare-overlay", "overlay", "overlay-ref":
		return riLexicalCommand(ctx, root, args, out)
	case "runtime-binding", "close-producer", "prepare-producer", "produce", "bind-import", "prepare-import", "import", "prepare-publish", "publish", "prepare-publication-recovery", "recover-publication":
		return riLifecycleCommand(ctx, root, args, out)
	}
	if operation == "changed" {
		return riChangedCommand(ctx, root, args, out)
	}
	if operation == "related" {
		return riRelatedCommand(ctx, root, args, out)
	}
	if operation == "locate" {
		return riLocateCommand(ctx, root, args, out)
	}
	valid := (operation == "semantic" && (len(args) == 6 || len(args) == 7)) || (operation == "path" && len(args) == 12) || (operation == "status" && len(args) == 5) || (operation == "coverage" && len(args) == 8) || ((operation == "deps" || operation == "rdeps" || operation == "definition" || operation == "references") && (len(args) == 8 || len(args) == 9))
	if !valid {
		return errors.New("usage: ri semantic EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID QUERY_JSON [CURSOR]; ri rank EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]; ri modules; ri facts EXE EXE_SHA256 PATH [CACHE_DIR]; ri graph EXE EXE_SHA256 SPEC_JSON [CACHE_DIR]; ri query EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]; ri candidate-query RUN EXE EXE_SHA256 BASE_SPEC_JSON QUERY_JSON [CACHE_DIR]; ri context EXE EXE_SHA256 SPEC_JSON OBJECTIVE [CACHE_DIR]; ri topology EXE EXE_SHA256 SPEC_JSON CHANGED_PATHS_JSON MAX_GROUP_FILES [CACHE_DIR]; ri status|coverage|deps|rdeps|path EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID; coverage adds NODE RELATION DIRECTION; deps/rdeps add NODE PRODUCER LIMIT [CURSOR]; path adds FROM TO RELATION DIRECTION PRODUCER MAX_DEPTH MAX_EDGES")
	}
	var semanticQuery ri.SemanticSnapshotQuery
	if operation == "semantic" {
		queryPath, err := riAbsolutePath(root, args[5])
		if err != nil {
			return err
		}
		semanticQuery, err = readRIOrientationQuery[ri.SemanticSnapshotQuery](queryPath)
		if err != nil {
			return err
		}
		if err := ri.ValidateSemanticSnapshotQuery(semanticQuery); err != nil {
			return err
		}
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
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	ref := ri.SnapshotRef{Path: artifact, ID: args[4], Source: source}
	if operation == "semantic" {
		var after *string
		if len(args) == 7 {
			after = &args[6]
		}
		result, err := client.QuerySemanticSnapshot(ctx, ref, semanticQuery, after)
		if err != nil {
			return err
		}
		return output(out, result)
	}
	if operation == "definition" || operation == "references" {
		limit, err := strconv.Atoi(args[7])
		if err != nil {
			return err
		}
		var after *string
		if len(args) == 9 {
			after = &args[8]
		}
		result, err := client.Occurrences(ctx, ref, ri.OccurrenceQuery{Symbol: args[5], Producer: args[6], Definitions: operation == "definition", Limit: limit}, after)
		if err != nil {
			return err
		}
		return output(out, result)
	}
	if operation == "path" {
		depth, err := strconv.Atoi(args[10])
		if err != nil {
			return err
		}
		edges, err := strconv.Atoi(args[11])
		if err != nil {
			return err
		}
		result, err := client.Path(ctx, ref, ri.PathQuery{From: args[5], To: args[6], Relation: args[7], Direction: args[8], Producer: args[9], MaxDepth: depth, MaxEdges: edges})
		if err != nil {
			return err
		}
		return output(out, result)
	}
	if operation == "deps" || operation == "rdeps" {
		limit, err := strconv.Atoi(args[7])
		if err != nil {
			return err
		}
		direction := "OUTGOING"
		if operation == "rdeps" {
			direction = "INCOMING"
		}
		var after *string
		if len(args) == 9 {
			after = &args[8]
		}
		result, err := client.Neighbors(ctx, ref, ri.NeighborQuery{Node: args[5], Relation: "DEPENDS_ON", Direction: direction, Producer: args[6], Limit: limit}, after)
		if err != nil {
			return err
		}
		return output(out, result)
	}
	if operation == "status" {
		result, err := client.Inspect(ctx, ref)
		if err != nil {
			return err
		}
		return output(out, result)
	}
	result, err := client.Coverage(ctx, ref, ri.CoverageScope{Node: args[5], Relation: args[6], Direction: args[7]})
	if err != nil {
		return err
	}
	return output(out, result)
}

func riGoModulesCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: ri modules")
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	inventory, err := ri.CollectGoModuleInventory(ctx, identity)
	if err != nil {
		return err
	}
	return output(out, inventory)
}

func riAbsolutePath(root, path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return filepath.Abs(path)
}
