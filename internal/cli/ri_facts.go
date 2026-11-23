package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
)

type goFactsResult struct {
	Repository ri.Source               `json:"repository"`
	Source     repository.SourceDigest `json:"source"`
	Facts      ri.GoFileFacts          `json:"facts"`
}

var errGoFactsSourceTooLarge = errors.New("committed Go source exceeds the 1 MiB analysis bound")

type goFactsSourceBuffer struct{ bytes.Buffer }

// Write retains committed source bytes within the Go facts input ceiling.
func (b *goFactsSourceBuffer) Write(value []byte) (int, error) {
	if len(value) > (1<<20)-b.Len() {
		return 0, errGoFactsSourceTooLarge
	}
	return b.Buffer.Write(value)
}

func riGoFactsCommand(ctx context.Context, root string, args []string, out io.Writer) error {
	if len(args) != 4 && len(args) != 5 {
		return errors.New("usage: ri facts EXE EXE_SHA256 PATH [CACHE_DIR]")
	}
	cfg, err := configuration(root)
	if err != nil {
		return err
	}
	identity, err := repository.Discover(ctx, root, cfg.Repository)
	if err != nil {
		return err
	}
	repositorySource, err := ri.FromRepository(identity)
	if err != nil {
		return err
	}
	repositoryID, err := identity.ID()
	if err != nil {
		return err
	}
	executable, err := riAbsolutePath(root, args[1])
	if err != nil {
		return err
	}
	cacheDir := ""
	if len(args) == 5 {
		if !filepath.IsAbs(args[4]) || filepath.Clean(args[4]) != args[4] {
			return errors.New("cache directory must be an absolute clean path")
		}
		cacheDir, err = riAbsolutePath(root, args[4])
		if err != nil {
			return err
		}
		volumeRoot := filepath.VolumeName(cacheDir) + string(filepath.Separator)
		if filepath.Clean(cacheDir) != cacheDir || cacheDir == volumeRoot {
			return errors.New("cache directory must be a clean non-root path")
		}
	}
	source := &goFactsSourceBuffer{}
	digest, err := repository.CopySource(ctx, identity, args[3], source)
	if err != nil {
		return err
	}
	if digest.RepositoryID != repositoryID || digest.Commit != identity.Commit || digest.Path != args[3] || digest.Bytes != int64(source.Len()) || source.Len() > 1<<20 {
		return errors.New("committed source binding mismatch")
	}
	client := ri.Client{Executable: executable, ExecutableHash: args[2]}
	facts, err := client.GoFileFacts(ctx, args[3], source.Bytes(), cacheDir)
	if err != nil {
		return err
	}
	return output(out, goFactsResult{Repository: repositorySource, Source: digest, Facts: facts})
}
