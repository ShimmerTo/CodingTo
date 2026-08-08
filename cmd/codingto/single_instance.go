package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// singleInstanceLockPath returns the lock file used to keep the desktop client
// to a single running instance. Multiple instances each start the steward
// connector for the same IM channels; the long connections kick each other
// and overwrite the shared channel status, so only one instance may run.
func singleInstanceLockPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codingto", "codingto.lock"), nil
}

// acquireSingleInstance claims the single-instance lock file. It returns a
// release function that removes the lock on shutdown. A stale lock whose PID
// is no longer alive is reclaimed. An error means another live instance owns
// the lock.
func acquireSingleInstance() (func(), error) {
	lockPath, err := singleInstanceLockPath()
	if err != nil {
		return nil, err
	}
	pid := os.Getpid()
	open := func() (*os.File, error) {
		return os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	}
	f, err := open()
	if err != nil {
		data, readErr := os.ReadFile(lockPath)
		ownerPID := -1
		if readErr == nil {
			ownerPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		}
		if ownerPID > 0 && pidAlive(ownerPID) {
			return nil, fmt.Errorf("已有 CodingTo 实例在运行（pid=%d）", ownerPID)
		}
		// Stale lock from a dead instance: reclaim it.
		_ = os.Remove(lockPath)
		f, err = open()
		if err != nil {
			return nil, fmt.Errorf("single instance lock: %w", err)
		}
	}
	if _, err := fmt.Fprintf(f, "%d\n", pid); err != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return nil, err
	}
	return func() {
		_ = f.Close()
		_ = os.Remove(lockPath)
	}, nil
}
