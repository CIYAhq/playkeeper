package panel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// guardSwitch is a joined machine's Keep servers away from this machine:
// each request it got, and whether it's on. With refuse set, it stays off.
type guardSwitch struct {
	mu     sync.Mutex
	asked  []string
	host   bool
	refuse bool
}

func (g *guardSwitch) set(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.asked = append(g.asked, string(b))
	g.host = !g.refuse && strings.Contains(string(b), `"host":true`)
	host := g.host
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"on":true,"host":%t}`, host)
}

func (g *guardSwitch) requests() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.asked)
}

// joinForCustomers joins a machine with 30 GB free and servers not kept
// away from it to a dashboard whose own machine is full, and returns its
// id, its agent and its guard.
func joinForCustomers(t *testing.T, e *env, own member) (string, *remoteAgent, *guardSwitch) {
	t.Helper()
	e.reply("GET", "/v1/machine", liveMachine(0, true))
	e.reply("GET", "/v1/servers", `[]`)
	ra := newRemoteAgent()
	ra.reply("GET /v1/servers", `[]`)
	g := &guardSwitch{}
	ra.handle("POST /v1/network-guard", g.set)
	ra.handle("GET /v1/machine", func(w http.ResponseWriter, _ *http.Request) {
		g.mu.Lock()
		host := g.host
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, liveMachine(30000, host))
	})
	id, _ := e.joinMachine(t, own.cookie, own.csrf, ra)
	return id, ra, g
}

// lastMachineEvent is the kind and actor of the machine's newest event.
func lastMachineEvent(t *testing.T, e *env, id string) (kind, actor string) {
	t.Helper()
	if err := e.srv.db.QueryRow(`SELECT kind, actor FROM machine_events WHERE machine_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&kind, &actor); err != nil {
		t.Fatal(err)
	}
	return kind, actor
}

// A joined machine takes no customers until the owner confirms it, which
// keeps servers away from it first and places the customers waiting for
// room. Stopping it sends new customers elsewhere, and those it has stay.
// Only the owner may do either.
func TestAJoinedMachineTakesCustomersOnceTheOwnerConfirmsIt(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, _, guard := joinForCustomers(t, e, own)
	n := &recordingNotifier{}
	e.srv.notifier = n
	core := customerCore{s: e.srv}
	ctx := context.Background()
	start := func(subject string) int64 {
		t.Helper()
		if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Subject: subject, Handle: strings.TrimPrefix(subject, "user_")}, starter); err != nil {
			t.Fatal(err)
		}
		info, _, err := core.CustomerAccount(ctx, whopProvider, subject)
		if err != nil {
			t.Fatal(err)
		}
		return info.UserID
	}
	alex := start("user_alex")
	if home, ok, _ := e.srv.homeMachine(ctx, alex); ok {
		t.Fatalf("alex was placed on %q before the owner confirmed it", home)
	}
	if asked := guard.requests(); len(asked) != 0 {
		t.Fatalf("placement asked a machine nobody confirmed to keep servers away: %v", asked)
	}

	path := "/api/machines/" + rid + "/customers"
	lena := addAdmin(t, e, "lena", "*")
	if r := e.do(t, "PUT", path, `{"on":true}`, lena.auth()); r.status != http.StatusForbidden {
		t.Fatalf("an admin of every server confirms it: %d %v", r.status, r.body)
	}
	if asked := guard.requests(); len(asked) != 0 {
		t.Fatalf("an admin's refused confirmation reached the machine: %v", asked)
	}
	placing, stop := context.WithCancel(ctx)
	defer stop()
	go e.srv.runRoom(placing)
	r := e.do(t, "PUT", path, `{"on":true}`, own.auth())
	takes, _ := r.body["takesCustomers"].(map[string]any)
	if r.status != http.StatusOK || takes["by"] != "admin" || takes["since"] != "2026-09-24T12:00:00Z" || r.body["id"] != rid {
		t.Fatalf("the owner confirms it: %d %v", r.status, r.body)
	}
	if asked := guard.requests(); len(asked) != 1 || !strings.Contains(asked[0], `"host":true`) || !strings.Contains(asked[0], `"actor":"admin"`) {
		t.Fatalf("confirming asked the machine %v, want servers kept away first", asked)
	}
	if rows := e.auditRows(t, "machine.customers"); len(rows) != 1 || rows[0] != "admin home-server confirmed takes customers, with servers kept away from it" {
		t.Fatalf("audit: %v", rows)
	}
	if kind, actor := lastMachineEvent(t, e, rid); kind != "machine.customers_on" || actor != "admin" {
		t.Fatalf("the machine's last event: %s by %q", kind, actor)
	}
	eventually(t, "alex is placed on the machine and told", func() bool {
		home, ok, _ := e.srv.homeMachine(ctx, alex)
		return ok && home == rid && slices.Equal(n.kinds(), []string{messageSettingUp, messageReady})
	})
	stop()
	if r := e.do(t, "PUT", path, `{"on":true}`, own.auth()); r.status != http.StatusOK || len(e.auditRows(t, "machine.customers")) != 1 {
		t.Fatalf("confirming again: %d %v", r.status, r.body)
	}

	// Stopping waits for a customer being placed, so none lands on the
	// machine once it's stopped.
	e.srv.placeMu.Lock()
	stopped := make(chan resp, 1)
	go func() { stopped <- e.do(t, "PUT", path, `{"on":false}`, own.auth()) }()
	select {
	case r := <-stopped:
		e.srv.placeMu.Unlock()
		t.Fatalf("the owner stopped it while a customer was being placed: %d %v", r.status, r.body)
	case <-time.After(200 * time.Millisecond):
	}
	e.srv.placeMu.Unlock()
	r = <-stopped
	if r.status != http.StatusOK || r.body["takesCustomers"] != nil || r.body["customers"] != float64(1) {
		t.Fatalf("the owner stops it: %d %v", r.status, r.body)
	}
	if rows := e.auditRows(t, "machine.customers"); len(rows) != 2 || rows[1] != "admin home-server stopped takes no new customers; those it has stay" {
		t.Fatalf("audit: %v", rows)
	}
	if kind, actor := lastMachineEvent(t, e, rid); kind != "machine.customers_off" || actor != "admin" {
		t.Fatalf("the machine's last event: %s by %q", kind, actor)
	}
	sam := start("user_sam")
	if home, ok, _ := e.srv.homeMachine(ctx, sam); ok {
		t.Fatalf("sam was placed on %q after the owner stopped it", home)
	}
	if home, ok, _ := e.srv.homeMachine(ctx, alex); !ok || home != rid {
		t.Fatalf("alex's home once it's stopped: %q, %v", home, ok)
	}
}

// Confirming keeps servers away from the machine first: a machine that
// doesn't, or can't be reached, isn't confirmed. The dashboard's own machine
// always takes customers.
func TestConfirmingKeepsServersAwayFromTheMachineFirst(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, _, guard := joinForCustomers(t, e, own)
	path := "/api/machines/" + rid + "/customers"
	unconfirmed := func(what string) {
		t.Helper()
		if m := e.machineView(t, own.cookie, rid); m["takesCustomers"] != nil {
			t.Fatalf("%s: the machine takes customers: %v", what, m)
		}
		if rows := e.auditRows(t, "machine.customers"); len(rows) != 0 {
			t.Fatalf("%s: audit %v", what, rows)
		}
		select {
		case <-e.srv.roomKick:
			t.Fatalf("%s: waiting customers were placed", what)
		default:
		}
	}

	guard.mu.Lock()
	guard.refuse = true
	guard.mu.Unlock()
	if r := e.do(t, "PUT", path, `{"on":true}`, own.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "didn't keep servers away") {
		t.Fatalf("confirming a machine that leaves servers free to reach it: %d %v", r.status, r.body)
	}
	unconfirmed("a machine that leaves servers free to reach it")

	for _, body := range []string{`{}`, `{"on":"yes"}`, `{"on":true,"by":"x"}`} {
		if r := e.do(t, "PUT", path, body, own.auth()); r.status != http.StatusBadRequest {
			t.Fatalf("confirming with %s: %d %v", body, r.status, r.body)
		}
	}
	if r := e.do(t, "PUT", "/api/machines/"+e.localMachine(t)+"/customers", `{"on":false}`, own.auth()); r.status != http.StatusBadRequest {
		t.Fatalf("stopping the dashboard's own machine: %d %v", r.status, r.body)
	}
	if r := e.do(t, "PUT", "/api/machines/nomachine2/customers", `{"on":true}`, own.auth()); r.status != http.StatusNotFound {
		t.Fatalf("confirming a machine that isn't joined: %d %v", r.status, r.body)
	}

	guard.mu.Lock()
	guard.refuse = false
	guard.mu.Unlock()
	if err := e.srv.hub.Remove(context.Background(), rid, "admin"); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "PUT", path, `{"on":true}`, own.auth()); r.status != http.StatusNotFound {
		t.Fatalf("confirming a machine the owner removed: %d %v", r.status, r.body)
	}
}

// A machine that's away can't be confirmed, since it can't be asked to keep
// servers away from itself.
func TestAMachineThatsAwayIsntConfirmed(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	e.reply("GET", "/v1/machine", liveMachine(0, true))
	ra := newRemoteAgent()
	rid, link := e.joinMachine(t, own.cookie, own.csrf, ra)
	link.stop()
	eventually(t, "the machine is offline", func() bool { return linkState(e.machineView(t, own.cookie, rid)) == "offline" })
	if r := e.do(t, "PUT", "/api/machines/"+rid+"/customers", `{"on":true}`, own.auth()); r.status != http.StatusServiceUnavailable || r.body["code"] != machinelink.CodeNotConnected {
		t.Fatalf("confirming a machine that's away: %d %v", r.status, r.body)
	}
	if m := e.machineView(t, own.cookie, rid); m["takesCustomers"] != nil {
		t.Fatalf("a machine that's away takes customers: %v", m)
	}
}

// A customer placed on a joined machine creates their servers there, and
// uploads worlds and backups for them there, but not on the dashboard's own
// machine, whose catalog offers them no memory for a new server.
func TestACustomerCreatesServersOnTheJoinedMachineTheyrePlacedOn(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, ra, _ := joinForCustomers(t, e, own)
	ra.reply("GET /v1/catalog", `{"memoryOptionsMB":[2048,4096,8192],"recommendedMemoryMB":8192,"maxMemoryMB":24576,"memoryFreeMB":24576,"servers":[]}`)
	ra.reply("POST /v1/servers", `{"id":"0123456789abcdef","serverId":"cafebabe23","kind":"create","status":"running"}`)
	ra.reply("POST /v1/world-imports", `{"id":"0123456789abcdef","files":[]}`)
	e.reply("GET", "/v1/catalog", `{"memoryOptionsMB":[2048,4096],"recommendedMemoryMB":4096,"maxMemoryMB":8192,"memoryFreeMB":8192,"servers":[]}`)
	e.srv.notifier = &recordingNotifier{}
	if r := e.do(t, "PUT", "/api/machines/"+rid+"/customers", `{"on":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms it: %d %v", r.status, r.body)
	}
	core := customerCore{s: e.srv}
	ctx := context.Background()
	if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, "user_alex")
	alex := signIn(t, e, info.UserID)
	var me struct {
		Access struct {
			Home string `json:"home"`
		} `json:"access"`
	}
	if e.get(t, "/api/auth/me", alex.cookie, &me); me.Access.Home != rid {
		t.Fatalf("alex's dashboard says their servers go on %q, want %q", me.Access.Home, rid)
	}
	var ownMe struct {
		Access map[string]any `json:"access"`
	}
	if e.get(t, "/api/auth/me", own.cookie, &ownMe); ownMe.Access["home"] != nil {
		t.Fatalf("the owner's dashboard names a machine their servers go on: %v", ownMe.Access)
	}

	local := e.localMachine(t)
	create := `{"name":"alex","acceptEula":true,"memoryMB":4096}`
	if r := e.do(t, "POST", "/api/machines/"+local+"/servers", create, alex.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "isn't the machine your servers go on") {
		t.Fatalf("alex creates a server on the dashboard's machine: %d %v", r.status, r.body)
	}
	if e.sawLocally("POST /v1/servers") {
		t.Fatal("the dashboard's machine got alex's server")
	}
	if r := e.do(t, "POST", "/api/machines/"+local+"/world-imports", `{}`, alex.auth()); r.status != http.StatusForbidden {
		t.Fatalf("alex opens a world upload on the dashboard's machine: %d %v", r.status, r.body)
	}
	options := func(mid string) []any {
		t.Helper()
		var c map[string]any
		e.get(t, "/api/machines/"+mid+"/catalog", alex.cookie, &c)
		opts, _ := c["memoryOptionsMB"].([]any)
		return opts
	}
	if opts := options(local); len(opts) != 0 {
		t.Fatalf("the dashboard's machine offers alex %v for a new server", opts)
	}
	if opts := options(rid); !slices.Equal(opts, []any{float64(2048), float64(4096)}) {
		t.Fatalf("alex's machine offers %v for a new server", opts)
	}

	if r := e.do(t, "POST", "/api/machines/"+rid+"/world-imports", `{}`, alex.auth()); r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("alex opens a world upload on their machine: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/machines/"+rid+"/servers", create, alex.auth()); r.status != http.StatusOK || r.body["serverId"] != "cafebabe23" {
		t.Fatalf("alex creates a server on their machine: %d %v", r.status, r.body)
	}
	if actor, ok := ra.saw("POST /v1/servers"); !ok || actor != info.Username {
		t.Fatalf("alex's machine got the create from %q", actor)
	}
	if owned, _ := e.srv.creatorServers(info.UserID); !slices.Equal(owned, []string{"cafebabe23"}) {
		t.Fatalf("alex created %v", owned)
	}
	if _, ok := ra.saw("POST /v1/servers/cafebabe23/backup-rules"); !ok {
		t.Fatal("alex's new server didn't get a creator's backups")
	}
	if m, err := e.srv.machineForServer("cafebabe23"); err != nil || m.ID != rid {
		t.Fatalf("alex's server goes to %q, %v", m.ID, err)
	}
}

// Servers stay away from a joined machine while it takes customers, and
// while it has some after it stopped.
func TestServersStayAwayFromAJoinedMachineWhileItTakesCustomers(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, _, guard := joinForCustomers(t, e, own)
	e.srv.notifier = &recordingNotifier{}
	guardPath := "/api/machines/" + rid + "/network-guard"
	if r := e.do(t, "PUT", "/api/machines/"+rid+"/customers", `{"on":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms it: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", guardPath, `{"host":false}`, own.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "while it takes customers") {
		t.Fatalf("turning the guard off while it takes customers: %d %v", r.status, r.body)
	}
	if _, err := (customerCore{s: e.srv}).StartCustomer(context.Background(), Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "PUT", "/api/machines/"+rid+"/customers", `{"on":false}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner stops it: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", guardPath, `{"host":false}`, own.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "while it has customers") {
		t.Fatalf("turning the guard off while it has a customer: %d %v", r.status, r.body)
	}
	if asked := guard.requests(); slices.ContainsFunc(asked, func(b string) bool { return strings.Contains(b, `"host":false`) }) {
		t.Fatalf("a refused guard change reached the machine: %v", asked)
	}
	if _, err := e.srv.db.Exec(`DELETE FROM customer_homes`); err != nil {
		t.Fatal(err)
	}
	if r := e.do(t, "POST", guardPath, `{"host":false}`, own.auth()); r.status != http.StatusOK || r.body["host"] != false {
		t.Fatalf("turning the guard off once it has no customers: %d %v", r.status, r.body)
	}
}
