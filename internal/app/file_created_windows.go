//go:build windows

package app

import (
	"os"
	"syscall"
)

func fileCreatedAt(info os.FileInfo) int64 {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return 0
	}
	return attributes.CreationTime.Nanoseconds() / int64(1e6)
}
