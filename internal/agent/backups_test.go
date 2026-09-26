package agent

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// backupNow asks for a backup of the current server and waits for it.
func (e *agentEnv) backupNow(body map[string]any) *api.Operation {
	e.t.Helper()
	full := map[string]any{"actor": "admin"}
	for k, v := range body {
		full[k] = v
	}
	code, out := e.call("POST", e.sp("/backups"), full)
	if code != 202 {
		e.t.Fatalf("backup: %d %v", code, out)
	}
	return e.waitOp(out["id"].(string))
}

// commandsSent is every console command the fake server received, in order.
func (fr *fakeRCON) commandsSent() []string {
	fr.mu.Lock()
	defer fr.mu.Unlock()
	return append([]string(nil), fr.commands...)
}

func (fr *fakeRCON) setAnswer(answer func(cmd string) (string, bool)) {
	fr.mu.Lock()
	fr.answer = answer
	fr.mu.Unlock()
}

// A backup of an online server keeps its players: world saving is paused
// while the world is copied, nobody is warned or disconnected, and the
// backup records how it was made.
func TestOnlineBackupKeepsPlayersOnline(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.rcon.setOnline("Friend")
	e.waitFor("the player online", func() bool { p := e.status().Players; return p != nil && p.Online == 1 })
	before := e.status().StartedAt
	op := e.backupNow(nil)
	if op.Status != api.OpSucceeded || op.Detail["method"] != "online_copy" || op.Detail["downtimeMs"] != float64(0) || op.Detail["savingPaused"] != false {
		t.Fatalf("online backup: %+v", op)
	}
	if after := e.status().StartedAt; before == nil || after == nil || !after.Equal(*before) {
		t.Fatalf("the server restarted for an online backup: started %v, then %v", before, after)
	}
	var steps []string
	for _, c := range e.rcon.commandsSent() {
		switch {
		case strings.HasPrefix(c, "say "):
			t.Fatalf("players were warned about an online backup: %q", c)
		case c == "save-off", c == "save-all flush", c == "save-on":
			steps = append(steps, c)
		}
	}
	if got := strings.Join(steps, ", "); got != "save-off, save-all flush, save-on" {
		t.Fatalf("console steps: %s", got)
	}
	if e.rcon.savingIsOff() {
		t.Fatal("world saving was left off")
	}
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	if len(list) != 1 || list[0].Method != "online_copy" || list[0].DowntimeMs != 0 || list[0].Verified == nil || !*list[0].Verified {
		t.Fatalf("backup record: %+v", list)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'backup.created' AND detail LIKE '%method online_copy saving paused %'`); n != 1 {
		t.Fatalf("the audit must say how the backup was made (%d rows)", n)
	}
	if st := e.status(); st.SavingPausedSince != nil {
		t.Fatalf("a finished backup left a saving pause: %v", st.SavingPausedSince)
	}
	if entries, _ := os.ReadDir(e.cfg.StagingDir()); len(entries) != 0 {
		t.Fatalf("the staging copy was left behind: %v", entries)
	}
}

// "Back up with the server stopped" warns players in chat first, with the wait
// before the stop, then stops the server, archives it and starts it again.
func TestStoppedBackupWarnsPlayersFirst(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.rcon.setOnline("Friend")
	e.waitFor("the player online", func() bool { p := e.status().Players; return p != nil && p.Online == 1 })
	before := e.status().StartedAt
	op := e.backupNow(map[string]any{"stopped": true})
	if op.Status != api.OpSucceeded || op.Detail["method"] != "stopped" || op.Detail["stopped"] != true {
		t.Fatalf("stopped backup: %+v", op)
	}
	if d, _ := op.Detail["downtimeMs"].(float64); d <= 0 {
		t.Fatalf("a stopped backup must record its downtime: %+v", op.Detail)
	}
	warned, savedAfter := false, false
	for _, c := range e.rcon.commandsSent() {
		switch {
		case c == "save-off":
			t.Fatal("a stopped backup must not pause saving over the console")
		case c == "say "+backupWarning(e.a.opts.BackupWarnDelay):
			warned = true
		case warned && strings.HasPrefix(c, "save-all"):
			savedAfter = true
		}
	}
	if !warned || !savedAfter {
		t.Fatalf("players must be warned, with the wait before the stop, before the server saves and stops: %v", e.rcon.commandsSent())
	}
	e.waitFor("online again", e.onlineIdle)
	if after := e.status().StartedAt; after == nil || after.Equal(*before) {
		t.Fatal("the server was not stopped for a stopped backup")
	}
}

// A backup that can't fit is refused before anything reaches the server:
// saving isn't paused, and the stopped kind doesn't warn or stop anyone.
func TestBackupWithoutRoomNeverTouchesTheServer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	before := e.status().StartedAt
	e.diskFree.Store(1 << 20)
	for _, body := range []map[string]any{nil, {"stopped": true}} {
		op := e.backupNow(body)
		if op.Status != api.OpFailed || op.Detail["errorKind"] != "insufficient_space" || op.Detail["neededBytes"] == nil || op.Detail["stopped"] != nil {
			t.Fatalf("backup %v without room: %+v", body, op)
		}
	}
	for _, c := range e.rcon.commandsSent() {
		if strings.HasPrefix(c, "say ") || c == "save-off" {
			t.Fatalf("a refused backup reached the console: %q", c)
		}
	}
	if after := e.status().StartedAt; before == nil || after == nil || !after.Equal(*before) {
		t.Fatalf("the server was stopped for a backup that could not fit: started %v, then %v", before, after)
	}
}

// The chat line before a stopped backup says when the server stops: after the
// wait that follows it.
func TestBackupWarningSaysWhenTheServerStops(t *testing.T) {
	for wait, want := range map[time.Duration]string{
		3 * time.Second:       "stops in 3 seconds",
		10 * time.Millisecond: "stops in 1 second",
		time.Minute:           "stops in 1 minute",
		90 * time.Second:      "stops in 90 seconds",
		2 * time.Minute:       "stops in 2 minutes",
	} {
		if got := backupWarning(wait); !strings.Contains(got, want) {
			t.Errorf("%v: %q does not say %q", wait, got, want)
		}
	}
}

// A server that is starting can't pause saving yet: the backup is refused
// with what to do, and nothing reaches its console.
func TestBackupWaitsUntilTheServerIsOnline(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.srv().setRunPhase(api.PhaseStarting, "")
	code, out := e.call("POST", e.sp("/backups"), map[string]any{"actor": "admin"})
	if code != 409 || !strings.Contains(fmt.Sprint(out["hint"]), "Wait until the server is online") {
		t.Fatalf("a backup while starting: %d %v", code, out)
	}
	if n := e.rcon.count("save-off"); n != 0 {
		t.Fatalf("save-off reached a starting server %d times", n)
	}
}

// A backup that fails after pausing saving turns it back on, keeps nothing,
// and says why in the operation's detail.
func TestFailedOnlineBackupTurnsSavingBackOn(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.rcon.setAnswer(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "save-all") {
			return "Unable to save the game", true
		}
		return "", false
	})
	op := e.backupNow(nil)
	if op.Status != api.OpFailed || op.Detail["errorKind"] != "save_failed" || op.Detail["savingPaused"] != false {
		t.Fatalf("a backup whose save failed: %+v", op)
	}
	if e.rcon.savingIsOff() || e.rcon.count("save-on") != 1 {
		t.Fatalf("saving must be turned back on once (sent %d times)", e.rcon.count("save-on"))
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups`); n != 0 {
		t.Fatalf("%d backups recorded for a failed backup", n)
	}
	if files, _ := os.ReadDir(e.cfg.BackupsDir()); len(files) != 0 {
		t.Fatalf("a failed backup left files: %v", files)
	}
	if st := e.status(); st.SavingPausedSince != nil {
		t.Fatalf("saving is on, but a pause is shown: %v", st.SavingPausedSince)
	}
}

// A lost reply to save-on is never resent by the console connection, so the
// backup's own retry reads "already on" as its answer and keeps the backup.
// A resent save-on would answer the first attempt "already on", which reads
// as saving turned on by someone else and throws a good backup away.
func TestLostSaveOnReplyKeepsTheBackup(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	var lost atomic.Bool
	e.rcon.mu.Lock()
	e.rcon.lose = func(cmd string) bool { return cmd == "save-on" && lost.CompareAndSwap(false, true) }
	e.rcon.mu.Unlock()
	op := e.backupNow(nil)
	if op.Status != api.OpSucceeded {
		t.Fatalf("a lost reply to save-on must not throw the backup away: %+v", op)
	}
	if n := e.rcon.count("save-on"); n != 2 {
		t.Fatalf("save-on reached the server %d times; only the backup's own retry may send it again", n)
	}
	if e.rcon.savingIsOff() {
		t.Fatal("world saving was left off")
	}
}

// When saving can't be turned back on, the backup fails saying so, the pause
// is remembered and shown, and "Turn saving back on" ends it.
func TestSavingLeftPausedIsShownAndTurnedBackOn(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	var broken atomic.Bool
	broken.Store(true)
	e.rcon.setAnswer(func(cmd string) (string, bool) {
		if cmd == "save-on" && broken.Load() {
			return "An unexpected error occurred trying to execute that command", true
		}
		return "", false
	})
	op := e.backupNow(nil)
	if op.Status != api.OpFailed || op.Detail["errorKind"] != "saving_paused" || op.Detail["savingPaused"] != true || !strings.Contains(op.Hint, "save-on") {
		t.Fatalf("a backup that left saving off: %+v", op)
	}
	if st := e.status(); st.SavingPausedSince == nil || !e.rcon.savingIsOff() {
		t.Fatalf("the pause must be remembered and shown: %v", st.SavingPausedSince)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM backups`); n != 0 {
		t.Fatalf("%d backups recorded while saving is still paused", n)
	}
	if code, out := e.call("POST", e.sp("/saving/resume"), map[string]any{"actor": "admin"}); code != http.StatusBadGateway ||
		out["error"] != "The server did not turn world saving back on." || !strings.Contains(fmt.Sprint(out["hint"]), "save-on") {
		t.Fatalf("a failed save-on must say so plainly, without the console's reply: %d %v", code, out)
	}
	broken.Store(false)
	if code, out := e.call("POST", e.sp("/saving/resume"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("turn saving back on: %d %v", code, out)
	}
	e.waitFor("the pause to end", func() bool { return e.status().SavingPausedSince == nil })
	if e.rcon.savingIsOff() {
		t.Fatal("world saving is still off")
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'saving_resumed'`); n != 1 {
		t.Fatalf("%d saving_resumed events, want 1", n)
	}
}

// The reconciler turns saving back on that a backup left off, also after the
// agent restarted mid-backup, and only forgets a pause that a stop or restart
// of the server ended.
func TestReconcilerTurnsSavingBackOn(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	pass, err := e.srv().rconPassword()
	if err != nil {
		t.Fatal(err)
	}
	e.stop()
	e.rcon.save(pass, false)
	db, err := store.Open(filepath.Join(e.cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE servers SET saving_paused_since = ? WHERE id = ?`, time.Now().UnixMilli(), e.sid); err != nil {
		t.Fatal(err)
	}
	db.Close()
	e.start()
	e.waitFor("saving back on", func() bool { return !e.rcon.savingIsOff() && e.status().SavingPausedSince == nil })
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'saving_resumed'`); n != 1 {
		t.Fatalf("%d saving_resumed events, want 1", n)
	}

	sent := e.rcon.count("save-on")
	started := e.status().StartedAt
	e.a.db.Exec(`UPDATE servers SET saving_paused_since = ? WHERE id = ?`, started.Add(-time.Minute).UnixMilli(), e.sid)
	e.waitFor("a pause from before the server started to be forgotten", func() bool { return e.status().SavingPausedSince == nil })
	if code, _ := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 202 {
		t.Fatalf("stop: %d", code)
	}
	e.waitFor("stopped", func() bool { st := e.status(); return st.Phase == api.PhaseStopped && st.Operation == nil })
	e.a.db.Exec(`UPDATE servers SET saving_paused_since = ? WHERE id = ?`, time.Now().UnixMilli(), e.sid)
	e.waitFor("a stopped server's pause to be forgotten", func() bool { return e.status().SavingPausedSince == nil })
	if n := e.rcon.count("save-on"); n != sent {
		t.Fatalf("save-on sent %d more times for pauses the server already ended", n-sent)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'saving_resumed'`); n != 1 {
		t.Fatalf("%d saving_resumed events, want 1", n)
	}
}

// Archives an earlier agent left half written are removed when it starts;
// nothing else in the backups folder is touched.
func TestHalfWrittenBackupsAreRemovedAtStart(t *testing.T) {
	e := newAgentEnv(t)
	partial := filepath.Join(e.cfg.BackupsDir(), ".playkeeper-world-20260925-120000-abcdef.tar.gz.partial")
	other := filepath.Join(e.cfg.BackupsDir(), "notes.txt")
	for _, p := range []string{partial, other} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.stop()
	e.start()
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("the half-written archive is still there: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another file was removed: %v", err)
	}
}
