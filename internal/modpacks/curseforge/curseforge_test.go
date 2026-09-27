package curseforge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
)

const testKey = "$2a$10$test-key-not-real-0123456789abcdefghijklmnopqr"

func mustKey(t *testing.T) Key {
	t.Helper()
	k, err := NewKey(testKey, KeyFile)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeAPI serves h behind the key check CurseForge does and returns a client
// for it.
func fakeAPI(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testKey {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(mustKey(t), fetch.Options{BaseURL: srv.URL, UserAgent: "test", HTTP: srv.Client()})
}

func TestRecordedProjectAndFileDecode(t *testing.T) {
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/mods/238222":
			w.Write(fixture(t, "mod-238222.json"))
		case "/v1/mods/238222/files/4644453":
			w.Write(fixture(t, "file-238222-4644453.json"))
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	m, err := c.Mod(ctx, 238222)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Just Enough Items (JEI)" || m.Slug != "jei" || m.ClassID != ClassMods || m.DownloadCount < 1e8 ||
		m.AllowModDistribution == nil || !*m.AllowModDistribution || m.Logo == nil || !strings.HasPrefix(m.Logo.ThumbnailURL, "https://media.forgecdn.net/") {
		t.Errorf("mod %+v", m)
	}
	if len(m.LatestFilesIndexes) == 0 || m.LatestFilesIndexes[0].ModLoader == nil || *m.LatestFilesIndexes[0].ModLoader != LoaderNeoForge {
		t.Errorf("latestFilesIndexes %+v", m.LatestFilesIndexes)
	}
	f, err := c.File(ctx, 238222, 4644453)
	if err != nil {
		t.Fatal(err)
	}
	if f.FileName != "jei-1.20.1-forge-15.2.0.23.jar" || f.FileLength != 1112923 || f.SHA1() != "480813d80c8a32cc3d05950250f9b3390a1d931d" ||
		f.DownloadURL != "https://edge.forgecdn.net/files/4644/453/jei-1.20.1-forge-15.2.0.23.jar" || f.ClientOnly() || f.ReleaseType != Beta {
		t.Errorf("file %+v", f)
	}
	if m.FilePage(f.ID) != "https://www.curseforge.com/minecraft/mc-mods/jei/files/4644453" {
		t.Errorf("FilePage = %s", m.FilePage(f.ID))
	}
	if _, err := c.Mod(ctx, 1); !errors.Is(err, fetch.ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}
}

func TestRecordedSearchDecodes(t *testing.T) {
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "search-worldguard.json"))
	})
	res, err := c.Search(context.Background(), SearchQuery{ClassID: ClassBukkitPlugins, Text: "worldguard"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 1 || res.Pagination.TotalCount != 1 {
		t.Fatalf("result %+v", res)
	}
	m := res.Data[0]
	if m.ID != 31054 || m.ClassID != ClassBukkitPlugins || m.ProjectPage() != "https://www.curseforge.com/minecraft/bukkit-plugins/worldguard" {
		t.Errorf("mod %+v", m)
	}
	for _, fi := range m.LatestFilesIndexes {
		if fi.ModLoader != nil {
			t.Errorf("plugin file index with a loader: %+v", fi)
		}
	}
}

func TestSearchSendsTheDocumentedParameters(t *testing.T) {
	var got []string
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RawQuery)
		w.Write(fixture(t, "search-modpacks.json"))
	})
	ctx := context.Background()
	res, err := c.Search(ctx, SearchQuery{ClassID: ClassModpacks, Text: "create & friends", SortField: SortPopularity, Index: 40, PageSize: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Data) != 5 || res.Pagination.TotalCount != 812 || res.Data[0].ClassID != ClassModpacks {
		t.Errorf("result %+v", res.Pagination)
	}
	if _, err := c.Search(ctx, SearchQuery{ClassID: ClassModpacks, GameVersion: "1.21.1", ModLoaderType: LoaderNeoForge}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(ctx, SearchQuery{ClassID: ClassModpacks, ModLoaderType: LoaderFabric}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"classId=4471&gameId=432&index=40&pageSize=50&searchFilter=create+%26+friends&sortField=2&sortOrder=desc",
		"classId=4471&gameId=432&gameVersion=1.21.1&modLoaderType=6&sortOrder=desc",
		"classId=4471&gameId=432&sortOrder=desc",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("queries:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBatchesAskFiftyAtATime(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ModIDs       []int64 `json:"modIds"`
			FileIDs      []int64 `json:"fileIds"`
			FilterPcOnly *bool   `json:"filterPcOnly"`
		}
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || json.Unmarshal(b, &body) != nil {
			t.Errorf("%s %s %s", r.Method, r.URL.Path, b)
		}
		mu.Lock()
		defer mu.Unlock()
		var items []string
		switch r.URL.Path {
		case "/v1/mods":
			if body.FilterPcOnly == nil || !*body.FilterPcOnly {
				t.Errorf("filterPcOnly missing: %s", b)
			}
			sizes = append(sizes, len(body.ModIDs))
			for _, id := range body.ModIDs {
				items = append(items, fmt.Sprintf(`{"id":%d,"classId":6}`, id))
			}
		case "/v1/mods/files":
			sizes = append(sizes, len(body.FileIDs))
			for _, id := range body.FileIDs {
				items = append(items, fmt.Sprintf(`{"id":%d,"downloadUrl":null}`, id))
			}
		}
		fmt.Fprintf(w, `{"data":[%s]}`, strings.Join(items, ","))
	})
	ids := make([]int64, 120)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	mods, err := c.Mods(context.Background(), ids)
	if err != nil || len(mods) != 120 {
		t.Fatalf("Mods: %d, %v", len(mods), err)
	}
	files, err := c.Files(context.Background(), ids[:51])
	if err != nil || len(files) != 51 || files[0].DownloadURL != "" {
		t.Fatalf("Files: %d, %v", len(files), err)
	}
	if fmt.Sprint(sizes) != "[50 50 20 50 1]" {
		t.Errorf("batch sizes %v", sizes)
	}
}

func TestModFilesListsAProjectsFiles(t *testing.T) {
	c := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/mods/9100001/files" || r.URL.RawQuery != "gameVersion=26.2&pageSize=50" {
			t.Errorf("request %s", r.URL)
		}
		w.Write(fixture(t, "files-9100001.json"))
	})
	files, page, err := c.ModFiles(context.Background(), 9100001, FilesQuery{GameVersion: "26.2", PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 || page.TotalCount != 4 || !files[0].IsServerPack || files[1].ServerPackFileID == nil || *files[1].ServerPackFileID != 9200004 {
		t.Errorf("files %+v", files)
	}
}

func TestARefusedKeyIsReportedWithoutTheKey(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := New(mustKey(t), fetch.Options{BaseURL: srv.URL, HTTP: srv.Client()})
	_, err := c.Mod(context.Background(), 238222)
	var se *fetch.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "test-key") {
		t.Errorf("the key leaked: %q", err)
	}
}

func TestClientOnlyUsesTheEnvironmentTags(t *testing.T) {
	cases := []struct {
		tags []string
		want bool
	}{
		{[]string{"1.21.1", "NeoForge"}, false},
		{[]string{"1.21.1", "Fabric", "Client"}, true},
		{[]string{"1.21.1", "Fabric", "client", "Server"}, false},
		{[]string{"Server", "Quilt"}, false},
	}
	for _, c := range cases {
		f := File{GameVersions: c.tags}
		if f.ClientOnly() != c.want {
			t.Errorf("ClientOnly(%v) = %v", c.tags, !c.want)
		}
	}
}

func TestProjectPageOnlyLinksToCurseForge(t *testing.T) {
	cases := []struct{ site, want string }{
		{"https://www.curseforge.com/minecraft/mc-mods/jei", "https://www.curseforge.com/minecraft/mc-mods/jei/files/7"},
		{"https://evilcurseforge.com/x", "https://www.curseforge.com/projects/42"},
		{"http://www.curseforge.com/minecraft/mc-mods/jei", "https://www.curseforge.com/projects/42"},
		{"https://user@www.curseforge.com/x", "https://www.curseforge.com/projects/42"},
		{"", "https://www.curseforge.com/projects/42"},
	}
	for _, c := range cases {
		m := Mod{ID: 42, Links: Links{WebsiteURL: c.site}}
		if got := m.FilePage(7); got != c.want {
			t.Errorf("FilePage with %q = %s", c.site, got)
		}
	}
}

func TestParseRealManifest(t *testing.T) {
	m, err := ParseManifest(fixture(t, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	name, version, err := m.Loader()
	if m.Name != "Fabulously Optimized" || m.Version != "15.0.0-alpha.3" || m.Minecraft.Version != "26.3" || m.Overrides != "overrides" ||
		len(m.Files) != 8 || name != "fabric" || version != "0.19.5" || err != nil {
		t.Errorf("manifest %+v, loader %q %q %v", m, name, version, err)
	}
	if f := m.Files[0]; f.ProjectID != 306612 || f.FileID != 8913512 || !f.Required {
		t.Errorf("first file %+v", f)
	}
}

func TestParseManifestRefusals(t *testing.T) {
	base := func() map[string]any {
		var m map[string]any
		json.Unmarshal(fixture(t, "manifest.json"), &m)
		return m
	}
	cases := []struct {
		name   string
		change func(m map[string]any)
		reason string
	}{
		{"not a modpack", func(m map[string]any) { m["manifestType"] = "minecraftWorld" }, "manifestType"},
		{"newer version", func(m map[string]any) { m["manifestVersion"] = 2 }, "version 2"},
		{"no name", func(m map[string]any) { m["name"] = "" }, "no name"},
		{"no minecraft", func(m map[string]any) { m["minecraft"].(map[string]any)["version"] = "" }, "Minecraft version"},
		{"odd minecraft", func(m map[string]any) { m["minecraft"].(map[string]any)["version"] = "1.21 && reboot" }, "Minecraft version"},
		{"escaping overrides", func(m map[string]any) { m["overrides"] = "../../etc" }, "unsafe overrides"},
		{"absolute overrides", func(m map[string]any) { m["overrides"] = "/etc" }, "unsafe overrides"},
		{"no file id", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["fileID"] = 0 }, "without a project"},
		{"duplicate project", func(m map[string]any) {
			fs := m["files"].([]any)
			fs[1].(map[string]any)["projectID"] = fs[0].(map[string]any)["projectID"]
		}, "more than once"},
		{"loader without version", func(m map[string]any) {
			m["minecraft"].(map[string]any)["modLoaders"] = []any{map[string]any{"id": "forge", "primary": true}}
		}, "loader"},
		{"loader with odd version", func(m map[string]any) {
			m["minecraft"].(map[string]any)["modLoaders"] = []any{map[string]any{"id": "fabric-0.19;id", "primary": true}}
		}, "loader"},
	}
	for _, c := range cases {
		m := base()
		c.change(m)
		b, _ := json.Marshal(m)
		_, err := ParseManifest(b)
		var me *ManifestError
		if !errors.As(err, &me) || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if _, err := ParseManifest([]byte("{")); err == nil {
		t.Error("broken JSON accepted")
	}
}

func TestLoaderPicksThePrimaryOne(t *testing.T) {
	m := Manifest{Minecraft: ManifestMinecraft{ModLoaders: []ModLoader{{ID: "forge-47.2.0"}, {ID: "neoforge-1.20.1-47.1.99", Primary: true}}}}
	name, version, err := m.Loader()
	if name != "neoforge" || version != "1.20.1-47.1.99" || err != nil {
		t.Errorf("Loader() = %q %q %v", name, version, err)
	}
	if name, _, _ := (&Manifest{}).Loader(); name != "" {
		t.Errorf("vanilla pack loader %q", name)
	}
}

func TestSplitLoader(t *testing.T) {
	cases := []struct{ id, name, version string }{
		{"forge-47.2.0", "forge", "47.2.0"},
		{"fabric-0.19.5", "fabric", "0.19.5"},
		{"quilt-0.26.4", "quilt", "0.26.4"},
		{"neoforge-21.1.77", "neoforge", "21.1.77"},
		{"NeoForge-26.1.2.68-beta", "neoforge", "26.1.2.68-beta"},
	}
	for _, c := range cases {
		name, version, err := SplitLoader(c.id)
		if name != c.name || version != c.version || err != nil {
			t.Errorf("SplitLoader(%q) = %q %q %v", c.id, name, version, err)
		}
	}
	for _, id := range []string{"", "forge", "-1.0", "fabric-", "fab ric-1.0", "fabric-1.0 2", "f4bric-1.0"} {
		if _, _, err := SplitLoader(id); err == nil {
			t.Errorf("SplitLoader(%q) accepted", id)
		}
	}
}
