package diskusage

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	day = 24 * time.Hour
	old = 40 * day
)

// fixture builds files under a temporary folder, dated relative to now.
type fixture struct {
	t   *testing.T
	dir string
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	return &fixture{t: t, dir: t.TempDir(), now: time.Now().Truncate(time.Second)}
}

func (f *fixture) path(rel string) string { return filepath.Join(f.dir, filepath.FromSlash(rel)) }

// file writes size bytes to rel, last modified age ago.
func (f *fixture) file(rel string, size int, age time.Duration) string {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.date(rel, age)
	return p
}

func (f *fixture) link(rel, target string) {
	f.t.Helper()
	p := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		f.t.Fatal(err)
	}
}

// date sets when rel was last modified to age ago.
func (f *fixture) date(rel string, age time.Duration) {
	f.t.Helper()
	at := f.now.Add(-age)
	if err := os.Chtimes(f.path(rel), at, at); err != nil {
		f.t.Fatal(err)
	}
}

// dateTree dates rel and everything in it, links aside.
func (f *fixture) dateTree(rel string, age time.Duration) {
	f.t.Helper()
	at := f.now.Add(-age)
	err := filepath.WalkDir(f.path(rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		return os.Chtimes(p, at, at)
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) options() Options {
	return Options{Now: func() time.Time { return f.now }}
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Lstat(f.path(rel))
	return err == nil
}

// du is what the files and folders at paths take, each folder with what is
// in it, without following links and counting hard-linked files once.
func du(t *testing.T, paths ...string) Usage {
	t.Helper()
	var u Usage
	seen := map[fileKey]bool{}
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			st := statOf(fi)
			if st.linked {
				if seen[st.key] {
					st.bytes = 0
				}
				seen[st.key] = true
			}
			u.add(Usage{Bytes: st.bytes, Files: 1})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func scanOK(t *testing.T, l Layout, o Options) *Report {
	t.Helper()
	rep, err := Scan(context.Background(), l, o)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return rep
}

func byKind(ks []KindUsage) map[Kind]Usage {
	m := map[Kind]Usage{}
	for _, k := range ks {
		m[k.Kind] = k.Usage
	}
	return m
}

func (f *fixture) candidates(rep *Report) map[string]Candidate {
	m := map[string]Candidate{}
	for _, c := range rep.Candidates {
		rel, err := filepath.Rel(f.dir, c.Path)
		if err != nil {
			f.t.Fatal(err)
		}
		m[filepath.ToSlash(rel)] = c
	}
	return m
}

// machine is a machine with two servers, laid out as Playkeeper lays them
// out, with something for every rule to offer or leave alone.
type machine struct {
	*fixture
	l      Layout
	recent string // the name of a copy a restore set aside an hour ago
}

func newMachine(t *testing.T) *machine {
	f := newFixture(t)
	a := "servers/a/data/"
	f.file(a+"world/level.dat", 100, 0)
	f.file(a+"world/session.lock", 3, 0)
	f.file(a+"world/region/r.0.0.mca", 8192, 0)
	f.file(a+"world_nether/level.dat", 100, 0)
	f.file(a+"world_nether/DIM-1/region/r.0.0.mca", 4096, 0)
	f.file(a+"logs/latest.log", 500, old)
	f.file(a+"logs/2020-01-01-1.log.gz", 700, old)
	f.file(a+"logs/2020-02-01-1.log.gz", 300, day)
	f.file(a+"crash-reports/crash-2020-01-01_00.00.00-server.txt", 900, old)
	f.file(a+"crash-reports/crash-2020-02-01_00.00.00-server.txt", 900, day)
	f.file(a+"libraries/com/example/lib.jar", 2000, 0)
	f.file(a+"versions/1.21.4/paper-1.21.4.jar", 3000, 0)
	f.file(a+"versions/1.21.3/paper-1.21.3.jar", 3000, old)
	f.file(a+"cache/mojang_1.21.4.jar", 5000, 0)
	f.file(a+"cache/mojang_1.21.3.jar", 5000, old)
	f.file(a+"plugins/Example.jar", 1500, 0)
	f.file(a+"plugins/Example/config.yml", 50, 0)
	f.file(a+"paper-1.21.4-232.jar", 6000, 0)
	f.file(a+"paper-1.21.3-100.jar", 6000, old)
	f.file(a+"purpur-1.21.3-2000.jar", 6000, old)
	f.file(a+"hs_err_pid42.log", 400, old)
	f.file(a+"java_pid42.hprof", 10000, old)
	f.file(a+"server.properties", 120, 0)
	f.file(a+"eula.txt", 10, 0)
	f.file("servers/a/data.replaced-20200101-000000/world/level.dat", 100, old)
	f.file("servers/a/data.replaced-20200101-000000/server.properties", 120, old)
	recent := "data.failed-restore-" + f.now.Add(-time.Hour).UTC().Format("20060102-150405")
	f.file("servers/a/"+recent+"/world/level.dat", 100, time.Hour)
	f.link("servers/a/data.failed-update-20200101-000000", "data")
	f.file("servers/a/notes.txt", 10, 0)

	b := "servers/b/data/"
	f.file(b+"logs/2020-01-01-1.log.gz", 700, old)
	f.file(b+"server.properties", 120, 0)
	f.file("servers/b/data.replaced-20200101-000000/logs/latest.log", 10, old)

	f.file("backups/playkeeper-a-1.tar.gz", 20000, old)
	f.file("backups/playkeeper-a-1.tar.gz.sha256", 80, old)
	f.file("backups/playkeeper-a-2.tar.gz", 20000, day)
	f.file("backups/playkeeper-a-2.tar.gz.sha256", 80, day)
	f.file("backups/playkeeper-b-1.tar.gz", 15000, old)
	f.file("backups/playkeeper-gone-9.tar.gz", 9000, old)
	f.file("backups/playkeeper-unknown-7.tar.gz", 8000, old)
	f.file("backups/.playkeeper-a-3.tar.gz.partial", 7000, 2*time.Hour)
	f.file("backups/.offsite-1234.partial", 7000, 0)
	f.file("backups/notes.txt", 10, 0)

	f.file("staging/abc123/archive.tar.gz", 20000, 0)
	f.file("staging/abc123/stage.json", 100, 0)
	f.file("staging/abc123/data/world/level.dat", 100, 0)
	f.dateTree("staging/abc123", 2*day)
	f.file("staging/def456/archive.tar.gz", 20000, 0)
	f.file("staging/ghi789/archive.tar.gz", 20000, 0)
	f.dateTree("staging/ghi789", 2*day)

	return &machine{fixture: f, recent: recent, l: Layout{
		Servers: []Server{
			{ID: "a", DataDir: f.path("servers/a/data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"},
			{ID: "b", DataDir: f.path("servers/b/data")},
		},
		BackupsDir: f.path("backups"),
		Backups: []Backup{
			{ID: "a1", ServerID: "a", FileName: "playkeeper-a-1.tar.gz", CreatedAt: f.now.Add(-old), Prune: true},
			{ID: "a2", ServerID: "a", FileName: "playkeeper-a-2.tar.gz", CreatedAt: f.now.Add(-day)},
			{ID: "b1", ServerID: "b", FileName: "playkeeper-b-1.tar.gz", CreatedAt: f.now.Add(-old), Prune: true},
			{ID: "g9", ServerID: "gone", FileName: "playkeeper-gone-9.tar.gz", CreatedAt: f.now.Add(-old), Prune: true},
		},
		StagingDir:       f.path("staging"),
		ActiveStages:     []string{"ghi789"},
		DockerImageBytes: 500 << 20,
	}}
}

// offered is what the machine's rules offer, by path.
var offered = map[string]Reason{
	"servers/a/data/logs/2020-01-01-1.log.gz":                           ReasonOldLog,
	"servers/a/data/crash-reports/crash-2020-01-01_00.00.00-server.txt": ReasonOldCrashReport,
	"servers/a/data/hs_err_pid42.log":                                   ReasonJavaErrorLog,
	"servers/a/data/java_pid42.hprof":                                   ReasonMemoryDump,
	"servers/a/data/paper-1.21.3-100.jar":                               ReasonUnusedSoftware,
	"servers/a/data/versions/1.21.3":                                    ReasonUnusedSoftware,
	"servers/a/data/cache/mojang_1.21.3.jar":                            ReasonUnusedSoftware,
	"servers/a/data.replaced-20200101-000000":                           ReasonLeftoverCopy,
	"servers/b/data/logs/2020-01-01-1.log.gz":                           ReasonOldLog,
	"backups/.playkeeper-a-3.tar.gz.partial":                            ReasonPartialFile,
	"backups/playkeeper-a-1.tar.gz":                                     ReasonPrunedBackup,
	"backups/playkeeper-b-1.tar.gz":                                     ReasonPrunedBackup,
	"staging/abc123":                                                    ReasonStaleStage,
}

func TestScanBreakdown(t *testing.T) {
	m := newMachine(t)
	rep := scanOK(t, m.l, m.options())
	p := m.path
	a, b := p("servers/a/data")+"/", p("servers/b/data")+"/"
	want := map[string]map[Kind]Usage{
		"a": {
			KindWorld:        du(t, a+"world", a+"world_nether"),
			KindLogs:         du(t, a+"logs"),
			KindCrashReports: du(t, a+"crash-reports", a+"hs_err_pid42.log", a+"java_pid42.hprof"),
			KindSoftware:     du(t, a+"libraries", a+"versions", a+"paper-1.21.4-232.jar", a+"paper-1.21.3-100.jar", a+"purpur-1.21.3-2000.jar"),
			KindCaches:       du(t, a+"cache"),
			KindAddons:       du(t, a+"plugins"),
			KindOther:        du(t, a+"server.properties", a+"eula.txt"),
			KindLeftovers:    du(t, p("servers/a/data.replaced-20200101-000000"), p("servers/a/"+m.recent), p("servers/a/data.failed-update-20200101-000000")),
			KindBackups:      du(t, p("backups/playkeeper-a-1.tar.gz"), p("backups/playkeeper-a-1.tar.gz.sha256"), p("backups/playkeeper-a-2.tar.gz"), p("backups/playkeeper-a-2.tar.gz.sha256")),
		},
		"b": {
			KindLogs:      du(t, b+"logs"),
			KindOther:     du(t, b+"server.properties"),
			KindLeftovers: du(t, p("servers/b/data.replaced-20200101-000000")),
			KindBackups:   du(t, p("backups/playkeeper-b-1.tar.gz")),
		},
	}
	wantMachine := map[Kind]Usage{
		KindBackups:     du(t, p("backups/playkeeper-gone-9.tar.gz"), p("backups/playkeeper-unknown-7.tar.gz")),
		KindLeftovers:   du(t, p("backups/.playkeeper-a-3.tar.gz.partial"), p("backups/.offsite-1234.partial")),
		KindOther:       du(t, p("backups/notes.txt")),
		KindStaging:     du(t, p("staging/abc123"), p("staging/def456"), p("staging/ghi789")),
		KindDockerImage: {Bytes: 500 << 20},
	}

	if len(rep.Servers) != 2 || rep.Servers[0].ID != "a" || rep.Servers[1].ID != "b" {
		t.Fatalf("servers = %+v, want a then b", rep.Servers)
	}
	var total Usage
	for _, sv := range rep.Servers {
		if got := byKind(sv.Kinds); !mapsEqual(got, want[sv.ID]) {
			t.Errorf("server %s kinds:\n got %v\nwant %v", sv.ID, got, want[sv.ID])
		}
		var sum Usage
		for _, u := range want[sv.ID] {
			sum.add(u)
		}
		if sv.Total != sum {
			t.Errorf("server %s total = %+v, want %+v", sv.ID, sv.Total, sum)
		}
		if !slices.IsSortedFunc(sv.Kinds, func(x, y KindUsage) int { return cmp.Compare(y.Bytes, x.Bytes) }) {
			t.Errorf("server %s kinds not largest first: %+v", sv.ID, sv.Kinds)
		}
		total.add(sum)
	}
	if got := byKind(rep.Machine); !mapsEqual(got, wantMachine) {
		t.Errorf("machine kinds:\n got %v\nwant %v", got, wantMachine)
	}
	for _, u := range wantMachine {
		total.add(u)
	}
	if rep.Total != total {
		t.Errorf("total = %+v, want %+v", rep.Total, total)
	}
	if rep.Truncated || len(rep.Problems) != 0 {
		t.Errorf("truncated = %v, problems = %+v; want a complete scan", rep.Truncated, rep.Problems)
	}
	if !rep.ScannedAt.Equal(m.now) {
		t.Errorf("scannedAt = %v, want %v", rep.ScannedAt, m.now)
	}
}

func mapsEqual(a, b map[Kind]Usage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestCandidates(t *testing.T) {
	m := newMachine(t)
	rep := scanOK(t, m.l, m.options())
	got := m.candidates(rep)
	for rel, reason := range offered {
		c, ok := got[rel]
		if !ok {
			t.Errorf("%s not offered, want %s", rel, reason)
			continue
		}
		if c.Reason != reason {
			t.Errorf("%s offered as %s, want %s", rel, c.Reason, reason)
		}
		wantRisk := RiskLow
		if reason == ReasonLeftoverCopy || reason == ReasonPrunedBackup {
			wantRisk = RiskMedium
		}
		if c.Risk != wantRisk {
			t.Errorf("%s risk = %s, want %s", rel, c.Risk, wantRisk)
		}
		wantUsage := du(t, c.Path)
		if _, err := os.Lstat(c.Path + ".sha256"); err == nil && reason == ReasonPrunedBackup {
			wantUsage = du(t, c.Path, c.Path+".sha256")
		}
		if c.Usage != wantUsage {
			t.Errorf("%s usage = %+v, want %+v", rel, c.Usage, wantUsage)
		}
		if !validID(c.ID) || c.Text == "" || !strings.HasSuffix(c.Text, ".") {
			t.Errorf("%s: id %q, text %q", rel, c.ID, c.Text)
		}
		switch {
		case strings.HasPrefix(rel, "servers/a/"):
			if c.ServerID != "a" {
				t.Errorf("%s server = %q, want a", rel, c.ServerID)
			}
		case strings.HasPrefix(rel, "servers/b/"):
			if c.ServerID != "b" {
				t.Errorf("%s server = %q, want b", rel, c.ServerID)
			}
		case reason == ReasonPrunedBackup:
			if c.BackupID == "" || c.ServerID == "" {
				t.Errorf("%s: backup %q of server %q", rel, c.BackupID, c.ServerID)
			}
		default:
			if c.ServerID != "" {
				t.Errorf("%s server = %q, want none", rel, c.ServerID)
			}
		}
	}
	for rel, c := range got {
		if _, ok := offered[rel]; !ok {
			t.Errorf("%s offered as %s, want it left alone", rel, c.Reason)
		}
	}
	for i := 1; i < len(rep.Candidates); i++ {
		x, y := rep.Candidates[i-1], rep.Candidates[i]
		if x.Risk.rank() > y.Risk.rank() || x.Risk == y.Risk && x.Bytes < y.Bytes {
			t.Errorf("candidate %s (%s, %d) comes before %s (%s, %d)", x.Path, x.Risk, x.Bytes, y.Path, y.Risk, y.Bytes)
		}
	}
}

func TestCandidateTexts(t *testing.T) {
	m := newMachine(t)
	got := m.candidates(scanOK(t, m.l, m.options()))
	logDay := m.now.Add(-old).UTC()
	stageDay := m.now.Add(-2 * day).UTC()
	tests := []struct {
		rel    string
		params map[string]string
		text   string
	}{
		{
			"servers/a/data/logs/2020-01-01-1.log.gz",
			map[string]string{"file": "2020-01-01-1.log.gz", "date": logDay.Format("2006-01-02")},
			"A server log last written on " + logDay.Format("Monday 2 January 2006") + ". Old logs only help to look into past problems.",
		},
		{
			"servers/a/data/versions/1.21.3",
			map[string]string{"path": "versions/1.21.3", "version": "1.21.3"},
			"Minecraft 1.21.3 server software this server no longer uses (versions/1.21.3). It is downloaded again if it's ever needed.",
		},
		{
			"servers/a/data/paper-1.21.3-100.jar",
			map[string]string{"path": "paper-1.21.3-100.jar"},
			"Server software this server no longer uses (paper-1.21.3-100.jar). It is downloaded again if it's ever needed.",
		},
		{
			"servers/a/data.replaced-20200101-000000",
			map[string]string{"why": "replaced", "date": "2020-01-01"},
			"The server's files from before a restore on Wednesday 1 January 2020. Playkeeper normally deletes this copy once the restore has finished.",
		},
		{
			"backups/playkeeper-a-1.tar.gz",
			map[string]string{"backupId": "a1", "createdAt": logDay.Format(time.RFC3339)},
			"The backup from " + logDay.Format("Monday 2 January 2006") + " at " + logDay.Format("15:04") + ", which the backup rules would delete.",
		},
		{
			"staging/abc123",
			map[string]string{"stage": "abc123", "date": stageDay.Format("2006-01-02")},
			"A backup unpacked on " + stageDay.Format("Monday 2 January 2006") + " for a restore that nobody applied or cancelled.",
		},
	}
	for _, tt := range tests {
		c := got[tt.rel]
		if c.Text != tt.text {
			t.Errorf("%s text:\n got %q\nwant %q", tt.rel, c.Text, tt.text)
		}
		if fmt.Sprint(c.Params) != fmt.Sprint(tt.params) {
			t.Errorf("%s params = %v, want %v", tt.rel, c.Params, tt.params)
		}
	}

	b, err := json.Marshal(got["backups/playkeeper-a-1.tar.gz"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "serverId", "kind", "reason", "risk", "path", "backupId", "bytes", "files", "modifiedAt", "params", "text"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("candidate JSON has no %q: %s", k, b)
		}
	}
	if len(fields) != 12 {
		t.Errorf("candidate JSON has other fields: %s", b)
	}
}

func TestCandidateIDs(t *testing.T) {
	m := newMachine(t)
	first := m.candidates(scanOK(t, m.l, m.options()))
	m.date("servers/a/data/logs/2020-01-01-1.log.gz", old+day)
	second := m.candidates(scanOK(t, m.l, m.options()))
	for rel, c := range first {
		changed := rel == "servers/a/data/logs/2020-01-01-1.log.gz"
		if same := second[rel].ID == c.ID; same == changed {
			t.Errorf("%s: id %s then %s; want it to change only when the file changes", rel, c.ID, second[rel].ID)
		}
	}
}

func TestWorldNeverOffered(t *testing.T) {
	f := newFixture(t)
	f.file("data/logs/level.dat", 10, old)
	f.file("data/logs/2020-01-01-1.log.gz", 10, old)
	f.file("data/crash-reports/session.lock", 10, old)
	f.file("data/crash-reports/crash-old.txt", 10, old)
	f.file("data/versions/1.20.1/level.dat_old", 10, old)
	f.file("data/versions/1.20.1/region/r.0.0.mca", 10, old)
	f.file("data/cache/world/level.dat", 10, old)
	f.file("data/cache/mojang_1.20.1.jar", 10, old)
	f.file("data/hs_err_pid1.log/level.dat", 10, old)
	l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
	rep := scanOK(t, l, f.options())
	got := f.candidates(rep)
	if len(got) != 1 || got["data/cache/mojang_1.20.1.jar"].Reason != ReasonUnusedSoftware {
		t.Errorf("candidates = %v, want only cache/mojang_1.20.1.jar", slices.Sorted(maps.Keys(got)))
	}
	d := f.path("data") + "/"
	want := du(t, d+"logs", d+"crash-reports", d+"versions/1.20.1", d+"cache/world", d+"hs_err_pid1.log")
	if w := byKind(rep.Servers[0].Kinds)[KindWorld]; w != want {
		t.Errorf("world usage = %+v, want %+v", w, want)
	}

	g := newFixture(t)
	g.file("data/level.dat", 10, old)
	g.file("data/logs/2020-01-01-1.log.gz", 10, old)
	g.file("data/paper-1.20.1-1.jar", 10, old)
	l = Layout{Servers: []Server{{ID: "s", DataDir: g.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
	rep = scanOK(t, l, g.options())
	if len(rep.Candidates) != 0 {
		t.Errorf("a data folder that is itself a world: candidates %+v, want none", rep.Candidates)
	}
	if ks := rep.Servers[0].Kinds; len(ks) != 1 || ks[0].Kind != KindWorld {
		t.Errorf("a data folder that is itself a world: kinds %+v, want only world", ks)
	}
}

func TestBusyServer(t *testing.T) {
	m := newMachine(t)
	idle := scanOK(t, m.l, m.options())
	m.l.Servers[0].Busy = true
	busy := scanOK(t, m.l, m.options())
	for _, c := range busy.Candidates {
		if c.ServerID == "a" || c.Reason == ReasonPartialFile || c.Reason == ReasonStaleStage {
			t.Errorf("offered %s (%s) while server a is busy", c.Path, c.Reason)
		}
	}
	got := m.candidates(busy)
	for _, rel := range []string{"servers/b/data/logs/2020-01-01-1.log.gz", "backups/playkeeper-b-1.tar.gz"} {
		if _, ok := got[rel]; !ok {
			t.Errorf("%s of idle server b not offered while a is busy", rel)
		}
	}
	if !mapsEqual(byKind(idle.Servers[0].Kinds), byKind(busy.Servers[0].Kinds)) {
		t.Errorf("a busy server's breakdown changed:\n idle %v\n busy %v", idle.Servers[0].Kinds, busy.Servers[0].Kinds)
	}
}

func TestLeftoversNeedAWorld(t *testing.T) {
	m := newMachine(t)
	for _, rel := range []string{"servers/a/data/world", "servers/a/data/world_nether"} {
		if err := os.RemoveAll(m.path(rel)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range scanOK(t, m.l, m.options()).Candidates {
		if c.Reason == ReasonLeftoverCopy {
			t.Errorf("offered %s while server a's folder has no world", c.Path)
		}
	}
}

func TestScanNeverFollowsLinks(t *testing.T) {
	f := newFixture(t)
	f.file("outside/huge.bin", 1<<20, old)
	f.file("outside/2020-01-01-1.log.gz", 10, old)
	f.file("data/world/level.dat", 100, 0)
	f.file("data/plugins/big.bin", 50000, 0)
	if err := os.Link(f.path("data/plugins/big.bin"), f.path("data/plugins/big-copy.bin")); err != nil {
		t.Fatal(err)
	}
	f.link("data/plugins/loop", "..")
	f.link("data/plugins/escape", f.path("outside"))
	f.link("data/logs/huge.log.gz", f.path("outside/huge.bin"))
	f.link("data/logs/world.log.gz", "../world/level.dat")
	f.link("data/crash-reports", f.path("outside"))
	f.link("data/paper-1.21.3-100.jar", "world/level.dat")
	f.link("data/versions/1.21.3", "../world")
	f.link("data/cache/mojang_1.21.3.jar", f.path("outside/huge.bin"))
	l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
	rep := scanOK(t, l, f.options())
	if len(rep.Candidates) != 0 {
		t.Errorf("offered links: %+v", rep.Candidates)
	}
	ks := byKind(rep.Servers[0].Kinds)
	if want := du(t, f.path("data/plugins")); ks[KindAddons] != want {
		t.Errorf("add-ons = %+v, want %+v (the hard link once, links not followed)", ks[KindAddons], want)
	}
	entries, err := os.ReadDir(f.path("data"))
	if err != nil {
		t.Fatal(err)
	}
	var inside []string
	for _, e := range entries {
		inside = append(inside, f.path("data/"+e.Name()))
	}
	if want := du(t, inside...); rep.Total != want {
		t.Errorf("total = %+v, want %+v", rep.Total, want)
	}
	if rep.Total.Bytes >= 1<<20 {
		t.Errorf("total = %d bytes: a link out of the folder was followed", rep.Total.Bytes)
	}
	if len(rep.Problems) != 0 {
		t.Errorf("problems = %+v", rep.Problems)
	}
}

func TestScanCaps(t *testing.T) {
	t.Run("entries", func(t *testing.T) {
		f := newFixture(t)
		f.file("data/world/level.dat", 10, 0)
		for i := range 30 {
			f.file(fmt.Sprintf("data/plugins/f%02d", i), 10, 0)
		}
		l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data")}}}
		o := f.options()
		o.MaxEntries = 10
		rep := scanOK(t, l, o)
		if got := rep.Servers[0].Total.Files; got != 10 {
			t.Errorf("counted %d files and folders, want the cap of 10", got)
		}
		if !rep.Truncated || len(rep.Problems) != 1 || rep.Problems[0].Code != "too_many_files" || rep.Problems[0].Path != f.path("data") {
			t.Errorf("truncated = %v, problems = %+v; want one too_many_files for the data folder", rep.Truncated, rep.Problems)
		}
	})
	t.Run("entries next to the data folder", func(t *testing.T) {
		f := newFixture(t)
		f.file("s/data/world/level.dat", 10, 0)
		for i := range 30 {
			f.file(fmt.Sprintf("s/data.replaced-20200101-000000/f%02d", i), 10, 0)
		}
		l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("s/data")}}}
		o := f.options()
		o.MaxEntries = 10
		if rep := scanOK(t, l, o); len(rep.Candidates) != 0 || !rep.Truncated {
			t.Errorf("candidates = %+v, truncated = %v; want a copy not fully counted left alone", rep.Candidates, rep.Truncated)
		}
		o.MaxEntries = 100
		if rep := scanOK(t, l, o); len(rep.Candidates) != 1 || rep.Truncated {
			t.Errorf("candidates = %+v, truncated = %v; want the copy offered under a higher cap", rep.Candidates, rep.Truncated)
		}
	})
	t.Run("depth", func(t *testing.T) {
		f := newFixture(t)
		f.file("data/a/b/c/d/deep.txt", 10, 0)
		f.file("data/versions/1.21.3/x/y/z.jar", 10, old)
		l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}}}
		o := f.options()
		o.MaxDepth = 3
		rep := scanOK(t, l, o)
		if got := rep.Servers[0].Total.Files; got != 6 {
			t.Errorf("counted %d files and folders, want 6 (a, b, c, versions, 1.21.3, x)", got)
		}
		var deep []string
		for _, p := range rep.Problems {
			if p.Code == "too_deep" {
				deep = append(deep, p.Path)
			}
		}
		slices.Sort(deep)
		if want := []string{f.path("data/a/b/c"), f.path("data/versions/1.21.3/x")}; !rep.Truncated || !slices.Equal(deep, want) {
			t.Errorf("truncated = %v, too deep = %v; want %v", rep.Truncated, deep, want)
		}
		if len(rep.Candidates) != 0 {
			t.Errorf("offered %+v, which wasn't fully counted", rep.Candidates)
		}
	})
}

func TestScanProblems(t *testing.T) {
	f := newFixture(t)
	f.file("data/world/level.dat", 10, 0)
	f.file("data/plugins/secret/x", 10, 0)
	f.file("data/versions/1.21.3/paper-1.21.3.jar", 10, old)
	for _, rel := range []string{"data/plugins/secret", "data/versions/1.21.3"} {
		if err := os.Chmod(f.path(rel), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(f.path(rel), 0o755) })
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads folders without permission")
	}
	l := Layout{
		Servers:    []Server{{ID: "s", DataDir: f.path("data"), Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}},
		BackupsDir: f.path("no-backups"),
	}
	rep := scanOK(t, l, f.options())
	var codes []string
	for _, p := range rep.Problems {
		codes = append(codes, p.Code+" "+p.Path)
		if !strings.HasSuffix(p.Text, ".") || !strings.Contains(p.Text, p.Path) {
			t.Errorf("problem text %q doesn't name %s in a sentence", p.Text, p.Path)
		}
	}
	slices.Sort(codes)
	want := []string{"missing " + f.path("no-backups"), "unreadable " + f.path("data/plugins/secret"), "unreadable " + f.path("data/versions/1.21.3")}
	if !slices.Equal(codes, want) {
		t.Errorf("problems = %v, want %v", codes, want)
	}
	if len(rep.Candidates) != 0 {
		t.Errorf("offered %+v, which couldn't be read", rep.Candidates)
	}
}

// cancelAfter is a context cancelled after n checks.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n <= 0 {
		return context.Canceled
	}
	c.n--
	return nil
}

func TestScanCancelled(t *testing.T) {
	m := newMachine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, ctx := range map[string]context.Context{"before": ctx, "during": &cancelAfter{Context: context.Background(), n: 20}} {
		rep, err := Scan(ctx, m.l, m.options())
		var e *Error
		if !errors.As(err, &e) || e.Kind != "canceled" || !errors.Is(err, context.Canceled) || rep != nil {
			t.Errorf("%s: Scan = %v, %v; want a canceled error", name, rep, err)
		}
	}
}

func TestLayoutChecks(t *testing.T) {
	dir := t.TempDir()
	good := func() Layout {
		return Layout{
			Servers:    []Server{{ID: "a", DataDir: dir + "/a/data", Jar: "paper-1.21.4-232.jar", MinecraftVersion: "1.21.4"}},
			BackupsDir: dir + "/backups",
			Backups:    []Backup{{ID: "b1", ServerID: "a", FileName: "playkeeper-a-1.tar.gz"}},
			StagingDir: dir + "/staging",
		}
	}
	tests := []struct {
		name  string
		edit  func(l *Layout)
		field string
	}{
		{"no server id", func(l *Layout) { l.Servers[0].ID = "" }, "servers"},
		{"server id with a space", func(l *Layout) { l.Servers[0].ID = "a b" }, "servers"},
		{"two servers with one id", func(l *Layout) { l.Servers = append(l.Servers, Server{ID: "a", DataDir: dir + "/b"}) }, "servers"},
		{"relative data folder", func(l *Layout) { l.Servers[0].DataDir = "a/data" }, "dataDir"},
		{"data folder not clean", func(l *Layout) { l.Servers[0].DataDir = dir + "/a/../data" }, "dataDir"},
		{"data folder is the filesystem root", func(l *Layout) { l.Servers[0].DataDir = "/" }, "dataDir"},
		{"jar in a folder", func(l *Layout) { l.Servers[0].Jar = "../paper.jar" }, "jar"},
		{"jar not a jar", func(l *Layout) { l.Servers[0].Jar = "paper.sh" }, "jar"},
		{"odd version", func(l *Layout) { l.Servers[0].MinecraftVersion = "1.21/../x" }, "minecraftVersion"},
		{"relative backups folder", func(l *Layout) { l.BackupsDir = "backups" }, "backupsDir"},
		{"staging folder is the filesystem root", func(l *Layout) { l.StagingDir = "/" }, "stagingDir"},
		{"data folder in the backups folder", func(l *Layout) { l.Servers[0].DataDir = dir + "/backups/a" }, "layout"},
		{"two servers in one folder", func(l *Layout) { l.Servers = append(l.Servers, Server{ID: "b", DataDir: dir + "/a/data"}) }, "layout"},
		{"backup file in a folder", func(l *Layout) { l.Backups[0].FileName = "../x.tar.gz" }, "backups"},
		{"backup file named ..", func(l *Layout) { l.Backups[0].FileName = ".." }, "backups"},
		{"no backup id", func(l *Layout) { l.Backups[0].ID = "" }, "backups"},
		{"two backups in one file", func(l *Layout) { l.Backups = append(l.Backups, Backup{ID: "b2", FileName: "playkeeper-a-1.tar.gz"}) }, "backups"},
		{"negative image size", func(l *Layout) { l.DockerImageBytes = -1 }, "dockerImageBytes"},
	}
	for _, tt := range tests {
		l := good()
		tt.edit(&l)
		_, err := Scan(context.Background(), l, Options{})
		var e *Error
		if !errors.As(err, &e) || e.Kind != "invalid_layout" || e.Field != tt.field || !strings.HasSuffix(e.Msg, ".") {
			t.Errorf("%s: err = %#v, want invalid_layout for %s", tt.name, err, tt.field)
		}
	}
	if _, err := Scan(context.Background(), good(), Options{}); err != nil {
		t.Errorf("a good layout: %v", err)
	}
}
