package repository

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"harness.local/engorch/internal/safepath"
)

// SourceChunk preserves exact committed blob bytes as base64 and binds each
// bounded read to repository, commit, path and Git blob identity.
type SourceChunk struct {
	RepositoryID  string  `json:"repository_id"`
	Commit        string  `json:"commit"`
	Path          string  `json:"path"`
	Blob          string  `json:"blob"`
	Offset        int64   `json:"offset"`
	TotalBytes    int64   `json:"total_bytes"`
	ContentBase64 string  `json:"content_base64"`
	ContentUTF8   *string `json:"content_utf8"`
	ChunkHash     string  `json:"chunk_hash"`
	NextOffset    *int64  `json:"next_offset"`
}

// ReadSource reads only a regular blob from the recorded commit. It never reads
// working-tree bytes or follows a committed symlink. Reads are paginated at
// 32 KiB, and blobs above 64 MiB are explicitly unsupported.
func ReadSource(ctx context.Context, i Identity, path string, offset int64, limit int) (SourceChunk, error) {
	return readSource(ctx, i, path, offset, limit, nil)
}

// SourceDigest binds a full regular committed blob to its raw content SHA-256.
type SourceDigest struct {
	RepositoryID string `json:"repository_id"`
	Commit       string `json:"commit"`
	Path         string `json:"path"`
	Blob         string `json:"blob"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

// DigestSource streams one exact committed blob, retaining at most one byte.
// It shares ReadSource's regular-file, tree, size and process bounds.
func DigestSource(ctx context.Context, i Identity, path string) (SourceDigest, error) {
	digest := sha256.New()
	chunk, err := readSource(ctx, i, path, 0, 1, digest)
	if err != nil {
		return SourceDigest{}, err
	}
	return SourceDigest{chunk.RepositoryID, chunk.Commit, chunk.Path, chunk.Blob, chunk.TotalBytes, hex.EncodeToString(digest.Sum(nil))}, nil
}

// CopySource streams an exact regular committed blob to a caller-owned writer.
// The caller owns destination creation, synchronization and partial-write recovery.
// An error grants no successful-copy evidence and may leave partial output.
func CopySource(ctx context.Context, i Identity, path string, destination io.Writer) (SourceDigest, error) {
	if destination == nil {
		return SourceDigest{}, errors.New("source destination required")
	}
	digest := sha256.New()
	chunk, err := readSource(ctx, i, path, 0, 1, io.MultiWriter(destination, digest))
	if err != nil {
		return SourceDigest{}, err
	}
	return SourceDigest{chunk.RepositoryID, chunk.Commit, chunk.Path, chunk.Blob, chunk.TotalBytes, hex.EncodeToString(digest.Sum(nil))}, nil
}

func readSource(ctx context.Context, i Identity, path string, offset int64, limit int, digest io.Writer) (SourceChunk, error) {
	var result SourceChunk
	id, err := i.ID()
	if err != nil {
		return result, err
	}
	if err := safepath.Relative(path); err != nil {
		return result, err
	}
	if offset < 0 || limit < 1 || limit > 32<<10 {
		return result, errors.New("invalid source read bounds")
	}
	tree, err := git(ctx, i.Root, "rev-parse", "--verify", i.Commit+"^{tree}")
	if err != nil {
		return result, err
	}
	if tree != i.Tree {
		return result, errors.New("recorded source tree mismatch")
	}
	entry, err := git(ctx, i.Root, "--literal-pathspecs", "ls-tree", "-z", i.Tree, "--", path)
	if err != nil {
		return result, err
	}
	meta, name, ok := strings.Cut(strings.TrimSuffix(entry, "\x00"), "\t")
	fields := strings.Fields(meta)
	if !ok || name != path || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return result, errors.New("source path is not an exact regular committed file")
	}
	blob := fields[2]
	if len(blob) != len(i.Commit) || strings.Trim(blob, "0123456789abcdef") != "" {
		return result, errors.New("invalid source blob identity")
	}
	sizeText, err := git(ctx, i.Root, "cat-file", "-s", blob)
	if err != nil {
		return result, err
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil || size < 0 || size > 64<<20 || offset > size {
		return result, errors.New("unsupported blob size or offset")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-replace-objects", "-C", i.Root, "cat-file", "blob", blob)
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	sink := &chunkSink{offset: offset, limit: limit, digest: digest}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	var stderr bounded
	cmd.Stdout = sink
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return result, errors.New("committed blob read failed")
	}
	if sink.total != size || stderr.overflow {
		return result, errors.New("committed blob size changed or diagnostic overflow")
	}
	h := sha256.Sum256(sink.data)
	result = SourceChunk{RepositoryID: id, Commit: i.Commit, Path: path, Blob: blob, Offset: offset, TotalBytes: size, ContentBase64: base64.StdEncoding.EncodeToString(sink.data), ChunkHash: hex.EncodeToString(h[:])}
	if utf8.Valid(sink.data) {
		value := string(sink.data)
		result.ContentUTF8 = &value
	}
	if next := offset + int64(len(sink.data)); next < size {
		result.NextOffset = &next
	}
	return result, nil
}

type chunkSink struct {
	digest        io.Writer
	offset, total int64
	limit         int
	data          []byte
}

// Write drains the blob while retaining only the requested interval.
func (s *chunkSink) Write(b []byte) (int, error) {
	n := len(b)
	if s.total+int64(n) > 64<<20 {
		return 0, errors.New("blob read bound exceeded")
	}
	if s.digest != nil {
		written, err := s.digest.Write(b)
		if err != nil {
			return written, err
		}
		if written != n {
			return written, io.ErrShortWrite
		}
	}
	start := max(int64(0), s.offset-s.total)
	if start < int64(n) && len(s.data) < s.limit {
		end := min(int64(n), start+int64(s.limit-len(s.data)))
		s.data = append(s.data, b[start:end]...)
	}
	s.total += int64(n)
	if s.total > 64<<20 {
		return n, errors.New("blob read bound exceeded")
	}
	return n, nil
}
