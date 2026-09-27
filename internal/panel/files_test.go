package panel

import (
	"io"
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/invites"
)

// fileRoutes are the file browser's routes, each with a request that would
// otherwise reach the agent.
func fileRoutes() [][3]string {
	srv := "/api/servers/" + sampleServer + "/files"
	up := srv + "/uploads/0123456789abcdef"
	return [][3]string{
		{"GET", srv + "?path=plugins", ""},
		{"GET", srv + "/content?path=server.properties", ""},
		{"GET", srv + "/download?path=server.properties", ""},
		{"PUT", srv + "/content?path=server.properties", "motd=x\n"},
		{"POST", srv + "/folder", `{"path":"plugins/new"}`},
		{"POST", srv + "/move", `{"items":[{"from":"a","to":"b"}]}`},
		{"POST", srv + "/delete", `{"paths":["a"]}`},
		{"POST", srv + "/uploads", `{"folder":"plugins"}`},
		{"GET", up, ""},
		{"DELETE", up, ""},
		{"POST", up + "/files", `{"name":"a.jar","size":3}`},
		{"PUT", up + "/files/0?offset=0", "abc"},
	}
}

// A server's raw files hold what the dashboard keeps from moderators and
// viewers, and changing them can run code in the game, so only admins may
// look at them or change them, with two-factor sign-in on as for every
// Admin right.
func TestTheFileBrowserIsForAdmins(t *testing.T) {
	e := newEnv(t)
	ownerCookie, ownerCSRF := e.setup(t)
	viewer := addMember(t, e, "vic", invites.RoleViewer, "*")
	moderator := addMember(t, e, "mo", invites.RoleModerator, "*")
	noFactor := addMember(t, e, "una", invites.RoleAdmin, "*")
	admin := addAdmin(t, e, "ada", "*")
	for _, c := range fileRoutes() {
		for who, m := range map[string]member{"a viewer": viewer, "a moderator": moderator} {
			e.clock.add(2 * time.Second)
			if r := e.do(t, c[0], c[1], c[2], m.auth()); r.status != http.StatusForbidden || r.body["error"] != errForbidden.Msg {
				t.Errorf("%s, %s %s: %d %v", who, c[0], c[1], r.status, r.body)
			}
		}
		e.clock.add(2 * time.Second)
		if r := e.do(t, c[0], c[1], c[2], noFactor.auth()); r.status != http.StatusForbidden || r.body["code"] != invites.CodeTwoFactorRequired {
			t.Errorf("an admin without two-factor sign-in, %s %s: %d %v", c[0], c[1], r.status, r.body)
		}
	}
	if hits := e.agentHits(); len(hits) != 0 {
		t.Fatalf("refused requests reached the agent: %v", hits)
	}
	for who, h := range map[string]map[string]string{"the owner": auth(ownerCookie, ownerCSRF), "an admin": admin.auth()} {
		for _, c := range fileRoutes() {
			e.clock.add(2 * time.Second)
			if r := e.do(t, c[0], c[1], c[2], h); r.status != http.StatusOK {
				t.Errorf("%s, %s %s: %d %v", who, c[0], c[1], r.status, r.body)
			}
		}
	}
	if hits := e.agentHits(); len(hits) != 2*len(fileRoutes()) {
		t.Fatalf("the agent saw %d requests, want %d: %v", len(hits), 2*len(fileRoutes()), hits)
	}
	var me map[string]any
	for _, m := range []member{moderator, admin} {
		me = e.do(t, "GET", "/api/auth/me", "", m.auth()).body
		can := me["access"].(map[string]any)["can"].([]any)
		has := func(a string) bool {
			for _, c := range can {
				if c == a {
					return true
				}
			}
			return false
		}
		if want := m == admin; has("files.view") != want || has("files.edit") != want {
			t.Errorf("can = %v", can)
		}
	}
}

// The editor's text goes to the agent as it is, with the version it was
// opened with and who saved it; the panel refuses more than the editor may
// save before sending anything.
func TestTheEditorsSaveGoesToTheAgentAsItIs(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	agentPath := "/v1/servers/" + sampleServer + "/files/content"
	e.reply("PUT", agentPath, `{"path":"server.properties","size":7,"version":"`+strings.Repeat("a", 64)+`"}`)
	r := e.do(t, "PUT", "/api/servers/"+sampleServer+"/files/content?path=server.properties&expect="+strings.Repeat("b", 64)+"&actor=mallory", "motd=x\n", auth(cookie, csrf))
	if r.status != http.StatusOK || r.body["size"] != float64(7) {
		t.Fatalf("save: %d %v", r.status, r.body)
	}
	req := e.agentRequest(t, "PUT", agentPath)
	if req.query.Get("path") != "server.properties" || req.query.Get("expect") != strings.Repeat("b", 64) || req.query.Has("actor") {
		t.Fatalf("the agent was asked %v", req.query)
	}
	e.agent.mu.Lock()
	body, h := e.agent.lastBody["PUT "+agentPath], e.agent.headers["PUT "+agentPath]
	e.agent.mu.Unlock()
	if body != "motd=x\n" || h.Get("X-Playkeeper-Actor") != "admin" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
		t.Fatalf("the agent got %q with %v", body, h)
	}
	hits := len(e.agentHits())
	if r := e.do(t, "PUT", "/api/servers/"+sampleServer+"/files/content?path=big.log", strings.Repeat("#", maxSaveBytes+1), auth(cookie, csrf)); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("a save over 2 MB: %d %v", r.status, r.body)
	}
	if len(e.agentHits()) != hits {
		t.Fatal("a save over 2 MB reached the agent")
	}
	e.replyStatus("PUT", agentPath, http.StatusConflict, `{"error":"server.properties changed since you opened it.","code":"file_changed"}`)
	if r := e.do(t, "PUT", "/api/servers/"+sampleServer+"/files/content?path=server.properties", "x", auth(cookie, csrf)); r.status != http.StatusConflict || r.body["code"] != "file_changed" {
		t.Fatalf("a refused save: %d %v", r.status, r.body)
	}
}

func download(t *testing.T, e *env, cookie, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
	req.Header.Set("Cookie", cookieName+"="+cookie)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// A download is named and typed by the panel from the path asked for, never
// by the machine, so nothing a joined machine sends can render on the
// panel's origin or be saved under a name it chose.
func TestFileDownloadsAreNamedAndTypedByThePanel(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	agentPath := "/v1/servers/" + sampleServer + "/files/download"
	contentType := "text/html"
	e.agent.mu.Lock()
	e.agent.answers["GET "+agentPath] = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `attachment; filename="index.html"`)
		io.WriteString(w, "<script>alert(1)</script>")
	}
	e.agent.mu.Unlock()
	base := "/api/servers/" + sampleServer + "/files/download?path="
	for _, c := range []struct {
		zip         bool
		query       string
		name        string
		disposition string
	}{
		{false, "plugins/Essentials/config.yml", "config.yml", ""},
		{false, "logs/%22quoted%22%0Aname.log", "_quoted__name.log", ""},
		{false, "plugins/Caf%C3%A9.yml", "Café.yml", "attachment; filename*=utf-8''Caf%C3%A9.yml"},
		{true, "plugins/Essentials", "Essentials.zip", ""},
		{true, "", "server-files.zip", ""},
		{true, "plugins/a.jar&path=plugins/b.jar", "plugins.zip", ""},
		{true, "a.jar&path=b.jar", "server-files.zip", ""},
	} {
		contentType = "text/html"
		if c.zip {
			contentType = "application/zip"
		}
		resp, body := download(t, e, cookie, base+c.query)
		if resp.StatusCode != 200 || body != "<script>alert(1)</script>" {
			t.Fatalf("%s: %d %q", c.query, resp.StatusCode, body)
		}
		_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
		if err != nil || params["filename"] != c.name || (c.disposition != "" && resp.Header.Get("Content-Disposition") != c.disposition) {
			t.Errorf("%s: saved as %q (%v)", c.query, resp.Header.Get("Content-Disposition"), err)
		}
		if resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("Content-Security-Policy") != "sandbox" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: headers %v", c.query, resp.Header)
		}
	}
	req := e.agentRequest(t, "GET", agentPath)
	if strings.Join(req.query["path"], " ") != "a.jar b.jar" {
		t.Fatalf("the agent was asked for %v", req.query["path"])
	}
	e.agent.mu.Lock()
	actor := e.agent.headers["GET "+agentPath].Get("X-Playkeeper-Actor")
	delete(e.agent.answers, "GET "+agentPath)
	e.agent.mu.Unlock()
	if actor != "admin" {
		t.Fatalf("the agent was told %q downloaded it", actor)
	}
	e.replyStatus("GET", agentPath, http.StatusConflict, `{"error":"evil.yml in the server's files is a link, which Playkeeper does not follow.","code":"link"}`)
	if resp, body := download(t, e, cookie, base+"evil.yml"); resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"code":"link"`) || resp.Header.Get("Content-Disposition") != "" {
		t.Fatalf("a refused download: %d %s %v", resp.StatusCode, body, resp.Header)
	}
	if resp, _ := download(t, e, cookie, "/api/servers/"+sampleServer+"/files/download"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a download of nothing: %d", resp.StatusCode)
	}
}

// Uploading a folder of small files sends many pieces; they have a bucket of
// their own, so the uploads neither run into the 30 actions a minute nor use
// them up.
func TestUploadPiecesHaveTheirOwnRateLimit(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	up := "/api/servers/" + sampleServer + "/files/uploads/0123456789abcdef"
	for i := range 200 {
		if r := e.do(t, "POST", up+"/files", `{"name":"a.yml","size":1}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("announce %d: %d %v", i+1, r.status, r.body)
		}
		if r := e.do(t, "PUT", up+"/files/0?offset=0", "x", auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("piece %d: %d %v", i+1, r.status, r.body)
		}
	}
	for i := range 30 {
		if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/files/folder", `{"path":"a"}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("action %d after the uploads: %d %v", i+1, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/files/folder", `{"path":"a"}`, auth(cookie, csrf)); r.status != http.StatusTooManyRequests {
		t.Fatalf("the 31st action in a burst: %d %v", r.status, r.body)
	}
	for _, bad := range []string{"/files/0?offset=-1", "/files/0?offset=01", "/files/0?offset=x", "/files/abc?offset=0"} {
		if r := e.do(t, "PUT", up+bad, "x", auth(cookie, csrf)); r.status != http.StatusBadRequest {
			t.Errorf("PUT %s: %d %v", bad, r.status, r.body)
		}
	}
}

// A file open in the Files tab has its path in the page's address, so a
// reload or a shared link loads the dashboard, whatever the file's
// extension. Any other address with an extension and no built file behind it
// stays missing, so a script left over from an older build fails plainly.
func TestFilesPagesLoadTheDashboard(t *testing.T) {
	e, _ := newScriptedEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, p := range []string{"/servers/survival/files", "/servers/survival/files/plugins/LuckPerms", "/servers/survival/files/config.d",
		"/servers/survival/file/server.properties", "/servers/survival/file/plugins/BlueMap/web/index.html"} {
		r, body := e.stream(t, "GET", p, nil, nil)
		if r.StatusCode != 200 || string(body) != indexPage {
			t.Errorf("%s: %d %q", p, r.StatusCode, body)
		}
	}
	for _, p := range []string{"/assets/gone.js", "/gone.js", "/servers/survival/gone.txt", "/servers/files/x.js"} {
		if r, _ := e.stream(t, "GET", p, nil, nil); r.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", p, r.StatusCode)
		}
	}
}

func TestEveryUploadRouteIsACheckedRoute(t *testing.T) {
	e := newEnv(t)
	routes := map[string]Route{}
	for _, rt := range e.srv.Routes() {
		routes[rt.Method+" "+rt.Pattern] = rt
	}
	for key := range uploadRoutes {
		if rt, ok := routes[key]; !ok || rt.Level != needSessionCSRF || rt.Act != actEditFiles {
			t.Errorf("%s: not a file browser route with a session and a CSRF token (found %v, %+v)", key, ok, rt)
		}
	}
}
