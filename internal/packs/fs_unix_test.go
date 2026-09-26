//go:build unix

package packs

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestListSkipsLinks(t *testing.T) {
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	folder := filepath.Join(d.DataDir, "world", "datapacks")
	writeFile(t, filepath.Join(folder, "real.zip"), "zip")
	writeFile(t, filepath.Join(folder, "pack", "pack.mcmeta"), dataMcmeta)
	symlink(t, "real.zip", filepath.Join(folder, "link.zip"))
	symlink(t, "pack", filepath.Join(folder, "linked-folder"))
	if err := os.Mkdir(filepath.Join(folder, "linked-mcmeta"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "../pack/pack.mcmeta", filepath.Join(folder, "linked-mcmeta", "pack.mcmeta"))

	packs, err := d.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range packs {
		got = append(got, p.Name)
	}
	if want := []string{"pack", "real.zip"}; !slices.Equal(got, want) {
		t.Errorf("List = %q, want %q", got, want)
	}
}

func TestDataPacksStayInDataDir(t *testing.T) {
	ctx := context.Background()
	pack := dataPack(t)
	outside, dataDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "datapacks", "a.zip"), "a")
	if err := os.Mkdir(filepath.Join(dataDir, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(dataDir, "world", "datapacks"))
	symlink(t, outside, filepath.Join(dataDir, "linked"))

	for _, level := range []string{"world", "linked"} {
		d := DataPacks{DataDir: dataDir, Level: level}
		_, _, err := d.Install(ctx, "evil.zip", bytes.NewReader(pack), int64(len(pack)), true)
		wantCode(t, err, CodeFileRefused)
		_, err = d.List()
		wantCode(t, err, CodeFileRefused)
		wantCode(t, d.Remove("a.zip"), CodeFileRefused)
	}
	if names := dirNames(t, outside); !slices.Equal(names, []string{"datapacks"}) {
		t.Errorf("outside the data directory: %q", names)
	}
	if names := dirNames(t, filepath.Join(outside, "datapacks")); !slices.Equal(names, []string{"a.zip"}) {
		t.Errorf("outside the data directory: %q", names)
	}
}

// A link that stays inside the data directory is refused as well, as
// internal/gamefiles refuses it, so the refusal names the link to fix and
// what it leads to is neither read nor changed.
func TestDataPacksDoNotFollowLinksInside(t *testing.T) {
	ctx := context.Background()
	pack := dataPack(t)
	dataDir := t.TempDir()
	elsewhere := filepath.Join(dataDir, "elsewhere")
	writeFile(t, filepath.Join(elsewhere, "datapacks", "Graves.zip"), string(pack))
	writeFile(t, filepath.Join(elsewhere, "datapacks", "Loose", "pack.mcmeta"), dataMcmeta)
	d := DataPacks{DataDir: dataDir, Level: "world"}
	world := filepath.Join(dataDir, "world")
	datapacks := filepath.Join(world, "datapacks")

	for _, link := range []struct{ at, to string }{{world, "elsewhere"}, {datapacks, "../elsewhere/datapacks"}} {
		if err := os.RemoveAll(world); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(link.at), 0o755); err != nil {
			t.Fatal(err)
		}
		symlink(t, link.to, link.at)
		_, _, err := d.Install(ctx, "evil.zip", bytes.NewReader(pack), int64(len(pack)), true)
		e := wantCode(t, err, CodeFileRefused)
		if rel, _ := filepath.Rel(dataDir, link.at); e.Params["path"] != rel || e.Params["kind"] != "link" || e.Hint == "" {
			t.Errorf("refusal = %+v", e)
		}
		_, err = d.List()
		wantCode(t, err, CodeFileRefused)
		wantCode(t, d.Remove("Graves.zip"), CodeFileRefused)
		_, err = d.Summary(ctx, "Loose")
		wantCode(t, err, CodeFileRefused)
		_, err = d.Icon(ctx, "Graves.zip")
		wantCode(t, err, CodeFileRefused)
	}
	_, _, err := d.Install(ctx, "evil.zip", bytes.NewReader(pack), int64(len(pack)), true)
	if e := wantCode(t, err, CodeFileRefused); e.Msg != "Playkeeper couldn't install the data pack. world/datapacks in the server's files is a link, which Playkeeper does not follow." {
		t.Errorf("message = %q", e.Msg)
	}

	if err := os.Remove(datapacks); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(datapacks, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "../../elsewhere/datapacks/Graves.zip", filepath.Join(datapacks, "Graves.zip"))
	wantCode(t, d.Remove("Graves.zip"), CodeFileRefused)
	_, _, err = d.Install(ctx, "Graves.zip", bytes.NewReader(pack), int64(len(pack)), true)
	wantCode(t, err, CodeFileRefused)
	if packs, err := d.List(); err != nil || len(packs) != 0 {
		t.Errorf("List = %v, %v", packs, err)
	}
	if fi, err := os.Lstat(filepath.Join(datapacks, "Graves.zip")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced or removed: %v", err)
	}

	if names := dirNames(t, filepath.Join(elsewhere, "datapacks")); !slices.Equal(names, []string{"Graves.zip", "Loose"}) {
		t.Errorf("the folder links led to holds %q", names)
	}
	if b, err := os.ReadFile(filepath.Join(elsewhere, "datapacks", "Graves.zip")); err != nil || !bytes.Equal(b, pack) {
		t.Errorf("the pack a link led to changed: %v", err)
	}
}

func TestStoreFIFO(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	pack := resourcePack(t)
	info, err := Inspect(context.Background(), bytes.NewReader(pack), int64(len(pack)), Resource, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.Dir, info.SHA1+".zip")
	if err := syscall.Mkfifo(name, 0o644); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(s, func(string) bool { return true })
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PackPath(info.SHA1), nil))
		done <- rec.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusNotFound {
			t.Errorf("serving a FIFO: status %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handler hangs on a FIFO")
	}

	if _, err := s.Put(context.Background(), bytes.NewReader(pack), int64(len(pack))); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(name); err != nil || !st.Mode().IsRegular() {
		t.Errorf("Put left %v, %v", st.Mode(), err)
	}
}

func TestSummaryFIFO(t *testing.T) {
	d := DataPacks{DataDir: t.TempDir(), Level: "world"}
	folder := filepath.Join(d.DataDir, "world", "datapacks", "Loose")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pack.mcmeta", "pack.png"} {
		if err := syscall.Mkfifo(filepath.Join(folder, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		s, err := d.Summary(context.Background(), "Loose")
		if err == nil && s != (Summary{}) {
			err = errors.New("summary of FIFOs isn't empty")
		}
		if err == nil {
			_, err = d.Icon(context.Background(), "Loose")
			if errors.Is(err, ErrNoIcon) {
				err = nil
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Summary hangs on a FIFO")
	}
}

func TestStoreUmask(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "resourcepacks")}
	defer syscall.Umask(syscall.Umask(0o077))
	pack := resourcePack(t)
	info, err := s.Put(context.Background(), bytes.NewReader(pack), int64(len(pack)))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{s.Dir: 0o755, filepath.Join(s.Dir, info.SHA1+".zip"): 0o644} {
		if st, err := os.Stat(name); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s: %v, want mode %v", name, err, want)
		}
	}
}

func TestStoreLinks(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	pack := resourcePack(t)
	info, err := Inspect(context.Background(), bytes.NewReader(pack), int64(len(pack)), Resource, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.zip")
	writeFile(t, outside, "outside")
	name := filepath.Join(s.Dir, info.SHA1+".zip")
	symlink(t, outside, name)

	rec := httptest.NewRecorder()
	NewHandler(s, func(string) bool { return true }).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, PackPath(info.SHA1), nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("serving a link out of the store: status %d", rec.Code)
	}
	if _, err := s.Put(context.Background(), bytes.NewReader(pack), int64(len(pack))); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(name); err != nil || !st.Mode().IsRegular() {
		t.Errorf("Put left %v, %v", st.Mode(), err)
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "outside" {
		t.Errorf("Put wrote through the link: %q, %v", b, err)
	}
}

func TestInstallOwner(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		uid, gid = 4242, 4243
	}
	d := DataPacks{DataDir: t.TempDir(), Level: "world", Owner: &gamefiles.Owner{UID: uid, GID: gid}}
	pack := dataPack(t)
	if _, _, err := d.Install(context.Background(), "test.zip", bytes.NewReader(pack), int64(len(pack)), false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"world", "world/datapacks", "world/datapacks/test.zip"} {
		st, err := os.Lstat(filepath.Join(d.DataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if sys := st.Sys().(*syscall.Stat_t); int(sys.Uid) != uid || int(sys.Gid) != gid {
			t.Errorf("%s is owned by %d:%d, want %d:%d", name, sys.Uid, sys.Gid, uid, gid)
		}
	}
}
