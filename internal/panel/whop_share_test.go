package panel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// shareEnv is twoStores with the owner's own Whop account, siyabuilt,
// named to receive Playkeeper's share, and Other Hosting's app store.
func shareEnv(t *testing.T) (*fakeWhop, *env, member, whopStore) {
	t.Helper()
	f, e, own := twoStores(t)
	f.mu.Lock()
	f.users["user_siya"] = "siyabuilt"
	f.mu.Unlock()
	if r := e.do(t, "PUT", "/api/whop/app", `{"shareUser":"@siyabuilt"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("naming who receives the share: %d %v", r.status, r.body)
	}
	st, ok, err := e.srv.whopStoreByID(t.Context(), "biz_other")
	if err != nil || !ok {
		t.Fatalf("Other Hosting's store: %v, %v", ok, err)
	}
	return f, e, own, st
}

// syncShares is the store's shares brought in line, as its pass does, or
// as Open the store does with force.
func (e *env) syncShares(t *testing.T, st whopStore, force bool) string {
	t.Helper()
	c, err := e.srv.whopClientFor(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	problem, err := e.srv.syncWhopShares(t.Context(), c, st, force)
	if err != nil {
		t.Fatalf("syncing the shares: %v", err)
	}
	return problem
}

// share is the store's revenue share on a product as Whop has it.
func (f *fakeWhop) share(biz, product string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.installed[biz].shares {
		if s["product_id"] == product {
			return s
		}
	}
	return nil
}

// Only the owner names who receives Playkeeper's share, a Whop user Whop
// knows, kept by id. Once a store pays it to them, it can't go to anyone
// else.
func TestOnlyTheOwnerNamesWhoReceivesPlaykeepersShare(t *testing.T) {
	f, e, own := twoStores(t)
	f.mu.Lock()
	f.users["user_siya"] = "siyabuilt"
	f.mu.Unlock()
	viewer := addMember(t, e, "vic", invites.RoleViewer, "*")
	if r := e.do(t, "PUT", "/api/whop/app", `{"shareUser":"siyabuilt"}`, viewer.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a viewer naming who receives the share: %d", r.status)
	}
	for body, status := range map[string]int{
		`{"shareUser":"nobody_here"}`:                              http.StatusBadRequest,
		`{"shareUser":"a b"}`:                                      http.StatusBadRequest,
		`{"shareUser":" @ "}`:                                      http.StatusBadRequest,
		`{"shareUser":"siyabuilt","key":"` + whopTestAppKey + `"}`: http.StatusBadRequest,
	} {
		if r := e.do(t, "PUT", "/api/whop/app", body, own.auth()); r.status != status {
			t.Errorf("%s: %d %v", body, r.status, r.body)
		}
	}
	if r := e.do(t, "PUT", "/api/whop/app", `{"shareUser":" @siyabuilt "}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("naming siyabuilt: %d %v", r.status, r.body)
	}
	var user, name, audited string
	e.srv.db.QueryRow(`SELECT share_user, share_username FROM whop_app WHERE id = 1`).Scan(&user, &name)
	e.srv.db.QueryRow(`SELECT group_concat(detail) FROM audit WHERE action = 'whop.app'`).Scan(&audited)
	if v := e.whopView(t, own).App; user != "user_siya" || name != "siyabuilt" || v.ShareUser != "siyabuilt" || !strings.Contains(audited, "@siyabuilt (user_siya)") {
		t.Fatalf("kept %q (%q), shown %+v, audited %q", user, name, v, audited)
	}
	st, _, _ := e.srv.whopStoreByID(t.Context(), "biz_other")
	e.syncShares(t, st, true)
	f.mu.Lock()
	f.users["user_jo"] = "joplays"
	f.mu.Unlock()
	for _, body := range []string{`{"shareUser":"joplays"}`, `{"shareUser":""}`} {
		if r := e.do(t, "PUT", "/api/whop/app", body, own.auth()); r.status != http.StatusConflict {
			t.Errorf("%s once a store pays siyabuilt: %d %v", body, r.status, r.body)
		}
	}
	if r := e.do(t, "PUT", "/api/whop/app", `{"shareUser":"siyabuilt"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("naming siyabuilt again: %d %v", r.status, r.body)
	}
}

// Opening a store makes the share's receiver its partner, and gives each
// hosting product the share that comes to $8.50 for each 4 GB its plan
// allows, before Whop's fees. The share follows the product's price when
// the seller changes it on Whop, and a product they add gets one.
func TestPlaykeepersShareIsSetOnEachHostingProductFromItsPrice(t *testing.T) {
	f, e, _, st := shareEnv(t)
	if problem := e.syncShares(t, st, true); problem != "" {
		t.Fatalf("opening the store: %s", problem)
	}
	s := f.share("biz_other", "prod_other")
	f.mu.Lock()
	partner := f.installed["biz_other"].partner
	f.mu.Unlock()
	if partner != "user_siya" || s == nil || s["commission_type"] != "percentage" || s["commission_value"] != 70.84 || s["revenue_basis"] != "pre_fees" || s["override_type"] != "rev_share" {
		t.Fatalf("the partner %q and the share %v", partner, s)
	}
	if problem := e.syncShares(t, st, false); problem != "" || len(f.shareWrites) != 1 {
		t.Fatalf("a pass with nothing changed: %q, writes %v", problem, f.shareWrites)
	}
	f.mu.Lock()
	b := f.installed["biz_other"]
	b.plans[0]["renewal_price"] = 15
	b.products["prod_big"] = whop.Metadata{}
	b.plans = append(b.plans, map[string]any{"id": "plan_big", "title": "Big", "visibility": "visible", "plan_type": "renewal", "billing_period": 30,
		"currency": "usd", "initial_price": 20, "renewal_price": 24, "product": map[string]any{"id": "prod_big", "title": "Big server"},
		"metadata": map[string]any{whop.MetaServers: "2", whop.MetaMemoryGB: "8"}})
	f.mu.Unlock()
	if problem := e.syncShares(t, st, false); problem != "" {
		t.Fatalf("after the seller's changes: %s", problem)
	}
	if s := f.share("biz_other", "prod_other"); s["commission_value"] != 56.67 {
		t.Fatalf("after the price went to $15: %v", s)
	}
	if s := f.share("biz_other", "prod_big"); s == nil || s["commission_value"] != 85.0 {
		t.Fatalf("the product the seller added, whose first payment is $20 for 8 GB: %v", s)
	}
	var bp int64
	e.srv.db.QueryRow(`SELECT basis_points FROM whop_shares WHERE store_id = 'biz_other' AND product_id = 'prod_other'`).Scan(&bp)
	if bp != 5667 {
		t.Fatalf("kept %d basis points", bp)
	}
}

// A product's share covers the neediest of its plans, from each plan's
// memory and the least it charges; archived plans, plans that allow
// nothing and plans of no product don't count.
func TestAProductsShareCoversEachOfItsPlans(t *testing.T) {
	plan := func(id, product string, first, renewal float64, gb string) whop.Plan {
		p := whop.Plan{ID: id, Title: id, Visibility: "visible", PlanType: "renewal", BillingPeriod: 30, Currency: "usd", InitialPrice: first, RenewalPrice: renewal,
			Product: whop.Ref{ID: product, Title: product}}
		if gb != "" {
			p.Metadata = whop.Metadata{whop.MetaServers: "1", whop.MetaMemoryGB: gb}
		}
		return p
	}
	archived := plan("old", "prod_a", 0, 9, "4")
	archived.Visibility = "archived"
	wants, problems := whopShareWants([]whop.Plan{
		plan("roomy", "prod_a", 0, 15, "6"),
		plan("small", "prod_a", 0, 12, "4"),
		archived,
		plan("bare", "prod_a", 0, 9, ""),
		plan("orphan", "", 0, 9, "4"),
		plan("big", "prod_b", 20, 24, "8"),
	})
	if len(problems) != 0 || len(wants) != 2 || wants[0] != (whopShareWant{Product: "prod_a", Title: "prod_a", BasisPoints: 8500}) ||
		wants[1] != (whopShareWant{Product: "prod_b", Title: "prod_b", BasisPoints: 8500}) {
		t.Fatalf("wants %+v, problems %v", wants, problems)
	}
	if _, problems := whopShareWants([]whop.Plan{plan("free", "prod_c", 0, 0, "4")}); len(problems) != 1 || !strings.Contains(problems[0], "free charges") {
		t.Fatalf("a free plan: %v", problems)
	}
}

// A share the seller removes, lowers below what the price needs, or
// changes from a percentage of the full price is a problem, and stays as
// they left it until Open the store sets it right. One they raise stays,
// even then. A plan that can't carry the share is a problem too.
func TestAShareTheSellerRemovesOrLowersIsAProblem(t *testing.T) {
	f, e, _, st := shareEnv(t)
	e.syncShares(t, st, true)
	edit := func(change func(b *fakeBusiness)) {
		f.mu.Lock()
		change(f.installed["biz_other"])
		f.mu.Unlock()
	}
	edit(func(b *fakeBusiness) { b.shares = nil })
	if problem := e.syncShares(t, st, false); !strings.Contains(problem, "Playkeeper's share on Minecraft server was removed") || f.share("biz_other", "prod_other") != nil {
		t.Fatalf("a removed share: %q", problem)
	}
	if problem := e.syncShares(t, st, true); problem != "" || f.share("biz_other", "prod_other")["commission_value"] != 70.84 {
		t.Fatalf("Open the store again: %q", problem)
	}
	edit(func(b *fakeBusiness) { b.shares[0]["commission_value"] = 50 })
	if problem := e.syncShares(t, st, false); problem != "Playkeeper's share on Minecraft server is 50%, under the 70.84% its price needs." {
		t.Fatalf("a lowered share: %q", problem)
	}
	if problem := e.syncShares(t, st, true); problem != "" || f.share("biz_other", "prod_other")["commission_value"] != 70.84 {
		t.Fatalf("Open the store after the share was lowered: %q, %v", problem, f.share("biz_other", "prod_other"))
	}
	edit(func(b *fakeBusiness) { b.shares[0]["commission_value"] = 80 })
	for _, force := range []bool{false, true} {
		if problem := e.syncShares(t, st, force); problem != "" || f.share("biz_other", "prod_other")["commission_value"] != 80 {
			t.Fatalf("a raised share, with force %v: %q, %v", force, problem, f.share("biz_other", "prod_other"))
		}
	}
	edit(func(b *fakeBusiness) {
		b.shares[0]["commission_value"], b.shares[0]["revenue_basis"] = 70.84, "post_fees"
	})
	if problem := e.syncShares(t, st, false); !strings.Contains(problem, "isn't a percentage of the full price") {
		t.Fatalf("a share of what's left after Whop's fees: %q", problem)
	}
	if problem := e.syncShares(t, st, true); problem != "" || f.share("biz_other", "prod_other")["revenue_basis"] != "pre_fees" || f.share("biz_other", "prod_other")["commission_value"] != 70.84 {
		t.Fatalf("Open the store after the share was changed: %q, %v", problem, f.share("biz_other", "prod_other"))
	}
	edit(func(b *fakeBusiness) { b.shares[0]["revenue_basis"], b.plans[0]["renewal_price"] = "pre_fees", 8 })
	if problem := e.syncShares(t, st, false); !strings.Contains(problem, "Other charges") || !strings.Contains(problem, "can't carry Playkeeper's share of $8.50") {
		t.Fatalf("a plan cheaper than the share: %q", problem)
	}
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET share_user = '', share_username = ''`); err != nil {
		t.Fatal(err)
	}
	if problem := e.syncShares(t, st, false); !strings.Contains(problem, "hasn't said who receives it") {
		t.Fatalf("with nobody to receive the share: %q", problem)
	}
}

// A customer starts only once their membership's latest paid payment
// carried Playkeeper's share for their plan, in a revenue share line, and
// every line it was checked with is kept.
func TestACustomerStartsOnlyWhenTheirPaymentCarriedPlaykeepersShare(t *testing.T) {
	f, e, _, st := shareEnv(t)
	c, err := e.srv.whopClientFor(t.Context(), st)
	if err != nil {
		t.Fatal(err)
	}
	paid := func(memoryMB int) error { return e.srv.whopSharePaid(t.Context(), c, st, "mem_1", memoryMB) }
	money := func(amount, currency string) map[string]any {
		return map[string]any{"amount": amount, "currency": currency, "decimals": 2}
	}
	pay := func(id string, lines ...map[string]any) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b := f.installed["biz_other"]
		b.payments = append([]map[string]any{{"id": id, "status": "paid", "membership_id": "mem_1", "plan_id": "plan_other", "total": money("12.00", "usd")}}, b.payments...)
		if b.fees == nil {
			b.fees = map[string][]map[string]any{}
		}
		b.fees[id] = append([]map[string]any{{"type": "whop_fee", "origin": "whop_processing_fee", "label": "Whop fee", "settlement_amount": money("0.36", "usd")}}, lines...)
	}
	share := func(amount, currency string) map[string]any {
		return map[string]any{"type": "affiliate_program_fee", "origin": "revshare_percentage_fee", "label": "Revenue share", "settlement_amount": money(amount, currency)}
	}
	if err := paid(4096); err == nil || !strings.Contains(err.Error(), "no paid payment") {
		t.Fatalf("before any payment: %v", err)
	}
	for _, c := range []struct {
		pay  string
		line map[string]any
	}{
		{"pay_none", nil},
		{"pay_short", share("8.49", "usd")},
		{"pay_eur", share("8.50", "eur")},
		{"pay_affiliate", map[string]any{"type": "affiliate_program_fee", "origin": "affiliate_fee", "label": "Affiliate", "settlement_amount": money("8.50", "usd")}},
	} {
		if c.line == nil {
			pay(c.pay)
		} else {
			pay(c.pay, c.line)
		}
		if err := paid(4096); err == nil || !strings.Contains(err.Error(), c.pay) || !strings.Contains(err.Error(), "didn't carry Playkeeper's share of $8.50") {
			t.Errorf("%s: %v", c.pay, err)
		}
	}
	pay("pay_good", share("8.50", "usd"))
	if err := paid(4096); err != nil {
		t.Fatalf("a payment that carried the share: %v", err)
	}
	if err := paid(8192); err == nil || !strings.Contains(err.Error(), "$17.00") {
		t.Fatalf("the same payment for an 8 GB plan: %v", err)
	}
	pay("pay_negative", share("-17.00", "usd"))
	if err := paid(8192); err != nil {
		t.Fatalf("a share written as money out of the payment: %v", err)
	}
	f.mu.Lock()
	f.installed["biz_other"].payments[0]["refunded_amount"] = money("12.00", "usd")
	f.mu.Unlock()
	if err := paid(8192); err == nil || !strings.Contains(err.Error(), "was refunded") {
		t.Fatalf("a refunded payment: %v", err)
	}
	var lines int
	var origins string
	e.srv.db.QueryRow(`SELECT COUNT(*), group_concat(line, ', ') FROM (SELECT origin || ' ' || amount || ' ' || currency AS line FROM whop_fee_lines
		WHERE payment_id = 'pay_good' ORDER BY n)`).Scan(&lines, &origins)
	if lines != 2 || origins != "whop_processing_fee 0.36 usd, revshare_percentage_fee 8.50 usd" {
		t.Fatalf("the lines kept of pay_good, checked twice: %d, %q", lines, origins)
	}
	if whopShareFor(4096) != 850 || whopShareFor(8192) != 1700 || whopShareFor(6144) != 1275 || whopShareFor(512) != 107 {
		t.Fatalf("the share for 4, 8, 6 and 0.5 GB: %d, %d, %d, %d", whopShareFor(4096), whopShareFor(8192), whopShareFor(6144), whopShareFor(512))
	}
}
