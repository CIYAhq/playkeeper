package agent

import (
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// The fresh-install walkthrough of 1 Oct 2026: new servers have the
// allowlist on, and nobody asked for the owner's own name, so their first
// join was refused. The players a create names go on the allowlist once the
// server runs, after a first start that failed too, each once, and are then
// forgotten.
func TestACreatesPlayersGoOnTheAllowlistOnceTheServerRuns(t *testing.T) {
	e := newAgentEnv(t)
	e.fd.mu.Lock()
	e.fd.failBoots = 1
	e.fd.mu.Unlock()
	code, out := e.startCreate(map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "players": []string{"Steve_Builds", "steve_builds"}})
	if code != 202 {
		t.Fatalf("create: %d %v", code, out)
	}
	e.sid = out["serverId"].(string)
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("the first start: %+v", op)
	}
	if n := e.rcon.count("whitelist add Steve_Builds"); n != 0 {
		t.Fatalf("a server that never ran had its allowlist changed %d times", n)
	}
	if sc := e.status().Config; sc == nil || sc.PendingPlayers != "Steve_Builds" {
		t.Fatalf("after a first start that failed: %+v", sc)
	}

	code, out = e.call("POST", e.sp("/start"), map[string]any{"actor": "siya"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the start: %+v", op)
	}
	if n := e.rcon.count("whitelist add Steve_Builds"); n != 1 {
		t.Fatalf("whitelist add Steve_Builds ran %d times: %v", n, e.rcon.commandsSent())
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'whitelist.add' AND target = 'Steve_Builds' AND result = 'succeeded' AND actor = 'siya'`); n != 1 {
		t.Fatalf("%d audit entries for the add", n)
	}
	if sc := e.status().Config; sc == nil || sc.PendingPlayers != "" {
		t.Fatalf("the players were kept after they were added: %+v", sc)
	}

	code, out = e.callWhenFree("POST", e.sp("/restart"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("restart: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("the restart: %+v", op)
	}
	if n := e.rcon.count("whitelist add Steve_Builds"); n != 1 {
		t.Fatalf("a restart added the player again: %d", n)
	}
}

func TestACreateRefusesPlayersMinecraftCantHave(t *testing.T) {
	e := newAgentEnv(t)
	for _, players := range [][]string{{"bad name;op"}, {"ab"}, {"Alex", "Bea", "Cai", "Dev", "Eli", "Fay"}} {
		code, out := e.call("POST", "/v1/servers", map[string]any{"acceptEula": true, "versionId": "paper-26.1.2", "memoryMB": 1536, "actor": "admin", "players": players})
		if code != 400 {
			t.Errorf("%v: %d %v", players, code, out)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM servers`); n != 0 {
		t.Fatalf("a refused create left %d servers", n)
	}
}
