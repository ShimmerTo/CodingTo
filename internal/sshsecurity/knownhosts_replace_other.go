//go:build !windows

package sshsecurity

import "os"

func replaceKnownHostsFile(src, dst string) error {
	return os.Rename(src, dst)
}
