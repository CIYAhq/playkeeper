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
	if _, err := e.srv.db.Exec(`INSERT INTO whop_app(id, api_key) VALUES(1, ?) ON CONFLICT(id) DO UPDATE SET api_key = excluded.api_key`, whopTestAppKey); err != nil {
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
	if err := e.srv.closeWhopStore(ctx, "biz_other", "share", "Playkeeper's share is gone from Other"); err != nil {
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
	if open, err := e.srv.openWhopStore(ctx, "biz_other", "share"); err != nil || !open {
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
