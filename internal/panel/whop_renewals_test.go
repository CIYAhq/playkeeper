package panel

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// renewalEnv is twoStores with kim started at Other Hosting on a payment
// made at the start, and what the tests of later payments use: pass, which
// moves the clock on and runs the reconciler, saying the core's calls it
// made; renew, which adds a payment of a membership at a time, with
// Playkeeper's share or without; and kimProblem, kim's line on the store's
// page.
type renewalEnv struct {
	f          *fakeWhop
	e          *env
	own        member
	start      time.Time
	pass       func(time.Duration) []string
	renew      func(id, membership, user string, at time.Time, share bool)
	kimProblem func() string
}

func newRenewalEnv(t *testing.T) renewalEnv {
	t.Helper()
	f, e, own := twoStores(t)
	core := useFakeCore(e)
	r := renewalEnv{f: f, e: e, own: own, start: e.clock.now()}
	r.pass = func(d time.Duration) []string {
		n := len(core.got())
		e.clock.add(d)
		e.reconcile()
		return core.got()[n:]
	}
	r.renew = func(id, membership, user string, at time.Time, share bool) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b := f.installed["biz_other"]
		when := at.UTC().Format(time.RFC3339)
		b.payments = append([]map[string]any{{"id": id, "status": "paid", "membership_id": membership, "plan_id": "plan_other", "product_id": "prod_other",
			"created_at": when, "paid_at": when, "user": map[string]any{"id": user}, "total": map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}}}, b.payments...)
		lines := []map[string]any{{"type": "processing_fee", "origin": "payment_processing_percentage_fee", "label": "Processing",
			"settlement_amount": map[string]any{"amount": "0.60", "currency": "usd", "decimals": 2}}}
		if share {
			lines = append(lines, map[string]any{"type": "affiliate_program_fee", "origin": whopShareOrigin, "label": "Revenue share",
				"settlement_amount": map[string]any{"amount": "8.50", "currency": "usd", "decimals": 2}})
		}
		b.fees[id] = lines
	}
	r.kimProblem = func() string {
		var problem string
		e.srv.db.QueryRow(`SELECT problem FROM whop_customers WHERE store_id = 'biz_other' AND whop_user_id = 'user_kim'`).Scan(&problem)
		return problem
	}
	f.mu.Lock()
	f.users["user_kim"] = "kimbuilds"
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_kim", "user_kim", "plan_other", "active")
	if calls := r.pass(time.Minute); !calledFor(calls, "start ", "user_kim") {
		t.Fatalf("kim didn't start: %q", calls)
	}
	return r
}

// A customer's membership gives them servers while its latest payment that
// carried Playkeeper's share is younger than the billing period and the
// renewal grace, 30 and 7 days. With no renewal since, they stop past it,
// and their line on the store's page says since when.
func TestAMembershipNoRenewalPaidStopsPastItsGrace(t *testing.T) {
	r := newRenewalEnv(t)
	if calls := r.pass(37*24*time.Hour - 2*time.Hour); len(calls) > 0 {
		t.Fatalf("37 days less an hour after kim's payment: the core's calls %q", calls)
	}
	if calls := r.pass(2 * time.Hour); !calledFor(calls, "pause (its latest payment on Whop (pay_mem_kim) was on 24 September, longer ago than it pays for", "user_kim") {
		t.Fatalf("an hour past kim's grace: the core's calls %q", calls)
	}
	if problem := r.kimProblem(); !strings.Contains(problem, "(pay_mem_kim) was on 24 September, longer ago than it pays for") {
		t.Fatalf("kim's line once paused: %q", problem)
	}
}

// A refund counts whatever the share check finds when it's read, since it
// only takes away: one read while Playkeeper's share is wrong still leaves
// the membership without the payment refunded. Its customer, already
// running, stays as they are until the check can look for the payment
// before, and runs on that payment once it does.
func TestARefundReadWhileTheShareIsWrongStillCounts(t *testing.T) {
	r := newRenewalEnv(t)
	renewed := r.start.Add(30 * 24 * time.Hour)
	r.renew("pay_renew", "mem_kim", "user_kim", renewed, true)
	r.pass(30*24*time.Hour + 20*time.Minute)
	r.f.mu.Lock()
	b := r.f.installed["biz_other"]
	kept := b.shares
	b.shares = nil
	b.refunds = append(b.refunds, map[string]any{"id": "ref_renew", "payment_id": "pay_renew", "status": "succeeded",
		"created_at": r.e.clock.now().UTC().Format(time.RFC3339)})
	b.payments[slices.IndexFunc(b.payments, func(p map[string]any) bool { return p["id"] == "pay_renew" })]["refunded_amount"] =
		map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}
	r.f.mu.Unlock()
	if calls := r.pass(20 * time.Minute); len(calls) > 0 {
		t.Fatalf("the renewal's refund read without the share: the core's calls %q", calls)
	}
	var paidAt int64
	var payment string
	if err := r.e.srv.db.QueryRow(`SELECT paid_at FROM whop_membership_checks WHERE membership_id = 'mem_kim'`).Scan(&paidAt); err != nil || paidAt == renewed.UnixMilli() {
		t.Fatalf("kim's membership once its renewal's refund was read without the share: paid %v, %v", time.UnixMilli(paidAt).UTC(), err)
	}
	r.f.mu.Lock()
	b.shares = kept
	r.f.mu.Unlock()
	if calls := r.pass(20 * time.Minute); len(calls) > 0 {
		t.Fatalf("once the share was right again: the core's calls %q", calls)
	}
	if err := r.e.srv.db.QueryRow(`SELECT paid_at, paid_payment FROM whop_membership_checks WHERE membership_id = 'mem_kim'`).Scan(&paidAt, &payment); err != nil ||
		paidAt != r.start.UnixMilli() || payment != "pay_mem_kim" {
		t.Fatalf("kim's membership once checked again: paid %v by %q, %v", time.UnixMilli(paidAt).UTC(), payment, err)
	}
	if calls := r.pass(7 * 24 * time.Hour); !calledFor(calls, "pause ", "user_kim") {
		t.Fatalf("past the grace of kim's first payment: the core's calls %q", calls)
	}
}

// A membership whose only payment is refunded in full stops at once, and
// gives nothing until a newer payment carries the share.
func TestAMembershipWhoseOnlyPaymentIsRefundedStops(t *testing.T) {
	r := newRenewalEnv(t)
	r.f.mu.Lock()
	b := r.f.installed["biz_other"]
	b.refunds = append(b.refunds, map[string]any{"id": "ref_kim", "payment_id": "pay_mem_kim", "status": "succeeded",
		"created_at": r.e.clock.now().UTC().Format(time.RFC3339)})
	b.payments[slices.IndexFunc(b.payments, func(p map[string]any) bool { return p["id"] == "pay_mem_kim" })]["refunded_amount"] =
		map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}
	r.f.mu.Unlock()
	if calls := r.pass(20 * time.Minute); !calledFor(calls, "pause ", "user_kim") {
		t.Fatalf("once kim's only payment was refunded: the core's calls %q", calls)
	}
	var paid int
	if err := r.e.srv.db.QueryRow(`SELECT paid_mb FROM whop_membership_checks WHERE membership_id = 'mem_kim'`).Scan(&paid); err != nil || paid != 0 {
		t.Fatalf("kim's membership once refused: paid for %d MB, %v", paid, err)
	}
	if problem := r.kimProblem(); !strings.Contains(problem, "(pay_mem_kim) was refunded") {
		t.Fatalf("kim's line once paused: %q", problem)
	}
}

// A renewal read while the share check finds Playkeeper's share wrong
// doesn't count for its membership: its revenue share line may have paid
// whoever the seller put in Playkeeper's place.
func TestARenewalReadWhileTheShareIsWrongDoesntCount(t *testing.T) {
	r := newRenewalEnv(t)
	r.f.mu.Lock()
	b := r.f.installed["biz_other"]
	kept := b.shares
	b.shares = nil
	r.f.mu.Unlock()
	r.renew("pay_renew", "mem_kim", "user_kim", r.start.Add(30*24*time.Hour), true)
	r.pass(30*24*time.Hour + 20*time.Minute)
	if st := r.e.otherStore(t); st.ClosedWhy == "" {
		t.Fatal("the store stayed open without Playkeeper's share")
	}
	var paidAt int64
	if err := r.e.srv.db.QueryRow(`SELECT paid_at FROM whop_membership_checks WHERE membership_id = 'mem_kim'`).Scan(&paidAt); err != nil || paidAt != r.start.UnixMilli() {
		t.Fatalf("kim's membership once the renewal was read without the share: paid %v, %v", time.UnixMilli(paidAt).UTC(), err)
	}
	r.f.mu.Lock()
	b.shares = kept
	r.f.mu.Unlock()
}

// A renewal that carried Playkeeper's share is read with the store's
// payments, on the share check's schedule, and keeps the membership for
// another billing period and grace, with no read of its own.
func TestARenewalThatCarriedTheShareKeepsItsMembership(t *testing.T) {
	r := newRenewalEnv(t)
	renewed := r.start.Add(30 * 24 * time.Hour)
	r.renew("pay_renew", "mem_kim", "user_kim", renewed, true)
	if calls := r.pass(30*24*time.Hour + 20*time.Minute); len(calls) > 0 {
		t.Fatalf("once kim renewed: the core's calls %q", calls)
	}
	var paidAt int64
	var payment string
	if err := r.e.srv.db.QueryRow(`SELECT paid_at, paid_payment FROM whop_membership_checks WHERE membership_id = 'mem_kim'`).Scan(&paidAt, &payment); err != nil ||
		paidAt != renewed.UnixMilli() || payment != "pay_renew" {
		t.Fatalf("kim's membership once the renewal was read: paid %v by %q, %v", time.UnixMilli(paidAt).UTC(), payment, err)
	}
	if calls := r.pass(10 * 24 * time.Hour); len(calls) > 0 {
		t.Fatalf("40 days after kim's first payment, 10 after the renewal: the core's calls %q", calls)
	}
	if calls := r.pass(27*24*time.Hour + time.Hour); !calledFor(calls, "pause ", "user_kim") {
		t.Fatalf("past the renewal's grace: the core's calls %q", calls)
	}
}

// A renewal that didn't carry Playkeeper's share doesn't count, nor does
// one refunded in full: a refund of the payment a membership was last paid
// with has its payment check look again at once, and it falls back on the
// payment before, so it stops once that payment's period and grace end,
// whichever order the payments read takes them in.
func TestARenewalWithoutTheShareOrRefundedDoesntCount(t *testing.T) {
	r := newRenewalEnv(t)
	r.f.mu.Lock()
	r.f.users["user_alex"] = "alexplays"
	r.f.mu.Unlock()
	r.f.buyAt("biz_other", "mem_alex", "user_alex", "plan_other", "active")
	if calls := r.pass(time.Minute); !calledFor(calls, "start ", "user_alex") {
		t.Fatalf("alex didn't start: %q", calls)
	}
	renewed := r.start.Add(30 * 24 * time.Hour)
	r.renew("pay_kim2", "mem_kim", "user_kim", renewed, false)
	r.renew("pay_alex2", "mem_alex", "user_alex", renewed, true)
	if calls := r.pass(30*24*time.Hour + 20*time.Minute); len(calls) > 0 {
		t.Fatalf("once both renewed: the core's calls %q", calls)
	}
	r.f.mu.Lock()
	b := r.f.installed["biz_other"]
	b.refunds = append(b.refunds, map[string]any{"id": "ref_alex2", "payment_id": "pay_alex2", "status": "succeeded",
		"created_at": r.e.clock.now().UTC().Format(time.RFC3339)})
	b.payments[slices.IndexFunc(b.payments, func(p map[string]any) bool { return p["id"] == "pay_alex2" })]["refunded_amount"] =
		map[string]any{"amount": "12.00", "currency": "usd", "decimals": 2}
	r.f.mu.Unlock()
	if calls := r.pass(20 * time.Minute); len(calls) > 0 {
		t.Fatalf("once alex's renewal was refunded, their first payment 30 days old: the core's calls %q", calls)
	}
	var paidAt int64
	var payment string
	if err := r.e.srv.db.QueryRow(`SELECT paid_at, paid_payment FROM whop_membership_checks WHERE membership_id = 'mem_alex'`).Scan(&paidAt, &payment); err != nil ||
		paidAt != r.start.UnixMilli() || payment != "pay_mem_alex" {
		t.Fatalf("alex's membership once its renewal was refunded: paid %v by %q, %v", time.UnixMilli(paidAt).UTC(), payment, err)
	}
	if calls := r.pass(7*24*time.Hour + time.Hour); !calledFor(calls, "pause ", "user_kim") || !calledFor(calls, "pause ", "user_alex") {
		t.Fatalf("past their first payments' grace: the core's calls %q", calls)
	}
	if problem := r.kimProblem(); !strings.Contains(problem, "(pay_kim2) didn't carry Playkeeper's share") {
		t.Fatalf("kim's line once paused: %q", problem)
	}
}

// The owner alone sets how many days a renewal may be late, 0 to 30, and
// the store's pass keeps to it.
func TestTheRenewalGraceIsTheOwnersToSet(t *testing.T) {
	r := newRenewalEnv(t)
	if v := r.e.whopView(t, r.own).App; v.RenewalGraceDays != whopRenewalGraceDays {
		t.Fatalf("the grace before the owner set it: %d", v.RenewalGraceDays)
	}
	viewer := addMember(t, r.e, "vic", invites.RoleViewer, "*")
	if res := r.e.do(t, "PUT", "/api/whop/app", `{"renewalGraceDays":2}`, viewer.auth()); res.status != http.StatusForbidden {
		t.Fatalf("a viewer setting the grace: %d", res.status)
	}
	for _, body := range []string{`{"renewalGraceDays":-1}`, `{"renewalGraceDays":31}`, `{"renewalGraceDays":2,"key":""}`, `{"renewalGraceDays":2,"shareUser":"siyabuilt"}`} {
		if res := r.e.do(t, "PUT", "/api/whop/app", body, r.own.auth()); res.status != http.StatusBadRequest {
			t.Fatalf("%s: %d %v", body, res.status, res.body)
		}
	}
	if res := r.e.do(t, "PUT", "/api/whop/app", `{"renewalGraceDays":2}`, r.own.auth()); res.status != http.StatusOK {
		t.Fatalf("setting the grace to 2 days: %d %v", res.status, res.body)
	}
	if v := r.e.whopView(t, r.own).App; v.RenewalGraceDays != 2 {
		t.Fatalf("the grace once set: %d", v.RenewalGraceDays)
	}
	if rows := r.e.auditRows(t, "whop.app"); !slices.ContainsFunc(rows, func(row string) bool { return strings.Contains(row, "a renewal may be 2 days late") }) {
		t.Fatalf("audit: %q", rows)
	}
	if calls := r.pass(32*24*time.Hour - 2*time.Hour); len(calls) > 0 {
		t.Fatalf("32 days less an hour after kim's payment: the core's calls %q", calls)
	}
	if calls := r.pass(2 * time.Hour); !calledFor(calls, "pause ", "user_kim") {
		t.Fatalf("an hour past a grace of 2 days: the core's calls %q", calls)
	}
}

// A restart needs a payment from its billing period: a membership the
// seller brings back without one doesn't start its customer again on the
// payment from before they were paused, though a customer still running
// would keep it through the grace, and one paid since does.
func TestARestartNeedsAPaymentFromItsBillingPeriod(t *testing.T) {
	r := newRenewalEnv(t)
	r.f.mu.Lock()
	r.f.installed["biz_other"].memberships["mem_kim"]["status"] = "expired"
	r.f.mu.Unlock()
	if calls := r.pass(31 * 24 * time.Hour); !calledFor(calls, "pause ", "user_kim") {
		t.Fatalf("once kim's membership expired: the core's calls %q", calls)
	}
	r.f.mu.Lock()
	r.f.installed["biz_other"].memberships["mem_kim"]["status"] = "active"
	r.f.mu.Unlock()
	if calls := r.pass(2 * 24 * time.Hour); len(calls) > 0 {
		t.Fatalf("kim's membership brought back without a payment: the core's calls %q", calls)
	}
	if problem := r.kimProblem(); !strings.Contains(problem, "longer ago than it pays for") {
		t.Fatalf("kim's line, brought back without a payment: %q", problem)
	}
	r.renew("pay_back", "mem_kim", "user_kim", r.e.clock.now().Add(time.Hour), true)
	if calls := r.pass(2 * time.Hour); !calledFor(calls, "start ", "user_kim") {
		t.Fatalf("once kim paid again: the core's calls %q", calls)
	}
}

// The upgrade counts each membership already found paid as paid on its
// day, so nobody's servers stop for it, and leaves the grace at 7 days.
func TestTheUpgradeCountsMembershipsFoundPaidAsPaidThatDay(t *testing.T) {
	at := slices.IndexFunc(panelMigrations, func(m string) bool { return strings.Contains(m, "renewal_grace_days") })
	if at < 0 {
		t.Fatal("no migration keeps the renewal grace")
	}
	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := store.Open(path, panelMigrations[:at])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO whop_app(id, api_key) VALUES(1, 'apik_x');
		INSERT INTO whop_membership_checks(store_id, membership_id, paid_plan_id, paid_mb, answered) VALUES('biz_app', 'mem_paid', 'plan_other', 4096, 1);
		INSERT INTO whop_membership_checks(store_id, membership_id, problem, answered) VALUES('biz_app', 'mem_unpaid', 'no share', 1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before := time.Now().Add(-time.Minute).UnixMilli()
	if db, err = store.Open(path, panelMigrations); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var paid, unpaid int64
	var grace int
	db.QueryRow(`SELECT paid_at FROM whop_membership_checks WHERE membership_id = 'mem_paid'`).Scan(&paid)
	db.QueryRow(`SELECT paid_at FROM whop_membership_checks WHERE membership_id = 'mem_unpaid'`).Scan(&unpaid)
	db.QueryRow(`SELECT renewal_grace_days FROM whop_app WHERE id = 1`).Scan(&grace)
	if paid < before || unpaid != 0 || grace != whopRenewalGraceDays {
		t.Fatalf("after the upgrade: paid %d (want at least %d), unpaid %d, grace %d", paid, before, unpaid, grace)
	}
}
