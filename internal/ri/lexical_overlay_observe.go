package ri

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/safepath"
)

// ObserveLexicalOverlay rechecks staged bytes against an independently admitted
// descriptor. It performs no writes and returns the merged scope identity only
// on complete success. Caller-owned immutable staging is required throughout use.
func ObserveLexicalOverlay(ctx context.Context, base LexicalManifest, ref LexicalOverlayRef) (id string, err error) {
	id, _, err = ref.scope(base)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			id = ""
		}
	}()
	expected := sha256.New()
	if err := ref.Manifest.WriteRecords(expected); err != nil {
		return "", err
	}
	if err := safepath.Directory(filepath.Dir(ref.ManifestPath)); err != nil {
		return "", err
	}
	manifestRoot, err := os.OpenRoot(filepath.Dir(ref.ManifestPath))
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, manifestRoot.Close()) }()
	hash, _, _, exists, err := safepath.ReadRegular(manifestRoot, filepath.Base(ref.ManifestPath), 512<<20)
	if err != nil {
		return "", err
	}
	if !exists || hash != hex.EncodeToString(expected.Sum(nil)) {
		return "", errors.New("overlay manifest artifact differs")
	}
	if err := safepath.Directory(ref.SourceRoot); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(ref.SourceRoot)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	verified := map[string]LexicalFile{}
	for _, file := range ref.Manifest.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if previous, ok := verified[file.SHA256]; ok {
			if previous.Blob != file.Blob || previous.Bytes != file.Bytes {
				return "", errors.New("overlay duplicate content has conflicting blob identity")
			}
			continue
		}
		var data bytes.Buffer
		hash, size, _, exists, err := safepath.CopyRegular(root, file.SHA256, 64<<20, &data)
		if err != nil {
			return "", err
		}
		if !exists || hash != file.SHA256 || size != file.Bytes {
			return "", errors.New("overlay source artifact differs")
		}
		blob, err := gitlocal.ObjectID(ref.Manifest.Source.ObjectFormat, "blob", data.Bytes())
		if err != nil {
			return "", err
		}
		if blob != file.Blob {
			return "", errors.New("overlay source Git identity differs")
		}
		verified[file.SHA256] = file
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return id, nil
}
