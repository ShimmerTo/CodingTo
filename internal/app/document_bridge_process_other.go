//go:build !windows

package app

import "os/exec"

func configureDocumentBridgeProcess(_ *exec.Cmd) {}
