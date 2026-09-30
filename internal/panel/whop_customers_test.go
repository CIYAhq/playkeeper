package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

// buy gives a customer a membership of a plan on the fake Whop, or changes
// its status. A new one takes one off the plan's stock when it has one.
func (f *fakeWhop) buy(id, user, plan, status string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]any{"id": id, "status": status, "plan_id": plan, "product_id": "prod_mc", "user_id": user, "cancel_at_period_end": false}
	_, had := f.memberships[id]
	f.memberships[id] = m
	if p := f.plan(plan); p != nil && !had && p["unlimited_stock"] == false {
		if n, _ := p["stock"].(int); n > 0 {
			p["stock"] = n - 1
		}
	}
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
	body, _ := json.Marshal(map[string]any{"type": event, "api_version": "v1", "api_version_date": whop.APIVersion,
		"timestamp": e.clock.now().Format(time.RFC3339Nano), "account_id": "biz_pip", "data": m})
	hdr := map[string]string{}
	for k, v := range whop.SignWebhook(whopTestSecret, id, e.clock.now(), body) {
		hdr[k] = v[0]
	}
	hdr["Origin"] = ""
	return e.do(t, "POST", whopWebhookPath, string(body), hdr)
}

// fakeCore is the hosting core as a billing provider sees it: it records
// each call, can refuse them, and knows the accounts it made.
type fakeCore struct {
	mu       sync.Mutex
	calls    []string
	refuse   error
	accounts map[string]CustomerAccountInfo
}

// useFakeCore has e's dashboard call a fake hosting core.
func useFakeCore(e *env) *fakeCore {
	fc := &fakeCore{accounts: map[string]CustomerAccountInfo{}}
	e.srv.hosting = fc
	return fc
}

func (fc *fakeCore) call(c Customer, what string) error {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.refuse != nil {
		return fc.refuse
	}
	fc.calls = append(fc.calls, fmt.Sprintf("%s %s/%s %s", what, c.Provider, c.Subject, c.Handle))
	return nil
}

func planText(p CustomerPlan) string {
	return fmt.Sprintf("%s (%s) %d/%d/%d", p.ID, p.Name, p.Servers, p.MemoryMB, p.DiskGB)
}

func (fc *fakeCore) StartCustomer(_ context.Context, c Customer, p CustomerPlan) (StartedCustomer, error) {
	if err := fc.call(c, "start "+planText(p)+" for"); err != nil {
		return StartedCustomer{}, err
	}
	fc.mu.Lock()
	fc.accounts[c.Subject] = CustomerAccountInfo{UserID: 42, Username: c.Handle, State: CustomerActive, SignIn: true}
	fc.mu.Unlock()
	return StartedCustomer{Account: c.Handle, Dashboard: whopDashboard, Server: c.Handle + ".beta.playkeeper.me"}, nil
}

func (fc *fakeCore) ChangeCustomerPlan(_ context.Context, c Customer, p CustomerPlan) error {
	return fc.call(c, "change to "+planText(p)+" for")
}

func (fc *fakeCore) PauseCustomer(_ context.Context, c Customer, reason string) error {
	return fc.call(c, "pause ("+reason+")")
}

func (fc *fakeCore) CustomerAccount(_ context.Context, provider, subject string) (CustomerAccountInfo, bool, error) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	info, ok := fc.accounts[subject]
	return info, ok && provider == whopProvider, nil
}

func (fc *fakeCore) got() []string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([]string(nil), fc.calls...)
}

func (e *env) reconcile() { e.srv.reconcileWhop(context.Background()) }

func TestConnectingAddsTheWebhookAndDisconnectingRemovesIt(t *testing.T) {
	f, e, own := connectedWhop(t)
	var hook map[string]any
	for _, h := range f.webhooks {
		hook = h
	}
	events, _ := hook["events"].([]any)
	_, versioned := hook["api_version"]
	if len(f.webhooks) != 1 || hook["url"] != whopDashboard+whopWebhookPath || versioned || hook["api_version_date"] != whop.APIVersion ||
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

// The whole path on the Whop side: Whop tells the dashboard of a paid
// membership, the dashboard confirms it with Whop's API and starts the
// customer on the hosting core, which tells them their server is ready,
// and that goes out in the store's support chat with them.
func TestAConfirmedMembershipStartsTheCustomerAndTheCoresMessageReachesThem(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("delivery: %d %v", r.status, r.body)
	}
	e.reconcile()
	if got := core.got(); len(got) != 1 || got[0] != "start plan_starter (Starter) 1/4096/0 for whop/user_alex alexplays" {
		t.Fatalf("the core's calls: %q", got)
	}
	var invitesMade int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM invites`).Scan(&invitesMade)
	if invitesMade != 0 || len(f.sent("user_alex")) != 0 {
		t.Fatalf("the Whop side made %d invites and sent %q itself", invitesMade, f.sent("user_alex"))
	}
	e.reconcile()
	if len(core.got()) != 1 {
		t.Fatalf("a customer in line with their plans was called again: %q", core.got())
	}
	// The core tells them through the notifier; it goes out on the next look.
	ready := "Your Pip Hosting server is ready at alexplays.beta.playkeeper.me. Sign in with Whop at " + whopDashboard
	if err := e.srv.notifier.Notify(context.Background(), Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alexplays"}, CustomerMessage{Kind: "ready", Text: ready}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	e.reconcile()
	if msgs := f.sent("user_alex"); len(msgs) != 1 || msgs[0] != ready {
		t.Fatalf("messages: %q", msgs)
	}
	// Whop takes messages from people alone: it went out as the store's
	// owner, with a token that may send support chat messages and nothing
	// else.
	f.mu.Lock()
	senders := f.senders["chan_user_alex"]
	var scopes [][]string
	for _, tok := range f.tokens {
		scopes = append(scopes, tok.actions)
	}
	f.mu.Unlock()
	if !slices.Equal(senders, []string{whopTestOwner}) || len(scopes) == 0 {
		t.Fatalf("sent by %q, with %d tokens", senders, len(scopes))
	}
	for _, s := range scopes {
		if !slices.Equal(s, []string{whop.MessageAction}) {
			t.Fatalf("a token that may do %q", s)
		}
	}
	v := e.whopView(t, own)
	if len(v.Customers) != 1 {
		t.Fatalf("customers: %+v", v.Customers)
	}
	if c := v.Customers[0]; c.WhopUserID != "user_alex" || c.Handle != "alexplays" || c.Status != "active" || c.Plan != "Starter" || c.Account != "alexplays" ||
		c.Allowance.Servers != 1 || c.Allowance.MemoryMB != 4096 || c.Problem != "" {
		t.Fatalf("alex on the page: %+v", c)
	}
}

func TestTheNotifierTakesOnlyWhopCustomersAndSendsTheirMessagesInOrder(t *testing.T) {
	f, e, _ := connectedWhop(t)
	useFakeCore(e)
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alexplays"}
	for _, bad := range []struct {
		c Customer
		m CustomerMessage
	}{
		{Customer{Provider: "stripe", Subject: "cus_1"}, CustomerMessage{Kind: "ready", Text: "hi"}},
		{Customer{Provider: whopProvider, Subject: "user alex; drop"}, CustomerMessage{Kind: "ready", Text: "hi"}},
		{alex, CustomerMessage{Kind: "ready", Text: "  "}},
	} {
		if err := e.srv.notifier.Notify(context.Background(), bad.c, bad.m); err == nil {
			t.Errorf("notified %+v with %+v", bad.c, bad.m)
		}
	}
	f.chatDown = true
	for _, text := range []string{"first", "second"} {
		if err := e.srv.notifier.Notify(context.Background(), alex, CustomerMessage{Kind: "ready", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	e.reconcile()
	f.chatDown = false
	e.reconcile()
	if len(f.sent("user_alex")) != 0 {
		t.Fatal("a message went out before its wait, or out of order")
	}
	e.clock.add(2 * time.Minute)
	e.reconcile()
	if msgs := f.sent("user_alex"); len(msgs) != 2 || msgs[0] != "first" || msgs[1] != "second" {
		t.Fatalf("messages: %q", msgs)
	}
}

// A message that can't go out waits for its next try, and the customer's
// row on the page says why until it goes: here Whop won't give the token to
// send it with, and then the key lacks the permission a token needs.
func TestAMessageThatCantGoOutShowsOnThePageUntilItDoes(t *testing.T) {
	f, e, own := connectedWhop(t)
	useFakeCore(e)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_1", whop.EventMembershipActivated, m)
	e.reconcile()
	problem := func() string {
		t.Helper()
		v := e.whopView(t, own)
		if len(v.Customers) != 1 {
			t.Fatalf("customers: %+v", v.Customers)
		}
		return v.Customers[0].MessageProblem
	}
	if p := problem(); p != "" {
		t.Fatalf("a problem before any message: %q", p)
	}
	f.mu.Lock()
	f.tokenDown = true
	f.mu.Unlock()
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alexplays"}
	if err := e.srv.notifier.Notify(context.Background(), alex, CustomerMessage{Kind: "ready", Text: "ready"}); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	if p := problem(); len(f.sent("user_alex")) != 0 || p != "Whop said: Something went wrong" {
		t.Fatalf("while Whop gives no token: sent %q, problem %q", f.sent("user_alex"), p)
	}
	f.mu.Lock()
	f.tokenDown = false
	f.missing[whop.MessageAction] = true
	f.mu.Unlock()
	e.clock.add(2 * time.Minute)
	e.reconcile()
	if p := problem(); len(f.sent("user_alex")) != 0 || !strings.Contains(p, "missing all required permissions: support_chat:message:create") {
		t.Fatalf("with a key that lacks the permission: sent %q, problem %q", f.sent("user_alex"), p)
	}
	f.mu.Lock()
	delete(f.missing, whop.MessageAction)
	f.mu.Unlock()
	e.clock.add(4 * time.Minute)
	e.reconcile()
	if p := problem(); !slices.Equal(f.sent("user_alex"), []string{"ready"}) || p != "" {
		t.Fatalf("once it can go: sent %q, problem %q", f.sent("user_alex"), p)
	}
}

// A delivery only says where to look: a customer starts once Whop's API
// confirms their membership, and one Whop doesn't know starts nobody.
func TestOnlyAMembershipWhopConfirmsStartsAnyone(t *testing.T) {
	f, e, _ := connectedWhop(t)
	core := useFakeCore(e)
	e.reconcile()
	unknown := map[string]any{"id": "mem_ghost", "status": "active", "plan_id": "plan_starter", "product_id": "prod_mc", "user_id": "user_ghost"}
	e.deliver(t, "msg_1", whop.EventMembershipActivated, unknown)
	e.reconcile()
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_memberships WHERE membership_id = 'mem_ghost'`).Scan(&n)
	if len(core.got()) != 0 || n != 0 {
		t.Fatalf("a membership Whop doesn't know: calls %q, kept %d", core.got(), n)
	}
	// While Whop can't confirm it, nothing happens; once it does, it does.
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_2", whop.EventMembershipActivated, m)
	f.mu.Lock()
	f.memberships = map[string]map[string]any{}
	f.mu.Unlock()
	e.reconcile()
	if len(core.got()) != 0 {
		t.Fatalf("started before Whop confirmed the membership: %q", core.got())
	}
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.deliver(t, "msg_3", whop.EventMembershipActivated, m)
	e.reconcile()
	if len(core.got()) != 1 {
		t.Fatalf("not started once Whop confirmed the membership: %q", core.got())
	}
}

func TestPlansChangingChangeTheCustomersPlanAndTheirEndPausesThem(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	// A second plan adds to the first.
	e.deliver(t, "msg_1", whop.EventMembershipActivated, f.buy("mem_alex2", "user_alex", "plan_big", "active"))
	e.reconcile()
	// The first ends: what's left is Big alone.
	e.deliver(t, "msg_2", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "canceled"))
	e.reconcile()
	// The last ends: paused, once.
	e.deliver(t, "msg_3", whop.EventMembershipDeactivated, f.buy("mem_alex2", "user_alex", "plan_big", "expired"))
	e.reconcile()
	e.reconcile()
	if v := e.whopView(t, own); len(v.Customers) != 1 || v.Customers[0].Status != "paused" {
		t.Fatalf("customers: %+v", v.Customers)
	}
	// A plan again starts them again.
	e.deliver(t, "msg_4", whop.EventMembershipActivated, f.buy("mem_alex3", "user_alex", "plan_starter", "trialing"))
	e.reconcile()
	want := []string{
		"start plan_starter (Starter) 1/4096/0 for whop/user_alex alexplays",
		"change to plan_big+plan_starter (Big + Starter) 3/12288/0 for whop/user_alex alexplays",
		"change to plan_big (Big) 2/8192/0 for whop/user_alex alexplays",
		"pause (their Whop membership is expired) whop/user_alex alexplays",
		"start plan_starter (Starter) 1/4096/0 for whop/user_alex alexplays",
	}
	if got := core.got(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the core's calls:\n%s", strings.Join(got, "\n"))
	}
}

func TestAPlanAddsUpWithinTheBoundsAndItsDiskCountsWhenEachPlanSaysOne(t *testing.T) {
	p := customerPlan([]planPart{{id: "plan_b", name: "B", servers: 8, memoryMB: 40960, diskGB: 60}, {id: "plan_a", name: "", servers: 4, memoryMB: 40960, diskGB: 30}})
	if p.ID != "plan_a+plan_b" || p.Name != "plan_a + B" || p.Servers != 10 || p.MemoryMB != 65536 || p.DiskGB != 90 {
		t.Fatalf("two plans: %+v", p)
	}
	if p := customerPlan([]planPart{{id: "plan_a", servers: 1, memoryMB: 4096, diskGB: 30}, {id: "plan_b", servers: 1, memoryMB: 4096}}); p.DiskGB != 0 {
		t.Fatalf("a plan without disk leaves it to the core: %+v", p)
	}
	if p := customerPlan(nil); p != (CustomerPlan{}) {
		t.Fatalf("no plans: %+v", p)
	}
}

func TestAPlansDiskOnWhopReachesTheCore(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	f.mu.Lock()
	f.plans[0]["metadata"] = map[string]any{whop.MetaServers: "1", whop.MetaMemoryGB: "4", whop.MetaDiskGB: "30"}
	f.mu.Unlock()
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	if got := core.got(); len(got) != 1 || !strings.HasPrefix(got[0], "start plan_starter (Starter) 1/4096/30 ") {
		t.Fatalf("the core's calls: %q", got)
	}
}

func TestAPlanStillHeldKeepsItsCustomersWhenItsArchivedOrGone(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	f.mu.Lock()
	f.plans[0]["visibility"] = "archived"
	f.mu.Unlock()
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	e.reconcile()
	f.mu.Lock()
	f.plans = f.plans[1:]
	f.mu.Unlock()
	e.do(t, "POST", "/api/whop/sync", "", own.auth())
	e.clock.add(whopPollEvery)
	e.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("the core's calls after Starter was archived and dropped: %q", got)
	}
	for _, p := range e.whopView(t, own).Plans {
		if p.ID == "plan_starter" {
			t.Fatalf("an archived plan is listed: %+v", p)
		}
	}
}

func TestACancellationIsRemindedOnceAndAgainAfterItsUndone(t *testing.T) {
	f, e, _ := connectedWhop(t)
	useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	cancel := func(on bool, id string) {
		f.mu.Lock()
		m := f.memberships["mem_alex1"]
		m["cancel_at_period_end"] = on
		m["current_period_end"] = "2026-10-12T09:00:00Z"
		f.mu.Unlock()
		e.deliver(t, id, whop.EventMembershipCancelling, m)
		e.reconcile()
	}
	cancel(true, "msg_1")
	e.reconcile()
	msgs := f.sent("user_alex")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "You cancelled your Pip Hosting plan. It keeps running until 12 October, then your servers stop.") {
		t.Fatalf("messages: %q", msgs)
	}
	cancel(false, "msg_2")
	cancel(true, "msg_3")
	if len(f.sent("user_alex")) != 2 {
		t.Fatalf("a second cancellation: %q", f.sent("user_alex"))
	}
}

// Whop's current API calls a membership cancelled at its period's end
// canceling, and it runs until then.
func TestACancelingMembershipKeepsItsCustomerUntilItEnds(t *testing.T) {
	f, e, _ := connectedWhop(t)
	core := useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	f.mu.Lock()
	m := f.memberships["mem_alex1"]
	m["status"], m["cancel_at_period_end"], m["current_period_end"] = "canceling", true, "2026-10-12T09:00:00Z"
	f.mu.Unlock()
	e.deliver(t, "msg_1", whop.EventMembershipCancelling, m)
	e.reconcile()
	e.reconcile()
	if got := core.got(); len(got) != 1 {
		t.Fatalf("a membership running to its period's end paused its customer: %q", got)
	}
	if msgs := f.sent("user_alex"); len(msgs) != 1 || !strings.Contains(msgs[0], "It keeps running until 12 October") {
		t.Fatalf("messages: %q", msgs)
	}
	e.deliver(t, "msg_2", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "canceled"))
	e.reconcile()
	if got := core.got(); len(got) != 2 || !strings.HasPrefix(got[1], "pause") {
		t.Fatalf("once it ended: %q", got)
	}
}

func TestCancellingOnePlanWhileAnotherGoesOnSaysNothingUntilTheLast(t *testing.T) {
	f, e, _ := connectedWhop(t)
	useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	f.buy("mem_alex2", "user_alex", "plan_big", "active")
	e.reconcile()
	cancel := func(id, end, delivery string) {
		f.mu.Lock()
		m := f.memberships[id]
		m["cancel_at_period_end"], m["current_period_end"] = true, end
		f.mu.Unlock()
		e.deliver(t, delivery, whop.EventMembershipCancelling, m)
		e.reconcile()
	}
	cancel("mem_alex1", "2026-10-12T09:00:00Z", "msg_1")
	if msgs := f.sent("user_alex"); len(msgs) != 0 {
		t.Fatalf("cancelling one of two plans: %q", msgs)
	}
	cancel("mem_alex2", "2026-10-20T09:00:00Z", "msg_2")
	e.reconcile()
	if msgs := f.sent("user_alex"); len(msgs) != 1 || !strings.Contains(msgs[0], "It keeps running until 20 October, then your servers stop.") {
		t.Fatalf("cancelling the last: %q", msgs)
	}
}

func TestACustomerWhoBuysAgainIsSettingUpUntilTheCoreTakesThemBack(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	e.deliver(t, "msg_1", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "canceled"))
	e.reconcile()
	if v := e.whopView(t, own); v.Customers[0].Status != "paused" {
		t.Fatalf("after the plan ended: %+v", v.Customers[0])
	}
	core.refuse = errors.New("The machine is full.")
	e.deliver(t, "msg_2", whop.EventMembershipActivated, f.buy("mem_alex2", "user_alex", "plan_starter", "active"))
	e.reconcile()
	if c := e.whopView(t, own).Customers[0]; c.Status != "starting" || c.Problem != "The machine is full." {
		t.Fatalf("buying again while the core refuses: %+v", c)
	}
}

func TestADeliveryThatCantBeKeptIsTakenAgain(t *testing.T) {
	f, e, _ := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	if _, err := e.srv.db.Exec(`ALTER TABLE whop_memberships RENAME TO whop_memberships_away`); err != nil {
		t.Fatal(err)
	}
	if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusServiceUnavailable {
		t.Fatalf("a delivery that couldn't be kept: %d", r.status)
	}
	if _, err := e.srv.db.Exec(`ALTER TABLE whop_memberships_away RENAME TO whop_memberships`); err != nil {
		t.Fatal(err)
	}
	if r := e.deliver(t, "msg_1", whop.EventMembershipActivated, m); r.status != http.StatusOK {
		t.Fatalf("Whop sending it again: %d", r.status)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_memberships WHERE membership_id = 'mem_alex1'`).Scan(&n)
	if n != 1 {
		t.Fatal("the delivery sent again wasn't kept")
	}
}

func TestACallTheCoreRefusesIsTriedAgainLaterAndAtOnceAfterARestart(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	core.refuse = errors.New("The machine is full.")
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	v := e.whopView(t, own)
	if len(v.Customers) != 1 || v.Customers[0].Status != "starting" || v.Customers[0].Problem != "The machine is full." {
		t.Fatalf("customers: %+v", v.Customers)
	}
	core.refuse = nil
	e.reconcile()
	if len(core.got()) != 0 {
		t.Fatal("tried again before the wait")
	}
	e.clock.add(2 * time.Minute)
	e.reconcile()
	if len(core.got()) != 1 {
		t.Fatal("not tried again after the wait")
	}
	if v := e.whopView(t, own); v.Customers[0].Status != "active" || v.Customers[0].Problem != "" {
		t.Fatalf("after starting: %+v", v.Customers[0])
	}
	// A long wait ends when the dashboard starts again.
	core.refuse = errors.New("The machine is full.")
	e.deliver(t, "msg_1", whop.EventMembershipDeactivated, f.buy("mem_alex1", "user_alex", "plan_starter", "canceled"))
	for range 5 {
		e.clock.add(time.Hour)
		e.reconcile()
	}
	core.refuse = nil
	e.reconcile()
	if len(core.got()) != 1 {
		t.Fatalf("tried before a six-hour wait: %q", core.got())
	}
	e.srv.retryWhopNow()
	e.reconcile()
	if got := core.got(); len(got) != 2 || !strings.HasPrefix(got[1], "pause") {
		t.Fatalf("after a restart: %q", got)
	}
}

func TestWithoutTheHostingCoreACustomerWaitsAndThePageSaysWhy(t *testing.T) {
	f, e, own := connectedWhop(t)
	e.srv.hosting = noHostingCore{}
	f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	e.reconcile()
	v := e.whopView(t, own)
	if len(v.Customers) != 1 || v.Customers[0].Status != "starting" || v.Customers[0].Problem != errNoHostingCore.Error() || v.Customers[0].Account != "" {
		t.Fatalf("customers: %+v", v.Customers)
	}
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

// Whop names the business account_id, or company_id on a webhook without a
// pin. Either way the webhook keeps only its own store's memberships.
func TestTheWebhookKeepsOnlyItsOwnStoresMemberships(t *testing.T) {
	f, e, _ := connectedWhop(t)
	m := f.buy("mem_alex1", "user_alex", "plan_starter", "active")
	deliver := func(id string, envelope map[string]any) {
		t.Helper()
		envelope["type"], envelope["api_version"], envelope["data"] = whop.EventMembershipActivated, "v1", m
		body, _ := json.Marshal(envelope)
		hdr := map[string]string{"Origin": ""}
		for k, v := range whop.SignWebhook(whopTestSecret, id, e.clock.now(), body) {
			hdr[k] = v[0]
		}
		if r := e.do(t, "POST", whopWebhookPath, string(body), hdr); r.status != http.StatusOK {
			t.Fatalf("delivery %s: %d", id, r.status)
		}
	}
	kept := func() (n int) {
		e.srv.db.QueryRow(`SELECT COUNT(*) FROM whop_memberships`).Scan(&n)
		return n
	}
	deliver("msg_1", map[string]any{"api_version_date": whop.APIVersion, "account_id": "biz_other"})
	deliver("msg_2", map[string]any{"company_id": "biz_other"})
	if n := kept(); n != 0 {
		t.Fatalf("%d memberships kept from another business", n)
	}
	deliver("msg_3", map[string]any{"company_id": "biz_pip"})
	if n := kept(); n != 1 {
		t.Fatalf("%d memberships kept from the store's own delivery without a pin", n)
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
	core := useFakeCore(e)
	// The first look after connecting reads every membership there is.
	f.buy("mem_early", "user_early", "plan_starter", "active")
	f.mu.Lock()
	f.users["user_early"], f.users["user_sam"] = "earlybird", "samcrafts"
	f.mu.Unlock()
	e.reconcile()
	if got := core.got(); len(got) != 1 || !strings.HasSuffix(got[0], "whop/user_early earlybird") {
		t.Fatalf("a membership from before connecting: %q", got)
	}
	f.buy("mem_sam1", "user_sam", "plan_big", "active")
	f.buy("mem_old1", "user_old", "plan_starter", "canceled")
	e.reconcile()
	if len(core.got()) != 1 {
		t.Fatal("memberships were read before whopPollEvery passed")
	}
	e.clock.add(whopPollEvery)
	e.reconcile()
	if got := core.got(); len(got) != 2 || got[1] != "start plan_big (Big) 2/8192/0 for whop/user_sam samcrafts" {
		t.Fatalf("the core's calls: %q", got)
	}
}

// Without a webhook pointing at the dashboard, reading every membership is
// how a purchase is heard of, so it happens every minute rather than every
// ten. The look that adds the webhook reads everything at once, since the
// webhook won't tell of what came before it, and then it's every ten.
func TestWithoutAWebhookEveryMembershipIsReadEveryMinute(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	f.mu.Lock()
	for id := range f.webhooks {
		delete(f.webhooks, id)
	}
	f.hooksDown = true
	f.users["user_sam"], f.users["user_bo"], f.users["user_cy"] = "samcrafts", "bobuilds", "cycrafts"
	f.mu.Unlock()
	e.srv.db.Exec(`UPDATE whop_account SET webhook_id = '', webhook_url = '', webhook_secret = ''`)
	e.reconcile()
	if v := e.whopView(t, own); v.Webhook {
		t.Fatal("the view has a webhook Whop refused")
	}
	f.buy("mem_sam1", "user_sam", "plan_starter", "active")
	e.clock.add(whopPollUnhooked - time.Second)
	e.reconcile()
	if len(core.got()) != 0 {
		t.Fatalf("read before a minute passed: %q", core.got())
	}
	e.clock.add(time.Second)
	e.reconcile()
	if got := core.got(); len(got) != 1 || got[0] != "start plan_starter (Starter) 1/4096/0 for whop/user_sam samcrafts" {
		t.Fatalf("a purchase without a webhook, a minute on: %q", got)
	}
	// Bo buys just before Whop takes the webhook, which never tells of it.
	f.buy("mem_bo1", "user_bo", "plan_starter", "active")
	f.mu.Lock()
	f.hooksDown = false
	f.mu.Unlock()
	e.clock.add(10 * time.Second)
	e.reconcile()
	if v := e.whopView(t, own); !v.Webhook {
		t.Fatal("the webhook wasn't added once Whop took it")
	}
	if got := core.got(); len(got) != 2 || !strings.HasSuffix(got[1], "whop/user_bo bobuilds") {
		t.Fatalf("a purchase from just before the webhook, read as it's added: %q", got)
	}
	f.buy("mem_cy1", "user_cy", "plan_starter", "active")
	e.clock.add(whopPollUnhooked)
	e.reconcile()
	if len(core.got()) != 2 {
		t.Fatalf("read every minute with a webhook: %q", core.got())
	}
	e.clock.add(whopPollEvery)
	e.reconcile()
	if len(core.got()) != 3 {
		t.Fatalf("the ten-minute read with a webhook: %q", core.got())
	}
}

// A purchase no webhook told of, as while Whop refused the webhook, is read
// at once after an update, a new key or reading the store again, rather than
// at the next ten-minute read, and reading again starts nobody twice.
func TestAPurchaseNoWebhookToldOfIsReadAtOnceAfterAnUpdateANewKeyOrARead(t *testing.T) {
	f, e, own := connectedWhop(t)
	core := useFakeCore(e)
	e.reconcile()
	f.mu.Lock()
	f.users["user_ann"], f.users["user_bo"], f.users["user_cy"] = "annplays", "bobuilds", "cycrafts"
	f.mu.Unlock()

	f.buy("mem_ann", "user_ann", "plan_starter", "completed")
	e.srv.retryWhopNow()
	e.reconcile()
	if got := core.got(); len(got) != 1 || !strings.HasSuffix(got[0], "whop/user_ann annplays") {
		t.Fatalf("after an update: %q", got)
	}
	f.buy("mem_bo", "user_bo", "plan_starter", "active")
	if r := e.do(t, "POST", "/api/whop/connect", `{"key":"`+whopTestKey+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("replacing the key: %d %v", r.status, r.body)
	}
	e.reconcile()
	if got := core.got(); len(got) != 2 || !strings.HasSuffix(got[1], "whop/user_bo bobuilds") {
		t.Fatalf("after a new key: %q", got)
	}
	f.buy("mem_cy", "user_cy", "plan_starter", "active")
	if r := e.do(t, "POST", "/api/whop/sync", "", own.auth()); r.status != http.StatusOK {
		t.Fatalf("reading the store again: %d %v", r.status, r.body)
	}
	e.reconcile()
	if got := core.got(); len(got) != 3 || !strings.HasSuffix(got[2], "whop/user_cy cycrafts") {
		t.Fatalf("after reading the store again: %q", got)
	}
	e.srv.retryWhopNow()
	e.reconcile()
	e.reconcile()
	if got := core.got(); len(got) != 3 {
		t.Fatalf("reading again called the core again: %q", got)
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
}
