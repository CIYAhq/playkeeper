//go:build linux

package backup

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A running server can swap a listed file for a symlink out of the data
// directory or a FIFO before it is read. Neither may be archived, and the
// FIFO must not hang the backup.
func TestWriteFileRefusesSwappedSymlinkAndFIFO(t *testing.T) {
	d := fixtureDataDir(t)
	outside := t.TempDir()
	write(t, outside, "secret.txt", "host secret")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(d, "world", "escape.dat")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(d, "world", "pipe.dat"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, rel := range []string{"world/escape.dat", "world/pipe.dat"} {
		done := make(chan error, 1)
		go func() {
			_, err := writeFile(tar.NewWriter(io.Discard), root, rel, &fileTally{lim: DefaultLimits()})
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s was archived", rel)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("archiving %s hung", rel)
		}
	}

	arch, m := createArchive(t, d)
	for _, f := range m.Files {
		if f.Path == "world/escape.dat" || f.Path == "world/pipe.dat" {
			t.Errorf("Create listed %s", f.Path)
		}
	}
	if _, err := Verify(bytes.NewReader(arch), DefaultLimits()); err != nil {
		t.Fatal(err)
	}
}
