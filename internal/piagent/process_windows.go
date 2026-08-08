//go:build windows

package piagent

import (
	"os"
	"os/exec"
	"strconv"
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

// killProcessTree terminates the child and every descendant. On Windows the
// direct child of the bridge is the pi.cmd cmd.exe shim with the real node
// process underneath (and possibly further descendants), so a plain
// Process.Kill would leave the wedged node orphaned and burning CPU.
// taskkill /T /F walks the whole tree rooted at the child's PID.
func killProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid))
	// taskkill is a console executable; without a hidden window it flashes a
	// cmd window on every shutdown/session stop. Reuse the same background
	// process attributes the Pi adapter itself is launched with.
	configureBackgroundProcess(kill)
	_ = kill.Run()
	_ = p.Kill() // best-effort fallback in case taskkill is unavailable
}
