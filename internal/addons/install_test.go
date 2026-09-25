package addons

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestInstallModrinth(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	srv.Owner = &Owner{UID: os.Getuid(), GID: os.Getgid()}
	res, err := l.Install(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"})
	if err != nil {
		t.Fatal(err)
	}

	plugins := filepath.Join(srv.Dir, "plugins")
	if got, want := ls(t, plugins), []string{"ViaBackwards-5.12.0.jar", "ViaRewind-4.2.0.jar", "ViaVersion-5.12.0.jar"}; !slices.Equal(got, want) {
		t.Fatalf("plugins holds %v, want %v", got, want)
	}
	for _, id := range []string{"EPLCoxMK", "SxGhdsPK", "FaishMnD"} {
		jar := f.mfile(id)
		if !bytes.Equal(readFile(t, filepath.Join(plugins, jar.name)), jar.data) {
			t.Errorf("%s differs from the download", jar.name)
		}
		if fi, err := os.Stat(filepath.Join(plugins, jar.name)); err != nil || fi.Mode().Perm()&^0o640 != 0 {
			t.Errorf("%s: mode %v, %v", jar.name, fi.Mode(), err)
		}
	}
	if fi, err := os.Stat(plugins); err != nil || fi.Mode().Perm()&^0o750 != 0 {
		t.Errorf("plugins: mode %v, %v", fi.Mode(), err)
	}

	rewind := f.mfile("EPLCoxMK")
	if len(res.Installed) != 3 {
		t.Fatalf("installed %+v", res.Installed)
	}
	sameJSON(t, "ViaRewind's record", res.Installed[0], Installed{
		Source: Modrinth, ProjectID: "TbHIxhx5", Slug: "viarewind", Name: "ViaRewind",
		Summary:   "ViaVersion addon to allow 1.8.x and 1.7.x clients on newer server versions.",
		IconURL:   "https://cdn.modrinth.com/data/TbHIxhx5/f59ffe031387b06a9b1efa736dbbb4db44284574_96.webp",
		VersionID: "EPLCoxMK", VersionNumber: "4.2.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 8, 24, 612555000, time.UTC),
		FileName: "ViaRewind-4.2.0.jar", HashAlgo: "sha512", Hash: sha512hex(rewind.data), Size: int64(len(rewind.data)),
		Requires: []string{"NpvuJQoq", "P1OZGk5p"}, InstalledAt: testNow,
	})
	for _, rec := range res.Installed[1:] {
		if rec.DependencyOf != "TbHIxhx5" || rec.InstalledAt != testNow {
			t.Errorf("record %+v", rec)
		}
	}
	if !res.RestartNeeded || len(res.Replaced)+len(res.Manual)+len(res.Warnings) != 0 {
		t.Errorf("result %+v", res)
	}
	if ls(t, l.TempDir) != nil {
		t.Errorf("temporary files left: %v", ls(t, l.TempDir))
	}
	for _, r := range f.sent("cdn") {
		if r.userAgent != wantUserAgent {
			t.Errorf("CDN request with User-Agent %q", r.userAgent)
		}
	}

	scan, err := l.Scan(context.Background(), srv, res.Installed, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range scan.Entries {
		if e.Status != FileManaged {
			t.Errorf("%s is %s after the install", e.FileName, e.Status)
		}
	}
	if len(scan.Entries) != 3 || len(scan.Missing) != 0 {
		t.Errorf("scan %+v", scan)
	}
}

func TestInstallHangar(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	res, err := l.Install(context.Background(), srv, nil, InstallRequest{Source: Hangar, Project: "ViaRewind"})
	if err != nil {
		t.Fatal(err)
	}
	plugins := filepath.Join(srv.Dir, "plugins")
	for _, id := range []string{"30418", "30417", "30415"} {
		jar := f.hfile(id)
		if !bytes.Equal(readFile(t, filepath.Join(plugins, jar.name)), jar.data) {
			t.Errorf("%s differs from the download", jar.name)
		}
	}
	rewind := f.hfile("30418")
	sameJSON(t, "ViaRewind's record", res.Installed[0], Installed{
		Source: Hangar, ProjectID: "112", Slug: "ViaRewind", Name: "ViaRewind", IconURL: "https://hangarcdn.papermc.io/avatars/project/112.webp?v=1",
		Summary:   "ViaVersion addon to allow 1.8.x and 1.7.x clients on newer server versions.",
		VersionID: "30418", VersionNumber: "4.2.0", Channel: "release", Published: time.Date(2026, 9, 18, 15, 8, 16, 539019000, time.UTC),
		FileName: "ViaRewind-4.2.0.jar", HashAlgo: "sha256", Hash: sha256hex(rewind.data), Size: int64(len(rewind.data)),
		Requires: []string{"12", "31"}, InstalledAt: testNow,
	})
	if n := len(f.sent("cdn")); n != 3 {
		t.Errorf("%d downloads, want 3", n)
	}
}

// Each broken download is refused before anything reaches the server's
// folder, and nothing is left in the temporary folder either. ViaVersion is
// the last of the three files ViaRewind needs, so two good files have been
// downloaded when it fails.
func TestInstallRefusesBadDownloads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *fakes, l *Library)
		kind  Kind
		msg   string
	}{
		{"hash mismatch", func(f *fakes, l *Library) {
			jar := f.mfile("FaishMnD")
			bad := slices.Clone(jar.data)
			bad[len(bad)/2] ^= 0xff
			f.hook(jar.path, serveBytes(bad))
		}, KindHashMismatch, "The download of ViaVersion-5.12.0.jar does not match the sha512 hash Modrinth publishes, so Playkeeper did not install it."},
		{"size mismatch", func(f *fakes, l *Library) {
			jar := f.mfile("FaishMnD")
			f.hook(jar.path, serveBytes(append(slices.Clone(jar.data), 'x')))
		}, KindSizeMismatch, "The download of ViaVersion-5.12.0.jar is not the size Modrinth lists, so Playkeeper did not install it."},
		{"listed size above the limit", func(f *fakes, l *Library) {
			l.MaxFileSize = 64
		}, KindTooLarge, "ViaRewind-4.2.0.jar is larger than the 64 bytes Playkeeper accepts for one add-on file."},
		{"unlisted size, endless body", func(f *fakes, l *Library) {
			l.MaxFileSize = 2 << 10
			jar := f.mfile("FaishMnD")
			f.patchFile("FaishMnD", func(file obj) { file["size"] = 0 })
			f.hook(jar.path, func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				w.Write(bytes.Repeat([]byte("x"), 8<<10))
			})
		}, KindTooLarge, "ViaVersion-5.12.0.jar is larger than the 2 KiB Playkeeper accepts for one add-on file."},
		{"redirect to another host", func(f *fakes, l *Library) {
			f.hook(f.mfile("FaishMnD").path, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, f.evil.URL+"/ViaVersion-5.12.0.jar", http.StatusFound)
			})
		}, KindRedirectRefused, "The download of ViaVersion-5.12.0.jar was redirected to 127.0.0.1, which is not one of Modrinth's own file hosts."},
		{"file on another host", func(f *fakes, l *Library) {
			f.patchFile("FaishMnD", func(file obj) { file["url"] = "https://files.example.com/ViaVersion-5.12.0.jar" })
		}, KindHostNotAllowed, "ViaVersion-5.12.0.jar would be downloaded from files.example.com, which is not one of Modrinth's own file hosts."},
		{"plain HTTP", func(f *fakes, l *Library) {
			f.patchFile("FaishMnD", func(file obj) { file["url"] = strings.Replace(str(file["url"]), "https:", "http:", 1) })
		}, KindNotHTTPS, "ViaVersion-5.12.0.jar would be downloaded from 127.0.0.1 without HTTPS."},
		{"file name with a folder", func(f *fakes, l *Library) {
			f.patchFile("FaishMnD", func(file obj) { file["filename"] = "../ViaVersion-5.12.0.jar" })
		}, KindBadFileName, `ViaVersion offers a file named "../ViaVersion-5.12.0.jar", which Playkeeper will not write to the server.`},
		{"file that is not a jar", func(f *fakes, l *Library) {
			f.patchFile("FaishMnD", func(file obj) { file["filename"] = "ViaVersion.sh" })
		}, KindBadFileName, `ViaVersion offers a file named "ViaVersion.sh", which Playkeeper will not write to the server.`},
		{"no SHA-512", func(f *fakes, l *Library) {
			f.patchFile("FaishMnD", func(file obj) { delete(file["hashes"].(obj), "sha512") })
		}, KindNoHash, "Modrinth lists no usable hash for ViaVersion-5.12.0.jar, so Playkeeper cannot check the download."},
		{"file missing on the CDN", func(f *fakes, l *Library) {
			f.hook(f.mfile("FaishMnD").path, http.NotFound)
		}, KindUpstream, "Modrinth answered with an error (HTTP 404)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakes(t)
			l := f.library()
			tc.setup(f, l)
			srv := newServer(t, "paper", "26.2")
			res, err := l.Install(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"})
			if res != nil {
				t.Errorf("result %+v", res)
			}
			if e := wantKind(t, err, tc.kind); e.Msg != tc.msg {
				t.Errorf("message %q, want %q", e.Msg, tc.msg)
			}
			if got := ls(t, srv.Dir); got != nil {
				t.Errorf("server folder holds %v", got)
			}
			if got := ls(t, l.TempDir); got != nil {
				t.Errorf("temporary folder holds %v", got)
			}
			if s := f.strays(); len(s) != 0 {
				t.Errorf("requests went to %v", s)
			}
		})
	}
}

func TestInstallNeverOverwrites(t *testing.T) {
	t.Run("file already there", func(t *testing.T) {
		f := newFakes(t)
		srv := newServer(t, "paper", "26.2")
		mine := filepath.Join(srv.Dir, "plugins", "Chunky-Bukkit-1.5.3.jar")
		writeFile(t, mine, []byte("my own build"))
		_, err := f.library().Install(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
		if e := wantKind(t, err, KindFileExists); e.Msg != "The plugins folder already has a file named Chunky-Bukkit-1.5.3.jar." {
			t.Errorf("message %q", e.Msg)
		}
		if string(readFile(t, mine)) != "my own build" || len(ls(t, filepath.Dir(mine))) != 1 || len(f.sent("cdn")) != 0 {
			t.Errorf("folder %v, %d downloads", ls(t, filepath.Dir(mine)), len(f.sent("cdn")))
		}
	})
	t.Run("file appears during the download", func(t *testing.T) {
		f := newFakes(t)
		srv := newServer(t, "paper", "26.2")
		mine := filepath.Join(srv.Dir, "plugins", "Chunky-Bukkit-1.5.3.jar")
		jar := f.mfile("MdY6JATr")
		f.hook(jar.path, func(w http.ResponseWriter, r *http.Request) {
			os.MkdirAll(filepath.Dir(mine), 0o755)
			os.WriteFile(mine, []byte("uploaded meanwhile"), 0o644)
			serveBytes(jar.data)(w, r)
		})
		_, err := f.library().Install(context.Background(), srv, nil, InstallRequest{Source: Modrinth, Project: "chunky"})
		wantKind(t, err, KindFileExists)
		if string(readFile(t, mine)) != "uploaded meanwhile" || len(ls(t, filepath.Dir(mine))) != 1 {
			t.Errorf("folder %v", ls(t, filepath.Dir(mine)))
		}
	})
}

func TestInstallRefusesAChangedPlan(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "paper", "26.2")
	req := InstallRequest{Source: Modrinth, Project: "viarewind"}
	confirmed := mustPlan(t, l, srv, nil, req).Fingerprint

	f.patchVersion("FaishMnD", func(v obj) { v["version_number"] = "5.12.0-hotfix" })
	req.Fingerprint = confirmed
	_, err := l.Install(context.Background(), srv, nil, req)
	if e := wantKind(t, err, KindPlanChanged); e.Msg != "What this would do has changed since you confirmed it." {
		t.Errorf("message %q", e.Msg)
	}
	if ls(t, srv.Dir) != nil || len(f.sent("cdn")) != 0 {
		t.Errorf("wrote %v after %d downloads", ls(t, srv.Dir), len(f.sent("cdn")))
	}

	req.Fingerprint = mustPlan(t, l, srv, nil, InstallRequest{Source: Modrinth, Project: "viarewind"}).Fingerprint
	if _, err := l.Install(context.Background(), srv, nil, req); err != nil {
		t.Fatal(err)
	}
}

// txn replaces files by name and puts everything back when a later file
// cannot be placed.
func TestTxnRestoresTheFolderOnFailure(t *testing.T) {
	old := []byte("version 1")
	setup := func(t *testing.T) (dir string, tx *txn, staged []string) {
		dir = t.TempDir()
		writeFile(t, filepath.Join(dir, "a-1.0.jar"), old)
		writeFile(t, filepath.Join(dir, "b.jar"), []byte("someone else's"))
		stage := t.TempDir()
		for i, content := range []string{"version 2", "new file"} {
			p := filepath.Join(stage, strings.Repeat("x", i+1))
			writeFile(t, p, []byte(content))
			staged = append(staged, p)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { root.Close() })
		return dir, &txn{root: root, t: Target{Folder: "plugins"}}, staged
	}
	replaces := &Installed{Name: "A", FileName: "a-1.0.jar", HashAlgo: "sha512", Hash: sha512hex(old), Size: int64(len(old))}

	t.Run("rollback", func(t *testing.T) {
		dir, tx, staged := setup(t)
		err := tx.run([]Step{{Name: "A", FileName: "a-2.0.jar", Replaces: replaces}, {Name: "B", FileName: "b.jar"}}, staged)
		wantKind(t, err, KindFileExists)
		tx.rollback()
		if got := ls(t, dir); !slices.Equal(got, []string{"a-1.0.jar", "b.jar"}) {
			t.Fatalf("folder %v", got)
		}
		if string(readFile(t, filepath.Join(dir, "a-1.0.jar"))) != "version 1" || string(readFile(t, filepath.Join(dir, "b.jar"))) != "someone else's" {
			t.Error("files changed")
		}
	})
	t.Run("commit", func(t *testing.T) {
		dir, tx, staged := setup(t)
		if err := tx.run([]Step{{Name: "A", FileName: "a-2.0.jar", Replaces: replaces}, {Name: "C", FileName: "c.jar"}}, staged); err != nil {
			t.Fatal(err)
		}
		tx.commit()
		if got := ls(t, dir); !slices.Equal(got, []string{"a-2.0.jar", "b.jar", "c.jar"}) {
			t.Fatalf("folder %v", got)
		}
		if string(readFile(t, filepath.Join(dir, "a-2.0.jar"))) != "version 2" {
			t.Error("a-2.0.jar has the wrong content")
		}
	})
	t.Run("changed file is not replaced", func(t *testing.T) {
		dir, tx, staged := setup(t)
		writeFile(t, filepath.Join(dir, "a-1.0.jar"), []byte("edited by hand"))
		err := tx.run([]Step{{Name: "A", FileName: "a-2.0.jar", Replaces: replaces}}, staged[:1])
		wantKind(t, err, KindModified)
		tx.rollback()
		if got := ls(t, dir); !slices.Equal(got, []string{"a-1.0.jar", "b.jar"}) {
			t.Fatalf("folder %v", got)
		}
	})
}
