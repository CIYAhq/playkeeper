package agent

import (
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// The fresh-install walkthrough of 1 Oct 2026: new servers have the
// allowlist on, and nobody asked for the owner's own name, so their first
// join was refused. The players a create names go on the allowlist and are
// made operators once the server runs, after a first start that failed too,
// each once, and are then forgotten.
func TestACreatesOperatorsGoOnTheAllowlistOnceTheServerRuns(t *testing.T) {
	e := newAgentEnv(t)
	e.fd.mu.Lock()
	e.fd.failBoots = 1
	e.fd.mu.Unlock()
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": []string{"Steve_Builds", "steve_builds"}})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the first start: %+v", op)
	}
	commands := []string{"whitelist add Steve_Builds", "op Steve_Builds"}
	for _, cmd := range commands {
		if n := e.rcon.count(cmd); n != 0 {
			t.Fatalf("a server that never ran got %q %d times", cmd, n)
		}
	}
	if sc := e.status().Config; sc == nil || sc.PendingOperators != "Steve_Builds" {
		t.Fatalf("after a first start that failed: %+v", sc)
	}

	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "siya"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the start: %+v", op)
	}
	for _, cmd := range commands {
		if n := e.rcon.count(cmd); n != 1 {
			t.Fatalf("%q ran %d times: %v", cmd, n, e.rcon.commandsSent())
		}
	}
	for _, action := range []string{"whitelist.add", "operator.add"} {
		if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = '` + action + `' AND target = 'Steve_Builds' AND result = 'succeeded' AND actor = 'siya'`); n != 1 {
			t.Fatalf("%d audit entries for %s", n, action)
		}
	}
	if sc := e.status().Config; sc == nil || sc.PendingOperators != "" {
		t.Fatalf("the players were kept after they were added: %+v", sc)
	}

	code, out = e.callWhenFree("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("restart: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the restart: %+v", op)
	}
	for _, cmd := range commands {
		if n := e.rcon.count(cmd); n != 1 {
			t.Fatalf("a restart sent %q again: %d", cmd, n)
		}
	}
}

// A name Minecraft doesn't know can't go on the allowlist, so it isn't made
// an operator either, and the audit log says why.
func TestACreatesUnknownPlayerIsNotMadeAnOperator(t *testing.T) {
	e := newAgentEnv(t)
	e.rcon.mu.Lock()
	e.rcon.answer = func(cmd string) (string, bool) {
		return "That player does not exist", cmd == "whitelist add Nobody_Here"
	}
	e.rcon.mu.Unlock()
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": []string{"Nobody_Here"}})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the first start: %+v", op)
	}
	if n := e.rcon.count("op Nobody_Here"); n != 0 {
		t.Fatalf("a player who isn't on the allowlist was made an operator %d times", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'whitelist.add' AND target = 'Nobody_Here' AND result = 'failed' AND detail = 'That player does not exist'`); n != 1 {
		t.Fatalf("%d audit entries for the failed add", n)
	}
	if sc := e.status().Config; sc == nil || sc.PendingOperators != "" {
		t.Fatalf("a name Minecraft doesn't know was kept to try again: %+v", sc)
	}
}

func TestACreateRefusesOperatorsMinecraftCantHave(t *testing.T) {
	e := newAgentEnv(t)
	for _, operators := range [][]string{{"bad name;op"}, {"ab"}, {"Alex", "Bea", "Cai", "Dev", "Eli", "Fay"}} {
		code, out := e.call("POST", "/v1/servers", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": operators})
		if code != 400 {
			t.Errorf("%v: %d %v", operators, code, out)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 0 {
		t.Fatalf("a refused create left %d servers", n)
	}
}
