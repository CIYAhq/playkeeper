package panel

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// testAIKey has the shape of an OpenRouter key.
const testAIKey = "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"

const (
	aiKeysSet   = `{"keys":{"openrouter":{"set":true}},"pending":true,"available":true}`
	aiKeysUnset = `{"keys":{"openrouter":{"set":false}},"pending":false,"available":true}`
)

// aiKeyRequests are the AI key routes, each with a request an admin makes.
func aiKeyRequests() [][3]string {
	p := "/api/servers/" + sampleServer + "/ai-keys"
	return [][3]string{
		{"GET", p, ""},
		{"PUT", p + "/openrouter", `{"key":"` + testAIKey + `"}`},
		{"DELETE", p + "/openrouter", ""},
	}
}

// A key spends its owner's money, and whoever may change a server's files
// can add a plugin that reads it, so only admins, with two-factor sign-in
// on as for every Admin right, may see whether one is set, save one or
// remove it.
func TestAIKeysAreForAdmins(t *testing.T) {
	e := newEnv(t)
	ownerCookie, ownerCSRF := e.setup(t)
	viewer := addMember(t, e, "vic", invites.RoleViewer, "*")
	moderator := addMember(t, e, "mo", invites.RoleModerator, "*")
	noFactor := addMember(t, e, "una", invites.RoleAdmin, "*")
	admin := addAdmin(t, e, "ada", "*")
	for _, c := range aiKeyRequests() {
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
		for _, c := range aiKeyRequests() {
			e.clock.add(2 * time.Second)
			if r := e.do(t, c[0], c[1], c[2], h); r.status != http.StatusOK {
				t.Errorf("%s, %s %s: %d %v", who, c[0], c[1], r.status, r.body)
			}
		}
	}
	if hits := e.agentHits(); len(hits) != 2*len(aiKeyRequests()) {
		t.Fatalf("the agent saw %d requests, want %d: %v", len(hits), 2*len(aiKeyRequests()), hits)
	}
}

// Saving and removing keys count against the 30 actions a minute, like
// every other change.
func TestAIKeyChangesAreRateLimited(t *testing.T) {
	e := newEnv(t)
	cookie, csrf := e.setup(t)
	p := "/api/servers/" + sampleServer + "/ai-keys/openrouter"
	for i := range 15 {
		if r := e.do(t, "PUT", p, `{"key":"`+testAIKey+`"}`, auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("save %d: %d %v", i+1, r.status, r.body)
		}
		if r := e.do(t, "DELETE", p, "", auth(cookie, csrf)); r.status != http.StatusOK {
			t.Fatalf("remove %d: %d %v", i+1, r.status, r.body)
		}
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if r := e.do(t, method, p, `{"key":"`+testAIKey+`"}`, auth(cookie, csrf)); r.status != http.StatusTooManyRequests || r.body["code"] != api.CodeRateLimited {
			t.Errorf("%s as the 31st action in a minute: %d %v", method, r.status, r.body)
		}
	}
}

// remoteKeyRequest is what a joined machine's agent got for a key route.
type remoteKeyRequest struct {
	body, actor string
}

// The panel sends a key to the agent of the machine that runs the server,
// the dashboard's own or a joined one, with who saved it, and relays the
// agent's answers and refusals. It keeps the key nowhere: not in its
// database, its audit log or its log, even at debug level.
func TestAnAIKeyGoesOnlyToItsServersMachine(t *testing.T) {
	logs := &syncBuffer{}
	e := newEnvConfig(t, withDomain, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	cookie, csrf := e.setup(t)
	e.reply("GET", "/v1/machine", `{"hostname":"my-vps","agentVersion":"0.4.9"}`)
	e.reply("GET", "/v1/servers", `[{"id":"abcdefghjk","name":"Survival","phase":"online"}]`)
	ra := newRemoteAgent()
	var mu sync.Mutex
	remote := map[string]remoteKeyRequest{}
	for key, answer := range map[string]string{
		"GET /v1/servers/rstuvwxyzq/ai-keys":               aiKeysUnset,
		"PUT /v1/servers/rstuvwxyzq/ai-keys/openrouter":    aiKeysSet,
		"DELETE /v1/servers/rstuvwxyzq/ai-keys/openrouter": aiKeysUnset,
	} {
		ra.handle(key, func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			remote[key] = remoteKeyRequest{body: string(b), actor: r.URL.Query().Get("actor")}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, answer)
		})
	}
	e.joinMachine(t, cookie, csrf, ra)
	var list []map[string]any
	if r := e.get(t, "/api/servers", cookie, &list); r != http.StatusOK || ids(list) != "abcdefghjk rstuvwxyzq" {
		t.Fatalf("servers: %d %v", r, list)
	}
	var answers []string
	call := func(method, path, body string) resp {
		t.Helper()
		h := auth(cookie, csrf)
		if method == "GET" {
			h = auth(cookie, "")
		}
		e.clock.add(2 * time.Second)
		r := e.do(t, method, path, body, h)
		answers = append(answers, fmt.Sprint(r.body))
		return r
	}

	local := "/v1/servers/abcdefghjk/ai-keys"
	e.reply("GET", local, aiKeysUnset)
	e.reply("PUT", local+"/openrouter", aiKeysSet)
	e.reply("DELETE", local+"/openrouter", aiKeysUnset)
	if r := call("GET", "/api/servers/abcdefghjk/ai-keys", ""); r.status != http.StatusOK || fmt.Sprint(r.body) != "map[available:true keys:map[openrouter:map[set:false]] pending:false]" {
		t.Fatalf("the keys: %d %v", r.status, r.body)
	}
	// Who saved it is the signed-in account, whatever the browser says.
	if r := call("PUT", "/api/servers/abcdefghjk/ai-keys/openrouter", `{"key":"`+testAIKey+`","actor":"mallory"}`); r.status != http.StatusOK || r.body["pending"] != true {
		t.Fatalf("save: %d %v", r.status, r.body)
	}
	if req := e.agentRequest(t, "PUT", local+"/openrouter"); len(req.body) != 2 || req.body["key"] != testAIKey || req.body["actor"] != "admin" || len(req.query) != 0 {
		t.Fatalf("the agent was sent %v %v", req.body, req.query)
	}
	if r := call("DELETE", "/api/servers/abcdefghjk/ai-keys/openrouter", ""); r.status != http.StatusOK || fmt.Sprint(r.body["keys"]) != "map[openrouter:map[set:false]]" {
		t.Fatalf("remove: %d %v", r.status, r.body)
	}
	if req := e.agentRequest(t, "DELETE", local+"/openrouter"); req.query.Get("actor") != "admin" || req.body != nil {
		t.Fatalf("the agent was asked to remove it with %v %v", req.query, req.body)
	}

	// The agent's refusals reach the dashboard as they are, and what the
	// panel can't read goes nowhere.
	e.replyStatus("PUT", local+"/openrouter", http.StatusBadRequest,
		`{"error":"That isn't an OpenRouter key: those start with sk-or-.","code":"invalid_request","field":"key","reason":"ai_key_prefix"}`)
	r := call("PUT", "/api/servers/abcdefghjk/ai-keys/openrouter", `{"key":"sk-proj-`+testAIKey[9:]+`"}`)
	if r.status != http.StatusBadRequest || r.body["error"] != "That isn't an OpenRouter key: those start with sk-or-." || r.body["field"] != "key" || r.body["reason"] != "ai_key_prefix" {
		t.Fatalf("a refused key: %d %v", r.status, r.body)
	}
	before := len(e.agentHits())
	for _, body := range []string{`"` + testAIKey + `"`, `{"key":"` + testAIKey + `"`, `["` + testAIKey + `"]`, `{"key":"` + testAIKey + strings.Repeat("a", 64<<10) + `"}`} {
		if r := call("PUT", "/api/servers/abcdefghjk/ai-keys/openrouter", body); r.status != http.StatusBadRequest || r.body["error"] != "Request body must be a JSON object." {
			t.Errorf("the body %.40q: %d %v", body, r.status, r.body)
		}
	}
	if hits := e.agentHits(); len(hits) != before {
		t.Fatalf("bodies the panel couldn't read reached the agent: %v", hits[before:])
	}

	// A joined machine's server: its key goes over the link to that
	// machine alone.
	if r := call("GET", "/api/servers/rstuvwxyzq/ai-keys", ""); r.status != http.StatusOK || fmt.Sprint(r.body["keys"]) != "map[openrouter:map[set:false]]" {
		t.Fatalf("the joined machine's keys: %d %v", r.status, r.body)
	}
	if r := call("PUT", "/api/servers/rstuvwxyzq/ai-keys/openrouter", `{"key":"`+testAIKey+`"}`); r.status != http.StatusOK || r.body["pending"] != true {
		t.Fatalf("save on the joined machine: %d %v", r.status, r.body)
	}
	if r := call("DELETE", "/api/servers/rstuvwxyzq/ai-keys/openrouter", ""); r.status != http.StatusOK {
		t.Fatalf("remove on the joined machine: %d %v", r.status, r.body)
	}
	mu.Lock()
	put, del := remote["PUT /v1/servers/rstuvwxyzq/ai-keys/openrouter"], remote["DELETE /v1/servers/rstuvwxyzq/ai-keys/openrouter"]
	mu.Unlock()
	if put.body != `{"actor":"admin","key":"`+testAIKey+`"}` || del.actor != "admin" || del.body != "" {
		t.Fatalf("the joined machine got %+v and %+v", put, del)
	}
	for _, key := range []string{"PUT /v1/servers/rstuvwxyzq/ai-keys/openrouter", "DELETE /v1/servers/rstuvwxyzq/ai-keys/openrouter"} {
		if actor, ok := ra.saw(key); !ok || actor != "admin" {
			t.Errorf("%s went over the link from %q (%v)", key, actor, ok)
		}
		if e.sawLocally(key) {
			t.Errorf("the dashboard's own agent got %s", key)
		}
	}

	tail := testAIKey[len(testAIKey)-24:]
	if strings.Contains(strings.Join(answers, "\n"), tail) {
		t.Errorf("an answer holds the key: %v", answers)
	}
	if strings.Contains(logs.String(), tail) {
		t.Errorf("the log holds the key:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "ai-keys/openrouter") {
		t.Fatal("the log has none of the requests, so this test no longer reads it")
	}
	if where := panelKeeps(t, e, tail); len(where) > 0 {
		t.Errorf("the panel keeps the key in %v", where)
	}
}

// panelKeeps names the panel's tables that hold s in any column, and the
// database files that hold it, written out or not.
func panelKeeps(t *testing.T, e *env, s string) []string {
	t.Helper()
	var tables []string
	rows, err := e.srv.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	if len(tables) < 10 {
		t.Fatalf("the panel's database has %d tables: %v", len(tables), tables)
	}
	var where []string
	for _, table := range tables {
		if holds(t, e.srv.db, table, s) {
			where = append(where, table)
		}
	}
	for _, name := range []string{"panel.db", "panel.db-wal"} {
		b, err := os.ReadFile(filepath.Join(e.cfg.PanelDir(), name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(b), s) {
			where = append(where, name)
		}
	}
	return where
}

func holds(t *testing.T, db *sql.DB, table, s string) bool {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			text := fmt.Sprint(v)
			if b, ok := v.([]byte); ok {
				text = string(b)
			}
			if strings.Contains(text, s) {
				return true
			}
		}
	}
	return false
}
