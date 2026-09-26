package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/config"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeAgent records forwarded requests; the panel tests only check that the
// panel guards routes, not agent behaviour.
type fakeAgent struct {
	mu   sync.Mutex
	hits []string
	// bodies are the request bodies and queries, in the order of hits.
	bodies []string
	// reqs are the forwarded requests with their query and JSON body.
	reqs []agentRequest
	// replies are canned bodies by "METHOD /path"; others get {"ok":true}.
	replies map[string]string
	// statuses are the replies' HTTP statuses by "METHOD /path"; others
	// are 200.
	statuses map[string]int
	// headers are the last request headers by "METHOD /path".
	headers map[string]http.Header
	// before run, by "METHOD /path", ahead of the reply: a test's way to act
	// while the panel waits for the agent.
	before map[string]func()
	// lastBody is the last request body by "METHOD /path".
	lastBody map[string]string
	// gates hold requests to "METHOD /path" until closed, or until the
	// request is cancelled.
	gates map[string]chan struct{}
}

type agentRequest struct {
	method, path string
	query        url.Values
	body         map[string]any
}

func startFakeAgent(t *testing.T, dir string) (string, *fakeAgent) {
	t.Helper()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAgent{replies: map[string]string{}, statuses: map[string]int{}, headers: map[string]http.Header{}, before: map[string]func(){}, lastBody: map[string]string{}, gates: map[string]chan struct{}{}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var body map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
		key := r.Method + " " + r.URL.Path
		fa.mu.Lock()
		fa.hits = append(fa.hits, key)
		fa.bodies = append(fa.bodies, r.URL.RawQuery+string(raw))
		fa.reqs = append(fa.reqs, agentRequest{r.Method, r.URL.Path, r.URL.Query(), body})
		fa.headers[key] = r.Header.Clone()
		fa.lastBody[key] = string(raw)
		reply, ok := fa.replies[key]
		status := fa.statuses[key]
		hook := fa.before[key]
		gate := fa.gates[key]
		fa.mu.Unlock()
		if hook != nil {
			hook()
		}
		if gate != nil {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			reply = `{"ok":true}`
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		io.WriteString(w, reply)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock, fa
}

type env struct {
	srv   *Server
	ts    *httptest.Server
	clock *clock
	agent *fakeAgent
	cfg   config.Config
	names *fakeResolver
	logs  *syncBuffer
}

// fakeResolver answers name lookups from a map; unknown names don't exist.
type fakeResolver struct {
	mu    sync.Mutex
	addrs map[string][]netip.Addr
}

func (f *fakeResolver) set(host string, addrs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range addrs {
		f.addrs[host] = append(f.addrs[host], netip.MustParseAddr(a))
	}
}

func (f *fakeResolver) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.addrs[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

// newEnvWith lets a test change the panel's options, for example to point
// faces and name lookups at local fakes.
func newEnvWith(t *testing.T, tweak func(*Options)) *env {
	t.Helper()
	return newEnvConfig(t, nil, tweak)
}

// newEnvConfig is newEnvWith with the install's configuration changed by
// mod first.
func newEnvConfig(t *testing.T, mod func(*config.Config), tweak func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	sock, fa := startFakeAgent(t, dir)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = sock
	if mod != nil {
		mod(&cfg)
	}
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	names := &fakeResolver{addrs: map[string][]netip.Addr{}}
	logs := &syncBuffer{}
	opts := Options{Config: cfg, Now: clk.now, Logger: slog.New(slog.NewTextHandler(logs, nil)), Agent: agentclient.New(sock), IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour,
		LinkRoutes: agent.LinkRoutes(), LookupIP: names.lookup}
	if tweak != nil {
		tweak(&opts)
		cfg = opts.Config
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{srv: s, ts: ts, clock: clk, agent: fa, cfg: cfg, names: names, logs: logs}
}

type resp struct {
	status int
	body   map[string]any
	cookie string
	// pending is the second sign-in step's cookie, and cleared whether it
	// was deleted.
	pending        string
	pendingCleared bool
	header         http.Header
}

func (e *env) do(t *testing.T, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.ts.URL)
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	out := resp{status: r.StatusCode, body: map[string]any{}, header: r.Header}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &out.body)
	for _, c := range r.Cookies() {
		switch c.Name {
		case cookieName:
			out.cookie = c.Value
		case pendingCookieName:
			out.pending, out.pendingCleared = c.Value, c.MaxAge < 0
		default:
			continue
		}
		if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
			t.Fatalf("cookie %s is missing Secure/HttpOnly/SameSite=Strict/Path=/: %+v", c.Name, c)
		}
	}
	return out
}

// setup creates the admin with a fresh setup code and returns cookie + CSRF.
func (e *env) setup(t *testing.T) (cookie, csrf string) {
	t.Helper()
	code, err := NewSetupToken(e.cfg.SetupTokenPath(), 24*time.Hour, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"admin","password":"correct horse battery"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != 200 || r.cookie == "" {
		t.Fatalf("setup failed: %d %v", r.status, r.body)
	}
	return r.cookie, r.body["csrfToken"].(string)
}

func auth(cookie, csrf string) map[string]string {
	h := map[string]string{"Cookie": cookieName + "=" + cookie}
	if csrf != "" {
		h["X-CSRF-Token"] = csrf
	}
	return h
}

const sampleServer = "abcdefghjk"

// sampleCode has the shape of an invite code but opens nothing.
const sampleCode = "AbCdEfGhJkMnPqRsTuVwXy"

func samplePath(p string) string {
	return strings.NewReplacer("{id}", sampleServer, "{mid}", "mnpqrstuvw", "{bid}", "20260924-120000-abcdef", "{rid}", "0123456789abcdef",
		"{op}", "0123456789abcdef", "{name}", "PkBotFriend", "{sid}", "qrstuvwxyz", "{cid}", "cdefghjkmn", "{tid}", "tokenidabc", "{source}", "modrinth", "{project}", "AANobbMI", "{version}", "TPV00001",
		"{invite}", "qrstuvwxyz", "{request}", "zyxwvutsrq", "{uid}", "2", "{code}", sampleCode).Replace(p)
}

func TestEveryRouteRequiresSessionAndCSRF(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	for _, rt := range e.srv.Routes() {
		path := samplePath(rt.Pattern)
		if rt.NeedsSession() {
			if r := e.do(t, rt.Method, path, `{}`, nil); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s without a session: got %d, want 401", rt.Method, rt.Pattern, r.status)
			}
			if r := e.do(t, rt.Method, path, `{}`, auth("forged-cookie-value", csrf)); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s with a forged cookie: got %d, want 401", rt.Method, rt.Pattern, r.status)
			}
		}
		if rt.Level == needSessionCSRF {
			if r := e.do(t, rt.Method, path, `{}`, auth(cookie, "")); r.status != http.StatusForbidden {
				t.Errorf("%s %s without CSRF token: got %d, want 403", rt.Method, rt.Pattern, r.status)
			}
			if r := e.do(t, rt.Method, path, `{}`, auth(cookie, "wrong-token")); r.status != http.StatusForbidden {
				t.Errorf("%s %s with wrong CSRF token: got %d, want 403", rt.Method, rt.Pattern, r.status)
			}
			h := auth(cookie, csrf)
			h["Origin"] = "https://evil.example"
			if r := e.do(t, rt.Method, path, `{}`, h); r.status != http.StatusForbidden {
				t.Errorf("%s %s from another origin: got %d, want 403", rt.Method, rt.Pattern, r.status)
			}
		}
		if rt.Level == pendingSession {
			xrw := map[string]string{"X-Requested-With": "playkeeper"}
			if r := e.do(t, rt.Method, path, `{}`, xrw); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s without a second-step cookie: got %d, want 401", rt.Method, rt.Pattern, r.status)
			}
			full := auth(cookie, csrf)
			full["X-Requested-With"] = "playkeeper"
			full["Cookie"] += "; " + pendingCookieName + "=" + cookie
			if r := e.do(t, rt.Method, path, `{}`, full); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s with a full session's token as the second-step cookie: got %d, want 401", rt.Method, rt.Pattern, r.status)
			}
			if r := e.do(t, rt.Method, path, `{}`, nil); r.status != http.StatusForbidden {
				t.Errorf("%s %s without X-Requested-With: got %d, want 403", rt.Method, rt.Pattern, r.status)
			}
			if r := e.do(t, rt.Method, path, `{}`, map[string]string{"X-Requested-With": "playkeeper", "Origin": "https://evil.example"}); r.status != http.StatusForbidden {
				t.Errorf("%s %s from another origin: got %d, want 403", rt.Method, rt.Pattern, r.status)
			}
		}
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	if len(e.agent.hits) != 0 {
		t.Fatalf("rejected requests reached the agent: %v", e.agent.hits)
	}
}

func TestValidRequestReachesAgentWithActor(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/start", `{}`, auth(cookie, csrf)); r.status != 200 {
		t.Fatalf("start: %d %v", r.status, r.body)
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	if len(e.agent.hits) != 1 || e.agent.hits[0] != "POST /v1/servers/"+sampleServer+"/start" {
		t.Fatalf("agent saw %v", e.agent.hits)
	}
}

func TestPublicMutationsNeedSameOriginMarker(t *testing.T) {
	e := newEnv(t)
	body := `{"username":"admin","password":"x"}`
	if r := e.do(t, "POST", "/api/auth/login", body, nil); r.status != http.StatusForbidden {
		t.Fatalf("login without X-Requested-With: %d", r.status)
	}
	if r := e.do(t, "POST", "/api/auth/login", body, map[string]string{"X-Requested-With": "playkeeper", "Origin": "https://evil.example"}); r.status != http.StatusForbidden {
		t.Fatalf("cross-origin login: %d", r.status)
	}
}

func TestSetupCodeIsSingleUseAndExpires(t *testing.T) {
	e := newEnv(t)
	xrw := map[string]string{"X-Requested-With": "playkeeper"}
	if r := e.do(t, "POST", "/api/setup", `{"token":"guess-guess-guess-guess","username":"eve","password":"eve password 123"}`, xrw); r.status != http.StatusForbidden {
		t.Fatalf("setup without any code: %d", r.status)
	}
	code, _ := NewSetupToken(e.cfg.SetupTokenPath(), time.Hour, e.clock.now())
	if r := e.do(t, "POST", "/api/setup", `{"token":"wrong-code-xxxxx-xxxxx","username":"eve","password":"eve password 123"}`, xrw); r.status != http.StatusForbidden {
		t.Fatalf("setup with wrong code: %d", r.status)
	}
	e.clock.add(2 * time.Hour)
	if r := e.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"eve","password":"eve password 123"}`, xrw); r.status != http.StatusForbidden {
		t.Fatalf("expired setup code accepted: %d", r.status)
	}
	code, _ = NewSetupToken(e.cfg.SetupTokenPath(), time.Hour, e.clock.now())
	if r := e.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"admin","password":"correct horse battery"}`, xrw); r.status != 200 {
		t.Fatalf("valid setup: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"eve","password":"eve password 123"}`, xrw); r.status != http.StatusConflict {
		t.Fatalf("reused setup code / second admin: %d", r.status)
	}
	if _, err := os.Stat(e.cfg.SetupTokenPath()); err == nil {
		t.Fatal("setup code file must be deleted after use")
	}
	if n, _ := e.srv.userCount(); n != 1 {
		t.Fatalf("expected exactly one admin, got %d", n)
	}
}

// The first-admin check and the insert used to be separate steps, with a slow
// password hash between them, so concurrent setups with one code all got in.
// The first admin is made only on an empty install: with any account
// there, even one that isn't the owner, setup makes nobody.
func TestFirstAdminOnlyOnAnEmptyInstall(t *testing.T) {
	e := newEnv(t)
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('mara', 'x', 0, 0, 'member')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.createFirstAdmin("admin", "correct horse battery"); err != errSetupDone {
		t.Fatalf("setup with an account already there: %v", err)
	}
	var owners int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'owner'`).Scan(&owners)
	if owners != 0 {
		t.Fatalf("%d owner accounts made", owners)
	}
}

func TestConcurrentSetupsCreateOneAdmin(t *testing.T) {
	e := newEnv(t)
	code, err := NewSetupToken(e.cfg.SetupTokenPath(), time.Hour, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	xrw := map[string]string{"X-Requested-With": "playkeeper"}
	const n = 4
	statuses := make([]int, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			statuses[i] = e.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"admin`+string(rune('a'+i))+`","password":"correct horse battery"}`, xrw).status
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for i, st := range statuses {
		switch st {
		case http.StatusOK:
			ok++
		case http.StatusConflict, http.StatusForbidden:
		default:
			t.Fatalf("setup %d: unexpected %d", i, st)
		}
	}
	if users, _ := e.srv.userCount(); ok != 1 || users != 1 {
		t.Fatalf("concurrent setups with one code: %d succeeded, %d admin accounts (statuses %v)", ok, users, statuses)
	}
}

func TestNoSignupRoute(t *testing.T) {
	e := newEnv(t)
	for _, rt := range e.srv.Routes() {
		if strings.Contains(rt.Pattern, "signup") || strings.Contains(rt.Pattern, "register") {
			t.Fatalf("unexpected signup route %s", rt.Pattern)
		}
	}
	if r := e.do(t, "POST", "/api/auth/signup", `{}`, nil); r.status != http.StatusNotFound {
		t.Fatalf("signup: %d", r.status)
	}
}

func TestSessionIdleAndAbsoluteExpiry(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != 200 {
		t.Fatalf("fresh session: %d", r.status)
	}
	e.clock.add(61 * time.Minute)
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != http.StatusUnauthorized {
		t.Fatalf("session idle for 61m (limit 60m) still valid: %d", r.status)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != 200 {
		t.Fatalf("login: %d", r.status)
	}
	cookie = r.cookie
	loginAt := e.clock.now()
	// Stay active (a request every 50 minutes) so only the absolute limit applies.
	for {
		e.clock.add(50 * time.Minute)
		elapsed := e.clock.now().Sub(loginAt)
		res := e.do(t, "GET", "/api/auth/me", "", auth(cookie, ""))
		if elapsed < 24*time.Hour {
			if res.status != 200 {
				t.Fatalf("active session expired early after %s: %d", elapsed, res.status)
			}
			continue
		}
		if res.status != http.StatusUnauthorized {
			t.Fatalf("session still valid %s after login (absolute limit 24h): %d", elapsed, res.status)
		}
		return
	}
}

func TestLogoutInvalidatesCookie(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	if r := e.do(t, "POST", "/api/auth/logout", `{}`, auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d", r.status)
	}
	if r := e.do(t, "GET", "/api/auth/me", "", auth(cookie, "")); r.status != http.StatusUnauthorized {
		t.Fatalf("cookie reused after logout: %d", r.status)
	}
}

func TestLoginRateLimitAndLockout(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	xrw := map[string]string{"X-Requested-With": "playkeeper"}
	got429 := false
	for i := 0; i < 12; i++ {
		r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"wrong password!"}`, xrw)
		if r.status == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if r.status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, r.status)
		}
	}
	if !got429 {
		t.Fatal("repeated failed logins were never rate limited")
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw); r.status != http.StatusTooManyRequests {
		t.Fatalf("locked account accepted a login immediately: %d", r.status)
	}
	e.clock.add(20 * time.Minute)
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, xrw); r.status != 200 {
		t.Fatalf("login after lockout expired: %d %v", r.status, r.body)
	}
}

// A different username on every attempt never trips the per-account lockout,
// so only the per-address limit stops these; it also guards wrong setup codes.
func TestSignInAndSetupAreRateLimitedPerAddress(t *testing.T) {
	xrw := map[string]string{"X-Requested-With": "playkeeper"}
	e := newEnv(t)
	e.setup(t)
	limited := false
	for i := 0; i < 15 && !limited; i++ {
		r := e.do(t, "POST", "/api/auth/login", `{"username":"guess`+strings.Repeat("x", i)+`","password":"wrong password!"}`, xrw)
		switch r.status {
		case http.StatusTooManyRequests:
			limited = true
		case http.StatusUnauthorized:
		default:
			t.Fatalf("sign-in attempt %d: %d", i, r.status)
		}
	}
	if !limited {
		t.Fatal("sign-ins with a new username each time were never rate limited for the address")
	}

	f := newEnv(t)
	code, err := NewSetupToken(f.cfg.SetupTokenPath(), time.Hour, f.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	limited = false
	for i := 0; i < 15 && !limited; i++ {
		r := f.do(t, "POST", "/api/setup", `{"token":"wrong-`+strings.Repeat("x", i)+`","username":"admin","password":"correct horse battery"}`, xrw)
		switch r.status {
		case http.StatusTooManyRequests:
			limited = true
		case http.StatusForbidden:
		default:
			t.Fatalf("setup attempt %d: %d", i, r.status)
		}
	}
	if !limited {
		t.Fatal("wrong setup codes from one address were never rate limited")
	}
	if r := f.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"admin","password":"correct horse battery"}`, xrw); r.status != http.StatusTooManyRequests {
		t.Fatalf("the right code must wait out the limit too: %d", r.status)
	}
	f.clock.add(20 * time.Minute)
	if r := f.do(t, "POST", "/api/setup", `{"token":"`+code+`","username":"admin","password":"correct horse battery"}`, xrw); r.status != 200 {
		t.Fatalf("setup after the limit expired: %d %v", r.status, r.body)
	}
}

func TestControlActionsAreRateLimited(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	limited := false
	for i := 0; i < 40; i++ {
		if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/restart", `{}`, auth(cookie, csrf)); r.status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("40 control actions in a burst were never rate limited")
	}
}

func TestAgentDownIsReportedNotHidden(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	os.Remove(e.cfg.SocketPath)
	e.srv.agent = agentclient.New(e.cfg.SocketPath)
	r := e.do(t, "GET", "/api/server", "", auth(cookie, ""))
	if r.status != http.StatusServiceUnavailable || r.body["code"] != "agent_unavailable" {
		t.Fatalf("agent down: %d %v", r.status, r.body)
	}
}

func TestSecurityHeadersAndNoSecretsInAudit(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	req, _ := http.NewRequest("GET", e.ts.URL+"/", nil)
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if r.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	e.do(t, "POST", "/api/auth/password", `{"currentPassword":"correct horse battery","newPassword":"another long password"}`, auth(cookie, csrf))
	rows, _ := e.srv.db.Query(`SELECT actor || action || target || detail FROM audit UNION ALL SELECT csrf || id_hash FROM sessions`)
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		for _, secret := range []string{"correct horse battery", "another long password", cookie} {
			if strings.Contains(s, secret) {
				t.Fatalf("database contains a secret: %q", s)
			}
		}
	}
	var hash string
	e.srv.db.QueryRow(`SELECT password_hash FROM users`).Scan(&hash)
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("password not stored as argon2id: %q", hash)
	}
}

func TestSelfSignedCertIsStable(t *testing.T) {
	dir := t.TempDir()
	fp1, err := EnsureSelfSignedCert(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fp2, _ := EnsureSelfSignedCert(dir, time.Now())
	if fp1 != fp2 || len(fp1) != 95 {
		t.Fatalf("fingerprints %q %q", fp1, fp2)
	}
	st, _ := os.Stat(filepath.Join(dir, "key.pem"))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %v", st.Mode().Perm())
	}
	_ = context.Background()
}

func TestTheAuditLogKeepsAYearAndBoundedRows(t *testing.T) {
	e := newEnv(t)
	s := e.srv
	actions := func() string {
		rows, err := s.db.Query(`SELECT action FROM audit ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a string
			rows.Scan(&a)
			out = append(out, a)
		}
		return strings.Join(out, " ")
	}
	s.maxAudit = 4
	s.audit("admin", "old", "", "succeeded", "")
	e.clock.add(366 * 24 * time.Hour)
	for i := range 6 {
		s.audit("admin", fmt.Sprint("a", i), "", "succeeded", "")
	}
	s.pruneAudit()
	if got := actions(); got != "a2 a3 a4 a5" {
		t.Fatalf("kept %q", got)
	}

	// Writing prunes every pruneAuditEvery rows.
	for range pruneAuditEvery - int(s.audits.Load()%pruneAuditEvery) {
		s.audit("admin", "flood", "", "refused", "")
	}
	if got := actions(); got != "flood flood flood flood" {
		t.Fatalf("after a flood: %q", got)
	}

	// Opening the database prunes too.
	s.maxAudit = 100_000
	s.audit("admin", "b", "", "succeeded", "")
	e.clock.add(366 * 24 * time.Hour)
	s.Close()
	again, err := New(Options{Config: e.cfg, Now: e.clock.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Agent: agentclient.New(e.cfg.SocketPath)})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	var n int
	again.db.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&n)
	if n != 0 {
		t.Fatalf("a year later %d rows are left", n)
	}
}
