//go:build linux

package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Between listing and copying, a running server can swap a file for a symlink
// out of the data directory or a FIFO. Neither may be staged, and the FIFO
// must not hang the copy while saving is paused.
func TestStagingRefusesSwappedSymlinkAndFIFO(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "secret.txt", "host secret")
	for name, swap := range map[string]func(path string) error{
		"symlink out of the data directory": func(p string) error { return os.Symlink(filepath.Join(outside, "secret.txt"), p) },
		"fifo":                              func(p string) error { return syscall.Mkfifo(p, 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			d, staged := fixtureDataDir(t), t.TempDir()
			s := &stager{retryDelay: time.Millisecond, inject: func(step string) error {
				if step != "open world/level.dat" {
					return nil
				}
				p := filepath.Join(d, "world", "level.dat")
				if err := os.Remove(p); err != nil {
					return err
				}
				return swap(p)
			}}
			done := make(chan error, 1)
			go func() { done <- s.copyAll(context.Background(), d, staged) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("the swapped file was staged")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("staging hung")
			}
			b, _ := os.ReadFile(filepath.Join(staged, "world", "level.dat"))
			if strings.Contains(string(b), "host secret") {
				t.Fatal("a file outside the data directory was staged")
			}
		})
	}
}
