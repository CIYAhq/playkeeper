package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

func TestOnlyWhoManagesTheMachineChangesItsCurseForgeKey(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, csrf := e.setup(t)
	var machines []map[string]any
	e.get(t, "/api/machines", cookie, &machines)
	if len(machines) == 0 {
		t.Fatal("no machine")
	}
	sources := "/api/machines/" + machines[0]["id"].(string) + "/addon-sources"
	agent.take()
	owner := auth(cookie, csrf)
	owner["X-Requested-With"] = "playkeeper"
	owner["Content-Type"] = "application/json"

	if r, b := e.send(t, "GET", sources, "", "", auth(cookie, "")); r.StatusCode != http.StatusOK {
		t.Fatalf("GET: %d %s", r.StatusCode, b)
	}
	if r, b := e.send(t, "POST", sources+"/curseforge", "", `{"key":"pasted-key-0123456789abcdef"}`, owner); r.StatusCode != http.StatusOK {
		t.Fatalf("save: %d %s", r.StatusCode, b)
	}
	if r, b := e.send(t, "DELETE", sources+"/curseforge", "", "", owner); r.StatusCode != http.StatusOK {
		t.Fatalf("remove: %d %s", r.StatusCode, b)
	}
	calls := agent.take()
	if len(calls) != 3 || calls[0].method != "GET" || calls[0].path != "/v1/addon-sources" {
		t.Fatalf("the agent saw %+v", calls)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[1].body), &body); err != nil || calls[1].method != "POST" || calls[1].path != "/v1/addon-sources/curseforge" ||
		body["key"] != "pasted-key-0123456789abcdef" || body["actor"] != "admin" {
		t.Fatalf("the save reached the agent as %+v (%v)", calls[1], err)
	}
	if c := calls[2]; c.method != "DELETE" || c.path != "/v1/addon-sources/curseforge" || c.query.Get("actor") != "admin" {
		t.Fatalf("the removal reached the agent as %+v", c)
	}

	member := addMember(t, e, "friend", invites.RoleModerator, "*").auth()
	member["X-Requested-With"] = "playkeeper"
	member["Content-Type"] = "application/json"
	if r, b := e.send(t, "GET", sources, "", "", member); r.StatusCode != http.StatusOK {
		t.Errorf("a member may see the sources: %d %s", r.StatusCode, b)
	}
	for _, m := range []string{"POST", "DELETE"} {
		if r, b := e.send(t, m, sources+"/curseforge", "", `{"key":"pasted-key-0123456789abcdef"}`, member); r.StatusCode != http.StatusForbidden {
			t.Errorf("a member may not %s the key: %d %s", m, r.StatusCode, b)
		}
	}
	for _, c := range agent.take() {
		if c.method != "GET" {
			t.Errorf("a member's change reached the agent: %+v", c)
		}
	}
}
