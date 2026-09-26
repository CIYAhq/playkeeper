package modrinth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

type fakeModrinth struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   []string
	slugs    map[string]string // project id or slug -> fixture slug
}

func (f *fakeModrinth) last() (*http.Request, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1], f.bodies[len(f.bodies)-1]
}

func startFake(t *testing.T) (*fakeModrinth, *Client) {
	t.Helper()
	f := &fakeModrinth{slugs: map[string]string{}}
	files, _ := filepath.Glob("testdata/project-*.json")
	for _, p := range files {
		var pr Project
		b, _ := os.ReadFile(p)
		if err := json.Unmarshal(b, &pr); err != nil {
			t.Fatal(err)
		}
		f.slugs[pr.ID], f.slugs[pr.Slug] = pr.Slug, pr.Slug
	}
	serve := func(w http.ResponseWriter, name string) {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		w.Header().Set("X-Ratelimit-Limit", "300")
		w.Header().Set("X-Ratelimit-Remaining", "299")
		w.Header().Set("X-Ratelimit-Reset", "0")
		path := strings.TrimPrefix(r.URL.Path, "/v2")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		switch {
		case path == "/search" && strings.HasPrefix(r.URL.Query().Get("facets"), "[[\"categories\"]"):
			w.WriteHeader(http.StatusBadRequest)
			serve(w, "error-400.json")
		case path == "/search":
			serve(w, "search-plugins-paper-26.2.json")
		case path == "/projects":
			serve(w, "projects.json")
		case path == "/version_files":
			serve(w, "version-files.json")
		case path == "/version_files/update":
			serve(w, "version-files-update.json")
		case len(parts) == 2 && parts[0] == "project" && f.slugs[parts[1]] != "":
			serve(w, "project-"+f.slugs[parts[1]]+".json")
		case len(parts) == 3 && parts[0] == "project" && parts[2] == "version" && f.slugs[parts[1]] != "":
			serve(w, "versions-"+f.slugs[parts[1]]+".json")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, New(fetch.Options{BaseURL: srv.URL + "/v2", HTTP: srv.Client()})
}

func TestSearchSendsFacetsAndTheUserAgentModrinthAsksFor(t *testing.T) {
	f, c := startFake(t)
	facets := [][]string{
		Facet("categories", "paper", "spigot", "bukkit"),
		Facet("versions", "26.2"),
		Facet("project_type", "plugin"),
		Facet("server_side", "required", "optional"),
	}
	res, err := c.Search(context.Background(), SearchQuery{Query: "chunk gen", Facets: facets, Index: "downloads", Limit: 5, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := f.last()
	q := r.URL.Query()
	if got := q.Get("facets"); got != `[["categories:paper","categories:spigot","categories:bukkit"],["versions:26.2"],["project_type:plugin"],["server_side:required","server_side:optional"]]` {
		t.Errorf("facets = %s", got)
	}
	if q.Get("query") != "chunk gen" || q.Get("index") != "downloads" || q.Get("limit") != "5" || q.Get("offset") != "10" {
		t.Errorf("query = %v", q)
	}
	if ua := r.Header.Get("User-Agent"); ua != "CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)" {
		t.Errorf("User-Agent = %q", ua)
	}
	if res.TotalHits != 4196 || len(res.Hits) != 5 {
		t.Fatalf("total %d, hits %d", res.TotalHits, len(res.Hits))
	}
	h := res.Hits[2]
	if h.ProjectID != "fALzjamp" || h.Slug != "chunky" || h.Title != "Chunky" || h.License != "GPL-3.0-only" ||
		h.ServerSide != "optional" || h.Downloads == 0 || !strings.HasPrefix(h.IconURL, "https://cdn.modrinth.com/") ||
		h.DateModified.IsZero() || len(h.Environment) == 0 {
		t.Errorf("hit = %+v", h)
	}
}

func TestProjectDecodesTheRealAnswer(t *testing.T) {
	_, c := startFake(t)
	p, err := c.Project(context.Background(), "chunky")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "fALzjamp" || p.License.ID != "GPL-3.0-only" || p.ServerSide != "optional" || p.Updated.Year() != 2026 ||
		!reflect.DeepEqual(p.Environment, []string{"client_or_server_prefers_both"}) || len(p.Versions) != 4 {
		t.Errorf("project = %+v", p)
	}
	byID, err := c.Project(context.Background(), "fALzjamp")
	if err != nil || byID.Slug != "chunky" {
		t.Errorf("by id: %+v, %v", byID, err)
	}
}

func TestProjectVersionsAsksForLoadersAndGameVersions(t *testing.T) {
	f, c := startFake(t)
	vs, err := c.ProjectVersions(context.Background(), "zfastnoise", VersionFilter{Loaders: []string{"fabric", "quilt"}, GameVersions: []string{"26.2"}})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := f.last()
	q := r.URL.Query()
	if q.Get("loaders") != `["fabric","quilt"]` || q.Get("game_versions") != `["26.2"]` || q.Get("include_changelog") != "false" {
		t.Errorf("query = %v", q)
	}
	v := vs[0]
	if v.ID != "RxI4fDfx" || v.VersionType != "beta" || v.Environment != "client_or_server_prefers_both" || len(v.Files) != 2 {
		t.Fatalf("version = %+v", v)
	}
	pf, ok := v.PrimaryFile()
	if !ok || pf.Filename != "zfastnoise-1.1.0-beta.6+26.2.jar" || pf.Size != 52156 ||
		pf.Hashes.SHA512 != "3e4d78c668ff02d91aebf5a9f60755f0e4320d63f53fb2a0a625f5584defe47c88fa50f71b656cbd5306628be27466e6a47add19b0d14cbdcce069aa74496200" {
		t.Errorf("primary file = %+v", pf)
	}
	types := map[string][]string{}
	for _, d := range v.Dependencies {
		types[d.DependencyType] = append(types[d.DependencyType], d.ProjectID)
	}
	if !reflect.DeepEqual(types[Required], []string{"4qmvXRB9"}) || !reflect.DeepEqual(types[Incompatible], []string{"KOHu7RCS", "sml2FMaA"}) || len(types[Optional]) != 2 {
		t.Errorf("dependencies = %v", types)
	}
}

func TestPrimaryFileSkipsSourcesAndSignatures(t *testing.T) {
	v := Version{Files: []File{
		{Filename: "x-sources.jar", Primary: true, FileType: "sources-jar"},
		{Filename: "x.jar.asc", FileType: "signature"},
		{Filename: "x.jar"},
	}}
	if f, ok := v.PrimaryFile(); !ok || f.Filename != "x.jar" {
		t.Errorf("PrimaryFile = %+v, %v", f, ok)
	}
	if _, ok := (&Version{Files: []File{{Filename: "x-dev.jar", FileType: "dev-jar"}}}).PrimaryFile(); ok {
		t.Error("a dev jar was offered as the add-on")
	}
}

func TestVersionsFromHashesPostsTheHashList(t *testing.T) {
	f, c := startFake(t)
	hashes := []string{"43ffecc6e6a734b752da41575bbb316526c124c3f878942437d5133c377bfbd9b78bda975520dc074d7158c15dade58a444ccd0fd8d8a25d165b6fc450140422", strings.Repeat("0", 128)}
	got, err := c.VersionsFromHashes(context.Background(), "sha512", hashes)
	if err != nil {
		t.Fatal(err)
	}
	r, body := f.last()
	if r.Method != http.MethodPost || body != `{"hashes":["`+hashes[0]+`","`+hashes[1]+`"],"algorithm":"sha512"}` {
		t.Errorf("request %s %s", r.Method, body)
	}
	if v, ok := got[hashes[0]]; !ok || v.ID != "MdY6JATr" || v.ProjectID != "fALzjamp" {
		t.Errorf("identified = %+v", got)
	}
	if _, ok := got[hashes[1]]; ok {
		t.Error("an unknown hash was identified")
	}
}

func TestLatestVersionsFromHashesSendsTheFilter(t *testing.T) {
	f, c := startFake(t)
	h := "6d4b119596514666f8dfd44c151ad89f71c2cd6a57cf830602eb31b90f1107b428d6fd5273420496696dbd8b57012297fa163b06617697b095c6c8236642e1bd"
	got, err := c.LatestVersionsFromHashes(context.Background(), "sha512", []string{h}, VersionFilter{Loaders: []string{"paper"}, GameVersions: []string{"26.2"}}, []string{"release"})
	if err != nil {
		t.Fatal(err)
	}
	_, body := f.last()
	if body != `{"hashes":["`+h+`"],"algorithm":"sha512","loaders":["paper"],"game_versions":["26.2"],"version_types":["release"]}` {
		t.Errorf("body %s", body)
	}
	if got[h].VersionNumber != "1.5.3" {
		t.Errorf("latest = %+v", got[h])
	}
}

func TestProjectsSplitsLongIDLists(t *testing.T) {
	f, c := startFake(t)
	ids := make([]string, 120)
	for i := range ids {
		ids[i] = "P1OZGk5p"
	}
	ps, err := c.Projects(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 || len(ps) != 6 {
		t.Fatalf("%d requests, %d projects; want 3 requests of at most 50 ids", len(f.requests), len(ps))
	}
	var first []string
	json.Unmarshal([]byte(f.requests[0].URL.Query().Get("ids")), &first)
	if len(first) != 50 {
		t.Errorf("first request asked for %d ids", len(first))
	}
}

func TestUpstreamErrorsAreTyped(t *testing.T) {
	_, c := startFake(t)
	if _, err := c.Project(context.Background(), "no-such-project"); !errors.Is(err, fetch.ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}
	_, err := c.Search(context.Background(), SearchQuery{Facets: [][]string{{"categories"}}})
	var se *fetch.StatusError
	if !errors.As(err, &se) || se.Status != 400 || se.Detail != "searching projects" || se.Service != "Modrinth" {
		t.Errorf("bad facets: %#v", err)
	}
}

func TestRunsOnServer(t *testing.T) {
	cases := []struct {
		side string
		env  []string
		want bool
	}{
		{"required", nil, true},
		{"optional", []string{"client_or_server_prefers_both"}, true},
		{"unsupported", []string{"client_only"}, false},
		{"unsupported", nil, false},
		{"optional", []string{"client_only"}, false},
		{"", []string{"singleplayer_only"}, false},
		{"", []string{"client_only", "server_only"}, true},
		{"", []string{"client_only_server_optional"}, true},
		{"unknown", []string{"unknown"}, true},
		{"", []string{""}, true},
	}
	for _, c := range cases {
		if got := RunsOnServer(c.side, c.env...); got != c.want {
			t.Errorf("RunsOnServer(%q, %v) = %v", c.side, c.env, got)
		}
	}

	var res SearchResult
	b, _ := os.ReadFile("testdata/search-mods-fabric-26.2.json")
	json.Unmarshal(b, &res)
	var server []string
	for _, h := range res.Hits {
		if h.RunsOnServer() {
			server = append(server, h.Slug)
		}
	}
	if !reflect.DeepEqual(server, []string{"fabric-api", "cloth-config"}) {
		t.Errorf("server-capable fabric hits = %v (sodium, iris and entityculling are client only)", server)
	}
}

func TestIsLoaderTag(t *testing.T) {
	for _, tag := range []string{"paper", "spigot", "bukkit", "fabric", "neoforge", "folia", "datapack"} {
		if !IsLoaderTag(tag) {
			t.Errorf("%s is a loader", tag)
		}
	}
	for _, tag := range []string{"utility", "worldgen", "game-mechanics", "optimization"} {
		if IsLoaderTag(tag) {
			t.Errorf("%s is a category", tag)
		}
	}
}
