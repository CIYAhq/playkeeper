package diskusage

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// pageGroup is where the Disk space design shows each kind: for a server,
// and for the machine's shared folders.
var pageGroup = map[Kind][2]Group{
	KindWorld:        {GroupWorlds, GroupWorlds},
	KindBackups:      {GroupBackups, GroupBackups},
	KindLeftovers:    {GroupBackups, GroupBackups},
	KindStaging:      {GroupBackups, GroupBackups},
	KindLogs:         {GroupLogs, GroupLogs},
	KindCrashReports: {GroupLogs, GroupLogs},
	KindSoftware:     {GroupServerFiles, GroupServerFiles},
	KindAddons:       {GroupServerFiles, GroupServerFiles},
	KindCaches:       {GroupServerFiles, GroupServerFiles},
	KindDownloads:    {GroupServerFiles, GroupServerFiles},
	KindDockerImage:  {GroupServerFiles, GroupServerFiles},
	KindOther:        {GroupServerFiles, GroupOther},
}

// wantBar is the bar of a disk of total bytes with free left, holding what
// the report counted.
func wantBar(t *testing.T, rep *Report, total, free int64) []GroupUsage {
	t.Helper()
	sums := map[Group]int64{}
	add := func(ks []KindUsage, i int) {
		for _, k := range ks {
			g, ok := pageGroup[k.Kind]
			if !ok {
				t.Fatalf("no group for kind %s", k.Kind)
			}
			sums[g[i]] += k.Bytes
		}
	}
	for _, sv := range rep.Servers {
		add(sv.Kinds, 0)
	}
	add(rep.Machine, 1)
	counted := sums[GroupBackups] + sums[GroupWorlds] + sums[GroupServerFiles] + sums[GroupLogs]
	return []GroupUsage{
		{GroupBackups, sums[GroupBackups]},
		{GroupWorlds, sums[GroupWorlds]},
		{GroupServerFiles, sums[GroupServerFiles]},
		{GroupLogs, sums[GroupLogs]},
		{GroupOther, total - free - counted},
		{GroupFree, free},
	}
}

func TestDiskBar(t *testing.T) {
	m := newMachine(t)
	var asked []string
	o := m.options()
	o.DiskSpace = func(dir string) (int64, int64, error) {
		asked = append(asked, dir)
		return 41e9, 80e9, nil
	}
	rep := scanOK(t, m.l, o)
	d := rep.Disk
	if d == nil {
		t.Fatalf("no disk measured; problems %+v", rep.Problems)
	}
	if data := m.path("servers/a/data"); d.Dir != data || !slices.Equal(asked, []string{data}) {
		t.Errorf("measured %s after asking for %v, want the first server's data folder %s", d.Dir, asked, data)
	}
	if d.Total != 80e9 || d.Free != 41e9 || d.Used != 39e9 {
		t.Errorf("disk of %d bytes, %d free and %d used; want 80 GB, 41 GB free and 39 GB used", d.Total, d.Free, d.Used)
	}
	if want := wantBar(t, rep, 80e9, 41e9); !slices.Equal(d.Bar, want) {
		t.Errorf("bar:\n got %v\nwant %v", d.Bar, want)
	}
	var sum int64
	for _, g := range d.Bar {
		sum += g.Bytes
	}
	if sum != d.Total {
		t.Errorf("the bar adds up to %d bytes, want the disk's %d", sum, d.Total)
	}
	p := m.path
	worlds := du(t, p("servers/a/data/world"), p("servers/a/data/world_nether"))
	logs := du(t, p("servers/a/data/logs"), p("servers/a/data/crash-reports"), p("servers/a/data/hs_err_pid42.log"),
		p("servers/a/data/java_pid42.hprof"), p("servers/b/data/logs"))
	if d.Bar[1].Bytes != worlds.Bytes || d.Bar[3].Bytes != logs.Bytes {
		t.Errorf("worlds %d and logs %d bytes, want %d and %d", d.Bar[1].Bytes, d.Bar[3].Bytes, worlds.Bytes, logs.Bytes)
	}

	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Disk map[string]json.RawMessage `json:"disk"`
	}
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(fields.Disk)); !slices.Equal(got, []string{"bar", "dir", "free", "total", "used"}) {
		t.Errorf("disk JSON has %v", got)
	}
	var bar []map[string]any
	if err := json.Unmarshal(fields.Disk["bar"], &bar); err != nil || len(bar) != 6 || bar[0]["group"] != "backups" || bar[5]["group"] != "free" || bar[5]["bytes"] != 41e9 {
		t.Errorf("bar JSON %s (%v)", fields.Disk["bar"], err)
	}

	t.Run("a disk the folders aren't on", func(t *testing.T) {
		l := m.l
		l.DiskDir = otherDevice(t, m.dir)
		other := scanOK(t, l, m.options())
		want := []GroupUsage{{GroupBackups, 0}, {GroupWorlds, 0}, {GroupServerFiles, 500 << 20}, {GroupLogs, 0}, {GroupOther, 39e9 - 500<<20}, {GroupFree, 41e9}}
		if other.Disk == nil || other.Disk.Dir != l.DiskDir || !slices.Equal(other.Disk.Bar, want) {
			t.Errorf("disk %+v; want only the Docker image counted in its bar: %v", other.Disk, want)
		}
		for i, sv := range other.Servers {
			if sv.Total != rep.Servers[i].Total || !slices.Equal(sv.Groups, rep.Servers[i].Groups) {
				t.Errorf("server %s: %+v in %v, want %+v in %v as before", sv.ID, sv.Total, sv.Groups, rep.Servers[i].Total, rep.Servers[i].Groups)
			}
		}
	})
}

// otherDevice is a folder on another device than dir, or skips the test.
func otherDevice(t *testing.T, dir string) string {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	here := statOf(fi)
	if !here.hasDev {
		t.Skip("which device a folder is on isn't known here")
	}
	for _, p := range []string{"/proc", "/sys", "/dev", "/dev/shm", "/run"} {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			if st := statOf(fi); st.hasDev && st.key.dev != here.key.dev {
				return p
			}
		}
	}
	t.Skip("no folder on another device to measure")
	return ""
}

func TestDiskMeasured(t *testing.T) {
	f, l := newServer(t)
	data := f.path("data")
	tests := []struct {
		name        string
		free, total int64
		err         error
		disk        *Disk // nil when it can't be measured
		problem     string
	}{
		{"unreadable", 0, 0, errors.New("input/output error"), nil,
			"Playkeeper couldn't read the size of the disk that holds " + data + " (input/output error)."},
		{"no size", 5, 0, nil, nil, "The disk that holds " + data + " reported no size, so how full it is isn't known."},
		{"more free than it holds", 90, 80, nil, &Disk{Dir: data, Total: 80, Free: 80}, ""},
		{"less than nothing free", -5, 80e9, nil, &Disk{Dir: data, Total: 80e9, Used: 80e9}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := f.options()
			o.DiskSpace = func(string) (int64, int64, error) { return tt.free, tt.total, tt.err }
			rep := scanOK(t, l, o)
			var problems []string
			for _, p := range rep.Problems {
				problems = append(problems, p.Code+" "+p.Path+" "+p.Text)
			}
			if tt.disk == nil {
				if want := []string{"disk_space " + data + " " + tt.problem}; rep.Disk != nil || !slices.Equal(problems, want) {
					t.Errorf("disk %+v, problems %q; want no disk and %q", rep.Disk, problems, want)
				}
				return
			}
			d := rep.Disk
			if len(problems) != 0 || d == nil || d.Dir != tt.disk.Dir || d.Total != tt.disk.Total || d.Free != tt.disk.Free || d.Used != tt.disk.Used {
				t.Fatalf("disk %+v, problems %q; want %+v", d, problems, tt.disk)
			}
			if other := d.Bar[4]; other.Group != GroupOther || other.Bytes < 0 {
				t.Errorf("other = %+v, want 0 or more", other)
			}
		})
	}
}

func TestDiskDir(t *testing.T) {
	f := newFixture(t)
	for _, rel := range []string{"data", "backups", "staging", "downloads", "disk"} {
		f.file(rel+"/x", 1, 0)
	}
	servers := []Server{{ID: "s", DataDir: f.path("data")}}
	tests := []struct {
		name string
		l    Layout
		want string // "" when there is nothing to measure
	}{
		{"the folder given", Layout{Servers: servers, BackupsDir: f.path("backups"), DiskDir: f.path("disk")}, f.path("disk")},
		{"the first server's data folder", Layout{Servers: servers, BackupsDir: f.path("backups")}, f.path("data")},
		{"the backups folder", Layout{BackupsDir: f.path("backups"), StagingDir: f.path("staging")}, f.path("backups")},
		{"the staging folder", Layout{StagingDir: f.path("staging"), DownloadsDir: f.path("downloads")}, f.path("staging")},
		{"the downloads folder", Layout{DownloadsDir: f.path("downloads")}, f.path("downloads")},
		{"nothing to measure", Layout{}, ""},
	}
	for _, tt := range tests {
		var asked []string
		o := f.options()
		o.DiskSpace = func(dir string) (int64, int64, error) {
			asked = append(asked, dir)
			return 41e9, 80e9, nil
		}
		rep := scanOK(t, tt.l, o)
		switch {
		case tt.want == "" && (len(asked) != 0 || rep.Disk != nil || len(rep.Problems) != 0):
			t.Errorf("%s: asked for %v, disk %+v, problems %+v; want nothing measured", tt.name, asked, rep.Disk, rep.Problems)
		case tt.want != "" && (!slices.Equal(asked, []string{tt.want}) || rep.Disk == nil || rep.Disk.Dir != tt.want):
			t.Errorf("%s: asked for %v, disk %+v; want %s measured", tt.name, asked, rep.Disk, tt.want)
		}
	}
}

func TestDiskSpace(t *testing.T) {
	f, l := newServer(t)
	free, total, err := diskSpace(f.path("data"))
	if errors.Is(err, errors.ErrUnsupported) {
		t.Skip("disk sizes aren't read on this system")
	}
	if err != nil || total <= 0 || free < 0 || free > total {
		t.Fatalf("diskSpace = %d, %d, %v; want the disk's size and what is free of it", free, total, err)
	}
	if _, _, err := diskSpace(f.path("missing")); err == nil {
		t.Error("diskSpace of a missing folder: no error")
	}
	rep := scanOK(t, l, Options{Now: func() time.Time { return f.now }})
	if d := rep.Disk; d == nil || d.Total != total || d.Free < 0 || d.Free > d.Total || d.Used != d.Total-d.Free {
		t.Errorf("disk %+v (problems %+v); want %d bytes as the system reports", d, rep.Problems, total)
	}
}

func TestServerGroups(t *testing.T) {
	m := newMachine(t)
	rep := scanOK(t, m.l, m.options())
	for _, sv := range rep.Servers {
		sums := map[Group]int64{}
		for _, k := range sv.Kinds {
			sums[pageGroup[k.Kind][0]] += k.Bytes
		}
		want := []GroupUsage{{GroupBackups, sums[GroupBackups]}, {GroupWorlds, sums[GroupWorlds]}, {GroupServerFiles, sums[GroupServerFiles]}, {GroupLogs, sums[GroupLogs]}}
		if !slices.Equal(sv.Groups, want) {
			t.Errorf("server %s groups:\n got %v\nwant %v", sv.ID, sv.Groups, want)
		}
		var sum int64
		for _, g := range sv.Groups {
			sum += g.Bytes
		}
		if sum != sv.Total.Bytes {
			t.Errorf("server %s groups add up to %d bytes, want its total %d", sv.ID, sum, sv.Total.Bytes)
		}
	}
	p := m.path
	want := []GroupUsage{
		{GroupBackups, du(t, p("backups/playkeeper-b-1.tar.gz"), p("servers/b/data.replaced-20200101-000000")).Bytes},
		{GroupWorlds, 0},
		{GroupServerFiles, du(t, p("servers/b/data/server.properties")).Bytes},
		{GroupLogs, du(t, p("servers/b/data/logs")).Bytes},
	}
	if got := rep.Servers[1].Groups; !slices.Equal(got, want) {
		t.Errorf("server b groups:\n got %v\nwant %v", got, want)
	}

	f, l := newServer(t)
	if got := scanOK(t, l, f.options()).Servers[0].Name; got != "s" {
		t.Errorf("a server without a name is called %q, want its id s", got)
	}
}

func TestSpoolFiles(t *testing.T) {
	names := map[string]bool{
		".offsite-0123456789abcdef.age":     true,
		".offsite-0123456789abcdef.partial": true,
		".offsite-0123456789ABCDEF.age":     false,
		".offsite-0123456789abcde.age":      false,
		".offsite-0123456789abcdef0.age":    false,
		".offsite-0123456789abcdeg.age":     false,
		".offsite-0123456789abcdef.age.tmp": false,
		"offsite-0123456789abcdef.age":      false,
		".offsite-.age":                     false,
		"playkeeper-a-1.tar.gz.age":         false,
	}
	for name, want := range names {
		if got := isSpoolFile(name); got != want {
			t.Errorf("isSpoolFile(%q) = %v, want %v", name, got, want)
		}
	}

	f := newFixture(t)
	f.file("data/world/level.dat", 10, 0)
	f.file("spool/.offsite-0123456789abcdef.age", 3000, old)
	f.file("spool/.offsite-00112233445566ff.partial", 2000, old)
	f.file("spool/.offsite-fedcba9876543210.age/inside", 10, old)
	f.link("spool/.offsite-1111111111111111.age", f.path("data/world/level.dat"))
	f.file("spool/notes.txt", 10, old)
	l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data"), SpoolDir: f.path("spool")}}}
	rep := scanOK(t, l, f.options())
	ks := byKind(rep.Servers[0].Kinds)
	s := f.path("spool") + "/"
	if want := du(t, s+".offsite-0123456789abcdef.age", s+".offsite-00112233445566ff.partial"); ks[KindBackups] != want {
		t.Errorf("copies in the spool = %+v, want %+v as the server's backups", ks[KindBackups], want)
	}
	if want := du(t, s+".offsite-fedcba9876543210.age", s+".offsite-1111111111111111.age", s+"notes.txt"); ks[KindOther] != want {
		t.Errorf("the rest of the spool = %+v, want %+v as the server's other files", ks[KindOther], want)
	}
	if len(rep.Candidates) != 0 || len(rep.Problems) != 0 {
		t.Errorf("candidates %+v, problems %+v; want the spool counted and nothing in it offered", rep.Candidates, rep.Problems)
	}

	l.Servers[0].SpoolDir = f.path("no-spool")
	rep = scanOK(t, l, f.options())
	if len(rep.Problems) != 1 || rep.Problems[0].Code != "missing" || rep.Problems[0].Path != f.path("no-spool") {
		t.Errorf("problems = %+v, want the missing spool folder, which offsite needs", rep.Problems)
	}
}

func TestDownloads(t *testing.T) {
	f := newFixture(t)
	f.file("data/world/level.dat", 10, 0)
	f.file("downloads/old.jar", 1500, old)
	f.file("downloads/new.jar", 1500, 10*time.Minute)
	f.file("downloads/hangar/Other-2.0.jar", 800, 2*day)
	f.date("downloads/hangar", old)
	f.file("downloads/modrinth/done.jar", 800, old)
	f.file("downloads/modrinth/.writing.jar", 10, time.Minute)
	f.date("downloads/modrinth", old)
	l := Layout{Servers: []Server{{ID: "s", DataDir: f.path("data")}}, DownloadsDir: f.path("downloads")}
	rep := scanOK(t, l, f.options())
	got := f.candidates(rep)
	if keys := slices.Sorted(maps.Keys(got)); !slices.Equal(keys, []string{"downloads/hangar", "downloads/old.jar"}) {
		t.Fatalf("offered %v, want what hasn't changed for an hour", keys)
	}
	for rel, c := range got {
		if c.Reason != ReasonDownloaded || c.Kind != KindDownloads || c.Risk != RiskLow || c.ServerID != "" || c.Usage != du(t, c.Path) {
			t.Errorf("%s: %+v", rel, c)
		}
	}
	if date := got["downloads/hangar"].Params["date"]; date != f.now.Add(-2*day).UTC().Format("2006-01-02") {
		t.Errorf("a folder of downloads dated %s, want the date of the newest file in it", date)
	}
	d := f.path("downloads") + "/"
	want := du(t, d+"old.jar", d+"new.jar", d+"hangar", d+"modrinth")
	if ks := byKind(rep.Machine); ks[KindDownloads] != want {
		t.Errorf("downloads = %+v, want %+v for the machine", ks[KindDownloads], want)
	}

	l.Servers[0].Busy = true
	if rep := scanOK(t, l, f.options()); len(rep.Candidates) != 0 {
		t.Errorf("offered %+v while a server is busy", rep.Candidates)
	}

	l = Layout{Servers: l.Servers[:1], DownloadsDir: filepath.Join(f.dir, "not-yet")}
	l.Servers[0].Busy = false
	if rep := scanOK(t, l, f.options()); len(rep.Problems) != 0 || len(rep.Machine) != 0 {
		t.Errorf("a downloads folder not made yet: problems %+v, machine %+v; want neither", rep.Problems, rep.Machine)
	}
}

func TestDownloadsCleaned(t *testing.T) {
	f := newFixture(t)
	f.file("downloads/old.jar", 1500, old)
	f.file("downloads/hangar/Other-2.0.jar", 800, old)
	f.dateTree("downloads/hangar", old)
	l := Layout{DownloadsDir: f.path("downloads")}
	rep := scanOK(t, l, f.options())
	if len(rep.Ways) != 1 || rep.Ways[0].ID != WayDownloads || rep.Ways[0].Action != ActionClear {
		t.Fatalf("ways = %+v, want one to clear the downloads", rep.Ways)
	}
	res := cleanOK(t, context.Background(), l, f.options(), rep.Ways[0].CandidateIDs, nil)
	if res.Freed != rep.Ways[0].Bytes || res.Freed != rep.Freeable {
		t.Errorf("freed %d bytes, want the way's %d", res.Freed, rep.Ways[0].Bytes)
	}
	if f.exists("downloads/old.jar") || f.exists("downloads/hangar") || !f.exists("downloads") {
		t.Error("clearing the downloads left files behind, or deleted the folder itself")
	}
}
