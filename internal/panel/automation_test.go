package panel

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestWaveSevenRoutesReachTheAgent(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	ms, err := e.srv.machines()
	if err != nil || len(ms) != 1 {
		t.Fatalf("machines: %v %v", ms, err)
	}
	srv := "/api/servers/" + sampleServer
	for _, c := range []struct{ method, path, agent string }{
		{"POST", srv + "/schedules", "POST /v1/servers/" + sampleServer + "/schedules"},
		{"POST", srv + "/schedules/qrstuvwxyz", "POST /v1/servers/" + sampleServer + "/schedules/qrstuvwxyz"},
		{"DELETE", srv + "/schedules/qrstuvwxyz", "DELETE /v1/servers/" + sampleServer + "/schedules/qrstuvwxyz"},
		{"POST", srv + "/sleep", "POST /v1/servers/" + sampleServer + "/sleep"},
		{"POST", srv + "/offsite/new-key", "POST /v1/servers/" + sampleServer + "/offsite/new-key"},
		{"GET", "/api/machines/" + ms[0].ID + "/disk?tz=Europe%2FBerlin", "GET /v1/disk"},
		{"POST", "/api/machines/" + ms[0].ID + "/disk/clean", "POST /v1/disk/clean"},
	} {
		if r := e.do(t, c.method, c.path, `{}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Errorf("%s %s: %d %v", c.method, c.path, r.status, r.body)
		}
		e.agent.mu.Lock()
		hit := slices.Contains(e.agent.hits, c.agent)
		e.agent.mu.Unlock()
		if !hit {
			t.Errorf("%s %s did not reach the agent as %s", c.method, c.path, c.agent)
		}
	}
}

func TestRecoveryKeyIsNeverCachedAndNamesWhoTookIt(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	agentPath := "/v1/servers/" + sampleServer + "/offsite/recovery-key"
	e.reply("GET", agentPath, "AGE-SECRET-KEY-1TESTTESTTEST\n")
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/servers/"+sampleServer+"/offsite/recovery-key", nil)
	req.Header.Set("Cookie", cookieName+"="+cookie)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "AGE-SECRET-KEY-") {
		t.Fatalf("recovery key: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("recovery key headers: %v", resp.Header)
	}
	e.agent.mu.Lock()
	actor := e.agent.headers["GET "+agentPath].Get("X-Playkeeper-Actor")
	e.agent.mu.Unlock()
	if actor != "admin" {
		t.Fatalf("the agent was told %q took the key, want admin", actor)
	}
}

func TestMembersCannotTouchBackupCopiesOrTheRecoveryKey(t *testing.T) {
	e := newEnv(t)
	e.setup(t)
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES('friend', ?, 0, 0, 'member')`, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"friend","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != http.StatusOK {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	cookie, csrf := r.cookie, r.body["csrfToken"].(string)
	ms, _ := e.srv.machines()
	srv := "/api/servers/" + sampleServer
	for _, p := range []string{srv + "/schedules", srv + "/sleep", srv + "/backup-rules", srv + "/offsite"} {
		if r := e.do(t, "GET", p, "", auth(cookie, "")); r.status != http.StatusOK {
			t.Errorf("a member may look at %s: %d", p, r.status)
		}
	}
	e.agent.mu.Lock()
	e.agent.hits = nil
	e.agent.mu.Unlock()
	refused := []struct{ method, path string }{
		{"POST", srv + "/schedules"}, {"DELETE", srv + "/schedules/qrstuvwxyz"}, {"POST", srv + "/sleep"}, {"POST", srv + "/backup-rules"},
		{"POST", srv + "/offsite"}, {"POST", srv + "/offsite/test"}, {"POST", srv + "/offsite/ssh-key"}, {"POST", srv + "/offsite/retry"},
		{"GET", srv + "/offsite/recovery-key"}, {"POST", srv + "/offsite/new-key"}, {"POST", srv + "/offsite/restore"},
		{"POST", "/api/machines/" + url.PathEscape(ms[0].ID) + "/disk/clean"},
	}
	for _, c := range refused {
		if r := e.do(t, c.method, c.path, `{}`, auth(cookie, csrf)); r.status != http.StatusForbidden {
			t.Errorf("a member may not use %s %s: %d", c.method, c.path, r.status)
		}
	}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	if len(e.agent.hits) != 0 {
		t.Fatalf("refused requests reached the agent: %v", e.agent.hits)
	}
}
