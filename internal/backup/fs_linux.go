//go:build linux

package backup

import (
	"os"
	"syscall"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY
