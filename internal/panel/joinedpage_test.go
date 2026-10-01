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
	"unicode/utf8"

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
	j.ra.reply("GET /v1/servers/"+cobblemonID+"/public-page", `{"enabled":true,"players":true,"about":"","stream":"","host":"home-server.example.com"}`)
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

// Whether a joined server is on the page rests on the dashboard's word as
// well as its machine's: once the owner turns its page off through the
// dashboard, its name gets the page's one 404 and its Settings say it's
// off, whatever the machine says, until the owner turns it on again.
func TestAJoinedServersPageIsOnOnlyWhileTheDashboardSetItSo(t *testing.T) {
	j := newJoinedPage(t)
	h := j.e.srv.pageHandler(true)
	path := "/api/servers/" + cobblemonID + "/public-page"
	j.ra.reply("POST /v1/servers/"+cobblemonID+"/public-page", `{"enabled":true,"players":false,"about":"","stream":""}`)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 200 {
		t.Fatalf("the page before: %d", resp.StatusCode)
	}
	if r := j.e.do(t, "POST", path, `{"enabled":false}`, auth(j.cookie, j.csrf)); r.status != 200 || r.body["enabled"] != false {
		t.Fatalf("turning the page off: %d %v", r.status, r.body)
	}
	_, unknown := pageGet(t, h, "GET", "other.beta.playkeeper.me", "/")
	if resp, body := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 || body != unknown {
		t.Fatalf("with the page turned off through the dashboard and the machine still showing it: %d %q", resp.StatusCode, body)
	}
	if r := j.e.do(t, "GET", path, "", auth(j.cookie, j.csrf)); r.status != 200 || r.body["enabled"] != false {
		t.Fatalf("the Settings of a page the dashboard turned off: %d %v", r.status, r.body)
	}
	if r := j.e.do(t, "POST", path, `{"enabled":true}`, auth(j.cookie, j.csrf)); r.status != 200 || r.body["enabled"] != true {
		t.Fatalf("turning the page on: %d %v", r.status, r.body)
	}
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 200 {
		t.Fatalf("the page turned on again: %d", resp.StatusCode)
	}
}

// For a joined server whose page the dashboard never set, it takes its
// machine's word once, and keeps that: a machine that later puts the
// server on the page by itself doesn't.
func TestAJoinedServerNeverSetTakesItsMachinesWordOnce(t *testing.T) {
	j := newJoinedPage(t)
	settings := "GET /v1/servers/" + cobblemonID + "/public-page"
	j.ra.reply(settings, `{"enabled":false,"players":false,"about":"","stream":""}`)
	h := j.e.srv.pageHandler(true)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 {
		t.Fatalf("a server its machine has off the page: %d", resp.StatusCode)
	}
	j.ra.reply(settings, `{"enabled":true,"players":false,"about":"","stream":""}`)
	j.e.clock.add(pageCacheFor + time.Second)
	if resp, _ := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 {
		t.Fatalf("a server its machine put on the page by itself: %d", resp.StatusCode)
	}
	if on, known := j.e.srv.pageRecord(cobblemonID); on || !known {
		t.Fatalf("the dashboard's record: on %v, known %v", on, known)
	}
}

// A move gives the dashboard the page's switch a server had where it was,
// unless the dashboard set it itself.
func TestAMoveKeepsWhetherAServerWasOnThePage(t *testing.T) {
	j := newJoinedPage(t)
	ctx := context.Background()
	local, err := j.e.srv.localMachine()
	if err != nil {
		t.Fatal(err)
	}
	list, err := j.e.srv.machines()
	if err != nil {
		t.Fatal(err)
	}
	var remote machine
	for _, m := range list {
		if m.ID == j.machineID {
			remote = m
		}
	}
	move := func(id, state string) {
		t.Helper()
		j.e.reply("GET", "/v1/servers/"+id+"/move-state", state)
		j.ra.reply("PUT /v1/servers/"+id+"/move-state", `{}`)
		if err := j.e.srv.copyMoveState(ctx, id, local, remote); err != nil {
			t.Fatal(err)
		}
	}
	move("abcdefghjk", `{"rows":{"servers":[{"public_page":0,"sleep":""}]}}`)
	if on, known := j.e.srv.pageRecord("abcdefghjk"); on || !known {
		t.Fatalf("a server moved off the page: on %v, known %v", on, known)
	}
	j.e.srv.setPageRecord("survzzzzz2", false)
	move("survzzzzz2", `{"rows":{"servers":[{"public_page":1,"sleep":""}]}}`)
	if on, known := j.e.srv.pageRecord("survzzzzz2"); on || !known {
		t.Fatalf("a move overrode what the dashboard set: on %v, known %v", on, known)
	}
}

// A joined machine's answer reaches the page only as the agent itself would
// let it: names, description, About, board and lines within their bounds
// and without control characters, a type the page knows, a next session
// within a month, and no more players named than are playing.
func TestAJoinedMachinesTextIsHeldToWhatTheAgentAllows(t *testing.T) {
	j := newJoinedPage(t)
	next := j.e.clock.now().Add(365 * 24 * time.Hour)
	odd := *j.shown.Load()
	odd.Name = "\u202e" + strings.Repeat("Ä", 40)
	odd.MOTD = strings.Repeat("m", 100) + "§c"
	odd.About = strings.Repeat("line\n", 30) + strings.Repeat("a", 1000)
	odd.Type = "bukkit"
	odd.MinecraftVersion = strings.Repeat("1", 200)
	odd.Modpack = &api.PublicModpack{Name: strings.Repeat("p", 300), Version: "1.0"}
	odd.Players = &api.PublicPlayers{Online: 1, Max: 20, Names: []string{"mara_k", "tobi2009"}}
	odd.Board = &api.PublicBoard{Headline: strings.Repeat("h", 200), Next: &next}
	for range 10 {
		odd.Board.Stats = append(odd.Board.Stats, api.BoardStat{Label: strings.Repeat("l", 50), Value: strings.Repeat("v", 50)})
	}
	for range 30 {
		odd.Board.Checklist = append(odd.Board.Checklist, api.BoardItem{Label: strings.Repeat("c", 80)})
	}
	j.shown.Store(&odd)
	_, raw := pageGet(t, j.e.srv.pageHandler(true), "GET", cobblemonName, "/api/public/server-page")
	var page api.PublicPage
	if json.Unmarshal([]byte(raw), &page) != nil || len(page.Servers) != 1 {
		t.Fatalf("the page: %.200s", raw)
	}
	sv := page.Servers[0]
	runes := utf8.RuneCountInString
	if runes(sv.Name) > api.ServerNameMax || strings.ContainsRune(sv.Name, '\u202e') || runes(sv.MOTD) > api.ServerMOTDMax || strings.ContainsRune(sv.MOTD, '§') {
		t.Errorf("the name %q and description %q", sv.Name, sv.MOTD)
	}
	if runes(sv.About) > api.PublicAboutMax || strings.Count(sv.About, "\n")+1 > api.PublicAboutLines {
		t.Errorf("About of %d characters on %d lines", runes(sv.About), strings.Count(sv.About, "\n")+1)
	}
	if sv.Type != "" || runes(sv.MinecraftVersion) > pageLineMax || sv.Modpack == nil || runes(sv.Modpack.Name) > pageLineMax {
		t.Errorf("the type %q, version of %d characters and modpack %+v", sv.Type, runes(sv.MinecraftVersion), sv.Modpack)
	}
	if sv.Players == nil || !slices.Equal(sv.Players.Names, []string{"mara_k"}) {
		t.Errorf("the players: %+v", sv.Players)
	}
	b := sv.Board
	if b == nil || runes(b.Headline) > api.BoardHeadlineMax || b.Next != nil || len(b.Stats) > api.BoardStatsMax || len(b.Checklist) > api.BoardItemsMax {
		t.Fatalf("the board: %+v", b)
	}
	for _, st := range b.Stats {
		if runes(st.Label) > api.BoardStatLabelMax || runes(st.Value) > api.BoardStatValueMax {
			t.Errorf("a number: %+v", st)
		}
	}
	for _, it := range b.Checklist {
		if runes(it.Label) > api.BoardItemLabelMax {
			t.Errorf("a checklist item: %+v", it)
		}
	}
}

// A joined machine whose text would make a slow share card holds up no page
// of the dashboard's own: the text is bounded, and its card is drawn among
// its own machine's answers, not the dashboard's own agent's.
func TestAJoinedCardHoldsUpNoOtherPage(t *testing.T) {
	j := newJoinedPage(t)
	long := *j.shown.Load()
	long.Name = strings.Repeat("a", 16000)
	long.Board = &api.PublicBoard{Headline: strings.Repeat("b", 16000)}
	j.shown.Store(&long)
	h := j.e.srv.pageHandler(true)
	release := make(chan struct{})
	j.e.agent.mu.Lock()
	j.e.agent.gates["GET /v1/public-page"] = release
	j.e.agent.mu.Unlock()
	t.Cleanup(func() { close(release) })
	go j.e.srv.pageData(context.Background(), "beta.playkeeper.me")
	eventually(t, "the dashboard's own agent to be asked about its page", func() bool { return j.e.sawLocally("GET /v1/public-page") })
	done := make(chan int, 1)
	go func() {
		resp, _ := pageGet(t, h, "GET", cobblemonName, "/api/public/server-page/card.png")
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatalf("the joined server's card: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the joined server's card waited on the dashboard's own agent")
	}
	start := time.Now()
	j.e.srv.joinedAnswers(j.machineID).get(context.Background(), j.e.srv.now, "page other", func(context.Context) (pageAnswer, bool) { return pageAnswer{}, false })
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("the joined machine's answers waited %v after its card", waited)
	}
}

// A copy a move left on the dashboard's machine keeps the server's name
// there until it's deleted, while the zone sends the name to the machine
// the server moved to: the page answers as the zone does.
func TestTheZoneAnswersAheadOfACopyAMoveLeft(t *testing.T) {
	j := newJoinedPage(t)
	j.e.srv.page.mu.Lock()
	j.e.srv.page.hosts = []string{cobblemonName}
	j.e.srv.page.mu.Unlock()
	h := j.e.srv.pageHandler(true)
	if resp, body := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 200 || !strings.Contains(body, "<title>Cobblemon · Minecraft server</title>") {
		t.Fatalf("the name while a copy on the dashboard's machine has it: %d %s", resp.StatusCode, body)
	}
	j.on.Store(false)
	j.e.clock.add(pageCacheFor + time.Second)
	_, unknown := pageGet(t, h, "GET", "other.beta.playkeeper.me", "/")
	if resp, body := pageGet(t, h, "GET", cobblemonName, "/"); resp.StatusCode != 404 || body != unknown {
		t.Fatalf("the name off the page while a copy has it: %d %q", resp.StatusCode, body)
	}
}

// The port keeper asks a joined machine about its servers only until the
// look's deadline: a machine slow to answer holds it up no longer.
func TestTheKeepersAsksEndWithItsDeadline(t *testing.T) {
	j := newJoinedPage(t)
	var listed []map[string]any
	var named []zoneServer
	for i, id := range []string{cobblemonID, "rstuvwxyza", "rstuvwxyzb"} {
		slug := []string{"cobblemon", "sky", "skyblock"}[i]
		listed = append(listed, map[string]any{"id": id, "name": slug, "slug": slug, "phase": "online", "gamePort": 25566 + i})
		named = append(named, zoneServer{id: id, label: slug, machineID: j.machineID})
		j.ra.reply("GET /v1/servers/"+id+"/public-page", `{"enabled":true,"players":false,"about":"","stream":""}`)
		j.ra.handle("GET /v1/servers/"+id+"/public-page/shown", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(time.Second)
			writeErr(w, http.StatusNotFound, api.CodeNotFound, "There's no server page here.", "")
		})
	}
	b, _ := json.Marshal(listed)
	j.ra.reply("GET /v1/servers", string(b))
	var servers []map[string]any
	j.e.get(t, "/api/servers", j.cookie, &servers)
	j.e.srv.setZoneAddresses(named, "beta.playkeeper.me", true)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if j.e.srv.anyJoinedPageOn(ctx) {
		t.Fatal("a joined page is on")
	}
	if took := time.Since(start); took > 700*time.Millisecond {
		t.Fatalf("a look with a 300 ms deadline waited %v on a slow machine's three servers", took)
	}
	over, stop := context.WithCancel(context.Background())
	stop()
	start = time.Now()
	j.e.srv.anyJoinedPageOn(over)
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Fatalf("a look past its deadline waited %v", took)
	}
}
