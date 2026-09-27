package addons

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func statuses(res *ScanResult) []FileStatus {
	var out []FileStatus
	for _, e := range res.Entries {
		out = append(out, e.Status)
	}
	return out
}

func TestScanSortsTheFolder(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	installed := mustInstall(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
	installed = mustInstall(t, l, srv, installed, InstallRequest{Source: Modrinth, Project: "viaversion"})
	gone := Installed{Source: Hangar, ProjectID: "112", Name: "ViaRewind", VersionID: "30418", VersionNumber: "4.2.0", Channel: "release",
		FileName: "ViaRewind-4.2.0.jar", HashAlgo: "sha256", Hash: sha256hex([]byte("gone")), InstalledAt: testNow}
	installed = append(installed, gone)

	plugins := filepath.Join(srv.Dir, "plugins")
	chunky := filepath.Join(plugins, "Chunky-Bukkit-1.5.3.jar")
	writeFile(t, chunky, append(readFile(t, chunky), "patched"...))
	mine := pluginJar(t, "MyPlugin", "0.1")
	backwards, oldVia := f.mfile("SxGhdsPK").data, f.mfile("ZH8459B6").data
	writeFile(t, filepath.Join(plugins, "MyPlugin.jar"), mine)
	writeFile(t, filepath.Join(plugins, "ViaBackwards.jar"), backwards)
	writeFile(t, filepath.Join(plugins, "ViaVersion-old.jar"), oldVia)
	writeFile(t, filepath.Join(plugins, "notes.txt"), []byte("remember to back up"))
	writeFile(t, filepath.Join(plugins, ".hidden.jar"), backwards)
	outside := filepath.Join(t.TempDir(), "outside.jar")
	writeFile(t, outside, backwards)
	if err := errors.Join(os.Mkdir(filepath.Join(plugins, "Folder.jar"), 0o755), os.Symlink(outside, filepath.Join(plugins, "link.jar"))); err != nil {
		t.Fatal(err)
	}
	before, earlier := ls(t, plugins), len(f.sent("modrinth"))

	res, err := l.Scan(context.Background(), srv, installed, true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "scan", res, &ScanResult{
		Folder: "plugins",
		Entries: []ScanEntry{
			{FileName: "Chunky-Bukkit-1.5.3.jar", Size: installed[0].Size + int64(len("patched")), Status: FileModified, Installed: &installed[0],
				Meta: JarMeta{ID: "Chunky", Version: "1.5.3", Kind: "plugin"}},
			{FileName: "MyPlugin.jar", Size: int64(len(mine)), Status: FileUnknown, Meta: JarMeta{ID: "MyPlugin", Version: "0.1", Kind: "plugin"}},
			{FileName: "ViaBackwards.jar", Size: int64(len(backwards)), Status: FileIdentified, Identified: &Installed{
				Source: Modrinth, ProjectID: "NpvuJQoq", Slug: "viabackwards", Name: "ViaBackwards",
				Summary:   "Allow older Java Edition clients to connect to newer servers.",
				IconURL:   "https://cdn.modrinth.com/data/NpvuJQoq/c9fd86f343657c62206d5b82fee1d2b24f0b278d_96.webp",
				VersionID: "SxGhdsPK", VersionNumber: "5.12.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 4, 50, 516162000, time.UTC),
				FileName: "ViaBackwards.jar", HashAlgo: "sha512", Hash: sha512hex(backwards), Size: int64(len(backwards)),
				Requires: []string{"P1OZGk5p"}, InstalledAt: testNow,
			}, Meta: JarMeta{ID: "ViaBackwards", Version: "5.12.0", Kind: "plugin"}},
			{FileName: "ViaVersion-5.12.0.jar", Size: installed[1].Size, Status: FileManaged, Installed: &installed[1],
				Meta: JarMeta{ID: "ViaVersion", Version: "5.12.0", Kind: "plugin"}},
			{FileName: "ViaVersion-old.jar", Size: int64(len(oldVia)), Status: FileIdentified, Identified: &Installed{
				Source: Modrinth, ProjectID: "P1OZGk5p", Slug: "viaversion", Name: "ViaVersion",
				Summary:   "Allow newer Java Edition clients to connect to older servers.",
				IconURL:   "https://cdn.modrinth.com/data/P1OZGk5p/ad14260a7308dc9e4c3385f3f6b5bdabfe17f295_96.webp",
				VersionID: "ZH8459B6", VersionNumber: "5.11.0", Channel: "release", Published: time.Date(2026, 7, 12, 16, 45, 13, 784294000, time.UTC),
				FileName: "ViaVersion-old.jar", HashAlgo: "sha512", Hash: sha512hex(oldVia), Size: int64(len(oldVia)), InstalledAt: testNow,
			}, Meta: JarMeta{ID: "ViaVersion", Version: "5.11.0", Kind: "plugin"}},
		},
		Missing: []Installed{gone},
		Warnings: []Notice{notice(KindDuplicate,
			kv("name", "ViaVersion", "file", "ViaVersion-old.jar", "other", "ViaVersion-5.12.0.jar", "folder", "plugins"),
			"ViaVersion is in the plugins folder twice: ViaVersion-5.12.0.jar and ViaVersion-old.jar.",
			"Remove one of them; the server loads only one copy.")},
	})

	reqs := f.sent("modrinth")[earlier:]
	if len(reqs) != 2 || reqs[0].method != "POST" || reqs[0].path != "/v2/version_files" || reqs[1].path != "/v2/projects" {
		t.Fatalf("the scan sent %+v", reqs)
	}
	var body struct {
		Hashes    []string
		Algorithm string
	}
	json.Unmarshal([]byte(reqs[0].body), &body)
	if !slices.Equal(body.Hashes, []string{sha512hex(mine), sha512hex(backwards), sha512hex(oldVia)}) || body.Algorithm != "sha512" {
		t.Errorf("identification sent %+v", body)
	}
	if ids := jsonStrings(reqs[1].query.Get("ids")); !slices.Equal(ids, []string{"NpvuJQoq", "P1OZGk5p"}) {
		t.Errorf("project lookup for %v", ids)
	}
	if after := ls(t, plugins); !slices.Equal(after, before) {
		t.Errorf("the scan changed the folder from %v to %v", before, after)
	}

	earlier = len(f.sent("modrinth"))
	res, err = l.Scan(context.Background(), srv, installed, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []FileStatus{FileModified, FileUnknown, FileUnknown, FileManaged, FileUnknown}
	if got := statuses(res); !slices.Equal(got, want) || len(res.Warnings) != 0 || len(f.sent("modrinth")) != earlier {
		t.Errorf("without identifying: %v, warnings %v", got, res.Warnings)
	}

	f.setBusy(true)
	res, err = l.Scan(context.Background(), srv, installed, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := statuses(res); !slices.Equal(got, want) {
		t.Errorf("while Modrinth is busy: %v", got)
	}
	wantNotices(t, "warnings while Modrinth is busy", res.Warnings,
		"rate_limited: Modrinth asked Playkeeper to slow down. Files added by hand were not identified.")
	if len(res.Warnings) == 1 && res.Warnings[0].Hint != "Try again in 42 seconds." {
		t.Errorf("hint %q", res.Warnings[0].Hint)
	}
}

func TestScanModsFolder(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	api := f.mfile("ewUK83HI")
	writeFile(t, filepath.Join(srv.Dir, "mods", "fabric-api.jar"), api.data)
	writeFile(t, filepath.Join(srv.Dir, "plugins", "MyPlugin.jar"), pluginJar(t, "MyPlugin", "0.1"))

	res, err := l.Scan(context.Background(), srv, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "mods" || len(res.Entries) != 1 {
		t.Fatalf("scan %+v", res)
	}
	e := res.Entries[0]
	if e.Status != FileIdentified || e.Identified.ProjectID != "P7dR8mSH" || e.Identified.Name != "Fabric API" || e.Identified.Requires != nil {
		t.Errorf("entry %+v", e)
	}
	sameJSON(t, "meta", e.Meta, JarMeta{ID: "fabric-api", Name: "Fabric API", Version: "0.161.0+26.2", Kind: "mod"})
}

func TestScanWithoutAFolder(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	rec := Installed{Source: Modrinth, ProjectID: "fALzjamp", Name: "Chunky", VersionID: "MdY6JATr", VersionNumber: "1.5.3", Channel: "release",
		FileName: "Chunky-Bukkit-1.5.3.jar", HashAlgo: "sha512", Hash: sha512hex([]byte("chunky")), InstalledAt: testNow}

	res, err := l.Scan(context.Background(), srv, []Installed{rec}, true)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "scan", res, &ScanResult{Folder: "plugins", Entries: []ScanEntry{}, Missing: []Installed{rec}, Warnings: []Notice{}})
	if _, err := os.Lstat(filepath.Join(srv.Dir, "plugins")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the scan created the plugins folder: %v", err)
	}
	if reqs := f.sent("modrinth"); len(reqs) != 0 {
		t.Errorf("the scan sent %+v", reqs)
	}
}

func TestScanRefusals(t *testing.T) {
	f := newFakes(t)
	l := f.library()

	file := newServer(t, "paper", "26.2")
	writeFile(t, filepath.Join(file.Dir, "plugins"), []byte("not a folder"))
	linked := newServer(t, "paper", "26.2")
	if err := os.Symlink(t.TempDir(), filepath.Join(linked.Dir, "plugins")); err != nil {
		t.Fatal(err)
	}
	for _, srv := range []Server{file, linked} {
		_, err := l.Scan(context.Background(), srv, nil, true)
		if e := wantKind(t, err, KindFolderUnusable); e.Msg != "Playkeeper cannot use the server's plugins folder: it is not a folder." {
			t.Errorf("message %q", e.Msg)
		}
	}

	_, err := l.Scan(context.Background(), newServer(t, "vanilla", "26.2"), nil, true)
	wantKind(t, err, KindNoAddons)
	_, err = l.Scan(context.Background(), Server{Dir: filepath.Join(t.TempDir(), "gone"), Type: "paper"}, nil, true)
	wantKind(t, err, KindFolderUnusable)
}

func TestScanLooksAtABoundedNumberOfEntries(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	plugins := filepath.Join(srv.Dir, "plugins")
	writeFile(t, filepath.Join(plugins, "notes.txt"), nil)
	for i := range maxFolderEntries {
		if err := os.WriteFile(filepath.Join(plugins, "old-"+strconv.Itoa(i)+".txt"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := l.Scan(context.Background(), srv, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	wantNotices(t, "warnings", res.Warnings, "too_large: The plugins folder has more than 2000 entries, so Playkeeper looked at only 2000 of them.")
}
