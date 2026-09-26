package panel

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// otherServer is a second server, for accounts that may use only one.
const otherServer = "bcdefghjkm"

const bothServers = `[{"id":"abcdefghjk","name":"Survival","phase":"online"},{"id":"bcdefghjkm","name":"Creative","phase":"stopped"}]`

// setFactor turns name's two-factor sign-in on as set up at the given
// time (Unix milliseconds), or off with 0.
func setFactor(t *testing.T, e *env, name string, at int64) {
	t.Helper()
	var err error
	if at == 0 {
		_, err = e.srv.db.Exec(`DELETE FROM user_factors WHERE user_id = (SELECT id FROM users WHERE username = ?)`, name)
	} else {
		_, err = e.srv.db.Exec(`INSERT INTO user_factors(user_id, kind, secret, created_at, confirmed_at, revision) SELECT id, 'totp', 'JBSWY3DPEHPK3PXP', ?, ?, 1 FROM users WHERE username = ?
			ON CONFLICT(user_id, kind) DO UPDATE SET confirmed_at = excluded.confirmed_at`, at, at, name)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// member is a signed-in team member.
type member struct {
	id           int64
	cookie, csrf string
}

func (m member) auth() map[string]string { return auth(m.cookie, m.csrf) }

func (m member) path() string { return "/api/team/members/" + strconv.FormatInt(m.id, 10) }

// addMember puts an account on the team with a role and servers ("*" for
// all of them) and signs it in.
func addMember(t *testing.T, e *env, name, role, servers string) member {
	t.Helper()
	h, _ := hashPassword("member password 1")
	res, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES(?, ?, 0, 0, 'member')`, name, h)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := e.srv.db.Exec(`INSERT INTO project_members(project_id, user_id, role, servers, created_at) SELECT id, ?, ?, ?, ? FROM projects LIMIT 1`,
		id, role, servers, e.clock.now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return signIn(t, e, id)
}

// addAdmin adds an admin whose two-factor sign-in is on and confirmed.
func addAdmin(t *testing.T, e *env, name, servers string) member {
	t.Helper()
	m := addMember(t, e, name, invites.RoleAdmin, servers)
	at := e.clock.now().UnixMilli()
	setFactor(t, e, name, at)
	if _, err := e.srv.db.Exec(`UPDATE project_members SET admin_factor = ?, factor_seen = ? WHERE user_id = ?`, at, at, m.id); err != nil {
		t.Fatal(err)
	}
	return m
}

// signIn starts a session for an account without going through the
// sign-in page and its limits.
func signIn(t *testing.T, e *env, id int64) member {
	t.Helper()
	var u user
	if err := e.srv.db.QueryRow(`SELECT id, username, role FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Username, &u.Role); err != nil {
		t.Fatal(err)
	}
	token, sess, err := e.srv.newSession(u)
	if err != nil {
		t.Fatal(err)
	}
	return member{id: id, cookie: token, csrf: sess.CSRF}
}

// owner is the account e.setup made.
func owner(t *testing.T, e *env) member {
	t.Helper()
	cookie, csrf := e.setup(t)
	var id int64
	if err := e.srv.db.QueryRow(`SELECT id FROM users WHERE role = 'owner'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return member{id: id, cookie: cookie, csrf: csrf}
}

func machineID(t *testing.T, e *env) string {
	t.Helper()
	var id string
	if err := e.srv.db.QueryRow(`SELECT id FROM machines LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) agentHits() []string {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	return append([]string(nil), e.agent.hits...)
}

// agentBody is the body of the last request the agent saw for key.
func (e *env) agentBody(key string) string {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	for i := len(e.agent.hits) - 1; i >= 0; i-- {
		if e.agent.hits[i] == key {
			return e.agent.bodies[i]
		}
	}
	return ""
}

// agentBodies are the bodies of every request the agent saw for key, oldest
// first.
func (e *env) agentBodies(key string) []string {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	var out []string
	for i, h := range e.agent.hits {
		if h == key {
			out = append(out, e.agent.bodies[i])
		}
	}
	return out
}

// waitForHit waits for a request the panel sends in the background.
func (e *env) waitForHit(t *testing.T, key string) string {
	t.Helper()
	for range 200 {
		if b := e.agentBody(key); b != "" {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the agent never saw %s: %v", key, e.agentHits())
	return ""
}

func (e *env) auditRows(t *testing.T, action string) []string {
	t.Helper()
	rows, err := e.srv.db.Query(`SELECT actor || ' ' || target || ' ' || result || ' ' || detail FROM audit WHERE action = ? ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

// A team member who may use one server can't reach another through any
// route that names a server, whatever their role (H1).
func TestEveryServerRouteChecksTheServer(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	accounts := map[string]member{
		invites.RoleViewer:    addMember(t, e, "vic", invites.RoleViewer, otherServer),
		invites.RoleModerator: addMember(t, e, "mo", invites.RoleModerator, otherServer),
		invites.RoleAdmin:     addAdmin(t, e, "ada", otherServer),
	}
	for role, m := range accounts {
		checked := 0
		for _, rt := range e.srv.Routes() {
			if !strings.Contains(rt.Pattern, "{id}") {
				continue
			}
			checked++
			e.clock.add(2 * time.Second)
			r := e.do(t, rt.Method, samplePath(rt.Pattern), `{}`, m.auth())
			if r.status != http.StatusForbidden || r.body["error"] != errNoServer.Msg {
				t.Errorf("%s: %s %s on a server outside theirs: got %d %v, want 403", role, rt.Method, rt.Pattern, r.status, r.body)
			}
		}
		if checked < 30 {
			t.Fatalf("only %d routes name a server", checked)
		}
	}
	if hits := e.agentHits(); len(hits) != 0 {
		t.Fatalf("refused requests reached the agent: %v", hits)
	}
	mod := accounts[invites.RoleModerator]
	for _, p := range []string{"/api/servers/" + otherServer, "/api/servers/" + otherServer + "/logs"} {
		if r := e.do(t, "GET", p, "", mod.auth()); r.status != http.StatusOK {
			t.Errorf("their own server, %s: %d %v", p, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", "/api/servers/"+otherServer+"/restart", `{}`, mod.auth()); r.status != http.StatusOK {
		t.Errorf("a moderator restarts their own server: %d %v", r.status, r.body)
	}
}

// Actions on the whole machine need the owner, or an admin of every server
// whose two-factor sign-in is on and confirmed (H1).
func TestMachineWideActionsNeedEveryServer(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	mid := machineID(t, e)
	wide := [][2]string{
		{"POST", "/api/machines/" + mid + "/servers"},
		{"POST", "/api/machines/" + mid + "/update/check"},
		{"POST", "/api/machines/" + mid + "/update/apply"},
		{"POST", "/api/machines/" + mid + "/restore/upload"},
		{"POST", "/api/machines/" + mid + "/world-imports"},
		{"POST", "/api/machines/" + mid + "/world-imports/w234567890/create"},
		{"GET", "/api/audit"},
		{"GET", "/api/discord"},
		{"POST", "/api/discord/connect"},
		{"PUT", "/api/discord"},
		{"DELETE", "/api/discord"},
		{"POST", "/api/discord/test"},
		{"POST", "/api/servers/" + otherServer + "/delete"},
		{"GET", "/api/machines/" + mid + "/address/available"},
		{"POST", "/api/machines/" + mid + "/address/claim"},
		{"POST", "/api/machines/" + mid + "/address/refresh"},
		{"POST", "/api/machines/" + mid + "/address/release"},
		{"POST", "/api/machines/" + mid + "/address/check"},
		{"POST", "/api/machines/" + mid + "/address/certificate"},
		{"DELETE", "/api/machines/" + mid + "/address"},
		{"POST", "/api/machines/" + mid + "/disk/clean"},
		{"POST", "/api/machines/" + mid + "/offsite/recover"},
		{"POST", "/api/machines/" + mid + "/offsite/recover/restore"},
	}
	scoped := addAdmin(t, e, "ada", otherServer)
	unconfirmed := addMember(t, e, "una", invites.RoleAdmin, "*")
	full := addAdmin(t, e, "fay", "*")
	for _, c := range wide {
		e.clock.add(2 * time.Second)
		if r := e.do(t, c[0], c[1], `{}`, scoped.auth()); r.status != http.StatusForbidden || r.body["error"] != errAllServers.Msg {
			t.Errorf("an admin of one server, %s %s: %d %v", c[0], c[1], r.status, r.body)
		}
		if r := e.do(t, c[0], c[1], `{}`, unconfirmed.auth()); r.status != http.StatusForbidden || r.body["code"] != invites.CodeTwoFactorRequired {
			t.Errorf("an admin without two-factor, %s %s: %d %v", c[0], c[1], r.status, r.body)
		}
	}
	// Pages that show every server need every server to look at them.
	viewer := addMember(t, e, "vic", invites.RoleViewer, otherServer)
	for _, p := range []string{"/api/machines/" + mid + "/address", "/api/machines/" + mid + "/disk"} {
		for who, m := range map[string]member{"an admin": scoped, "a viewer": viewer} {
			if r := e.do(t, "GET", p, "", m.auth()); r.status != http.StatusForbidden || r.body["error"] != errEveryServer.Msg {
				t.Errorf("%s of one server, GET %s: %d %v", who, p, r.status, r.body)
			}
		}
	}
	if hits := e.agentHits(); len(hits) != 0 {
		t.Fatalf("refused requests reached the agent: %v", hits)
	}
	for _, c := range wide {
		e.clock.add(2 * time.Second)
		if r := e.do(t, c[0], c[1], `{}`, full.auth()); r.status == http.StatusForbidden || r.status == http.StatusUnauthorized {
			t.Errorf("an admin of every server with two-factor, %s %s: %d %v", c[0], c[1], r.status, r.body)
		}
	}
}

// Lists show only the servers an account may use, and what it may not see
// reads as not there (H1).
func TestListsShowOnlyTheAccountsServers(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	mid := machineID(t, e)
	mod := addMember(t, e, "mara", invites.RoleModerator, otherServer)
	e.reply("GET", "/v1/servers", bothServers)
	var servers []map[string]any
	if st := e.get(t, "/api/servers", mod.cookie, &servers); st != 200 || len(servers) != 1 || servers[0]["id"] != otherServer {
		t.Fatalf("servers: %d %v", st, servers)
	}
	e.reply("GET", "/v1/activity", `[{"ts":"2026-09-24T11:03:00Z","serverId":"abcdefghjk","kind":"join","player":"Steve"},
		{"ts":"2026-09-24T11:02:00Z","serverId":"bcdefghjkm","kind":"join","player":"PixelPia"},
		{"ts":"2026-09-24T11:01:00Z","kind":"update","detail":"0.4.0"}]`)
	var acts []map[string]any
	if st := e.get(t, "/api/machines/"+mid+"/activity", mod.cookie, &acts); st != 200 || len(acts) != 2 || acts[0]["actor"] != "mara" || acts[1]["player"] != "PixelPia" {
		t.Fatalf("activity: %d %v", st, acts)
	}
	for serverID, want := range map[string]int{sampleServer: http.StatusNotFound, "": http.StatusNotFound, otherServer: http.StatusOK} {
		e.reply("GET", "/v1/operations/0123456789abcdef", `{"id":"0123456789abcdef","serverId":"`+serverID+`","kind":"backup","status":"running"}`)
		if r := e.do(t, "GET", "/api/machines/"+mid+"/operations/0123456789abcdef", "", mod.auth()); r.status != want {
			t.Errorf("an operation on %q: %d, want %d", serverID, r.status, want)
		}
	}
	e.reply("GET", "/v1/machine", `{"agentVersion":"0.4.0"}`)
	if r := e.do(t, "GET", "/api/server", "", mod.auth()); r.status != 200 || r.body["id"] != otherServer {
		t.Errorf("the old status route shows their first server: %d %v", r.status, r.body)
	}

	adm := addAdmin(t, e, "tobi", otherServer)
	restore := "/api/machines/" + mid + "/restore/0123456789abcdef"
	for serverID, want := range map[string]string{sampleServer: errNoServer.Msg, "": errAllServers.Msg} {
		e.reply("GET", "/v1/restore/0123456789abcdef", `{"id":"0123456789abcdef","serverId":"`+serverID+`"}`)
		for _, c := range [][2]string{{"GET", restore}, {"POST", restore + "/apply"}, {"DELETE", restore}} {
			if r := e.do(t, c[0], c[1], `{}`, adm.auth()); r.status != http.StatusForbidden || r.body["error"] != want {
				t.Errorf("a restore into %q, %s %s: %d %v", serverID, c[0], c[1], r.status, r.body)
			}
		}
	}
	if contains(e.agentHits(), "POST /v1/restore/0123456789abcdef/apply") {
		t.Fatal("a refused restore reached the agent")
	}
	e.reply("GET", "/v1/restore/0123456789abcdef", `{"id":"0123456789abcdef","serverId":"`+otherServer+`"}`)
	if r := e.do(t, "POST", restore+"/apply", `{}`, adm.auth()); r.status != http.StatusOK || !contains(e.agentHits(), "POST /v1/restore/0123456789abcdef/apply") {
		t.Errorf("a restore into their own server: %d %v", r.status, r.body)
	}
	var team teamBody
	if st := e.get(t, "/api/team", adm.cookie, &team); st != 200 || len(team.Servers) != 1 || team.Servers[0].Name != "Creative" {
		t.Errorf("the Team page offers their servers only: %d %+v", st, team.Servers)
	}
}

// Home's activity says when someone joined the team and as what. Who is on
// the team is for those who manage it, so a moderator sees only their own
// join.
func TestTeamJoinsShowInActivity(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	mid := machineID(t, e)
	mod := addMember(t, e, "mara", invites.RoleModerator, "*")
	e.clock.add(time.Minute)
	addMember(t, e, "tobi", invites.RoleViewer, otherServer)
	e.reply("GET", "/v1/activity", `[{"ts":"2026-09-24T11:03:00Z","serverId":"abcdefghjk","kind":"join","player":"Steve"}]`)

	var acts []api.Activity
	if st := e.get(t, "/api/machines/"+mid+"/activity", own.cookie, &acts); st != 200 || len(acts) != 3 ||
		acts[0] != (api.Activity{TS: e.clock.now(), Kind: api.ActivityTeamJoined, Actor: "tobi", Detail: invites.RoleViewer}) ||
		acts[1].Actor != "mara" || acts[1].Detail != invites.RoleModerator || acts[2].Player != "Steve" {
		t.Fatalf("the owner sees every join, newest first: %d %+v", st, acts)
	}
	if st := e.get(t, "/api/machines/"+mid+"/activity?limit=1", own.cookie, &acts); st != 200 || len(acts) != 1 || acts[0].Actor != "tobi" {
		t.Fatalf("the limit counts joins too: %d %+v", st, acts)
	}
	if st := e.get(t, "/api/machines/"+mid+"/activity", mod.cookie, &acts); st != 200 || len(acts) != 2 || acts[0].Actor != "mara" || acts[1].Player != "Steve" {
		t.Fatalf("a moderator sees only their own join: %d %+v", st, acts)
	}
}

// Removing a member deletes their account and its sessions, an account left
// without a membership gets nothing, and a restart gives nobody back their
// rights (H5).
func TestRemovedMembersStayRemovedAfterARestart(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	mod := addMember(t, e, "mara", invites.RoleModerator, "*")
	if r := e.do(t, "DELETE", mod.path(), "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("remove: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/servers/"+sampleServer, "", mod.auth()); r.status != http.StatusUnauthorized {
		t.Fatalf("a removed member's session: %d", r.status)
	}
	var n int
	e.srv.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id = ?`, mod.id).Scan(&n)
	if n != 0 {
		t.Fatal("the removed member's account is still there")
	}
	if rows := e.auditRows(t, "team.remove"); len(rows) != 1 || !strings.HasPrefix(rows[0], "admin mara succeeded") {
		t.Fatalf("audit: %v", rows)
	}
	// An account without a membership, as an older build left removed
	// members, and a membership whose servers were never set.
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('tobi', ?, 0, 0, 'member'), ('lee', ?, 0, 0, 'member')`, h, h); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.db.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) SELECT p.id, u.id, 'admin', 0 FROM projects p, users u WHERE u.username = 'lee'`); err != nil {
		t.Fatal(err)
	}

	s2, err := New(Options{Config: e.cfg, Now: e.clock.now, Agent: e.srv.agent, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	ts2 := httptest.NewTLSServer(s2.Handler())
	t.Cleanup(ts2.Close)
	e2 := &env{srv: s2, ts: ts2, clock: e.clock, agent: e.agent, cfg: e.cfg}
	var role string
	if err := s2.db.QueryRow(`SELECT COALESCE((SELECT m.role FROM project_members m JOIN users u ON u.id = m.user_id WHERE u.username = 'tobi'), '')`).Scan(&role); err != nil || role != "" {
		t.Fatalf("a restart gave an account without a membership the role %q (%v)", role, err)
	}
	e.reply("GET", "/v1/servers", bothServers)
	for _, name := range []string{"tobi", "lee"} {
		var id int64
		s2.db.QueryRow(`SELECT id FROM users WHERE username = ?`, name).Scan(&id)
		m := signIn(t, e2, id)
		if r := e2.do(t, "GET", "/api/servers/"+sampleServer, "", m.auth()); r.status != http.StatusForbidden {
			t.Errorf("%s may use a server: %d %v", name, r.status, r.body)
		}
		var servers []map[string]any
		if st := e2.get(t, "/api/servers", m.cookie, &servers); st != 200 && st != 403 || len(servers) != 0 {
			t.Errorf("%s sees servers: %d %v", name, st, servers)
		}
	}
	if r := e2.do(t, "GET", "/api/team", "", auth(own.cookie, "")); r.status != http.StatusOK {
		t.Fatalf("the owner keeps every right after a restart: %d %v", r.status, r.body)
	}
}

// Nothing can make a second owner, not even an account row that leaves the
// role out (M5).
func TestThereIsOnlyEverOneOwner(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at) VALUES('eve', 'x', 0, 0)`); err == nil {
		t.Fatal("an account without a role became a second owner")
	}
	m := addMember(t, e, "mara", invites.RoleModerator, "*")
	if _, err := e.srv.db.Exec(`UPDATE users SET role = 'owner' WHERE id = ?`, m.id); err == nil {
		t.Fatal("a member became a second owner")
	}
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('MARA', 'x', 0, 0, 'member')`); err == nil {
		t.Fatal("two accounts differ only in case")
	}
}

// Who may change whom on the Team page (M6): nobody changes themselves or
// the owner; an admin changes only members below admin whose servers are
// all theirs, and only as an invite would allow.
func TestTeamChangesFollowTheRules(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	e.reply("GET", "/v1/servers", bothServers)
	adm := addAdmin(t, e, "ada", otherServer)
	pia := addMember(t, e, "pia", invites.RoleModerator, otherServer)
	sam := addMember(t, e, "sam", invites.RoleModerator, "*")
	lee := addAdmin(t, e, "lee", "*")
	creative := `{"servers":["bcdefghjkm"]}`
	cases := []struct {
		who    member
		method string
		target member
		body   string
		status int
		code   string
	}{
		{adm, "PUT", pia, `{"role":"admin","servers":` + creative + `}`, 403, invites.CodeRoleNotAllowed},
		{adm, "PUT", pia, `{"role":"viewer","servers":{"all":true}}`, 403, invites.CodeServersNotAllowed},
		{adm, "PUT", sam, `{"role":"viewer","servers":` + creative + `}`, 403, invites.CodeServersNotAllowed},
		{adm, "PUT", lee, `{"role":"viewer","servers":` + creative + `}`, 403, invites.CodeMemberNotAllowed},
		{adm, "PUT", adm, `{"role":"admin","servers":{"all":true}}`, 403, invites.CodeNotYourself},
		{adm, "PUT", own, `{"role":"viewer","servers":` + creative + `}`, 403, invites.CodeOwnerFixed},
		{adm, "DELETE", sam, ``, 403, invites.CodeServersNotAllowed},
		{adm, "DELETE", lee, ``, 403, invites.CodeMemberNotAllowed},
		{adm, "DELETE", own, ``, 403, invites.CodeOwnerFixed},
		{own, "DELETE", own, ``, 403, invites.CodeOwnerFixed},
		{own, "PUT", pia, `{"role":"viewer","servers":{"servers":["zzzzzzzzzz"]}}`, 400, invites.CodeBadOptions},
		{adm, "PUT", pia, `{"role":"viewer","servers":` + creative + `}`, 200, ""},
		{own, "PUT", lee, `{"role":"moderator","servers":{"all":true}}`, 200, ""},
	}
	for i, c := range cases {
		e.clock.add(2 * time.Second)
		r := e.do(t, c.method, c.target.path(), c.body, c.who.auth())
		if r.status != c.status || c.code != "" && r.body["code"] != c.code {
			t.Errorf("case %d, %s %s: got %d %v, want %d %s", i, c.method, c.target.path(), r.status, r.body, c.status, c.code)
		}
	}
	var team teamBody
	if st := e.get(t, "/api/team", adm.cookie, &team); st != 200 || len(team.Members) != 5 || team.Members[0].Username != "admin" || !team.Members[0].Owner {
		t.Fatalf("team, owner first: %d %+v", st, team.Members)
	}
	for _, m := range team.Members {
		if m.CanEdit != (m.Username == "pia") {
			t.Errorf("an admin of one server may change %s: %v", m.Username, m.CanEdit)
		}
	}
	if r := e.do(t, "DELETE", pia.path(), "", adm.auth()); r.status != http.StatusNoContent {
		t.Fatalf("an admin removes a moderator of their server: %d %v", r.status, r.body)
	}
}

// Turning on two-factor sign-in doesn't make an admin an Admin: the owner or
// an admin confirms it with one click, the owner hears about it, and it is
// in the audit trail. Turned off and on again, it needs confirming again
// (M1, owner decision).
func TestAdminRightsWaitForConfirmation(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	e.reply("GET", "/v1/servers", bothServers)
	mara := addMember(t, e, "mara", invites.RoleAdmin, "*")
	me := func() accessBody {
		t.Helper()
		var body struct {
			Access accessBody `json:"access"`
		}
		if st := e.get(t, "/api/auth/me", mara.cookie, &body); st != 200 {
			t.Fatalf("me: %d", st)
		}
		return body.Access
	}
	teamStatus := func() (int, string) {
		t.Helper()
		e.clock.add(2 * time.Second)
		r := e.do(t, "GET", "/api/team", "", mara.auth())
		code, _ := r.body["code"].(string)
		return r.status, code
	}
	if a := me(); !a.NeedsTwoFactor || a.AwaitingConfirmation || contains(actionNames(a.Can), string(actManageTeam)) || !contains(actionNames(a.Can), string(actRunServers)) {
		t.Fatalf("an admin without two-factor has Moderator rights: %+v", a)
	}
	if st, code := teamStatus(); st != 403 || code != invites.CodeTwoFactorRequired {
		t.Fatalf("before two-factor: %d %s", st, code)
	}
	// A setup that was started but never confirmed doesn't count.
	if _, err := e.srv.db.Exec(`INSERT INTO user_factors(user_id, kind, secret, created_at, confirmed_at, revision) VALUES(?, 'totp', 'JBSWY3DPEHPK3PXP', 0, NULL, 1)`, mara.id); err != nil {
		t.Fatal(err)
	}
	if st, code := teamStatus(); st != 403 || code != invites.CodeTwoFactorRequired {
		t.Fatalf("an unconfirmed setup: %d %s", st, code)
	}

	setFactor(t, e, "mara", 1000)
	if st, code := teamStatus(); st != 403 || code != api.CodeAdminUnconfirmed {
		t.Fatalf("two-factor on, not confirmed: %d %s", st, code)
	}
	if a := me(); a.NeedsTwoFactor || !a.AwaitingConfirmation || !a.TwoFactor || contains(actionNames(a.Can), string(actManageTeam)) {
		t.Fatalf("waiting for confirmation: %+v", a)
	}
	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/restart", `{}`, mara.auth()); r.status != 200 {
		t.Fatalf("Moderator rights meanwhile: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", mara.path()+"/confirm-admin", `{}`, mara.auth()); r.status != 403 {
		t.Fatalf("an admin confirms themselves: %d %v", r.status, r.body)
	}
	if rows := e.auditRows(t, "team.two_factor"); len(rows) != 1 || !strings.Contains(rows[0], "turned on; Admin rights wait") {
		t.Fatalf("the change is audited once: %v", rows)
	}
	body := e.waitForHit(t, "POST /v1/discord/notify")
	if !strings.Contains(body, `"kind":"two_factor_changed"`) || !strings.Contains(body, `"member":"mara"`) || !strings.Contains(body, `"on":true`) || !strings.Contains(body, `"admin":true`) {
		t.Fatalf("the owner hears about it on Discord: %s", body)
	}

	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	var row teamMember
	for _, m := range team.Members {
		if m.Username == "mara" {
			row = m
		}
	}
	if !row.Waiting || !row.CanConfirm || !row.TwoFactor {
		t.Fatalf("the Team page shows the confirmation to the owner: %+v", row)
	}
	r := e.do(t, "POST", mara.path()+"/confirm-admin", `{}`, own.auth())
	if r.status != 200 || r.body["waiting"] != nil || r.body["twoFactor"] != true {
		t.Fatalf("confirm: %d %v", r.status, r.body)
	}
	if st, _ := teamStatus(); st != 200 {
		t.Fatalf("confirmed: %d", st)
	}
	if r := e.do(t, "POST", mara.path()+"/confirm-admin", `{}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("nothing left to confirm: %d", r.status)
	}
	if rows := e.auditRows(t, "team.confirm_admin"); len(rows) != 1 || !strings.HasPrefix(rows[0], "admin mara succeeded") {
		t.Fatalf("the confirmation is audited: %v", rows)
	}

	setFactor(t, e, "mara", 0)
	if st, code := teamStatus(); st != 403 || code != invites.CodeTwoFactorRequired {
		t.Fatalf("two-factor turned off: %d %s", st, code)
	}
	setFactor(t, e, "mara", 2000)
	if st, code := teamStatus(); st != 403 || code != api.CodeAdminUnconfirmed {
		t.Fatalf("a new setup needs confirming again: %d %s", st, code)
	}
	if rows := e.auditRows(t, "team.two_factor"); len(rows) != 3 || !strings.Contains(rows[1], "turned off") {
		t.Fatalf("each change is audited: %v", rows)
	}

	// An admin may confirm only admins whose servers are all theirs.
	ada := addAdmin(t, e, "ada", otherServer)
	if r := e.do(t, "POST", mara.path()+"/confirm-admin", `{}`, ada.auth()); r.status != 403 {
		t.Fatalf("an admin of one server confirms an admin of all: %d %v", r.status, r.body)
	}
	fay := addAdmin(t, e, "fay", "*")
	if r := e.do(t, "POST", mara.path()+"/confirm-admin", `{}`, fay.auth()); r.status != 200 {
		t.Fatalf("an admin of every server confirms: %d %v", r.status, r.body)
	}
	// The owner hears on Discord when an admin confirms someone, and not
	// about their own confirmations.
	var confirmed []string
	for range 200 {
		confirmed = confirmed[:0]
		for _, b := range e.agentBodies("POST /v1/discord/notify") {
			if strings.Contains(b, `"kind":"admin_confirmed"`) {
				confirmed = append(confirmed, b)
			}
		}
		if len(confirmed) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(confirmed) != 1 || !strings.Contains(confirmed[0], `"member":"mara"`) || !strings.Contains(confirmed[0], `"actor":"fay"`) {
		t.Fatalf("Discord hears of the admin's confirmation only: %v", confirmed)
	}

	// Making someone an admin is the owner's decision, so it confirms the
	// two-factor sign-in they have on.
	sam := addMember(t, e, "sam", invites.RoleModerator, "*")
	setFactor(t, e, "sam", 3000)
	if r := e.do(t, "PUT", sam.path(), `{"role":"admin","servers":{"all":true}}`, own.auth()); r.status != 200 || r.body["waiting"] != nil {
		t.Fatalf("the owner makes a member with two-factor an admin: %d %v", r.status, r.body)
	}
	if r := e.do(t, "GET", "/api/team", "", sam.auth()); r.status != 200 {
		t.Fatalf("promoted by the owner: %d %v", r.status, r.body)
	}
}

// A change of role or servers turns off the links the member can no longer
// make with the rights they have after it, and only those. Made a
// Moderator, an admin's team link goes and their friend link stays; left
// with Creative only, the friend link for Survival goes too. An admin who
// set up two-factor sign-in again keeps their team link when the owner makes
// them an Admin again, which confirms the new setup.
func TestRoleChangesTurnOffOnlyTheLinksTheNewRightsForbid(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	e.reply("GET", "/v1/servers", bothServers)
	open := func(id string) bool {
		t.Helper()
		var at int64
		if err := e.srv.db.QueryRow(`SELECT revoked_at FROM invites WHERE id = ?`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at == 0
	}
	teamLink := func(m member) string {
		t.Helper()
		r := e.do(t, "POST", "/api/team/invites", `{"role":"viewer","servers":{"all":true}}`, m.auth())
		inv, _ := r.body["invite"].(map[string]any)
		id, _ := inv["id"].(string)
		if r.status != http.StatusCreated || id == "" {
			t.Fatalf("team link: %d %v", r.status, r.body)
		}
		return id
	}
	edit := func(m member, body string) {
		t.Helper()
		e.clock.add(2 * time.Second)
		if r := e.do(t, "PUT", m.path(), body, own.auth()); r.status != http.StatusOK {
			t.Fatalf("edit %s: %d %v", body, r.status, r.body)
		}
	}

	ada := addAdmin(t, e.env, "ada", "*")
	friend, _ := friendInvite(t, e.env, ada, `{"label":"","expiry":"7d","maxUses":5,"approval":"right_away"}`)
	team := teamLink(ada)
	edit(ada, `{"role":"moderator","servers":{"all":true}}`)
	if open(team) || !open(friend) {
		t.Fatalf("made a Moderator, the team link must go and the friend link stay: team link open %v, friend link open %v", open(team), open(friend))
	}
	edit(ada, `{"role":"moderator","servers":{"servers":["bcdefghjkm"]}}`)
	if open(friend) {
		t.Fatal("left with Creative only, the friend link for Survival must go")
	}

	lee := addAdmin(t, e.env, "lee", "*")
	kept := teamLink(lee)
	setFactor(t, e.env, "lee", e.clock.now().UnixMilli()+1)
	edit(lee, `{"role":"admin","servers":{"all":true}}`)
	if !open(kept) {
		t.Fatal("made an Admin again by the owner, which confirms the new two-factor setup, lee keeps the team link")
	}
	if rows := e.auditRows(t, "invite.revoke"); len(rows) != 2 || !strings.Contains(rows[0], "creator's role changed") {
		t.Fatalf("each link turned off is audited with the reason: %v", rows)
	}
}

func actionNames(list []action) []string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = string(a)
	}
	return out
}

// loginFrom signs in through the handler from the given address.
func (e *env) loginFrom(t *testing.T, addr, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "https://panel.example/api/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.RemoteAddr = net.JoinHostPort(addr, "40000")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://panel.example")
	req.Header.Set("X-Requested-With", "playkeeper")
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Someone who knows the owner's username can lock them out only where they
// are themselves (M4): the review's attack, a wrong password each time the
// lock runs out for a day, never keeps the owner out elsewhere.
func TestFailedSignInsLockOnlyTheirOwnAddress(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	const attacker, home = "203.0.113.9", "198.51.100.20"
	next := e.clock.now()
	tries, owner := 0, 0
	for minute := 0; minute < 24*60; minute++ {
		if !e.clock.now().Before(next) {
			rec := e.loginFrom(t, attacker, "admin", "not the password")
			switch rec.Code {
			case http.StatusUnauthorized:
				tries++
			case http.StatusTooManyRequests:
				wait, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
				next = e.clock.now().Add(time.Duration(wait) * time.Second)
			default:
				t.Fatalf("attacker: %d %s", rec.Code, rec.Body)
			}
		}
		if minute%15 == 0 {
			if rec := e.loginFrom(t, home, "admin", "correct horse battery"); rec.Code != http.StatusOK {
				t.Fatalf("minute %d: the owner is kept out after %d wrong passwords: %d %s", minute, tries, rec.Code, rec.Body)
			}
			owner++
		}
		e.clock.add(time.Minute)
	}
	if tries < 50 || owner != 96 {
		t.Fatalf("%d wrong passwords, %d sign-ins", tries, owner)
	}
	e.loginFrom(t, attacker, "admin", "not the password")
	if rec := e.loginFrom(t, attacker, "admin", "correct horse battery"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the address that guesses is locked: %d", rec.Code)
	}
	// An IPv6 household is one address.
	for range 5 {
		e.loginFrom(t, "2001:db8:1:2::1", "admin", "not the password")
	}
	if rec := e.loginFrom(t, "2001:db8:1:2::99", "admin", "correct horse battery"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the same /64: %d", rec.Code)
	}
	if rec := e.loginFrom(t, "2001:db8:1:3::1", "admin", "correct horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("another /64: %d %s", rec.Code, rec.Body)
	}
}

// Guesses spread over many addresses still meet a slower limit per account,
// which fills up again in minutes and leaves other accounts alone (M4).
func TestGuessesFromManyAddressesAreSlowedPerAccount(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	addMember(t, e, "mara", invites.RoleModerator, "*")
	for i := 1; i <= 30; i++ {
		if rec := e.loginFrom(t, "203.0.113."+strconv.Itoa(i), "admin", "not the password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, rec.Code)
		}
	}
	rec := e.loginFrom(t, "198.51.100.20", "admin", "correct horse battery")
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after 30 guesses in a minute: %d", rec.Code)
	}
	if rec := e.loginFrom(t, "198.51.100.21", "mara", "member password 1"); rec.Code != http.StatusOK {
		t.Fatalf("another account: %d %s", rec.Code, rec.Body)
	}
	e.clock.add(3 * time.Minute)
	if rec := e.loginFrom(t, "198.51.100.20", "admin", "correct horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("minutes later: %d %s", rec.Code, rec.Body)
	}
}
