//go:build linux

package gamefiles

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// placeStaged places a file staged outside the data directory, as an upload
// does, at name.
func placeStaged(d *Dir, name string, replace bool) error {
	dir, err := os.MkdirTemp("", "playkeeper-staged-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	staged := filepath.Join(dir, "0.bin")
	if err := os.WriteFile(staged, []byte("uploaded\n"), 0o600); err != nil {
		return err
	}
	return d.Place(name, staged, 0o640, replace)
}

// The file browser's own operations, each with the name it acts on.
var browseOps = []fileOp{
	{"List", func(d *Dir, name string) error { _, _, err := d.List(name, 100); return err }},
	{"MakeFolder", func(d *Dir, name string) error { return d.MakeFolder(name) }},
	{"MoveFrom", func(d *Dir, name string) error { return d.Move(name, "moved.yml") }},
	{"MoveTo", func(d *Dir, name string) error {
		if err := d.WriteFile("to-move.yml", []byte("x: 1\n"), 0o640); err != nil {
			return err
		}
		return d.Move("to-move.yml", name)
	}},
	{"Delete", func(d *Dir, name string) error { return d.Delete(name) }},
	{"Walk", func(d *Dir, name string) error {
		return d.Walk(context.Background(), name, 100, func(string, Entry) error { return nil })
	}},
}

// Every path from the dashboard is checked before anything is touched: an
// absolute path, a dot segment or a path that climbs out is refused, and so
// is the data directory itself for anything but listing it.
func TestBrowsingStaysInsideTheDataDirectory(t *testing.T) {
	e := newEnv(t)
	check := e.watch()
	for _, name := range []string{"", "/etc/passwd", "../playkeeper/panel/panel.db", "plugins/../../playkeeper/panel/panel.db", "plugins/", "./server.properties", "..", "plugins//config.yml"} {
		for _, op := range browseOps {
			refused(t, op.run(e.d, name), KindBadName, name)
		}
	}
	for _, op := range browseOps[1:5] {
		refused(t, op.run(e.d, "."), KindBadName, ".")
	}
	if err := e.d.Move("elsewhere/config.yml", "../playkeeper/panel/panel.db"); KindOf(err) != KindBadName {
		t.Fatalf("a move out of the data directory: %v", err)
	}
	check()
	if got := e.read("elsewhere/config.yml"); got != "the game's own file\n" {
		t.Fatalf("elsewhere/config.yml = %q", got)
	}
}

// A link on the way to a name is refused wherever it leads, and nothing it
// leads to is listed, made, moved or deleted.
func TestBrowsingRefusesLinksOnTheWay(t *testing.T) {
	const name = "plugins/bStats/config.yml"
	for _, c := range []struct {
		at, what string
		to       func(e *env) string
	}{
		{"plugins", "Playkeeper's folder", func(e *env) string { return filepath.Join(e.outside, "panel") }},
		{"plugins", "a folder inside", func(*env) string { return "elsewhere" }},
		{"plugins/bStats", "Playkeeper's folder", func(e *env) string { return filepath.Join(e.outside, "panel") }},
		{"plugins/bStats", "a folder inside", func(*env) string { return "../elsewhere" }},
	} {
		for _, op := range browseOps {
			t.Run(op.name+"/"+filepath.Base(c.at)+" to "+c.what, func(t *testing.T) {
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

// A link or a special file at the name itself can be put out of the way:
// deleting or moving it deletes or moves the link, never what it leads to.
// Listing it or making a folder there is refused, without waiting on a pipe.
func TestALinkOrPipeAtTheNameIsMovedOrDeletedAsItself(t *testing.T) {
	for _, c := range []struct {
		what string
		make func(e *env, p string)
	}{
		{"a link to Playkeeper's database", func(e *env, p string) { e.plant(p, filepath.Join(e.outside, "panel", "panel.db")) }},
		{"a link to Playkeeper's folder", func(e *env, p string) { e.plant(p, filepath.Join(e.outside, "panel")) }},
		{"a link inside", func(e *env, p string) { e.plant(p, "../elsewhere") }},
		{"a named pipe", func(e *env, p string) { e.node(p, syscall.S_IFIFO) }},
		{"a socket", func(e *env, p string) { e.node(p, syscall.S_IFSOCK) }},
	} {
		t.Run(c.what, func(t *testing.T) {
			const name = "plugins/linked"
			e := newEnv(t)
			c.make(e, name)
			check := e.watch()
			if err := e.noBlock(e.path(name), func() error { _, _, err := e.d.List(name, 10); return err }); err == nil {
				t.Fatal("listed a link or a pipe as a folder")
			}
			if err := e.d.MakeFolder(name); err == nil || (KindOf(err) != KindLink && KindOf(err) != KindSpecial) {
				t.Fatalf("MakeFolder over it: %v", err)
			}
			if err := e.noBlock(e.path(name), func() error { return e.d.Move(name, "plugins/put-aside") }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(e.path(name)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("still at %s: %v", name, err)
			}
			if err := e.noBlock(e.path("plugins/put-aside"), func() error { return e.d.Delete("plugins/put-aside") }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(e.path("plugins/put-aside")); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("not deleted: %v", err)
			}
			check()
		})
	}
}

// A folder the game swaps for a link between Playkeeper's check and its step
// can't lead anywhere: os.Root refuses a link out of the data directory, and
// a handle is checked to be the folder that was checked.
func TestBrowsingSwapsAfterTheCheckReachNothingOutside(t *testing.T) {
	outside := func(e *env) string { return filepath.Join(e.outside, "panel") }
	for _, c := range []struct {
		what, step string
		setup      func(e *env)
		run        func(d *Dir) error
		swap       string
	}{
		{"deleting a file", "delete", func(e *env) { e.put("plugins/tls/panel.key", []byte("the game's key\n")) }, func(d *Dir) error { return d.Delete("plugins/tls/panel.key") }, "plugins"},
		{"deleting a folder", "delete", func(e *env) { e.put("plugins/tls/panel.key", []byte("the game's key\n")) }, func(d *Dir) error { return d.Delete("plugins/tls") }, "plugins"},
		{"moving into a folder", "move", func(e *env) {
			e.put("plugins/old.yml", []byte("x\n"))
			e.put("configs/keep.txt", []byte("x\n"))
		}, func(d *Dir) error { return d.Move("plugins/old.yml", "configs/panel.db") }, "configs"},
		{"making a folder", "mkdir", func(e *env) { e.put("plugins/keep.txt", []byte("x\n")) }, func(d *Dir) error { return d.MakeFolder("plugins/tls") }, "plugins"},
		{"giving a new folder to the game", "chown", func(e *env) { e.put("plugins/keep.txt", []byte("x\n")) }, func(d *Dir) error { return d.MakeFolder("plugins/new") }, "plugins/new"},
		{"placing an upload", "place", func(e *env) { e.put("plugins/keep.txt", []byte("x\n")) }, func(d *Dir) error { return placeStaged(d, "plugins/panel.db", true) }, "plugins"},
		{"listing a folder", "list", func(e *env) { e.put("plugins/keep.txt", []byte("x\n")) }, func(d *Dir) error { _, _, err := d.List("plugins", 10); return err }, "plugins"},
	} {
		t.Run(c.what, func(t *testing.T) {
			e := newEnv(t)
			c.setup(e)
			e.d.hook = func(step, _ string) {
				if step == c.step {
					e.d.hook = nil
					if err := os.Rename(e.path(c.swap), e.path("set-aside")); err != nil && !errors.Is(err, fs.ErrNotExist) {
						t.Fatal(err)
					}
					e.plant(c.swap, outside(e))
				}
			}
			check := e.watch()
			if err := c.run(e.d); err == nil {
				t.Fatalf("%s went through a link swapped in for %s", c.what, c.swap)
			}
			check()
		})
	}
}

func TestListDescribesEachEntryAsItIs(t *testing.T) {
	e := newEnv(t)
	e.put("plugins/Essentials/config.yml", []byte("ops-name-color: '4'\n"))
	e.put("plugins/EssentialsX.jar", []byte("PK\x03\x04jar"))
	e.plant("plugins/linked", filepath.Join(e.outside, "panel"))
	e.node("plugins/pipe", syscall.S_IFIFO)
	check := e.watch()
	var list []Entry
	err := e.noBlock(e.path("plugins/pipe"), func() error {
		var err error
		list, _, err = e.d.List("plugins", 100)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	check()
	var got []string
	for _, en := range list {
		kind := "file"
		switch {
		case en.Mode&fs.ModeSymlink != 0:
			kind = "link"
		case en.Mode.IsDir():
			kind = "folder"
		case !en.Mode.IsRegular():
			kind = "special"
		}
		got = append(got, en.Name+":"+kind)
	}
	if strings.Join(got, " ") != "Essentials:folder EssentialsX.jar:file linked:link pipe:special" {
		t.Fatalf("List = %v", got)
	}
	if list[1].Size != 7 || list[1].ModTime.IsZero() {
		t.Fatalf("EssentialsX.jar = %+v", list[1])
	}
	top, more, err := e.d.List(".", 100)
	if err != nil || more || len(top) != 2 {
		t.Fatalf("List(.) = %v %v %v", top, more, err)
	}
	some, more, err := e.d.List("plugins", 2)
	if err != nil || !more || len(some) != 2 {
		t.Fatalf("List with a limit = %v %v %v", some, more, err)
	}
	e.put("plugins/Essentials/config.yml", []byte("x"))
	_, _, err = e.d.List("plugins/Essentials/config.yml", 10)
	refused(t, err, KindNotFolder, "plugins/Essentials/config.yml")
	if _, _, err := e.d.List("plugins/Missing", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing folder: %v", err)
	}
}

func TestMakeFolderMakesItForTheGame(t *testing.T) {
	e := newEnv(t)
	if err := e.d.MakeFolder("plugins/Essentials/backup"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"plugins", "plugins/Essentials", "plugins/Essentials/backup"} {
		var st syscall.Stat_t
		if err := syscall.Lstat(e.path(p), &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR || int(st.Uid) != os.Getuid() {
			t.Fatalf("%s: %+v %v", p, st, err)
		}
	}
	refused(t, e.d.MakeFolder("plugins/Essentials"), KindExists, "plugins/Essentials")
	e.put("plugins/readme.txt", nil)
	refused(t, e.d.MakeFolder("plugins/readme.txt"), KindExists, "plugins/readme.txt")
	refused(t, e.d.MakeFolder("plugins/readme.txt/inside"), KindNotFolder, "plugins/readme.txt")
}

func TestMoveRenamesAndRefusesWhatItWouldLose(t *testing.T) {
	e := newEnv(t)
	e.put("plugins/Essentials/config.yml", []byte("a: 1\n"))
	e.put("plugins/old.yml", []byte("old\n"))
	e.put("plugins/keep.yml", []byte("keep\n"))
	if err := e.d.Move("plugins/old.yml", "plugins/new.yml"); err != nil {
		t.Fatal(err)
	}
	if e.read("plugins/new.yml") != "old\n" {
		t.Fatal("the rename lost the file")
	}
	refused(t, e.d.Move("plugins/new.yml", "plugins/keep.yml"), KindExists, "plugins/keep.yml")
	if e.read("plugins/keep.yml") != "keep\n" || e.read("plugins/new.yml") != "old\n" {
		t.Fatal("a refused move replaced a file")
	}
	refused(t, e.d.Move("plugins", "plugins/Essentials/plugins"), KindIntoItself, "plugins")
	refused(t, e.d.Move("plugins/Essentials", "plugins/Essentials"), KindIntoItself, "plugins/Essentials")
	if err := e.d.Move("plugins/Essentials", "Essentials"); err != nil {
		t.Fatal(err)
	}
	if e.read("Essentials/config.yml") != "a: 1\n" {
		t.Fatal("the folder moved without its file")
	}
	if err := e.d.Move("plugins/missing.yml", "plugins/other.yml"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("moving a missing file: %v", err)
	}
	refused(t, e.d.Move("plugins/new.yml", "plugins/keep.yml/inside"), KindNotFolder, "plugins/keep.yml")
}

func TestMoveRefusesAMissingDestinationFolder(t *testing.T) {
	e := newEnv(t)
	e.put("plugins/keep.yml", []byte("keep\n"))
	err := e.d.Move("plugins/keep.yml", "nowhere/keep.yml")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a move into a folder that isn't there: %v", err)
	}
}

// Deleting a folder deletes the links in it, never what they lead to.
func TestDeleteRemovesFoldersWithoutFollowingLinksInThem(t *testing.T) {
	e := newEnv(t)
	e.put("logs/latest.log", []byte("line\n"))
	e.put("logs/old/2026-09-01.log.gz", []byte("gz"))
	e.plant("logs/panel", filepath.Join(e.outside, "panel"))
	e.plant("logs/db", filepath.Join(e.outside, "panel", "panel.db"))
	e.plant("logs/inside", "../elsewhere")
	e.node("logs/pipe", syscall.S_IFIFO)
	check := e.watch()
	if err := e.noBlock(e.path("logs/pipe"), func() error { return e.d.Delete("logs") }); err != nil {
		t.Fatal(err)
	}
	check()
	if _, err := os.Lstat(e.path("logs")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("logs is still there: %v", err)
	}
	e.put("server.properties", nil)
	if err := e.d.Delete("server.properties"); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Delete("server.properties"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleting a missing file: %v", err)
	}
}

func TestPlaceMovesTheUploadInForTheGame(t *testing.T) {
	e := newEnv(t)
	if err := placeStaged(e.d, "plugins/Essentials/config.yml", false); err != nil {
		t.Fatal(err)
	}
	if e.read("plugins/Essentials/config.yml") != "uploaded\n" {
		t.Fatal("the upload isn't there")
	}
	var st syscall.Stat_t
	if err := syscall.Lstat(e.path("plugins/Essentials/config.yml"), &st); err != nil || st.Mode&0o777 != 0o640 || int(st.Uid) != os.Getuid() {
		t.Fatalf("the placed file: mode %o uid %d %v", st.Mode&0o777, st.Uid, err)
	}
	e.put("server.properties", []byte("motd=old\n"))
	refused(t, placeStaged(e.d, "server.properties", false), KindExists, "server.properties")
	if e.read("server.properties") != "motd=old\n" {
		t.Fatal("a refused place replaced the file")
	}
	if err := placeStaged(e.d, "server.properties", true); err != nil || e.read("server.properties") != "uploaded\n" {
		t.Fatalf("replace: %v", err)
	}
	if err := placeStaged(e.d, "server-icon.png", false); err != nil || e.read("server-icon.png") != "uploaded\n" {
		t.Fatalf("a file at the top: %v", err)
	}
}

// Where the staging folder and the data directory are on different file
// systems, the upload is copied in the way a write is, and the staged file
// is deleted.
func TestPlaceCopiesAcrossFileSystems(t *testing.T) {
	e := newEnv(t)
	orig := renameInto
	renameInto = func(string, *os.File, string) error { return errOtherFileSystem }
	t.Cleanup(func() { renameInto = orig })
	dir := t.TempDir()
	staged := filepath.Join(dir, "0.bin")
	if err := os.WriteFile(staged, []byte("copied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.d.Place("plugins/copied.yml", staged, 0o640, false); err != nil {
		t.Fatal(err)
	}
	if e.read("plugins/copied.yml") != "copied\n" {
		t.Fatal("not copied")
	}
	if _, err := os.Lstat(staged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the staged file is still there: %v", err)
	}
	entries, _ := os.ReadDir(e.path("plugins"))
	if len(entries) != 1 {
		t.Fatalf("left behind %v", entries)
	}
}

// Walk hands links and special files to its caller as they are and never
// goes into them, so a zip of a folder can't take in anything outside.
func TestWalkNeverFollowsLinks(t *testing.T) {
	e := newEnv(t)
	e.put("world/level.dat", []byte("nbt"))
	e.put("world/region/r.0.0.mca", []byte("region"))
	e.plant("world/panel", filepath.Join(e.outside, "panel"))
	e.plant("world/elsewhere", "../elsewhere")
	e.node("world/pipe", syscall.S_IFIFO)
	check := e.watch()
	var seen []string
	err := e.noBlock(e.path("world/pipe"), func() error {
		return e.d.Walk(context.Background(), "world", 100, func(p string, en Entry) error {
			kind := "file"
			switch {
			case en.Mode&fs.ModeSymlink != 0:
				kind = "link"
			case en.Mode.IsDir():
				kind = "folder"
			case !en.Mode.IsRegular():
				kind = "special"
			}
			seen = append(seen, p+":"+kind)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	check()
	want := "world:folder world/elsewhere:link world/level.dat:file world/panel:link world/pipe:special world/region:folder world/region/r.0.0.mca:file"
	if strings.Join(seen, " ") != want {
		t.Fatalf("Walk saw %v", seen)
	}
	var all []string
	if err := e.d.Walk(context.Background(), ".", 100, func(p string, _ Entry) error { all = append(all, p); return nil }); err != nil || all[0] != "." {
		t.Fatalf("Walk(.) = %v %v", all, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.d.Walk(ctx, "world", 100, func(string, Entry) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Walk with a cancelled context: %v", err)
	}
	err = e.d.Walk(context.Background(), "world", 3, func(string, Entry) error { return nil })
	refused(t, err, KindTooMany, "world")
}
