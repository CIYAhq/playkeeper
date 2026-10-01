package panel

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/store"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// otherStore is Other Hosting's store as the dashboard has it now.
func (e *env) otherStore(t *testing.T) whopStore {
	t.Helper()
	st, ok, err := e.srv.whopStoreByID(t.Context(), "biz_other")
	if err != nil || !ok {
		t.Fatalf("Other Hosting's store: %v, %v", ok, err)
	}
	return st
}

// calledFor says whether the core was asked to do what for user.
func calledFor(calls []string, what, user string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, what) && strings.Contains(c, "whop/biz_other/"+user) })
}

// An open store whose share the seller removes closes for that, and sells
// nothing and starts nobody, until the share is back, when it opens again.
// Gone for three days, the store leaves.
func TestAnOpenStoreWhoseShareIsGoneClosesUntilItsBackThenLeaves(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	pass := func() {
		e.clock.add(2 * whopPollEvery)
		e.reconcile()
	}
	shares := func(to []map[string]any) []map[string]any {
		f.mu.Lock()
		defer f.mu.Unlock()
		was := f.installed["biz_other"].shares
		f.installed["biz_other"].shares = to
		return was
	}
	e.reconcile()
	if st := e.otherStore(t); st.ClosedWhy != "" {
		t.Fatalf("with its share: closed, %q", st.ClosedWhy)
	}
	kept := shares(nil)
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	pass()
	if st := e.otherStore(t); st.ClosedWhy != "Playkeeper's share on Minecraft server was removed." || calledFor(core.got(), "start ", "user_kim") {
		t.Fatalf("without its share: closed %q, the core's calls %q", st.ClosedWhy, core.got())
	}
	pass()
	if calledFor(core.got(), "start ", "user_kim") {
		t.Fatalf("a buyer started while the share was gone: %q", core.got())
	}
	shares(kept)
	pass()
	if st := e.otherStore(t); st.ClosedWhy != "" || !calledFor(core.got(), "start ", "user_kim") {
		t.Fatalf("with its share back: closed %q, the core's calls %q", st.ClosedWhy, core.got())
	}
	shares(nil)
	pass()
	e.clock.add(whopShareGrace)
	e.reconcile()
	if st := e.otherStore(t); st.LeftAt.IsZero() || !strings.Contains(st.LeftWhy, "Playkeeper's share has been gone or short for 3 days: Playkeeper's share on Minecraft server was removed.") {
		t.Fatalf("after three days without its share: left %v, %q", st.LeftAt, st.LeftWhy)
	}
}

// An archived plan someone still has goes on needing Playkeeper's share,
// since its members go on renewing: archiving a customer's plan and
// removing the share closes the store, which leaves after whopShareGrace,
// whether Whop's plan list still shows the plan or not. An archived plan
// nobody has needs nothing.
func TestAnArchivedPlanSomeoneStillHasNeedsItsShare(t *testing.T) {
	for _, unlisted := range []bool{false, true} {
		f, e, _ := twoStores(t)
		core := useFakeCore(e)
		pass := func() {
			e.clock.add(2 * whopPollEvery)
			e.reconcile()
		}
		f.mu.Lock()
		f.users["user_kim"] = "kimbuilds"
		b := f.installed["biz_other"]
		b.unlistArchived = unlisted
		b.plan("plan_other")["visibility"] = "archived"
		kept := b.shares
		b.shares = nil
		f.mu.Unlock()
		pass()
		if st := e.otherStore(t); st.ClosedWhy != "" {
			t.Fatalf("unlisted %v: an archived plan nobody has, without its share: closed %q", unlisted, st.ClosedWhy)
		}
		f.mu.Lock()
		b.plan("plan_other")["visibility"] = "visible"
		b.shares = kept
		f.mu.Unlock()
		f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
		pass()
		if !calledFor(core.got(), "start ", "user_kim") {
			t.Fatalf("unlisted %v: kim didn't start: %q", unlisted, core.got())
		}
		f.mu.Lock()
		b.plan("plan_other")["visibility"] = "archived"
		b.shares = nil
		f.mu.Unlock()
		pass()
		if st := e.otherStore(t); st.ClosedWhy != "Playkeeper's share on Minecraft server was removed." {
			t.Fatalf("unlisted %v: kim's plan archived and its share removed: closed %q", unlisted, st.ClosedWhy)
		}
		e.clock.add(whopShareGrace)
		e.reconcile()
		if st := e.otherStore(t); st.LeftAt.IsZero() {
			t.Fatalf("unlisted %v: three days on, the store hasn't left: %q", unlisted, st.ClosedWhy)
		}
	}
}

// A plan someone still has that's gone from Whop altogether is a problem,
// since nothing shows Playkeeper's share on it any more.
func TestAPlanSomeoneHasThatsGoneFromWhopIsAProblem(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	f.mu.Lock()
	b := f.installed["biz_other"]
	b.plans = slices.DeleteFunc(b.plans, func(p map[string]any) bool { return p["id"] == "plan_other" })
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if st := e.otherStore(t); !strings.Contains(st.ClosedWhy, "Other, which customers still have, is gone from Whop") {
		t.Fatalf("kim's plan gone from Whop: closed %q", st.ClosedWhy)
	}
}

// A hosting plan the seller adds after Open the store is held to its rules
// at the next share check: one that's one-time, yearly, under the floor,
// on a free trial or past the fleet's limits closes the store for its
// share, naming the plan, and its buyer doesn't start, though their
// payment carried the share.
func TestAPlanAddedAfterOpeningIsHeldToOpenTheStoresRules(t *testing.T) {
	for _, c := range []struct {
		name, status, says string
		terms              map[string]any
	}{
		{"one-time", "completed", "New: It doesn't renew every month", map[string]any{"plan_type": "one_time", "initial_price": 12}},
		{"yearly", "active", "New: It doesn't renew every month", map[string]any{"plan_type": "renewal", "billing_period": 365, "renewal_price": 12}},
		{"under the floor", "active", "New: It charges $9.00, under the $12.00 floor for 4 GB", map[string]any{"plan_type": "renewal", "billing_period": 30, "renewal_price": 9}},
		{"on a free trial", "active", "New: It has a free trial", map[string]any{"plan_type": "renewal", "billing_period": 30, "renewal_price": 12, "trial_period_days": 3}},
		{"past the fleet's limits", "active", "New: It allows 20 servers, and hosted plans allow 1 to 10", map[string]any{"plan_type": "renewal", "billing_period": 30, "renewal_price": 12,
			"metadata": map[string]any{whop.MetaServers: "20", whop.MetaMemoryGB: "4"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, e, _ := twoStores(t)
			core := useFakeCore(e)
			plan := map[string]any{"id": "plan_new", "title": "New", "visibility": "visible", "currency": "usd", "unlimited_stock": true,
				"product": map[string]any{"id": "prod_other", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}}
			maps.Copy(plan, c.terms)
			f.mu.Lock()
			f.users["user_bob"] = "bob"
			f.installed["biz_other"].plans = append(f.installed["biz_other"].plans, plan)
			f.mu.Unlock()
			f.buyAt("biz_other", "mem_bob", "user_bob", "plan_new", c.status)
			e.clock.add(2 * whopPollEvery)
			e.reconcile()
			if st := e.otherStore(t); !strings.Contains(st.ClosedWhy, c.says) || calledFor(core.got(), "start ", "user_bob") {
				t.Fatalf("closed %q, the core's calls %q", st.ClosedWhy, core.got())
			}
		})
	}
}

// A one-time purchase gives no servers in an app store, whose hosted plans
// renew monthly: its buyer isn't started, even on a payment that carried
// the share, while the key store still hosts one.
func TestAOneTimePurchaseGivesNoServersInAnAppStore(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_bob"] = "bob"
	f.installed["biz_other"].plans = append(f.installed["biz_other"].plans, map[string]any{"id": "plan_once", "title": "Once", "visibility": "archived",
		"plan_type": "one_time", "initial_price": 12, "currency": "usd", "unlimited_stock": true,
		"product": map[string]any{"id": "prod_other", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_bob", "user_bob", "plan_once", "completed")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if st := e.otherStore(t); st.ClosedWhy != "" || calledFor(core.got(), "start ", "user_bob") {
		t.Fatalf("a one-time purchase in an app store: closed %q, the core's calls %q", st.ClosedWhy, core.got())
	}
	if got := e.srv.whopSignInWithoutAccount(t.Context(), "biz_other", "user_bob"); got != "no_account" {
		t.Errorf("signing in on a one-time purchase in an app store: %q", got)
	}
	if has, err := e.srv.hasPlan(t.Context(), e.srv.db, erasable{store: "biz_other", subject: "user_bob"}); err != nil || has {
		t.Errorf("a one-time purchase in an app store holds a deletion: %v, %v", has, err)
	}
	if !whopHosts(whopViaKey, "completed") || whopHosts(whopViaApp, "completed") || !whopHosts(whopViaApp, "past_due") || whopHosts(whopViaApp, "expired") {
		t.Error("the statuses that give servers in the key store and in an app store")
	}
}

// A customer of an app store who cancels their monthly plan is reminded to
// download their world, though they also have a one-time purchase there,
// since that keeps none of their servers running, even one the payment
// check found paid.
func TestACancellationIsRemindedThoughAOneTimePurchaseGoesOn(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	b.plans = append(b.plans, map[string]any{"id": "plan_once", "title": "Once", "visibility": "archived",
		"plan_type": "one_time", "initial_price": 12, "currency": "usd", "unlimited_stock": true,
		"product": map[string]any{"id": "prod_other", "title": "Minecraft server"}, "metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}})
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	f.buyAt("biz_other", "mem_once", "user_kim", "plan_once", "completed")
	if _, err := e.srv.db.Exec(`INSERT INTO whop_membership_checks(store_id, membership_id, paid_plan_id, paid_title, paid_servers, paid_mb)
		VALUES('biz_other', 'mem_once', 'plan_once', 'Once', 1, 4096)`); err != nil {
		t.Fatal(err)
	}
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	f.mu.Lock()
	b.memberships["mem_kim"]["status"], b.memberships["mem_kim"]["cancel_at_period_end"] = "canceling", true
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	e.reconcile()
	if msgs := f.sentIn("biz_other", "user_kim"); !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, "You cancelled your Other Hosting plan") }) {
		t.Fatalf("kim's chat at Other Hosting: %q", msgs)
	}
}

// A customer of an app store who cancels the membership that keeps their
// servers running is reminded to download their world, though another
// membership of theirs goes on, since its payment never carried
// Playkeeper's share and it gives them nothing.
func TestACancellationIsRemindedThoughAnUnpaidMembershipGoesOn(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	f.buyAt("biz_other", "mem_free", "user_kim", "plan_other", "active")
	f.mu.Lock()
	b.fees["pay_mem_free"] = nil
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if !calledFor(core.got(), "start plan_other ", "user_kim") {
		t.Fatalf("kim didn't start on their paid membership alone: %q", core.got())
	}
	f.mu.Lock()
	b.memberships["mem_kim"]["status"], b.memberships["mem_kim"]["cancel_at_period_end"] = "canceling", true
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	e.reconcile()
	if msgs := f.sentIn("biz_other", "user_kim"); !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, "You cancelled your Other Hosting plan") }) {
		t.Fatalf("kim's chat at Other Hosting: %q", msgs)
	}
}

// The migration keeps, for each customer already started on an app store,
// what the core was given for them as paid, and no more. A membership the
// payment check had refused, an extra one or one moved to more memory, is
// checked as any other, and one moved keeps what it was given meanwhile.
func TestTheMigrationKeepsWhatEachStartedCustomerWasGivenAsPaid(t *testing.T) {
	at := slices.IndexFunc(panelMigrations, func(m string) bool { return strings.Contains(m, "CREATE TABLE whop_membership_checks") })
	if at < 0 {
		t.Fatal("no migration keeps what the payment check found")
	}
	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := store.Open(path, panelMigrations[:at])
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO whop_stores(store_id, via, connected_at) VALUES('biz_app', 'app', 1), ('biz_key', 'key', 1)`)
	exec(`INSERT INTO whop_plans(store_id, plan_id, product_id, title, allowance_servers, allowance_memory_mb, allowance_from) VALUES
		('biz_app', 'plan_other', 'prod_other', 'Other', 1, 4096, 'store'), ('biz_app', 'plan_big', 'prod_big', 'Big', 2, 8192, 'store'),
		('biz_key', 'plan_starter', 'prod_mc', 'Starter', 1, 4096, 'store')`)
	given := map[string]string{
		"user_kim": "plan_other|1|4096|0", "user_dee": "plan_big+plan_other|3|12288|0", "user_alex": "plan_other|1|4096|0", "user_sam": "plan_small|1|4096|0",
		"user_ray": "plan_other|1|4096|0", "user_lee": "plan_big+plan_other|3|12288|0", "user_pat": "plan_other|1|4096|0",
		"user_max": strings.Repeat("plan_big+", 5) + "plan_big|10|49152|0",
	}
	for user, applied := range given {
		exec(`INSERT INTO whop_customers(store_id, whop_user_id, applied, paused, updated_at) VALUES('biz_app', ?, ?, ?, 1)`, user, applied, user == "user_pat")
	}
	exec(`INSERT INTO whop_customers(store_id, whop_user_id, applied, updated_at) VALUES('biz_key', 'user_kit', 'plan_starter|1|4096|0', 1)`)
	for _, m := range [][3]string{
		{"mem_kim", "user_kim", "plan_other"}, {"mem_dee1", "user_dee", "plan_big"}, {"mem_dee2", "user_dee", "plan_other"},
		{"mem_alex", "user_alex", "plan_other"}, {"mem_alex2", "user_alex", "plan_big"}, {"mem_sam", "user_sam", "plan_big"},
		{"mem_ray1", "user_ray", "plan_other"}, {"mem_ray2", "user_ray", "plan_other"},
		{"mem_lee1", "user_lee", "plan_big"}, {"mem_lee2", "user_lee", "plan_other"}, {"mem_lee3", "user_lee", "plan_other"}, {"mem_pat", "user_pat", "plan_other"},
		{"mem_max1", "user_max", "plan_big"}, {"mem_max2", "user_max", "plan_big"}, {"mem_max3", "user_max", "plan_big"},
		{"mem_max4", "user_max", "plan_big"}, {"mem_max5", "user_max", "plan_big"}, {"mem_max6", "user_max", "plan_big"},
	} {
		exec(`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at) VALUES('biz_app', ?, ?, ?, 'active', 1)`, m[0], m[1], m[2])
	}
	exec(`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at) VALUES('biz_key', 'mem_kit', 'user_kit', 'plan_starter', 'active', 1)`)
	db.Close()
	if db, err = store.Open(path, panelMigrations); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT membership_id, paid_plan_id, paid_title, paid_servers, paid_mb, paid_disk_gb FROM whop_membership_checks WHERE answered = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, plan, title string
		var servers, mb, disk int
		if err := rows.Scan(&id, &plan, &title, &servers, &mb, &disk); err != nil {
			t.Fatal(err)
		}
		got[id] = fmt.Sprintf("%s %s %d/%d/%d", plan, title, servers, mb, disk)
	}
	want := map[string]string{
		"mem_kim": "plan_other Other 1/4096/0", "mem_dee1": "plan_big Big 2/8192/0", "mem_dee2": "plan_other Other 1/4096/0",
		"mem_alex": "plan_other Other 1/4096/0", "mem_sam": "plan_small plan_small 1/4096/0",
	}
	for i := 1; i <= 6; i++ {
		want[fmt.Sprintf("mem_max%d", i)] = "plan_big Big 2/8192/0"
	}
	if !maps.Equal(got, want) {
		t.Fatalf("kept as paid: %v, want %v", got, want)
	}
}

// A customer already started, one of whose memberships the payment check
// has no answer for yet, as when the upgrade couldn't match what they hold
// with what they were given, isn't paused while that answer can't be had:
// while their store is closed, or while Whop fails the check. Once Whop
// answers that it was paid, they go on as they were.
func TestAStartedCustomerIsntPausedWhileTheirPaymentCantBeChecked(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	pass := func() []string {
		n := len(core.got())
		e.clock.add(2 * whopPollEvery)
		e.reconcile()
		return core.got()[n:]
	}
	if calls := pass(); !calledFor(calls, "start ", "user_kim") {
		t.Fatalf("kim didn't start: %q", calls)
	}
	if _, err := e.srv.db.Exec(`DELETE FROM whop_membership_checks WHERE membership_id = 'mem_kim'`); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.closeWhopStore(t.Context(), "biz_other", "test", "Closed for the test."); err != nil {
		t.Fatal(err)
	}
	if calls := pass(); len(calls) > 0 {
		t.Fatalf("with the store closed and kim's payment unchecked: the core's calls %q", calls)
	}
	if _, err := e.srv.openWhopStore(t.Context(), "biz_other", "test"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	b.paymentsDown = true
	f.mu.Unlock()
	if calls := pass(); len(calls) > 0 {
		t.Fatalf("while Whop fails kim's payment check: the core's calls %q", calls)
	}
	f.mu.Lock()
	b.paymentsDown = false
	f.mu.Unlock()
	if calls := pass(); len(calls) > 0 {
		t.Fatalf("once Whop answers that kim's membership was paid: the core's calls %q", calls)
	}
	var paid int
	if err := e.srv.db.QueryRow(`SELECT paid_mb FROM whop_membership_checks WHERE membership_id = 'mem_kim' AND answered = 1`).Scan(&paid); err != nil || paid != 4096 {
		t.Fatalf("kim's membership as checked: %d, %v", paid, err)
	}
}

// A payment check waiting for its next try is tried at once when the
// dashboard starts again (retryWhopNow), as a customer's call is, so a
// release or the end of a Whop outage needs no wait.
func TestAPaymentCheckWaitingIsTriedAtOnceOnStartingAgain(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	b.paymentsDown = true
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if calledFor(core.got(), "start ", "user_kim") {
		t.Fatal("kim started while Whop failed their payment check")
	}
	f.mu.Lock()
	b.paymentsDown = false
	f.mu.Unlock()
	e.clock.add(time.Second)
	e.srv.retryWhopNow()
	e.reconcile()
	if !calledFor(core.got(), "start ", "user_kim") {
		t.Fatalf("starting again didn't try kim's payment check at once: %q", core.got())
	}
}

// A hosting plan that shares its product with another plan on sale makes
// the store need a look, in both plans' words, on its seller's page too,
// from the read that found them, even when Whop then fails the share
// check. It stays open, since the product's share is its neediest plan's.
// Once the other plan is archived, so no longer on sale, the problem goes,
// though someone still has it.
func TestAProductSharedWithAnotherPlanNeedsALookButStaysOpen(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	b.plans = append(b.plans, map[string]any{"id": "plan_dear", "title": "Dear", "visibility": "visible", "plan_type": "renewal", "billing_period": 30,
		"currency": "usd", "renewal_price": 15, "product": map[string]any{"id": "prod_other", "title": "Minecraft server"},
		"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}, "unlimited_stock": true})
	b.sharesDown = true
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if st := e.otherStore(t); st.ClosedWhy != "" || !strings.Contains(st.Problem, "Other: It shares its product, Minecraft server, with Dear") ||
		!strings.Contains(st.Problem, "Dear: It shares its product, Minecraft server, with Other") {
		t.Fatalf("Other and Dear on one product, while Whop fails the share check: closed %q, problem %q", st.ClosedWhy, st.Problem)
	}
	if v, err := e.srv.sellerStoreView(t.Context(), "biz_other"); err != nil || v.Store.State != "needsLook" || !strings.Contains(v.Store.Why, "with Dear") {
		t.Fatalf("Other's seller page: %+v, %v", v.Store, err)
	}
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_dear", "active")
	f.mu.Lock()
	b.sharesDown = false
	b.plan("plan_dear")["visibility"] = "archived"
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if st := e.otherStore(t); st.Problem != "" || st.ClosedWhy != "" {
		t.Fatalf("once Dear's archived, though kim has it: problem %q, closed %q", st.Problem, st.ClosedWhy)
	}
}

// A store that isn't open yet has no share to check: its seller's Open the
// store sets it.
func TestAStoreNotOpenYetHasNoShareChecked(t *testing.T) {
	f, e, _ := twoStores(t)
	if err := e.srv.closeWhopStore(t.Context(), "biz_other", whopNotOpenYet, whopNotOpenYetWhy); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.installed["biz_other"].shares = nil
	writes := len(f.shareWrites)
	f.mu.Unlock()
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if st := e.otherStore(t); st.ClosedWhy != whopNotOpenYetWhy || len(f.shareWrites) != writes {
		t.Fatalf("a store not open yet: closed %q, share writes %v", st.ClosedWhy, f.shareWrites)
	}
	if closed, err := e.srv.whopClosedFor(t.Context(), "biz_other", whopShareClosed); err != nil || closed {
		t.Fatalf("closed for its share: %v, %v", closed, err)
	}
}

// A store whose grant has lacked what its pass reads for a week leaves, as
// after the app was uninstalled; a grant back in the meantime starts the
// week again, and a grant Whop couldn't check counts neither way.
func TestAnAppStoreWhoseGrantIsGoneForAWeekLeaves(t *testing.T) {
	f, e, _ := twoStores(t)
	grant := func(revoked, down bool) {
		f.mu.Lock()
		f.installed["biz_other"].revoked, f.permissionsDown = revoked, down
		f.mu.Unlock()
	}
	e.reconcile()
	grant(true, false)
	e.clock.add(time.Minute)
	e.reconcile()
	e.clock.add(whopGrantGrace - time.Hour)
	e.reconcile()
	if st := e.otherStore(t); !st.LeftAt.IsZero() {
		t.Fatalf("left before a week: %q", st.LeftWhy)
	}
	grant(false, false)
	e.clock.add(time.Minute)
	e.reconcile()
	grant(true, false)
	e.clock.add(2 * time.Hour)
	e.reconcile()
	if st := e.otherStore(t); !st.LeftAt.IsZero() {
		t.Fatalf("left though the grant came back meanwhile: %q", st.LeftWhy)
	}
	grant(true, true)
	e.clock.add(whopGrantGrace)
	e.reconcile()
	if st := e.otherStore(t); !st.LeftAt.IsZero() {
		t.Fatalf("left on a grant Whop couldn't check: %q", st.LeftWhy)
	}
	grant(true, false)
	e.reconcile()
	if st := e.otherStore(t); st.LeftAt.IsZero() || !strings.Contains(st.LeftWhy, "lacked plan:basic:read, member:basic:read for 7 days") {
		t.Fatalf("after a week without its grant: left %v, %q", st.LeftAt, st.LeftWhy)
	}
}

// A customer starts only once their membership's payment carried
// Playkeeper's share, and their plan grows only once the new membership's
// did.
func TestACustomerStartsOrGrowsOnlyOnPaymentsThatCarriedTheShare(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	fee := func(pay, amount string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.installed["biz_other"].fees[pay][0]["settlement_amount"] = map[string]any{"amount": amount, "currency": "usd", "decimals": 2}
	}
	problem := func() string {
		var p string
		e.srv.db.QueryRow(`SELECT problem FROM whop_customers WHERE store_id = 'biz_other' AND whop_user_id = 'user_kim'`).Scan(&p)
		return p
	}
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	fee("pay_mem_kim", "0.00")
	e.reconcile()
	if calledFor(core.got(), "start ", "user_kim") || !strings.Contains(problem(), "didn't carry Playkeeper's share of $8.50") {
		t.Fatalf("a payment without the share: %q, problem %q", core.got(), problem())
	}
	fee("pay_mem_kim", "8.50")
	e.clock.add(2 * time.Minute)
	e.reconcile()
	if !calledFor(core.got(), "start plan_other", "user_kim") {
		t.Fatalf("once the payment carried the share: %q", core.got())
	}
	f.mu.Lock()
	b := f.installed["biz_other"]
	b.plans = append(b.plans, map[string]any{"id": "plan_other_big", "title": "Other big", "visibility": "visible", "plan_type": "renewal", "billing_period": 30,
		"currency": "usd", "renewal_price": 24, "product": map[string]any{"id": "prod_other", "title": "Minecraft server"},
		"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "8"}, "unlimited_stock": true})
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim2", "user_kim", "plan_other_big", "active")
	fee("pay_mem_kim2", "8.50")
	e.clock.add(2 * whopPollEvery)
	e.reconcile()
	if calledFor(core.got(), "change to ", "user_kim") || !strings.Contains(problem(), "didn't carry Playkeeper's share of $17.00") {
		t.Fatalf("a bigger plan whose payment was short: %q, problem %q", core.got(), problem())
	}
	fee("pay_mem_kim2", "17.00")
	e.clock.add(2 * time.Minute)
	e.reconcile()
	if !calledFor(core.got(), "change to ", "user_kim") {
		t.Fatalf("once the bigger plan's payment carried the share: %q", core.got())
	}
}

// Every payment the checks read is kept for the seller's view, with
// Playkeeper's share from its fee lines: the one a customer started on, then
// each renewal read on the share check's schedule, even one paid on a retry
// long after it was made, and a refund of one, even once it settles after the
// reads have moved on. A sale of anything else the business sells isn't.
func TestEveryPaymentTheChecksReadIsKeptForTheSellersView(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	kept := func(pay string) (amount, share, refunded int64, user, plan string) {
		t.Helper()
		e.srv.db.QueryRow(`SELECT amount, share, refunded, whop_user_id, plan_id FROM whop_payments WHERE payment_id = ? AND store_id = 'biz_other' AND currency = 'usd'`, pay).
			Scan(&amount, &share, &refunded, &user, &plan)
		return
	}
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	e.reconcile()
	if amount, share, refunded, user, plan := kept("pay_mem_kim"); amount != 1200 || share != 850 || refunded != 0 || user != "user_kim" || plan != "plan_other" {
		t.Fatalf("the payment kim started on: %d paid, %d shared, %d refunded, by %q for %q", amount, share, refunded, user, plan)
	}
	// The renewal is read once; the read after next, over an hour on, goes
	// back only to an hour before the last, so its refund alone brings it
	// back. The guide the business also sells isn't hosting.
	f.mu.Lock()
	b := f.installed["biz_other"]
	at := e.clock.now().Add(time.Minute).UTC().Format(time.RFC3339)
	b.payments = append([]map[string]any{
		{"id": "pay_renew", "status": "paid", "membership_id": "mem_kim", "plan_id": "plan_other", "product_id": "prod_other", "created_at": at, "paid_at": at,
			"user": map[string]any{"id": "user_kim"}, "total": map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}},
		{"id": "pay_guide", "status": "paid", "plan_id": "plan_guide", "product_id": "prod_guide", "created_at": at, "paid_at": at,
			"user": map[string]any{"id": "user_kim"}, "total": map[string]any{"amount": "20.00", "currency": "usd", "decimals": 2}},
	}, b.payments...)
	b.fees["pay_renew"] = []map[string]any{{"type": "affiliate_program_fee", "origin": whopShareOrigin, "label": "Revenue share",
		"settlement_amount": map[string]any{"amount": "8.50", "currency": "usd", "decimals": 2}}}
	b.fees["pay_guide"] = []map[string]any{{"type": "processing_fee", "origin": "payment_processing_percentage_fee", "label": "Processing",
		"settlement_amount": map[string]any{"amount": "0.60", "currency": "usd", "decimals": 2}}}
	f.mu.Unlock()
	e.clock.add(90 * time.Minute)
	e.reconcile()
	if amount, share, refunded, _, _ := kept("pay_renew"); amount != 1200 || share != 850 || refunded != 0 {
		t.Fatalf("the renewal: %d paid, %d shared, %d refunded", amount, share, refunded)
	}
	if amount, _, _, _, _ := kept("pay_guide"); amount != 0 {
		t.Fatalf("a sale of the guide was kept as hosting: %d paid", amount)
	}
	// A refund asked for is read while it's pending, and a renewal is made
	// but its card declined. Both settle only once the reads have moved an
	// hour and a half on: the refund succeeds, and a retry pays the renewal.
	f.mu.Lock()
	at = e.clock.now().Add(time.Minute).UTC().Format(time.RFC3339)
	b.refunds = append(b.refunds, map[string]any{"id": "ref_1", "payment_id": "pay_renew", "status": "pending", "created_at": at})
	b.payments = append(b.payments, map[string]any{"id": "pay_retry", "status": "open", "membership_id": "mem_kim", "plan_id": "plan_other", "product_id": "prod_other",
		"created_at": at, "user": map[string]any{"id": "user_kim"}, "total": map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}})
	b.fees["pay_retry"] = b.fees["pay_renew"]
	f.mu.Unlock()
	e.clock.add(90 * time.Minute)
	e.reconcile()
	if _, _, refunded, _, _ := kept("pay_renew"); refunded != 0 {
		t.Fatalf("the renewal while its refund is pending: %d refunded", refunded)
	}
	if amount, _, _, _, _ := kept("pay_retry"); amount != 0 {
		t.Fatalf("a renewal not paid yet was kept: %d paid", amount)
	}
	f.mu.Lock()
	b.refunds[0]["status"] = "succeeded"
	b.payments[0]["refunded_amount"] = map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}
	retry := b.payments[len(b.payments)-1]
	retry["status"], retry["paid_at"] = "paid", e.clock.now().UTC().Format(time.RFC3339)
	f.mu.Unlock()
	e.clock.add(90 * time.Minute)
	e.reconcile()
	if _, _, refunded, _, _ := kept("pay_renew"); refunded != 1200 {
		t.Fatalf("the refunded renewal: %d refunded", refunded)
	}
	if amount, share, _, _, _ := kept("pay_retry"); amount != 1200 || share != 850 {
		t.Fatalf("the renewal paid on a retry: %d paid, %d shared", amount, share)
	}
}

// An app store's customer is hosted only by memberships whose payment
// carried Playkeeper's share: when their paid membership ends while another
// of the same size, never paid, goes on, they're paused, and they start
// again once a payment of it carries the share. A membership moved to a
// plan with more memory gives what it was paid for until a payment carries
// the share for its new plan.
func TestAnAppStoresCustomerIsHostedOnlyByPaidMemberships(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	pass := func() []string {
		n := len(core.got())
		e.clock.add(2 * whopPollEvery)
		e.reconcile()
		return core.got()[n:]
	}
	problem := func() string {
		var p string
		e.srv.db.QueryRow(`SELECT problem FROM whop_customers WHERE store_id = 'biz_other' AND whop_user_id = 'user_kim'`).Scan(&p)
		return p
	}
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	b := f.installed["biz_other"]
	for _, p := range []struct {
		id, gb string
		price  float64
	}{{"plan_other2", "4", 12}, {"plan_other_big", "8", 24}} {
		b.plans = append(b.plans, map[string]any{"id": p.id, "title": p.id, "visibility": "visible", "plan_type": "renewal", "billing_period": 30,
			"currency": "usd", "renewal_price": p.price, "product": map[string]any{"id": "prod_other", "title": "Minecraft server"},
			"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: p.gb}, "unlimited_stock": true})
	}
	f.mu.Unlock()
	pay := func(id, membership, plan, share string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		at := e.clock.now().UTC().Format(time.RFC3339)
		b.payments = append([]map[string]any{{"id": id, "status": "paid", "membership_id": membership, "plan_id": plan, "product_id": "prod_other",
			"created_at": at, "paid_at": at, "user": map[string]any{"id": "user_kim"}, "total": map[string]any{"amount": "24.00", "currency": "usd", "decimals": 2}}}, b.payments...)
		b.fees[id] = []map[string]any{{"type": "affiliate_program_fee", "origin": whopShareOrigin, "label": "Revenue share",
			"settlement_amount": map[string]any{"amount": share, "currency": "usd", "decimals": 2}}}
	}
	f.buyAt("biz_other", "mem_a", "user_kim", "plan_other", "active")
	if calls := pass(); !calledFor(calls, "start plan_other ", "user_kim") {
		t.Fatalf("kim on a paid membership: %q", calls)
	}
	f.mu.Lock()
	b.memberships["mem_b"] = map[string]any{"id": "mem_b", "status": "active", "plan_id": "plan_other2", "product_id": "prod_other", "user_id": "user_kim", "cancel_at_period_end": false}
	b.memberships["mem_a"]["status"] = "expired"
	f.mu.Unlock()
	if calls := pass(); !calledFor(calls, "pause ", "user_kim") || !strings.Contains(problem(), "no paid payment") {
		t.Fatalf("kim's paid membership ended while another, never paid, goes on: the core's calls %q, problem %q", calls, problem())
	}
	pay("pay_b", "mem_b", "plan_other2", "8.50")
	if calls := pass(); !calledFor(calls, "start plan_other2 ", "user_kim") || problem() != "" {
		t.Fatalf("once mem_b's payment carried the share: the core's calls %q, problem %q", calls, problem())
	}
	f.mu.Lock()
	b.memberships["mem_b"]["plan_id"] = "plan_other_big"
	f.mu.Unlock()
	if calls := pass(); len(calls) != 0 || !strings.Contains(problem(), "didn't carry Playkeeper's share of $17.00") {
		t.Fatalf("mem_b moved to 8 GB on a payment of the 4 GB share: the core's calls %q, problem %q", calls, problem())
	}
	pay("pay_big", "mem_b", "plan_other_big", "17.00")
	if calls := pass(); !calledFor(calls, "change to plan_other_big ", "user_kim") || problem() != "" {
		t.Fatalf("once a payment carried the 8 GB share: the core's calls %q, problem %q", calls, problem())
	}
}
