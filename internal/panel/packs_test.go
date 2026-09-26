package panel

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// agentCall is a request the recording agent answered.
type agentCall struct {
	method, path, actor, contentType, body string
	query                                  url.Values
}

// recordingAgent records every request; answer, when set, writes the reply
// instead of {"ok":true}.
type recordingAgent struct {
	mu     sync.Mutex
	calls  []agentCall
	answer http.HandlerFunc
}

func (a *recordingAgent) handler(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.calls = append(a.calls, agentCall{r.Method, r.URL.Path, r.Header.Get("X-Playkeeper-Actor"), r.Header.Get("Content-Type"), string(b), r.URL.Query()})
	answer := a.answer
	a.mu.Unlock()
	if answer != nil {
		answer(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"ok":true}`)
}

func (a *recordingAgent) setAnswer(h http.HandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.answer = h
}

// take returns the requests recorded since it was last called.
func (a *recordingAgent) take() []agentCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.calls
	a.calls = nil
	return out
}

// send is e.do for a dashboard opened at host (the test server's address
// when empty), returning the raw reply.
func (e *env) send(t *testing.T, method, path, host, body string, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", e.ts.URL)
	if host != "" {
		req.Host = host
		req.Header.Set("Origin", "https://"+host)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, b
}

func jsonBody(b []byte) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func TestResourcePackUploadUsesTheDashboardAddress(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, csrf := e.setup(t)
	agent.take()
	const pack = "PK\x03\x04 a resource pack"
	path := "/api/servers/" + sampleServer + "/resourcepack?name=Faithful%2032x.zip&host=198.51.100.1&port=1&https=true"

	for _, host := range []string{"", "localhost:8443", "[::1]:8443", "0.0.0.0:8443", "203.0.113.10:0"} {
		r, b := e.send(t, "POST", path, host, pack, auth(cookie, csrf))
		body := jsonBody(b)
		if r.StatusCode != http.StatusBadRequest || body["code"] != "invalid_host" || body["hint"] != "Open the dashboard at the address players use to join, then upload the pack again." {
			t.Errorf("a dashboard opened at %q: %d %v", host, r.StatusCode, body)
		}
	}
	if calls := agent.take(); len(calls) != 0 {
		t.Fatalf("refused uploads reached the agent: %v", calls)
	}

	for host, want := range map[string]url.Values{
		"203.0.113.10:8443":   {"host": {"203.0.113.10"}, "port": {"8443"}, "name": {"Faithful 32x.zip"}},
		"play.example.com":    {"host": {"play.example.com"}, "port": {"443"}, "name": {"Faithful 32x.zip"}},
		"[2001:db8::10]:8443": {"host": {"2001:db8::10"}, "port": {"8443"}, "name": {"Faithful 32x.zip"}},
	} {
		r, b := e.send(t, "POST", path, host, pack, auth(cookie, csrf))
		if r.StatusCode != http.StatusOK {
			t.Errorf("a dashboard opened at %s: %d %s", host, r.StatusCode, b)
			continue
		}
		calls := agent.take()
		if len(calls) != 1 {
			t.Fatalf("the agent saw %v", calls)
		}
		c := calls[0]
		if c.method != "POST" || c.path != "/v1/servers/"+sampleServer+"/resourcepack" || !reflect.DeepEqual(c.query, want) {
			t.Errorf("a dashboard opened at %s: the agent saw %s %s %v, want the query %v", host, c.method, c.path, c.query, want)
		}
		if c.actor != "admin" || c.contentType != "application/zip" || c.body != pack {
			t.Errorf("a dashboard opened at %s: actor %q, type %q, body %q", host, c.actor, c.contentType, c.body)
		}
	}
}

func TestAddonAndPackRoutesReachTheAgent(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, csrf := e.setup(t)
	agent.take()
	srv := "/api/servers/" + sampleServer
	for _, tc := range []struct {
		method, path, body string
		wantPath           string
		wantQuery          url.Values
		wantActorIn        string
	}{
		{"GET", srv + "/addons/search?q=chunky&sort=downloads", "", "/v1/servers/" + sampleServer + "/addons/search", url.Values{"q": {"chunky"}, "sort": {"downloads"}}, ""},
		{"GET", srv + "/addons/project/hangar/EssentialsX/removal", "", "/v1/servers/" + sampleServer + "/addons/project/hangar/EssentialsX/removal", url.Values{}, ""},
		{"POST", srv + "/addons/install", `{"source":"modrinth","project":"AANobbMI","fingerprint":"f1"}`, "/v1/servers/" + sampleServer + "/addons/install", url.Values{}, "body"},
		{"POST", srv + "/addons/update/plan", `{"addons":[{"source":"modrinth","projectId":"AANobbMI"}]}`, "/v1/servers/" + sampleServer + "/addons/update/plan", url.Values{}, "body"},
		{"POST", srv + "/pregen/start", `{"radius":2500}`, "/v1/servers/" + sampleServer + "/pregen/start", url.Values{}, "body"},
		{"POST", srv + "/datapacks/Graves_v2.zip/disable", `{}`, "/v1/servers/" + sampleServer + "/datapacks/Graves_v2.zip/disable", url.Values{}, "body"},
		{"DELETE", srv + "/datapacks/Hand%20Made", "", "/v1/servers/" + sampleServer + "/datapacks/Hand Made", url.Values{"actor": {"admin"}}, ""},
		{"DELETE", srv + "/resourcepack", "", "/v1/servers/" + sampleServer + "/resourcepack", url.Values{"actor": {"admin"}}, ""},
		{"POST", srv + "/datapacks?name=Graves%20v2.zip&actor=eve&host=198.51.100.1", "PK zip", "/v1/servers/" + sampleServer + "/datapacks", url.Values{"name": {"Graves v2.zip"}}, "header"},
	} {
		r, b := e.send(t, tc.method, tc.path, "", tc.body, auth(cookie, csrf))
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, r.StatusCode, b)
			continue
		}
		calls := agent.take()
		if len(calls) != 1 {
			t.Errorf("%s %s: the agent saw %v", tc.method, tc.path, calls)
			continue
		}
		c := calls[0]
		if c.method != tc.method || c.path != tc.wantPath || !reflect.DeepEqual(c.query, tc.wantQuery) {
			t.Errorf("%s %s: the agent saw %s %s %v", tc.method, tc.path, c.method, c.path, c.query)
		}
		switch tc.wantActorIn {
		case "body":
			if jsonBody([]byte(c.body))["actor"] != "admin" {
				t.Errorf("%s %s: the agent's body %s has no actor", tc.method, tc.path, c.body)
			}
		case "header":
			if c.actor != "admin" || c.contentType != "application/zip" || c.body != tc.body {
				t.Errorf("%s %s: actor %q, type %q, body %q", tc.method, tc.path, c.actor, c.contentType, c.body)
			}
		}
	}
}

func TestAddonIconsAreOnlyImages(t *testing.T) {
	agent := &recordingAgent{}
	e := newEnvAgent(t, agent.handler, io.Discard)
	cookie, _ := e.setup(t)
	const png = "\x89PNG\r\n\x1a\n pretend icon"
	agent.setAnswer(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("url") {
		case "https://cdn.modrinth.com/data/AANobbMI/icon.png":
			w.Header().Set("Content-Type", "image/png")
			io.WriteString(w, png)
		case "https://cdn.modrinth.com/data/AANobbMI/icon.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`)
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"Playkeeper only loads icons from Modrinth's and Hangar's file hosts, not from evil.example.","code":"host_not_allowed"}`)
		}
	})
	icon := "/api/servers/" + sampleServer + "/addons/icon?url="
	r, b := e.send(t, "GET", icon+url.QueryEscape("https://cdn.modrinth.com/data/AANobbMI/icon.png"), "", "", auth(cookie, ""))
	if r.StatusCode != 200 || string(b) != png || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("Cache-Control") != "private, max-age=86400" {
		t.Errorf("a PNG icon: %d %q %v", r.StatusCode, b, r.Header)
	}
	r, b = e.send(t, "GET", icon+url.QueryEscape("https://cdn.modrinth.com/data/AANobbMI/icon.svg"), "", "", auth(cookie, ""))
	if r.StatusCode != http.StatusBadGateway || strings.Contains(string(b), "<svg") || r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("an SVG icon was relayed: %d %q %v", r.StatusCode, b, r.Header)
	}
	r, b = e.send(t, "GET", icon+url.QueryEscape("https://evil.example/icon.png"), "", "", auth(cookie, ""))
	if r.StatusCode != http.StatusBadRequest || jsonBody(b)["code"] != "host_not_allowed" {
		t.Errorf("an icon from another host: %d %s", r.StatusCode, b)
	}
	for _, c := range agent.take() {
		if c.path == "/v1/addons/icon" && len(c.query) != 1 {
			t.Errorf("the icon request carried more than its address: %v", c.query)
		}
	}
}

func TestMembersCannotChangeAddonsOrPacks(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('friend', ?, 0, 0, 'member')`, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"friend","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != 200 {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	cookie, csrf := r.cookie, r.body["csrfToken"].(string)
	n := 0
	for _, rt := range e.srv.Routes() {
		if !strings.Contains(rt.Pattern, "/addons") && !strings.Contains(rt.Pattern, "/pregen") &&
			!strings.Contains(rt.Pattern, "/datapacks") && !strings.Contains(rt.Pattern, "/resourcepack") {
			continue
		}
		n++
		r := e.do(t, rt.Method, samplePath(rt.Pattern), `{}`, auth(cookie, csrf))
		if rt.Mutating() && r.status != http.StatusForbidden {
			t.Errorf("a member may not use %s %s: %d", rt.Method, rt.Pattern, r.status)
		}
		if !rt.Mutating() && (r.status == http.StatusForbidden || r.status == http.StatusUnauthorized) {
			t.Errorf("a member may look at %s: %d", rt.Pattern, r.status)
		}
	}
	if n != 29 {
		t.Errorf("checked %d add-on, pre-generation and pack routes, want 29", n)
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	for _, hit := range e.agent.hits {
		if !strings.HasPrefix(hit, "GET ") {
			t.Errorf("a member's change reached the agent: %s", hit)
		}
	}
}
