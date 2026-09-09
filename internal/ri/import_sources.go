package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"sort"
	"strings"
)

// VerifyCommittedSources compares selected producer input hashes with regular
// blobs in the exact recorded Git commit. It reads no working-tree source bytes.
// It does not materialize files or authenticate the semantic producer itself.
func VerifyCommittedSources(ctx context.Context, plan ImportPlan) ([]repository.SourceDigest, error) {
	if _, err := plan.ID(); err != nil {
		return nil, err
	}
	observed, err := repository.DiscoverCommit(ctx, plan.Repository.Root, plan.Repository.Name, plan.Repository.Commit)
	if err != nil {
		return nil, err
	}
	if observed != plan.Repository {
		return nil, errors.New("import repository identity is no longer observable")
	}
	inputs := map[string]string{}
	found := false
	for _, producer := range plan.Request.Manifest.Producers {
		if producer.ID != plan.Request.Producer {
			continue
		}
		if found {
			return nil, errors.New("duplicate import producer")
		}
		found = true
		for _, input := range producer.Inputs {
			path, ok := strings.CutPrefix(input.Name, "source:")
			if !ok {
				continue
			}
			if _, exists := inputs[path]; exists {
				return nil, errors.New("duplicate source input")
			}
			inputs[path] = input.SHA256
		}
	}
	if !found || len(inputs) != len(plan.Request.Sources) {
		return nil, errors.New("import source registry mismatch")
	}
	paths := make([]string, 0, len(inputs))
	for path := range inputs {
		if _, ok := plan.Request.Sources[path]; !ok {
			return nil, errors.New("unselected committed source input")
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]repository.SourceDigest, 0, len(paths))
	var total int64
	for _, path := range paths {
		digest, err := repository.DigestSource(ctx, plan.Repository, path)
		if err != nil {
			return nil, err
		}
		total += digest.Bytes
		if total > 64<<20 || digest.SHA256 != inputs[path] {
			return nil, errors.New("committed import source hash or size mismatch")
		}
		result = append(result, digest)
	}
	return result, nil
}
