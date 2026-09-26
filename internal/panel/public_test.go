package panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
)

const (
	sumA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sumB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sumC = "cccccccccccccccccccccccccccccccccccccccc"
)

// syncBuffer is a log destination tests read while the panel writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newEnvAgent is newEnv with the agent's answers coming from h and the
// panel's log written to logw.
func newEnvAgent(t *testing.T, h http.HandlerFunc, logw io.Writer) *env {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	agent := &http.Server{Handler: h}
	go agent.Serve(ln)
	t.Cleanup(func() { agent.Close() })
	cfg := config.Default()
	cfg.DataDir = filepath.Join(dir, "data")
	cfg.SocketPath = sock
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	s, err := New(Options{Config: cfg, Now: clk.now, Logger: slog.New(slog.NewTextHandler(logw, nil)), Agent: agentclient.New(sock), IdleTimeout: time.Hour, AbsoluteTimeout: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ts := httptest.NewTLSServer(s.Handler())
	t.Cleanup(ts.Close)
	return &env{srv: s, ts: ts, clock: clk, cfg: cfg}
}

// offeredPacks is an agent that answers which resource packs are offered.
type offeredPacks struct {
	mu    sync.Mutex
	sums  []string
	down  bool
	calls int
}

func (o *offeredPacks) set(sums ...string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sums = sums
}

func (o *offeredPacks) setDown(down bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.down = down
}

func (o *offeredPacks) handler(w http.ResponseWriter, r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1/resource-packs/active" {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not here","code":"not_found"}`)
		return
	}
	o.calls++
	if o.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"busy","code":"agent_unavailable"}`)
		return
	}
	io.WriteString(w, `{"sha1":["`+strings.Join(o.sums, `","`)+`"]}`)
}

func storePack(t *testing.T, cfg config.Config, sum, body string) {
	t.Helper()
	if err := os.MkdirAll(cfg.ResourcePacksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.ResourcePacksDir(), sum+".zip"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// get sends an anonymous request, as a player's game does.
func get(t *testing.T, c *http.Client, method, url string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

func TestResourcePacksAreServedWithoutSignIn(t *testing.T) {
	agent := &offeredPacks{}
	agent.set(sumA)
	logs := &syncBuffer{}
	e := newEnvAgent(t, agent.handler, logs)
	storePack(t, e.cfg, sumA, "pack A")
	storePack(t, e.cfg, sumB, "pack B")
	c := e.ts.Client()

	r, body := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumA+".zip", nil)
	if r.StatusCode != 200 || body != "pack A" {
		t.Fatalf("an offered pack: %d %q", r.StatusCode, body)
	}
	if r.Header.Get("Content-Type") != "application/zip" || r.Header.Get("Cache-Control") != packCache {
		t.Errorf("an offered pack's headers: %v", r.Header)
	}
	if r, _ := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumA+".zip", map[string]string{"If-None-Match": `"` + sumA + `"`}); r.StatusCode != http.StatusNotModified || r.Header.Get("Cache-Control") != packCache {
		t.Errorf("a cache asking again: %d %v", r.StatusCode, r.Header)
	}
	if r, body := get(t, c, "HEAD", e.ts.URL+"/resource-packs/"+sumA+".zip", nil); r.StatusCode != 200 || body != "" || r.ContentLength != 6 {
		t.Errorf("HEAD: %d %q length %d", r.StatusCode, body, r.ContentLength)
	}

	var notFound string
	for _, tc := range []struct{ what, method, path string }{
		{"a stored pack no server offers", "GET", "/resource-packs/" + sumB + ".zip"},
		{"an unknown pack", "GET", "/resource-packs/" + sumC + ".zip"},
		{"an upper-case hash", "GET", "/resource-packs/" + strings.ToUpper(sumA) + ".zip"},
		{"a pack without .zip", "GET", "/resource-packs/" + sumA},
		{"the folder", "GET", "/resource-packs/"},
		{"a POST", "POST", "/resource-packs/" + sumA + ".zip"},
	} {
		r, body := get(t, c, tc.method, e.ts.URL+tc.path, nil)
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d %q", tc.what, r.StatusCode, body)
		}
		if notFound == "" {
			notFound = body
		} else if body != notFound {
			t.Errorf("%s answered %q, not the same 404 as the others (%q)", tc.what, body, notFound)
		}
		if r.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", tc.what, r.Header.Get("Cache-Control"))
		}
	}
	if l := logs.String(); !strings.Contains(l, "/resource-packs/…") || strings.Contains(l, sumB) || strings.Contains(l, sumC) {
		t.Errorf("public paths were logged in full:\n%s", l)
	}

	agent.set(sumA, sumB)
	if r, _ := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumB+".zip", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("the agent was asked again within a second: %d", r.StatusCode)
	}
	e.clock.add(activeRetry)
	if r, body := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumB+".zip", nil); r.StatusCode != 200 || body != "pack B" {
		t.Errorf("a pack a server just started offering: %d %q", r.StatusCode, body)
	}

	agent.set(sumB)
	e.clock.add(activeFresh)
	if r, _ := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumA+".zip", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("a pack no server offers any more: %d", r.StatusCode)
	}

	agent.setDown(true)
	e.clock.add(activeFresh)
	if r, body := get(t, c, "GET", e.ts.URL+"/resource-packs/"+sumB+".zip", nil); r.StatusCode != 200 || body != "pack B" {
		t.Errorf("while the agent can't answer, its last list: %d %q", r.StatusCode, body)
	}
}

func TestActivePacksAsksTheAgentSparingly(t *testing.T) {
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	list, calls, fail := []string{sumA}, 0, false
	ap := &activePacks{now: clk.now, fetch: func(context.Context) ([]string, error) {
		calls++
		if fail {
			return nil, errors.New("agent down")
		}
		return slices.Clone(list), nil
	}}
	check := func(sum string, want bool, wantCalls int, what string) {
		t.Helper()
		if got := ap.has(sum); got != want || calls != wantCalls {
			t.Errorf("%s: has = %v after %d calls, want %v after %d", what, got, calls, want, wantCalls)
		}
	}
	check(sumA, true, 1, "first request")
	check(sumB, false, 1, "a miss within a second of asking")
	list = []string{sumA, sumB}
	clk.add(activeRetry)
	check(sumB, true, 2, "a miss a second later")
	check(sumA, true, 2, "a hit on a fresh list")
	fail = true
	clk.add(activeFresh)
	check(sumA, true, 3, "a stale list the agent can't renew")
	check(sumC, false, 3, "a miss right after a failed renewal")
	fail, list = false, []string{sumB}
	clk.add(activeRetry)
	check(sumA, false, 4, "a pack dropped from a stale list")
}

func TestListedPacksDontWaitForTheAgent(t *testing.T) {
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	var mu sync.Mutex
	list, answer := []string{sumA}, make(chan struct{})
	close(answer)
	ap := &activePacks{now: clk.now, fetch: func(ctx context.Context) ([]string, error) {
		mu.Lock()
		l, a := slices.Clone(list), answer
		mu.Unlock()
		select {
		case <-a:
			return l, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	if !ap.has(sumA) {
		t.Fatal("the first request was refused")
	}

	// A server starts offering pack B, and the agent is slow to say so.
	hold := make(chan struct{})
	mu.Lock()
	list, answer = []string{sumA, sumB}, hold
	mu.Unlock()
	clk.add(activeRetry)
	newPack := make(chan bool)
	go func() { newPack <- ap.has(sumB) }()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		ap.mu.Lock()
		asking := ap.renewing != nil
		ap.mu.Unlock()
		if asking {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the agent was never asked about pack B")
		}
	}
	listed := make(chan bool)
	go func() { listed <- ap.has(sumA) }()
	select {
	case ok := <-listed:
		if !ok {
			t.Fatal("pack A was refused while the agent was asked about pack B")
		}
	case <-time.After(time.Second):
		t.Fatal("pack A waited for the agent's answer about pack B")
	}
	close(hold)
	if !<-newPack {
		t.Fatal("pack B was refused once the agent answered")
	}
}

func TestAddressKeyIgnoresHeadersAndGroupsIPv6(t *testing.T) {
	for remote, want := range map[string]string{
		"192.0.2.10:52000":               "192.0.2.10",
		"[::ffff:192.0.2.10]:52000":      "192.0.2.10",
		"[2001:db8:1:2:3:4:5:6]:443":     "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::1]:443":     "2001:db8:1:2::/64",
		"[fe80::1%eth0]:443":             "fe80::/64",
		"not an address":                 "not an address",
		"[2001:db8:1:3::1]:443":          "2001:db8:1:3::/64",
		"198.51.100.7:1":                 "198.51.100.7",
		"[2001:db8:abcd:12:ab::cd]:8443": "2001:db8:abcd:12::/64",
	} {
		if got := addressKey(remote); got != want {
			t.Errorf("addressKey(%q) = %q, want %q", remote, got, want)
		}
	}
}

// guarded is a public route for testing the guard, whose handler waits
// for hold to close when a request asks it to.
func guarded(limits publicLimits, hold chan struct{}) (*publicGroup, *clock) {
	clk := &clock{t: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hold") != "" {
			<-hold
		}
		io.WriteString(w, "ok")
	})
	return newPublicGroup([]publicRoute{{prefix: "/t/", limits: limits, handler: h}}, clk.now), clk
}

func hit(g *publicGroup, remote, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", target, nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.handler("/t/").ServeHTTP(rec, req)
	return rec
}

func TestPublicRoutesAreLimitedPerAddress(t *testing.T) {
	g, clk := guarded(publicLimits{perMinute: 3, open: 8, read: time.Second, write: time.Second}, nil)
	for i, remote := range []string{"[2001:db8::1]:1000", "[2001:db8::2]:1001", "[2001:db8::ffff:1]:1002"} {
		if rec := hit(g, remote, "/t/x", map[string]string{"X-Forwarded-For": "198.51.100." + string(rune('1'+i))}); rec.Code != 200 {
			t.Fatalf("request %d from the /64: %d", i+1, rec.Code)
		}
	}
	rec := hit(g, "[2001:db8::3]:1003", "/t/x", map[string]string{"X-Forwarded-For": "198.51.100.99", "X-Real-IP": "198.51.100.98", "Forwarded": "for=198.51.100.97"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a fourth request from the same /64 with forwarded headers: %d %v", rec.Code, rec.Header())
	}
	if rec := hit(g, "[2001:db8:0:1::1]:1004", "/t/x", nil); rec.Code != 200 {
		t.Errorf("another /64: %d", rec.Code)
	}
	if rec := hit(g, "192.0.2.10:1005", "/t/x", nil); rec.Code != 200 {
		t.Errorf("an IPv4 address: %d", rec.Code)
	}
	clk.add(time.Minute)
	if rec := hit(g, "[2001:db8::3]:1006", "/t/x", nil); rec.Code != 200 {
		t.Errorf("a minute later: %d", rec.Code)
	}
}

func TestPublicDownloadsAreCapped(t *testing.T) {
	hold := make(chan struct{})
	g, _ := guarded(publicLimits{perMinute: 100, open: 1, download: true, read: time.Second, write: time.Second}, hold)
	started := make(chan *httptest.ResponseRecorder)
	go func() { started <- hit(g, "192.0.2.10:1000", "/t/x?hold=1", nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(g.downloads) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first download never started")
		}
		time.Sleep(time.Millisecond)
	}
	if rec := hit(g, "192.0.2.10:1001", "/t/x", nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a second download from one address past its limit: %d", rec.Code)
	}
	if rec := hit(g, "192.0.2.11:1000", "/t/x", nil); rec.Code != 200 {
		t.Errorf("a download from another address: %d", rec.Code)
	}
	close(hold)
	if rec := <-started; rec.Code != 200 {
		t.Errorf("the held download: %d", rec.Code)
	}

	for range publicDownloads {
		g.downloads <- struct{}{}
	}
	rec := hit(g, "192.0.2.12:1000", "/t/x", nil)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("with every download slot taken: %d %v", rec.Code, rec.Header())
	}
	<-g.downloads
	if rec := hit(g, "192.0.2.12:1001", "/t/x", nil); rec.Code != 200 {
		t.Errorf("with a slot free again: %d", rec.Code)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.open) != 0 {
		t.Errorf("finished requests are still counted: %v", g.open)
	}
}

func TestPublicDownloadsDropClientsThatStopReading(t *testing.T) {
	wrote := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		for {
			if _, err := w.Write(buf); err != nil {
				wrote <- err
				return
			}
		}
	})
	g := newPublicGroup([]publicRoute{{prefix: "/t/", limits: publicLimits{perMinute: 10, open: 2, download: true, read: time.Second, write: time.Minute, stall: 100 * time.Millisecond}, handler: h}}, time.Now)
	s := &Server{log: slog.New(slog.NewTextHandler(io.Discard, nil)), public: g}
	ts := httptest.NewServer(s.logRequests(g.handler("/t/")))
	defer ts.Close()
	c, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	io.WriteString(c, "GET /t/pack HTTP/1.1\r\nHost: example.com\r\n\r\n")
	select {
	case err := <-wrote:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Logf("the stalled write failed with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a client that stopped reading still holds its download after 10 seconds")
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(g.downloads) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled download's slot was never freed")
		}
		time.Sleep(time.Millisecond)
	}
}

// What is unknown, switched off or on a stopped server must look alike to
// someone without a sign-in: the same 404, headers and body, and no sooner
// than an answer the agent was asked for.
func TestPublicRoutesAnswerOneNotFound(t *testing.T) {
	answered := make(chan struct{}, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Route", "set by the route")
		w.Header().Set("Cache-Control", "max-age=86400")
		switch r.URL.Path {
		case "/t/unknown":
			http.NotFound(w, r)
		case "/t/off":
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			io.WriteString(w, `{"error":"The map is switched off."}`)
		case "/t/private":
			http.Error(w, "Forbidden", http.StatusForbidden)
		case "/t/stopped":
			w.Header().Set("Retry-After", "5")
			writeErr(w, http.StatusServiceUnavailable, api.CodeAgentUnavailable, "The server is stopped.", "")
		case "/t/slot":
			answered <- struct{}{}
			http.NotFound(w, r)
		default:
			io.WriteString(w, "ok")
		}
	})
	g := newPublicGroup([]publicRoute{{prefix: "/t/", limits: publicLimits{perMinute: 100, open: 8, download: true, read: time.Second, write: time.Second}, handler: h}}, time.Now)
	var first *httptest.ResponseRecorder
	for _, p := range []string{"/t/unknown", "/t/off", "/t/private", "/t/stopped"} {
		start := time.Now()
		rec := hit(g, "192.0.2.10:1000", p, nil)
		if took := time.Since(start); rec.Code != http.StatusNotFound || took < publicNotFoundAfter {
			t.Errorf("%s: %d after %v, want 404 after at least %v", p, rec.Code, took, publicNotFoundAfter)
		}
		if first == nil {
			first = rec
		} else if rec.Body.String() != first.Body.String() || !reflect.DeepEqual(rec.Header(), first.Header()) {
			t.Errorf("%s answered %v %q, not the same 404 as %v %q", p, rec.Header(), rec.Body, first.Header(), first.Body)
		}
	}
	if first.Header().Get("Cache-Control") != "no-store" || first.Header().Get("X-Route") != "" {
		t.Errorf("the 404's headers: %v", first.Header())
	}
	if rec := hit(g, "192.0.2.10:1000", "/t/file", nil); rec.Code != 200 || rec.Body.String() != "ok" || rec.Header().Get("X-Route") == "" {
		t.Errorf("an answer the route gives: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		hit(g, "192.0.2.11:1000", "/t/slot", nil)
	}()
	<-answered
	for deadline := time.Now().Add(publicNotFoundAfter / 2); len(g.downloads) != 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a 404 holds its download slot while it waits")
		}
	}
	<-done
}

// Only a route's successful answers may be cached, and only when the route
// says so, whatever its handler sets.
func TestPublicRoutesCacheOnlyWhatTheyMay(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=86400")
		switch path.Base(r.URL.Path) {
		case "partial":
			w.WriteHeader(http.StatusPartialContent)
		case "same":
			w.WriteHeader(http.StatusNotModified)
		case "moved":
			http.Redirect(w, r, "/", http.StatusFound)
		case "range":
			http.Error(w, "Requested Range Not Satisfiable", http.StatusRequestedRangeNotSatisfiable)
		default:
			io.WriteString(w, "ok")
		}
	})
	limits := publicLimits{perMinute: 100, open: 8, read: time.Second, write: time.Second}
	g := newPublicGroup([]publicRoute{
		{prefix: "/t/", limits: limits, handler: h},
		{prefix: "/c/", limits: limits, cache: packCache, handler: h},
	}, time.Now)
	for p, want := range map[string]string{
		"/c/file": packCache, "/c/partial": packCache, "/c/same": packCache, "/c/moved": "no-store", "/c/range": "no-store",
		"/t/file": "no-store", "/t/partial": "no-store", "/t/moved": "no-store",
	} {
		rec := httptest.NewRecorder()
		g.handler(p[:3]).ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if got := rec.Header().Get("Cache-Control"); got != want {
			t.Errorf("%s: Cache-Control %q, want %q", p, got, want)
		}
	}
}

func TestPanelPortServesPacksOverPlainHTTP(t *testing.T) {
	agent := &offeredPacks{}
	agent.set(sumA)
	e := newEnvAgent(t, agent.handler, io.Discard)
	storePack(t, e.cfg, sumA, "pack A")
	dir := t.TempDir()
	if _, err := EnsureSelfSignedCert(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pemBytes)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- e.srv.serve(ctx, ln, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
	}()
	addr := ln.Addr().String()

	secure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	defer secure.CloseIdleConnections()
	if r, body := get(t, secure, "GET", "https://"+addr+"/api/health", nil); r.StatusCode != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("HTTPS health: %d %q", r.StatusCode, body)
	}
	if r, body := get(t, secure, "GET", "https://"+addr+"/resource-packs/"+sumA+".zip", nil); r.StatusCode != 200 || body != "pack A" {
		t.Errorf("a pack over HTTPS, for links when the panel has a trusted certificate: %d %q", r.StatusCode, body)
	}
	plain := &http.Client{Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer plain.CloseIdleConnections()
	if r, body := get(t, plain, "GET", "http://"+addr+"/resource-packs/"+sumA+".zip", nil); r.StatusCode != 200 || body != "pack A" {
		t.Errorf("a pack over plain HTTP: %d %q", r.StatusCode, body)
	}
	if r, _ := get(t, plain, "GET", "http://"+addr+"/resource-packs/"+sumC+".zip", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown pack over plain HTTP: %d", r.StatusCode)
	}
	for _, p := range []string{"/api/health", "/", "/api/servers/" + sampleServer + "/resourcepack"} {
		r, _ := get(t, plain, "GET", "http://"+addr+p, nil)
		if r.StatusCode != http.StatusPermanentRedirect || r.Header.Get("Location") != "https://"+addr+p {
			t.Errorf("plain HTTP %s: %d to %q, want a redirect to HTTPS", p, r.StatusCode, r.Header.Get("Location"))
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("serve after its context ended = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve didn't return after its context ended")
	}
	if c, err := net.Dial("tcp", addr); err == nil {
		c.Close()
		t.Error("the panel's port still accepts connections after serve returned")
	}
}
