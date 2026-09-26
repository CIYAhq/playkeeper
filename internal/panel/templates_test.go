package panel

import (
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestTemplateRoutesReachTheAgent(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, csrf := e.setup(t)
	var machines []map[string]any
	e.get(t, "/api/machines", cookie, &machines)
	if len(machines) == 0 {
		t.Fatal("no machine")
	}
	mid := "/api/machines/" + machines[0]["id"].(string)
	agent.take()

	r, b := e.send(t, "GET", "/api/servers/"+sampleServer+"/template?addons=off&versions=latest&author=someone+else", "", "", auth(cookie, ""))
	if r.StatusCode != http.StatusOK {
		t.Fatalf("export: %d %s", r.StatusCode, b)
	}
	calls := agent.take()
	// The template's author is the signed-in account, whatever the request says.
	want := url.Values{"addons": {"off"}, "versions": {"latest"}, "author": {"admin"}}
	if len(calls) != 1 || calls[0].method != "GET" || calls[0].path != "/v1/servers/"+sampleServer+"/template" || !reflect.DeepEqual(calls[0].query, want) {
		t.Fatalf("export: the agent saw %v", calls)
	}

	const link = "https://playkeeper.io/t#eyJ2IjoxfQ"
	r, b = e.send(t, "POST", mid+"/templates/plan?name=x", "", link, auth(cookie, csrf))
	if r.StatusCode != http.StatusOK || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("plan: %d %v %s", r.StatusCode, r.Header, b)
	}
	calls = agent.take()
	if len(calls) != 1 {
		t.Fatalf("plan: the agent saw %v", calls)
	}
	if c := calls[0]; c.method != "POST" || c.path != "/v1/templates/plan" || len(c.query) != 0 ||
		c.actor != "admin" || c.contentType != "text/plain" || c.body != link {
		t.Errorf("plan: the agent saw %s %s %v, actor %q, type %q, body %q", c.method, c.path, c.query, c.actor, c.contentType, c.body)
	}

	for _, tc := range []struct {
		name, method, path string
		hdr                map[string]string
		want               int
	}{
		{"a plan without the CSRF token", "POST", mid + "/templates/plan", auth(cookie, ""), http.StatusForbidden},
		{"a plan on another machine", "POST", "/api/machines/zzzzzzzzzz/templates/plan", auth(cookie, csrf), http.StatusNotFound},
		{"a plan without a session", "POST", mid + "/templates/plan", nil, http.StatusUnauthorized},
		{"an export without a session", "GET", "/api/servers/" + sampleServer + "/template", nil, http.StatusUnauthorized},
	} {
		if r, b := e.send(t, tc.method, tc.path, "", link, tc.hdr); r.StatusCode != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, r.StatusCode, b, tc.want)
		}
	}
	if calls := agent.take(); len(calls) != 0 {
		t.Errorf("refused requests reached the agent: %v", calls)
	}
}
