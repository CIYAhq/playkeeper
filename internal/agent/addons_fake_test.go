package agent

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
)

// fakeSources is a small Modrinth (its API and its file host on one local
// TLS server) and a Hangar with no plugins, so the add-on routes run end to
// end without the network.
type fakeSources struct {
	t        *testing.T
	modrinth *httptest.Server
	hangar   *httptest.Server
	client   *http.Client

	mu       sync.Mutex
	projects []*fakeProject
	versions []*fakeVersion // in the order they were published
	icons    map[string][]byte
	hits     map[string]int // "METHOD path"
}

type fakeProject struct {
	id, slug, title, summary string
	downloads                int64
}

type fakeVersion struct {
	id, project, number, file string
	published                 time.Time
	requires                  []string // project ids
	data                      []byte
}

func newFakeSources(t *testing.T) *fakeSources {
	t.Helper()
	f := &fakeSources{t: t, icons: map[string][]byte{}, hits: map[string]int{}}
	f.modrinth = httptest.NewTLSServer(http.HandlerFunc(f.serveModrinth))
	f.hangar = httptest.NewTLSServer(http.HandlerFunc(f.serveHangar))
	t.Cleanup(f.modrinth.Close)
	t.Cleanup(f.hangar.Close)
	// httptest's TLS servers share one certificate, so one client trusts both.
	f.client = &http.Client{Transport: loopbackOnly{f.modrinth.Client().Transport}}

	f.addProject(&fakeProject{id: "mvcore00", slug: "multiverse-core", title: "Multiverse-Core", summary: "More than one world on a server.", downloads: 900000})
	f.addProject(&fakeProject{id: "mvportal", slug: "multiverse-portals", title: "Multiverse-Portals", summary: "Portals between your worlds.", downloads: 400000})
	f.addProject(&fakeProject{id: "fALzjamp", slug: "chunky", title: "Chunky", summary: "Pre-generates chunks, quickly and efficiently.", downloads: 800000})
	f.publish("mvcore00", "mvc-v1", "5.0.1", time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	f.publish("mvportal", "mvp-v1", "5.0.0", time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC), "mvcore00")
	f.publish("fALzjamp", "chunky-v1", "1.4.40", time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
	f.icons["/icons/mvcore00.png"] = tinyPNG(t)
	f.icons["/icons/mvportal.png"] = tinyPNG(t)
	f.icons["/icons/chunky.png"] = tinyPNG(t)
	f.icons["/icons/evil.svg"] = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	return f
}

// library is an add-on library that reaches only the fakes.
func (f *fakeSources) library(tempDir string) *addons.Library {
	files := fetch.Hosts{f.modrinth.Listener.Addr().String()}
	return &addons.Library{
		Modrinth:      modrinth.New(fetch.Options{BaseURL: f.modrinth.URL + "/v2", HTTP: f.client}),
		Hangar:        hangar.New(fetch.Options{BaseURL: f.hangar.URL + "/api/v1", HTTP: f.client}),
		HTTP:          f.client,
		TempDir:       tempDir,
		ModrinthFiles: files,
		HangarFiles:   files,
		IconHosts:     files,
	}
}

func (f *fakeSources) addProject(p *fakeProject) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.projects = append(f.projects, p)
}

// publish adds a version of a project, with a plugin jar named after it.
func (f *fakeSources) publish(project, id, number string, at time.Time, requires ...string) *fakeVersion {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.project(project)
	v := &fakeVersion{id: id, project: project, number: number, published: at, requires: requires,
		file: p.title + "-" + number + ".jar", data: pluginJar(f.t, p.title, number)}
	f.versions = append(f.versions, v)
	return v
}

// jar is a published version's file.
func (f *fakeSources) jar(versionID string) (name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.versions {
		if v.id == versionID {
			return v.file, v.data
		}
	}
	f.t.Fatalf("no version %s", versionID)
	return "", nil
}

func (f *fakeSources) hitCount(method, path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[method+" "+path]
}

func (f *fakeSources) project(ref string) *fakeProject {
	for _, p := range f.projects {
		if p.id == ref || p.slug == ref {
			return p
		}
	}
	return nil
}

func (f *fakeSources) version(id string) *fakeVersion {
	for _, v := range f.versions {
		if v.id == id {
			return v
		}
	}
	return nil
}

func (f *fakeSources) byHash(algo, h string) *fakeVersion {
	for _, v := range f.versions {
		s512, s1 := sha512.Sum512(v.data), sha1.Sum(v.data)
		if algo == "sha512" && hex.EncodeToString(s512[:]) == h || algo == "sha1" && hex.EncodeToString(s1[:]) == h {
			return v
		}
	}
	return nil
}

func (f *fakeSources) iconURL(p *fakeProject) string {
	name := p.id
	if p.id == "fALzjamp" {
		name = "chunky"
	}
	return f.modrinth.URL + "/icons/" + name + ".png"
}

func (f *fakeSources) projectJSON(p *fakeProject) map[string]any {
	ids := []string{}
	updated := time.Time{}
	for _, v := range f.versions {
		if v.project == p.id {
			ids = append(ids, v.id)
			updated = v.published
		}
	}
	return map[string]any{
		"id": p.id, "slug": p.slug, "project_type": "plugin", "title": p.title, "description": p.summary,
		"categories": []string{"bukkit", "paper", "management"}, "loaders": []string{"bukkit", "paper"},
		"game_versions": []string{"26.1.2"}, "client_side": "unsupported", "server_side": "required", "status": "approved",
		"downloads": p.downloads, "icon_url": f.iconURL(p), "license": map[string]any{"id": "BSD-3-Clause"},
		"updated": updated.Format(time.RFC3339), "versions": ids,
	}
}

func (f *fakeSources) hitJSON(p *fakeProject) map[string]any {
	j := f.projectJSON(p)
	return map[string]any{
		"project_id": p.id, "project_type": "plugin", "slug": p.slug, "author": "sample-author", "title": p.title,
		"description": p.summary, "categories": j["categories"], "display_categories": []string{"management"},
		"versions": j["game_versions"], "downloads": p.downloads, "icon_url": j["icon_url"], "date_modified": j["updated"],
		"license": "BSD-3-Clause", "client_side": "unsupported", "server_side": "required",
	}
}

func (f *fakeSources) versionJSON(v *fakeVersion) map[string]any {
	s512, s1 := sha512.Sum512(v.data), sha1.Sum(v.data)
	deps := []map[string]any{}
	for _, r := range v.requires {
		deps = append(deps, map[string]any{"project_id": r, "dependency_type": "required"})
	}
	return map[string]any{
		"id": v.id, "project_id": v.project, "name": v.number, "version_number": v.number, "version_type": "release",
		"status": "listed", "game_versions": []string{"26.1.2"}, "loaders": []string{"bukkit", "paper"},
		"date_published": v.published.Format(time.RFC3339),
		"files": []map[string]any{{
			"hashes":   map[string]string{"sha512": hex.EncodeToString(s512[:]), "sha1": hex.EncodeToString(s1[:])},
			"url":      f.modrinth.URL + "/cdn/" + v.id + "/" + v.file,
			"filename": v.file, "primary": true, "size": len(v.data),
		}},
		"dependencies": deps, "changelog": "## Changes\n- Portals remember where they lead.",
	}
}

func (f *fakeSources) serveModrinth(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[r.Method+" "+r.URL.Path]++
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	if get && strings.HasPrefix(r.URL.Path, "/cdn/") {
		for _, v := range f.versions {
			if r.URL.Path == "/cdn/"+v.id+"/"+v.file {
				w.Header().Set("Content-Length", strconv.Itoa(len(v.data)))
				w.Write(v.data)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if get && strings.HasPrefix(r.URL.Path, "/icons/") {
		if b, ok := f.icons[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	q := r.URL.Query()
	switch {
	case get && path == "/search":
		var facets [][]string
		json.Unmarshal([]byte(q.Get("facets")), &facets)
		var only []string
		for _, group := range facets {
			for _, facet := range group {
				if id, ok := strings.CutPrefix(facet, "project_id:"); ok {
					only = append(only, id)
				}
			}
		}
		text := strings.ToLower(q.Get("query"))
		hits := []map[string]any{}
		for _, p := range f.projects {
			if len(only) > 0 && !slices.Contains(only, p.id) || !strings.Contains(strings.ToLower(p.title), text) {
				continue
			}
			hits = append(hits, f.hitJSON(p))
		}
		fakeJSON(w, http.StatusOK, map[string]any{"hits": hits, "offset": 0, "limit": 25, "total_hits": len(hits)})
	case get && path == "/projects":
		out := []map[string]any{}
		for _, id := range jsonList(q.Get("ids")) {
			if p := f.project(id); p != nil {
				out = append(out, f.projectJSON(p))
			}
		}
		fakeJSON(w, http.StatusOK, out)
	case get && path == "/versions":
		out := []map[string]any{}
		for _, id := range jsonList(q.Get("ids")) {
			if v := f.version(id); v != nil {
				out = append(out, f.versionJSON(v))
			}
		}
		fakeJSON(w, http.StatusOK, out)
	case get && len(parts) == 2 && parts[0] == "version" && f.version(parts[1]) != nil:
		fakeJSON(w, http.StatusOK, f.versionJSON(f.version(parts[1])))
	case get && len(parts) == 2 && parts[0] == "project" && f.project(parts[1]) != nil:
		fakeJSON(w, http.StatusOK, f.projectJSON(f.project(parts[1])))
	case get && len(parts) == 3 && parts[0] == "project" && parts[2] == "version" && f.project(parts[1]) != nil:
		id := f.project(parts[1]).id
		out := []map[string]any{}
		for i := len(f.versions) - 1; i >= 0; i-- {
			if v := f.versions[i]; v.project == id {
				out = append(out, f.versionJSON(v))
			}
		}
		fakeJSON(w, http.StatusOK, out)
	case post && path == "/version_files":
		var req struct {
			Hashes    []string `json:"hashes"`
			Algorithm string   `json:"algorithm"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{}
		for _, h := range req.Hashes {
			if v := f.byHash(req.Algorithm, h); v != nil {
				out[h] = f.versionJSON(v)
			}
		}
		fakeJSON(w, http.StatusOK, out)
	case post && path == "/version_files/update":
		var req struct {
			Hashes    []string `json:"hashes"`
			Algorithm string   `json:"algorithm"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]any{}
		for _, h := range req.Hashes {
			v := f.byHash(req.Algorithm, h)
			if v == nil {
				continue
			}
			newest := v
			for _, c := range f.versions {
				if c.project == v.project && c.published.After(newest.published) {
					newest = c
				}
			}
			out[h] = f.versionJSON(newest)
		}
		fakeJSON(w, http.StatusOK, out)
	default:
		fakeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found", "description": "the requested route does not exist"})
	}
}

func (f *fakeSources) serveHangar(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits["hangar "+r.Method+" "+r.URL.Path]++
	f.mu.Unlock()
	if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects" {
		fakeJSON(w, http.StatusOK, map[string]any{"pagination": map[string]any{"limit": 25, "offset": 0, "count": 0}, "result": []any{}})
		return
	}
	fakeJSON(w, http.StatusNotFound, map[string]any{"message": "Not found", "isHangarApiException": true, "httpError": map[string]any{"statusCode": 404}})
}

// loopbackOnly refuses every request that is not for a local test server.
type loopbackOnly struct{ rt http.RoundTripper }

func (l loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	if h := r.URL.Hostname(); h != "127.0.0.1" && h != "::1" {
		if r.Body != nil {
			r.Body.Close()
		}
		return nil, errors.New("tests do not reach " + r.URL.Host)
	}
	return l.rt.RoundTrip(r)
}

func fakeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonList(s string) []string {
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}

func pluginJar(t testing.TB, name, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("plugin.yml")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("name: " + name + "\nversion: " + version + "\nmain: test.Main\n"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tinyPNG(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
