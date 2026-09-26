//go:build linux

package agent

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/sleep"
)

// The stand-in shows the server's own icon, and never reads through a link
// or waits on a named pipe that a plugin put at server-icon.png.
func TestStandInIconDoesNotFollowPlantedFiles(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	at := filepath.Join(e.dataDir(), sleep.IconFile)
	outside := filepath.Join(t.TempDir(), "icon.png")
	if err := os.WriteFile(outside, icon.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(at)
	if err := os.Symlink(outside, at); err != nil {
		t.Fatal(err)
	}
	if got := e.srv().standInIcon(); got != "" {
		t.Fatalf("the stand-in read an icon through a link: %.40q", got)
	}

	if err := os.Remove(at); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(at, 0o640); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() { done <- e.srv().standInIcon() }()
	select {
	case got := <-done:
		if got != "" {
			t.Fatalf("the stand-in read an icon from a named pipe: %.40q", got)
		}
	case <-time.After(5 * time.Second):
		if f, err := os.OpenFile(at, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		t.Fatal("a named pipe at server-icon.png made the stand-in wait")
	}

	if err := os.Remove(at); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(at, icon.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.srv().standInIcon(); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("the server's own icon: %.40q", got)
	}
}
