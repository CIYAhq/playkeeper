package panel

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// A creator's servers must not reach the machine: making a creator invite
// turns Keep servers away from this machine on first, and makes no invite
// when that doesn't work.
func TestACreatorInviteKeepsServersAwayFromTheMachine(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	if r := e.do(t, "POST", "/api/team/invites", `{"role":"moderator","servers":{"all":true},"label":"sam"}`, own.auth()); r.status != http.StatusCreated {
		t.Fatalf("a team invite: %d %v", r.status, r.body)
	}
	if body := e.agentBody("POST /v1/network-guard"); body != "" {
		t.Fatalf("a team invite changed the machine: %s", body)
	}
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth()); r.status != http.StatusCreated {
		t.Fatalf("a creator invite: %d %v", r.status, r.body)
	}
	if body := e.agentBody("POST /v1/network-guard"); !strings.Contains(body, `"host":true`) {
		t.Fatalf("the agent was told %s", body)
	}
	for name, reply := range map[string]struct {
		status int
		body   string
	}{
		"the agent fails":         {http.StatusInternalServerError, `{"error":"no","code":"internal"}`},
		"the agent leaves it off": {http.StatusOK, `{"on":true,"host":false}`},
	} {
		e.agent.mu.Lock()
		e.agent.statuses["POST /v1/network-guard"], e.agent.replies["POST /v1/network-guard"] = reply.status, reply.body
		e.agent.mu.Unlock()
		e.clock.add(2 * time.Second)
		r := e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth())
		if msg, _ := r.body["error"].(string); r.status != http.StatusBadGateway || !strings.Contains(msg, "made no creator invite") {
			t.Fatalf("%s: %d %v", name, r.status, r.body)
		}
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	if len(team.Invites) != 2 {
		t.Fatalf("invites made while servers weren't kept away: %+v", team.Invites)
	}
}

// Servers stay away from the dashboard's own machine while it has
// creators, or a creator invite that still works.
func TestServersStayAwayWhileThereAreCreators(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	path := "/api/machines/" + machineID(t, e.env) + "/network-guard"
	if r := e.do(t, "POST", path, `{"host":false}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("turning it off with no creators: %d %v", r.status, r.body)
	}
	if body := e.agentBody("POST /v1/network-guard"); !strings.Contains(body, `"host":false`) || !strings.Contains(body, `"actor":"`) {
		t.Fatalf("the agent was told %s", body)
	}
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", "/api/team/invites", creatorInviteBody, own.auth()); r.status != http.StatusCreated {
		t.Fatalf("a creator invite: %d %v", r.status, r.body)
	}
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", path, `{"host":false}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("turning it off with a creator invite waiting: %d %v", r.status, r.body)
	}
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", path, `{"host":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("turning it on: %d %v", r.status, r.body)
	}
	var team teamBody
	e.get(t, "/api/team", own.cookie, &team)
	if r := e.do(t, "DELETE", "/api/team/invites/"+team.Invites[0].ID, "", own.auth()); r.status >= 300 {
		t.Fatalf("turning the invite off: %d %v", r.status, r.body)
	}
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", path, `{"host":false}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("turning it off once the invite is off: %d %v", r.status, r.body)
	}
	addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	e.clock.add(2 * time.Second)
	if r := e.do(t, "POST", path, `{"host":false}`, own.auth()); r.status != http.StatusConflict {
		t.Fatalf("turning it off with a creator: %d %v", r.status, r.body)
	}
}
