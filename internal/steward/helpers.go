package steward

import (
	"time"
)

// unixMillisNow returns the current Unix time in milliseconds.
func unixMillisNow() int64 { return time.Now().UnixMilli() }

// timeAfter is a small indirection over time.After so tests can override the
// package-level timeout if needed.
func timeAfter(d time.Duration) <-chan time.Time { return time.After(d) }
