package panel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/whop"
)

// connectedWhop is a dashboard selling for Pip Hosting, whose Big plan the
// owner gave 2 servers with 8 GB.
func connectedWhop(t *testing.T) (*fakeWhop, *env, member) {
	t.Helper()
	f := newFakeWhop(t)
	e := newWhopEnv(t, f)
	own := owner(t, e)
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("connect: %d %v", r.status, r.body)
	}
	if r := e.do(t, "PUT", "/api/whop/plans/plan_big", `{"servers":2,"memoryMB":8192}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("Big's allowance: %d %v", r.status, r.body)
	}
	return f, e, own
}

// buy gives a buyer a membership of a plan on the fake Whop.
func (f *fakeWhop) buy(id, user, plan, status string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]any{"id": id, "status": status, "plan_id": plan, "product_id": "prod_mc", "user_id": user, "cancel_at_period_end": false}
	f.memberships[id] = m
	return m
}

func (f *fakeWhop) sent(user string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.messages["chan_"+user]...)
}

// deliver posts a membership event to the webhook as Whop would, signed at
// the dashboard's time.
func (e *env) deliver(t *testing.T, id, event string, m map[string]any) resp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "type": event, "api_version": "v1", "account_id": "biz_pip", "data": m})
	hdr := map[string]string{}
	for k, v := range whop.SignWebhook(whopTestSecret, id, e.clock.now(), body) {
		hdr[k] = v[0]
	}
	hdr["Origin"] = ""
	return e.do(t, "POST", whopWebhookPath, string(body), hdr)
}

var reInviteLink = regexp.MustCompile(`\((https://beta\.playkeeper\.me:8443/join/([A-Za-z0-9]+))\)`)

// inviteIn is the invite link and code in a message sent to a buyer.
func inviteIn(t *testing.T, msg string) (link, code string) {
	t.Helper()
	m := reInviteLink.FindStringSubmatch(msg)
	if m == nil {
		t.Fatalf("no invite link in %q", msg)
	}
	return m[1], m[2]
}

func TestConnectingAddsTheWebhookAndDisconnectingRemovesIt(t *testing.T) {
	f, e, own := connectedWhop(t)
	var hook map[string]any
	for _, h := range f.webhooks {
		hook = h
	}
	events, _ := hook["events"].([]any)
	if len(f.webhooks) != 1 || hook["url"] != whopDashboard+whopWebhookPath || hook["api_version"] != "v1" || hook["api_version_date"] != whop.APIVersion ||
		hook["resource_id"] != "biz_pip" || len(events) != 3 {
		t.Fatalf("webhooks: %v", f.webhooks)
	}
	if v := e.whopView(t, own); !v.Webhook {
		t.Fatalf("the view doesn't know of the webhook: %+v", v)
	}
	// A new address moves it rather than adding another.
	e.setAddress(t, "play.pip.gg")
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	if len(f.webhooks) != 1 || hook["url"] != "https://play.pip.gg:8443"+whopWebhookPath {
		t.Fatalf("after the address changed: %v", f.webhooks)
	}
	if r := e.do(t, "DELETE", "/api/whop", "", own.auth()); r.status != http.StatusOK || len(f.webhooks) != 0 {
		t.Fatalf("disconnect: %d, webhooks %v", r.status, f.webhooks)
	}
}

// The whole path a buyer takes: Whop tells the dashboard of a paid
// membership, the buyer gets a creator invite with their plan's allowance
// in the store's support chat, and the link makes their account.
func TestABuyersMembershipBecomesACreatorInviteInTheirSupportChat(t *testing.T) {
	f, e, own := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "trialing")
	if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("delivery: %d %v", r.status, r.body)
	}
	e.srv.reconcileWhop(context.Background())
	msgs := f.sent("user_alex")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "Thanks for choosing Pip Hosting!") || !strings.Contains(msgs[0], "up to 1 server with 4 GB of memory") {
		t.Fatalf("messages to alex: %q", msgs)
	}
	_, code := inviteIn(t, msgs[0])
	var inv invites.Invite
	for _, row := range e.teamInvites(t, own) {
		inv = row
	}
	if inv.Allowance != (invites.Allowance{Servers: 1, MemoryMB: 4096}) || inv.Label != "alexplays" {
		t.Fatalf("the invite: %+v", inv)
	}
	if rows := e.auditRows(t, "invite.create"); len(rows) != 1 || !strings.HasPrefix(rows[0], "whop ") || !strings.Contains(rows[0], "Sell on Whop, for alexplays") || strings.Contains(rows[0], code) {
		t.Fatalf("audit: %v", rows)
	}
	// Reconciling again sends nothing more.
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 1 {
		t.Fatal("a second invite for the same buyer")
	}
	v := e.whopView(t, own)
	if len(v.Buyers) != 1 || v.Buyers[0].Status != "invited" || v.Buyers[0].Username != "alexplays" || v.Buyers[0].InvitedAt == nil {
		t.Fatalf("buyers: %+v", v.Buyers)
	}

	if r := e.public(t, "accept", codeBody(code, "username", "alex", "password", "buyer password 1")); r.status != http.StatusOK {
		t.Fatalf("accept: %d %v", r.status, r.body)
	}
	var servers, memory int
	e.srv.db.QueryRow(`SELECT m.allowance_servers, m.allowance_memory_mb FROM users u JOIN project_members m ON m.user_id = u.id WHERE u.username = 'alex'`).Scan(&servers, &memory)
	if servers != 1 || memory != 4096 {
		t.Fatalf("alex's allowance: %d servers, %d MB", servers, memory)
	}
	v = e.whopView(t, own)
	if len(v.Buyers) != 1 || v.Buyers[0].Status != "joined" || v.Buyers[0].Account != "alex" {
		t.Fatalf("buyers after joining: %+v", v.Buyers)
	}
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 1 {
		t.Fatal("a buyer who joined got another invite")
	}
}

func (e *env) teamInvites(t *testing.T, m member) []invites.Invite {
	t.Helper()
	var team teamBody
	e.get(t, "/api/team", m.cookie, &team)
	var out []invites.Invite
	for _, inv := range team.Invites {
		out = append(out, inv.Invite)
	}
	return out
}

func TestTheWebhookTakesOnlyWhopsOwnDeliveriesOnce(t *testing.T) {
	f, e, _ := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	body, _ := json.Marshal(map[string]any{"type": whop.EventMembershipActivated, "data": m})
	forged := map[string]string{"webhook-id": "msg_9", "webhook-timestamp": "1790672400", "webhook-signature": "v1,AAAA", "Origin": ""}
	if r := e.do(t, "POST", whopWebhookPath, string(body), forged); r.status != http.StatusUnauthorized {
		t.Fatalf("a forged delivery: %d", r.status)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_memberships`).Scan(&n)
	if n != 0 {
		t.Fatal("a forged delivery was kept")
	}
	// Signed long ago: a replay.
	e.clock.add(-10 * time.Minute)
	old := map[string]string{"Origin": ""}
	for k, v := range whop.SignWebhook(whopTestSecret, "msg_8", e.clock.now(), body) {
		old[k] = v[0]
	}
	e.clock.add(10 * time.Minute)
	if r := e.do(t, "POST", whopWebhookPath, string(body), old); r.status != http.StatusUnauthorized {
		t.Fatalf("a delivery signed ten minutes ago: %d", r.status)
	}
	if r := e.do(t, "GET", whopWebhookPath, "", map[string]string{"Origin": ""}); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r.status)
	}
	// Whop retries a delivery with the same id; it counts once.
	for range 2 {
		if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
			t.Fatalf("delivery: %d", r.status)
		}
	}
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_deliveries`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d deliveries kept for one id", n)
	}
}

func TestWithoutAConnectionTheWebhookIsNotThere(t *testing.T) {
	e := newEnv(t)
	owner(t, e)
	if r := e.do(t, "POST", whopWebhookPath, `{}`, map[string]string{"Origin": ""}); r.status != http.StatusNotFound {
		t.Fatalf("webhook without a connection: %d", r.status)
	}
}

func TestReadingMembershipsCatchesWhatNoWebhookSaid(t *testing.T) {
	f, e, _ := connectedWhop(t)
	// The first look after connecting reads every membership there is.
	f.buy("mem_early", "user_early", "plan_starter", "active")
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_early")) != 1 {
		t.Fatal("a membership from before connecting got no invite")
	}
	f.buy("mem_sam1", "user_sam", "plan_big", "active")
	f.buy("mem_old1", "user_old", "plan_starter", "canceled")
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_sam")) != 0 {
		t.Fatal("memberships were read before whopPollEvery passed")
	}
	e.clock.add(whopPollEvery)
	e.srv.reconcileWhop(context.Background())
	msgs := f.sent("user_sam")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "up to 2 servers with 8 GB of memory") {
		t.Fatalf("sam's messages: %q", msgs)
	}
	if len(f.sent("user_old")) != 0 {
		t.Fatal("a cancelled membership got an invite")
	}
}

func TestAnInviteThatCantBeSentIsTurnedOffAndTriedAgainLater(t *testing.T) {
	f, e, own := connectedWhop(t)
	f.chatDown = true
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_1", whop.EventMembershipActivated, m)
	e.srv.reconcileWhop(context.Background())
	for _, inv := range e.teamInvites(t, own) {
		if inv.RevokedAt.IsZero() {
			t.Fatalf("an invite nobody received still works: %+v", inv)
		}
	}
	v := e.whopView(t, own)
	if len(v.Buyers) != 1 || v.Buyers[0].Status != "sending" || v.Buyers[0].Problem == "" {
		t.Fatalf("buyers: %+v", v.Buyers)
	}
	f.chatDown = false
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 0 {
		t.Fatal("tried again before the wait")
	}
	e.clock.add(2 * time.Minute)
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 1 {
		t.Fatal("not tried again after the wait")
	}
	if v := e.whopView(t, own); v.Buyers[0].Status != "invited" || v.Buyers[0].Problem != "" {
		t.Fatalf("after sending: %+v", v.Buyers[0])
	}
}

func TestABuyerWhoseInviteWasTurnedOffGetsNoOtherButAnExpiredOneIsReplaced(t *testing.T) {
	f, e, own := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_1", whop.EventMembershipActivated, m)
	e.srv.reconcileWhop(context.Background())
	// The plan changes before alex joins: a new invite with the new
	// allowance replaces the first.
	f.buy("mem_alex1", "user_alex", "plan_big", "active")
	e.deliver(t, "msg_2", whop.EventMembershipActivated, f.memberships["mem_alex1"])
	e.srv.reconcileWhop(context.Background())
	msgs := f.sent("user_alex")
	if len(msgs) != 2 || !strings.Contains(msgs[1], "up to 2 servers with 8 GB") {
		t.Fatalf("after the plan changed: %q", msgs)
	}
	working := 0
	for _, inv := range e.teamInvites(t, own) {
		if inv.RevokedAt.IsZero() {
			working++
		}
	}
	if working != 1 {
		t.Fatalf("%d invites work for alex", working)
	}
	// It expires unused, 7 days on: another one goes out.
	e.clock.add(invites.MemberLifetime + time.Minute)
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 3 {
		t.Fatalf("after the invite expired: %d messages", len(f.sent("user_alex")))
	}
	// The owner turns the new one off: none is sent again.
	own = signIn(t, e, own.id)
	for _, inv := range e.teamInvites(t, own) {
		if inv.RevokedAt.IsZero() {
			if r := e.do(t, "DELETE", "/api/team/invites/"+inv.ID, "", own.auth()); r.status != http.StatusNoContent {
				t.Fatalf("turning off the invite: %d %v", r.status, r.body)
			}
		}
	}
	e.clock.add(whopPollEvery)
	e.srv.reconcileWhop(context.Background())
	if len(f.sent("user_alex")) != 3 {
		t.Fatal("an invite the owner turned off was sent again")
	}
	if v := e.whopView(t, own); v.Buyers[0].Status != "turned_off" {
		t.Fatalf("buyers: %+v", v.Buyers)
	}
}

func TestTheWebhookAnswersQuicklyWithoutWhop(t *testing.T) {
	f, e, _ := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.srv.Close()
	start := time.Now()
	if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("delivery while Whop is down: %d", r.status)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the delivery waited on Whop")
	}
	var status string
	e.srv.db.QueryRow(`SELECT status FROM whop_memberships WHERE membership_id = 'mem_alex1'`).Scan(&status)
	if status != "active" {
		t.Fatalf("the membership kept as %q", status)
	}
	_ = io.Discard
}
