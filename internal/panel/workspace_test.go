package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/version"
)

func (e *env) reply(method, path, body string) {
	e.agent.mu.Lock()
	e.agent.replies[method+" "+path] = body
	e.agent.mu.Unlock()
}

func TestWorkspaceHasAProjectAndThisMachine(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	e.reply("GET", "/v1/machine", `{"hostname":"my-vps","agentVersion":"0.3.0","memoryTotalMB":16384}`)
	var machines []map[string]any
	r := e.get(t, "/api/machines", cookie, &machines)
	if r != 200 || len(machines) != 1 || machines[0]["kind"] != "local" || machines[0]["name"] != "my-vps" {
		t.Fatalf("machines: %d %v", r, machines)
	}
	live, _ := machines[0]["live"].(map[string]any)
	if live["memoryTotalMB"] != float64(16384) {
		t.Fatalf("the machine carries what its agent reports: %v", machines[0])
	}
	var projects []map[string]any
	if r := e.get(t, "/api/projects", cookie, &projects); r != 200 || len(projects) != 1 || projects[0]["role"] != "admin" {
		t.Fatalf("the first account belongs to the one project: %d %v", r, projects)
	}
	me := e.do(t, "GET", "/api/auth/me", "", auth(cookie, ""))
	if user, _ := me.body["user"].(map[string]any); user["role"] != "owner" {
		t.Fatalf("the first account owns the install: %v", me.body)
	}
	// A second start of the panel keeps the one project and machine.
	s2, err := New(Options{Config: e.cfg, Now: e.clock.now, Agent: e.srv.agent})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if list, _ := s2.machines(); len(list) != 1 || list[0].ID != machines[0]["id"] {
		t.Fatalf("machines after a restart: %v", list)
	}
}

func TestServersAreListedWithTheirMachine(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"},{"id":"bcdefghjkm","name":"Creative","phase":"stopped"}]`)
	var machines []map[string]any
	e.get(t, "/api/machines", cookie, &machines)
	mid := machines[0]["id"].(string)
	var servers []map[string]any
	if r := e.get(t, "/api/servers", cookie, &servers); r != 200 || len(servers) != 2 || servers[1]["name"] != "Creative" || servers[0]["machineId"] != mid {
		t.Fatalf("servers: %d %v", r, servers)
	}
	if r := e.do(t, "POST", "/api/machines/"+mid+"/servers", `{"name":"Skyblock"}`, auth(cookie, csrf)); r.status != 200 {
		t.Fatalf("create on the machine: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/machines/zzzzzzzzzz/servers", `{}`, auth(cookie, csrf)); r.status != http.StatusNotFound {
		t.Fatalf("an unknown machine: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/servers/..%2Fetc", "", auth(cookie, "")); r.status != http.StatusBadRequest {
		t.Fatalf("a bad server id: %d", r.status)
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	if !contains(e.agent.hits, "POST /v1/servers") {
		t.Fatalf("the create went to the machine's agent: %v", e.agent.hits)
	}
}

// A dashboard left open from 0.2.0 polls GET /api/server while Playkeeper
// updates, and reloads once the new version answers there.
func TestLegacyStatusForDashboardsFromBefore(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	e.reply("GET", "/v1/machine", `{"agentVersion":"0.3.0","defaultGamePort":25565}`)
	e.reply("GET", "/v1/servers", `[]`)
	r := e.do(t, "GET", "/api/server", "", auth(cookie, ""))
	if r.status != 200 || r.body["agentVersion"] != "0.3.0" || r.body["exists"] != false || r.body["phase"] != "not_created" || r.body["updateInstalling"] != nil {
		t.Fatalf("no server yet: %d %v", r.status, r.body)
	}
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","exists":true,"phase":"online","startedAt":"2026-09-24T11:00:00Z","config":{"motd":"Hi","maxPlayers":7}}]`)
	e.reply("GET", "/v1/machine", `{"agentVersion":"0.3.0","updateInstalling":"0.3.1"}`)
	r = e.do(t, "GET", "/api/server", "", auth(cookie, ""))
	if r.status != 200 || r.body["phase"] != "online" || r.body["startedAt"] != "2026-09-24T11:00:00Z" || r.body["updateInstalling"] != "0.3.1" {
		t.Fatalf("the first server, in the old shape: %d %v", r.status, r.body)
	}
	if cfg, _ := r.body["config"].(map[string]any); cfg["maxPlayers"] != float64(7) {
		t.Fatalf("its settings: %v", r.body)
	}
}

func TestMembersCanLookButNotManage(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('friend', ?, 0, 0, 'member')`, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"friend","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != 200 {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	cookie, csrf := r.cookie, r.body["csrfToken"].(string)
	if r := e.do(t, "GET", "/api/servers/"+sampleServer, "", auth(cookie, "")); r.status != 200 {
		t.Fatalf("a member may look: %d %v", r.status, r.body)
	}
	for _, p := range []string{"/api/servers/" + sampleServer + "/stop", "/api/servers/" + sampleServer + "/command"} {
		if r := e.do(t, "POST", p, `{}`, auth(cookie, csrf)); r.status != http.StatusForbidden {
			t.Errorf("a member may not use %s: %d", p, r.status)
		}
	}
	if r := e.do(t, "GET", "/api/audit", "", auth(cookie, "")); r.status != http.StatusForbidden {
		t.Errorf("a member may not read the audit log: %d", r.status)
	}
}

func TestPreferencesAreKeptPerAccount(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	r := e.do(t, "POST", "/api/me/prefs", `{"checklist.hidden.abcdefghjk":"1","theme":"light"}`, auth(cookie, csrf))
	if r.status != 200 || r.body["checklist.hidden.abcdefghjk"] != "1" {
		t.Fatalf("set: %d %v", r.status, r.body)
	}
	r = e.do(t, "POST", "/api/me/prefs", `{"theme":""}`, auth(cookie, csrf))
	if r.status != 200 || r.body["theme"] != nil || r.body["checklist.hidden.abcdefghjk"] != "1" {
		t.Fatalf("an empty value removes a key: %d %v", r.status, r.body)
	}
	for _, bad := range []string{`{"Bad Key":"1"}`, `{}`, `{"x":` + strings.Repeat(`"`, 1) + strings.Repeat("a", 600) + `"}`} {
		if r := e.do(t, "POST", "/api/me/prefs", bad, auth(cookie, csrf)); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
}

// skin draws a 64×64 skin whose face is red with a blue hat pixel.
func skin(t *testing.T, height int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 64, height))
	for y := 8; y < 16; y++ {
		for x := 8; x < 16; x++ {
			img.Set(x, y, color.NRGBA{200, 30, 30, 255})
		}
	}
	img.Set(41, 9, color.NRGBA{20, 40, 220, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func textures(url string) string {
	v, _ := json.Marshal(map[string]any{"textures": map[string]any{"SKIN": map[string]any{"url": url}}})
	return base64.StdEncoding.EncodeToString(v)
}

func TestPlayerFacesComeFromTheirOwnSkin(t *testing.T) {
	var lookups atomic.Int32
	var skins *httptest.Server
	skins = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/texture/custom" {
			w.Write(skin(t, 64))
			return
		}
		http.NotFound(w, r)
	}))
	defer skins.Close()
	mojang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		switch r.URL.Path {
		case "/users/profiles/minecraft/maraK":
			io.WriteString(w, `{"id":"0123456789abcdef0123456789abcdef","name":"mara_k"}`)
		case "/users/profiles/minecraft/elsewhere":
			io.WriteString(w, `{"id":"2123456789abcdef0123456789abcdef","name":"elsewhere"}`)
		case "/session/minecraft/profile/0123456789abcdef0123456789abcdef":
			io.WriteString(w, `{"name":"mara_k","properties":[{"name":"textures","value":"`+textures(skins.URL+"/texture/custom")+`"}]}`)
		case "/session/minecraft/profile/2123456789abcdef0123456789abcdef":
			io.WriteString(w, `{"name":"elsewhere","properties":[{"name":"textures","value":"`+textures("https://evil.example/texture/x")+`"}]}`)
		case "/session/minecraft/profile/3123456789abcdef0123456789abcdef":
			io.WriteString(w, `{"name":"somebody","properties":[{"name":"textures","value":"`+textures(skins.URL+"/texture/5c500205248f3af53ea628f862ebf756fe8e7c9ec8afa4bd963fed1497f46ee1")+`"}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mojang.Close()
	e := newEnvWith(t, &HeadSources{ProfilesURL: mojang.URL, SessionURL: mojang.URL, TexturesURL: skins.URL, Client: &http.Client{Timeout: 5 * time.Second}})
	cookie, _ := e.setup(t)

	req, _ := http.NewRequest("GET", e.ts.URL+"/api/players/maraK/head", nil)
	req.Header.Set("Cookie", cookieName+"="+cookie)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("a player's face: %d %s", resp.StatusCode, body)
	}
	face, err := png.Decode(bytes.NewReader(body))
	if err != nil || face.Bounds().Dx() != 8 || face.Bounds().Dy() != 8 {
		t.Fatalf("the face is 8×8: %v %v", face.Bounds(), err)
	}
	if r, _, b, _ := face.At(0, 0).RGBA(); r>>8 != 200 || b>>8 != 30 {
		t.Fatalf("the face comes from the skin: %v", face.At(0, 0))
	}
	if _, _, b, _ := face.At(1, 1).RGBA(); b>>8 != 220 {
		t.Fatalf("the hat layer lies over the face: %v", face.At(1, 1))
	}
	before := lookups.Load()
	if r := e.do(t, "GET", "/api/players/maraK/head", "", auth(cookie, "")); r.status != 200 || lookups.Load() != before {
		t.Fatalf("a cached face is not looked up again: %d, %d lookups", r.status, lookups.Load()-before)
	}
	for name, why := range map[string]string{"nobody_here": "an unknown player", "elsewhere": "a skin outside the textures host"} {
		if r := e.do(t, "GET", "/api/players/"+name+"/head", "", auth(cookie, "")); r.status != http.StatusNotFound {
			t.Errorf("%s gets no face: %d", why, r.status)
		}
	}
	if r := e.do(t, "GET", "/api/players/somebody/head?uuid=3123456789abcdef0123456789abcdef", "", auth(cookie, "")); r.status != http.StatusNotFound {
		t.Errorf("a player on a default skin gets initials, not Mojang art: %d", r.status)
	}
	// Faces are cached by name: another player's UUID must not put their face under this name.
	if r := e.do(t, "GET", "/api/players/Alice/head?uuid=0123456789abcdef0123456789abcdef", "", auth(cookie, "")); r.status != http.StatusNotFound {
		t.Errorf("a UUID that isn't Alice's gives no face: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/players/MARA_K/head?uuid=0123456789abcdef0123456789abcdef", "", auth(cookie, "")); r.status != http.StatusOK {
		t.Errorf("a UUID matches its name in any case: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/players/bad;name/head", "", auth(cookie, "")); r.status != http.StatusBadRequest {
		t.Errorf("a bad name: %d", r.status)
	}
	if got, err := cropFace(skin(t, 32)); err != nil || len(got) == 0 {
		t.Errorf("an old 64×32 skin has a face too: %v", err)
	}
	if _, err := cropFace([]byte("not a png")); err == nil {
		t.Error("a file that is not a skin must be refused")
	}
}

func TestTheSignInPageGetsOnlyTheMachinesNameAndTheVersion(t *testing.T) {
	e := newEnv(t)
	var st map[string]any
	if r := e.get(t, "/api/setup/status", "", &st); r != 200 || st["needsSetup"] != true {
		t.Fatalf("before the first account: %d %v", r, st)
	}
	e.setup(t)
	e.agent.mu.Lock()
	hits := len(e.agent.hits)
	e.agent.mu.Unlock()
	host, _ := os.Hostname()
	st = nil
	if r := e.get(t, "/api/setup/status", "", &st); r != 200 || st["needsSetup"] != false || st["machine"] != host || st["version"] != version.Version {
		t.Fatalf("a machine without a name of its own goes by its hostname, as in its certificate: %d %v", r, st)
	}
	if _, err := e.srv.db.Exec(`UPDATE machines SET name = 'my-vps' WHERE kind = ?`, localKind); err != nil {
		t.Fatal(err)
	}
	st = nil
	e.get(t, "/api/setup/status", "", &st)
	if st["machine"] != "my-vps" || len(st) != 3 {
		t.Fatalf("the sign-in page gets the name the dashboard shows, the version and nothing more: %v", st)
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	if len(e.agent.hits) != hits {
		t.Errorf("the sign-in page must not ask the agent: %v", e.agent.hits[hits:])
	}
}

func (e *env) get(t *testing.T, path, cookie string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
	req.Header.Set("Cookie", cookieName+"="+cookie)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
