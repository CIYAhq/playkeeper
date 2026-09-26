//go:build unix

package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// The game can put a link or a named pipe at server.properties. Neither is
// read: the level name falls back to "world" instead of one from a file
// outside the server's files, and nothing waits on the pipe. A backup, and
// the check before one, refuse and name the file rather than back up
// "world", which may be another world, without its settings.
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
		kind  gamefiles.Kind
	}{
		{"a link to a file outside", func() error { return os.Symlink(outside, props) }, gamefiles.KindLink},
		{"a named pipe", func() error { return syscall.Mkfifo(props, 0o640) }, gamefiles.KindSpecial},
	} {
		if err := os.Remove(props); err != nil {
			t.Fatal(err)
		}
		if err := c.plant(); err != nil {
			t.Fatal(err)
		}
		var name string
		var createErr, checkErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			name = LevelName(d)
			_, createErr = Create(io.Discard, d, Manifest{}, DefaultLimits())
			checkErr = Check(d, DefaultLimits())
		}()
		if waited(t, props, done) {
			t.Errorf("%s at server.properties: the backup waited on it", c.what)
		}
		if name != "world" {
			t.Errorf("%s at server.properties: level %q, want the default", c.what, name)
		}
		for what, err := range map[string]error{"the backup": createErr, "the check before it": checkErr} {
			if gamefiles.KindOf(err) != c.kind || !strings.Contains(fmt.Sprint(err), "server.properties") {
				t.Errorf("%s at server.properties: %s must refuse it and name it: %v", c.what, what, err)
			}
		}
	}
}

// A server that hasn't written server.properties yet still backs up "world".
func TestABackupWithoutServerPropertiesIsOfWorld(t *testing.T) {
	d := fixtureDataDir(t)
	if err := os.Remove(filepath.Join(d, "server.properties")); err != nil {
		t.Fatal(err)
	}
	m, err := Create(io.Discard, d, Manifest{}, DefaultLimits())
	if err != nil || m.LevelName != "world" {
		t.Fatalf("backup without server.properties: level %q: %v", m.LevelName, err)
	}
	if err := Check(d, DefaultLimits()); err != nil {
		t.Fatalf("the check before a backup without server.properties: %v", err)
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
