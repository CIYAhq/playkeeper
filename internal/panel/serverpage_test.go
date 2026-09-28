package panel

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

const pageHostName = "mc.example.com"

var facePNG = []byte("\x89PNG\r\n\x1a\nface bytes")

// pageAgent answers the panel's questions about the public page as an agent
// would, for a machine whose address is pageHostName.
type pageAgent struct {
	on      atomic.Bool
	names   []string
	pages   atomic.Int32
	asks    chan api.PagePortsRequest
	answer  func(api.PagePortsRequest) (api.PublicPagePorts, []*os.File)
	retried atomic.Int32
}

func (a *pageAgent) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/public-page/state":
			writeJSON(w, 200, api.PublicPageState{Host: pageHostName, On: a.on.Load()})
		case r.URL.Path == "/v1/public-page":
			a.pages.Add(1)
			if !a.on.Load() || r.URL.Query().Get("host") == "" || !pageHost(r.URL.Query().Get("host"), pageHostName) {
				writeErr(w, 404, api.CodeNotFound, "There's no server page here.", "")
				return
			}
			sv := api.PublicServer{Slug: "survival", Name: `Tom's <script>alert(1)</script> "world"`, MOTD: "Hi", Address: pageHostName, State: api.PublicOnline,
				Players: &api.PublicPlayers{Online: len(a.names), Max: 10, Names: a.names}, MinecraftVersion: "1.21.10", Type: "paper", HasIcon: true}
			writeJSON(w, 200, api.PublicPage{Address: pageHostName, Servers: []api.PublicServer{sv}})
		case r.URL.Path == "/v1/public-page/icons/survival":
			w.Header().Set("Content-Type", "image/png")
			w.Write(tilePNG)
		case r.URL.Path == "/v1/acme-challenge/tok3n":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "tok3n.key")
		case strings.HasPrefix(r.URL.Path, "/v1/acme-challenge/"):
			writeErr(w, 404, api.CodeNotFound, "No check is pending for that token.", "")
		case r.URL.Path == "/v1/public-page/ports":
			var want api.PagePortsRequest
			json.NewDecoder(r.Body).Decode(&want)
			if a.asks != nil {
				a.asks <- want
			}
			ports, files := a.answer(want)
			sendSockets(t, w, ports, files)
		case r.URL.Path == "/v1/public-page/ports/retry":
			a.retried.Add(1)
			writeJSON(w, 200, map[string]bool{"ok": true})
		default:
			writeErr(w, 404, api.CodeNotFound, "Unknown agent operation.", "")
		}
	}
}

// sendSockets answers as the agent does, passing files with the answer.
func sendSockets(t *testing.T, w http.ResponseWriter, v any, files []*os.File) {
	body, _ := json.Marshal(v)
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	var oob []byte
	if len(files) > 0 {
		fds := []int{}
		for _, f := range files {
			fds = append(fds, int(f.Fd()))
		}
		oob = syscall.UnixRights(fds...)
	}
	msg := fmt.Appendf(nil, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	if _, _, err := conn.(*net.UnixConn).WriteMsgUnix(msg, oob, nil); err != nil {
		t.Error(err)
	}
	for _, f := range files {
		f.Close()
	}
}

func newPageEnv(t *testing.T, a *pageAgent) *env {
	t.Helper()
	e, _ := newScriptedEnv(t, a.handle(t))
	e.srv.static = fstest.MapFS{
		"index.html":           {Data: []byte(indexPage)},
		"assets/index-a1b2.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}
	e.srv.page.mu.Lock()
	e.srv.page.host = pageHostName
	e.srv.page.mu.Unlock()
	return e
}

// pageGet asks the page's handler for path with the Host header host.
func pageGet(t *testing.T, h http.Handler, method, host, path string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	req.Host = host
	req.RemoteAddr = "198.51.100.7:40000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp := w.Result()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestThePagesPortsServeThePageAndNothingElse(t *testing.T) {
	a := &pageAgent{}
	a.on.Store(true)
	e := newPageEnv(t, a)
	h := e.srv.securityHeaders(e.srv.pageHandler(true))

	resp, body := pageGet(t, h, "GET", pageHostName, "/")
	if resp.StatusCode != 200 || !strings.Contains(body, `<div id="root" data-page="server">`) {
		t.Fatalf("the page: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "<title>Tom&#39;s &lt;script&gt;alert(1)&lt;/script&gt; &#34;world&#34; · Minecraft server</title>") || strings.Contains(body, "<script>alert") {
		t.Fatalf("the owner's text isn't escaped: %s", body)
	}
	if !strings.Contains(body, `content="Online, 0 of 10 playing · Minecraft: Java Edition 1.21.10 · Join at mc.example.com"`) || !strings.Contains(body, `<meta name="robots" content="noindex">`) {
		t.Fatalf("the page's description: %s", body)
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") || !strings.Contains(got, "frame-ancestors 'none'") {
		t.Fatalf("the page's CSP: %q", got)
	}
	if resp.Header.Get("X-Robots-Tag") != "noindex" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("headers: %v", resp.Header)
	}

	resp, body = pageGet(t, h, "GET", pageHostName, "/api/public/server-page")
	var page api.PublicPage
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &page) != nil || len(page.Servers) != 1 || page.Servers[0].Slug != "survival" {
		t.Fatalf("the page's data: %d %s", resp.StatusCode, body)
	}
	if resp, body := pageGet(t, h, "GET", pageHostName, "/api/public/server-page/icons/survival"); resp.StatusCode != 200 || body != string(tilePNG) || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("the icon: %d %q", resp.StatusCode, body)
	}
	if resp, body := pageGet(t, h, "GET", pageHostName, "/assets/index-a1b2.js"); resp.StatusCode != 200 || body != "console.log(1)" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("an asset: %d %q %v", resp.StatusCode, body, resp.Header)
	}

	// Another name, the IP address, the dashboard and everything but GET and
	// HEAD get one plain 404.
	notFound := func(method, host, path string) {
		t.Helper()
		resp, body := pageGet(t, h, method, host, path)
		// A path with dot segments is sent to its cleaned form first.
		if loc := resp.Header.Get("Location"); resp.StatusCode == http.StatusTemporaryRedirect && strings.HasPrefix(loc, "/") {
			resp, body = pageGet(t, h, method, host, loc)
		}
		if resp.StatusCode != 404 || strings.Contains(body, "Tom") || strings.Contains(body, "survival") {
			t.Errorf("%s %s%s: %d %q", method, host, path, resp.StatusCode, body)
		}
	}
	for _, host := range []string{"other.example.com", "203.0.113.10", "mc.example.com.evil.test", ""} {
		notFound("GET", host, "/")
		notFound("GET", host, "/api/public/server-page")
	}
	for _, path := range []string{"/api/auth/me", "/api/servers", "/api/setup/status", "/mcp", "/setup", "/login", "/map/Xq3pL9sKd2Wm8Rt4Vn6bYc", "/packs/Pk7uYt2wQz9mN4bV6cX1aL",
		"/api/public/server-page/icons/../../v1/servers", "/api/public/server-page/nope", "/resource-packs/" + strings.Repeat("a", 40) + ".zip", "/index.html", "/assets/../index.html", "/healthz"} {
		notFound("GET", pageHostName, path)
	}
	notFound("POST", pageHostName, "/")
	notFound("DELETE", pageHostName, "/api/public/server-page")
}

func TestThePageNamesAndFacesOnlyPlayersTheOwnerShows(t *testing.T) {
	a := &pageAgent{}
	a.on.Store(true)
	e := newPageEnv(t, a)
	if _, err := e.srv.db.Exec(`INSERT INTO player_heads(name, status, png, fetched_at) VALUES('mara_k', ?, ?, ?)`, headOK, facePNG, e.clock.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	h := e.srv.pageHandler(true)
	if resp, _ := pageGet(t, h, "GET", pageHostName, "/api/public/server-page/faces/mara_k"); resp.StatusCode != 404 {
		t.Fatalf("a face while no names are shown: %d", resp.StatusCode)
	}
	a.names = []string{"mara_k"}
	e.clock.add(pageCacheFor + time.Second)
	if resp, body := pageGet(t, h, "GET", pageHostName, "/api/public/server-page/faces/MARA_K"); resp.StatusCode != 200 || body != string(facePNG) {
		t.Fatalf("a listed player's face: %d %q", resp.StatusCode, body)
	}
	if resp, _ := pageGet(t, h, "GET", pageHostName, "/api/public/server-page/faces/Notch"); resp.StatusCode != 404 {
		t.Fatalf("an unlisted player's face: %d", resp.StatusCode)
	}
}

func TestManyVisitorsAskTheAgentOnceAndEachAddressIsLimited(t *testing.T) {
	a := &pageAgent{}
	a.on.Store(true)
	e := newPageEnv(t, a)
	h := e.srv.pageHandler(true)
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "http://"+pageHostName+"/api/public/server-page", nil)
			req.Host = pageHostName
			req.RemoteAddr = "198.51.100." + strconv.Itoa(i+1) + ":4000"
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	if n := a.pages.Load(); n != 1 {
		t.Fatalf("40 visitors at once asked the agent %d times", n)
	}
	limited := 0
	for range pageLimits.perMinute + 5 {
		if resp, _ := pageGet(t, h, "GET", pageHostName, "/"); resp.StatusCode == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 5 {
		t.Fatalf("%d of %d page requests from one address were refused, want 5", limited, pageLimits.perMinute+5)
	}
}

func TestThePagesPort80RedirectsOnlyWhileHTTPSServesWithACertificate(t *testing.T) {
	a := &pageAgent{}
	a.on.Store(true)
	e := newPageEnv(t, a)
	h := e.srv.pageHandler(false)
	now := e.clock.now()
	writeBundle(t, e.cfg.CertsDir(), pageHostName, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	e.srv.pageCerts = e.srv.pageCertStore()
	if resp, body := pageGet(t, h, "GET", pageHostName, "/"); resp.StatusCode != 200 || !strings.Contains(body, `data-page="server"`) {
		t.Fatalf("with a certificate but port 443 not served, plain HTTP must serve the page: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	e.srv.page.mu.Lock()
	e.srv.page.held[0] = &pageListener{srv: &http.Server{}, port: 443}
	e.srv.page.mu.Unlock()
	resp, _ := pageGet(t, h, "GET", pageHostName, "/?a=1")
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+pageHostName+"/?a=1" {
		t.Fatalf("with port 443 served: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Let's Encrypt's checks are passed on, not redirected.
	if resp, body := pageGet(t, h, "GET", pageHostName, "/.well-known/acme-challenge/tok3n"); resp.StatusCode != 200 || body != "tok3n.key" {
		t.Fatalf("a check: %d %q", resp.StatusCode, body)
	}
	if resp, _ := pageGet(t, h, "GET", pageHostName, "/.well-known/acme-challenge/other"); resp.StatusCode != 404 {
		t.Fatalf("a check nobody made: %d", resp.StatusCode)
	}
	// Port 443 hands out only the agent's certificate for the name, never the
	// self-signed one.
	if _, err := e.srv.pageCertificate(&tls.ClientHelloInfo{ServerName: "203.0.113.10"}); err == nil {
		t.Fatal("port 443 answered an IP address with a certificate")
	}
	if c, err := e.srv.pageCertificate(&tls.ClientHelloInfo{ServerName: pageHostName}); err != nil || c == nil {
		t.Fatalf("port 443 has no certificate for the name: %v", err)
	}
}

func TestTheKeeperHoldsThePortsOnlyWhileThePageIsOn(t *testing.T) {
	listen := func() (*os.File, int) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		f, err := ln.(*net.TCPListener).File()
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		return f, port
	}
	a := &pageAgent{asks: make(chan api.PagePortsRequest, 4)}
	var https int
	a.answer = func(want api.PagePortsRequest) (api.PublicPagePorts, []*os.File) {
		var files []*os.File
		out := api.PublicPagePorts{HTTPS: api.PagePort{Port: 443, State: api.PortOff}, HTTP: api.PagePort{Port: 80, State: api.PortOff}}
		if want.HTTPS {
			f, p := listen()
			https, files, out.HTTPS = p, append(files, f), api.PagePort{Port: p, State: api.PortOpen}
		}
		if want.HTTP {
			out.HTTP = api.PagePort{Port: 80, State: api.PortBusy, Holder: "nginx"}
		}
		return out, files
	}
	e := newPageEnv(t, a)
	ctx := context.Background()

	// Off: nothing is asked for or held.
	e.srv.lookAtPage(ctx)
	if len(a.asks) != 0 || e.srv.page.portsNow().HTTPS.State != api.PortOff {
		t.Fatalf("with the page off the keeper asked %d times: %+v", len(a.asks), e.srv.page.portsNow())
	}
	// On: the free port is served, the busy one noted and not asked for at
	// the next look; the held one is never asked for again.
	a.on.Store(true)
	e.srv.lookAtPage(ctx)
	if want := <-a.asks; !want.HTTPS || !want.HTTP {
		t.Fatalf("the first ask: %+v", want)
	}
	ports := e.srv.page.portsNow()
	if ports.HTTPS.State != api.PortOpen || ports.HTTP.State != api.PortBusy || ports.HTTP.Holder != "nginx" {
		t.Fatalf("after the first look: %+v", ports)
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(https))
	if err != nil {
		t.Fatalf("the page doesn't answer on its port: %v", err)
	}
	c.Close()
	e.srv.lookAtPage(ctx)
	if len(a.asks) != 0 {
		t.Fatalf("the keeper asked again for %+v", <-a.asks)
	}
	// The owner frees port 80 and asks to try again: only that port is asked
	// for.
	e.srv.page.retryNow()
	e.srv.lookAtPage(ctx)
	if want := <-a.asks; want.HTTPS || !want.HTTP {
		t.Fatalf("after a retry the keeper asked for %+v", want)
	}
	// Off again: the ports are given back at once.
	a.on.Store(false)
	e.srv.lookAtPage(ctx)
	if st := e.srv.page.portsNow(); st.HTTPS.State != api.PortOff || st.HTTP.State != api.PortOff {
		t.Fatalf("after turning the page off: %+v", st)
	}
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(https)); err != nil {
		t.Fatalf("the keeper kept port %d after the page went off: %v", https, err)
	} else {
		ln.Close()
	}
}

func TestTheDashboardShowsThePortsAndTriesAgain(t *testing.T) {
	a := &pageAgent{}
	e := newPageEnv(t, a)
	e.srv.page.mu.Lock()
	e.srv.page.ports.HTTPS = api.PagePort{Port: 443, State: api.PortClaimed, Holder: "nginx"}
	e.srv.page.next = [2]time.Time{e.clock.now().Add(time.Hour), e.clock.now().Add(time.Hour)}
	e.srv.page.mu.Unlock()
	if s := e.srv.page.portsNow(); s.HTTPS.Holder != "nginx" {
		t.Fatalf("ports: %+v", s)
	}
	e.srv.page.retryNow()
	if want := e.srv.pagePortsWanted(); !want.HTTPS || !want.HTTP {
		t.Fatalf("after a retry the keeper wants %+v", want)
	}
	e.cfg.PanelPort = 443
	e.srv.cfg.PanelPort = 443
	if want := e.srv.pagePortsWanted(); want.HTTPS {
		t.Fatal("the keeper asks for the dashboard's own port")
	}
}
