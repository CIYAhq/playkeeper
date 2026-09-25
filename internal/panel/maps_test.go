package panel

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/config"
)

const indexPage = "<!doctype html><title>Playkeeper</title><div id=root></div>"

// scriptedAgent answers the panel from a handler and records what reached
// it, with the headers and query each request carried.
type scriptedAgent struct {
	mu   sync.Mutex
	hits []*http.Request
}

func (a *scriptedAgent) seen() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*http.Request(nil), a.hits...)
}

func (a *scriptedAgent) forget() {
	a.mu.Lock()
	a.hits = nil
	a.mu.Unlock()
}

func newScriptedEnv(t *testing.T, h http.HandlerFunc) (*env, *scriptedAgent) {
	t.Helper()
	return newScriptedEnvLogging(t, h, io.Discard)
}

// newScriptedEnvLogging is newScriptedEnv with the panel's log written to
// logs.
func newScriptedEnvLogging(t *testing.T, h http.HandlerFunc, logs io.Writer) (*env, *scriptedAgent) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	sa := &scriptedAgent{}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sa.mu.Lock()
		sa.hits = append(sa.hits, r.Clone(r.Context()))
		sa.mu.Unlock()
		h(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = sock
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	s, err := New(Options{Config: cfg, Now: clk.now, Logger: slog.New(slog.NewTextHandler(logs, nil)), Agent: agentclient.New(sock),
		IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, Static: fstest.MapFS{"index.html": {Data: []byte(indexPage)}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{srv: s, ts: ts, clock: clk, cfg: cfg}, sa
}

// raw sends a request and returns the whole answer.
func (e *env) raw(t *testing.T, method, path string, body io.Reader, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, b
}

var tilePNG = []byte("\x89PNG\r\n\x1a\ntile bytes")

func agentUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"error":"This map isn't available.","code":"not_found","hint":"Ask whoever shared it for a new link."}`)
}

func TestMapTabCallsReachTheServersAgent(t *testing.T) {
	e, agent := newScriptedEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/map/tiles/minecraft_overworld/3/0_-1.png"):
			w.Header().Set("ETag", `"t1"`)
			w.Header().Set("Last-Modified", "Thu, 24 Sep 2026 11:00:00 GMT")
			w.Header().Set("Cache-Control", "private, max-age=30")
			if r.Header.Get("If-None-Match") == `"t1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(tilePNG)
		case strings.HasSuffix(r.URL.Path, "/map/tiles/minecraft_overworld/3/9_9.png"):
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "private, max-age=30")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"This part of the map is not drawn yet.","code":"not_found"}`)
		case strings.HasSuffix(r.URL.Path, "/map/worlds"):
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			io.WriteString(w, `{"worlds":[{"name":"minecraft_overworld"}],"tileSize":512}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
		}
	})
	cookie, csrf := e.setup(t)
	signedIn := auth(cookie, "")
	tile := "/api/servers/" + sampleServer + "/map/tiles/minecraft_overworld/3/0_-1.png"

	r, body := e.raw(t, "GET", tile, nil, signedIn)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, tilePNG) ||
		r.Header.Get("ETag") != `"t1"` || r.Header.Get("Cache-Control") != "private, max-age=30" {
		t.Fatalf("tile: %d %v %q", r.StatusCode, r.Header, body)
	}
	cond := map[string]string{"Cookie": signedIn["Cookie"], "If-None-Match": `"t1"`}
	if r, body := e.raw(t, "GET", tile, nil, cond); r.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("revalidated tile: %d %q", r.StatusCode, body)
	}
	hits := agent.seen()
	if len(hits) != 2 || hits[0].URL.Path != "/v1/servers/"+sampleServer+"/map/tiles/minecraft_overworld/3/0_-1.png" || hits[1].Header.Get("If-None-Match") != `"t1"` {
		t.Fatalf("agent saw %v", hits)
	}
	if hits[0].Header.Get("Cookie") != "" {
		t.Fatal("the session cookie reached the agent")
	}
	r, body = e.raw(t, "GET", "/api/servers/"+sampleServer+"/map/tiles/minecraft_overworld/3/9_9.png", nil, signedIn)
	if r.StatusCode != 404 || r.Header.Get("Content-Type") != "application/json" || !bytes.Contains(body, []byte("not drawn yet")) {
		t.Fatalf("undrawn tile: %d %v %s", r.StatusCode, r.Header, body)
	}
	r, body = e.raw(t, "GET", "/api/servers/"+sampleServer+"/map/worlds", nil, signedIn)
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" || !bytes.Contains(body, []byte("minecraft_overworld")) {
		t.Fatalf("worlds: %d %v %s", r.StatusCode, r.Header, body)
	}

	agent.forget()
	if res := e.do(t, "POST", "/api/servers/"+sampleServer+"/map/share", `{"public":true}`, auth(cookie, csrf)); res.status != 200 {
		t.Fatalf("share: %d %v", res.status, res.body)
	}
	if hits := agent.seen(); len(hits) != 1 || hits[0].URL.Path != "/v1/servers/"+sampleServer+"/map/share" {
		t.Fatalf("agent saw %v", hits)
	}
	if r, _ := e.raw(t, "GET", tile, nil, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a tile without signing in: %d", r.StatusCode)
	}
}

// Link tokens for the shared map tests: the map's token, the one it had
// before sharing was switched off and on, and one nobody has.
const (
	mapToken   = "Ab3dEf6hIj9lMn2pQr5tUv"
	oldToken   = "Zy9xWv8uTs7rQp6oNm5lKj"
	otherToken = "Qq1wEe2rTt3yUu4iOo5pAa"
)

// sharedMapAgent plays the agent's shared map for one server, "Survival",
// under mapToken. Like the agent, it lists nobody while players are hidden.
type sharedMapAgent struct {
	mu      sync.Mutex
	shared  bool
	players bool
	down    bool
}

func (m *sharedMapAgent) set(shared, players bool) {
	m.mu.Lock()
	m.shared, m.players = shared, players
	m.mu.Unlock()
}

func (m *sharedMapAgent) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	shared, players, down := m.shared, m.players, m.down
	m.mu.Unlock()
	if down {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close()
		}
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v1/public-maps/"+mapToken)
	if !ok || !shared {
		agentUnavailable(w)
		return
	}
	switch {
	case rest == "":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"name":"Survival","players":`+map[bool]string{true: "true", false: "false"}[players]+`}`)
	case rest == "/worlds":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"worlds":[{"name":"minecraft_overworld"}],"tileSize":512}`)
	case rest == "/players" && players:
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"players":[{"name":"Alex","uuid":"4566e69f-c907-48ee-8d71-d7ba5aa00d20","world":"minecraft_overworld","x":1,"z":2}],"updatedAt":"2026-09-24T12:00:00Z"}`)
	case rest == "/players":
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"players":[],"updatedAt":"2026-09-24T12:00:00Z"}`)
	case rest == "/icon":
		w.Header().Set("Content-Type", "image/png")
		w.Write(tilePNG)
	case strings.HasPrefix(rest, "/tiles/"):
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"t1"`)
		w.Header().Set("Last-Modified", "Thu, 24 Sep 2026 11:00:00 GMT")
		w.Header().Set("Cache-Control", "private, max-age=30")
		w.Write(tilePNG)
	default:
		agentUnavailable(w)
	}
}

func TestSharedMapAnswersTheSameWhenItIsNotAvailable(t *testing.T) {
	sm := &sharedMapAgent{}
	e, agent := newScriptedEnv(t, sm.serve)
	face := []byte("\x89PNG\r\n\x1a\nAlex's face")
	if _, err := e.srv.db.Exec(`INSERT INTO player_heads(name, status, png, fetched_at) VALUES('alex', 'ok', ?, ?)`, face, e.clock.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		r, body := e.raw(t, "GET", path, nil, nil)
		if r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("ETag") != "" || r.Header.Get("Last-Modified") != "" || len(r.Cookies()) != 0 {
			t.Fatalf("%s: headers %v", path, r.Header)
		}
		return r, body
	}
	page, pub := "/map/"+mapToken, "/api/public/map/"+mapToken
	_, pageOff := get(page)
	_, apiOff := get(pub)
	unavailable := func(what string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			r, body := get(p)
			want := apiOff
			if strings.HasPrefix(p, "/map/") {
				want = pageOff
			}
			if r.StatusCode != 404 || !bytes.Equal(body, want) {
				t.Fatalf("%s, %s: %d %q", what, p, r.StatusCode, body)
			}
		}
	}
	if bytes.Contains(apiOff, []byte("Survival")) || !bytes.Contains(apiOff, []byte("This map isn't available.")) || string(pageOff) != indexPage {
		t.Fatalf("unavailable answers: %s / %s", apiOff, pageOff)
	}

	unavailable("not shared", page, pub+"/worlds", pub+"/tiles/minecraft_overworld/3/0_0.png", pub+"/icon", pub+"/players")
	unavailable("unknown link", "/map/"+otherToken, "/api/public/map/"+otherToken, "/api/public/map/"+otherToken+"/worlds")

	sm.set(true, false)
	r, body := get(page)
	if r.StatusCode != 200 || string(body) != indexPage || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("shared page: %d %v %s", r.StatusCode, r.Header, body)
	}
	if r, body := get(pub); r.StatusCode != 200 || string(body) != `{"name":"Survival","players":false}` {
		t.Fatalf("shared details: %d %s", r.StatusCode, body)
	}
	if r, body := get(pub + "/tiles/minecraft_overworld/3/-1_2.png"); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, tilePNG) {
		t.Fatalf("shared tile: %d %v", r.StatusCode, r.Header)
	}
	if r, _ := get(pub + "/icon"); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("shared icon: %d %v", r.StatusCode, r.Header)
	}
	if r, body := get(pub + "/players"); r.StatusCode != 200 || !bytes.Contains(body, []byte(`"players":[]`)) {
		t.Fatalf("players while they are hidden: %d %s", r.StatusCode, body)
	}
	unavailable("faces while players are hidden", pub+"/faces/Alex", pub+"/faces/alex")
	unavailable("old link", "/map/"+oldToken, "/api/public/map/"+oldToken, "/api/public/map/"+oldToken+"/players")

	agent.forget()
	unavailable("the server's slug", "/map/survival", "/api/public/map/survival", "/api/public/map/survival/worlds", "/api/public/map/survival/players")
	unavailable("invalid paths", "/map/Bad_Slug", "/map/"+mapToken[1:], "/map/"+mapToken+"x", "/api/public/map/"+mapToken[1:], "/api/public/map/"+mapToken+"x/worlds",
		"/api/public/map/"+mapToken+"%3Fx/worlds", "/api/public/map/"+mapToken[:21]+"-/worlds",
		pub+"/tiles/..%2Fplugins/3/0_0.png", pub+"/tiles/minecraft_overworld/99x/0_0.png",
		pub+"/tiles/minecraft_overworld/3/0_0.jpg", pub+"/faces/no%20name", "/api/public/map/survival/faces/Alex")
	if hits := agent.seen(); len(hits) != 0 {
		t.Fatalf("invalid paths reached the agent: %v", hits)
	}

	sm.set(true, true)
	if r, body := get(pub + "/players"); r.StatusCode != 200 || !bytes.Contains(body, []byte(`"Alex"`)) {
		t.Fatalf("shared players: %d %s", r.StatusCode, body)
	}
	if r, body := get(pub + "/faces/alex"); r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || !bytes.Equal(body, face) {
		t.Fatalf("a listed player's face: %d %v", r.StatusCode, r.Header)
	}
	unavailable("someone not on the map", pub+"/faces/Steve")

	sm.set(false, true)
	unavailable("turned off", page, pub, pub+"/players", pub+"/faces/Alex")

	sm.set(true, true)
	sm.mu.Lock()
	sm.down = true
	sm.mu.Unlock()
	unavailable("agent down", page, pub, pub+"/tiles/minecraft_overworld/3/0_0.png")
}

// lockedBuffer is a log destination the panel's handlers may write to
// while a test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A link token in the request log would let whoever reads it open the map.
func TestSharedMapTokensStayOutOfTheLog(t *testing.T) {
	sm := &sharedMapAgent{}
	sm.set(true, false)
	logs := &lockedBuffer{}
	e, _ := newScriptedEnvLogging(t, sm.serve, logs)
	for _, p := range []string{
		"/map/" + mapToken, "/map/" + oldToken, "/MAP/" + mapToken, "/map/" + mapToken[:21],
		"/api/public/map/" + mapToken, "/api/public/map/" + mapToken + "/tiles/minecraft_overworld/3/0_0.png",
		"/api/public/map/" + oldToken + "/players", "/api/public/map/" + mapToken + "x/worlds", "/api/public//map/" + mapToken + "/worlds",
	} {
		e.raw(t, "GET", p, nil, nil)
	}
	out := logs.String()
	if !strings.Contains(out, "path=/api/public/map/[token]/tiles/minecraft_overworld/3/0_0.png") || !strings.Contains(out, "path=/map/[token]") {
		t.Fatalf("request log:\n%s", out)
	}
	for _, tok := range []string{mapToken[:21], oldToken[:21], mapToken[1:]} {
		if strings.Contains(out, tok) {
			t.Fatalf("the request log holds a link token:\n%s", out)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{"/map/" + mapToken, "/map/[token]"},
		{"/api/public/map/" + mapToken + "/faces/Alex", "/api/public/map/[token]/faces/Alex"},
		{"/api/servers/abcdefghjk/map/worlds", "/api/servers/abcdefghjk/map/worlds"},
		{"/mapped", "/mapped"},
		{"/map/", "/map/"},
	} {
		if got := redactMapToken(tc.in); got != tc.want {
			t.Errorf("redactMapToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSharedMapIsRateLimitedPerAddress(t *testing.T) {
	e, _ := newScriptedEnv(t, func(w http.ResponseWriter, r *http.Request) { agentUnavailable(w) })
	for i := 0; i < publicMapRequests; i++ {
		if r, _ := e.raw(t, "GET", "/api/public/map/"+mapToken, nil, nil); r.StatusCode != 404 {
			t.Fatalf("request %d: %d", i, r.StatusCode)
		}
	}
	for _, p := range []string{"/api/public/map/" + mapToken + "/worlds", "/map/" + mapToken, "/map/survival"} {
		r, _ := e.raw(t, "GET", p, nil, nil)
		if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s over the limit: %d %v", p, r.StatusCode, r.Header)
		}
	}
	e.clock.add(time.Minute)
	if r, _ := e.raw(t, "GET", "/map/"+mapToken, nil, nil); r.StatusCode != 404 {
		t.Fatalf("a minute later: %d", r.StatusCode)
	}
}

// The public route group is the only place without a sign-in: health,
// setup and sign-in aside, a route either needs a session or belongs to
// the shared map, and those only read.
func TestOnlyTheSharedMapIsPublic(t *testing.T) {
	e := newEnv(t)
	signIn := map[string]bool{"GET /api/health": true, "GET /api/setup/status": true, "POST /api/setup": true, "POST /api/auth/login": true}
	group := map[string]bool{}
	for _, rt := range e.srv.publicPages() {
		group[rt.Method+" "+rt.Pattern] = true
		if rt.Level != public || rt.Method != "GET" {
			t.Errorf("%s %s: level %d", rt.Method, rt.Pattern, rt.Level)
		}
	}
	for _, rt := range e.srv.Routes() {
		key := rt.Method + " " + rt.Pattern
		switch {
		case signIn[key] || group[key]:
		case rt.NeedsSession():
			if strings.HasPrefix(rt.Pattern, "/api/public/") || strings.HasPrefix(rt.Pattern, "/map/") {
				t.Errorf("%s is under a public path but needs a session", key)
			}
		default:
			t.Errorf("%s is public outside the public route group", key)
		}
	}
}
