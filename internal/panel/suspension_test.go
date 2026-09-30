package panel

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/store"
)

// suspensionPath is where the owner suspends the account id, or lifts it.
func suspensionPath(id int64) string {
	return "/api/customers/" + strconv.FormatInt(id, 10) + "/suspension"
}

// hit counts the requests the agent got for what.
func (p pausable) hit(what string) int {
	p.e.agent.mu.Lock()
	defer p.e.agent.mu.Unlock()
	n := 0
	for _, h := range p.e.agent.hits {
		if h == what {
			n++
		}
	}
	return n
}

// A customer the owner suspends has their server stopped, their API token
// revoked and their session ended, and can't sign in, with nothing said to
// them. Suspending again changes nothing. Once the owner lifts it they're
// told, and may sign in and start their server again.
func TestASuspendedCustomerCanDoNothingUntilTheOwnerLiftsIt(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	path := suspensionPath(p.alex.id)
	kicked(e.env)
	r := e.do(t, "POST", path, `{"reason":"their server attacked another"}`, p.own.auth())
	if r.status != http.StatusOK || r.body["state"] != "suspended" || r.body["suspendedSelf"] != true || r.body["suspendedStore"] != false {
		t.Fatalf("suspending alex: %d %v", r.status, r.body)
	}
	if !kicked(e.env) {
		t.Fatal("suspending alex didn't send the machines the hold at once")
	}
	info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if info.State != CustomerSuspended || info.SignIn || p.stops() != 1 || p.tokens(t) != 0 {
		t.Fatalf("alex once suspended: %+v, %d stops, %d tokens", info, p.stops(), p.tokens(t))
	}
	if st := e.do(t, "GET", "/api/auth/me", "", p.alex.auth()).status; st != http.StatusUnauthorized {
		t.Fatalf("alex's session once suspended: %d", st)
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady}) {
		t.Fatalf("alex was told %v", k)
	}
	if rows := e.auditRows(t, "customer.suspend"); len(rows) != 1 || !strings.Contains(rows[0], "their server attacked another") {
		t.Fatalf("audit: %q", rows)
	}
	if r := e.do(t, "POST", path, `{"reason":"once more"}`, p.own.auth()); r.status != http.StatusOK || p.stops() != 1 || len(e.auditRows(t, "customer.suspend")) != 1 {
		t.Fatalf("suspending alex again: %d %v, %d stops", r.status, r.body, p.stops())
	}

	r = e.do(t, "DELETE", path, "", p.own.auth())
	info, _, _ = p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if r.status != http.StatusOK || r.body["state"] != "active" || info.State != CustomerActive || !info.SignIn {
		t.Fatalf("lifting alex's suspension: %d %v, %+v", r.status, r.body, info)
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady, messageUnsuspended}) {
		t.Fatalf("alex was told %v", k)
	}
	alex := signIn(t, e.env, p.alex.id)
	if r := e.do(t, "POST", "/api/servers/"+p.serverID+"/start", `{}`, alex.auth()); r.status == http.StatusForbidden {
		t.Fatalf("alex, lifted, starts their server: %d %v", r.status, r.body)
	}
}

// Only the owner suspends or lifts, only a customer's account, and only
// with a reason.
func TestOnlyTheOwnerSuspendsACustomerWithAReason(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	kim := addAdmin(t, e.env, "kim", p.serverID)
	path := suspensionPath(p.alex.id)
	for who, m := range map[string]member{"an admin": kim, "the customer": p.alex} {
		for _, method := range []string{"POST", "DELETE"} {
			if r := e.do(t, method, path, `{"reason":"because"}`, m.auth()); r.status != http.StatusForbidden {
				t.Fatalf("%s %ss alex's suspension: %d %v", who, method, r.status, r.body)
			}
		}
		if r := e.do(t, "GET", "/api/whop/stores", "", m.auth()); r.status != http.StatusForbidden {
			t.Fatalf("%s lists the stores: %d %v", who, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", suspensionPath(kim.id), `{"reason":"because"}`, p.own.auth()); r.status != http.StatusNotFound {
		t.Fatalf("suspending kim, who isn't a customer: %d %v", r.status, r.body)
	}
	for _, body := range []string{`{}`, `{"reason":"   "}`, `{"reason":"` + strings.Repeat("x", maxSuspendReason+1) + `"}`} {
		if r := e.do(t, "POST", path, body, p.own.auth()); r.status != http.StatusBadRequest {
			t.Fatalf("suspending alex with %s: %d %v", body, r.status, r.body)
		}
	}
	if info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); info.State != CustomerActive || p.stops() != 0 {
		t.Fatalf("alex after refused suspensions: %+v, %d stops", info, p.stops())
	}
}

// A suspended customer's plan goes on underneath: one that ended isn't
// deleted while they're suspended, and lifting leaves them paused with a
// fresh grace period, and says until when; one that started again leaves
// them active.
func TestASuspendedCustomersPlanGoesOnUnderneath(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	path := suspensionPath(p.alex.id)
	if r := e.do(t, "POST", path, `{"reason":"a chargeback"}`, p.own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending alex: %d %v", r.status, r.body)
	}
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	e.clock.add(20 * 24 * time.Hour)
	e.srv.deleteLapsedCustomers(ctx)
	if info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); info.State != CustomerSuspended || p.hit("POST /v1/servers/"+p.serverID+"/delete") != 0 {
		t.Fatalf("alex, suspended with their plan ended 20 days ago: %+v, deleted %d times", info, p.hit("POST /v1/servers/"+p.serverID+"/delete"))
	}
	own := signIn(t, e.env, p.own.id)
	if r := e.do(t, "DELETE", path, "", own.auth()); r.status != http.StatusOK || r.body["state"] != "paused" {
		t.Fatalf("lifting alex's suspension: %d %v", r.status, r.body)
	}
	var until int64
	if err := e.srv.db.QueryRow(`SELECT delete_after FROM customers WHERE user_id = ?`, p.alex.id).Scan(&until); err != nil {
		t.Fatal(err)
	}
	if left := time.UnixMilli(until).Sub(e.clock.now()); left < 13*24*time.Hour {
		t.Fatalf("alex, lifted, keeps their servers for %v", left)
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady, messagePaused}) || !strings.Contains(p.n.sent[1].Text, time.UnixMilli(until).UTC().Format("2 January")) {
		t.Fatalf("alex was told %v: %+v", k, p.n.sent)
	}
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.hit("POST /v1/servers/" + p.serverID + "/delete"); n != 0 {
		t.Fatalf("alex's server was deleted %d times as soon as their suspension was lifted", n)
	}

	if r := e.do(t, "POST", path, `{"reason":"another chargeback"}`, own.auth()); r.status != http.StatusOK || r.body["state"] != "suspended" {
		t.Fatalf("suspending alex, paused: %d %v", r.status, r.body)
	}
	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	if info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); info.State != CustomerSuspended {
		t.Fatalf("alex, suspended, renewed: %+v", info)
	}
	if r := e.do(t, "DELETE", path, "", own.auth()); r.status != http.StatusOK || r.body["state"] != "active" {
		t.Fatalf("lifting alex's suspension once they renewed: %d %v", r.status, r.body)
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady, messagePaused, messageUnsuspended}) {
		t.Fatalf("alex was told %v", k)
	}
}

// storeAccount is the account of subject at store.
func storeAccount(t *testing.T, e *env, store, subject string) CustomerAccountInfo {
	t.Helper()
	info, ok, err := customerCore{s: e.srv}.CustomerAccount(context.Background(), whopProvider, store, subject)
	if err != nil || !ok {
		t.Fatalf("the account of %s at %s: %v, %v", subject, store, ok, err)
	}
	return info
}

// storesWithCustomers is two stores with alex a customer of both and sam of
// Other Hosting alone, each with an account.
func storesWithCustomers(t *testing.T) (*fakeWhop, *env, member) {
	t.Helper()
	f, e, own := twoStores(t)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	f.buyAt("biz_other", "mem_sam2", "user_sam", "plan_other", "active")
	f.mu.Lock()
	f.users["user_sam"] = "samcrafts"
	f.mu.Unlock()
	e.reconcile()
	for _, c := range [][2]string{{testStore, "user_alex"}, {"biz_other", "user_alex"}, {"biz_other", "user_sam"}} {
		if info := storeAccount(t, e, c[0], c[1]); info.State != CustomerActive {
			t.Fatalf("%s at %s before any suspension: %+v", c[1], c[0], info)
		}
	}
	return f, e, own
}

// Suspending an app store suspends every customer of it, and nobody else:
// alex's account at the other store goes on. Lifting it lifts them.
func TestSuspendingAStoreSuspendsItsOwnCustomersAlone(t *testing.T) {
	_, e, own := storesWithCustomers(t)
	r := e.do(t, "POST", "/api/whop/stores/biz_other/suspension", `{"reason":"selling to cheaters"}`, own.auth())
	stores, _ := r.body["stores"].([]any)
	if r.status != http.StatusOK || len(stores) != 1 {
		t.Fatalf("suspending Other Hosting: %d %v", r.status, r.body)
	}
	if other, _ := stores[0].(map[string]any); other["id"] != "biz_other" || other["suspendedAt"] == nil || other["suspendReason"] != "selling to cheaters" || other["customers"] != 2.0 {
		t.Fatalf("Other Hosting once suspended: %v", other)
	}
	for c, want := range map[[2]string]CustomerState{{testStore, "user_alex"}: CustomerActive, {"biz_other", "user_alex"}: CustomerSuspended, {"biz_other", "user_sam"}: CustomerSuspended} {
		if info := storeAccount(t, e, c[0], c[1]); info.State != want {
			t.Fatalf("%s at %s once Other Hosting is suspended: %+v", c[1], c[0], info)
		}
	}
	if rows := e.auditRows(t, "whop.store_suspend"); len(rows) != 1 || !strings.Contains(rows[0], "2 customer(s) suspended with it") {
		t.Fatalf("audit: %q", rows)
	}
	if r := e.do(t, "DELETE", "/api/whop/stores/biz_other/suspension", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("lifting Other Hosting's suspension: %d %v", r.status, r.body)
	}
	for _, c := range [][2]string{{testStore, "user_alex"}, {"biz_other", "user_alex"}, {"biz_other", "user_sam"}} {
		if info := storeAccount(t, e, c[0], c[1]); info.State != CustomerActive {
			t.Fatalf("%s at %s once Other Hosting's suspension is lifted: %+v", c[1], c[0], info)
		}
	}
}

// Lifting a store's suspension leaves a customer the owner suspended on
// their own suspended, and lifting a customer's own suspension leaves them
// suspended while their store is.
func TestTwoSuspensionsAreLiftedApart(t *testing.T) {
	_, e, own := storesWithCustomers(t)
	alex, sam := storeAccount(t, e, "biz_other", "user_alex"), storeAccount(t, e, "biz_other", "user_sam")
	if r := e.do(t, "POST", suspensionPath(alex.UserID), `{"reason":"griefing"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending alex at Other: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/whop/stores/biz_other/suspension", `{"reason":"selling to cheaters"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending Other Hosting: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", suspensionPath(sam.UserID), "", own.auth()); r.status != http.StatusOK || r.body["state"] != "suspended" || r.body["suspendedStore"] != true {
		t.Fatalf("lifting sam's own suspension, which he hasn't: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/whop/stores/biz_other/suspension", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("lifting Other Hosting's suspension: %d %v", r.status, r.body)
	}
	if a, s := storeAccount(t, e, "biz_other", "user_alex"), storeAccount(t, e, "biz_other", "user_sam"); a.State != CustomerSuspended || s.State != CustomerActive {
		t.Fatalf("once Other Hosting's suspension is lifted: alex %+v, sam %+v", a, s)
	}
	if r := e.do(t, "DELETE", suspensionPath(alex.UserID), "", own.auth()); r.status != http.StatusOK || r.body["state"] != "active" {
		t.Fatalf("lifting alex's own suspension: %d %v", r.status, r.body)
	}
}

// A suspended store sells nothing: the fleet keeps no room for its plans,
// each is set to 0 on Whop and stays so whatever number the fleet had for
// it, and a purchase made meanwhile starts nobody. The other store goes on.
// Once lifted, it's read again at once, sells again and starts the buyer.
func TestASuspendedStoreSellsNothing(t *testing.T) {
	f, e, own := twoStores(t)
	core := useFakeCore(e)
	ctx := context.Background()
	f.mu.Lock()
	f.users["user_sam"] = "samcrafts"
	f.mu.Unlock()
	f.buyAt("biz_other", "mem_alex2", "user_alex", "plan_other", "active")
	e.reconcile()
	if err := e.srv.sales.SetAvailability(ctx, map[string]int{"plan_starter": 3, "plan_other": 2}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if r := e.do(t, "POST", "/api/whop/stores/biz_other/suspension", `{"reason":"selling to cheaters"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending Other Hosting: %d %v", r.status, r.body)
	}
	e.reconcile()
	f.buyAt("biz_other", "mem_sam2", "user_sam", "plan_other", "active")
	if err := e.srv.sales.SetAvailability(ctx, map[string]int{"plan_starter": 3, "plan_other": 5}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	plans, err := e.srv.sales.SalePlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range plans {
		ids = append(ids, p.ID)
	}
	var others []string
	for _, w := range f.stockWrites() {
		if strings.HasPrefix(w, "plan_other=") {
			others = append(others, w)
		}
	}
	if slices.Contains(ids, "plan_other") || !slices.Contains(ids, "plan_starter") || len(others) == 0 || others[len(others)-1] != "plan_other=0" || slices.Contains(others, "plan_other=5") {
		t.Fatalf("with Other Hosting suspended: plans for sale %v, Other's stock written %v", ids, others)
	}
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the core's calls with Other Hosting suspended: %q", got)
	}

	if r := e.do(t, "DELETE", "/api/whop/stores/biz_other/suspension", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("lifting Other Hosting's suspension: %d %v", r.status, r.body)
	}
	e.reconcile()
	if got := core.got(); len(got) != 2 || !strings.Contains(got[1], "whop/biz_other/user_sam") {
		t.Fatalf("the core's calls once Other Hosting's suspension is lifted: %q", got)
	}
	if plans, _ := e.srv.sales.SalePlans(ctx); !slices.ContainsFunc(plans, func(p SalePlan) bool { return p.ID == "plan_other" }) {
		t.Fatalf("plans for sale once lifted: %v", plans)
	}
}

// Only an app store can be suspended: the dashboard's own store can't, and
// one it doesn't sell for is refused.
func TestOnlyAnAppStoreCanBeSuspended(t *testing.T) {
	_, e, own := twoStores(t)
	if r := e.do(t, "POST", "/api/whop/stores/"+testStore+"/suspension", `{"reason":"because"}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("suspending the key store: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/whop/stores/biz_nobody/suspension", `{"reason":"because"}`, own.auth()); r.status != http.StatusNotFound {
		t.Fatalf("suspending a store the dashboard doesn't sell for: %d %v", r.status, r.body)
	}
	if st, _, _ := e.srv.whopStoreByID(context.Background(), testStore); !st.SuspendedAt.IsZero() {
		t.Fatal("the key store was suspended")
	}
}

// The Team page shows a customer's store and state, and why they're
// suspended; only the owner may suspend them.
func TestTheTeamPageShowsACustomersStoreAndSuspension(t *testing.T) {
	_, e, own := storesWithCustomers(t)
	alex := storeAccount(t, e, "biz_other", "user_alex")
	if r := e.do(t, "POST", suspensionPath(alex.UserID), `{"reason":"griefing"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("suspending alex at Other: %d %v", r.status, r.body)
	}
	kim := addAdmin(t, e, "kim", "*")
	for who, m := range map[string]member{"the owner": own, "an admin": kim} {
		var team struct {
			Members []teamMember `json:"members"`
		}
		e.get(t, "/api/team", m.cookie, &team)
		i := slices.IndexFunc(team.Members, func(x teamMember) bool { return x.ID == alex.UserID })
		if i < 0 {
			t.Fatalf("%s's Team page lacks alex at Other: %+v", who, team.Members)
		}
		row := team.Members[i]
		if row.CustomerState != CustomerSuspended || row.Store != "biz_other" || row.StoreName != "Other Hosting" || !row.SuspendedSelf || row.SuspendReason != "griefing" || row.CanSuspend != (who == "the owner") {
			t.Fatalf("alex at Other on %s's Team page: %+v", who, row)
		}
	}
}

// The migration keeps a suspension made before it as the owner's own.
func TestTheMigrationKeepsASuspensionAsTheOwnersOwn(t *testing.T) {
	at := slices.IndexFunc(panelMigrations, func(m string) bool { return strings.Contains(m, "ADD COLUMN suspended_self") })
	if at < 0 {
		t.Fatal("no migration keeps suspensions")
	}
	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := store.Open(path, panelMigrations[:at])
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id, username, password_hash, role, created_at, password_changed_at) VALUES(7, 'alex', '', 'member', 1, 1), (8, 'sam', '', 'member', 1, 1)`,
		`INSERT INTO customers(user_id, provider, store, subject, state, created_at, updated_at) VALUES(7, 'whop', 'biz_pip', 'user_alex', 'suspended', 1, 1), (8, 'whop', 'biz_pip', 'user_sam', 'active', 1, 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if db, err = store.Open(path, panelMigrations); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, want := range map[int64]bool{7: true, 8: false} {
		var self, withStore bool
		if err := db.QueryRow(`SELECT suspended_self, suspended_store FROM customers WHERE user_id = ?`, id).Scan(&self, &withStore); err != nil {
			t.Fatal(err)
		}
		if self != want || withStore {
			t.Fatalf("account %d after the migration: suspended on its own %v, with its store %v", id, self, withStore)
		}
	}
}
