//go:build !windows

package app

import "os/exec"

func configureGitProcess(_ *exec.Cmd) {}
