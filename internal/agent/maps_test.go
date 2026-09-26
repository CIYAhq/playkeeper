package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

const squaremapFile = "squaremap-paper-mc26.1.2-1.3.9.jar"

// fakeMapSource serves squaremap's Modrinth project and its Paper build for
// 26.1.2 from local TLS servers.
type fakeMapSource struct {
	api, cdn  *httptest.Server
	downloads atomic.Int32
}

func startFakeMapSource(t *testing.T) *fakeMapSource {
	t.Helper()
	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	w, _ := zw.Create("plugin.yml")
	io.WriteString(w, "name: squaremap\nversion: 1.3.9\nmain: xyz.jpenilla.squaremap.paper.SquaremapPaper\n")
	zw.Close()
	data := jar.Bytes()
	f := &fakeMapSource{}
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data/"+webmap.ModrinthProjectID+"/versions/sqm1/"+squaremapFile {
			http.NotFound(w, r)
			return
		}
		f.downloads.Add(1)
		w.Write(data)
	}))
	s512, s1 := sha512.Sum512(data), sha1.Sum(data)
	project := map[string]any{
		"id": webmap.ModrinthProjectID, "slug": "squaremap", "project_type": "mod", "title": "squaremap",
		"loaders": []string{"fabric", "neoforge", "paper"}, "game_versions": []string{"26.1.2"},
		"client_side": "optional", "server_side": "required", "status": "approved", "license": map[string]any{"id": "MIT"},
		"published": "2021-11-12T00:00:00Z", "updated": "2026-09-01T00:00:00Z", "versions": []string{"sqm1"},
	}
	version := map[string]any{
		"id": "sqm1", "project_id": webmap.ModrinthProjectID, "name": "squaremap 1.3.9", "version_number": "1.3.9",
		"version_type": "release", "status": "listed", "game_versions": []string{"26.1.2"}, "loaders": []string{"paper"},
		"date_published": "2026-09-01T00:00:00Z", "dependencies": []any{},
		"files": []any{map[string]any{
			"hashes":   map[string]string{"sha512": hex.EncodeToString(s512[:]), "sha1": hex.EncodeToString(s1[:])},
			"url":      f.cdn.URL + "/data/" + webmap.ModrinthProjectID + "/versions/sqm1/" + squaremapFile,
			"filename": squaremapFile, "primary": true, "size": len(data),
		}},
	}
	f.api = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/project/" + webmap.ModrinthProjectID, "/v2/project/squaremap":
			json.NewEncoder(w).Encode(project)
		case "/v2/project/" + webmap.ModrinthProjectID + "/version":
			json.NewEncoder(w).Encode([]any{version})
		case "/v2/projects":
			json.NewEncoder(w).Encode([]any{project})
		case "/v2/version_files":
			io.WriteString(w, "{}")
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
type fakeSquaremapWeb struct {
	srv   *httptest.Server
	mu    sync.Mutex
	paths []string
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
// whose servers' squaremap is the fake web server.
func newMapEnv(t *testing.T) (*agentEnv, *fakeMapSource, *fakeSquaremapWeb) {
	t.Helper()
	e := newAgentEnv(t)
	src, sq := startFakeMapSource(t), startFakeSquaremapWeb(t)
	e.a.opts.Addons = src.library(t)
	addr := sq.srv.Listener.Addr().String()
	e.a.opts.MapAddr = func(string) string { return addr }
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

func TestSharedMapAnswersOnlyWhileItsSwitchIsOn(t *testing.T) {
	e, _, sq := newMapEnv(t)
	e.a.cfg.Domain = "play.example.com"
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
