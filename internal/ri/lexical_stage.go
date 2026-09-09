package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
	"io"
	"os"
	"path/filepath"
)

// StageLexical executes one fully bound indexing plan in new local staging.
// The controller must persist intent first. Failure leaves partial output UNKNOWN;
// this function never retries, reuses, removes or publishes the directory.
func StageLexical(ctx context.Context, plan LexicalPlan, manifest LexicalManifest) (LexicalRef, error) {
	if err := plan.ValidateManifest(manifest); err != nil {
		return LexicalRef{}, err
	}
	if err := ctx.Err(); err != nil {
		return LexicalRef{}, err
	}
	exeRoot, err := os.OpenRoot(filepath.Dir(plan.Executable))
	if err != nil {
		return LexicalRef{}, err
	}
	executable, err := exeRoot.Open(filepath.Base(plan.Executable))
	if err != nil {
		_ = exeRoot.Close()
		return LexicalRef{}, err
	}
	info, err := executable.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
		_ = executable.Close()
		_ = exeRoot.Close()
		return LexicalRef{}, errors.New("invalid lexical executable file")
	}
	digest := sha256.New()
	n, readErr := io.Copy(digest, io.LimitReader(executable, (256<<20)+1))
	executableCloseErr := executable.Close()
	closeErr := exeRoot.Close()
	if err := errors.Join(readErr, executableCloseErr, closeErr); err != nil {
		return LexicalRef{}, err
	}
	if n > 256<<20 || hex.EncodeToString(digest.Sum(nil)) != plan.ExecutableSHA256 {
		return LexicalRef{}, errors.New("lexical executable pin mismatch")
	}
	if err := safepath.Directory(filepath.Dir(plan.StageRoot)); err != nil {
		return LexicalRef{}, err
	}
	parent, err := os.OpenRoot(filepath.Dir(plan.StageRoot))
	if err != nil {
		return LexicalRef{}, err
	}
	defer parent.Close()
	name := filepath.Base(plan.StageRoot)
	if err := safepath.Relative(name); err != nil {
		return LexicalRef{}, err
	}
	if err := parent.Mkdir(name, 0700); err != nil {
		return LexicalRef{}, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return LexicalRef{}, err
	}
	defer root.Close()
	ref := LexicalRef{ManifestPath: filepath.Join(plan.StageRoot, "manifest.jsonl"), SourceRoot: filepath.Join(plan.StageRoot, "sources"), IndexPath: filepath.Join(plan.StageRoot, "index"), Manifest: manifest}
	if err := MaterializeLexical(ctx, plan.Repository, manifest, ref.SourceRoot); err != nil {
		return LexicalRef{}, err
	}
	file, err := root.OpenFile("manifest.jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return LexicalRef{}, err
	}
	writeErr := manifest.WriteRecords(file)
	syncErr := file.Sync()
	closeErr = file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return LexicalRef{}, err
	}
	client := Client{Executable: plan.Executable, ExecutableHash: plan.ExecutableSHA256}
	if err := client.ValidateLexicalArtifact(ctx, ref.ManifestPath, manifest); err != nil {
		return LexicalRef{}, err
	}
	ref.Build, err = client.BuildLexical(ctx, manifest, ref.ManifestPath, ref.SourceRoot, ref.IndexPath, plan.BatchBytes, plan.BatchFiles)
	if err != nil {
		return LexicalRef{}, err
	}
	for _, shard := range ref.Build.Shards {
		for _, name := range []string{"lookup.bin", "index.bin", "files.bin"} {
			path := filepath.Join("index", shard.Directory, name)
			file, err := root.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return LexicalRef{}, err
			}
			syncErr := file.Sync()
			closeErr := file.Close()
			if err := errors.Join(syncErr, closeErr); err != nil {
				return LexicalRef{}, err
			}
		}
	}
	planID, err := plan.ID()
	if err != nil {
		return LexicalRef{}, err
	}
	buildID, err := ref.Build.ID()
	if err != nil {
		return LexicalRef{}, err
	}
	record, err := canonical.Bytes(LexicalCompletion{PlanID: planID, BuildID: buildID, Build: ref.Build})
	if err != nil {
		return LexicalRef{}, err
	}
	completion, err := root.OpenFile("completion.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return LexicalRef{}, err
	}
	_, writeErr = completion.Write(record)
	syncErr = completion.Sync()
	closeErr = completion.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return LexicalRef{}, err
	}
	return ref, nil
}
