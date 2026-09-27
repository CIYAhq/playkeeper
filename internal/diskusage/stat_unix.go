//go:build unix

package diskusage

import (
	"io/fs"
	"syscall"
)

func statOf(fi fs.FileInfo) fileStat {
	st := fileStat{bytes: fi.Size()}
	if s, ok := fi.Sys().(*syscall.Stat_t); ok {
		st.bytes = int64(s.Blocks) * 512
		st.key = fileKey{dev: uint64(s.Dev), ino: uint64(s.Ino)}
		st.linked = !fi.IsDir() && s.Nlink > 1
		st.hasDev = true
	}
	return st
}
