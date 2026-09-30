package agent

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// moveOut streams the current server's whole folder, as the dashboard asks
// for it when it moves the server.
func (e *agentEnv) moveOut() (int, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+e.sp("/move-out"), nil)
	req.Header.Set("X-Playkeeper-Actor", "playkeeper")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// A server moved to another machine takes its whole folder, not what a
// backup keeps: a plugin's world, its server type's own settings, scripts
// and files uploaded at the top all arrive where it goes, and a server with
// no world, as before its first start, moves too. Nothing is written where
// it was, so a server whose disk limit is full moves as it is. Its folder
// goes only while it's stopped, and only a move takes it: a restore refuses
// it.
func TestAServerMovesWithItsWholeFolder(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival", "playStyle": "friends"})
	id := e.sid
	had, err := e.srv().serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"creative/level.dat": "a plugin's world", "purpur.yml": "purpur: true", "scripts/recipes.zs": "// crafttweaker", "notes.txt": "uploaded at the top"}
	for rel, body := range files {
		p := filepath.Join(e.dataDir(), rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.setLimits(map[string]any{"id": "account-7", "limitBytes": 1, "servers": []string{id}})
	if code, _ := e.moveOut(); code != http.StatusConflict {
		t.Fatalf("moving out a running server: %d", code)
	}
	if op := e.act("stop"); op.Status != api.OpSucceeded {
		t.Fatalf("stop: %+v", op)
	}
	if err := os.RemoveAll(filepath.Join(e.dataDir(), "world")); err != nil {
		t.Fatal(err)
	}
	code, raw := e.moveOut()
	if code != http.StatusOK {
		t.Fatalf("moving out Survival, stopped, with its disk limit full: %d %s", code, raw)
	}
	code, out := e.uploadTo("/v1/restore/upload", raw)
	if code != http.StatusOK {
		t.Fatalf("staging Survival's folder: %d %v", code, out)
	}
	rid := out["id"].(string)
	if code, out := e.callWhenFree("POST", "/v1/restore/"+rid+"/apply", map[string]any{"actor": "admin", "confirm": "Survival"}); code != http.StatusConflict || !strings.Contains(out["error"].(string), "whole folder") {
		t.Errorf("restoring a server's whole folder: %d %v", code, out)
	}

	code, out = e.callWhenFree("POST", e.sp("/delete"), map[string]any{"confirm": "Survival", "actor": "admin"})
	if code != http.StatusAccepted {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	code, out = e.moveIn(rid, moveInBody(id, "survival", had, false))
	if code != http.StatusAccepted {
		t.Fatalf("move-in: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("move-in: %+v", op)
	}
	for rel, body := range files {
		if got, err := os.ReadFile(filepath.Join(e.dataDir(), rel)); err != nil || string(got) != body {
			t.Errorf("%s where Survival moved: %q, %v", rel, got, err)
		}
	}
}
