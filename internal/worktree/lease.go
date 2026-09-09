package worktree

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"

	"harness.local/engorch/internal/safepath"
)

// Lease is an exclusive process-owned writer lock. It must span admission,
// execution and receipt observation; Close removes only its original writer
// token and releases, but does not delete, the stable reader/writer guard.
type Lease struct {
	mu        sync.Mutex
	file      *os.File
	guard     *os.File
	guardPath string
	path      string
	token     string
	request   Request
}

// Acquire refuses an existing lock, including a crashed owner's lock. It never
// infers that a lock is stale from age, PID reuse or a missing receipt.
func Acquire(r Request) (*Lease, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	root, directory := leaseRoot(r)
	if err := safepath.EnsureDirectory(root, directory); err != nil {
		return nil, err
	}
	guard, guardPath, err := acquireLeaseGuard(r, false)
	if err != nil {
		return nil, err
	}
	guardAccepted := false
	defer func() {
		if !guardAccepted {
			guard.Close()
		}
	}()
	p := LeasePath(r)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockLeaseFile(f); err != nil {
		f.Close()
		return nil, err
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		f.Close()
		return nil, err
	}
	token := hex.EncodeToString(raw)
	if _, err = f.WriteString(token); err == nil {
		err = f.Sync()
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	guardAccepted = true
	return &Lease{file: f, guard: guard, guardPath: guardPath, path: p, token: token, request: r}, nil
}

// Close is idempotent. Substitution leaves the replacement untouched and returns
// an error; ownership uncertainty is never resolved by deleting another lock.
func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	guard := l.guard
	l.file = nil
	l.guard = nil
	original, err := f.Stat()
	if err != nil {
		return errors.Join(err, f.Close(), guard.Close())
	}
	current, err := os.Lstat(l.path)
	if err != nil {
		return errors.Join(err, f.Close(), guard.Close())
	}
	reader, err := os.Open(l.path)
	if err != nil {
		return errors.Join(err, f.Close(), guard.Close())
	}
	b, readErr := io.ReadAll(io.LimitReader(reader, 65))
	if err := errors.Join(readErr, reader.Close()); err != nil {
		return errors.Join(err, f.Close(), guard.Close())
	}
	if !os.SameFile(original, current) || string(b) != l.token {
		return errors.Join(errors.New("writer lease ownership changed"), f.Close(), guard.Close())
	}
	if err = verifyLeaseGuard(guard, l.guardPath, l.request); err != nil {
		return errors.Join(err, f.Close(), guard.Close())
	}
	if err = f.Close(); err != nil {
		guard.Close()
		return err
	}
	return errors.Join(os.Remove(l.path), guard.Close())
}
