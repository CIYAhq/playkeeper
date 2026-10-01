package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// The fresh-install walkthrough of 1 Oct 2026: new servers have the
// allowlist on, and nobody asked for the owner's own name, so their first
// join was refused. The players a create names go on the allowlist and are
// made operators once the server runs, after a first start that failed too,
// each once, and are then forgotten. Spawn protection, which Minecraft
// turns on with the first operator, starts off, so friends can still build
// at spawn, and stays as the owner sets it after that.
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
	if p := readProperties(e.dataDir()); p["spawn-protection"] != "0" {
		t.Fatalf("spawn protection before the first start: %q", p["spawn-protection"])
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

	// The owner turns spawn protection back on.
	e.putData("server.properties", "spawn-protection=16\n")
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
	if p := readProperties(e.dataDir()); p["spawn-protection"] != "16" {
		t.Fatalf("a restart changed the owner's spawn protection to %q", p["spawn-protection"])
	}
}

// A server that has an operator already protects spawn from everyone else,
// as a restore can bring one, so making the owner an operator too leaves
// spawn protection as it is.
func TestOperatorsJoiningAnotherLeaveSpawnProtectionAlone(t *testing.T) {
	e := newAgentEnv(t)
	e.fd.mu.Lock()
	e.fd.failBoots = 1
	e.fd.mu.Unlock()
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": []string{"Steve_Builds"}})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the first start: %+v", op)
	}
	e.putData("ops.json", `[{"uuid":"00000000-0000-0000-0000-0000000000c5","name":"Oscar","level":4}]`)
	e.putData("server.properties", "spawn-protection=16\n")
	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "siya"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the start: %+v", op)
	}
	if n := e.rcon.count("op Steve_Builds"); n != 1 {
		t.Fatalf("op Steve_Builds ran %d times", n)
	}
	if p := readProperties(e.dataDir()); p["spawn-protection"] != "16" {
		t.Fatalf("spawn protection on a server with an operator became %q", p["spawn-protection"])
	}
}

// A create without operators leaves spawn protection as Minecraft has it.
func TestACreateWithoutOperatorsLeavesSpawnProtectionAlone(t *testing.T) {
	e := newAgentEnv(t)
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin"})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the first start: %+v", op)
	}
	if p, ok := readProperties(e.dataDir())["spawn-protection"]; ok {
		t.Fatalf("spawn protection was set to %q", p)
	}
}

// A first start that times out leaves the server starting, and Start then
// finds it running and starts nothing, so the players are added once it's
// online, by the agent's own look at its servers.
func TestACreatesOperatorsAreAddedWhenASlowFirstStartComesUpAfterAll(t *testing.T) {
	e := newAgentEnvWith(t, func(e *agentEnv) { e.tweak = func(o *Options) { o.ReadyTimeout = 300 * time.Millisecond } })
	e.fd.mu.Lock()
	e.fd.bootDelay = 2 * time.Second
	e.fd.mu.Unlock()
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": []string{"Steve_Builds"}})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed || !strings.Contains(op.Error, "did not finish starting") {
		t.Fatalf("the first start: %+v", op)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.rcon.count("op Steve_Builds") == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the players weren't added once the server was online: %v", e.rcon.commandsSent())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n := e.rcon.count("whitelist add Steve_Builds"); n != 1 {
		t.Fatalf("whitelist add Steve_Builds ran %d times", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'operator.add' AND target = 'Steve_Builds' AND result = 'succeeded' AND actor = 'playkeeper'`); n != 1 {
		t.Fatalf("%d audit entries for the operator", n)
	}
	time.Sleep(300 * time.Millisecond)
	if n := e.rcon.count("op Steve_Builds"); n != 1 {
		t.Fatalf("op Steve_Builds ran %d times", n)
	}
	if sc := e.status().Config; sc == nil || sc.PendingOperators != "" {
		t.Fatalf("the players were kept after they were added: %+v", sc)
	}
}

// A server that gives no answer about the players, as its console can just
// after it comes up, keeps them, and they're added once it answers, with one
// line each in the activity.
func TestACreatesOperatorsTheServerDidntAnswerAboutAreTriedAgain(t *testing.T) {
	old := pendingRetryWait
	pendingRetryWait = 200 * time.Millisecond
	t.Cleanup(func() { pendingRetryWait = old })
	for _, c := range []struct {
		name, unanswered string
		// audited is how many lines the start left in the activity.
		audited int
	}{
		{"the allowlist", "whitelist add Steve_Builds", 0},
		{"the operator, once on the allowlist", "op Steve_Builds", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.rcon.mu.Lock()
			e.rcon.hangUp[c.unanswered] = true
			e.rcon.mu.Unlock()
			code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "operators": []string{"Steve_Builds"}})
			if code != 202 {
				t.Fatalf("create: %d %v", code, out)
			}
			e.sid = out["serverId"].(string)
			if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
				t.Fatalf("the first start: %+v", op)
			}
			if sc := e.status().Config; sc == nil || sc.PendingOperators != "Steve_Builds" {
				t.Fatalf("a player the server gave no answer about was forgotten: %+v", sc)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE target = 'Steve_Builds'`); n != c.audited {
				t.Fatalf("%d audit entries after the start, want %d", n, c.audited)
			}
			if c.audited > 0 {
				// Minecraft writes the allowlist as it adds someone; this fake console doesn't.
				e.putData("whitelist.json", `[{"uuid":"00000000-0000-0000-0000-0000000000a1","name":"Steve_Builds"}]`)
			}

			e.rcon.mu.Lock()
			delete(e.rcon.hangUp, c.unanswered)
			e.rcon.mu.Unlock()
			deadline := time.Now().Add(10 * time.Second)
			for e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'operator.add' AND target = 'Steve_Builds'`) == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("the player wasn't added once the server answered: %v", e.rcon.commandsSent())
				}
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(500 * time.Millisecond)
			for _, action := range []string{"whitelist.add", "operator.add"} {
				if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = '` + action + `' AND target = 'Steve_Builds' AND result = 'succeeded'`); n != 1 {
					t.Fatalf("%d audit entries for %s", n, action)
				}
			}
			if sc := e.status().Config; sc == nil || sc.PendingOperators != "" {
				t.Fatalf("the player was kept after it was added: %+v", sc)
			}
		})
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
