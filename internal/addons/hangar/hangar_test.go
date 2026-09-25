package hangar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

type fakeHangar struct {
	mu       sync.Mutex
	requests []*http.Request
	names    map[string]string // slug or id -> fixture name
	versions map[int64]Version
}

func (f *fakeHangar) last() *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func startFake(t *testing.T) (*fakeHangar, *Client) {
	t.Helper()
	f := &fakeHangar{names: map[string]string{}, versions: map[int64]Version{}}
	projects, _ := filepath.Glob("testdata/project-*.json")
	for _, p := range projects {
		var pr Project
		b, _ := os.ReadFile(p)
		if err := json.Unmarshal(b, &pr); err != nil {
			t.Fatal(err)
		}
		f.names[pr.Namespace.Slug] = pr.Namespace.Slug
		f.names[strconv.FormatInt(pr.ID, 10)] = pr.Namespace.Slug
	}
	lists, _ := filepath.Glob("testdata/versions-*.json")
	for _, p := range lists {
		var vl VersionList
		b, _ := os.ReadFile(p)
		if err := json.Unmarshal(b, &vl); err != nil {
			t.Fatal(err)
		}
		for _, v := range vl.Result {
			f.versions[v.ID] = v
		}
	}
	serve := func(w http.ResponseWriter, status int, name string) {
		b, _ := os.ReadFile(filepath.Join("testdata", name))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(b)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.mu.Unlock()
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1"), "/"), "/")
		switch {
		case len(parts) == 1 && parts[0] == "projects":
			serve(w, 200, "search-paper-26.2.json")
		case len(parts) == 2 && parts[0] == "projects" && parts[1] == "busy":
			w.WriteHeader(http.StatusTooManyRequests)
		case len(parts) == 2 && parts[0] == "projects" && f.names[parts[1]] != "":
			serve(w, 200, "project-"+f.names[parts[1]]+".json")
		case len(parts) == 3 && parts[0] == "projects" && parts[2] == "versions" && f.names[parts[1]] != "":
			serve(w, 200, "versions-"+f.names[parts[1]]+".json")
		case len(parts) == 2 && parts[0] == "versions":
			id, _ := strconv.ParseInt(parts[1], 10, 64)
			if v, ok := f.versions[id]; ok {
				json.NewEncoder(w).Encode(v)
				return
			}
			serve(w, 404, "error-404.json")
		default:
			serve(w, 404, "error-404.json")
		}
	}))
	t.Cleanup(srv.Close)
	return f, New(fetch.Options{BaseURL: srv.URL + "/api/v1", HTTP: srv.Client()})
}

func TestSearchFiltersByPlatformAndVersion(t *testing.T) {
	f, c := startFake(t)
	res, err := c.Search(context.Background(), SearchQuery{Query: "via", Platform: Paper, Version: "26.2", Sort: "-downloads", Limit: 40})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("query") != "via" || q.Get("platform") != "PAPER" || q.Get("version") != "26.2" || q.Get("sort") != "-downloads" || q.Get("limit") != "25" || q.Has("offset") {
		t.Errorf("query = %v", q)
	}
	if ua := f.last().Header.Get("User-Agent"); ua != "CIYAhq/playkeeper/dev (https://github.com/CIYAhq/playkeeper)" {
		t.Errorf("User-Agent = %q", ua)
	}
	if res.Pagination.Count != 891 || len(res.Result) != 5 {
		t.Fatalf("pagination %+v, %d results", res.Pagination, len(res.Result))
	}
	p := res.Result[0]
	if p.ID != 12 || p.Name != "ViaBackwards" || p.Namespace.Owner != "ViaVersion" || p.Stats.Downloads != 742460 ||
		p.Settings.License.Type != "GPL" || p.Category != "misc" || p.AvatarURL != "https://hangarcdn.papermc.io/avatars/project/12.webp?v=1" ||
		p.LastUpdated.IsZero() || p.PageURL() != "https://hangar.papermc.io/ViaVersion/ViaBackwards" {
		t.Errorf("project = %+v", p)
	}
	found := false
	for _, v := range p.SupportedPlatforms[Paper] {
		found = found || v == "26.2"
	}
	if !found {
		t.Errorf("supported PAPER versions %v lack 26.2", p.SupportedPlatforms[Paper])
	}
}

func TestVersionsDecodeDownloadsAndDependencies(t *testing.T) {
	f, c := startFake(t)
	vl, err := c.Versions(context.Background(), "ViaRewind", VersionFilter{Platform: Paper, PlatformVersion: "26.2", Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	q := f.last().URL.Query()
	if q.Get("platform") != "PAPER" || q.Get("platformVersion") != "26.2" || q.Get("limit") != "25" {
		t.Errorf("query = %v", q)
	}
	v := vl.Result[0]
	d := v.Downloads[Paper]
	if v.ID != 30418 || v.ProjectID != 112 || v.Name != "4.2.0" || !v.Channel.Stable() || d.FileInfo == nil ||
		d.FileInfo.Name != "ViaRewind-4.2.0.jar" || d.FileInfo.SizeBytes != 408809 ||
		d.FileInfo.SHA256Hash != "f5113ea939353fec6170e6d1b7d2326fab76fa8d0cee594f689796995fc96720" ||
		d.DownloadURL != "https://hangarcdn.papermc.io/plugins/ViaVersion/ViaRewind/versions/4.2.0/PAPER/ViaRewind-4.2.0.jar" || d.ExternalURL != "" {
		t.Errorf("version = %+v, download %+v", v, d)
	}
	deps := v.PluginDependencies[Paper]
	if len(deps) != 2 || deps[0].Name != "ViaBackwards" || deps[0].ProjectID == nil || *deps[0].ProjectID != 12 || !deps[0].Required {
		t.Errorf("dependencies = %+v", deps)
	}
	if vl.Result[1].Channel.Stable() {
		t.Errorf("channel %q counted as stable", vl.Result[1].Channel.Name)
	}
}

func TestExternalOnlyDownloadsAndDependencies(t *testing.T) {
	_, c := startFake(t)
	geyser, err := c.Versions(context.Background(), "Geyser", VersionFilter{Platform: Paper})
	if err != nil {
		t.Fatal(err)
	}
	d := geyser.Result[0].Downloads[Paper]
	if d.FileInfo != nil || d.DownloadURL != "" || !strings.HasPrefix(d.ExternalURL, "https://download.geysermc.org/") {
		t.Errorf("Geyser download = %+v", d)
	}
	ore, err := c.Versions(context.Background(), "Orebfuscator", VersionFilter{Platform: Paper})
	if err != nil {
		t.Fatal(err)
	}
	dep := ore.Result[0].PluginDependencies[Paper][0]
	if dep.Name != "ProtocolLib" || dep.ProjectID != nil || !dep.Required || dep.ExternalURL != "https://github.com/dmulloy2/ProtocolLib/" {
		t.Errorf("ProtocolLib dependency = %+v", dep)
	}
}

func TestProjectAndVersionByID(t *testing.T) {
	_, c := startFake(t)
	p, err := c.Project(context.Background(), "112")
	if err != nil || p.Name != "ViaRewind" {
		t.Fatalf("project 112 = %+v, %v", p, err)
	}
	v, err := c.Version(context.Background(), 30417)
	if err != nil || v.Name != "5.12.0" || v.ProjectID != 12 {
		t.Fatalf("version 30417 = %+v, %v", v, err)
	}
}

func TestUpstreamErrorsAreTyped(t *testing.T) {
	_, c := startFake(t)
	_, err := c.Project(context.Background(), "this-project-does-not-exist-xyz")
	var se *fetch.StatusError
	if !errors.Is(err, fetch.ErrNotFound) || !errors.As(err, &se) || se.Detail != "Unknown value 'this-project-does-not-exist-xyz' for path variable 'slugOrId'" {
		t.Errorf("missing project: %#v", err)
	}
	var rl *fetch.RateLimitError
	if _, err := c.Project(context.Background(), "busy"); !errors.As(err, &rl) || rl.Service != "Hangar" {
		t.Errorf("rate limited: %v", err)
	}
}

func TestChannelStable(t *testing.T) {
	for name, want := range map[string]bool{"Release": true, "release": true, "Stable": true, "Snapshot": false, "Beta": false, "Alpha": false, "Dev": false} {
		if got := (Channel{Name: name}).Stable(); got != want {
			t.Errorf("%s: Stable() = %v", name, got)
		}
	}
}
