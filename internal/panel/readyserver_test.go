package panel

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

// recordingNotifier keeps what the core tells customers, and fails while
// fail is set.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []CustomerMessage
	fail error
}

func (n *recordingNotifier) Notify(_ context.Context, _ Customer, m CustomerMessage) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	n.sent = append(n.sent, m)
	return nil
}

func (n *recordingNotifier) kinds() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, m := range n.sent {
		out = append(out, m.Kind)
	}
	return out
}

// waitingFor reads whether the signed-in account's dashboard says its server
// is waiting for room.
func waitingFor(t *testing.T, e *env, m member) bool {
	t.Helper()
	var me struct {
		Access struct {
			WaitingForRoom bool `json:"waitingForRoom"`
		} `json:"access"`
	}
	e.get(t, "/api/auth/me", m.cookie, &me)
	return me.Access.WaitingForRoom
}

// A customer is told once that their server is ready to start, as soon as
// placement gives them a home machine. Starting them again or a new plan
// tells them nothing more, and a message that couldn't be sent is sent by
// the next start.
func TestACustomerIsToldOnceTheirServerIsReady(t *testing.T) {
	e, _, core := customerEnv(t)
	n := &recordingNotifier{fail: errors.New("the provider is away")}
	e.srv.notifier = n
	ctx := context.Background()
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}
	if _, err := core.StartCustomer(ctx, alex, starter); err == nil {
		t.Fatal("a start whose message couldn't be sent went through")
	}
	n.mu.Lock()
	n.fail = nil
	n.mu.Unlock()
	for range 2 {
		if _, err := core.StartCustomer(ctx, alex, starter); err != nil {
			t.Fatal(err)
		}
	}
	if err := core.ChangeCustomerPlan(ctx, alex, CustomerPlan{ID: "plan_plus", Servers: 2, MemoryMB: 8192}); err != nil {
		t.Fatal(err)
	}
	if k := n.kinds(); !slices.Equal(k, []string{messageReady}) || !strings.Contains(n.sent[0].Text, "ready to start") {
		t.Fatalf("what alex was told: %v %+v", k, n.sent)
	}
}

// With no room, a customer is told once that their server is being set up,
// their dashboard says so, and creating a server waits. When room appears,
// startWaitingCustomer places them and tells them it's ready, once.
func TestACustomerWaitingForRoomIsToldAndStartedWhenRoomAppears(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	e.reply("GET", "/v1/machine", liveMachine(0, true))
	e.reply("GET", "/v1/servers", `[]`)
	n := &recordingNotifier{}
	e.srv.notifier = n
	core := customerCore{s: e.srv}
	ctx := context.Background()
	for range 2 {
		if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
			t.Fatal(err)
		}
	}
	if k := n.kinds(); !slices.Equal(k, []string{messageSettingUp}) {
		t.Fatalf("what alex was told with no room: %v", k)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, "user_alex")
	alex := signIn(t, e.env, info.UserID)
	if !waitingFor(t, e.env, alex) {
		t.Fatal("alex's dashboard doesn't say their server is being set up")
	}
	create := `{"name":"alex","acceptEula":true,"memoryMB":4096}`
	if r := e.do(t, "POST", "/api/machines/"+machineID(t, e.env)+"/servers", create, alex.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "being set up") {
		t.Fatalf("alex creates a server with no room: %d %v", r.status, r.body)
	}

	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	for range 2 {
		if err := e.srv.startWaitingCustomer(ctx, info.UserID); err != nil {
			t.Fatal(err)
		}
	}
	if k := n.kinds(); !slices.Equal(k, []string{messageSettingUp, messageReady}) {
		t.Fatalf("what alex was told once room appeared: %v", k)
	}
	if waitingFor(t, e.env, alex) {
		t.Fatal("alex's dashboard still says their server is waiting")
	}
}

// A customer paused while waiting gets no server and no ready message when
// room appears, and a suspended one isn't told it's ready when their plan
// starts again.
func TestOnlyAnActiveCustomerIsToldTheirServerIsReady(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	e.reply("GET", "/v1/machine", liveMachine(0, true))
	e.reply("GET", "/v1/servers", `[]`)
	n := &recordingNotifier{}
	e.srv.notifier = n
	core := customerCore{s: e.srv}
	ctx := context.Background()
	alex := Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}
	if _, err := core.StartCustomer(ctx, alex, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, "user_alex")
	if _, err := e.srv.db.Exec(`UPDATE customers SET state = 'paused' WHERE user_id = ?`, info.UserID); err != nil {
		t.Fatal(err)
	}
	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	if err := e.srv.startWaitingCustomer(ctx, info.UserID); err != nil {
		t.Fatal(err)
	}
	var home string
	e.srv.db.QueryRow(`SELECT machine_id FROM customer_homes WHERE user_id = ?`, info.UserID).Scan(&home)
	if k := n.kinds(); !slices.Equal(k, []string{messageSettingUp}) || home != "" {
		t.Fatalf("a paused customer once room appeared: told %v, home %q", k, home)
	}
	if _, err := e.srv.db.Exec(`UPDATE customers SET state = 'suspended' WHERE user_id = ?`, info.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := core.StartCustomer(ctx, alex, starter); err != nil {
		t.Fatal(err)
	}
	if k := n.kinds(); !slices.Equal(k, []string{messageSettingUp}) {
		t.Fatalf("a suspended customer whose plan started again was told %v", k)
	}
}
