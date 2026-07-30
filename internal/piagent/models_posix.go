//go:build !windows

package piagent

import "os"

// tryReplace on non-Windows platforms is a plain atomic rename.
func tryReplace(src, dst string) error {
	return os.Rename(src, dst)
}
