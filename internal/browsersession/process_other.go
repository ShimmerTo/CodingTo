//go:build !windows

package browsersession

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureChromeProcess(cmd *exec.Cmd)  { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func configureAdapterProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func replaceFile(source, target string) error { return os.Rename(source, target) }
