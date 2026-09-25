//go:build unix

package backup

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The game can put a link or a named pipe at server.properties. Neither is
// read, so a backup keeps the default level name instead of one from a file
// outside the server's files, and does not wait on the pipe.
func TestLevelNameDoesNotFollowALinkOrWaitOnAPipe(t *testing.T) {
	d := fixtureDataDir(t)
	props := filepath.Join(d, "server.properties")
	outside := filepath.Join(t.TempDir(), "server.properties")
	if err := os.WriteFile(outside, []byte("level-name=leaked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what  string
		plant func() error
	}{
		{"a link to a file outside", func() error { return os.Symlink(outside, props) }},
		{"a named pipe", func() error { return syscall.Mkfifo(props, 0o640) }},
	} {
		if err := os.Remove(props); err != nil {
			t.Fatal(err)
		}
		if err := c.plant(); err != nil {
			t.Fatal(err)
		}
		var name string
		var m Manifest
		var err error
		done := make(chan struct{})
		go func() {
			defer close(done)
			name = LevelName(d)
			m, err = Create(io.Discard, d, Manifest{}, DefaultLimits())
		}()
		if waited(t, props, done) {
			t.Errorf("%s at server.properties: the backup waited on it", c.what)
		}
		if name != "world" || err != nil || m.LevelName != "world" {
			t.Errorf("%s at server.properties: level %q, backup of %q: %v", c.what, name, m.LevelName, err)
		}
	}
}

// waited waits for done. After three seconds a reader is taken to be stuck
// opening the named pipe at fifo, which is then opened for writing until
// done, for at most five more seconds.
func waited(t *testing.T, fifo string, done <-chan struct{}) bool {
	t.Helper()
	select {
	case <-done:
		return false
	case <-time.After(3 * time.Second):
	}
	for give := time.Now().Add(5 * time.Second); time.Now().Before(give); {
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		select {
		case <-done:
			return true
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("still waiting on %s", fifo)
	return true
}
