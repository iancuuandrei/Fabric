package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/ri"
)

// readRIOrientationQuery shares the bounded, regular-file reader with the
// existing policy commands, then rejects duplicate and unknown JSON fields.
func readRIOrientationQuery[T any](path string) (T, error) {
	var query T
	raw, err := readRegularPolicyJSON(path, 32<<10, "RI query", "32 KiB")
	if err != nil {
		return query, err
	}
	normal, err := canonical.Normalize(raw)
	if err != nil {
		return query, fmt.Errorf("invalid RI query JSON: %w", err)
	}
	if err := canonical.Decode(normal, &query); err != nil {
		return query, fmt.Errorf("invalid RI query selectors: %w", err)
	}
	return query, nil
}

type goRankingEnvelope struct {
	Repository ri.Source                  `json:"repository"`
	Sources    []goGraphSourceObservation `json:"sources"`
	Ranking    ri.GoEngineeringRanking    `json:"ranking"`
}

func riGoRankingCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 5 && len(args) != 6 {
		return errors.New("usage: ri rank EXE EXE_SHA256 SPEC_JSON QUERY_JSON [CACHE_DIR]")
	}
	if len(args) == 6 && args[5] == "" {
		return errors.New("cache directory cannot be empty when supplied")
	}
	path, err := riAbsolutePath(root, args[4])
	if err != nil {
		return err
	}
	query, err := readRIOrientationQuery[ri.GoEngineeringRankingQuery](path)
	if err != nil {
		return err
	}
	if err := ri.ValidateGoEngineeringRankingQuery(query); err != nil {
		return err
	}
	built, err := buildGoGraph(ctx, root, args[1], args[2], args[3], cacheArgument(args, 5))
	if err != nil {
		return err
	}
	ranking, err := ri.QueryGoEngineeringRanking(built.result.Graph, query)
	if err != nil {
		return err
	}
	return output(out, goRankingEnvelope{Repository: built.result.Repository, Sources: built.result.Sources, Ranking: ranking})
}
