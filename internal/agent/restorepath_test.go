package agent

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// failPuttingBack makes a restore's swap fail and its undo fail too, the way
// a disk fault does: the restored world can't move into place and the
// previous world can't move back, so the live world folder is missing.
func failPuttingBack(t *testing.T, e *agentEnv, id string) {
	t.Helper()
	live, staged := e.dataDir(), filepath.Join(e.cfg.StagingDir(), id, "data")
	renameDir = func(from, to string) error {
		if to == live && (strings.HasPrefix(from, live+".replaced-") || from == staged) {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })
}

// While a restore that didn't finish leaves the world folder missing, the
// status says where the previous world is, for as long as it takes, and a
// start or a backup is refused with the same words and marked so the
// dashboard drops the failure once the world is back.
func TestAWorldFolderARestoreLeftMissingIsShownUntilItIsBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	live := e.dataDir()
	failPuttingBack(t, e, id)
	if op := e.applyRestore(id, phrase); op.Status != api.OpFailed {
		t.Fatalf("restore: %+v", op)
	}
	asides, _ := filepath.Glob(live + ".replaced-*")
	if len(asides) != 1 {
		t.Fatalf("want the previous world's copy, got %v", asides)
	}
	m := e.status().WorldMissing
	if m == nil || m.Previous != asides[0] || m.DataDir != live || m.SetAsideAt.IsZero() {
		t.Fatalf("the status must say where the previous world is: %+v", m)
	}
	for _, path := range []string{"/start", "/backups"} {
		code, out := e.call("POST", e.sp(path), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", path, code, out)
		}
		op := e.waitOp(out["id"].(string))
		if op.Status != api.OpFailed || !strings.Contains(op.Error, "the previous world is at "+asides[0]) ||
			op.Detail["errorKind"] != codeWorldMissing || strings.Contains(op.Hint, "disk") {
			t.Fatalf("%s must be refused for the missing world folder: %+v", path, op)
		}
	}
	if _, err := os.Stat(live); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused start or backup made a world folder: %v", err)
	}
	renameDir = os.Rename
	e.stop()
	e.start()
	if m := e.status().WorldMissing; m != nil {
		t.Fatalf("the previous world is back, but the status still says it's missing: %+v", m)
	}
}

// worldLeftMissing makes a restore leave the world folder missing, the way a
// disk fault does, and returns what the status says about it.
func worldLeftMissing(t *testing.T) (*agentEnv, *api.WorldMissing) {
	t.Helper()
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	failPuttingBack(t, e, id)
	if op := e.applyRestore(id, phrase); op.Status != api.OpFailed {
		t.Fatalf("restore: %+v", op)
	}
	m := e.status().WorldMissing
	if m == nil {
		t.Fatal("the restore should have left the world folder missing")
	}
	return e, m
}

// refusedForMissingWorldFolder fails unless a request was refused with a
// start's words for the missing world folder, ending with then.
func refusedForMissingWorldFolder(t *testing.T, m *api.WorldMissing, what string, code int, out map[string]any, then string) {
	t.Helper()
	if code != http.StatusConflict || out["code"] != codeWorldMissing ||
		out["error"] != "The world folder is missing because a restore did not finish; the previous world is at "+m.Previous+"." ||
		out["hint"] != "Move that folder back to "+m.DataDir+", then "+then+"." {
		t.Fatalf("%s must be refused for the missing world folder: %d %v", what, code, out)
	}
}

// A new server icon or data pack refused for the missing world folder says
// to upload or add it again once the folder is back, not to press Start.
func TestAnIconOrPackRefusedForTheMissingWorldFolderSaysWhatToRedo(t *testing.T) {
	e, m := worldLeftMissing(t)
	code, out := e.uploadTo(e.sp("/datapacks?name=extra.zip"), dataPackZip(t, "Extra", false))
	refusedForMissingWorldFolder(t, m, "a new data pack", code, out, "add the data pack again")
	var icon bytes.Buffer
	if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	code, out = e.uploadTo(e.sp("/icon"), icon.Bytes())
	refusedForMissingWorldFolder(t, m, "a new server icon", code, out, "upload the icon again")
}

// The data pack list is refused for the missing world folder too, rather
// than failing to open it with a hint about a full disk.
func TestTheDataPackListWaitsForTheMissingWorldFolder(t *testing.T) {
	e, m := worldLeftMissing(t)
	code, out := e.call("GET", e.sp("/datapacks"), nil)
	refusedForMissingWorldFolder(t, m, "the data pack list", code, out, "try again")
}

// No restore starts while another one's swap journal is kept, whether its
// world folder is still missing or was moved back by hand: settling the kept
// one puts its previous settings back, over whatever a newer restore brought.
func TestNoRestoreStartsWhileAnotherIsUnsettled(t *testing.T) {
	for _, state := range []struct {
		name      string
		movedBack bool
		code      string
	}{
		{"with the world folder missing", false, codeWorldMissing},
		{"with the world moved back by hand", true, codeRestoreUnsettled},
	} {
		t.Run(state.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			id, phrase := e.backupAndStage()
			list, _ := e.srv().listBackups(`kind = 'manual'`)
			b := list[0]
			code, spare := e.call("POST", e.sp("/backups/"+b.ID+"/restore"), map[string]any{"actor": "admin"})
			if code != 200 {
				t.Fatalf("stage: %d %v", code, spare)
			}
			archive, err := os.ReadFile(e.srv().backupPath(b.FileName))
			if err != nil {
				t.Fatal(err)
			}
			failPuttingBack(t, e, id)
			if op := e.applyRestore(id, phrase); op.Status != api.OpFailed {
				t.Fatalf("restore: %+v", op)
			}
			renameDir = os.Rename
			// A job holding the server keeps the agent from settling the
			// restore as soon as its world folder is back.
			release, ok := e.srv().holdOpLock()
			if !ok {
				t.Fatal("the server is busy")
			}
			defer release()
			if state.movedBack {
				asides, _ := filepath.Glob(e.dataDir() + ".replaced-*")
				if len(asides) != 1 {
					t.Fatalf("want the previous world's copy, got %v", asides)
				}
				if err := os.Rename(asides[0], e.dataDir()); err != nil {
					t.Fatal(err)
				}
			}
			before := worldState(t, e)
			if st := e.status(); (st.WorldMissing == nil) != state.movedBack || st.RestoreUnsettled == nil {
				t.Fatalf("status: world missing %+v, restore unsettled %v", st.WorldMissing, st.RestoreUnsettled)
			}
			for _, entry := range []struct {
				name string
				send func() (int, map[string]any)
			}{
				{"applying a staged restore", func() (int, map[string]any) {
					return e.call("POST", "/v1/restore/"+spare["id"].(string)+"/apply", map[string]any{"confirm": spare["confirmPhrase"], "actor": "admin"})
				}},
				{"a restore from a backup", func() (int, map[string]any) {
					return e.call("POST", e.sp("/backups/"+b.ID+"/restore"), map[string]any{"actor": "admin"})
				}},
				{"a restore from an uploaded backup", func() (int, map[string]any) { return e.upload(archive) }},
				{"a restore from an off-site copy", func() (int, map[string]any) {
					return e.call("POST", e.sp("/offsite/restore"), map[string]any{"actor": "admin", "name": b.FileName + ".age"})
				}},
			} {
				if code, out := entry.send(); code != http.StatusConflict || out["code"] != state.code {
					t.Errorf("%s must be refused with %s: %d %v", entry.name, state.code, code, out)
				}
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action LIKE 'restore.%' AND result = 'refused'`); n != 4 {
				t.Errorf("want the four refused restores audited, got %d", n)
			}
			if after := worldState(t, e); after != before {
				t.Errorf("a refused restore changed the world folder: %q, was %q", after, before)
			}
		})
	}
}

// worldState is the world folder's contents' hash, or "missing".
func worldState(t *testing.T, e *agentEnv) string {
	t.Helper()
	if _, err := os.Stat(e.dataDir()); errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	return worldHash(t, e.dataDir())
}

// restoreLeftUnsettled has a restore's world fail to start and its undo fail
// to put the previous world back, as a disk fault would: the world folder is
// missing, the restored settings are in place and the journal is kept. It
// returns the restore and where the previous world was set aside.
func restoreLeftUnsettled(t *testing.T, e *agentEnv) (*api.Operation, string) {
	t.Helper()
	id, phrase, _, _ := e.restoreScenario()
	live := e.dataDir()
	renameDir = func(from, to string) error {
		if to == live && strings.HasPrefix(from, live+".replaced-") {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })
	e.fd.mu.Lock()
	e.fd.failBoots = 1
	e.fd.mu.Unlock()
	op := e.waitOp(e.startRestore(id, phrase))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "Putting the previous world back failed") {
		t.Fatalf("want the undo to fail putting the previous world back: %+v", op)
	}
	renameDir = os.Rename
	asides, _ := filepath.Glob(live + ".replaced-*")
	if len(asides) != 1 {
		t.Fatalf("want the previous world's copy, got %v", asides)
	}
	if sc, _ := e.srv().serverConfig(); sc.MOTD == "Before the restore" {
		t.Fatalf("the failed undo should leave the restored settings in place: %+v", sc)
	}
	return op, asides[0]
}

// A restore whose journal is kept is settled as soon as its world folder is
// back and the server is stopped, with no agent restart: by the agent's next
// reconcile tick, or by a start that comes first. The settings go back to
// what they were before the restore, and its record and the audit log say
// what happened.
func TestARestoreIsSettledOnceItsWorldIsBackWithoutAnAgentRestart(t *testing.T) {
	running := func(t *testing.T, e *agentEnv) func() {
		set := func(on bool) {
			e.fd.mu.Lock()
			defer e.fd.mu.Unlock()
			c := e.fd.byName[e.cname()]
			if c == nil {
				t.Fatal("no server container")
			}
			c.running = on
		}
		set(true)
		return func() { set(false) }
	}
	busy := func(t *testing.T, e *agentEnv) func() {
		release, ok := e.srv().holdOpLock()
		if !ok {
			t.Fatal("the server is busy")
		}
		return release
	}
	for _, c := range []struct {
		name string
		// hold keeps the restore unsettled until what it returns runs.
		hold func(t *testing.T, e *agentEnv) func()
		// start presses Start before any tick can settle it.
		start bool
	}{
		{name: "by the next tick, with the server stopped"},
		{name: "by the tick once a job holding the server is done", hold: busy},
		{name: "by the tick once a server started outside Playkeeper stops", hold: running},
		{name: "by a start before the next tick", start: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				if c.start {
					e.tweak = func(o *Options) { o.ReconcileInterval = time.Hour }
				}
			})
			op, aside := restoreLeftUnsettled(t, e)
			var release func()
			if c.hold != nil {
				release = c.hold(t, e)
			}
			if err := os.Rename(aside, e.dataDir()); err != nil {
				t.Fatal(err)
			}
			if c.hold != nil {
				time.Sleep(300 * time.Millisecond)
				if e.status().RestoreUnsettled == nil {
					t.Fatal("the restore was settled while it had to wait")
				}
				release()
			}
			if c.start {
				code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
				if code != 202 {
					t.Fatalf("start: %d %v", code, out)
				}
				if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
					t.Fatalf("start: %+v", op)
				}
			}
			e.waitFor("the restore to be settled", func() bool { return e.status().RestoreUnsettled == nil })
			settled := e.opAtRest(op.ID)
			if !strings.HasSuffix(settled.Error, " Your previous world was already back in place, and Playkeeper put its settings back.") {
				t.Fatalf("the restore's record must say its settings were put back: %+v", settled)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.settled' AND actor = 'playkeeper' AND detail = 'put the previous world''s settings back; the world was already back in place'`); n != 1 {
				t.Fatalf("want the settle audited once, got %d", n)
			}
			if sc, _ := e.srv().serverConfig(); sc.MOTD != "Before the restore" {
				t.Fatalf("the settings must be the ones from before the restore: %+v", sc)
			}
		})
	}
}

// A world moved back by hand while the agent is down is settled as it
// starts, and its record and the audit log say only the settings were put
// back.
func TestAnAgentStartSettlesAWorldMovedBackByHand(t *testing.T) {
	e := newAgentEnv(t)
	op, aside := restoreLeftUnsettled(t, e)
	e.stop()
	if err := os.Rename(aside, e.dataDir()); err != nil {
		t.Fatal(err)
	}
	e.start()
	if u := e.status().RestoreUnsettled; u != nil {
		t.Fatalf("the agent start must settle the restore: %+v", u)
	}
	settled := e.opAtRest(op.ID)
	if !strings.HasSuffix(settled.Error, " Your previous world was already back in place, and Playkeeper put its settings back when it started again.") {
		t.Fatalf("the restore's record must say the start put its settings back: %+v", settled)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.settled' AND detail = 'put the previous world''s settings back after the Playkeeper agent restarted; the world was already back in place'`); n != 1 {
		t.Fatalf("want the audit log to say only the settings were put back, got %d such lines", n)
	}
	if sc, _ := e.srv().serverConfig(); sc.MOTD != "Before the restore" {
		t.Fatalf("the settings must be the ones from before the restore: %+v", sc)
	}
}

// No job starts a server while a restore of it keeps its journal: with the
// world folder back, the server would run the previous world with the
// settings the restore left. Start and the reconcile tick settle it first;
// another job starting the server in the moment before a tick is refused.
func TestNoJobStartsAServerWhoseRestoreIsntSettled(t *testing.T) {
	for _, c := range []struct {
		name string
		// run has the job start the server and returns its operation.
		run func(t *testing.T, e *agentEnv) *api.Operation
	}{
		{"a restart of a server started outside Playkeeper", func(t *testing.T, e *agentEnv) *api.Operation {
			e.fd.mu.Lock()
			e.fd.byName[e.cname()].running = true
			e.fd.mu.Unlock()
			return e.runOp("POST", "/restart")
		}},
		{"an automatic start", func(t *testing.T, e *agentEnv) *api.Operation {
			e.srv().autoStart("recover")
			var id string
			e.waitFor("the automatic start", func() bool {
				return e.a.db.QueryRow(`SELECT id FROM operations WHERE kind = 'recover'`).Scan(&id) == nil
			})
			return e.waitOp(id)
		}},
		{"pre-generating the map", func(t *testing.T, e *agentEnv) *api.Operation {
			e.chunky()
			code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": "small", "pauseForPlayers": false, "actor": "admin"})
			if code != 202 {
				t.Fatalf("pre-generate: %d %v", code, out)
			}
			return e.waitOp(out["id"].(string))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				e.tweak = func(o *Options) { o.ReconcileInterval = time.Hour }
			})
			e.withSources()
			_, aside := restoreLeftUnsettled(t, e)
			if err := os.Rename(aside, e.dataDir()); err != nil {
				t.Fatal(err)
			}
			op := c.run(t, e)
			if op.Status != api.OpFailed || op.Error != "A restore isn't finished, so "+e.srv().name()+" can't start until it is." ||
				op.Hint != "Playkeeper finishes it once the server is stopped, then press Start." || op.Detail["errorKind"] != codeRestoreUnsettled {
				t.Fatalf("the start must be refused until the restore is settled: %+v", op)
			}
			e.fd.mu.Lock()
			running := e.fd.byName[e.cname()].running
			e.fd.mu.Unlock()
			if running {
				t.Fatal("the server runs the previous world with the settings the restore left")
			}
		})
	}
}

// A restore the agent can't settle says why, in the status and in a refused
// start, and the tick after the cause is gone settles it.
func TestARestoreThatCantBeSettledSaysWhy(t *testing.T) {
	e := newAgentEnv(t)
	_, aside := restoreLeftUnsettled(t, e)
	e.failConfigSaves()
	if err := os.Rename(aside, e.dataDir()); err != nil {
		t.Fatal(err)
	}
	var problem string
	e.waitFor("settling to fail", func() bool {
		if u := e.status().RestoreUnsettled; u != nil {
			problem = u.Problem
		}
		return problem != ""
	})
	if !strings.Contains(problem, "disk I/O error") || !strings.HasSuffix(problem, ".") {
		t.Fatalf("the status must say why the restore isn't settled, as a sentence: %q", problem)
	}
	op := e.runOp("POST", "/start")
	if op.Status != api.OpFailed || op.Error != "A restore isn't finished, so "+e.srv().name()+" can't start until it is." ||
		!strings.HasPrefix(op.Hint, "Playkeeper couldn't finish it: ") || !strings.Contains(op.Hint, "disk I/O error") {
		t.Fatalf("the start must be refused, saying why the restore isn't settled: %+v", op)
	}
	if _, err := e.a.db.Exec(`DROP TRIGGER fail_config`); err != nil {
		t.Fatal(err)
	}
	e.waitFor("the restore to be settled", func() bool { return e.status().RestoreUnsettled == nil })
	e.serverOp("/start")
	if sc, _ := e.srv().serverConfig(); sc.MOTD != "Before the restore" {
		t.Fatalf("the settings must be the ones from before the restore: %+v", sc)
	}
}

// A swap journal that can't be read holds no server back, as when the agent
// starts: whose restore it was is unknown. The status, and a restore refused
// meanwhile, say it can't be read.
func TestAnUnreadableSwapJournalHoldsNoServerBack(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.backupAndStage()
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	e.serverOp("/stop")
	stage := filepath.Join(e.cfg.StagingDir(), "unreadable")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, swapJournalFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	why := "the swap journal in " + stage + " can't be read (unreadable swap journal: unexpected end of JSON input)"
	if u := e.status().RestoreUnsettled; u == nil || u.Problem != sentence(why) {
		t.Fatalf("the status must say the journal can't be read: %+v", u)
	}
	code, out := e.call("POST", e.sp("/backups/"+list[0].ID+"/restore"), map[string]any{"actor": "admin"})
	if code != http.StatusConflict || out["code"] != codeRestoreUnsettled || out["hint"] != "Playkeeper couldn't finish it: "+why+"." {
		t.Fatalf("a restore must be refused, saying the journal can't be read: %d %v", code, out)
	}
	e.serverOp("/start")
}

// A restore whose previous world the next start put back says so: its record
// no longer says putting it back failed, the activity has a line for it, and
// the audit log has what the start did.
func TestARestoreSettledAtStartSaysSo(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	previous := worldHash(t, e.dataDir())
	failPuttingBack(t, e, id)
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "putting the previous world back also failed") {
		t.Fatalf("restore: %+v", op)
	}
	renameDir = os.Rename
	e.stop()
	e.start()
	if got := worldHash(t, e.dataDir()); got != previous {
		t.Fatal("the previous world is not back in place")
	}
	settled := e.opAtRest(op.ID)
	if settled.Status != api.OpFailed || strings.Contains(settled.Error, "also failed") ||
		!strings.HasSuffix(settled.Error, " Playkeeper put your previous world back when it started again.") ||
		!strings.Contains(settled.Hint, ".failed-restore-") || settled.Detail["settledAfterRestart"] != true {
		t.Fatalf("the restore's record must say the previous world is back: %+v", settled)
	}
	if settled.FinishedAt == nil || !settled.FinishedAt.After(*op.FinishedAt) {
		t.Fatalf("the restore is over only once the previous world is back: %v, failed at %v", settled.FinishedAt, op.FinishedAt)
	}
	acts, err := e.a.Activity(e.sid, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		found = found || a.Kind == "put_back"
	}
	if !found {
		t.Fatalf("the activity has no line for the previous world put back: %+v", acts)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.settled' AND result = 'succeeded'`); n != 1 {
		t.Fatalf("want what the start did audited once, got %d", n)
	}
}

// restoreLeftInTheWay has a restore's world fail to start and its undo fail
// to move the restored world out of the way, as a disk fault would: the
// restored world is still in the world folder, the previous world is set
// aside and the journal is kept. It returns the restore and where the
// previous world was set aside.
func restoreLeftInTheWay(t *testing.T, e *agentEnv) (*api.Operation, string) {
	t.Helper()
	id, phrase, _, _ := e.restoreScenario()
	live := e.dataDir()
	renameDir = func(from, to string) error {
		if from == live && strings.HasPrefix(to, live+".failed-restore-") {
			return errors.New("injected rename failure")
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { renameDir = os.Rename })
	e.fd.mu.Lock()
	e.fd.failBoots = 1
	e.fd.mu.Unlock()
	op := e.waitOp(e.startRestore(id, phrase))
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "Moving the restored world out of the way failed") {
		t.Fatalf("want the undo to fail moving the restored world out of the way: %+v", op)
	}
	renameDir = os.Rename
	asides, _ := filepath.Glob(live + ".replaced-*")
	if len(asides) != 1 {
		t.Fatalf("want the previous world's copy, got %v", asides)
	}
	return op, asides[0]
}

// A settle says Playkeeper put the previous world back whenever Playkeeper
// moved it, also when saving its settings failed after the move and a second
// try finished the job. Only a world someone moved back by hand was already
// back in place.
func TestASettleTriedAgainSaysPlaykeeperPutTheWorldBack(t *testing.T) {
	for _, c := range []struct {
		name string
		// ready runs before the first try; the func it returns, if any, runs
		// before a second.
		ready         func(t *testing.T, e *agentEnv, aside string) func()
		back, audited string
	}{
		{"at once", nil, " Playkeeper put your previous world back.", "put the previous world back"},
		{"once saving the settings works again", func(t *testing.T, e *agentEnv, aside string) func() {
			e.failConfigSaves()
			return func() {
				if _, err := e.a.db.Exec(`DROP TRIGGER fail_config`); err != nil {
					t.Fatal(err)
				}
			}
		}, " Playkeeper put your previous world back.", "put the previous world back"},
		{"with the world moved back by hand", func(t *testing.T, e *agentEnv, aside string) func() {
			if err := os.RemoveAll(e.dataDir()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(aside, e.dataDir()); err != nil {
				t.Fatal(err)
			}
			return nil
		}, " Your previous world was already back in place, and Playkeeper put its settings back.", "put the previous world's settings back; the world was already back in place"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				e.tweak = func(o *Options) { o.ReconcileInterval = time.Hour }
			})
			op, aside := restoreLeftInTheWay(t, e)
			var again func()
			if c.ready != nil {
				again = c.ready(t, e, aside)
			}
			e.srv().settleWhenBack(context.Background())
			if again != nil {
				if u := e.status().RestoreUnsettled; u == nil || !strings.Contains(u.Problem, "disk I/O error") || dirExists(aside) {
					t.Fatalf("the first try must move the world back and fail to save its settings: %+v, copy still aside %v", u, dirExists(aside))
				}
				again()
				e.srv().settleWhenBack(context.Background())
			}
			if u := e.status().RestoreUnsettled; u != nil {
				t.Fatalf("the restore must be settled: %+v", u)
			}
			settled, err := e.a.loadOperation(op.ID)
			if err != nil || !strings.HasSuffix(settled.Error, c.back) {
				t.Fatalf("the restore's record must end %q: %+v %v", c.back, settled, err)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.settled' AND detail = ?`, c.audited); n != 1 {
				t.Fatalf("want the settle audited once as %q, got %d", c.audited, n)
			}
			if sc, _ := e.srv().serverConfig(); sc.MOTD != "Before the restore" {
				t.Fatalf("the settings must be the ones from before the restore: %+v", sc)
			}
		})
	}
}

// restoreLeftInTheStage leaves the current server as a restore onto a server
// without a world yet, such as every restore as a new server, leaves it when
// the agent stops before the restored world is in place and can't move it
// there when it starts again: stopped, without a world folder, the restore's
// journal kept in "moving" and the restored world only in its stage. It
// returns where that world is.
func restoreLeftInTheStage(t *testing.T, e *agentEnv) string {
	t.Helper()
	e.serverOp("/stop")
	s := e.srv()
	sc, err := s.serverConfig()
	if err != nil {
		t.Fatal(err)
	}
	dir := e.a.stageDir("0123456789abcdef")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "data")
	if err := os.Rename(s.dataDir(), staged); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	if err := writeSwapJournal(dir, &swapJournal{ServerID: s.id, OpID: "0000000000000000", Actor: "admin", Aside: "data.replaced-" + stamp,
		Failed: "data.failed-restore-" + stamp, StartedAt: time.Now(), Previous: sc, Restored: *sc, State: swapMoving}); err != nil {
		t.Fatal(err)
	}
	return staged
}

// While a restore that didn't finish keeps the restored world only in its
// stage, nothing gives the server a new, empty world folder: a start, a new
// icon, a player added to the stopped server's allowlist, a new data pack and
// an add-on install are each refused, saying where the restored world is.
// Neither the reconcile tick nor an agent start gives up the stage.
func TestNothingMakesAWorldFolderWhileTheRestoredWorldIsInTheStage(t *testing.T) {
	refused := func(t *testing.T, code int, out map[string]any) (string, string) {
		t.Helper()
		if code != http.StatusConflict || out["code"] != codeRestoreUnsettled {
			t.Fatalf("want a refusal for the unfinished restore: %d %v", code, out)
		}
		msg, _ := out["error"].(string)
		hint, _ := out["hint"].(string)
		return msg, hint
	}
	for _, c := range []struct {
		name, then string
		// send asks for it and returns the refusal's message and hint.
		send func(t *testing.T, e *agentEnv) (string, string)
	}{
		{"a start", "press Start", func(t *testing.T, e *agentEnv) (string, string) {
			op := e.runOp("POST", "/start")
			if op.Status != api.OpFailed || op.Detail["errorKind"] != codeRestoreUnsettled {
				t.Fatalf("the start must fail, marked as waiting for the restore: %+v", op)
			}
			return op.Error, op.Hint
		}},
		{"a new server icon", "upload the icon again", func(t *testing.T, e *agentEnv) (string, string) {
			var icon bytes.Buffer
			if err := png.Encode(&icon, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
				t.Fatal(err)
			}
			code, out := e.uploadTo(e.sp("/icon"), icon.Bytes())
			return refused(t, code, out)
		}},
		{"a player added to the allowlist", "try again", func(t *testing.T, e *agentEnv) (string, string) {
			code, out := e.call("POST", e.sp("/whitelist"), map[string]any{"name": "JunoFox", "uuid": "5507140b-cf95-3383-b75a-47dd34196981", "actor": "admin"})
			return refused(t, code, out)
		}},
		{"a new data pack", "add the data pack again", func(t *testing.T, e *agentEnv) (string, string) {
			code, out := e.uploadTo(e.sp("/datapacks?name=extra.zip"), dataPackZip(t, "Extra", false))
			return refused(t, code, out)
		}},
		{"an add-on install", "try again", func(t *testing.T, e *agentEnv) (string, string) {
			op := e.addonOp("/addons/install", map[string]any{"source": "modrinth", "projectId": "mvportal", "fingerprint": otherPlan, "actor": "admin"})
			if op.Status != api.OpFailed {
				t.Fatalf("the install must fail: %+v", op)
			}
			return op.Error, op.Hint
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.withSources()
			e.create()
			staged := restoreLeftInTheStage(t, e)
			msg, hint := c.send(t, e)
			if want := "The world folder is missing because a restore did not finish; the restored world is only at " + staged + "."; msg != want {
				t.Errorf("message %q, want %q", msg, want)
			}
			if want := "Move that folder to " + e.dataDir() + ", then " + c.then + "."; hint != want {
				t.Errorf("hint %q, want %q", hint, want)
			}
			if dirExists(e.dataDir()) {
				t.Fatal("the refused request made a world folder")
			}
			time.Sleep(300 * time.Millisecond)
			if !dirExists(staged) {
				t.Fatal("a reconcile tick gave up the stage with the restored world")
			}
			e.stop()
			e.start()
			if !dirExists(staged) || dirExists(e.dataDir()) {
				t.Fatalf("after an agent start: restored world kept %v, world folder made %v", dirExists(staged), dirExists(e.dataDir()))
			}
		})
	}
}

// A restored world still only in its stage is never settled away, even once
// something other than Playkeeper made the world folder: the reconcile tick
// and an agent start keep the stage, and the status says why.
func TestARestoredWorldInTheStageIsNeverSettledAway(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	staged := restoreLeftInTheStage(t, e)
	if err := os.Mkdir(e.dataDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	var u *api.RestoreUnsettled
	e.waitFor("the tick to try settling the restore", func() bool {
		u = e.status().RestoreUnsettled
		return u == nil || u.Problem != ""
	})
	why := "the restored world is only in the stage at " + staged + ", not in the world directory " + e.dataDir()
	if u == nil || u.Problem != sentence(why) || !dirExists(staged) {
		t.Fatalf("the tick must keep the stage and say why: %+v, restored world kept %v", u, dirExists(staged))
	}
	e.stop()
	e.start()
	if !dirExists(staged) {
		t.Fatal("an agent start gave up the stage with the restored world")
	}
}

// A restore the next agent process finished has its own line in the
// activity, and a restore that finished by itself keeps the usual one.
func TestARestoreFinishedAfterARestartSaysSo(t *testing.T) {
	kinds := func(e *agentEnv) []string {
		t.Helper()
		acts, err := e.a.Activity(e.sid, 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range acts {
			if strings.HasPrefix(a.Kind, "restored") {
				out = append(out, a.Kind)
			}
		}
		return out
	}

	e := newAgentEnv(t)
	id, phrase, _, _ := e.restoreScenario()
	reached := make(chan struct{})
	setRestoreStep(t, func(ctx context.Context, step string) {
		if step == "moved" {
			close(reached)
			runtime.Goexit()
		}
	})
	opID := e.startRestore(id, phrase)
	waitClosed(t, reached, "the restored world to be moved into place")
	e.stop()
	restoreStep = func(context.Context, string) {}
	e.start()
	if op := e.waitOp(opID); op.Status != api.OpSucceeded || op.Detail["resumedAfterRestart"] != true {
		t.Fatalf("the next agent process must finish the restore: %+v", op)
	}
	if got := kinds(e); len(got) != 1 || got[0] != "restored_after_restart" {
		t.Fatalf("want one line for a restore finished after a restart, got %v", got)
	}

	e = newAgentEnv(t)
	id, phrase, _, _ = e.restoreScenario()
	if op := e.applyRestore(id, phrase); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	if got := kinds(e); len(got) != 1 || got[0] != "restored" {
		t.Fatalf("want the usual line for a restore that finished by itself, got %v", got)
	}
}
