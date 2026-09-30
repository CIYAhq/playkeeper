package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Everyone on the team sees the machines but creators and customers, a
// paused one or one whose plan ended included.
func TestOnlyTheTeamSeesTheMachines(t *testing.T) {
	account := func(install, role string, servers invites.Scope, al invites.Allowance, customer CustomerState) access {
		return access{Account: invites.Account{UserID: 7, InstallRole: install, ProjectRole: role, Servers: servers, Allowance: al, TwoFactor: true}, FactorOn: true, Customer: customer}
	}
	some := invites.Scope{Servers: []string{sampleServer}}
	for _, c := range []struct {
		name string
		a    access
		sees bool
	}{
		{"the owner", account(roleOwner, invites.RoleAdmin, invites.AllServers(), invites.Allowance{}, ""), true},
		{"an admin of every server", account(roleMember, invites.RoleAdmin, invites.AllServers(), invites.Allowance{}, ""), true},
		{"a moderator of one server", account(roleMember, invites.RoleModerator, some, invites.Allowance{}, ""), true},
		{"a viewer", account(roleMember, invites.RoleViewer, some, invites.Allowance{}, ""), true},
		{"an invited creator", account(roleMember, invites.RoleAdmin, some, fourGB, ""), false},
		{"a customer", account(roleMember, invites.RoleAdmin, some, fourGB, CustomerActive), false},
		{"a paused customer", account(roleMember, invites.RoleAdmin, some, fourGB, CustomerPaused), false},
		{"a customer whose plan ended", account(roleMember, invites.RoleAdmin, some, invites.Allowance{}, CustomerPaused), false},
	} {
		if sees := permit(c.a, actViewMachines, "") == nil; sees != c.sees || c.a.hidesMachines() == c.sees {
			t.Errorf("%s sees the machines: %v, want %v", c.name, sees, c.sees)
		}
	}
}

// A customer placed on a joined machine sees their servers and never the
// machines. Their machine list names none and lists no other machine; the
// machines' own pages, events and usage are refused; a path naming any
// other machine isn't found; their catalog's memory is their plan's, and
// so are their servers' crash help and disk; and when their machine can't
// be reached, neither the refusal nor an AI agent names it. The owner sees
// all of it.
func TestACustomerSeesTheirServersAndNeverTheMachines(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, ra, _, link := joinForCustomers(t, e, own)
	attic := e.addRemote(t, "a2345abcde", "attic")
	local := e.localMachine(t)
	ra.reply("GET /v1/catalog", `{"memoryOptionsMB":[2048,4096,8192],"recommendedMemoryMB":8192,"hostMemoryMB":32000,"systemReserveMB":768,"maxMemoryMB":24576,"memoryFreeMB":24576,"servers":[]}`)
	ra.reply("POST /v1/servers", `{"id":"0123456789abcdef","serverId":"cafebabe23","kind":"create","status":"running"}`)
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
	if r := e.do(t, "POST", "/api/machines/"+rid+"/servers", `{"name":"alex","acceptEula":true,"memoryMB":2048}`, alex.auth()); r.status != http.StatusOK {
		t.Fatalf("alex creates a server: %d %v", r.status, r.body)
	}
	alex = signIn(t, e, info.UserID)
	ra.reply("GET /v1/servers", `[{"id":"cafebabe23","name":"alex","slug":"alex","phase":"crashed","gamePort":25566,"config":{"memoryMB":2048},
		"crash":{"at":"2026-09-24T11:00:00Z","start":false,"kind":"heap_out_of_memory","certain":true,"title":"","explanation":"","evidence":[],"fixes":[{"kind":"raise_memory","params":{"from_mb":2048,"to_mb":8192},"title":"Give it 8 GB"}],"lines":[],"roomMB":20000},
		"resources":{"diskFreeBytes":107374182400,"diskTotalBytes":214748364800,"at":"2026-09-24T11:00:00Z"}}]`)
	ra.reply("GET /v1/servers/cafebabe23", `{"id":"cafebabe23","name":"alex","slug":"alex","phase":"crashed","gamePort":25566,"config":{"memoryMB":2048},
		"crash":{"at":"2026-09-24T11:00:00Z","start":false,"kind":"heap_out_of_memory","certain":true,"title":"","explanation":"","evidence":[],"fixes":[{"kind":"raise_memory","params":{"from_mb":2048,"to_mb":8192},"title":"Give it 8 GB"}],"lines":[],"roomMB":20000},
		"resources":{"diskFreeBytes":107374182400,"diskTotalBytes":214748364800,"at":"2026-09-24T11:00:00Z"}}`)
	names := []string{"home-server", "attic", `"siya"`, "panel.example.com", "joinedFrom", "fingerprint"}
	shows := func(what string, body []byte) {
		t.Helper()
		for _, n := range names {
			if strings.Contains(string(body), n) {
				t.Errorf("%s shows alex %s: %s", what, n, body)
			}
		}
	}

	r, body := e.raw(t, "GET", "/api/machines", "", alex.auth())
	var machines []map[string]any
	if r.StatusCode != http.StatusOK || json.Unmarshal([]byte(body), &machines) != nil || len(machines) != 1 {
		t.Fatalf("alex's machine list: %d %s", r.StatusCode, body)
	}
	shows("the machine list", []byte(body))
	if strings.Contains(body, local) {
		t.Errorf("alex's machine list has the dashboard's own machine: %s", body)
	}
	m := machines[0]
	ml, _ := m["link"].(map[string]any)
	if m["id"] != rid || m["kind"] != "remote" || m["name"] != "" || ml["state"] != "connected" || ml["address"] != "127.0.0.1" || ml["name"] != nil {
		t.Errorf("alex's machine, for alex: %v", m)
	}
	if live, _ := m["live"].(map[string]any); len(live) != 1 || live["agentVersion"] == nil {
		t.Errorf("what alex's machine says of the dashboard: %v", live)
	}
	var full []map[string]any
	e.get(t, "/api/machines", own.cookie, &full)
	var named []string
	for _, m := range full {
		named = append(named, m["name"].(string))
	}
	if slices.Sort(named); !slices.Equal(named, []string{"attic", "home-server", "siya"}) {
		t.Errorf("the owner's machine list names %v", named)
	}

	for _, path := range []string{"/api/machines/link", "/api/machines/" + rid, "/api/machines/" + rid + "/events", "/api/machines/" + rid + "/activity",
		"/api/machines/" + rid + "/update", "/api/machines/" + rid + "/preflight", "/api/machines/" + rid + "/disk", "/api/usage-stats", "/api/server"} {
		if r, body := e.raw(t, "GET", path, "", alex.auth()); r.StatusCode != http.StatusForbidden {
			t.Errorf("alex looks at %s: %d %s", path, r.StatusCode, body)
		}
		if r, _ := e.raw(t, "GET", path, "", own.auth()); r.StatusCode == http.StatusForbidden {
			t.Errorf("the owner looks at %s: %d", path, r.StatusCode)
		}
	}
	for _, mid := range []string{attic.ID, local} {
		if r, body := e.raw(t, "GET", "/api/machines/"+mid+"/catalog", "", alex.auth()); r.StatusCode != http.StatusNotFound {
			t.Errorf("alex asks another machine for its catalog: %d %s", r.StatusCode, body)
		}
		if r, body := e.raw(t, "POST", "/api/machines/"+mid+"/servers", `{"name":"x","acceptEula":true,"memoryMB":2048}`, alex.auth()); r.StatusCode != http.StatusNotFound {
			t.Errorf("alex creates a server on another machine: %d %s", r.StatusCode, body)
		}
	}
	var catalog map[string]any
	if e.get(t, "/api/machines/"+rid+"/catalog", alex.cookie, &catalog); catalog["hostMemoryMB"] != float64(4096) || catalog["systemReserveMB"] != float64(0) {
		t.Errorf("alex's catalog's memory: %v of it, %v kept back", catalog["hostMemoryMB"], catalog["systemReserveMB"])
	}

	plan := func(what string, sv map[string]any) {
		t.Helper()
		crash, _ := sv["crash"].(map[string]any)
		res, _ := sv["resources"].(map[string]any)
		fixes, _ := crash["fixes"].([]any)
		if crash["roomMB"] != float64(2048) || len(fixes) != 0 || res["diskTotalBytes"] != float64(30<<30) || res["diskFreeBytes"] != float64(30<<30) || sv["machineId"] != nil && sv["machineId"] != rid {
			t.Errorf("%s, for alex: crash room %v with fixes %v, disk %v free of %v", what, crash["roomMB"], fixes, res["diskFreeBytes"], res["diskTotalBytes"])
		}
	}
	var servers []map[string]any
	if e.get(t, "/api/servers", alex.cookie, &servers); len(servers) != 1 {
		t.Fatalf("alex's servers: %v", servers)
	}
	plan("the server list", servers[0])
	var one map[string]any
	e.get(t, "/api/servers/cafebabe23", alex.cookie, &one)
	plan("the server", one)
	ra.reply("GET /v1/servers/cafebabe23/pregen", `{"state":"idle","installed":false,"pauseForPlayers":false,"presets":[],"diskFreeBytes":107374182400}`)
	var pregen map[string]any
	if e.get(t, "/api/servers/cafebabe23/pregen", alex.cookie, &pregen); pregen["diskFreeBytes"] != float64(30<<30) {
		t.Errorf("alex's pregen says %v free", pregen["diskFreeBytes"])
	}
	if r, body := e.raw(t, "POST", "/api/servers/cafebabe23/resourcepack", "pack", alex.auth()); r.StatusCode != http.StatusConflict || strings.Contains(body, "home-server") {
		t.Errorf("alex offers a resource pack on their machine: %d %s", r.StatusCode, body)
	}
	var owners []map[string]any
	e.get(t, "/api/servers", own.cookie, &owners)
	if i := slices.IndexFunc(owners, func(sv map[string]any) bool { return sv["id"] == "cafebabe23" }); i < 0 || owners[i]["crash"].(map[string]any)["roomMB"] != float64(20000) ||
		len(owners[i]["crash"].(map[string]any)["fixes"].([]any)) != 1 {
		t.Errorf("the owner's view of alex's server: %v", owners)
	}

	_, secret := e.newToken(t, alex.cookie, alex.csrf, `{"name":"bot","role":"viewer","servers":["cafebabe23"]}`)
	if a := e.callTool(t, secret, "list_servers", nil); a.isError || !strings.Contains(a.text, "alex") || strings.Contains(a.text, "home-server") {
		t.Errorf("alex's AI agent lists the servers: %+v", a)
	}

	link.stop()
	eventually(t, "the machine is offline", func() bool { return linkState(e.machineView(t, own.cookie, rid)) == "offline" })
	for _, who := range []member{alex, own} {
		r, body := e.raw(t, "POST", "/api/servers/cafebabe23/start", `{}`, who.auth())
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("starting the server while its machine is away: %d %s", r.StatusCode, body)
		}
		if who == alex {
			shows("the refusal", []byte(body))
			if !strings.Contains(body, unreachableText) || !strings.Contains(body, "machine_not_connected") {
				t.Errorf("the refusal alex gets: %s", body)
			}
		} else if !strings.Contains(body, "home-server") {
			t.Errorf("the refusal the owner gets: %s", body)
		}
	}
	if a := e.callTool(t, secret, "get_server_status", map[string]any{"server": "cafebabe23"}); !a.isError || a.kind != "machine_not_connected" || strings.Contains(a.text, "home-server") {
		t.Errorf("alex's AI agent asks about a server whose machine is away: %+v", a)
	}
}

// The sign-in page names the dashboard's machine, until customers sign in
// there with Whop.
func TestTheSignInPageNamesNoMachineOnceCustomersSignInThere(t *testing.T) {
	_, e, own := connectedWhop(t)
	var st api.SetupStatus
	if e.get(t, "/api/setup/status", "", &st); st.Machine == "" || st.WhopSignIn {
		t.Fatalf("the sign-in page before Sign in with Whop: %+v", st)
	}
	if r := e.do(t, "PUT", "/api/whop/signin", `{"clientId":"`+whopTestApp+`","clientSecret":"`+whopTestAppSecret+`"}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("setting up Sign in with Whop: %d %v", r.status, r.body)
	}
	st = api.SetupStatus{}
	if e.get(t, "/api/setup/status", "", &st); !st.WhopSignIn || st.Machine != "" {
		t.Fatalf("the sign-in page customers sign in on: %+v", st)
	}
}

// An invited creator lives on the dashboard's own machine, which their
// list shows as the only one, however many machines joined.
func TestAnInvitedCreatorSeesOnlyTheirOwnMachine(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	newCreatorAgent(e.env)
	alex := addCreator(t, e.env, "alex", fourGB)
	e.addRemote(t, "a2345abcde", "attic")
	var machines []map[string]any
	if e.get(t, "/api/machines", alex.cookie, &machines); len(machines) != 1 || machines[0]["kind"] != "local" || machines[0]["name"] != "" {
		t.Fatalf("an invited creator's machines: %v", machines)
	}
	var catalog map[string]any
	if st := e.get(t, "/api/machines/"+machines[0]["id"].(string)+"/catalog", alex.cookie, &catalog); st != http.StatusOK || catalog["hostMemoryMB"] != float64(4096) {
		t.Fatalf("an invited creator's catalog: %d %v", st, catalog)
	}
}
