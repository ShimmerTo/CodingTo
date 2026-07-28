//go:build windows

package browsersession

import (
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func configureChromeProcess(cmd *exec.Cmd)  { cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: false} }
func configureAdapterProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &windows.SysProcAttr{HideWindow: true} }

func processAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == 259 // STILL_ACTIVE
}

func replaceFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(sourcePtr, targetPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return os.Chmod(target, 0o600)
}
