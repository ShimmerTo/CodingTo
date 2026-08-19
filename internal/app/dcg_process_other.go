//go:build !windows

package app

import "os/exec"

func configureDCGProcess(_ *exec.Cmd) {}
