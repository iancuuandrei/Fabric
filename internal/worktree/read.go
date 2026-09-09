package worktree

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"unicode/utf8"

	"harness.local/engorch/internal/safepath"
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
