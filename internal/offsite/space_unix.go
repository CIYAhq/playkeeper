//go:build linux || darwin || freebsd

package offsite

import "syscall"

// freeSpace is how many bytes a process without root can still write in
// the file system that holds dir.
func freeSpace(dir string) (int64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, err
	}
	return int64(s.Bavail) * int64(s.Bsize), nil
}
