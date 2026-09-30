package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// cloudPlans is what playkeeper.io/cloud hears from the dashboard, with no
// sign-in, and the answer's headers.
func (e *env) cloudPlans(t *testing.T) ([]map[string]any, http.Header) {
	t.Helper()
	r := e.do(t, "GET", cloudPlansPath, "", nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET %s: %d %v", cloudPlansPath, r.status, r.body)
	}
	raw, _ := json.Marshal(r.body["plans"])
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	return plans, r.header
}

// playkeeper.io/cloud hears, for each paid plan of the dashboard's own store,
// whether it's available or sold out, and never how many are left.
// Any site may ask, and caches may keep the answer a minute.
func TestTheCloudPageHearsWhichPlansAreSoldOut(t *testing.T) {
	_, e, _ := connectedWhop(t)
	ctx := context.Background()
	e.reply("GET", "/v1/servers", `[]`)
	e.reply("GET", "/v1/machine", liveMachine(6144, true))
	e.srv.syncSaleRoom(ctx)
	e.reconcile()
	plans, h := e.cloudPlans(t)
	want := []map[string]any{
		{"id": "plan_starter", "name": "Starter", "memoryMB": float64(4096), "available": true},
		{"id": "plan_big", "name": "Big", "memoryMB": float64(8192), "available": false},
	}
	if len(plans) != len(want) {
		t.Fatalf("plans %v, want %v", plans, want)
	}
	for i := range want {
		if len(plans[i]) != len(want[i]) {
			t.Errorf("plan %d says %v, want only %v", i, plans[i], want[i])
		}
		for k, v := range want[i] {
			if plans[i][k] != v {
				t.Errorf("plan %d's %s is %v, want %v", i, k, plans[i][k], v)
			}
		}
	}
	if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Cache-Control") != cloudPlansCache {
		t.Errorf("headers: %v", h)
	}

	e.reply("GET", "/v1/machine", liveMachine(1024, true))
	e.srv.syncSaleRoom(ctx)
	e.reconcile()
	plans, _ = e.cloudPlans(t)
	if slices.ContainsFunc(plans, func(p map[string]any) bool { return p["available"] != false }) {
		t.Fatalf("with no room, plans %v", plans)
	}
}

// Only the dashboard's own store's paid plans are Playkeeper Cloud's: a free
// plan isn't sold, an archived one isn't any more, and a business selling
// through the app sells its own. A plan the machines' room hasn't limited
// is available. A store another dashboard took over is that one's to sell.
func TestTheCloudPageHearsOnlyTheOwnStoresPaidPlans(t *testing.T) {
	f, e, own := twoStores(t)
	f.mu.Lock()
	f.plans = append(f.plans,
		map[string]any{"id": "plan_creator", "title": "Creator", "visibility": "hidden", "plan_type": "one_time", "initial_price": 0,
			"product": map[string]any{"id": "prod_mc", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}},
		map[string]any{"id": "plan_gone", "title": "Gone", "visibility": "archived", "plan_type": "renewal", "billing_period": 30, "renewal_price": 30,
			"product": map[string]any{"id": "prod_mc", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "3", whop.MetaMemoryGB: "12"}})
	f.mu.Unlock()
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("reading the store: %d %v", r.status, r.body)
	}
	e.reconcile()
	plans, _ := e.cloudPlans(t)
	var ids []string
	for _, p := range plans {
		ids = append(ids, p["id"].(string))
		if p["available"] != true {
			t.Errorf("%v, whose stock the machines' room hasn't limited, isn't available", p)
		}
	}
	if !slices.Equal(ids, []string{"plan_starter", "plan_big"}) {
		t.Fatalf("the plans Playkeeper Cloud sells: %v", ids)
	}

	b, ownB := secondDashboard(t, f)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("taking Pip over: %d %v", r.status, r.body)
	}
	e.clock.add(whopPollEvery)
	e.reconcile()
	if plans, _ := e.cloudPlans(t); !e.srv.whopTakenOver(testStore) || len(plans) != 0 {
		t.Fatalf("Pip taken over: %v; the plans Playkeeper Cloud sells here: %v", e.srv.whopTakenOver(testStore), plans)
	}
}

// A dashboard that sells nothing answers with no plans.
func TestTheCloudPageHearsNoPlansFromADashboardThatSellsNone(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	if plans, _ := e.cloudPlans(t); len(plans) != 0 {
		t.Fatalf("plans %v", plans)
	}
}
