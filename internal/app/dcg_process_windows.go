//go:build windows

package app

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDCGProcess(command *exec.Cmd) {
	if command == nil {
		return
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
