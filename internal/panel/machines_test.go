package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agent"
	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/config"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/version"
)

func withDomain(c *config.Config) { c.Domain = "panel.example.com" }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// remoteAgent is a joined machine's agent. It answers whatever the machine
// link lets through and records each request with the actor it carried.
type remoteAgent struct {
	mu       sync.Mutex
	actors   map[string]string
	replies  map[string]string
	handlers map[string]http.HandlerFunc
}

func newRemoteAgent() *remoteAgent {
	return &remoteAgent{actors: map[string]string{}, handlers: map[string]http.HandlerFunc{}, replies: map[string]string{
		"GET /v1/machine": `{"hostname":"home-server","agentVersion":"0.4.0","memoryTotalMB":8192}`,
		"GET /v1/servers": `[{"id":"rstuvwxyzq","name":"Cobblemon","phase":"online"}]`,
		"GET /v1/audit":   `[{"id":1,"ts":"2026-09-24T11:00:00Z","actor":"admin","action":"server.start","result":"succeeded"}]`,
	}}
}

func (a *remoteAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	a.mu.Lock()
	a.actors[key] = r.Header.Get(machinelink.ActorHeader)
	reply, ok := a.replies[key]
	h := a.handlers[key]
	a.mu.Unlock()
	if h != nil {
		h(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		reply = `{"ok":true}`
	}
	io.WriteString(w, reply)
}

// handle makes the agent answer key ("METHOD path") with h.
func (a *remoteAgent) handle(key string, h http.HandlerFunc) {
	a.mu.Lock()
	a.handlers[key] = h
	a.mu.Unlock()
}

// saw reports whether the agent got the request, and with which actor.
func (a *remoteAgent) saw(key string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	actor, ok := a.actors[key]
	return actor, ok
}

func (a *remoteAgent) reply(key, body string) {
	a.mu.Lock()
	a.replies[key] = body
	a.mu.Unlock()
}

func (e *env) sawLocally(key string) bool {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	return contains(e.agent.hits, key)
}

// sharePort serves the panel as it runs, HTTPS with machines on the same
// port, and returns its address.
func (e *env) sharePort(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := EnsureSelfSignedCert(dir, e.clock.now()); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := e.srv.httpServer(ln.Addr().String(), cert)
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func (e *env) linkInfo(t *testing.T, cookie string) map[string]any {
	t.Helper()
	var out map[string]any
	if r := e.get(t, "/api/machines/link", cookie, &out); r != http.StatusOK {
		t.Fatalf("link info: %d %v", r, out)
	}
	return out
}

func (e *env) joinCode(t *testing.T, cookie, csrf, body string) map[string]any {
	t.Helper()
	r := e.do(t, "POST", "/api/machines/join-codes", body, auth(cookie, csrf))
	if r.status != http.StatusCreated {
		t.Fatalf("join code: %d %v", r.status, r.body)
	}
	return r.body
}

func (e *env) machineViews(t *testing.T, cookie string) []map[string]any {
	t.Helper()
	var list []map[string]any
	if r := e.get(t, "/api/machines", cookie, &list); r != http.StatusOK {
		t.Fatalf("machines: %d", r)
	}
	return list
}

func (e *env) machineView(t *testing.T, cookie, id string) map[string]any {
	t.Helper()
	for _, m := range e.machineViews(t, cookie) {
		if m["id"] == id {
			return m
		}
	}
	return nil
}

func linkState(m map[string]any) string {
	l, _ := m["link"].(map[string]any)
	s, _ := l["state"].(string)
	return s
}

func newIdentity(t *testing.T) *machinelink.Identity {
	t.Helper()
	id, err := machinelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// runningLink is a joined machine's side of its link, running until stopped.
type runningLink struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (e *env) runLink(t *testing.T, d machinelink.Dashboard, id *machinelink.Identity, h http.Handler) *runningLink {
	t.Helper()
	l, err := machinelink.NewLink(machinelink.LinkOptions{Dashboard: d, Identity: id, Handler: h, Routes: agent.LinkRoutes(),
		Version: version.Version, Now: e.clock.now, MinBackoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rl := &runningLink{cancel: cancel, done: make(chan struct{})}
	go func() {
		rl.err = l.Run(ctx)
		close(rl.done)
	}()
	t.Cleanup(func() { rl.stop() })
	return rl
}

func (rl *runningLink) stop() error {
	rl.cancel()
	<-rl.done
	return rl.err
}

func (rl *runningLink) ended(t *testing.T) error {
	t.Helper()
	select {
	case <-rl.done:
		return rl.err
	case <-time.After(10 * time.Second):
		t.Fatal("the link is still running")
		return nil
	}
}

// auditHas reports whether the panel's audit log has a row with these
// fields and a detail containing detail.
func (e *env) auditHas(t *testing.T, actor, action, target, result, detail string) bool {
	t.Helper()
	var n int
	if err := e.srv.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE actor = ? AND action = ? AND target = ? AND result = ? AND instr(detail, ?) > 0`,
		actor, action, target, result, detail).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestAMachineJoinsAndItsServersAreReachable(t *testing.T) {
	e := newEnvConfig(t, nil, withDomain)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/machine", `{"hostname":"my-vps","agentVersion":"0.4.0"}`)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"}]`)
	addr := e.sharePort(t)

	info := e.linkInfo(t, cookie)
	fp, _ := info["fingerprint"].(string)
	minimum, _ := info["minimum"].(map[string]any)
	if info["available"] != true || fp == "" || fmt.Sprint(info["codes"]) != "[]" || info["joinPausedSeconds"] != float64(0) ||
		minimum["cores"] != float64(2) || minimum["memoryGB"] != float64(3) || minimum["freeDiskGB"] != float64(5) {
		t.Fatalf("link info: %v", info)
	}

	jc := e.joinCode(t, cookie, csrf, `{"name":"home-server","dial":"name"}`)
	code, _ := jc["code"].(string)
	install, _ := jc["install"].(string)
	for _, want := range []string{"--join panel.example.com:8443", "--code " + code, "--fingerprint " + fp, "--name home-server"} {
		if !strings.Contains(install, want) {
			t.Errorf("the install command lacks %q: %s", want, install)
		}
	}
	if lines, _ := jc["joinLines"].([]any); len(lines) != 4 || lines[0] != `sudo playkeeper join panel.example.com:8443 \` {
		t.Errorf("join lines: %v", jc["joinLines"])
	}
	if jc["expiresAt"] != "2026-09-24T12:30:00Z" || jc["state"] != "waiting" || jc["dials"] != "panel.example.com:8443" || jc["createdBy"] != "admin" {
		t.Errorf("the code: %v", jc)
	}
	secretNowhere(t, e, code)

	ra := newRemoteAgent()
	id := newIdentity(t)
	d, err := machinelink.Join(context.Background(), machinelink.JoinOptions{Address: addr, Code: code, Fingerprint: fp, Identity: id,
		Name: "home-server", Version: version.Version, Now: e.clock.now})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	rid := d.MachineID
	link := e.runLink(t, d, id, ra)
	eventually(t, "the machine is connected", func() bool { return linkState(e.machineView(t, cookie, rid)) == "connected" })

	list := e.machineViews(t, cookie)
	if len(list) != 2 || list[0]["kind"] != "local" || list[1]["id"] != rid {
		t.Fatalf("the dashboard's machine comes first: %v", list)
	}
	m := list[1]
	live, _ := m["live"].(map[string]any)
	if m["kind"] != "remote" || m["name"] != "home-server" || m["dials"] != "panel.example.com:8443" || m["addedBy"] != "admin" ||
		m["joinedFrom"] != "127.0.0.1" || m["joinedAt"] != "2026-09-24T12:00:00Z" || live["hostname"] != "home-server" {
		t.Fatalf("the joined machine: %v", m)
	}
	codes, _ := e.linkInfo(t, cookie)["codes"].([]any)
	if len(codes) != 1 || codes[0].(map[string]any)["state"] != "used" || codes[0].(map[string]any)["machineId"] != rid {
		t.Fatalf("the code is used by the machine: %v", codes)
	}

	var servers []map[string]any
	e.get(t, "/api/servers", cookie, &servers)
	if len(servers) != 2 || servers[0]["id"] != "abcdefghjk" || servers[0]["machineId"] != list[0]["id"] ||
		servers[1]["id"] != "rstuvwxyzq" || servers[1]["machineId"] != rid || servers[1]["lastKnownAt"] != nil {
		t.Fatalf("servers on both machines: %v", servers)
	}

	if r := e.do(t, "POST", "/api/servers/rstuvwxyzq/start", `{}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("start on the joined machine: %d %v", r.status, r.body)
	}
	if actor, ok := ra.saw("POST /v1/servers/rstuvwxyzq/start"); !ok || actor != "admin" {
		t.Fatalf("the joined machine got the start from admin: %v %q", ok, actor)
	}
	if e.sawLocally("POST /v1/servers/rstuvwxyzq/start") {
		t.Fatal("the dashboard's own agent got another machine's start")
	}
	if r := e.do(t, "GET", "/api/servers/abcdefghjk", "", auth(cookie, "")); r.status != http.StatusOK || !e.sawLocally("GET /v1/servers/abcdefghjk") {
		t.Fatalf("the dashboard's own server stays local: %d", r.status)
	}
	e.do(t, "GET", "/api/servers/nobodyhass", "", auth(cookie, ""))
	for _, id := range []string{"abcdefghjk", "nobodyhass"} {
		if _, ok := ra.saw("GET /v1/servers/" + id); ok {
			t.Fatalf("the joined machine was asked about %s, which it doesn't run", id)
		}
	}

	ra.reply("POST /v1/servers", `{"id":"0123456789abcdef","serverId":"newsrvabcd","kind":"create","status":"running"}`)
	if r := e.do(t, "POST", "/api/machines/"+rid+"/servers", `{"name":"Skyblock"}`, auth(cookie, csrf)); r.status != http.StatusOK {
		t.Fatalf("create on the joined machine: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/servers/newsrvabcd", "", auth(cookie, "")); r.status != http.StatusOK {
		t.Fatalf("the new server: %d %v", r.status, r.body)
	}
	if _, ok := ra.saw("GET /v1/servers/newsrvabcd"); !ok {
		t.Fatal("a server made on the joined machine is found there before any list")
	}
	ra.reply("GET /v1/servers", `[{"id":"rstuvwxyzq","name":"Cobblemon","phase":"online"},{"id":"newsrvabcd","name":"Skyblock","phase":"starting"}]`)
	servers = nil
	e.get(t, "/api/servers", cookie, &servers)
	if ids(servers) != "abcdefghjk rstuvwxyzq newsrvabcd" {
		t.Fatalf("servers with the new one: %v", servers)
	}

	var audit []map[string]any
	e.get(t, "/api/audit", cookie, &audit)
	remoteEntry := false
	for _, a := range audit {
		if a["action"] == "server.start" && a["machineId"] == rid {
			remoteEntry = true
		}
	}
	if !remoteEntry || !e.auditHas(t, "admin", "machine.joined", "home-server", "succeeded", "from 127.0.0.1") ||
		!e.auditHas(t, "admin", "machine.join_code", jc["id"].(string), "created", "dials panel.example.com:8443") {
		t.Fatalf("the audit log has the join and the machine's own entries: %v", audit)
	}

	// Away, its servers show as last seen, and requests are refused plainly.
	link.stop()
	eventually(t, "the machine is offline", func() bool { return linkState(e.machineView(t, cookie, rid)) == "offline" })
	servers = nil
	e.get(t, "/api/servers", cookie, &servers)
	if ids(servers) != "abcdefghjk newsrvabcd rstuvwxyzq" || servers[1]["phase"] != "starting" || servers[2]["machineId"] != rid ||
		servers[2]["phase"] != "online" || servers[2]["lastKnownAt"] != "2026-09-24T12:00:00Z" {
		t.Fatalf("an offline machine's servers as last seen: %v", servers)
	}
	r := e.do(t, "POST", "/api/servers/rstuvwxyzq/start", `{}`, auth(cookie, csrf))
	if r.status != http.StatusServiceUnavailable || r.body["code"] != machinelink.CodeNotConnected {
		t.Fatalf("start on an offline machine: %d %v", r.status, r.body)
	}
	if merr, _ := e.machineView(t, cookie, rid)["error"].(map[string]any); merr["code"] != machinelink.CodeNotConnected {
		t.Fatalf("the offline machine says why: %v", merr)
	}

	var events []map[string]any
	eventually(t, "the disconnection is in the machine's events", func() bool {
		events = nil
		e.get(t, "/api/machines/"+rid+"/events", cookie, &events)
		return len(events) == 3
	})
	var kinds []string
	for _, ev := range events {
		kinds = append(kinds, ev["kind"].(string))
	}
	if strings.Join(kinds, " ") != "machine.disconnected machine.connected machine.joined" || events[2]["actor"] != "admin" || events[2]["address"] != "127.0.0.1" {
		t.Fatalf("recent events, newest first: %v", events)
	}

	// Back, then removed: its link ends and its key never works again.
	link = e.runLink(t, d, id, ra)
	eventually(t, "the machine is back", func() bool { return linkState(e.machineView(t, cookie, rid)) == "connected" })
	if r := e.do(t, "DELETE", "/api/machines/"+rid, "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("remove: %d %v", r.status, r.body)
	}
	if err := link.ended(t); machinelink.CodeOf(err) != machinelink.CodeMachineRemoved {
		t.Fatalf("the removed machine's link ended with %v", err)
	}
	if list := e.machineViews(t, cookie); len(list) != 1 || list[0]["kind"] != "local" {
		t.Fatalf("machines after the removal: %v", list)
	}
	servers = nil
	e.get(t, "/api/servers", cookie, &servers)
	if len(servers) != 1 || servers[0]["id"] != "abcdefghjk" {
		t.Fatalf("servers after the removal: %v", servers)
	}
	var claims int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM server_machines WHERE machine_id = ?`, rid).Scan(&claims)
	if claims != 0 || !e.auditHas(t, "admin", "machine.removed", "home-server", "succeeded", "") {
		t.Fatalf("the removal is audited and its servers forgotten: %d claims", claims)
	}
	if r := e.do(t, "DELETE", "/api/machines/"+rid, "", auth(cookie, csrf)); r.status != http.StatusNotFound {
		t.Fatalf("remove twice: %d", r.status)
	}
	if r := e.do(t, "DELETE", "/api/machines/"+list[0]["id"].(string), "", auth(cookie, csrf)); r.status != http.StatusBadRequest {
		t.Fatalf("remove the dashboard's own machine: %d", r.status)
	}
	_, err = machinelink.Join(context.Background(), machinelink.JoinOptions{Address: addr, Code: e.joinCode(t, cookie, csrf, `{}`)["code"].(string),
		Fingerprint: fp, Identity: id, Name: "home-server"})
	if machinelink.CodeOf(err) != machinelink.CodeMachineRemoved {
		t.Fatalf("a removed machine's key joins again: %v", err)
	}
}

// secretNowhere fails if a join code shows up in the logs, the audit log or
// the join codes table.
func secretNowhere(t *testing.T, e *env, code string) {
	t.Helper()
	forms := []string{code, strings.ReplaceAll(code, "-", "")}
	var stored []string
	rows, err := e.srv.db.Query(`SELECT id || name || dials || created_by FROM machine_join_codes UNION ALL SELECT actor || action || target || detail FROM audit`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		stored = append(stored, s)
	}
	haystack := e.logs.String() + strings.Join(stored, "\n")
	for _, f := range forms {
		if strings.Contains(haystack, f) {
			t.Fatalf("the join code %s was kept", f)
		}
	}
}

// addRemote adds a joined machine straight to the database.
func (e *env) addRemote(t *testing.T, id, name string) machine {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.db.Exec(`INSERT INTO machines(id, project_id, name, kind, created_at, public_key) SELECT ?, id, ?, ?, ?, ? FROM projects`,
		id, name, remoteKind, millis(e.clock.now()), []byte(pub)); err != nil {
		t.Fatal(err)
	}
	m, err := e.srv.machineByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func serverList(ids ...string) []map[string]any {
	var out []map[string]any
	for _, id := range ids {
		out = append(out, map[string]any{"id": id, "name": id, "phase": "online"})
	}
	return out
}

func lastKnownAt(sv map[string]any) time.Time {
	t, _ := sv["lastKnownAt"].(time.Time)
	return t
}

func ids(list []map[string]any) string {
	var out []string
	for _, sv := range list {
		out = append(out, fmt.Sprint(sv["id"]))
	}
	return strings.Join(out, " ")
}

func TestServersStayWithTheMachineThatRunsThem(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	s := e.srv
	local, err := s.machineForServer("nobodyhass")
	if err != nil || local.Kind != localKind {
		t.Fatalf("an unknown server is looked for on the dashboard's machine: %v %v", local, err)
	}
	alpha, beta := e.addRemote(t, "alphaalpha", "alpha"), e.addRemote(t, "betabetabe", "beta")
	owner := func(id string) string {
		m, err := s.machineForServer(id)
		if errors.Is(err, errDisputed) {
			return "disputed"
		}
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	events := func(m machine) string {
		rows, err := s.db.Query(`SELECT kind || ' ' || code FROM machine_events WHERE machine_id = ? ORDER BY id`, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var ev string
			rows.Scan(&ev)
			out = append(out, ev)
		}
		return strings.Join(out, ", ")
	}

	if got := ids(s.claimServers(alpha, serverList("xxxxxxxxxx", "yyyyyyyyyy", "../etc", ""))); got != "xxxxxxxxxx yyyyyyyyyy" {
		t.Fatalf("alpha shows %q", got)
	}
	if owner("xxxxxxxxxx") != alpha.ID || owner("yyyyyyyyyy") != alpha.ID {
		t.Fatal("alpha's servers go to alpha")
	}

	// A server that two joined machines list goes to neither.
	if got := ids(s.claimServers(beta, serverList("xxxxxxxxxx", "zzzzzzzzzz"))); got != "zzzzzzzzzz" {
		t.Fatalf("beta shows %q", got)
	}
	if owner("xxxxxxxxxx") != "disputed" || owner("zzzzzzzzzz") != beta.ID || !strings.Contains(e.logs.String(), "another machine runs") {
		t.Fatal("beta listing alpha's server disputes it")
	}
	s.claimServers(beta, serverList("xxxxxxxxxx", "zzzzzzzzzz"))
	if got := events(beta); got != "machine.server_disputed xxxxxxxxxx" {
		t.Fatalf("beta's events: %q", got)
	}
	if r := e.do(t, "POST", "/api/servers/xxxxxxxxxx/start", `{}`, auth(cookie, csrf)); r.status != http.StatusConflict || r.body["code"] != codeServerDisputed ||
		e.sawLocally("POST /v1/servers/xxxxxxxxxx/start") {
		t.Fatalf("a disputed server's requests go nowhere: %d %v", r.status, r.body)
	}
	if got := s.claimServers(alpha, serverList("xxxxxxxxxx", "yyyyyyyyyy")); len(got) != 2 || got[0]["disputed"] != true || got[1]["disputed"] != nil {
		t.Fatalf("alpha's list marks the disputed server: %v", got)
	}
	s.claimServers(beta, serverList("zzzzzzzzzz"))
	if owner("xxxxxxxxxx") != alpha.ID {
		t.Fatal("once beta stops listing it, alpha has it again")
	}

	// The dashboard's own machine keeps its ids, even while its agent can't
	// be reached.
	s.claimLocal(local, serverList("yyyyyyyyyy", "localsrvab", "../etc"))
	if owner("yyyyyyyyyy") != local.ID || owner("localsrvab") != local.ID {
		t.Fatal("the dashboard's own machine takes its server ids")
	}
	if got := ids(s.claimServers(alpha, serverList("xxxxxxxxxx", "yyyyyyyyyy", "localsrvab"))); got != "xxxxxxxxxx" ||
		owner("yyyyyyyyyy") != local.ID || owner("localsrvab") != local.ID {
		t.Fatalf("alpha can't take the dashboard's servers: alpha shows %q", got)
	}
	s.claimLocal(local, serverList("yyyyyyyyyy"))
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM server_machines WHERE server_id IN ('localsrvab', '../etc')`).Scan(&n)
	if n != 0 {
		t.Fatal("a server the dashboard's machine no longer lists is forgotten")
	}

	// Statuses are kept for when a machine is away, at most every 30 s.
	stopped := []map[string]any{{"id": "xxxxxxxxxx", "name": "x", "phase": "stopped"}}
	e.clock.add(10 * time.Second)
	s.claimServers(alpha, stopped)
	if last := s.lastKnownServers(alpha); len(last) != 1 || last[0]["phase"] != "online" || !lastKnownAt(last[0]).Equal(e.clock.now().Add(-10*time.Second)) {
		t.Fatalf("within 30 s: %v", last)
	}
	e.clock.add(25 * time.Second)
	s.claimServers(alpha, stopped)
	if last := s.lastKnownServers(alpha); len(last) != 1 || last[0]["phase"] != "stopped" || !lastKnownAt(last[0]).Equal(e.clock.now()) {
		t.Fatalf("after 30 s: %v", last)
	}

	s.claimServers(alpha, nil)
	if owner("xxxxxxxxxx") != local.ID || len(s.lastKnownServers(alpha)) != 0 {
		t.Fatal("a server alpha no longer lists is forgotten")
	}
	s.claimCreated(alpha, []byte(`{"id":"0123456789abcdef","serverId":"newsrvabcd"}`))
	s.claimCreated(alpha, []byte(`{"serverId":"yyyyyyyyyy"}`))
	s.claimCreated(alpha, []byte(`{"serverId":"../etc"}`))
	s.claimCreated(local, []byte(`{"serverId":"newlocalab"}`))
	if owner("newsrvabcd") != alpha.ID || owner("yyyyyyyyyy") != local.ID {
		t.Fatal("a server made on alpha goes to alpha, and only if no machine has its id")
	}
	var mid string
	s.db.QueryRow(`SELECT machine_id FROM server_machines WHERE server_id = 'newlocalab'`).Scan(&mid)
	s.db.QueryRow(`SELECT COUNT(*) FROM server_machines WHERE server_id = '../etc'`).Scan(&n)
	if mid != local.ID || n != 0 {
		t.Fatalf("a server made on the dashboard's machine is kept as its: %q, odd ids %d", mid, n)
	}

	// Removing a machine ends its disputes and forgets its servers.
	s.claimServers(beta, serverList("newsrvabcd", "zzzzzzzzzz"))
	if owner("newsrvabcd") != "disputed" {
		t.Fatal("beta disputes alpha's new server")
	}
	s.onMachineEvent(machinelink.Event{Kind: machinelink.EventRemoved, MachineID: beta.ID, Name: "beta", Actor: "admin", At: e.clock.now()})
	if owner("newsrvabcd") != alpha.ID || owner("zzzzzzzzzz") != local.ID {
		t.Fatal("a removed machine's servers and disputes are gone")
	}
}

func TestAMachineCantFillTheDatabaseWithServers(t *testing.T) {
	e := newEnv(t)
	alpha := e.addRemote(t, "alphaalpha", "alpha")
	var many []string
	for range maxMachineServers + 50 {
		many = append(many, randomID())
	}
	if got := e.srv.claimServers(alpha, serverList(many...)); len(got) != maxMachineServers {
		t.Fatalf("alpha shows %d servers", len(got))
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM server_machines WHERE machine_id = ?`, alpha.ID).Scan(&n)
	if n != maxMachineServers || !strings.Contains(e.logs.String(), "more servers than the dashboard keeps") {
		t.Fatalf("rows kept: %d", n)
	}
}

func TestAnOfflineMachineShowsItsLastKnownServers(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"}]`)
	alpha := e.addRemote(t, "alphaalpha", "alpha")
	e.srv.claimServers(alpha, serverList("xxxxxxxxxx"))
	var list []map[string]any
	if r := e.get(t, "/api/servers", cookie, &list); r != http.StatusOK || ids(list) != "abcdefghjk xxxxxxxxxx" || list[1]["machineId"] != alpha.ID || list[1]["lastKnownAt"] == nil {
		t.Fatalf("servers: %d %v", r, list)
	}
	m := e.machineView(t, cookie, alpha.ID)
	if merr, _ := m["error"].(map[string]any); merr["code"] != machinelink.CodeNotConnected || linkState(m) != "waiting" {
		t.Fatalf("a machine that never connected: %v", m)
	}
	if r := e.do(t, "POST", "/api/servers/xxxxxxxxxx/restart", `{}`, auth(cookie, csrf)); r.status != http.StatusServiceUnavailable || r.body["code"] != machinelink.CodeNotConnected {
		t.Fatalf("restart on an offline machine: %d %v", r.status, r.body)
	}
	if e.sawLocally("POST /v1/servers/xxxxxxxxxx/restart") {
		t.Fatal("another machine's restart went to the dashboard's agent")
	}
}

// With other machines joined, the dashboard's own servers stay listed as
// last known while its agent is down, instead of vanishing from the list.
func TestTheDashboardsServersStayListedWhileItsAgentIsDown(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"}]`)
	var list []map[string]any
	e.get(t, "/api/servers", cookie, &list)
	e.addRemote(t, "alphaalpha", "alpha")
	os.Remove(e.cfg.SocketPath)
	e.srv.agent = agentclient.New(e.cfg.SocketPath)
	if r := e.get(t, "/api/servers", cookie, &list); r != http.StatusOK || ids(list) != "abcdefghjk" ||
		list[0]["machineId"] != e.localMachine(t) || list[0]["phase"] != "online" || list[0]["lastKnownAt"] == nil {
		t.Fatalf("servers while the dashboard's agent is down: %d %v", r, list)
	}
	local := e.machineView(t, cookie, e.localMachine(t))
	if merr, _ := local["error"].(map[string]any); merr["code"] != api.CodeAgentUnavailable {
		t.Fatalf("the dashboard's machine says its agent is down: %v", local)
	}
}

func TestJoinCodesAreForThoseWhoManageMachines(t *testing.T) {
	e := newEnvConfig(t, nil, withDomain)
	cookie, csrf := e.setup(t)
	first := e.joinCode(t, cookie, csrf, `{}`)
	second := e.joinCode(t, cookie, csrf, `{"name":"  box   two ","dial":"name"}`)
	if first["name"] != nil || second["name"] != "box two" || !strings.Contains(second["install"].(string), "--name 'box two'") {
		t.Fatalf("names: %v / %v", first, second)
	}
	for _, bad := range []string{`{"name":"///"}`, `{"dial":"carrier-pigeon"}`, `{"name":1}`} {
		if r := e.do(t, "POST", "/api/machines/join-codes", bad, auth(cookie, csrf)); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %v", bad, r.status, r.body)
		}
	}
	if codes, _ := e.linkInfo(t, cookie)["codes"].([]any); len(codes) != 2 {
		t.Fatalf("two codes wait: %v", codes)
	}
	path := "/api/machines/join-codes/" + first["id"].(string)
	if r := e.do(t, "DELETE", path, "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("cancel: %d %v", r.status, r.body)
	}
	for _, p := range []string{path, "/api/machines/join-codes/zzzzzzzzzz", "/api/machines/join-codes/NOT-AN-ID"} {
		if r := e.do(t, "DELETE", p, "", auth(cookie, csrf)); r.status != http.StatusNotFound {
			t.Errorf("cancel %s: %d", p, r.status)
		}
	}
	if !e.auditHas(t, "admin", "machine.join_code", first["id"].(string), "cancelled", "") {
		t.Fatal("the cancel is audited")
	}
	e.clock.add(31 * time.Minute)
	if codes, _ := e.linkInfo(t, cookie)["codes"].([]any); len(codes) != 1 || codes[0].(map[string]any)["state"] != "expired" {
		t.Fatalf("a code that ran out: %v", codes)
	}
	e.clock.add(40 * time.Minute)
	e.linkInfo(t, cookie)
	e.clock.add(20 * time.Minute)
	if codes, _ := e.linkInfo(t, cookie)["codes"].([]any); len(codes) != 0 {
		t.Fatalf("codes that ran out over an hour ago aren't listed: %v", codes)
	}

	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('friend', ?, 0, 0, 'member')`, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"friend","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != http.StatusOK {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	mc, mcsrf := r.cookie, r.body["csrfToken"].(string)
	if info := e.linkInfo(t, mc); info["codes"] != nil || info["fingerprint"] == nil {
		t.Fatalf("a member sees the fingerprint but not the codes: %v", info)
	}
	if r := e.do(t, "POST", "/api/machines/join-codes", `{}`, auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member makes a code: %d", r.status)
	}
	if r := e.do(t, "DELETE", "/api/machines/join-codes/"+second["id"].(string), "", auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member cancels a code: %d", r.status)
	}
	local := e.machineViews(t, mc)[0]["id"].(string)
	if r := e.do(t, "DELETE", "/api/machines/"+local, "", auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member removes a machine: %d", r.status)
	}
}

func TestTooManyWrongCodesPauseJoining(t *testing.T) {
	if ln, err := net.Listen("tcp", "127.0.0.2:0"); err != nil {
		t.Skip("this system has no loopback addresses besides 127.0.0.1")
	} else {
		ln.Close()
	}
	e := newEnvConfig(t, nil, withDomain)
	cookie, csrf := e.setup(t)
	addr := e.sharePort(t)
	fp := e.linkInfo(t, cookie)["fingerprint"].(string)
	guess := func(from string) string {
		t.Helper()
		d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(from)}}
		_, err := machinelink.Join(context.Background(), machinelink.JoinOptions{Address: addr, Code: "7KQ2-M9XD", Fingerprint: fp,
			Identity: newIdentity(t), Name: "guess", Dial: d.DialContext})
		return machinelink.CodeOf(err)
	}

	// One noisy address is shut out on its own; codes can still be made.
	for range 5 {
		if c := guess("127.0.0.1"); c != machinelink.CodeJoinCodeWrong {
			t.Fatalf("a wrong code: %s", c)
		}
	}
	if c := guess("127.0.0.1"); c != machinelink.CodeJoinRateLimited {
		t.Fatalf("the sixth wrong code from one address: %s", c)
	}
	if e.linkInfo(t, cookie)["joinPausedSeconds"] != float64(0) {
		t.Fatal("one address pauses joining for everyone")
	}
	e.joinCode(t, cookie, csrf, `{}`)

	// Wrong codes from many addresses pause joining for everyone.
	for i := 2; i <= 4; i++ {
		for range 5 {
			guess(fmt.Sprintf("127.0.0.%d", i))
		}
	}
	if info := e.linkInfo(t, cookie); info["joinPausedSeconds"] != float64(900) {
		t.Fatalf("paused: %v", info["joinPausedSeconds"])
	}
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/machines/join-codes", strings.NewReader(`{}`))
	for k, v := range auth(cookie, csrf) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Origin", e.ts.URL)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") != "900" ||
		!strings.Contains(string(body), `"code":"join_rate_limited"`) || !strings.Contains(string(body), `"seconds":"900"`) ||
		!strings.Contains(string(body), "Try again in 15 minutes.") {
		t.Fatalf("a code while paused: %d %s", resp.StatusCode, body)
	}
	// Each kind of refusal from one address gets a row, and its repeats one
	// more that counts them, written when the log is read. Refusals while
	// joining is paused are counted together, from every address.
	for range 3 {
		guess("127.0.0.5")
	}
	if got := e.auditDetails(t, "machine.join"); len(got) != 5 {
		t.Fatalf("before the log is read: %q", got)
	}
	var entries []map[string]any
	e.get(t, "/api/audit", cookie, &entries)
	want := []string{
		"join_code_wrong from 127.0.0.1",
		"join_rate_limited from 127.0.0.1",
		"join_code_wrong from 127.0.0.2",
		"join_code_wrong from 127.0.0.3",
		"join_code_wrong from 127.0.0.4",
		"join_code_wrong from 127.0.0.1 · 4 more at 12:00 UTC",
		"join_code_wrong from 127.0.0.2 · 4 more at 12:00 UTC",
		"join_code_wrong from 127.0.0.3 · 4 more at 12:00 UTC",
		"join_code_wrong from 127.0.0.4 · 4 more at 12:00 UTC",
		"join_rate_limited · 3 more at 12:00 UTC",
	}
	if got := e.auditDetails(t, "machine.join"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("refused joins in the audit log:\n%s", strings.Join(got, "\n"))
	}
	if !e.auditHas(t, "(unknown machine)", "machine.join", "", "refused", "join_rate_limited · 3 more") {
		t.Fatal("the count has the first refusal's actor and result")
	}
	e.clock.add(15 * time.Minute)
	e.joinCode(t, cookie, csrf, `{}`)
}

// auditDetails are the details of the panel's audit rows for an action,
// oldest first.
func (e *env) auditDetails(t *testing.T, action string) []string {
	t.Helper()
	rows, err := e.srv.db.Query(`SELECT detail FROM audit WHERE action = ? ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		out = append(out, d)
	}
	return out
}

func TestDialAddresses(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*config.Config)
		host string
		want string
		// hostIP is set when the IP is this host's own, which depends on
		// where the test runs; want then leaves it out.
		hostIP bool
	}{
		{"the domain wins", withDomain, "203.0.113.7:8443", "name=panel.example.com:8443 ip=203.0.113.7:8443", false},
		{"the name the admin used", nil, "play.example.net:8443", "name=play.example.net:8443", true},
		{"an IPv6 address", nil, "[2001:db8::7]:8443", "ip=[2001:db8::7]:8443", false},
		{"not localhost", nil, "localhost:8443", "", true},
		{"localhost in development", func(c *config.Config) { c.Dev = true; c.PanelPort = 8448 }, "localhost:8448", "name=localhost:8448 ip=127.0.0.1:8448", false},
		{"loopback in development", func(c *config.Config) { c.Dev = true }, "127.0.0.1:8443", "ip=127.0.0.1:8443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnvConfig(t, nil, tc.mod)
			r := httptest.NewRequest("GET", "/api/machines/link", nil)
			r.Host = tc.host
			var got []string
			for _, a := range e.srv.dialAddresses(context.Background(), r) {
				if tc.hostIP && a.Kind == dialIP {
					continue
				}
				got = append(got, a.Kind+"="+a.Address)
			}
			if s := strings.Join(got, " "); s != tc.want {
				t.Fatalf("got %q, want %q", s, tc.want)
			}
		})
	}
}

func TestANameBehindCloudflareIsFlagged(t *testing.T) {
	e := newEnvConfig(t, nil, withDomain)
	e.names.set("panel.example.com", "104.16.132.229")
	r := httptest.NewRequest("GET", "/api/machines/link", nil)
	r.Host = "203.0.113.7:8443"
	addrs := e.srv.dialAddresses(context.Background(), r)
	if len(addrs) != 2 || !addrs[0].Proxied || addrs[1].Proxied {
		t.Fatalf("addresses: %+v", addrs)
	}
	e.names.mu.Lock()
	e.names.addrs["panel.example.com"] = nil
	e.names.mu.Unlock()
	e.names.set("panel.example.com", "203.0.113.7")
	if !e.srv.dialAddresses(context.Background(), r)[0].Proxied {
		t.Fatal("the answer is kept for a few minutes")
	}
	e.clock.add(6 * time.Minute)
	if e.srv.dialAddresses(context.Background(), r)[0].Proxied {
		t.Fatal("the name no longer points at Cloudflare")
	}
	for _, a := range []string{"2606:4700::6810:84e5", "::ffff:172.67.1.1"} {
		if !behindCloudflare([]netip.Addr{netip.MustParseAddr(a)}) {
			t.Errorf("%s is Cloudflare's", a)
		}
	}
}

func TestWithoutMachineLinks(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	s, err := New(Options{Config: e.cfg, Now: e.clock.now, Agent: e.srv.agent})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.hub != nil {
		t.Fatal("a panel without link routes runs a hub")
	}
	ts := httptest.NewTLSServer(s.Handler())
	defer ts.Close()
	plain := &env{srv: s, ts: ts, clock: e.clock, agent: e.agent, cfg: e.cfg, names: e.names, logs: e.logs}
	alpha := plain.addRemote(t, "alphaalpha", "alpha")
	if info := plain.linkInfo(t, cookie); info["available"] != false || info["fingerprint"] != nil {
		t.Fatalf("link info: %v", info)
	}
	if r := plain.do(t, "POST", "/api/machines/join-codes", `{}`, auth(cookie, csrf)); r.status != http.StatusServiceUnavailable {
		t.Fatalf("a join code: %d", r.status)
	}
	if merr, _ := plain.machineView(t, cookie, alpha.ID)["error"].(map[string]any); merr["code"] != machinelink.CodeNotConnected {
		t.Fatalf("a joined machine: %v", merr)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.PanelDir(), "link.key")); err != nil {
		t.Fatal("the panel with links made its key")
	}
}

func TestLinkStore(t *testing.T) {
	e := newEnv(t)
	st := &linkStore{db: e.srv.db}
	ctx := context.Background()
	now := e.clock.now()
	pub, _, _ := ed25519.GenerateKey(nil)
	jc := machinelink.JoinCode{ID: "cdefghjkmn", Hash: []byte("hash"), CreatedAt: now, ExpiresAt: now.Add(machinelink.CodeTTL), CreatedBy: "admin"}
	if err := st.AddJoinCode(ctx, jc); err != nil {
		t.Fatal(err)
	}
	m := machinelink.Machine{ID: "mnpqrstuvw", Name: "home-server", PublicKey: pub, JoinedAt: now, JoinedFrom: "203.0.113.9", CreatedBy: "admin", Version: "0.4.0"}
	if err := st.Pair(ctx, "zzzzzzzzzz", m); !errors.Is(err, machinelink.ErrNotFound) {
		t.Fatalf("pair with a missing code: %v", err)
	}
	if err := st.Pair(ctx, jc.ID, m); err != nil {
		t.Fatal(err)
	}
	m2 := m
	m2.ID = "pqrstuvwxy"
	if err := st.Pair(ctx, jc.ID, m2); !errors.Is(err, machinelink.ErrCodeUsed) {
		t.Fatalf("pair with a used code: %v", err)
	}
	codes, _ := st.JoinCodes(ctx)
	if len(codes) != 1 || codes[0].MachineID != m.ID || !codes[0].UsedAt.Equal(now) || string(codes[0].Hash) != "hash" {
		t.Fatalf("codes: %+v", codes)
	}
	got, found, err := st.MachineByKey(ctx, pub)
	if err != nil || !found || got.Name != "home-server" || got.JoinedFrom != "203.0.113.9" || got.CreatedBy != "admin" || !got.JoinedAt.Equal(now) {
		t.Fatalf("by key: %+v %v %v", got, found, err)
	}
	if _, found, _ := st.Machine(ctx, m2.ID); found {
		t.Fatal("the second pairing added a machine")
	}
	local, _ := e.srv.machines()
	if _, found, _ := st.Machine(ctx, local[0].ID); found {
		t.Fatal("the dashboard's own machine is not a joined one")
	}

	later := now.Add(time.Minute)
	if err := st.Seen(ctx, m.ID, later, "", "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	st.Seen(ctx, m.ID, now, "0.4.1", "")
	got, _, _ = st.Machine(ctx, m.ID)
	if !got.LastSeen.Equal(later) || got.Version != "0.4.1" || got.LastAddr != "203.0.113.10" {
		t.Fatalf("seen: %+v", got)
	}
	if err := st.Seen(ctx, "zzzzzzzzzz", now, "", ""); !errors.Is(err, machinelink.ErrNotFound) {
		t.Fatalf("seen, a missing machine: %v", err)
	}

	if err := st.Revoke(ctx, m.ID, later, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.Revoke(ctx, m.ID, later.Add(time.Hour), "machine:"+m.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _ = st.Machine(ctx, m.ID)
	if !got.Removed() || !got.RevokedAt.Equal(later) || got.RevokedBy != "admin" {
		t.Fatalf("the first removal is kept: %+v", got)
	}
	if err := st.Revoke(ctx, "zzzzzzzzzz", now, "admin"); !errors.Is(err, machinelink.ErrNotFound) {
		t.Fatalf("revoke, a missing machine: %v", err)
	}
	if all, _ := st.Machines(ctx); len(all) != 1 || all[0].ID != m.ID {
		t.Fatalf("removed machines stay listed: %+v", all)
	}
	if list, _ := e.srv.machines(); len(list) != 1 {
		t.Fatalf("the panel leaves removed machines out: %v", list)
	}

	if err := st.DeleteJoinCode(ctx, jc.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteJoinCode(ctx, jc.ID); !errors.Is(err, machinelink.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestMachineEventsAreCapped(t *testing.T) {
	e := newEnv(t)
	for i := range maxMachineEvents + 20 {
		e.srv.machineEvent("alphaalpha", e.clock.now(), "machine.connected", "", fmt.Sprintf("203.0.113.%d", i%250), "")
	}
	e.srv.machineEvent("betabetabe", e.clock.now(), "machine.connected", "", "", "")
	var alpha, beta int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM machine_events WHERE machine_id = 'alphaalpha'`).Scan(&alpha)
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM machine_events WHERE machine_id = 'betabetabe'`).Scan(&beta)
	if alpha != maxMachineEvents || beta != 1 {
		t.Fatalf("events kept: alpha %d, beta %d", alpha, beta)
	}
}

func TestFailuresOfJoinedMachines(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&machinelink.Error{Code: machinelink.CodeNotConnected, Msg: "home-server is not connected."}, 503, machinelink.CodeNotConnected},
		{&machinelink.Error{Code: machinelink.CodeMachineRemoved, Msg: "removed"}, 404, machinelink.CodeMachineRemoved},
		{&machinelink.Error{Code: machinelink.CodeTimeout, Msg: "slow"}, 504, machinelink.CodeTimeout},
		{&machinelink.Error{Code: machinelink.CodeRouteNotAllowed, Msg: "no"}, 500, machinelink.CodeRouteNotAllowed},
		{&machinelink.Error{Code: machinelink.CodeDropped, Msg: "dropped"}, 502, machinelink.CodeDropped},
		{fmt.Errorf("wrapped: %w", errLinksOff), 503, machinelink.CodeNotConnected},
		{errors.New("dial unix: no such file"), 503, "agent_unavailable"},
		{&agentclient.Error{Status: 409, Body: api.Error{Error: "busy", Code: "conflict"}}, 409, "conflict"},
		{&agentclient.Error{Status: 401, Body: api.Error{Error: "sign in", Code: "unauthorized"}}, 502, "unauthorized"},
		{&agentclient.Error{Status: 407, Body: api.Error{Error: "proxy", Code: "internal"}}, 502, "internal"},
		{&agentclient.Error{Status: 999, Body: api.Error{Error: "odd", Code: "internal"}}, 502, "internal"},
		{fmt.Errorf("decoding: %w", agentclient.ErrBadAnswer), 502, machinelink.CodeProtocol},
	} {
		status, body := failureOf(tc.err)
		if status != tc.status || body.Code != tc.code || body.Error == "" {
			t.Errorf("%v: %d %+v", tc.err, status, body)
		}
	}
}

// joined joins a machine whose agent is h and waits until it's connected.
func (e *env) joined(t *testing.T, cookie, csrf string, h http.Handler) (string, *runningLink) {
	t.Helper()
	addr := e.sharePort(t)
	fp, _ := e.linkInfo(t, cookie)["fingerprint"].(string)
	code, _ := e.joinCode(t, cookie, csrf, `{"name":"home-server"}`)["code"].(string)
	id := newIdentity(t)
	d, err := machinelink.Join(context.Background(), machinelink.JoinOptions{Address: addr, Code: code, Fingerprint: fp, Identity: id,
		Name: "home-server", Version: version.Version, Now: e.clock.now})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	link := e.runLink(t, d, id, h)
	eventually(t, "the machine is connected", func() bool { return linkState(e.machineView(t, cookie, d.MachineID)) == "connected" })
	return d.MachineID, link
}

// fetch sends a request as the browser would and returns the answer with
// its whole body.
func (e *env) fetch(t *testing.T, method, path, contentType, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Origin", e.ts.URL)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r, string(b)
}

func TestAJoinedMachineCantChooseHowTheDashboardServesItsAnswers(t *testing.T) {
	e := newEnvConfig(t, nil, withDomain)
	cookie, csrf := e.setup(t)
	ra := newRemoteAgent()
	e.joined(t, cookie, csrf, ra)
	e.get(t, "/api/servers", cookie, nil)

	const bid = "20260924-120000-abcdef"
	download := "/api/servers/rstuvwxyzq/backups/" + bid + "/download"
	page := `<!doctype html><script src="/api/servers/rstuvwxyzq/backups/evil/download"></script>`
	ra.handle("GET /v1/servers/rstuvwxyzq/backups/"+bid+"/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("Content-Security-Policy", "default-src *")
		w.Header().Set("X-Playkeeper-SHA256", `"><img src=x>`)
		io.WriteString(w, page)
	})
	r, body := e.fetch(t, "GET", download, "", "", auth(cookie, ""))
	h := r.Header
	if r.StatusCode != http.StatusOK || body != page || h.Get("Content-Type") != "application/octet-stream" ||
		h.Get("Content-Disposition") != `attachment; filename="playkeeper-`+bid+`.tar.gz"` || h.Get("Content-Security-Policy") != "sandbox" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Playkeeper-SHA256") != "" {
		t.Fatalf("a machine's page downloads as a file: %d %v", r.StatusCode, h)
	}
	if actor, _ := ra.saw("GET /v1/servers/rstuvwxyzq/backups/" + bid + "/download"); actor != "admin" {
		t.Fatalf("the download carries the actor: %q", actor)
	}

	sum := strings.Repeat("0a", 32)
	ra.handle("GET /v1/servers/rstuvwxyzq/backups/"+bid+"/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="playkeeper-Cobblemon-`+bid+`.tar.gz"`)
		w.Header().Set("X-Playkeeper-SHA256", sum)
		io.WriteString(w, "archive")
	})
	r, body = e.fetch(t, "GET", download, "", "", auth(cookie, ""))
	if h = r.Header; body != "archive" || h.Get("Content-Disposition") != `attachment; filename="playkeeper-Cobblemon-`+bid+`.tar.gz"` ||
		h.Get("X-Playkeeper-SHA256") != sum || h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Length") != "7" {
		t.Fatalf("an archive keeps its name and checksum: %v %q", h, body)
	}

	ra.handle("GET /v1/servers/rstuvwxyzq/backups/"+bid+"/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, page)
	})
	r, body = e.fetch(t, "GET", download, "", "", auth(cookie, ""))
	if r.StatusCode != http.StatusNotFound || r.Header.Get("Content-Type") != "application/json" || strings.Contains(body, "<script") {
		t.Fatalf("a machine's error page is not passed on: %d %v %q", r.StatusCode, r.Header, body)
	}
	if r, _ := e.fetch(t, "GET", "/api/servers/rstuvwxyzq/backups/..%2f..%2fx/download", "", "", auth(cookie, "")); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("an odd backup id: %d", r.StatusCode)
	}

	ra.handle("GET /v1/servers/rstuvwxyzq/icon", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)
	})
	if r, _ := e.fetch(t, "GET", "/api/servers/rstuvwxyzq/icon", "", "", auth(cookie, "")); r.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("an icon is always a PNG: %v", r.Header)
	}

	ra.handle("POST /v1/servers/rstuvwxyzq/icon", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, page)
	})
	r, body = e.fetch(t, "POST", "/api/servers/rstuvwxyzq/icon", "image/png", "png", auth(cookie, csrf))
	if r.StatusCode != http.StatusBadGateway || r.Header.Get("Content-Type") != "application/json" || strings.Contains(body, "<script") {
		t.Fatalf("an upload's answer must be JSON: %d %v %q", r.StatusCode, r.Header, body)
	}

	ra.handle("GET /v1/servers/rstuvwxyzq", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"Please sign in.","code":"unauthorized"}`)
	})
	if got := e.do(t, "GET", "/api/servers/rstuvwxyzq", "", auth(cookie, "")); got.status != http.StatusBadGateway {
		t.Fatalf("a machine can't end the dashboard session: %d %v", got.status, got.body)
	}
	ra.handle("POST /v1/servers/rstuvwxyzq/start", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
	})
	if got := e.do(t, "POST", "/api/servers/rstuvwxyzq/start", `{}`, auth(cookie, csrf)); got.status != http.StatusBadGateway || got.body["code"] != machinelink.CodeProtocol {
		t.Fatalf("an answer no agent gives: %d %v", got.status, got.body)
	}
}

func TestBackupFileNames(t *testing.T) {
	const bid = "20260924-120000-abcdef"
	plain := "playkeeper-" + bid + ".tar.gz"
	for _, tc := range []struct{ disposition, want string }{
		{`attachment; filename="playkeeper-Cobblemon-` + bid + `.tar.gz"`, "playkeeper-Cobblemon-" + bid + ".tar.gz"},
		{`attachment; filename="playkeeper-my_world-2-` + bid + `.tar.gz"`, "playkeeper-my_world-2-" + bid + ".tar.gz"},
		{"", plain},
		{"inline", plain},
		{`attachment; filename="index.html"`, plain},
		{`attachment; filename="playkeeper-Cobblemon-20260101-000000-000000.tar.gz"`, plain},
		{`attachment; filename="playkeeper--` + bid + `.tar.gz"`, plain},
		{`attachment; filename="playkeeper-a\"b-` + bid + `.tar.gz"`, plain},
		{`attachment; filename="playkeeper-../x-` + bid + `.tar.gz"`, plain},
		{`attachment; filename*=UTF-8''playkeeper-%3Cb%3E-` + bid + `.tar.gz`, plain},
		{`attachment; filename="playkeeper-` + strings.Repeat("w", 65) + `-` + bid + `.tar.gz"`, plain},
		{`attachment; filename="playkeeper-Cobblemon-` + bid + `.tar.gz.html"`, plain},
	} {
		if got := backupFileName(bid, tc.disposition); got != tc.want {
			t.Errorf("%s: %s", tc.disposition, got)
		}
	}
}
