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
// world folder is still missing or was moved back by hand: the agent start
// that settles the kept one puts its previous settings back, over whatever a
// newer restore brought.
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
			if st := e.status(); (st.WorldMissing == nil) != state.movedBack || !st.RestoreUnsettled {
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

// A world moved back by hand is settled by the next agent start that finds
// the server stopped: the restore's record says so and restores can start
// again. A start that finds it running leaves the journal, as the refusal's
// hint says.
func TestAnAgentStartSettlesAWorldMovedBackByHandWithTheServerStopped(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	id, phrase := e.backupAndStage()
	failPuttingBack(t, e, id)
	op := e.applyRestore(id, phrase)
	if op.Status != api.OpFailed {
		t.Fatalf("restore: %+v", op)
	}
	renameDir = os.Rename
	asides, _ := filepath.Glob(e.dataDir() + ".replaced-*")
	if len(asides) != 1 {
		t.Fatalf("want the previous world's copy, got %v", asides)
	}
	if err := os.Rename(asides[0], e.dataDir()); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"/start", "/stop"} {
		code, out := e.call("POST", e.sp(step), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", step, code, out)
		}
		if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
			t.Fatalf("%s: %+v", step, op)
		}
		e.stop()
		e.start()
		if unsettled := e.status().RestoreUnsettled; unsettled != (step == "/start") {
			t.Fatalf("after %s and an agent start, restore unsettled = %v", step, unsettled)
		}
	}
	settled := e.opAtRest(op.ID)
	if !strings.HasSuffix(settled.Error, " Your previous world was already back in place, and Playkeeper put its settings back when it started again.") {
		t.Fatalf("the restore's record must say the start put its settings back: %+v", settled)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.settled' AND detail = 'put the previous world''s settings back after the Playkeeper agent restarted; the world was already back in place'`); n != 1 {
		t.Fatalf("want the audit log to say only the settings were put back, got %d such lines", n)
	}
	list, _ := e.srv().listBackups(`kind = 'manual'`)
	if code, out := e.call("POST", e.sp("/backups/"+list[0].ID+"/restore"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("a restore once the unfinished one is settled: %d %v", code, out)
	}
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
