//go:build !linux

package backup

import (
	"os"
	"syscall"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK

// deviceOf is not implemented off Linux; callers then assume one filesystem.
func deviceOf(os.FileInfo) (uint64, bool) { return 0, false }
