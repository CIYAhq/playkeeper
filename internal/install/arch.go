package install

import (
	"io"
	"os"
)

// archNames are the CPUs Playkeeper releases are built for, by GOARCH, as the
// installer names them.
var archNames = map[string]string{"amd64": "x86_64 (amd64)", "arm64": "64-bit ARM (arm64)"}

// userland32 reports whether this system's programs are 32-bit on a 64-bit
// CPU, as on a Raspberry Pi with a 32-bit OS: its Docker would pull 32-bit
// images, which the Java 21 and 25 runtimes don't have. /bin/sh tells: the
// fifth byte of an ELF file is 1 for 32-bit.
func userland32(sys System) bool {
	f, err := os.Open(sys.P("/bin/sh"))
	if err != nil {
		return false
	}
	defer f.Close()
	var ident [5]byte
	if _, err := io.ReadFull(f, ident[:]); err != nil {
		return false
	}
	return string(ident[:4]) == "\x7fELF" && ident[4] == 1
}
