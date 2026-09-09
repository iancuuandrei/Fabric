package worktree

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
)

// LeaseIdentity is the immutable workspace request protected by a live lease.
// It contains no ownership token or process-local handle.
type LeaseIdentity struct {
	Version int     `json:"version"`
	Request Request `json:"request"`
}

// ID hashes the exact workspace request protected by the lease.
func (i LeaseIdentity) ID() (string, error) {
	if i.Version != 1 || i.Request.Validate() != nil {
		return "", errors.New("invalid writer lease identity")
	}
	return canonical.Hash("harness.worktree-lease-identity.v1", i)
}

// WithOwnership validates that l still owns the exact expected request and
// holds that ownership against concurrent Close for the duration of visit.
// It does not prevent another process from corrupting or replacing the lock;
// callers must treat later filesystem errors as ownership uncertainty.
func (l *Lease) WithOwnership(expected Request, visit func(LeaseIdentity) error) error {
	if l == nil || visit == nil || expected.Validate() != nil {
		return errors.New("invalid writer lease ownership guard")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil || l.request != expected || filepath.Clean(l.path) != filepath.Clean(LeasePath(expected)) {
		return errors.New("writer lease does not own expected workspace")
	}
	if l.guard == nil || filepath.Clean(l.guardPath) != filepath.Clean(LeaseGuardPath(expected)) || verifyLeaseGuard(l.guard, l.guardPath, expected) != nil {
		return errors.New("writer lease exclusion guard unavailable")
	}
	original, err := l.file.Stat()
	if err != nil {
		return errors.New("writer lease ownership unavailable")
	}
	current, err := os.Lstat(l.path)
	if err != nil {
		return errors.New("writer lease ownership unavailable")
	}
	reader, err := os.Open(l.path)
	if err != nil {
		return errors.New("writer lease ownership unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, 65))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !os.SameFile(original, current) || string(raw) != l.token {
		return errors.New("writer lease ownership changed")
	}
	identity := LeaseIdentity{Version: 1, Request: expected}
	if _, err := identity.ID(); err != nil {
		return err
	}
	return visit(identity)
}
