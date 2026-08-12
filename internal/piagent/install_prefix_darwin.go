//go:build darwin

package piagent

import (
	"os"
	"path/filepath"
)

// managedNpmPrefix keeps Pi Agent writable by the current user. The official
// Node.pkg uses /usr/local as npm's global prefix, which would otherwise make a
// GUI-triggered `npm install -g` fail unless CodingTo itself ran as root.
func managedNpmPrefix() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codingto", "runtime", "npm")
}
