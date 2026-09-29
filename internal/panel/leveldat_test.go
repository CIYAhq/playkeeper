package panel

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// A new level.dat changes the world, as a restore does, so it takes what a
// restore takes: a moderator's request never reaches the agent, an admin's
// does, with who asked.
func TestOnlyAnAdminMakesANewLevelDat(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.reply("POST", "/v1/servers/"+sampleServer+"/world/rebuild-level", `{"id":"op1","kind":"rebuild-level","status":"running"}`)
	path := "/api/servers/" + sampleServer + "/world/rebuild-level"
	mod := addMember(t, e, "mo", invites.RoleModerator, "*")
	if r := e.do(t, "POST", path, `{"world":"world","start":true}`, mod.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a moderator: %d %v", r.status, r.body)
	}
	if hits := e.agentHits(); len(hits) != 0 {
		t.Fatalf("a moderator's request reached the agent: %v", hits)
	}
	e.clock.add(2 * time.Second)
	ada := addAdmin(t, e, "ada", "*")
	if r := e.do(t, "POST", path, `{"world":"world","start":true}`, ada.auth()); r.status == http.StatusForbidden || r.status == http.StatusUnauthorized {
		t.Fatalf("an admin: %d %v", r.status, r.body)
	}
	if body := e.waitForHit(t, "POST /v1/servers/"+sampleServer+"/world/rebuild-level"); !strings.Contains(body, `"actor":"ada"`) || !strings.Contains(body, `"world":"world"`) {
		t.Fatalf("the agent got %s", body)
	}
}
