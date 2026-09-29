package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

const (
	whopTestKey   = "apik_pip_hosting_0123456789abcd"
	whopOtherKey  = "apik_other_business_0123456789"
	whopDashboard = "https://beta.playkeeper.me:8443"
)

// fakeWhop answers like Whop's API for one seller, Pip Hosting, whose store
// sells one product with two plans: Starter, whose metadata says what it
// allows, and Big, whose metadata doesn't.
type fakeWhop struct {
	mu      sync.Mutex
	srv     *httptest.Server
	missing map[string]bool
	// permissionsDown makes Whop's permission check fail, patchDown its
	// product updates, and revoked refuses the test key.
	permissionsDown, patchDown, revoked bool
	products                            map[string]whop.Metadata
	plans                               []map[string]any
	patches                             []string
	keysSeen                            map[string]bool
}

func newFakeWhop(t *testing.T) *fakeWhop {
	t.Helper()
	f := &fakeWhop{missing: map[string]bool{}, keysSeen: map[string]bool{},
		products: map[string]whop.Metadata{"prod_mc": {"color": "green"}},
		plans: []map[string]any{
			{"id": "plan_starter", "title": "Starter", "visibility": "hidden", "plan_type": "renewal", "billing_period": 30, "formatted_price": "$8.00 / month",
				"trial_period_days": 3, "product": map[string]any{"id": "prod_mc", "title": "Minecraft server"},
				"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}},
			{"id": "plan_big", "title": "Big", "visibility": "hidden", "plan_type": "renewal", "billing_period": 30, "currency": "usd", "renewal_price": 16,
				"product": map[string]any{"id": "prod_mc", "title": "Minecraft server"}, "metadata": map[string]any{}},
			{"id": "plan_old", "title": "Old", "visibility": "archived", "product": map[string]any{"id": "prod_mc"}},
		}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWhop) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.keysSeen[key] = true
	w.Header().Set("Content-Type", "application/json")
	account := map[string]any{"id": "biz_pip", "title": "Pip Hosting", "route": "pip-hosting"}
	switch {
	case key == whopTestKey && !f.revoked:
	case key == whopOtherKey:
		account = map[string]any{"id": "biz_other", "title": "Other", "route": "other"}
	default:
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"type":"authentication_error","message":"Invalid API key"}}`)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /accounts/me":
		json.NewEncoder(w).Encode(account)
	case "GET /permissions":
		if f.permissionsDown {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
			return
		}
		var data []map[string]any
		for _, a := range strings.Split(r.URL.Query().Get("actions"), ",") {
			data = append(data, map[string]any{"action": a, "granted": !f.missing[a]})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case "GET /products":
		var data []map[string]any
		for id, meta := range f.products {
			data = append(data, map[string]any{"id": id, "title": "Minecraft server", "metadata": meta})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})
	case "GET /variants":
		json.NewEncoder(w).Encode(map[string]any{"data": f.plans, "page_info": map[string]any{"has_next_page": false}})
	case "PATCH /products/prod_mc":
		if f.patchDown {
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"type":"server_error","message":"Try again"}}`)
			return
		}
		var body struct {
			Metadata whop.Metadata `json:"metadata"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.products["prod_mc"] = body.Metadata
		f.patches = append(f.patches, body.Metadata[whop.MetaDashboard])
		json.NewEncoder(w).Encode(map[string]any{"id": "prod_mc"})
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"not_found","message":"No such route"}}`)
	}
}

func (f *fakeWhop) dashboardMeta() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.products["prod_mc"][whop.MetaDashboard]
}

// newWhopEnv is a dashboard whose Sell on Whop talks to f, on a machine
// whose address is beta.playkeeper.me with a certificate good for a while.
func newWhopEnv(t *testing.T, f *fakeWhop) *env {
	t.Helper()
	e := newEnvConfig(t, func(c *config.Config) { c.WhopAPIURL = f.srv.URL }, nil)
	e.setAddress(t, "beta.playkeeper.me")
	return e
}

// setAddress makes the agent answer that the machine's address is host,
// with a certificate for it good for 60 days, or no address for "".
func (e *env) setAddress(t *testing.T, host string) {
	t.Helper()
	if host == "" {
		e.replyStatus("GET", "/v1/address", 200, `{"kind":"","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{}}`)
		return
	}
	after := e.clock.now().Add(60 * 24 * time.Hour).Format(time.RFC3339)
	e.replyStatus("GET", "/v1/address", 200, `{"kind":"playkeeper","host":"`+host+`","panelPort":8443,"base":"playkeeper.me","servers":[],"names":{},
		"certificate":{"names":["`+host+`"],"challenge":"dns-01","notAfter":"`+after+`"}}`)
}

func (e *env) whopView(t *testing.T, m member) whopView {
	t.Helper()
	var v whopView
	if st := e.get(t, "/api/whop", m.cookie, &v); st != http.StatusOK {
		t.Fatalf("GET /api/whop: %d", st)
	}
	return v
}

func TestSellOnWhopIsTheOwnersAlone(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	owner(t, e)
	lena := addAdmin(t, e, "lena", "*")
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/whop", ""}, {"POST", "/api/whop/connect", `{"key":"` + whopTestKey + `"}`}, {"POST", "/api/whop/sync", ""},
		{"PUT", "/api/whop/plans/plan_big", `{"servers":1,"memoryMB":4096}`}, {"DELETE", "/api/whop", ""},
	} {
		if r := e.do(t, rt.method, rt.path, rt.body, lena.auth()); r.status != http.StatusForbidden {
			t.Errorf("an admin of every server: %s %s = %d %v", rt.method, rt.path, r.status, r.body)
		}
	}
	if len(f.keysSeen) != 0 {
		t.Fatalf("Whop was asked for a non-owner: %v", f.keysSeen)
	}
}

func TestConnectingReadsTheStoreAndMarksItsProducts(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	v := e.whopView(t, own)
	if v.Connected || v.Dashboard != whopDashboard || len(v.Needs) != len(whop.Needs) || len(v.Plans) != 0 {
		t.Fatalf("before connecting: %+v", v)
	}

	r := e.do(t, "POST", "/api/whop/connect", `{"key":"  `+whopTestKey+`  "}`, own.auth())
	if r.status != http.StatusOK || r.body["connected"] != true || r.body["keyEnding"] != "abcd" {
		t.Fatalf("connect: %d %v", r.status, r.body)
	}
	if strings.Contains(mustJSON(t, r.body), whopTestKey) {
		t.Fatal("the answer names the key")
	}
	v = e.whopView(t, own)
	if v.Account == nil || v.Account.ID != "biz_pip" || v.Account.Title != "Pip Hosting" || v.ConnectedBy != "admin" || v.Problem != "" || v.SyncedAt == nil {
		t.Fatalf("connected: %+v", v)
	}
	want := []whopPlanView{
		{ID: "plan_starter", ProductID: "prod_mc", ProductTitle: "Minecraft server", Title: "Starter", Price: "$8.00 / month", Visibility: "hidden", TrialDays: 3,
			Allowance: invites.Allowance{Servers: 1, MemoryMB: 4096}, AllowanceFrom: "store"},
		{ID: "plan_big", ProductID: "prod_mc", ProductTitle: "Minecraft server", Title: "Big", Price: "USD 16.00 every 30 days", Visibility: "hidden"},
	}
	if mustJSON(t, v.Plans) != mustJSON(t, want) {
		t.Fatalf("plans:\n%+v\nwant\n%+v", v.Plans, want)
	}
	if got := f.dashboardMeta(); got != whopDashboard || f.products["prod_mc"]["color"] != "green" {
		t.Fatalf("the product's metadata: %v", f.products["prod_mc"])
	}
	var stored string
	e.srv.db.QueryRow(`SELECT api_key FROM whop_account`).Scan(&stored)
	if stored != whopTestKey {
		t.Fatalf("stored key %q", stored)
	}
	rows := e.auditRows(t, "whop.connect")
	if len(rows) != 1 || !strings.Contains(rows[0], "succeeded key ending abcd") || strings.Contains(rows[0], whopTestKey) {
		t.Fatalf("audit: %v", rows)
	}

	// Reading again changes nothing on Whop when nothing changed.
	patches := len(f.patches)
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK || len(f.patches) != patches {
		t.Fatalf("sync: %d %v, %d new patches", r.status, r.body, len(f.patches)-patches)
	}
	// A key of another business needs a disconnect first.
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopOtherKey+`"}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("another business's key: %d %v", r.status, r.body)
	}
}

func TestKeysWhopRefusesOrThatLackPermissionsAreNotKept(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	for _, key := range []string{"short", "has a space in it 0123456789"} {
		if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+key+`"}`, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != "whop_key_refused" {
			t.Fatalf("key %q: %d %v", key, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"apik_wrong_0123456789abcdef"}`, own.auth()); r.status != http.StatusBadRequest || r.body["code"] != "whop_key_refused" {
		t.Fatalf("a key Whop refuses: %d %v", r.status, r.body)
	}
	f.missing["support_chat:create"], f.missing["developer:manage_webhook"] = true, true
	r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	params, _ := r.body["params"].(map[string]any)
	if r.status != http.StatusBadRequest || r.body["code"] != "whop_permissions" || params["missing"] != "developer:manage_webhook,support_chat:create" {
		t.Fatalf("a key without every permission: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); v.Connected {
		t.Fatalf("a refused key was kept: %+v", v)
	}
	if f.dashboardMeta() != "" {
		t.Fatal("a refused key marked the store")
	}
	// When Whop won't say what a key may do, the key is kept: the work
	// itself shows what's missing.
	f.mu.Lock()
	f.missing = map[string]bool{}
	f.permissionsDown = true
	f.mu.Unlock()
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth()); r.status != http.StatusOK || r.body["connected"] != true {
		t.Fatalf("connect while Whop's permission check fails: %d %v", r.status, r.body)
	}
}

func TestAPlanWithoutMetadataGetsItsAllowanceHere(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	for _, tc := range []struct {
		plan, body string
		status     int
	}{
		{"plan_starter", `{"servers":2,"memoryMB":8192}`, http.StatusConflict},
		{"plan_nope", `{"servers":2,"memoryMB":8192}`, http.StatusNotFound},
		{"plan_big", `{"servers":11,"memoryMB":8192}`, http.StatusBadRequest},
		{"plan_big", `{"servers":1,"memoryMB":1000}`, http.StatusBadRequest},
		{"plan_big", `{"servers":2,"memoryMB":8192}`, http.StatusOK},
	} {
		if r := e.do(t, "PUT", "/api/whop/plans/"+tc.plan, tc.body, own.auth()); r.status != tc.status {
			t.Fatalf("PUT %s %s: %d %v", tc.plan, tc.body, r.status, r.body)
		}
	}
	v := e.whopView(t, own)
	if v.Plans[1].Allowance != (invites.Allowance{Servers: 2, MemoryMB: 8192}) || v.Plans[1].AllowanceFrom != "owner" {
		t.Fatalf("Big: %+v", v.Plans[1])
	}
	// What the owner set outlasts reading the store again.
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	if v := e.whopView(t, own); v.Plans[1].AllowanceFrom != "owner" || v.Plans[1].Allowance.Servers != 2 {
		t.Fatalf("Big after a sync: %+v", v.Plans[1])
	}
	if rows := e.auditRows(t, "whop.plan"); len(rows) != 1 || !strings.Contains(rows[0], "Big: creator: up to 2 servers with 8192 MB") {
		t.Fatalf("audit: %v", rows)
	}
	if r := e.do(t, "PUT", "/api/whop/plans/plan_big", `{"servers":0,"memoryMB":0}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("clearing Big: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); v.Plans[1].AllowanceFrom != "" || !v.Plans[1].Allowance.IsZero() {
		t.Fatalf("Big cleared: %+v", v.Plans[1])
	}
}

func TestWithoutAnAddressTheStoreStaysClosed(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	e.setAddress(t, "")
	own := owner(t, e)
	r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	if r.status != http.StatusOK || r.body["dashboard"] != "" || !strings.Contains(r.body["problem"].(string), "no address") {
		t.Fatalf("connect without an address: %d %v", r.status, r.body)
	}
	if f.dashboardMeta() != "" {
		t.Fatal("the store was marked without an address")
	}
	// A certificate that lapsed counts as none.
	e.setAddress(t, "beta.playkeeper.me")
	e.clock.add(61 * 24 * time.Hour)
	if v := e.whopView(t, signIn(t, e, own.id)); v.Dashboard != "" {
		t.Fatalf("a lapsed certificate: %+v", v.Dashboard)
	}
}

func TestDisconnectingClosesTheStoreAndForgetsTheKey(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	if f.dashboardMeta() != whopDashboard {
		t.Fatal("not marked after connecting")
	}
	r := e.do(t, "DELETE", "/api/whop", "", own.auth())
	if r.status != http.StatusOK || r.body["connected"] != false {
		t.Fatalf("disconnect: %d %v", r.status, r.body)
	}
	if _, ok := f.products["prod_mc"][whop.MetaDashboard]; ok || f.products["prod_mc"]["color"] != "green" {
		t.Fatalf("the product after disconnecting: %v", f.products["prod_mc"])
	}
	var n int
	e.srv.db.QueryRow(`SELECT (SELECT COUNT(*) FROM whop_account) + (SELECT COUNT(*) FROM whop_plans)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after disconnecting", n)
	}
	if rows := e.auditRows(t, "whop.disconnect"); len(rows) != 1 {
		t.Fatalf("audit: %v", rows)
	}
}

// A store that still names this dashboard keeps taking orders, so a
// disconnect that can't close it keeps the key for another try, unless
// Whop no longer takes the key, which then can't close it either.
func TestADisconnectThatCantCloseTheStoreKeepsTheKey(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	f.mu.Lock()
	f.patchDown = true
	f.mu.Unlock()
	if r := e.do(t, "DELETE", "/api/whop", "", own.auth()); r.status != http.StatusBadGateway {
		t.Fatalf("disconnect while Whop fails: %d %v", r.status, r.body)
	}
	if v := e.whopView(t, own); !v.Connected || f.dashboardMeta() != whopDashboard {
		t.Fatalf("after a disconnect that failed: connected %v, product %q", v.Connected, f.dashboardMeta())
	}
	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	r := e.do(t, "DELETE", "/api/whop", "", own.auth())
	if r.status != http.StatusOK || r.body["connected"] != false || !strings.Contains(fmt.Sprint(r.body["notice"]), whop.MetaDashboard) {
		t.Fatalf("disconnect with a key Whop refuses: %d %v", r.status, r.body)
	}
}

// An agent that can't be asked for the address leaves the store as it was,
// rather than closing it as if the machine had none.
func TestAnAgentThatCantBeAskedLeavesTheStoreOpen(t *testing.T) {
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth())
	e.replyStatus("GET", "/v1/address", http.StatusServiceUnavailable, `{"error":"Busy.","code":"busy"}`)
	r := e.do(t, "POST", "/api/whop/sync", "", own.auth())
	if r.status != http.StatusOK || f.dashboardMeta() != whopDashboard || !strings.Contains(fmt.Sprint(r.body["problem"]), "couldn't ask this machine for its address") {
		t.Fatalf("sync while the agent can't be asked: %d %v, product %q", r.status, r.body, f.dashboardMeta())
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
