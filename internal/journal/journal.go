package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
)

const maxJournal = 64 << 20

// ErrLockUnavailable identifies failure to acquire the exclusive journal lock.
var ErrLockUnavailable = errors.New("journal lock unavailable")

// Event is one integrity-bound record. Payload semantics belong to the controller.
type Event struct {
	Version  int             `json:"version"`
	Sequence int             `json:"sequence"`
	Previous string          `json:"previous"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
	Hash     string          `json:"hash"`
}

type unsigned struct {
	Version  int             `json:"version"`
	Sequence int             `json:"sequence"`
	Previous string          `json:"previous"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

func digest(e Event) (string, error) {
	return canonical.Hash("harness.event.v1", unsigned{e.Version, e.Sequence, e.Previous, e.Kind, e.Payload})
}

func isSimpleCanonicalASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			return false
		}
	}
	return true
}

// fastDigestAndLineLen computes the exact canonical event hash and serialized
// line length directly from an already-validated canonical e.Payload, avoiding
// redundant full-payload JSON parse/sort/encode passes on the hot replay path.
func fastDigestAndLineLen(e Event) (string, int, error) {
	if !isSimpleCanonicalASCII(e.Kind) || !isSimpleCanonicalASCII(e.Previous) || (e.Hash != "" && !isSimpleCanonicalASCII(e.Hash)) {
		h, err := digest(e)
		if err != nil {
			return "", 0, err
		}
		line, err := canonical.Bytes(e)
		if err != nil {
			return "", 0, err
		}
		return h, len(line), nil
	}
	seq := strconv.Itoa(e.Sequence)
	// Exact canonical key order for unsigned: kind, payload, previous, sequence, version.
	unsignedLen := len(`{"kind":"`) + len(e.Kind) + len(`","payload":`) + len(e.Payload) +
		len(`,"previous":"`) + len(e.Previous) + len(`","sequence":`) + len(seq) + len(`,"version":1}`)
	if unsignedLen > canonical.MaxBytes {
		return "", 0, errors.New("event exceeds size bound")
	}
	h := sha256.New()
	h.Write([]byte("harness.event.v1\n"))
	h.Write([]byte(`{"kind":"`))
	h.Write([]byte(e.Kind))
	h.Write([]byte(`","payload":`))
	h.Write(e.Payload)
	h.Write([]byte(`,"previous":"`))
	h.Write([]byte(e.Previous))
	h.Write([]byte(`","sequence":`))
	h.Write([]byte(seq))
	h.Write([]byte(`,"version":1}`))
	sum := hex.EncodeToString(h.Sum(nil))
	// Exact canonical Event adds `"hash":"<64hex>",` right after `{`.
	lineLen := unsignedLen + len(`"hash":"",`) + len(e.Hash)
	return sum, lineLen, nil
}

// Replay validates all records or returns an error without returning a partial
// success. Preconditions: bounded input. Postcondition: contiguous canonical chain.
func Replay(data []byte) ([]Event, error) {
	if len(data) > maxJournal {
		return nil, errors.New("journal exceeds size bound")
	}
	if len(data) == 0 {
		return []Event{}, nil
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("torn journal tail")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	result := make([]Event, 0, len(lines))
	previous := strings.Repeat("0", 64)
	for i, line := range lines {
		if len(line)+1 > canonical.MaxBytes {
			return nil, errors.New("event exceeds size bound")
		}
		normal, err := canonical.Normalize(line)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(normal, line) {
			return nil, errors.New("noncanonical event")
		}
		var e Event
		if err = canonical.Decode(line, &e); err != nil {
			return nil, err
		}
		if e.Version != 1 || e.Sequence != i+1 || e.Previous != previous || e.Kind == "" || len(e.Payload) == 0 || e.Payload[0] != '{' {
			return nil, errors.New("invalid event envelope")
		}
		h, _, err := fastDigestAndLineLen(e)
		if err != nil || h != e.Hash {
			return nil, errors.New("event hash mismatch")
		}
		previous = e.Hash
		result = append(result, e)
	}
	return result, nil
}

func lock(path string) (func() error, error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("journal lock: %w", errors.Join(ErrLockUnavailable, err))
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return func() error { return os.Remove(path + ".lock") }, nil
}

func read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxJournal+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxJournal {
		return nil, errors.New("journal exceeds size bound")
	}
	return b, nil
}

// Read acquires the same exclusive lock as writers to avoid reading an append
// in flight. A missing journal is empty; a stale lock is an explicit error.
func readJSONL(path string) (events []Event, err error) {
	release, err := lock(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, release()) }()
	b, err := read(path)
	if err != nil {
		return nil, err
	}
	return Replay(b)
}

// Append validates the proposed complete history under exclusive lock, then
// writes and syncs one event. Validate MUST NOT perform effects or reenter this
// journal. Any error after writing starts requires inspection before retry.
func appendJSONL(path, kind string, payload any, validate func([]Event) error) (event Event, err error) {
	if validate == nil {
		return event, errors.New("semantic validator required")
	}
	release, err := lock(path)
	if err != nil {
		return event, err
	}
	defer func() { err = errors.Join(err, release()) }()
	b, err := read(path)
	if err != nil {
		return event, err
	}
	events, err := Replay(b)
	if err != nil {
		return event, err
	}
	p, err := canonical.Bytes(payload)
	if err != nil {
		return event, err
	}
	if len(p) == 0 || p[0] != '{' || kind == "" {
		return event, errors.New("event requires kind and object payload")
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
	if err = validate(append(events, event)); err != nil {
		return event, err
	}
	line, err := canonical.Bytes(event)
	if err != nil {
		return event, err
	}
	line = append(line, '\n')
	if len(line) > canonical.MaxBytes || len(b)+len(line) > maxJournal {
		return event, errors.New("journal size bound exceeded")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return event, err
	}
	n, writeErr := f.Write(line)
	if writeErr == nil && n != len(line) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	return event, errors.Join(writeErr, closeErr)
}
