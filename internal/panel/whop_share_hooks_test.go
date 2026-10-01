package panel

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

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
// since that keeps none of their servers running.
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
