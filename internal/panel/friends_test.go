package panel

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
	"github.com/CIYAhq/playkeeper/internal/modpacks/share"
	"github.com/CIYAhq/playkeeper/internal/mojang"
	"github.com/CIYAhq/playkeeper/internal/names"
	"github.com/CIYAhq/playkeeper/internal/packs"
)

// logBuffer collects the panel's log for tests that read it.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Accounts the Mojang fake knows, with the design's sample names.
var sampleProfiles = map[string]string{
	"pixelpia": `{"id":"5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f","name":"PixelPia"}`,
	"mara_k":   `{"id":"0123456789abcdef0123456789abcdef","name":"mara_k"}`,
	"tobi_k":   `{"id":"4123456789abcdef0123456789abcdef","name":"tobi_k"}`,
}

// joinEnv is a panel whose name lookups and faces go to local fakes, and
// whose log a test can read.
type joinEnv struct {
	*env
	log     *logBuffer
	lookups *atomic.Int32
	// together, when set, holds each name lookup until that many have
	// arrived, so requests racing for an invite have all read it by then.
	together *atomic.Int32
}

func newJoinEnv(t *testing.T) joinEnv {
	t.Helper()
	lookups, together := &atomic.Int32{}, &atomic.Int32{}
	skins := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(skin(t, 64))
	}))
	t.Cleanup(skins.Close)
	profiles := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		for deadline := time.Now().Add(5 * time.Second); lookups.Load() < together.Load() && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		name, _ := strings.CutPrefix(r.URL.Path, "/minecraft/profile/lookup/name/")
		if p, ok := sampleProfiles[strings.ToLower(name)]; ok {
			io.WriteString(w, p)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(profiles.Close)
	sessions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/minecraft/profile/5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f" {
			io.WriteString(w, `{"name":"PixelPia","properties":[{"name":"textures","value":"`+textures(skins.URL+"/texture/pia")+`"}]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(sessions.Close)
	mc, err := mojang.NewClient(mojang.Options{BaseURL: profiles.URL, HTTP: profiles.Client()})
	if err != nil {
		t.Fatal(err)
	}
	log := &logBuffer{}
	e := newEnvWith(t, func(o *Options) {
		o.Mojang = mc
		o.Heads = &HeadSources{SessionURL: sessions.URL, TexturesURL: skins.URL, Client: &http.Client{Timeout: 5 * time.Second}}
		o.Logger = slog.New(slog.NewTextHandler(log, nil))
	})
	e.reply("GET", "/v1/servers", bothServers)
	e.reply("GET", "/v1/servers/"+sampleServer, `{"id":"abcdefghjk","name":"Survival","phase":"online","gamePort":25565,
		"config":{"minecraftVersion":"1.21.8"},"players":{"online":2,"names":["Steve","Alex"],"at":"2026-09-24T11:59:30Z"}}`)
	e.reply("GET", "/v1/servers/"+sampleServer+"/whitelist", `[]`)
	e.reply("POST", "/v1/servers/"+sampleServer+"/whitelist", `{"message":"Added.","whitelist":[],"added":true}`)
	return joinEnv{env: e, log: log, lookups: lookups, together: together}
}

// public calls a public invite route as the join page does.
func (e *env) public(t *testing.T, route, body string) resp {
	t.Helper()
	return e.do(t, "POST", "/api/public/join/"+route, body, map[string]string{"X-Requested-With": "playkeeper"})
}

// raw sends a request and returns the response with its body.
func (e *env) raw(t *testing.T, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.ts.URL)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r, string(b)
}

// friendInvite makes a friend invite link for Survival and returns its id
// and code.
func friendInvite(t *testing.T, e *env, m member, body string) (id, code string) {
	t.Helper()
	r := e.do(t, "POST", "/api/servers/"+sampleServer+"/invites", body, m.auth())
	path, _ := r.body["path"].(string)
	code, ok := strings.CutPrefix(path, invites.JoinPath+"/")
	if r.status != http.StatusCreated || !ok || !invites.WellFormed(code) {
		t.Fatalf("new invite link: %d %v", r.status, r.body)
	}
	return r.body["id"].(string), code
}

func codeBody(code string, fields ...string) string {
	b := `{"code":"` + code + `"`
	for i := 0; i+1 < len(fields); i += 2 {
		b += `,"` + fields[i] + `":"` + fields[i+1] + `"`
	}
	return b + "}"
}

// beforeAgentReply runs f while the panel waits for the agent's answer to
// method path.
func (e *env) beforeAgentReply(method, path string, f func()) {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	e.agent.before[method+" "+path] = f
}

// doAside is do for a goroutine other than the test's: it gives the status,
// or 0 when the request didn't go through.
func (e *env) doAside(method, path, body string, hdr map[string]string) int {
	req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", e.ts.URL)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := e.ts.Client().Do(req)
	if err != nil {
		return 0
	}
	r.Body.Close()
	return r.StatusCode
}

func (e *env) hitCount(key string) int {
	n := 0
	for _, h := range e.agentHits() {
		if h == key {
			n++
		}
	}
	return n
}

// A friend opens a link, checks their name and is on the allowlist, which
// then says how they got in; the link stops after its last friend and when
// turned off. Public pages never show the inviter's username (M4).
func TestFriendInviteLetsFriendsIn(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	id, code := friendInvite(t, e.env, own, `{"label":"Discord crew","expiry":"7d","maxUses":2,"approval":"right_away"}`)
	if rows := e.auditRows(t, "invite.create"); len(rows) != 1 || strings.Contains(rows[0], code) || !strings.Contains(rows[0], "Survival; works 7d; 2 friends; right_away") {
		t.Fatalf("audit: %v", rows)
	}

	r := e.public(t, "preview", codeBody(code))
	if r.status != 200 || r.body["kind"] != "player" || r.body["server"] != "Survival" || r.body["version"] != "1.21.8" || r.body["playing"] != float64(2) || r.body["inviter"] != "" {
		t.Fatalf("preview: %d %v", r.status, r.body)
	}
	r = e.public(t, "lookup", codeBody(code, "name", "pixelpia"))
	face, _ := r.body["face"].(string)
	if r.status != 200 || r.body["name"] != "PixelPia" || r.body["uuid"] != "5c4d3e2f-1a0b-9c8d-7e6f-5a4b3c2d1e0f" || !strings.HasPrefix(face, "data:image/png;base64,") {
		t.Fatalf("lookup: %d %v", r.status, r.body)
	}
	if e.hitCount("POST /v1/servers/"+sampleServer+"/whitelist") != 0 {
		t.Fatal("a lookup changed the allowlist")
	}
	r = e.public(t, "redeem", codeBody(code, "name", "PixelPia"))
	if r.status != 200 || r.body["player"] != "PixelPia" || r.body["address"] != "127.0.0.1" || r.body["waiting"] != nil {
		t.Fatalf("redeem: %d %v", r.status, r.body)
	}
	body := e.agentBody("POST /v1/servers/" + sampleServer + "/whitelist")
	if !strings.Contains(body, `"uuid":"5c4d3e2f-1a0b-9c8d-7e6f-5a4b3c2d1e0f"`) || !strings.Contains(body, `"actor":"invite:`+id+`"`) {
		t.Fatalf("the agent adds them by UUID, as the invite: %s", body)
	}

	e.reply("GET", "/v1/servers/"+sampleServer+"/whitelist", `[{"name":"PixelPia","uuid":"5c4d3e2f-1a0b-9c8d-7e6f-5a4b3c2d1e0f"},{"name":"Steve"}]`)
	var list []map[string]any
	if st := e.get(t, "/api/servers/"+sampleServer+"/whitelist", own.cookie, &list); st != 200 || len(list) != 2 || list[1]["joined"] != nil {
		t.Fatalf("allowlist: %d %v", st, list)
	}
	if joined, _ := list[0]["joined"].(map[string]any); !strings.Contains(joined["text"].(string), "Discord crew") || joined["at"] == nil {
		t.Fatalf("the allowlist says how and when they got in: %v", list[0])
	}
	var links invitesBody
	if st := e.get(t, "/api/servers/"+sampleServer+"/invites", own.cookie, &links); st != 200 || len(links.Invites) != 1 || links.Invites[0].Uses != 1 || *links.Invites[0].UsesLeft != 1 || links.Link.Friendly {
		t.Fatalf("the link counts the use: %d %+v", st, links)
	}

	if r := e.public(t, "redeem", codeBody(code, "name", "mara_k")); r.status != 200 {
		t.Fatalf("the second friend: %d %v", r.status, r.body)
	}
	r = e.public(t, "redeem", codeBody(code, "name", "tobi_k"))
	if r.status != http.StatusGone || r.body["code"] != invites.CodeUsedUp || strings.Contains(r.body["hint"].(string), "admin") {
		t.Fatalf("a third friend on a link for two: %d %v", r.status, r.body)
	}
	if n := e.hitCount("POST /v1/servers/" + sampleServer + "/whitelist"); n != 2 {
		t.Fatalf("the agent added %d friends", n)
	}

	id2, code2 := friendInvite(t, e.env, own, `{"label":"","expiry":"until_turned_off","unlimited":true,"approval":"right_away"}`)
	if r := e.public(t, "preview", codeBody(code2)); r.status != 200 {
		t.Fatalf("a link until turned off: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/servers/"+sampleServer+"/invites/"+id2, "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("turn off: %d %v", r.status, r.body)
	}
	if r := e.public(t, "preview", codeBody(code2)); r.status != http.StatusNotFound || r.body["code"] != invites.CodeNotWorking {
		t.Fatalf("a link that was turned off: %d %v", r.status, r.body)
	}

	// Seven days on, the first link reads as expired, with no name in it.
	e.clock.add(8 * 24 * time.Hour)
	r = e.public(t, "preview", codeBody(code))
	if r.status != http.StatusGone || r.body["code"] != invites.CodeUsedUp && r.body["code"] != invites.CodeExpired {
		t.Fatalf("an old link: %d %v", r.status, r.body)
	}
	if strings.Contains(e.log.String(), code) || strings.Contains(e.log.String(), code2) {
		t.Fatal("an invite code is in the log")
	}
}

// Friends who open a link for one at the same moment can't all take its
// last use: one gets in, and the others read that it's used up.
func TestTheLastUseGoesToOneFriend(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	id, code := friendInvite(t, e.env, own, `{"label":"","expiry":"1d","maxUses":1,"approval":"right_away"}`)
	names := []string{"PixelPia", "mara_k", "tobi_k"}
	e.together.Store(int32(len(names)))
	statuses := make([]int, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			req, _ := http.NewRequest("POST", e.ts.URL+"/api/public/join/redeem", strings.NewReader(codeBody(code, "name", name)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", e.ts.URL)
			req.Header.Set("X-Requested-With", "playkeeper")
			res, err := e.ts.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			res.Body.Close()
			statuses[i] = res.StatusCode
		})
	}
	wg.Wait()
	in, gone := 0, 0
	for _, st := range statuses {
		switch st {
		case http.StatusOK:
			in++
		case http.StatusGone:
			gone++
		}
	}
	if in != 1 || gone != len(names)-1 {
		t.Fatalf("three friends racing for one use: %v", statuses)
	}
	if n := e.hitCount("POST /v1/servers/" + sampleServer + "/whitelist"); n != 1 {
		t.Fatalf("the agent added %d friends with a link for one", n)
	}
	if inv, _ := e.srv.inviteByID(id); inv.Uses != 1 {
		t.Fatalf("the link counts %d uses", inv.Uses)
	}
}

// A link that waits for a yes makes one join request per player, tells
// Discord, and holds a use until someone answers.
func TestJoinRequestsWaitForAYes(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	id, code := friendInvite(t, e.env, own, `{"label":"School friends","expiry":"30d","maxUses":3,"approval":"after_yes"}`)
	if r := e.public(t, "preview", codeBody(code)); r.status != 200 || r.body["approval"] != "after_yes" {
		t.Fatalf("preview: %d %v", r.status, r.body)
	}
	for range 2 {
		r := e.public(t, "redeem", codeBody(code, "name", "PixelPia"))
		steps, _ := r.body["steps"].([]any)
		first := map[string]any{}
		if len(steps) > 0 {
			first, _ = steps[0].(map[string]any)
		}
		if r.status != 200 || r.body["waiting"] != true || first["key"] != "invite.join.waitAnyone" {
			t.Fatalf("asking to join: %d %v", r.status, r.body)
		}
	}
	if n := e.hitCount("POST /v1/servers/" + sampleServer + "/whitelist"); n != 0 {
		t.Fatalf("nobody said yes, yet the agent added %d", n)
	}
	if n := e.hitCount("POST /v1/discord/notify"); n != 1 {
		t.Fatalf("Discord heard about %d requests", n)
	}
	if body := e.agentBody("POST /v1/discord/notify"); !strings.Contains(body, `"kind":"join_requested"`) || !strings.Contains(body, `"player":"PixelPia"`) || strings.Contains(body, "School friends") {
		t.Fatalf("the Discord alert: %s", body)
	}
	var reqs []requestView
	if st := e.get(t, "/api/servers/"+sampleServer+"/join-requests", own.cookie, &reqs); st != 200 || len(reqs) != 1 {
		t.Fatalf("one request for one player: %d %+v", st, reqs)
	}
	if n := reqs[0].Notice; n.Title.Text != "PixelPia wants to join" || !strings.Contains(n.Detail.Text, "School friends") {
		t.Fatalf("the notice: %+v", n)
	}
	pia := reqs[0].Request.ID
	approve := "/api/servers/" + sampleServer + "/join-requests/" + pia + "/approve"
	if r := e.do(t, "POST", approve, `{}`, own.auth()); r.status != 200 || r.body["origin"] == nil {
		t.Fatalf("let in: %d %v", r.status, r.body)
	}
	if body := e.agentBody("POST /v1/servers/" + sampleServer + "/whitelist"); !strings.Contains(body, `"actor":"admin"`) {
		t.Fatalf("whoever said yes adds them: %s", body)
	}
	if r := e.do(t, "POST", approve, `{}`, own.auth()); r.status != http.StatusConflict || r.body["code"] != invites.CodeRequestDecided {
		t.Fatalf("answering twice: %d %v", r.status, r.body)
	}

	e.public(t, "redeem", codeBody(code, "name", "mara_k"))
	e.get(t, "/api/servers/"+sampleServer+"/join-requests", own.cookie, &reqs)
	if len(reqs) != 1 || reqs[0].Request.PlayerName != "mara_k" {
		t.Fatalf("the next request: %+v", reqs)
	}
	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/join-requests/"+reqs[0].Request.ID+"/decline", `{}`, own.auth()); r.status != 200 {
		t.Fatalf("say no: %d %v", r.status, r.body)
	}
	inv, _ := e.srv.inviteByID(id)
	if inv.Uses != 1 {
		t.Fatalf("a no gives the use back: %d uses", inv.Uses)
	}

	// Someone on the allowlist already just gets the address.
	e.reply("GET", "/v1/servers/"+sampleServer+"/whitelist", `[{"name":"tobi_k","uuid":"41234567-89ab-cdef-0123-456789abcdef"}]`)
	if r := e.public(t, "redeem", codeBody(code, "name", "tobi_k")); r.status != 200 || r.body["waiting"] != nil {
		t.Fatalf("a player on the allowlist: %d %v", r.status, r.body)
	}
	e.get(t, "/api/servers/"+sampleServer+"/join-requests", own.cookie, &reqs)
	if len(reqs) != 0 {
		t.Fatalf("no request for a player on the allowlist: %+v", reqs)
	}

	// A moderator of another server can't answer this one's requests.
	e.public(t, "redeem", codeBody(code, "name", "mara_k"))
	e.get(t, "/api/servers/"+sampleServer+"/join-requests", own.cookie, &reqs)
	mod := addMember(t, e.env, "mo", invites.RoleModerator, otherServer)
	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/join-requests/"+reqs[0].Request.ID+"/approve", `{}`, mod.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a moderator of another server: %d %v", r.status, r.body)
	}
	if strings.Contains(e.log.String(), code) {
		t.Fatal("the invite code is in the log")
	}
}

// A "Let in" the agent couldn't carry out puts the request back only as the
// approve left it. When its link was turned off meanwhile, by a revoke or by
// removing the member who made it, or the request was decided another way,
// it stays decided, and the friend can't be let in through the dead link.
func TestAFailedApprovePutsBackOnlyTheRequestItLeft(t *testing.T) {
	whitelist := "/v1/servers/" + sampleServer + "/whitelist"
	for _, c := range []struct {
		name string
		// meanwhile runs while the panel waits for the agent to add the
		// friend, and gives the status of what it did.
		meanwhile func(e joinEnv, own, mod member, inviteID, requestID string) int
		want      string
	}{
		{name: "nothing else happens", want: "pending"},
		{name: "the link is turned off", want: "declined", meanwhile: func(e joinEnv, own, _ member, inviteID, _ string) int {
			return e.doAside("DELETE", "/api/servers/"+sampleServer+"/invites/"+inviteID, "", own.auth())
		}},
		{name: "the member who made the link is removed", want: "declined", meanwhile: func(e joinEnv, own, mod member, _, _ string) int {
			return e.doAside("DELETE", mod.path(), "", own.auth())
		}},
		{name: "the request is decided another way", want: "declined", meanwhile: func(e joinEnv, _, _ member, _, requestID string) int {
			if _, err := e.srv.db.Exec(`UPDATE join_requests SET state = 'declined' WHERE id = ?`, requestID); err != nil {
				return 0
			}
			return http.StatusOK
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newJoinEnv(t)
			own := owner(t, e.env)
			mod := addMember(t, e.env, "mo", invites.RoleModerator, sampleServer)
			inviteID, code := friendInvite(t, e.env, mod, `{"label":"Crew","expiry":"30d","maxUses":3,"approval":"after_yes"}`)
			if r := e.public(t, "redeem", codeBody(code, "name", "PixelPia")); r.status != 200 || r.body["waiting"] != true {
				t.Fatalf("asking to join: %d %v", r.status, r.body)
			}
			var reqs []requestView
			if st := e.get(t, "/api/servers/"+sampleServer+"/join-requests", own.cookie, &reqs); st != 200 || len(reqs) != 1 {
				t.Fatalf("the request: %d %+v", st, reqs)
			}
			id := reqs[0].Request.ID
			e.replyStatus("POST", whitelist, http.StatusInternalServerError, `{"error":"Docker is not responding."}`)
			var meanwhile atomic.Int32
			if c.meanwhile != nil {
				e.beforeAgentReply("POST", whitelist, func() { meanwhile.Store(int32(c.meanwhile(e, own, mod, inviteID, id))) })
			}
			approve := "/api/servers/" + sampleServer + "/join-requests/" + id + "/approve"
			if r := e.do(t, "POST", approve, `{}`, own.auth()); r.status < 500 {
				t.Fatalf("let in, with the agent failing: %d %v", r.status, r.body)
			}
			if st := meanwhile.Load(); c.meanwhile != nil && (st < 200 || st > 299) {
				t.Fatalf("what happened meanwhile failed: %d", st)
			}
			var state string
			if err := e.srv.db.QueryRow(`SELECT state FROM join_requests WHERE id = ?`, id).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != c.want {
				t.Fatalf("after the failed let in, the request is %s, want %s", state, c.want)
			}
			if c.want == "pending" {
				return
			}
			if r := e.do(t, "POST", approve, `{}`, own.auth()); r.status != http.StatusConflict || r.body["code"] != invites.CodeRequestDecided {
				t.Fatalf("letting the friend in afterwards: %d %v", r.status, r.body)
			}
			if n := e.hitCount("POST " + whitelist); n != 1 {
				t.Fatalf("the agent was asked to add the friend %d times, want the one that failed", n)
			}
		})
	}
}

// A team invite makes an account with the role and servers it gives, which
// is a member, never an owner (M5), and works once. The page never names
// the inviter (M4).
func TestTeamInvitesMakeMembers(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	for i, role := range []string{invites.RoleViewer, invites.RoleModerator, invites.RoleAdmin} {
		r := e.do(t, "POST", "/api/team/invites", `{"role":"`+role+`","servers":{"servers":["bcdefghjkm"]}}`, own.auth())
		path, _ := r.body["path"].(string)
		code, _ := strings.CutPrefix(path, invites.JoinPath+"/")
		if r.status != http.StatusCreated || !invites.WellFormed(code) {
			t.Fatalf("%s invite: %d %v", role, r.status, r.body)
		}
		var team teamBody
		e.get(t, "/api/team", own.cookie, &team)
		if len(team.Invites) != 1 || team.Invites[0].Path != "" || team.Invites[0].Role != role {
			t.Fatalf("the Team page lists the unused link without its code: %+v", team.Invites)
		}
		r = e.public(t, "preview", codeBody(code))
		names, _ := r.body["serverNames"].([]any)
		if r.status != 200 || r.body["kind"] != "member" || r.body["role"] != role || r.body["inviter"] != "" || r.body["team"] != nil || len(names) != 1 || names[0] != "Creative" {
			t.Fatalf("%s preview: %d %v", role, r.status, r.body)
		}
		name := []string{"vic", "mara", "ada"}[i]
		r = e.public(t, "accept", codeBody(code, "username", name, "password", "member password 1"))
		if r.status != 200 || r.cookie == "" {
			t.Fatalf("%s accept: %d %v", role, r.status, r.body)
		}
		user, _ := r.body["user"].(map[string]any)
		acc, _ := r.body["access"].(map[string]any)
		if user["role"] != invites.InstallMember || acc["role"] != role {
			t.Fatalf("%s account: %v", role, r.body)
		}
		requires, _ := r.body["requires"].([]any)
		if (role == invites.RoleAdmin) != (len(requires) == 1) || (role == invites.RoleAdmin) != (acc["needsTwoFactor"] == true) {
			t.Fatalf("%s: admins must turn on two-factor sign-in: %v", role, r.body)
		}
		var installRole, servers string
		e.srv.db.QueryRow(`SELECT u.role, m.servers FROM users u JOIN project_members m ON m.user_id = u.id WHERE u.username = ?`, name).Scan(&installRole, &servers)
		if installRole != invites.InstallMember || servers != otherServer {
			t.Fatalf("%s stored as %s of %q", role, installRole, servers)
		}
		m := member{cookie: r.cookie, csrf: r.body["csrfToken"].(string)}
		if st := e.do(t, "GET", "/api/servers/"+otherServer, "", m.auth()).status; st != 200 {
			t.Fatalf("%s uses their server: %d", role, st)
		}
		if st := e.do(t, "GET", "/api/servers/"+sampleServer, "", m.auth()).status; st != http.StatusForbidden {
			t.Fatalf("%s uses another server: %d", role, st)
		}
		r = e.public(t, "accept", codeBody(code, "username", name+"x", "password", "member password 1"))
		if r.status != http.StatusGone || r.body["code"] != invites.CodeUsedUp {
			t.Fatalf("%s link used twice: %d %v", role, r.status, r.body)
		}
		if rows := e.auditRows(t, "invite.accept"); len(rows) != i+1 || !strings.Contains(rows[i], role+" of Creative") || strings.Contains(rows[i], code) {
			t.Fatalf("audit: %v", rows)
		}
	}
	r := e.do(t, "POST", "/api/team/invites", `{"role":"moderator","servers":{"all":true}}`, own.auth())
	code, _ := strings.CutPrefix(r.body["path"].(string), invites.JoinPath+"/")
	if r := e.public(t, "accept", codeBody(code, "username", "ADMIN", "password", "member password 1")); r.status != http.StatusConflict || r.body["code"] != invites.CodeUsernameTaken {
		t.Fatalf("a name taken in another case: %d %v", r.status, r.body)
	}
	if r := e.public(t, "accept", codeBody(code, "username", "sam", "password", "short")); r.status != http.StatusBadRequest || r.body["code"] != invites.CodePassword {
		t.Fatalf("a short password: %d %v", r.status, r.body)
	}
	if r := e.public(t, "redeem", codeBody(code, "name", "PixelPia")); r.status != http.StatusNotFound {
		t.Fatalf("a team link used as a friend link: %d %v", r.status, r.body)
	}
	if r := e.public(t, "accept", codeBody(code, "username", "sam", "password", "member password 1")); r.status != 200 {
		t.Fatalf("after mistakes the link still works: %d %v", r.status, r.body)
	}
	if strings.Contains(e.log.String(), code) || strings.Contains(e.log.String(), "member password 1") {
		t.Fatal("a code or password is in the log")
	}
}

// The invite page and the new member's Home name the team once it has a
// name of its own, and say nothing while it has the default one.
func TestTeamNameOnInviteAndHome(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	var me struct {
		Access accessBody `json:"access"`
	}
	e.get(t, "/api/auth/me", own.cookie, &me)
	if me.Access.Team != "" {
		t.Fatalf("the default name is shown: %q", me.Access.Team)
	}
	if _, err := e.srv.db.Exec(`UPDATE projects SET name = 'Friends'`); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/team/invites", `{"role":"moderator","servers":{"all":true}}`, own.auth())
	code, _ := strings.CutPrefix(r.body["path"].(string), invites.JoinPath+"/")
	if r := e.public(t, "preview", codeBody(code)); r.status != 200 || r.body["team"] != "Friends" {
		t.Fatalf("preview: %d %v", r.status, r.body)
	}
	r = e.public(t, "accept", codeBody(code, "username", "mara", "password", "member password 1"))
	if acc, _ := r.body["access"].(map[string]any); r.status != 200 || acc["team"] != "Friends" {
		t.Fatalf("accept: %d %v", r.status, r.body)
	}
}

// The public invite pages are no-store, logged without their codes, and
// slow down anyone trying codes.
func TestPublicInvitePagesKeepCodesSafe(t *testing.T) {
	e := newJoinEnv(t)
	for _, p := range []string{"/join/" + sampleCode, "/JOIN/" + sampleCode, "/join/" + sampleCode + "/more"} {
		res, _ := e.raw(t, "GET", p, "", nil)
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("the join page at %s: %d %q", p, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
	if res, _ := e.raw(t, "POST", "/join/"+sampleCode, "", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("posting to the join page: %d", res.StatusCode)
	}
	for _, route := range []string{"preview", "lookup", "redeem", "accept"} {
		res, body := e.raw(t, "POST", "/api/public/join/"+route, codeBody(sampleCode, "name", "PixelPia"), map[string]string{"X-Requested-With": "playkeeper"})
		if res.StatusCode != http.StatusNotFound || res.Header.Get("Cache-Control") != "no-store" || !strings.Contains(body, invites.CodeNotWorking) {
			t.Fatalf("%s with a code that opens nothing: %d %q %s", route, res.StatusCode, res.Header.Get("Cache-Control"), body)
		}
	}
	if r := e.public(t, "preview", `{"code":"not a code"}`); r.status != http.StatusNotFound || r.body["code"] != invites.CodeNotWorking {
		t.Fatalf("a malformed code reads the same: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/public/join/preview", codeBody(sampleCode), nil); r.status != http.StatusForbidden {
		t.Fatalf("a public call from another page: %d", r.status)
	}
	log := e.log.String()
	if strings.Contains(log, sampleCode) || !strings.Contains(log, "path=/join/…") || !strings.Contains(log, "path=/api/public/join/…") {
		t.Fatalf("the log shows a code, or more of a public path than its prefix: %s", log)
	}
	if e.lookups.Load() != 0 {
		t.Fatal("a code that opens nothing cost a Mojang lookup")
	}
	limited := false
	for range 70 {
		r := e.public(t, "preview", codeBody(sampleCode))
		if r.status == http.StatusTooManyRequests {
			limited = r.body["code"] == invites.CodeRateLimited
			break
		}
	}
	if !limited {
		t.Fatal("guessing codes from one address is never slowed down")
	}
}

// The invite pages are the public group's, next to the resource packs:
// they answer without a sign-in, keep the invite guard's refusals (an
// expired link says so; a turned-off one reads like an unknown one), and
// get the group's per-address limit. The route table's only open routes
// are health, setup and sign-in with its second step, and everything else
// asks for a sign-in (TestEveryRouteRequiresSessionAndCSRF tries each one).
func TestInvitePagesArePublicAndNothingElse(t *testing.T) {
	e := newJoinEnv(t)
	var prefixes []string
	for _, rt := range e.srv.public.routes {
		prefixes = append(prefixes, rt.prefix)
	}
	if want := []string{packs.PathPrefix, names.AlivePath, share.PathPrefix, "/join/", "/api/public/join/"}; !slices.Equal(prefixes, want) {
		t.Errorf("the public group serves %v, want %v", prefixes, want)
	}
	var open []string
	for _, rt := range e.srv.Routes() {
		if !rt.NeedsSession() {
			open = append(open, rt.Method+" "+rt.Pattern)
		}
	}
	if want := []string{"GET /api/health", "GET /api/setup/status", "POST /api/setup", "POST /api/auth/login", "POST /api/auth/second-factor", "POST /api/auth/second-factor/cancel"}; !slices.Equal(open, want) {
		t.Errorf("the route table opens %v without a sign-in, want %v", open, want)
	}

	own := owner(t, e.env)
	_, code := friendInvite(t, e.env, own, `{"label":"","expiry":"1d","maxUses":1,"approval":"right_away"}`)
	offID, off := friendInvite(t, e.env, own, `{"label":"","expiry":"until_turned_off","unlimited":true,"approval":"right_away"}`)
	if res, _ := e.raw(t, "GET", "/join/"+code, "", nil); res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the join page without a sign-in: %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	if r := e.public(t, "preview", codeBody(off)); r.status != 200 || r.body["kind"] != "player" {
		t.Fatalf("a link until turned off, without a sign-in: %d %v", r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/servers/"+sampleServer+"/invites/"+offID, "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("turn off: %d %v", r.status, r.body)
	}
	unknown := e.public(t, "preview", codeBody(sampleCode))
	turnedOff := e.public(t, "preview", codeBody(off))
	if unknown.status != http.StatusNotFound || unknown.body["code"] != invites.CodeNotWorking || !reflect.DeepEqual(turnedOff, unknown) {
		t.Fatalf("a turned-off link must read like an unknown one: %d %v and %d %v", turnedOff.status, turnedOff.body, unknown.status, unknown.body)
	}
	e.clock.add(2 * 24 * time.Hour)
	if r := e.public(t, "preview", codeBody(code)); r.status != http.StatusGone || r.body["code"] != invites.CodeExpired {
		t.Fatalf("an expired link must say so: %d %v", r.status, r.body)
	}

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/servers/" + sampleServer + "/invites"},
		{"GET", "/api/servers/" + sampleServer + "/join-requests"},
		{"POST", "/api/servers/" + sampleServer + "/join-requests/zyxwvutsrq/approve"},
		{"GET", "/api/team"},
	} {
		if r := e.do(t, c.method, c.path, `{}`, map[string]string{"X-Requested-With": "playkeeper"}); r.status != http.StatusUnauthorized {
			t.Errorf("%s %s without a sign-in: %d, want 401", c.method, c.path, r.status)
		}
	}
	if res, _ := e.raw(t, "GET", "/api/public/join/preview", "", nil); res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET of a join call: %d", res.StatusCode)
	}
	if res, _ := e.raw(t, "POST", "/api/public/join/other", codeBody(code), map[string]string{"X-Requested-With": "playkeeper"}); res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown join call: %d", res.StatusCode)
	}
	if r := e.do(t, "GET", "/api/public/other", "", nil); r.status != http.StatusNotFound || r.body["code"] != api.CodeNotFound {
		t.Errorf("a path next to the join calls: %d %v", r.status, r.body)
	}

	limited := false
	for range 61 {
		if res, _ := e.raw(t, "GET", "/join/"+code, "", nil); res.StatusCode == http.StatusTooManyRequests {
			limited = res.Header.Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Fatal("the join page is not limited per address by the public group")
	}
}
