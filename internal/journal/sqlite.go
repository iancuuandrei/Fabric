package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	_ "modernc.org/sqlite"
)

const sqliteBusyTimeoutMS = 10_000

// ErrCommitUncertain means Commit returned an error after the event had been
// inserted in the transaction. The caller must read and reconcile the journal
// before retrying; this error does not prove whether the event became durable.
var ErrCommitUncertain = errors.New("SQLite commit outcome uncertain")

// SQLiteStore persists the journal v1 envelope in SQLite. It is an opt-in
// migration target; the package-level JSONL Read and Append functions remain
// unchanged.
type SQLiteStore struct {
	db       *sql.DB
	commitTx func(*sql.Tx) error
	readOnly bool
}

// OpenSQLite opens or creates a journal database. SQLite supplies transaction,
// locking and recovery behavior; this package continues to own event identity
// and semantic validation.
func OpenSQLite(path string) (*SQLiteStore, error) {
	return openSQLite(path, false)
}

// OpenSQLiteReadOnly opens an existing journal without creating it or changing
// its schema or connection-level persistent settings.
func OpenSQLiteReadOnly(path string) (*SQLiteStore, error) {
	return openSQLite(path, true)
}

func openSQLite(path string, readOnly bool) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("SQLite journal path required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	slashPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		slashPath = "/" + slashPath
	}
	u := &url.URL{Scheme: "file", Path: slashPath}
	q := u.Query()
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	q.Set("_defensive", "1")
	if readOnly {
		q.Set("mode", "ro")
		q.Set("_txlock", "deferred")
	} else {
		q.Add("_pragma", "foreign_keys(1)")
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(FULL)")
		q.Set("_txlock", "immediate")
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &SQLiteStore{db: db, commitTx: func(tx *sql.Tx) error { return tx.Commit() }, readOnly: readOnly}
	if readOnly {
		err = s.verifySchema()
	} else {
		err = s.initialize()
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) verifySchema() error {
	var version string
	if err := s.db.QueryRow(`SELECT value FROM journal_metadata WHERE key = 'schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("read SQLite journal schema: %w", err)
	}
	if version != "1" {
		return fmt.Errorf("unsupported SQLite journal schema %q", version)
	}
	return nil
}

func (s *SQLiteStore) initialize() error {
	const schema = `
CREATE TABLE IF NOT EXISTS journal_metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
INSERT INTO journal_metadata(key, value) VALUES ('schema_version', '1')
    ON CONFLICT(key) DO NOTHING;
CREATE TABLE IF NOT EXISTS events (
    sequence INTEGER PRIMARY KEY CHECK (sequence >= 1),
    version INTEGER NOT NULL CHECK (version = 1),
    previous TEXT NOT NULL CHECK (length(previous) = 64),
    kind TEXT NOT NULL CHECK (kind <> ''),
    payload BLOB NOT NULL CHECK (typeof(payload) = 'blob' AND length(payload) > 0),
    hash TEXT NOT NULL UNIQUE CHECK (length(hash) = 64)
) STRICT;
CREATE TRIGGER IF NOT EXISTS events_validate_append
BEFORE INSERT ON events
BEGIN
    SELECT CASE
        WHEN NEW.sequence <> COALESCE((SELECT MAX(sequence) + 1 FROM events), 1)
        THEN RAISE(ABORT, 'noncontiguous event sequence')
    END;
    SELECT CASE
        WHEN NEW.previous <> COALESCE(
            (SELECT hash FROM events ORDER BY sequence DESC LIMIT 1),
            '0000000000000000000000000000000000000000000000000000000000000000')
        THEN RAISE(ABORT, 'event previous hash mismatch')
    END;
END;
CREATE TRIGGER IF NOT EXISTS events_no_update
BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'journal events are append-only');
END;
CREATE TRIGGER IF NOT EXISTS events_no_delete
BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'journal events are append-only');
END;`
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(schema); err != nil {
		return fmt.Errorf("initialize SQLite journal: %w", err)
	}
	var version string
	if err = tx.QueryRow(`SELECT value FROM journal_metadata WHERE key = 'schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("read SQLite journal schema: %w", err)
	}
	if version != "1" {
		return fmt.Errorf("unsupported SQLite journal schema %q", version)
	}
	return tx.Commit()
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSQLiteEvents(ctx context.Context, q rowQueryer) ([]Event, int, error) {
	rows, err := q.QueryContext(ctx, `SELECT version, sequence, previous, kind, payload, hash FROM events ORDER BY sequence`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	events := make([]Event, 0)
	total := 0
	previous := strings.Repeat("0", 64)
	for rows.Next() {
		var e Event
		if err = rows.Scan(&e.Version, &e.Sequence, &e.Previous, &e.Kind, &e.Payload, &e.Hash); err != nil {
			return nil, 0, err
		}
		if e.Version != 1 || e.Sequence != len(events)+1 || e.Previous != previous || e.Kind == "" || len(e.Payload) == 0 || e.Payload[0] != '{' {
			return nil, 0, errors.New("invalid SQLite event envelope")
		}
		normal, normalizeErr := canonical.Normalize(e.Payload)
		if normalizeErr != nil || !bytes.Equal(normal, e.Payload) {
			return nil, 0, errors.New("noncanonical SQLite event payload")
		}
		h, hashErr := digest(e)
		if hashErr != nil || h != e.Hash {
			return nil, 0, errors.New("SQLite event hash mismatch")
		}
		line, encodeErr := canonical.Bytes(e)
		if encodeErr != nil || len(line)+1 > canonical.MaxBytes {
			return nil, 0, errors.New("SQLite event exceeds size bound")
		}
		total += len(line) + 1
		if total > maxJournal {
			return nil, 0, errors.New("SQLite journal exceeds size bound")
		}
		previous = e.Hash
		events = append(events, e)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// Read validates the complete stored chain and never returns a partial history.
func (s *SQLiteStore) Read() ([]Event, error) {
	return s.ReadContext(context.Background())
}

// ReadContext is Read with caller-controlled cancellation and deadlines.
func (s *SQLiteStore) ReadContext(ctx context.Context) ([]Event, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("SQLite journal is closed")
	}
	if ctx == nil {
		return nil, errors.New("SQLite journal context required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	events, _, err := readSQLiteEvents(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return events, nil
}

func cloneEvents(events []Event) []Event {
	cloned := make([]Event, len(events))
	for i, e := range events {
		cloned[i] = e
		cloned[i].Payload = bytes.Clone(e.Payload)
	}
	return cloned
}

// Append validates the complete proposed history while holding an immediate
// SQLite transaction, inserts one event, and commits it atomically. A commit
// error is deliberately classified as uncertain; callers must Read before any
// retry because the driver cannot prove which side of the commit boundary won.
func (s *SQLiteStore) Append(kind string, payload any, validate func([]Event) error) (event Event, err error) {
	return s.AppendContext(context.Background(), kind, payload, validate)
}

// AppendContext is Append with caller-controlled cancellation and deadlines.
func (s *SQLiteStore) AppendContext(ctx context.Context, kind string, payload any, validate func([]Event) error) (event Event, err error) {
	if s == nil || s.db == nil {
		return event, errors.New("SQLite journal is closed")
	}
	if ctx == nil {
		return event, errors.New("SQLite journal context required")
	}
	if s.readOnly {
		return event, errors.New("SQLite journal is read-only")
	}
	if validate == nil {
		return event, errors.New("semantic validator required")
	}
	p, err := canonical.Bytes(payload)
	if err != nil {
		return event, err
	}
	if len(p) == 0 || p[0] != '{' || kind == "" {
		return event, errors.New("event requires kind and object payload")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return event, err
	}
	defer tx.Rollback()
	events, total, err := readSQLiteEvents(ctx, tx)
	if err != nil {
		return event, err
	}
	previous := strings.Repeat("0", 64)
	if len(events) > 0 {
		previous = events[len(events)-1].Hash
	}
	event = Event{Version: 1, Sequence: len(events) + 1, Previous: previous, Kind: kind, Payload: p}
	event.Hash, err = digest(event)
	if err != nil {
		return event, err
	}
	proposed := cloneEvents(append(events, event))
	if err = validate(proposed); err != nil {
		return event, err
	}
	line, err := canonical.Bytes(event)
	if err != nil {
		return event, err
	}
	if len(line)+1 > canonical.MaxBytes || total+len(line)+1 > maxJournal {
		return event, errors.New("journal size bound exceeded")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(version, sequence, previous, kind, payload, hash) VALUES (?, ?, ?, ?, ?, ?)`,
		event.Version, event.Sequence, event.Previous, event.Kind, []byte(event.Payload), event.Hash)
	if err != nil {
		return event, err
	}
	if err = s.commitTx(tx); err != nil {
		return event, fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return event, nil
}

// ExportJSONL returns the exact canonical JSONL representation of the validated
// database. It does not change the database.
func (s *SQLiteStore) ExportJSONL() ([]byte, error) {
	return s.ExportJSONLContext(context.Background())
}

// ExportJSONLContext is ExportJSONL with caller-controlled cancellation and deadlines.
func (s *SQLiteStore) ExportJSONLContext(ctx context.Context) ([]byte, error) {
	events, err := s.ReadContext(ctx)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, e := range events {
		line, encodeErr := canonical.Bytes(e)
		if encodeErr != nil {
			return nil, encodeErr
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// ImportJSONL validates the complete source before starting a write transaction.
// It only imports into an empty database and never replaces existing events.
func (s *SQLiteStore) ImportJSONL(data []byte) (err error) {
	return s.ImportJSONLContext(context.Background(), data)
}

// ImportJSONLContext is ImportJSONL with caller-controlled cancellation and deadlines.
func (s *SQLiteStore) ImportJSONLContext(ctx context.Context, data []byte) (err error) {
	if s == nil || s.db == nil {
		return errors.New("SQLite journal is closed")
	}
	if ctx == nil {
		return errors.New("SQLite journal context required")
	}
	if s.readOnly {
		return errors.New("SQLite journal is read-only")
	}
	events, err := Replay(data)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("SQLite journal import target is not empty")
	}
	for _, e := range events {
		if _, err = tx.ExecContext(ctx, `INSERT INTO events(version, sequence, previous, kind, payload, hash) VALUES (?, ?, ?, ?, ?, ?)`,
			e.Version, e.Sequence, e.Previous, e.Kind, []byte(e.Payload), e.Hash); err != nil {
			return err
		}
	}
	if err = s.commitTx(tx); err != nil {
		return fmt.Errorf("%w: %w", ErrCommitUncertain, err)
	}
	return nil
}

// ImportJSONLFrom reads a bounded JSONL stream and delegates to ImportJSONL.
func (s *SQLiteStore) ImportJSONLFrom(r io.Reader) error {
	if r == nil {
		return errors.New("JSONL import reader required")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxJournal+1))
	if err != nil {
		return err
	}
	if len(data) > maxJournal {
		return errors.New("journal exceeds size bound")
	}
	return s.ImportJSONL(data)
}
