package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// A seller's view shows their own store alone: its plans with how many
// customers have each, its customers as they stand, and what it earned,
// with nothing of the other store the same buyer bought from, not even a
// membership of its plan kept for that store, nor the buyer's suspension
// there.
func TestASellersViewShowsTheirStoreAlone(t *testing.T) {
	_, e, own := storesWithCustomers(t)
	ctx := context.Background()
	paid := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	for _, p := range []whopPayment{
		{ID: "pay_other1", Store: "biz_other", WhopUserID: "user_alex", PlanID: "plan_other", Currency: "USD", Amount: 1500, Share: 850, PaidAt: paid},
		{ID: "pay_other2", Store: "biz_other", WhopUserID: "user_sam", PlanID: "plan_other", Currency: "usd", Amount: 1500, Share: 850, PaidAt: paid.Add(24 * time.Hour)},
		{ID: "pay_pip1", Store: testStore, WhopUserID: "user_alex", PlanID: "plan_starter", Currency: "usd", Amount: 800, PaidAt: paid},
	} {
		if err := e.srv.keepWhopPayment(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range [][2]string{{"biz_other", "user_sam"}, {testStore, "user_alex"}} {
		acct := storeAccount(t, e, c[0], c[1])
		if r := e.do(t, "POST", suspensionPath(acct.UserID), `{"reason":"griefing"}`, own.auth()); r.status != http.StatusOK {
			t.Fatalf("suspending %s at %s: %d %v", c[1], c[0], r.status, r.body)
		}
	}
	if err := e.srv.keepMembership(testStore, whop.Membership{ID: "mem_stray", UserID: "user_kim", PlanID: "plan_other", Status: "active"}, false); err != nil {
		t.Fatal(err)
	}
	v, err := e.srv.sellerStoreView(ctx, "biz_other")
	if err != nil {
		t.Fatal(err)
	}
	if v.Store.ID != "biz_other" || v.Store.Title != "Other Hosting" || v.Store.State != "selling" {
		t.Fatalf("the store: %+v", v.Store)
	}
	if len(v.Plans) != 1 || v.Plans[0].ID != "plan_other" || v.Plans[0].Customers != 2 || v.Plans[0].Servers != 1 || v.Plans[0].MemoryMB != 4096 {
		t.Fatalf("the plans: %+v", v.Plans)
	}
	statuses := map[string]string{}
	for _, c := range v.Customers {
		statuses[c.Handle] = c.Status
		if c.Since == nil || c.Plan == "" {
			t.Fatalf("a customer without their plan or since when: %+v", c)
		}
	}
	if len(statuses) != 2 || statuses["alexplays"] != "active" || statuses["samcrafts"] != "suspended" {
		t.Fatalf("the customers: %+v", v.Customers)
	}
	if want := []sellerMonth{{Month: "2026-09", Currency: "usd", Sales: 3000, Share: 1700, Kept: 1300}}; !slices.Equal(v.Earnings, want) {
		t.Fatalf("the earnings: %+v", v.Earnings)
	}
	pip, err := e.srv.sellerCustomers(ctx, testStore)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(pip, func(c sellerCustomer) bool { return c.Handle == "alexplays" && c.Status == "suspended" }) {
		t.Fatalf("Pip's customers: %+v", pip)
	}
}

// Each payment is kept once, by its id, for its store: kept again after a
// refund, it takes the newer amounts, but it never moves to another store,
// and one for a store the dashboard doesn't sell for, or whose amounts
// don't add up, isn't kept.
func TestKeepWhopPaymentKeepsEachPaymentOnceForItsStore(t *testing.T) {
	_, e, _ := twoStores(t)
	ctx := context.Background()
	p := whopPayment{ID: "pay_1", Store: "biz_other", WhopUserID: "user_alex", PlanID: "plan_other", Currency: "usd", Amount: 1500, Share: 850,
		PaidAt: time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)}
	if err := e.srv.keepWhopPayment(ctx, p); err != nil {
		t.Fatal(err)
	}
	refunded := p
	refunded.Refunded, refunded.Share = 1500, 0
	if err := e.srv.keepWhopPayment(ctx, refunded); err != nil {
		t.Fatal(err)
	}
	moved := p
	moved.Store = testStore
	stranger := p
	stranger.ID, stranger.Store = "pay_2", "biz_nobody"
	over := p
	over.ID, over.Refunded = "pay_3", 1600
	greedy := p
	greedy.ID, greedy.Share = "pay_4", 1600
	money := p
	money.ID, money.Currency = "pay_5", "dollars"
	for name, bad := range map[string]whopPayment{"moved to Pip": moved, "for a stranger": stranger, "refunding more than paid": over, "sharing more than paid": greedy, "in no currency": money} {
		if err := e.srv.keepWhopPayment(ctx, bad); err == nil {
			t.Fatalf("a payment %s was kept", name)
		}
	}
	var n int
	var store string
	var amount, share, back int64
	if err := e.srv.db.QueryRow(`SELECT COUNT(*), MIN(store_id), SUM(amount), SUM(share), SUM(refunded) FROM whop_payments`).Scan(&n, &store, &amount, &share, &back); err != nil {
		t.Fatal(err)
	}
	if n != 1 || store != "biz_other" || amount != 1500 || share != 0 || back != 1500 {
		t.Fatalf("payments kept: %d, for %s, %d paid, %d shared, %d refunded", n, store, amount, share, back)
	}
}

// The seller's view says how the store stands: selling, needing a look,
// closed and why, suspended without the owner's reason, or left and why.
// The dashboard's own store, and one it doesn't sell for, have none.
func TestASellersViewSaysHowTheStoreStands(t *testing.T) {
	_, e, own := twoStores(t)
	ctx := context.Background()
	stands := func(want, why string) {
		t.Helper()
		v, err := e.srv.sellerStoreView(ctx, "biz_other")
		if err != nil || v.Store.State != want || v.Store.Why != why {
			t.Fatalf("Other stands as %+v, %v; want %s %q", v.Store, err, want, why)
		}
	}
	stands("selling", "")
	if _, err := e.srv.db.Exec(`UPDATE whop_stores SET problem = 'Whop said: Something went wrong' WHERE store_id = 'biz_other'`); err != nil {
		t.Fatal(err)
	}
	stands("needsLook", "Whop said: Something went wrong")
	if err := e.srv.closeWhopStore(ctx, "biz_other", "share", "Playkeeper's share is gone from Other"); err != nil {
		t.Fatal(err)
	}
	stands("closed", "Playkeeper's share is gone from Other")
	if r := e.do(t, "POST", "/api/whop/stores/biz_other/suspension", `{"reason":"our own words"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending Other: %d %v", r.status, r.body)
	}
	stands("suspended", "")
	if err := e.srv.whopStoreLeft(ctx, "biz_other", "the app has been missing for 7 days"); err != nil {
		t.Fatal(err)
	}
	stands("left", "the app has been missing for 7 days")
	for _, store := range []string{testStore, "biz_nobody"} {
		if _, err := e.srv.sellerStoreView(ctx, store); !errors.Is(err, errNoSellerView) {
			t.Fatalf("the seller's view of %s: %v", store, err)
		}
	}
}

// A store's earnings add up by month and currency, the newest month first,
// with refunds taken off the sales.
func TestAStoresEarningsAddUpByMonth(t *testing.T) {
	_, e, _ := twoStores(t)
	ctx := context.Background()
	aug, sep := time.Date(2026, 8, 31, 23, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	for _, p := range []whopPayment{
		{ID: "pay_a", Store: "biz_other", WhopUserID: "user_alex", Currency: "usd", Amount: 1200, Share: 850, PaidAt: aug},
		{ID: "pay_b", Store: "biz_other", WhopUserID: "user_alex", Currency: "usd", Amount: 1500, Share: 850, PaidAt: sep},
		{ID: "pay_c", Store: "biz_other", WhopUserID: "user_sam", Currency: "usd", Amount: 1500, Refunded: 1500, PaidAt: sep},
		{ID: "pay_d", Store: "biz_other", WhopUserID: "user_kim", Currency: "eur", Amount: 1400, Share: 850, PaidAt: sep},
	} {
		if err := e.srv.keepWhopPayment(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.srv.storeEarnings(ctx, "biz_other")
	if err != nil {
		t.Fatal(err)
	}
	want := []sellerMonth{
		{Month: "2026-09", Currency: "eur", Sales: 1400, Share: 850, Kept: 550},
		{Month: "2026-09", Currency: "usd", Sales: 1500, Share: 850, Kept: 650},
		{Month: "2026-08", Currency: "usd", Sales: 1200, Share: 850, Kept: 350},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the earnings: %+v", got)
	}
	if pip, err := e.srv.storeEarnings(ctx, testStore); err != nil || len(pip) != 0 {
		t.Fatalf("Pip's earnings: %+v, %v", pip, err)
	}
}

// viewAsSeller is the seller's page reading the seller's view of the store,
// as it does through Whop's proxy: with the token, from the page itself.
func (e *env) viewAsSeller(t *testing.T, biz, token string, hdr map[string]string) resp {
	t.Helper()
	h := map[string]string{whop.UserTokenHeader: token, "X-Requested-With": "playkeeper", "Sec-Fetch-Site": "same-origin", "Origin": ""}
	for k, v := range hdr {
		h[k] = v
	}
	return e.do(t, "GET", whopSellerPrefix+biz, "", h)
}

// sellerViewOf is the seller's view a page read.
func sellerViewOf(t *testing.T, r resp) sellerView {
	t.Helper()
	var v sellerView
	b, err := json.Marshal(r.body)
	if err == nil {
		err = json.Unmarshal(b, &v)
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Once a seller opened their store from their Whop dashboard, their page
// reads the seller's view of it: closed until they open it, so its plan has
// no stock; the customer who bought it, who waits to be set up meanwhile;
// and what it earned. Before the store is open there's none.
func TestASellersPageReadsTheirStoresView(t *testing.T) {
	f, e := sellerEnv(t)
	token := f.sellerToken(e, whopTestApp, "user_otherowner")
	if r := e.viewAsSeller(t, "biz_other", token, nil); r.status != http.StatusNotFound || r.body["code"] != "not_found" {
		t.Fatalf("before the store is open: %d %v", r.status, r.body)
	}
	if r := e.openAsSeller(t, "biz_other", token, nil); r.status != http.StatusOK {
		t.Fatalf("opening the store: %d %v", r.status, r.body)
	}
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	e.reconcile()
	paid := e.clock.now()
	if err := e.srv.keepWhopPayment(t.Context(), whopPayment{ID: "pay_other1", Store: "biz_other", WhopUserID: "user_alex", PlanID: "plan_other", Currency: "usd", Amount: 1200, Share: 850, PaidAt: paid}); err != nil {
		t.Fatal(err)
	}
	r := e.viewAsSeller(t, "biz_other", token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("the view: %d %v", r.status, r.body)
	}
	v := sellerViewOf(t, r)
	if v.Store.ID != "biz_other" || v.Store.Title != "Other Hosting" || v.Store.State != "closed" || v.Store.Why != whopNotOpenYetWhy {
		t.Fatalf("the store: %+v", v.Store)
	}
	if len(v.Plans) != 1 || v.Plans[0].ID != "plan_other" || v.Plans[0].Customers != 1 || v.Plans[0].Servers != 1 || v.Plans[0].MemoryMB != 4096 || v.Plans[0].Stock != 0 || v.Plans[0].UnlimitedStock {
		t.Fatalf("the plans: %+v", v.Plans)
	}
	if len(v.Customers) != 1 || v.Customers[0].Handle == "" || v.Customers[0].Status != "starting" {
		t.Fatalf("the customers: %+v", v.Customers)
	}
	want := []sellerMonth{{Month: paid.UTC().Format("2006-01"), Currency: "usd", Sales: 1200, Share: 850, Kept: 350}}
	if !slices.Equal(v.Earnings, want) {
		t.Fatalf("the earnings: %+v", v.Earnings)
	}
}

// Only the business's team reads its store's view, only through Whop's
// proxy with the app's token, only from the page itself, and only by GET.
func TestOnlyTheBusinesssTeamReadsItsStoresView(t *testing.T) {
	f, e := sellerEnv(t)
	owner := f.sellerToken(e, whopTestApp, "user_otherowner")
	if r := e.openAsSeller(t, "biz_other", owner, nil); r.status != http.StatusOK {
		t.Fatalf("opening the store: %d %v", r.status, r.body)
	}
	f.buyAt("biz_other", "mem_other1", "user_alex", "plan_other", "active")
	for name, c := range map[string]struct {
		biz, token string
		hdr        map[string]string
		status     int
		code       string
	}{
		"no token":            {"biz_other", "", nil, http.StatusUnauthorized, "whop_token"},
		"another app's token": {"biz_other", f.sellerToken(e, "app_someoneelse", "user_otherowner"), nil, http.StatusUnauthorized, "whop_token"},
		"a buyer":             {"biz_other", f.sellerToken(e, whopTestApp, "user_alex"), nil, http.StatusForbidden, "whop_not_team"},
		"another business":    {"biz_pip", owner, nil, http.StatusForbidden, "whop_not_team"},
		"not from the page":   {"biz_other", owner, map[string]string{"X-Requested-With": ""}, http.StatusForbidden, "forbidden"},
		"from another site":   {"biz_other", owner, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden, "forbidden"},
		"not a business":      {"user_otherowner", owner, nil, http.StatusNotFound, ""},
		"a path under it":     {"biz_other/", owner, nil, http.StatusNotFound, ""},
	} {
		r := e.viewAsSeller(t, c.biz, c.token, c.hdr)
		if r.status != c.status || (c.code != "" && r.body["code"] != c.code) {
			t.Errorf("%s: %d %v", name, r.status, r.body)
		}
	}
	f.mu.Lock()
	f.team["biz_other"] = []string{"user_mod"}
	f.mu.Unlock()
	if r := e.viewAsSeller(t, "biz_other", f.sellerToken(e, whopTestApp, "user_mod"), nil); r.status != http.StatusOK || sellerViewOf(t, r).Store.ID != "biz_other" {
		t.Fatalf("someone on the team: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", whopSellerPrefix+"biz_other", `{}`, map[string]string{whop.UserTokenHeader: owner, "X-Requested-With": "playkeeper", "Origin": ""}); r.status != http.StatusMethodNotAllowed || r.header.Get("Allow") != http.MethodGet {
		t.Fatalf("POST to the view: %d %v", r.status, r.header)
	}
}
