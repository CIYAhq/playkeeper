package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
)

const (
	hetznerTestToken  = "Hz0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXyz"
	hetznerOtherToken = "Zz0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWX99"
)

// fakeHetzner answers GET /server_types like Hetzner Cloud's API for two
// tokens, offering each of stockTypes in Falkenstein, Nuremberg and
// Helsinki, with stock a test changes.
type fakeHetzner struct {
	mu  sync.Mutex
	srv *httptest.Server
	// stock is whether each location has the type now.
	stock map[string]bool
	// refused refuses every token, down answers 503, and resetAt, when set,
	// answers 429 with it as the rate limit's reset.
	refused, down bool
	resetAt       time.Time
	asked         int
	tokens        map[string]bool
	// servers are the project's servers, as GET /servers lists them, and
	// listed how many times it did.
	servers []map[string]any
	listed  int
}

func newFakeHetzner(t *testing.T) *fakeHetzner {
	t.Helper()
	f := &fakeHetzner{stock: map[string]bool{"fsn1": false, "nbg1": false, "hel1": false}, tokens: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHetzner) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.tokens[token] = true
	w.Header().Set("Content-Type", "application/json")
	switch {
	case f.refused || token != hetznerTestToken && token != hetznerOtherToken:
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"unauthorized","message":"unable to authenticate"}}`)
		return
	case !f.resetAt.IsZero():
		w.Header().Set("RateLimit-Reset", fmt.Sprint(f.resetAt.Unix()))
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"code":"rate_limit_exceeded","message":"limit reached"}}`)
		return
	case f.down:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case r.Method == http.MethodGet && r.URL.Path == "/servers":
		f.listed++
		json.NewEncoder(w).Encode(map[string]any{"servers": f.servers, "meta": map[string]any{"pagination": map[string]any{"page": 1, "next_page": nil}}})
		return
	case r.Method != http.MethodGet || r.URL.Path != "/server_types":
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
		return
	}
	name := r.URL.Query().Get("name")
	if !slices.Contains(stockTypes, name) {
		io.WriteString(w, `{"server_types":[],"meta":{}}`)
		return
	}
	var locations []map[string]any
	for i, l := range []string{"fsn1", "nbg1", "hel1"} {
		locations = append(locations, map[string]any{"id": i + 1, "name": l, "available": f.stock[l], "recommended": false, "deprecation": nil})
	}
	json.NewEncoder(w).Encode(map[string]any{"server_types": []map[string]any{{"id": 117, "name": name, "description": strings.ToUpper(name),
		"cores": 16, "memory": 32.0, "disk": 320, "locations": locations}}})
}

func (f *fakeHetzner) set(stock map[string]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for l, in := range stock {
		f.stock[l] = in
	}
}

func (f *fakeHetzner) change(fn func(f *fakeHetzner)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeHetzner) questions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked
}

func newHetznerEnv(t *testing.T, f *fakeHetzner) *env {
	t.Helper()
	return newEnvConfig(t, func(c *config.Config) { c.HetznerAPIURL = f.srv.URL }, nil)
}

func (e *env) hetznerView(t *testing.T, m member) hetznerView {
	t.Helper()
	var v hetznerView
	if st := e.get(t, "/api/hetzner", m.cookie, &v); st != http.StatusOK {
		t.Fatalf("GET /api/hetzner: %d", st)
	}
	return v
}

// inStockPosts are the locations of each machines-in-stock alert the panel
// gave the agent, in order, as "cx53: fsn1 hel1".
func (e *env) inStockPosts() []string {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	var out []string
	for _, r := range e.agent.reqs {
		if r.method != http.MethodPost || r.path != "/v1/discord/notify" || r.body["kind"] != "in_stock" {
			continue
		}
		var locs []string
		for _, l := range r.body["locations"].([]any) {
			locs = append(locs, l.(string))
		}
		out = append(out, fmt.Sprintf("%v: %s", r.body["serverType"], strings.Join(locs, " ")))
	}
	return out
}

func TestHetznerStockIsTheOwnersAlone(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	lena := addAdmin(t, e, "lena", "*")
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/hetzner", ""}, {"PUT", "/api/hetzner", `{"token":"` + hetznerTestToken + `"}`}, {"DELETE", "/api/hetzner", ""},
	} {
		if r := e.do(t, rt.method, rt.path, rt.body, lena.auth()); r.status != http.StatusForbidden {
			t.Errorf("an admin of every server: %s %s = %d %v", rt.method, rt.path, r.status, r.body)
		}
	}
	if f.questions() != 0 {
		t.Fatalf("Hetzner was asked for a non-owner: %v", f.tokens)
	}
	var me struct {
		Access struct {
			Can []string `json:"can"`
		} `json:"access"`
	}
	e.get(t, "/api/auth/me", own.cookie, &me)
	if !slices.Contains(me.Access.Can, string(actWatchStock)) {
		t.Fatalf("the owner's rights: %v", me.Access.Can)
	}
	e.get(t, "/api/auth/me", lena.cookie, &me)
	if slices.Contains(me.Access.Can, string(actWatchStock)) {
		t.Fatalf("an admin's rights: %v", me.Access.Can)
	}
}

func TestWatchingNeedsATokenHetznerTakes(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	v := e.hetznerView(t, own)
	if v.Connected || v.ServerType != "cx53" || !slices.Equal(v.Types, stockTypes) || len(v.Places) != 0 || v.TokenEnding != "" {
		t.Fatalf("before watching: %+v", v)
	}
	for _, c := range []struct{ name, body, code string }{
		{"no token", `{"serverType":"cx53"}`, "hetzner_token_refused"},
		{"not a token", `{"token":"hcloud_abc"}`, "hetzner_token_refused"},
		{"a type not offered", `{"token":"` + hetznerTestToken + `","serverType":"ccx63"}`, "invalid_request"},
	} {
		if r := e.do(t, "PUT", "/api/hetzner", c.body, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != c.code {
			t.Errorf("%s: %d %v", c.name, r.status, r.body)
		}
	}
	if f.questions() != 0 {
		t.Fatal("Hetzner was asked about a request that can't work")
	}
	f.change(func(f *fakeHetzner) { f.refused = true })
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != "hetzner_token_refused" {
		t.Fatalf("a token Hetzner refuses: %d %v", r.status, r.body)
	}
	if rows := e.auditRows(t, "hetzner.watch"); len(rows) != 1 || !strings.Contains(rows[0], "refused Hetzner refused the token") {
		t.Fatalf("audit: %v", rows)
	}
	if v := e.hetznerView(t, own); v.Connected {
		t.Fatal("a refused token was kept")
	}
	f.change(func(f *fakeHetzner) { f.refused, f.down = false, true })
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusBadGateway || r.body["code"] != "upstream_unavailable" {
		t.Fatalf("Hetzner down: %d %v", r.status, r.body)
	}
	if len(e.inStockPosts()) != 0 {
		t.Fatalf("posted: %v", e.inStockPosts())
	}
}

func TestWatchingPostsWhatsInStockNowAndKeepsTheTokenToItself(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	f.set(map[string]bool{"fsn1": true})
	r := e.do(t, "PUT", "/api/hetzner", `{"token":"  `+hetznerTestToken+`  ","serverType":""}`, own.auth())
	if r.status != http.StatusOK || r.body["connected"] != true || r.body["tokenEnding"] != "WXyz" || r.body["serverType"] != "cx53" {
		t.Fatalf("watch: %d %v", r.status, r.body)
	}
	if strings.Contains(mustJSON(t, r.body), hetznerTestToken) {
		t.Fatal("the answer names the token")
	}
	if got := e.inStockPosts(); !slices.Equal(got, []string{"cx53: fsn1"}) {
		t.Fatalf("posted %v, want what's in stock at once", got)
	}
	if e.hetznerView(t, own).Discord {
		t.Fatal("Discord shows connected while the agent says it isn't")
	}
	e.replyStatus("GET", "/v1/discord", 200, `{"connected":true,"alerts":[],"liveStatus":false,"delivery":{},"kinds":[]}`)
	v := e.hetznerView(t, own)
	if v.SetBy != "admin" || v.CheckedAt == nil || v.Problem != "" || len(v.Places) != 3 || !v.Discord {
		t.Fatalf("watching: %+v", v)
	}
	fsn, nbg := v.Places[0], v.Places[1]
	if fsn.Location != "fsn1" || fsn.City != "Falkenstein" || !fsn.Available || fsn.BuyURL != "https://console.hetzner.com/create/server?location=fsn1&type=cx53&useIPv4=true" ||
		nbg.City != "Nuremberg" || nbg.Available || nbg.BuyURL != "" || v.Places[2].City != "Helsinki" {
		t.Fatalf("places: %+v", v.Places)
	}
	var stored string
	e.srv.db.QueryRow(`SELECT token FROM hetzner_watch`).Scan(&stored)
	if stored != hetznerTestToken {
		t.Fatalf("stored token %q", stored)
	}
	rows := e.auditRows(t, "hetzner.watch")
	if len(rows) != 1 || !strings.Contains(rows[0], "admin cx53 succeeded token ending WXyz, watching cx53") || strings.Contains(rows[0], hetznerTestToken) {
		t.Fatalf("audit: %v", rows)
	}
	if strings.Contains(e.logs.String(), hetznerTestToken) {
		t.Fatal("the log names the token")
	}
}

func TestTheWatchPostsAPlaceAgainOnlyAfterItWasGoneHalfAnHour(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watch: %d %v", r.status, r.body)
	}
	check := func(stock map[string]bool, after time.Duration) {
		t.Helper()
		f.set(stock)
		e.clock.add(after)
		if next := e.srv.checkStock(t.Context()); next != stockEvery {
			t.Fatalf("next check in %v", next)
		}
	}
	check(nil, time.Minute)
	check(map[string]bool{"fsn1": true}, time.Minute)
	check(nil, time.Minute)
	check(map[string]bool{"fsn1": false}, time.Minute)
	check(map[string]bool{"fsn1": true}, 10*time.Minute)
	if got := e.inStockPosts(); !slices.Equal(got, []string{"cx53: fsn1"}) {
		t.Fatalf("posted %v: back after 10 minutes away isn't news", got)
	}
	check(map[string]bool{"fsn1": false}, time.Minute)
	check(map[string]bool{"fsn1": true}, 31*time.Minute)
	check(map[string]bool{"nbg1": true, "hel1": true}, time.Minute)
	if got := e.inStockPosts(); !slices.Equal(got, []string{"cx53: fsn1", "cx53: fsn1", "cx53: nbg1 hel1"}) {
		t.Fatalf("posted %v", got)
	}
	v := e.hetznerView(t, own)
	for _, p := range v.Places {
		if !p.Available || p.BuyURL == "" || p.Since == nil {
			t.Fatalf("places: %+v", v.Places)
		}
	}
	if !v.Places[1].Since.Equal(e.clock.now()) || !v.Places[0].Since.Equal(e.clock.now().Add(-time.Minute)) {
		t.Fatalf("since: %v %v", v.Places[0].Since, v.Places[1].Since)
	}
}

func TestARefusedTokenStopsTheWatchUntilItsReplaced(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watch: %d %v", r.status, r.body)
	}
	f.change(func(f *fakeHetzner) { f.refused = true })
	e.srv.checkStock(t.Context())
	asked := f.questions()
	e.srv.checkStock(t.Context())
	if f.questions() != asked {
		t.Fatal("the watch kept asking with a token Hetzner refused")
	}
	if v := e.hetznerView(t, own); !v.Connected || !strings.Contains(v.Problem, "no longer takes the token") {
		t.Fatalf("after the refusal: %+v", v)
	}
	f.change(func(f *fakeHetzner) { f.refused = false })
	f.set(map[string]bool{"hel1": true})
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerOtherToken+`"}`, own.auth()); r.status != http.StatusOK || r.body["problem"] != nil || r.body["tokenEnding"] != "WX99" {
		t.Fatalf("replace the token: %d %v", r.status, r.body)
	}
	e.srv.checkStock(t.Context())
	if got := e.inStockPosts(); !slices.Equal(got, []string{"cx53: hel1"}) {
		t.Fatalf("posted %v", got)
	}
	if rows := e.auditRows(t, "hetzner.watch"); len(rows) != 2 || !strings.Contains(rows[1], "replaced the token; token ending WX99") {
		t.Fatalf("audit: %v", rows)
	}
}

func TestHetznerNotAnsweringShowsAfterAFewChecks(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watch: %d %v", r.status, r.body)
	}
	checked := e.hetznerView(t, own).CheckedAt
	f.change(func(f *fakeHetzner) { f.down = true })
	for i := 1; i <= stockPatience; i++ {
		e.clock.add(time.Minute)
		if next := e.srv.checkStock(t.Context()); next != stockEvery {
			t.Fatalf("check %d: next in %v", i, next)
		}
		v := e.hetznerView(t, own)
		if (v.Problem != "") != (i == stockPatience) || !v.CheckedAt.Equal(*checked) {
			t.Fatalf("after %d failed checks: %+v", i, v)
		}
	}
	f.change(func(f *fakeHetzner) { f.down, f.resetAt = false, e.clock.now().Add(20*time.Minute) })
	if next := e.srv.checkStock(t.Context()); next != 20*time.Minute {
		t.Fatalf("rate limited until 20 minutes from now: next in %v", next)
	}
	f.change(func(f *fakeHetzner) { f.resetAt = e.clock.now().Add(3 * time.Hour) })
	if next := e.srv.checkStock(t.Context()); next != stockSlowest {
		t.Fatalf("rate limited for hours: next in %v", next)
	}
	f.change(func(f *fakeHetzner) { f.resetAt = time.Time{} })
	e.srv.checkStock(t.Context())
	if v := e.hetznerView(t, own); v.Problem != "" || !v.CheckedAt.Equal(e.clock.now()) {
		t.Fatalf("answering again: %+v", v)
	}
}

func TestChangingTheTypeAsksAgainAndStoppingForgetsTheToken(t *testing.T) {
	f := newFakeHetzner(t)
	e := newHetznerEnv(t, f)
	own := owner(t, e)
	f.set(map[string]bool{"fsn1": true})
	if r := e.do(t, "PUT", "/api/hetzner", `{"token":"`+hetznerTestToken+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("watch: %d %v", r.status, r.body)
	}
	if r := e.do(t, "PUT", "/api/hetzner", `{"serverType":"cx43"}`, own.auth()); r.status != http.StatusOK || r.body["serverType"] != "cx43" || r.body["tokenEnding"] != "WXyz" {
		t.Fatalf("watch cx43: %d %v", r.status, r.body)
	}
	if got := e.inStockPosts(); !slices.Equal(got, []string{"cx53: fsn1", "cx43: fsn1"}) {
		t.Fatalf("posted %v", got)
	}
	if rows := e.auditRows(t, "hetzner.watch"); len(rows) != 2 || !strings.Contains(rows[1], "now watching cx43 instead of cx53") {
		t.Fatalf("audit: %v", rows)
	}
	r := e.do(t, "DELETE", "/api/hetzner", "", own.auth())
	if r.status != http.StatusOK || r.body["connected"] != false || r.body["tokenEnding"] != nil {
		t.Fatalf("stop: %d %v", r.status, r.body)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM hetzner_watch`).Scan(&n)
	if n != 0 {
		t.Fatal("the token is still kept")
	}
	if rows := e.auditRows(t, "hetzner.stop"); len(rows) != 1 || !strings.Contains(rows[0], "forgot the token ending WXyz") {
		t.Fatalf("audit: %v", rows)
	}
	asked := f.questions()
	e.srv.checkStock(t.Context())
	if f.questions() != asked {
		t.Fatal("the watch asked Hetzner after it stopped")
	}
}
