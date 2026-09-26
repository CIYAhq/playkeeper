//go:build unix

package backup

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

// Check runs before the server stops, so the game can swap a link or a named
// pipe in at server.properties after the files are listed. Its size is then
// refused, not taken from a file outside the server's files or waited on.
func TestArchivedSizeDoesNotFollowALinkOrWaitOnAPipe(t *testing.T) {
	d := fixtureDataDir(t)
	props := filepath.Join(d, "server.properties")
	outside := filepath.Join(t.TempDir(), "server.properties")
	if err := os.WriteFile(outside, []byte("motd=outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what  string
		plant func() error
		want  gamefiles.Kind
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
		var size int64
		var err error
		done := make(chan struct{})
		go func() {
			defer close(done)
			size, err = archivedSize(d, "server.properties")
		}()
		if waited(t, props, done) {
			t.Errorf("%s at server.properties: Check waited on it", c.what)
		}
		if k := gamefiles.KindOf(err); k != c.want {
			t.Errorf("%s at server.properties: size %d, error %v (kind %q); want kind %q", c.what, size, err, k, c.want)
		}
	}
}
