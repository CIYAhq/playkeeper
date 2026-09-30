package panel

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// Taking something out of a world changes the world, as a restore does, so
// it takes what a restore takes: a moderator's request never reaches the
// agent, an admin's does, with who asked.
func TestOnlyAnAdminTakesSomethingOutOfAWorld(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	e.reply("POST", "/v1/servers/"+sampleServer+"/world/remove-entity", `{"id":"op1","kind":"remove-entity","status":"running"}`)
	path := "/api/servers/" + sampleServer + "/world/remove-entity"
	body := `{"what":"entity","type":"minecraft:minecart","dimension":"minecraft:overworld","x":6,"y":120,"z":6,"start":true}`
	mod := addMember(t, e, "mo", invites.RoleModerator, "*")
	if r := e.do(t, "POST", path, body, mod.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a moderator: %d %v", r.status, r.body)
	}
	if hits := e.agentHits(); len(hits) != 0 {
		t.Fatalf("a moderator's request reached the agent: %v", hits)
	}
	e.clock.add(2 * time.Second)
	ada := addAdmin(t, e, "ada", "*")
	if r := e.do(t, "POST", path, body, ada.auth()); r.status == http.StatusForbidden || r.status == http.StatusUnauthorized {
		t.Fatalf("an admin: %d %v", r.status, r.body)
	}
	if got := e.waitForHit(t, "POST /v1/servers/"+sampleServer+"/world/remove-entity"); !strings.Contains(got, `"actor":"ada"`) || !strings.Contains(got, `"type":"minecraft:minecart"`) {
		t.Fatalf("the agent got %s", got)
	}
}
