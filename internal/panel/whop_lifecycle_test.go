package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

const buyerServer = "cafebabe23"

// fakeServer is a buyer's server as the fake agent answers for it: its
// phase and backups change as the panel asks for a backup, a stop or a
// delete.
type fakeServer struct {
	mu      sync.Mutex
	phase   string
	backups []map[string]any
	gone    bool
	asked   []string
	deletes []map[string]any
}

func (fs *fakeServer) ask(what string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.asked = append(fs.asked, what)
}

func (fs *fakeServer) askedFor() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.asked...)
}

// buyerEnv is a dashboard selling for Pip Hosting whose buyer alex bought
// Starter, joined with the invite and created one server.
type buyerEnv struct {
	f      *fakeWhop
	e      *env
	own    member
	alex   member
	server *fakeServer
}

func newBuyerEnv(t *testing.T) buyerEnv {
	t.Helper()
	f, e, own := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_1", whop.EventMembershipActivated, m)
	e.srv.reconcileWhop(context.Background())
	_, code := inviteIn(t, f.sent("user_alex")[0])
	r := e.public(t, "accept", codeBody(code, "username", "alex", "password", "buyer password 1"))
	if r.status != http.StatusOK || r.cookie == "" {
		t.Fatalf("accept: %d %v", r.status, r.body)
	}
	var alexID int64
	e.srv.db.QueryRow(`SELECT id FROM users WHERE username = 'alex'`).Scan(&alexID)
	if _, err := e.srv.db.Exec(`INSERT INTO creator_servers(server_id, user_id, created_at) VALUES(?,?,0)`, buyerServer, alexID); err != nil {
		t.Fatal(err)
	}
	fs := &fakeServer{phase: "online"}
	path := "/v1/servers/" + buyerServer
	e.agent.mu.Lock()
	e.agent.answers["GET "+path] = func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.gone {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"No such server.","code":"not_found"}`)
			return
		}
		fmt.Fprintf(w, `{"id":%q,"name":"alex","phase":%q}`, buyerServer, fs.phase)
	}
	e.agent.answers["GET "+path+"/backups"] = func(w http.ResponseWriter, _ *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		json.NewEncoder(w).Encode(append([]map[string]any{}, fs.backups...))
	}
	e.agent.answers["POST "+path+"/backups"] = func(w http.ResponseWriter, r *http.Request) {
		fs.ask("backup")
		fs.mu.Lock()
		fs.backups = append(fs.backups, map[string]any{"id": "b1", "serverId": buyerServer, "kind": "manual", "createdAt": e.clock.now().Format(time.RFC3339Nano)})
		fs.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-backup","kind":"backup","status":"running"}`)
	}
	e.agent.answers["POST "+path+"/stop"] = func(w http.ResponseWriter, _ *http.Request) {
		fs.ask("stop")
		fs.mu.Lock()
		fs.phase = "stopped"
		fs.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-stop","kind":"stop","status":"running"}`)
	}
	e.agent.answers["POST "+path+"/delete"] = func(w http.ResponseWriter, r *http.Request) {
		// The fake agent has read the body already, and keeps it.
		e.agent.mu.Lock()
		raw := e.agent.lastBody["POST "+path+"/delete"]
		e.agent.mu.Unlock()
		var body map[string]any
		json.Unmarshal([]byte(raw), &body)
		fs.ask("delete")
		fs.mu.Lock()
		fs.deletes = append(fs.deletes, body)
		fs.gone = true
		fs.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-delete","kind":"delete","status":"running"}`)
	}
	e.agent.mu.Unlock()
	return buyerEnv{f: f, e: e, own: own, alex: member{id: alexID, cookie: r.cookie, csrf: r.body["csrfToken"].(string)}, server: fs}
}

// end has Whop say alex's membership ended, and the dashboard look.
func (b buyerEnv) end(t *testing.T) {
	t.Helper()
	m := b.f.buy("mem_alex1", "user_alex", "plan_starter", "canceled")
	b.e.deliver(t, fmt.Sprintf("msg_end_%d", b.e.clock.now().UnixNano()), whop.EventMembershipDeactivated, m)
	b.e.srv.reconcileWhop(context.Background())
}

func (b buyerEnv) buyerView(t *testing.T) whopBuyerView {
	t.Helper()
	v := b.e.whopView(t, signIn(t, b.e, b.own.id))
	if len(v.Buyers) != 1 {
		t.Fatalf("buyers: %+v", v.Buyers)
	}
	return v.Buyers[0]
}

func TestWhenAPlanEndsSignInPausesAndTheServerGetsAFinalBackupThenStops(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	token := e.do(t, "POST", "/api/tokens", `{"name":"alex's bot","role":"viewer","allServers":false,"servers":[],"days":30}`, b.alex.auth())
	b.end(t)

	var paused int64
	e.srv.db.QueryRow(`SELECT paused_at FROM users WHERE id = ?`, b.alex.id).Scan(&paused)
	if paused == 0 {
		t.Fatal("alex's sign-in isn't paused")
	}
	if r := e.do(t, "GET", "/api/auth/me", "", b.alex.auth()); r.status != http.StatusUnauthorized {
		t.Fatalf("alex's session after the plan ended: %d", r.status)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"alex","password":"buyer password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != http.StatusForbidden || r.body["error"] != "This account is paused." {
		t.Fatalf("alex signs in: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"alex","password":"wrong password 1"}`, map[string]string{"X-Requested-With": "playkeeper"}); r.status != http.StatusUnauthorized {
		t.Fatalf("a wrong password for a paused account: %d %v", r.status, r.body)
	}
	if id, _ := token.body["token"].(map[string]any); id != nil {
		var revoked int64
		e.srv.db.QueryRow(`SELECT revoked_at FROM api_tokens WHERE id = ?`, id["id"]).Scan(&revoked)
		if revoked == 0 {
			t.Fatal("alex's token still works")
		}
	}
	if got := b.server.askedFor(); len(got) != 1 || got[0] != "backup" {
		t.Fatalf("asked of the server: %v", got)
	}
	msgs := b.f.sent("user_alex")
	if len(msgs) != 2 || !strings.Contains(msgs[1], "Your Pip Hosting plan ended, so your server is stopped") || !strings.Contains(msgs[1], "Renew within 14 days") {
		t.Fatalf("messages: %q", msgs)
	}
	// The next look finds the final backup and stops the server.
	e.srv.reconcileWhop(context.Background())
	if got := b.server.askedFor(); len(got) != 2 || got[1] != "stop" {
		t.Fatalf("asked of the server: %v", got)
	}
	e.srv.reconcileWhop(context.Background())
	e.srv.reconcileWhop(context.Background())
	if got := b.server.askedFor(); len(got) != 2 {
		t.Fatalf("asked again once stopped: %v", got)
	}
	if len(b.f.sent("user_alex")) != 2 {
		t.Fatal("told again")
	}
	v := b.buyerView(t)
	if v.Status != "paused" || v.DeletesAt == nil || !v.DeletesAt.Equal(e.clock.now().Add(whopGrace).Truncate(time.Millisecond)) {
		t.Fatalf("alex on the page: %+v (now %v)", v, e.clock.now())
	}
	if rows := e.auditRows(t, "whop.buyer_ended"); len(rows) != 1 {
		t.Fatalf("audit: %v", rows)
	}
}

func TestAServerWhoseFinalBackupNeverComesStillStops(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	e.agent.mu.Lock()
	e.agent.answers["POST /v1/servers/"+buyerServer+"/backups"] = func(w http.ResponseWriter, _ *http.Request) {
		b.server.ask("backup")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"Busy.","code":"busy"}`)
	}
	e.agent.mu.Unlock()
	b.end(t)
	e.srv.reconcileWhop(context.Background())
	if got := b.server.askedFor(); len(got) != 2 || got[0] != "backup" || got[1] != "backup" {
		t.Fatalf("asked of the server: %v", got)
	}
	e.clock.add(whopBackupWait)
	e.srv.reconcileWhop(context.Background())
	if got := b.server.askedFor(); got[len(got)-1] != "stop" {
		t.Fatalf("not stopped after the wait: %v", got)
	}
}

func TestARenewedPlanBringsTheBuyerBack(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	b.end(t)
	e.clock.add(3 * 24 * time.Hour)
	m := b.f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_back", whop.EventMembershipActivated, m)
	e.srv.reconcileWhop(context.Background())
	var paused int64
	e.srv.db.QueryRow(`SELECT paused_at FROM users WHERE id = ?`, b.alex.id).Scan(&paused)
	if paused != 0 {
		t.Fatal("alex is still paused")
	}
	if r := e.do(t, "POST", "/api/auth/login", `{"username":"alex","password":"buyer password 1"}`, map[string]string{"X-Requested-With": "playkeeper"}); r.status != http.StatusOK {
		t.Fatalf("alex signs in again: %d %v", r.status, r.body)
	}
	msgs := b.f.sent("user_alex")
	if !strings.Contains(msgs[len(msgs)-1], "Welcome back to Pip Hosting! Your panel is open again: sign in at "+whopDashboard) {
		t.Fatalf("messages: %q", msgs)
	}
	if v := b.buyerView(t); v.Status != "joined" {
		t.Fatalf("alex on the page: %+v", v)
	}
	// Two weeks on, nothing is deleted.
	e.clock.add(whopGrace)
	e.srv.reconcileWhop(context.Background())
	for _, what := range b.server.askedFor() {
		if what == "delete" {
			t.Fatal("a renewed buyer's server was deleted")
		}
	}
}

func TestFourteenDaysAfterAPlanEndsTheServersAreDeleted(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	b.end(t)
	e.srv.reconcileWhop(context.Background())
	e.clock.add(whopGrace - time.Minute)
	e.srv.reconcileWhop(context.Background())
	if got := b.server.askedFor(); len(got) != 2 {
		t.Fatalf("deleted early: %v", got)
	}
	e.clock.add(time.Minute)
	e.srv.reconcileWhop(context.Background())
	b.server.mu.Lock()
	deletes := b.server.deletes
	b.server.mu.Unlock()
	if len(deletes) != 1 || deletes[0]["confirm"] != "alex" || deletes[0]["forgetKey"] != true || deletes[0]["actor"] != "whop" {
		t.Fatalf("deletes: %v", deletes)
	}
	// The next look finds it gone.
	e.srv.reconcileWhop(context.Background())
	msgs := b.f.sent("user_alex")
	if !strings.Contains(msgs[len(msgs)-1], "Your Pip Hosting server and its backups were deleted, 14 days after your plan ended") {
		t.Fatalf("messages: %q", msgs)
	}
	if v := b.buyerView(t); v.Status != "deleted" {
		t.Fatalf("alex on the page: %+v", v)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM creator_servers WHERE user_id = ?`, b.alex.id).Scan(&n)
	if n != 0 {
		t.Fatal("the deleted server still counts as alex's")
	}
	// Buying again brings alex back to an empty allowance.
	m := b.f.buy("mem_alex2", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_again", whop.EventMembershipActivated, m)
	e.srv.reconcileWhop(context.Background())
	msgs = b.f.sent("user_alex")
	if !strings.Contains(msgs[len(msgs)-1], "Your old server was deleted after your last plan ended, so create a new one.") {
		t.Fatalf("messages: %q", msgs)
	}
}

func TestTheAllowanceFollowsThePlansAndACancellationIsRemindedOnce(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	b.f.buy("mem_alex1", "user_alex", "plan_big", "active")
	e.deliver(t, "msg_up", whop.EventMembershipActivated, b.f.memberships["mem_alex1"])
	e.srv.reconcileWhop(context.Background())
	var al invites.Allowance
	e.srv.db.QueryRow(`SELECT allowance_servers, allowance_memory_mb FROM project_members WHERE user_id = ?`, b.alex.id).Scan(&al.Servers, &al.MemoryMB)
	if al != (invites.Allowance{Servers: 2, MemoryMB: 8192}) {
		t.Fatalf("alex's allowance after upgrading: %+v", al)
	}
	msgs := b.f.sent("user_alex")
	if !strings.Contains(msgs[len(msgs)-1], "you can now have up to 2 servers with 8 GB of memory between them") {
		t.Fatalf("messages: %q", msgs)
	}
	told := len(msgs)
	m := b.f.buy("mem_alex1", "user_alex", "plan_big", "active")
	b.f.mu.Lock()
	m["cancel_at_period_end"], m["current_period_end"] = true, "2026-10-24T12:00:00Z"
	b.f.mu.Unlock()
	e.deliver(t, "msg_cancel", whop.EventMembershipCancelling, m)
	e.srv.reconcileWhop(context.Background())
	e.srv.reconcileWhop(context.Background())
	msgs = b.f.sent("user_alex")
	if len(msgs) != told+1 || !strings.Contains(msgs[told], "You cancelled your Pip Hosting plan. It keeps running until 24 October. Download your world before then") {
		t.Fatalf("messages after cancelling: %q", msgs[told:])
	}
	var paused int64
	e.srv.db.QueryRow(`SELECT paused_at FROM users WHERE id = ?`, b.alex.id).Scan(&paused)
	if paused != 0 {
		t.Fatal("a cancelled plan that still runs paused alex")
	}
}

func TestAMembershipWhopHasntConfirmedChangesNothing(t *testing.T) {
	b := newBuyerEnv(t)
	e := b.e
	m := b.f.buy("mem_alex1", "user_alex", "plan_starter", "canceled")
	e.deliver(t, "msg_end", whop.EventMembershipDeactivated, m)
	// Whop can't be asked: the delivery stays unconfirmed.
	b.f.srv.Close()
	e.srv.reconcileWhop(context.Background())
	var paused int64
	e.srv.db.QueryRow(`SELECT paused_at FROM users WHERE id = ?`, b.alex.id).Scan(&paused)
	if paused != 0 || len(b.server.askedFor()) != 0 {
		t.Fatalf("an unconfirmed delivery paused alex (%d) or touched the server (%v)", paused, b.server.askedFor())
	}
}
