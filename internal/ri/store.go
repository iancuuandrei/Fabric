package ri

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/safepath"
)

// Store publishes opaque RI artifacts without replacing an existing name.
// Rust validation must precede publication and follow every artifact read.
type Store struct{ Directory string }

// SnapshotID identifies exact snapshot bytes with the Rust snapshot hash domain.
func SnapshotID(data []byte) string {
	h := sha256.New()
	h.Write([]byte("harness.ri.snapshot.v1\n"))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func (s Store) open(id string) (*os.Root, error) {
	if err := safepath.RequireDigest(id); err != nil {
		return nil, err
	}
	if err := safepath.Directory(s.Directory); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.Directory)
}

func stored(root *os.Root, name, id string) ([]byte, error) {
	if err := safepath.Check(root, name, false); err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	meta, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !meta.Mode().IsRegular() || meta.Size() > 64<<20 {
		return nil, errors.New("invalid snapshot artifact type or size")
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 || SnapshotID(data) != id {
		return nil, errors.New("snapshot artifact content mismatch")
	}
	return data, nil
}

// Read verifies stored bytes against their expected content identity. A pending
// publication is not admitted; readers cannot mistake partial work for completion.
func (s Store) Read(id string) ([]byte, error) {
	root, err := s.open(id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Lstat(id + ".pending"); !os.IsNotExist(err) {
		return nil, errors.New("snapshot publication pending; reconcile explicitly")
	}
	return stored(root, id+".jsonl", id)
}

// Publish synchronizes staging bytes then links them to a new content-addressed
// name. Existing content is verified and reused; it is never overwritten.
// Errors after staging leave evidence for explicit Reconcile; no retry is hidden.
// Caller must journal publication intent before invoking this effect.
func (s Store) Publish(id string, data []byte) (string, error) {
	if len(data) == 0 || len(data) > 64<<20 || SnapshotID(data) != id {
		return "", errors.New("invalid snapshot bytes or identity")
	}
	root, err := s.open(id)
	if err != nil {
		return "", err
	}
	defer root.Close()
	pending, final := id+".pending", id+".jsonl"
	if _, err := root.Lstat(pending); !os.IsNotExist(err) {
		return "", errors.New("snapshot publication pending; reconcile explicitly")
	}
	if _, err := root.Lstat(final); err == nil {
		if _, err := stored(root, final, id); err != nil {
			return "", err
		}
		return filepath.Join(s.Directory, final), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	file, err := root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return "", err
	}
	if err := root.Link(pending, final); err != nil {
		return "", err
	}
	if err := root.Remove(pending); err != nil {
		return "", err
	}
	if _, err := stored(root, final, id); err != nil {
		return "", err
	}
	return filepath.Join(s.Directory, final), nil
}

// Reconcile completes a publication only from exact validated staging bytes.
// When both names exist they must identify the same file. Partial staging or
// independently created destinations remain untouched and return an error.
// Caller must explicitly authorize and journal this recovery action.
func (s Store) Reconcile(id string) (string, error) {
	root, err := s.open(id)
	if err != nil {
		return "", err
	}
	defer root.Close()
	pending, final := id+".pending", id+".jsonl"
	staged, err := root.Lstat(pending)
	if os.IsNotExist(err) {
		if _, err := stored(root, final, id); err != nil {
			return "", err
		}
		return filepath.Join(s.Directory, final), nil
	}
	if err != nil {
		return "", err
	}
	if _, err := stored(root, pending, id); err != nil {
		return "", err
	}
	existing, err := root.Lstat(final)
	if os.IsNotExist(err) {
		if err := root.Link(pending, final); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if !os.SameFile(staged, existing) {
		return "", errors.New("publication destination is an independent file")
	}
	if _, err := stored(root, final, id); err != nil {
		return "", err
	}
	if err := root.Remove(pending); err != nil {
		return "", err
	}
	return filepath.Join(s.Directory, final), nil
}
