package panel

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

// addCreator adds a creator with the allowance, their Admin rights
// confirmed, and signs them in.
func addCreator(t *testing.T, e *env, name string, al invites.Allowance) member {
	t.Helper()
	m := addAdmin(t, e, name, "")
	if _, err := e.srv.db.Exec(`UPDATE project_members SET allowance_servers = ?, allowance_memory_mb = ? WHERE user_id = ?`, al.Servers, al.MemoryMB, m.id); err != nil {
		t.Fatal(err)
	}
	return m
}

// creatorAgent answers the server list and creates servers as the
// dashboard's machine would, starting with the owner's Survival at 4 GB.
type creatorAgent struct {
	mu      sync.Mutex
	servers []api.ServerStatus
	ids     []string
}

func newCreatorAgent(e *env, ids ...string) *creatorAgent {
	c := &creatorAgent{servers: []api.ServerStatus{{ID: sampleServer, Name: "Survival", Config: &api.ServerConfig{MemoryMB: 4096}}}, ids: ids}
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	e.agent.answers["GET /v1/servers"] = func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		json.NewEncoder(w).Encode(c.servers)
	}
	e.agent.answers["POST /v1/servers"] = func(w http.ResponseWriter, _ *http.Request) {
		e.agent.mu.Lock()
		raw := e.agent.lastBody["POST /v1/servers"]
		e.agent.mu.Unlock()
		var req api.CreateServerRequest
		json.Unmarshal([]byte(raw), &req)
		c.mu.Lock()
		defer c.mu.Unlock()
		id := c.ids[0]
		c.ids = c.ids[1:]
		c.servers = append(c.servers, api.ServerStatus{ID: id, Name: req.Name, Config: &api.ServerConfig{MemoryMB: req.MemoryMB}})
		json.NewEncoder(w).Encode(map[string]string{"id": "0123456789abcdef", "serverId": id})
	}
	e.agent.replies["GET /v1/catalog"] = `{"memoryOptionsMB":[2048,4096,8192],"recommendedMemoryMB":8192,"maxMemoryMB":24576,"memoryFreeMB":24576,"servers":[]}`
	return c
}

// A creator creates servers on the dashboard's machine inside their
// allowance: each joins their servers, their memory changes and restores
// stay inside it, and they delete only the servers they created. An admin
// of some servers without an allowance still can't create any.
func TestCreatorsCreateTheirOwnServersInsideTheirAllowance(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	newCreatorAgent(e.env, "cafebabe23", "deadbeef45")
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 6144})
	lena := addAdmin(t, e.env, "lena", sampleServer)
	mid := machineID(t, e.env)
	create := func(m member, body string) resp {
		return e.do(t, "POST", "/api/machines/"+mid+"/servers", body, m.auth())
	}
	options := func() []any {
		var c map[string]any
		e.get(t, "/api/machines/"+mid+"/catalog", alex.cookie, &c)
		opts, _ := c["memoryOptionsMB"].([]any)
		return opts
	}

	if r := create(lena, `{"name":"lena","acceptEula":true,"memoryMB":2048}`); r.status != http.StatusForbidden {
		t.Fatalf("an admin of one server without an allowance creates a server: %d %v", r.status, r.body)
	}
	if opts := options(); !slices.Equal(opts, []any{float64(2048), float64(4096)}) {
		t.Fatalf("alex's memory choices before creating: %v", opts)
	}
	for _, body := range []string{`{"name":"alex","acceptEula":true}`, `{"name":"alex","acceptEula":true,"memoryMB":8192}`} {
		if r := create(alex, body); r.status != http.StatusBadRequest && r.status != http.StatusConflict {
			t.Fatalf("alex creates %s: %d %v", body, r.status, r.body)
		}
	}
	if r := create(alex, `{"name":"alex","acceptEula":true,"memoryMB":4096}`); r.status != http.StatusOK || r.body["serverId"] != "cafebabe23" {
		t.Fatalf("alex creates a server inside the allowance: %d %v", r.status, r.body)
	}
	var servers, created string
	e.srv.db.QueryRow(`SELECT m.servers, c.server_id FROM project_members m JOIN creator_servers c ON c.user_id = m.user_id WHERE m.user_id = ?`, alex.id).Scan(&servers, &created)
	if servers != "cafebabe23" || created != "cafebabe23" {
		t.Fatalf("alex's servers %q, created %q", servers, created)
	}
	if st := e.do(t, "GET", "/api/servers/cafebabe23", "", alex.auth()).status; st != http.StatusOK {
		t.Fatalf("alex uses the server they created: %d", st)
	}
	if r := create(alex, `{"name":"two","acceptEula":true,"memoryMB":2048}`); r.status != http.StatusConflict {
		t.Fatalf("a second server past the allowance, though its memory fits: %d %v", r.status, r.body)
	}
	if opts := options(); !slices.Equal(opts, []any{float64(2048)}) {
		t.Fatalf("memory choices with 2 GB of the allowance left: %v", opts)
	}

	if _, err := e.srv.db.Exec(`UPDATE project_members SET servers = ? WHERE user_id = ?`, sampleServer+",cafebabe23", alex.id); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, body string
		status   int
	}{
		{"cafebabe23", `{"memoryMB":8192}`, http.StatusConflict},
		{"cafebabe23", `{"memoryMB":2048}`, http.StatusOK},
		{"cafebabe23", `{"motd":"hi"}`, http.StatusOK},
		{sampleServer, `{"memoryMB":8192}`, http.StatusConflict},
		{sampleServer, `{"memoryMB":4096}`, http.StatusOK},
	} {
		if r := e.do(t, "POST", "/api/servers/"+tc.id+"/settings", tc.body, alex.auth()); r.status != tc.status {
			t.Fatalf("alex changes %s's settings to %s: %d %v", tc.id, tc.body, r.status, r.body)
		}
	}
	e.agent.mu.Lock()
	e.agent.replies["GET /v1/restore/0123456789abcdef"] = `{"id":"0123456789abcdef","serverId":"cafebabe23","memoryMB":8192}`
	e.agent.mu.Unlock()
	restore := "/api/machines/" + mid + "/restore/0123456789abcdef/apply"
	if r := e.do(t, "POST", restore, `{}`, alex.auth()); r.status != http.StatusConflict {
		t.Fatalf("a restore bringing back 8 GB: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", restore, `{"memoryMB":4096}`, alex.auth()); r.status != http.StatusOK {
		t.Fatalf("a restore at 4 GB: %d %v", r.status, r.body)
	}

	if r := e.do(t, "POST", "/api/servers/"+sampleServer+"/delete", "", alex.auth()); r.status != http.StatusForbidden {
		t.Fatalf("alex deletes the owner's server: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", "/api/servers/cafebabe23/delete", "", alex.auth()); r.status != http.StatusOK {
		t.Fatalf("alex deletes the server they created: %d %v", r.status, r.body)
	}
	if r := create(own, `{"name":"owner","acceptEula":true,"memoryMB":8192}`); r.status != http.StatusOK || r.body["serverId"] != "deadbeef45" {
		t.Fatalf("the owner creates a server: %d %v", r.status, r.body)
	}
}

// Two creates at once can't both fit an allowance with room for one: the
// second waits for the first and finds the allowance used.
func TestTwoCreatesAtOnceCantBothFitTheAllowance(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	newCreatorAgent(e.env, "cafebabe23", "deadbeef45")
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	path := "/api/machines/" + machineID(t, e.env) + "/servers"
	second := make(chan resp, 1)
	var once sync.Once
	e.agent.mu.Lock()
	e.agent.before["POST /v1/servers"] = func() {
		once.Do(func() {
			go func() {
				second <- e.do(t, "POST", path, `{"name":"two","acceptEula":true,"memoryMB":2048}`, alex.auth())
			}()
			time.Sleep(200 * time.Millisecond)
		})
	}
	e.agent.mu.Unlock()
	first := e.do(t, "POST", path, `{"name":"one","acceptEula":true,"memoryMB":2048}`, alex.auth())
	r := <-second
	if first.status != http.StatusOK || r.status != http.StatusConflict {
		t.Fatalf("two creates at once: %d %v, then %d %v", first.status, first.body, r.status, r.body)
	}
}

// A creator pre-generates at most 2,500 blocks around spawn, as the terms
// say: the larger sizes are neither offered nor started. The owner's are
// unchanged.
func TestCreatorsPreGenerateUpTo2500Blocks(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	newCreatorAgent(e.env, "cafebabe23")
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	if r := e.do(t, "POST", "/api/machines/"+machineID(t, e.env)+"/servers", `{"name":"alex","acceptEula":true,"memoryMB":4096}`, alex.auth()); r.status != http.StatusOK {
		t.Fatalf("alex creates a server: %d %v", r.status, r.body)
	}
	e.agent.mu.Lock()
	e.agent.replies["GET /v1/servers/cafebabe23/pregen"] = `{"state":"idle","presets":[{"id":"small","radius":1000},{"id":"medium","radius":2500},{"id":"large","radius":5000},{"id":"huge","radius":10000}]}`
	e.agent.mu.Unlock()
	presets := func(m member) []string {
		var v struct {
			Presets []api.PregenPreset `json:"presets"`
		}
		e.get(t, "/api/servers/cafebabe23/pregen", m.cookie, &v)
		var ids []string
		for _, p := range v.Presets {
			ids = append(ids, p.ID)
		}
		return ids
	}
	if got := presets(alex); !slices.Equal(got, []string{"small", "medium"}) {
		t.Fatalf("alex's sizes: %v", got)
	}
	if got := presets(own); len(got) != 4 {
		t.Fatalf("the owner's sizes: %v", got)
	}
	start := func(m member, preset string) int {
		return e.do(t, "POST", "/api/servers/cafebabe23/pregen/start", `{"preset":"`+preset+`","pauseForPlayers":true}`, m.auth()).status
	}
	for preset, want := range map[string]int{"large": http.StatusForbidden, "huge": http.StatusForbidden, "medium": http.StatusOK} {
		if st := start(alex, preset); st != want {
			t.Fatalf("alex starts %s: %d, want %d", preset, st, want)
		}
	}
	if st := start(own, "huge"); st != http.StatusOK {
		t.Fatalf("the owner starts huge: %d", st)
	}
}
