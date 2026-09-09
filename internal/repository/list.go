package repository

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// SourceEntry identifies a committed tree leaf, including unsupported file kinds.
type SourceEntry struct {
	Path   string `json:"path"`
	Object string `json:"object"`
	Mode   string `json:"mode"`
	Kind   string `json:"kind"`
}

// SourcePage is a lexicographically ordered page bound to one immutable commit.
type SourcePage struct {
	RepositoryID string        `json:"repository_id"`
	Commit       string        `json:"commit"`
	Entries      []SourceEntry `json:"entries"`
	NextAfter    *string       `json:"next_after"`
}

// ListSource streams committed leaves and retains at most limit+1 entries.
// It fails explicitly above 200,000 leaves or for non-UTF-8/oversized paths.
func ListSource(ctx context.Context, i Identity, after string, limit int) (SourcePage, error) {
	var result SourcePage
	id, err := i.ID()
	if err != nil {
		return result, err
	}
	if limit < 1 || limit > 128 || len(after) > 4096 || !utf8.ValidString(after) || strings.ContainsRune(after, 0) {
		return result, errors.New("invalid source listing bounds")
	}
	sink := &treeSink{after: after, limit: limit + 1, oidLength: len(i.Commit), entries: []SourceEntry{}}
	if err := streamTree(ctx, i, sink); err != nil {
		return result, err
	}
	result = SourcePage{RepositoryID: id, Commit: i.Commit, Entries: sink.entries}
	if len(result.Entries) > limit {
		result.Entries = result.Entries[:limit]
		cursor := result.Entries[limit-1].Path
		result.NextAfter = &cursor
	}
	return result, nil
}

// VisitSource enumerates an exact committed tree once in Git traversal order.
// It retains one bounded record and admits at most 500,000 leaves. A callback
// failure or truncated stream invalidates the whole observation; callbacks must
// not publish partial results as a complete repository scope.
func VisitSource(ctx context.Context, i Identity, visit func(SourceEntry) error) error {
	if _, err := i.ID(); err != nil {
		return err
	}
	if visit == nil {
		return errors.New("source visitor required")
	}
	return streamTree(ctx, i, &treeSink{oidLength: len(i.Commit), maxCount: 500000, visit: visit})
}

func streamTree(ctx context.Context, i Identity, sink *treeSink) error {
	tree, err := git(ctx, i.Root, "rev-parse", "--verify", i.Commit+"^{tree}")
	if err != nil {
		return err
	}
	if tree != i.Tree {
		return errors.New("recorded source tree mismatch")
	}
	duration := sink.timeout
	if duration == 0 {
		duration = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-replace-objects", "-C", i.Root, "ls-tree", "-rz", i.Tree)
	cmd.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	var stderr bounded
	cmd.Stdout, cmd.Stderr = sink, &stderr
	if err := cmd.Run(); err != nil {
		return errors.New("committed tree listing failed: " + err.Error())
	}
	if len(sink.pending) != 0 || stderr.overflow {
		return errors.New("incomplete tree listing or diagnostic overflow")
	}
	return nil
}

type treeSink struct {
	timeout                 time.Duration
	maxCount                int
	visit                   func(SourceEntry) error
	after                   string
	limit, oidLength, count int
	pending                 []byte
	entries                 []SourceEntry
}

// Write decodes bounded NUL-delimited records across arbitrary stream chunks.
func (s *treeSink) Write(data []byte) (int, error) {
	for n, b := range data {
		if b != 0 {
			if len(s.pending) >= 4200 {
				return n, errors.New("tree entry exceeds bound")
			}
			s.pending = append(s.pending, b)
			continue
		}
		if err := s.accept(string(s.pending)); err != nil {
			return n + 1, err
		}
		s.pending = s.pending[:0]
	}
	return len(data), nil
}

func (s *treeSink) accept(record string) error {
	s.count++
	maximum := s.maxCount
	if maximum == 0 {
		maximum = 200000
	}
	if s.count > maximum {
		return errors.New("tree leaf count exceeds bound")
	}
	meta, path, ok := strings.Cut(record, "\t")
	f := strings.Fields(meta)
	if !ok || len(f) != 3 || path == "" || len(path) > 4096 || !utf8.ValidString(path) || len(f[2]) != s.oidLength || strings.Trim(f[2], "0123456789abcdef") != "" {
		return errors.New("invalid committed tree entry")
	}
	kind := "unsupported"
	switch {
	case f[1] == "blob" && (f[0] == "100644" || f[0] == "100755"):
		kind = "file"
	case f[1] == "blob" && f[0] == "120000":
		kind = "symlink"
	case f[1] == "commit" && f[0] == "160000":
		kind = "submodule"
	}
	if s.visit != nil {
		return s.visit(SourceEntry{Path: path, Object: f[2], Mode: f[0], Kind: kind})
	}
	if path <= s.after {
		return nil
	}
	pos := sort.Search(len(s.entries), func(n int) bool { return s.entries[n].Path >= path })
	if pos < len(s.entries) && s.entries[pos].Path == path {
		return errors.New("duplicate committed path")
	}
	if pos >= s.limit {
		return nil
	}
	s.entries = append(s.entries, SourceEntry{})
	copy(s.entries[pos+1:], s.entries[pos:])
	s.entries[pos] = SourceEntry{Path: path, Object: f[2], Mode: f[0], Kind: kind}
	if len(s.entries) > s.limit {
		s.entries = s.entries[:s.limit]
	}
	return nil
}
