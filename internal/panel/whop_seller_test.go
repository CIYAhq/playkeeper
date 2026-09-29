package panel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// otherDashboard is where secondDashboard answers.
const otherDashboard = "https://other.playkeeper.me:8443"

// secondDashboard is another dashboard, at otherDashboard, reaching the same
// store on Whop as the first.
func secondDashboard(t *testing.T, f *fakeWhop) (*env, member) {
	t.Helper()
	e := newWhopEnv(t, f)
	e.setAddress(t, "other.playkeeper.me")
	return e, owner(t, e)
}

func (f *fakeWhop) businessMeta() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.products["prod_mc"][whop.MetaBusiness]
}

func TestAnotherDashboardIsRefusedAStoreThisOneSellsForUnlessItTakesItOver(t *testing.T) {
	f, a, ownA := connectedWhop(t)
	if f.dashboardMeta() != whopDashboard || f.businessMeta() != "biz_pip" {
		t.Fatalf("the store's marking: %q for %q", f.dashboardMeta(), f.businessMeta())
	}
	b, ownB := secondDashboard(t, f)
	r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, ownB.auth())
	params, _ := r.body["params"].(map[string]any)
	if r.status != http.StatusConflict || r.body["code"] != api.CodeWhopOtherSeller || params["dashboard"] != whopDashboard {
		t.Fatalf("connecting a store another dashboard sells for: %d %v", r.status, r.body)
	}
	if v := b.whopView(t, ownB); v.Connected || f.dashboardMeta() != whopDashboard {
		t.Fatalf("the refused dashboard kept the key (%v) or marked the store (%q)", v.Connected, f.dashboardMeta())
	}

	// Taken over, the store is the second dashboard's.
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("taking the store over: %d %v", r.status, r.body)
	}
	if f.dashboardMeta() != otherDashboard {
		t.Fatalf("after the takeover the store names %q", f.dashboardMeta())
	}
	if rows := b.auditRows(t, "whop.connect"); len(rows) != 2 || !strings.HasSuffix(rows[1], "took the store over from "+whopDashboard) {
		t.Fatalf("audit: %q", rows)
	}

	// The first stops at its next read of the store: it starts nobody and
	// leaves the store's marking alone, even when read by hand.
	core := useFakeCore(a)
	patches := len(f.patches)
	f.buy("mem_alex", "user_alex", "plan_starter", "active")
	a.clock.add(whopPollEvery)
	a.reconcile()
	a.reconcile()
	a.do(t, "POST", "/api/whop/sync", "", ownA.auth())
	if v := a.whopView(t, ownA); v.TakenOverBy != otherDashboard || v.TakenOverAt == nil {
		t.Fatalf("the first dashboard's view: taken over by %q at %v", v.TakenOverBy, v.TakenOverAt)
	}
	if got := core.got(); len(got) != 0 || len(f.patches) != patches || f.dashboardMeta() != otherDashboard {
		t.Fatalf("a dashboard taken over still acted: calls %q, %d new marks, store names %q", got, len(f.patches)-patches, f.dashboardMeta())
	}
	f.mu.Lock()
	before := f.requests
	f.mu.Unlock()
	a.clock.add(whopPollEvery)
	a.reconcile()
	f.mu.Lock()
	asked := f.requests - before
	f.mu.Unlock()
	if asked != 0 {
		t.Fatalf("a dashboard taken over still asked Whop %d times on its own", asked)
	}

	// Taking it back starts selling again, and the second stops in turn.
	r = a.do(t, "POST", "/api/whop/sync", `{"takeOver":true}`, ownA.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != nil || f.dashboardMeta() != whopDashboard {
		t.Fatalf("taking the store back: %d %v, store names %q", r.status, r.body, f.dashboardMeta())
	}
	a.reconcile()
	if got := core.got(); len(got) != 1 || !strings.HasPrefix(got[0], "start ") {
		t.Fatalf("the first dashboard's calls once it sells again: %q", got)
	}
	if rows := a.auditRows(t, "whop.take_back"); len(rows) != 1 || !strings.HasSuffix(rows[0], "took the store back from "+otherDashboard) {
		t.Fatalf("audit: %q", rows)
	}
	b.do(t, "POST", "/api/whop/sync", "", ownB.auth())
	if v := b.whopView(t, ownB); v.TakenOverBy != whopDashboard {
		t.Fatalf("the second dashboard wasn't stopped: %+v", v)
	}
}

func TestANewAddressOrACopysInheritedMarkingIsntAnotherDashboard(t *testing.T) {
	f, e, own := connectedWhop(t)
	e.setAddress(t, "new.playkeeper.me")
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK || r.body["takenOverBy"] != nil {
		t.Fatalf("reading the store at a new address: %d %v", r.status, r.body)
	}
	if got := f.dashboardMeta(); got != "https://new.playkeeper.me:8443" {
		t.Fatalf("the store names %q", got)
	}

	// A copy of the store in another business keeps its publisher's marking.
	copied := newFakeWhop(t)
	copied.products["prod_mc"] = whop.Metadata{whop.MetaDashboard: "https://publisher.example", whop.MetaBusiness: "biz_publisher", "color": "green"}
	c := newWhopEnv(t, copied)
	ownC := owner(t, c)
	if r := c.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, ownC.auth()); r.status != http.StatusOK {
		t.Fatalf("connecting a copy: %d %v", r.status, r.body)
	}
	if copied.dashboardMeta() != whopDashboard || copied.businessMeta() != "biz_pip" {
		t.Fatalf("the copy's marking: %q for %q", copied.dashboardMeta(), copied.businessMeta())
	}
}

func TestDisconnectingLeavesAnotherDashboardsMarking(t *testing.T) {
	f, a, ownA := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("taking the store over: %d %v", r.status, r.body)
	}
	if r := a.do(t, "DELETE", "/api/whop", "", ownA.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnecting: %d %v", r.status, r.body)
	}
	if got := f.dashboardMeta(); got != otherDashboard {
		t.Fatalf("disconnecting the first dashboard left the store naming %q", got)
	}
	f.mu.Lock()
	hooks := len(f.webhooks)
	f.mu.Unlock()
	if hooks != 1 {
		t.Fatalf("webhooks left: %d, want the second dashboard's", hooks)
	}
}
