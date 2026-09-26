package modpacks

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/modpacks/curseforge"
	"github.com/CIYAhq/playkeeper/internal/modpacks/mrpack"
)

type obj = map[string]any

var testNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// testKey is made up; the CurseForge fake refuses every other key.
const testKey = "$2a$10$test-key-not-real-0123456789abcdefghijklmnopqr"

// Test pack ids on Modrinth: a made-up project for packs a test builds.
const (
	testProject = "TSTpk001"
	testSlug    = "test-pack"
)

// fakes serves the recorded Modrinth answers in testdata/modrinth and the
// CurseForge fixtures from local TLS servers. Every pack version with a
// trimmed pack in testdata/packs gets that pack, zipped, on a fake CDN; the
// files the packs list are small generated files there, with hashes, sizes
// and addresses rewritten to match.
type fakes struct {
	t *testing.T
	// modrinth and curseforge are the APIs, cdn and forgecdn their file
	// hosts. github redirects to assets, or to evil, which no list allows.
	modrinth, cdn, github, assets, evil, curseforge, forgecdn *httptest.Server
	client                                                    *http.Client

	mu       sync.Mutex
	projects map[string]obj // Modrinth, by id and by slug
	versions map[string]obj
	order    []string          // Modrinth version ids, newest first
	files    map[string][]byte // by host and path
	hooks    map[string]http.HandlerFunc
	requests []request
	evilHits int
	offHosts []string
	cfMods   map[int64]obj
	cfFiles  map[int64]obj
	// handedOut holds the addresses given out on the file hosts, by host and
	// path, served or not; url adds to it with or without mu held.
	handedOut sync.Map
}

type request struct {
	service, method, path string
	query                 url.Values
	body                  string
}

// entry is one entry of a zip a test builds. mode is 0 for a file.
type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func newFakes(t *testing.T) *fakes {
	t.Helper()
	f := &fakes{t: t, projects: map[string]obj{}, versions: map[string]obj{}, files: map[string][]byte{},
		hooks: map[string]http.HandlerFunc{}, cfMods: map[int64]obj{}, cfFiles: map[int64]obj{}}
	f.modrinth = httptest.NewTLSServer(http.HandlerFunc(f.serveModrinth))
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(f.serveFiles("cdn")))
	f.github = httptest.NewTLSServer(http.HandlerFunc(f.serveGitHub))
	f.assets = httptest.NewTLSServer(http.HandlerFunc(f.serveFiles("assets")))
	f.evil = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.foreign("evil", w, r) {
			return
		}
		f.mu.Lock()
		f.evilHits++
		f.mu.Unlock()
		w.Write([]byte("not a pack file"))
	}))
	f.curseforge = httptest.NewTLSServer(http.HandlerFunc(f.serveCurseForge))
	f.forgecdn = httptest.NewTLSServer(http.HandlerFunc(f.serveFiles("forgecdn")))
	for _, s := range []*httptest.Server{f.modrinth, f.cdn, f.github, f.assets, f.evil, f.curseforge, f.forgecdn} {
		t.Cleanup(s.Close)
	}
	// Every httptest TLS server uses the same certificate, so one client
	// trusts them all.
	f.client = &http.Client{Transport: localOnly{f.modrinth.Client().Transport, f}}
	f.loadModrinth()
	f.loadCurseForge()
	return f
}

func host(s *httptest.Server) string { return s.Listener.Addr().String() }

// library returns a Library that reaches only the fakes. Its clock stands
// still, so a rate-limit pause never runs out during a test.
func (f *fakes) library() *Library {
	now := func() time.Time { return testNow }
	key, err := curseforge.NewKey(testKey, curseforge.KeyFile)
	if err != nil {
		f.t.Fatal(err)
	}
	return &Library{
		Modrinth:        modrinth.New(fetch.Options{BaseURL: f.modrinth.URL + "/v2", HTTP: f.client, Now: now}),
		CurseForge:      curseforge.New(key, fetch.Options{BaseURL: f.curseforge.URL, HTTP: f.client, Now: now}),
		HTTP:            f.client,
		Now:             now,
		TempDir:         f.t.TempDir(),
		PackHosts:       fetch.Hosts{host(f.cdn), host(f.github)},
		PackRedirects:   fetch.Hosts{host(f.assets)},
		ModrinthFiles:   fetch.Hosts{host(f.cdn)},
		CurseForgeFiles: fetch.Hosts{host(f.forgecdn)},
	}
}

// packFolders maps recorded pack versions to their trimmed packs.
func packFolders() map[string]string {
	return map[string]string{
		"EZaeTUP8": "adrenaline",
		"jmYEuzNA": "vanilla-perfected-1.0.2",
		"zCNpmrT6": "vanilla-perfected-1.0.3",
		"BSg2ZS8u": "create-plus-6.0.0-alpha-f",
		"OirSzesD": "create-plus-5.2.1b",
		"ck8SrkA4": "csmp-1.5",
	}
}

func (f *fakes) loadModrinth() {
	var ps []obj
	f.decode("testdata/modrinth/projects.json", &ps)
	for _, p := range ps {
		f.projects[str(p["id"])], f.projects[str(p["slug"])] = p, p
	}
	tp := obj{
		"id": testProject, "slug": testSlug, "project_type": "modpack", "title": "Test Pack", "description": "A pack a test built.",
		"categories": []any{"technology"}, "loaders": []any{"fabric"}, "game_versions": []any{"26.2"}, "client_side": "required",
		"server_side": "required", "license": obj{"id": "MIT"}, "status": "approved",
	}
	f.projects[testProject], f.projects[testSlug] = tp, tp
	folders := packFolders()
	paths, _ := filepath.Glob("testdata/modrinth/versions-*.json")
	for _, p := range paths {
		var vs []obj
		f.decode(p, &vs)
		for _, v := range vs {
			for _, x := range list(v["files"]) {
				file := x.(obj)
				u := f.parse(str(file["url"]))
				if folder := folders[str(v["id"])]; folder != "" {
					setSums(file, "size", f.put(f.cdn, u.Path, f.folderPack(folder)))
				}
				f.handedOut.Store(host(f.cdn)+u.Path, true)
				file["url"] = f.cdn.URL + u.EscapedPath()
			}
			f.versions[str(v["id"])] = v
			f.order = append(f.order, str(v["id"]))
		}
	}
	slices.SortStableFunc(f.order, func(a, b string) int {
		return published(f.versions[b]).Compare(published(f.versions[a]))
	})
}

func published(v obj) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, str(v["date_published"]))
	return t
}

// folderPack zips a trimmed pack. The files its index lists are put on the
// fake CDN, and names in overrides.txt become placeholder overrides.
func (f *fakes) folderPack(folder string) []byte {
	dir := filepath.Join("testdata", "packs", folder)
	var ix obj
	f.decode(filepath.Join(dir, mrpack.IndexName), &ix)
	for _, x := range list(ix["files"]) {
		file := x.(obj)
		dls := strs(file["downloads"])
		data := generated(f.parse(dls[0]).Path)
		urls := make([]any, len(dls))
		for i, d := range dls {
			u := f.parse(d)
			f.put(f.cdn, u.Path, data)
			urls[i] = f.cdn.URL + u.EscapedPath()
		}
		file["downloads"] = urls
		setSums(file, "fileSize", data)
	}
	entries := []entry{{name: mrpack.IndexName, data: mustJSON(f.t, ix)}}
	des, err := os.ReadDir(dir)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, de := range des {
		switch name := de.Name(); {
		case name == mrpack.IndexName:
		case name == "overrides.txt":
			b, _ := os.ReadFile(filepath.Join(dir, name))
			for n := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
				entries = append(entries, entry{name: "overrides/" + n, data: []byte("placeholder for " + n + "\n")})
			}
		case de.IsDir():
			filepath.WalkDir(filepath.Join(dir, name), func(p string, d fs.DirEntry, err error) error {
				if err == nil && d.Type().IsRegular() {
					rel, _ := filepath.Rel(dir, p)
					b, _ := os.ReadFile(p)
					entries = append(entries, entry{name: filepath.ToSlash(rel), data: b})
				}
				return err
			})
		default:
			b, _ := os.ReadFile(filepath.Join(dir, name))
			entries = append(entries, entry{name: name, data: b})
		}
	}
	return zipOf(f.t, entries...)
}

// generated is the made-up content of a pack file on the fake CDN.
func generated(p string) []byte {
	return []byte(strings.Repeat("generated for "+p+"\n", 3))
}

// setSums sets a file record's hashes and size (under sizeKey) for data.
func setSums(file obj, sizeKey string, data []byte) {
	s512, s1 := sha512.Sum512(data), sha1.Sum(data)
	file["hashes"] = obj{"sha512": hex.EncodeToString(s512[:]), "sha1": hex.EncodeToString(s1[:])}
	file[sizeKey] = len(data)
}

// put serves data from s at path and returns data.
func (f *fakes) put(s *httptest.Server, p string, data []byte) []byte {
	f.files[host(s)+p] = data
	return data
}

// url is the address of path on s.
func (f *fakes) url(s *httptest.Server, p string) string {
	f.handedOut.Store(host(s)+p, true)
	return s.URL + (&url.URL{Path: p}).EscapedPath()
}

// indexFile puts data on the fake CDN and returns an index entry for it at
// path. The file belongs to the project its name starts with, up to the
// first dash: mods/lithium-2.jar is lithium's. env is the client and server
// support, when given.
func (f *fakes) indexFile(p string, data []byte, env ...string) obj {
	base := path.Base(p)
	project, _, _ := strings.Cut(base, "-")
	sum := sha1.Sum(data)
	file := f.fileAt(p, data, f.serve(f.cdn, "/data/"+project+"/versions/"+hex.EncodeToString(sum[:4])+"/"+base, data))
	if len(env) == 2 {
		file["env"] = obj{"client": env[0], "server": env[1]}
	}
	return file
}

// fileAt returns an index entry at path for data, listed at urls.
func (f *fakes) fileAt(p string, data []byte, urls ...string) obj {
	file := obj{"path": p, "downloads": toAny(urls)}
	setSums(file, "fileSize", data)
	return file
}

// serve serves data from s at path and returns its address.
func (f *fakes) serve(s *httptest.Server, p string, data []byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(s, p, data)
	return f.url(s, p)
}

// replace changes what the fake CDN serves for an index entry.
func (f *fakes) replace(file obj, data []byte) {
	u := f.parse(strs(file["downloads"])[0])
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[u.Host+u.Path] = data
}

// testIndex is a Fabric 26.2 pack index with the given files.
func testIndex(files ...obj) obj {
	items := make([]any, len(files))
	for i, x := range files {
		items[i] = x
	}
	return obj{
		"formatVersion": 1, "game": "minecraft", "versionId": "1.0.0", "name": "Test Pack", "files": items,
		"dependencies": obj{"minecraft": "26.2", "fabric-loader": "0.19.5"},
	}
}

// testRef names a version of the test project.
func testRef(version string) Ref {
	return Ref{Source: addons.Modrinth, Project: testProject, Version: version}
}

// setProject adds or replaces a Modrinth project.
func (f *fakes) setProject(p obj) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects[str(p["id"])], f.projects[str(p["slug"])] = p, p
}

// mrpackOf is the .mrpack file record of a Modrinth version.
func (f *fakes) mrpackOf(version string) obj {
	f.mu.Lock()
	defer f.mu.Unlock()
	return list(f.versions[version]["files"])[0].(obj)
}

// editVersion changes what Modrinth says about a version.
func (f *fakes) editVersion(version string, edit func(v obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.versions[version])
}

// addPack serves a pack a test built as a new version of the test project
// and returns the version id. index may be nil when entries has the index.
func (f *fakes) addPack(index obj, entries ...entry) string {
	if index != nil {
		entries = append([]entry{{name: mrpack.IndexName, data: mustJSON(f.t, index)}}, entries...)
	}
	data := zipOf(f.t, entries...)
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, id := range f.order {
		if str(f.versions[id]["project_id"]) == testProject {
			n++
		}
	}
	id := fmt.Sprintf("TSTv%04d", n+1)
	p := "/data/" + testProject + "/versions/" + id + "/test-pack-" + id + ".mrpack"
	file := obj{"url": f.url(f.cdn, p), "filename": path.Base(p), "primary": true, "file_type": nil}
	setSums(file, "size", f.put(f.cdn, p, data))
	v := obj{
		"id": id, "project_id": testProject, "name": "Test Pack " + id, "version_number": "1.0." + strconv.Itoa(n),
		"version_type": "release", "status": "listed", "game_versions": []any{"26.2"}, "loaders": []any{"fabric"},
		"date_published": testNow.Add(time.Duration(n-100) * time.Hour).Format(time.RFC3339Nano), "files": []any{file},
	}
	f.versions[id] = v
	f.order = append([]string{id}, f.order...)
	return id
}

// hook replaces what a file server answers for path.
func (f *fakes) hook(p string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks[p] = h
}

// foreign fails the test on a request for nothing the service serves or a
// test gave out an address for, and answers it 404, so it isn't counted
// with the fakes' requests. Such a request comes from another package's
// test that found one of these servers on a port it had just closed.
func (f *fakes) foreign(service string, w http.ResponseWriter, r *http.Request) bool {
	owned := false
	switch service {
	case "modrinth":
		owned = strings.HasPrefix(r.URL.Path, "/v2/")
	case "curseforge":
		owned = strings.HasPrefix(r.URL.Path, "/v1/")
	default:
		_, owned = f.handedOut.Load(r.Host + r.URL.Path)
		f.mu.Lock()
		_, served := f.files[r.Host+r.URL.Path]
		owned = owned || served || f.hooks[r.URL.Path] != nil
		f.mu.Unlock()
	}
	if owned {
		return false
	}
	f.t.Errorf("the fake %s got %s %s, an address no test gave out: a request from another package's test, on a port it closed and this server reused?", service, r.Method, r.URL.Path)
	http.NotFound(w, r)
	return true
}

func (f *fakes) serveModrinth(w http.ResponseWriter, r *http.Request) {
	if f.foreign("modrinth", w, r) {
		return
	}
	f.log("modrinth", r)
	f.mu.Lock()
	hook := f.hooks["modrinth "+r.URL.Path]
	f.mu.Unlock()
	if hook != nil {
		hook(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v2")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	q := r.URL.Query()
	switch {
	case r.Method != http.MethodGet:
		w.WriteHeader(http.StatusMethodNotAllowed)
	case p == "/search":
		serveFile(w, "testdata/modrinth/search-modpacks.json")
	case p == "/versions" || p == "/projects":
		have := f.versions
		if p == "/projects" {
			have = f.projects
		}
		out := []obj{}
		for _, id := range jsonStrings(q.Get("ids")) {
			if v := have[id]; v != nil {
				out = append(out, v)
			}
		}
		writeJSON(w, out)
	case len(parts) == 2 && parts[0] == "version" && f.versions[parts[1]] != nil:
		writeJSON(w, f.versions[parts[1]])
	case len(parts) == 2 && parts[0] == "project" && f.projects[parts[1]] != nil:
		writeJSON(w, f.projects[parts[1]])
	case len(parts) == 3 && parts[0] == "project" && parts[2] == "version" && f.projects[parts[1]] != nil:
		pid := f.projects[parts[1]]["id"]
		loaders, gvs := jsonStrings(q.Get("loaders")), jsonStrings(q.Get("game_versions"))
		out := []obj{}
		for _, id := range f.order {
			v := f.versions[id]
			if v["project_id"] == pid && overlaps(strs(v["loaders"]), loaders) && overlaps(strs(v["game_versions"]), gvs) {
				out = append(out, v)
			}
		}
		writeJSON(w, out)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func overlaps(have, want []string) bool {
	return len(want) == 0 || slices.ContainsFunc(have, func(s string) bool { return slices.Contains(want, s) })
}

func (f *fakes) serveFiles(service string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if f.foreign(service, w, r) {
			return
		}
		f.log(service, r)
		f.mu.Lock()
		hook := f.hooks[r.URL.Path]
		data, ok := f.files[r.Host+r.URL.Path]
		f.mu.Unlock()
		switch {
		case hook != nil:
			hook(w, r)
		case !ok:
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data)
		}
	}
}

// serveGitHub redirects /release/<path> to <path> on the assets host and
// /elsewhere/<path> to the evil host, like GitHub's release downloads.
func (f *fakes) serveGitHub(w http.ResponseWriter, r *http.Request) {
	if f.foreign("github", w, r) {
		return
	}
	f.log("github", r)
	if rest, ok := strings.CutPrefix(r.URL.Path, "/release"); ok {
		http.Redirect(w, r, f.url(f.assets, rest), http.StatusFound)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/elsewhere"); ok {
		http.Redirect(w, r, f.url(f.evil, rest), http.StatusFound)
		return
	}
	http.NotFound(w, r)
}

// cfProject is a made-up CurseForge project and file for one entry of the
// Fabulously Optimized manifest in curseforge/testdata.
type cfProject struct {
	id, file     int64
	name, slug   string
	class        int
	fileName     string
	gameVersions []string
	noDownload   bool // the author forbids downloads outside CurseForge's app
}

func cfProjects() []cfProject {
	both := []string{"26.3", "Fabric", "Client", "Server"}
	return []cfProject{
		{306612, 8913512, "Fabric API", "fabric-api", curseforge.ClassMods, "fabric-api-0.141.0+26.3.jar", both, false},
		{394468, 8888037, "Sodium", "sodium", curseforge.ClassMods, "sodium-fabric-0.9.2+mc26.3.jar", []string{"26.3", "Fabric", "Client"}, false},
		{360438, 8895969, "Lithium", "lithium", curseforge.ClassMods, "lithium-fabric-0.25.3+mc26.3.jar", both, false},
		{459857, 7806040, "FerriteCore", "ferritecore", curseforge.ClassMods, "ferritecore-9.0.0-fabric.jar", []string{"26.3", "Fabric"}, true},
		{348521, 8884063, "Cloth Config API", "cloth-config", curseforge.ClassMods, "cloth-config-26.3.155-fabric.jar", both, false},
		{1037459, 8271471, "Text Placeholder API", "text-placeholder-api", curseforge.ClassMods, "placeholder-api-3.1.0+26.3.jar", both, false},
		{867964, 8917489, "Translations for Sodium", "translations-for-sodium", curseforge.ClassResourcePacks, "Translations-for-Sodium-26.3.zip", []string{"26.3"}, false},
		{855981, 8932469, "Chat Reporting Helper", "chat-reporting-helper", curseforge.ClassResourcePacks, "Chat-Reporting-Helper-26.3.zip", []string{"26.3"}, false},
	}
}

// cfPackMinecraft maps the example Fabric pack's files to the Minecraft
// version their manifest names.
func cfPackMinecraft() map[int64]string {
	return map[int64]string{9200001: "26.2", 9200002: "26.2", 9200003: "26.3"}
}

func (f *fakes) loadCurseForge() {
	var search obj
	f.decode("curseforge/testdata/search-modpacks.json", &search)
	for _, x := range list(search["data"]) {
		m := x.(obj)
		f.cfMods[int64(num(m["id"]))] = m
	}
	for _, p := range cfProjects() {
		section := "mc-mods"
		if p.class == curseforge.ClassResourcePacks {
			section = "texture-packs"
		}
		f.cfMods[p.id] = obj{
			"id": p.id, "gameId": curseforge.GameMinecraft, "name": p.name, "slug": p.slug, "classId": p.class, "isAvailable": true,
			"links": obj{"websiteUrl": "https://www.curseforge.com/minecraft/" + section + "/" + p.slug}, "allowModDistribution": !p.noDownload,
		}
		file := obj{
			"id": p.file, "gameId": curseforge.GameMinecraft, "modId": p.id, "isAvailable": true, "displayName": p.fileName,
			"fileName": p.fileName, "releaseType": curseforge.Release, "fileStatus": curseforge.StatusApproved,
			"fileDate": "2026-09-01T10:00:00Z", "gameVersions": toAny(p.gameVersions), "isServerPack": false,
		}
		f.cfFile(file, generated(p.fileName), !p.noDownload)
	}
	var files obj
	f.decode("curseforge/testdata/files-9100001.json", &files)
	mcs := cfPackMinecraft()
	for _, x := range list(files["data"]) {
		file := x.(obj)
		id := int64(num(file["id"]))
		var data []byte
		if mc := mcs[id]; mc != "" {
			data = f.cfPack(str(file["displayName"]), mc, nil)
		}
		f.cfFile(file, data, true)
	}
}

// cfAddFile serves a new release of the example pack for Minecraft 26.2,
// its manifest changed by edit, and returns the file's id.
func (f *fakes) cfAddFile(edit func(m obj), extra ...entry) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := int64(9200100 + len(f.cfFiles))
	name := fmt.Sprintf("Example Fabric Pack 3.0.%d", id)
	file := obj{
		"id": id, "gameId": curseforge.GameMinecraft, "modId": 9100001, "isAvailable": true, "displayName": name,
		"fileName": name + ".zip", "releaseType": curseforge.Release, "fileStatus": curseforge.StatusApproved,
		"fileDate": testNow.Add(-time.Hour).Format(time.RFC3339), "gameVersions": []any{"26.2", "Fabric"}, "isServerPack": false,
	}
	f.cfFile(file, f.cfPack(name, "26.2", edit, extra...), true)
	return id
}

// cfFile fills in a CurseForge file record's hashes, size and download
// address for data and serves data from the fake forgecdn.
func (f *fakes) cfFile(file obj, data []byte, downloadable bool) {
	id := int64(num(file["id"]))
	p := fmt.Sprintf("/files/%d/%d/%s", id/1000, id%1000, str(file["fileName"]))
	s1, m5 := sha1.Sum(data), md5.Sum(data)
	file["hashes"] = []any{obj{"value": hex.EncodeToString(s1[:]), "algo": curseforge.HashSHA1}, obj{"value": hex.EncodeToString(m5[:]), "algo": curseforge.HashMD5}}
	file["fileLength"] = len(data)
	file["downloadUrl"] = nil
	if downloadable {
		file["downloadUrl"] = f.forgecdn.URL + p
		f.put(f.forgecdn, p, data)
	}
	f.cfFiles[id] = file
}

// cfPack zips the example pack: the Fabulously Optimized manifest for
// Minecraft mc, changed by edit when it is not nil, and overrides for the
// server, the client and a server.properties.
func (f *fakes) cfPack(name, mc string, edit func(m obj), extra ...entry) []byte {
	var m obj
	f.decode("curseforge/testdata/manifest.json", &m)
	m["name"], m["version"] = "Example Fabric Pack", strings.TrimPrefix(name, "Example Fabric Pack ")
	m["minecraft"].(obj)["version"] = mc
	if edit != nil {
		edit(m)
	}
	entries := append([]entry{
		{name: curseforge.ManifestName, data: mustJSON(f.t, m)},
		{name: "modlist.html", data: []byte("<ul><li>Fabric API</li></ul>\n")},
		{name: "overrides/config/example.json", data: []byte(`{"pack": "` + name + `"}` + "\n")},
		{name: "overrides/options.txt", data: []byte("renderDistance:12\n")},
		{name: "overrides/server.properties", data: []byte("difficulty=hard\nmotd=Example\\: a pack\nserver-port=25570\nview-distance=8\n")},
	}, extra...)
	return zipOf(f.t, entries...)
}

// cfChange changes a CurseForge file record.
func (f *fakes) cfChange(id int64, change func(file obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.cfFiles[id])
}

// cfChangeMod changes a CurseForge project record.
func (f *fakes) cfChangeMod(id int64, change func(m obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.cfMods[id])
}

// cfServe changes what the fake forgecdn serves for a CurseForge file.
func (f *fakes) cfServe(id int64, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.parse(str(f.cfFiles[id]["downloadUrl"]))
	f.files[u.Host+u.Path] = data
}

func (f *fakes) serveCurseForge(w http.ResponseWriter, r *http.Request) {
	if f.foreign("curseforge", w, r) {
		return
	}
	body := f.log("curseforge", r)
	if r.Header.Get("x-api-key") != testKey {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1"), "/"), "/")
	id := func(i int) int64 {
		n, _ := strconv.ParseInt(parts[i], 10, 64)
		return n
	}
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case get && len(parts) == 2 && parts[0] == "mods" && parts[1] == "search":
		serveFile(w, "curseforge/testdata/search-modpacks.json")
	case get && len(parts) == 2 && parts[0] == "mods" && f.cfMods[id(1)] != nil:
		writeJSON(w, obj{"data": f.cfMods[id(1)]})
	case get && len(parts) == 3 && parts[0] == "mods" && parts[2] == "files" && f.cfMods[id(1)] != nil:
		out := []obj{}
		for _, file := range f.cfFiles {
			if int64(num(file["modId"])) == id(1) {
				out = append(out, file)
			}
		}
		slices.SortFunc(out, func(a, b obj) int { return strings.Compare(str(b["fileDate"]), str(a["fileDate"])) })
		writeJSON(w, obj{"data": out, "pagination": obj{"index": 0, "pageSize": 50, "resultCount": len(out), "totalCount": len(out)}})
	case get && len(parts) == 4 && parts[0] == "mods" && parts[2] == "files" && f.cfFiles[id(3)] != nil:
		writeJSON(w, obj{"data": f.cfFiles[id(3)]})
	case post && len(parts) == 1 && parts[0] == "mods":
		var req struct{ ModIDs []int64 }
		json.Unmarshal([]byte(body), &req)
		out := []obj{}
		for _, i := range req.ModIDs {
			if m := f.cfMods[i]; m != nil {
				out = append(out, m)
			}
		}
		writeJSON(w, obj{"data": out})
	case post && len(parts) == 2 && parts[0] == "mods" && parts[1] == "files":
		var req struct{ FileIDs []int64 }
		json.Unmarshal([]byte(body), &req)
		out := []obj{}
		for _, i := range req.FileIDs {
			if file := f.cfFiles[i]; file != nil {
				out = append(out, file)
			}
		}
		writeJSON(w, obj{"data": out})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakes) log(service string, r *http.Request) string {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request{service, r.Method, r.URL.Path, r.URL.Query(), string(body)})
	return string(body)
}

// served lists the requests a service got, as "METHOD path".
func (f *fakes) served(service string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		if r.service == service {
			out = append(out, r.method+" "+r.path)
		}
	}
	return out
}

// lastQuery is the query of the last request a service got.
func (f *fakes) lastQuery(service string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range slices.Backward(f.requests) {
		if r.service == service {
			return r.query
		}
	}
	return nil
}

// lastQueryAt is the query of the last request a service got at path.
func (f *fakes) lastQueryAt(service, path string) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range slices.Backward(f.requests) {
		if r.service == service && r.path == path {
			return r.query
		}
	}
	return nil
}

// localOnly refuses every request that is not for a local test server, so
// no test can reach the network.
type localOnly struct {
	rt http.RoundTripper
	f  *fakes
}

func (l localOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if h := r.URL.Hostname(); h != "127.0.0.1" && h != "::1" {
		if r.Body != nil {
			r.Body.Close()
		}
		l.f.mu.Lock()
		l.f.offHosts = append(l.f.offHosts, r.URL.Host)
		l.f.mu.Unlock()
		return nil, errors.New("tests do not reach " + r.URL.Host)
	}
	return l.rt.RoundTrip(r)
}

func (f *fakes) decode(p string, v any) {
	f.t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		f.t.Fatalf("%s: %v", p, err)
	}
}

func (f *fakes) parse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// zipOf builds a zip of entries in order. Names are used as they are, so a
// test can build archives Go's own tools would refuse to make.
func zipOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: testNow}
		switch {
		case e.mode != 0:
			h.SetMode(e.mode)
		case strings.HasSuffix(e.name, "/"):
			h.SetMode(fs.ModeDir | 0o755)
		default:
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func serveFile(w http.ResponseWriter, p string) {
	b, err := os.ReadFile(p)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func jsonStrings(s string) []string {
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	switch n := v.(type) {
	case json.Number:
		f, _ := n.Float64()
		return f
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func strs(v any) []string {
	var out []string
	for _, x := range list(v) {
		out = append(out, str(x))
	}
	return out
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func newServer(t *testing.T, typ, mc string) Server {
	return Server{Server: addons.Server{Dir: t.TempDir(), Type: typ, MinecraftVersion: mc}}
}

func mustPlan(t *testing.T, l *Library, srv Server, req InstallRequest) *Plan {
	t.Helper()
	p, err := l.PlanInstall(context.Background(), srv, nil, req)
	if err != nil {
		t.Fatalf("planning %s %s: %v", req.Project, req.Version, err)
	}
	return p
}

// mustInstall plans an install and carries it out as planned.
func mustInstall(t *testing.T, l *Library, srv Server, req InstallRequest) *Result {
	t.Helper()
	req.Fingerprint = mustPlan(t, l, srv, req).Fingerprint
	res, err := l.Install(context.Background(), srv, nil, req)
	if err != nil {
		t.Fatalf("installing %s %s: %v", req.Project, req.Version, err)
	}
	return res
}

func wantKind(t *testing.T, err error, k addons.Kind) *addons.Error {
	t.Helper()
	var e *addons.Error
	if !errors.As(err, &e) || e.Kind != k {
		t.Fatalf("error %v (kind %q), want kind %q", err, addons.KindOf(err), k)
	}
	return e
}

func wantList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// changeList is "action path" for each change, marked when the file is the
// user's or goes into a new world.
func changeList(cs []Change) []string {
	out := []string{}
	for _, c := range cs {
		s := string(c.Action) + " " + c.Path
		if c.Yours {
			s += " (yours)"
		}
		if c.World {
			s += " (world)"
		}
		out = append(out, s)
	}
	return out
}

func skippedList(ss []Skipped) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, string(s.Reason)+" "+s.Path)
	}
	return out
}

func noticeList(ns []addons.Notice) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, string(n.Kind)+": "+n.Msg)
	}
	return out
}

// wantTree compares everything in dir, folders with a trailing slash, with
// want in any order.
func wantTree(t *testing.T, dir string, want ...string) {
	t.Helper()
	got := []string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel = filepath.ToSlash(rel); d.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	wantList(t, "files in "+dir, got, want...)
}

func writeFile(t *testing.T, srv Server, rel, data string) {
	t.Helper()
	p := filepath.Join(srv.Dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, srv Server, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(srv.Dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sameJSON compares values the way the panel sees them.
func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.MarshalIndent(got, "", "  ")
	w, _ := json.MarshalIndent(want, "", "  ")
	if !bytes.Equal(g, w) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, g, w)
	}
}

// roundTrip passes v through JSON, as a caller storing it does.
func roundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func sha512hex(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

func sha1hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}
