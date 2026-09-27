package panel

import (
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestModpackRoutesReachTheMachinesAgent(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, _ := e.setup(t)
	var machines []map[string]any
	e.get(t, "/api/machines", cookie, &machines)
	if len(machines) == 0 {
		t.Fatal("no machine")
	}
	mid := "/api/machines/" + machines[0]["id"].(string)
	agent.take()
	agent.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/addons/icon" {
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, "\x89PNG\r\n\x1a\n pretend icon")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	})
	icon := "https://cdn.modrinth.com/data/TPK00001/icon.png"
	for _, tc := range []struct {
		path      string
		wantPath  string
		wantQuery url.Values
	}{
		{mid + "/modpacks?q=cobblemon&sort=downloads&offset=20", "/v1/modpacks", url.Values{"q": {"cobblemon"}, "sort": {"downloads"}, "offset": {"20"}}},
		{mid + "/modpacks/modrinth/TPK00001", "/v1/modpacks/modrinth/TPK00001", url.Values{}},
		{mid + "/modpacks/curseforge/123456/versions/7654321/preview", "/v1/modpacks/curseforge/123456/versions/7654321/preview", url.Values{}},
		{mid + "/modpacks/icon?url=" + url.QueryEscape(icon) + "&extra=1", "/v1/addons/icon", url.Values{"url": {icon}}},
	} {
		r, b := e.send(t, "GET", tc.path, "", "", auth(cookie, ""))
		if r.StatusCode != http.StatusOK {
			t.Errorf("GET %s: %d %s", tc.path, r.StatusCode, b)
			continue
		}
		calls := agent.take()
		if len(calls) != 1 {
			t.Errorf("GET %s: the agent saw %v", tc.path, calls)
			continue
		}
		if c := calls[0]; c.method != "GET" || c.path != tc.wantPath || !reflect.DeepEqual(c.query, tc.wantQuery) {
			t.Errorf("GET %s: the agent saw %s %s %v", tc.path, c.method, c.path, c.query)
		}
	}

	if r, b := e.send(t, "GET", "/api/machines/zzzzzzzzzz/modpacks", "", "", auth(cookie, "")); r.StatusCode != http.StatusNotFound {
		t.Errorf("another machine's modpacks: %d %s", r.StatusCode, b)
	}
	if r, _ := e.send(t, "GET", mid+"/modpacks", "", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("modpacks without a session: %d", r.StatusCode)
	}
	if calls := agent.take(); len(calls) != 0 {
		t.Errorf("refused requests reached the agent: %v", calls)
	}
}
