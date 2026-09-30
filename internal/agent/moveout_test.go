package agent

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// A server moved to another machine keeps what the agent where it was kept
// about it: its sleep and public page settings, its pack page's and map's
// links, its schedules, its copies somewhere else with their secrets and
// keys, so those copies still open, and the add-ons Playkeeper installed.
// An own address that doesn't fit the machine it goes to is left out, and
// said so.
func TestAServerMovedInKeepsItsSettings(t *testing.T) {
	from := newAgentEnv(t)
	from.addIdleServer()
	id := from.sid
	if code, out := from.call("POST", from.sp("/schedules"), map[string]any{"actor": "admin", "kind": "backup",
		"timing": map[string]any{"kind": "daily", "at": "04:00", "timeZone": "UTC"}}); code != http.StatusCreated {
		t.Fatalf("a schedule: %d %v", code, out)
	}
	for _, q := range []string{
		`UPDATE servers SET sleep = '{"enabled":true,"idleMinutes":15}', public_page = 0, public_about = 'our place', own_address = 'survival.example.com', packs_public = 1, packs_token = 'packtoken' WHERE id = ?`,
		`INSERT INTO maps(server_id, addons, installed_at, public, share_token) VALUES(?, '[]', 1, 1, 'maptoken')`,
		`INSERT INTO offsite(server_id, enabled, config, secret, keys, updated_at) VALUES(?, 1, '{"kind":"s3"}', 'the-secret', 'the-keys', 1)`,
		`INSERT INTO offsite_copies(server_id, backup_id, kind, backup_created_at, file_name, size_bytes, copy, copied_at) VALUES(?, 'b1', 'automatic', 1, 'f.tar.gz', 10, 'copy-1', 2)`,
		`INSERT INTO addons(server_id, source, project_id, name, version_id, file_name, hash_algo, hash, installed_at) VALUES(?, 'playkeeper', 'ai-build-battle', 'AI Build Battle', 'v1', 'ai-build-battle.jar', 'sha256', 'abc', 3)`,
	} {
		if _, err := from.a.db.Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
	code, state := from.call("GET", from.sp("/move-state"), nil)
	if code != http.StatusOK {
		t.Fatalf("reading what the agent keeps about Survival: %d %v", code, state)
	}

	to := newAgentEnv(t)
	to.addIdleServer()
	code, out := to.call("PUT", to.sp("/move-state"), map[string]any{"state": state, "actor": "playkeeper"})
	if code != http.StatusOK || fmt.Sprint(out["left"]) != "[ownAddress]" {
		t.Fatalf("giving the server moved in what it had: %d %v", code, out)
	}
	var sleep, about, own, packs, share, secret, keys string
	var page, public int
	if err := to.a.db.QueryRow(`SELECT s.sleep, s.public_page, s.public_about, s.own_address, s.packs_token, m.share_token, m.public, o.secret, o.keys
		FROM servers s JOIN maps m ON m.server_id = s.id JOIN offsite o ON o.server_id = s.id WHERE s.id = ?`, to.sid).
		Scan(&sleep, &page, &about, &own, &packs, &share, &public, &secret, &keys); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sleep, `"idleMinutes":15`) || page != 0 || about != "our place" || own != "" || packs != "packtoken" || share != "maptoken" || public != 1 || secret != "the-secret" || keys != "the-keys" {
		t.Errorf("the server moved in has sleep %s, public page %d %q, own address %q, pack link %q, map link %q %d, off-site %q %q", sleep, page, about, own, packs, share, public, secret, keys)
	}
	if code, out := to.call("GET", to.sp("/schedules"), nil); code != http.StatusOK || len(out["schedules"].([]any)) != 1 {
		t.Errorf("the server moved in's schedules: %d %v", code, out)
	}
	var copies int
	if err := to.a.db.QueryRow(`SELECT COUNT(*) FROM offsite_copies WHERE server_id = ? AND backup_id = 'b1' AND copy = 'copy-1'`, to.sid).Scan(&copies); err != nil || copies != 1 {
		t.Errorf("the server moved in's copies somewhere else: %d, %v", copies, err)
	}
	if has, err := to.srv().hasAIBuildBattle(); err != nil || !has {
		t.Errorf("the server moved in lost Playkeeper's record of the add-ons it installed: %v %v", has, err)
	}
}

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
