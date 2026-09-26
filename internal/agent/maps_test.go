package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/addons/fetch"
	"github.com/CIYAhq/playkeeper/internal/addons/hangar"
	"github.com/CIYAhq/playkeeper/internal/addons/modrinth"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/certs"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/update"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

const (
	squaremapFile       = "squaremap-paper-mc26.1.2-1.3.9.jar"
	squaremapFabricFile = "squaremap-fabric-mc26.1.2-1.3.9.jar"
	fabricAPIID         = "P7dR8mSH"
	fabricAPIFile       = "fabric-api-0.119.2+26.1.2.jar"
)

// fakeMapSource serves squaremap's Modrinth project with its Paper and
// Fabric builds for 26.1.2, and Fabric API, which the Fabric build needs,
// from local TLS servers.
type fakeMapSource struct {
	api, cdn  *httptest.Server
	downloads atomic.Int32
	// fabricAPI is Fabric API's jar as Modrinth serves it.
	fabricAPI []byte
}

func jarOf(name, content string) []byte {
	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	w, _ := zw.Create(name)
	io.WriteString(w, content)
	zw.Close()
	return jar.Bytes()
}

func startFakeMapSource(t *testing.T) *fakeMapSource {
	t.Helper()
	data := jarOf("plugin.yml", "name: squaremap\nversion: 1.3.9\nmain: xyz.jpenilla.squaremap.paper.SquaremapPaper\n")
	fabricData := jarOf("fabric.mod.json", `{"schemaVersion":1,"id":"squaremap","version":"1.3.9","depends":{"fabric-api":"*"}}`)
	// Fabric API's jar says nothing about itself here, and Modrinth's hash
	// lookup below doesn't know it, so only its record tells the planner a
	// server has it.
	f := &fakeMapSource{fabricAPI: jarOf("LICENSE", "Apache-2.0")}
	files := map[string][]byte{
		"/data/" + webmap.ModrinthProjectID + "/versions/sqm1/" + squaremapFile:       data,
		"/data/" + webmap.ModrinthProjectID + "/versions/sqmf/" + squaremapFabricFile: fabricData,
		"/data/" + fabricAPIID + "/versions/fapi1/" + fabricAPIFile:                   f.fabricAPI,
	}
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		f.downloads.Add(1)
		w.Write(b)
	}))
	file := func(project, version, name string, b []byte) map[string]any {
		s512, s1 := sha512.Sum512(b), sha1.Sum(b)
		return map[string]any{
			"hashes":   map[string]string{"sha512": hex.EncodeToString(s512[:]), "sha1": hex.EncodeToString(s1[:])},
			"url":      f.cdn.URL + "/data/" + project + "/versions/" + version + "/" + name,
			"filename": name, "primary": true, "size": len(b),
		}
	}
	project := map[string]any{
		"id": webmap.ModrinthProjectID, "slug": "squaremap", "project_type": "mod", "title": "squaremap",
		"loaders": []string{"fabric", "neoforge", "paper"}, "game_versions": []string{"26.1.2"},
		"client_side": "optional", "server_side": "required", "status": "approved", "license": map[string]any{"id": "MIT"},
		"published": "2021-11-12T00:00:00Z", "updated": "2026-09-01T00:00:00Z", "versions": []string{"sqm1", "sqmf"},
	}
	version := map[string]any{
		"id": "sqm1", "project_id": webmap.ModrinthProjectID, "name": "squaremap 1.3.9", "version_number": "1.3.9",
		"version_type": "release", "status": "listed", "game_versions": []string{"26.1.2"}, "loaders": []string{"paper"},
		"date_published": "2026-09-01T00:00:00Z", "dependencies": []any{},
		"files": []any{file(webmap.ModrinthProjectID, "sqm1", squaremapFile, data)},
	}
	fabricVersion := map[string]any{
		"id": "sqmf", "project_id": webmap.ModrinthProjectID, "name": "squaremap 1.3.9", "version_number": "1.3.9",
		"version_type": "release", "status": "listed", "game_versions": []string{"26.1.2"}, "loaders": []string{"fabric"},
		"date_published": "2026-09-01T00:00:00Z", "dependencies": []any{map[string]any{"project_id": fabricAPIID, "dependency_type": "required"}},
		"files": []any{file(webmap.ModrinthProjectID, "sqmf", squaremapFabricFile, fabricData)},
	}
	apiProject := map[string]any{
		"id": fabricAPIID, "slug": "fabric-api", "project_type": "mod", "title": "Fabric API",
		"loaders": []string{"fabric"}, "game_versions": []string{"26.1.2"},
		"client_side": "required", "server_side": "required", "status": "approved", "license": map[string]any{"id": "Apache-2.0"},
		"published": "2018-11-27T00:00:00Z", "updated": "2026-09-01T00:00:00Z", "versions": []string{"fapi1"},
	}
	apiVersion := map[string]any{
		"id": "fapi1", "project_id": fabricAPIID, "name": "Fabric API 0.119.2", "version_number": "0.119.2+26.1.2",
		"version_type": "release", "status": "listed", "game_versions": []string{"26.1.2"}, "loaders": []string{"fabric"},
		"date_published": "2026-09-01T00:00:00Z", "dependencies": []any{},
		"files": []any{file(fabricAPIID, "fapi1", fabricAPIFile, f.fabricAPI)},
	}
	known := map[string]map[string]any{}
	for _, v := range []map[string]any{version, fabricVersion} {
		for _, h := range v["files"].([]any)[0].(map[string]any)["hashes"].(map[string]string) {
			known[h] = v
		}
	}
	f.api = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/project/" + webmap.ModrinthProjectID, "/v2/project/squaremap":
			json.NewEncoder(w).Encode(project)
		case "/v2/project/" + webmap.ModrinthProjectID + "/version":
			json.NewEncoder(w).Encode([]any{version, fabricVersion})
		case "/v2/project/" + fabricAPIID, "/v2/project/fabric-api":
			json.NewEncoder(w).Encode(apiProject)
		case "/v2/project/" + fabricAPIID + "/version":
			json.NewEncoder(w).Encode([]any{apiVersion})
		case "/v2/projects":
			var ids []string
			json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids)
			out := []any{}
			for _, p := range []map[string]any{project, apiProject} {
				if len(ids) == 0 || slices.Contains(ids, p["id"].(string)) {
					out = append(out, p)
				}
			}
			json.NewEncoder(w).Encode(out)
		case "/v2/versions":
			var ids []string
			json.Unmarshal([]byte(r.URL.Query().Get("ids")), &ids)
			out := []any{}
			for _, v := range []map[string]any{version, fabricVersion, apiVersion} {
				if slices.Contains(ids, v["id"].(string)) {
					out = append(out, v)
				}
			}
			json.NewEncoder(w).Encode(out)
		case "/v2/version_files":
			// Modrinth knows squaremap's jars by their hashes, like any file on it.
			var req struct {
				Hashes []string `json:"hashes"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			out := map[string]any{}
			for _, h := range req.Hashes {
				if v, ok := known[h]; ok {
					out[h] = v
				}
			}
			json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not_found","description":"fake"}`)
		}
	}))
	t.Cleanup(f.api.Close)
	t.Cleanup(f.cdn.Close)
	return f
}

// library reaches only the fakes; every httptest TLS server shares one
// certificate, so one client trusts both.
func (f *fakeMapSource) library(t *testing.T) *addons.Library {
	client := f.api.Client()
	return &addons.Library{
		Modrinth:      modrinth.New(fetch.Options{BaseURL: f.api.URL + "/v2", HTTP: client}),
		Hangar:        hangar.New(fetch.Options{BaseURL: f.api.URL + "/hangar", HTTP: client}),
		HTTP:          client,
		TempDir:       t.TempDir(),
		ModrinthFiles: fetch.Hosts{f.cdn.Listener.Addr().String()},
		IconHosts:     fetch.Hosts{f.cdn.Listener.Addr().String()},
	}
}

// fakeSquaremapWeb answers like squaremap's web server in the container.
// While hold is set, a request for its list of worlds gets no answer until
// its asker gives up; held counts those requests.
type fakeSquaremapWeb struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
	hold  atomic.Bool
	held  atomic.Int32
}

func startFakeSquaremapWeb(t *testing.T) *fakeSquaremapWeb {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x7f
	}
	img.Set(0, 0, color.RGBA{R: 0x33, G: 0x99, B: 0x33, A: 0xff})
	var tile bytes.Buffer
	png.Encode(&tile, img)
	world := `{"spawn":{"x":16,"z":-48},"zoom":{"def":3,"max":3,"extra":2}}`
	files := map[string]string{
		"/tiles/settings.json":                      `{"worlds":[{"name":"minecraft_overworld","display_name":"minecraft:overworld","type":"normal","order":0},{"name":"minecraft_the_nether","display_name":"minecraft:the_nether","type":"nether","order":0}]}`,
		"/tiles/minecraft_overworld/settings.json":  world,
		"/tiles/minecraft_the_nether/settings.json": world,
		"/tiles/players.json":                       `{"max":10,"players":[{"world":"minecraft_overworld","name":"Alex","x":120,"y":64,"z":-40,"uuid":"853c80ef3c3749fdaa49938b674adae6","yaw":0}]}`,
	}
	f := &fakeSquaremapWeb{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		if r.URL.Path == "/tiles/settings.json" && f.hold.Load() {
			f.held.Add(1)
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/tiles/minecraft_overworld/3/0_0.png" {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("ETag", `"1790351880000"`)
			w.Write(tile.Bytes())
			return
		}
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newMapEnv is an agent whose add-on library reaches the fake Modrinth and
// whose servers' squaremap is the fake web server, across the agent's
// restarts too.
func newMapEnv(t *testing.T) (*agentEnv, *fakeMapSource, *fakeSquaremapWeb) {
	t.Helper()
	return newMapEnvWith(t, nil)
}

// newMapEnvWith is newMapEnv with setup run before the agent first starts.
func newMapEnvWith(t *testing.T, setup func(e *agentEnv)) (*agentEnv, *fakeMapSource, *fakeSquaremapWeb) {
	t.Helper()
	src, sq := startFakeMapSource(t), startFakeSquaremapWeb(t)
	addr := sq.srv.Listener.Addr().String()
	e := newAgentEnvWith(t, func(e *agentEnv) {
		e.addons = src.library(t)
		e.tweak = func(o *Options) { o.MapAddr = func(string) string { return addr } }
		if setup != nil {
			setup(e)
		}
	})
	return e, src, sq
}

func (e *agentEnv) mapInfo() api.MapInfo {
	e.t.Helper()
	info, err := e.srv().mapInfo(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return info
}

// mapOp starts a map operation and waits for it.
func (e *agentEnv) mapOp(rest string, body map[string]any) *api.Operation {
	e.t.Helper()
	body["actor"] = "admin"
	code, out := e.call("POST", e.sp(rest), body)
	if code != http.StatusAccepted {
		e.t.Fatalf("%s: %d %v", rest, code, out)
	}
	return e.waitOp(out["id"].(string))
}

// get fetches an agent path and returns its status, headers and body.
func (e *agentEnv) get(path string) (int, http.Header, []byte) {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

func (e *agentEnv) slug() string {
	e.t.Helper()
	r, err := e.srv().row()
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Slug
}

func (e *agentEnv) startedAt() time.Time {
	e.t.Helper()
	c, err := e.a.docker.ContainerInspect(context.Background(), e.cname())
	if err != nil {
		e.t.Fatal(err)
	}
	t, _ := c.State.Started()
	return t
}

func TestMapTurnsOnDrawsOnceAndTurnsOff(t *testing.T) {
	e, src, _ := newMapEnv(t)
	e.create()

	m := e.mapInfo()
	if !m.Supported || m.Enabled || m.State != string(webmap.StateNotInstalled) || m.Path != "" || m.EstimatedMinutes == 0 {
		t.Fatalf("before: %+v", m)
	}
	if code, out := e.call("GET", e.sp("/map/worlds"), nil); code != 404 || out["error"] != "The map is not turned on." {
		t.Fatalf("proxy before: %d %v", code, out)
	}

	started := e.startedAt()
	op := e.mapOp("/map/enable", map[string]any{})
	if op.Status != api.OpSucceeded || op.Detail["plugin"] != "squaremap" || op.Detail["version"] != "1.3.9" {
		t.Fatalf("enable: %+v", op)
	}
	if src.downloads.Load() != 1 {
		t.Fatalf("downloads = %d", src.downloads.Load())
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile)); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "squaremap", "config.yml"))
	if err != nil || !bytes.Contains(cfg, []byte("port: 25580")) || !bytes.Contains(cfg, []byte("web-address: ''")) {
		t.Fatalf("config: %v\n%s", err, cfg)
	}
	if !e.startedAt().After(started) {
		t.Fatal("nobody was playing, so the server should have restarted to load the map")
	}
	for _, d := range []string{"overworld", "the_nether", "the_end"} {
		cmd := "squaremap fullrender minecraft:" + d
		e.waitFor(cmd, func() bool { return e.rcon.count(cmd) == 1 })
	}
	e.waitFor("the first render to be recorded", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
	})
	m = e.mapInfo()
	if !m.Enabled || m.State != string(webmap.StateDrawing) || m.PluginVersion != "1.3.9" || m.Public || e.status().PendingRestart {
		t.Fatalf("after: %+v", m)
	}

	code, _, body := e.get(e.sp("/map/worlds"))
	if code != 200 || !bytes.Contains(body, []byte(`"minecraft_the_nether"`)) {
		t.Fatalf("worlds: %d %s", code, body)
	}
	code, h, body := e.get(e.sp("/map/tiles/minecraft_overworld/3/0_0.png"))
	if code != 200 || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=30" || !bytes.HasPrefix(body, []byte("\x89PNG")) {
		t.Fatalf("tile: %d %v", code, h)
	}
	if code, _, body := e.get(e.sp("/map/players")); code != 200 || !bytes.Contains(body, []byte(`"Alex"`)) {
		t.Fatalf("players: %d %s", code, body)
	}
	if code, _, _ := e.get(e.sp("/map/../../icon")); code == 200 {
		t.Fatal("the proxy must not reach other agent routes")
	}

	// A restart does not ask for the whole map again.
	code, out := e.call("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("restart: %d %v", code, out)
	}
	time.Sleep(200 * time.Millisecond)
	if n := e.rcon.count("squaremap fullrender minecraft:overworld"); n != 1 {
		t.Fatalf("fullrender sent %d times", n)
	}

	tile := filepath.Join(e.dataDir(), "plugins", "squaremap", "web", "tiles", "minecraft_overworld", "3", "0_0.png")
	os.MkdirAll(filepath.Dir(tile), 0o755)
	os.WriteFile(tile, []byte("\x89PNG drawn"), 0o644)
	if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
		t.Fatalf("disable: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile)); !os.IsNotExist(err) {
		t.Fatalf("squaremap is still installed: %v", err)
	}
	if _, err := os.Stat(tile); err != nil {
		t.Fatalf("the drawn map should be kept: %v", err)
	}
	if m := e.mapInfo(); m.Enabled || m.State != string(webmap.StateNotInstalled) || e.countRows(`SELECT COUNT(*) FROM maps`) != 0 {
		t.Fatalf("after turning off: %+v", m)
	}
	e.waitFor("online", e.onlineIdle)

	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable again: %+v", op)
	}
	if code, out := e.call("POST", e.sp("/map/enable"), map[string]any{"actor": "admin"}); code != 409 {
		t.Fatalf("enable twice: %d %v", code, out)
	}
	e.waitFor("online", e.onlineIdle)
	if op := e.mapOp("/map/disable", map[string]any{"deleteMap": true}); op.Status != api.OpSucceeded {
		t.Fatalf("disable deleting: %+v", op)
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", "squaremap")); !os.IsNotExist(err) {
		t.Fatalf("the drawn map should be deleted: %v", err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind IN ('map_enabled', 'map_disabled')`); n != 4 {
		t.Fatalf("map events = %d", n)
	}
}

// The Plugins tab lists the map's squaremap as the Map's rather than as a
// file added by hand: it is never offered to manage, and installing,
// updating, removing or forgetting it there is refused. Only turning the
// map off removes it.
func TestPluginsTabLeavesTheMapsSquaremapToTheMap(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)

	list := e.addonList()
	if len(list.Files) != 1 || len(list.Missing) != 0 {
		t.Fatalf("list: %+v", list)
	}
	if f := list.Files[0]; f.FileName != squaremapFile || f.Status != "managed" || f.Addon == nil || f.Addon.UsedBy != api.UsedByMap || f.Addon.Name != "squaremap" || f.Pending {
		t.Fatalf("the map's squaremap shows as %+v (addon %+v)", f, f.Addon)
	}
	var checks api.AddonChecks
	e.decode("GET", e.sp("/addons/checks"), &checks)
	if len(checks.Identified) != 0 || len(checks.Updates) != 0 {
		t.Fatalf("the checks offer the map's squaremap: %+v", checks)
	}
	var d api.AddonDetails
	e.decode("GET", e.sp("/addons/project/modrinth/"+webmap.ModrinthProjectID), &d)
	if d.Installed == nil || d.Installed.UsedBy != api.UsedByMap || !d.Card.Installed || d.UpdateAvailable || d.Plan != nil {
		t.Fatalf("details: %+v, installed %+v", d, d.Installed)
	}

	key := map[string]any{"source": "modrinth", "projectId": webmap.ModrinthProjectID}
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/addons/adopt", map[string]any{"fileName": squaremapFile}},
		{"GET", "/addons/project/modrinth/" + webmap.ModrinthProjectID + "/removal", nil},
		{"POST", "/addons/remove", key},
		{"POST", "/addons/update/plan", map[string]any{"addons": []any{key}}},
		{"POST", "/addons/update", map[string]any{"addons": []any{key}, "fingerprint": otherPlan}},
		{"POST", "/addons/install", map[string]any{"source": "modrinth", "projectId": webmap.ModrinthProjectID, "fingerprint": otherPlan}},
		{"POST", "/addons/forget", key},
	} {
		body := map[string]any{"actor": "admin"}
		maps.Copy(body, c.body)
		if c.method == "GET" {
			body = nil
		}
		if code, out := e.call(c.method, e.sp(c.path), body); code != http.StatusConflict || out["error"] != "squaremap is part of the Map." {
			t.Errorf("%s %s: %d %v", c.method, c.path, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile)); err != nil {
		t.Fatal(err)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM addons`); n != 0 {
		t.Fatalf("the Plugins tab took over %d of the map's add-ons", n)
	}

	if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
		t.Fatalf("disable: %+v", op)
	}
	if list := e.addonList(); len(list.Files) != 0 || len(list.Missing) != 0 {
		t.Fatalf("after turning the map off: %+v", list)
	}
}

// fabricMapServer is a stopped Fabric server, whose squaremap needs Fabric
// API. Stopped, turning the map on or off doesn't restart it.
func (e *agentEnv) fabricMapServer() {
	e.t.Helper()
	e.create()
	if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
		e.t.Fatalf("stop: %+v", op)
	}
	e.fabricForShare()
}

// modsTabAddon puts a jar in the mods folder with the record the Mods tab
// keeps for it, as if it had installed it.
func (e *agentEnv) modsTabAddon(rec addons.Installed, jar []byte) {
	e.t.Helper()
	dir := filepath.Join(e.dataDir(), "mods")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rec.FileName), jar, 0o644); err != nil {
		e.t.Fatal(err)
	}
	sum := sha512.Sum512(jar)
	rec.Source, rec.HashAlgo, rec.Hash, rec.Size, rec.InstalledAt = addons.Modrinth, "sha512", hex.EncodeToString(sum[:]), int64(len(jar)), e.a.now()
	if err := e.srv().saveAddons([]addons.Installed{rec}, nil, false); err != nil {
		e.t.Fatal(err)
	}
}

func addonKeys(recs []addons.Installed) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.ProjectID)
	}
	slices.Sort(out)
	return out
}

// squaremap the Plugins tab installed stays the tab's: turning the map on
// uses it, rather than refusing because it is installed or recording a map
// with no files of its own, which would count as off for good. The map is
// drawn, the Plugins tab keeps managing squaremap, and turning the map off
// leaves it there.
func TestTheMapUsesSquaremapThePluginsTabInstalled(t *testing.T) {
	e, src, _ := newMapEnv(t)
	e.create()
	e.installAddon(webmap.ModrinthProjectID)
	jar := filepath.Join(e.dataDir(), "plugins", squaremapFile)
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("the Plugins tab's squaremap: %v", err)
	}
	downloads := src.downloads.Load()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded || op.Detail["version"] != "1.3.9" {
		t.Fatalf("turning the map on with squaremap on the Plugins tab: %+v", op)
	}
	if src.downloads.Load() != downloads {
		t.Fatal("turning the map on downloaded squaremap again")
	}
	if m := e.mapInfo(); !m.Enabled || m.Missing || m.PluginVersion != "1.3.9" {
		t.Fatalf("the map with the Plugins tab's squaremap: %+v", m)
	}
	if rec, err := e.srv().loadMap(); err != nil || rec == nil || len(rec.addons) != 0 {
		t.Fatalf("the map's record: %+v (%v), want one that owns nothing", rec, err)
	}
	e.waitFor("the first render", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
	})
	if files := e.addonList().Files; len(files) != 1 || files[0].Addon == nil || files[0].Addon.UsedBy != "" {
		t.Fatalf("the Plugins tab no longer manages its squaremap: %+v", files)
	}
	if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
		t.Fatalf("turning the map off: %+v", op)
	}
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("turning the map off removed the Plugins tab's squaremap: %v", err)
	}
	if installed, _ := e.srv().installedAddons(); !slices.Equal(addonKeys(installed), []string{webmap.ModrinthProjectID}) {
		t.Fatalf("the Plugins tab's records after the map was turned off: %v", addonKeys(installed))
	}
}

// Turning the map off takes out what the map installed while the server is
// stopped, so squaremap isn't drawing into the folder "Delete the drawn map"
// deletes, then starts a running server again. The record goes even when
// some of the drawing can't be deleted. squaremap the Plugins tab installed
// stays that tab's, folder and all, and the server keeps running it.
func TestTurningTheMapOffStopsSquaremapFirst(t *testing.T) {
	for _, c := range []struct {
		name       string
		pluginsTab bool // the Plugins tab installed squaremap
		stopped    bool // the server is stopped
		deleteMap  bool
		stuck      bool // part of the drawing can't be deleted
		restarts   bool
		folder     bool // squaremap's folder is still there after
		says       string
	}{
		{name: "the map's squaremap, deleting the drawing", deleteMap: true, restarts: true},
		{name: "the map's squaremap, keeping the drawing", restarts: true, folder: true},
		{name: "the map's squaremap on a stopped server", stopped: true, deleteMap: true},
		{name: "the map's squaremap, part of the drawing stuck", deleteMap: true, stuck: true, restarts: true, folder: true, says: "The map is off, but some of what squaremap drew could not be deleted"},
		{name: "the Plugins tab's squaremap", pluginsTab: true, deleteMap: true, folder: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.stuck && os.Geteuid() == 0 {
				t.Skip("root deletes a read-only folder's files")
			}
			e, _, _ := newMapEnv(t)
			e.create()
			if c.pluginsTab {
				e.installAddon(webmap.ModrinthProjectID)
			}
			if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
				t.Fatalf("enable: %+v", op)
			}
			e.waitFor("online", e.onlineIdle)
			folder := filepath.Join(e.dataDir(), "plugins", "squaremap")
			if c.stuck {
				stuck := filepath.Join(folder, "web", "tiles", "minecraft_the_nether", "0")
				os.MkdirAll(stuck, 0o755)
				os.WriteFile(filepath.Join(stuck, "0_0.png"), []byte("\x89PNG nether"), 0o644)
				os.Chmod(stuck, 0o500)
				t.Cleanup(func() { os.Chmod(stuck, 0o755) })
			}
			if c.stopped {
				code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
				if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
					t.Fatalf("stop: %d %v", code, out)
				}
			}
			// squaremap draws tiles until the server stops.
			tile := filepath.Join(folder, "web", "tiles", "minecraft_overworld", "3", "9_9.png")
			e.fd.mu.Lock()
			e.fd.stopped = func(*fakeContainer) {
				if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile)); err == nil {
					os.MkdirAll(filepath.Dir(tile), 0o755)
					os.WriteFile(tile, []byte("\x89PNG drawn"), 0o644)
				}
			}
			e.fd.mu.Unlock()
			t.Cleanup(func() {
				e.fd.mu.Lock()
				e.fd.stopped = nil
				e.fd.mu.Unlock()
			})
			started := time.Time{}
			if !c.stopped {
				started = e.startedAt()
			}
			op := e.mapOp("/map/disable", map[string]any{"deleteMap": c.deleteMap})
			if c.says == "" && op.Status != api.OpSucceeded || c.says != "" && (op.Status != api.OpFailed || !strings.HasPrefix(op.Error, c.says)) {
				t.Fatalf("turning the map off: %+v", op)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM maps`); n != 0 {
				t.Fatalf("the map's record is still there")
			}
			_, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile))
			if kept := err == nil; kept != c.pluginsTab {
				t.Fatalf("squaremap kept: %v, want %v", kept, c.pluginsTab)
			}
			if _, err := os.Stat(folder); (err == nil) != c.folder {
				t.Fatalf("squaremap's folder there: %v, want %v", err == nil, c.folder)
			}
			if c.pluginsTab {
				if _, err := os.Stat(filepath.Join(folder, "config.yml")); err != nil {
					t.Fatalf("the Plugins tab's squaremap settings went: %v", err)
				}
			}
			if c.stopped {
				if e.status().Phase != api.PhaseStopped {
					t.Fatalf("the stopped server started: %s", e.status().Phase)
				}
				return
			}
			e.waitFor("online", e.onlineIdle)
			if restarted := e.startedAt().After(started); restarted != c.restarts {
				t.Fatalf("the server restarted: %v, want %v", restarted, c.restarts)
			}
		})
	}
}

// Replacing a world with the map on deletes what squaremap drew of the
// previous world while the server is stopped, and draws the imported one
// once it is online. With the map off, squaremap's folder isn't the Map
// tab's to change.
func TestAReplacedWorldIsDrawnAfresh(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "the map on", false: "the map off"}[on], func(t *testing.T) {
			e, _, _ := newMapEnv(t)
			e.create()
			if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
				t.Fatalf("enable: %+v", op)
			}
			e.waitFor("the first render", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
			})
			if !on {
				if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
					t.Fatalf("disable: %+v", op)
				}
			}
			e.waitFor("online", e.onlineIdle)
			squaremap := filepath.Join(e.dataDir(), "plugins", "squaremap")
			drawn := []string{filepath.Join(squaremap, "web", "tiles", "minecraft_overworld", "3", "0_0.png"), filepath.Join(squaremap, "data", "minecraft_overworld", "regions.dat")}
			for _, f := range drawn {
				os.MkdirAll(filepath.Dir(f), 0o755)
				os.WriteFile(f, []byte("the previous world"), 0o644)
			}
			archive, _ := paperServerUpload(t)
			imp := e.uploadWorld(e.sp("/world-imports"), "paper-server.zip", archive)
			phrase := e.importPreview(imp, map[string]any{}).ConfirmPhrase
			code, out := e.call("POST", importPath(imp, "/apply"), map[string]any{"confirm": phrase, "actor": "admin"})
			if code != 202 {
				t.Fatalf("apply: %d %v", code, out)
			}
			if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
				t.Fatalf("the import: %+v", op)
			}
			e.waitFor("online", e.onlineIdle)
			for _, f := range drawn {
				if _, err := os.Stat(f); (err == nil) == on {
					t.Fatalf("%s there after the import: %v, want %v", f, err == nil, !on)
				}
			}
			if !on {
				return
			}
			e.waitFor("the imported world to be drawn", func() bool {
				return e.rcon.count("squaremap fullrender minecraft:overworld") == 2 &&
					e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
			})
		})
	}
}

// A map change that can't find out whether the server is running doesn't
// claim to be live. With Docker not answering the look at the container,
// turning the map on installs squaremap, then fails saying to restart, and
// the map waits for that restart; turning it off removes nothing, since
// squaremap must stop first. Either way the server stays as it was.
func TestAMapChangeThatCantCheckTheServerSaysToRestart(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e *agentEnv)
		path  string
		body  map[string]any
		says  string
		hint  string
		after func(e *agentEnv)
	}{
		{name: "turning the map on", setup: func(*agentEnv) {}, path: "/map/enable", body: map[string]any{}, says: "squaremap is installed, but", hint: "Restart the server",
			after: func(e *agentEnv) {
				if m := e.mapInfo(); !m.Enabled || m.State != string(webmap.StateNeedsRestart) {
					e.t.Fatalf("the map once Docker answers again: %+v", m)
				}
			}},
		{name: "turning the map off", setup: func(e *agentEnv) {
			if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
				e.t.Fatalf("enable: %+v", op)
			}
			e.waitFor("online", e.onlineIdle)
		}, path: "/map/disable", body: map[string]any{"deleteMap": true}, says: "The map is still on: Playkeeper couldn't tell whether the server is running", hint: "Try again once Docker answers",
			after: func(e *agentEnv) {
				if _, err := os.Stat(filepath.Join(e.dataDir(), "plugins", squaremapFile)); err != nil || e.countRows(`SELECT COUNT(*) FROM maps`) != 1 {
					e.t.Fatalf("the map after it couldn't be turned off: %v", err)
				}
				if m := e.mapInfo(); !m.Enabled {
					e.t.Fatalf("the map once Docker answers again: %+v", m)
				}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, _ := newMapEnv(t)
			e.create()
			c.setup(e)
			started := e.startedAt()
			down := func(prefix string) {
				e.fd.mu.Lock()
				e.fd.down = prefix
				e.fd.mu.Unlock()
			}
			down("/containers/" + e.cname() + "/json")
			op := e.mapOp(c.path, c.body)
			down("")
			if op.Status != api.OpFailed || !strings.HasPrefix(op.Error, c.says) || !strings.Contains(op.Hint, c.hint) {
				t.Fatalf("%s with Docker not answering: %+v", c.name, op)
			}
			if !e.startedAt().Equal(started) {
				t.Fatal("the server restarted")
			}
			c.after(e)
		})
	}
}

// The map owns only what it installed. A Fabric API the Mods tab installed
// first stays when the map is turned off, and so does one the map installed
// that a mod added since needs, which the Mods tab then takes over.
func TestTurningTheMapOffRemovesOnlyWhatItAddedAndNothingElseNeeds(t *testing.T) {
	fabricAPI := addons.Installed{ProjectID: fabricAPIID, Slug: "fabric-api", Name: "Fabric API", VersionID: "fapi1", VersionNumber: "0.119.2+26.1.2", FileName: fabricAPIFile}
	mods := func(e *agentEnv) string { return filepath.Join(e.dataDir(), "mods") }

	t.Run("Fabric API installed before the map", func(t *testing.T) {
		e, src, _ := newMapEnv(t)
		e.fabricMapServer()
		e.modsTabAddon(fabricAPI, src.fabricAPI)
		if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map on with Fabric API there: %+v", op)
		}
		rec, err := e.srv().loadMap()
		if err != nil || rec == nil || !slices.Equal(addonKeys(rec.addons), []string{webmap.ModrinthProjectID}) {
			t.Fatalf("the map owns %v (%v), want only squaremap", addonKeys(rec.addons), err)
		}
		if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map off: %+v", op)
		}
		if _, err := os.Stat(filepath.Join(mods(e), squaremapFabricFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("squaremap stayed after the map was turned off: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(mods(e), fabricAPIFile)); err != nil || !bytes.Equal(got, src.fabricAPI) {
			t.Fatalf("turning the map off took the Mods tab's Fabric API: %v", err)
		}
		if installed, _ := e.srv().installedAddons(); !slices.Equal(addonKeys(installed), []string{fabricAPIID}) {
			t.Fatalf("the Mods tab's records after the map was turned off: %v", addonKeys(installed))
		}
	})

	t.Run("a map turned on before this fix recorded the Mods tab's Fabric API", func(t *testing.T) {
		e, src, _ := newMapEnv(t)
		e.fabricMapServer()
		e.modsTabAddon(fabricAPI, src.fabricAPI)
		if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map on: %+v", op)
		}
		s := e.srv()
		rec, _ := s.loadMap()
		installed, _ := s.installedAddons()
		legacy := installed[0]
		legacy.DependencyOf = webmap.ModrinthProjectID
		rec.addons = append(rec.addons, legacy)
		if err := s.saveMap(rec); err != nil {
			t.Fatal(err)
		}
		if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map off: %+v", op)
		}
		if got, err := os.ReadFile(filepath.Join(mods(e), fabricAPIFile)); err != nil || !bytes.Equal(got, src.fabricAPI) {
			t.Fatalf("turning the map off took the Mods tab's Fabric API: %v", err)
		}
		if installed, _ := s.installedAddons(); !slices.Equal(addonKeys(installed), []string{fabricAPIID}) {
			t.Fatalf("the Mods tab's records after the map was turned off: %v", addonKeys(installed))
		}
	})

	t.Run("a mod added since needs the map's Fabric API", func(t *testing.T) {
		e, src, _ := newMapEnv(t)
		e.fabricMapServer()
		if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map on: %+v", op)
		}
		if rec, err := e.srv().loadMap(); err != nil || rec == nil || !slices.Equal(addonKeys(rec.addons), []string{fabricAPIID, webmap.ModrinthProjectID}) {
			t.Fatalf("the map installed %v (%v), want squaremap and Fabric API", addonKeys(rec.addons), err)
		}
		e.modsTabAddon(addons.Installed{ProjectID: "waystones1", Slug: "waystones", Name: "Waystones", VersionID: "way1", VersionNumber: "21.1.0", FileName: "waystones-fabric-26.1.2.jar", Requires: []string{fabricAPIID}},
			jarOf("fabric.mod.json", `{"schemaVersion":1,"id":"waystones","depends":{"fabric-api":"*"}}`))
		if op := e.mapOp("/map/disable", map[string]any{"deleteMap": false}); op.Status != api.OpSucceeded {
			t.Fatalf("turning the map off: %+v", op)
		}
		if _, err := os.Stat(filepath.Join(mods(e), squaremapFabricFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("squaremap stayed after the map was turned off: %v", err)
		}
		if got, err := os.ReadFile(filepath.Join(mods(e), fabricAPIFile)); err != nil || !bytes.Equal(got, src.fabricAPI) {
			t.Fatalf("turning the map off took the Fabric API Waystones needs: %v", err)
		}
		installed, _ := e.srv().installedAddons()
		i := slices.IndexFunc(installed, func(a addons.Installed) bool { return a.ProjectID == fabricAPIID })
		if !slices.Equal(addonKeys(installed), []string{fabricAPIID, "waystones1"}) || i < 0 || installed[i].DependencyOf != "waystones1" {
			t.Fatalf("the Mods tab takes over Fabric API for Waystones: %+v", installed)
		}
	})
}

// A failed removal keeps the add-on error's own status and kind.
func TestMapAddonErrorsKeepTheirStatus(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	if err := os.WriteFile(filepath.Join(e.dataDir(), "plugins", squaremapFile), []byte("changed by hand"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := e.srv()
	rec, err := s.loadMap()
	if err != nil || rec == nil {
		t.Fatalf("map: %v", err)
	}
	sc, _ := s.serverConfig()
	err = s.removeMapAddons(context.Background(), s.addonServer(s.serverType(nil), *sc), rec.addons, false)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict || ae.Code != string(addons.KindModified) || !strings.Contains(ae.Msg, "has changed since Playkeeper installed it") {
		t.Fatalf("removing a squaremap changed by hand: %#v", err)
	}
}

// A map whose squaremap was deleted by hand counts as off: the Map tab says
// the plugin is gone, the shared link stops working and nothing can be
// shared, and turning the map on installs squaremap again, unshared, and
// draws the explored land again.
func TestTurningOnTheMapInstallsAMissingSquaremapAgain(t *testing.T) {
	e, src, _ := newMapEnv(t)
	e.create()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("the first render to be recorded", func() bool {
		return e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
	})
	e.waitFor("online", e.onlineIdle)
	code, out := e.call("POST", e.sp("/map/share"), map[string]any{"public": true, "actor": "admin"})
	path, _ := out["path"].(string)
	token := strings.TrimPrefix(path, "/map/")
	if code != 200 || !webmap.ValidShareToken(token) {
		t.Fatalf("share: %d %v", code, out)
	}
	if code, _, _ := e.get("/v1/public-maps/" + token); code != 200 {
		t.Fatalf("shared map before: %d", code)
	}

	jar := filepath.Join(e.dataDir(), "plugins", squaremapFile)
	if err := os.Remove(jar); err != nil {
		t.Fatal(err)
	}
	m := e.mapInfo()
	if m.Enabled || !m.Missing || m.State != string(webmap.StateNotInstalled) || m.Public || m.Path != "" || m.PluginVersion != "" {
		t.Fatalf("with squaremap deleted: %+v", m)
	}
	if code, _, _ := e.get("/v1/public-maps/" + token); code != 404 {
		t.Fatalf("the shared link answers %d without squaremap", code)
	}
	if code, out := e.call("POST", e.sp("/map/share"), map[string]any{"players": true, "actor": "admin"}); code != http.StatusConflict {
		t.Fatalf("sharing a map without squaremap: %d %v", code, out)
	}
	if list := e.addonList(); len(list.Files) != 0 || len(list.Missing) != 0 {
		t.Fatalf("the Plugins tab offers what the map lost: %+v", list)
	}

	op := e.mapOp("/map/enable", map[string]any{})
	if op.Status != api.OpSucceeded || op.Detail["reinstalled"] != true {
		t.Fatalf("turning the map on again: %+v", op)
	}
	if _, err := os.Stat(jar); err != nil || src.downloads.Load() != 2 {
		t.Fatalf("squaremap after turning the map on again: %v, %d downloads", err, src.downloads.Load())
	}
	e.waitFor("the explored land to be drawn again", func() bool { return e.rcon.count("squaremap fullrender minecraft:overworld") == 2 })
	e.waitFor("online", e.onlineIdle)
	if m := e.mapInfo(); !m.Enabled || m.Missing || m.Public || m.Path != "" || m.PluginVersion != "1.3.9" {
		t.Fatalf("after turning it on again: %+v", m)
	}
	if code, _, _ := e.get("/v1/public-maps/" + token); code != 404 {
		t.Fatalf("the old link answers %d after the map was turned on again", code)
	}
	if code, out := e.call("POST", e.sp("/map/enable"), map[string]any{"actor": "admin"}); code != http.StatusConflict || out["error"] != "The map is already on." {
		t.Fatalf("turning it on twice: %d %v", code, out)
	}
}

func TestMapWaitsForPlayersBeforeRestarting(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.rcon.setOnline("Alex")
	e.waitFor("Alex to be seen", func() bool { return e.srv().playersOnline() == 1 })

	started := e.startedAt()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	if !e.startedAt().Equal(started) {
		t.Fatal("the server restarted while Alex was playing")
	}
	if m := e.mapInfo(); m.State != string(webmap.StateNeedsRestart) || !e.status().PendingRestart {
		t.Fatalf("state = %+v, pending %v", m, e.status().PendingRestart)
	}
	if n := e.rcon.count("squaremap fullrender minecraft:overworld"); n != 0 {
		t.Fatal("squaremap is not loaded yet, so nothing should be drawn")
	}

	code, out := e.call("POST", e.sp("/map/restart-later"), map[string]any{"actor": "admin"})
	if code != 200 || out["restartWhenEmpty"] != true {
		t.Fatalf("restart later: %d %v", code, out)
	}
	time.Sleep(5 * e.a.opts.SampleInterval)
	if !e.startedAt().Equal(started) {
		t.Fatal("the server restarted while Alex was still playing")
	}

	e.rcon.setOnline()
	e.waitFor("the restart once nobody plays", func() bool {
		return e.startedAt().After(started) && e.onlineIdle()
	})
	if m := e.mapInfo(); m.State == string(webmap.StateNeedsRestart) || m.RestartWhenEmpty || e.status().PendingRestart {
		t.Fatalf("after the restart: %+v", m)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'restart' AND actor = 'admin' AND status = 'succeeded'`); n != 1 {
		t.Fatalf("restarts = %d", n)
	}
	e.waitFor("the first render", func() bool { return e.rcon.count("squaremap fullrender minecraft:overworld") == 1 })
}

// A map that was never drawn is drawn after whatever brings the server
// online: a restart, a stop and a start, a crash and the automatic restart,
// the agent restarting, or a Playkeeper update, the server running all
// along in the last two. squaremap never answers the run that loaded it
// here, and that run's wait has long stopped asking often, so each run
// must wait afresh and ask at once.
func TestEveryRunThatComesOnlineGetsTheFirstRender(t *testing.T) {
	defer func(w time.Duration) { firstRenderWait = w }(firstRenderWait)
	firstRenderWait = 0
	do := func(e *agentEnv, verb string) {
		e.t.Helper()
		code, out := e.call("POST", e.sp(verb), map[string]any{"actor": "admin"})
		if code != http.StatusAccepted || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
			e.t.Fatalf("%s: %d %v", verb, code, out)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(e *agentEnv)
		// event brings the server online again; release lets squaremap
		// answer from then on.
		event func(e *agentEnv, release func())
	}{
		{name: "a restart", event: func(e *agentEnv, release func()) {
			release()
			do(e, "/restart")
		}},
		{name: "a stop, then a start", event: func(e *agentEnv, release func()) {
			release()
			do(e, "/stop")
			do(e, "/start")
		}},
		{name: "a crash and the automatic restart", event: func(e *agentEnv, release func()) {
			release()
			started := e.startedAt()
			e.fd.crash(137)
			e.waitFor("the automatic restart", func() bool { return e.startedAt().After(started) && e.onlineIdle() })
		}},
		{name: "the agent restarting", event: func(e *agentEnv, release func()) {
			e.stop()
			release()
			e.start()
		}},
		{name: "a Playkeeper update", setup: func(e *agentEnv) { e.useReleases() }, event: func(e *agentEnv, release func()) {
			if op := e.applyUpdate("0.2.1"); op.Status != api.OpRunning || op.Phase != "restarting" {
				e.t.Fatalf("update: %+v", op)
			}
			// The updater takes the request and starts the new agent.
			e.stop()
			release()
			dir := filepath.Join(e.cfg.AgentDir(), "update")
			if err := os.Rename(filepath.Join(dir, update.RequestFile), filepath.Join(dir, update.ApplyingFile)); err != nil {
				e.t.Fatal(err)
			}
			e.start()
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, sq := newMapEnvWith(t, c.setup)
			e.create()
			sq.hold.Store(true)
			if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
				t.Fatalf("enable: %+v", op)
			}
			e.waitFor("the first run's wait to ask squaremap", func() bool { return sq.held.Load() > 0 })
			c.event(e, func() { sq.hold.Store(false) })
			e.waitFor("the first render", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM maps WHERE first_render_at IS NOT NULL`) == 1
			})
			time.Sleep(200 * time.Millisecond)
			if n := e.rcon.count("squaremap fullrender minecraft:overworld"); n != 1 {
				t.Fatalf("fullrender sent %d times", n)
			}
		})
	}
}

// Putting off the restart that loads the map until nobody plays is taken
// only while squaremap needs that restart. A server that has loaded
// squaremap, or isn't running, is refused and not restarted for it; a
// restart put off before a start some other way loaded squaremap is dropped
// once nobody plays, not done.
func TestRestartLaterOnlyWhileSquaremapNeedsARestart(t *testing.T) {
	for _, c := range []struct {
		name    string
		playing bool // Alex plays as the map is turned on, so squaremap waits for a restart
		stopped bool
		earlier bool // the restart was put off before squaremap was loaded
		code    int
		says    string
		kept    int
	}{
		{name: "squaremap needs a restart", playing: true, code: http.StatusOK, kept: 1},
		{name: "squaremap loaded", code: http.StatusConflict, says: "The server has already loaded the map, so it doesn't need a restart."},
		{name: "server stopped", stopped: true, code: http.StatusConflict, says: "The server isn't running. It loads the map when it starts."},
		{name: "put off before squaremap was loaded", earlier: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, _, _ := newMapEnv(t)
			e.create()
			if c.playing {
				e.rcon.setOnline("Alex")
				e.waitFor("Alex to be seen", func() bool { return e.srv().playersOnline() == 1 })
			}
			if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
				t.Fatalf("enable: %+v", op)
			}
			if c.stopped {
				code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
				if code != http.StatusAccepted || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
					t.Fatalf("stop: %d %v", code, out)
				}
			}
			started := e.startedAt()
			if c.earlier {
				if _, err := e.a.db.Exec(`UPDATE maps SET restart_when_empty = 'admin'`); err != nil {
					t.Fatal(err)
				}
			} else {
				code, out := e.call("POST", e.sp("/map/restart-later"), map[string]any{"actor": "admin"})
				if code != c.code || (c.says != "" && out["error"] != c.says) || (code == http.StatusOK && out["restartWhenEmpty"] != true) {
					t.Fatalf("restart later: %d %v", code, out)
				}
			}
			time.Sleep(5 * e.a.opts.SampleInterval)
			if !e.startedAt().Equal(started) {
				t.Fatal("the server was restarted")
			}
			if n := e.countRows(`SELECT COUNT(*) FROM maps WHERE restart_when_empty != ''`); n != c.kept {
				t.Fatalf("%d restarts put off, want %d", n, c.kept)
			}
		})
	}
}

func (f *fakeSquaremapWeb) asked(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.paths {
		if p == path {
			return true
		}
	}
	return false
}

// withCertificate saves a certificate for the address's name that expires
// at notAfter, as getting one from Let's Encrypt does.
func (e *agentEnv) withCertificate(st addressState, notAfter time.Time) {
	e.t.Helper()
	c := &certs.Certificate{Names: []string{st.Host}, NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter, RenewAt: notAfter.Add(-30 * 24 * time.Hour), Issuer: "Let's Encrypt R12"}
	if err := e.a.saveCertificate(&certRow{name: st.Host, source: st.Kind, challenge: "http-01", status: certs.Status{Names: []string{st.Host}, Certificate: c}}); err != nil {
		e.t.Fatal(err)
	}
}

// nameWorks gives the machine an own domain others can open the dashboard
// at: it points at the machine, so the address loop's own checks find it
// ready, and it has a certificate.
func (e *agentEnv) nameWorks(host string) {
	e.t.Helper()
	e.a.opts.Resolver.(*fakeResolver).set(host, testIP.String())
	st := addressState{Kind: api.AddressOwn, Host: host, Check: &api.AddressCheck{Ready: true}}
	if err := e.a.setAddress(st); err != nil {
		e.t.Fatal(err)
	}
	e.withCertificate(st, e.a.now().Add(60*24*time.Hour))
}

// The shared map's link is on the machine's name only once others can open
// the dashboard there, as for a friends' pack; until then the Sharing card
// copies the address the dashboard was opened at and suggests an address.
func TestSharedMapLinkWaitsForAWorkingName(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.createWith(map[string]any{"name": "Survival"})
	token := webmap.NewShareToken()
	rec := &mapRecord{public: true, shareToken: token}
	active := func(dns string) *freeState {
		return &freeState{Name: names.Name{Name: "alex", State: names.StateActive, DNS: dns}}
	}
	own := func(ready bool) *api.AddressCheck { return &api.AddressCheck{Ready: ready} }
	later, earlier := e.a.now().Add(60*24*time.Hour), e.a.now().Add(-time.Hour)
	cases := []struct {
		what string
		st   addressState
		cert time.Time
		want string
	}{
		{"no address", addressState{Kind: api.AddressNone}, time.Time{}, ""},
		{"an own domain not checked yet", addressState{Kind: api.AddressOwn, Host: "play.example.com"}, later, ""},
		{"an own domain that points elsewhere", addressState{Kind: api.AddressOwn, Host: "play.example.com", Check: own(false)}, later, ""},
		{"an own domain without a certificate", addressState{Kind: api.AddressOwn, Host: "play.example.com", Check: own(true)}, time.Time{}, ""},
		{"an own domain whose certificate expired", addressState{Kind: api.AddressOwn, Host: "play.example.com", Check: own(true)}, earlier, ""},
		{"a working own domain", addressState{Kind: api.AddressOwn, Host: "play.example.com", Check: own(true)}, later, "https://play.example.com:8443/map/" + token},
		{"a free name being claimed", addressState{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.io"}, later, ""},
		{"a free name still publishing", addressState{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.io", Free: active(names.DNSPending)}, later, ""},
		{"a lapsed free name", addressState{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.io", Free: &freeState{Name: names.Name{Name: "alex", State: names.StateLapsed, DNS: names.DNSOK}}}, later, ""},
		{"a free name without a certificate", addressState{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.io", Free: active(names.DNSOK)}, time.Time{}, ""},
		{"a working free name", addressState{Kind: api.AddressPlaykeeper, Host: "alex.playkeeper.io", Free: active(names.DNSOK)}, later, "https://alex.playkeeper.io:8443/map/" + token},
	}
	for _, c := range cases {
		e.a.forgetCertificate("play.example.com")
		e.a.forgetCertificate("alex.playkeeper.io")
		if err := e.a.setAddress(c.st); err != nil {
			t.Fatal(err)
		}
		if !c.cert.IsZero() {
			e.withCertificate(c.st, c.cert)
		}
		if got := e.srv().mapLink(rec); got != c.want {
			t.Errorf("%s: the map's link is %q, want %q", c.what, got, c.want)
		}
	}
	e.nameWorks("play.example.com")
	if got := e.srv().mapLink(&mapRecord{shareToken: token}); got != "" {
		t.Errorf("a map that isn't shared has the link %q", got)
	}
}

func TestSharedMapAnswersOnlyWhileItsSwitchIsOn(t *testing.T) {
	e, _, sq := newMapEnv(t)
	e.nameWorks("play.example.com")
	e.createWith(map[string]any{"name": "Survival"})
	bySlug := "/v1/public-maps/" + e.slug()
	unknown := "/v1/public-maps/" + webmap.NewShareToken()

	code, _, unavailable := e.get(bySlug)
	if code != 404 || !bytes.Contains(unavailable, []byte("This map isn't available.")) || bytes.Contains(unavailable, []byte(e.srv().name())) {
		t.Fatalf("map off: %d %s", code, unavailable)
	}
	same := func(what string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			code, h, body := e.get(p)
			if code != 404 || !bytes.Equal(body, unavailable) || h.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s, %s: %d %v %s", what, p, code, h, body)
			}
		}
	}
	share := func(body map[string]any) (string, map[string]any) {
		t.Helper()
		body["actor"] = "admin"
		code, out := e.call("POST", e.sp("/map/share"), body)
		if code != 200 {
			t.Fatalf("share %v: %d %v", body, code, out)
		}
		path, _ := out["path"].(string)
		return strings.TrimPrefix(path, "/map/"), out
	}
	players := func(p string) []webmap.Player {
		t.Helper()
		code, h, body := e.get(p)
		var got webmap.Players
		if code != 200 || h.Get("Cache-Control") != "no-store" || json.Unmarshal(body, &got) != nil || got.Players == nil {
			t.Fatalf("players %s: %d %v %s", p, code, h, body)
		}
		return got.Players
	}

	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if m := e.mapInfo(); m.Public || m.Path != "" || m.Link != "" {
		t.Fatalf("before sharing: %+v", m)
	}
	same("not shared", bySlug, unknown, unknown+"/tiles/minecraft_overworld/3/0_0.png")

	first, out := share(map[string]any{"public": true})
	if !webmap.ValidShareToken(first) || out["public"] != true || out["publicPlayers"] != false || out["link"] != "https://play.example.com:8443/map/"+first {
		t.Fatalf("share: %v", out)
	}
	pub := "/v1/public-maps/" + first
	code, _, body := e.get(pub)
	if code != 200 || string(bytes.TrimSpace(body)) != `{"name":"Survival","players":false}` {
		t.Fatalf("shared: %d %s", code, body)
	}
	if code, _, body := e.get(pub + "/worlds"); code != 200 || !bytes.Contains(body, []byte("minecraft_overworld")) || bytes.Contains(body, []byte("Alex")) {
		t.Fatalf("shared worlds: %d %s", code, body)
	}
	if code, h, _ := e.get(pub + "/tiles/minecraft_overworld/3/0_0.png"); code != 200 || h.Get("Content-Type") != "image/png" {
		t.Fatalf("shared tile: %d %v", code, h)
	}
	same("no icon of its own", pub+"/icon")
	icon := []byte("\x89PNG\r\n\x1a\nserver icon")
	if err := os.WriteFile(filepath.Join(e.dataDir(), iconFile), icon, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, h, body := e.get(pub + "/icon"); code != 200 || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "no-store" || !bytes.Equal(body, icon) {
		t.Fatalf("shared icon: %d %v %q", code, h, body)
	}
	if got := players(pub + "/players"); len(got) != 0 {
		t.Fatalf("players while they are hidden: %+v", got)
	}
	if sq.asked("/tiles/players.json") {
		t.Fatal("squaremap was asked who is online while players are hidden")
	}
	same("other paths", pub+"/settings")
	same("the server's slug", bySlug, bySlug+"/worlds", bySlug+"/players", bySlug+"/tiles/minecraft_overworld/3/0_0.png")
	same("an unknown token", unknown, unknown+"/worlds", unknown+"/players")
	same("malformed tokens", pub[:len(pub)-1], pub[:len(pub)-1]+"/worlds", pub+"x", "/v1/public-maps/Bad_Slug")

	if tok, out := share(map[string]any{"players": true}); tok != first || out["publicPlayers"] != true {
		t.Fatalf("share players: %v", out)
	}
	if got := players(pub + "/players"); len(got) != 1 || got[0].Name != "Alex" {
		t.Fatalf("players while they are shown: %+v", got)
	}
	if tok, _ := share(map[string]any{"public": true}); tok != first {
		t.Fatalf("sharing a shared map again changed its link to %s", tok)
	}

	// squaremap reads its link at startup.
	code, out = e.call("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("restart: %d %v", code, out)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "squaremap", "config.yml"))
	if !bytes.Contains(cfg, []byte("web-address: 'https://play.example.com:8443/map/"+first+"'")) {
		t.Fatalf("config:\n%s", cfg)
	}
	e.waitFor("online", e.onlineIdle)
	e.srv().forgetMapLive()

	if tok, out := share(map[string]any{"public": false}); tok != "" || out["link"] != nil {
		t.Fatalf("unshare: %v", out)
	}
	same("turned off", pub, pub+"/worlds", pub+"/players", pub+"/tiles/minecraft_overworld/3/0_0.png", pub+"/icon")

	second, out := share(map[string]any{"public": true})
	if !webmap.ValidShareToken(second) || second == first || out["link"] != "https://play.example.com:8443/map/"+second {
		t.Fatalf("shared again: %v", out)
	}
	same("the old link", pub, pub+"/worlds", pub+"/players", pub+"/tiles/minecraft_overworld/3/0_0.png")
	next := "/v1/public-maps/" + second
	if code, _, body := e.get(next); code != 200 || string(bytes.TrimSpace(body)) != `{"name":"Survival","players":true}` {
		t.Fatalf("the new link: %d %s", code, body)
	}

	code, out = e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("stop: %d %v", code, out)
	}
	e.srv().forgetMapLive()
	same("stopped", next, next+"/tiles/minecraft_overworld/3/0_0.png", next+"/icon", next+"/players")

	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'map.share'`); n != 5 {
		t.Fatalf("share audits = %d", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE instr(detail, ?) > 0 OR instr(detail, ?) > 0`, first, second); n != 0 {
		t.Fatalf("%d audit rows hold a link token", n)
	}
	if code, _ := e.call("POST", e.sp("/map/share"), map[string]any{"actor": "admin"}); code != 400 {
		t.Fatalf("share without a switch: %d", code)
	}
}

func TestMapIsNotOfferedOnVanilla(t *testing.T) {
	e, src, _ := newMapEnv(t)
	e.addIdleServer()
	if _, err := e.a.db.Exec(`UPDATE servers SET type = 'vanilla' WHERE id = ?`, e.sid); err != nil {
		t.Fatal(err)
	}
	if m := e.mapInfo(); m.Supported || m.State != string(webmap.StateUnsupported) {
		t.Fatalf("vanilla: %+v", m)
	}
	code, out := e.call("POST", e.sp("/map/enable"), map[string]any{"actor": "admin"})
	if code != 400 || out["error"] != "Vanilla servers cannot show a live map." {
		t.Fatalf("enable on vanilla: %d %v", code, out)
	}
	if src.downloads.Load() != 0 {
		t.Fatal("nothing should be downloaded")
	}
}

func TestMapSettingsThatCannotBeWrittenStopTheStart(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("stop: %d %v", code, out)
	}
	dir := filepath.Join(e.dataDir(), "plugins", "squaremap")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "cannot write squaremap's settings in plugins/squaremap") {
		t.Fatalf("start with a linked squaremap folder: %+v", op)
	}
	if running := e.fd.containerCount(containerPrefix); running != 0 && e.status().Phase == api.PhaseOnline {
		t.Fatal("the server must not start without Playkeeper's squaremap settings")
	}
}

func TestDeletingAServerForgetsItsMap(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	code, out := e.call("POST", e.sp("/delete"), map[string]any{"confirm": e.srv().name(), "actor": "admin"})
	if code != 202 || e.waitOp(out["id"].(string)).Status != api.OpSucceeded {
		t.Fatalf("delete: %d %v", code, out)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM maps`); n != 0 {
		t.Fatalf("maps rows = %d", n)
	}
}
