//go:build linux || darwin || freebsd

package safepath

import (
	"errors"
	"os"
	"syscall"
)

func linked(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
func singleLink(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || s.Nlink != 1 {
		return errors.New("hardlink file rejected")
	}
	return nil
}
