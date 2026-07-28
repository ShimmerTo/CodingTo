//go:build windows

package piagent

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureBackgroundProcess prevents console shims such as pi.cmd and
// npm.cmd from creating a visible console window when CodingTo starts them
// from its GUI process. HideWindow covers ordinary Windows executables while
// CREATE_NO_WINDOW prevents cmd.exe from allocating a console in the first
// place.
func configureBackgroundProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
