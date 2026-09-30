package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/store"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// plan is the business's plan id, nil when there's none.
func (b *fakeBusiness) plan(id string) map[string]any {
	for _, p := range b.plans {
		if p["id"] == id {
			return p
		}
	}
	return nil
}

// serveInstalled answers a request made with the Playkeeper Cloud app's
// key, which reaches a business that installed the app by the id the
// request names, and no other business; f.mu must be held.
func (f *fakeWhop) serveInstalled(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	route := r.Method + " " + r.URL.Path
	id := route[strings.LastIndexByte(route, '/')+1:]
	if name, ok := f.users[id]; ok && route == "GET /users/"+id {
		json.NewEncoder(w).Encode(map[string]any{"id": id, "username": name})
		return
	}
	biz := r.URL.Query().Get("account_id")
	if named := r.URL.Query().Get("resource_id"); named != "" {
		biz = named
	}
	if named, _ := body["account_id"].(string); named != "" {
		biz = named
	}
	for b, fb := range f.installed {
		switch {
		case route == "GET /accounts/"+b:
			biz = b
		case strings.HasPrefix(route, "GET /memberships/") && fb.memberships[id] != nil:
			biz = b
		case strings.HasPrefix(route, "PATCH /variants/") && fb.plan(id) != nil:
			biz = b
		}
	}
	b := f.installed[biz]
	if b == nil {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":{"type":"forbidden","message":"You do not have permission to access this resource"}}`)
		return
	}
	page := func(data any) {
		if b.revoked {
			data = []map[string]any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data, "page_info": map[string]any{"has_next_page": false}})
	}
	switch {
	case route == "GET /permissions" && f.permissionsDown:
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"type":"server_error","message":"Something went wrong"}}`)
	case route == "GET /permissions":
		var data []map[string]any
		for _, a := range strings.Split(r.URL.Query().Get("actions"), ",") {
			data = append(data, map[string]any{"action": a, "granted": !b.revoked})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	case b.revoked && strings.HasPrefix(route, "GET /memberships/"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"not_found","message":"No such membership"}}`)
	case route == "GET /accounts/"+biz:
		json.NewEncoder(w).Encode(b.account)
	case route == "GET /products":
		var data []map[string]any
		for _, p := range slices.Sorted(maps.Keys(b.products)) {
			data = append(data, map[string]any{"id": p, "title": "Minecraft server", "metadata": b.products[p]})
		}
		page(data)
	case route == "GET /variants":
		page(b.plans)
	case route == "GET /memberships":
		plan := r.URL.Query().Get("plan_id")
		var data []map[string]any
		for _, m := range b.memberships {
			if plan == "" || m["plan_id"] == plan {
				data = append(data, m)
			}
		}
		page(data)
	case strings.HasPrefix(route, "GET /memberships/"):
		json.NewEncoder(w).Encode(b.memberships[id])
	case strings.HasPrefix(route, "PATCH /variants/"):
		n, _ := body["stock"].(float64)
		p := b.plan(id)
		p["stock"], p["unlimited_stock"] = int(n), false
		f.stockSets = append(f.stockSets, fmt.Sprintf("%s=%d", id, int(n)))
		json.NewEncoder(w).Encode(p)
	case route == "POST /support_channels":
		user, _ := body["user_id"].(string)
		chat := "chan_" + biz + "_" + user
		f.chats[chat] = fakeChat{account: biz, user: user}
		json.NewEncoder(w).Encode(map[string]any{"id": chat})
	case route == "POST /access_tokens":
		owner, _ := b.account["owner"].(map[string]any)
		user, _ := body["user_id"].(string)
		var actions []string
		for _, a := range body["scoped_actions"].([]any) {
			actions = append(actions, fmt.Sprint(a))
		}
		if user != owner["id"] || len(actions) == 0 {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"error":{"type":"forbidden","message":"You do not have permission to access this resource"}}`)
			return
		}
		token := fmt.Sprintf("ut_%d", len(f.tokens)+1)
		f.tokens[token] = fakeToken{user: user, account: biz, actions: actions}
		json.NewEncoder(w).Encode(map[string]any{"token": token, "expires_at": "2026-09-30T13:00:00Z"})
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"type":"not_found","message":"No such route"}}`)
	}
}

// installOther has Other Hosting install the Playkeeper Cloud app. Its
// store sells one product with one plan, Other, whose metadata says it
// allows a server with 4 GB.
func (f *fakeWhop) installOther() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed["biz_other"] = &fakeBusiness{
		account: map[string]any{"id": "biz_other", "title": "Other Hosting", "route": "other-hosting",
			"owner": map[string]any{"id": "user_otherowner", "username": "otherowner", "name": "Other"}},
		products: map[string]whop.Metadata{"prod_other": {}},
		plans: []map[string]any{{"id": "plan_other", "title": "Other", "visibility": "visible", "plan_type": "renewal", "billing_period": 30,
			"currency": "usd", "renewal_price": 12, "product": map[string]any{"id": "prod_other", "title": "Minecraft server"},
			"metadata": map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4"}, "unlimited_stock": true}},
		memberships: map[string]map[string]any{},
	}
}

// buyAt is buy at a business that installed the app.
func (f *fakeWhop) buyAt(biz, id, user, plan, status string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]any{"id": id, "status": status, "plan_id": plan, "product_id": "prod_other", "user_id": user, "cancel_at_period_end": false}
	f.installed[biz].memberships[id] = m
	return m
}

// sentIn is what went to the support chat of the business that installed
// the app with user.
func (f *fakeWhop) sentIn(biz, user string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages["chan_"+biz+"_"+user]...)
}

// twoStores is a dashboard selling for Pip Hosting with its own key, and for
// Other Hosting, which installed the Playkeeper Cloud app, with the app's
// key.
func twoStores(t *testing.T) (*fakeWhop, *env, member) {
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

// storesOf maps what q lists, an id and a store id, to its store.
func storesOf(t *testing.T, e *env, q string) map[string]string {
	t.Helper()
	rows, err := e.srv.db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		out[id] = st
	}
	return out
}

// Each store's customers are its own: someone who buys at two stores is
// started at each for that store's plan, as two customers of the core. One
// store's plans never count for another's customers, a plan ending at one
// store pauses them there alone, and Settings › Sell on Whop shows the key
// store's plans and customers.
func TestEachStoreStartsItsOwnCustomers(t *testing.T) {
	f, e, own := twoStores(t)
	core := useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	want := []string{
		"start plan_starter (Starter) 1/4096/0 for whop/biz_pip/user_alex alexplays",
		"start plan_other (Other) 1/4096/0 for whop/biz_other/user_alex alexplays",
	}
	if got := core.got(); !slices.Equal(got, want) {
		t.Fatalf("the core's calls: %q", got)
	}
	if got := storesOf(t, e, `SELECT membership_id, store_id FROM whop_memberships`); !maps.Equal(got, map[string]string{"mem_alex1": testStore, "mem_alex2": "biz_other"}) {
		t.Fatalf("the memberships' stores: %v", got)
	}
	if got := storesOf(t, e, `SELECT plan_id, store_id FROM whop_plans`); got["plan_starter"] != testStore || got["plan_other"] != "biz_other" {
		t.Fatalf("the plans' stores: %v", got)
	}
	if got := storesOf(t, e, `SELECT store_id || '/' || whop_user_id, store_id FROM whop_customers`); len(got) != 2 {
		t.Fatalf("the customers: %v", got)
	}
	v := e.whopView(t, own)
	var plans []string
	for _, p := range v.Plans {
		plans = append(plans, p.ID)
	}
	if !slices.Equal(plans, []string{"plan_starter", "plan_big"}) || len(v.Customers) != 1 || v.Customers[0].Plan != "Starter" {
		t.Fatalf("Settings › Sell on Whop shows plans %v and customers %+v", plans, v.Customers)
	}

	e.deliver(t, "msg_1", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "expired"))
	e.reconcile()
	if got := core.got(); len(got) != 3 || got[2] != "pause (their Whop membership is expired) whop/biz_pip/user_alex alexplays" {
		t.Fatalf("after alex's plan at Pip ended: %q", got)
	}
}

// What the core tells a customer goes out in the chat of the store they
// bought from, as that store's owner, and in no other store's chat, even
// for someone who bought at both. A store the dashboard doesn't sell for
// takes no message.
func TestAStoresMessagesGoOutInItsOwnChats(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	ctx := context.Background()
	for _, m := range []struct{ store, text string }{{testStore, "from Pip"}, {"biz_other", "from Other"}} {
		if err := e.srv.notifier.Notify(ctx, Customer{Provider: whopProvider, Store: m.store, Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: m.text}); err != nil {
			t.Fatal(err)
		}
	}
	e.reconcile()
	if pip, other := f.sent("user_alex"), f.sentIn("biz_other", "user_alex"); !slices.Equal(pip, []string{"from Pip"}) || !slices.Equal(other, []string{"from Other"}) {
		t.Fatalf("Pip's chat has %q and Other's %q", pip, other)
	}
	f.mu.Lock()
	senders := f.senders["chan_biz_other_user_alex"]
	f.mu.Unlock()
	if !slices.Equal(senders, []string{"user_otherowner"}) {
		t.Fatalf("Other's message went out as %q", senders)
	}
	if err := e.srv.notifier.Notify(ctx, Customer{Provider: whopProvider, Store: "biz_nobody", Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: "hi"}); !errors.Is(err, errNotThisStore) {
		t.Fatalf("a message for a store the dashboard doesn't sell for: %v", err)
	}
}

// Cancelling the plan at one store is reminded there, while a plan at
// another store goes on, since that plan keeps none of this store's
// servers running.
func TestACancellationAtOneStoreIsRemindedThere(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	f.mu.Lock()
	m := f.memberships["mem_alex1"]
	m["status"], m["cancel_at_period_end"], m["current_period_end"] = "canceling", true, "2026-10-12T09:00:00Z"
	f.mu.Unlock()
	e.deliver(t, "msg_1", whop.EventMembershipCancelling, m)
	e.reconcile()
	e.reconcile()
	if pip, other := f.sent("user_alex"), f.sentIn("biz_other", "user_alex"); len(pip) != 1 || !strings.Contains(pip[0], "You cancelled your Pip Hosting plan") || len(other) != 0 {
		t.Fatalf("Pip's chat has %q and Other's %q", pip, other)
	}
}

// A membership stays with the store it was heard of from: the key store's
// webhook keeps no event of another business, and a membership one store
// has is never another's.
func TestAMembershipStaysWithItsStore(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	m := f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	if r := e.deliverFor(t, "biz_other", "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("delivery: %d %v", r.status, r.body)
	}
	if got := storesOf(t, e, `SELECT membership_id, store_id FROM whop_memberships`); len(got) != 0 {
		t.Fatalf("the key store's webhook kept another business's membership: %v", got)
	}
	e.reconcile()
	if err := e.srv.keepMembership(testStore, whop.Membership{ID: "mem_alex2", UserID: "user_alex", PlanID: "plan_other", Status: "expired"}, false); err != nil {
		t.Fatal(err)
	}
	var st, status string
	if err := e.srv.db.QueryRow(`SELECT store_id, status FROM whop_memberships WHERE membership_id = 'mem_alex2'`).Scan(&st, &status); err != nil || st != "biz_other" || status != "active" {
		t.Fatalf("Other's membership, once another store kept it: %s %s, %v", st, status, err)
	}
}

// Reading one store leaves every other store's plans as they were, even
// one it names too.
func TestReadingOneStoreLeavesAnothersPlans(t *testing.T) {
	f, e, own := twoStores(t)
	core := useFakeCore(e)
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	f.mu.Lock()
	f.plans = append(f.plans, map[string]any{"id": "plan_other", "title": "Pip's copy", "visibility": "visible", "product": map[string]any{"id": "prod_mc"},
		"metadata": map[string]any{whop.MetaServers: "3", whop.MetaMemoryGB: "12"}})
	f.mu.Unlock()
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("reading Pip's store again: %d %v", r.status, r.body)
	}
	var st, title string
	var servers int
	if err := e.srv.db.QueryRow(`SELECT store_id, title, allowance_servers FROM whop_plans WHERE plan_id = 'plan_other'`).Scan(&st, &title, &servers); err != nil ||
		st != "biz_other" || title != "Other" || servers != 1 {
		t.Fatalf("Other's plan after Pip's store was read: %s %q %d, %v", st, title, servers, err)
	}
	e.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the core's calls: %q", got)
	}
}

// Disconnecting the key store forgets all the dashboard kept of it, and
// nothing of another store, which goes on selling, with Sign in with Whop.
func TestDisconnectingTheKeyStoreLeavesTheOtherStores(t *testing.T) {
	f, e, own := twoStores(t)
	useFakeCore(e)
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	e.srv.sales.SetAvailability(context.Background(), map[string]int{"plan_starter": 3, "plan_other": 2})
	for _, st := range []string{testStore, "biz_other"} {
		if err := e.srv.notifier.Notify(context.Background(), Customer{Provider: whopProvider, Store: st, Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	if r := e.do(t, "DELETE", "/api/whop", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("disconnecting: %d %v", r.status, r.body)
	}
	for _, table := range []string{"whop_plans", "whop_memberships", "whop_customers", "whop_messages", "whop_stock", "whop_stores"} {
		if got := storesOf(t, e, `SELECT DISTINCT store_id, store_id FROM `+table); !maps.Equal(got, map[string]string{"biz_other": "biz_other"}) {
			t.Errorf("%s after disconnecting Pip: %v", table, got)
		}
	}
	if !e.srv.whopSignInOn() {
		t.Fatal("Sign in with Whop went with Pip, though Other Hosting still sells")
	}
	// A business that sells through the app needs no key of its own.
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopOtherKey+`"}`, own.auth()); r.status != http.StatusConflict || !strings.Contains(fmt.Sprint(r.body["error"]), "sells through the Playkeeper Cloud app") {
		t.Fatalf("connecting Other Hosting's own key: %d %v", r.status, r.body)
	}
}

// Each store's plans are for sale with their store, and each plan's stock
// goes to its own store with that store's key, counting that store's
// customers' purchases alone.
func TestEachStoresStockIsItsOwn(t *testing.T) {
	f, e, _ := twoStores(t)
	useFakeCore(e)
	ctx := context.Background()
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	plans, err := e.srv.sales.SalePlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stores := map[string]string{}
	for _, p := range plans {
		stores[p.ID] = p.Store
	}
	if !maps.Equal(stores, map[string]string{"plan_starter": testStore, "plan_big": testStore, "plan_other": "biz_other"}) {
		t.Fatalf("the plans for sale: %v", stores)
	}
	e.clock.add(2 * whopPollEvery)
	if err := e.srv.sales.SetAvailability(ctx, map[string]int{"plan_starter": 3, "plan_other": 2}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if got := storesOf(t, e, `SELECT plan_id, store_id FROM whop_stock`); !maps.Equal(got, map[string]string{"plan_starter": testStore, "plan_other": "biz_other"}) {
		t.Fatalf("the stock kept: %v", got)
	}
	if writes := f.stockWrites(); !slices.Contains(writes, "plan_starter=3") || !slices.Contains(writes, "plan_other=2") || len(writes) != 2 {
		t.Fatalf("stock written: %v", writes)
	}
}

// A store another dashboard took over sells on that dashboard's machines,
// so its plans take none of this one's room from the stores it still sells
// for.
func TestATakenOverStoresPlansTakeNoRoom(t *testing.T) {
	f, e, _ := twoStores(t)
	ctx := context.Background()
	e.reconcile()
	b, ownB := secondDashboard(t, f)
	if r := b.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`","takeOver":true}`, ownB.auth()); r.status != http.StatusOK {
		t.Fatalf("taking Pip over: %d %v", r.status, r.body)
	}
	e.clock.add(whopPollEvery)
	e.reconcile()
	plans, err := e.srv.sales.SalePlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stores := map[string]string{}
	for _, p := range plans {
		stores[p.ID] = p.Store
	}
	if taken := e.srv.whopTakenOver(testStore); !taken || !maps.Equal(stores, map[string]string{"plan_other": "biz_other"}) {
		t.Fatalf("Pip taken over: %v; the plans for sale: %v", taken, stores)
	}
}

// A store's delivery or message hurries that store's pass alone, so a buyer
// doesn't wait on every store's; a kick for every store looks at them all.
func TestAKickHurriesItsOwnStoresPass(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	e.reconcile()
	e.srv.takeWhopKicks()
	e.deliver(t, "msg_1", whop.EventMembershipActivated, f.buy("mem_alex1", "user_alex", "plan_starter", "active"))
	if only := e.srv.takeWhopKicks(); !maps.Equal(only, map[string]bool{testStore: true}) {
		t.Fatalf("Pip's delivery hurried %v", only)
	}
	if err := e.srv.notifier.Notify(ctx, Customer{Provider: whopProvider, Store: "biz_other", Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if only := e.srv.takeWhopKicks(); !maps.Equal(only, map[string]bool{"biz_other": true}) {
		t.Fatalf("a message at Other hurried %v", only)
	}
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	if err := e.srv.keepMembership("biz_other", whop.Membership{ID: "mem_alex2", UserID: "user_alex", PlanID: "plan_other", Status: "active"}, true); err != nil {
		t.Fatal(err)
	}
	e.srv.kickWhopStore("biz_other")
	e.srv.reconcileWhop(ctx, e.srv.takeWhopKicks())
	if got := core.got(); len(got) != 1 || !strings.Contains(got[0], "whop/biz_other/user_alex") {
		t.Fatalf("after Other's kick alone: %q", got)
	}
	e.srv.kickWhop()
	e.srv.reconcileWhop(ctx, e.srv.takeWhopKicks())
	if got := core.got(); len(got) != 2 || !strings.Contains(got[1], "whop/biz_pip/user_alex") {
		t.Fatalf("after a kick for every store: %q", got)
	}
}

// A business that took the Playkeeper Cloud app's grant back is still
// answered by Whop, with empty lists: its store's pass changes nothing,
// not even for a membership it would read again, and says why, while the
// other stores go on. Once the grant is back, however soon, the pass reads
// the store and every membership again, so a purchase made meanwhile
// counts at once and the problem goes.
func TestAStoreWhoseGrantIsGoneChangesNothing(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the core's calls: %q", got)
	}
	grant := func(revoked, down bool) {
		f.mu.Lock()
		f.installed["biz_other"].revoked, f.permissionsDown = revoked, down
		f.mu.Unlock()
	}
	tell := func(text string) {
		t.Helper()
		if err := e.srv.notifier.Notify(ctx, Customer{Provider: whopProvider, Store: "biz_other", Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	var problem string
	var memberships, plans int
	readOther := func() {
		e.srv.db.QueryRow(`SELECT problem FROM whop_stores WHERE store_id = 'biz_other'`).Scan(&problem)
		e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_memberships WHERE store_id = 'biz_other'`).Scan(&memberships)
		e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_plans WHERE store_id = 'biz_other'`).Scan(&plans)
	}

	// The grant goes straight after Other was read.
	grant(true, false)
	if err := e.srv.keepMembership("biz_other", whop.Membership{ID: "mem_alex2", UserID: "user_alex", PlanID: "plan_other", Status: "active"}, true); err != nil {
		t.Fatal(err)
	}
	f.buyAt("biz_other", "mem_jo2", "user_jo", "plan_other", "active")
	f.mu.Lock()
	f.users["user_sam"], f.users["user_jo"] = "samcrafts", "joplays"
	f.mu.Unlock()
	e.deliver(t, "msg_1", whop.EventMembershipActivated, f.buy("mem_sam1", "user_sam", "plan_starter", "active"))
	tell("hi")
	e.reconcile()
	e.reconcile()
	readOther()
	if got := core.got(); len(got) != 2 || !strings.Contains(got[1], "whop/biz_pip/user_sam") {
		t.Fatalf("the core's calls with Other's grant gone: %q", got)
	}
	if memberships != 1 || plans != 1 || !strings.Contains(problem, "grant on this store lacks plan:basic:read, member:basic:read") {
		t.Fatalf("Other with its grant gone: %d memberships, %d plans, problem %q", memberships, plans, problem)
	}
	if sent := f.sentIn("biz_other", "user_alex"); len(sent) != 0 {
		t.Fatalf("messages went out with the grant gone: %q", sent)
	}

	// It's back long before Other's next read of the store was due.
	grant(false, false)
	e.reconcile()
	readOther()
	if got, sent := core.got(), f.sentIn("biz_other", "user_alex"); len(got) != 3 || !strings.Contains(got[2], "whop/biz_other/user_jo") || memberships != 2 || problem != "" || len(sent) != 1 {
		t.Fatalf("once the grant was back: calls %q, %d memberships, problem %q, messages %q", got, memberships, problem, sent)
	}

	// A grant that can't be checked is no better.
	grant(false, true)
	tell("hi again")
	e.reconcile()
	readOther()
	if got, sent := core.got(), f.sentIn("biz_other", "user_alex"); len(got) != 3 || len(sent) != 1 || !strings.Contains(problem, "couldn't check") {
		t.Fatalf("with the grant unchecked: calls %q, problem %q, messages %q", got, problem, sent)
	}

	grant(false, false)
	e.reconcile()
	readOther()
	if got, sent := core.got(), f.sentIn("biz_other", "user_alex"); len(got) != 3 || problem != "" || len(sent) != 2 {
		t.Fatalf("once the grant could be checked: calls %q, problem %q, messages %q", got, problem, sent)
	}
}

// An app store waits while the dashboard has no key for the Playkeeper
// Cloud app, with that as its problem, and the other stores go on; once
// the key is there, it sells too.
func TestAnAppStoreWaitsForTheAppsKey(t *testing.T) {
	f, e, _ := twoStores(t)
	core := useFakeCore(e)
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET api_key = ''`); err != nil {
		t.Fatal(err)
	}
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	var problem string
	e.srv.db.QueryRow(`SELECT problem FROM whop_stores WHERE store_id = 'biz_other'`).Scan(&problem)
	if got := core.got(); len(got) != 1 || !strings.Contains(got[0], "whop/biz_pip/user_alex") || problem != errNoWhopApp.Error() {
		t.Fatalf("without the app's key: calls %q, Other's problem %q", got, problem)
	}
	if _, err := e.srv.db.Exec(`UPDATE whop_app SET api_key = ?`, whopTestAppKey); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	e.srv.db.QueryRow(`SELECT problem FROM whop_stores WHERE store_id = 'biz_other'`).Scan(&problem)
	if got := core.got(); len(got) != 2 || !strings.Contains(got[1], "whop/biz_other/user_alex") || problem != "" {
		t.Fatalf("with the app's key: calls %q, Other's problem %q", got, problem)
	}
}

// The migration makes the store the dashboard sold for its key store, with
// all it kept, gives it every plan, membership, customer, message and
// stock, and makes Sign in with Whop's app the dashboard's. With no store
// connected, what's left of one goes.
func TestTheMigrationMakesTheStoreTheKeyStore(t *testing.T) {
	at := slices.IndexFunc(panelMigrations, func(m string) bool { return strings.Contains(m, "CREATE TABLE whop_stores") })
	if at < 0 {
		t.Fatal("no migration keeps stores")
	}
	for name, selling := range map[string]bool{"selling": true, "not selling": false} {
		t.Run(name, func(t *testing.T) {
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
			if selling {
				exec(`INSERT INTO whop_account(id, account_id, title, route, api_key, connected_by, connected_at, synced_at, problem, webhook_id, webhook_url, webhook_secret,
					polled_at, marked_as, taken_over_by, taken_over_at, oauth_client_id, oauth_client_secret)
					VALUES(1, 'biz_pip', 'Pip Hosting', 'pip-hosting', 'apik_pip', 'siya', 1, 2, 'a problem', 'hook_x', 'https://beta.playkeeper.me/api/public/whop/webhook', 'ws_x',
					3, 'https://beta.playkeeper.me', '', 0, 'app_pipcloud', 'whop_secret_x')`)
			}
			exec(`INSERT INTO whop_plans(plan_id, product_id, title) VALUES('plan_starter', 'prod_mc', 'Starter')`)
			exec(`INSERT INTO whop_memberships(membership_id, whop_user_id, plan_id, status, updated_at) VALUES('mem_alex1', 'user_alex', 'plan_starter', 'active', 1)`)
			exec(`INSERT INTO whop_customers(whop_user_id, handle, applied, channel_id) VALUES('user_alex', 'alexplays', 'plan_starter|1|4096|0', 'chan_user_alex')`)
			exec(`INSERT INTO whop_messages(whop_user_id, text, created_at) VALUES('user_alex', 'hi', 1)`)
			exec(`INSERT INTO whop_stock(plan_id, want, set_at) VALUES('plan_starter', 3, 1)`)
			db.Close()
			if db, err = store.Open(path, panelMigrations); err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var n int
			db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'whop_account'`).Scan(&n)
			if n != 0 {
				t.Fatal("whop_account is still there")
			}
			stores := 0
			for _, table := range []string{"whop_plans", "whop_memberships", "whop_customers", "whop_messages", "whop_stock"} {
				var total, pip int
				db.QueryRow(`SELECT COUNT(*), COUNT(CASE WHEN store_id = 'biz_pip' THEN 1 END) FROM `+table).Scan(&total, &pip)
				if selling && (total != 1 || pip != 1) || !selling && total != 0 {
					t.Errorf("%s: %d rows, %d of them Pip's", table, total, pip)
				}
			}
			db.QueryRow(`SELECT COUNT(*) FROM whop_stores`).Scan(&stores)
			if !selling {
				if stores != 0 {
					t.Fatalf("%d stores with none connected", stores)
				}
				return
			}
			var st whopStore
			var connected, synced, polled, taken, suspended int64
			if err := db.QueryRow(`SELECT `+whopStoreColumns+` FROM whop_stores`).Scan(&st.ID, &st.Via, &st.Title, &st.Route, &st.Key, &st.ConnectedBy, &connected, &synced,
				&st.Problem, &st.WebhookID, &st.WebhookURL, &st.WebhookSecret, &polled, &st.MarkedAs, &st.TakenOverBy, &taken, &suspended, &st.SuspendReason); err != nil {
				t.Fatal(err)
			}
			if stores != 1 || st.ID != "biz_pip" || st.Via != whopViaKey || st.Title != "Pip Hosting" || st.Route != "pip-hosting" || st.Key != "apik_pip" || st.ConnectedBy != "siya" ||
				connected != 1 || synced != 2 || st.Problem != "a problem" || st.WebhookID != "hook_x" || st.WebhookSecret != "ws_x" || polled != 3 || st.MarkedAs != "https://beta.playkeeper.me" {
				t.Fatalf("the key store: %d stores, %+v %d %d %d", stores, st, connected, synced, polled)
			}
			var client, secret string
			if err := db.QueryRow(`SELECT client_id, client_secret FROM whop_app WHERE id = 1`).Scan(&client, &secret); err != nil || client != "app_pipcloud" || secret != "whop_secret_x" {
				t.Fatalf("the Playkeeper Cloud app: %q %q, %v", client, secret, err)
			}
			if _, err := db.Exec(`INSERT INTO whop_customers(store_id, whop_user_id) VALUES('biz_other', 'user_alex')`); err != nil {
				t.Fatalf("alex at another store: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO whop_stores(store_id, via, connected_at) VALUES('biz_two', 'key', 4)`); err == nil {
				t.Fatal("a second key store")
			}
		})
	}
}
