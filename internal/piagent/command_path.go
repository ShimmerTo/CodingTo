package piagent

import (
	"os/exec"
)

// commandPath resolves runtime commands after applying platform-specific PATH
// additions. macOS GUI apps launched by Finder receive a minimal PATH and do
// not normally see /usr/local/bin, Homebrew, nvm, Volta, or similar locations.
func commandPath(name string) (string, error) {
	prepareCommandPath()
	return exec.LookPath(name)
}
