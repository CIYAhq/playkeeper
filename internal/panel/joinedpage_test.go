package panel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/webmap"
)

const (
	cobblemonID   = "rstuvwxyzq"
	cobblemonName = "cobblemon.beta.playkeeper.me"
)

// joinedPage is a dashboard at beta.playkeeper.me with a joined machine,
// home-server, whose server Cobblemon the zone names cobblemonName. The
// machine says the page shows Cobblemon while on is set, and names its own
// address, where players join Bedrock and links under its own name as well:
// what a machine that is lying, or has a name of its own, would say.
type joinedPage struct {
	e             *env
	ra            *remoteAgent
	machineID     string
	cookie, csrf  string
	on            atomic.Bool
	shown         atomic.Pointer[api.PublicServerShown]
	mapToken      string
	otherPackLink string
}

func newJoinedPage(t *testing.T) *joinedPage {
	t.Helper()
	e := newEnvConfig(t, withDomain, nil)
	cookie, csrf := e.setup(t)
	e.srv.static = fstest.MapFS{
		"index.html":           {Data: []byte(indexPage)},
		"assets/index-a1b2.js": {Data: []byte("console.log(1)")},
	}
	e.reply("GET", "/v1/servers", `[]`)
	e.reply("GET", "/v1/address", `{"kind":"own","host":"beta.playkeeper.me","certificate":{"names":["beta.playkeeper.me"],"challenge":"dns-01","notAfter":"2099-01-01T00:00:00Z"},"dashboard":{"on":true,"state":"open","port":443}}`)
	j := &joinedPage{e: e, ra: newRemoteAgent(), cookie: cookie, csrf: csrf, mapToken: webmap.NewShareToken()}
	j.on.Store(true)
	packToken, err := share.NewToken(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	j.otherPackLink = packToken
	j.shown.Store(&api.PublicServerShown{
		PublicServer: api.PublicServer{Slug: "cobblemon-2", Name: "Cobblemon", MOTD: "Catch them all", Address: "65.108.10.20:25566", State: api.PublicOnline,
			Players: &api.PublicPlayers{Online: 2, Max: 20, Names: []string{"mara_k", "not a name!"}}, MinecraftVersion: "1.21.1", Type: "fabric", HasIcon: true,
			Bedrock: &api.BedrockJoin{Host: "65.108.10.20", Port: 19133}, Map: "https://home-server.example.com:8443/map/x", Pack: "https://home-server.example.com:8443/packs/y",
			Stream: &api.PublicStream{Site: "twitch", Channel: "example_channel", URL: "https://evil.example/watch"}},
		MapToken: j.mapToken, PackToken: packToken,
	})
	j.ra.reply("GET /v1/servers", `[{"id":"`+cobblemonID+`","name":"Cobblemon","slug":"cobblemon","phase":"online","gamePort":25566}]`)
	j.ra.handle("GET /v1/servers/"+cobblemonID+"/public-page/shown", func(w http.ResponseWriter, r *http.Request) {
		if !j.on.Load() {
			writeErr(w, http.StatusNotFound, api.CodeNotFound, "There's no server page here.", "")
			return
		}
		writeJSON(w, http.StatusOK, j.shown.Load())
	})
	j.ra.handle("GET /v1/servers/"+cobblemonID+"/icon", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(tilePNG)
	})
	d, _, _ := e.joinedAs(t, cookie, csrf, j.ra)
	j.machineID = d.MachineID
	var servers []map[string]any
	e.get(t, "/api/servers", cookie, &servers)
	m := machine{ID: j.machineID}
	e.srv.recordLink(mapLink, j.mapToken, cobblemonID, m)
	e.srv.recordLink(packLink, packToken, "otherservr", m)
	e.srv.setZoneAddresses([]zoneServer{{id: cobblemonID, label: "cobblemon", machineID: j.machineID}}, "beta.playkeeper.me", true)
	return j
}

// leaks are what the page must never show of Cobblemon's machine, or of
// what the machine said beyond what the page shows.
func (j *joinedPage) leaks() []string {
	return []string{"65.108.10.20", "home-server", j.machineID, "19133", "evil.example", "cobblemon-2", j.otherPackLink}
}

// A server on a joined machine has its page at the name the zone gives it,
// served by the dashboard's machine from what the server's machine says the
// page shows: that server alone, with the dashboard's address and slug for
// it, no Bedrock address, only the links the dashboard saw that machine
// make for it, under the dashboard's own address, and nothing that says
// which machine runs it.
func TestAJoinedServersNameOpensItsPageAndNothingOfItsMachine(t *testing.T) {
	j := newJoinedPage(t)
	h := j.e.srv.securityHeaders(j.e.srv.pageHandler(true))
	resp, body := pageGet(t, h, "GET", "Cobblemon.Beta.Playkeeper.me", "/")
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>Cobblemon · Minecraft server</title>") || !strings.Contains(body, "Join at "+cobblemonName) {
		t.Fatalf("the page at the server's name: %d %s", resp.StatusCode, body)
	}
	resp, raw := pageGet(t, h, "GET", cobblemonName, "/api/public/server-page")
	var page api.PublicPage
	if resp.StatusCode != 200 || json.Unmarshal([]byte(raw), &page) != nil || page.Address != cobblemonName || len(page.Servers) != 1 {
		t.Fatalf("the page's data: %d %s", resp.StatusCode, raw)
	}
	sv := page.Servers[0]
	if sv.Slug != "cobblemon" || sv.Address != cobblemonName || sv.Bedrock != nil || sv.Name != "Cobblemon" || sv.MOTD != "Catch them all" {
		t.Fatalf("the server on its page: %s", raw)
	}
	if sv.Map != "https://beta.playkeeper.me/map/"+j.mapToken || sv.Pack != "" {
		t.Fatalf("the page's links: map %q, pack %q", sv.Map, sv.Pack)
	}
	if sv.Players == nil || !slices.Equal(sv.Players.Names, []string{"mara_k"}) || sv.Stream != nil {
		t.Fatalf("the page's players and stream: %s", raw)
	}
	for _, leak := range j.leaks() {
		if strings.Contains(body, leak) || strings.Contains(raw, leak) {
			t.Errorf("the page holds %q: %s", leak, raw)
		}
	}
	if resp, icon := pageGet(t, h, "GET", cobblemonName, "/api/public/server-page/icons/cobblemon"); resp.StatusCode != 200 || icon != string(tilePNG) {
		t.Fatalf("the server's icon: %d %q", resp.StatusCode, icon)
	}
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/api/public/server-page/icons/cobblemon-2"); resp.StatusCode != 404 {
		t.Fatalf("the icon under the machine's slug: %d", resp.StatusCode)
	}

	// A state the page doesn't know reads as offline, with nobody playing.
	odd := *j.shown.Load()
	odd.State = "on fire"
	j.shown.Store(&odd)
	j.e.clock.add(pageCacheFor + time.Second)
	_, raw = pageGet(t, h, "GET", cobblemonName, "/api/public/server-page")
	var offline api.PublicPage
	if json.Unmarshal([]byte(raw), &offline) != nil || len(offline.Servers) != 1 || offline.Servers[0].State != api.PublicOffline || offline.Servers[0].Players != nil {
		t.Fatalf("a state the page doesn't know: %s", raw)
	}

	// Any other name, the machine's own included, gets the page's one 404.
	_, unknown := pageGet(t, h, "GET", "other.beta.playkeeper.me", "/")
	for _, host := range []string{j.machineID + ".m.beta.playkeeper.me", cobblemonName + ".evil.test", "beta.playkeeper.me"} {
		if resp, body := pageGet(t, h, "GET", host, "/"); resp.StatusCode != 404 || body != unknown {
			t.Errorf("%s: %d %q", host, resp.StatusCode, body)
		}
	}

	// Off the page, the name answers like one nobody has, assets included.
	j.on.Store(false)
	j.e.clock.add(pageCacheFor + time.Second)
	for _, path := range []string{"/", "/api/public/server-page", "/api/public/server-page/icons/cobblemon", "/assets/index-a1b2.js"} {
		if resp, body := pageGet(t, h, "GET", cobblemonName, path); resp.StatusCode != 404 || path == "/" && body != unknown {
			t.Errorf("%s off the page: %d %q", path, resp.StatusCode, body)
		}
	}
}

// The page asks only the machine the dashboard knows runs the server: a
// name the zone gives it while it's on another machine shows nothing.
func TestAJoinedServersPageAsksOnlyTheMachineThatRunsIt(t *testing.T) {
	j := newJoinedPage(t)
	h := j.e.srv.pageHandler(true)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 200 {
		t.Fatalf("the page on its machine: %d", resp.StatusCode)
	}
	j.e.srv.setZoneAddresses([]zoneServer{{id: cobblemonID, label: "cobblemon", machineID: "zzzzzzzzzz"}}, "beta.playkeeper.me", true)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 {
		t.Fatalf("the page with the zone naming another machine: %d", resp.StatusCode)
	}
}

// Holding a joined server's page answer too long would show a page that is
// off: a changed zone forgets the page's answers and asks the port keeper
// to look again, and an unchanged one does neither.
func TestAChangedZoneForgetsThePageAndLooksAgain(t *testing.T) {
	j := newJoinedPage(t)
	h := j.e.srv.pageHandler(true)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 200 {
		t.Fatalf("the page: %d", resp.StatusCode)
	}
	select {
	case <-j.e.srv.page.kick:
	default:
	}
	named := []zoneServer{{id: cobblemonID, label: "cobblemon", machineID: j.machineID}}
	j.e.srv.setZoneAddresses(named, "beta.playkeeper.me", true)
	if len(j.e.srv.page.kick) != 0 {
		t.Fatal("an unchanged zone asked the keeper to look again")
	}
	j.on.Store(false)
	j.e.srv.setZoneAddresses(append(named, zoneServer{id: "abcdefghjk", label: "survival", machineID: j.machineID}), "beta.playkeeper.me", true)
	if len(j.e.srv.page.kick) != 1 {
		t.Fatal("a changed zone didn't ask the keeper to look again")
	}
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 {
		t.Fatalf("after the zone changed, a page that went off: %d", resp.StatusCode)
	}
}

// The keeper holds ports 443 and 80 while a server on a joined machine is
// on the page at its name, though none on the dashboard's machine is, and
// gives them back once it's off.
func TestTheKeeperHoldsThePortsWhileAJoinedServersPageIsOn(t *testing.T) {
	j := newJoinedPage(t)
	j.e.reply("GET", "/v1/public-page/state", `{"host":"beta.playkeeper.me","on":false}`)
	asks := func() int {
		j.e.agent.mu.Lock()
		defer j.e.agent.mu.Unlock()
		n := 0
		for _, k := range j.e.agent.hits {
			if k == "POST /v1/public-page/ports" {
				n++
			}
		}
		return n
	}
	ctx := context.Background()
	j.e.srv.lookAtPage(ctx)
	if asks() != 1 {
		t.Fatalf("with a joined server on the page the keeper asked for the ports %d times", asks())
	}
	j.on.Store(false)
	j.e.clock.add(pageCacheFor + time.Second)
	j.e.srv.lookAtPage(ctx)
	if n, st := asks(), j.e.srv.page.portsNow(); n != 1 || st.HTTPS.State != api.PortOff || st.HTTP.State != api.PortOff {
		t.Fatalf("with it off the keeper asked %d times and the ports are %+v", n, st)
	}
}

// A joined server's Settings give its page's address, the name the zone
// gives it, and the dashboard's ports, never an address its machine has of
// its own; its ports can be tried again there too.
func TestAJoinedServersSettingsGiveItsPageAtItsName(t *testing.T) {
	j := newJoinedPage(t)
	j.ra.reply("GET /v1/servers/"+cobblemonID+"/public-page", `{"enabled":true,"players":false,"about":"","stream":"","host":"home-server.example.com"}`)
	path := "/api/servers/" + cobblemonID + "/public-page"
	r := j.e.do(t, "GET", path, "", auth(j.cookie, j.csrf))
	if r.status != 200 || r.body["host"] != cobblemonName || r.body["ports"] == nil {
		t.Fatalf("the page's Settings: %d %v", r.status, r.body)
	}
	j.e.srv.setZoneAddresses(nil, "", false)
	r = j.e.do(t, "GET", path, "", auth(j.cookie, j.csrf))
	if r.status != 200 || r.body["host"] != nil || r.body["ports"] == nil {
		t.Fatalf("the page's Settings without a name: %d %v", r.status, r.body)
	}
	if r := j.e.do(t, "POST", path+"/retry", `{}`, auth(j.cookie, j.csrf)); r.status != 200 || !j.e.sawLocally("POST /v1/public-page/ports/retry") {
		t.Fatalf("trying the ports again: %d %v", r.status, r.body)
	}
}
