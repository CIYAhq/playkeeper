package panel

import (
	"context"
	"errors"
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

// keptBackupsAt has the machine keep one backup under each label, with its
// id, and list them all whichever label it's asked for.
func keptBackupsAt(e *env, kept map[string]string) {
	var list []string
	for kid, label := range kept {
		list = append(list, `{"id":"`+kid+`","serverId":"cafebabe23","serverName":"alex","keptFor":"`+label+`","fileName":"playkeeper-alex-`+kid+`.tar.gz","sizeBytes":5,"sha256":"","madeAt":"2026-10-13T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z"}`)
	}
	e.replyStatus("GET", "/v1/kept-backups", http.StatusOK, "["+strings.Join(list, ",")+"]")
}

// agentHit says whether the machine was asked for what, as "METHOD /path".
func agentHit(e *env, what string) bool {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	return slices.Contains(e.agent.hits, what)
}

// A customer the owner deletes on request, once their plan ended, loses
// their account and everything personal the dashboard keeps of them:
// their server, with every backup and no final one kept, and the final
// backups a machine keeps for them; their sign-ins and tokens, at once,
// even a token the pause left working;
// their memberships, Whop row and messages at their store; their invites,
// the copies their moves left, and their server's join requests, players'
// origins and shared links. A payment of theirs keeps its amounts for the
// store's accounts without who paid, and nothing of another store or
// another account changes. A deletion that stops midway, as when a machine
// can't list the backups it keeps, ends at a later look, and the server it
// deleted already took its records along.
func TestDeletingACustomerOnRequestRemovesTheirAccountAndRecords(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.db.Exec(`UPDATE api_tokens SET revoked_at = 0, revoked_by = '' WHERE user_id = ?`, p.alex.id); err != nil {
		t.Fatal(err)
	}
	e.replyStatus("GET", "/v1/kept-backups", http.StatusServiceUnavailable, `{"error":"The machine is busy.","code":"busy"}`)
	var project string
	if err := e.srv.db.QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	alexID, ownID := strconv.FormatInt(p.alex.id, 10), strconv.FormatInt(p.own.id, 10)
	for _, q := range []string{
		`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at) VALUES('` + testStore + `', 'mem_alex1', 'user_alex', 'plan_starter', 'expired', 1)`,
		`INSERT INTO whop_customers(store_id, whop_user_id, handle, updated_at) VALUES('` + testStore + `', 'user_alex', 'alex', 1)`,
		`INSERT INTO whop_messages(store_id, whop_user_id, kind, text, created_at) VALUES('` + testStore + `', 'user_alex', 'paused', 'Your plan ended.', 1)`,
		`INSERT INTO whop_payments(payment_id, store_id, whop_user_id, plan_id, currency, amount, share, refunded, paid_at, updated_at) VALUES('pay_alex1', '` + testStore + `', 'user_alex', 'plan_starter', 'usd', 1000, 0, 0, 1, 1)`,
		`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at) VALUES('biz_other', 'mem_alex9', 'user_alex', 'plan_other', 'active', 1)`,
		`INSERT INTO whop_customers(store_id, whop_user_id, handle, updated_at) VALUES('biz_other', 'user_alex', 'alex', 1)`,
		`INSERT INTO whop_messages(store_id, whop_user_id, kind, text, created_at) VALUES('biz_other', 'user_alex', 'ready', 'Your server is ready.', 1)`,
		`INSERT INTO whop_payments(payment_id, store_id, whop_user_id, plan_id, currency, amount, share, refunded, paid_at, updated_at) VALUES('pay_alex2', 'biz_other', 'user_alex', 'plan_other', 'usd', 1500, 850, 0, 1, 1)`,
		`INSERT INTO invites(id, kind, code_hash, project_id, server_id, created_by, created_at, expires_at, max_uses) VALUES('inv_alex', 'player', 'hash_alex', '` + project + `', '` + p.serverID + `', ` + alexID + `, 1, 0, 10)`,
		`INSERT INTO invites(id, kind, code_hash, project_id, server_id, created_by, created_at, expires_at, max_uses) VALUES('inv_own', 'player', 'hash_own', '` + project + `', '` + p.serverID + `', ` + ownID + `, 1, 0, 10)`,
		`INSERT INTO join_requests(id, invite_id, server_id, player_uuid, player_name, state, created_at) VALUES('jr_steve', 'inv_own', '` + p.serverID + `', 'uuid-steve', 'Steve', 'pending', 1)`,
		`INSERT INTO player_origins(server_id, player_uuid, player_name, invite_id, joined_at) VALUES('` + p.serverID + `', 'uuid-kai', 'Kai', 'inv_own', 1)`,
		`INSERT INTO public_links(kind, token_hash, server_id, machine_id, created_at) VALUES('map', 'hash_map', '` + p.serverID + `', 'm_alex', 1)`,
		`INSERT INTO left_copies(server_id, machine_id, user_id, keep_days, left_at) VALUES('` + p.serverID + `', 'm_old', ` + alexID + `, 7, 1)`,
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
	if n := p.tokens(t); n != 0 {
		t.Fatalf("%d of alex's tokens still work once their deletion was asked for", n)
	}
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); !ok {
		t.Fatal("alex was deleted though the backups kept for them couldn't be listed")
	}
	for _, table := range []string{"join_requests", "player_origins", "public_links", "creator_servers"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE server_id = ?`, p.serverID); n != 0 {
			t.Errorf("%s still has alex's deleted server once the deletion stopped: %d", table, n)
		}
	}
	keptBackupsAt(e.env, map[string]string{"20261013-120000-abc123": keptFor(p.alex.id), "20261013-120000-def456": keptFor(p.alex.id + 1)})
	e.srv.eraseDueCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("alex's server was asked to be deleted %d times", n)
	}
	e.agent.mu.Lock()
	raw := e.agent.lastBody["POST /v1/servers/"+p.serverID+"/delete"]
	e.agent.mu.Unlock()
	if !strings.Contains(raw, `"forgetKey":true`) || strings.Contains(raw, "keepFinalBackupDays") || strings.Contains(raw, "keptFor") {
		t.Fatalf("the deletion asked for %s", raw)
	}
	if !agentHit(e.env, "DELETE /v1/kept-backups/20261013-120000-abc123") {
		t.Fatal("the final backup kept for alex wasn't deleted")
	}
	if agentHit(e.env, "DELETE /v1/kept-backups/20261013-120000-def456") {
		t.Fatal("a final backup kept for another account was deleted")
	}
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); ok {
		t.Fatal("alex still has an account")
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM users WHERE id = ?`, `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, `SELECT COUNT(*) FROM customer_homes WHERE user_id = ?`,
		`SELECT COUNT(*) FROM creator_servers WHERE user_id = ?`, `SELECT COUNT(*) FROM invites WHERE created_by = ?`, `SELECT COUNT(*) FROM left_copies WHERE user_id = ?`,
	} {
		if n := e.count(t, q, p.alex.id); n != 0 {
			t.Errorf("%s: %d", q, n)
		}
	}
	for _, table := range []string{"whop_memberships", "whop_customers", "whop_messages"} {
		if n := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE store_id = ? AND whop_user_id = 'user_alex'`, testStore); n != 0 {
			t.Errorf("%s still has alex at their store: %d", table, n)
		}
		if n := e.count(t, `SELECT COUNT(*) FROM `+table+` WHERE store_id = 'biz_other' AND whop_user_id = 'user_alex'`); n != 1 {
			t.Errorf("%s lost alex at another store: %d", table, n)
		}
	}
	var payer string
	var amount int64
	if err := e.srv.db.QueryRow(`SELECT whop_user_id, amount FROM whop_payments WHERE payment_id = 'pay_alex1'`).Scan(&payer, &amount); err != nil || payer != "" || amount != 1000 {
		t.Fatalf("alex's payment at their store: %q, %d, %v", payer, amount, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_payments WHERE payment_id = 'pay_alex2' AND whop_user_id = 'user_alex'`); n != 1 {
		t.Fatalf("alex's payment at another store lost who paid: %d", n)
	}
	rows := e.auditRows(t, "customer.erase")
	if len(rows) != 3 || !strings.HasPrefix(rows[0], "admin alex requested") ||
		!strings.HasPrefix(rows[1], "admin alex failed it stopped, after 1 server(s) with their backups were deleted, and goes on later") ||
		!strings.HasPrefix(rows[2], "admin alex succeeded") || !strings.Contains(rows[2], "on request: 0 server(s) with their backups, 1 kept backup(s); 1 payment(s) kept") {
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
	f.mu.Lock()
	f.installed["biz_other"].memberships["mem_alex3"]["status"] = "expired"
	f.mu.Unlock()
	e.srv.db.Exec(`UPDATE whop_stores SET polled_at = 0`)
	e.reconcile()
	if again := storeAccount(t, e, "biz_other", "user_alex"); again.State != CustomerPaused {
		t.Fatalf("alex at Other once the plan they bought again ended: %+v", again)
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
// runs keeps their account and the backups kept for them.
func TestOnlyTheOwnerDeletesACustomerWhosePlanEnded(t *testing.T) {
	f, e, own := storesWithCustomers(t)
	ctx := context.Background()
	other := storeAccount(t, e, "biz_other", "user_alex")
	keptBackupsAt(e, map[string]string{"20261013-120000-abc123": keptFor(other.UserID)})
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
	if _, err := e.srv.db.Exec(`INSERT INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, stale, updated_at)
		VALUES('biz_other', 'mem_alex9', 'user_alex', 'plan_other', 'active', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "DELETE", path, `{"confirm":"`+name+`"}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("deleting alex while Whop reports a renewal its API hasn't confirmed: %d %v", r.status, r.body)
	}
	if _, err := e.srv.db.Exec(`DELETE FROM whop_memberships WHERE membership_id = 'mem_alex9'`); err != nil {
		t.Fatal(err)
	}
	for who, c := range map[string]struct {
		body   string
		auth   map[string]string
		status int
	}{
		"the wrong name": {`{"confirm":"alex"}`, own.auth(), http.StatusBadRequest},
		"no name":        {`{}`, own.auth(), http.StatusBadRequest},
		"an admin":       {`{"confirm":"` + name + `"}`, addAdmin(t, e, "morgan", "*").auth(), http.StatusForbidden},
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
	if agentHit(e, "DELETE /v1/kept-backups/20261013-120000-abc123") {
		t.Fatal("the backup kept for alex, who bought again, was deleted")
	}
	if rows := e.auditRows(t, "customer.erase"); len(rows) != 2 || !strings.HasSuffix(rows[1], "refused they have a plan at their store again") {
		t.Fatalf("the audit log: %v", rows)
	}
}

// A customer who buys again while their deletion runs keeps whatever wasn't
// deleted yet, and their account: each server's deletion starts only after
// another look, with the store's pass and the hosting core waiting.
func TestACustomerWhoBuysAgainWhileBeingDeletedKeepsTheRest(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	const second, secondOp = "deadbeef45", "fedcba9876543210"
	if _, err := e.srv.db.Exec(`INSERT INTO creator_servers(server_id, user_id, created_at) VALUES(?, ?, 1)`, second, p.alex.id); err != nil {
		t.Fatal(err)
	}
	renew := func() {
		e.srv.db.Exec(`INSERT OR IGNORE INTO whop_memberships(store_id, membership_id, whop_user_id, plan_id, status, updated_at)
			VALUES(?, 'mem_alex2', 'user_alex', 'plan_starter', 'active', 1)`, testStore)
	}
	e.agent.mu.Lock()
	e.agent.replies["GET /v1/servers/"+second] = `{"id":"` + second + `","name":"alex-2"}`
	e.agent.statuses["POST /v1/servers/"+second+"/delete"] = http.StatusAccepted
	e.agent.replies["POST /v1/servers/"+second+"/delete"] = `{"id":"` + secondOp + `","status":"running"}`
	e.agent.replies["GET /v1/operations/"+secondOp] = `{"id":"` + secondOp + `","status":"succeeded"}`
	e.agent.before["POST /v1/servers/"+p.serverID+"/delete"] = renew
	e.agent.before["POST /v1/servers/"+second+"/delete"] = renew
	e.agent.mu.Unlock()
	e.reply("GET", "/v1/kept-backups", `[]`)
	if r := e.do(t, "DELETE", customerDeletePath(p.alex.id), `{"confirm":"alex"}`, p.own.auth()); r.status != http.StatusAccepted {
		t.Fatalf("asking for alex's deletion: %d %v", r.status, r.body)
	}
	e.srv.eraseDueCustomers(ctx)
	first, other := agentHit(e.env, "POST /v1/servers/"+p.serverID+"/delete"), agentHit(e.env, "POST /v1/servers/"+second+"/delete")
	if first == other {
		t.Fatalf("alex's servers were asked to be deleted: %s %v, %s %v", p.serverID, first, second, other)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM creator_servers WHERE user_id = ?`, p.alex.id); n != 1 {
		t.Fatalf("alex's servers left: %d", n)
	}
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); !ok {
		t.Fatal("alex, who bought again, lost their account")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM customers WHERE user_id = ? AND erase_requested_at > 0`, p.alex.id); n != 0 {
		t.Fatal("alex's deletion is still asked for once they bought again")
	}
	if rows := e.auditRows(t, "customer.erase"); len(rows) != 2 ||
		!strings.HasSuffix(rows[1], "refused they have a plan at their store again, after 1 server(s) with their backups were deleted") {
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
	if r := e.do(t, "PUT", "/api/customers/retention", `{"days":45}`, addAdmin(t, e.env, "morgan", "*").auth()); r.status != http.StatusForbidden {
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
	if rows := e.auditRows(t, "customer.retention"); len(rows) != 2 || !strings.HasSuffix(rows[0], "a customer is deleted 60 days after their servers") {
		t.Fatalf("the audit log of the setting: %v", rows)
	}
}

// A customer whose servers are being moved, or whose move left a copy on
// its old machine that isn't deleted yet, is deleted once that's done, so
// no copy of theirs is left behind.
func TestACustomersDeletionWaitsForTheirMoves(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	e.reply("GET", "/v1/kept-backups", `[]`)
	if _, err := e.srv.db.Exec(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine) VALUES(?, ?, 'm_old', 'm_new')`, p.serverID, p.alex.id); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "DELETE", customerDeletePath(p.alex.id), `{"confirm":"alex"}`, p.own.auth()); r.status != http.StatusAccepted {
		t.Fatalf("asking for alex's deletion: %d %v", r.status, r.body)
	}
	kept := func() bool {
		_, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
		return ok && p.deletions() == 0
	}
	e.srv.eraseDueCustomers(ctx)
	if !kept() {
		t.Fatal("alex was deleted while their server was being moved")
	}
	for _, q := range []string{`DELETE FROM server_moves WHERE user_id = ?`, `INSERT INTO left_copies(server_id, machine_id, user_id, keep_days) VALUES('cafebabe23', 'm_old', ?, 7)`} {
		if _, err := e.srv.db.Exec(q, p.alex.id); err != nil {
			t.Fatal(err)
		}
	}
	e.srv.eraseDueCustomers(ctx)
	if !kept() {
		t.Fatal("alex was deleted while a copy their move left waited to be deleted")
	}
	if _, err := e.srv.db.Exec(`UPDATE left_copies SET left_at = 1 WHERE user_id = ?`, p.alex.id); err != nil {
		t.Fatal(err)
	}
	e.srv.eraseDueCustomers(ctx)
	if _, ok, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex"); ok || p.deletions() != 1 {
		t.Fatalf("alex once their move was done: deleted %d times", p.deletions())
	}
}

// The last look before a customer's records go, in the same transaction,
// keeps everything of one who bought again since the deletion began: their
// membership gives access, or they run as active again, whatever the
// deletion saw of them when it began.
func TestACustomerWhoBuysAgainWhileBeingDeletedKeepsEverything(t *testing.T) {
	_, e, _ := storesWithCustomers(t)
	ctx := context.Background()
	other := storeAccount(t, e, "biz_other", "user_alex")
	c, ok, err := e.srv.erasableCustomer(ctx, other.UserID)
	if err != nil || !ok {
		t.Fatalf("alex at Other: %v %v", ok, err)
	}
	c.state = CustomerPaused
	if _, err := e.srv.eraseCustomerRecords(ctx, c); !errors.Is(err, errCustomerHasPlan) {
		t.Fatalf("deleting the records of alex, whose membership gives access: %v", err)
	}
	if _, err := e.srv.db.Exec(`UPDATE whop_memberships SET status = 'expired' WHERE store_id = 'biz_other' AND whop_user_id = 'user_alex'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.eraseCustomerRecords(ctx, c); !errors.Is(err, errCustomerHasPlan) {
		t.Fatalf("deleting the records of alex, who runs as active: %v", err)
	}
	if again := storeAccount(t, e, "biz_other", "user_alex"); again.UserID != other.UserID {
		t.Fatalf("alex at Other: %+v", again)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM whop_memberships WHERE store_id = 'biz_other' AND whop_user_id = 'user_alex'`); n != 1 {
		t.Fatalf("alex's memberships at Other: %d", n)
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
