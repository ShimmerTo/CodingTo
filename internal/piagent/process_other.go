//go:build !windows

package piagent

import "os/exec"

func configureBackgroundProcess(_ *exec.Cmd) {}
