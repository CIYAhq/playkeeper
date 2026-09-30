package panel

import (
	"context"
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
	if rows := a.auditRows(t, "whop.take_over"); len(rows) != 1 || !strings.HasSuffix(rows[0], "took the store over from "+otherDashboard) {
		t.Fatalf("audit: %q", rows)
	}
	b.do(t, "POST", "/api/whop/sync", "", ownB.auth())
	if v := b.whopView(t, ownB); v.TakenOverBy != whopDashboard {
		t.Fatalf("the second dashboard wasn't stopped: %+v", v)
	}
}

func (f *fakeWhop) refuseMarks(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patchDown = v
}

func TestATakeoverCountsOnlyOnceTheStoreCarriesItsMarks(t *testing.T) {
	f, a, _ := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	coreA, coreB := useFakeCore(a), useFakeCore(b)

	// Whop won't take the second dashboard's marks, so the first still
	// sells and the second doesn't.
	f.refuseMarks(true)
	r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != whopDashboard || r.body["takenOverAt"] != nil || r.body["problem"] == nil {
		t.Fatalf("a takeover Whop didn't mark: %d %v", r.status, r.body)
	}
	a.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	b.clock.add(whopPollEvery)
	b.reconcile()
	a.reconcile()
	if got := coreB.got(); len(got) != 0 || f.dashboardMeta() != whopDashboard {
		t.Fatalf("the second dashboard sold before the store was its: calls %q, store names %q", got, f.dashboardMeta())
	}
	if got := coreA.got(); len(got) != 1 {
		t.Fatalf("the first dashboard's calls: %q", got)
	}
	if rows := b.auditRows(t, "whop.connect"); len(rows) != 1 || !strings.Contains(rows[0], "couldn't take the store over from "+whopDashboard+": Whop said: Try again") {
		t.Fatalf("audit: %q", rows)
	}

	// Trying again once Whop takes them makes the store the second's.
	f.refuseMarks(false)
	r = b.do(t, "POST", "/api/whop/sync", `{"takeOver":true}`, ownB.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != nil || f.dashboardMeta() != otherDashboard {
		t.Fatalf("taking it over again: %d %v, store names %q", r.status, r.body, f.dashboardMeta())
	}
	if rows := b.auditRows(t, "whop.take_over"); len(rows) != 1 || !strings.Contains(rows[0], " succeeded took the store over from "+whopDashboard) {
		t.Fatalf("audit: %q", rows)
	}
}

func TestATakeoverWhopHalfTakesIsPutBack(t *testing.T) {
	f := newFakeWhop(t)
	f.products["prod_plus"] = whop.Metadata{}
	f.plans = append(f.plans, map[string]any{"id": "plan_plus", "title": "Plus", "visibility": "hidden", "plan_type": "renewal", "billing_period": 30, "renewal_price": 16,
		"product": map[string]any{"id": "prod_plus", "title": "Minecraft server Plus"}, "metadata": map[string]any{whop.MetaServers: "2", whop.MetaMemoryGB: "8"}, "unlimited_stock": true})
	a := newWhopEnv(t, f)
	ownA := owner(t, a)
	if r := a.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, ownA.auth()); r.status != http.StatusOK {
		t.Fatalf("connect: %d %v", r.status, r.body)
	}
	marks := func() []string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return []string{f.products["prod_mc"][whop.MetaDashboard], f.products["prod_plus"][whop.MetaDashboard]}
	}
	if got := marks(); got[0] != whopDashboard || got[1] != whopDashboard {
		t.Fatalf("the store's marks: %q", got)
	}

	// Whop takes the second dashboard's mark on the first product and
	// refuses it on the second, so the first is put back.
	b, ownB := secondDashboard(t, f)
	f.mu.Lock()
	f.refuseProduct = "prod_plus"
	f.mu.Unlock()
	r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != whopDashboard {
		t.Fatalf("a takeover Whop half took: %d %v", r.status, r.body)
	}
	if got := marks(); got[0] != whopDashboard || got[1] != whopDashboard {
		t.Fatalf("the store's marks after a takeover Whop half took: %q", got)
	}
	core := useFakeCore(a)
	a.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	a.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the first dashboard stopped selling: %q", got)
	}
}

func TestATakeoverWhopMadeButAnsweredWithAnErrorIsPutBack(t *testing.T) {
	f, a, _ := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	f.mu.Lock()
	f.lostReply = "prod_mc"
	f.mu.Unlock()
	r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != whopDashboard || f.dashboardMeta() != whopDashboard {
		t.Fatalf("a takeover whose answer was lost: %d %v, store names %q", r.status, r.body, f.dashboardMeta())
	}
	core := useFakeCore(a)
	a.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	a.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the first dashboard stopped selling: %q", got)
	}
}

func TestDisconnectingTakesOffMarksAtTheMachinesAddress(t *testing.T) {
	f, _, _ := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	f.refuseMarks(true)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK || r.body["takenOverBy"] != whopDashboard {
		t.Fatalf("a takeover Whop refused: %d %v", r.status, r.body)
	}
	// Whop kept the second dashboard's marks though it answered with an
	// error, which the dashboard never recorded as its own.
	f.mu.Lock()
	f.products["prod_mc"] = whop.Metadata{whop.MetaDashboard: otherDashboard, whop.MetaBusiness: "biz_pip", "color": "green"}
	f.mu.Unlock()
	f.refuseMarks(false)
	if r := b.do(t, "DELETE", "/api/whop", "", ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnecting: %d %v", r.status, r.body)
	}
	if got := f.dashboardMeta(); got != "" {
		t.Fatalf("disconnecting left the store naming %q", got)
	}
}

func TestTakingAStoreBackWhopWontMarkLeavesItWithTheOther(t *testing.T) {
	f, a, ownA := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK || r.body["takenOverBy"] != nil {
		t.Fatalf("taking the store over: %d %v", r.status, r.body)
	}
	a.reconcile()
	f.refuseMarks(true)
	r := a.do(t, "POST", "/api/whop/sync", `{"takeOver":true}`, ownA.auth())
	if r.status != http.StatusOK || r.body["takenOverBy"] != otherDashboard || r.body["takenOverAt"] == nil || f.dashboardMeta() != otherDashboard {
		t.Fatalf("taking the store back while Whop won't mark it: %d %v, store names %q", r.status, r.body, f.dashboardMeta())
	}
	core := useFakeCore(a)
	a.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	a.reconcile()
	if got := core.got(); len(got) != 0 {
		t.Fatalf("the first dashboard sold after taking the store back failed: %q", got)
	}
	if rows := a.auditRows(t, "whop.take_over"); len(rows) != 1 || !strings.Contains(rows[0], " failed couldn't take the store over from "+otherDashboard) {
		t.Fatalf("audit: %q", rows)
	}
}

func TestADashboardTakenOverStopsBeforeItActsAgain(t *testing.T) {
	f, a, _ := connectedWhop(t)
	b, ownB := secondDashboard(t, f)
	core := useFakeCore(a)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("taking the store over: %d %v", r.status, r.body)
	}

	// A buyer and more room reach the first dashboard before its next read
	// of the store is due.
	if err := a.srv.sales.SetAvailability(context.Background(), map[string]int{"plan_starter": 2}); err != nil {
		t.Fatal(err)
	}
	a.deliver(t, "evt_alex", whop.EventMembershipActivated, f.buy("mem_alex", "user_alex", "plan_starter", "active"))
	a.reconcile()
	if got := core.got(); len(got) != 0 || len(f.stockWrites()) != 0 {
		t.Fatalf("a dashboard taken over still acted: calls %q, stock written %q", got, f.stockWrites())
	}
	if !a.srv.whopTakenOver(testStore) {
		t.Fatal("the first dashboard didn't notice it was taken over")
	}
}

func TestTakingAStoreOverNeedsAnAddress(t *testing.T) {
	f, a, ownA := connectedWhop(t)
	b := newWhopEnv(t, f)
	b.setAddress(t, "")
	ownB := owner(t, b)
	r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth())
	problem, _ := r.body["problem"].(string)
	if r.status != http.StatusOK || r.body["takenOverBy"] != whopDashboard || !strings.Contains(problem, "no address") || f.dashboardMeta() != whopDashboard {
		t.Fatalf("taking a store over without an address: %d %v, store names %q", r.status, r.body, f.dashboardMeta())
	}

	// Nor does a dashboard that lost its address take another's marks off,
	// and it notices it was taken over all the same.
	c, ownC := secondDashboard(t, f)
	if r := c.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownC.auth()); r.status != http.StatusOK || r.body["takenOverBy"] != nil {
		t.Fatalf("taking the store over: %d %v", r.status, r.body)
	}
	a.setAddress(t, "")
	a.do(t, "POST", "/api/whop/sync", `{"takeOver":true}`, ownA.auth())
	if got := f.dashboardMeta(); got != otherDashboard {
		t.Fatalf("a dashboard without an address took the store's marks off: it names %q", got)
	}
	a.reconcile()
	if !a.srv.whopTakenOver(testStore) {
		t.Fatal("a dashboard without an address didn't notice it was taken over")
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
