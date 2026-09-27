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

	"github.com/CIYAhq/playkeeper/internal/invites"
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

// member adds a viewer of every server to the team and signs it in.
func (e *env) member(t *testing.T, name string) (cookie, csrf string) {
	t.Helper()
	m := addMember(t, e, name, invites.RoleViewer, "*")
	return m.cookie, m.csrf
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
	body := e.agent.lastBody["POST /v1/servers/"+survivalID+"/backups"]
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
// made for more than that is revoked as soon as it shows, whichever way it
// is used next.
func TestATokenNeverOutranksItsAccount(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.setup(t)
	e.reply("GET", "/v1/servers", twoServers)

	// A viewer's tokens may only look.
	mc, mcsrf := e.member(t, "friend")
	if r := e.do(t, "POST", "/api/tokens", `{"name":"x","role":"moderator","allServers":true}`, auth(mc, mcsrf)); r.status != http.StatusForbidden {
		t.Fatalf("a viewer's moderator token: %d %v", r.status, r.body)
	}
	friendID, friend := e.newToken(t, mc, mcsrf, `{"name":"Friend's Claude","role":"viewer","allServers":true}`)
	if tools := e.toolNames(t, friend); slices.Contains(tools, "start_server") {
		t.Fatalf("a viewer's token: %v", tools)
	}
	insideID, _ := e.newToken(t, mc, mcsrf, `{"name":"Inside a call","role":"viewer","allServers":true}`)
	listedID, _ := e.newToken(t, mc, mcsrf, `{"name":"Listed","role":"viewer","allServers":true}`)
	raise := func(id string) {
		t.Helper()
		if _, err := e.srv.db.Exec(`UPDATE api_tokens SET role = 'moderator' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}

	// At the door.
	raise(friendID)
	if r := e.mcpRequest(t, friend, "tools/list", nil); r.status != http.StatusUnauthorized || revokedBy(t, e, friendID) != "playkeeper" {
		t.Fatalf("a token made for more than its account, at the door: %d", r.status)
	}
	if !e.auditHas(t, "playkeeper", "token.revoke", "Friend's Claude", "succeeded", "its account can no longer do everything") {
		t.Fatal("the revocation is not audited")
	}
	// Inside a call, as when it changes while one is on its way.
	raise(insideID)
	if _, err := (mcpBackend{e.srv}).Access(context.Background(), mcp.Principal{ID: tokenActor(insideID)}); err != mcptools.ErrRevoked || revokedBy(t, e, insideID) != "playkeeper" {
		t.Fatalf("a token made for more than its account, inside a call: %v", err)
	}
	// Listing tokens revokes the rest.
	raise(listedID)
	e.tokenList(t, cookie)
	if revokedBy(t, e, listedID) != "playkeeper" {
		t.Fatal("listing tokens left a token made for more than its account")
	}

	// A token for a server its account no longer has stops; one for every
	// server narrows to the account's servers.
	mo := addMember(t, e, "mo", invites.RoleModerator, survivalID)
	lostID, lost := e.newToken(t, mo.cookie, mo.csrf, `{"name":"Survival bot","role":"moderator","servers":["`+survivalID+`"]}`)
	_, every := e.newToken(t, mo.cookie, mo.csrf, `{"name":"Every server bot","role":"moderator","allServers":true}`)
	if r := e.do(t, "POST", "/api/tokens", `{"name":"Creative bot","role":"moderator","servers":["`+creativeID+`"]}`, mo.auth()); r.status != http.StatusForbidden {
		t.Fatalf("a token for a server its account doesn't have: %d %v", r.status, r.body)
	}
	if a := e.callTool(t, lost, "get_server_status", map[string]any{"server": "survival"}); a.isError {
		t.Fatalf("before: %+v", a)
	}
	if _, err := e.srv.db.Exec(`UPDATE project_members SET servers = ? WHERE user_id = ?`, creativeID, mo.id); err != nil {
		t.Fatal(err)
	}
	if r := e.mcpRequest(t, lost, "tools/list", nil); r.status != http.StatusUnauthorized || revokedBy(t, e, lostID) != "playkeeper" {
		t.Fatalf("a token for a server its account lost: %d", r.status)
	}
	if a := e.callTool(t, every, "list_servers", nil); a.isError || strings.Contains(a.text, "Survival") || !strings.Contains(a.text, "Creative") {
		t.Fatalf("a token for every server after its account swapped servers: %+v", a)
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
// What an account's tokens may do follows its project role and servers.
func TestATokenLosesWhatItsAccountLoses(t *testing.T) {
	member := func(role string, servers invites.Scope) access {
		return access{Account: invites.Account{UserID: 2, Name: "mara", InstallRole: roleMember, ProjectRole: role, Servers: servers}}
	}
	owner := access{Account: invites.Account{UserID: 1, Name: "siya", InstallRole: roleOwner}}
	survival := invites.Scope{Servers: []string{survivalID}}
	for _, c := range []struct {
		name   string
		token  rights
		max    rights
		within bool
	}{
		{"a viewer token of a viewer", rights{role: tokenViewer, all: true}, accountRights(member(invites.RoleViewer, invites.AllServers())), true},
		{"a moderator token of a viewer", rights{role: tokenModerator, all: true}, accountRights(member(invites.RoleViewer, invites.AllServers())), false},
		{"a moderator token of a moderator", rights{role: tokenModerator, all: true}, accountRights(member(invites.RoleModerator, invites.AllServers())), true},
		{"an admin token of a moderator", rights{role: tokenAdmin, all: true}, accountRights(member(invites.RoleModerator, invites.AllServers())), false},
		{"an admin token of the owner", rights{role: tokenAdmin, all: true}, accountRights(owner), true},
		{"a token of an account off the team", rights{role: tokenViewer, all: true}, accountRights(member("", invites.AllServers())), false},
		{"a token without a role", rights{all: true}, accountRights(owner), false},
		{"a token for a server its account has", rights{role: tokenViewer, servers: []string{survivalID}}, accountRights(member(invites.RoleViewer, survival)), true},
		{"a token for a server its account lost", rights{role: tokenViewer, servers: []string{survivalID, creativeID}}, accountRights(member(invites.RoleViewer, survival)), false},
		{"a token for every server of an account with some", rights{role: tokenViewer, all: true}, accountRights(member(invites.RoleViewer, survival)), true},
	} {
		if got := c.token.within(c.max); got != c.within {
			t.Errorf("%s: within is %v", c.name, got)
		}
	}
	some := accountRights(member(invites.RoleAdmin, survival))
	if got := (rights{role: tokenViewer, all: true}).effective(some); got.role != tokenViewer || got.all || !slices.Equal(got.servers, some.servers) {
		t.Fatalf("a token for every server may use more than its account's servers: %+v", got)
	}
	listed := rights{role: tokenModerator, servers: []string{survivalID}}
	if got := listed.effective(accountRights(owner)); got.all || !slices.Equal(got.servers, listed.servers) {
		t.Fatalf("a token for some servers may use more than those: %+v", got)
	}
	for grant, want := range map[string]int{roleOwner: 4, invites.RoleAdmin: 3, invites.RoleModerator: 2, invites.RoleViewer: 1, "": 0} {
		if got := grantRank(grant); got != want {
			t.Errorf("grantRank(%q) = %d, want %d", grant, got, want)
		}
	}
}

// toolOutcome is how a tool call came out for a token: refused at the door
// because the token stopped ("gone"), refused by the token's own role
// ("scope") or by its account's ("action"), or it ran.
func (e *env) toolOutcome(t *testing.T, token, tool string) string {
	t.Helper()
	args := map[string]any{}
	for _, tl := range mcptools.Tools(nil) {
		if tl.Name != tool {
			continue
		}
		for _, name := range tl.InputSchema.Required {
			args[name] = map[string]any{"server": "survival", "message": "Back in five", "command": "time set day", "player": "Steve_1",
				"operation": "0123456789abcdef", "source": "modrinth", "project": "chunky"}[name]
		}
	}
	r := e.mcpRequest(t, token, "tools/call", map[string]any{"name": tool, "arguments": args})
	if r.status == http.StatusUnauthorized {
		return "gone"
	}
	res, ok := r.body["result"].(map[string]any)
	if r.status != http.StatusOK || !ok {
		t.Fatalf("%s: %d %v", tool, r.status, r.body)
	}
	meta, _ := res["_meta"].(map[string]any)
	pe, _ := meta["io.playkeeper/error"].(map[string]any)
	switch pe["kind"] {
	case mcp.KindScopeMissing:
		return "scope"
	case mcptools.RefusedAction:
		return "action"
	}
	return "ran"
}

// A token can never do more than its account's role allows now: each tool
// asks permit about the account, as the matching dashboard route does, on
// top of the token's own role and servers. It stops working as soon as its
// account leaves the team or holds a lower role than when it was made, and
// the Team page stops it at once. A role raised later leaves it as it was
// made.
func TestATokenFollowsItsAccountsRole(t *testing.T) {
	e := newEnv(t)
	own := owner(t, e)
	e.reply("GET", "/v1/servers", twoServers)
	tokenOf := func(m member, role string) (id, secret string) {
		t.Helper()
		return e.newToken(t, m.cookie, m.csrf, fmt.Sprintf(`{"name":"%s %d","role":"%s","allServers":true}`, role, m.id, role))
	}
	read := []string{"list_servers", "get_server_status", "read_console", "list_online_players", "list_whitelist", "list_backups", "get_operation", "get_lag_report", "explain_crash"}
	run := []string{"start_server", "stop_server", "restart_server", "send_chat_message", "add_to_whitelist", "remove_from_whitelist", "create_backup"}
	outcomes := func(reads, runs, console, install string) map[string]string {
		out := map[string]string{"run_console_command": console, "install_addon": install}
		for _, tool := range read {
			out[tool] = reads
		}
		for _, tool := range run {
			out[tool] = runs
		}
		return out
	}
	everything := outcomes("ran", "ran", "ran", "ran")
	onlyLooks := outcomes("ran", "scope", "scope", "scope")
	check := func(name, secret string, want map[string]string) {
		t.Helper()
		if len(want) != len(mcptools.Actions()) {
			t.Fatalf("%s: %d tools expected, there are %d", name, len(want), len(mcptools.Actions()))
		}
		for tool, w := range want {
			if got := e.toolOutcome(t, secret, tool); got != w {
				t.Errorf("%s, %s: %s, want %s", name, tool, got, w)
			}
		}
	}
	// A token that stopped is refused at the door, whatever it calls. Each
	// refusal counts toward the door's limit for wrong tokens, so it's
	// asked twice and the limit's minute is let pass.
	stopped := func(name, secret string) {
		t.Helper()
		for _, tool := range []string{"list_servers", "restart_server"} {
			if got := e.toolOutcome(t, secret, tool); got != "gone" {
				t.Errorf("%s, %s: %s, want gone", name, tool, got)
			}
		}
		e.clock.add(time.Minute)
	}

	ownerID, ownerToken := tokenOf(own, tokenAdmin)
	al := addAdmin(t, e, "al", "*")
	_, alToken := tokenOf(al, tokenAdmin)
	ada := addMember(t, e, "ada", invites.RoleAdmin, "*")
	adaID, adaToken := tokenOf(ada, tokenAdmin)
	mo := addMember(t, e, "mo", invites.RoleModerator, "*")
	_, moToken := tokenOf(mo, tokenModerator)
	_, moViewer := tokenOf(mo, tokenViewer)
	vi := addMember(t, e, "vi", invites.RoleViewer, "*")
	_, viToken := tokenOf(vi, tokenViewer)
	for _, row := range []struct {
		name   string
		secret string
		want   map[string]string
	}{
		{"the owner's admin token", ownerToken, everything},
		{"an admin's admin token", alToken, everything},
		// Admin rights wait for two-factor sign-in; until then an admin has a moderator's.
		{"an admin token of an admin without two-factor sign-in", adaToken, outcomes("ran", "ran", "ran", "action")},
		{"a moderator's moderator token", moToken, outcomes("ran", "ran", "scope", "scope")},
		{"a moderator's viewer token", moViewer, onlyLooks},
		{"a viewer's viewer token", viToken, onlyLooks},
	} {
		check(row.name, row.secret, row.want)
	}
	if !e.auditHas(t, tokenActor(adaID), "mcp.call", "install_addon", "refused", mcptools.RefusedAction) {
		t.Errorf("the refusal is not audited: %q", e.auditDetails(t, "mcp.call"))
	}

	// The account's rights, as each tool call sees them.
	for _, c := range []struct {
		name string
		m    member
		acts map[action]bool
	}{
		{"the owner", own, map[action]bool{actView: true, actManageServers: true, actManageBackupCopies: true, actRecoveryKey: true}},
		{"an admin with two-factor sign-in", al, map[action]bool{actManageServers: true, actManageBackupCopies: true, actRecoveryKey: true}},
		{"an admin without it", ada, map[action]bool{actMakeBackups: true, actManageServers: false, actManageBackupCopies: false, actRecoveryKey: false}},
		{"a moderator", mo, map[action]bool{actRunServers: true, actConsole: true, actManageServers: false, actRecoveryKey: false}},
		{"a viewer", vi, map[action]bool{actView: true, actRunServers: false, actMakeBackups: false}},
	} {
		var id string
		if err := e.srv.db.QueryRow(`SELECT id FROM api_tokens WHERE user_id = ? AND revoked_at = 0 LIMIT 1`, c.m.id).Scan(&id); err != nil {
			t.Fatal(err)
		}
		a, err := (mcpBackend{e.srv}).Access(context.Background(), mcp.Principal{ID: tokenActor(id)})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for act, want := range c.acts {
			if got := a.May(string(act)); got != want {
				t.Errorf("%s may %s: %v, want %v", c.name, act, got, want)
			}
		}
	}

	// Taken off the team on the Team page: its tokens stop at once, and the
	// revocation says why.
	if r := e.do(t, "DELETE", vi.path(), "", own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("remove the viewer: %d %v", r.status, r.body)
	}
	stopped("a removed viewer's token", viToken)
	if !e.auditHas(t, "admin", "token.revoke", "viewer "+fmt.Sprint(vi.id), "succeeded", "removed from the team") {
		t.Error("removing a member doesn't revoke their tokens in the audit log")
	}

	// A lower role on the Team page: the moderator's tokens made before stop
	// at once, a viewer token too, though a viewer may hold one.
	if r := e.do(t, "PUT", mo.path(), `{"role":"viewer","servers":{"all":true}}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("lower the moderator: %d %v", r.status, r.body)
	}
	var working int
	if err := e.srv.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked_at = 0`, mo.id).Scan(&working); err != nil || working != 0 {
		t.Fatalf("a lowered role left %d working tokens (%v)", working, err)
	}
	stopped("a demoted moderator's viewer token", moViewer)
	if !e.auditHas(t, "playkeeper", "token.revoke", "viewer "+fmt.Sprint(mo.id), "succeeded", "its role was lowered") {
		t.Error("the revocation doesn't say why")
	}
	_, after := tokenOf(mo, tokenViewer)
	check("a token made after the lower role", after, onlyLooks)

	// A role changed behind the Team page's back is caught at the next use.
	if _, err := e.srv.db.Exec(`UPDATE users SET role = 'member' WHERE id = ?`, own.id); err != nil {
		t.Fatal(err)
	}
	stopped("the admin token of an owner who is now an admin", ownerToken)
	if revokedBy(t, e, ownerID) != "playkeeper" {
		t.Error("the former owner's token was not revoked")
	}

	// A higher role leaves a token as it was made.
	if r := e.do(t, "PUT", mo.path(), `{"role":"moderator","servers":{"all":true}}`, al.auth()); r.status != http.StatusOK {
		t.Fatalf("raise the viewer again: %d %v", r.status, r.body)
	}
	check("a viewer token after its account became a moderator", after, onlyLooks)
}

// Each MCP tool asks permit for the action of the dashboard route that does
// what the tool does.
func TestEveryToolTakesTheActionOfItsDashboardRoute(t *testing.T) {
	routeOf := map[string]string{
		"list_servers":          "GET /api/servers",
		"get_server_status":     "GET /api/servers/{id}",
		"start_server":          "POST /api/servers/{id}/start",
		"stop_server":           "POST /api/servers/{id}/stop",
		"restart_server":        "POST /api/servers/{id}/restart",
		"read_console":          "GET /api/servers/{id}/logs",
		"send_chat_message":     "POST /api/servers/{id}/command",
		"run_console_command":   "POST /api/servers/{id}/command",
		"list_online_players":   "GET /api/servers/{id}",
		"list_whitelist":        "GET /api/servers/{id}/whitelist",
		"add_to_whitelist":      "POST /api/servers/{id}/whitelist",
		"remove_from_whitelist": "DELETE /api/servers/{id}/whitelist/{name}",
		"list_backups":          "GET /api/servers/{id}/backups",
		"create_backup":         "POST /api/servers/{id}/backups",
		"get_operation":         "GET /api/machines/{mid}/operations/{op}",
		"get_lag_report":        "GET /api/servers/{id}/metrics",
		"explain_crash":         "GET /api/servers/{id}/events",
		"install_addon":         "POST /api/servers/{id}/addons/install",
	}
	acts := map[string]action{}
	for _, rt := range newEnv(t).srv.Routes() {
		acts[rt.Method+" "+rt.Pattern] = rt.Act
	}
	tools := mcptools.Actions()
	for tool, act := range tools {
		route, ok := routeOf[tool]
		if !ok {
			t.Errorf("%s: no dashboard route listed for it", tool)
			continue
		}
		want, ok := acts[route]
		if !ok {
			t.Errorf("%s: the dashboard has no route %s", tool, route)
			continue
		}
		if action(act) != want {
			t.Errorf("%s asks permit for %q, but %s asks for %q", tool, act, route, want)
		}
	}
	for tool := range routeOf {
		if _, ok := tools[tool]; !ok {
			t.Errorf("%s is listed but is no tool", tool)
		}
	}
}
