package ri

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/safepath"
)

// StageLexicalOverlay writes verified changed bytes and a manifest into a fresh
// controller-owned directory. The caller must journal the exact intent first and
// retain exclusive ownership of inputs and staging. Failures leave partial output;
// this primitive never retries, deletes, publishes or establishes authority.
func StageLexicalOverlay(ctx context.Context, base LexicalManifest, candidate string, changed LexicalManifest, deleted []string, sources map[string][]byte, output string) (ref LexicalOverlayRef, err error) {
	ref = LexicalOverlayRef{Candidate: candidate, ManifestPath: filepath.Join(output, "manifest.jsonl"), SourceRoot: filepath.Join(output, "sources"), Manifest: changed, Deleted: deleted}
	defer func() {
		if err != nil {
			ref = LexicalOverlayRef{}
		}
	}()
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		return ref, errors.New("normalized absolute overlay staging required")
	}
	if _, _, err := ref.scope(base); err != nil {
		return ref, err
	}
	if len(sources) != len(changed.Files) {
		return ref, errors.New("overlay byte scope differs")
	}
	// Validate every byte before creating output, including native blob identities.
	for _, file := range changed.Files {
		if err := ctx.Err(); err != nil {
			return ref, err
		}
		data, ok := sources[file.Path]
		hash := sha256.Sum256(data)
		if !ok || int64(len(data)) != file.Bytes || hex.EncodeToString(hash[:]) != file.SHA256 {
			return ref, errors.New("overlay source differs from manifest")
		}
		blob, err := gitlocal.ObjectID(changed.Source.ObjectFormat, "blob", data)
		if err != nil {
			return ref, err
		}
		if blob != file.Blob {
			return ref, errors.New("overlay Git blob identity differs")
		}
	}
	if err := ctx.Err(); err != nil {
		return ref, err
	}
	if err := safepath.Directory(filepath.Dir(output)); err != nil {
		return ref, err
	}
	parent, err := os.OpenRoot(filepath.Dir(output))
	if err != nil {
		return ref, err
	}
	defer func() { err = errors.Join(err, parent.Close()) }()
	name := filepath.Base(output)
	if err := safepath.Relative(name); err != nil {
		return ref, err
	}
	if err := parent.Mkdir(name, 0700); err != nil {
		return ref, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return ref, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if err := root.Mkdir("sources", 0700); err != nil {
		return ref, err
	}
	written := map[string]bool{}
	for _, file := range changed.Files {
		if err := ctx.Err(); err != nil {
			return ref, err
		}
		if written[file.SHA256] {
			continue
		}
		f, err := root.OpenFile("sources/"+file.SHA256, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return ref, err
		}
		data := sources[file.Path]
		n, writeErr := f.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = io.ErrShortWrite
		}
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return ref, err
		}
		hash, size, _, exists, err := safepath.ReadRegular(root, "sources/"+file.SHA256, 64<<20)
		if err != nil {
			return ref, err
		}
		if !exists || hash != file.SHA256 || size != file.Bytes {
			return ref, errors.New("overlay source readback differs")
		}
		written[file.SHA256] = true
	}
	f, err := root.OpenFile("manifest.jsonl", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ref, err
	}
	if err := errors.Join(changed.WriteRecords(f), f.Sync(), f.Close()); err != nil {
		return ref, err
	}
	_, err = ObserveLexicalOverlay(ctx, base, ref)
	return ref, err
}
