package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/mcp"
	"github.com/CIYAhq/playkeeper/internal/mcptools"
)

const (
	survivalID = sampleServer
	creativeID = "kjhgfedcba"
	twoServers = `[{"id":"abcdefghjk","slug":"survival","name":"Survival","phase":"online","desired":"running","reachable":true},` +
		`{"id":"kjhgfedcba","slug":"creative","name":"Creative","phase":"stopped","desired":"stopped"}]`
	mcpVersion = "2026-07-28"
)

type mcpAnswer struct {
	status int
	header http.Header
	body   map[string]any
}

// mcpPost sends one request to /mcp as a desktop client would: no Origin
// and no cookie, unless hdr adds them.
func (e *env) mcpPost(t *testing.T, hdr map[string]string, body string) mcpAnswer {
	t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Error(err)
		return mcpAnswer{}
	}
	defer resp.Body.Close()
	out := mcpAnswer{status: resp.StatusCode, header: resp.Header, body: map[string]any{}}
	json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

// mcpRequest sends a stateless 2026-07-28 request.
func (e *env) mcpRequest(t *testing.T, token, method string, params map[string]any) mcpAnswer {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": mcpVersion, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	hdr := map[string]string{"MCP-Protocol-Version": mcpVersion, "Mcp-Method": method}
	if token != "" {
		hdr["Authorization"] = "Bearer " + token
	}
	if name, ok := params["name"].(string); ok && method == "tools/call" {
		hdr["Mcp-Name"] = name
	}
	return e.mcpPost(t, hdr, string(b))
}

type toolAnswer struct {
	text    string
	isError bool
	kind    string
}

func (e *env) callTool(t *testing.T, token, name string, args map[string]any) toolAnswer {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	r := e.mcpRequest(t, token, "tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := r.body["result"].(map[string]any)
	if r.status != http.StatusOK || !ok {
		t.Fatalf("%s: %d %v", name, r.status, r.body)
	}
	var a toolAnswer
	if content, _ := res["content"].([]any); len(content) > 0 {
		a.text, _ = content[0].(map[string]any)["text"].(string)
	}
	a.isError, _ = res["isError"].(bool)
	if meta, ok := res["_meta"].(map[string]any); ok {
		if pe, ok := meta["io.playkeeper/error"].(map[string]any); ok {
			a.kind, _ = pe["kind"].(string)
		}
	}
	return a
}

func (e *env) toolNames(t *testing.T, token string) []string {
	t.Helper()
	r := e.mcpRequest(t, token, "tools/list", nil)
	res, ok := r.body["result"].(map[string]any)
	if r.status != http.StatusOK || !ok {
		t.Fatalf("tools/list: %d %v", r.status, r.body)
	}
	var out []string
	for _, tool := range res["tools"].([]any) {
		out = append(out, tool.(map[string]any)["name"].(string))
	}
	return out
}

func (e *env) newToken(t *testing.T, cookie, csrf, body string) (id, secret string) {
	t.Helper()
	r := e.do(t, "POST", "/api/tokens", body, auth(cookie, csrf))
	if r.status != http.StatusCreated {
		t.Fatalf("create a token with %s: %d %v", body, r.status, r.body)
	}
	return r.body["token"].(map[string]any)["id"].(string), r.body["secret"].(string)
}

// member adds an account that isn't an owner and signs it in.
func (e *env) member(t *testing.T, name string) (cookie, csrf string) {
	t.Helper()
	h, _ := hashPassword("member password 1")
	if _, err := e.srv.db.Exec(`INSERT INTO users(username, password_hash, created_at, password_changed_at, role) VALUES(?, ?, 0, 0, 'member')`, name, h); err != nil {
		t.Fatal(err)
	}
	r := e.do(t, "POST", "/api/auth/login", `{"username":"`+name+`","password":"member password 1"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != http.StatusOK {
		t.Fatalf("member login: %d %v", r.status, r.body)
	}
	return r.cookie, r.body["csrfToken"].(string)
}

func (e *env) tokenList(t *testing.T, cookie string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if r := e.get(t, "/api/tokens", cookie, &out); r != http.StatusOK {
		t.Fatalf("tokens: %d", r)
	}
	return out
}

func revokedBy(t *testing.T, e *env, id string) string {
	t.Helper()
	var by string
	if err := e.srv.db.QueryRow(`SELECT revoked_by FROM api_tokens WHERE id = ?`, id).Scan(&by); err != nil {
		t.Fatal(err)
	}
	return by
}

func TestMCPTakesABearerTokenAndNoBrowser(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	modern := map[string]string{"MCP-Protocol-Version": mcpVersion, "Mcp-Method": "tools/list"}
	with := func(extra map[string]string) map[string]string {
		h := map[string]string{}
		for k, v := range modern {
			h[k] = v
		}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}

	// A signed-in browser's cookie is not a credential here.
	r := e.mcpPost(t, with(map[string]string{"Cookie": cookieName + "=" + cookie, "X-CSRF-Token": csrf}), list)
	if r.status != http.StatusUnauthorized || !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("a cookie and no token: %d %v", r.status, r.body)
	}
	_, secret := e.newToken(t, cookie, csrf, `{"name":"Claude on my laptop","role":"viewer","allServers":true}`)
	if !strings.HasPrefix(secret, tokenPrefix) || len(secret) != len(tokenPrefix)+32 {
		t.Fatalf("token %q", secret)
	}
	// Web pages can't call it, not even this panel's own, token or not.
	for _, origin := range []string{e.ts.URL, "https://evil.example"} {
		for _, hdr := range []map[string]string{
			{"Origin": origin, "Cookie": cookieName + "=" + cookie},
			{"Origin": origin, "Authorization": "Bearer " + secret},
		} {
			if r := e.mcpPost(t, with(hdr), list); r.status != http.StatusForbidden {
				t.Errorf("from a page on %s with %v: %d", origin, hdr, r.status)
			}
		}
	}
	for _, bad := range []string{"pk_mcp_", secret[:len(secret)-1], secret + "x", strings.Replace(secret, "pk_mcp_", "pk_xyz_", 1), "Bearer"} {
		if r := e.mcpPost(t, with(map[string]string{"Authorization": "Bearer " + bad}), list); r.status != http.StatusUnauthorized {
			t.Errorf("token %q: %d", bad, r.status)
		}
	}
	if r := e.mcpPost(t, with(map[string]string{"Authorization": "Bearer " + secret}), list); r.status != http.StatusOK {
		t.Fatalf("a desktop client with the token: %d %v", r.status, r.body)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/mcp", nil)
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("GET /mcp is the endpoint's, not the UI's: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if strings.Contains(e.logs.String(), secret) || strings.Contains(e.logs.String(), secret[len(tokenPrefix):]) {
		t.Fatal("the token is in the logs")
	}
}

func TestATokenDoesWhatItsRoleAllowsAsItself(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	e.reply("POST", "/v1/servers/"+survivalID+"/backups", `{"id":"0123456789abcdef","serverId":"abcdefghjk","kind":"backup","status":"running","startedAt":"2026-09-24T12:00:00Z"}`)
	viewerID, viewer := e.newToken(t, cookie, csrf, `{"name":"Claude on my laptop","role":"viewer","allServers":true}`)
	modID, moderator := e.newToken(t, cookie, csrf, `{"name":"Cursor","role":"moderator","allServers":true}`)
	_, admin := e.newToken(t, cookie, csrf, `{"name":"Owner's agent","role":"admin","allServers":true}`)

	vt, mt, at := e.toolNames(t, viewer), e.toolNames(t, moderator), e.toolNames(t, admin)
	if slices.Contains(vt, "create_backup") || !slices.Contains(vt, "list_servers") || !slices.Contains(mt, "create_backup") ||
		slices.Contains(mt, "run_console_command") || !slices.Contains(at, "run_console_command") {
		t.Fatalf("tools: viewer %v, moderator %v, admin %v", vt, mt, at)
	}
	if a := e.callTool(t, viewer, "list_servers", nil); a.isError || !strings.Contains(a.text, "Survival") || !strings.Contains(a.text, "Creative") {
		t.Fatalf("list_servers: %+v", a)
	}
	if a := e.callTool(t, viewer, "create_backup", map[string]any{"server": "survival"}); a.kind != mcp.KindScopeMissing {
		t.Fatalf("a viewer backs up: %+v", a)
	}
	if e.sawLocally("POST /v1/servers/" + survivalID + "/backups") {
		t.Fatal("a refused call reached the agent")
	}
	if a := e.callTool(t, moderator, "create_backup", map[string]any{"server": "survival"}); a.isError || !strings.Contains(a.text, "Backing up Survival") {
		t.Fatalf("a moderator backs up: %+v", a)
	}
	e.agent.mu.Lock()
	body := e.agent.bodies["POST /v1/servers/"+survivalID+"/backups"]
	e.agent.mu.Unlock()
	if !strings.Contains(body, `"actor":"token:`+modID+`"`) {
		t.Fatalf("the agent heard of the backup as %s", body)
	}

	// What agents did lately, and the logs, show the token's name.
	e.callTool(t, moderator, "get_server_status", map[string]any{"server": creativeID})
	e.callTool(t, moderator, "get_server_status", map[string]any{"server": "Creative"})
	e.callTool(t, moderator, "list_servers", nil)
	var did []map[string]any
	e.get(t, "/api/tokens/activity", cookie, &did)
	if len(did) != 2 || did[0]["tool"] != "get_server_status" || did[0]["serverName"] != "Creative" || did[0]["count"] != float64(2) ||
		did[1]["tool"] != "create_backup" || did[1]["tokenName"] != "Cursor" || did[1]["serverId"] != survivalID {
		t.Fatalf("what agents did: %v", did)
	}
	e.reply("GET", "/v1/audit", `[{"id":7,"ts":"2026-09-24T12:00:00Z","actor":"token:`+modID+`","action":"backup.create","result":"succeeded"},`+
		`{"id":8,"ts":"2026-09-24T12:00:01Z","actor":"cli:alice","action":"server.start","result":"succeeded"}]`)
	var audit []map[string]any
	e.get(t, "/api/audit", cookie, &audit)
	names := map[string]string{}
	for _, a := range audit {
		if a["actorKind"] != nil {
			names[a["actor"].(string)] = a["actorKind"].(string) + " " + a["actorName"].(string)
		}
	}
	if names["token:"+modID] != "token Cursor" || names["cli:alice"] != "cli alice" || names["token:"+viewerID] != "token Claude on my laptop" {
		t.Fatalf("actor names in the audit log: %v", names)
	}
	e.reply("GET", "/v1/activity", `[{"ts":"2026-09-24T12:00:00Z","serverId":"abcdefghjk","kind":"backup","actor":"token:`+modID+`"},{"ts":"2026-09-24T11:00:00Z","kind":"started","actor":"admin"}]`)
	for _, path := range []string{"/api/servers/" + survivalID + "/activity", "/api/machines/" + e.localMachine(t) + "/activity?limit=5"} {
		var feed []map[string]any
		if r := e.get(t, path, cookie, &feed); r != http.StatusOK || len(feed) != 2 || feed[0]["actorName"] != "Cursor" || feed[0]["actorKind"] != "token" || feed[1]["actorName"] != nil {
			t.Fatalf("%s: %d %v", path, r, feed)
		}
	}
	var tokens []map[string]any
	e.get(t, "/api/tokens", cookie, &tokens)
	for _, tok := range tokens {
		if tok["id"] == modID && tok["lastUsedAt"] == nil {
			t.Fatalf("the token's last use: %v", tok)
		}
	}
}

func (e *env) localMachine(t *testing.T) string {
	t.Helper()
	var id string
	if err := e.srv.db.QueryRow(`SELECT id FROM machines WHERE kind = ?`, localKind).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A token made for some servers sees only those: the others look exactly
// like servers that don't exist, and trying one is audited.
func TestATokenForSomeServersSeesOnlyThose(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	id, secret := e.newToken(t, cookie, csrf, `{"name":"Survival bot","role":"moderator","servers":["`+survivalID+`"]}`)
	if a := e.callTool(t, secret, "list_servers", nil); !strings.Contains(a.text, "Survival") || strings.Contains(a.text, "Creative") || strings.Contains(a.text, creativeID) {
		t.Fatalf("list_servers: %+v", a)
	}
	other := e.callTool(t, secret, "get_server_status", map[string]any{"server": "creative"})
	none := e.callTool(t, secret, "get_server_status", map[string]any{"server": "nowhere"})
	if other.kind != "server_not_found" || strings.ReplaceAll(other.text, "creative", "nowhere") != none.text {
		t.Fatalf("another server: %+v; no server: %+v", other, none)
	}
	if e.sawLocally("GET /v1/servers/" + creativeID) {
		t.Fatal("the other server's agent route was called")
	}
	if !e.auditHas(t, tokenActor(id), "mcp.call", "get_server_status", "refused", mcptools.RefusedServer) {
		t.Fatalf("the refusal is not audited: %q", e.auditDetails(t, "mcp.call"))
	}
}

// Finding H1: a token never does more than its account may do now, and one
// made for more than that is revoked as soon as it shows.
func TestATokenNeverOutranksItsAccount(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)

	// A member may only look, so its tokens may only look.
	mc, mcsrf := e.member(t, "friend")
	if r := e.do(t, "POST", "/api/tokens", `{"name":"x","role":"moderator","allServers":true}`, auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Fatalf("a member's moderator token: %d %v", r.status, r.body)
	}
	friendID, friend := e.newToken(t, mc, mcsrf, `{"name":"Friend's Claude","role":"viewer","allServers":true}`)
	if tools := e.toolNames(t, friend); slices.Contains(tools, "start_server") {
		t.Fatalf("a member's token: %v", tools)
	}

	// The owner loses the owner role: tokens above viewer stop at once,
	// whichever way they are used next.
	adminID, admin := e.newToken(t, cookie, csrf, `{"name":"Admin agent","role":"admin","allServers":true}`)
	modID, _ := e.newToken(t, cookie, csrf, `{"name":"Mod agent","role":"moderator","allServers":true}`)
	listedID, _ := e.newToken(t, cookie, csrf, `{"name":"Listed agent","role":"moderator","servers":["`+survivalID+`"]}`)
	_, view := e.newToken(t, cookie, csrf, `{"name":"View agent","role":"viewer","servers":["`+survivalID+`"]}`)
	if a := e.callTool(t, admin, "list_servers", nil); a.isError {
		t.Fatalf("before: %+v", a)
	}
	if _, err := e.srv.db.Exec(`UPDATE users SET role = 'member' WHERE username = 'admin'`); err != nil {
		t.Fatal(err)
	}
	if r := e.mcpRequest(t, admin, "tools/list", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("an admin token of a demoted account at the door: %d", r.status)
	}
	if revokedBy(t, e, adminID) != "playkeeper" {
		t.Fatal("the admin token was not revoked")
	}
	// Inside a call, as when the role changes while one is on its way.
	b := mcpBackend{e.srv}
	if _, err := b.Access(context.Background(), mcp.Principal{ID: tokenActor(modID)}); err != mcptools.ErrRevoked {
		t.Fatalf("a moderator token inside a call: %v", err)
	}
	if revokedBy(t, e, modID) != "playkeeper" || revokedBy(t, e, listedID) != "" {
		t.Fatal("the moderator token was not revoked, or another one was")
	}
	// Listing tokens revokes the rest that outrank their account, and a
	// member's list has only its own.
	var names []string
	for _, tok := range e.tokenList(t, cookie) {
		names = append(names, tok["name"].(string))
	}
	if strings.Join(names, ",") != "View agent" || revokedBy(t, e, listedID) != "playkeeper" {
		t.Fatalf("working tokens: %v", names)
	}
	if !e.auditHas(t, "playkeeper", "token.revoke", "Admin agent", "succeeded", "its account can no longer do everything") {
		t.Fatal("the revocation is not audited")
	}
	if a := e.callTool(t, view, "list_servers", nil); a.isError || strings.Contains(a.text, "Creative") {
		t.Fatalf("a viewer token for one server still works for it: %+v", a)
	}
	if r := e.do(t, "DELETE", "/api/tokens/"+friendID, "", auth(cookie, csrf)); r.status != http.StatusNotFound {
		t.Fatalf("a demoted owner revokes someone else's token: %d", r.status)
	}
}

func TestRevokingATokenEndsItsSessionsAndCalls(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	id, secret := e.newToken(t, cookie, csrf, `{"name":"Claude on my laptop","role":"viewer","allServers":true}`)
	bearer := map[string]string{"Authorization": "Bearer " + secret}
	r := e.mcpPost(t, bearer, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	session := r.header.Get("Mcp-Session-Id")
	if r.status != http.StatusOK || session == "" {
		t.Fatalf("initialize: %d %v", r.status, r.body)
	}
	inSession := map[string]string{"Authorization": "Bearer " + secret, "Mcp-Session-Id": session, "MCP-Protocol-Version": "2025-11-25"}
	e.mcpPost(t, inSession, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	gate := make(chan struct{})
	defer close(gate)
	e.agent.mu.Lock()
	e.agent.gates["GET /v1/servers/"+survivalID] = gate
	e.agent.mu.Unlock()
	done := make(chan mcpAnswer, 1)
	go func() {
		done <- e.mcpPost(t, inSession, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_server_status","arguments":{"server":"survival"}}}`)
	}()
	eventually(t, "the call reaches the agent", func() bool { return e.sawLocally("GET /v1/servers/" + survivalID) })
	if r := e.do(t, "DELETE", "/api/tokens/"+id, "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("revoke: %d %v", r.status, r.body)
	}
	select {
	case r := <-done:
		errBody, _ := r.body["error"].(map[string]any)
		if r.status != http.StatusOK || errBody["message"] != "The request was cancelled." {
			t.Fatalf("the call in progress: %d %v", r.status, r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call in progress went on after the token was revoked")
	}
	if r := e.mcpPost(t, inSession, `{"jsonrpc":"2.0","id":3,"method":"ping"}`); r.status != http.StatusUnauthorized {
		t.Fatalf("the session after revoking: %d", r.status)
	}
	if !e.auditHas(t, "admin", "token.revoke", "Claude on my laptop", "succeeded", "") || len(e.tokenList(t, cookie)) != 0 {
		t.Fatal("revoking is not audited, or the token is still listed")
	}
	if r := e.do(t, "DELETE", "/api/tokens/"+id, "", auth(cookie, csrf)); r.status != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", r.status)
	}
}

func TestTokensRunOutAndResettingAPasswordRevokesThem(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	_, short := e.newToken(t, cookie, csrf, `{"name":"Short","role":"viewer","allServers":true,"days":30}`)
	longID, long := e.newToken(t, cookie, csrf, `{"name":"Long","role":"viewer","allServers":true}`)
	e.clock.add(30*24*time.Hour + time.Second)
	if r := e.mcpRequest(t, short, "tools/list", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("a token that ran out: %d", r.status)
	}
	if r := e.mcpRequest(t, long, "tools/list", nil); r.status != http.StatusOK {
		t.Fatalf("a 60-day token after 30 days: %d", r.status)
	}
	cookie, _ = e.login(t)
	if list := e.tokenList(t, cookie); len(list) != 2 {
		t.Fatalf("tokens that ran out stay listed until revoked: %v", list)
	}
	if err := e.srv.ResetAdmin("admin", "a brand new password"); err != nil {
		t.Fatal(err)
	}
	if r := e.mcpRequest(t, long, "tools/list", nil); r.status != http.StatusUnauthorized || revokedBy(t, e, longID) != "root@host" {
		t.Fatalf("a token after its account's password was reset: %d", r.status)
	}
}

func (e *env) login(t *testing.T) (cookie, csrf string) {
	t.Helper()
	r := e.do(t, "POST", "/api/auth/login", `{"username":"admin","password":"correct horse battery"}`, map[string]string{"X-Requested-With": "playkeeper"})
	if r.status != http.StatusOK {
		t.Fatalf("login: %d %v", r.status, r.body)
	}
	return r.cookie, r.body["csrfToken"].(string)
}

func TestMakingATokenChecksWhatItAsksFor(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"name":"","role":"viewer","allServers":true}`, 400},
		{`{"name":"   ","role":"viewer","allServers":true}`, 400},
		{`{"name":"` + strings.Repeat("x", 41) + `","role":"viewer","allServers":true}`, 400},
		{`{"name":"two\nlines","role":"viewer","allServers":true}`, 400},
		{`{"name":"a","role":"owner","allServers":true}`, 400},
		{`{"name":"a","role":"viewer","allServers":true,"days":7}`, 400},
		{`{"name":"a","role":"viewer"}`, 400},
		{`{"name":"a","role":"viewer","servers":["../etc"]}`, 400},
		{`{"name":"a","role":"viewer","allServers":true,"servers":["abcdefghjk"]}`, 400},
		{`{"name":"a","role":"viewer","allServers":true,"scopes":["owner"]}`, 400},
		{`{"name":"` + strings.Repeat("é", 40) + `","role":"viewer","allServers":true,"days":365}`, 201},
	} {
		if r := e.do(t, "POST", "/api/tokens", tc.body, auth(cookie, csrf)); r.status != tc.status {
			t.Errorf("%s: %d %v, want %d", tc.body, r.status, r.body, tc.status)
		}
	}
	id, secret := e.newToken(t, cookie, csrf, `{"name":"Claude","role":"viewer","servers":["kjhgfedcba","abcdefghjk","kjhgfedcba"]}`)
	for _, tok := range e.tokenList(t, cookie) {
		if tok["id"] == id && (fmt.Sprint(tok["servers"]) != "[abcdefghjk kjhgfedcba]" || tok["allServers"] != false || tok["secret"] != nil) {
			t.Fatalf("listed: %v", tok)
		}
	}
	if r := e.do(t, "POST", "/api/tokens", `{"name":"claude","role":"viewer","allServers":true}`, auth(cookie, csrf)); r.status != http.StatusConflict {
		t.Fatalf("the same name again: %d", r.status)
	}
	for i := len(e.tokenList(t, cookie)); i < maxTokens; i++ {
		e.clock.add(2 * time.Second) // stays under the dashboard's limit on actions per minute
		e.newToken(t, cookie, csrf, fmt.Sprintf(`{"name":"agent %d","role":"viewer","allServers":true}`, i))
	}
	r := e.do(t, "POST", "/api/tokens", `{"name":"one too many","role":"viewer","allServers":true}`, auth(cookie, csrf))
	if r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "20 working tokens") {
		t.Fatalf("token %d: %d %v", maxTokens+1, r.status, r.body)
	}
	if r := e.do(t, "DELETE", "/api/tokens/"+id, "", auth(cookie, csrf)); r.status != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.status)
	}
	e.newToken(t, cookie, csrf, `{"name":"Claude","role":"viewer","allServers":true}`)

	rows, _ := e.srv.db.Query(`SELECT id || name || token_hash || role || servers FROM api_tokens UNION ALL SELECT actor || action || target || detail FROM audit`)
	defer rows.Close()
	for rows.Next() {
		var s string
		rows.Scan(&s)
		if strings.Contains(s, secret) || strings.Contains(s, secret[len(tokenPrefix):]) {
			t.Fatalf("the database has the token: %q", s)
		}
	}
	if !e.auditHas(t, "admin", "token.create", "Claude", "succeeded", "viewer · abcdefghjk, kjhgfedcba · runs out 2026-11-23") {
		t.Fatal("making a token is not audited")
	}
}

// Finding L1: refused calls are audited, but a token that keeps trying
// adds one row per kind of refusal, and one more that counts the rest.
func TestRefusedCallsAreCountedNotEachAudited(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)
	id, viewer := e.newToken(t, cookie, csrf, `{"name":"Viewer","role":"viewer","servers":["`+survivalID+`"]}`)
	actor := tokenActor(id)
	for range 35 {
		e.callTool(t, viewer, "restart_server", map[string]any{"server": "survival"})
	}
	for range 3 {
		e.callTool(t, viewer, "get_server_status", map[string]any{"server": "creative"})
	}
	if got := e.auditDetails(t, "mcp.call"); strings.Join(got, ",") != "denied,rate_limited,server_not_allowed" {
		t.Fatalf("before the log is read: %q", got)
	}
	var audit []map[string]any
	e.get(t, "/api/audit", cookie, &audit)
	want := []string{"denied", "rate_limited", "server_not_allowed",
		"denied · 29 more at 12:00 UTC", "rate_limited · 4 more at 12:00 UTC", "server_not_allowed · 2 more at 12:00 UTC"}
	if got := e.auditDetails(t, "mcp.call"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after: %q", got)
	}
	if !e.auditHas(t, actor, "mcp.call", "restart_server", "refused", "denied · 29 more") {
		t.Fatal("the count has the refusal's actor and tool")
	}
	named := false
	for _, a := range audit {
		named = named || (a["actor"] == actor && a["actorName"] == "Viewer")
	}
	if !named {
		t.Fatal("refusals in the audit log don't show the token's name")
	}

	// Ten minutes on, a refusal starts a new count; one more in between
	// was counted and is written when the next comes.
	e.clock.add(5 * time.Minute)
	e.callTool(t, viewer, "get_server_status", map[string]any{"server": "creative"})
	e.clock.add(5 * time.Minute)
	e.callTool(t, viewer, "get_server_status", map[string]any{"server": "creative"})
	got := e.auditDetails(t, "mcp.call")
	if tail := strings.Join(got[len(want):], ","); tail != "server_not_allowed · 1 more at 12:05 UTC,server_not_allowed" {
		t.Fatalf("a window later: %q", tail)
	}
}

// Accounts cover every server for now, so an account losing a server is only
// reachable through rights.
func TestATokenLosesWhatItsAccountLoses(t *testing.T) {
	some := rights{role: tokenAdmin, servers: []string{survivalID}}
	for _, c := range []struct {
		name   string
		token  rights
		max    rights
		within bool
	}{
		{"a viewer token of a member", rights{role: tokenViewer, all: true}, accountRights(roleMember), true},
		{"a moderator token of a member", rights{role: tokenModerator, all: true}, accountRights(roleMember), false},
		{"a token of an account without a role", rights{role: tokenViewer, all: true}, accountRights(""), false},
		{"a token without a role", rights{all: true}, accountRights(roleOwner), false},
		{"a token for a server its account has", rights{role: tokenViewer, servers: []string{survivalID}}, some, true},
		{"a token for a server its account lost", rights{role: tokenViewer, servers: []string{survivalID, creativeID}}, some, false},
		{"a token for every server of an account with some", rights{role: tokenViewer, all: true}, some, true},
	} {
		if got := c.token.within(c.max); got != c.within {
			t.Errorf("%s: within is %v", c.name, got)
		}
	}
	if got := (rights{role: tokenViewer, all: true}).effective(some); got.role != tokenViewer || got.all || !slices.Equal(got.servers, some.servers) {
		t.Fatalf("a token for every server may use more than its account's servers: %+v", got)
	}
	listed := rights{role: tokenModerator, servers: []string{survivalID}}
	if got := listed.effective(accountRights(roleOwner)); got.all || !slices.Equal(got.servers, listed.servers) {
		t.Fatalf("a token for some servers may use more than those: %+v", got)
	}
}
