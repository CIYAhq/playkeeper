package panel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	// replies are canned bodies by "METHOD /path"; others get {"ok":true}.
	replies map[string]string
}

func startFakeAgent(t *testing.T, dir string) (string, *fakeAgent) {
	t.Helper()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAgent{replies: map[string]string{}}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fa.mu.Lock()
		fa.hits = append(fa.hits, r.Method+" "+r.URL.Path)
		reply, ok := fa.replies[r.Method+" "+r.URL.Path]
		fa.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			reply = `{"ok":true}`
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
}

func newEnv(t *testing.T) *env {
	t.Helper()
	return newEnvWith(t, nil)
}

func newEnvWith(t *testing.T, heads *HeadSources) *env {
	t.Helper()
	dir := t.TempDir()
	sock, fa := startFakeAgent(t, dir)
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = sock
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	s, err := New(Options{Config: cfg, Now: clk.now, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Agent: agentclient.New(sock), IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour, Heads: heads})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{srv: s, ts: ts, clock: clk, agent: fa, cfg: cfg}
}

type resp struct {
	status int
	body   map[string]any
	cookie string
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
	out := resp{status: r.StatusCode, body: map[string]any{}}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &out.body)
	for _, c := range r.Cookies() {
		if c.Name == cookieName {
			out.cookie = c.Value
			if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Fatalf("session cookie is missing Secure/HttpOnly/SameSite=Strict: %+v", c)
			}
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

func samplePath(p string) string {
	return strings.NewReplacer("{id}", sampleServer, "{mid}", "mnpqrstuvw", "{bid}", "20260924-120000-abcdef", "{rid}", "0123456789abcdef",
		"{op}", "0123456789abcdef", "{name}", "PkBotFriend", "{source}", "modrinth", "{project}", "AANobbMI").Replace(p)
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
