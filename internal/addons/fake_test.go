package addons

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

type obj = map[string]any

var testNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// fakes serves the real answers in modrinth/testdata and hangar/testdata
// from local TLS servers. The files they list are replaced by small
// generated jars on a fake CDN, with hashes, sizes and URLs rewritten to
// match; everything else is what the sources sent.
type fakes struct {
	t                           *testing.T
	modrinth, hangar, cdn, evil *httptest.Server
	client                      *http.Client

	mu        sync.Mutex
	mProjects map[string]obj // by id and by slug
	mVersions map[string]obj
	mOrder    []string       // version ids in fixture order
	hProjects map[string]obj // by id and by slug
	hVersions map[string]obj
	hOrder    []string
	hListed   func(v obj)       // changes each version Hangar's version list serves
	files     map[string][]byte // by CDN path
	hooks     map[string]http.HandlerFunc
	requests  []request
	evilHits  int
	offHosts  []string
	busy      bool // Modrinth answers 429
}

type request struct {
	service, method, path string
	query                 url.Values
	body, userAgent       string
}

func newFakes(t *testing.T) *fakes {
	t.Helper()
	f := &fakes{t: t, mProjects: map[string]obj{}, mVersions: map[string]obj{}, hProjects: map[string]obj{}, hVersions: map[string]obj{},
		files: map[string][]byte{}, hooks: map[string]http.HandlerFunc{}}
	f.modrinth = httptest.NewTLSServer(http.HandlerFunc(f.serveModrinth))
	f.hangar = httptest.NewTLSServer(http.HandlerFunc(f.serveHangar))
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(f.serveCDN))
	f.evil = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.evilHits++
		f.mu.Unlock()
		w.Write([]byte("not an add-on"))
	}))
	for _, s := range []*httptest.Server{f.modrinth, f.hangar, f.cdn, f.evil} {
		t.Cleanup(s.Close)
	}
	// Every httptest TLS server uses the same certificate, so one client
	// trusts them all.
	f.client = &http.Client{Transport: localOnly{f.modrinth.Client().Transport, f}}

	for _, path := range f.glob("modrinth/testdata/project-*.json") {
		var p obj
		f.decode(path, &p)
		f.mProjects[str(p["id"])], f.mProjects[str(p["slug"])] = p, p
	}
	for _, path := range f.glob("modrinth/testdata/versions-*.json") {
		var vs []obj
		f.decode(path, &vs)
		for _, v := range vs {
			f.addModrinthVersion(v)
		}
	}
	for _, path := range f.glob("hangar/testdata/project-*.json") {
		var p obj
		f.decode(path, &p)
		ns, _ := p["namespace"].(obj)
		f.hProjects[num(p["id"])], f.hProjects[str(ns["slug"])] = p, p
	}
	for _, path := range f.glob("hangar/testdata/versions-*.json") {
		var vl obj
		f.decode(path, &vl)
		for _, v := range list(vl["result"]) {
			f.addHangarVersion(v.(obj))
		}
	}
	return f
}

func (f *fakes) glob(pattern string) []string {
	paths, _ := filepath.Glob(pattern)
	if len(paths) == 0 {
		f.t.Fatalf("no fixtures match %s", pattern)
	}
	return paths
}

func (f *fakes) decode(path string, v any) {
	b, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		f.t.Fatalf("%s: %v", path, err)
	}
}

func (f *fakes) addModrinthVersion(v obj) {
	p := f.mProjects[str(v["project_id"])]
	for _, x := range list(v["files"]) {
		file := x.(obj)
		u := f.parse(str(file["url"]))
		data := fakeJar(f.t, u.Path, str(p["title"]), str(p["slug"]), str(v["version_number"]), strs(v["loaders"]))
		f.files[u.Path] = data
		s512, s1 := sha512.Sum512(data), sha1.Sum(data)
		file["hashes"] = obj{"sha512": hex.EncodeToString(s512[:]), "sha1": hex.EncodeToString(s1[:])}
		file["size"] = len(data)
		file["url"] = f.cdn.URL + u.EscapedPath()
	}
	f.mVersions[str(v["id"])] = v
	f.mOrder = append(f.mOrder, str(v["id"]))
}

func (f *fakes) addHangarVersion(v obj) {
	p := f.hProjects[num(v["projectId"])]
	dls, _ := v["downloads"].(obj)
	for _, x := range dls {
		d := x.(obj)
		fi, _ := d["fileInfo"].(obj)
		if fi == nil || str(d["downloadUrl"]) == "" {
			continue
		}
		// Platforms share one file, and then one URL.
		u := f.parse(str(d["downloadUrl"]))
		data, ok := f.files[u.Path]
		if !ok {
			data = fakeJar(f.t, u.Path, str(p["name"]), strings.ToLower(str(p["name"])), str(v["name"]), []string{"paper"})
			f.files[u.Path] = data
		}
		sum := sha256.Sum256(data)
		fi["sha256Hash"], fi["sizeBytes"] = hex.EncodeToString(sum[:]), len(data)
		d["downloadUrl"] = f.cdn.URL + u.EscapedPath()
	}
	f.hVersions[num(v["id"])] = v
	f.hOrder = append(f.hOrder, num(v["id"]))
}

func (f *fakes) parse(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fakes) log(service string, r *http.Request) string {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request{service, r.Method, r.URL.Path, r.URL.Query(), string(body), r.UserAgent()})
	return string(body)
}

func (f *fakes) serveModrinth(w http.ResponseWriter, r *http.Request) {
	body := f.log("modrinth", r)
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("X-Ratelimit-Limit", "300")
	if f.busy {
		w.Header().Set("X-Ratelimit-Remaining", "0")
		w.Header().Set("X-Ratelimit-Reset", "42")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	w.Header().Set("X-Ratelimit-Remaining", "299")
	w.Header().Set("X-Ratelimit-Reset", "60")
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	q := r.URL.Query()
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case get && path == "/search":
		// The Fabric fixture was fetched without the server_side facet, so
		// it has client-only mods for the library to leave out.
		name := "search-plugins-paper-26.2.json"
		if strings.Contains(q.Get("facets"), `"categories:fabric"`) {
			name = "search-mods-fabric-26.2.json"
		}
		serveFile(w, http.StatusOK, "modrinth/testdata/"+name)
	case get && path == "/projects":
		out := []obj{}
		for _, id := range jsonStrings(q.Get("ids")) {
			if p := f.mProjects[id]; p != nil && !slices.ContainsFunc(out, func(o obj) bool { return o["id"] == p["id"] }) {
				out = append(out, p)
			}
		}
		writeJSON(w, http.StatusOK, out)
	case get && path == "/versions":
		out := []obj{}
		for _, id := range jsonStrings(q.Get("ids")) {
			if v := f.mVersions[id]; v != nil {
				out = append(out, v)
			}
		}
		writeJSON(w, http.StatusOK, out)
	case get && len(parts) == 2 && parts[0] == "version" && f.mVersions[parts[1]] != nil:
		writeJSON(w, http.StatusOK, f.mVersions[parts[1]])
	case get && len(parts) == 2 && parts[0] == "project" && f.mProjects[parts[1]] != nil:
		writeJSON(w, http.StatusOK, f.mProjects[parts[1]])
	case get && len(parts) == 3 && parts[0] == "project" && parts[2] == "version" && f.mProjects[parts[1]] != nil:
		pid := f.mProjects[parts[1]]["id"]
		loaders, gvs := jsonStrings(q.Get("loaders")), jsonStrings(q.Get("game_versions"))
		out := []obj{}
		for _, id := range f.mOrder {
			v := f.mVersions[id]
			if v["project_id"] == pid && matches(strs(v["loaders"]), loaders) && matches(strs(v["game_versions"]), gvs) {
				out = append(out, v)
			}
		}
		writeJSON(w, http.StatusOK, out)
	case post && path == "/version_files":
		var req struct {
			Hashes    []string
			Algorithm string
		}
		json.Unmarshal([]byte(body), &req)
		out := obj{}
		for _, h := range req.Hashes {
			if v := f.byHash(req.Algorithm, h); v != nil {
				out[h] = v
			}
		}
		writeJSON(w, http.StatusOK, out)
	case post && path == "/version_files/update":
		var req struct {
			Hashes       []string
			Algorithm    string
			Loaders      []string
			GameVersions []string `json:"game_versions"`
			VersionTypes []string `json:"version_types"`
		}
		json.Unmarshal([]byte(body), &req)
		out := obj{}
		for _, h := range req.Hashes {
			v := f.byHash(req.Algorithm, h)
			if v == nil {
				continue
			}
			var newest obj
			for _, id := range f.mOrder {
				c := f.mVersions[id]
				if c["project_id"] != v["project_id"] || !matches(strs(c["loaders"]), req.Loaders) || !matches(strs(c["game_versions"]), req.GameVersions) ||
					len(req.VersionTypes) > 0 && !slices.Contains(req.VersionTypes, str(c["version_type"])) {
					continue
				}
				if newest == nil || published(c, "date_published").After(published(newest, "date_published")) {
					newest = c
				}
			}
			if newest != nil {
				out[h] = newest
			}
		}
		writeJSON(w, http.StatusOK, out)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakes) byHash(algo, h string) obj {
	for _, id := range f.mOrder {
		v := f.mVersions[id]
		for _, x := range list(v["files"]) {
			if hs, _ := x.(obj)["hashes"].(obj); hs != nil && str(hs[algo]) == h {
				return v
			}
		}
	}
	return nil
}

func (f *fakes) serveHangar(w http.ResponseWriter, r *http.Request) {
	f.log("hangar", r)
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1"), "/"), "/")
	q := r.URL.Query()
	switch {
	case len(parts) == 1 && parts[0] == "projects":
		serveFile(w, http.StatusOK, "hangar/testdata/search-paper-26.2.json")
	case len(parts) == 2 && parts[0] == "projects" && f.hProjects[parts[1]] != nil:
		writeJSON(w, http.StatusOK, f.hProjects[parts[1]])
	case len(parts) == 3 && parts[0] == "projects" && parts[2] == "versions" && f.hProjects[parts[1]] != nil:
		pid := num(f.hProjects[parts[1]]["id"])
		platform, pv, channel := q.Get("platform"), q.Get("platformVersion"), q.Get("channel")
		all := []obj{}
		for _, id := range f.hOrder {
			v := f.hVersions[id]
			dls, _ := v["downloads"].(obj)
			deps, _ := v["platformDependencies"].(obj)
			ch, _ := v["channel"].(obj)
			switch {
			case num(v["projectId"]) != pid:
			case platform != "" && dls[platform] == nil:
			case platform != "" && pv != "" && !slices.Contains(strs(deps[platform]), pv):
			case channel != "" && !strings.EqualFold(str(ch["name"]), channel):
			default:
				if f.hListed != nil {
					f.hListed(v)
				}
				all = append(all, v)
			}
		}
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			limit = 10
		}
		page := all[min(offset, len(all)):min(offset+limit, len(all))]
		writeJSON(w, http.StatusOK, obj{"pagination": obj{"count": len(all), "limit": limit, "offset": offset}, "result": page})
	case len(parts) == 2 && parts[0] == "versions" && f.hVersions[parts[1]] != nil:
		writeJSON(w, http.StatusOK, f.hVersions[parts[1]])
	default:
		serveFile(w, http.StatusNotFound, "hangar/testdata/error-404.json")
	}
}

func (f *fakes) serveCDN(w http.ResponseWriter, r *http.Request) {
	f.log("cdn", r)
	f.mu.Lock()
	hook := f.hooks[r.URL.Path]
	data, ok := f.files[r.URL.Path]
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

// library returns a Library that reaches only the fakes. Its clock stands
// still, so a rate-limit pause never runs out during a test.
func (f *fakes) library() *Library {
	cdn := fetch.Hosts{f.cdn.Listener.Addr().String()}
	now := func() time.Time { return testNow }
	return &Library{
		Modrinth:      modrinth.New(fetch.Options{BaseURL: f.modrinth.URL + "/v2", HTTP: f.client, Now: now}),
		Hangar:        hangar.New(fetch.Options{BaseURL: f.hangar.URL + "/api/v1", HTTP: f.client, Now: now}),
		HTTP:          f.client,
		Now:           now,
		TempDir:       f.t.TempDir(),
		ModrinthFiles: cdn,
		HangarFiles:   cdn,
		IconHosts:     cdn,
	}
}

type fakeFile struct {
	name, path string
	data       []byte
}

// mfile is the primary file of a Modrinth version.
func (f *fakes) mfile(versionID string) fakeFile {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range list(f.mVersions[versionID]["files"]) {
		if file := x.(obj); file["primary"] == true {
			return fakeFile{str(file["filename"]), f.parse(str(file["url"])).Path, f.files[f.parse(str(file["url"])).Path]}
		}
	}
	f.t.Fatalf("the fixtures have no Modrinth version %s", versionID)
	return fakeFile{}
}

// hfile is the Paper download of a Hangar version.
func (f *fakes) hfile(versionID string) fakeFile {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	dls, _ := f.hVersions[versionID]["downloads"].(obj)
	d, _ := dls["PAPER"].(obj)
	fi, _ := d["fileInfo"].(obj)
	if fi == nil {
		f.t.Fatalf("the fixtures have no Hangar file for version %s", versionID)
	}
	path := f.parse(str(d["downloadUrl"])).Path
	return fakeFile{str(fi["name"]), path, f.files[path]}
}

func (f *fakes) patchVersion(versionID string, fn func(v obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.mVersions[versionID])
}

func (f *fakes) patchFile(versionID string, fn func(file obj)) {
	f.patchVersion(versionID, func(v obj) {
		for _, x := range list(v["files"]) {
			if file := x.(obj); file["primary"] == true {
				fn(file)
			}
		}
	})
}

func (f *fakes) patchHangarVersion(versionID string, fn func(v obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.hVersions[versionID])
}

// onHangarList has fn change each version as Hangar's version list serves
// it, as the real one reorders dependencies from one request to the next.
func (f *fakes) onHangarList(fn func(v obj)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hListed = fn
}

func (f *fakes) addModrinthProject(p obj) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mProjects[str(p["id"])], f.mProjects[str(p["slug"])] = p, p
}

// hook replaces what the CDN serves at path.
func (f *fakes) hook(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hooks[path] = h
}

func (f *fakes) setBusy(busy bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy = busy
}

func (f *fakes) sent(service string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.requests {
		if r.service == service {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakes) sentTo(service, path string) []request {
	return slices.DeleteFunc(f.sent(service), func(r request) bool { return r.path != path })
}

// strays lists requests that went where they must not: to the evil server,
// or away from the local test servers.
func (f *fakes) strays() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.offHosts)
	for range f.evilHits {
		out = append(out, f.evil.URL)
	}
	return out
}

// fakeJar makes a jar with the descriptors of the given loaders, and the
// path it is served at so that every file differs.
func fakeJar(t testing.TB, path, name, id, version string, loaders []string) []byte {
	entries := map[string]string{"playkeeper-test.txt": path}
	if overlaps(loaders, []string{"paper", "spigot", "bukkit", "folia", "purpur"}) {
		entries["plugin.yml"] = "name: " + name + "\nversion: " + version + "\nmain: test.Main\n"
	}
	switch {
	case slices.Contains(loaders, "fabric"):
		entries["fabric.mod.json"] = jsonText(obj{"schemaVersion": 1, "id": id, "name": name, "version": version})
	case slices.Contains(loaders, "quilt"):
		entries["quilt.mod.json"] = jsonText(obj{"quilt_loader": obj{"id": id, "version": version, "metadata": obj{"name": name}}})
	}
	if slices.Contains(loaders, "neoforge") {
		entries["META-INF/neoforge.mods.toml"] = "[[mods]]\nmodId=\"" + id + "\"\nversion=\"" + version + "\"\ndisplayName=\"" + name + "\"\n"
	}
	return makeJar(t, entries)
}

func makeJar(t testing.TB, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, entries[name])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pluginJar(t testing.TB, name, version string) []byte {
	return makeJar(t, map[string]string{"plugin.yml": "name: " + name + "\nversion: " + version + "\nmain: test.Main\n"})
}

// mdep is a Modrinth dependency on a project, or on a file when projectID
// is empty.
func mdep(typ, projectID, fileName string) obj {
	d := obj{"version_id": nil, "project_id": nil, "file_name": nil, "dependency_type": typ}
	if projectID != "" {
		d["project_id"] = projectID
	}
	if fileName != "" {
		d["file_name"] = fileName
	}
	return d
}

func serveBytes(data []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Write(data)
	}
}

// sameJSON compares values the way the panel will see them.
func sameJSON(t *testing.T, what string, got, want any) {
	t.Helper()
	g, _ := json.MarshalIndent(got, "", "  ")
	w, _ := json.MarshalIndent(want, "", "  ")
	if !bytes.Equal(g, w) {
		t.Errorf("%s:\ngot  %s\nwant %s", what, g, w)
	}
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) string {
	n, _ := v.(json.Number)
	return n.String()
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

func jsonStrings(s string) []string {
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}

func published(v obj, field string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, str(v[field]))
	return t
}

// matches is how the sources filter lists: no filter, or a value in common.
func matches(have, want []string) bool { return len(want) == 0 || overlaps(have, want) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func serveFile(w http.ResponseWriter, status int, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		status, b = http.StatusInternalServerError, nil
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func newServer(t *testing.T, typ, mc string) Server {
	return Server{Dir: t.TempDir(), Type: typ, MinecraftVersion: mc}
}

// ls lists a folder with hidden entries; nil when it is empty or missing.
func ls(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha512hex(b []byte) string {
	s := sha512.Sum512(b)
	return hex.EncodeToString(s[:])
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func wantKind(t *testing.T, err error, k Kind) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Kind != k {
		t.Fatalf("error %v (kind %q), want kind %q", err, KindOf(err), k)
	}
	return e
}

// stepList is "name version versionID" for each step of a plan.
func stepList(p *Plan) string {
	var out []string
	for _, s := range p.Steps {
		out = append(out, s.Name+" "+s.VersionNumber+" "+s.VersionID)
	}
	return strings.Join(out, ", ")
}

func mustInstall(t *testing.T, l *Library, srv Server, installed []Installed, req InstallRequest) []Installed {
	t.Helper()
	res, err := l.Install(context.Background(), srv, installed, req)
	if err != nil {
		t.Fatalf("installing %s: %v", req.Project, err)
	}
	return append(slices.Clone(installed), res.Installed...)
}
