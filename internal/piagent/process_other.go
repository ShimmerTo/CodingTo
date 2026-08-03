//go:build !windows

package piagent

import (
	"os"
	"os/exec"
)

func configureBackgroundProcess(_ *exec.Cmd) {}

// killProcessTree terminates the child. On Unix the pi launcher script uses
// `exec node` (sh replaces itself), so the direct child IS the node process
// and a plain Kill is sufficient; descendant tool processes are managed by pi
// itself and exit when it dies.
func killProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}
