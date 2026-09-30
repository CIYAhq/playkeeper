package panel

import (
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
// each renewal read on the share check's schedule, and a refund of one.
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
	// back.
	f.mu.Lock()
	b := f.installed["biz_other"]
	b.payments = append([]map[string]any{{"id": "pay_renew", "status": "paid", "membership_id": "mem_kim", "plan_id": "plan_other",
		"created_at": e.clock.now().Add(time.Minute).UTC().Format(time.RFC3339), "paid_at": e.clock.now().Add(time.Minute).UTC().Format(time.RFC3339),
		"user": map[string]any{"id": "user_kim"}, "total": map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}}}, b.payments...)
	b.fees["pay_renew"] = []map[string]any{{"type": "affiliate_program_fee", "origin": whopShareOrigin, "label": "Revenue share",
		"settlement_amount": map[string]any{"amount": "8.50", "currency": "usd", "decimals": 2}}}
	f.mu.Unlock()
	e.clock.add(90 * time.Minute)
	e.reconcile()
	if amount, share, refunded, _, _ := kept("pay_renew"); amount != 1200 || share != 850 || refunded != 0 {
		t.Fatalf("the renewal: %d paid, %d shared, %d refunded", amount, share, refunded)
	}
	f.mu.Lock()
	b.payments[0]["refunded_amount"] = map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}
	b.refunds = append(b.refunds, map[string]any{"id": "ref_1", "payment_id": "pay_renew", "status": "succeeded"})
	f.mu.Unlock()
	e.clock.add(90 * time.Minute)
	e.reconcile()
	if _, _, refunded, _, _ := kept("pay_renew"); refunded != 1200 {
		t.Fatalf("the refunded renewal: %d refunded", refunded)
	}
}

// A plan grows when it gives more servers or memory than the one applied.
func TestAPlanGrowsWithMoreServersOrMemory(t *testing.T) {
	applied := planKey(CustomerPlan{ID: "plan_a", Servers: 1, MemoryMB: 4096})
	for _, c := range []struct {
		p    CustomerPlan
		want bool
	}{
		{CustomerPlan{Servers: 2, MemoryMB: 4096}, true},
		{CustomerPlan{Servers: 1, MemoryMB: 8192}, true},
		{CustomerPlan{Servers: 1, MemoryMB: 4096, DiskGB: 40}, false},
		{CustomerPlan{Servers: 1, MemoryMB: 2048}, false},
	} {
		if got := whopPlanGrows(applied, c.p); got != c.want {
			t.Errorf("%+v after %q: %v", c.p, applied, got)
		}
	}
	if !whopPlanGrows("", CustomerPlan{Servers: 1, MemoryMB: 4096}) || !whopPlanGrows("a|b|c|d", CustomerPlan{Servers: 1, MemoryMB: 4096}) {
		t.Error("a plan applied in a shape that can't be read doesn't count as growing")
	}
}
