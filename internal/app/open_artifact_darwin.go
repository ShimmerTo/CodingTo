//go:build darwin

package app

import "os/exec"

func openLocalPath(path string) error {
	return exec.Command("open", path).Start()
}
