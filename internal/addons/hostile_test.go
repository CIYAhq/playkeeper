package addons

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/zipdir/zipdirtest"
)

// pipeOnOpen makes the next open of name find a named pipe in dir: the
// game swaps the file or folder for one after Playkeeper checked it.
func pipeOnOpen(t *testing.T, dir, name string) {
	t.Helper()
	openHook = func(n string) {
		if n != name {
			return
		}
		openHook = nil
		p := filepath.Join(dir, name)
		if err := errors.Join(os.Rename(p, p+".moved"), syscall.Mkfifo(p, 0o644)); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { openHook = nil })
}

// promptly runs fn and fails the test when it is still waiting after a few
// seconds. It then opens the named pipes in dir for writing, which lets a
// reader stuck in open(2) go on.
func promptly(t *testing.T, dir string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
		return
	case <-time.After(5 * time.Second):
	}
	t.Error("still waiting after 5s")
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if e.Type()&os.ModeNamedPipe == 0 {
			continue
		}
		if fd, err := syscall.Open(filepath.Join(dir, e.Name()), syscall.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			syscall.Close(fd)
		}
	}
	<-done
}

func TestPipesSwappedInAreRefusedWithoutWaiting(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	setup := func(t *testing.T) (Server, []Installed, string) {
		srv := newServer(t, "paper", "26.2")
		installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
		return srv, installed, filepath.Join(srv.Dir, "plugins")
	}

	t.Run("scan", func(t *testing.T) {
		srv, installed, plugins := setup(t)
		pipeOnOpen(t, plugins, installed[0].FileName)
		var res *ScanResult
		var err error
		promptly(t, plugins, func() { res, err = l.Scan(context.Background(), srv, installed, false) })
		if err != nil || len(res.Entries) != 1 || res.Entries[0].Status != FileModified {
			t.Errorf("scan %+v, %v", res, err)
		}
	})
	t.Run("removal preview", func(t *testing.T) {
		srv, installed, plugins := setup(t)
		pipeOnOpen(t, plugins, installed[0].FileName)
		var p *RemovalPreview
		var err error
		promptly(t, plugins, func() { p, err = l.PreviewUninstall(context.Background(), srv, installed, installed[0].Key()) })
		if err != nil || !p.Changed {
			t.Errorf("preview %+v, %v", p, err)
		}
	})
	t.Run("removal", func(t *testing.T) {
		srv, installed, plugins := setup(t)
		pipeOnOpen(t, plugins, installed[0].FileName)
		var err error
		promptly(t, plugins, func() {
			_, err = l.Uninstall(context.Background(), srv, installed, installed[0].Key(), UninstallOptions{})
		})
		wantKind(t, err, KindModified)
	})
	t.Run("removal of a changed file", func(t *testing.T) {
		srv, installed, plugins := setup(t)
		pipeOnOpen(t, plugins, installed[0].FileName)
		var err error
		promptly(t, plugins, func() {
			_, err = l.Uninstall(context.Background(), srv, installed, installed[0].Key(), UninstallOptions{Changed: true})
		})
		wantKind(t, err, KindModified)
		if fi, err := os.Lstat(filepath.Join(plugins, installed[0].FileName)); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
			t.Errorf("the pipe was removed: %v", err)
		}
	})
	t.Run("replacement", func(t *testing.T) {
		dir := t.TempDir()
		old := []byte("version 1")
		writeFile(t, filepath.Join(dir, "a-1.0.jar"), old)
		staged := filepath.Join(t.TempDir(), "a-2.0.jar")
		writeFile(t, staged, []byte("version 2"))
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		tx := &txn{root: root, t: Target{Folder: "plugins"}, max: DefaultMaxFileSize}
		replaces := &Installed{Name: "A", FileName: "a-1.0.jar", HashAlgo: "sha512", Hash: sha512hex(old), Size: int64(len(old))}
		pipeOnOpen(t, dir, "a-1.0.jar")
		promptly(t, dir, func() {
			err = tx.run(context.Background(), []Step{{Name: "A", FileName: "a-2.0.jar", Replaces: replaces}}, []string{staged})
		})
		wantKind(t, err, KindModified)
	})
	t.Run("folder", func(t *testing.T) {
		srv, installed, _ := setup(t)
		pipeOnOpen(t, srv.Dir, "plugins")
		var err error
		promptly(t, srv.Dir, func() { _, err = l.Scan(context.Background(), srv, installed, false) })
		wantKind(t, err, KindFolderUnusable)
	})
}

// A plugin can grow a jar Playkeeper installed to terabytes of holes. Only
// a file of the recorded size is hashed, so each check answers at once.
func TestGrownJarsAreNotHashed(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	if err := os.Truncate(filepath.Join(srv.Dir, "plugins", installed[0].FileName), 64<<30); err != nil {
		t.Fatal(err)
	}
	unsized := installed[0]
	unsized.Size = 0
	for _, rec := range []Installed{installed[0], unsized} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, err := l.Scan(ctx, srv, []Installed{rec}, false)
		if err != nil || len(res.Entries) != 1 || res.Entries[0].Status != FileModified {
			t.Errorf("size %d: scan %+v, %v", rec.Size, res, err)
		}
		if p, err := l.PreviewUninstall(ctx, srv, []Installed{rec}, rec.Key()); err != nil || !p.Changed {
			t.Errorf("size %d: preview %+v, %v", rec.Size, p, err)
		}
		_, err = l.Uninstall(ctx, srv, []Installed{rec}, rec.Key(), UninstallOptions{})
		cancel()
		wantKind(t, err, KindModified)
	}
}

func TestHashingReadsOnlyTheSizeItSawAndStopsWithItsContext(t *testing.T) {
	dir := t.TempDir()
	data := make([]byte, 1<<20)
	jar := filepath.Join(dir, "a.jar")
	writeFile(t, jar, data)
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, st, err := openFile(root, "a.jar")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sumFile(ctx, f, st.Size(), "sha512"); !errors.Is(err, context.Canceled) {
		t.Errorf("hashing with a cancelled context: %v", err)
	}
	w, err := os.OpenFile(jar, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Write([]byte("grown by the game"))
	if err = errors.Join(err, w.Close()); err != nil {
		t.Fatal(err)
	}
	if sums, err := sumFile(context.Background(), f, st.Size(), "sha512"); err != nil || sums["sha512"] != sha512hex(data) {
		t.Errorf("hashing a file that grew: %v, %v", sums, err)
	}
	if err := os.Truncate(jar, 1<<19); err != nil {
		t.Fatal(err)
	}
	if _, err := sumFile(context.Background(), f, st.Size(), "sha512"); !errors.Is(err, errNotRegular) {
		t.Errorf("hashing a file that shrank: %v", err)
	}
}

func TestAScanStopsWithItsContext(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res, err := l.Scan(ctx, srv, installed, false); !errors.Is(err, context.Canceled) {
		t.Errorf("scan with a cancelled context: %+v, %v", res, err)
	}
}

// allocated is how many bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// The game can put a jar in the add-on folder whose table of contents
// archive/zip would hold in memory by the hundred megabytes.
func TestJarsWithHugeTablesOfContentsAreNotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		jar  []byte
	}{
		{"a count that wraps", zipdirtest.Bomb(300_000, false)},
		{"more entries than metadata needs", zipdirtest.Bomb(200_000, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, "paper", "1.21.4")
			writeFile(t, filepath.Join(srv.Dir, "plugins", "EssentialsX.jar"), tc.jar)
			var res *ScanResult
			var err error
			if n := allocated(func() { res, err = (&Library{}).Scan(context.Background(), srv, nil, false) }); n > 8<<20 {
				t.Errorf("the scan allocated %d MiB", n>>20)
			}
			if err != nil || len(res.Entries) != 1 || res.Entries[0].Meta != (JarMeta{}) {
				t.Fatalf("scan: %+v, %v", res, err)
			}
		})
	}
}
