package panel

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

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

// Schedule previews and backup-rule estimates follow typing: they have a
// bucket of their own, larger than the 30 actions a minute, and using it up
// leaves the actions alone.
func TestPreviewsHaveTheirOwnRateLimit(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	srv := "/api/servers/" + sampleServer
	previews := []string{srv + "/schedules/preview", srv + "/backup-rules/estimate"}
	for i := range 100 {
		if r := e.do(t, "POST", previews[i%2], `{}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("preview %d, %s: %d %v", i+1, previews[i%2], r.status, r.body)
		}
	}
	limited := false
	for range 50 {
		if r := e.do(t, "POST", previews[0], `{}`, auth(cookie, csrf)); r.status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("150 previews in a burst were never rate limited")
	}
	for i := range 30 {
		if r := e.do(t, "POST", srv+"/schedules", `{}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("action %d after the previews ran out: %d %v", i+1, r.status, r.body)
		}
	}
	if r := e.do(t, "POST", srv+"/schedules", `{}`, auth(cookie, csrf)); r.status != http.StatusTooManyRequests {
		t.Fatalf("the 31st action in a burst: %d %v", r.status, r.body)
	}
}

func TestEveryPreviewRouteIsACheckedRoute(t *testing.T) {
	e := newEnv(t)
	routes := map[string]authLevel{}
	for _, rt := range e.srv.Routes() {
		routes[rt.Method+" "+rt.Pattern] = rt.Level
	}
	for key := range previewRoutes {
		if level, ok := routes[key]; !ok || level != needSessionCSRF {
			t.Errorf("%s: not a route with a session and a CSRF token (found %v, level %d)", key, ok, level)
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

// Wave 7's routes follow the Team page's table: viewers look, moderators
// back up now and check or retry copies, admins change schedules, sleep and
// backup rules and restore, and only those who may hold backup keys change
// where copies go or take the recovery key.
func TestWaveSevenRoutesFollowTheTeamTable(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	mid := machineID(t, e)
	srv := "/api/servers/" + sampleServer
	copyPath := srv + "/offsite/copies/survival-1.tar.zst.age"
	const (
		looks = iota + 1
		runs
		manages
		holdsKeys
	)
	routes := []struct {
		method, path string
		least        int
	}{
		{"GET", srv + "/schedules", looks}, {"GET", srv + "/schedules/runs", looks}, {"GET", srv + "/sleep", looks},
		{"GET", srv + "/backup-rules", looks}, {"GET", srv + "/offsite", looks}, {"GET", srv + "/offsite/copies", looks},
		{"GET", "/api/machines/" + mid + "/disk", looks},
		{"POST", srv + "/backups", runs}, {"POST", srv + "/offsite/retry", runs}, {"POST", copyPath + "/check", runs},
		{"POST", srv + "/schedules", manages}, {"POST", srv + "/schedules/preview", manages}, {"POST", srv + "/schedules/qrstuvwxyz", manages},
		{"DELETE", srv + "/schedules/qrstuvwxyz", manages}, {"POST", srv + "/sleep", manages}, {"POST", srv + "/backup-rules", manages},
		{"POST", srv + "/backup-rules/estimate", manages}, {"POST", srv + "/offsite/restore", manages}, {"POST", srv + "/offsite/restore/cancel", manages},
		{"POST", "/api/machines/" + mid + "/disk/clean", manages},
		{"POST", srv + "/offsite", holdsKeys}, {"POST", srv + "/offsite/test", holdsKeys}, {"POST", srv + "/offsite/ssh-key", holdsKeys},
		{"GET", srv + "/offsite/recovery-key", holdsKeys}, {"POST", srv + "/offsite/new-key", holdsKeys}, {"DELETE", copyPath, holdsKeys},
		{"POST", "/api/machines/" + mid + "/offsite/recover", holdsKeys}, {"POST", "/api/machines/" + mid + "/offsite/recover/restore", holdsKeys},
	}
	accounts := []struct {
		who   string
		m     member
		level int
	}{
		{"a viewer", addMember(t, e, "friend", invites.RoleViewer, "*"), looks},
		{"a moderator", addMember(t, e, "mo", invites.RoleModerator, "*"), runs},
		{"an admin without two-factor", addMember(t, e, "una", invites.RoleAdmin, "*"), runs},
		{"an admin with two-factor", addAdmin(t, e, "ada", "*"), holdsKeys},
		{"the owner", own, holdsKeys},
	}
	for _, a := range accounts {
		for _, c := range routes {
			e.clock.add(2 * time.Second)
			e.agent.mu.Lock()
			e.agent.hits = nil
			e.agent.mu.Unlock()
			r := e.do(t, c.method, c.path, `{}`, a.m.auth())
			switch {
			case a.level >= c.least && (r.status == http.StatusForbidden || r.status == http.StatusUnauthorized):
				t.Errorf("%s may use %s %s: %d %v", a.who, c.method, c.path, r.status, r.body)
			case a.level >= c.least:
			case r.status != http.StatusForbidden:
				t.Errorf("%s may not use %s %s: %d %v", a.who, c.method, c.path, r.status, r.body)
			case a.who == "an admin without two-factor" && r.body["code"] != invites.CodeTwoFactorRequired:
				t.Errorf("%s is refused %s %s without asking for two-factor: %v", a.who, c.method, c.path, r.body)
			case len(e.agentHits()) != 0:
				t.Errorf("%s's refused %s %s reached the agent: %v", a.who, c.method, c.path, e.agentHits())
			}
		}
	}
	for action, want := range map[string]int{"offsite.recovery_key": 2, "offsite.recover": 2} {
		var n int
		if err := e.srv.db.QueryRow(`SELECT COUNT(*) FROM audit WHERE actor = 'friend' AND action = ? AND result = 'refused'`, action).Scan(&n); err != nil || n != want {
			t.Errorf("audited %d refused %s requests, want %d (%v)", n, action, want, err)
		}
	}
	req, _ := http.NewRequest("GET", e.ts.URL+srv+"/offsite/recovery-key", nil)
	req.Header.Set("Cookie", cookieName+"="+accounts[1].m.cookie)
	res, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || res.Header.Get("Cache-Control") != "no-store" || strings.Contains(string(body), "AGE-SECRET-KEY") {
		t.Fatalf("a moderator's recovery key request: %d %v %q", res.StatusCode, res.Header, body)
	}
}

// Who may change where copies go or hold the recovery key is decided in
// mayHoldBackupKeys and nowhere else: the owner, or an admin whose two-factor
// sign-in is on (and confirmed, as for every Admin right).
func TestOneCheckDecidesWhoHoldsBackupKeys(t *testing.T) {
	e := newEnv(t)
	want := map[string]action{
		"POST /api/servers/{id}/offsite":                   actManageBackupCopies,
		"POST /api/servers/{id}/offsite/test":              actManageBackupCopies,
		"POST /api/servers/{id}/offsite/ssh-key":           actManageBackupCopies,
		"DELETE /api/servers/{id}/offsite/copies/{name}":   actManageBackupCopies,
		"GET /api/servers/{id}/offsite/recovery-key":       actRecoveryKey,
		"POST /api/servers/{id}/offsite/new-key":           actRecoveryKey,
		"POST /api/machines/{mid}/offsite/recover":         actRecoverBackups,
		"POST /api/machines/{mid}/offsite/recover/restore": actRecoverBackups,
	}
	for _, rt := range e.srv.Routes() {
		key := rt.Method + " " + rt.Pattern
		act, listed := want[key]
		switch {
		case listed && rt.Act != act:
			t.Errorf("%s is checked as %q, want %q", key, rt.Act, act)
		case !listed && keyActions[rt.Act]:
			t.Errorf("%s is checked as %q but isn't listed here", key, rt.Act)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Errorf("routes missing: %v", want)
	}
	admin := func(servers invites.Scope, factorOn, confirmed bool) access {
		return access{Account: invites.Account{UserID: 3, Name: "co", InstallRole: roleMember, ProjectRole: invites.RoleAdmin, Servers: servers, TwoFactor: factorOn && confirmed}, FactorOn: factorOn}
	}
	for _, c := range []struct {
		who     string
		a       access
		holds   bool
		refusal *invites.Error
	}{
		{"nobody", access{}, false, errForbidden},
		{"the owner", access{Account: invites.Account{UserID: 1, Name: "siya", InstallRole: roleOwner}}, true, nil},
		{"a viewer", access{Account: invites.Account{UserID: 2, Name: "friend", InstallRole: roleMember, ProjectRole: invites.RoleViewer, Servers: invites.AllServers()}}, false, errForbidden},
		{"a moderator with two-factor on", access{Account: invites.Account{UserID: 2, Name: "mo", InstallRole: roleMember, ProjectRole: invites.RoleModerator, Servers: invites.AllServers(), TwoFactor: true}, FactorOn: true}, false, errForbidden},
		{"an admin with two-factor on", admin(invites.AllServers(), true, true), true, nil},
		{"an admin of one server with two-factor on", admin(invites.OnlyServers(otherServer), true, true), true, nil},
		{"an admin without two-factor", admin(invites.AllServers(), false, false), false, invites.TwoFactorRequired()},
		{"an admin whose Admin rights wait to be confirmed", admin(invites.AllServers(), true, false), false, errAdminUnconfirmed},
	} {
		if got := mayHoldBackupKeys(c.a); got != c.holds {
			t.Errorf("mayHoldBackupKeys(%s) = %v, want %v", c.who, got, c.holds)
		}
		for _, act := range []action{actManageBackupCopies, actRecoveryKey} {
			err := permit(c.a, act, "")
			if (err == nil) != c.holds {
				t.Errorf("permit(%s, %q) = %v, but mayHoldBackupKeys says %v", c.who, act, err, c.holds)
			}
			if ie, ok := err.(*invites.Error); c.refusal != nil && (!ok || ie.Code != c.refusal.Code || ie.Msg != c.refusal.Msg) {
				t.Errorf("permit(%s, %q) = %v, want %q", c.who, act, err, c.refusal.Msg)
			}
		}
		// Bringing a server back makes one, so it needs every server too.
		err := permit(c.a, actRecoverBackups, "")
		switch everyServer := c.a.owner() || c.a.Servers.All; {
		case c.holds && everyServer && err != nil:
			t.Errorf("%s may not bring a server back: %v", c.who, err)
		case c.holds && !everyServer && err != errAllServers:
			t.Errorf("%s of one server may bring a server back: %v", c.who, err)
		case !c.holds && err == nil:
			t.Errorf("%s may bring a server back", c.who)
		}
	}
}
