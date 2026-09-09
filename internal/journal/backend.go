package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
)

var sqliteHeader = []byte("SQLite format 3\x00")

type backendKind uint8

const (
	backendMissing backendKind = iota
	backendJSONL
	backendSQLite
)

// detectBackend identifies SQLite only by its file header. Extensions never
// select a backend. A dangling symlink is treated as a legacy path so existing
// JSONL path-following behavior is preserved.
func detectBackend(path string) (backendKind, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if _, lockErr := os.Lstat(path + ".lock"); lockErr == nil {
			return backendJSONL, nil
		} else if !os.IsNotExist(lockErr) {
			return 0, lockErr
		}
		return backendMissing, nil
	}
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if _, err = os.Stat(path); os.IsNotExist(err) {
			return backendJSONL, nil
		} else if err != nil {
			return 0, err
		}
	} else if !info.Mode().IsRegular() {
		return 0, errors.New("journal path is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	header := make([]byte, len(sqliteHeader))
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	if n == len(sqliteHeader) && bytes.Equal(header, sqliteHeader) {
		return backendSQLite, nil
	}
	return backendJSONL, nil
}

// Read validates the journal selected by its on-disk content. A missing path
// is empty and remains missing. Existing JSONL journals retain their exclusive
// lock behavior; SQLite reads use a read-only database handle.
func Read(path string) ([]Event, error) {
	return ReadContext(context.Background(), path)
}

// ReadContext is Read with caller-controlled cancellation for SQLite queries
// and cancellation checks around bounded legacy JSONL replay.
func ReadContext(ctx context.Context, path string) ([]Event, error) {
	if ctx == nil {
		return nil, errors.New("journal context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind, err := detectBackend(path)
	if err != nil {
		return nil, err
	}
	switch kind {
	case backendMissing:
		return []Event{}, nil
	case backendJSONL:
		events, err := readJSONL(path)
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		return events, nil
	case backendSQLite:
		s, err := OpenSQLiteReadOnly(path)
		if err != nil {
			return nil, err
		}
		events, readErr := s.ReadContext(ctx)
		return events, errors.Join(readErr, s.Close())
	default:
		return nil, errors.New("unknown journal backend")
	}
}

// Append preserves an existing journal's backend. A first successful append to
// a missing path creates SQLite through an atomically published temporary file.
// A legacy lock beside a missing path remains an explicit recovery stop.
func Append(path, kind string, payload any, validate func([]Event) error) (Event, error) {
	for attempts := 0; attempts < 3; attempts++ {
		backend, err := detectBackend(path)
		if err != nil {
			return Event{}, err
		}
		switch backend {
		case backendJSONL:
			return appendJSONL(path, kind, payload, validate)
		case backendSQLite:
			s, openErr := OpenSQLite(path)
			if openErr != nil {
				return Event{}, openErr
			}
			event, appendErr := s.Append(kind, payload, validate)
			closeErr := s.Close()
			if appendErr != nil {
				return event, errors.Join(appendErr, closeErr)
			}
			if closeErr != nil {
				return event, fmt.Errorf("%w: close SQLite journal: %w", ErrCommitUncertain, closeErr)
			}
			return event, nil
		case backendMissing:
			event, published, createErr := createSQLiteJournal(path, kind, payload, validate)
			if createErr != nil {
				return Event{}, createErr
			}
			if published {
				return event, nil
			}
		default:
			return Event{}, errors.New("unknown journal backend")
		}
	}
	return Event{}, errors.New("journal backend changed repeatedly during append")
}

func createSQLiteJournal(path, kind string, payload any, validate func([]Event) error) (Event, bool, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	f, err := os.CreateTemp(dir, "."+base+".sqlite-init-*")
	if err != nil {
		return Event{}, false, err
	}
	temp := f.Name()
	if err = f.Close(); err != nil {
		_ = os.Remove(temp)
		return Event{}, false, err
	}
	defer func() {
		_ = os.Remove(temp)
		_ = os.Remove(temp + "-wal")
		_ = os.Remove(temp + "-shm")
	}()
	s, err := OpenSQLite(temp)
	if err != nil {
		return Event{}, false, err
	}
	event, appendErr := s.Append(kind, payload, validate)
	closeErr := s.Close()
	if err = errors.Join(appendErr, closeErr); err != nil {
		return Event{}, false, err
	}
	for _, sidecar := range []string{temp + "-wal", temp + "-shm"} {
		if _, statErr := os.Lstat(sidecar); statErr == nil {
			return Event{}, false, errors.New("closed SQLite journal retained a sidecar before publication")
		} else if !os.IsNotExist(statErr) {
			return Event{}, false, statErr
		}
	}
	if err = os.Link(temp, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Event{}, false, nil
		}
		return Event{}, false, fmt.Errorf("publish SQLite journal: %w", err)
	}
	return event, true, nil
}

// ExportJSONL validates either backend and returns canonical newline-terminated
// JSONL. It does not create, replace, rename, repair or truncate any path.
func ExportJSONL(path string) ([]byte, error) {
	return ExportJSONLContext(context.Background(), path)
}

// ExportJSONLContext is ExportJSONL with caller-controlled cancellation.
func ExportJSONLContext(ctx context.Context, path string) ([]byte, error) {
	events, err := ReadContext(ctx, path)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := canonical.Bytes(event)
		if err != nil {
			return nil, err
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
