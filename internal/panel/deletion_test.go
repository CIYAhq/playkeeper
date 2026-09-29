package panel

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// deletionOp is the operation the fake machine answers a deletion with.
const deletionOp = "0123456789abcdef"

// lapse has the fake machine answer the deletion of alex's server, which
// ends with status.
func (p pausable) lapse(status string) {
	p.e.agent.mu.Lock()
	defer p.e.agent.mu.Unlock()
	p.e.agent.replies["GET /v1/servers/"+p.serverID] = `{"id":"` + p.serverID + `","name":"alex"}`
	p.e.agent.statuses["POST /v1/servers/"+p.serverID+"/delete"] = http.StatusAccepted
	p.e.agent.replies["POST /v1/servers/"+p.serverID+"/delete"] = `{"id":"` + deletionOp + `","status":"running"}`
	p.e.agent.replies["GET /v1/operations/"+deletionOp] = `{"id":"` + deletionOp + `","status":"` + status + `","error":"the disk is full"}`
}

// deletions counts the deletions of alex's server the machine was asked for.
func (p pausable) deletions() int {
	p.e.agent.mu.Lock()
	defer p.e.agent.mu.Unlock()
	n := 0
	for _, h := range p.e.agent.hits {
		if h == "POST /v1/servers/"+p.serverID+"/delete" {
			n++
		}
	}
	return n
}

// home is alex's home machine, "" while none is set aside for them.
func (p pausable) home(t *testing.T) string {
	t.Helper()
	var m string
	if err := p.e.srv.db.QueryRow(`SELECT machine_id FROM customer_homes WHERE user_id = ?`, p.alex.id).Scan(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// lapsed pauses alex and lets the grace period end.
func (p pausable) lapsed(t *testing.T) {
	t.Helper()
	if err := p.core.PauseCustomer(context.Background(), p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	p.e.clock.add(graceDays*24*time.Hour + time.Minute)
}

// Once a customer's plan has been over for 14 days, and not a day before,
// each server they created is deleted with a final backup kept 30 days
// under their account's label. Their home machine is freed, they're told
// until when the backups download, and their dashboard says their servers
// are gone. It happens once.
func TestALapsedCustomersServersGoWithAFinalBackupKept(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	mid := p.home(t)
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	e.clock.add((graceDays - 1) * 24 * time.Hour)
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 0 {
		t.Fatalf("a paused customer's server was deleted a day before their grace period ended (%d)", n)
	}
	e.clock.add(24*time.Hour + time.Minute)
	kicked(e.env)
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("the lapsed customer's server was asked to be deleted %d times", n)
	}
	e.agent.mu.Lock()
	raw := e.agent.lastBody["POST /v1/servers/"+p.serverID+"/delete"]
	e.agent.mu.Unlock()
	for _, want := range []string{`"confirm":"alex"`, `"actor":"playkeeper"`, `"forgetKey":true`, `"keepFinalBackupDays":30`, `"keptFor":"account-` + strconv.FormatInt(p.alex.id, 10) + `"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("the deletion asked for %s, missing %s", raw, want)
		}
	}
	if ids, _ := e.srv.creatorServers(p.alex.id); len(ids) != 0 {
		t.Fatalf("alex still created %v", ids)
	}
	if h := p.home(t); h != "" {
		t.Fatalf("alex's home is still %q", h)
	}
	var deletedAt int64
	var keptOn string
	if err := e.srv.db.QueryRow(`SELECT servers_deleted_at, final_backups_machine FROM customers WHERE user_id = ?`, p.alex.id).Scan(&deletedAt, &keptOn); err != nil || deletedAt == 0 || keptOn != mid {
		t.Fatalf("alex's record: deleted at %d, final backups on %q, %v", deletedAt, keptOn, err)
	}
	if !kicked(e.env) {
		t.Fatal("deleting alex's servers didn't send the limits at once")
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady, messagePaused, messageDeleted}) || !strings.Contains(p.n.sent[2].Text, "until") {
		t.Fatalf("what alex was told: %v %+v", k, p.n.sent)
	}

	e.clock.add(lapsedEvery)
	e.srv.deleteLapsedCustomers(ctx)
	if n, k := p.deletions(), p.n.kinds(); n != 1 || len(k) != 3 {
		t.Fatalf("looking again deleted %d times and told %v", n, k)
	}
	alex := signIn(t, e.env, p.alex.id)
	var me struct {
		Access struct {
			PausedUntil    *time.Time `json:"pausedUntil"`
			ServersDeleted bool       `json:"serversDeleted"`
		} `json:"access"`
	}
	e.get(t, "/api/auth/me", alex.cookie, &me)
	if !me.Access.ServersDeleted || me.Access.PausedUntil != nil {
		t.Fatalf("alex's dashboard says deleted %v, paused until %v", me.Access.ServersDeleted, me.Access.PausedUntil)
	}
}

// A customer who renews keeps their servers: before their grace period
// ends, nothing is deleted. One who renews after their servers were deleted
// is placed again and told their server is ready, as a new customer is.
func TestARenewalKeepsWhatsLeft(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	p.lapsed(t)
	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 0 {
		t.Fatalf("a renewed customer's server was deleted (%d)", n)
	}

	p.lapsed(t)
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("the lapsed customer's server was asked to be deleted %d times", n)
	}
	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := p.core.CustomerAccount(ctx, whopProvider, "user_alex")
	if info.State != CustomerActive || p.home(t) == "" {
		t.Fatalf("alex once renewed after the deletion: %+v, home %q", info, p.home(t))
	}
	want := []string{messageReady, messagePaused, messageBack, messagePaused, messageDeleted, messageBack, messageReady}
	if k := p.n.kinds(); !slices.Equal(k, want) {
		t.Fatalf("what alex was told: %v, want %v", k, want)
	}
	var deletedAt int64
	if err := e.srv.db.QueryRow(`SELECT servers_deleted_at FROM customers WHERE user_id = ?`, p.alex.id).Scan(&deletedAt); err != nil || deletedAt != 0 {
		t.Fatalf("alex's servers are still recorded as deleted once they renewed: %d, %v", deletedAt, err)
	}
}

// Only a customer still paused past their grace period, with their servers
// not yet deleted, loses them, however the deletion is reached: not one
// whose grace period hasn't ended, one the owner suspended, or one whose
// servers are gone already, who isn't told twice.
func TestOnlyALapsedCustomerIsDeleted(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.deleteLapsedCustomer(ctx, p.alex.id); err != nil || p.deletions() != 0 {
		t.Fatalf("deleting before the grace period ended: %v, asked %d times", err, p.deletions())
	}
	e.clock.add(graceDays*24*time.Hour + time.Minute)
	for _, state := range []CustomerState{CustomerSuspended, CustomerPaused} {
		if _, err := e.srv.db.Exec(`UPDATE customers SET state = ? WHERE user_id = ?`, string(state), p.alex.id); err != nil {
			t.Fatal(err)
		}
		if err := e.srv.deleteLapsedCustomer(ctx, p.alex.id); err != nil {
			t.Fatalf("deleting a %s customer: %v", state, err)
		}
	}
	if n := p.deletions(); n != 1 {
		t.Fatalf("the server was asked to be deleted %d times, suspended then paused", n)
	}
	if err := e.srv.deleteLapsedCustomer(ctx, p.alex.id); err != nil || len(p.n.kinds()) != 3 {
		t.Fatalf("deleting again: %v, told %v", err, p.n.kinds())
	}
}

// A customer who renews while their server is being deleted keeps what's
// left, and still gets that server's final backup: it's listed, and their
// dashboard offers it, once they're back.
func TestARenewalDuringADeletionKeepsItsFinalBackup(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	p.lapsed(t)
	var once sync.Once
	e.agent.mu.Lock()
	e.agent.before["GET /v1/operations/"+deletionOp] = func() {
		once.Do(func() {
			if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
				t.Error(err)
			}
		})
	}
	e.agent.mu.Unlock()
	e.srv.deleteLapsedCustomers(ctx)
	info, _, _ := p.core.CustomerAccount(ctx, whopProvider, "user_alex")
	if info.State != CustomerActive || p.deletions() != 1 {
		t.Fatalf("alex renewed during the deletion: %+v, asked %d times", info, p.deletions())
	}
	e.reply("GET", "/v1/kept-backups", `[{"id":"20261013-120000-abc123","serverId":"`+p.serverID+`","serverName":"alex","keptFor":"`+keptFor(p.alex.id)+`","fileName":"playkeeper-alex-20261013-120000-abc123.tar.gz","sizeBytes":5,"sha256":"","madeAt":"2026-10-13T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z"}]`)
	alex := signIn(t, e.env, p.alex.id)
	var list []finalBackup
	if st := e.get(t, "/api/final-backups", alex.cookie, &list); st != http.StatusOK || len(list) != 1 {
		t.Fatalf("alex's final backups once back: %d %+v", st, list)
	}
	var me struct {
		Access struct {
			ServersDeleted bool `json:"serversDeleted"`
			FinalBackups   bool `json:"finalBackups"`
		} `json:"access"`
	}
	e.get(t, "/api/auth/me", alex.cookie, &me)
	if !me.Access.FinalBackups || me.Access.ServersDeleted {
		t.Fatalf("alex's dashboard once back: final backups %v, servers deleted %v", me.Access.FinalBackups, me.Access.ServersDeleted)
	}
}

// A deletion the machine couldn't finish changes nothing, and the next look
// tries again.
func TestADeletionThatFailsIsTriedAgain(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("failed")
	p.lapsed(t)
	e.srv.deleteLapsedCustomers(ctx)
	if n := p.deletions(); n != 1 {
		t.Fatalf("the deletion was asked for %d times", n)
	}
	if ids, _ := e.srv.creatorServers(p.alex.id); len(ids) != 1 || p.home(t) == "" || len(p.n.kinds()) != 2 {
		t.Fatalf("after a failed deletion: created %v, home %q, told %v", ids, p.home(t), p.n.kinds())
	}
	p.lapse("succeeded")
	e.srv.deleteLapsedCustomers(ctx)
	if n, k := p.deletions(), p.n.kinds(); n != 2 || len(k) != 3 || k[2] != messageDeleted {
		t.Fatalf("trying again: asked %d times, told %v", n, k)
	}
}

// A customer whose servers were deleted lists and downloads their final
// backups, from the machine keeping them, and nobody else's: another
// account's on the same machine, or one that isn't a kept backup, isn't
// found. Nobody else gets theirs either.
func TestACustomerDownloadsOnlyTheirOwnFinalBackups(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	p.lapse("succeeded")
	p.lapsed(t)
	e.srv.deleteLapsedCustomers(ctx)
	label := keptFor(p.alex.id)
	e.reply("GET", "/v1/kept-backups", `[{"id":"20261013-120000-abc123","serverId":"`+p.serverID+`","serverName":"alex","keptFor":"`+label+`","fileName":"playkeeper-alex-20261013-120000-abc123.tar.gz","sizeBytes":5,"sha256":"","madeAt":"2026-10-13T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z"},
		{"id":"20261013-120000-def456","serverId":"deadbeef45","serverName":"sam","keptFor":"account-999","fileName":"playkeeper-sam-20261013-120000-def456.tar.gz","sizeBytes":5,"sha256":"","madeAt":"2026-10-13T12:00:00Z","expiresAt":"2026-11-12T12:00:00Z"}]`)
	for _, kid := range []string{"20261013-120000-abc123", "20261013-120000-def456"} {
		e.agent.mu.Lock()
		e.agent.answers["GET /v1/kept-backups/"+kid+"/download"] = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Disposition", `attachment; filename="playkeeper-alex-`+kid+`.tar.gz"`)
			io.WriteString(w, "world")
		}
		e.agent.mu.Unlock()
	}
	alex := signIn(t, e.env, p.alex.id)
	var list []finalBackup
	if st := e.get(t, "/api/final-backups", alex.cookie, &list); st != http.StatusOK || len(list) != 1 || list[0].ID != "20261013-120000-abc123" || list[0].ServerName != "alex" {
		t.Fatalf("alex's final backups: %d %+v", st, list)
	}
	e.agent.mu.Lock()
	asked := ""
	for _, r := range e.agent.reqs {
		if r.method == "GET" && r.path == "/v1/kept-backups" {
			asked = r.query.Get("keptFor")
		}
	}
	e.agent.mu.Unlock()
	if asked != label {
		t.Fatalf("the machine was asked for the kept backups of %q, not %q", asked, label)
	}
	resp, body := e.raw(t, "GET", "/api/final-backups/20261013-120000-abc123/download", "", alex.auth())
	if resp.StatusCode != http.StatusOK || body != "world" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("alex downloads their final backup: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	for _, kid := range []string{"20261013-120000-def456", "not-a-backup"} {
		if resp, body := e.raw(t, "GET", "/api/final-backups/"+kid+"/download", "", alex.auth()); resp.StatusCode != http.StatusNotFound {
			t.Fatalf("alex downloads %s: %d %q", kid, resp.StatusCode, body)
		}
	}
	own := signIn(t, e.env, p.own.id)
	if st := e.get(t, "/api/final-backups", own.cookie, &list); st != http.StatusOK || len(list) != 0 {
		t.Fatalf("the owner's own final backups: %d %+v", st, list)
	}
	if resp, body := e.raw(t, "GET", "/api/final-backups/20261013-120000-abc123/download", "", own.auth()); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("someone else downloads alex's final backup: %d %q", resp.StatusCode, body)
	}
}
