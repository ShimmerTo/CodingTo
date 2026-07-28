//go:build !windows

package extensions

import "syscall"

func windowsHiddenProcessAttributes() *syscall.SysProcAttr {
	return nil
}
