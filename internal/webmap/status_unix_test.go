//go:build unix

package webmap

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestStatusDoesNotWaitOnNamedPipes(t *testing.T) {
	_, m := startFakeSquaremap(t)
	m.Dir = t.TempDir()
	sq := filepath.Join(m.Dir, "plugins", "squaremap")
	writeTile(t, sq, "minecraft_overworld", 3, 0, 0, testNow)
	tiles := filepath.Join(sq, "web", "tiles")
	mkfifo(t, filepath.Join(sq, "data", "minecraft_overworld", "resume_render.json"))
	mkfifo(t, filepath.Join(tiles, "minecraft_overworld", "3", "1_1.png"))
	mkfifo(t, filepath.Join(tiles, "minecraft_the_nether"))
	mkfifo(t, filepath.Join(tiles, "minecraft_the_end", "3"))
	done := make(chan Status, 1)
	go func() { done <- m.Status(context.Background(), running) }()
	select {
	case s := <-done:
		if s.Areas != 1 || s.Progress != nil || s.State != StateReady {
			t.Errorf("got %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Status waited on a named pipe")
	}
}

func TestOpenNoFollowDoesNotWaitForAPipesWriter(t *testing.T) {
	dir := t.TempDir()
	mkfifo(t, filepath.Join(dir, "pipe"))
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan fs.FileMode, 1)
	go func() {
		f, fi, err := openNoFollow(r, "pipe")
		if err != nil {
			t.Error(err)
			done <- 0
			return
		}
		f.Close()
		done <- fi.Mode()
	}()
	select {
	case mode := <-done:
		if mode&fs.ModeNamedPipe == 0 {
			t.Errorf("mode %v", mode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a named pipe waited for a writer")
	}
}

func mkfifo(t *testing.T, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(name, 0o640); err != nil {
		t.Fatal(err)
	}
}
