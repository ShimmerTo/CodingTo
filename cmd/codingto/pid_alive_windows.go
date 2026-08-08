//go:build windows

package main

import "golang.org/x/sys/windows"

// pidAlive reports whether a process with the given PID is still running.
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return true
}
