//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package worktree

import (
	"errors"
	"os"
	"syscall"
)

func leaseLockContended(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func lockLeaseFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func lockLeaseFileShared(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
}
