//go:build !windows

package app

import "os"

func fileCreatedAt(info os.FileInfo) int64 {
	return 0
}
