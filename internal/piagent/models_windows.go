//go:build windows

package piagent

import (
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

// replaceFile replaces dst with src on Windows using ReplaceFileW, which can
// replace a file that is currently open by another process (the replaced file
// stays valid for existing handles while new opens see the replacement). This
// avoids the ERROR_SHARING_VIOLATION that os.Rename hits when the destination is
// in use, which manifests as "The process cannot access the file because it is
// being used by another process".
func replaceFile(src, dst string) error {
	replaced, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	replacement, err := windows.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	r, _, err := windows.NewLazySystemDLL("kernel32.dll").
		NewProc("ReplaceFileW").
		Call(uintptr(unsafe.Pointer(replaced)), uintptr(unsafe.Pointer(replacement)), 0, 0, 0, 0)
	if r == 0 {
		return err
	}
	return nil
}

// tryReplace attempts the Windows-aware replace first, then falls back to a plain
// rename if ReplaceFileW is unavailable or fails for another reason.
func tryReplace(src, dst string) error {
	if err := replaceFile(src, dst); err == nil {
		return nil
	}
	return os.Rename(src, dst)
}
