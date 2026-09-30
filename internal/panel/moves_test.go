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

	"github.com/CIYAhq/playkeeper/internal/mcp"
)

// moveFleet is alex, a customer on the joined machine home-server with the
// server alex there, running, and the dashboard's own machine with room for
// their plan, each machine's agent ready to do its part of a move.
type moveFleet struct {
	e     *env
	own   member
	alex  member
	rid   string
	ra    *remoteAgent
	local string
	// archive is alex's backup on home-server, and sum its SHA-256.
	archive []byte
	sum     string

	mu sync.Mutex
	// madeHere says the dashboard's machine has made alex's server.
	madeHere bool
	// moveIn is what the dashboard's machine was asked to make it from.
	moveIn map[string]any
}

const (
	movedServer   = "cafebabe23"
	movedBackupID = "20260930-020000-abc123"
	movedStatus   = `{"id":"cafebabe23","name":"alex","slug":"alex","phase":"online","desired":"running","gamePort":25566,
		"config":{"memoryMB":2048,"playStyle":"friends","createdAt":"2026-09-20T10:00:00Z","eulaAcceptedAt":"2026-09-20T10:00:00Z","eulaAcceptedBy":"alex"}}`
	movedUpload = "0123456789abcdef"
)

func newMoveFleet(t *testing.T) *moveFleet {
	t.Helper()
	e := newEnvConfig(t, withDomain, nil)
	own := owner(t, e)
	rid, ra, _, _ := joinForCustomers(t, e, own)
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
	f := &moveFleet{e: e, own: own, alex: signIn(t, e, info.UserID), rid: rid, ra: ra, local: e.localMachine(t), archive: []byte("alex's world, as a backup archive")}
	s := sha256.Sum256(f.archive)
	f.sum = hex.EncodeToString(s[:])

	ra.reply("GET /v1/servers", "["+movedStatus+"]")
	ra.reply("GET /v1/servers/"+movedServer, movedStatus)
	startsOp(ra, "POST /v1/servers/"+movedServer+"/stop", "op-stop", "succeeded", "")
	startsOp(ra, "POST /v1/servers/"+movedServer+"/backups", "op-backup", "succeeded", `,"detail":{"backupId":"`+movedBackupID+`"}`)
	startsOp(ra, "POST /v1/servers/"+movedServer+"/delete", "op-delete", "succeeded", "")
	ra.handle("GET /v1/servers/"+movedServer+"/backups/"+movedBackupID+"/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("X-Playkeeper-SHA256", f.sum)
		w.Write(f.archive)
	})
	ra.reply("GET /v1/servers/"+movedServer+"/backup-rules", `{"automatic":{"enabled":true,"everyHours":24,"onlyIfPlayed":true},"rules":{"onHost":{"daily":5}},"custom":true}`)

	e.reply("GET", "/v1/machine", liveMachine(30000, true))
	e.reply("GET", "/v1/servers", `[]`)
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

// The owner moves a customer to another machine: their plan's memory is set
// aside there at once, and their server follows. It stops, is backed up,
// and the backup goes to the other machine as an upload against their disk
// limit, which makes it with the same id, name and slug, the EULA
// acceptance it had, and started, since it ran. Its requests then go
// there, and the machine it left deletes it, keeping its final backup a
// week. Each step goes in the audit log.
func TestTheOwnerMovesACustomerAndTheirServerFollows(t *testing.T) {
	f := newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`INSERT INTO public_links(kind, token_hash, server_id, machine_id, created_at) VALUES('map', 'h', ?, ?, 0)`, movedServer, f.rid); err != nil {
		t.Fatal(err)
	}
	if r := f.move(t, f.local); r.status != http.StatusAccepted || r.body["machineId"] != f.local {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	if home, _, _ := f.e.srv.homeMachine(context.Background(), f.alex.id); home != f.local {
		t.Fatalf("alex's machine once the move starts: %q", home)
	}
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
	f.e.agent.mu.Lock()
	for _, r := range f.e.agent.reqs {
		if r.method == "POST" && r.path == "/v1/restore/upload" {
			query = r.query.Get("diskLimit")
		}
	}
	f.e.agent.mu.Unlock()
	if query != accountLimit(f.alex.id) {
		t.Errorf("the backup came against the disk limit %q", query)
	}
	hits := f.e.agentHits()
	if limits, upload := slices.Index(hits, "PUT /v1/disk-limits"), slices.Index(hits, "POST /v1/restore/upload"); limits < 0 || limits > upload {
		t.Errorf("the dashboard's machine didn't have alex's disk limit before the backup: %v", hits)
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
	for _, key := range []string{"POST /v1/servers/" + movedServer + "/stop", "POST /v1/servers/" + movedServer + "/backups", "POST /v1/servers/" + movedServer + "/delete"} {
		if actor, ok := f.ra.saw(key); !ok || actor != placementActor {
			t.Errorf("home-server wasn't asked to %s as the dashboard: %v %q", key, ok, actor)
		}
	}
	if !json.Valid([]byte(f.ra.body("POST /v1/servers/" + movedServer + "/delete"))) {
		t.Fatalf("the delete's body: %q", f.ra.body("POST /v1/servers/"+movedServer+"/delete"))
	}
	json.Unmarshal([]byte(f.ra.body("POST /v1/servers/"+movedServer+"/delete")), &del)
	if del["confirm"] != "alex" || del["keepFinalBackupDays"] != float64(movedBackupDays) || del["keptFor"] != keptFor(f.alex.id) || del["forgetKey"] != true {
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
	if r := f.move(t, f.local); r.status != http.StatusAccepted {
		t.Fatalf("moving alex: %d %v", r.status, r.body)
	}
	why := f.moved(t)
	if why != "alex: the dashboard's machine couldn't make it from its backup: The restored world did not start." {
		t.Fatalf("why the move stopped: %q", why)
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

// Only the owner moves customers and sees whose servers go on a machine:
// the move's what customers can never see, and it's the owner's machines'
// room it uses.
func TestOnlyTheOwnerMovesCustomers(t *testing.T) {
	f := newMoveFleet(t)
	admin := addMember(t, f.e, "sam", "admin", "*")
	for _, who := range []member{admin, f.alex} {
		if r := f.e.do(t, "POST", "/api/customers/"+strconv.FormatInt(f.alex.id, 10)+"/move", `{}`, who.auth()); r.status != http.StatusForbidden {
			t.Errorf("account %d moves alex: %d %v", who.id, r.status, r.body)
		}
		if r, body := f.e.raw(t, "GET", "/api/machines/"+f.rid+"/customers", "", who.auth()); r.StatusCode != http.StatusForbidden {
			t.Errorf("account %d lists home-server's customers: %d %s", who.id, r.StatusCode, body)
		}
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
	if err := leftCopy(context.Background(), s.db, "leftsrv234", alpha.ID, 7, movedBackupDays); err != nil {
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
	if n := func() int {
		var n int
		s.db.QueryRow(`SELECT COUNT(*) FROM left_copies`).Scan(&n)
		return n
	}(); n != 0 {
		t.Errorf("%d copies still recorded once alpha listed after its copy went", n)
	}

	// Once movingsrv2's requests go to beta, a listing of beta's asked for
	// before then, without it, doesn't drop its record.
	if _, err := s.db.Exec(`UPDATE server_moves SET to_machine = ? WHERE server_id = 'movingsrv2'`, beta.ID); err != nil {
		t.Fatal(err)
	}
	asked := e.clock.now()
	e.clock.add(time.Second)
	if err := s.switchServer(context.Background(), serverMove{serverID: "movingsrv2", userID: 7, from: alpha.ID, to: beta.ID}, beta, "movingsrv2"); err != nil {
		t.Fatal(err)
	}
	s.claimListing(beta, nil, asked)
	if m, _ := recordOf("movingsrv2"); m != beta.ID {
		t.Errorf("a listing of beta's asked for before the server moved there dropped its record: %q", m)
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
	if err := leftCopy(context.Background(), f.e.srv.db, movedServer, f.local, f.alex.id, 0); err != nil {
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
		t.Errorf("the old copy wasn't deleted before the new backup came: %v", hits)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/backups"); !ok {
		t.Error("the old copy was taken for the server: no new backup was made")
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
	if err := leftCopy(ctx, f.e.srv.db, movedServer, f.rid, f.alex.id, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.srv.db.Exec(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine) VALUES(?, ?, ?, ?)`, movedServer, f.alex.id, f.rid, f.local); err != nil {
		t.Fatal(err)
	}
	if err := leftCopy(ctx, f.e.srv.db, movedServer, f.local, f.alex.id, 0); err != nil {
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

// A backup that reaches the other machine changed isn't made into the
// server: the move stops, the upload goes, and the server stays where it
// was.
func TestABackupThatArrivesChangedIsntMovedIn(t *testing.T) {
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
		t.Error("a backup that arrived changed was moved in, or its upload stayed")
	}
	if at := f.recorded(t); at != f.rid {
		t.Errorf("the server's requests go to %q", at)
	}
}

// A move a restart of the dashboard stopped carries on: a server the other
// machine made before the restart isn't copied again, its requests go
// there, and the machine it left deletes its copy.
func TestAMoveCarriesOnAfterARestart(t *testing.T) {
	f := newMoveFleet(t)
	f.mu.Lock()
	f.madeHere = true
	f.mu.Unlock()
	for _, q := range []string{
		fmt.Sprintf(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by) VALUES(%d, '%s', 0, 'admin')`, f.alex.id, f.local),
		fmt.Sprintf(`INSERT INTO server_moves(server_id, user_id, from_machine, to_machine, ran) VALUES('%s', %d, '%s', '%s', 1)`, movedServer, f.alex.id, f.rid, f.local),
		fmt.Sprintf(`UPDATE customer_homes SET machine_id = '%s' WHERE user_id = %d`, f.local, f.alex.id),
	} {
		if _, err := f.e.srv.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	f.e.srv.resumeMoves(context.Background())
	if why := f.moved(t); why != "" {
		t.Fatalf("the move carried on stopped: %s", why)
	}
	if at := f.recorded(t); at != f.local {
		t.Errorf("once the move carried on the server's requests go to %q", at)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/backups"); ok {
		t.Error("the server was backed up again, though the other machine had made it")
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); !ok {
		t.Error("the machine it left kept its copy")
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
// deleted once their grace period ends until their servers are all on
// their machine: the deletion looks there.
func TestALapsedCustomerBeingMovedIsDeletedLater(t *testing.T) {
	f := newMoveFleet(t)
	if _, err := f.e.srv.db.Exec(`INSERT INTO customer_moves(user_id, to_machine, started_at, started_by, error) VALUES(?, ?, 0, 'admin', 'It stopped.')`, f.alex.id, f.local); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.srv.db.Exec(`UPDATE customers SET state = ?, delete_after = ? WHERE user_id = ?`, string(CustomerPaused), f.e.clock.now().Add(-time.Hour).UnixMilli(), f.alex.id); err != nil {
		t.Fatal(err)
	}
	if err := f.e.srv.deleteLapsedCustomer(context.Background(), f.alex.id); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.ra.saw("POST /v1/servers/" + movedServer + "/delete"); ok {
		t.Error("the servers of a customer whose move stopped were deleted")
	}
}
