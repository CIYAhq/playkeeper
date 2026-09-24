//go:build !linux

package agent

import (
	"errors"
	"net"
)

// Playkeeper hosts are Linux; other platforms can build and unit test the code
// but the agent refuses every peer because credentials cannot be checked.
func peerUID(net.Conn) (uint32, error) {
	return 0, errors.New("peer credentials are only supported on Linux")
}

func umask(m int) int { return m }

func statfs(string) (free, total int64, err error) {
	return 0, 0, errors.New("disk usage is only supported on Linux")
}
