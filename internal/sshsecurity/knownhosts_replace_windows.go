//go:build windows

package sshsecurity

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func replaceKnownHostsFile(src, dst string) error {
	destination, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	result, _, replaceErr := windows.NewLazySystemDLL("kernel32.dll").
		NewProc("ReplaceFileW").
		Call(uintptr(unsafe.Pointer(destination)), uintptr(unsafe.Pointer(replacement)), 0, 0, 0, 0)
	if result != 0 {
		return nil
	}
	if renameErr := os.Rename(src, dst); renameErr == nil {
		return nil
	} else if replaceErr == nil {
		return renameErr
	}
	return replaceErr
}
