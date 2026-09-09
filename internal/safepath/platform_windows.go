package safepath

import (
	"errors"
	"os"
	"syscall"
)

func linked(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return ok && data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func singleLink(f *os.File) error {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("hardlink or reparse file rejected")
	}
	return nil
}
