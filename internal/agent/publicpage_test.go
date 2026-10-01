package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

const pageTestHost = "mc.example.com"

// withPageAddress gives the machine an own domain, where the page answers.
func (e *agentEnv) withPageAddress() {
	e.t.Helper()
	if err := e.a.updateAddress(func(st *addressState) { st.Kind, st.Host = api.AddressOwn, pageTestHost }); err != nil {
		e.t.Fatal(err)
	}
}

// page is what the public page shows to a browser that asked for host.
func (e *agentEnv) page(host string) (int, api.PublicPage, string) {
	e.t.Helper()
	resp, err := http.Get(e.ts.URL + "/v1/public-page?host=" + host)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var p api.PublicPage
	if resp.StatusCode == 200 {
		if err := json.Unmarshal(b, &p); err != nil {
			e.t.Fatal(err)
		}
	}
	return resp.StatusCode, p, string(b)
}

func TestThePublicPageShowsAServerButNotWhosPlaying(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.withPageAddress()
	e.rcon.setOnline("tobi2009", "mara_k")
	var p api.PublicPage
	e.waitFor("the page to count the players", func() bool {
		code, got, _ := e.page(pageTestHost)
		p = got
		return code == 200 && len(got.Servers) == 1 && got.Servers[0].Players != nil && got.Servers[0].Players.Online == 2
	})
	s := p.Servers[0]
	if p.Address != pageTestHost || s.State != api.PublicOnline || s.Address != pageTestHost || s.Players.Max < 2 || s.MinecraftVersion == "" || s.Type != api.TypePaper || s.Name == "" {
		t.Fatalf("the page shows %+v", p)
	}
	if len(s.Players.Names) != 0 || s.Map != "" || s.Pack != "" {
		t.Fatalf("a page nobody changed names players or links: %+v", s)
	}
	_, _, raw := e.page(pageTestHost)
	for _, leak := range []string{testIP.String(), "tobi2009", "mara_k", e.sid, "memory", "crash", "backup"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the public page's answer holds %q: %s", leak, raw)
		}
	}
	// The owner shows who's playing: the names, sorted; then hides them again.
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"players": true, "actor": "admin"}); code != 200 || out["players"] != true || out["enabled"] != true {
		t.Fatalf("showing players: %d %v", code, out)
	}
	e.waitFor("the names on the page", func() bool {
		_, got, _ := e.page(pageTestHost)
		return len(got.Servers) == 1 && got.Servers[0].Players != nil && slices.Equal(got.Servers[0].Players.Names, []string{"mara_k", "tobi2009"})
	})
	e.call("POST", e.sp("/public-page"), map[string]any{"players": false, "actor": "admin"})
	if _, got, _ := e.page(pageTestHost); len(got.Servers) != 1 || got.Servers[0].Players == nil || len(got.Servers[0].Players.Names) != 0 {
		t.Fatalf("names stay after the owner hid them: %+v", got)
	}
}

func TestAPublicPageThatIsOffAnswersLikeAnUnknownAddress(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.withPageAddress()
	unknown, _, unknownBody := e.page("other.example.com")
	if unknown != 404 {
		t.Fatalf("another address gets %d", unknown)
	}
	for _, host := range []string{pageTestHost, "MC.Example.com.", pageTestHost + ":443", pageTestHost + ":80"} {
		if code, _, _ := e.page(host); code != 200 {
			t.Fatalf("the machine's address as %q gets %d", host, code)
		}
	}
	if code, out := e.call("POST", e.sp("/public-page"), map[string]any{"enabled": false, "actor": "admin"}); code != 200 || out["enabled"] != false {
		t.Fatalf("turning the page off: %d %v", code, out)
	}
	code, _, body := e.page(pageTestHost)
	if code != 404 || body != unknownBody {
		t.Fatalf("a page that is off answers %d %s, an unknown address %d %s", code, body, unknown, unknownBody)
	}
	if st := e.a.publicPageState(nil); st.On || st.Host != pageTestHost {
		t.Fatalf("with every server off the page the state is %+v", st)
	}
	slug := e.status().Slug
	if code, _ := e.call("GET", "/v1/public-page/icons/"+slug+"?host="+pageTestHost, nil); code != 404 {
		t.Fatalf("the icon of a server off the page answers %d", code)
	}
	var audited int
	e.a.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action = 'public_page.changed' AND actor = 'admin'`).Scan(&audited)
	if audited != 1 {
		t.Fatalf("%d audit rows for changing the page", audited)
	}
}

func TestAMachineWithoutAnAddressHasNoPublicPage(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) { e.cfg.Dev = false })
	e.create()
	if st := e.a.publicPageState(nil); st.On || st.Host != "" {
		t.Fatalf("the state without an address is %+v", st)
	}
	if code, _, _ := e.page("localhost"); code != 404 {
		t.Fatalf("the page answers %d without an address", code)
	}
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	if len(files) != 0 || ports.HTTPS.State != api.PortOff || ports.HTTP.State != api.PortOff {
		t.Fatalf("without an address the agent opened %v: %+v", len(files), ports)
	}
}

// The dashboard serves the page of a server on a joined machine at the
// server's name, and asks the machine what the page shows of it: all the
// machine's own page shows of it but where players join, which is the
// dashboard's to say, and its shared links as tokens, not links under the
// machine's own name. A server off the page is the page's 404.
func TestThePageShowsTheDashboardAServerButNotTheMachinesAddress(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.nameWorks("play.example.com")
	e.createWith(map[string]any{"name": "Survival"})
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	code, out := e.call("POST", e.sp("/map/share"), map[string]any{"public": true, "actor": "admin"})
	path, _ := out["path"].(string)
	mapToken := strings.TrimPrefix(path, "/map/")
	if code != 200 || !webmap.ValidShareToken(mapToken) {
		t.Fatalf("share: %d %v", code, out)
	}
	const packToken = "Pk7uYt2wQz9mN4bV6cX1aL"
	if _, err := e.a.db.Exec(`UPDATE servers SET packs_public = 1, packs_token = ? WHERE id = ?`, packToken, e.sid); err != nil {
		t.Fatal(err)
	}
	e.rcon.setOnline("mara_k")
	e.call("POST", e.sp("/public-page"), map[string]any{"players": true, "actor": "admin"})
	var own api.PublicServer
	e.waitFor("the machine's own page to name the player", func() bool {
		code, p, _ := e.page("play.example.com")
		if code != 200 || len(p.Servers) != 1 || p.Servers[0].Players == nil {
			return false
		}
		own = p.Servers[0]
		return len(own.Players.Names) == 1
	})
	if !strings.Contains(own.Map, "play.example.com") || !strings.Contains(own.Pack, "play.example.com") || own.Address == "" {
		t.Fatalf("the machine's own page, under its own name: %+v", own)
	}

	code, _, raw := e.get(e.sp("/public-page/shown"))
	var shown api.PublicServerShown
	if code != 200 || json.Unmarshal(raw, &shown) != nil {
		t.Fatalf("what the page shows of the server: %d %s", code, raw)
	}
	if shown.Name != "Survival" || shown.State != api.PublicOnline || shown.Players == nil || !slices.Equal(shown.Players.Names, []string{"mara_k"}) ||
		shown.MapToken != mapToken || shown.PackToken != packToken {
		t.Fatalf("the dashboard hears %s", raw)
	}
	if shown.Address != "" || shown.Bedrock != nil || shown.Map != "" || shown.Pack != "" {
		t.Fatalf("the dashboard hears where players join or a link: %s", raw)
	}
	for _, leak := range []string{"play.example.com", testIP.String()} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("what the page shows of the server holds %q: %s", leak, raw)
		}
	}

	e.call("POST", e.sp("/public-page"), map[string]any{"enabled": false, "actor": "admin"})
	if code, _, _ := e.get(e.sp("/public-page/shown")); code != 404 {
		t.Fatalf("a server off the page answers %d", code)
	}
}

func TestThePublicPageSwitchesRefuseNothingToChange(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"actor": "admin"}); code != 400 {
		t.Fatalf("no switch named: %d", code)
	}
	if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"enabled": true}); code != 400 {
		t.Fatalf("no actor: %d", code)
	}
	if code, out := e.call("GET", e.sp("/public-page"), nil); code != 200 || out["enabled"] != true || out["players"] != false {
		t.Fatalf("a new server's switches: %d %v", code, out)
	}
}

// freePort is a TCP port nothing listens on right now. It comes from below
// the range the kernel hands out for ":0" and outgoing connections, so a
// connection elsewhere in the run can't take it between the tests' binds.
// scripts/go-test-shard.sh runs the agent's tests in shards side by side, and
// each shard takes its ports from its own part of the range, so another
// shard can't take a port a test lets go of and binds again.
func freePort(t *testing.T) int {
	t.Helper()
	lo, n := 20000, 10000
	k, kerr := strconv.Atoi(os.Getenv("PLAYKEEPER_TEST_SHARD"))
	of, oerr := strconv.Atoi(os.Getenv("PLAYKEEPER_TEST_SHARDS"))
	if kerr == nil && oerr == nil && of > 0 && k >= 1 && k <= of {
		n = 10000 / of
		lo += (k - 1) * n
	}
	for range 100 {
		p := lo + rand.IntN(n)
		if ln, err := net.Listen("tcp", ":"+strconv.Itoa(p)); err == nil {
			ln.Close()
			return p
		}
	}
	t.Fatalf("no free port between %d and %d", lo, lo+n-1)
	return 0
}

// pageEnv is an agent whose page answers at pageTestHost, with the page's
// ports on free high ports and the machine up for up.
func pageEnv(t *testing.T, up time.Duration, setup func(e *agentEnv)) (e *agentEnv, https, plain int) {
	t.Helper()
	https, plain = freePort(t), freePort(t)
	systemd := t.TempDir()
	e = newAgentEnvWith(t, func(e *agentEnv) {
		e.tweak = func(o *Options) {
			o.PageHTTPSAddr, o.PageHTTPAddr = ":"+strconv.Itoa(https), ":"+strconv.Itoa(plain)
			o.Uptime = func() time.Duration { return up }
			o.SystemdDir = systemd
		}
		if setup != nil {
			setup(e)
		}
	})
	e.create()
	e.withPageAddress()
	return e, https, plain
}

func closeAll(files []*os.File) {
	for _, f := range files {
		f.Close()
	}
}

// A server on a joined machine on the page at its name is the panel's to
// say, as the agent sees only its own servers' pages: with none of those on
// it, the agent hands over the ports for the joined one, but only while the
// machine has an address. That changes no setting and leaves no audit
// entry, so the agent logs it.
func TestThePortsOpenForAJoinedServersPageWithNoServerHereOnIt(t *testing.T) {
	var logs logBuffer
	e, https, _ := pageEnv(t, time.Hour, func(e *agentEnv) {
		e.cfg.Dev = false
		ports := e.tweak
		e.tweak = func(o *Options) {
			ports(o)
			o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		}
	})
	const handedOver = "the public page's ports go to the panel for a server on a joined machine"
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 2 || strings.Contains(logs.String(), handedOver) {
		t.Fatalf("for a server of its own on the page the agent handed over %d and logged:\n%s", len(files), logs.String())
	}
	if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"enabled": false, "actor": "admin"}); code != 200 {
		t.Fatal("turning the page off")
	}
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTPS.State != api.PortOff {
		t.Fatalf("with no server on the page the agent handed over %+v", ports)
	}
	joined := api.PagePortsRequest{HTTPS: true, HTTP: true, Joined: true}
	ports, files = e.a.takePagePorts(context.Background(), joined)
	closeAll(files)
	if len(files) != 2 || ports.HTTPS.State != api.PortOpen || ports.HTTPS.Port != https {
		t.Fatalf("for a joined server's page the agent handed over %d: %+v", len(files), ports)
	}
	if n := strings.Count(logs.String(), handedOver); n != 1 {
		t.Fatalf("the ports went to the panel for a joined server's page, logged %d times:\n%s", n, logs.String())
	}
	if err := e.a.updateAddress(func(st *addressState) { st.Kind, st.Host = api.AddressNone, "" }); err != nil {
		t.Fatal(err)
	}
	ports, files = e.a.takePagePorts(context.Background(), joined)
	closeAll(files)
	if len(files) != 0 || strings.Count(logs.String(), handedOver) != 1 {
		t.Fatalf("without an address the agent handed over %+v for a joined server's page, and logged:\n%s", ports, logs.String())
	}
}

// The fresh-install walkthrough of 1 Oct 2026: a machine without an address
// refused a browser given its bare IP address, which goes to port 80. The
// agent hands over port 80 alone for the panel to send it to the dashboard,
// only while the machine has no address, and names what claims the port so
// the panel can give it back; it never hands over a claimed one.
func TestPort80PointsAtTheDashboardOnlyWhileTheMachineHasNoAddress(t *testing.T) {
	var logs logBuffer
	e, _, plain := pageEnv(t, time.Hour, func(e *agentEnv) {
		e.cfg.Dev = false
		ports := e.tweak
		e.tweak = func(o *Options) {
			ports(o)
			o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		}
	})
	ctx := context.Background()
	pointer := api.PagePortsRequest{HTTPS: true, HTTP: true, Pointer: true}
	if code, _ := e.call("POST", e.sp("/public-page"), map[string]any{"enabled": false, "actor": "admin"}); code != 200 {
		t.Fatal("turning the page off")
	}
	ports, files := e.a.takePagePorts(ctx, pointer)
	closeAll(files)
	if len(files) != 0 || ports.HTTP.State != api.PortOff {
		t.Fatalf("with an address the agent handed over %+v for the pointer", ports)
	}

	if err := e.a.updateAddress(func(st *addressState) { st.Kind, st.Host = api.AddressNone, "" }); err != nil {
		t.Fatal(err)
	}
	ports, files = e.a.takePagePorts(ctx, api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 {
		t.Fatalf("without an address and not asked for the pointer, the agent handed over %+v", ports)
	}
	ports, files = e.a.takePagePorts(ctx, pointer)
	closeAll(files)
	if len(files) != 1 || ports.HTTP.State != api.PortOpen || ports.HTTP.Port != plain || ports.HTTPS.State != api.PortOff {
		t.Fatalf("for the pointer the agent handed over %d: %+v", len(files), ports)
	}
	if !strings.Contains(logs.String(), "port 80 goes to the panel to send browsers at the IP address to the dashboard") {
		t.Fatalf("the hand-over for the pointer wasn't logged:\n%s", logs.String())
	}

	if code, out := e.call("GET", "/v1/public-page/state", nil); code != 200 || out["httpClaimed"] != nil {
		t.Fatalf("with nothing wanting port 80 the state said %d %v", code, out)
	}
	wants := filepath.Join(e.a.opts.SystemdDir, "multi-user.target.wants")
	os.MkdirAll(wants, 0o755)
	os.Symlink("/lib/systemd/system/nginx.service", filepath.Join(wants, "nginx.service"))
	if code, out := e.call("GET", "/v1/public-page/state", nil); code != 200 || out["httpClaimed"] != "nginx" {
		t.Fatalf("with nginx set to start with the machine the state said %d %v", code, out)
	}
	ports, files = e.a.takePagePorts(ctx, pointer)
	closeAll(files)
	if len(files) != 0 || ports.HTTP.State != api.PortClaimed || ports.HTTP.Holder != "nginx" {
		t.Fatalf("with nginx set to start with the machine the agent handed over %d: %+v", len(files), ports)
	}
	e.withPageAddress()
	if code, out := e.call("GET", "/v1/public-page/state", nil); code != 200 || out["httpClaimed"] != nil {
		t.Fatalf("with an address the state named what claims port 80: %d %v", code, out)
	}
}

func TestThePageLeavesPortsToWhatStartsWithTheMachine(t *testing.T) {
	e, _, _ := pageEnv(t, 90*time.Second, nil)
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTPS.State != api.PortWaiting || ports.HTTP.State != api.PortWaiting {
		t.Fatalf("a machine up for 90 seconds handed over %d ports: %+v", len(files), ports)
	}
}

func TestThePageNeverTakesAPortSomethingElseUsesOrWillUse(t *testing.T) {
	blocker, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	busy := blocker.Addr().(*net.TCPAddr).Port
	var e *agentEnv
	var https, plain int
	e, https, plain = pageEnv(t, time.Hour, func(e *agentEnv) {
		e.portHolder = func(port int) (string, int, bool) { return "nginx", 4242, port == busy }
	})
	e.a.opts.PageHTTPSAddr = ":" + strconv.Itoa(busy)

	// A program listens on the HTTPS port; a stopped Docker container has the
	// HTTP port in its settings.
	e.fd.mu.Lock()
	e.fd.others = append(e.fd.others, fakeListed{name: "caddy-proxy", ports: []fakePort{{plain, "tcp"}}})
	e.fd.mu.Unlock()
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTPS.State != api.PortBusy || ports.HTTPS.Holder != "nginx" || ports.HTTP.State != api.PortClaimed || ports.HTTP.Holder != "caddy-proxy" {
		t.Fatalf("handed over %d ports: %+v", len(files), ports)
	}

	// The program stops: the port found busy isn't tried again, so a web
	// server restarting just then can't lose it, until the owner asks.
	blocker.Close()
	if ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true}); len(files) != 0 || ports.HTTPS.State != api.PortBusy {
		closeAll(files)
		t.Fatalf("a port found busy was tried again: %+v", ports)
	}
	if code, _ := e.call("POST", "/v1/public-page/ports/retry", map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("retry: %d", code)
	}
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true})
	closeAll(files)
	if len(files) != 1 || ports.HTTPS.State != api.PortOpen {
		t.Fatalf("after the owner asked, the free port: %+v (%d files)", ports, len(files))
	}

	// A web server set to start with the machine claims both ports, running
	// or not.
	e.fd.mu.Lock()
	e.fd.others = nil
	e.fd.mu.Unlock()
	e.a.opts.PageHTTPSAddr = ":" + strconv.Itoa(https)
	wants := filepath.Join(e.a.opts.SystemdDir, "multi-user.target.wants")
	os.MkdirAll(wants, 0o755)
	os.Symlink("/lib/systemd/system/apache2.service", filepath.Join(wants, "apache2.service"))
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTPS.State != api.PortClaimed || ports.HTTPS.Holder != "apache2" || ports.HTTP.Holder != "apache2" {
		t.Fatalf("with apache2 enabled: %+v (%d files)", ports, len(files))
	}
}

func TestThePagesPortsReachThePanelOnlyOverTheAgentSocket(t *testing.T) {
	e, https, plain := pageEnv(t, time.Hour, nil)
	// Over anything but the agent's own socket the route doesn't even try
	// the ports. They're held here meanwhile, so a try would find them busy
	// and remember that, and the hand-over below would fail.
	var held []net.Listener
	for _, p := range []int{https, plain} {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, ln)
	}
	req, _ := http.NewRequest("POST", e.ts.URL+pagePortsPath, strings.NewReader(`{"https":true,"http":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, ln := range held {
		ln.Close()
	}
	if resp.StatusCode != 400 {
		t.Fatalf("over TCP the hand-over answers %d", resp.StatusCode)
	}

	sock := filepath.Join(e.dir, "page.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e.a.Handler(), ConnContext: withConn}
	go srv.Serve(ln)
	defer srv.Close()
	ports, files, err := agentclient.New(sock).PublicPagePorts(context.Background(), api.PagePortsRequest{HTTPS: true, HTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer closeAll(files)
	if len(files) != 2 || ports.HTTPS.State != api.PortOpen || ports.HTTPS.Port != https || ports.HTTP.State != api.PortOpen || ports.HTTP.Port != plain {
		t.Fatalf("the panel got %d sockets: %+v", len(files), ports)
	}
	// The sockets listen on the ports, and the agent kept no copy: closing
	// the panel's frees the port.
	l, err := net.FileListener(files[0])
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(https))
	if err != nil {
		t.Fatalf("the handed-over socket doesn't listen: %v", err)
	}
	c.Close()
	l.Close()
	files[0].Close()
	if again, err := net.Listen("tcp", ":"+strconv.Itoa(https)); err != nil {
		t.Fatalf("the agent kept port %d open: %v", https, err)
	} else {
		again.Close()
	}
}

func TestLetsEncryptsChecksReachTheAgentThroughThePage(t *testing.T) {
	e := newAgentEnv(t)
	if code, _ := e.call("GET", "/v1/acme-challenge/tok3n", nil); code != 404 {
		t.Fatalf("no check pending: %d", code)
	}
	e.a.http01.Addr = ""
	release, err := e.a.http01.Present("tok3n", "tok3n.key")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(e.ts.URL + "/v1/acme-challenge/tok3n")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "tok3n.key" {
		t.Fatalf("a pending check: %d %q", resp.StatusCode, b)
	}
	release()
	if code, _ := e.call("GET", "/v1/acme-challenge/tok3n", nil); code != 404 {
		t.Fatalf("a check released: %d", code)
	}
}

func TestAServerTurnedOffLeavesThePageAndTheOthersStay(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival"})
	first := e.status()
	e.createWith(map[string]any{"name": "Creative"})
	second := e.status()
	e.withPageAddress()
	if code, p, _ := e.page(pageTestHost); code != 200 || len(p.Servers) != 2 {
		t.Fatalf("with both on: %d %+v", code, p)
	}
	if code, out := e.call("POST", "/v1/servers/"+second.ID+"/public-page", map[string]any{"enabled": false, "actor": "admin"}); code != 200 || out["enabled"] != false {
		t.Fatalf("turning Creative off: %d %v", code, out)
	}
	code, p, raw := e.page(pageTestHost)
	if code != 200 || len(p.Servers) != 1 || p.Servers[0].Slug != first.Slug || strings.Contains(raw, "Creative") || strings.Contains(raw, second.Slug) {
		t.Fatalf("with Creative off the page shows %d %s", code, raw)
	}
	if p.Servers[0].Address != pageTestHost {
		t.Fatalf("the server on 25565 joins at %q, want the bare address", p.Servers[0].Address)
	}
	if code, _ := e.call("GET", "/v1/public-page/icons/"+second.Slug+"?host="+pageTestHost, nil); code != 404 {
		t.Fatalf("the icon of a server off the page: %d", code)
	}
}

// Let's Encrypt's check for an own domain has the agent listen on port 80
// for a few seconds. A hand-over in that time leaves the port for the next
// look instead of counting it as busy for good.
func TestAnHTTP01CheckDoesntMakePort80Busy(t *testing.T) {
	e, _, plain := pageEnv(t, time.Hour, nil)
	e.a.http01.Addr = ":" + strconv.Itoa(plain)
	e.a.opts.HTTP01Addr = e.a.http01.Addr
	release, err := e.a.http01.Present("tok3n", "tok3n.key")
	if err != nil {
		t.Fatal(err)
	}
	ports, files := e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTP: true})
	closeAll(files)
	if len(files) != 0 || ports.HTTP.State != api.PortWaiting {
		t.Fatalf("during the check the page got %+v (%d files)", ports.HTTP, len(files))
	}
	release()
	ports, files = e.a.takePagePorts(context.Background(), api.PagePortsRequest{HTTP: true})
	closeAll(files)
	if len(files) != 1 || ports.HTTP.State != api.PortOpen {
		t.Fatalf("after the check the page got %+v (%d files)", ports.HTTP, len(files))
	}
}
