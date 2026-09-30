package panel

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// otherPauses are the core's pauses of Other Hosting's customers in got.
func otherPauses(got []string) []string {
	var out []string
	for _, c := range got {
		if strings.HasPrefix(c, "pause (") {
			out = append(out, c)
		}
	}
	return out
}

// Only the Whop side's call makes a store leave: a grant missing for days,
// with Whop's empty answers, pauses nobody. Once called, every customer of
// the store who was started is paused from what the dashboard kept, the
// grant still gone, and the other store's customers go on. Calling it again
// changes nothing.
func TestOnlyTheWhopSidesCallMakesAStoreLeave(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.mu.Lock()
	f.users["user_sam"] = "samcrafts"
	f.mu.Unlock()
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	f.buyAt("biz_other", "mem_sam2", "user_sam", "plan_other", "active")
	e.reconcile()
	if got := core.got(); len(got) != 3 {
		t.Fatalf("the core's calls: %q", got)
	}
	f.mu.Lock()
	f.installed["biz_other"].revoked = true
	f.mu.Unlock()
	for range 6 {
		e.clock.add(24 * time.Hour)
		e.reconcile()
	}
	if st, _, _ := e.srv.whopStoreByID(ctx, "biz_other"); !st.LeftAt.IsZero() || len(otherPauses(core.got())) != 0 {
		t.Fatalf("with Other's grant gone six days: left at %v, calls %q", st.LeftAt, core.got())
	}

	why := "the Playkeeper Cloud app's grant has been missing for 7 days"
	for range 2 {
		if err := e.srv.whopStoreLeft(ctx, "biz_other", why); err != nil {
			t.Fatal(err)
		}
	}
	e.reconcile()
	e.reconcile()
	pauses := otherPauses(core.got())
	slices.Sort(pauses)
	want := []string{
		"pause (their store left Playkeeper Cloud: " + why + ") whop/biz_other/user_alex alexplays",
		"pause (their store left Playkeeper Cloud: " + why + ") whop/biz_other/user_sam samcrafts",
	}
	if !slices.Equal(pauses, want) {
		t.Fatalf("the pauses once Other left: %q", pauses)
	}
	if rows := e.auditRows(t, "whop.store_left"); len(rows) != 1 || !strings.Contains(rows[0], why) {
		t.Fatalf("audit: %q", rows)
	}
}

// A store that left sells nothing: the fleet keeps no room for its plans,
// each goes to 0 on Whop, and a purchase starts nobody. Its customers, and
// nobody else, are paused and told so in its chat while the app can still
// post there, and the owner's list says it left and why.
func TestAStoreThatLeftSellsNothingAndTellsItsCustomers(t *testing.T) {
	f, e, own := storesWithCustomers(t)
	ctx := context.Background()
	if err := e.srv.sales.SetAvailability(ctx, map[string]int{"plan_starter": 3, "plan_other": 2}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	told := len(f.sentIn("biz_other", "user_alex"))
	why := "Playkeeper's share has been gone for 72 hours"
	if err := e.srv.whopStoreLeft(ctx, "biz_other", why); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	for c, want := range map[[2]string]CustomerState{{testStore, "user_alex"}: CustomerActive, {"biz_other", "user_alex"}: CustomerPaused, {"biz_other", "user_sam"}: CustomerPaused} {
		if info := storeAccount(t, e, c[0], c[1]); info.State != want {
			t.Fatalf("%s at %s once Other Hosting left: %+v", c[1], c[0], info)
		}
	}
	if sent := f.sentIn("biz_other", "user_alex"); len(sent) != told+1 || !strings.Contains(sent[len(sent)-1], "Your Playkeeper plan has ended") {
		t.Fatalf("alex was told in Other's chat: %q", sent)
	}
	var others []string
	for _, w := range f.stockWrites() {
		if strings.HasPrefix(w, "plan_other=") {
			others = append(others, w)
		}
	}
	plans, err := e.srv.sales.SalePlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(plans, func(p SalePlan) bool { return p.Store == "biz_other" }) || len(others) == 0 || others[len(others)-1] != "plan_other=0" {
		t.Fatalf("once Other left: plans for sale %v, Other's stock written %v", plans, others)
	}
	f.buyAt("biz_other", "mem_kim2", "user_kim", "plan_other", "active")
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if _, ok, _ := (customerCore{s: e.srv}).CustomerAccount(ctx, whopProvider, "biz_other", "user_kim"); ok {
		t.Fatal("kim was started at a store that left")
	}
	r := e.do(t, "GET", "/api/whop/stores", "", signIn(t, e, own.id).auth())
	stores, _ := r.body["stores"].([]any)
	if r.status != http.StatusOK || len(stores) != 1 {
		t.Fatalf("the owner's list of stores: %d %v", r.status, r.body)
	}
	if other, _ := stores[0].(map[string]any); other["leftAt"] == nil || other["leftWhy"] != why {
		t.Fatalf("Other Hosting on the owner's list: %v", other)
	}
}

// A store that left is back once 2.1 adds it again: it's read again at
// once, so its customers with a plan start again, as a renewal does, a
// purchase made while it was away starts too, and it sells again. Added
// once more, it's a store already.
func TestAStoreThatLeftIsBackOnceAddedAgain(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	if err := e.srv.whopStoreLeft(ctx, "biz_other", "Playkeeper's share has been short for 72 hours"); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if pauses := otherPauses(core.got()); len(pauses) != 1 {
		t.Fatalf("the pauses once Other left: %q", core.got())
	}
	f.buyAt("biz_other", "mem_kim2", "user_kim", "plan_other", "active")
	added, err := e.srv.addWhopStore(ctx, whop.Account{ID: "biz_other", Title: "Other Hosting", Route: "other-hosting"})
	if err != nil || !added {
		t.Fatalf("adding Other Hosting again: %v, %v", added, err)
	}
	e.reconcile()
	got := core.got()
	started := func(user string) bool {
		return slices.ContainsFunc(got[2:], func(c string) bool {
			return strings.HasPrefix(c, "start plan_other") && strings.Contains(c, "whop/biz_other/"+user)
		})
	}
	if len(got) != 4 || !started("user_alex") || !started("user_kim") {
		t.Fatalf("the core's calls once Other is back: %q", got)
	}
	if plans, _ := e.srv.sales.SalePlans(ctx); !slices.ContainsFunc(plans, func(p SalePlan) bool { return p.ID == "plan_other" }) {
		t.Fatalf("plans for sale once Other is back: %v", plans)
	}
	if again, err := e.srv.addWhopStore(ctx, whop.Account{ID: "biz_other", Title: "Other Hosting"}); err != nil || again {
		t.Fatalf("adding Other Hosting once more: %v, %v", again, err)
	}
	if rows := e.auditRows(t, "whop.store_back"); len(rows) != 1 {
		t.Fatalf("audit: %q", rows)
	}
}

// A store that leaves while suspended ends its customers' plans, so once
// its suspension is lifted they're paused, not suspended, even with the
// app uninstalled and a membership Whop told of that it can no longer
// confirm.
func TestLiftingAStoreThatLeftLeavesItsCustomersPaused(t *testing.T) {
	f, e, own := storesWithCustomers(t)
	ctx := context.Background()
	if r := e.do(t, "POST", "/api/whop/stores/biz_other/suspension", `{"reason":"selling to cheaters"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending Other Hosting: %d %v", r.status, r.body)
	}
	f.mu.Lock()
	f.installed["biz_other"].revoked = true
	f.mu.Unlock()
	if err := e.srv.whopStoreLeft(ctx, "biz_other", "the Playkeeper Cloud app's grant has been missing for 7 days"); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if info := storeAccount(t, e, "biz_other", "user_alex"); info.State != CustomerSuspended {
		t.Fatalf("alex at Other once it left, suspended: %+v", info)
	}
	if err := e.srv.keepMembership("biz_other", whop.Membership{ID: "mem_alex9", UserID: "user_alex", PlanID: "plan_other", Status: "active"}, true); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "DELETE", "/api/whop/stores/biz_other/suspension", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("lifting Other Hosting's suspension: %d %v", r.status, r.body)
	}
	e.reconcile()
	for _, subject := range []string{"user_alex", "user_sam"} {
		if info := storeAccount(t, e, "biz_other", subject); info.State != CustomerPaused {
			t.Fatalf("%s at Other once its suspension was lifted: %+v", subject, info)
		}
	}
	if info := storeAccount(t, e, testStore, "user_alex"); info.State != CustomerActive {
		t.Fatalf("alex at Pip: %+v", info)
	}
}

// Only an app store leaves: the dashboard's own store doesn't, and one it
// doesn't sell for is refused.
func TestOnlyAnAppStoreLeaves(t *testing.T) {
	_, e, _ := twoStores(t)
	ctx := context.Background()
	if err := e.srv.whopStoreLeft(ctx, testStore, "gone"); err == nil {
		t.Fatal("the key store left")
	}
	if err := e.srv.whopStoreLeft(ctx, "biz_nobody", "gone"); err == nil {
		t.Fatal("a store the dashboard doesn't sell for left")
	}
	if st, _, _ := e.srv.whopStoreByID(ctx, testStore); !st.LeftAt.IsZero() {
		t.Fatal("the key store was marked as left")
	}
}
