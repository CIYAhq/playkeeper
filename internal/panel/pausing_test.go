package panel

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/mcptools"
)

// pausable is alex, a customer on a dashboard with room, with a server they
// created and an API token for it.
type pausable struct {
	e        joinEnv
	core     customerCore
	n        *recordingNotifier
	cust     Customer
	own      member
	alex     member
	serverID string
}

func newPausable(t *testing.T) pausable {
	t.Helper()
	e, own, core := customerEnv(t)
	newCreatorAgent(e.env, "cafebabe23")
	n := &recordingNotifier{}
	e.srv.notifier = n
	ctx := context.Background()
	cust := Customer{Provider: whopProvider, Store: testStore, Subject: "user_alex", Handle: "alex"}
	if _, err := core.StartCustomer(ctx, cust, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	alex := signIn(t, e.env, info.UserID)
	if r := e.do(t, "POST", "/api/machines/"+machineID(t, e.env)+"/servers", `{"name":"alex","acceptEula":true,"memoryMB":4096}`, alex.auth()); r.status != http.StatusOK || r.body["serverId"] != "cafebabe23" {
		t.Fatalf("alex creates a server: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/tokens", `{"name":"bot","role":"moderator","servers":["cafebabe23"]}`, alex.auth()); r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("alex makes a token: %d %v", r.status, r.body)
	}
	return pausable{e: e, core: core, n: n, cust: cust, own: own, alex: alex, serverID: "cafebabe23"}
}

func (p pausable) stops() int {
	p.e.agent.mu.Lock()
	defer p.e.agent.mu.Unlock()
	n := 0
	for _, h := range p.e.agent.hits {
		if h == "POST /v1/servers/"+p.serverID+"/stop" {
			n++
		}
	}
	return n
}

func (p pausable) tokens(t *testing.T) int {
	t.Helper()
	list, err := p.e.srv.tokensWhere(`t.user_id = ? AND t.revoked_at = 0`, p.alex.id)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// A customer whose plan ends is paused: their server stops, their API token
// is revoked, and they're told until when they can still download. They may
// sign in to see their servers and download backups, but not start or
// create one, or make a new token, and their dashboard never says a server
// is on its way. Pausing again changes nothing, and renewing brings them
// back.
func TestAPausedCustomerSeesTheirServersButRunsNothing(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	if p.tokens(t) != 1 {
		t.Fatal("alex's token wasn't made")
	}
	for range 2 {
		if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
			t.Fatal(err)
		}
	}
	info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if info.State != CustomerPaused || !info.SignIn {
		t.Fatalf("alex once paused: %+v", info)
	}
	if n := p.stops(); n != 1 {
		t.Fatalf("alex's server was asked to stop %d times", n)
	}
	if p.tokens(t) != 0 {
		t.Fatal("alex's token still works")
	}
	if k := p.n.kinds(); !slices.Equal(k, []string{messageReady, messagePaused}) || !strings.Contains(p.n.sent[1].Text, "until") {
		t.Fatalf("what alex was told: %v %+v", k, p.n.sent)
	}
	if _, err := e.srv.db.Exec(`UPDATE customer_homes SET machine_id = '' WHERE user_id = ?`, p.alex.id); err != nil {
		t.Fatal(err)
	}
	var me struct {
		Access struct {
			PausedUntil    *time.Time `json:"pausedUntil"`
			WaitingForRoom bool       `json:"waitingForRoom"`
		} `json:"access"`
	}
	e.get(t, "/api/auth/me", p.alex.cookie, &me)
	if me.Access.PausedUntil == nil || me.Access.PausedUntil.Sub(e.clock.now()) < 13*24*time.Hour || me.Access.WaitingForRoom {
		t.Fatalf("alex's dashboard says paused until %v, waiting for room %v", me.Access.PausedUntil, me.Access.WaitingForRoom)
	}
	if st := e.do(t, "GET", "/api/servers/"+p.serverID, "", p.alex.auth()).status; st != http.StatusOK {
		t.Fatalf("alex looks at their server: %d", st)
	}
	for path, body := range map[string]string{
		"/api/servers/" + p.serverID + "/start":             `{}`,
		"/api/machines/" + machineID(t, e.env) + "/servers": `{"name":"two","acceptEula":true,"memoryMB":2048}`,
		"/api/tokens": `{"name":"bot two","role":"viewer","servers":["` + p.serverID + `"]}`,
	} {
		if r := e.do(t, "POST", path, body, p.alex.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "plan has ended") {
			t.Fatalf("alex, paused, posts %s: %d %v", path, r.status, r.body)
		}
	}

	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ = p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if k := p.n.kinds(); info.State != CustomerActive || !slices.Equal(k, []string{messageReady, messagePaused, messageBack}) {
		t.Fatalf("alex once renewed: %+v, told %v", info, k)
	}
	if r := e.do(t, "POST", "/api/servers/"+p.serverID+"/start", `{}`, p.alex.auth()); r.status == http.StatusForbidden {
		t.Fatalf("alex, renewed, starts their server: %d %v", r.status, r.body)
	}
}

// Whoever alex shares their server with can't run or change it once alex
// is paused, from the dashboard or with a token, restores and world imports
// included, though they can still look at it, and its machine is told to
// start it for nobody. The owner may still look after it. Renewing lifts
// the hold, and a suspension brings back a stricter one, under which they
// may only look.
func TestAPausedCustomersServerIsHeldForWhoeverTheyShareItWith(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	disk := newDiskLimitsAgent(e.env, 0)
	kim := addAdmin(t, e.env, "kim", p.serverID)
	_, secret := e.newToken(t, kim.cookie, kim.csrf, `{"name":"kim's bot","role":"moderator","servers":["`+p.serverID+`"]}`)
	held := func(want string) {
		t.Helper()
		e.srv.syncDiskLimits(ctx)
		if l := disk.last().Limits; len(l) != 1 || l[0].Hold != want {
			t.Fatalf("the limits sent: %+v, want the hold %q", l, want)
		}
	}
	start := func(who member) resp {
		return e.do(t, "POST", "/api/servers/"+p.serverID+"/start", `{}`, who.auth())
	}
	held("")
	kicked(e.env)
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	if !kicked(e.env) {
		t.Fatal("pausing alex didn't send the limits at once")
	}
	held("the plan it's on has ended.")
	if r := start(kim); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "plan has ended") {
		t.Fatalf("kim starts alex's paused server: %d %v", r.status, r.body)
	}
	if a := e.callTool(t, secret, "start_server", map[string]any{"server": p.serverID}); a.kind != mcptools.RefusedAction || !strings.Contains(a.text, "plan has ended") {
		t.Fatalf("kim's token starts alex's paused server: %+v", a)
	}
	mid := machineID(t, e.env)
	e.reply("GET", "/v1/restore/0123456789abcdef", `{"id":"0123456789abcdef","serverId":"`+p.serverID+`"}`)
	e.reply("GET", "/v1/world-imports/0123456789abcdef", `{"id":"0123456789abcdef","serverId":"`+p.serverID+`","files":[]}`)
	for _, path := range []string{"/api/machines/" + mid + "/restore/0123456789abcdef/apply", "/api/machines/" + mid + "/world-imports/0123456789abcdef/apply"} {
		if r := e.do(t, "POST", path, `{}`, kim.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "plan has ended") {
			t.Fatalf("kim posts %s onto alex's paused server: %d %v", path, r.status, r.body)
		}
	}
	if st := e.do(t, "GET", "/api/servers/"+p.serverID, "", kim.auth()).status; st != http.StatusOK {
		t.Fatalf("kim looks at alex's paused server: %d", st)
	}
	if r := start(p.own); r.status == http.StatusForbidden {
		t.Fatalf("the owner looks after alex's paused server: %d %v", r.status, r.body)
	}

	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	held("")
	if r := start(kim); r.status == http.StatusForbidden {
		t.Fatalf("kim starts alex's server once alex renewed: %d %v", r.status, r.body)
	}

	if _, err := e.srv.db.Exec(`UPDATE customers SET state = 'suspended' WHERE user_id = ?`, p.alex.id); err != nil {
		t.Fatal(err)
	}
	held("its account is suspended.")
	if r := e.do(t, "POST", "/api/servers/"+p.serverID+"/backups", `{}`, kim.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "suspended") {
		t.Fatalf("kim backs up a suspended customer's server: %d %v", r.status, r.body)
	}
	if st := e.do(t, "GET", "/api/servers/"+p.serverID, "", kim.auth()).status; st != http.StatusOK {
		t.Fatalf("kim looks at a suspended customer's server: %d", st)
	}
}

// A customer the owner suspended can do nothing, and nothing their billing
// provider does lifts it: neither their plan ending nor starting again.
func TestNothingABillingProviderDoesLiftsASuspension(t *testing.T) {
	p := newPausable(t)
	e, ctx := p.e, context.Background()
	if _, err := e.srv.db.Exec(`UPDATE customers SET state = 'suspended' WHERE user_id = ?`, p.alex.id); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "GET", "/api/servers/"+p.serverID, "", p.alex.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "suspended") {
		t.Fatalf("a suspended customer looks at their server: %d %v", r.status, r.body)
	}
	if err := p.core.PauseCustomer(ctx, p.cust, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.core.StartCustomer(ctx, p.cust, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := p.core.CustomerAccount(ctx, whopProvider, testStore, "user_alex")
	if k := p.n.kinds(); info.State != CustomerSuspended || info.SignIn || !slices.Equal(k, []string{messageReady}) {
		t.Fatalf("a suspended customer after their plan ended and started again: %+v, told %v", info, k)
	}
}
