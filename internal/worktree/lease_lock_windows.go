package worktree

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

func leaseLockContended(err error) bool {
	return errors.Is(err, syscall.Errno(33)) // ERROR_LOCK_VIOLATION
}

var leaseLockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

// Lock at 4 GiB, beyond bounded token reads (which can request past EOF), so
// separate read-only observations remain legal even with buffered reads.
// The kernel releases this exclusion when the owning handle/process closes.
func lockLeaseFile(file *os.File) error {
	return lockLeaseFileMode(file, true)
}

func lockLeaseFileShared(file *os.File) error {
	return lockLeaseFileMode(file, false)
}

func lockLeaseFileMode(file *os.File, exclusive bool) error {
	overlapped := syscall.Overlapped{OffsetHigh: 1}
	flags := uintptr(1) // LOCKFILE_FAIL_IMMEDIATELY
	if exclusive {
		flags |= 2 // LOCKFILE_EXCLUSIVE_LOCK
	}
	result, _, err := leaseLockFileEx.Call(file.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}
