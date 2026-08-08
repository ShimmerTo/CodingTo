//go:build !windows

package main

import "syscall"

// pidAlive reports whether a process with the given PID is still running.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
