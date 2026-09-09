//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package worktree

import (
	"errors"
	"os"
)

func leaseLockContended(_ error) bool { return false }

func lockLeaseFile(_ *os.File) error {
	return errors.New("workspace lease OS exclusion unavailable on this platform")
}

func lockLeaseFileShared(_ *os.File) error {
	return errors.New("workspace lease OS exclusion unavailable on this platform")
}
