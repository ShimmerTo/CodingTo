//go:build windows

package app

import (
	"os/exec"
	"syscall"
)

func configureDocumentBridgeProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
