//go:build linux

package agent

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// act asks for a start or a stop and waits for its operation.
func (e *agentEnv) act(verb string) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
	if code != 202 {
		e.t.Fatalf("%s: %d %v", verb, code, out)
	}
	return e.waitOp(out["id"].(string))
}

// hostFiles makes stand-ins for Playkeeper's own files next to the servers'
// and returns their folder. panel.db is a player list and link.key sets a
// difficulty, so a read that followed a link to them would show it.
func (e *agentEnv) hostFiles() string {
	e.t.Helper()
	dir := filepath.Join(e.cfg.DataDir, "panel")
	for name, content := range map[string]string{
		"panel.db":    `[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"PanelSecret","level":4,"bypassesPlayerLimit":false}]`,
		"link.key":    "difficulty=hard\n",
		"tls/key.pem": "private key\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	return dir
}

// tree describes every file and folder under root by what a write or a
// chown would change: contents, mode, owner and change time. A chown moves
// the change time even when the owner stays the same.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			return err
		}
		b, _ := os.ReadFile(p)
		out[p] = fmt.Sprintf("mode %o owner %d:%d changed %d.%09d %q", st.Mode, st.Uid, st.Gid, st.Ctim.Sec, st.Ctim.Nsec, b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A plugin or mod can plant a link where the agent writes Paper's bStats
// setting before every start. The start stops with a message naming the
// link, nothing the link leads to is written or chowned, and once the link
// is gone the server starts.
func TestPlantedLinksCannotRedirectTheBStatsWrite(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	host := e.hostFiles()
	for _, c := range []struct{ at, to string }{
		{"plugins", host},
		{"plugins/bStats", filepath.Join(host, "tls")},
		{"plugins/bStats/config.yml", filepath.Join(host, "panel.db")},
	} {
		if op := e.act("stop"); op.Status != api.OpSucceeded {
			t.Fatalf("stop: %+v", op)
		}
		at := filepath.Join(e.dataDir(), filepath.FromSlash(c.at))
		if err := os.RemoveAll(filepath.Join(e.dataDir(), "plugins")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(c.to, at); err != nil {
			t.Fatal(err)
		}
		before := tree(t, host)
		time.Sleep(20 * time.Millisecond)
		op := e.act("start")
		if op.Status != api.OpFailed || !strings.Contains(op.Error, c.at+" in the server's files is a link") || !strings.Contains(op.Hint, "plugin or mod") {
			t.Fatalf("start with a link at %s: %+v", c.at, op)
		}
		if after := tree(t, host); !maps.Equal(after, before) {
			t.Fatalf("the link at %s changed what it leads to:\n%v\nwas\n%v", c.at, after, before)
		}
		if err := os.Remove(at); err != nil {
			t.Fatal(err)
		}
		if op := e.act("start"); op.Status != api.OpSucceeded {
			t.Fatalf("start once the link is gone: %+v", op)
		}
		if b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "bStats", "config.yml")); err != nil || !bStatsOff(b) {
			t.Fatalf("bStats after the start: %q %v", b, err)
		}
	}
}

// The agent reads the allowlist, the operators, server.properties and the
// server icon from the server's files. A link planted at any of them is not
// followed, so the dashboard never shows what it leads to, and a named pipe
// at server.properties does not make a status wait.
func TestGameFilesAreReadWithoutFollowingLinks(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	host := e.hostFiles()
	before := tree(t, host)
	plant := func(name, to string) {
		t.Helper()
		p := filepath.Join(e.dataDir(), name)
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, p); err != nil {
			t.Fatal(err)
		}
	}

	for route, name := range map[string]string{"/whitelist": "whitelist.json", "/operators": "ops.json"} {
		plant(name, filepath.Join(host, "panel.db"))
		code, out := e.call("GET", e.sp(route), nil)
		if msg, _ := out["error"].(string); code != 409 || !strings.Contains(msg, name+" in the server's files is a link") {
			t.Errorf("%s with a link at %s: %d %v", route, name, code, out)
		}
	}

	props := filepath.Join(e.dataDir(), "server.properties")
	if err := os.WriteFile(props, []byte("difficulty=peaceful\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if g := e.status().Gameplay; g.Difficulty != "peaceful" {
		t.Fatalf("server.properties was not read: %+v", g)
	}
	plant("server.properties", filepath.Join(host, "link.key"))
	if g := e.status().Gameplay; g.Difficulty != "easy" {
		t.Errorf("the status followed a link at server.properties: %+v", g)
	}

	if err := os.Remove(props); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(props, 0o640); err != nil {
		t.Fatal(err)
	}
	s := e.srv()
	done := make(chan api.ServerStatus, 1)
	go func() { done <- s.Status(context.Background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		if f, err := os.OpenFile(props, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		t.Fatal("a named pipe at server.properties made the status wait")
	}

	plant("server-icon.png", filepath.Join(host, "panel.db"))
	if code, _ := e.call("GET", e.sp("/icon"), nil); code != 404 {
		t.Errorf("the icon with a link at server-icon.png: %d", code)
	}
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	code, out := e.uploadTo(e.sp("/icon"), icon.Bytes())
	if msg, _ := out["error"].(string); code != 409 || !strings.Contains(msg, "server-icon.png in the server's files is a link") {
		t.Errorf("an icon upload over a link: %d %v", code, out)
	}

	if after := tree(t, host); !maps.Equal(after, before) {
		t.Fatalf("Playkeeper's files changed:\n%v\nwas\n%v", after, before)
	}
}

// A plugin can truncate the server's jar to a terabyte of holes. The start
// doesn't spend hours hashing it: it's too large to be the software
// Playkeeper installed, so the start stops at once and says the software
// changed, like any other change to it, and Reinstall puts it back.
func TestAHugeSparseJarDoesNotHoldUpTheStart(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if op := e.act("stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	jars, _ := filepath.Glob(filepath.Join(e.dataDir(), "paper-*.jar"))
	if len(jars) != 1 {
		t.Fatalf("jars: %v", jars)
	}
	if err := os.Truncate(jars[0], 1<<40); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if op := e.act("start"); op.Status != api.OpFailed || !strings.Contains(op.Error, "doesn't match what Playkeeper installed") {
		t.Fatalf("start with a huge sparse jar: %+v", op)
	}
	if d := time.Since(began); d > 30*time.Second {
		t.Fatalf("the start spent %s on a huge sparse jar", d)
	}
	if fi, err := os.Stat(jars[0]); err != nil || fi.Size() != 1<<40 {
		t.Fatalf("the changed jar was replaced on its own: %v %v", fi, err)
	}
	if op := e.runOp("POST", "/software/reinstall"); op.Status != api.OpSucceeded {
		t.Fatalf("reinstall: %+v", op)
	}
	if fi, err := os.Stat(jars[0]); err != nil || fi.Size() != int64(len(e.fd.jarContent)) {
		t.Fatalf("reinstall did not put the jar back: %v %v", fi, err)
	}
}

// A restored world is given to the game's user file by file. A link in it
// is given as it is, so what it leads to keeps its mode.
func TestRestoredWorldsAreGivenToTheGameWithoutFollowingLinks(t *testing.T) {
	host, world := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{
		filepath.Join(host, "panel.db"):            "secret",
		filepath.Join(world, "world", "level.dat"): "level",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(host, "panel.db"), filepath.Join(world, "world", "level.dat_old")); err != nil {
		t.Fatal(err)
	}
	before := tree(t, host)
	time.Sleep(20 * time.Millisecond)
	if err := giveTree(world, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if after := tree(t, host); !maps.Equal(after, before) {
		t.Fatalf("giving the world to the game changed what a link in it leads to:\n%v\nwas\n%v", after, before)
	}
	for rel, want := range map[string]fs.FileMode{"world": 0o750, "world/level.dat": 0o640} {
		fi, err := os.Lstat(filepath.Join(world, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s has mode %o, want %o", rel, fi.Mode().Perm(), want)
		}
	}
}
