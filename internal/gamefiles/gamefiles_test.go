//go:build linux

package gamefiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// env is a data directory, a folder outside it that stands for Playkeeper's
// own files (panel.db, the panel's TLS key), and a folder inside it that a
// link could lead to.
type env struct {
	t       *testing.T
	data    string
	outside string
	d       *Dir
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, data: filepath.Join(base, "data"), outside: filepath.Join(base, "playkeeper")}
	writeFile(t, filepath.Join(e.outside, "panel", "panel.db"), []byte("accounts and sessions\n"))
	writeFile(t, filepath.Join(e.outside, "panel", "tls", "panel.key"), []byte("private key\n"))
	e.put("elsewhere/config.yml", []byte("the game's own file\n"))
	d, err := Open(e.data, &Owner{UID: os.Getuid(), GID: os.Getgid()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	e.d = d
	return e
}

func writeFile(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o640); err != nil {
		t.Fatal(err)
	}
}

func (e *env) path(rel string) string { return filepath.Join(e.data, filepath.FromSlash(rel)) }

func (e *env) put(rel string, b []byte) {
	e.t.Helper()
	writeFile(e.t, e.path(rel), b)
}

func (e *env) read(rel string) string {
	e.t.Helper()
	b, err := os.ReadFile(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(b)
}

// clear removes whatever is at rel and makes the folders above it.
func (e *env) clear(rel string) string {
	e.t.Helper()
	p := e.path(rel)
	if err := os.RemoveAll(p); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// plant puts a link to target at rel, as a plugin could.
func (e *env) plant(rel, target string) {
	e.t.Helper()
	if err := os.Symlink(target, e.clear(rel)); err != nil {
		e.t.Fatal(err)
	}
}

// node puts a named pipe (syscall.S_IFIFO) or a socket (syscall.S_IFSOCK) at
// rel.
func (e *env) node(rel string, mode uint32) {
	e.t.Helper()
	if err := syscall.Mknod(e.clear(rel), mode|0o640, 0); err != nil {
		e.t.Fatal(err)
	}
}

// state is what shows that a file or folder was touched. Every write and
// every chown moves the change time, even a chown to the owner it already
// has.
type state struct {
	mode, uid, gid uint32
	size           int64
	mtime, ctime   syscall.Timespec
	content        string
}

func snapshot(t *testing.T, root string) map[string]state {
	t.Helper()
	out := map[string]state{}
	err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			return err
		}
		s := state{mode: st.Mode, uid: st.Uid, gid: st.Gid, size: st.Size, mtime: st.Mtim, ctime: st.Ctim}
		if st.Mode&syscall.S_IFMT == syscall.S_IFREG {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s.content = string(b)
		}
		out[p] = s
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// watch records the folder outside the data directory and the folder inside
// it that links lead to, and returns a check that nothing in either changed.
func (e *env) watch() func() {
	e.t.Helper()
	roots := []string{e.outside, e.path("elsewhere")}
	before := make([]map[string]state, len(roots))
	for i, r := range roots {
		before[i] = snapshot(e.t, r)
	}
	// Change times can move in steps as coarse as a clock tick.
	time.Sleep(20 * time.Millisecond)
	return func() {
		e.t.Helper()
		for i, r := range roots {
			after := snapshot(e.t, r)
			for p, b := range before[i] {
				if a, ok := after[p]; !ok || a != b {
					e.t.Errorf("%s was touched: now %+v, was %+v", p, a, b)
				}
			}
			for p := range after {
				if _, ok := before[i][p]; !ok {
					e.t.Errorf("%s appeared", p)
				}
			}
		}
	}
}

// noBlock runs fn, which must not wait on the named pipe at fifo. If fn is
// still waiting after a few seconds, noBlock opens the pipe for writing so
// that fn can return, and fails the test.
func (e *env) noBlock(fifo string, fn func() error) error {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
	}
	e.t.Errorf("waited on the named pipe %s", fifo)
	giveUp := time.After(5 * time.Second)
	for {
		if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		select {
		case err := <-done:
			return err
		case <-giveUp:
			e.t.Fatal("still waiting after the pipe was opened for writing")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// refused fails the test unless err refuses p with kind.
func refused(t *testing.T, err error, kind Kind, p string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != kind || e.Params["path"] != p {
		t.Fatalf("got %v (kind %q), want %s refused as %s", err, KindOf(err), p, kind)
	}
}

type fileOp struct {
	name string
	run  func(d *Dir, name string) error
}

var readOps = []fileOp{
	{"ReadFile", func(d *Dir, name string) error { _, err := d.ReadFile(name, 1<<20); return err }},
	{"ReadTail", func(d *Dir, name string) error { _, err := d.ReadTail(name, 1<<10); return err }},
	{"ReadRange", func(d *Dir, name string) error { _, _, err := d.ReadRange(name, 0, 1<<10); return err }},
	{"ReadJSON", func(d *Dir, name string) error { var v any; return d.ReadJSON(name, 1<<20, &v) }},
	{"SHA256", func(d *Dir, name string) error { _, err := d.SHA256(context.Background(), name, 1<<20); return err }},
	{"EnsureFile", func(d *Dir, name string) error { return d.EnsureFile(name, []byte("enabled: false\n"), 0o640, nil) }},
	{"OpenFile", func(d *Dir, name string) error {
		f, _, err := d.OpenFile(name)
		if err == nil {
			f.Close()
		}
		return err
	}},
}

var ops = append(slices.Clone(readOps),
	fileOp{"WriteFile", func(d *Dir, name string) error { return d.WriteFile(name, []byte("enabled: false\n"), 0o640) }},
	fileOp{"Remove", func(d *Dir, name string) error { return d.Remove(name) }})

// A plugin or mod can put a link anywhere on the way to a file Playkeeper
// uses. Wherever it is and wherever it points, it is refused, and nothing it
// leads to is read, written or given to the game.
func TestLinksAreRefusedAtEveryStep(t *testing.T) {
	const name = "plugins/bStats/config.yml"
	panel := func(e *env) string { return filepath.Join(e.outside, "panel") }
	for _, c := range []struct {
		at, what string
		to       func(e *env) string
	}{
		{"plugins", "Playkeeper's folder", panel},
		{"plugins", "a folder inside", func(*env) string { return "elsewhere" }},
		{"plugins/bStats", "Playkeeper's folder", panel},
		{"plugins/bStats", "a folder inside", func(*env) string { return "../elsewhere" }},
		{name, "panel.db", func(e *env) string { return filepath.Join(e.outside, "panel", "panel.db") }},
		{name, "Playkeeper's folder", func(e *env) string { return filepath.Join(e.outside, "panel", "tls") }},
		{name, "a file inside", func(*env) string { return "../../elsewhere/config.yml" }},
	} {
		for _, op := range ops {
			t.Run(op.name+"/"+path.Base(c.at)+" to "+c.what, func(t *testing.T) {
				e := newEnv(t)
				e.plant(c.at, c.to(e))
				check := e.watch()
				refused(t, op.run(e.d, name), KindLink, c.at)
				check()
				if fi, err := os.Lstat(e.path(c.at)); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
					t.Errorf("the link at %s was replaced or removed", c.at)
				}
			})
		}
	}
}

// A plain open of a named pipe waits until something writes to it, which
// would hold the server's operation forever. A pipe or socket is refused at
// once, whether it stands for the file or for a folder on the way.
func TestSpecialFilesAreRefusedWithoutWaiting(t *testing.T) {
	const name = "plugins/bStats/config.yml"
	for _, c := range []struct {
		what, at string
		mode     uint32
		kind     Kind
	}{
		{"named pipe", name, syscall.S_IFIFO, KindSpecial},
		{"socket", name, syscall.S_IFSOCK, KindSpecial},
		{"named pipe for a folder", "plugins", syscall.S_IFIFO, KindNotFolder},
		{"named pipe for the last folder", "plugins/bStats", syscall.S_IFIFO, KindNotFolder},
	} {
		for _, op := range ops {
			t.Run(op.name+"/"+c.what, func(t *testing.T) {
				e := newEnv(t)
				e.node(c.at, c.mode)
				check := e.watch()
				refused(t, e.noBlock(e.path(c.at), func() error { return op.run(e.d, name) }), c.kind, c.at)
				check()
			})
		}
	}
}

func TestAFolderForAFileOrAFileForAFolderIsRefused(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path("plugins/bStats/config.yml"), 0o750); err != nil {
		t.Fatal(err)
	}
	e.put("world", []byte("not a folder"))
	for _, op := range ops {
		refused(t, op.run(e.d, "plugins/bStats/config.yml"), KindNotFile, "plugins/bStats/config.yml")
		refused(t, op.run(e.d, "world/level.dat"), KindNotFolder, "world")
	}
}

// The game can swap a file between Playkeeper's check and its open. What was
// opened is checked again on the handle, so the swap is refused, and a named
// pipe swapped in does not make the open wait.
func TestFilesSwappedAfterTheCheckAreRefused(t *testing.T) {
	const name = "whitelist.json"
	for _, c := range []struct {
		what string
		swap func(e *env, p string)
		// kind is empty where os.Root refuses with its own error.
		kind Kind
	}{
		{"a named pipe", func(e *env, p string) { e.node(p, syscall.S_IFIFO) }, KindChanged},
		{"a link inside", func(e *env, p string) { e.plant(p, "elsewhere/config.yml") }, KindChanged},
		{"another file", func(e *env, p string) {
			e.put("other.json", []byte("[]"))
			if err := os.Rename(e.path("other.json"), e.path(p)); err != nil {
				e.t.Fatal(err)
			}
		}, KindChanged},
		{"a link to panel.db", func(e *env, p string) { e.plant(p, filepath.Join(e.outside, "panel", "panel.db")) }, ""},
	} {
		for _, op := range readOps {
			t.Run(op.name+"/"+c.what, func(t *testing.T) {
				e := newEnv(t)
				e.put(name, []byte(`[{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"Steve"}]`))
				e.d.hook = func(step, p string) {
					if step == "open" {
						c.swap(e, p)
					}
				}
				check := e.watch()
				err := e.noBlock(e.path(name), func() error { return op.run(e.d, name) })
				if c.kind != "" {
					refused(t, err, c.kind, name)
				} else if err == nil {
					t.Fatal("read through a link swapped in after the check")
				}
				check()
			})
		}
	}
}

// A write makes a new file and renames it over the old one: a hard link to
// the old file keeps the old contents, and a link swapped in after the check
// is replaced, not written through.
func TestWritesReplaceWhatIsThereWithoutWritingThroughIt(t *testing.T) {
	e := newEnv(t)
	e.put("server.properties", []byte("motd=old\n"))
	if err := os.Link(e.path("server.properties"), e.path("elsewhere/hard.properties")); err != nil {
		t.Fatal(err)
	}
	if err := e.d.WriteProperties([]byte("motd=new\n")); err != nil {
		t.Fatal(err)
	}
	if got := e.read("server.properties"); got != "motd=new\n" {
		t.Fatalf("server.properties = %q", got)
	}
	if got := e.read("elsewhere/hard.properties"); got != "motd=old\n" {
		t.Fatalf("the write went through the old file: %q", got)
	}

	e.d.hook = func(step, _ string) {
		if step == "create" {
			e.plant("server.properties", filepath.Join(e.outside, "panel", "panel.db"))
		}
	}
	check := e.watch()
	if err := e.d.WriteProperties([]byte("motd=newer\n")); err != nil {
		t.Fatal(err)
	}
	check()
	if fi, err := os.Lstat(e.path("server.properties")); err != nil || !fi.Mode().IsRegular() || e.read("server.properties") != "motd=newer\n" {
		t.Fatalf("server.properties is not the new file: %v %v", fi, err)
	}
	entries, _ := os.ReadDir(e.data)
	for _, en := range entries {
		if strings.Contains(en.Name(), ".playkeeper-") {
			t.Errorf("left behind %s", en.Name())
		}
	}
}

// The temporary file is created with O_EXCL, so a link planted at its name
// (by a game that guessed or raced it) fails the write instead of being
// written through.
func TestTheTemporaryFileIsAlwaysANewOne(t *testing.T) {
	for _, c := range []struct {
		what string
		to   func(e *env) string
	}{
		{"a file inside", func(*env) string { return "elsewhere/config.yml" }},
		{"panel.db", func(e *env) string { return filepath.Join(e.outside, "panel", "panel.db") }},
	} {
		t.Run(c.what, func(t *testing.T) {
			e := newEnv(t)
			e.put("server.properties", []byte("motd=old\n"))
			e.d.hook = func(step, tmp string) {
				if step == "create" {
					e.plant(tmp, c.to(e))
				}
			}
			check := e.watch()
			if err := e.d.WriteProperties([]byte("motd=new\n")); err == nil {
				t.Fatal("wrote through a link planted at the temporary name")
			}
			check()
			if got := e.read("server.properties"); got != "motd=old\n" {
				t.Fatalf("server.properties = %q", got)
			}
		})
	}
}

// A folder Playkeeper makes is given to the game through a handle on it,
// checked to be the folder just made, so a link swapped in for it does not
// hand the folder it leads to to the game.
func TestNewFoldersAreGivenToTheGameThroughTheirOwnHandle(t *testing.T) {
	for _, c := range []struct {
		what string
		to   func(e *env) string
		// kind is empty where os.Root refuses with its own error.
		kind Kind
	}{
		{"a folder inside", func(*env) string { return "elsewhere" }, KindChanged},
		{"Playkeeper's folder", func(e *env) string { return filepath.Join(e.outside, "panel") }, ""},
	} {
		t.Run(c.what, func(t *testing.T) {
			e := newEnv(t)
			e.d.hook = func(step, p string) {
				if step == "chown" && p == "plugins" {
					e.plant(p, c.to(e))
				}
			}
			check := e.watch()
			err := e.d.WriteFile("plugins/bStats/config.yml", []byte("enabled: false\n"), 0o640)
			if c.kind != "" {
				refused(t, err, c.kind, "plugins")
			} else if err == nil {
				t.Fatal("wrote through a link swapped in for a new folder")
			}
			check()
		})
	}
}

func TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext(t *testing.T) {
	e := newEnv(t)
	const jar = "paper-26.1.2-74.jar"
	content := bytes.Repeat([]byte("fake paper jar "), 20000)
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])
	e.put(jar, content)
	if got, err := e.d.SHA256(context.Background(), jar, 1<<20); err != nil || got != want {
		t.Fatalf("SHA256 = %s %v, want %s", got, err, want)
	}

	e.d.hook = func(step, _ string) {
		if step != "read" {
			return
		}
		f, err := os.OpenFile(e.path(jar), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write(bytes.Repeat([]byte("x"), 2<<20)); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := e.d.SHA256(context.Background(), jar, 1<<20); err != nil || got != want {
		t.Fatalf("SHA256 of a file that grew after the size check = %s %v, want %s", got, err, want)
	}

	e.put(jar, content)
	e.d.hook = func(step, _ string) {
		if step == "read" {
			if err := os.Truncate(e.path(jar), 100); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err := e.d.SHA256(context.Background(), jar, 1<<20)
	refused(t, err, KindChanged, jar)

	e.d.hook = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.d.SHA256(ctx, jar, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("SHA256 with a cancelled context: %v", err)
	}
}

// A plugin can truncate a file to terabytes of holes that take no disk space
// but hours to read. Sizes are checked before reading, and reads are capped.
func TestHugeSparseFilesAreRefusedQuickly(t *testing.T) {
	e := newEnv(t)
	const huge = 1 << 36
	for _, name := range []string{"paper-26.1.2-74.jar", "server.properties", "logs/latest.log", "plugins/bStats/config.yml"} {
		e.put(name, nil)
		if err := os.Truncate(e.path(name), huge); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := e.d.SHA256(ctx, "paper-26.1.2-74.jar", 256<<20)
	refused(t, err, KindTooLarge, "paper-26.1.2-74.jar")
	_, err = e.d.ReadProperties()
	refused(t, err, KindTooLarge, "server.properties")
	if b, err := e.d.ReadTail("logs/latest.log", 64<<10); err != nil || len(b) != 64<<10 {
		t.Fatalf("ReadTail of a huge log: %d bytes, %v", len(b), err)
	}
	if b, _, err := e.d.ReadRange("logs/latest.log", 0, 64<<10); err != nil || len(b) != 64<<10 {
		t.Fatalf("ReadRange of a huge log: %d bytes, %v", len(b), err)
	}
	want := []byte("enabled: false\n")
	if err := e.d.EnsureFile("plugins/bStats/config.yml", want, 0o640, nil); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e.path("plugins/bStats/config.yml")); err != nil || fi.Size() != int64(len(want)) {
		t.Fatalf("a huge config was not replaced: %v %v", fi, err)
	}
}

// A log is read on from where the last read stopped, and a crash report only
// from its start. ReadRange says which file it read, so a log the game
// replaced under the same name is read from its own start.
func TestReadRangeReadsFromItsOffsetAndSaysWhichFile(t *testing.T) {
	e := newEnv(t)
	e.put("logs/gc.log", []byte("first line\nsecond line\n"))
	b, fi, err := e.d.ReadRange("logs/gc.log", 11, 6)
	if err != nil || string(b) != "second" || fi.Size() != 23 {
		t.Fatalf("ReadRange = %q %v %v", b, fi, err)
	}
	if b, _, err := e.d.ReadRange("logs/gc.log", 23, 6); err != nil || len(b) != 0 {
		t.Fatalf("ReadRange at the end = %q %v", b, err)
	}
	if err := os.Rename(e.path("logs/gc.log"), e.path("logs/gc.log.0")); err != nil {
		t.Fatal(err)
	}
	e.put("logs/gc.log", []byte("new\n"))
	b, next, err := e.d.ReadRange("logs/gc.log", 0, 64)
	if err != nil || string(b) != "new\n" || os.SameFile(fi, next) {
		t.Fatalf("ReadRange of the replaced log = %q %v, same file %v", b, err, os.SameFile(fi, next))
	}
}

func TestOversizedFilesAreRefused(t *testing.T) {
	e := newEnv(t)
	e.put("server.properties", bytes.Repeat([]byte("#"), MaxProperties))
	if _, err := e.d.ReadProperties(); err != nil {
		t.Fatalf("server.properties at the limit: %v", err)
	}
	big := bytes.Repeat([]byte("#"), MaxProperties+1)
	e.put("server.properties", big)
	_, err := e.d.ReadProperties()
	refused(t, err, KindTooLarge, "server.properties")
	if limit := err.(*Error).Params["limit"]; limit != "1048576" {
		t.Errorf("limit = %q", limit)
	}

	e.put("whitelist.json", []byte("["+strings.Repeat(`{"uuid":"853c80ef-3c37-49fd-aa49-938b674adae6","name":"Steve"},`, 40)+"{}]"))
	var v any
	refused(t, e.d.ReadJSON("whitelist.json", 1<<10, &v), KindTooLarge, "whitelist.json")

	refused(t, e.d.WriteProperties(append(big, '#')), KindTooLarge, "server.properties")
	if fi, err := os.Stat(e.path("server.properties")); err != nil || fi.Size() != int64(len(big)) {
		t.Fatalf("a refused write changed server.properties: %v %v", fi, err)
	}
}

func TestEnsureFileWritesOnlyWhenItMust(t *testing.T) {
	e := newEnv(t)
	const name = "plugins/bStats/config.yml"
	want := []byte("enabled: false\n")
	off := func(b []byte) bool { return strings.Contains(string(b), "enabled: false") }

	if err := e.d.EnsureFile(name, want, 0o640, off); err != nil {
		t.Fatal(err)
	}
	if got := e.read(name); got != string(want) {
		t.Fatalf("a missing file: %q", got)
	}
	for _, p := range []string{"plugins", "plugins/bStats"} {
		if fi, err := os.Lstat(e.path(p)); err != nil || !fi.IsDir() {
			t.Fatalf("%s was not made: %v %v", p, fi, err)
		}
	}

	kept := "enabled: false\nserverUuid: 853c80ef-3c37-49fd-aa49-938b674adae6\n"
	e.put(name, []byte(kept))
	before := snapshot(t, e.path(name))
	time.Sleep(20 * time.Millisecond)
	if err := e.d.EnsureFile(name, want, 0o640, off); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(t, e.path(name)); after[e.path(name)] != before[e.path(name)] {
		t.Fatal("a file that keep accepts was written again")
	}

	for _, c := range []struct {
		what string
		cur  []byte
		keep func([]byte) bool
	}{
		{"one keep refuses", []byte("enabled: true\n"), off},
		{"one that is not b, without keep", []byte("enabled: false\n# changed\n"), nil},
		{"one too large to compare", bytes.Repeat([]byte("#"), maxEnsured+1), off},
	} {
		e.put(name, c.cur)
		if err := e.d.EnsureFile(name, want, 0o640, c.keep); err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		if got := e.read(name); got != string(want) {
			t.Fatalf("%s was not replaced: %q", c.what, got)
		}
	}
	if entries, _ := os.ReadDir(e.path("plugins/bStats")); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

func TestReadDirListsRealFoldersOnly(t *testing.T) {
	e := newEnv(t)
	e.put("crash-reports/crash-2026-09-02_10.00.00-server.txt", []byte("---- Minecraft Crash Report ----\n"))
	e.put("crash-reports/crash-2026-09-01_10.00.00-server.txt", []byte("---- Minecraft Crash Report ----\n"))
	if err := os.Mkdir(e.path("crash-reports/old"), 0o750); err != nil {
		t.Fatal(err)
	}
	es, err := e.d.ReadDir("crash-reports", 10)
	var names []string
	for _, en := range es {
		names = append(names, en.Name())
	}
	if err != nil || strings.Join(names, " ") != "crash-2026-09-01_10.00.00-server.txt crash-2026-09-02_10.00.00-server.txt old" || !es[2].IsDir() {
		t.Fatalf("ReadDir = %v %v", names, err)
	}
	if es, err := e.d.ReadDir(".", 10); err != nil || len(es) != 2 {
		t.Fatalf("ReadDir(.) = %v %v", es, err)
	}
	_, err = e.d.ReadDir("crash-reports", 2)
	refused(t, err, KindTooMany, "crash-reports")
	_, err = e.d.ReadDir("crash-reports/old/crash.txt", 10)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing folder: %v", err)
	}
	_, err = e.d.ReadDir("crash-reports/crash-2026-09-01_10.00.00-server.txt", 10)
	refused(t, err, KindNotFolder, "crash-reports/crash-2026-09-01_10.00.00-server.txt")

	e.plant("crash-reports", filepath.Join(e.outside, "panel"))
	check := e.watch()
	_, err = e.d.ReadDir("crash-reports", 10)
	refused(t, err, KindLink, "crash-reports")
	check()

	e.node("crash-reports", syscall.S_IFIFO)
	err = e.noBlock(e.path("crash-reports"), func() error { _, err := e.d.ReadDir("crash-reports", 10); return err })
	refused(t, err, KindNotFolder, "crash-reports")
}

// Lstat describes whatever is at a name, a link too, so that a caller can
// skip it as the game does; a link on the way to it is refused.
func TestLstatDescribesTheNameButNotTheWayToIt(t *testing.T) {
	e := newEnv(t)
	e.put("world/datapacks/Graves.zip", []byte("PK"))
	e.plant("world/datapacks/Linked.zip", filepath.Join(e.outside, "panel", "panel.db"))
	if fi, err := e.d.Lstat("world/datapacks/Graves.zip"); err != nil || !fi.Mode().IsRegular() || fi.Size() != 2 {
		t.Fatalf("Lstat(Graves.zip) = %v %v", fi, err)
	}
	if fi, err := e.d.Lstat("world/datapacks/Linked.zip"); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat(Linked.zip) = %v %v", fi, err)
	}
	if _, err := e.d.Lstat("world/datapacks/Missing.zip"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing file: %v", err)
	}
	_, err := e.d.Lstat("../playkeeper/panel/panel.db")
	refused(t, err, KindBadName, "../playkeeper/panel/panel.db")

	e.plant("world/datapacks", "../elsewhere")
	_, err = e.d.Lstat("world/datapacks/config.yml")
	refused(t, err, KindLink, "world/datapacks")
}

// A write that fails part way leaves the file as it was, and nothing behind.
func TestAFailedWriteLeavesTheFileAsItWas(t *testing.T) {
	e := newEnv(t)
	e.put("world/datapacks/Graves.zip", []byte("old"))
	err := e.d.WriteFrom("world/datapacks/Graves.zip", 0o640, func(w io.Writer) error {
		if _, err := w.Write([]byte("half")); err != nil {
			return err
		}
		return errors.New("the upload ended early")
	})
	if err == nil || err.Error() != "the upload ended early" {
		t.Fatalf("WriteFrom = %v", err)
	}
	if got := e.read("world/datapacks/Graves.zip"); got != "old" {
		t.Fatalf("Graves.zip = %q", got)
	}
	if entries, _ := os.ReadDir(e.path("world/datapacks")); len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}

func TestRemoveDeletesAFile(t *testing.T) {
	e := newEnv(t)
	e.put("world/datapacks/Graves.zip", []byte("PK"))
	if err := e.d.Remove("world/datapacks/Graves.zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(e.path("world/datapacks/Graves.zip")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Graves.zip is still there: %v", err)
	}
}

func TestPathsMustStayInsideTheDataDirectory(t *testing.T) {
	e := newEnv(t)
	check := e.watch()
	for _, name := range []string{"", ".", "/etc/passwd", "../playkeeper/panel/panel.db", "plugins/../../playkeeper/panel/panel.db", "plugins/", "./server.properties"} {
		for _, op := range ops {
			refused(t, op.run(e.d, name), KindBadName, name)
		}
		if name != "." {
			_, err := e.d.ReadDir(name, 10)
			refused(t, err, KindBadName, name)
		}
	}
	check()
}

func TestRefusalsSayWhichFileAndWhatToDo(t *testing.T) {
	e := newEnv(t)
	e.plant("plugins", filepath.Join(e.outside, "panel"))
	e.node("whitelist.json", syscall.S_IFIFO)
	e.put("server.properties", bytes.Repeat([]byte("#"), MaxProperties+1))
	var v any
	_, tooLarge := e.d.ReadProperties()
	for _, c := range []struct {
		err       error
		msg, hint string
	}{
		{e.d.WriteFile("plugins/bStats/config.yml", nil, 0o640),
			"plugins in the server's files is a link, which Playkeeper does not follow.",
			"Delete it, or replace it with the file or folder it points to, then try again. If you did not make it, a plugin or mod may have."},
		{e.d.ReadJSON("whitelist.json", 1<<20, &v),
			"whitelist.json in the server's files is not a normal file (it is a named pipe).",
			"Delete it, then try again. If you did not make it, a plugin or mod may have."},
		{tooLarge,
			"server.properties in the server's files is larger than 1 MB, the most Playkeeper reads.",
			"Delete it or make it smaller, then try again."},
	} {
		var ge *Error
		if !errors.As(c.err, &ge) || ge.Msg != c.msg || ge.Hint != c.hint {
			t.Errorf("got %+v, want %q with the hint %q", c.err, c.msg, c.hint)
		}
	}
}
