//go:build linux

package backup

import (
	"os"
	"syscall"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY

// deviceOf returns the filesystem a file is on.
func deviceOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}
