package ri

import (
	"context"
	"errors"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/safepath"
	"io"
	"os"
	"path/filepath"
)

type lexicalSyncFile struct{ *os.File }

// Close syncs staged bytes before closing the underlying file.
func (f lexicalSyncFile) Close() error { return errors.Join(f.Sync(), f.File.Close()) }

type lexicalDiscard struct{}

// Write consumes duplicate content while the Git batch independently hashes it.
func (lexicalDiscard) Write(b []byte) (int, error) { return len(b), nil }

// Close completes a duplicate-content sink without filesystem effects.
func (lexicalDiscard) Close() error { return nil }

// MaterializeLexical copies a complete admitted regular-file scope into a fresh
// directory using digest filenames. It deduplicates equal content, streams through
// one Git batch process and syncs each new file. Caller must retain its durable
// intent, own the parent directory and publish only after success. No retry or
// cleanup is performed when an error leaves partial staging output.
func MaterializeLexical(ctx context.Context, identity repository.Identity, manifest LexicalManifest, output string) error {
	if _, err := manifest.ID(); err != nil {
		return err
	}
	observed, err := repository.DiscoverCommit(ctx, identity.Root, identity.Name, identity.Commit)
	if err != nil {
		return err
	}
	source, err := FromRepository(identity)
	if err != nil {
		return err
	}
	if observed != identity || source != manifest.Source {
		return errors.New("lexical materialization source changed")
	}
	if !filepath.IsAbs(output) {
		return errors.New("absolute lexical source staging required")
	}
	parentPath := filepath.Dir(output)
	if err := safepath.Directory(parentPath); err != nil {
		return err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return err
	}
	defer parent.Close()
	name := filepath.Base(output)
	if err := safepath.Relative(name); err != nil {
		return err
	}
	if err := parent.Mkdir(name, 0700); err != nil {
		return err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	expected := map[string]LexicalFile{}
	for _, file := range manifest.Files {
		expected[file.Path] = file
	}
	written := map[string]bool{}
	seen := map[string]bool{}
	err = repository.CopySourceBatch(ctx, identity, func(entry repository.SourceEntry, size int64) (io.WriteCloser, error) {
		file, ok := expected[entry.Path]
		if !ok || file.Blob != entry.Object || file.Bytes != size || seen[entry.Path] {
			return nil, errors.New("lexical copy scope differs from manifest")
		}
		if written[file.SHA256] {
			return lexicalDiscard{}, nil
		}
		out, err := root.OpenFile(file.SHA256, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		written[file.SHA256] = true
		return lexicalSyncFile{out}, nil
	}, func(entry repository.SourceEntry, digest *repository.SourceDigest) error {
		if digest == nil {
			return nil
		}
		file, ok := expected[entry.Path]
		if !ok || digest.RepositoryID != source.RepositoryID || digest.Commit != source.Commit || digest.Path != file.Path || digest.Blob != file.Blob || digest.Bytes != file.Bytes || digest.SHA256 != file.SHA256 {
			return errors.New("copied lexical bytes differ from manifest")
		}
		seen[entry.Path] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("lexical materialization incomplete")
	}
	return nil
}
