package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// moveFleet is alex, a customer on the joined machine home-server with the
// server alex there, running, and the dashboard's own machine with room for
// their plan, each machine's agent ready to do its part of a move.
type moveFleet struct {
	e      *env
	own    member
	alex   member
	rid    string
	ra     *remoteAgent
	joined joinedForCustomers
	local  string
	// archive is alex's server's whole folder as home-server streams it
	// out, and sum its SHA-256.
	archive []byte
	sum     string

	mu sync.Mutex
	// madeHere says the dashboard's machine has made alex's server.
	madeHere bool
	// moveIn is what the dashboard's machine was asked to make it from.
	moveIn map[string]any
}

const (
	// movedState is what home-server's agent keeps about alex's server.
	movedState  = `{"rows":{"servers":[{"public_page":0,"packs_token":"packtoken"}],"schedules":[{"id":"sched12345","name":"nightly"}],"offsite":[{"secret":"the-secret","keys":"the-keys"}]}}`
	movedServer = "cafebabe23"
	movedStatus = `{"id":"cafebabe23","name":"alex","slug":"alex","phase":"online","desired":"running","gamePort":25566,
		"config":{"memoryMB":2048,"playStyle":"friends","createdAt":"2026-09-20T10:00:00Z","eulaAcceptedAt":"2026-09-20T10:00:00Z","eulaAcceptedBy":"alex"}}`
	movedUpload = "0123456789abcdef"
)

func newMoveFleet(t *testing.T) *moveFleet {
	t.Helper()
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	joined := joinForCustomersAs(t, e, own)
	rid, ra := joined.d.MachineID, joined.ra
	e.srv.notifier = &recordingNotifier{}
	if r := e.do(t, "PUT", "/api/machines/"+rid+"/customers", `{"on":true}`, own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms home-server: %d %v", r.status, r.body)
	}
	core := customerCore{s: e.srv}
	ctx := context.Background()
	if _, err := core.StartCustomer(ctx, Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, starter); err != nil {
		t.Fatal(err)
	}
	info, _, _ := core.CustomerAccount(ctx, whopProvider, "user_alex")
	alex := signIn(t, e, info.UserID)
	ra.reply("POST /v1/servers", `{"id":"0123456789abcdef","serverId":"cafebabe23","kind":"create","status":"running"}`)
	if r := e.do(t, "POST", "/api/machines/"+rid+"/servers", `{"name":"alex","acceptEula":true,"memoryMB":2048}`, alex.auth()); r.status != http.StatusOK {
		t.Fatalf("alex creates a server: %d %v", r.status, r.body)
	}
	f := &moveFleet{e: e, own: own, alex: signIn(t, e, info.UserID), rid: rid, ra: ra, joined: joined, local: e.localMachine(t), archive: []byte("alex's server's folder, as an archive")}
	s := sha256.Sum256(f.archive)
	f.sum = hex.EncodeToString(s[:])

	ra.reply("GET /v1/servers", "["+movedStatus+"]")
	ra.reply("GET /v1/servers/"+movedServer, movedStatus)
	startsOp(ra, "POST /v1/servers/"+movedServer+"/stop", "op-stop", "succeeded", "")
	startsOp(ra, "POST /v1/servers/"+movedServer+"/delete", "op-delete", "succeeded", "")
	ra.handle("GET /v1/servers/"+movedServer+"/move-out", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(f.archive)
	})
	ra.reply("GET /v1/servers/"+movedServer+"/move-state", movedState)
	ra.reply("GET /v1/servers/"+movedServer+"/backup-rules", `{"automatic":{"enabled":true,"everyHours":24,"onlyIfPlayed":true},"rules":{"onHost":{"daily":5}},"custom":true}`)

	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.answer("GET /v1/servers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if f.made() {
			io.WriteString(w, "["+movedStatus+"]")
			return
		}
		io.WriteString(w, `[]`)
	})
	e.answer("GET /v1/servers/"+movedServer, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !f.made() {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"Server not found.","code":"not_found"}`)
			return
		}
		io.WriteString(w, strings.Replace(movedStatus, `"phase":"online"`, `"phase":"online","lastOperation":{"id":"op-movein","kind":"restore","status":"succeeded"}`, 1))
	})
	e.answer("POST /v1/restore/upload", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"`+movedUpload+`","sha256":"`+f.sum+`","compatible":true}`)
	})
	e.answer("POST /v1/restore/"+movedUpload+"/move-in", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.Unmarshal([]byte(e.agentBody("POST /v1/restore/"+movedUpload+"/move-in")), &body)
		f.mu.Lock()
		f.madeHere, f.moveIn = true, body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-movein","status":"running"}`)
	})
	e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"succeeded"}`)
	return f
}

func (f *moveFleet) made() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.madeHere
}

// startsOp has ra answer key with operation opID, which has ended as status
// with detail by the time it's asked about.
func startsOp(ra *remoteAgent, key, opID, status, detail string) {
	ra.handle(key, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"`+opID+`","status":"running"}`)
	})
	ra.reply("GET /v1/operations/"+opID, `{"id":"`+opID+`","status":"`+status+`"`+detail+`}`)
}

// answer has the dashboard's own agent answer key with h.
func (e *env) answer(key string, h http.HandlerFunc) {
	e.agent.mu.Lock()
	e.agent.answers[key] = h
	e.agent.mu.Unlock()
}

// move asks, as the owner, to move alex to machine mid ("" for the
// fullest with room).
func (f *moveFleet) move(t *testing.T, mid string) resp {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"machineId": mid})
	return f.e.do(t, "POST", "/api/customers/"+strconv.FormatInt(f.alex.id, 10)+"/move", string(body), f.own.auth())
}

// moved waits until alex's move has ended, and says why it stopped, or ""
// when it finished.
func (f *moveFleet) moved(t *testing.T) string {
	t.Helper()
	var why string
	eventually(t, "alex's move ends", func() bool {
		if f.e.srv.moves.running(f.alex.id) {
			return false
		}
		why = ""
		err := f.e.srv.db.QueryRow(`SELECT error FROM customer_moves WHERE user_id = ?`, f.alex.id).Scan(&why)
		return isNoRows(err) || err == nil && why != ""
	})
	return why
}

func (f *moveFleet) recorded(t *testing.T) string {
	t.Helper()
	at, err := f.e.srv.recordedMachine(context.Background(), movedServer)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func (f *moveFleet) rows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.e.srv.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// disconnect drops home-server's link and waits until the dashboard knows.
func (f *moveFleet) disconnect(t *testing.T) {
	t.Helper()
	f.joined.link.stop()
	eventually(t, "home-server is away", func() bool { return !f.e.srv.hub.Connected(f.rid) })
}

// reconnect has home-server's link come back, as the machine's does after
// the dashboard restarts, and waits until it's connected.
func (f *moveFleet) reconnect(t *testing.T) {
	t.Helper()
	f.joined.link = f.e.runLink(t, f.joined.d, f.joined.identity, f.ra)
	eventually(t, "home-server is connected again", func() bool { return f.e.srv.hub.Connected(f.rid) })
}

// restarted records a move of alex's to the dashboard's machine that a
// restart of the dashboard stopped, with alex's server still on
// home-server, and made, the operation that made its copy there.
func (f *moveFleet) restarted(t *testing.T, made string) {
	t.Helper()
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by) VALUES(%d, '%s', 0, 'admin')`, f.alex.id, f.local),
		fmt.Sprintf(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine, ran, made_by) VALUES('%s', %d, '%s', '%s', 1, '%s')`, movedServer, f.alex.id, f.rid, f.local, made),
		fmt.Sprintf(`UPDATE customer_homes SET machine_id = '%s' WHERE user_id = %d`, f.local, f.alex.id),
	} {
		if _, err := f.e.srv.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// pause pauses alex, whose servers are deleted at until unless they renew.
func (f *moveFleet) pause(t *testing.T, until time.Time) {
	t.Helper()
	if _, err := f.e.srv.db.Exec(`UPDATE customers SET state = ?, delete_after = ? WHERE user_id = ?`, string(CustomerPaused), until.UnixMilli(), f.alex.id); err != nil {
		t.Fatal(err)
	}
}

// stopped records a move of alex's to machine to that stopped before their
// server left home-server, with home their machine since.
func (f *moveFleet) stopped(t *testing.T, to, home string) {
	t.Helper()
	if _, err := f.e.srv.db.Exec(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by, error) VALUES(?, ?, 0, 'admin', 'It stopped.')`, f.alex.id, to); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.srv.db.Exec(`UPDATE customer_homes SET machine_id = ? WHERE user_id = ?`, home, f.alex.id); err != nil {
		t.Fatal(err)
	}
}

// deleted is alex's record once their servers are deleted: when, and the
// machine keeping their final backups.
func (f *moveFleet) deleted(t *testing.T) (at int64, keptOn string) {
	t.Helper()
	if err := f.e.srv.db.QueryRow(`SELECT servers_deleted_at, final_backups_machine FROM customers WHERE user_id = ?`, f.alex.id).Scan(&at, &keptOn); err != nil {
		t.Fatal(err)
	}
	return at, keptOn
}

// The owner moves a customer to another machine: their plan's memory is set
// aside there at once, and their server follows. It stops, and its whole
// folder goes to the other machine as an upload, which makes it with the
// same id, name and slug, the EULA acceptance it had, and started, since it
// ran. It counts against their disk limit there before its requests go
// there, and the upload doesn't, since the server counts already. The
// machine it left then deletes it, keeping its final backup a week. Each
// step goes in the audit log.
func TestTheOwnerMovesACustomerAndTheirServerFollows(t *testing.T) {
	f := newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`INSERT INTO public_links(kind, token_hash, server_id, machine_id, created_at) VALUES('map', 'h', ?, ?, 0)`, movedServer, f.rid); err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	f.e.agent.mu.Lock()
	f.e.agent.gates["PUT /v1/disk-limits"] = gate
	f.e.agent.mu.Unlock()
	if r := f.move(t, f.local); r.status != http.StatusAccepted || r.body["machineId"] != f.local {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if home, _, _ := f.e.srv.homeMachine(context.Background(), f.alex.id); home != f.local {
		t.Fatalf("alex's machine once the move starts: %q", home)
	}
	eventually(t, "the dashboard's machine is sent alex's disk limit", func() bool { return f.e.sawLocally("PUT /v1/disk-limits") })
	if at := f.recorded(t); at != f.rid {
		t.Errorf("the server's requests went to %q before its disk limit did", at)
	}
	close(gate)
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	if at := f.recorded(t); at != f.local {
		t.Fatalf("alex's server's requests go to %q, want the dashboard's machine", at)
	}
	f.e.agent.mu.Lock()
	upload := f.e.agent.lastBody["POST /v1/restore/upload"]
	f.e.agent.mu.Unlock()
	if upload != string(f.archive) || !slices.Contains(f.e.agentHits(), "POST /v1/restore/upload") {
		t.Errorf("the dashboard's machine got %q as the backup", upload)
	}
	var query string
	var limits api.DiskLimitsRequest
	f.e.agent.mu.Lock()
	for i, r := range f.e.agent.reqs {
		switch {
		case r.method == "POST" && r.path == "/v1/restore/upload":
			query = r.query.Get("diskLimit")
		case r.method == "PUT" && r.path == "/v1/disk-limits" && limits.Limits == nil:
			json.Unmarshal([]byte(f.e.agent.bodies[i]), &limits)
		}
	}
	f.e.agent.mu.Unlock()
	if query != "" {
		t.Errorf("the backup came against the disk limit %q, beside the server it's a backup of", query)
	}
	if i := slices.IndexFunc(limits.Limits, func(l api.DiskLimit) bool { return l.ID == accountLimit(f.alex.id) }); i < 0 || !slices.Equal(limits.Limits[i].Servers, []string{movedServer}) {
		t.Errorf("the dashboard's machine was sent the disk limits %+v, without the server moved to it in alex's", limits.Limits)
	}
	hits := f.e.agentHits()
	if movein, sent := slices.Index(hits, "POST /v1/restore/"+movedUpload+"/move-in"), slices.Index(hits, "PUT /v1/disk-limits"); movein < 0 || sent < movein {
		t.Errorf("alex's disk limit went to the dashboard's machine before it made the server: %v", hits)
	}
	if kept, sent := slices.Index(hits, "PUT /v1/servers/"+movedServer+"/move-state"), slices.Index(hits, "PUT /v1/disk-limits"); kept < 0 || kept > sent {
		t.Errorf("the server moved wasn't given what home-server kept about it before its requests went there: %v", hits)
	}
	if kept := f.e.agentBody("PUT /v1/servers/" + movedServer + "/move-state"); !strings.Contains(kept, `"keys":"the-keys"`) || !strings.Contains(kept, `"packs_token":"packtoken"`) || !strings.Contains(kept, `"sched12345"`) {
		t.Errorf("the server moved was given %s", kept)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM public_links WHERE server_id = ? AND machine_id = ?`, movedServer, f.local); n != 1 {
		t.Error("the server's public link stayed with the machine it left")
	}
	f.mu.Lock()
	in := f.moveIn
	f.mu.Unlock()
	for k, want := range map[string]any{"serverId": movedServer, "name": "alex", "slug": "alex", "memoryMB": float64(2048), "playStyle": "friends", "start": true,
		"eulaAcceptedBy": "alex", "eulaAcceptedAt": "2026-09-20T10:00:00Z", "createdAt": "2026-09-20T10:00:00Z", "actor": placementActor} {
		if in[k] != want {
			t.Errorf("the move-in's %s: %v, want %v", k, in[k], want)
		}
	}
	var del map[string]any
	for _, key := range []string{"POST /v1/servers/" + movedServer + "/stop", "GET /v1/servers/" + movedServer + "/move-out", "POST /v1/servers/" + movedServer + "/delete"} {
		if actor, ok := f.ra.saw(key); !ok || actor != placementActor {
			t.Errorf("home-server wasn't asked to %s as the dashboard: %v %q", key, ok, actor)
		}
	}
	if !json.Valid([]byte(f.ra.body("POST /v1/servers/" + movedServer + "/delete"))) {
		t.Fatalf("the delete's body: %q", f.ra.body("POST /v1/servers/"+movedServer+"/delete"))
	}
	json.Unmarshal([]byte(f.ra.body("POST /v1/servers/"+movedServer+"/delete")), &del)
	if del["confirm"] != "alex" || del["keepFinalBackupDays"] != float64(movedBackupDays) || del["keptFor"] != movedKeptFor(f.alex.id) || del["forgetKey"] != true {
		t.Errorf("home-server deleted its copy with %v", del)
	}
	if rules := f.e.agentBody("POST /v1/servers/" + movedServer + "/backup-rules"); !strings.Contains(rules, `"everyHours":24`) || !strings.Contains(rules, `"daily":5`) {
		t.Errorf("the server moved got the backup rules %s", rules)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM server_moves`); n != 0 {
		t.Errorf("%d moves left under way", n)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM left_copies WHERE server_id = ? AND machine_id = ? AND left_at > 0`, movedServer, f.rid); n != 1 {
		t.Errorf("the copy home-server had isn't recorded as gone")
	}
	started, finished := f.e.auditRows(t, "customer.move"), 0
	for _, row := range started {
		if strings.HasPrefix(row, placementActor+" alex succeeded 1 server(s) moved") {
			finished++
		}
	}
	if len(started) != 2 || !strings.HasPrefix(started[0], "admin alex started to the dashboard's machine, with 4 GB set aside there") || finished != 1 {
		t.Errorf("audit: %v", started)
	}
}

// While a server moves, no request reaches it, from its owner, its
// customer or an AI agent, and its customer is told it's being moved, with
// no machine named. It's listed once, as being moved: the copy the other
// machine makes isn't the server yet. The customer can't make another
// server until their servers have all moved.
func TestNothingReachesAServerWhileItMoves(t *testing.T) {
	f := newMoveFleet(t)
	gate := make(chan struct{})
	f.e.agent.mu.Lock()
	f.e.agent.gates["GET /v1/operations/op-movein"] = gate
	f.e.agent.mu.Unlock()
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	eventually(t, "the dashboard's machine makes the server", f.made)
	eventually(t, "the operation making the copy is recorded, for a restart to carry on with it", func() bool {
		return f.rows(t, `SELECT COUNT(*) FROM server_moves WHERE server_id = ? AND made_by = 'op-movein'`, movedServer) == 1
	})
	f.e.reply("GET", "/v1/servers", "["+movedStatus+"]")

	for _, who := range []member{f.alex, f.own} {
		r, body := f.e.raw(t, "POST", "/api/servers/"+movedServer+"/start", `{}`, who.auth())
		var out map[string]any
		json.Unmarshal([]byte(body), &out)
		if r.StatusCode != http.StatusConflict || out["code"] != codeServerMoving || out["error"] != serverMovingText {
			t.Errorf("starting the server while it moves: %d %s", r.StatusCode, body)
		}
		for _, n := range []string{"home-server", "siya"} {
			if strings.Contains(body, n) {
				t.Errorf("the refusal names %s: %s", n, body)
			}
		}
	}
	if _, err := (mcpBackend{s: f.e.srv}).Agent(context.Background(), movedServer); err == nil {
		t.Error("an AI agent reaches the server while it moves")
	} else if te, ok := err.(*mcp.ToolError); !ok || te.Kind != codeServerMoving {
		t.Errorf("an AI agent's refusal: %v", err)
	}
	var servers []map[string]any
	f.e.get(t, "/api/servers", f.alex.cookie, &servers)
	if len(servers) != 1 || servers[0]["id"] != movedServer || servers[0]["moving"] != true {
		t.Errorf("alex's servers while theirs moves: %v", servers)
	}
	if r := f.e.do(t, "POST", "/api/machines/"+f.local+"/servers", `{"name":"alex 2","acceptEula":true,"memoryMB":2048}`, f.alex.auth()); r.status != http.StatusConflict || r.body["error"] != errCustomerMoving.Msg {
		t.Errorf("alex makes another server while theirs moves: %d %v", r.status, r.body)
	}
	if r := f.move(t, ""); r.status != http.StatusConflict || r.body["error"] != errMoveRunning.Msg {
		t.Errorf("moving alex again meanwhile: %d %v", r.status, r.body)
	}
	close(gate)
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	if r := f.e.do(t, "POST", "/api/servers/"+movedServer+"/start", `{}`, f.alex.auth()); r.status == http.StatusConflict {
		t.Errorf("alex starts their server once it's moved: %d %v", r.status, r.body)
	}
}

// A move that fails before the server's requests go to the other machine
// leaves it where it was, started again since it ran, and that machine
// deletes the copy it made. The owner sees why, and the customer makes no
// new server until the owner moves them again, which carries on.
func TestAFailedMoveLeavesTheServerWhereItWas(t *testing.T) {
	f := newMoveFleet(t)
	f.e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"failed","error":"The restored world did not start."}`)
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-undo","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-undo", `{"id":"op-undo","status":"succeeded"}`)
	f.e.srv.syncDiskLimits(context.Background())
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	why := f.moved(t)
	if why != "alex: the dashboard's machine couldn't make it from its folder: The restored world did not start." {
		t.Fatalf("why the move stopped: %q", why)
	}
	f.e.srv.diskUse.Lock()
	counted := f.e.srv.diskUse.at
	f.e.srv.diskUse.Unlock()
	if !counted.IsZero() {
		t.Errorf("what alex's servers take isn't counted again once their move stopped (last counted %v)", counted)
	}
	if at := f.recorded(t); at != f.rid {
		t.Errorf("after a failed move the server's requests go to %q", at)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); !ok {
		t.Error("the server that ran didn't start again where it was")
	}
	if f.made() || !slices.Contains(f.e.agentHits(), "POST /v1/servers/"+movedServer+"/delete") {
		t.Error("the copy the failed move made is still on the dashboard's machine")
	}
	if !slices.Contains(f.e.agentHits(), "DELETE /v1/restore/"+movedUpload) {
		t.Error("the upload the failed move-in was made from stayed on the dashboard's machine")
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM server_moves`); n != 0 {
		t.Errorf("%d moves still under way", n)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM left_copies WHERE server_id = ? AND machine_id = ? AND left_at > 0`, movedServer, f.local); n != 1 {
		t.Error("the copy the failed move made isn't recorded as gone")
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); ok {
		t.Error("home-server deleted the server whose move failed")
	}
	if r := f.e.do(t, "POST", "/api/machines/"+f.local+"/servers", `{"name":"alex 2","acceptEula":true,"memoryMB":2048}`, f.alex.auth()); r.status != http.StatusConflict {
		t.Errorf("alex makes a server after their move stopped: %d %v", r.status, r.body)
	}
	var list []machineCustomer
	f.e.get(t, "/api/machines/"+f.local+"/customers", f.own.cookie, &list)
	if len(list) != 1 || list[0].Name != "alex" || list[0].Move == nil || list[0].Move.Left != 1 || list[0].Move.Error != why {
		t.Errorf("the dashboard's machine's customers: %+v", list)
	}

	f.e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"succeeded"}`)
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex again: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move tried again stopped: %s", why)
	}
	if at := f.recorded(t); at != f.local {
		t.Errorf("once the move is tried again the server's requests go to %q", at)
	}
	list = nil
	f.e.get(t, "/api/machines/"+f.local+"/customers", f.own.cookie, &list)
	if len(list) != 1 || list[0].Move != nil || list[0].Servers != 1 {
		t.Errorf("the dashboard's machine's customers once alex moved: %+v", list)
	}
}

// A move whose last step, sending the server's requests to the machine it
// moved to, fails is a failed move too: the server's requests stay where it
// was, it starts again there, and the copy the move made goes, rather than
// the server staying stopped and unreachable until the move is tried again.
func TestAMoveWhoseSwitchFailsLeavesTheServerWhereItWas(t *testing.T) {
	f := newMoveFleet(t)
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-undo","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-undo", `{"id":"op-undo","status":"succeeded"}`)
	if _, err := f.e.srv.db.Exec(`CREATE TRIGGER switch_fails BEFORE INSERT ON left_copies WHEN NEW.machine_id = '` + f.rid + `' BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); !strings.HasPrefix(why, "alex: its requests couldn't go to the dashboard's machine") {
		t.Errorf("why the move stopped: %q", why)
	}
	if at := f.recorded(t); at != f.rid {
		t.Errorf("after a failed switch the server's requests go to %q", at)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); !ok {
		t.Error("the server that ran didn't start again where it was")
	}
	if f.made() {
		t.Error("the copy the move made is still on the dashboard's machine")
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM server_moves`); n != 0 {
		t.Errorf("%d moves still under way", n)
	}
}

// Only the owner moves customers and sees whose servers go on a machine:
// the move's what customers can never see, and it's the owner's machines'
// room it uses. So only the owner removes a machine customers are on, which
// places them again.
func TestOnlyTheOwnerMovesCustomers(t *testing.T) {
	f := newMoveFleet(t)
	admin := addAdmin(t, f.e, "sam", "*")
	for _, who := range []member{admin, f.alex} {
		if r := f.e.do(t, "POST", "/api/customers/"+strconv.FormatInt(f.alex.id, 10)+"/move", `{}`, who.auth()); r.status != http.StatusForbidden {
			t.Errorf("account %d moves alex: %d %v", who.id, r.status, r.body)
		}
		if r, body := f.e.raw(t, "GET", "/api/machines/"+f.rid+"/customers", "", who.auth()); r.StatusCode != http.StatusForbidden {
			t.Errorf("account %d lists home-server's customers: %d %s", who.id, r.StatusCode, body)
		}
	}
	if r := f.e.do(t, "DELETE", "/api/machines/"+f.rid, "", admin.auth()); r.status != http.StatusForbidden || !strings.Contains(fmt.Sprint(r.body), "Only the owner removes") {
		t.Errorf("an admin of every server removes home-server, which alex is on: %d %v", r.status, r.body)
	}
	if f.e.srv.moves.running(f.alex.id) || f.rows(t, `SELECT COUNT(*) FROM customer_moves`) != 0 {
		t.Error("a refused move started")
	}
}

// A move goes only to a machine that takes customers and has room for the
// customer's plan, and only for a customer placed on a machine whose plan
// hasn't ended. With no machine named it goes to the fullest other one with
// room.
func TestAMoveGoesOnlyWhereTheCustomerFits(t *testing.T) {
	f := newMoveFleet(t)
	uid := strconv.FormatInt(f.alex.id, 10)
	f.e.reply("GET", "/v1/machine", liveMachine(2048, true))
	for _, c := range []struct {
		name, body, want string
	}{
		{"to the machine they're on", `{"machineId":"` + f.rid + `"}`, errMoveThere.Msg},
		{"to a machine with no room for their plan", `{"machineId":"` + f.local + `"}`, "That machine can set aside 2 GB, and their plan needs 4 GB."},
		{"to the fullest other machine, with none having room", `{}`, errMoveNowhere.Msg},
		{"to a machine that doesn't exist", `{"machineId":"nosuchmach"}`, errMoveMachine.Msg},
	} {
		if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", c.body, f.own.auth()); r.body["error"] != c.want {
			t.Errorf("moving alex %s: %d %v", c.name, r.status, r.body)
		}
	}
	attic := newRemoteAgent()
	attic.reply("GET /v1/machine", liveMachine(30000, true))
	attic.reply("GET /v1/servers", `[]`)
	unconfirmed, _ := f.e.joinMachine(t, f.own.cookie, f.own.csrf, attic)
	if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", `{"machineId":"`+unconfirmed+`"}`, f.own.auth()); r.body["error"] != "Joined machines take customers once you confirm them." {
		t.Errorf("moving alex to a machine with room that doesn't take customers: %d %v", r.status, r.body)
	}
	f.e.reply("GET", "/v1/machine", liveMachine(30000, false))
	f.e.reply("POST", "/v1/network-guard", `{"on":true,"host":false}`)
	if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", `{"machineId":"`+f.local+`"}`, f.own.auth()); r.body["error"] != errMoveUnguarded.Msg {
		t.Errorf("moving alex to a machine that won't keep servers away: %d %v", r.status, r.body)
	}
	f.e.reply("POST", "/v1/network-guard", `{"on":true,"host":true}`)
	if _, err := f.e.srv.db.Exec(`UPDATE customer_homes SET machine_id = '' WHERE user_id = ?`, f.alex.id); err != nil {
		t.Fatal(err)
	}
	if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", `{}`, f.own.auth()); r.body["error"] != errMoveWaiting.Msg {
		t.Errorf("moving alex while they wait for room: %d %v", r.status, r.body)
	}
	if _, err := f.e.srv.db.Exec(`UPDATE customer_homes SET machine_id = ? WHERE user_id = ?`, f.rid, f.alex.id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.srv.db.Exec(`UPDATE customers SET state = ?, delete_after = ? WHERE user_id = ?`, string(CustomerPaused), f.e.clock.now().Add(-time.Hour).UnixMilli(), f.alex.id); err != nil {
		t.Fatal(err)
	}
	f.e.reply("GET", "/v1/machine", liveMachine(30000, true))
	if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", `{}`, f.own.auth()); r.body["error"] != errMoveLapsed.Msg {
		t.Errorf("moving alex once their plan ended past its grace period: %d %v", r.status, r.body)
	}
	if r := f.e.do(t, "POST", "/api/customers/"+strconv.FormatInt(f.own.id, 10)+"/move", `{}`, f.own.auth()); r.status != http.StatusNotFound {
		t.Errorf("moving the owner: %d %v", r.status, r.body)
	}
	if home, _, _ := f.e.srv.homeMachine(context.Background(), f.alex.id); home != f.rid || f.rows(t, `SELECT COUNT(*) FROM customer_moves`) != 0 {
		t.Errorf("a refused move changed alex's machine to %q", home)
	}
	if _, err := f.e.srv.db.Exec(`UPDATE customers SET state = ?, delete_after = 0 WHERE user_id = ?`, string(CustomerActive), f.alex.id); err != nil {
		t.Fatal(err)
	}
	if r := f.e.do(t, "POST", "/api/customers/"+uid+"/move", `{}`, f.own.auth()); r.status != http.StatusAccepted || r.body["machineId"] != f.local {
		t.Errorf("moving alex to the fullest other machine with room: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
}

// While a server moves, the machine it goes to lists the copy it's making,
// and afterwards the machine it left lists its old copy until that's
// deleted. Neither listing takes the server or disputes it, and the old
// copy's machine counts as any again from a listing asked for after its
// copy went.
func TestTheCopiesAMoveMakesAndLeavesDontCountAsTheServer(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	owner(t, e)
	e.reply("GET", "/v1/servers", `[]`)
	local, _ := e.srv.machineByID(e.localMachine(t))
	alpha := e.addRemote(t, "a2345abcde", "alpha")
	beta := e.addRemote(t, "b2345abcde", "beta")
	s := e.srv
	now := e.clock.now()
	recordOf := func(id string) (string, string) {
		var m, disputed string
		s.db.QueryRow(`SELECT machine_id, disputed_by FROM server_machines WHERE server_id = ?`, id).Scan(&m, &disputed)
		return m, disputed
	}
	s.claimServers(alpha, serverList("movingsrv2", "leftsrv234"))
	if _, err := s.db.Exec(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine) VALUES('movingsrv2', 7, ?, ?)`, alpha.ID, beta.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.claimServers(beta, serverList("movingsrv2")); len(got) != 0 {
		t.Errorf("the machine a server is moving to shows its copy: %v", got)
	}
	if m, disputed := recordOf("movingsrv2"); m != alpha.ID || disputed != "" {
		t.Errorf("the copy being made took or disputed the server: %s %q", m, disputed)
	}
	if _, err := s.db.Exec(`UPDATE server_moves SET to_machine = ? WHERE server_id = 'movingsrv2'`, local.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.claimLocal(local, serverList("movingsrv2"), now); len(got) != 0 {
		t.Errorf("the dashboard's machine shows a copy it's making: %v", got)
	}
	if m, _ := recordOf("movingsrv2"); m != alpha.ID {
		t.Errorf("the dashboard's machine took a server from the copy it's making: %s", m)
	}

	// leftsrv234 moved from alpha to beta, and alpha deletes its copy.
	if _, err := s.db.Exec(`UPDATE server_machines SET machine_id = ? WHERE server_id = 'leftsrv234'`, beta.ID); err != nil {
		t.Fatal(err)
	}
	if err := leftCopy(context.Background(), s.db, "leftsrv234", alpha.ID, 7, movedBackupDays, 0); err != nil {
		t.Fatal(err)
	}
	if got := s.claimServers(alpha, serverList("leftsrv234")); len(got) != 0 {
		t.Errorf("the machine a server left shows its copy: %v", got)
	}
	if m, disputed := recordOf("leftsrv234"); m != beta.ID || disputed != "" {
		t.Errorf("the copy left took or disputed the server: %s %q", m, disputed)
	}
	e.clock.add(time.Minute)
	went := e.clock.now()
	if _, err := s.db.Exec(`UPDATE left_copies SET left_at = ? WHERE server_id = 'leftsrv234'`, millis(went)); err != nil {
		t.Fatal(err)
	}
	if got := s.claimListing(alpha, serverList("leftsrv234"), went.Add(-time.Second)); len(got) != 0 {
		t.Errorf("a listing asked for before the copy went shows it: %v", got)
	}
	if _, disputed := recordOf("leftsrv234"); disputed != "" {
		t.Error("a listing asked for before the copy went disputes the server")
	}
	e.clock.add(time.Minute)
	if got := s.claimListing(alpha, serverList("leftsrv234"), e.clock.now()); len(got) != 0 {
		t.Errorf("a machine that still lists a server it left shows it: %v", got)
	}
	if _, disputed := recordOf("leftsrv234"); disputed != alpha.ID {
		t.Error("a machine that still lists a server it left after its copy went doesn't dispute it")
	}
	leftRows := func() int {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM left_copies`).Scan(&n)
		return n
	}
	if n := leftRows(); n != 1 {
		t.Errorf("%d copies recorded right after alpha listed once its copy went, not the one it left", n)
	}
	e.clock.add(leftCopyKept)
	s.claimListing(alpha, serverList("leftsrv234"), e.clock.now())
	if n := leftRows(); n != 0 {
		t.Errorf("%d copies still recorded once alpha listed %s after its copy went", n, leftCopyKept)
	}

	// Once movingsrv2's requests go to beta, a listing of beta's asked for
	// before then, without it, doesn't drop its record, and until beta lists
	// it, it's listed as alpha last listed it.
	if _, err := s.db.Exec(`UPDATE server_moves SET to_machine = ? WHERE server_id = 'movingsrv2'`, beta.ID); err != nil {
		t.Fatal(err)
	}
	s.claimServers(alpha, serverList("movingsrv2"))
	asked := e.clock.now()
	e.clock.add(time.Second)
	if err := s.switchServer(context.Background(), serverMove{serverID: "movingsrv2", userID: 7, from: alpha.ID, to: beta.ID}, beta, "movingsrv2"); err != nil {
		t.Fatal(err)
	}
	s.claimListing(beta, nil, asked)
	if m, _ := recordOf("movingsrv2"); m != beta.ID {
		t.Errorf("a listing of beta's asked for before the server moved there dropped its record: %q", m)
	}
	known, err := s.lastKnownServers(beta)
	if err != nil || !slices.ContainsFunc(known, func(sv map[string]any) bool { return sv["id"] == "movingsrv2" }) {
		t.Errorf("while beta is away, the server moved there isn't listed as it last was: %v %v", known, err)
	}

	// The same for the dashboard's machine: a server moved to it keeps its
	// record through a listing of its asked for before then.
	s.claimServers(alpha, serverList("tolocal234"))
	if _, err := s.db.Exec(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine) VALUES('tolocal234', 7, ?, ?)`, alpha.ID, local.ID); err != nil {
		t.Fatal(err)
	}
	asked = e.clock.now()
	e.clock.add(time.Second)
	if err := s.switchServer(context.Background(), serverMove{serverID: "tolocal234", userID: 7, from: alpha.ID, to: local.ID}, local, "tolocal234"); err != nil {
		t.Fatal(err)
	}
	s.claimLocal(local, nil, asked)
	if m, _ := recordOf("tolocal234"); m != local.ID {
		t.Errorf("a listing of the dashboard's machine asked for before the server moved there dropped its record: %q", m)
	}
}

// A copy a move left on the dashboard's machine is never where a server's
// requests go, not even once the machine the server moved to is removed.
func TestACopyLeftOnTheDashboardsMachineIsNeverTheServer(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	owner(t, e)
	e.reply("GET", "/v1/servers", `[]`)
	local, err := e.srv.machineByID(e.localMachine(t))
	if err != nil {
		t.Fatal(err)
	}
	beta := e.addRemote(t, "b2345abcde", "beta")
	s := e.srv
	s.claimServers(beta, serverList("leftsrv234"))
	if err := leftCopy(context.Background(), s.db, "leftsrv234", local.ID, 7, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := s.claimLocal(local, serverList("leftsrv234"), e.clock.now()); len(got) != 0 {
		t.Errorf("the dashboard's machine shows a copy a move left on it: %v", got)
	}
	if _, err := s.db.Exec(`UPDATE machines SET revoked_at = 1 WHERE id = ?`, beta.ID); err != nil {
		t.Fatal(err)
	}
	if m, err := s.machineForServer("leftsrv234"); err == nil {
		t.Errorf("once beta is removed, the server's requests go to %s, where only a copy a move left is", m.ID)
	}
}

// A listing asked for while a copy a move left was still there, which
// arrives after one asked for once the copy went, still leaves the copy out:
// it neither disputes the server where it moved, on a joined machine, nor
// becomes it, on the dashboard's machine.
func TestALateListingStillLeavesOutACopyThatWent(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	owner(t, e)
	e.reply("GET", "/v1/servers", `[]`)
	local, err := e.srv.machineByID(e.localMachine(t))
	if err != nil {
		t.Fatal(err)
	}
	alpha := e.addRemote(t, "a2345abcde", "alpha")
	beta := e.addRemote(t, "b2345abcde", "beta")
	s := e.srv
	s.claimServers(beta, serverList("leftsrv234", "leftsrv567"))
	for _, m := range []machine{alpha, local} {
		if err := leftCopy(context.Background(), s.db, map[string]string{alpha.ID: "leftsrv234", local.ID: "leftsrv567"}[m.ID], m.ID, 7, movedBackupDays, 0); err != nil {
			t.Fatal(err)
		}
	}
	e.clock.add(time.Minute)
	asked := e.clock.now()
	e.clock.add(time.Second)
	went := e.clock.now()
	if _, err := s.db.Exec(`UPDATE left_copies SET left_at = ?`, millis(went)); err != nil {
		t.Fatal(err)
	}
	e.clock.add(time.Second)
	s.claimListing(alpha, nil, e.clock.now())
	s.claimLocal(local, nil, e.clock.now())
	if got := s.claimListing(alpha, serverList("leftsrv234"), asked); len(got) != 0 {
		t.Errorf("alpha's listing from before its copy went, arriving late, shows it: %v", got)
	}
	if got := s.claimLocal(local, serverList("leftsrv567"), asked); len(got) != 0 {
		t.Errorf("the dashboard's machine's listing from before its copy went, arriving late, shows it: %v", got)
	}
	for _, id := range []string{"leftsrv234", "leftsrv567"} {
		var m, disputed string
		if err := s.db.QueryRow(`SELECT machine_id, disputed_by FROM server_machines WHERE server_id = ?`, id).Scan(&m, &disputed); err != nil || m != beta.ID || disputed != "" {
			t.Errorf("%s, which moved to beta, is on %q, disputed by %q (%v)", id, m, disputed, err)
		}
	}
}

// A copy a move left on a machine that was removed before it could delete
// it isn't forgotten. That machine's host can only join again as a new
// machine, and when it lists the copy, the copy is taken for what it is
// and deleted, keeping its final backup, rather than disputing the server
// on the machine it moved to.
func TestACopyLeftOnARemovedMachineIsntTakenForTheServer(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	owner(t, e)
	e.reply("GET", "/v1/servers", `[]`)
	alpha := e.addRemote(t, "a2345abcde", "alpha")
	beta := e.addRemote(t, "b2345abcde", "beta")
	s := e.srv
	ctx := context.Background()
	s.claimServers(beta, serverList("movedsrv23"))
	if err := leftCopy(ctx, s.db, "movedsrv23", alpha.ID, 7, movedBackupDays, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE machines SET revoked_at = 1 WHERE id = ?`, alpha.ID); err != nil {
		t.Fatal(err)
	}
	s.leaveLeftovers(ctx)

	gamma := e.addRemote(t, "c2345abcde", "gamma")
	if got := s.claimServers(gamma, serverList("movedsrv23", "gammasrv23")); len(got) != 1 || got[0]["id"] != "gammasrv23" {
		t.Errorf("alpha's host, joined again as gamma, shows the copy a move left there: %v", got)
	}
	var m, disputed string
	if err := s.db.QueryRow(`SELECT machine_id, disputed_by FROM server_machines WHERE server_id = 'movedsrv23'`).Scan(&m, &disputed); err != nil || m != beta.ID || disputed != "" {
		t.Errorf("the server that moved to beta is on %q, disputed by %q (%v)", m, disputed, err)
	}
	var user int64
	var days int
	if err := s.db.QueryRow(`SELECT user_id, keep_days FROM left_copies WHERE server_id = 'movedsrv23' AND machine_id = ? AND left_at = 0`, gamma.ID).Scan(&user, &days); err != nil || user != 7 || days != movedBackupDays {
		t.Errorf("gamma isn't asked to delete the copy keeping its final backup: %d %d %v", user, days, err)
	}
	if n := func() int {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM left_copies WHERE machine_id = ?`, alpha.ID).Scan(&n)
		return n
	}(); n != 0 {
		t.Errorf("alpha still has %d copies recorded once gamma has its copy", n)
	}
}

// When the machine a server moved to was removed too, a new machine that
// lists it may be either host joining again. What it lists is taken for the
// copy the move left only when it stopped before the server moved away from
// it, as that copy did. The server where it moved, running, crashed or
// stopped since, is taken over, and so is one whose copy's move isn't known
// to have finished, so the server is never deleted for a copy.
func TestAServerBothOfWhoseMachinesWereRemovedIsTakenForWhatItIs(t *testing.T) {
	e := newEnvConfig(t, withDomain, nil)
	owner(t, e)
	e.reply("GET", "/v1/servers", `[]`)
	alpha := e.addRemote(t, "a2345abcde", "alpha")
	beta := e.addRemote(t, "b2345abcde", "beta")
	s := e.srv
	ctx := context.Background()
	ids := []string{"stalesrv23", "runssrv234", "crashsrv23", "stopsrv234", "unknsrv234"}
	s.claimServers(beta, serverList(ids...))
	switched := e.clock.now()
	for _, id := range ids {
		at := millis(switched)
		if id == "unknsrv234" {
			at = 0
		}
		if err := leftCopy(ctx, s.db, id, alpha.ID, 7, movedBackupDays, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE machines SET revoked_at = 1 WHERE id IN (?, ?)`, alpha.ID, beta.ID); err != nil {
		t.Fatal(err)
	}
	e.clock.add(time.Hour)
	before, after := switched.Add(-time.Minute).Format(time.RFC3339Nano), switched.Add(time.Minute).Format(time.RFC3339Nano)
	listed := []map[string]any{
		{"id": "stalesrv23", "name": "stale", "phase": "stopped", "stoppedAt": before},
		{"id": "runssrv234", "name": "runs", "phase": "online"},
		{"id": "crashsrv23", "name": "crashed", "phase": "crashed", "stoppedAt": before},
		{"id": "stopsrv234", "name": "stopped since", "phase": "stopped", "stoppedAt": after},
		{"id": "unknsrv234", "name": "unknown", "phase": "stopped", "stoppedAt": before},
	}
	gamma := e.addRemote(t, "c2345abcde", "gamma")
	var runs []string
	for _, sv := range s.claimServers(gamma, listed) {
		runs = append(runs, sv["id"].(string))
	}
	slices.Sort(runs)
	if want := []string{"crashsrv23", "runssrv234", "stopsrv234", "unknsrv234"}; !slices.Equal(runs, want) {
		t.Errorf("a new machine, after both of the servers' machines were removed, runs %v, want %v", runs, want)
	}
	var days int
	if err := s.db.QueryRow(`SELECT keep_days FROM left_copies WHERE server_id = 'stalesrv23' AND machine_id = ? AND left_at = 0`, gamma.ID).Scan(&days); err != nil || days != movedBackupDays {
		t.Errorf("the new machine isn't asked to delete the copy a move left, keeping it: %d %v", days, err)
	}
	for _, id := range runs {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM left_copies WHERE server_id = ? AND machine_id = ?`, id, gamma.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s, which the new machine runs, is to be deleted there: %d %v", id, n, err)
		}
	}
}

// A copy of the server still on the machine it's going to, which an
// earlier move left there, is an old one: the move deletes it and makes
// the server there from a new backup, and the copy's record goes once the
// server's requests go there, so that machine's listings show it.
func TestAMoveDeletesAnOldCopyOnTheMachineItGoesTo(t *testing.T) {
	f := newMoveFleet(t)
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	if err := leftCopy(context.Background(), f.e.srv.db, movedServer, f.local, f.alex.id, 0, 0); err != nil {
		t.Fatal(err)
	}
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-old","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-old", `{"id":"op-old","status":"succeeded"}`)
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	hits := f.e.agentHits()
	if old, upload := slices.Index(hits, "POST /v1/servers/"+movedServer+"/delete"), slices.Index(hits, "POST /v1/restore/upload"); old < 0 || old > upload {
		t.Errorf("the old copy wasn't deleted before the server's folder came: %v", hits)
	}
	if _, ok := f.ra.saw("GET /v1/servers/" + movedServer + "/move-out"); !ok {
		t.Error("the old copy was taken for the server: its folder wasn't copied")
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM left_copies WHERE machine_id = ?`, f.local); n != 0 {
		t.Error("the copy's record stayed once the server's requests went to its machine")
	}
	local, err := f.e.srv.machineByID(f.local)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.e.srv.claimLocal(local, serverList(movedServer), f.e.clock.now()); len(got) != 1 {
		t.Errorf("the dashboard's machine doesn't show the server moved to it: %v", got)
	}
}

// A copy recorded as left on the machine that runs the server, or on the
// machine it's moving to, is the server or its copy being made: nothing
// deletes it, and its record as a copy left goes.
func TestALeftCopyThatIsTheServerIsNeverDeleted(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	if err := leftCopy(ctx, f.e.srv.db, movedServer, f.rid, f.alex.id, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.srv.db.Exec(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine) VALUES(?, ?, ?, ?)`, movedServer, f.alex.id, f.rid, f.local); err != nil {
		t.Fatal(err)
	}
	if err := leftCopy(ctx, f.e.srv.db, movedServer, f.local, f.alex.id, 0, 0); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	f.e.srv.leaveLeftovers(ctx)
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); ok {
		t.Error("the machine that runs the server deleted it as a copy left there")
	}
	if slices.Contains(f.e.agentHits(), "POST /v1/servers/"+movedServer+"/delete") {
		t.Error("the machine the server is moving to deleted the copy it's making")
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM left_copies`); n != 0 {
		t.Errorf("%d records of copies left stayed", n)
	}
}

// A server whose settings the machine it goes to won't take isn't moved:
// the move stops, that machine deletes the copy it made, and the server
// stays where it was, started again since it ran.
func TestAMoveWhoseSettingsDontArriveIsUndone(t *testing.T) {
	f := newMoveFleet(t)
	f.e.answer("PUT /v1/servers/"+movedServer+"/move-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"The schedules the server had can't be kept.","code":"invalid_request"}`)
	})
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-undo","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-undo", `{"id":"op-undo","status":"succeeded"}`)
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); !strings.Contains(why, "didn't take what home-server kept about it") {
		t.Fatalf("why the move stopped: %q", why)
	}
	if _, started := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); f.recorded(t) != f.rid || f.made() || !started {
		t.Errorf("the server whose settings didn't arrive: requests go to %q, copy still made %v, started again %v", f.recorded(t), f.made(), started)
	}
}

// A server's folder that reaches the other machine changed isn't made into
// the server: the move stops, the upload goes, and the server stays where
// it was.
func TestAFolderThatArrivesChangedIsntMovedIn(t *testing.T) {
	f := newMoveFleet(t)
	f.e.answer("POST /v1/restore/upload", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"`+movedUpload+`","sha256":"`+strings.Repeat("0", 64)+`","compatible":true}`)
	})
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); !strings.Contains(why, "it arrived changed") {
		t.Fatalf("why the move stopped: %q", why)
	}
	if f.made() || !slices.Contains(f.e.agentHits(), "DELETE /v1/restore/"+movedUpload) {
		t.Error("a folder that arrived changed was moved in, or its upload stayed")
	}
	if at := f.recorded(t); at != f.rid {
		t.Errorf("the server's requests go to %q", at)
	}
}

// A move a restart of the dashboard stopped carries on: a server the other
// machine made before the restart isn't copied again, but gets the backup
// rules it had, its requests go there, and the machine it left deletes its
// copy.
func TestAMoveCarriesOnAfterARestart(t *testing.T) {
	f := newMoveFleet(t)
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	f.restarted(t, "op-movein")
	f.e.srv.resumeMoves(context.Background())
	if why := f.moved(t); why != "" {
		t.Fatalf("the move carried on stopped: %s", why)
	}
	if at := f.recorded(t); at != f.local {
		t.Errorf("once the move carried on the server's requests go to %q", at)
	}
	if _, ok := f.ra.saw("GET /v1/servers/" + movedServer + "/move-out"); ok {
		t.Error("the server's folder was copied again, though the other machine had made it")
	}
	if rules := f.e.agentBody("POST /v1/servers/" + movedServer + "/backup-rules"); !strings.Contains(rules, `"everyHours":24`) || !strings.Contains(rules, `"daily":5`) {
		t.Errorf("the server whose move carried on got the backup rules %q", rules)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); !ok {
		t.Error("the machine it left kept its copy")
	}
}

// A move a restart of the dashboard stopped that can't go on leaves its
// server where it was, as a failed move does: started again since it ran,
// its requests going there, and the copy the move made deleted. So it is
// when the machine it was going to was removed while the dashboard was
// down, and when the machine it's on doesn't answer. One deleted meanwhile
// has the copy deleted too.
func TestAMoveThatCantGoOnLeavesItsServerWhereItWas(t *testing.T) {
	// restarted is a move of alex's to the dashboard's machine, whose copy
	// there is made, or with removed to a machine removed since, which
	// leaves them no machine, stopped by a restart.
	restarted := func(t *testing.T, removed, made bool) *moveFleet {
		t.Helper()
		f := newMoveFleet(t)
		to, home := f.local, f.local
		if removed {
			to, home = "gonemach12", ""
		}
		f.mu.Lock()
		f.madeHere = made
		f.mu.Unlock()
		for _, q := range []string{
			fmt.Sprintf(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by) VALUES(%d, '%s', 0, 'admin')`, f.alex.id, to),
			fmt.Sprintf(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine, ran) VALUES('%s', %d, '%s', '%s', 1)`, movedServer, f.alex.id, f.rid, to),
			fmt.Sprintf(`UPDATE customer_homes SET machine_id = '%s' WHERE user_id = %d`, home, f.alex.id),
		} {
			if _, err := f.e.srv.db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
			f.mu.Lock()
			f.madeHere = false
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"op-undo","status":"running"}`)
		})
		f.e.reply("GET", "/v1/operations/op-undo", `{"id":"op-undo","status":"succeeded"}`)
		return f
	}
	ctx := context.Background()

	f := restarted(t, true, false)
	f.e.srv.resumeMoves(ctx)
	if why := f.moved(t); why != "they have no machine." {
		t.Errorf("why the move whose machine was removed stopped: %q", why)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); !ok || f.rows(t, `SELECT COUNT(*) FROM server_moves`) != 0 {
		t.Errorf("the server moving to a removed machine didn't stay where it was: started %v", ok)
	}
	if m, err := f.e.srv.machineForServer(movedServer); err != nil || m.ID != f.rid {
		t.Errorf("its requests go to %q, %v", m.ID, err)
	}

	f = restarted(t, false, true)
	f.ra.handle("GET /v1/servers/"+movedServer, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, `{"error":"The machine is busy.","code":"unavailable"}`)
	})
	f.e.srv.resumeMoves(ctx)
	if why := f.moved(t); !strings.Contains(why, "didn't say how server "+movedServer+" is") {
		t.Errorf("why the move whose machine didn't answer stopped: %q", why)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); !ok || f.made() || f.rows(t, `SELECT COUNT(*) FROM server_moves`) != 0 {
		t.Errorf("the server on a machine that didn't answer didn't stay where it was: started %v, copy still made %v", ok, f.made())
	}

	f = restarted(t, false, true)
	f.ra.handle("GET /v1/servers/"+movedServer, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"Server not found.","code":"not_found"}`)
	})
	f.e.srv.resumeMoves(ctx)
	if why := f.moved(t); why != "" {
		t.Errorf("the move of a server deleted meanwhile stopped: %q", why)
	}
	if f.made() || f.rows(t, `SELECT COUNT(*) FROM server_moves`) != 0 {
		t.Errorf("the copy of a server deleted meanwhile stayed: %v", f.made())
	}
}

// A move a restart of the dashboard stopped waits for the joined machine
// it's moving a server from, which connects some time after the dashboard
// starts: nothing is undone meanwhile, and once it connects the move
// carries on, the server starting where it goes since it ran.
func TestAMoveFromAJoinedMachineWaitsForItAfterARestart(t *testing.T) {
	f := newMoveFleet(t)
	f.restarted(t, "")
	f.disconnect(t)
	f.e.srv.resumeMoves(context.Background())
	if f.e.srv.moves.running(f.alex.id) || f.rows(t, `SELECT COUNT(*) FROM server_moves`) != 1 || f.rows(t, `SELECT COUNT(*) FROM customer_moves WHERE error = ''`) != 1 {
		t.Fatal("the move carried on, or was undone, while home-server was away")
	}
	f.reconnect(t)
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped once home-server connected: %s", why)
	}
	f.mu.Lock()
	in := f.moveIn
	f.mu.Unlock()
	if at := f.recorded(t); at != f.local || in["start"] != true {
		t.Errorf("once home-server connected the server's requests go to %q, started %v", at, in["start"])
	}
}

// A sleeping server moves as one that runs: it starts where it goes, and
// falls asleep again as its sleep setting says.
func TestASleepingServerIsUpWhereItMoves(t *testing.T) {
	f := newMoveFleet(t)
	f.ra.reply("GET /v1/servers/"+movedServer, strings.Replace(movedStatus, `"phase":"online","desired":"running"`, `"phase":"asleep","desired":"sleeping"`, 1))
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	f.mu.Lock()
	in := f.moveIn
	f.mu.Unlock()
	if in["start"] != true {
		t.Errorf("a sleeping server was made stopped where it moved: %v", in)
	}
}

// A server that ran, whose move failed and which then didn't start again
// where it was, starts once it can: when its machine answers. Moving it
// again meanwhile starts it where it goes, since it ran.
func TestAServerWhoseMoveFailedStartsAgainOnceItCan(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	f.e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"failed","error":"The restored world did not start."}`)
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-undo","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-undo", `{"id":"op-undo","status":"succeeded"}`)
	f.ra.handle("POST /v1/servers/"+movedServer+"/start", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"alex is busy with a backup.","code":"busy"}`)
	})
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why == "" {
		t.Fatal("the move whose move-in fails finished")
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM move_restarts WHERE server_id = ? AND machine_id = ?`, movedServer, f.rid); n != 1 {
		t.Fatalf("a server that didn't start again after its move failed isn't to start again: %d", n)
	}
	f.ra.reply("GET /v1/servers/"+movedServer, strings.Replace(movedStatus, `"phase":"online","desired":"running"`, `"phase":"stopped","desired":"stopped"`, 1))
	f.e.srv.retryRestarts(ctx, "")
	if n := f.rows(t, `SELECT COUNT(*) FROM move_restarts`); n != 1 {
		t.Fatal("a start refused again was taken for done")
	}

	f.e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"succeeded"}`)
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex again: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move tried again stopped: %s", why)
	}
	f.mu.Lock()
	in := f.moveIn
	f.mu.Unlock()
	if in["start"] != true || f.rows(t, `SELECT COUNT(*) FROM move_restarts`) != 0 {
		t.Errorf("a server that ran, moved again after its move failed, was made started %v", in["start"])
	}

	f = newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`INSERT INTO move_restarts(server_id, machine_id, user_id) VALUES(?, ?, ?)`, movedServer, f.rid, f.alex.id); err != nil {
		t.Fatal(err)
	}
	f.e.srv.retryRestarts(ctx, f.rid)
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); !ok || f.rows(t, `SELECT COUNT(*) FROM move_restarts`) != 0 {
		t.Errorf("a server to start again didn't once its machine answered: started %v", ok)
	}
}

// A move a restart stopped carries on only with the copy it made: one an
// earlier move left on the machine it goes to, even complete, is deleted,
// and the server's folder copied again.
func TestAResumedMoveTakesOnlyTheCopyItMade(t *testing.T) {
	f := newMoveFleet(t)
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = false
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-old","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-old", `{"id":"op-old","status":"succeeded"}`)
	f.restarted(t, "")
	f.e.srv.resumeMoves(context.Background())
	if why := f.moved(t); why != "" {
		t.Fatalf("the move carried on stopped: %s", why)
	}
	hits := f.e.agentHits()
	if old, upload := slices.Index(hits, "POST /v1/servers/"+movedServer+"/delete"), slices.Index(hits, "POST /v1/restore/upload"); old < 0 || old > upload {
		t.Errorf("an earlier move's copy was taken for this one's: %v", hits)
	}
	if _, ok := f.ra.saw("GET /v1/servers/" + movedServer + "/move-out"); !ok {
		t.Error("the server's folder wasn't copied again")
	}
}

// A server a customer makes as the owner moves them is among their servers
// when the move reads them, or refused: a move never leaves one behind on
// the machine they're leaving with no move of theirs recorded.
func TestAServerMadeAsAMoveStartsIsntLeftBehind(t *testing.T) {
	f := newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`UPDATE project_members SET allowance_servers = 2 WHERE user_id = ?`, f.alex.id); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.ra.handle("POST /v1/servers", func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"op-create2","serverId":"newserv234","kind":"create","status":"running"}`)
	})
	f.ra.reply("GET /v1/servers/newserv234", strings.NewReplacer(`"id":"cafebabe23"`, `"id":"newserv234"`, `"name":"alex"`, `"name":"alex 2"`, `"slug":"alex"`, `"slug":"alex-2"`).Replace(movedStatus))
	f.ra.handle("POST /v1/servers/newserv234/stop", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"alex 2 is busy with a backup.","code":"busy"}`)
	})
	created, moving := make(chan int, 1), make(chan int, 1)
	go func() {
		created <- f.e.doAside("POST", "/api/machines/"+f.rid+"/servers", `{"name":"alex 2","acceptEula":true,"memoryMB":2048}`, f.alex.auth())
	}()
	<-entered
	go func() {
		moving <- f.e.doAside("POST", "/api/customers/"+strconv.FormatInt(f.alex.id, 10)+"/move", `{"machineId":"`+f.local+`"}`, f.own.auth())
	}()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) && f.rows(t, `SELECT COUNT(*) FROM customer_moves`) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM customer_moves`); n != 0 {
		t.Error("alex's move started while a server of theirs was being made")
	}
	close(release)
	if code := <-created; code != http.StatusOK {
		t.Fatalf("alex makes a server: %d", code)
	}
	if code := <-moving; code != http.StatusAccepted {
		t.Fatalf("moving alex: %d", code)
	}
	if why := f.moved(t); !strings.Contains(why, "alex 2") || f.rows(t, `SELECT COUNT(*) FROM customer_moves WHERE user_id = ?`, f.alex.id) != 1 {
		t.Errorf("the move left alex's new server behind: %q", why)
	}
}

// A server of the customer's that turns up on the machine they're leaving
// while they move, as when a removed machine's host joins again with it, is
// moved too, and the move isn't forgotten while one is left there.
func TestAServerThatTurnsUpDuringAMoveIsMovedToo(t *testing.T) {
	f := newMoveFleet(t)
	var once sync.Once
	f.ra.handle("GET /v1/servers/"+movedServer+"/move-out", func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() {
			for _, q := range []string{
				fmt.Sprintf(`INSERT INTO creator_servers(server_id, user_id, created_at) VALUES('lateserv23', %d, 0)`, f.alex.id),
				fmt.Sprintf(`INSERT INTO server_machines(server_id, machine_id, slug, seen_at) VALUES('lateserv23', '%s', 'late', 0)`, f.rid),
			} {
				if _, err := f.e.srv.db.Exec(q); err != nil {
					t.Error(err)
				}
			}
		})
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(f.archive)
	})
	f.ra.reply("GET /v1/servers/lateserv23", strings.NewReplacer(`"id":"cafebabe23"`, `"id":"lateserv23"`, `"name":"alex"`, `"name":"late"`, `"slug":"alex"`, `"slug":"late"`).Replace(movedStatus))
	f.ra.handle("POST /v1/servers/lateserv23/stop", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"late is busy with a backup.","code":"busy"}`)
	})
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if why := f.moved(t); !strings.HasPrefix(why, "late:") || f.rows(t, `SELECT COUNT(*) FROM customer_moves WHERE user_id = ?`, f.alex.id) != 1 {
		t.Errorf("the server that turned up during alex's move: %q", why)
	}
}

// A server of a customer's on a machine that isn't theirs and wasn't
// removed, with no move of theirs recorded, as when a removed machine's host
// joins again with it, keeps them from making servers, and the owner can
// move them to bring their servers together, their plan ended or not.
func TestServersApartAreBroughtTogether(t *testing.T) {
	f := newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`UPDATE customer_homes SET machine_id = ? WHERE user_id = ?`, f.local, f.alex.id); err != nil {
		t.Fatal(err)
	}
	if r := f.e.do(t, "POST", "/api/machines/"+f.local+"/servers", `{"name":"alex 2","acceptEula":true,"memoryMB":2048}`, f.alex.auth()); r.status != http.StatusConflict {
		t.Errorf("alex makes a server with theirs apart: %d %v", r.status, r.body)
	}
	if r := f.e.do(t, "POST", "/api/servers/"+movedServer+"/settings", `{"memoryMB":4096}`, f.alex.auth()); r.status != http.StatusConflict {
		t.Errorf("alex gives a server more memory with theirs apart: %d %v", r.status, r.body)
	}
	f.pause(t, f.e.clock.now().Add(-time.Hour))
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex, their plan ended, to bring their servers together: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" || f.recorded(t) != f.local {
		t.Fatalf("bringing alex's servers together: %q, requests go to %q", why, f.recorded(t))
	}
}

// A customer whose servers are on two machines while a move of theirs is
// stopped gets their plan's disk once between them: each machine's limit
// for them is what their servers on the others leave of it, as counted
// once the move stopped. A copy the move left isn't theirs on its machine,
// so it isn't counted twice. While a move is under way nothing is split,
// as the last counts are from before servers switched machines. Together
// again, they get all of it.
func TestACustomerWhoseServersAreApartGetsTheirDiskOnce(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	s := f.e.srv
	id := accountLimit(f.alex.id)
	plan := int64(starter.MemoryMB) * 15 << 19
	counted := func(n int64) string {
		return `[{"id":"` + id + `","limitBytes":1,"servers":[],"usedBytes":` + strconv.FormatInt(n, 10) + `}]`
	}
	limitIn := func(body string) api.DiskLimit {
		var req api.DiskLimitsRequest
		json.Unmarshal([]byte(body), &req)
		if i := slices.IndexFunc(req.Limits, func(l api.DiskLimit) bool { return l.ID == id }); i >= 0 {
			return req.Limits[i]
		}
		return api.DiskLimit{}
	}
	f.ra.reply("GET /v1/disk-limits", counted(3<<30))
	f.e.reply("GET", "/v1/disk-limits", counted(0))
	if _, err := s.db.Exec(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by) VALUES(?, ?, 0, 'admin')`, f.alex.id, f.local); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE customer_homes SET machine_id = ? WHERE user_id = ?`, f.local, f.alex.id); err != nil {
		t.Fatal(err)
	}
	s.syncDiskLimits(ctx)
	s.syncDiskLimits(ctx)
	if there, here := limitIn(f.ra.body("PUT /v1/disk-limits")), limitIn(f.e.agentBody("PUT /v1/disk-limits")); there.LimitBytes != plan || here.LimitBytes != plan {
		t.Errorf("alex's limits while their move is under way: home-server %+v, the dashboard's machine %+v", there, here)
	}

	// The move stops, leaving a copy on the dashboard's machine, and alex's
	// server on home-server has grown since it was counted.
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	if err := leftCopy(ctx, s.db, movedServer, f.local, f.alex.id, 0, 0); err != nil {
		t.Fatal(err)
	}
	f.ra.reply("GET /v1/disk-limits", counted(12<<30))
	f.e.reply("GET", "/v1/disk-limits", counted(5<<30))
	if _, err := s.db.Exec(`UPDATE customer_moves SET error = 'It stopped.'`); err != nil {
		t.Fatal(err)
	}
	s.recountDisk()
	select {
	case <-s.diskKick:
	default:
	}
	s.syncDiskLimits(ctx)
	select {
	case <-s.diskKick:
	default:
		t.Error("a sync that counted what alex's servers take didn't have their limits made from it at once")
	}
	s.syncDiskLimits(ctx)
	if there := limitIn(f.ra.body("PUT /v1/disk-limits")); there.LimitBytes != plan-5<<30 {
		t.Errorf("home-server's limit for alex, whose servers are apart: %+v", there)
	}
	if here := limitIn(f.e.agentBody("PUT /v1/disk-limits")); here.LimitBytes != plan-12<<30 || len(here.Servers) != 0 {
		t.Errorf("the dashboard's machine's limit for alex, whose servers are apart: %+v", here)
	}

	for _, q := range []string{`DELETE FROM left_copies`, `DELETE FROM customer_moves`, `UPDATE customer_homes SET machine_id = '` + f.rid + `'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	f.madeHere = false
	f.mu.Unlock()
	s.syncDiskLimits(ctx)
	if there := limitIn(f.ra.body("PUT /v1/disk-limits")); there.LimitBytes != plan {
		t.Errorf("home-server's limit for alex, whose servers are together again: %+v", there)
	}
}

// A sync of the disk limits that read a machine's servers while a moving
// server's copy there was still hidden doesn't send them after the server
// switched there: the server stays in its customer's limit on the machine
// it moved to, with the hold and processor share that go with it.
func TestASyncDuringASwitchLeavesTheServerInItsLimit(t *testing.T) {
	f := newMoveFleet(t)
	s := f.e.srv
	var mu sync.Mutex
	var applied []api.DiskLimitsRequest
	entered, release, synced := make(chan struct{}), make(chan struct{}), make(chan struct{})
	first := true
	f.e.answer("PUT /v1/disk-limits", func(w http.ResponseWriter, _ *http.Request) {
		var req api.DiskLimitsRequest
		json.Unmarshal([]byte(f.e.agentBody("PUT /v1/disk-limits")), &req)
		mu.Lock()
		wait := first
		first = false
		mu.Unlock()
		if wait {
			close(entered)
			<-release
		}
		mu.Lock()
		applied = append(applied, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	f.e.answer("POST /v1/restore/"+movedUpload+"/move-in", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		f.madeHere = true
		f.mu.Unlock()
		go func() {
			s.syncDiskLimits(context.Background())
			close(synced)
		}()
		<-entered
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-movein","status":"running"}`)
	})
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	for deadline := time.Now().Add(time.Second); s.moves.running(f.alex.id) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	<-synced
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	mu.Lock()
	last := applied[len(applied)-1]
	mu.Unlock()
	i := slices.IndexFunc(last.Limits, func(l api.DiskLimit) bool { return l.ID == accountLimit(f.alex.id) })
	if i < 0 || !slices.Contains(last.Limits[i].Servers, movedServer) {
		t.Errorf("the dashboard's machine was last given limits leaving out the server moved there: %+v", last.Limits)
	}
}

// A machine still has a customer whose move stopped with a server on it: it
// counts them, and lists them with their machine and the servers left
// there, so it isn't taken for empty and removed with those servers on it.
func TestAMachineAMoveLeftServersOnCountsTheirCustomer(t *testing.T) {
	f := newMoveFleet(t)
	f.stopped(t, f.local, f.local)
	var view struct {
		Customers int `json:"customers"`
	}
	f.e.get(t, "/api/machines/"+f.rid, f.own.cookie, &view)
	var list []machineCustomer
	f.e.get(t, "/api/machines/"+f.rid+"/customers", f.own.cookie, &list)
	if view.Customers != 1 || len(list) != 1 || list[0].Name != "alex" || list[0].MachineID != f.local || list[0].Here != 1 || list[0].Move == nil || list[0].Move.Left != 1 {
		t.Errorf("home-server, which alex's stopped move left a server on, has %d customers: %+v", view.Customers, list)
	}
}

// Removing a machine places its customers again, as new customers are:
// each gets the fullest other machine with room for their plan, or waits
// for room, and their servers stay on the removed machine. A move of theirs
// that stopped then finishes without the servers left there, so nothing
// keeps them from making new ones. Customers whose machine was removed
// before are placed again too.
func TestARemovedMachinesCustomersGetRoomElsewhere(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	if r := f.e.do(t, "DELETE", "/api/machines/"+f.rid, "", f.own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("removing home-server: %d %v", r.status, r.body)
	}
	if home, placed, _ := f.e.srv.homeMachine(ctx, f.alex.id); placed || home != "" {
		t.Fatalf("alex's machine once home-server is removed: %q", home)
	}
	f.e.srv.startWaitingCustomers(ctx)
	if home, _, _ := f.e.srv.homeMachine(ctx, f.alex.id); home != f.local {
		t.Fatalf("alex's machine once placed again: %q", home)
	}
	rows := f.e.auditRows(t, "customer.place")
	if n := len(rows); n < 2 || !strings.Contains(rows[n-2], "waiting the machine they were on was removed") || !strings.Contains(rows[n-1], "placed on") {
		t.Errorf("audit: %v", rows)
	}

	if _, err := f.e.srv.db.Exec(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by, error) VALUES(?, ?, 0, 'admin', 'It stopped.')`, f.alex.id, f.local); err != nil {
		t.Fatal(err)
	}
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex again: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped on the server left on the removed machine: %s", why)
	}
	if r := f.e.do(t, "POST", "/api/machines/"+f.local+"/servers", `{"name":"alex 2","acceptEula":true,"memoryMB":2048}`, f.alex.auth()); r.status == http.StatusConflict {
		t.Errorf("alex makes a server once their move finished: %d %v", r.status, r.body)
	}

	if _, err := f.e.srv.db.Exec(`UPDATE customer_homes SET machine_id = ? WHERE user_id = ?`, f.rid, f.alex.id); err != nil {
		t.Fatal(err)
	}
	f.e.srv.rehomeStranded(ctx)
	if home, placed, _ := f.e.srv.homeMachine(ctx, f.alex.id); placed || home != "" {
		t.Errorf("a customer whose machine was removed before keeps it: %q", home)
	}
}

// A customer whose servers are being moved, or whose move stopped, isn't
// deleted once their grace period ends while they have a machine the owner
// can move them to again: their servers may be on two. The owner can still
// move them again, their plan having ended, and once their servers are
// together they're deleted there, their final backups kept there.
func TestALapsedCustomerBeingMovedIsDeletedLater(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	f.stopped(t, f.local, f.local)
	f.pause(t, f.e.clock.now().Add(-time.Hour))
	if err := f.e.srv.deleteLapsedCustomer(ctx, f.alex.id); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); ok {
		t.Fatal("the servers of a customer whose move stopped were deleted")
	}

	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex again once their plan ended: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move tried again stopped: %s", why)
	}
	f.e.answer("POST /v1/servers/"+movedServer+"/delete", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"id":"op-lapsed","status":"running"}`)
	})
	f.e.reply("GET", "/v1/operations/op-lapsed", `{"id":"op-lapsed","status":"succeeded"}`)
	if err := f.e.srv.deleteLapsedCustomer(ctx, f.alex.id); err != nil {
		t.Fatal(err)
	}
	var del map[string]any
	json.Unmarshal([]byte(f.e.agentBody("POST /v1/servers/"+movedServer+"/delete")), &del)
	if at, keptOn := f.deleted(t); del["keepFinalBackupDays"] != float64(finalBackupDays) || at == 0 || keptOn != f.local {
		t.Errorf("alex once their servers came together: deleted with %v at %d, final backups on %q", del, at, keptOn)
	}
}

// A customer whose plan ended with a move of theirs stopped, and whose
// machine, where it was going, was removed, has no machine to be moved to
// again: their servers are deleted where the move left them, their final
// backups kept there, and the move ends.
func TestALapsedCustomerWhoseMoveCantGoOnIsDeletedWhereTheirServersAre(t *testing.T) {
	f := newMoveFleet(t)
	f.stopped(t, "gonemach12", "")
	f.pause(t, f.e.clock.now().Add(-time.Hour))
	if err := f.e.srv.deleteLapsedCustomer(context.Background(), f.alex.id); err != nil {
		t.Fatal(err)
	}
	var del map[string]any
	json.Unmarshal([]byte(f.ra.body("POST /v1/servers/"+movedServer+"/delete")), &del)
	if at, keptOn := f.deleted(t); del["keepFinalBackupDays"] != float64(finalBackupDays) || at == 0 || keptOn != f.rid {
		t.Errorf("alex's deletion: %v at %d, final backups on %q", del, at, keptOn)
	}
	if n := f.rows(t, `SELECT COUNT(*) FROM customer_moves WHERE user_id = ?`, f.alex.id); n != 0 {
		t.Error("the move that couldn't go on is still recorded")
	}
}

// A customer whose machine was removed has their servers left on it, out
// of reach. Once their plan ends past its grace period those servers are
// forgotten, not taken for deleted: nothing is deleted on the machine they
// were given since, and they're promised no final backups of them.
func TestALapsedCustomersServersLeftOnARemovedMachineArentTakenForDeleted(t *testing.T) {
	f := newMoveFleet(t)
	ctx := context.Background()
	if r := f.e.do(t, "DELETE", "/api/machines/"+f.rid, "", f.own.auth()); r.status != http.StatusNoContent {
		t.Fatalf("removing home-server: %d %v", r.status, r.body)
	}
	f.e.srv.startWaitingCustomers(ctx)
	if home, _, _ := f.e.srv.homeMachine(ctx, f.alex.id); home != f.local {
		t.Fatalf("alex's machine once placed again: %q", home)
	}
	f.pause(t, f.e.clock.now().Add(-time.Hour))
	if err := f.e.srv.deleteLapsedCustomer(ctx, f.alex.id); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.e.agentHits(), "POST /v1/servers/"+movedServer+"/delete") {
		t.Error("the server left on the removed machine was deleted on the one alex was given since")
	}
	n := f.e.srv.notifier.(*recordingNotifier)
	n.mu.Lock()
	sent := slices.Clone(n.sent)
	n.mu.Unlock()
	if len(sent) == 0 || sent[len(sent)-1].Kind != messageDeleted || strings.Contains(sent[len(sent)-1].Text, "final backup") {
		t.Errorf("what alex was told: %+v", sent)
	}
	if at, keptOn := f.deleted(t); at == 0 || keptOn != "" {
		t.Errorf("alex's record: deleted at %d, final backups on %q", at, keptOn)
	}
	if ids, _ := f.e.srv.creatorServers(f.alex.id); len(ids) != 0 {
		t.Errorf("alex still created %v", ids)
	}
}

// A paused customer's servers stay stopped wherever they go: one that ran
// is made stopped on the machine it moves to, and one whose customer is
// paused while it moves stops there once its requests go there, as does
// one whose copy started before a restart of the dashboard. One whose move
// fails isn't started again where it was.
func TestAPausedCustomersServerMovesStopped(t *testing.T) {
	f := newMoveFleet(t)
	f.pause(t, f.e.clock.now().Add(time.Hour))
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex while they're paused: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	f.mu.Lock()
	in := f.moveIn
	f.mu.Unlock()
	if in["serverId"] != movedServer || in["start"] == true {
		t.Errorf("a paused customer's server was made where it moved from %v", in)
	}

	f = newMoveFleet(t)
	gate := make(chan struct{})
	f.e.agent.mu.Lock()
	f.e.agent.gates["PUT /v1/disk-limits"] = gate
	f.e.agent.mu.Unlock()
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	eventually(t, "the dashboard's machine is sent alex's disk limit", func() bool { return f.e.sawLocally("PUT /v1/disk-limits") })
	f.pause(t, f.e.clock.now().Add(time.Hour))
	close(gate)
	if why := f.moved(t); why != "" {
		t.Fatalf("the move stopped: %s", why)
	}
	hits := f.e.agentHits()
	if sent, stopped := slices.Index(hits, "PUT /v1/disk-limits"), slices.Index(hits, "POST /v1/servers/"+movedServer+"/stop"); stopped < sent {
		t.Errorf("a server whose customer was paused while it moved kept running where it went: %v", hits)
	}

	f = newMoveFleet(t)
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	f.restarted(t, "op-movein")
	f.pause(t, f.e.clock.now().Add(time.Hour))
	f.e.srv.resumeMoves(context.Background())
	if why := f.moved(t); why != "" {
		t.Fatalf("the move carried on stopped: %s", why)
	}
	if !f.e.sawLocally("POST /v1/servers/" + movedServer + "/stop") {
		t.Error("a copy that started before a restart kept running once its customer was paused")
	}

	f = newMoveFleet(t)
	f.e.reply("GET", "/v1/operations/op-movein", `{"id":"op-movein","status":"failed","error":"The restored world did not start."}`)
	f.pause(t, f.e.clock.now().Add(time.Hour))
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex while they're paused: %d %v", r.status, r.body)
	}
	if why := f.moved(t); why == "" {
		t.Fatal("the move whose move-in fails finished")
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/start"); ok {
		t.Error("a paused customer's server whose move failed started again where it was")
	}
}

// Pausing a customer stops each of their servers where it is: one a move of
// theirs that stopped left on the machine it came from stops there.
func TestPausingStopsTheServersAStoppedMoveLeftBehind(t *testing.T) {
	f := newMoveFleet(t)
	f.stopped(t, f.local, f.local)
	core := customerCore{s: f.e.srv}
	if err := core.PauseCustomer(context.Background(), Customer{Provider: whopProvider, Subject: "user_alex", Handle: "alex"}, "their Whop membership is expired"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/stop"); !ok {
		t.Error("the server a stopped move left on home-server kept running once alex was paused")
	}
}

// With no machine named, a move passes over one that can't keep servers
// away from itself, as placement does, for the next fullest with room.
func TestAMoveToTheFullestPassesOverAMachineItCantGuard(t *testing.T) {
	f := newMoveFleet(t)
	attic, _, _, _ := joinForCustomers(t, f.e, f.own)
	if r := f.e.do(t, "PUT", "/api/machines/"+attic+"/customers", `{"on":true}`, f.own.auth()); r.status != http.StatusOK {
		t.Fatalf("the owner confirms attic: %d %v", r.status, r.body)
	}
	f.e.reply("GET", "/v1/machine", liveMachine(8192, false))
	f.e.reply("POST", "/v1/network-guard", `{"on":true,"host":false}`)
	if r := f.move(t, ""); r.status != http.StatusAccepted || r.body["machineId"] != attic {
		t.Errorf("moving alex to the fullest machine with room it can guard: %d %v", r.status, r.body)
	}
	f.moved(t)
}
