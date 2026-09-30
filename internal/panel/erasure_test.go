package panel

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/whop"
)

// customerDeletePath is where the owner asks for the account uid's deletion.
func customerDeletePath(uid int64) string { return "/api/customers/" + strconv.FormatInt(uid, 10) }

// keptBackupAt has the machine keep one backup under label, id kid.
func keptBackupAt(e *env, label, kid string) {
	e.reply("GET", "/v1/kept-backups", `[{"id":"`+kid+`","serverId":"cafebabe23","serverName":"alex","keptFor":"`+label+`","fileName":"playkeeper-alex-`+kid+`.tar.gz","sizeBytes":5,"sha256":"","madeAt":"2026-10-13T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z"}]`)
}

// A customer the owner deletes on request, once their plan ended, loses
// their account and everything personal the dashboard keeps of them:
// their server, with every backup and no final one kept, and the final
// backups a machine keeps for them; their sign-ins and tokens; their
// memberships, Whop row and messages at their store. A payment of theirs
// keeps its amounts for the store's accounts without who paid, and nothing
// of another store changes.
func TestDeletingACustomerOnRequestRemovesTheirAccountAndRecords(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	keptBackupAt(e.env, keptFor(p.alex.id), "20261013-120000-abc123")
	for _, q := range []string{
		`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at) VALUES('` + testStore + `', 'mem_alex1', 'user_alex', 'plan_starter', 'expired', 1)`,
		`INSERT INTO whop_customers(store_id, whop_user_id, handle, updated_at) VALUES('` + testStore + `', 'user_alex', 'alex', 1)`,
		`INSERT INTO whop_messages(store_id, whop_user_id, kind, text, created_at) VALUES('` + testStore + `', 'user_alex', 'paused', 'Your plan ended.', 1)`,
		`INSERT INTO whop_payments(payment_id, store_id, whop_user_id, plan_id, currency, amount, share, refunded, paid_at, updated_at) VALUES('pay_alex1', '` + testStore + `', 'user_alex', 'plan_starter', 'usd', 1000, 0, 0, 1, 1)`,
		`INSERT INTO whop_messages(store_id, whop_user_id, kind, text, created_at) VALUES('biz_other', 'user_alex', 'ready', 'Your server is ready.', 1)`,
		`INSERT INTO whop_payments(payment_id, store_id, whop_user_id, plan_id, currency, amount, share, refunded, paid_at, updated_at) VALUES('pay_alex2', 'biz_other', 'user_alex', 'plan_other', 'usd', 1500, 850, 0, 1, 1)`,
	} {
		if _, err := e.srv.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if r := e.do(t, "DELETE", customerDeletePath(p.alex.id), `{"confirm":"alex"}`, p.own.auth()); r.status != http.StatusAccepted {
		t.Fatalf("asking for alex's deletion: %d %v", r.status, r.body)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sessions WHERE user_id = ?`, p.alex.id); n != 0 {
		t.Fatalf("alex is still signed in %d times once their deletion was asked for", n)
	}
	e.srv.eraseDueCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("alex's server was asked to be deleted %d times", n)
	}
	e.agent.mu.Lock()
	raw := e.agent.lastBody["POST /v1/servers/"+p.serverID+"/delete"]
	deletedKept := slices.Contains(e.agent.hits, "DELETE /v1/kept-backups/20261013-120000-abc123")
	e.agent.mu.Unlock()
	if !strings.Contains(raw, `"forgetKey":true`) || strings.Contains(raw, "keepFinalBackupDays") || strings.Contains(raw, "keptFor") {
		t.Fatalf("the deletion asked for %s", raw)
	}
	if !deletedKept {
		t.Fatal("the final backup kept for alex wasn't deleted")
	}
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); ok {
		t.Fatal("alex still has an account")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM users WHERE id = ?`, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, `SELECT COUNT(*) FROM customer_homes WHERE user_id = ?`,
		`SELECT COUNT(*) FROM creator_servers WHERE user_id = ?`,
	} {
		if n := e.count(t, q, p.alex.id); n != 0 {
			t.Errorf("%s: %d", q, n)
		}
	}
	for _, table := range []string{"whop_memberships", "whop_customers", "whop_messages"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE store_id = ? AND whop_user_id = 'user_alex'`, testStore); n != 0 {
			t.Errorf("%s still has alex at their store: %d", table, n)
		}
	}
	var payer string
	var amount int64
	if err := e.srv.db.QueryRow(`SELECT whop_user_id, amount FROM whop_payments WHERE payment_id = 'pay_alex1'`).Scan(&payer, &amount); err != nil || payer != "" || amount != 1000 {
		t.Fatalf("alex's payment at their store: %q, %d, %v", payer, amount, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_messages WHERE store_id = 'biz_other' AND whop_user_id = 'user_alex'`); n != 1 {
		t.Fatalf("alex's message at another store went: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_payments WHERE payment_id = 'pay_alex2' AND whop_user_id = 'user_alex'`); n != 1 {
		t.Fatalf("alex's payment at another store lost who paid: %d", n)
	}
	rows := e.auditRows(t, "customer.erase")
	if len(rows) != 2 || !strings.HasPrefix(rows[0], "admin alex requested") || !strings.HasPrefix(rows[1], "admin alex succeeded") ||
		!strings.Contains(rows[1], "on request: 1 server(s) with their backups, 1 kept backup(s); 1 payment(s) kept") {
		t.Fatalf("the audit log: %v", rows)
	}
	e.srv.eraseDueCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("looking again deleted %d times", n)
	}
}

// Deleting a customer of one store leaves the same Whop user's account at
// another store as it was, and the store's next read doesn't bring back
// the membership of theirs that ended. If they buy again, they're a new
// customer.
func TestDeletingACustomerLeavesTheirOtherStoreAlone(t *testing.T) {
	f, e, own := storesWithCustomers(t)
	ctx := context.Background()
	e.reply("GET", "/v1/kept-backups", `[]`)
	f.mu.Lock()
	f.installed["biz_other"].memberships["mem_alex2"]["status"] = "expired"
	f.mu.Unlock()
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	other := storeAccount(t, e, "biz_other", "user_alex")
	if other.State != CustomerPaused {
		t.Fatalf("alex at Other once their plan ended: %+v", other)
	}
	for _, pay := range []whopPayment{
		{ID: "pay_pip", Store: testStore, WhopUserID: "user_alex", PlanID: "plan_starter", Currency: "usd", Amount: 1000, PaidAt: e.clock.now()},
		{ID: "pay_other", Store: "biz_other", WhopUserID: "user_alex", PlanID: "plan_other", Currency: "usd", Amount: 1500, Share: 850, PaidAt: e.clock.now()},
	} {
		if err := e.srv.keepWhopPayment(ctx, pay); err != nil {
			t.Fatal(err)
		}
	}
	confirm := `{"confirm":"` + usernameOf(t, e, other.UserID) + `"}`
	if r := e.do(t, "DELETE", customerDeletePath(other.UserID), confirm, own.auth()); r.status != http.StatusAccepted {
		t.Fatalf("asking for alex's deletion at Other: %d %v", r.status, r.body)
	}
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := (customerCore{s: e.srv}).CustomerAccount(ctx, whopProvider, "biz_other", "user_alex"); ok {
		t.Fatal("alex still has an account at Other")
	}
	if pip := storeAccount(t, e, testStore, "user_alex"); pip.State != CustomerActive || pip.UserID == other.UserID {
		t.Fatalf("alex at Pip Hosting: %+v", pip)
	}
	if sam := storeAccount(t, e, "biz_other", "user_sam"); sam.State != CustomerActive {
		t.Fatalf("sam at Other: %+v", sam)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_memberships WHERE store_id = ? AND whop_user_id = 'user_alex'`, testStore); n != 1 {
		t.Fatalf("alex's membership at Pip Hosting: %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_payments WHERE whop_user_id = 'user_alex'`); n != 1 {
		t.Fatalf("payments still naming alex: %d, want Pip Hosting's", n)
	}
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	if n := e.count(t, `SELECT COUNT(*) FROM whop_memberships WHERE store_id = 'biz_other' AND whop_user_id = 'user_alex'`); n != 0 {
		t.Fatalf("Other's next read brought back alex's ended membership: %d", n)
	}
	if _, ok, _ := (customerCore{s: e.srv}).CustomerAccount(ctx, whopProvider, "biz_other", "user_alex"); ok {
		t.Fatal("Other's next read brought alex back")
	}
	f.buyAt("biz_other", "mem_alex3", "user_alex", "plan_other", "active")
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	if again := storeAccount(t, e, "biz_other", "user_alex"); again.State != CustomerActive {
		t.Fatalf("alex buying at Other again: %+v", again)
	}
}

// usernameOf is the account's username.
func usernameOf(t *testing.T, e *env, uid int64) string {
	t.Helper()
	var name string
	if err := e.srv.db.QueryRow(`SELECT username FROM users WHERE id = ?`, uid).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

// Only the owner deletes a customer, one whose plan has ended, with the
// account's name typed to confirm. One who buys again before the deletion
// runs keeps their account.
func TestOnlyTheOwnerDeletesACustomerWhosePlanEnded(t *testing.T) {
	f, e, own := storesWithCustomers(t)
	ctx := context.Background()
	e.reply("GET", "/v1/kept-backups", `[]`)
	other := storeAccount(t, e, "biz_other", "user_alex")
	name := usernameOf(t, e, other.UserID)
	path := customerDeletePath(other.UserID)
	if r := e.do(t, "DELETE", path, `{"confirm":"`+name+`"}`, own.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "still have a plan") {
		t.Fatalf("deleting alex while they have a plan: %d %v", r.status, r.body)
	}
	f.mu.Lock()
	f.installed["biz_other"].memberships["mem_alex2"]["status"] = "expired"
	f.mu.Unlock()
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	for who, c := range map[string]struct {
		body   string
		auth   map[string]string
		status int
	}{
		"the wrong name": {`{"confirm":"alex"}`, own.auth(), http.StatusBadRequest},
		"no name":        {`{}`, own.auth(), http.StatusBadRequest},
		"an admin":       {`{"confirm":"` + name + `"}`, addMember(t, e, "morgan", "admin", "*").auth(), http.StatusForbidden},
	} {
		if r := e.do(t, "DELETE", path, c.body, c.auth); r.status != c.status {
			t.Errorf("%s: %d %v", who, r.status, r.body)
		}
	}
	if r := e.do(t, "DELETE", customerDeletePath(own.id), `{"confirm":"admin"}`, own.auth()); r.status != http.StatusNotFound {
		t.Fatalf("deleting the owner as a customer: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", path, `{"confirm":"`+name+`"}`, own.auth()); r.status != http.StatusAccepted {
		t.Fatalf("deleting alex once their plan ended: %d %v", r.status, r.body)
	}
	var v teamBody
	e.get(t, "/api/team", own.cookie, &v)
	if i := slices.IndexFunc(v.Members, func(m teamMember) bool { return m.ID == other.UserID }); i < 0 || !v.Members[i].Deleting || !v.Members[i].CanDelete {
		t.Fatalf("the Team page's alex at Other: %+v", v.Members)
	}
	f.mu.Lock()
	f.installed["biz_other"].memberships["mem_alex2"]["status"] = "active"
	f.mu.Unlock()
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	e.srv.eraseDueCustomers(ctx)
	if again := storeAccount(t, e, "biz_other", "user_alex"); again.UserID != other.UserID {
		t.Fatalf("alex, who bought again, lost their account: %+v", again)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM customers WHERE user_id = ? AND erase_requested_at > 0`, other.UserID); n != 0 {
		t.Fatal("alex's deletion is still asked for once they bought again")
	}
	if rows := e.auditRows(t, "customer.erase"); len(rows) != 2 || !strings.Contains(rows[1], "refused they have a plan at their store again") {
		t.Fatalf("the audit log: %v", rows)
	}
}

// A customer is deleted the owner's number of days after their servers
// were deleted, 30 until the owner sets another, and not a day before. The
// owner alone sets it, from the 30 days their final backups are kept to
// 3650.
func TestACustomerIsDeletedSomeDaysAfterTheirServers(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	e.reply("GET", "/v1/kept-backups", `[]`)
	p.lapsed(t)
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("alex's server was deleted %d times", n)
	}
	owner := func() member { return signIn(t, e.env, p.own.id) }
	var v retentionView
	if st := e.get(t, "/api/customers/retention", owner().cookie, &v); st != http.StatusOK || v.Days != 30 || v.Default != 30 || v.Min != 30 || v.Max != 3650 {
		t.Fatalf("the setting: %d %+v", st, v)
	}
	if _, err := e.srv.db.Exec(`INSERT INTO panel_meta(key, value) VALUES(?, '7')`, metaCustomerRetention); err != nil {
		t.Fatal(err)
	}
	if st := e.get(t, "/api/customers/retention", owner().cookie, &v); st != http.StatusOK || v.Days != 30 {
		t.Fatalf("the setting with 7 days stored: %d %+v", st, v)
	}
	e.clock.add(29 * 24 * time.Hour)
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); !ok {
		t.Fatal("alex was deleted a day early")
	}
	for body, status := range map[string]int{`{"days":29}`: http.StatusBadRequest, `{"days":3651}`: http.StatusBadRequest, `{"days":60}`: http.StatusOK} {
		if r := e.do(t, "PUT", "/api/customers/retention", body, owner().auth()); r.status != status {
			t.Errorf("setting %s: %d %v", body, r.status, r.body)
		}
	}
	if r := e.do(t, "PUT", "/api/customers/retention", `{"days":45}`, addMember(t, e.env, "morgan", "admin", "*").auth()); r.status != http.StatusForbidden {
		t.Fatalf("an admin setting it: %d %v", r.status, r.body)
	}
	e.clock.add(2 * 24 * time.Hour)
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); !ok {
		t.Fatal("alex was deleted 31 days after their servers, with 60 days set")
	}
	if r := e.do(t, "PUT", "/api/customers/retention", `{"days":30}`, owner().auth()); r.status != http.StatusOK {
		t.Fatalf("setting 30 days again: %d %v", r.status, r.body)
	}
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); ok {
		t.Fatal("alex wasn't deleted 31 days after their servers")
	}
	if rows := e.auditRows(t, "customer.erase"); len(rows) != 1 || !strings.HasPrefix(rows[0], "playkeeper alex succeeded") || !strings.Contains(rows[0], "30 days after their servers were deleted") {
		t.Fatalf("the audit log: %v", rows)
	}
}

// A membership that no longer gives access, of a customer the dashboard
// deleted, isn't kept again; one that does is.
func TestADeletedCustomersEndedMembershipIsntKeptAgain(t *testing.T) {
	_, e, _ := storesWithCustomers(t)
	if _, err := e.srv.db.Exec(`INSERT INTO erased_customers(store_id, subject_hash, erased_at) VALUES('biz_other', ?, 1)`, erasedSubject("biz_other", "user_kim")); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.keepMembership("biz_other", whop.Membership{ID: "mem_kim1", UserID: "user_kim", PlanID: "plan_other", Status: "expired"}, false); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.keepMembership(testStore, whop.Membership{ID: "mem_kim2", UserID: "user_kim", PlanID: "plan_starter", Status: "expired"}, false); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.keepMembership("biz_other", whop.Membership{ID: "mem_kim3", UserID: "user_kim", PlanID: "plan_other", Status: "active"}, false); err != nil {
		t.Fatal(err)
	}
	var kept []string
	rows, _ := e.srv.db.Query(`SELECT membership_id FROM whop_memberships WHERE whop_user_id = 'user_kim' ORDER BY membership_id`)
	for rows.Next() {
		var id string
		rows.Scan(&id)
		kept = append(kept, id)
	}
	rows.Close()
	if !slices.Equal(kept, []string{"mem_kim2", "mem_kim3"}) {
		t.Fatalf("kim's memberships kept: %v", kept)
	}
}
