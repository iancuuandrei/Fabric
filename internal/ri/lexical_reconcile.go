package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/safepath"
	"os"
	"path/filepath"
)

// ObserveLexicalStage rechecks completed controller-owned staging without writes.
// A completion record alone is insufficient: committed scope, source bytes,
// shard bytes and the pinned Rust reader are all checked. The caller must keep
// staging exclusively owned and immutable throughout observation and later use.
// Recovery currently admits individual shard files up to 1 GiB; larger files
// remain unresolved rather than bypassing the bounded regular-file reader.
func ObserveLexicalStage(ctx context.Context, plan LexicalPlan) (LexicalRef, error) {
	completion, err := ReadLexicalCompletion(plan)
	if err != nil {
		return LexicalRef{}, err
	}
	observed, err := ObserveLexical(ctx, plan.Repository)
	if err != nil {
		return LexicalRef{}, err
	}
	if err := plan.ValidateManifest(observed.Manifest); err != nil {
		return LexicalRef{}, err
	}
	root, err := os.OpenRoot(plan.StageRoot)
	if err != nil {
		return LexicalRef{}, err
	}
	defer root.Close()
	seen := map[string]bool{}
	for _, file := range observed.Manifest.Files {
		if seen[file.SHA256] {
			continue
		}
		seen[file.SHA256] = true
		hash, size, _, exists, err := safepath.ReadRegular(root, "sources/"+file.SHA256, 64<<20)
		if err != nil {
			return LexicalRef{}, err
		}
		if !exists || hash != file.SHA256 || size != file.Bytes {
			return LexicalRef{}, errors.New("staged lexical source changed")
		}
	}
	for _, shard := range completion.Build.Shards {
		for n, name := range []string{"lookup.bin", "index.bin", "files.bin"} {
			hash, _, _, exists, err := safepath.ReadRegular(root, "index/"+shard.Directory+"/"+name, 1<<30)
			if err != nil {
				return LexicalRef{}, err
			}
			if !exists || hash != shard.Hashes[n] {
				return LexicalRef{}, errors.New("staged lexical shard changed")
			}
		}
	}
	ref := LexicalRef{ManifestPath: filepath.Join(plan.StageRoot, "manifest.jsonl"), SourceRoot: filepath.Join(plan.StageRoot, "sources"), IndexPath: filepath.Join(plan.StageRoot, "index"), Manifest: observed.Manifest, Build: completion.Build}
	client := Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	// Opening the disk reader validates full path coverage even when output is
	// limited. Exact source content for every path was independently checked above.
	if _, err := client.SearchLexical(ctx, ref, LexicalQuery{Pattern: "", Fixed: true}, 1, nil); err != nil {
		return LexicalRef{}, err
	}
	return ref, nil
}
