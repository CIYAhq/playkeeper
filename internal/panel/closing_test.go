package panel

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// closedStores is a dashboard selling for Pip Hosting with its own key, and
// Other Hosting just registered through the Playkeeper Cloud app, as 2.1
// does: not open yet.
func closedStores(t *testing.T) (*fakeWhop, *env, member) {
	t.Helper()
	f, e, own := connectedWhop(t)
	f.installOther()
	f.mu.Lock()
	f.users["user_siya"] = "siyabuilt"
	f.mu.Unlock()
	if _, err := e.srv.db.Exec(`INSERT INTO whop_app(id, api_key, share_user, share_username) VALUES(1, ?, 'user_siya', 'siyabuilt')
		ON CONFLICT(id) DO UPDATE SET api_key = excluded.api_key, share_user = excluded.share_user, share_username = excluded.share_username`, whopTestAppKey); err != nil {
		t.Fatal(err)
	}
	if added, err := e.srv.addWhopStore(context.Background(), whop.Account{ID: "biz_other", Title: "Other Hosting", Route: "other-hosting"}); err != nil || !added {
		t.Fatalf("adding Other Hosting: %v, %v", added, err)
	}
	return f, e, own
}

// saleStores are the stores whose plans are for sale.
func saleStores(t *testing.T, e *env) []string {
	t.Helper()
	plans, err := e.srv.sales.SalePlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range plans {
		if !slices.Contains(out, p.Store) {
			out = append(out, p.Store)
		}
	}
	return out
}

// A new app store sells nothing until its seller opens it: the fleet keeps
// no room for its plans, each is set to 0 on Whop, a purchase starts nobody,
// and the owner's list says it's not open yet. Opened, it sells and starts
// its buyer.
func TestANewAppStoreSellsNothingUntilItsSellerOpensIt(t *testing.T) {
	f, e, own := closedStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	if got, sale := core.got(), saleStores(t, e); len(got) != 0 || slices.Contains(sale, "biz_other") || !slices.Contains(f.stockWrites(), "plan_other=0") {
		t.Fatalf("Other not open yet: calls %q, stores for sale %v, stock written %v", got, sale, f.stockWrites())
	}
	r := e.do(t, "GET", "/api/whop/stores", "", own.auth())
	stores, _ := r.body["stores"].([]any)
	if r.status != http.StatusOK || len(stores) != 1 {
		t.Fatalf("the owner's list of stores: %d %v", r.status, r.body)
	}
	if other, _ := stores[0].(map[string]any); other["closedWhy"] != whopNotOpenYetWhy {
		t.Fatalf("Other Hosting on the owner's list: %v", other)
	}
	if open, err := e.srv.openWhopStore(ctx, "biz_other", whopNotOpenYet); err != nil || !open {
		t.Fatalf("opening Other Hosting: %v, %v", open, err)
	}
	e.reconcile()
	if got, sale := core.got(), saleStores(t, e); len(got) != 1 || !strings.Contains(got[0], "whop/biz_other/user_alex") || !slices.Contains(sale, "biz_other") {
		t.Fatalf("Other once open: calls %q, stores for sale %v", got, sale)
	}
}

// A store that sells nothing, closed, suspended or gone, has its hosting
// products hidden from its page on Whop, as its plans' stock is 0, and
// shown there again once it sells: those the dashboard hid and no other, so
// one its seller hid stays hidden, as does one archived meanwhile, and a
// product that isn't for hosting is left alone. Whop is read when that
// changes, not on every pass, and what it refuses is done on a later pass.
// A store back after leaving stays hidden until its seller opens it again,
// which shows every hosting product, and closing after that hides them all.
func TestAStoreThatSellsNothingIsHiddenOnWhopUntilItSellsAgain(t *testing.T) {
	f, e, token := openedAsSeller(t)
	sharesGoToSiya(t, e)
	ctx := context.Background()
	f.setOtherProduct("prod_plus", whop.Metadata{})
	f.setOtherProduct("prod_merch", whop.Metadata{})
	f.addOtherPlan(map[string]any{"id": "plan_plus", "title": "Plus", "renewal_price": 24, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "8"},
		"product": map[string]any{"id": "prod_plus", "title": "Plus server"}})
	f.addOtherPlan(map[string]any{"id": "plan_merch", "title": "Merch", "renewal_price": 5, "product": map[string]any{"id": "prod_merch", "title": "Merch"}})
	f.setOtherProductVisibility("prod_other", "hidden")
	f.setOtherProductVisibility("prod_plus", "hidden")
	f.mu.Lock()
	b := f.installed["biz_other"]
	f.mu.Unlock()
	sell := func(name string) {
		t.Helper()
		if r := e.asSeller(t, "POST", "biz_other/sell", acceptingTerms, token, nil); r.status != http.StatusOK || r.body["open"] != true {
			t.Fatalf("%s: %d %v", name, r.status, r.body)
		}
	}
	listed := func() string {
		return "Other " + f.otherProductVisibility("prod_other") + ", Plus " + f.otherProductVisibility("prod_plus") + ", Merch " + f.otherProductVisibility("prod_merch")
	}
	reads := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return b.productReads
	}
	refuse := func(hiding, showing bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b.hideDown, b.showProductDown = hiding, showing
	}
	// pass runs the store's pass, and another, which reads none of its
	// products from Whop, since nothing changed.
	pass := func(name, want string) {
		t.Helper()
		e.reconcile()
		n := reads()
		e.reconcile()
		if got := listed(); got != want || reads() != n {
			t.Fatalf("%s: %s, its products read %d more times on the next pass", name, got, reads()-n)
		}
	}

	sell("Open the store")
	f.setOtherProductVisibility("prod_plus", "hidden")
	pass("open, Plus hidden by its seller", "Other visible, Plus hidden, Merch visible")
	if err := e.srv.closeWhopStore(ctx, "biz_other", "held", "Other is held closed"); err != nil {
		t.Fatal(err)
	}
	pass("closed", "Other hidden, Plus hidden, Merch visible")
	if _, err := e.srv.openWhopStore(ctx, "biz_other", "held"); err != nil {
		t.Fatal(err)
	}
	pass("open again", "Other visible, Plus hidden, Merch visible")

	if _, err := e.srv.suspendWhopStore(ctx, "biz_other", "admin", "griefing"); err != nil {
		t.Fatal(err)
	}
	refuse(true, false)
	e.reconcile()
	if got := listed(); got != "Other visible, Plus hidden, Merch visible" {
		t.Fatalf("suspended, with Whop refusing to hide a product: %s", got)
	}
	refuse(false, false)
	pass("suspended", "Other hidden, Plus hidden, Merch visible")
	if _, err := e.srv.liftWhopStore(ctx, "biz_other", "admin"); err != nil {
		t.Fatal(err)
	}
	refuse(false, true)
	e.reconcile()
	if got := listed(); got != "Other hidden, Plus hidden, Merch visible" {
		t.Fatalf("lifted, with Whop refusing to show a product: %s", got)
	}
	refuse(false, false)
	pass("lifted", "Other visible, Plus hidden, Merch visible")

	if err := e.srv.whopStoreLeft(ctx, "biz_other", "the Playkeeper Cloud app was uninstalled"); err != nil {
		t.Fatal(err)
	}
	pass("gone", "Other hidden, Plus hidden, Merch visible")
	if back, err := e.srv.addWhopStore(ctx, whop.Account{ID: "biz_other", Title: "Other Hosting"}); err != nil || !back {
		t.Fatalf("Other back: %v, %v", back, err)
	}
	pass("back, not open yet", "Other hidden, Plus hidden, Merch visible")
	sell("Open the store once back")
	if got := listed(); got != "Other visible, Plus visible, Merch visible" {
		t.Fatalf("open once back: %s", got)
	}
	if err := e.srv.closeWhopStore(ctx, "biz_other", "held", "Other is held closed"); err != nil {
		t.Fatal(err)
	}
	pass("closed once back", "Other hidden, Plus hidden, Merch visible")
	f.setOtherProductVisibility("prod_plus", "archived")
	if _, err := e.srv.openWhopStore(ctx, "biz_other", "held"); err != nil {
		t.Fatal(err)
	}
	pass("open once back, Plus archived meanwhile", "Other visible, Plus archived, Merch visible")
}

// Each reason a store is closed for is taken back by its own name alone,
// and the store opens once none holds it closed. Only an app store is
// opened or closed.
func TestAStoreOpensOnceNoReasonHoldsItClosed(t *testing.T) {
	_, e, _ := closedStores(t)
	ctx := context.Background()
	share := "Playkeeper's share is gone from Other"
	if err := e.srv.closeWhopStore(ctx, "biz_other", "share", share); err != nil {
		t.Fatal(err)
	}
	if open, err := e.srv.openWhopStore(ctx, "biz_other", whopNotOpenYet); err != nil || open {
		t.Fatalf("opening Other while its share is gone: %v, %v", open, err)
	}
	if st, _, _ := e.srv.whopStoreByID(ctx, "biz_other"); st.ClosedWhy != share {
		t.Fatalf("Other closed for %q", st.ClosedWhy)
	}
	if open, err := e.srv.openWhopStore(ctx, "biz_other", "share"); err != nil || !open {
		t.Fatalf("opening Other once its share is back: %v, %v", open, err)
	}
	if err := e.srv.closeWhopStore(ctx, testStore, "share", share); err == nil {
		t.Fatal("the key store was closed")
	}
	if _, err := e.srv.openWhopStore(ctx, testStore, "share"); err == nil {
		t.Fatal("the key store was opened")
	}
	if err := e.srv.closeWhopStore(ctx, "biz_other", "Share!", share); err == nil {
		t.Fatal("a store was closed for a reason without a proper name")
	}
}

// A closed store's customers go on, and their plans still end, but nobody
// starts while it's closed: neither a new buyer nor a customer whose plan
// comes back. Opened again, both start.
func TestAClosedStoresCustomersGoOnButNobodyStarts(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	if err := e.srv.closeWhopStore(ctx, "biz_other", "held", "Other is held closed"); err != nil {
		t.Fatal(err)
	}
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "expired")
	f.buyAt("biz_other", "mem_kim2", "user_kim", "plan_other", "active")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	got := core.got()
	if len(got) != 2 || !strings.HasPrefix(got[1], "pause (") || !strings.Contains(got[1], "whop/biz_other/user_alex") {
		t.Fatalf("the core's calls while Other is closed: %q", got)
	}
	if open, err := e.srv.openWhopStore(ctx, "biz_other", "held"); err != nil || !open {
		t.Fatalf("opening Other: %v, %v", open, err)
	}
	e.reconcile()
	got = core.got()
	started := func(user string) bool {
		return slices.ContainsFunc(got[2:], func(c string) bool {
			return strings.HasPrefix(c, "start ") && strings.Contains(c, "whop/biz_other/"+user)
		})
	}
	if len(got) != 4 || !started("user_alex") || !started("user_kim") {
		t.Fatalf("the core's calls once Other is open: %q", got)
	}
}
