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
		wantRefusal(t, e.status().Crash, c.at, "link")
		if err := os.Remove(at); err != nil {
			t.Fatal(err)
		}
		if op := e.act("start"); op.Status != api.OpSucceeded {
			t.Fatalf("start once the link is gone: %+v", op)
		}
		if b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "bStats", "config.yml")); err != nil || !bStatsOff(b) {
			t.Fatalf("bStats after the start: %q %v", b, err)
		}
		if st := e.status(); st.Crash != nil {
			t.Fatalf("the refusal is still shown once %s is online: %+v", c.at, st.Crash)
		}
	}

	if op := e.act("stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	config := filepath.Join(e.dataDir(), "plugins", "bStats", "config.yml")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(config, 0o640); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f, err := os.OpenFile(config, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
			os.Remove(config)
			f.Close()
		}
	})
	if op := e.act("start"); op.Status != api.OpFailed || !strings.Contains(op.Error, "is not a normal file (it is a named pipe)") {
		t.Fatalf("start with a named pipe at the bStats settings: %+v", op)
	}
	wantRefusal(t, e.status().Crash, "plugins/bStats/config.yml", "special_file")
}

// wantRefusal checks that a start Playkeeper refused is shown as the crash
// helper's refused_file kind, naming the file, with starting again as the fix.
func wantRefusal(t *testing.T, c *api.Crash, path, reason string) {
	t.Helper()
	if c == nil || c.Kind != "refused_file" || !c.Start || !c.Certain || c.Params["path"] != path || c.Params["reason"] != reason ||
		len(c.Fixes) != 1 || c.Fixes[0].Kind != "restart" || len(c.Lines) != 0 || !strings.Contains(c.Explanation, path+" in the server's files") {
		t.Fatalf("the refusal of %s as the crash helper shows it: %+v", path, c)
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

// Crash reports, Java's error report and the add-on folder belong to the
// game too. The crash helper reads the newest report of the run and only its
// first megabyte, never through a link, and a named pipe in place of a report
// or of a folder it lists does not make it wait: a pipe at crash-reports or
// plugins used to hold a failed start or the reconcile loop forever.
func TestCrashHelperReadsReportsWithoutFollowingLinksOrWaiting(t *testing.T) {
	e := crashEnv(t)
	host := e.hostFiles()
	data := e.dataDir()
	s := e.srv()
	since := time.Now().Add(-time.Minute)
	report := func() (string, string) { text, name := s.newestCrashReport(since); return name, text }
	link := func(to, at string) {
		t.Helper()
		if err := os.RemoveAll(at); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, at); err != nil {
			t.Fatal(err)
		}
	}
	pipe := func(at string) {
		t.Helper()
		if err := os.RemoveAll(at); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(at, 0o640); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if f, err := os.OpenFile(at, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
				os.Remove(at)
				f.Close()
			}
		})
	}

	jvm := "#\n# A fatal error has been detected by the Java Runtime Environment:\n#\n#  SIGSEGV (0xb) at pc=0x00007f3a2c4d5e6f, pid=87, tid=112\n"
	writeGameFile(t, filepath.Join(data, "hs_err_pid12.log"), "# an earlier run\n", since.Add(-48*time.Hour))
	writeGameFile(t, filepath.Join(data, "hs_err_pid87.log"), jvm, time.Time{})
	if name, text := report(); name != "hs_err_pid87.log" || text != jvm {
		t.Fatalf("Java's report of the run: %s %q", name, text)
	}
	if text, name := s.newestCrashReport(time.Time{}); name != "" || text != "" {
		t.Fatalf("a start that failed before the server ran has no report, got %s", name)
	}
	reports := filepath.Join(data, "crash-reports")
	mc := "---- Minecraft Crash Report ----\nDescription: Exception in server tick loop\n"
	writeGameFile(t, filepath.Join(reports, "crash-2026-09-25_21.40.12-server.txt"), mc+strings.Repeat("x", 2*crashReportLimit), time.Time{})
	if name, text := report(); name != "crash-2026-09-25_21.40.12-server.txt" || !strings.HasPrefix(text, mc) || len(text) != crashReportLimit {
		t.Fatalf("Minecraft's report comes first, cut at %d bytes: %s, %d bytes", crashReportLimit, name, len(text))
	}

	writeGameFile(t, filepath.Join(host, "reports", "crash-2026-09-25_21.41.00-server.txt"), "Description: PanelSecret\n", time.Time{})
	writeGameFile(t, filepath.Join(data, "world", "hs_err_pid88.log"), jvm, time.Time{})
	link(filepath.Join(host, "reports"), reports)
	link(filepath.Join(host, "panel.db"), filepath.Join(data, "hs_err_pid87.log"))
	link("world/hs_err_pid88.log", filepath.Join(data, "hs_err_pid88.log"))
	if name, text := report(); name != "" || text != "" {
		t.Fatalf("a report was read through a link: %s %q", name, text)
	}

	pipe(reports)
	pipe(filepath.Join(data, "hs_err_pid89.log"))
	pipe(filepath.Join(data, "plugins"))
	done := make(chan string, 1)
	go func() {
		name, _ := report()
		done <- fmt.Sprintf("report %q, %d add-ons", name, len(s.addons(api.ServerConfig{})))
	}()
	select {
	case got := <-done:
		if got != `report "", 0 add-ons` {
			t.Fatalf("with named pipes: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the crash helper waited on a named pipe")
	}
	e.fd.crash(1)
	if c := e.waitCrash(); c.Kind != "unknown" {
		t.Fatalf("a crash with named pipes in the server's files: %s %s", c.Kind, c.Explanation)
	}
}

// A plugin can truncate the server's jar to a terabyte of holes. The start
// does not spend hours hashing it: it is too large to be Paper, so it is
// downloaded and checked again.
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
	if op := e.act("start"); op.Status != api.OpSucceeded {
		t.Fatalf("start with a huge sparse jar: %+v", op)
	}
	if fi, err := os.Stat(jars[0]); err != nil || fi.Size() != int64(len(e.fd.jarContent)) {
		t.Fatalf("the jar was not downloaded again: %v %v", fi, err)
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
