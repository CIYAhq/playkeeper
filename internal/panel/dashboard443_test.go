package panel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// dashboardHost is the machine's name in these tests, with a certificate.
const dashboardHost = "mc.example.com"

// newDashboardEnv is a dashboard whose machine's name works with a
// certificate, whose agent says the dashboard wants port 443 there, and
// whose public page shows one server.
func newDashboardEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.srv.static = fstest.MapFS{
		"index.html":           {Data: []byte(indexPage)},
		"assets/index-a1b2.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}
	e.dashboardState(true, false)
	e.reply("GET", "/v1/public-page", `{"address":"`+dashboardHost+`","servers":[{"slug":"survival","name":"Survival","address":"`+dashboardHost+`","state":"online","minecraftVersion":"1.21.10","type":"paper"}]}`)
	e.dashboardAddress(t, 8443, false)
	return e
}

// dashboardState has the agent say whether the dashboard wants port 443 at
// the machine's name, and whether a browser from outside reached it there;
// the keeper then looks.
func (e *env) dashboardState(dashboard, reached bool) {
	e.reply("GET", "/v1/public-page/state", `{"host":"`+dashboardHost+`","on":true,"dashboard":`+strconv.FormatBool(dashboard)+`,"reached":`+strconv.FormatBool(reached)+`}`)
	e.srv.lookAtPage(context.Background())
}

// dashboardAddress has the agent answer that the machine's name has a
// certificate, and that the dashboard's address has port.
func (e *env) dashboardAddress(t *testing.T, port int, reached bool) {
	t.Helper()
	after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	e.reply("GET", "/v1/address", `{"kind":"own","host":"`+dashboardHost+`","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
		"certificate":{"names":["`+dashboardHost+`"],"challenge":"http-01","notAfter":"`+after+`"},
		"dashboard":{"on":true,"state":"open","reached":`+strconv.FormatBool(reached)+`,"port":`+strconv.Itoa(port)+`}}`)
}

// holding443 has the keeper hold port 443, as after a hand-over.
func (e *env) holding443() {
	e.srv.page.mu.Lock()
	e.srv.page.held[0] = &pageListener{srv: &http.Server{}, port: 443}
	e.srv.page.mu.Unlock()
}

// on443 is a request to port 443 from remote.
func on443(h http.Handler, method, host, path, remote string, hdr map[string]string) (*http.Response, string) {
	req := httptest.NewRequest(method, "https://"+host+path, nil)
	req.Host = host
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp := w.Result()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

const (
	visitor  = "198.51.100.77:50000"
	neighbor = "10.0.0.8:50000"
)

// Port 443 answers the machine's name with the dashboard while the switch
// is on: its pages, API and /mcp, with the dashboard's own policy. The
// public page stays at the root for someone who isn't signed in, and the
// servers' own addresses and every other name answer as before.
func TestPort443AnswersTheMachinesNameWithTheDashboard(t *testing.T) {
	e := newDashboardEnv(t)
	own := owner(t, e)
	h := e.srv.httpsHandler()

	if resp, body := on443(h, "GET", dashboardHost, "/login", neighbor, nil); resp.StatusCode != 200 || body != indexPage {
		t.Fatalf("the sign-in page: %d %q", resp.StatusCode, body)
	}
	if resp, _ := on443(h, "GET", dashboardHost, "/api/auth/me", neighbor, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the API without a session: %d", resp.StatusCode)
	}
	if resp, _ := on443(h, "GET", dashboardHost, "/api/auth/me", neighbor, auth(own.cookie, "")); resp.StatusCode != 200 {
		t.Fatalf("the API with a session: %d", resp.StatusCode)
	}
	if resp, _ := on443(h, "POST", dashboardHost, "/mcp", neighbor, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/mcp without a token: %d", resp.StatusCode)
	}
	resp, _ := on443(h, "GET", dashboardHost, "/servers/survival", neighbor, nil)
	if csp := resp.Header.Get("Content-Security-Policy"); resp.StatusCode != 200 || strings.Contains(csp, "frame-src") || !strings.Contains(csp, "frame-ancestors 'none'") ||
		resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("a dashboard page: %d %v", resp.StatusCode, resp.Header)
	}

	// The root: the page, with a way to sign in, for someone who isn't
	// signed in.
	resp, body := on443(h, "GET", dashboardHost, "/", neighbor, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `<div id="root" data-page="server" data-sign-in="true">`) || !strings.Contains(body, "<title>Survival · Minecraft server</title>") {
		t.Fatalf("the root without a session: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Security-Policy") != pageCSP || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(resp.Header.Get("Vary"), "Cookie") ||
		resp.Header.Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("the root's page headers: %v", resp.Header)
	}
	if resp, body := on443(h, "GET", dashboardHost, "/api/public/server-page", neighbor, nil); resp.StatusCode != 200 || !strings.Contains(body, `"slug":"survival"`) {
		t.Fatalf("the page's data at the dashboard's name: %d %s", resp.StatusCode, body)
	}
	// The dashboard for someone signed in.
	resp, body = on443(h, "GET", dashboardHost, "/", neighbor, auth(own.cookie, ""))
	if resp.StatusCode != 200 || body != indexPage || !strings.Contains(resp.Header.Get("Vary"), "Cookie") || strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-src") {
		t.Fatalf("the root with a session: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	// The dashboard where customers sign in with Whop.
	for _, q := range []string{
		`INSERT INTO whop_stores(store_id, via, title, route, api_key, connected_by, connected_at) VALUES('biz_pip', 'key', 'Pip', 'pip', 'apik_x', 'admin', 1)`,
		`INSERT INTO whop_app(id, client_id) VALUES(1, 'app_pipcloud') ON CONFLICT(id) DO UPDATE SET client_id = excluded.client_id`,
	} {
		if _, err := e.srv.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, body := on443(h, "GET", dashboardHost, "/", neighbor, nil); body != indexPage {
		t.Fatalf("the root where customers sign in with Whop: %q", body)
	}
	e.srv.db.Exec(`DELETE FROM whop_stores`)
	e.srv.db.Exec(`DELETE FROM whop_app`)
	// The dashboard once no server is on the page.
	e.replyStatus("GET", "/v1/public-page", 404, `{"error":"There's no server page here.","code":"not_found"}`)
	e.clock.add(pageCacheFor + time.Second)
	if _, body := on443(h, "GET", dashboardHost, "/", neighbor, nil); body != indexPage {
		t.Fatalf("the root with no server on the page: %q", body)
	}

	// Another name, the IP address and the page's plain port: as before.
	for _, host := range []string{"other.example.com", "203.0.113.10", dashboardHost + ".evil.test"} {
		if resp, body := on443(h, "GET", host, "/api/auth/me", neighbor, auth(own.cookie, "")); resp.StatusCode != 404 || strings.Contains(body, "admin") {
			t.Errorf("%s on port 443: %d %q", host, resp.StatusCode, body)
		}
	}
	if resp, _ := on443(e.srv.pageHandler(false), "GET", dashboardHost, "/api/auth/me", neighbor, auth(own.cookie, "")); resp.StatusCode != 404 {
		t.Fatalf("the dashboard's API on port 80: %d", resp.StatusCode)
	}

	// Off, port 443 serves the page alone again.
	e.dashboardState(false, false)
	if resp, _ := on443(h, "GET", dashboardHost, "/login", neighbor, nil); resp.StatusCode != 404 {
		t.Fatalf("the sign-in page with the switch off: %d", resp.StatusCode)
	}
}

// Port 80, held for the dashboard alone, only sends browsers to HTTPS.
func TestPort80HeldForTheDashboardAloneOnlyRedirects(t *testing.T) {
	e := newDashboardEnv(t)
	e.reply("GET", "/v1/public-page/state", `{"host":"`+dashboardHost+`","on":false,"dashboard":true}`)
	e.srv.lookAtPage(context.Background())
	plain := e.srv.pageHandler(false)
	if resp, _ := on443(plain, "GET", dashboardHost, "/", neighbor, nil); resp.StatusCode != 404 {
		t.Fatalf("port 80 before port 443 serves: %d", resp.StatusCode)
	}
	now := e.clock.now()
	writeBundle(t, e.cfg.CertsDir(), dashboardHost, now.Add(-time.Hour), now.Add(90*24*time.Hour))
	e.srv.pageCerts = e.srv.pageCertStore()
	e.holding443()
	if resp, _ := on443(plain, "GET", dashboardHost, "/servers/x?a=1", neighbor, nil); resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+dashboardHost+"/servers/x?a=1" {
		t.Fatalf("port 80 once port 443 serves: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The panel's port sends a browser opening a page at the machine's name to
// port 443 only while the dashboard answers there without a port, and with
// a redirect nobody remembers; everything else keeps answering there.
func TestThePanelsPortSendsPagesTo443OnlyWhileTheDashboardAnswersThere(t *testing.T) {
	e := newDashboardEnv(t)
	h := e.srv.panelPortHandler()
	get := func(method, host, path string, hdr map[string]string) *http.Response {
		t.Helper()
		resp, _ := on443(h, method, host, path, visitor, hdr)
		return resp
	}
	withPort := dashboardHost + ":8443"
	sent := func(path string) bool {
		t.Helper()
		resp := get("GET", withPort, path, nil)
		return resp.StatusCode == http.StatusTemporaryRedirect && resp.Header.Get("Location") == "https://"+dashboardHost+path
	}
	// Held but not reached from outside, then reached but not held.
	e.holding443()
	if sent("/") {
		t.Fatal("sent to port 443 before a browser from outside reached it")
	}
	e.srv.page.mu.Lock()
	e.srv.page.held[0] = nil
	e.srv.page.mu.Unlock()
	e.dashboardState(true, true)
	if sent("/") {
		t.Fatal("sent to port 443 while the panel doesn't hold it")
	}
	e.holding443()
	for _, p := range []string{"/", "/login", "/servers/survival/players?tab=1", "/settings/whop", "/join/Xq3pL9sKd2", "/map/Xq3pL9sKd2Wm8Rt4Vn6bYc",
		"/packs/Pk7uYt2wQz9mN4bV6cX1aL", "/servers/survival/file/server.properties", "/setup"} {
		if !sent(p) {
			t.Errorf("%s wasn't sent to port 443: %d %q", p, get("GET", withPort, p, nil).StatusCode, get("GET", withPort, p, nil).Header.Get("Location"))
		}
	}
	resp := get("HEAD", withPort, "/login", nil)
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("the redirect: %d %v", resp.StatusCode, resp.Header)
	}
	for _, p := range []string{"/api/health", "/api/auth/me", "/api/public/whop/signin/callback?code=x", "/mcp", "/healthz", "/assets/index-a1b2.js", "/favicon.svg",
		"/resource-packs/" + strings.Repeat("a", 40) + ".zip", "/.well-known/playkeeper-names/n0nce", "/packs/Pk7uYt2wQz9mN4bV6cX1aL/page", "/packs/Pk7uYt2wQz9mN4bV6cX1aL/pack.mrpack"} {
		if resp := get("GET", withPort, p, nil); resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect {
			t.Errorf("%s was sent away: %d %q", p, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		for _, p := range []string{"/api/public/whop/webhook", "/login", "/"} {
			if resp := get(m, withPort, p, nil); resp.StatusCode == http.StatusTemporaryRedirect {
				t.Errorf("%s %s was sent away", m, p)
			}
		}
	}
	if resp := get("GET", withPort, "/", map[string]string{"Sec-Fetch-Dest": "empty"}); resp.StatusCode == http.StatusTemporaryRedirect {
		t.Error("a fetch was sent away")
	}
	for _, host := range []string{"203.0.113.10:8443", "other.example.com:8443"} {
		if resp := get("GET", host, "/", nil); resp.StatusCode == http.StatusTemporaryRedirect {
			t.Errorf("%s was sent to the machine's name", host)
		}
	}
	// Off: nothing is sent away, at once.
	e.reply("PUT", "/v1/dashboard-443", `{"on":false,"state":"off","port":8443}`)
	own := owner(t, e)
	if r := e.do(t, "PUT", "/api/dashboard-port", `{"on":false}`, own.auth()); r.status != 200 {
		t.Fatalf("turning it off: %d %v", r.status, r.body)
	}
	if sent("/") {
		t.Fatal("sent to port 443 after the switch went off")
	}
}

// A request that reaches port 443 from a public address tells the agent,
// once, which then lets the dashboard's address lose its port.
func TestPort443TellsTheAgentABrowserFromOutsideReachedIt(t *testing.T) {
	e := newDashboardEnv(t)
	// 203.0.113.10 is the machine's own public address, which the agent
	// refuses.
	var reports atomic.Int32
	e.agent.mu.Lock()
	e.agent.answers["POST /v1/dashboard-443/reached"] = func(w http.ResponseWriter, r *http.Request) {
		reports.Add(1)
		e.agent.mu.Lock()
		body := e.agent.lastBody["POST /v1/dashboard-443/reached"]
		e.agent.mu.Unlock()
		if strings.Contains(body, `"from":"203.0.113.10"`) {
			writeErr(w, http.StatusConflict, api.CodeConflict, "Only a visit from outside the machine shows port 443 opens.", "")
			return
		}
		writeJSON(w, 200, api.Dashboard443{On: true, State: api.PortOpen, Reached: true, Port: 443})
	}
	e.agent.mu.Unlock()
	h := e.srv.httpsHandler()
	for _, remote := range []string{neighbor, "127.0.0.1:4000", "100.101.1.2:4000", "[fd00::5]:4000", "192.168.1.9:4000"} {
		on443(h, "GET", dashboardHost, "/login", remote, nil)
	}
	time.Sleep(50 * time.Millisecond)
	if n := reports.Load(); n != 0 {
		t.Fatalf("visits from inside told the agent %d times", n)
	}
	idle := func() bool {
		e.srv.page.mu.Lock()
		defer e.srv.page.mu.Unlock()
		return !e.srv.page.reporting
	}
	on443(h, "GET", dashboardHost, "/", "203.0.113.10:4000", nil)
	eventually(t, "the agent to refuse the machine's own address", func() bool { return reports.Load() == 1 && idle() })
	on443(h, "GET", dashboardHost, "/", "203.0.113.10:4001", nil)
	time.Sleep(50 * time.Millisecond)
	if n := reports.Load(); n != 1 {
		t.Fatalf("the machine's own address was reported again within %v: %d", reportAgainAfter, n)
	}
	// A browser from outside right after it still counts.
	on443(h, "GET", "other.example.com", "/", visitor, nil)
	eventually(t, "the agent to hear of the visit", func() bool { return reports.Load() == 2 })
	req := e.agentRequest(t, "POST", "/v1/dashboard-443/reached")
	if req.body["host"] != dashboardHost || req.body["from"] != "198.51.100.77" {
		t.Fatalf("the report: %v", req.body)
	}
	eventually(t, "the keeper to know", func() bool {
		e.srv.page.mu.Lock()
		defer e.srv.page.mu.Unlock()
		return e.srv.page.reached
	})
	for range 5 {
		on443(h, "GET", dashboardHost, "/login", visitor, nil)
	}
	time.Sleep(50 * time.Millisecond)
	if n := reports.Load(); n != 2 {
		t.Fatalf("later visits told the agent again: %d", n)
	}
}

// A look at the page whose answer from the agent is from before a visit
// from outside was noted, or before the switch was turned off, undoes
// neither: 8443 keeps sending pages on after the visit, and stops at once
// when it's off.
func TestALookDoesntUndoWhatChangedWhileItAsked(t *testing.T) {
	e := newDashboardEnv(t)
	e.holding443()
	e.agent.mu.Lock()
	e.agent.answers["POST /v1/dashboard-443/reached"] = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, api.Dashboard443{On: true, State: api.PortOpen, Reached: true, Port: 443})
	}
	e.agent.mu.Unlock()
	page := func() *http.Response {
		req := httptest.NewRequest("GET", "https://"+dashboardHost+":8443/servers/x", nil)
		req.Header.Set("Sec-Fetch-Dest", "document")
		w := httptest.NewRecorder()
		e.srv.panelPortHandler().ServeHTTP(w, req)
		return w.Result()
	}
	// lookWhile runs a look whose answer from the agent is state, given
	// before change happens.
	lookWhile := func(state string, change func()) {
		t.Helper()
		gate := make(chan struct{})
		e.reply("GET", "/v1/public-page/state", state)
		e.agent.mu.Lock()
		e.agent.hits = nil
		e.agent.gates["GET /v1/public-page/state"] = gate
		e.agent.mu.Unlock()
		done := make(chan struct{})
		go func() {
			e.srv.lookAtPage(context.Background())
			close(done)
		}()
		eventually(t, "the look to ask the agent", func() bool { return e.sawLocally("GET /v1/public-page/state") })
		change()
		close(gate)
		<-done
		e.agent.mu.Lock()
		delete(e.agent.gates, "GET /v1/public-page/state")
		e.agent.mu.Unlock()
	}

	before := `{"host":"` + dashboardHost + `","on":true,"dashboard":true,"reached":false}`
	lookWhile(before, func() {
		on443(e.srv.httpsHandler(), "GET", dashboardHost, "/", visitor, nil)
		eventually(t, "the visit to be noted", func() bool {
			e.srv.page.mu.Lock()
			defer e.srv.page.mu.Unlock()
			return e.srv.page.reached
		})
	})
	if r := page(); r.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("after a look that asked before the visit, 8443 answers %d", r.StatusCode)
	}

	own := owner(t, e)
	lookWhile(`{"host":"`+dashboardHost+`","on":true,"dashboard":true,"reached":true}`, func() {
		if r := e.do(t, "PUT", "/api/dashboard-port", `{"on":false}`, own.auth()); r.status != 200 {
			t.Fatalf("turning it off: %d %v", r.status, r.body)
		}
	})
	if r := page(); r.StatusCode != http.StatusOK {
		t.Fatalf("after a look that asked before it was turned off, 8443 answers %d", r.StatusCode)
	}
}

// The dashboard's pages at the panel's port may ask port 443 at the
// machine's name whether a browser reaches it, and nothing else.
func TestTheDashboardsPagesMayCheckPort443(t *testing.T) {
	e := newDashboardEnv(t)
	resp := httptest.NewRecorder()
	e.srv.panelPortHandler().ServeHTTP(resp, httptest.NewRequest("GET", "https://203.0.113.10:8443/", nil))
	if csp := resp.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self' https://"+dashboardHost+";") {
		t.Fatalf("with a name: %q", csp)
	}
	r, body := on443(e.srv.httpsHandler(), "GET", dashboardHost, reachPath, visitor, nil)
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/gif" || r.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" || body != string(reachGIF) ||
		r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the check: %d %v %q", r.StatusCode, r.Header, body)
	}
	e.reply("GET", "/v1/public-page/state", `{"on":false}`)
	e.srv.lookAtPage(context.Background())
	resp = httptest.NewRecorder()
	e.srv.panelPortHandler().ServeHTTP(resp, httptest.NewRequest("GET", "https://203.0.113.10:8443/", nil))
	if csp := resp.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "connect-src 'self';") {
		t.Fatalf("without a name: %q", csp)
	}
	e.reply("GET", "/v1/public-page/state", `{"host":"a.example.com; script-src *","on":true,"dashboard":true}`)
	e.srv.lookAtPage(context.Background())
	resp = httptest.NewRecorder()
	e.srv.panelPortHandler().ServeHTTP(resp, httptest.NewRequest("GET", "https://203.0.113.10:8443/", nil))
	if csp := resp.Header().Get("Content-Security-Policy"); strings.Contains(csp, "script-src *") || !strings.Contains(csp, "connect-src 'self';") {
		t.Fatalf("a name that isn't one: %q", csp)
	}
}

// The addresses the dashboard gives out lose their port with it: Whop's,
// invite links, AI agents' and the ready message.
func TestTheDashboardsAddressFollowsItsPort(t *testing.T) {
	e := newDashboardEnv(t)
	own := owner(t, e)
	ctx := context.Background()
	if got, _ := e.srv.dashboardURL(ctx); got != "https://"+dashboardHost+":8443" {
		t.Fatalf("before a visit: %q", got)
	}
	e.dashboardAddress(t, 443, true)
	if got, _ := e.srv.dashboardURL(ctx); got != "https://"+dashboardHost {
		t.Fatalf("once reached: %q", got)
	}
	if now, old, _ := e.srv.dashboardURLs(ctx); now != "https://"+dashboardHost || old != "https://"+dashboardHost+":8443" {
		t.Fatalf("both addresses: %q %q", now, old)
	}
	req := httptest.NewRequest("GET", "https://203.0.113.10:8443/api/servers/x/invites", nil)
	if lb := e.srv.linkBase(req); lb.Base != "https://"+dashboardHost || !lb.Friendly {
		t.Fatalf("invite links: %+v", lb)
	}
	var link struct {
		Dashboard string `json:"dashboard"`
	}
	if st := e.get(t, "/api/machines/link", own.cookie, &link); st != 200 || link.Dashboard != "https://"+dashboardHost {
		t.Fatalf("AI agents' address: %d %q", st, link.Dashboard)
	}
	if got := e.srv.readyText(ctx, Customer{Provider: "whop", Store: "biz_pip", Subject: "user_alex"}); !strings.Contains(got, "Sign in at https://"+dashboardHost+"/login?store=biz_pip and") {
		t.Fatalf("the ready message: %q", got)
	}
	// Without a certificate, invite links name the host the admin used.
	e.setAddress(t, "")
	if lb := e.srv.linkBase(req); lb.Base != "https://203.0.113.10:8443" || lb.Friendly {
		t.Fatalf("without a name: %+v", lb)
	}
}

// The store sends buyers to the address its products name, so the marks
// follow the dashboard's port at once, as the webhook does, rather than at
// the next ten-minute read of the store: losing port 443 closes the address
// without a port. A Whop refusing the marks is asked again a minute later,
// not at every pass.
func TestTheStoreFollowsTheDashboardsPortAtOnce(t *testing.T) {
	f, e, _ := connectedWhop(t)
	const bare = "https://beta.playkeeper.me"
	port := func(p int) {
		after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
		e.replyStatus("GET", "/v1/address", 200, `{"kind":"playkeeper","host":"beta.playkeeper.me","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
			"certificate":{"names":["beta.playkeeper.me"],"challenge":"dns-01","notAfter":"`+after+`"},
			"dashboard":{"on":true,"state":"open","reached":true,"port":`+strconv.Itoa(p)+`}}`)
	}
	hook := func() any {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, h := range f.webhooks {
			return h["url"]
		}
		return nil
	}
	reads := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.planReads
	}

	port(443)
	e.reconcile()
	if f.dashboardMeta() != bare || hook() != bare+whopWebhookPath {
		t.Fatalf("on port 443: the store names %q, the webhook %v", f.dashboardMeta(), hook())
	}
	n := reads()
	e.clock.add(time.Minute)
	e.reconcile()
	if reads() != n {
		t.Fatal("the store was read again though nothing moved")
	}

	port(8443)
	f.refuseMarks(true)
	e.reconcile()
	if hook() != whopDashboard+whopWebhookPath || f.dashboardMeta() != bare {
		t.Fatalf("with Whop refusing the marks: the webhook %v, the store names %q", hook(), f.dashboardMeta())
	}
	n = reads()
	e.reconcile()
	if reads() != n {
		t.Fatal("Whop was asked for the marks again within the minute")
	}
	f.refuseMarks(false)
	e.clock.add(time.Minute)
	e.reconcile()
	if f.dashboardMeta() != whopDashboard {
		t.Fatalf("a minute later the store names %q", f.dashboardMeta())
	}
}

// Machine settings shows the switch with the changes it needs outside
// Playkeeper, Whop's to the owner alone, and passes a refusal on with
// what has port 443.
func TestTheSwitchSaysWhatOutsidePlaykeeperKeepsTheOldAddress(t *testing.T) {
	f, e, own, _, _ := sellingWithSignIn(t)
	e.reply("GET", "/v1/public-page/state", `{"host":"beta.playkeeper.me","on":true,"dashboard":true}`)
	e.srv.lookAtPage(context.Background())
	after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	e.reply("GET", "/v1/address", `{"kind":"own","host":"beta.playkeeper.me","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
		"certificate":{"names":["beta.playkeeper.me"],"challenge":"http-01","notAfter":"`+after+`"},"dashboard":{"on":true,"state":"open","port":8443}}`)
	if _, err := e.srv.db.Exec(`INSERT INTO api_tokens(id, user_id, name, token_hash, role, servers, created_at, expires_at) VALUES('tok1', ?, 'claude', 'h', 'viewer', '*', 0, ?)`,
		own.id, e.clock.now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	var v dashboardPortView
	if st := e.get(t, "/api/dashboard-port?check=1", own.cookie, &v); st != 200 {
		t.Fatalf("GET: %d", st)
	}
	const bare, old = "https://beta.playkeeper.me", "https://beta.playkeeper.me:8443"
	if !v.On || v.URL != bare || v.Old != old || v.PanelPort != 8443 || v.Port != 8443 || v.Serving {
		t.Fatalf("the view before the panel has port 443: %+v", v)
	}
	want := []outsideChange{
		{Kind: "whop_signin", App: whopTestApp, Add: bare + whopSignInCallback, Keep: old + whopSignInCallback},
		{Kind: "whop_webhook", Add: bare + whopWebhookPath, Automatic: true},
		{Kind: "mcp", Add: bare + "/mcp", Keep: old + "/mcp"},
	}
	if len(v.Outside) != len(want) {
		t.Fatalf("outside Playkeeper: %+v", v.Outside)
	}
	for i := range want {
		if v.Outside[i] != want[i] {
			t.Errorf("change %d: %+v, want %+v", i, v.Outside[i], want[i])
		}
	}
	// The owner adds the redirect URL on Whop.
	f.mu.Lock()
	f.redirects = []string{old + whopSignInCallback, bare + whopSignInCallback}
	f.mu.Unlock()
	e.get(t, "/api/dashboard-port?check=1", own.cookie, &v)
	if len(v.Outside) == 0 || !v.Outside[0].Done {
		t.Fatalf("once Whop lists the new redirect URL: %+v", v.Outside)
	}
	// An admin sees no Whop.
	lena := addAdmin(t, e, "lena", "*")
	e.get(t, "/api/dashboard-port", lena.cookie, &v)
	for _, c := range v.Outside {
		if strings.HasPrefix(c.Kind, "whop") {
			t.Errorf("an admin sees %+v", c)
		}
	}

	// Once the panel has port 443, the page's check of it may count.
	e.holding443()
	if e.get(t, "/api/dashboard-port", own.cookie, &v); !v.Serving {
		t.Fatalf("the view once the panel has port 443: %+v", v)
	}

	// Turning it on: the agent hears who, and whether the panel holds port
	// 443; its refusal, with what has the port, reaches the page as is.
	e.replyStatus("PUT", "/v1/dashboard-443", 409, `{"error":"nginx uses port 443, so the dashboard can't have it.","code":"port_in_use","params":{"port":443,"state":"busy","holder":"nginx"}}`)
	r := e.do(t, "PUT", "/api/dashboard-port", `{"on":true}`, own.auth())
	if r.status != 409 || r.body["code"] != api.CodePortInUse || r.body["error"] != "nginx uses port 443, so the dashboard can't have it." {
		t.Fatalf("a refusal: %d %v", r.status, r.body)
	}
	if b := e.agentRequest(t, "PUT", "/v1/dashboard-443").body; b["on"] != true || b["held"] != true || b["actor"] != "admin" {
		t.Fatalf("what the agent heard: %v", b)
	}
	if r := e.do(t, "PUT", "/api/dashboard-port", `{"on":true,"extra":1}`, own.auth()); r.status != 400 {
		t.Fatalf("an unknown field: %d", r.status)
	}
	e.replyStatus("POST", "/v1/public-page/ports/retry", 200, `{"ok":true}`)
	if r := e.do(t, "POST", "/api/dashboard-port/retry", ``, own.auth()); r.status != 200 {
		t.Fatalf("trying again: %d %v", r.status, r.body)
	}
	if b := e.agentRequest(t, "POST", "/v1/public-page/ports/retry").body; b["actor"] != "admin" {
		t.Fatalf("the retry: %v", b)
	}
}

// Sign in with Whop sends Whop a redirect URL the app lists: the
// dashboard's address without a port once the app has it, and until then
// the one with the panel's port, which keeps reaching the dashboard. Each
// sign-in trades its code with the redirect URL it left with.
func TestSignInWithWhopKeepsARedirectURLTheAppLists(t *testing.T) {
	f, e, own, _, _ := sellingWithSignIn(t)
	const bare = "https://beta.playkeeper.me"
	old := whopDashboard + whopSignInCallback
	b := newBrowser(t, e)
	_, leftEarlier := b.visit(whopSignInPath)
	if !strings.Contains(leftEarlier, "redirect_uri="+urlQuery(old)) {
		t.Fatalf("before the switch: %s", leftEarlier)
	}

	e.dashboardOn443(t)
	if v := e.whopView(t, own); v.Dashboard != bare || v.SignIn.RedirectURI != bare+whopSignInCallback || v.SignIn.Using != old {
		t.Fatalf("Sell on Whop while Whop lists only the old redirect URL: %q %+v", v.Dashboard, v.SignIn)
	}
	b2 := newBrowser(t, e)
	_, authorize := b2.visit(whopSignInPath)
	if !strings.Contains(authorize, "redirect_uri="+urlQuery(old)) {
		t.Fatalf("while Whop lists only the old redirect URL: %s", authorize)
	}
	if _, to := b2.visit(f.approve(t, authorize, "user_alex")); to != "/" {
		t.Fatalf("signing in through the old redirect URL: %q", to)
	}
	// A sign-in that left before the switch comes back as it left.
	if _, to := b.visit(f.approve(t, leftEarlier, "user_alex")); to != "/" {
		t.Fatalf("a sign-in from before the switch: %q", to)
	}
	// While Whop's answer on the new one says neither, sign-ins keep the old
	// one, which Whop says the app lists.
	f.mu.Lock()
	f.unsure = []string{bare + whopSignInCallback}
	f.mu.Unlock()
	e.clock.add(redirectRefusedFor + time.Second)
	_, authorize = newBrowser(t, e).visit(whopSignInPath)
	if !strings.Contains(authorize, "redirect_uri="+urlQuery(old)) {
		t.Fatalf("while Whop's answer on the new one says neither: %s", authorize)
	}
	f.mu.Lock()
	f.unsure = nil
	f.mu.Unlock()

	// The owner adds the new one on Whop: sign-ins use it once Whop's last
	// answer is old.
	f.mu.Lock()
	f.redirects = []string{old, bare + whopSignInCallback}
	f.mu.Unlock()
	e.clock.add(redirectRefusedFor + time.Second)
	b3 := newBrowser(t, e)
	_, authorize = b3.visit(whopSignInPath)
	if !strings.Contains(authorize, "redirect_uri="+urlQuery(bare+whopSignInCallback)) {
		t.Fatalf("once Whop lists the new one: %s", authorize)
	}
	if _, to := b3.visit(f.approve(t, authorize, "user_alex")); to != "/" {
		t.Fatalf("signing in through the new redirect URL: %q", to)
	}
	if v := e.whopView(t, own); v.SignIn.Using != "" {
		t.Fatalf("Sell on Whop once Whop lists the new one: %+v", v.SignIn)
	}

	// Turn on refuses an app that lists neither.
	f.mu.Lock()
	f.redirects = []string{"https://elsewhere.example/cb"}
	f.mu.Unlock()
	r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth())
	if hint, _ := r.body["hint"].(string); r.status != 400 || r.body["error"] != "Whop doesn't list this dashboard's redirect URL on that app." || !strings.Contains(hint, "add "+bare+whopSignInCallback+" as a redirect URL") {
		t.Fatalf("an app that lists neither: %d %v", r.status, r.body)
	}
}

// dashboardOn443 has the agent answer that the dashboard answers
// beta.playkeeper.me on port 443 without a port.
func (e *env) dashboardOn443(t *testing.T) {
	t.Helper()
	after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	e.reply("GET", "/v1/address", `{"kind":"playkeeper","host":"beta.playkeeper.me","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
		"certificate":{"names":["beta.playkeeper.me"],"challenge":"dns-01","notAfter":"`+after+`"},"dashboard":{"on":true,"state":"open","reached":true,"port":443}}`)
}

func urlQuery(s string) string {
	return strings.NewReplacer(":", "%3A", "/", "%2F").Replace(s)
}

func TestDashboardPagesAreWhatBrowsersOpen(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/login": true, "/servers/survival": true, "/servers/survival/files/plugins/x.yml": true, "/join/abc": true, "/map/abc": true, "/packs/abc": true,
		"/api/health": false, "/api": false, "/mcp": false, "/mcp/x": false, "/healthz": false, "/assets/a.js": false, "/favicon.svg": false, "/index.html": false,
		"/resource-packs/a.zip": false, "/.well-known/playkeeper-names/x": false, "/.well-known/acme-challenge/x": false, "/packs/abc/page": false, "/packs/": false,
	} {
		if got := dashboardPage(p); got != want {
			t.Errorf("%s: %v, want %v", p, got, want)
		}
	}
}
