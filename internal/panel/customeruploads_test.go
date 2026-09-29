package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/invites"
)

const (
	worldUpload  = "0123456789abcdef"
	backupUpload = "fedcba9876543210"
)

// uploadAgent answers a world upload and a backup upload for a new server,
// both made against the disk limit called limit, as the dashboard's machine
// would, and makes a server from either with the next of c's ids.
func uploadAgent(e *env, c *creatorAgent, limit string) {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	e.agent.replies["POST /v1/world-imports"] = `{"id":"` + worldUpload + `","files":[]}`
	e.agent.statuses["POST /v1/world-imports"] = http.StatusCreated
	e.agent.replies["GET /v1/world-imports/"+worldUpload] = `{"id":"` + worldUpload + `","diskLimit":"` + limit + `","files":[]}`
	e.agent.replies["POST /v1/restore/upload"] = `{"id":"` + backupUpload + `","diskLimit":"` + limit + `","memoryMB":2048}`
	e.agent.replies["GET /v1/restore/"+backupUpload] = `{"id":"` + backupUpload + `","diskLimit":"` + limit + `","memoryMB":2048}`
	made := func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		id := c.ids[0]
		c.ids = c.ids[1:]
		c.servers = append(c.servers, api.ServerStatus{ID: id, Name: "Imported", Config: &api.ServerConfig{MemoryMB: 2048}})
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{"id": "1111111111111111", "serverId": id})
	}
	e.agent.answers["POST /v1/world-imports/"+worldUpload+"/create"] = made
	e.agent.answers["POST /v1/restore/"+backupUpload+"/apply"] = made
}

// lastAsked is the last request the machine got to method and path.
func lastAsked(e *env, method, path string) agentRequest {
	e.agent.mu.Lock()
	defer e.agent.mu.Unlock()
	for i := len(e.agent.reqs) - 1; i >= 0; i-- {
		if r := e.agent.reqs[i]; r.method == method && r.path == path {
			return r
		}
	}
	return agentRequest{}
}

// A creator makes a server from a world they upload: against their disk
// limit, which the dashboard names whatever the browser says, and inside
// their allowance, and the server joins theirs. Another creator can't touch
// their upload, nobody else's upload names a limit, and a creator whose
// allowance is full can't start another.
func TestACreatorMakesAServerFromAWorldTheyUpload(t *testing.T) {
	e := newJoinEnv(t)
	own := owner(t, e.env)
	c := newCreatorAgent(e.env, "cafebabe23")
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	sam := addCreator(t, e.env, "sam", invites.Allowance{Servers: 1, MemoryMB: 4096})
	uploadAgent(e.env, c, accountLimit(alex.id))
	base := "/api/machines/" + machineID(t, e.env) + "/world-imports"
	if r := e.do(t, "POST", base, `{"diskLimit":"account-1"}`, alex.auth()); r.status != http.StatusCreated {
		t.Fatalf("alex opens a world upload: %d %v", r.status, r.body)
	}
	if got := lastAsked(e.env, "POST", "/v1/world-imports").body["diskLimit"]; got != accountLimit(alex.id) {
		t.Fatalf("alex's upload was opened against %v", got)
	}
	if r := e.do(t, "POST", base, `{"diskLimit":"account-1"}`, own.auth()); r.status != http.StatusCreated {
		t.Fatalf("the owner opens a world upload: %d %v", r.status, r.body)
	}
	if got, ok := lastAsked(e.env, "POST", "/v1/world-imports").body["diskLimit"]; ok {
		t.Fatalf("the owner's upload was opened against %v", got)
	}
	imp := base + "/" + worldUpload
	if r := e.do(t, "POST", imp+"/inspect", `{}`, sam.auth()); r.status != http.StatusForbidden {
		t.Fatalf("sam checks alex's upload: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", imp+"/inspect", `{}`, alex.auth()); r.status != http.StatusOK {
		t.Fatalf("alex checks their upload: %d %v", r.status, r.body)
	}
	create := func(m member, body string) resp { return e.do(t, "POST", imp+"/create", body, m.auth()) }
	if r := create(sam, `{"name":"Imported","acceptEula":true,"memoryMB":2048}`); r.status != http.StatusForbidden {
		t.Fatalf("sam makes a server from alex's upload: %d %v", r.status, r.body)
	}
	if r := create(alex, `{"name":"Imported","acceptEula":true,"memoryMB":8192}`); r.status != http.StatusConflict {
		t.Fatalf("alex makes a server past their memory: %d %v", r.status, r.body)
	}
	if r := create(alex, `{"name":"Imported","acceptEula":true,"memoryMB":2048}`); r.status != http.StatusAccepted || r.body["serverId"] != "cafebabe23" {
		t.Fatalf("alex makes a server from their upload: %d %v", r.status, r.body)
	}
	if owned, _ := e.srv.creatorServers(alex.id); !slices.Equal(owned, []string{"cafebabe23"}) {
		t.Fatalf("alex created %v", owned)
	}
	if !slices.Contains(e.agentHits(), "POST /v1/servers/cafebabe23/backup-rules") {
		t.Fatal("alex's new server didn't get a creator's backups")
	}
	if r := e.do(t, "POST", base, `{}`, alex.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "allowance") {
		t.Fatalf("alex opens an upload with their allowance full: %d %v", r.status, r.body)
	}
}

// A creator makes a server from a backup they upload the same way: the
// dashboard names their disk limit, whatever the browser's address says,
// and the server fits their allowance and joins theirs. Another creator
// can't restore it.
func TestACreatorMakesAServerFromABackupTheyUpload(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	c := newCreatorAgent(e.env, "cafebabe23")
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	sam := addCreator(t, e.env, "sam", invites.Allowance{Servers: 1, MemoryMB: 4096})
	uploadAgent(e.env, c, accountLimit(alex.id))
	mid := machineID(t, e.env)
	if r, body := e.raw(t, "POST", "/api/machines/"+mid+"/restore/upload?diskLimit=account-1", "backup", alex.auth()); r.StatusCode != http.StatusOK {
		t.Fatalf("alex uploads a backup: %d %s", r.StatusCode, body)
	}
	if got := lastAsked(e.env, "POST", "/v1/restore/upload").query.Get("diskLimit"); got != accountLimit(alex.id) {
		t.Fatalf("alex's backup was uploaded against %q", got)
	}
	apply := "/api/machines/" + mid + "/restore/" + backupUpload + "/apply"
	if r := e.do(t, "POST", apply, `{"confirm":"restore","acceptEula":true}`, sam.auth()); r.status != http.StatusForbidden {
		t.Fatalf("sam restores alex's backup: %d %v", r.status, r.body)
	}
	if r := e.do(t, "POST", apply, `{"confirm":"restore","acceptEula":true,"memoryMB":8192}`, alex.auth()); r.status != http.StatusConflict {
		t.Fatalf("alex restores it past their memory: %d %v", r.status, r.body)
	}
	preview := func(mb int) {
		e.replyStatus("GET", "/v1/restore/"+backupUpload, http.StatusOK, fmt.Sprintf(`{"id":%q,"diskLimit":%q,"memoryMB":%d}`, backupUpload, accountLimit(alex.id), mb))
	}
	preview(0)
	if r := e.do(t, "POST", apply, `{"confirm":"restore","acceptEula":true}`, alex.auth()); r.status != http.StatusBadRequest {
		t.Fatalf("alex restores a backup that doesn't say its memory, choosing none: %d %v", r.status, r.body)
	}
	preview(2048)
	if r := e.do(t, "POST", apply, `{"confirm":"restore","acceptEula":true}`, alex.auth()); r.status != http.StatusAccepted || r.body["serverId"] != "cafebabe23" {
		t.Fatalf("alex restores their backup as a new server: %d %v", r.status, r.body)
	}
	if owned, _ := e.srv.creatorServers(alex.id); !slices.Equal(owned, []string{"cafebabe23"}) {
		t.Fatalf("alex created %v", owned)
	}
	if r, body := e.raw(t, "POST", "/api/machines/"+mid+"/restore/upload", "backup", alex.auth()); r.StatusCode != http.StatusConflict {
		t.Fatalf("alex uploads another backup with their allowance full: %d %s", r.StatusCode, body)
	}
}

// A creator uploads a world or backup for a new server only to their
// machine, as they create servers: an invited creator's is the dashboard's
// own.
func TestACreatorUploadsForANewServerOnlyToTheirMachine(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	newCreatorAgent(e.env)
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	remote := e.addRemote(t, "r2345abcde", "home-server")
	if r := e.do(t, "POST", "/api/machines/"+remote.ID+"/world-imports", `{}`, alex.auth()); r.status != http.StatusForbidden || !strings.Contains(r.body["error"].(string), "isn't the machine your servers go on") {
		t.Fatalf("alex opens a world upload on home-server: %d %v", r.status, r.body)
	}
	if r, body := e.raw(t, "POST", "/api/machines/"+remote.ID+"/restore/upload", "backup", alex.auth()); r.StatusCode != http.StatusForbidden {
		t.Fatalf("alex uploads a backup to home-server: %d %s", r.StatusCode, body)
	}
}

// A customer still waiting for room makes no server from an upload, as they
// create none: it's refused before anything is uploaded.
func TestACustomerWaitingForRoomUploadsNothingForANewServer(t *testing.T) {
	e, _, core := customerEnv(t)
	e.reply("GET", "/v1/machine", liveMachine(1024, true))
	if _, err := core.StartCustomer(context.Background(), Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(context.Background(), whopProvider, "user_alex")
	alex := signIn(t, e.env, info.UserID)
	mid := machineID(t, e.env)
	if r := e.do(t, "POST", "/api/machines/"+mid+"/world-imports", `{}`, alex.auth()); r.status != http.StatusConflict || !strings.Contains(r.body["error"].(string), "being set up") {
		t.Fatalf("a waiting customer opens a world upload: %d %v", r.status, r.body)
	}
	if r, body := e.raw(t, "POST", "/api/machines/"+mid+"/restore/upload", "backup", alex.auth()); r.StatusCode != http.StatusConflict {
		t.Fatalf("a waiting customer uploads a backup: %d %s", r.StatusCode, body)
	}
}

// A creator's disk limit reaches the machine their servers go to before
// their first server there, so what they upload for it counts from the
// start; a customer without a machine has none there.
func TestACreatorsLimitReachesTheirMachineBeforeTheirFirstServer(t *testing.T) {
	e := newJoinEnv(t)
	owner(t, e.env)
	newCreatorAgent(e.env)
	disk := newDiskLimitsAgent(e.env, 0)
	alex := addCreator(t, e.env, "alex", invites.Allowance{Servers: 1, MemoryMB: 4096})
	e.srv.syncDiskLimits(context.Background())
	if l := disk.last().Limits; len(l) != 1 || l[0].ID != accountLimit(alex.id) || len(l[0].Servers) != 0 || l[0].LimitBytes != 30<<30 {
		t.Fatalf("the limits sent before alex's first server: %+v", l)
	}
	if _, err := e.srv.db.Exec(`INSERT INTO customer_homes(user_id, machine_id, placed_at) VALUES(?, '', 0)`, alex.id); err != nil {
		t.Fatal(err)
	}
	e.srv.syncDiskLimits(context.Background())
	if l := disk.last().Limits; len(l) != 0 {
		t.Fatalf("the limits sent for a customer without a machine: %+v", l)
	}
}
