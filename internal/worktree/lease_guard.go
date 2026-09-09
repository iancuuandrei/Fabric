package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"harness.local/engorch/internal/safepath"
)

// ReadLease is a shared process-owned worktree guard. It carries no writer
// token and no worktree mutation API accepts it as write authority.
type ReadLease struct {
	mu      sync.Mutex
	guard   *os.File
	path    string
	request Request
}

// ErrLeaseContention identifies a live kernel guard conflict, not a stale
// durable writer token, invalid request, or substituted guard.
var ErrLeaseContention = errors.New("worktree lease guard is occupied")

func leaseRoot(r Request) (string, string) {
	if r.ControllerStateRoot != "" {
		return r.ControllerStateRoot, "leases"
	}
	return r.Source.Root, ".harness/leases"
}

func leaseName(r Request) string {
	_, directory := leaseRoot(r)
	return directory + "/" + r.RunID + ".lock"
}

func leaseGuardName(r Request) string {
	_, directory := leaseRoot(r)
	return directory + "/" + r.RunID + ".guard"
}

// LeasePath returns the immutable durable writer-token location. Callers must
// validate the request before using the returned path for filesystem access.
func LeasePath(r Request) string {
	root, _ := leaseRoot(r)
	return filepath.Join(root, filepath.FromSlash(leaseName(r)))
}

// LeaseGuardPath returns the stable reader/writer kernel-guard location. Callers
// must validate the request before using the returned path for filesystem access.
func LeaseGuardPath(r Request) string {
	root, _ := leaseRoot(r)
	return filepath.Join(root, filepath.FromSlash(leaseGuardName(r)))
}

// AcquireRead obtains a shared cross-process guard for an exact request. A live
// or stale durable writer token rejects the reader after shared exclusion is held.
func AcquireRead(r Request) (*ReadLease, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	rootPath, directory := leaseRoot(r)
	if err := safepath.EnsureDirectory(rootPath, directory); err != nil {
		return nil, err
	}
	guard, guardPath, err := acquireLeaseGuard(r, true)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			guard.Close()
		}
	}()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	_, _, _, exists, tokenErr := safepath.ReadRegular(root, leaseName(r), 64)
	if err := errors.Join(tokenErr, root.Close()); err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("writer lease token blocks read lease")
	}
	accepted = true
	return &ReadLease{guard: guard, path: guardPath, request: r}, nil
}

func acquireLeaseGuard(r Request, shared bool) (*os.File, string, error) {
	rootPath, _ := leaseRoot(r)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	name := leaseGuardName(r)
	if err := safepath.Check(root, name, true); err != nil {
		return nil, "", err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, "", err
	}
	if shared {
		err = lockLeaseFileShared(f)
	} else {
		err = lockLeaseFile(f)
	}
	if err != nil {
		f.Close()
		if leaseLockContended(err) {
			return nil, "", errors.Join(ErrLeaseContention, err)
		}
		return nil, "", err
	}
	path := LeaseGuardPath(r)
	if err := verifyLeaseGuard(f, path, r); err != nil {
		f.Close()
		return nil, "", err
	}
	return f, path, nil
}

func verifyLeaseGuard(file *os.File, path string, request Request) error {
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || opened.Size() != 0 || !os.SameFile(opened, current) {
		return errors.New("worktree lease guard substituted")
	}
	rootPath, _ := leaseRoot(request)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	_, size, _, exists, readErr := safepath.ReadRegular(root, leaseGuardName(request), 0)
	if err := errors.Join(readErr, root.Close()); err != nil {
		return err
	}
	if !exists || size != 0 {
		return errors.New("worktree lease guard is not a stable empty file")
	}
	return nil
}

// WithOwnership validates the live shared guard and exact expected request for
// the duration of visit. LeaseIdentity is evidence of exclusion, not permission
// to mutate the worktree.
func (l *ReadLease) WithOwnership(expected Request, visit func(LeaseIdentity) error) error {
	if l == nil || visit == nil || expected.Validate() != nil {
		return errors.New("invalid read lease ownership guard")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.guard == nil || l.request != expected || filepath.Clean(l.path) != filepath.Clean(LeaseGuardPath(expected)) {
		return errors.New("read lease does not own expected workspace guard")
	}
	if err := verifyLeaseGuard(l.guard, l.path, expected); err != nil {
		return errors.New("read lease ownership unavailable")
	}
	identity := LeaseIdentity{Version: 1, Request: expected}
	if _, err := identity.ID(); err != nil {
		return err
	}
	return visit(identity)
}

// Close releases the shared kernel guard. It is idempotent and never removes
// the stable guard file or any durable writer token.
func (l *ReadLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.guard == nil {
		return nil
	}
	guard := l.guard
	l.guard = nil
	verifyErr := verifyLeaseGuard(guard, l.path, l.request)
	return errors.Join(verifyErr, guard.Close())
}
