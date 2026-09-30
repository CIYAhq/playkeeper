package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
)

// kept lists the kept backups, those kept for keptFor when it's set.
func (e *agentEnv) kept(keptFor string) []api.KeptBackup {
	e.t.Helper()
	path := "/v1/kept-backups"
	if keptFor != "" {
		path += "?keptFor=" + keptFor
	}
	code, _, body := e.getBytes(path)
	var out []api.KeptBackup
	if code != 200 || json.Unmarshal(body, &out) != nil {
		e.t.Fatalf("kept backups: %d %s", code, body)
	}
	return out
}

// deleteKeeping deletes the current server, keeping its final backup for
// days with the label keptFor.
func (e *agentEnv) deleteKeeping(days int, keptFor string) *api.Operation {
	e.t.Helper()
	code, out := e.callWhenFree("POST", e.sp("/delete"), map[string]any{"confirm": e.srv().name(), "actor": "admin", "keepFinalBackupDays": days, "keptFor": keptFor})
	if code != 202 {
		e.t.Fatalf("delete keeping a final backup: %d %v", code, out)
	}
	return e.waitOp(out["id"].(string))
}

// Deleting a server can keep a final backup of it: a new one, made once it
// stopped, kept for the days asked with its label, while its other backups
// go with it. It downloads whole, only its own label lists it, and the
// audit says it was kept. How long, and the label, are checked.
func TestDeletingAServerKeepsAFinalBackup(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if op := e.backupNow(nil); op.Status != api.OpSucceeded {
		t.Fatalf("an older backup: %+v", op)
	}
	older, err := e.srv().listBackups("")
	if err != nil || len(older) != 1 {
		t.Fatalf("the backups before: %+v, %v", older, err)
	}
	for _, bad := range []map[string]any{{"keepFinalBackupDays": 91}, {"keepFinalBackupDays": -1}, {"keepFinalBackupDays": 30, "keptFor": "Account 6"}, {"keptFor": "account-6"}, {"keepWhole": true}} {
		bad["confirm"], bad["actor"] = e.srv().name(), "admin"
		if code, out := e.call("POST", e.sp("/delete"), bad); code != 400 {
			t.Fatalf("deleting with %v: %d %v", bad, code, out)
		}
	}
	sid := e.sid
	if op := e.deleteKeeping(30, "account-6"); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if e.a.serverByID(sid) != nil {
		t.Fatal("the server is still there")
	}
	kept := e.kept("account-6")
	if len(kept) != 1 || kept[0].ID == older[0].ID || kept[0].ServerID != sid || kept[0].ServerName == "" || kept[0].KeptFor != "account-6" {
		t.Fatalf("kept: %+v", kept)
	}
	k := kept[0]
	if left := k.ExpiresAt.Sub(e.a.now()); left < 29*24*time.Hour || left > 30*24*time.Hour {
		t.Fatalf("kept until %v", k.ExpiresAt)
	}
	if _, err := os.Stat(e.a.backupPath(older[0].FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the older backup went nowhere: %v", err)
	}
	if other := e.kept("account-7"); len(other) != 0 {
		t.Fatalf("another label lists %+v", other)
	}
	code, hdr, body := e.getBytes("/v1/kept-backups/" + k.ID + "/download")
	sum := sha256.Sum256(body)
	if code != 200 || hex.EncodeToString(sum[:]) != k.SHA256 || hdr.Get("X-Playkeeper-SHA256") != k.SHA256 || int64(len(body)) != k.SizeBytes {
		t.Fatalf("download: %d, %d bytes, %s", code, len(body), hdr)
	}
	if _, err := backup.Verify(bytes.NewReader(body), backup.DefaultLimits()); err != nil {
		t.Fatalf("the kept backup doesn't read back: %v", err)
	}
	if !e.auditHas("server.deleted", "succeeded", "its final backup "+k.ID+" kept until") {
		t.Fatal("the audit doesn't say a final backup was kept")
	}
}

// A kept backup goes, archive and all, once its days are up, or when the
// dashboard deletes it first.
func TestAKeptBackupGoesWhenItsTimeIsUp(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if op := e.deleteKeeping(1, "account-6"); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	e.createWith(map[string]any{"name": "Creative"})
	if op := e.deleteKeeping(2, "account-6"); op.Status != api.OpSucceeded {
		t.Fatalf("delete the second: %+v", op)
	}
	kept := e.kept("")
	if len(kept) != 2 {
		t.Fatalf("kept: %+v", kept)
	}
	byDays := map[bool]api.KeptBackup{}
	for _, k := range kept {
		byDays[k.ExpiresAt.Sub(e.a.now()) > 36*time.Hour] = k
	}
	e.skew.Add(int64(25 * time.Hour))
	e.a.pruneKeptBackups()
	if left := e.kept(""); len(left) != 1 || left[0].ID != byDays[true].ID {
		t.Fatalf("kept after a day: %+v", left)
	}
	if _, err := os.Stat(e.a.backupPath(byDays[false].FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the archive whose time is up is still there: %v", err)
	}
	if code, out := e.call("DELETE", "/v1/kept-backups/"+byDays[true].ID+"?actor=admin", nil); code != 204 {
		t.Fatalf("deleting a kept backup: %d %v", code, out)
	}
	if left := e.kept(""); len(left) != 0 {
		t.Fatalf("kept once deleted: %+v", left)
	}
	if _, err := os.Stat(e.a.backupPath(byDays[true].FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the deleted kept backup's archive is still there: %v", err)
	}
	if !e.auditHas("kept_backup.expired", "succeeded", byDays[false].FileName) || !e.auditHas("kept_backup.deleted", "succeeded", byDays[true].FileName) {
		t.Fatal("the audit doesn't say the kept backups went")
	}
}

// A server no backup of which can be kept isn't deleted: its world stays
// and nothing is kept. Without the room for a new backup, its newest that
// reads back is kept instead, passing over a newer one that doesn't.
func TestAServerNoBackupOfWhichCanBeKeptStays(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.diskFree.Store(1 << 20)
	op := e.deleteKeeping(30, "account-6")
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "wasn't deleted") || !strings.Contains(op.Error, "room") {
		t.Fatalf("delete without room or a backup: %+v", op)
	}
	if e.a.serverByID(e.sid) == nil {
		t.Fatal("the server went without a backup kept")
	}
	if _, err := os.Stat(e.dataDir()); err != nil {
		t.Fatalf("the world went without a backup kept: %v", err)
	}
	if kept := e.kept(""); len(kept) != 0 {
		t.Fatalf("kept: %+v", kept)
	}

	e.diskFree.Store(0)
	for range 2 {
		if op := e.backupNow(nil); op.Status != api.OpSucceeded {
			t.Fatalf("a backup: %+v", op)
		}
	}
	list, err := e.srv().listBackups("")
	if err != nil || len(list) != 2 {
		t.Fatalf("the backups: %+v, %v", list, err)
	}
	if err := os.WriteFile(e.a.backupPath(list[0].FileName), []byte("not an archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.diskFree.Store(1 << 20)
	if op := e.deleteKeeping(30, "account-6"); op.Status != api.OpSucceeded {
		t.Fatalf("delete without room, with a backup: %+v", op)
	}
	if kept := e.kept(""); len(kept) != 1 || kept[0].ID != list[1].ID {
		t.Fatalf("kept: %+v, want the newest backup that reads back, %s", kept, list[1].ID)
	}
	if code, _, body := e.getBytes("/v1/kept-backups?keptFor=Account%206"); code != 400 {
		t.Fatalf("listing a label that can't be one: %d %s", code, body)
	}
}

// deleteWhole deletes the current server as the dashboard deletes the copy
// a move left: its whole folder kept for days with the label keptFor.
func (e *agentEnv) deleteWhole(days int, keptFor string) *api.Operation {
	e.t.Helper()
	code, out := e.callWhenFree("POST", e.sp("/delete"), map[string]any{"confirm": e.srv().name(), "actor": "playkeeper", "forgetKey": true,
		"keepFinalBackupDays": days, "keptFor": keptFor, "keepWhole": true})
	if code != 202 {
		e.t.Fatalf("delete keeping its whole folder: %d %v", code, out)
	}
	return e.waitOp(out["id"].(string))
}

// The copy a move left keeps its whole folder as its final backup, as the
// move carried it, so what a backup leaves out is kept too, and a copy with
// no world, as a server that never started, is deleted keeping one. A copy
// nothing can be kept of is deleted all the same, since its folder went
// where it moved, and the audit says so.
func TestACopyAMoveLeftKeepsItsWholeFolder(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"name": "Survival", "playStyle": "friends"})
	files := map[string]string{"creative/level.dat": "a plugin's world", "purpur.yml": "purpur: true"}
	for rel, body := range files {
		p := filepath.Join(e.dataDir(), rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if op := e.deleteWhole(7, "moved-account-7"); op.Status != api.OpSucceeded {
		t.Fatalf("deleting the copy a move left: %+v", op)
	}
	kept := e.kept("moved-account-7")
	if len(kept) != 1 {
		t.Fatalf("kept: %+v", kept)
	}
	code, _, body := e.getBytes("/v1/kept-backups/" + kept[0].ID + "/download")
	if code != 200 {
		t.Fatalf("download: %d", code)
	}
	if m, err := backup.Verify(bytes.NewReader(body), backup.DefaultLimits()); err != nil || !m.Whole {
		t.Fatalf("the kept copy isn't its whole folder: %v, %v", m.Whole, err)
	}
	for rel, want := range files {
		if got, err := backup.ReadFile(bytes.NewReader(body), rel, 1<<20); err != nil || string(got) != want {
			t.Errorf("%s in the kept copy: %q, %v", rel, got, err)
		}
	}

	e.createWith(map[string]any{"name": "Creative"})
	if err := os.RemoveAll(filepath.Join(e.dataDir(), "world")); err != nil {
		t.Fatal(err)
	}
	if op := e.deleteWhole(7, "moved-account-7"); op.Status != api.OpSucceeded {
		t.Fatalf("deleting the copy a move left of a server with no world: %+v", op)
	}
	if kept := e.kept("moved-account-7"); len(kept) != 2 {
		t.Fatalf("kept once the copy with no world went: %+v", kept)
	}

	e.createWith(map[string]any{"name": "Skyblock"})
	sid := e.sid
	e.diskFree.Store(1 << 20)
	if op := e.deleteWhole(7, "moved-account-7"); op.Status != api.OpSucceeded {
		t.Fatalf("deleting the copy a move left, without the room to keep it: %+v", op)
	}
	if e.a.serverByID(sid) != nil {
		t.Fatal("the copy nothing could be kept of is still there")
	}
	if kept := e.kept("moved-account-7"); len(kept) != 2 {
		t.Fatalf("kept once the copy nothing could be kept of went: %+v", kept)
	}
	if !e.auditHas("server.deleted", "succeeded", "no final backup kept") {
		t.Error("the audit doesn't say the copy went without a final backup")
	}
}

// A final backup made for a deletion that then fails goes with it: the
// server keeps only the backups it had, and nothing is kept.
func TestADeletionThatFailsLeavesNoFinalBackup(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.fd.mu.Lock()
	e.fd.down = "/containers/" + e.cname() + "-setup"
	e.fd.mu.Unlock()
	if op := e.deleteKeeping(30, "account-6"); op.Status != api.OpFailed || op.Phase != "deleting" || !strings.Contains(op.Error, "Docker") {
		t.Fatalf("a delete Docker fails once the final backup is made: %+v", op)
	}
	if list, err := e.srv().listBackups(""); err != nil || len(list) != 0 {
		t.Fatalf("the server's backups after the failed delete: %+v, %v", list, err)
	}
	if kept := e.kept(""); len(kept) != 0 {
		t.Fatalf("kept: %+v", kept)
	}
	entries, _ := os.ReadDir(e.cfg.BackupsDir())
	for _, en := range entries {
		t.Errorf("left in the backups folder: %s", en.Name())
	}
}
