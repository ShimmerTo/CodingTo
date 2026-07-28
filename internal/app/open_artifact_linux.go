//go:build linux

package app

import "os/exec"

func openLocalPath(path string) error {
	return exec.Command("xdg-open", path).Start()
}
