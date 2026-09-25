package diskusage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func (f *fixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(f.path(rel)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) move(from, to string) {
	f.t.Helper()
	if err := os.Rename(f.path(from), f.path(to)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) candidate(rep *Report, rel string) *Candidate {
	f.t.Helper()
	for i := range rep.Candidates {
		if rep.Candidates[i].Path == f.path(rel) {
			return &rep.Candidates[i]
		}
	}
	f.t.Fatalf("%s not offered", rel)
	return nil
}

// newServer is a data folder with a world, an old log and software for an
// older version, next to a folder that isn't the server's.
func newServer(t *testing.T) (*fixture, Layout) {
	f := newFixture(t)
	f.file("data/world/level.dat", 100, 0)
	f.file("data/logs/2020-01-01-1.log.gz", 700, old)
	f.file("data/versions/1.21.3/paper-1.21.3.jar", 3000, old)
	f.file("elsewhere/2020-01-01-1.log.gz", 700, old)
	return f, Layout{Servers: []Server{{ID: "s", DataDir: f.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
}

func TestRemoveChecksAgain(t *testing.T) {
	const logRel, versionRel = "data/logs/2020-01-01-1.log.gz", "data/versions/1.21.3"
	tests := []struct {
		name   string
		rel    string
		change func(f *fixture)
		want   error
		kept   []string
	}{
		{"file as scanned", logRel, nil, nil, []string{"data/world/level.dat"}},
		{"folder as scanned", versionRel, nil, nil, []string{"data/world/level.dat"}},
		{"file already gone", logRel, func(f *fixture) { f.remove(logRel) }, fs.ErrNotExist, nil},
		{"file written to", logRel, func(f *fixture) { f.file(logRel, 800, old) }, errChanged, []string{logRel}},
		{"file re-dated", logRel, func(f *fixture) { f.date(logRel, old+day) }, errChanged, []string{logRel}},
		{"file swapped for a link to the world", logRel, func(f *fixture) {
			f.remove(logRel)
			f.link(logRel, "../world/level.dat")
		}, errChanged, []string{logRel, "data/world/level.dat"}},
		{"folder on the way swapped for a link", logRel, func(f *fixture) {
			f.move("data/logs", "data/logs.moved")
			f.link("data/logs", "../elsewhere")
		}, errChanged, []string{"elsewhere/2020-01-01-1.log.gz", "data/logs.moved/2020-01-01-1.log.gz"}},
		{"world appears on the way", logRel, func(f *fixture) { f.file("data/logs/level.dat", 10, 0) }, errChanged, []string{logRel}},
		{"data folder becomes a world", logRel, func(f *fixture) { f.file("data/session.lock", 10, 0) }, errChanged, []string{logRel}},
		{"data folder replaced", logRel, func(f *fixture) {
			f.move("data", "data.moved")
			f.file(logRel, 700, old)
		}, errChanged, []string{logRel, "data.moved/logs/2020-01-01-1.log.gz"}},
		{"folder becomes a world", versionRel, func(f *fixture) { f.file(versionRel+"/level.dat", 10, 0) }, errChanged, []string{versionRel + "/paper-1.21.3.jar"}},
		{"folder swapped for a link to the world", versionRel, func(f *fixture) {
			f.move(versionRel, "data/versions/moved")
			f.link(versionRel, "../world")
		}, errChanged, []string{versionRel, "data/world/level.dat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, l := newServer(t)
			c := f.candidate(scanOK(t, l, f.options()), tt.rel)
			if tt.change != nil {
				tt.change(f)
			}
			err := c.remove()
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("remove = %v, want %v", err, tt.want)
			}
			if tt.want == nil && f.exists(tt.rel) {
				t.Errorf("%s is still there", tt.rel)
			}
			for _, rel := range tt.kept {
				if !f.exists(rel) {
					t.Errorf("%s was deleted", rel)
				}
			}
		})
	}
}

func TestOpenDirRefusesSwaps(t *testing.T) {
	f := newFixture(t)
	f.file("top/a/x", 1, 0)
	f.file("top/b/y", 1, 0)
	r, err := os.OpenRoot(f.path("top"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fi, err := r.Lstat("a")
	if err != nil {
		t.Fatal(err)
	}
	d, err := openDir(r, "a", fi)
	if err != nil {
		t.Fatalf("openDir of an unchanged folder: %v", err)
	}
	d.Close()

	f.move("top/a", "top/a.old")
	f.move("top/b", "top/a")
	if _, err := openDir(r, "a", fi); !errors.Is(err, errChanged) {
		t.Errorf("folder swapped for another: openDir = %v, want errChanged", err)
	}
	f.move("top/a", "top/b")
	f.link("top/a", "b")
	if _, err := openDir(r, "a", fi); !errors.Is(err, errChanged) {
		t.Errorf("folder swapped for a link: openDir = %v, want errChanged", err)
	}
}

// deleter deletes backups from the backups folder, as the agent would.
type deleter struct {
	dir   string
	files map[string]string
	calls []string
	err   error
	hook  func(id string)
}

func newDeleter(l Layout) *deleter {
	d := &deleter{dir: l.BackupsDir, files: map[string]string{}}
	for _, b := range l.Backups {
		d.files[b.ID] = b.FileName
	}
	return d
}

func (d *deleter) DeleteBackup(_ context.Context, id string) error {
	d.calls = append(d.calls, id)
	if d.hook != nil {
		d.hook(id)
	}
	if d.err != nil {
		return d.err
	}
	for _, name := range []string{d.files[id], d.files[id] + ".sha256"} {
		if err := os.Remove(filepath.Join(d.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func ids(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func statuses(res *CleanResult) []Status {
	var out []Status
	for _, o := range res.Outcomes {
		out = append(out, o.Status)
	}
	return out
}

func cleanOK(t *testing.T, ctx context.Context, l Layout, o Options, ids []string, d BackupDeleter) *CleanResult {
	t.Helper()
	res, err := Clean(ctx, l, o, ids, d)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	return res
}

func TestClean(t *testing.T) {
	m := newMachine(t)
	rep := scanOK(t, m.l, m.options())
	del := newDeleter(m.l)
	res := cleanOK(t, context.Background(), m.l, m.options(), ids(rep.Candidates), del)
	if len(res.Outcomes) != len(rep.Candidates) {
		t.Fatalf("%d outcomes for %d candidates", len(res.Outcomes), len(rep.Candidates))
	}
	var freed int64
	for i, out := range res.Outcomes {
		c := rep.Candidates[i]
		if out.ID != c.ID || out.Status != StatusDeleted || out.Bytes != c.Bytes || out.Path != c.Path || out.Text != "Deleted." {
			t.Errorf("outcome %+v for %s (%d bytes)", out, c.Path, c.Bytes)
		}
		if _, err := os.Lstat(c.Path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there", c.Path)
		}
		freed += c.Bytes
	}
	if res.Freed != freed {
		t.Errorf("freed %d bytes, want %d", res.Freed, freed)
	}
	slices.Sort(del.calls)
	if !slices.Equal(del.calls, []string{"a1", "b1"}) {
		t.Errorf("deleted backups %v, want a1 and b1", del.calls)
	}
	for _, rel := range []string{
		"servers/a/data/world/level.dat",
		"servers/a/data/world_nether/DIM-1/region/r.0.0.mca",
		"servers/a/data/logs/latest.log",
		"servers/a/data/logs/2020-02-01-1.log.gz",
		"servers/a/data/crash-reports/crash-2020-02-01_00.00.00-server.txt",
		"servers/a/data/paper-1.21.4-232.jar",
		"servers/a/data/purpur-1.21.3-2000.jar",
		"servers/a/data/versions/1.21.4/paper-1.21.4.jar",
		"servers/a/data/cache/mojang_1.21.4.jar",
		"servers/a/data/plugins/Example/config.yml",
		"servers/a/" + m.recent + "/world/level.dat",
		"servers/a/data.failed-update-20200101-000000",
		"servers/a/notes.txt",
		"servers/b/data.replaced-20200101-000000/logs/latest.log",
		"backups/playkeeper-a-2.tar.gz.sha256",
		"backups/playkeeper-gone-9.tar.gz",
		"backups/playkeeper-unknown-7.tar.gz",
		"backups/.offsite-1234.partial",
		"staging/def456/archive.tar.gz",
		"staging/ghi789/archive.tar.gz",
	} {
		if !m.exists(rel) {
			t.Errorf("%s was deleted", rel)
		}
	}
	if again := scanOK(t, m.l, m.options()); len(again.Candidates) != 0 {
		t.Errorf("after cleaning, still offered: %+v", again.Candidates)
	}
}

func TestCleanOnlyWhatAFreshScanOffers(t *testing.T) {
	m := newMachine(t)
	got := m.candidates(scanOK(t, m.l, m.options()))
	bLog := got["servers/b/data/logs/2020-01-01-1.log.gz"].ID
	aJar := got["servers/a/data/paper-1.21.3-100.jar"].ID
	b1 := got["backups/playkeeper-b-1.tar.gz"].ID
	m.date("servers/b/data/logs/2020-01-01-1.log.gz", old+day)
	m.l.Servers[0].Busy = true
	unknown := strings.Repeat("0", 32)

	res := cleanOK(t, context.Background(), m.l, m.options(), []string{bLog, aJar, unknown, bLog, b1}, newDeleter(m.l))
	want := []Status{StatusNotOffered, StatusNotOffered, StatusNotOffered, StatusDeleted}
	if got := statuses(res); !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	for _, out := range res.Outcomes[:3] {
		if out.Bytes != 0 || !strings.Contains(out.Text, "Scan again") {
			t.Errorf("outcome %+v", out)
		}
	}
	for _, rel := range []string{"servers/b/data/logs/2020-01-01-1.log.gz", "servers/a/data/paper-1.21.3-100.jar"} {
		if !m.exists(rel) {
			t.Errorf("%s was deleted", rel)
		}
	}
}

func TestCleanChecksEachPathAgain(t *testing.T) {
	m := newMachine(t)
	got := m.candidates(scanOK(t, m.l, m.options()))
	a1 := got["backups/playkeeper-a-1.tar.gz"]
	logRel, versionRel := "servers/a/data/logs/2020-01-01-1.log.gz", "servers/a/data/versions/1.21.3"
	del := newDeleter(m.l)
	del.hook = func(string) {
		m.remove(logRel)
		m.link(logRel, "../world/level.dat")
		m.file(versionRel+"/level.dat", 10, 0)
	}
	res := cleanOK(t, context.Background(), m.l, m.options(), []string{a1.ID, got[logRel].ID, got[versionRel].ID}, del)
	if got, want := statuses(res), []Status{StatusDeleted, StatusChanged, StatusChanged}; !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if res.Freed != a1.Bytes {
		t.Errorf("freed %d bytes, want %d", res.Freed, a1.Bytes)
	}
	for _, rel := range []string{logRel, "servers/a/data/world/level.dat", versionRel + "/paper-1.21.3.jar"} {
		if !m.exists(rel) {
			t.Errorf("%s was deleted", rel)
		}
	}
}

func TestCleanCancelled(t *testing.T) {
	m := newMachine(t)
	got := m.candidates(scanOK(t, m.l, m.options()))
	logRel := "servers/a/data/logs/2020-01-01-1.log.gz"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	del := newDeleter(m.l)
	del.hook = func(string) { cancel() }
	res := cleanOK(t, ctx, m.l, m.options(), []string{got["backups/playkeeper-a-1.tar.gz"].ID, got[logRel].ID}, del)
	if got, want := statuses(res), []Status{StatusDeleted, StatusSkipped}; !slices.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	if !m.exists(logRel) {
		t.Errorf("%s was deleted after cleaning was cancelled", logRel)
	}

	_, err := Clean(ctx, m.l, m.options(), []string{got[logRel].ID}, del)
	var e *Error
	if !errors.As(err, &e) || e.Kind != "canceled" {
		t.Errorf("Clean with a cancelled context = %v, want a canceled error", err)
	}
}

func TestCleanBackupNotDeleted(t *testing.T) {
	m := newMachine(t)
	a1 := m.candidates(scanOK(t, m.l, m.options()))["backups/playkeeper-a-1.tar.gz"].ID
	del := newDeleter(m.l)
	del.err = errors.New("Playkeeper is busy; try again when the current task finishes.")
	tests := []struct {
		d    BackupDeleter
		text string
	}{
		{del, "The backup couldn't be deleted: Playkeeper is busy; try again when the current task finishes."},
		{nil, "Backups can't be deleted from here."},
	}
	for _, tt := range tests {
		res := cleanOK(t, context.Background(), m.l, m.options(), []string{a1}, tt.d)
		if out := res.Outcomes[0]; out.Status != StatusFailed || out.Text != tt.text || res.Freed != 0 {
			t.Errorf("outcome %+v, freed %d; want failed with %q", out, res.Freed, tt.text)
		}
	}
	if !m.exists("backups/playkeeper-a-1.tar.gz") {
		t.Error("the backup's archive was deleted")
	}
}

func TestCleanNeverFollowsLinks(t *testing.T) {
	f := newFixture(t)
	f.file("outside/keep.txt", 10, 0)
	f.file("s/data/world/level.dat", 10, 0)
	f.file("s/data.replaced-20200101-000000/world/level.dat", 10, old)
	f.link("s/data.replaced-20200101-000000/escape", f.path("outside"))
	f.link("s/data.replaced-20200101-000000/live", "../data")
	f.file("s/data/versions/1.21.3/paper-1.21.3.jar", 10, old)
	f.link("s/data/versions/1.21.3/world", "../../world")
	f.link("s/data/versions/1.21.3/out", f.path("outside/keep.txt"))
	l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("s/data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
	rep := scanOK(t, l, f.options())
	if len(rep.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the copy and versions/1.21.3", rep.Candidates)
	}
	res := cleanOK(t, context.Background(), l, f.options(), ids(rep.Candidates), nil)
	if got := statuses(res); !slices.Equal(got, []Status{StatusDeleted, StatusDeleted}) {
		t.Fatalf("statuses = %v, want both deleted", got)
	}
	for _, rel := range []string{"outside/keep.txt", "s/data/world/level.dat"} {
		if !f.exists(rel) {
			t.Errorf("%s was deleted through a link", rel)
		}
	}
	for _, rel := range []string{"s/data.replaced-20200101-000000", "s/data/versions/1.21.3"} {
		if f.exists(rel) {
			t.Errorf("%s is still there", rel)
		}
	}
}

func TestCleanChecksItsInput(t *testing.T) {
	f, l := newServer(t)
	valid := strings.Repeat("a", 32)
	tests := []struct {
		name string
		l    Layout
		ids  []string
		kind string
	}{
		{"id not hex", l, []string{"../../etc/passwd"}, "invalid_ids"},
		{"id in capitals", l, []string{strings.Repeat("A", 32)}, "invalid_ids"},
		{"too many ids", l, slices.Repeat([]string{valid}, maxCleanIDs+1), "invalid_ids"},
		{"invalid layout", Layout{Servers: []Server{{ID: "s", DataDir: "data"}}}, []string{valid}, "invalid_layout"},
	}
	for _, tt := range tests {
		_, err := Clean(context.Background(), tt.l, f.options(), tt.ids, nil)
		var e *Error
		if !errors.As(err, &e) || e.Kind != tt.kind {
			t.Errorf("%s: err = %v, want %s", tt.name, err, tt.kind)
		}
	}
	if !f.exists("data/logs/2020-01-01-1.log.gz") {
		t.Error("an invalid request deleted something")
	}
}
