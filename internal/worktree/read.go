package worktree

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/safepath"
)

// SourceChunk binds a bounded byte page to the entire admitted candidate.
type SourceChunk struct {
	CandidateID   string  `json:"candidate_id"`
	Path          string  `json:"path"`
	SHA256        string  `json:"sha256"`
	Size          int64   `json:"size"`
	Offset        int64   `json:"offset"`
	ContentBase64 string  `json:"content_base64"`
	ContentUTF8   *string `json:"content_utf8"`
	NextOffset    *int64  `json:"next_offset"`
}

// SourceFile is one bounded raw-byte read from an admitted candidate. It is
// intended for trusted in-process consumers that already hold a workspace
// read lease; unlike SourceChunk it does not base64-encode the bytes.
type SourceFile struct {
	CandidateID string
	Path        string
	SHA256      string
	Size        int64
	Content     []byte
	NextOffset  *int64
	Err         error
}

type pageSink struct {
	offset, position int64
	limit            int
	bytes            []byte
}

// Write retains only the requested interval while consuming the full hash stream.
func (p *pageSink) Write(b []byte) (int, error) {
	n := len(b)
	start := max(p.offset-p.position, 0)
	end := min(int64(n), p.offset+int64(p.limit)-p.position)
	if start < end {
		p.bytes = append(p.bytes, b[start:end]...)
	}
	p.position += int64(n)
	return n, nil
}

// ReadSource reads a candidate file under a caller-held workspace lease. It
// rejects whole-candidate drift before and after reading, including unrelated
// file edits. It does not establish an OS confinement or immutable storage claim.
func ReadSource(ctx context.Context, binding Binding, expected Candidate, path string, offset int64, limit int) (SourceChunk, error) {
	if offset < 0 || offset > 64<<20 || limit < 1 || limit > 32768 {
		return SourceChunk{}, errors.New("invalid candidate read bounds")
	}
	if err := safepath.Writable(path); err != nil {
		return SourceChunk{}, err
	}
	if err := expected.ValidateBinding(binding); err != nil {
		return SourceChunk{}, err
	}
	id, err := expected.ID()
	if err != nil {
		return SourceChunk{}, err
	}
	before, files, err := Capture(ctx, binding)
	if err != nil {
		return SourceChunk{}, err
	}
	if before != expected {
		return SourceChunk{}, errors.New("candidate changed before read")
	}
	var selected *FileState
	for i := range files {
		if files[i].Path == path {
			selected = &files[i]
			break
		}
	}
	if selected == nil {
		return SourceChunk{}, errors.New("path absent from candidate")
	}
	root, err := os.OpenRoot(binding.Request.Path)
	if err != nil {
		return SourceChunk{}, err
	}
	sink := &pageSink{offset: offset, limit: limit}
	hash, size, executable, exists, readErr := safepath.CopyRegular(root, path, 64<<20, sink)
	closeErr := root.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return SourceChunk{}, err
	}
	if !exists || hash != selected.Hash || executable != selected.Executable || offset > size {
		return SourceChunk{}, errors.New("candidate file observation mismatch")
	}
	after, err := Fingerprint(ctx, binding)
	if err != nil {
		return SourceChunk{}, err
	}
	if after != expected {
		return SourceChunk{}, errors.New("candidate changed during read")
	}
	chunk := SourceChunk{CandidateID: id, Path: path, SHA256: hash, Size: size, Offset: offset, ContentBase64: base64.StdEncoding.EncodeToString(sink.bytes)}
	if utf8.Valid(sink.bytes) {
		text := string(sink.bytes)
		chunk.ContentUTF8 = &text
	}
	if next := offset + int64(len(sink.bytes)); next < size {
		chunk.NextOffset = &next
	}
	return chunk, nil
}

// ReadSources reads a bounded set of candidate files while doing only one
// whole-candidate observation before and after the batch. Each successful
// selected-file read is still hashed and compared with the captured manifest.
// The caller must hold the workspace read lease, as with ReadSource.
//
// Per-file observation errors are returned in the matching SourceFile.Err;
// candidate drift or invalid batch arguments fail the entire call.
func ReadSources(ctx context.Context, binding Binding, expected Candidate, paths []string, limit int) ([]SourceFile, error) {
	const maxBatchFiles = 24
	if len(paths) < 1 || len(paths) > maxBatchFiles || limit < 1 || limit > 32768 {
		return nil, errors.New("invalid candidate source batch bounds")
	}
	if err := expected.ValidateBinding(binding); err != nil {
		return nil, err
	}
	id, err := expected.ID()
	if err != nil {
		return nil, err
	}
	before, states, err := Capture(ctx, binding)
	if err != nil {
		return nil, err
	}
	if before != expected {
		return nil, errors.New("candidate changed before batch read")
	}
	stateByPath := make(map[string]FileState, len(states))
	for _, state := range states {
		stateByPath[state.Path] = state
	}
	seen := make(map[string]bool, len(paths))
	selected := make([]FileState, len(paths))
	for i, path := range paths {
		if err := safepath.Writable(path); err != nil {
			return nil, err
		}
		if seen[path] {
			return nil, errors.New("duplicate candidate source batch path")
		}
		seen[path] = true
		state, ok := stateByPath[path]
		if !ok {
			return nil, errors.New("path absent from candidate")
		}
		selected[i] = state
	}
	root, err := os.OpenRoot(binding.Request.Path)
	if err != nil {
		return nil, err
	}
	rootOpen := true
	defer func() {
		if rootOpen {
			_ = root.Close()
		}
	}()
	results := make([]SourceFile, len(paths))
	for i, state := range selected {
		if err := ctx.Err(); err != nil {
			rootOpen = false
			return nil, errors.Join(err, root.Close())
		}
		result := SourceFile{CandidateID: id, Path: state.Path}
		sink := &pageSink{offset: 0, limit: limit}
		hash, size, executable, exists, readErr := safepath.CopyRegular(root, state.Path, 64<<20, sink)
		if readErr != nil {
			result.Err = readErr
		} else if !exists || hash != state.Hash || executable != state.Executable {
			result.Err = errors.New("candidate file observation mismatch")
		} else {
			result.SHA256 = hash
			result.Size = size
			result.Content = sink.bytes
			if next := int64(len(sink.bytes)); next < size {
				result.NextOffset = &next
			}
		}
		results[i] = result
	}
	closeErr := root.Close()
	rootOpen = false
	if closeErr != nil {
		return nil, closeErr
	}
	after, err := Fingerprint(ctx, binding)
	if err != nil {
		return nil, err
	}
	if after != expected {
		return nil, errors.New("candidate changed during batch read")
	}
	return results, nil
}
