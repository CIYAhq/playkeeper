package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/agentclient"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/machinelink"
	"github.com/CIYAhq/playkeeper/internal/mcp"
)

func TestTheToolsRegister(t *testing.T) {
	tools := Tools(newWorld(false))
	if _, err := mcp.New(mcp.Options{Tools: tools, Instructions: Instructions}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		perServer := slices.Contains(tool.InputSchema.Required, "server")
		if perServer == (tool.Name == "list_servers") {
			t.Errorf("%s: names a server: %v", tool.Name, perServer)
		}
	}
	want := []string{"list_servers", "get_server_status", "start_server", "stop_server", "restart_server", "read_console",
		"send_chat_message", "run_console_command", "list_online_players", "list_whitelist", "add_to_whitelist",
		"remove_from_whitelist", "list_backups", "create_backup", "get_operation", "get_lag_report", "explain_crash"}
	if !slices.Equal(names, want) {
		t.Errorf("tools %v\nwant %v", names, want)
	}
}

func serverIDs(r result) []string {
	list, _ := r.Structured["servers"].([]any)
	ids := []string{}
	for _, s := range list {
		m, _ := s.(map[string]any)
		id, _ := m["id"].(string)
		ids = append(ids, id)
	}
	return ids
}

// A token for server A can't see or touch server B through any tool, on
// the same machine or another. Naming B gets the answer a server that
// doesn't exist gets, and no agent hears of B.
func TestATokenForOneServerCannotReachAnother(t *testing.T) {
	for _, layout := range []struct {
		name       string
		oneMachine bool
	}{{"two machines", false}, {"one machine", true}} {
		t.Run(layout.name, func(t *testing.T) {
			b := newWorld(layout.oneMachine)
			b.setAccess(Access{Scope: mcp.ScopeOwner, Servers: []string{idA}}, nil)
			c := connect(t, b, token(mcp.ScopeOwner))

			r := c.call("list_servers", nil)
			ok(t, "list_servers", r)
			if got := serverIDs(r); !slices.Equal(got, []string{idA}) {
				t.Errorf("list_servers shows %v", got)
			}
			if strings.Contains(r.Text, "Cobblemon") || strings.Contains(r.Text, idB) || !strings.HasPrefix(r.Text, "1 server:") {
				t.Errorf("list_servers shows the other server:\n%s", r.Text)
			}

			for _, tool := range Tools(b) {
				if !slices.Contains(tool.InputSchema.Required, "server") {
					continue
				}
				missing := c.call(tool.Name, sampleArgs(t, tool, "nosuchserver"))
				if missing.Kind != "server_not_found" {
					t.Fatalf("%s on a server that doesn't exist: %s: %s", tool.Name, missing.Kind, missing.Text)
				}
				if got := b.refusals(); len(got) != 0 {
					t.Errorf("%s on a server that doesn't exist was recorded as refused: %v", tool.Name, got)
				}
				for _, name := range []string{idB, "cobblemon", "Cobblemon", "COBBLEMON"} {
					r := c.call(tool.Name, sampleArgs(t, tool, name))
					want := strings.ReplaceAll(missing.Text, "nosuchserver", name)
					if r.Kind != "server_not_found" || r.Text != want {
						t.Errorf("%s on %q: %s: %s\nwant server_not_found: %s", tool.Name, name, r.Kind, r.Text, want)
					}
					if got := b.refusals(); !slices.Equal(got, []string{tool.Name + " " + RefusedServer}) {
						t.Errorf("%s on %q: refusals recorded: %v", tool.Name, name, got)
					}
					b.mu.Lock()
					b.refused = nil
					b.mu.Unlock()
				}
				ok(t, tool.Name, c.call(tool.Name, sampleArgs(t, tool, idA)))
			}

			for _, req := range b.allRequests() {
				if strings.Contains(req.Path, idB) || strings.Contains(req.Query.Encode(), idB) || strings.Contains(fmt.Sprint(req.Body), idB) {
					t.Errorf("an agent heard of the other server: %+v", req)
				}
			}
			if got := b.agents["m2"].requests(); len(got) != 0 {
				t.Errorf("the other machine's agent got requests: %+v", got)
			}
			for _, done := range b.calls() {
				if strings.Contains(done, idB) {
					t.Errorf("a call on the other server was recorded as done: %s", done)
				}
			}

			// The other server's operation, asked for through A.
			r = c.call("get_operation", map[string]any{"server": idA, "operation": opB})
			if r.Kind != "operation_not_found" || strings.Contains(r.Text, "Cobblemon") {
				t.Errorf("get_operation of the other server's operation: %s: %s", r.Kind, r.Text)
			}
		})
	}
}

// Rights are looked up again on every call: a token whose account lost
// rights after it authenticated gets only what the account has now.
func TestEveryCallAsksForTheCallersRightsAgain(t *testing.T) {
	b := newWorld(false)
	c := connect(t, b, token(mcp.ScopeOwner))

	b.setAccess(Access{Scope: mcp.ScopeRead, AllServers: true}, nil)
	r := c.call("restart_server", map[string]any{"server": idA})
	if r.Kind != mcp.KindScopeMissing || !strings.Contains(r.Text, "needs the manage scope") {
		t.Errorf("restart_server with read rights: %s: %s", r.Kind, r.Text)
	}
	r = c.call("run_console_command", map[string]any{"server": idA, "command": "op Steve"})
	if r.Kind != mcp.KindScopeMissing {
		t.Errorf("run_console_command with read rights: %s: %s", r.Kind, r.Text)
	}
	if got, want := b.refusals(), []string{"restart_server " + RefusedScope, "run_console_command " + RefusedScope}; !slices.Equal(got, want) {
		t.Errorf("refusals %v, want %v", got, want)
	}
	ok(t, "get_server_status", c.call("get_server_status", map[string]any{"server": idA}))
	for _, req := range b.allRequests() {
		if req.Method != "GET" {
			t.Errorf("a refused call reached the agent: %+v", req)
		}
	}

	b.setAccess(Access{Scope: mcp.ScopeOwner}, nil)
	r = c.call("list_servers", nil)
	ok(t, "list_servers", r)
	if r.Text != "There are no servers you can use." || len(serverIDs(r)) != 0 {
		t.Errorf("list_servers with no servers left: %s %v", r.Text, r.Structured)
	}
	if r := c.call("get_server_status", map[string]any{"server": idA}); r.Kind != "server_not_found" {
		t.Errorf("get_server_status on a server the token lost: %s: %s", r.Kind, r.Text)
	}

	b.setAccess(Access{}, ErrRevoked)
	if r := c.call("list_servers", nil); r.Kind != "token_revoked" || !strings.Contains(r.Text, "Settings › AI agents") {
		t.Errorf("a revoked token: %s: %s", r.Kind, r.Text)
	}
	b.setAccess(Access{}, errors.New("database is locked"))
	if r := c.call("list_servers", nil); r.Kind != mcp.KindInternal || strings.Contains(r.Text, "database") {
		t.Errorf("when rights can't be looked up: %s: %s", r.Kind, r.Text)
	}
}

func TestServersAreFoundByIDSlugOrName(t *testing.T) {
	b := newWorld(false)
	b.add("m1", "my-vps", sample("cccccccccc", "skyblock", "Skyblock"), "1111111111111111")
	b.add("m2", "home-server", sample("dddddddddd", "skyblock", "Skyblock"), "2222222222222222")
	b.add("m2", "home-server", sample("eeeeeeeeee", "creative", "survival"), "3333333333333333")
	c := connect(t, b, token(mcp.ScopeRead))

	for arg, want := range map[string]string{
		idA: idA, "survival": idA, "SURVIVAL": idA, "Cobblemon": idB, "cccccccccc": "cccccccccc",
		// A slug wins over another server's name.
		"creative": "eeeeeeeeee",
	} {
		r := c.call("get_server_status", map[string]any{"server": arg})
		ok(t, "get_server_status "+arg, r)
		if got := r.Structured["id"]; got != want {
			t.Errorf("%q named %v, want %s", arg, got, want)
		}
	}

	r := c.call("get_server_status", map[string]any{"server": "Skyblock"})
	want := `Several servers match "Skyblock": cccccccccc (Skyblock on my-vps), dddddddddd (Skyblock on home-server). Call the tool again with the server's id.`
	if r.Kind != "server_ambiguous" || r.Text != want {
		t.Errorf("an ambiguous name: %s: %s\nwant %s", r.Kind, r.Text, want)
	}

	// Servers the caller can't use don't make a name ambiguous.
	b.setAccess(Access{Scope: mcp.ScopeRead, Servers: []string{"cccccccccc"}}, nil)
	r = c.call("get_server_status", map[string]any{"server": "skyblock"})
	ok(t, "get_server_status skyblock", r)
	if r.Structured["id"] != "cccccccccc" || strings.Contains(r.Text, "home-server") {
		t.Errorf("skyblock with one of them covered: %v: %s", r.Structured["id"], r.Text)
	}
}

// Every request reaches the agent as the caller: in the context, which a
// machine link sends as the actor, and in the request itself for the
// agent's audit log.
func TestEveryRequestIsMadeAsTheCaller(t *testing.T) {
	b := newWorld(false)
	c := connect(t, b, token(mcp.ScopeOwner))
	var want []string
	for _, tool := range Tools(b) {
		ok(t, tool.Name, c.call(tool.Name, sampleArgs(t, tool, idA)))
		if tool.Name == "list_servers" {
			want = append(want, tool.Name)
			continue
		}
		want = append(want, tool.Name+" "+idA)
	}
	if got := b.calls(); !slices.Equal(got, want) {
		t.Errorf("calls recorded as done:\n%v\nwant\n%v", got, want)
	}

	var commands, changes []string
	for _, req := range b.agents["m1"].requests() {
		if req.Actor != actor {
			t.Errorf("%s %s was made as %q", req.Method, req.Path, req.Actor)
		}
		switch req.Method {
		case "POST":
			if req.Body["actor"] != actor {
				t.Errorf("POST %s names the actor %v", req.Path, req.Body["actor"])
			}
			if cmd, ok := req.Body["command"].(string); ok {
				commands = append(commands, cmd)
			}
			changes = append(changes, "POST "+strings.TrimPrefix(req.Path, "/v1/servers/"+idA))
		case "DELETE":
			if got := req.Query.Get("actor"); got != actor {
				t.Errorf("DELETE %s names the actor %q", req.Path, got)
			}
			changes = append(changes, "DELETE "+strings.TrimPrefix(req.Path, "/v1/servers/"+idA))
		}
	}
	if want := []string{"say Back in five minutes", "time set day"}; !slices.Equal(commands, want) {
		t.Errorf("console commands %q, want %q", commands, want)
	}
	wantChanges := []string{"POST /start", "POST /stop", "POST /restart", "POST /command", "POST /command",
		"POST /whitelist", "DELETE /whitelist/Steve_1", "POST /backups"}
	if !slices.Equal(changes, wantChanges) {
		t.Errorf("changes %v\nwant %v", changes, wantChanges)
	}
}

func TestAgentErrorsReachTheModelAsToolErrors(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeOwner))
	p := "/v1/servers/" + idA

	a.on("POST", p+"/command", http.StatusConflict, api.Error{Error: "The server is offline.", Code: "server_offline", Hint: "Start it first."})
	r := c.call("send_chat_message", map[string]any{"server": idA, "message": "hi"})
	if r.Kind != "server_offline" || r.Text != "The server is offline. Start it first." {
		t.Errorf("an agent error: %s: %s", r.Kind, r.Text)
	}

	long := strings.Repeat("é", 600) + "\xff"
	a.on("POST", p+"/restart", http.StatusConflict, api.Error{Error: long, Code: api.CodeBusy})
	r = c.call("restart_server", map[string]any{"server": idA})
	if r.Kind != api.CodeBusy || r.Text != strings.Repeat("é", 500)+"…" {
		t.Errorf("a long error: %s: %q", r.Kind, r.Text)
	}

	a.fail("POST", p+"/stop", &machinelink.Error{Code: machinelink.CodeNotConnected, Msg: "my-vps is not connected.", Hint: "Check that it is on."})
	r = c.call("stop_server", map[string]any{"server": idA})
	if r.Kind != machinelink.CodeNotConnected || r.Text != "my-vps is not connected. Check that it is on." {
		t.Errorf("a machine link error: %s: %s", r.Kind, r.Text)
	}

	a.on("GET", p+"/backups", http.StatusOK, "not json")
	if r := c.call("list_backups", map[string]any{"server": idA}); r.Kind != machinelink.CodeProtocol {
		t.Errorf("an answer that isn't JSON: %s: %s", r.Kind, r.Text)
	}
	a.on("POST", p+"/backups", http.StatusAccepted, map[string]any{"unexpected": true})
	if r := c.call("create_backup", map[string]any{"server": idA}); r.Kind != machinelink.CodeProtocol {
		t.Errorf("an operation without an id: %s: %s", r.Kind, r.Text)
	}
	a.on("GET", p+"/logs", http.StatusInternalServerError, "<html>")
	if r := c.call("read_console", map[string]any{"server": idA}); r.Kind != api.CodeInternal || r.Text != "agent returned HTTP 500" {
		t.Errorf("an error that isn't JSON: %s: %s", r.Kind, r.Text)
	}
	a.fail("GET", p+"/whitelist", errors.New("dial unix /run/playkeeper/agent.sock: connect: connection refused"))
	r = c.call("list_whitelist", map[string]any{"server": idA})
	if r.Kind != api.CodeAgentUnavailable || strings.Contains(r.Text, "/run/") || !strings.Contains(r.Hint, "systemctl status playkeeper-agent") {
		t.Errorf("an agent that isn't running: %s: %s", r.Kind, r.Text)
	}

	if err := agentError(context.Canceled); err != context.Canceled {
		t.Errorf("a cancelled request became %v", err)
	}
	if err := agentError(fmt.Errorf("%w: %w", agentclient.ErrUnavailable, context.DeadlineExceeded)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a deadline became %v", err)
	}
}

func TestListServers(t *testing.T) {
	b := newWorld(false)
	b.servers[1].LastKnownAt = new(t0.Add(-time.Hour))
	b.servers[0].Players.Online = 1
	b.servers[0].Players.Names = []string{"Alex"}
	c := connect(t, b, token(mcp.ScopeRead))
	r := c.call("list_servers", nil)
	ok(t, "list_servers", r)
	want := "2 servers:\n" +
		"- Survival on my-vps (id aaaaaaaaaa, slug survival): online, 1 of 20 players online, Paper 1.21.8\n" +
		"- Cobblemon on home-server (id bbbbbbbbbb, slug cobblemon): its machine can't be reached; it was online as of 2026-09-25 11:00 UTC"
	if r.Text != want {
		t.Errorf("text:\n%s\nwant\n%s", r.Text, want)
	}
	list, _ := r.Structured["servers"].([]any)
	second, _ := list[1].(map[string]any)
	if second["status"] != "unreachable" || second["lastKnownStatus"] != "online" || second["playersOnline"] != nil {
		t.Errorf("an unreachable server: %v", second)
	}
}

func TestOperationsAndTheirAnswers(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeManage))
	p := "/v1/servers/" + idA

	r := c.call("start_server", map[string]any{"server": "survival"})
	ok(t, "start_server", r)
	want := `Starting Survival. It runs in the background as operation 0123456789abcdef: call get_operation with server "aaaaaaaaaa" and that id to follow it.`
	if r.Text != want {
		t.Errorf("start_server: %s", r.Text)
	}
	if op, _ := r.Structured["operation"].(map[string]any); op["id"] != opA || op["status"] != api.OpRunning {
		t.Errorf("start_server's operation: %v", r.Structured)
	}

	a.on("POST", p+"/stop", http.StatusOK, map[string]any{"noop": true, "message": "Survival is already stopped."})
	r = c.call("stop_server", map[string]any{"server": idA})
	if r.Text != "Survival is already stopped." || r.Structured["noop"] != true {
		t.Errorf("stop_server with nothing to do: %s %v", r.Text, r.Structured)
	}

	r = c.call("create_backup", map[string]any{"server": idA, "note": "  before the update  "})
	ok(t, "create_backup", r)
	reqs := a.requests()
	if last := reqs[len(reqs)-1]; last.Body["note"] != "before the update" {
		t.Errorf("create_backup sent %v", last.Body)
	}

	finished := t0.Add(time.Minute)
	for _, tc := range []struct {
		op   api.Operation
		want string
	}{
		{api.Operation{ID: opA, ServerID: idA, Kind: "restart", Status: api.OpRunning, Phase: "starting"},
			"The restart of Survival is still running (starting); check again in a few seconds."},
		{api.Operation{ID: opA, ServerID: idA, Kind: "restart", Status: api.OpSucceeded, FinishedAt: &finished},
			"The restart of Survival succeeded at 2026-09-25 12:01 UTC."},
		{api.Operation{ID: opA, ServerID: idA, Kind: "backup", Status: api.OpFailed, Error: "The disk is full.", Hint: "Free some space."},
			"The backup of Survival failed: The disk is full. Free some space."},
	} {
		a.on("GET", "/v1/operations/"+opA, http.StatusOK, tc.op)
		r := c.call("get_operation", map[string]any{"server": idA, "operation": opA})
		ok(t, "get_operation", r)
		if r.Text != tc.want {
			t.Errorf("get_operation: %s\nwant %s", r.Text, tc.want)
		}
	}
	for _, answer := range []reply{
		{status: http.StatusNotFound, body: api.Error{Error: "Operation not found.", Code: api.CodeNotFound}},
		{status: http.StatusOK, body: api.Operation{ID: opA, Kind: "update", Status: api.OpRunning}},
	} {
		a.on("GET", "/v1/operations/"+opA, answer.status, answer.body)
		r := c.call("get_operation", map[string]any{"server": idA, "operation": opA})
		if r.Kind != "operation_not_found" {
			t.Errorf("get_operation of %v: %s: %s", answer.body, r.Kind, r.Text)
		}
	}
	if r := c.call("get_operation", map[string]any{"server": idA, "operation": "../../v1/servers"}); r.Kind != mcp.KindInvalidArguments {
		t.Errorf("get_operation with a path for an id: %s: %s", r.Kind, r.Text)
	}
}

func TestConsoleAndChat(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeOwner))
	p := "/v1/servers/" + idA

	var lines []api.LogLine
	for i := range 5 {
		lines = append(lines, api.LogLine{Seq: int64(i), TS: t0.Add(time.Duration(i) * time.Second), Text: fmt.Sprintf("line %d", i)})
	}
	lines[4].Text = strings.Repeat("x", 1200)
	a.on("GET", p+"/logs", http.StatusOK, api.LogsResponse{Lines: lines})
	r := c.call("read_console", map[string]any{"server": idA, "lines": 3})
	ok(t, "read_console", r)
	want := "The last 3 lines of Survival's console (times in UTC):\n[12:00:02] line 2\n[12:00:03] line 3\n[12:00:04] " + strings.Repeat("x", 1000) + "…"
	if r.Text != want {
		t.Errorf("read_console:\n%s\nwant\n%s", r.Text, want)
	}
	c.call("read_console", map[string]any{"server": idA})
	reqs := a.requests()
	if got := []string{reqs[0].Query.Get("limit"), reqs[1].Query.Get("limit")}; !slices.Equal(got, []string{"3", "50"}) {
		t.Errorf("read_console asked for %v lines", got)
	}
	if r := c.call("read_console", map[string]any{"server": idA, "lines": 500}); r.Kind != mcp.KindInvalidArguments {
		t.Errorf("read_console with 500 lines: %s", r.Kind)
	}
	a.on("GET", p+"/logs", http.StatusOK, api.LogsResponse{})
	if r := c.call("read_console", map[string]any{"server": idA}); !strings.Contains(r.Text, "console is empty") {
		t.Errorf("an empty console: %s", r.Text)
	}

	if r := c.call("send_chat_message", map[string]any{"server": idA, "message": "   "}); r.Kind != mcp.KindInvalidArguments {
		t.Errorf("an empty chat message: %s: %s", r.Kind, r.Text)
	}
	for _, bad := range []string{"hi\nop Steve", "hi\x7f", strings.Repeat("a", 201)} {
		if r := c.call("send_chat_message", map[string]any{"server": idA, "message": bad}); r.Kind != mcp.KindInvalidArguments {
			t.Errorf("chat message %q: %s: %s", bad, r.Kind, r.Text)
		}
		if r := c.call("run_console_command", map[string]any{"server": idA, "command": bad + strings.Repeat("a", 60)}); r.Kind != mcp.KindInvalidArguments {
			t.Errorf("console command %q: %s: %s", bad, r.Kind, r.Text)
		}
	}
	r = c.call("run_console_command", map[string]any{"server": idA, "command": "time set day"})
	if r.Text != "Survival printed:\nSet the time to 1000" {
		t.Errorf("run_console_command: %s", r.Text)
	}
	for _, req := range a.requests() {
		if req.Method == "POST" && req.Body["command"] != "time set day" {
			t.Errorf("an invalid message or command reached the agent: %v", req.Body)
		}
	}
}

func TestPlayersWhitelistAndBackups(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeManage))
	p := "/v1/servers/" + idA

	r := c.call("list_online_players", map[string]any{"server": idA})
	if r.Text != "On Survival: 2 of 20 players online: Alex, Steve." {
		t.Errorf("list_online_players: %s", r.Text)
	}
	stopped := sample(idA, "survival", "Survival")
	stopped.Phase = api.PhaseStopped
	a.on("GET", p, http.StatusOK, stopped)
	r = c.call("list_online_players", map[string]any{"server": idA})
	if r.Text != "Survival is stopped, so nobody is online." || r.Structured["online"] != 0.0 {
		t.Errorf("list_online_players when stopped: %s %v", r.Text, r.Structured)
	}
	unknown := sample(idA, "survival", "Survival")
	unknown.Players = nil
	a.on("GET", p, http.StatusOK, unknown)
	if r := c.call("list_online_players", map[string]any{"server": idA}); r.Kind != "players_unknown" {
		t.Errorf("list_online_players before the first count: %s: %s", r.Kind, r.Text)
	}

	r = c.call("list_whitelist", map[string]any{"server": idA})
	if r.Text != "Survival's whitelist is on, so only the players on it can join. It lists 2 players: Alex, Steve." {
		t.Errorf("list_whitelist: %s", r.Text)
	}
	if r := c.call("add_to_whitelist", map[string]any{"server": idA, "player": "Steve 1"}); r.Kind != mcp.KindInvalidArguments {
		t.Errorf("add_to_whitelist with a space in the name: %s", r.Kind)
	}
	r = c.call("add_to_whitelist", map[string]any{"server": idA, "player": "Steve_1"})
	if r.Text != "Survival answered: Added Steve_1 to the whitelist." {
		t.Errorf("add_to_whitelist: %s", r.Text)
	}

	var backups []api.Backup
	for i := range 60 {
		backups = append(backups, api.Backup{ID: fmt.Sprintf("bk%02d", i), Kind: "manual", CreatedAt: t0.Add(time.Duration(i) * time.Hour), SizeBytes: 3 << 20})
	}
	backups[59].Note = "before 1.21.9"
	a.on("GET", p+"/backups", http.StatusOK, backups)
	r = c.call("list_backups", map[string]any{"server": idA})
	first := strings.SplitN(r.Text, "\n", 3)
	if len(first) < 3 || first[0] != "Survival has 60 backups; the newest 50:" || first[1] != "- 2026-09-27 23:00 UTC, 3.0 MB, id bk59, note: before 1.21.9" {
		t.Errorf("list_backups: %q", first)
	}
	if list, _ := r.Structured["backups"].([]any); len(list) != 50 {
		t.Errorf("list_backups shows %d backups", len(list))
	}
}

func TestLagAndCrashes(t *testing.T) {
	b := newWorld(false)
	a := b.agents["m1"]
	c := connect(t, b, token(mcp.ScopeRead))
	p := "/v1/servers/" + idA

	lagging := sample(idA, "survival", "Survival")
	lagging.Resources = &api.Resources{TPS: new(12.0), CPUPercent: new(97.0), MemBytes: new(int64(3 << 30)), MemLimitBytes: new(int64(3 << 30))}
	a.on("GET", p, http.StatusOK, lagging)
	r := c.call("get_lag_report", map[string]any{"server": idA})
	want := "Survival is online.\nNow: 12.0 TPS, CPU 97%, memory 3.0 GB of 3.0 GB.\n" +
		"The game is lagging well behind full speed (20 TPS).\n" +
		"The CPU is the likely cause: the server uses nearly all it may.\n" +
		"Memory is nearly full, which slows Java down; more memory in the server's settings may help.\n" +
		"In the last hour: CPU peaked at 80% around 11:30 UTC, 3 players at most."
	if r.Text != want {
		t.Errorf("get_lag_report:\n%s\nwant\n%s", r.Text, want)
	}
	if reqs := a.requests(); reqs[len(reqs)-1].Query.Get("range") != "1h" {
		t.Errorf("get_lag_report asked for metrics %v", reqs[len(reqs)-1].Query)
	}

	crashed := sample(idA, "survival", "Survival")
	crashed.Phase, crashed.CrashCount, crashed.LastError, crashed.LastErrorHint = api.PhaseCrashed, 3,
		"The server ran out of memory.", "Give it more memory in its settings."
	a.on("GET", p, http.StatusOK, crashed)
	a.on("GET", p+"/events", http.StatusOK, []api.Event{
		{ID: 1, TS: t0.Add(-3 * time.Hour), Kind: "server_crashed", Detail: "Old crash."},
		{ID: 2, TS: t0.Add(-time.Hour), Kind: "server_crashed", Detail: "The server stopped unexpectedly (exit code 137)."},
		{ID: 3, TS: t0.Add(-30 * time.Minute), Kind: "player_joined", Player: "Alex"},
	})
	r = c.call("explain_crash", map[string]any{"server": idA})
	want = "Survival last crashed at 2026-09-25 11:00 UTC: The server stopped unexpectedly (exit code 137).\n" +
		"It crashed 3 times recently.\n" +
		"What Playkeeper says now: The server ran out of memory. Give it more memory in its settings.\n" +
		"It is crashed now.\n\n" +
		"The console's last lines (times in UTC):\n[12:00:00] Done (3.2s)! For help, type \"help\""
	if r.Text != want {
		t.Errorf("explain_crash:\n%s\nwant\n%s", r.Text, want)
	}

	a.on("GET", p, http.StatusOK, sample(idA, "survival", "Survival"))
	a.on("GET", p+"/events", http.StatusOK, []api.Event{})
	r = c.call("explain_crash", map[string]any{"server": idA})
	if !strings.HasPrefix(r.Text, "Playkeeper hasn't seen Survival crash recently. It is online.") {
		t.Errorf("explain_crash with no crash: %s", r.Text)
	}
}

// "playkeeper mcp" serves this machine's servers to root, as the account
// that ran sudo.
func TestTheCommandLineBackendServesThisMachine(t *testing.T) {
	a := newFakeAgent()
	st := sample(idA, "survival", "Survival")
	a.on("GET", "/v1/servers", http.StatusOK, []api.ServerStatus{st})
	serve(a, st, opA)
	c := connect(t, NewLocal(agentclient.Via(a)), mcp.Principal{ID: "cli:alice", Name: "command line", Scopes: []mcp.Scope{mcp.ScopeOwner}})

	r := c.call("list_servers", nil)
	if r.Text != "1 server:\n- Survival (id aaaaaaaaaa, slug survival): online, 2 of 20 players online, Paper 1.21.8" {
		t.Errorf("list_servers: %s", r.Text)
	}
	ok(t, "run_console_command", c.call("run_console_command", map[string]any{"server": "survival", "command": "time set day"}))
	reqs := a.requests()
	last := reqs[len(reqs)-1]
	if last.Path != "/v1/servers/"+idA+"/command" || last.Actor != "cli:alice" || last.Body["actor"] != "cli:alice" {
		t.Errorf("the command was sent as %+v", last)
	}
}
