//go:build !linux

package agent

import (
	"errors"
	"net"
	"os"
	"syscall"
)

const gameReadFlags = os.O_RDONLY | syscall.O_NONBLOCK

func fileInode(os.FileInfo) (uint64, bool) { return 0, false }

// Playkeeper hosts are Linux; other platforms can build and unit test the code
// but the agent refuses every peer because credentials cannot be checked.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer credentials are only supported on Linux")
}

func statfs(string) (free, total int64, err error) {
	return 0, 0, errors.New("disk usage is only supported on Linux")
}
