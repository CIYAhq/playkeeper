package panel

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

const creatorInviteBody = `{"role":"admin","servers":{},"label":"alex","allowance":{"servers":1,"memoryMB":4096}}`

// creatorDialogDefaults is what Invite a creator sends with its defaults, as
// the dashboard's test of the dialog pins it: no name, 1 server and 4 GB.
const creatorDialogDefaults = `{"role":"admin","servers":{},"allowance":{"servers":1,"memoryMB":4096}}`

func TestTheCreatorDialogsDefaultsMakeAnInvite(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	r := e.do(t, "POST", "/api/team/invites", creatorDialogDefaults, own.auth())
	inv, _ := r.body["invite"].(map[string]any)
	allowance, _ := inv["allowance"].(map[string]any)
	if r.status != http.StatusCreated || inv["role"] != invites.RoleAdmin || allowance["servers"] != float64(1) || allowance["memoryMB"] != float64(4096) {
		t.Fatalf("Invite a creator with its defaults: %d %v", r.status, r.body)
	}
}

// A creator invite (the managed beta's) makes an Admin with no servers and
// an allowance. Only the owner can make or turn one off, and nobody else on
// the team sees it. The account starts with no servers.
func TestCreatorInvitesAreTheOwnersAlone(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	lena := addAdmin(t, e.env, "lena", "*")
	for _, tc := range []struct {
		name, body string
		who        member
		status     int
	}{
		{"an admin of every server", creatorInviteBody, lena, http.StatusForbidden},
		{"with a server", `{"role":"admin","servers":{"servers":["abcdefghjk"]},"allowance":{"servers":1,"memoryMB":4096}}`, own, http.StatusBadRequest},
		{"as a moderator", `{"role":"moderator","servers":{},"allowance":{"servers":1,"memoryMB":4096}}`, own, http.StatusBadRequest},
		{"with too much memory", `{"role":"admin","servers":{},"allowance":{"servers":1,"memoryMB":131072}}`, own, http.StatusBadRequest},
	} {
		if r := e.do(t, "POST", "/api/team/invites", tc.body, tc.who.auth()); r.status != tc.status {
			t.Fatalf("a creator invite %s: %d %v", tc.name, r.status, r.body)
		}
	}

	r := e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth())
	code, _ := strings.CutPrefix(r.body["path"].(string), invites.JoinPath+"/")
	if r.status != http.StatusCreated || !invites.WellFormed(code) {
		t.Fatalf("the owner's creator invite: %d %v", r.status, r.body)
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	if len(team.Invites) != 1 || team.Invites[0].Allowance != (invites.Allowance{Servers: 1, MemoryMB: 4096}) || !team.Invites[0].CanEdit {
		t.Fatalf("the owner's Team page: %+v", team.Invites)
	}
	var lenas teamBody
	e.get(t, "/api/team", lena.cookie, &lenas)
	if len(lenas.Invites) != 0 {
		t.Fatalf("an admin of every server sees the creator invite: %+v", lenas.Invites)
	}
	id := team.Invites[0].ID
	if r := e.do(t, "PUT", "/api/team/invites/"+id, `{"role":"admin","servers":{}}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("changing a creator invite: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/team/invites/"+id, "", lena.auth()); r.status != http.StatusForbidden {
		t.Fatalf("an admin turns off the owner's creator invite: %d %v", r.status, r.body)
	}

	r = e.public(t, "preview", codeBody(code))
	allowance, _ := r.body["allowance"].(map[string]any)
	if names, _ := r.body["serverNames"].([]any); r.status != 200 || r.body["role"] != invites.RoleAdmin || len(names) != 0 || allowance["servers"] != float64(1) || allowance["memoryMB"] != float64(4096) {
		t.Fatalf("preview: %d %v", r.status, r.body)
	}
	r = e.public(t, "accept", codeBody(code, "username", "alex", "password", "member password 1"))
	if requires, _ := r.body["requires"].([]any); r.status != 200 || len(requires) != 1 {
		t.Fatalf("accept: %d %v", r.status, r.body)
	}
	var role, servers string
	var allowServers, allowMemory int
	e.srv.db.QueryRow(`SELECT m.role, m.servers, m.allowance_servers, m.allowance_memory_mb FROM users u JOIN project_members m ON m.user_id = u.id WHERE u.username = 'alex'`).
		Scan(&role, &servers, &allowServers, &allowMemory)
	if role != invites.RoleAdmin || servers != "" || allowServers != 1 || allowMemory != 4096 {
		t.Fatalf("alex stored as %s of %q with %d servers and %d MB", role, servers, allowServers, allowMemory)
	}
	if rows := e.auditRows(t, "invite.accept"); len(rows) != 1 || !strings.Contains(rows[0], "creator: up to 1 server with 4096 MB") {
		t.Fatalf("audit: %v", rows)
	}

	var alexID int64
	e.srv.db.QueryRow(`SELECT id FROM users WHERE username = 'alex'`).Scan(&alexID)
	at := e.clock.now().UnixMilli()
	setFactor(t, e.env, "alex", at)
	if _, err := e.srv.db.Exec(`UPDATE project_members SET admin_factor = ?, factor_seen = ? WHERE user_id = ?`, at, at, alexID); err != nil {
		t.Fatal(err)
	}
	alex := signIn(t, e.env, alexID)
	var list []map[string]any
	if st := e.get(t, "/api/servers", alex.cookie, &list); st != 200 || len(list) != 0 {
		t.Fatalf("alex's servers: %d %v", st, list)
	}
	if st := e.do(t, "GET", "/api/servers/"+sampleServer, "", alex.auth()).status; st != http.StatusForbidden {
		t.Fatalf("alex uses the owner's server: %d", st)
	}
	if r := e.do(t, "PUT", "/api/team/members/"+strconv.FormatInt(alexID, 10), `{"role":"admin","servers":{"servers":["abcdefghjk"]}}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("the owner changes a creator's servers: %d %v", r.status, r.body)
	}
	var alexs teamBody
	e.get(t, "/api/team", alex.cookie, &alexs)
	if len(alexs.Invites) != 0 || len(alexs.Members) != 2 {
		t.Fatalf("alex's Team page: %+v %+v", alexs.Members, alexs.Invites)
	}
	for _, m := range alexs.Members {
		if m.Username != "alex" && !m.Owner {
			t.Fatalf("alex sees %s", m.Username)
		}
	}
	e.get(t, "/api/team", own.cookie, &team)
	for _, m := range team.Members {
		if m.Username == "alex" && m.Allowance != (invites.Allowance{Servers: 1, MemoryMB: 4096}) {
			t.Fatalf("the owner's Team page shows alex as %+v", m)
		}
	}

	r = e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth())
	e.get(t, "/api/team", own.cookie, &team)
	if r.status != http.StatusCreated || len(team.Invites) != 1 {
		t.Fatalf("a second creator invite: %d %+v", r.status, team.Invites)
	}
	if r := e.do(t, "DELETE", "/api/team/invites/"+team.Invites[0].ID, "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("the owner turns off a creator invite: %d %v", r.status, r.body)
	}
}
