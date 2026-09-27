package modpacks

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

func TestUnsafeArchivesAreRefused(t *testing.T) {
	index := entry{name: mrpack.IndexName, data: mustJSON(t, testIndex())}
	for _, c := range []struct {
		name    string
		entries []entry
		kind    addons.Kind
	}{
		{"climbs out", []entry{index, {name: "overrides/../../evil.txt"}}, KindUnsafePath},
		{"absolute", []entry{index, {name: "/etc/cron.d/evil"}}, KindUnsafePath},
		{"backslashes", []entry{index, {name: `overrides\..\..\evil.txt`}}, KindUnsafePath},
		{"drive letter", []entry{index, {name: "C:/evil.txt"}}, KindUnsafePath},
		{"link", []entry{index, {name: "overrides/mods", data: []byte("/etc"), mode: fs.ModeSymlink | 0o777}}, KindUnsafePath},
		{"not UTF-8", []entry{index, {name: "overrides/\xff.txt"}}, KindUnsafePath},
		{"part Playkeeper does not read", []entry{index, {name: "client-overrides/../../../evil.txt"}}, KindUnsafePath},
		{"entry twice", []entry{index, {name: "overrides/a.txt"}, {name: "overrides/a.txt"}}, KindBadPack},
		{"file and folder", []entry{index, {name: "overrides/a"}, {name: "overrides/a/b.txt"}}, KindBadPack},
		{"file and folder entry", []entry{index, {name: "overrides/a/"}, {name: "overrides/a"}}, KindBadPack},
		{"no index", []entry{{name: "overrides/a.txt"}}, KindBadPack},
		{"index twice", []entry{index, index}, KindBadPack},
	} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: testRef(f.addPack(nil, c.entries...))})
		if e := addons.KindOf(err); e != c.kind {
			t.Errorf("%s: %v (kind %q), want kind %q", c.name, err, e, c.kind)
		}
		wantTree(t, srv.Dir)
		wantTree(t, l.TempDir)
	}

	f := newFakes(t)
	l := f.library()
	id := f.addPack(nil, entry{name: mrpack.IndexName, data: mustJSON(t, testIndex())})
	f.hook(f.parse(str(f.mrpackOf(id)["url"])).Path, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not a zip"))
	})
	f.editVersion(id, func(v obj) { setSums(list(v["files"])[0].(obj), "size", []byte("not a zip")) })
	_, err := l.PlanInstall(context.Background(), newServer(t, "", ""), nil, InstallRequest{Ref: testRef(id)})
	if e := wantKind(t, err, KindBadPack); e.Msg != "Test Pack cannot be installed: its archive is not a zip archive Playkeeper can read." {
		t.Errorf("message %q", e.Msg)
	}
}

func TestUnsafeIndexPathsAreRefused(t *testing.T) {
	for _, p := range []string{"../evil.jar", "/etc/evil.jar", `mods\evil.jar`, "mods/../../evil.jar", "mods//evil.jar", "C:/evil.jar", "mods/./evil.jar", "mods/evil\u202e.jar"} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		file := f.indexFile("mods/ok-1.jar", []byte("ok"))
		evil := f.indexFile("mods/evil-1.jar", []byte("evil"))
		evil["path"] = p
		_, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: testRef(f.addPack(testIndex(file, evil)))})
		if e := addons.KindOf(err); e != KindUnsafePath {
			t.Errorf("%q: %v (kind %q)", p, err, e)
		}
		if got := f.served("cdn"); len(got) != 1 {
			t.Errorf("%q: downloaded %q", p, got)
		}
		wantTree(t, srv.Dir)
	}
}

func TestDownloadsMustMatchThePacksHashes(t *testing.T) {
	for _, c := range []struct {
		name, algo string
		change     func(f *fakes, file obj)
	}{
		{"different file", "sha512", func(f *fakes, file obj) { f.replace(file, []byte("tampered")) }},
		{"sha1 differs", "sha1", func(f *fakes, file obj) { file["hashes"].(obj)["sha1"] = sha1hex([]byte("other")) }},
	} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		good := f.indexFile("mods/good-1.jar", []byte("good"))
		bad := f.indexFile("mods/bad-1.jar", []byte("the real file"))
		c.change(f, bad)
		id := f.addPack(testIndex(good, bad), entry{name: "overrides/config/a.txt", data: []byte("a")})
		p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
		_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p.Fingerprint})
		e := wantKind(t, err, addons.KindHashMismatch)
		if e.Params["algo"] != c.algo || e.Msg != "The download of mods/bad-1.jar does not match the "+c.algo+" hash the pack lists, so Playkeeper did not install Test Pack." {
			t.Errorf("%s: %+v", c.name, e.Notice)
		}
		wantTree(t, srv.Dir)
		wantTree(t, l.TempDir)
	}

	f := newFakes(t)
	l := f.library()
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a"))))
	f.editVersion(id, func(v obj) { list(v["files"])[0].(obj)["hashes"].(obj)["sha512"] = sha512hex([]byte("another pack")) })
	_, err := l.PlanInstall(context.Background(), newServer(t, "", ""), nil, InstallRequest{Ref: testRef(id)})
	if e := wantKind(t, err, addons.KindHashMismatch); e.Msg != "The download of test-pack-TSTv0001.mrpack does not match the sha512 hash Modrinth lists, so Playkeeper did not install Test Pack." {
		t.Errorf("message %q", e.Msg)
	}
	wantTree(t, l.TempDir)
}

func TestDownloadHosts(t *testing.T) {
	data := []byte("a mod")
	for _, c := range []struct {
		name string
		urls func(f *fakes) []string
		kind addons.Kind // of the plan's blocker, or of the install's error
		plan bool
	}{
		{name: "pack host", urls: func(f *fakes) []string { return []string{f.serve(f.cdn, "/data/a/versions/1/a-1.jar", data)} }},
		{name: "not a pack host", urls: func(f *fakes) []string { return []string{f.serve(f.evil, "/a-1.jar", data)} },
			kind: addons.KindHostNotAllowed, plan: true},
		{name: "redirect target", urls: func(f *fakes) []string { return []string{f.serve(f.assets, "/a-1.jar", data)} },
			kind: addons.KindHostNotAllowed, plan: true},
		{name: "second address", urls: func(f *fakes) []string {
			return []string{f.url(f.evil, "/a-1.jar"), f.url(f.cdn, "/missing/a-1.jar"), f.serve(f.cdn, "/data/a/versions/1/a-1.jar", data)}
		}},
		{name: "GitHub release", urls: func(f *fakes) []string {
			f.serve(f.assets, "/owner/repo/a-1.jar", data)
			return []string{f.url(f.github, "/release/owner/repo/a-1.jar")}
		}},
		{name: "redirect elsewhere", urls: func(f *fakes) []string {
			f.serve(f.evil, "/owner/repo/a-1.jar", data)
			return []string{f.url(f.github, "/elsewhere/owner/repo/a-1.jar")}
		}, kind: addons.KindRedirectRefused},
	} {
		f := newFakes(t)
		l := f.library()
		srv := newServer(t, "fabric", "26.2")
		id := f.addPack(testIndex(f.fileAt("mods/a-1.jar", data, c.urls(f)...)))
		p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
		if c.plan {
			if len(p.Blockers) != 1 || p.Blockers[0].Kind != c.kind || p.Ready {
				t.Errorf("%s: blockers %q", c.name, noticeList(p.Blockers))
			}
		} else if !p.Ready {
			t.Errorf("%s: blockers %q", c.name, noticeList(p.Blockers))
		}
		_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p.Fingerprint})
		switch {
		case c.kind == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.kind == "":
			if got := readFile(t, srv, "mods/a-1.jar"); got != string(data) {
				t.Errorf("%s: mods/a-1.jar is %q", c.name, got)
			}
		case addons.KindOf(err) != c.kind:
			t.Errorf("%s: %v (kind %q), want kind %q", c.name, err, addons.KindOf(err), c.kind)
		default:
			wantTree(t, srv.Dir)
		}
		if f.evilHits != 0 {
			t.Errorf("%s: a host packs may not use was contacted %d times", c.name, f.evilHits)
		}
	}

	f := newFakes(t)
	l := f.library()
	id := f.addPack(testIndex(f.fileAt("mods/a-1.jar", data, "http://"+host(f.cdn)+"/a-1.jar")))
	_, err := l.PlanInstall(context.Background(), newServer(t, "", ""), nil, InstallRequest{Ref: testRef(id)})
	wantKind(t, err, KindBadPack)
}

func TestLimits(t *testing.T) {
	six := []byte("123456")
	for _, c := range []struct {
		name   string
		limits Limits
		pack   func(f *fakes) string
		kind   addons.Kind
		when   string // plan, blocker or install
	}{
		{"files", Limits{Files: 2}, func(f *fakes) string {
			return f.addPack(testIndex(f.indexFile("mods/a-1.jar", six), f.indexFile("mods/b-1.jar", six)), entry{name: "overrides/c.txt", data: six})
		}, KindTooManyFiles, "plan"},
		{"entries", Limits{Entries: 2}, func(f *fakes) string {
			return f.addPack(testIndex(), entry{name: "overrides/a.txt"}, entry{name: "overrides/b.txt"})
		}, KindTooManyFiles, "plan"},
		{"index", Limits{Index: 64}, func(f *fakes) string { return f.addPack(testIndex(f.indexFile("mods/a-1.jar", six))) }, KindBadPack, "plan"},
		{"archive", Limits{Pack: 64}, func(f *fakes) string { return f.addPack(testIndex()) }, addons.KindTooLarge, "plan"},
		{"unpacked", Limits{Unpacked: 10}, func(f *fakes) string {
			return f.addPack(testIndex(), entry{name: "overrides/a.txt", data: six}, entry{name: "overrides/b.txt", data: six})
		}, addons.KindTooLarge, "plan"},
		{"download", Limits{File: 5}, func(f *fakes) string { return f.addPack(testIndex(f.indexFile("mods/a-1.jar", six))) }, addons.KindTooLarge, "blocker"},
		{"override", Limits{File: 5}, func(f *fakes) string { return f.addPack(testIndex(), entry{name: "overrides/a.txt", data: six}) }, addons.KindTooLarge, "blocker"},
		{"downloads", Limits{Downloads: 10}, func(f *fakes) string {
			return f.addPack(testIndex(f.indexFile("mods/a-1.jar", six), f.indexFile("mods/b-1.jar", six)))
		}, addons.KindTooLarge, "blocker"},
		{"downloads larger than listed", Limits{Downloads: 10}, func(f *fakes) string {
			a, b := f.indexFile("mods/a-1.jar", six), f.indexFile("mods/b-1.jar", six)
			a["fileSize"], b["fileSize"] = 1, 1
			return f.addPack(testIndex(a, b))
		}, addons.KindTooLarge, "install"},
	} {
		f := newFakes(t)
		l := f.library()
		l.Limits = c.limits
		srv := newServer(t, "fabric", "26.2")
		id := c.pack(f)
		p, err := l.PlanInstall(context.Background(), srv, nil, InstallRequest{Ref: testRef(id)})
		switch c.when {
		case "plan":
			if addons.KindOf(err) != c.kind {
				t.Errorf("%s: %v (kind %q), want kind %q", c.name, err, addons.KindOf(err), c.kind)
			}
			continue
		case "blocker":
			if err != nil || len(p.Blockers) != 1 || p.Blockers[0].Kind != c.kind {
				t.Errorf("%s: %v, blockers %q", c.name, err, noticeList(p.Blockers))
				continue
			}
		case "install":
			if err != nil || !p.Ready {
				t.Errorf("%s: %v, blockers %q", c.name, err, noticeList(p.Blockers))
				continue
			}
		}
		_, err = l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p.Fingerprint})
		if addons.KindOf(err) != c.kind {
			t.Errorf("%s: installing: %v (kind %q), want kind %q", c.name, err, addons.KindOf(err), c.kind)
		}
		wantTree(t, srv.Dir)
		wantTree(t, l.TempDir)
	}
}

// If writing into the server's folder fails part way, everything written is
// taken back.
func TestFailedInstallLeavesNoTrace(t *testing.T) {
	f := newFakes(t)
	l := f.library()
	srv := newServer(t, "fabric", "26.2")
	b := f.indexFile("zzz/b-1.jar", []byte("b"))
	id := f.addPack(testIndex(f.indexFile("mods/a-1.jar", []byte("a")), b), entry{name: "overrides/config/c.txt", data: []byte("c")})
	p := mustPlan(t, l, srv, InstallRequest{Ref: testRef(id)})
	// While the pack's files download, something puts a file where the pack
	// needs a folder.
	cdnPath := f.parse(strs(b["downloads"])[0]).Path
	f.hook(cdnPath, func(w http.ResponseWriter, r *http.Request) {
		os.WriteFile(filepath.Join(srv.Dir, "zzz"), []byte("in the way"), 0o644)
		w.Header().Set("Content-Length", strconv.Itoa(len("b")))
		w.Write([]byte("b"))
	})
	_, err := l.Install(context.Background(), srv, nil, InstallRequest{Ref: testRef(id), Fingerprint: p.Fingerprint})
	if e := wantKind(t, err, KindInTheWay); e.Msg != "Test Pack needs to write zzz/b-1.jar, but zzz on the server is a file or a link, not a folder." {
		t.Errorf("message %q", e.Msg)
	}
	wantTree(t, srv.Dir, "zzz")
	wantTree(t, l.TempDir)
}

func TestHiddenNames(t *testing.T) {
	a, b := hiddenName("mods/a.jar", "new"), hiddenName("mods/a.jar", "new")
	if a == b || !strings.HasPrefix(a, "mods/.a.jar.playkeeper-new-") || len(a) != len("mods/.a.jar.playkeeper-new-")+12 {
		t.Errorf("hidden names %q and %q", a, b)
	}
	if classify(a, "world") != classProtected {
		t.Errorf("%s is not protected", a)
	}
}
