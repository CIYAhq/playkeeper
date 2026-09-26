package panel

import (
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/invites"
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
		{"POST", srv + "/backup-rules/estimate", "POST /v1/servers/" + sampleServer + "/backup-rules/estimate"},
		{"POST", srv + "/offsite/new-key", "POST /v1/servers/" + sampleServer + "/offsite/new-key"},
		{"POST", srv + "/offsite/copies/survival-1.tar.zst.age/check", "POST /v1/servers/" + sampleServer + "/offsite/copies/survival-1.tar.zst.age/check"},
		{"DELETE", srv + "/offsite/copies/survival-1.tar.zst.age", "DELETE /v1/servers/" + sampleServer + "/offsite/copies/survival-1.tar.zst.age"},
		{"POST", srv + "/offsite/restore/cancel", "POST /v1/servers/" + sampleServer + "/offsite/restore/cancel"},
		{"POST", "/api/machines/" + ms[0].ID + "/offsite/recover", "POST /v1/offsite/recover"},
		{"POST", "/api/machines/" + ms[0].ID + "/offsite/recover/restore", "POST /v1/offsite/recover/restore"},
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
		{"POST", srv + "/backup-rules/estimate"}, {"POST", srv + "/offsite"}, {"POST", srv + "/offsite/test"}, {"POST", srv + "/offsite/ssh-key"}, {"POST", srv + "/offsite/retry"},
		{"GET", srv + "/offsite/recovery-key"}, {"POST", srv + "/offsite/new-key"}, {"POST", srv + "/offsite/restore"},
		{"POST", srv + "/offsite/copies/survival-1.tar.zst.age/check"}, {"DELETE", srv + "/offsite/copies/survival-1.tar.zst.age"}, {"POST", srv + "/offsite/restore/cancel"},
		{"POST", "/api/machines/" + url.PathEscape(ms[0].ID) + "/disk/clean"},
		{"POST", "/api/machines/" + url.PathEscape(ms[0].ID) + "/offsite/recover"}, {"POST", "/api/machines/" + url.PathEscape(ms[0].ID) + "/offsite/recover/restore"},
	}
	for _, c := range refused {
		if r := e.do(t, c.method, c.path, `{}`, auth(cookie, csrf)); r.status != http.StatusForbidden {
			t.Errorf("a member may not use %s %s: %d", c.method, c.path, r.status)
		}
	}
	e.agent.mu.Lock()
	hits := append([]string(nil), e.agent.hits...)
	e.agent.mu.Unlock()
	if len(hits) != 0 {
		t.Fatalf("refused requests reached the agent: %v", hits)
	}
	var refusedKeys int
	if err := e.srv.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE actor = 'friend' AND action = 'offsite.recovery_key' AND result = 'refused'`).Scan(&refusedKeys); err != nil || refusedKeys != 4 {
		t.Fatalf("audited %d refused recovery key requests, want 4 (%v)", refusedKeys, err)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+srv+"/offsite/recovery-key", nil)
	req.Header.Set("Cookie", cookieName+"="+cookie)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || res.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), "AGE-SECRET-KEY") {
		t.Fatalf("a member's recovery key request: %d %v %q", res.StatusCode, res.Header, body)
	}
}

// Who may change where copies go or hold the recovery key is decided in
// mayHoldBackupKeys and nowhere else, so widening it to admins with
// two-factor later is one change.
func TestOneCheckDecidesWhoHoldsBackupKeys(t *testing.T) {
	e := newEnv(t)
	want := map[string]action{
		"POST /api/servers/{id}/offsite":                   actManageBackupCopies,
		"POST /api/servers/{id}/offsite/test":              actManageBackupCopies,
		"POST /api/servers/{id}/offsite/ssh-key":           actManageBackupCopies,
		"DELETE /api/servers/{id}/offsite/copies/{name}":   actManageBackupCopies,
		"GET /api/servers/{id}/offsite/recovery-key":       actRecoveryKey,
		"POST /api/servers/{id}/offsite/new-key":           actRecoveryKey,
		"POST /api/machines/{mid}/offsite/recover":         actRecoveryKey,
		"POST /api/machines/{mid}/offsite/recover/restore": actRecoveryKey,
	}
	for _, rt := range e.srv.Routes() {
		key := rt.Method + " " + rt.Pattern
		act, listed := want[key]
		switch {
		case listed && rt.Act != act:
			t.Errorf("%s is checked as %q, want %q", key, rt.Act, act)
		case !listed && (rt.Act == actManageBackupCopies || rt.Act == actRecoveryKey):
			t.Errorf("%s is checked as %q but isn't listed here", key, rt.Act)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("routes missing: %v", want)
	}
	accounts := map[string]access{
		"nobody": {},
		"owner":  {Account: invites.Account{UserID: 1, Name: "siya", InstallRole: roleOwner}},
		"member": {Account: invites.Account{UserID: 2, Name: "friend", InstallRole: roleMember, ProjectRole: invites.RoleViewer}},
		"admin":  {Account: invites.Account{UserID: 3, Name: "co", InstallRole: roleMember, ProjectRole: invites.RoleAdmin}},
	}
	for who, a := range accounts {
		for _, act := range []action{actManageBackupCopies, actRecoveryKey} {
			if (permit(a, act, "") == nil) != mayHoldBackupKeys(a) {
				t.Errorf("permit(%s, %q) disagrees with mayHoldBackupKeys", who, act)
			}
		}
		if got := mayHoldBackupKeys(a); got != (who == "owner") {
			t.Errorf("mayHoldBackupKeys(%s) = %v", who, got)
		}
	}
}
