//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const windowsStillActiveExitCode uint32 = 259 // STILL_ACTIVE as defined by WinBase.h.

// pidAlive reports whether a CodingTo process with the given PID is still
// running. OpenProcess alone is not enough: Windows reuses PIDs, so a dead
// instance's number may belong to an unrelated process (dev helpers, terminal,
// ...). The lock is only considered owned when the process image name matches
// our own executable; anything else counts as a stale lock to reclaim.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	// A terminated Windows process object may remain queryable while another
	// handle still references it. QueryFullProcessImageName can still succeed in
	// that state, so checking only the image name turns a stale PID file into a
	// permanent single-instance block. Exit code 259 is Windows' STILL_ACTIVE.
	var exitCode uint32
	if err := windows.GetExitCodeProcess(h, &exitCode); err != nil || exitCode != windowsStillActiveExitCode {
		return false
	}
	expected, err := os.Executable()
	if err != nil {
		// Cannot determine our own name: err on the side of refusing a
		// second instance rather than risking a duplicate.
		return true
	}
	var buf [1024]uint16
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		// Access-denied or vanished process: do not treat the lock as stale.
		return true
	}
	actual := filepath.Base(windows.UTF16ToString(buf[:size]))
	return strings.EqualFold(strings.ToLower(filepath.Base(expected)), strings.ToLower(actual))
}
