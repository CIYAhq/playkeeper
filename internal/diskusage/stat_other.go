//go:build !unix

package diskusage

import "io/fs"

func statOf(fi fs.FileInfo) fileStat {
	return fileStat{bytes: fi.Size()}
}
