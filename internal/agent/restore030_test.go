package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// crash030 is true at the step of a restore by Playkeeper 0.3.0's code where
// the test makes the agent process die.
var crash030 = func(step string) bool { return false }

// apply030 applies a staged restore with Playkeeper 0.3.0's code and returns
// its operation's id without waiting for it.
func (e *agentEnv) apply030(id string) string {
	e.t.Helper()
	st, err := e.a.loadStage(id)
	if err != nil {
		e.t.Fatal(err)
	}
	s := e.srv()
	op, err := s.beginOp("restore", "admin", func(ctx context.Context, h *opHandle) error {
		return s.restoreOp030(ctx, h, st, api.RestoreApplyRequest{}, "admin")
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return op.ID
}

// A restore Playkeeper 0.3.0 was in the middle of when its agent process
// died is recovered after the upgrade. One whose restored settings were
// saved is finished: its world is checked again and kept if it starts. One
// that got less far is undone, as the memory chosen for it is not known, and
// one that was being undone is undone. The operation, and the audit log, say
// what happened.
func TestRestoreInterruptedUnder030IsRecovered(t *testing.T) {
	const runningAgain = " Your previous world was put back and is running."
	for _, tc := range []struct {
		name, step string
		// failsUnder030 makes the restored world fail to start under 0.3.0,
		// and failsAfter once after the upgrade.
		failsUnder030, failsAfter bool
		// kept is true if the restore is finished. Otherwise err is how its
		// operation's error starts, and failedCopy is true if the restored
		// world is kept as a failed-restore copy.
		kept       bool
		err        string
		failedCopy bool
	}{
		{name: "dies while saving the rollback archive", step: "saving_rollback",
			err: "The Playkeeper agent stopped before this restore replaced the world, so nothing was replaced."},
		{name: "dies with the live world moved aside", step: "moved_aside",
			err: "The Playkeeper agent stopped before the restored world was in place." + runningAgain},
		{name: "dies with the restored world moved in", step: "moved_in", failedCopy: true,
			err: "The Playkeeper agent stopped before the restored world's settings were saved." + runningAgain},
		{name: "dies with the restored settings saved", step: "settings_saved", kept: true},
		{name: "dies with the restored settings saved, and the restored world does not start", step: "settings_saved", failsAfter: true, failedCopy: true,
			err: "The restored world did not start (The server stopped while starting (exit code 1)."},
		{name: "dies while deleting the previous world's copy", step: "removing_aside", kept: true},
		{name: "dies while putting the previous world back", step: "reverting_moved", failsUnder030: true, failedCopy: true,
			err: "The restored world did not start, and the Playkeeper agent stopped while the previous world was being put back." + runningAgain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			id, _, restored, previous := e.restoreScenario()
			live := e.dataDir()
			backups, _ := e.srv().listBackups(`kind = 'manual'`)
			row, err := e.srv().row()
			if err != nil || len(backups) != 1 {
				t.Fatalf("want one manual backup: %v %v", backups, err)
			}
			partial := ""
			died := make(chan struct{})
			crash030 = func(step string) bool {
				if step != tc.step {
					return false
				}
				switch step {
				case "saving_rollback":
					// The rollback archive is written to a hidden file until it
					// is complete; the process dies halfway through.
					var buf bytes.Buffer
					if _, err := backup.Create(&buf, live, backup.Manifest{CreatedAt: time.Now().UTC()}, archiveLimits()); err != nil {
						t.Error(err)
					}
					partial = e.a.backupPath(fmt.Sprintf(".playkeeper-%s-%s-5e1f0a.tar.gz.partial", sanitizeName(row.Slug), time.Now().UTC().Format("20060102-150405")))
					if err := os.WriteFile(partial, buf.Bytes()[:buf.Len()/2], 0o600); err != nil {
						t.Error(err)
					}
				case "removing_aside":
					// The process dies partway through deleting the previous
					// world's copy.
					if asides, _ := restoreCopies(live); len(asides) != 1 {
						t.Errorf("want the previous world's copy, got %v", asides)
					} else if err := os.Remove(filepath.Join(asides[0], "world", "later.dat")); err != nil {
						t.Error(err)
					}
				}
				close(died)
				return true
			}
			t.Cleanup(func() { crash030 = func(string) bool { return false } })
			if tc.failsUnder030 {
				e.fd.mu.Lock()
				e.fd.bootExit = 1
				e.fd.mu.Unlock()
			}
			opID := e.apply030(id)
			waitClosed(t, died, "the 0.3.0 restore to reach "+tc.step)
			e.stop()
			crash030 = func(string) bool { return false }
			if op := e.opAtRest(opID); op.Status != api.OpRunning {
				t.Fatalf("0.3.0 leaves the restore running: %+v", op)
			}
			if tc.step == "saving_rollback" {
				if _, err := os.Stat(partial); err != nil {
					t.Fatal(err)
				}
			}
			e.fd.mu.Lock()
			e.fd.bootExit = 0
			if tc.failsAfter {
				e.fd.failBoots = 1
			}
			e.fd.mu.Unlock()
			if tc.step == "removing_aside" {
				// The previous world's copy is partly deleted, so the restore
				// is kept even if the restored world now crashes and fails to
				// start once.
				e.fd.crash(137)
				e.fd.mu.Lock()
				e.fd.failBoots = 1
				e.fd.mu.Unlock()
			}
			e.start()
			op := e.waitOp(opID)
			if op.Detail["resumedAfterRestart"] != true {
				t.Fatalf("the restore must say it was finished after the restart: %+v", op)
			}
			asides, failed := restoreCopies(live)
			if len(asides) != 0 {
				t.Fatalf("the previous world's copy is left: %v", asides)
			}
			if tc.kept {
				if op.Status != api.OpSucceeded || op.Phase != string(api.PhaseOnline) {
					t.Fatalf("the restore must be finished: %+v", op)
				}
				if got := worldHash(t, live); got != restored || len(failed) != 0 {
					t.Fatalf("the restored world was not kept (failed-restore copies %v)", failed)
				}
				if sc, _ := e.srv().serverConfig(); sc == nil || sc.MOTD == "Before the restore" {
					t.Fatalf("the restored settings are not in place: %+v", sc)
				}
				if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.applied' AND actor = 'admin' AND detail LIKE '%; rollback archive %; finished after the Playkeeper agent restarted'`); n != 1 {
					t.Fatalf("want the restore audited once as finished after the restart, got %d", n)
				}
			} else {
				end := runningAgain
				if tc.step == "saving_rollback" {
					end = "nothing was replaced."
				}
				if op.Status != api.OpFailed || !strings.HasPrefix(op.Error, tc.err) || !strings.HasSuffix(op.Error, end) {
					t.Fatalf("the restore must say it was undone, and why: %+v", op)
				}
				if got := worldHash(t, live); got != previous {
					t.Fatal("the previous world is not back in place")
				}
				if sc, _ := e.srv().serverConfig(); sc == nil || sc.MOTD != "Before the restore" {
					t.Fatalf("the previous settings are not back: %+v", sc)
				}
				if len(failed) > 1 || tc.failedCopy != (len(failed) == 1) || tc.failedCopy && worldHash(t, failed[0]) != restored {
					t.Fatalf("failed-restore copies: %v", failed)
				}
				if tc.failedCopy != strings.Contains(op.Hint, ".failed-restore-") {
					t.Fatalf("the hint must name the failed restore's copy if there is one: %q", op.Hint)
				}
				if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.applied'`); n != 0 {
					t.Fatalf("an undone restore was audited as applied %d times", n)
				}
			}
			result := map[bool]string{true: "succeeded", false: "failed"}[tc.kept]
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore' AND result = ?`, result); n != 1 {
				t.Fatalf("want the restore audited once as %s, got %d", result, n)
			}
			if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
				t.Fatalf("the recovered restore left its stage: %v", left)
			}
			if left, _ := filepath.Glob(filepath.Join(e.cfg.BackupsDir(), ".*.partial")); len(left) != 0 {
				t.Fatalf("the unfinished rollback archive is left: %v", left)
			}
			if _, err := os.Stat(filepath.Join(e.cfg.BackupsDir(), backups[0].FileName)); err != nil {
				t.Fatalf("the backup that was restored is gone: %v", err)
			}
			e.waitFor("the server online", e.onlineIdle)
		})
	}
}

// An agent that stops while it works out how far a 0.3.0 restore got leaves
// the restore for the next start instead of giving up on it or undoing it.
// It stops while looking up the backup's Paper build to check the restored
// settings. If the server was updated after the backup, the next start also
// looks up the newer build for the settings from before the restore.
func TestStoppingWhileTakingOverA030RestoreLeavesItForTheNextStart(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updated bool
	}{
		{name: "the server was updated after the backup", updated: true},
		{name: "the server has the backup's settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			id, _, restored, _ := e.worldRestoreScenario()
			if tc.updated {
				sc, err := e.srv().serverConfig()
				if err != nil {
					t.Fatal(err)
				}
				sc.VersionID, sc.MinecraftVersion, sc.PaperBuild = "paper-26.2", "26.2", 129
				if err := e.srv().saveServerConfig(*sc); err != nil {
					t.Fatal(err)
				}
			}
			died := make(chan struct{})
			crash030 = func(step string) bool {
				if step != "settings_saved" {
					return false
				}
				close(died)
				return true
			}
			t.Cleanup(func() { crash030 = func(string) bool { return false } })
			opID := e.apply030(id)
			waitClosed(t, died, "the 0.3.0 restore to save the restored settings")
			e.stop()
			crash030 = func(string) bool { return false }
			lookups := func() int {
				e.fill.mu.Lock()
				defer e.fill.mu.Unlock()
				return e.fill.requests
			}
			before := lookups()
			e.fill.mu.Lock()
			e.fill.hold = true
			e.fill.mu.Unlock()
			e.start()
			e.waitFor("the Paper build lookup to hang", func() bool { return lookups() > before })
			e.stop()
			e.fill.mu.Lock()
			e.fill.hold = false
			e.fill.mu.Unlock()
			if op := e.opAtRest(opID); op.Status != api.OpRunning || op.Error != "" {
				t.Fatalf("the restore must be left for the next start: %+v", op)
			}
			if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 1 {
				t.Fatalf("the restore's stage must be kept: %v", left)
			}
			e.start()
			op := e.waitOp(opID)
			if op.Status != api.OpSucceeded || worldHash(t, e.dataDir()) != restored {
				t.Fatalf("the next start must finish the restore: %+v", op)
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MinecraftVersion != "26.1.2" || sc.PaperBuild != 74 {
				t.Fatalf("the restored world must run on the backup's Paper build: %+v", sc)
			}
			if asides, failed := restoreCopies(e.dataDir()); len(asides)+len(failed) != 0 {
				t.Fatalf("the finished restore left world copies: %v %v", asides, failed)
			}
			e.waitFor("the restored world online", e.onlineIdle)
		})
	}
}

// A 0.3.0 restore that had not saved its settings is undone even when the
// backup's level, version, MOTD and max players are the server's, as they are
// for a backup of the same server. Only settings that are the backup's in
// every field 0.3.0 saved, memory and build included, keep the restore, and
// the restored world runs with them.
func TestRestoreInterruptedUnder030IsKeptOnlyWithTheBackupsSettings(t *testing.T) {
	for _, tc := range []struct {
		name, step string
		// memory gives the server another memory budget after the backup,
		// as well as another Paper build.
		memory, kept bool
	}{
		{name: "dies before the save with memory and build changed", step: "moved_in", memory: true},
		{name: "dies before the save with only the build changed", step: "moved_in"},
		{name: "dies after the save with memory and build changed", step: "settings_saved", memory: true, kept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.fill.set("", []fillVersionSpec{
				{"26.3", "SUPPORTED", []fillBuildSpec{{41, "ALPHA"}}},
				{"26.2", "SUPPORTED", []fillBuildSpec{{129, "STABLE"}}},
				{"26.1.2", "UNSUPPORTED", []fillBuildSpec{{73, "STABLE"}, {74, "STABLE"}}},
			})
			id, _, restored, previous := e.worldRestoreScenario()
			live := e.dataDir()
			backupSettings, err := e.srv().serverConfig()
			if err != nil || backupSettings == nil {
				t.Fatal(err)
			}
			prev := *backupSettings
			prev.PaperBuild = 73
			if tc.memory {
				opts, _, _ := e.srv().memoryFor(e.srv().id)
				for _, mb := range opts {
					if mb != prev.MemoryMB {
						prev.MemoryMB, prev.HeapMB = mb, minecraft.HeapMB(mb)
						break
					}
				}
				if prev.MemoryMB == backupSettings.MemoryMB {
					t.Fatalf("no other memory budget fits here: %v", opts)
				}
			}
			if err := e.srv().saveServerConfig(prev); err != nil {
				t.Fatal(err)
			}
			died := make(chan struct{})
			crash030 = func(step string) bool {
				if step != tc.step {
					return false
				}
				close(died)
				return true
			}
			t.Cleanup(func() { crash030 = func(string) bool { return false } })
			opID := e.apply030(id)
			waitClosed(t, died, "the 0.3.0 restore to reach "+tc.step)
			e.stop()
			crash030 = func(string) bool { return false }
			e.start()
			op := e.waitOp(opID)
			sc, err := e.srv().serverConfig()
			if err != nil || sc == nil {
				t.Fatal(err)
			}
			asides, failed := restoreCopies(live)
			if tc.kept {
				if op.Status != api.OpSucceeded || worldHash(t, live) != restored || len(asides)+len(failed) != 0 {
					t.Fatalf("the restore must be kept: %+v (world copies %v %v)", op, asides, failed)
				}
				if sc.MemoryMB != backupSettings.MemoryMB || sc.PaperBuild != 74 {
					t.Fatalf("the restored world must run with the backup's memory and build: %+v", sc)
				}
			} else {
				if op.Status != api.OpFailed || !strings.HasPrefix(op.Error, "The Playkeeper agent stopped before the restored world's settings were saved.") {
					t.Fatalf("the restore must be undone: %+v", op)
				}
				if worldHash(t, live) != previous || len(asides) != 0 || len(failed) != 1 || worldHash(t, failed[0]) != restored {
					t.Fatalf("the previous world must be back, and the restored one kept as a copy: %v %v", asides, failed)
				}
				sc.JarVerifiedAt, prev.JarVerifiedAt = nil, nil
				if !reflect.DeepEqual(*sc, prev) {
					t.Fatalf("the previous settings must be back:\n got %+v\nwant %+v", *sc, prev)
				}
			}
			e.waitFor("the server online", e.onlineIdle)
		})
	}
}

// Playkeeper 0.3.0 undid a restore when its agent stopped while the restored
// world was starting, and could not stop the restored world, which ran on
// from the failed restore's copy. After the upgrade the restore's record says
// why it was undone, and a restored world still running from then is stopped
// and the previous world started. A server stopped or restarted since is left
// to start or run as usual. It is all done once.
func TestRestoreUndoneBy030StoppingIsTidiedUp(t *testing.T) {
	const why = "The Playkeeper agent stopped while the restored world was starting, so the restore was undone and your previous world put back."
	for _, since := range []string{"", "stopped since", "restarted since"} {
		name := "the restored world still runs"
		if since != "" {
			name = "the server was " + since
		}
		t.Run(name, func(t *testing.T) {
			e := newAgentEnv(t)
			id, _, restored, previous := e.restoreScenario()
			live := e.dataDir()
			e.fd.mu.Lock()
			e.fd.bootDelay = 2 * time.Second
			e.fd.mu.Unlock()
			applied := time.Now()
			opID := e.apply030(id)
			e.waitFor("the restored world to boot", func() bool {
				e.fd.mu.Lock()
				defer e.fd.mu.Unlock()
				c := e.fd.byName[e.cname()]
				return c != nil && c.running && c.started.After(applied)
			})
			e.stop()
			e.fd.mu.Lock()
			e.fd.bootDelay = 30 * time.Millisecond
			e.fd.mu.Unlock()
			op := e.opAtRest(opID)
			if op.Status != api.OpFailed || op.Phase != "reverting" || !undoneByStop(op) || !strings.HasPrefix(op.Hint, "Press Start on the Overview. ") {
				t.Fatalf("want the record 0.3.0 leaves: %+v", op)
			}
			_, failed := restoreCopies(live)
			if worldHash(t, live) != previous || len(failed) != 1 || worldHash(t, failed[0]) != restored {
				t.Fatalf("want the previous world put back and the restored one moved out, got %v", failed)
			}
			switch since {
			case "stopped since":
				e.fd.crash(137)
			case "restarted since":
				e.fd.mu.Lock()
				e.fd.byName[e.cname()].started = time.Now().UTC()
				e.fd.mu.Unlock()
			}
			e.fd.mu.Lock()
			strayID, strayStarted := e.fd.byName[e.cname()].id, e.fd.byName[e.cname()].started
			e.fd.mu.Unlock()
			restarted := time.Now()
			e.start()
			detail := "corrected the record of a restore undone because the agent stopped"
			if since == "" {
				detail = "stopped the restored world, which was still running after the restore was undone"
			}
			e.waitFor("the restore's record to be corrected", func() bool {
				return e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.recovered' AND actor = 'playkeeper' AND target = ? AND result = 'succeeded' AND detail = ?`, opID, detail) == 1
			})
			e.waitFor("the server online", e.onlineIdle)
			fixed, err := e.a.loadOperation(opID)
			if err != nil || fixed.Status != api.OpFailed || fixed.Error != why || fixed.Hint != "The failed restore was kept at "+failed[0]+" for inspection." || fixed.Detail["recoveredAfterRestart"] != true {
				t.Fatalf("the restore's record must say why it was undone: %+v", fixed)
			}
			e.fd.mu.Lock()
			nowID, nowStarted := e.fd.byName[e.cname()].id, e.fd.byName[e.cname()].started
			e.fd.mu.Unlock()
			if since == "restarted since" {
				if nowID != strayID || !nowStarted.Equal(strayStarted) {
					t.Fatal("a server started after the restore was stopped")
				}
			} else if !nowStarted.After(restarted) {
				t.Fatal("the previous world was not started")
			}
			if got := worldHash(t, live); got != previous {
				t.Fatal("the previous world is not in place")
			}
			if got := worldHash(t, failed[0]); got != restored {
				t.Fatal("the failed restore's copy changed")
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MOTD != "Before the restore" {
				t.Fatalf("the previous settings are not in place: %+v", sc)
			}
			e.stop()
			e.start()
			e.waitFor("the server online", e.onlineIdle)
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.recovered'`); n != 1 {
				t.Fatalf("the undone restore was dealt with %d times", n)
			}
		})
	}
}

// Only a restore 0.3.0 undid because its agent was stopping while the
// restored world started, and that was not dealt with yet, counts.
func TestUndoneByStop(t *testing.T) {
	const recorded = `The restored world did not start (context canceled). Your previous world was put back but did not start either: Docker is not responding: Get "http://docker/v1.52/images/docker.io/itzg/minecraft-server@sha256:e8640538dac5d54c2838d57fa9641e735ad0cf2b71fb0e8a68da3b542a315749/json": context canceled`
	finished := time.Now().UTC()
	for _, tc := range []struct {
		name string
		edit func(op *api.Operation)
		want bool
	}{
		{"undone as the agent stopped", func(*api.Operation) {}, true},
		{"the restored world did not start", func(op *api.Operation) {
			op.Error = "The restored world did not start (The server stopped while starting (exit code 1).). Your previous world was put back but did not start either: context canceled"
		}, false},
		{"the previous world started", func(op *api.Operation) {
			op.Error = "The restored world did not start (context canceled). Your previous world was put back and is running."
		}, false},
		{"already dealt with", func(op *api.Operation) { op.Detail["recoveredAfterRestart"] = true }, false},
		{"not undone", func(op *api.Operation) { op.Phase = string(api.PhaseOnline) }, false},
		{"still running", func(op *api.Operation) { op.Status, op.FinishedAt = api.OpRunning, nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := &api.Operation{Kind: "restore", Status: api.OpFailed, Phase: "reverting", FinishedAt: &finished, Detail: map[string]any{}, Error: recorded}
			tc.edit(op)
			if got := undoneByStop(op); got != tc.want {
				t.Fatalf("undoneByStop = %v, want %v", got, tc.want)
			}
		})
	}
}

// restoreOp030 is Playkeeper 0.3.0's restoreOp word for word, except for the
// crashAt calls, where a test makes the agent process die, and the stage's
// cleanup, which a process that died does not run.
func (s *server) restoreOp030(ctx context.Context, h *opHandle, st *stage, req api.RestoreApplyRequest, actor string) error {
	dead := false
	crashAt := func(step string) {
		if crash030(step) {
			dead = true
			runtime.Goexit()
		}
	}
	worldSafe := true
	defer func() {
		if worldSafe && !dead {
			os.RemoveAll(st.dir)
		}
	}()
	m := st.manifest
	h.set("stage", st.preview.ID)
	entry, err := s.restoreBuild(ctx, m.MinecraftVersion, m.PaperBuild)
	if err != nil {
		return errInvalid("This backup cannot be restored: %v.", err)
	}
	prev, err := s.serverConfig()
	if err != nil {
		return err
	}
	start := s.now()
	var rollback *api.Backup
	wasRunning := false
	if prev != nil {
		_, wasRunning, _ = s.containerRunning(ctx)
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		if st.preview.CurrentWorld.Exists {
			h.phase("saving_rollback")
			crashAt("saving_rollback")
			rb, err := s.saveVerifiedRollback(*prev, actor, "Automatic rollback archive before restoring backup "+st.preview.SHA256[:12])
			if err != nil {
				s.startPrevious(ctx, h, prev, wasRunning)
				return s.withRefusalHint(fmt.Errorf("could not save a verified rollback archive of the current world, so nothing was replaced: %w", err))
			}
			rollback = rb
			h.set("rollbackBackupId", rb.ID)
		}
	}
	h.phase("replacing_world")
	live := s.dataDir()
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		return err
	}
	aside := live + ".replaced-" + s.now().UTC().Format("20060102-150405")
	hadLive := false
	if _, err := os.Stat(live); err == nil {
		if err := renameDir(live, aside); err != nil {
			return err
		}
		hadLive = true
	}
	crashAt("moved_aside")
	worldSafe = false
	// A restored world that has to make way goes to failedAt, next to the live
	// directory and outside the staging folder the agent clears at start.
	failedAt := live + ".failed-restore-" + s.now().UTC().Format("20060102-150405")
	// putBack moves the previous world back into place once the restored one
	// is out of the way, at restoredAt. If that fails the live directory is
	// missing: nothing is deleted, the restored copy leaves the stage, and the
	// error names where both copies are.
	putBack := func(restoredAt string, cause error) error {
		if !hadLive {
			worldSafe = true
			return nil
		}
		err := renameDir(aside, live)
		if err == nil {
			worldSafe = true
			return nil
		}
		if restoredAt == st.data && renameDir(st.data, failedAt) == nil {
			restoredAt = failedAt
		}
		return fmt.Errorf("%v; putting the previous world back also failed (%v), so nothing was deleted: the previous world is at %s and the restored world at %s", cause, err, aside, restoredAt)
	}
	if err := renameDir(st.data, live); err != nil {
		if perr := putBack(st.data, fmt.Errorf("could not move the restored world into place: %w", err)); perr != nil {
			return perr
		}
		return err
	}
	crashAt("moved_in")
	if err := chownTree(live, s.cfg.GameUID, s.cfg.GameGID); err != nil {
		s.log.Warn("chown restored world", "err", err)
	}
	mem := st.preview.MemoryMB
	if req.MemoryMB != 0 {
		mem = req.MemoryMB
	}
	maxPlayers, _ := strconv.Atoi(m.Settings["maxPlayers"])
	if maxPlayers < 1 || maxPlayers > 100 {
		maxPlayers = 10
	}
	// The restored server.properties carries the backup's game settings, so
	// none chosen since override them.
	sc := withBuild(api.ServerConfig{
		Type: api.TypePaper, MemoryMB: mem, HeapMB: minecraft.HeapMB(mem),
		LevelName: m.LevelName, MOTD: validMOTDOr(m.Settings["motd"]), MaxPlayers: maxPlayers, Whitelist: true, CreatedAt: s.now().UTC(),
	}, entry)
	if prev != nil {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy, sc.CreatedAt, sc.PlayStyle = prev.EULAAcceptedAt, prev.EULAAcceptedBy, prev.CreatedAt, prev.PlayStyle
	} else {
		sc.EULAAcceptedAt, sc.EULAAcceptedBy = s.now().UTC(), actor
	}
	if err := s.saveServerConfig(sc); err != nil {
		cause := fmt.Errorf("could not record the restored server's settings: %w", err)
		if rerr := renameDir(live, failedAt); rerr != nil {
			where := "there was no previous world"
			if hadLive {
				where = "the previous world is at " + aside
			}
			return fmt.Errorf("%v; moving the restored world out of the way also failed (%v), so nothing was deleted: the restored world is at %s and %s", cause, rerr, live, where)
		}
		if perr := putBack(failedAt, cause); perr != nil {
			return perr
		}
		os.RemoveAll(failedAt)
		s.startPrevious(ctx, h, prev, wasRunning)
		if !hadLive {
			return fmt.Errorf("%v, so the restore was undone", cause)
		}
		return fmt.Errorf("could not record the restored server's settings, so the previous world was put back: %w", err)
	}
	crashAt("settings_saved")
	_ = s.setDesired(api.DesiredRunning)
	startErr := s.startServer(ctx, h, sc)
	if startErr != nil && hadLive && prev != nil {
		h.phase("reverting")
		_ = s.stopServer(ctx, h)
		if err := renameDir(live, failedAt); err != nil {
			return fmt.Errorf("the restored world did not start (%v), and moving it aside failed (%v), so nothing was deleted: the restored world is at %s and the previous world at %s", startErr, err, live, aside)
		}
		crashAt("reverting_moved")
		if err := putBack(failedAt, fmt.Errorf("the restored world did not start: %w", startErr)); err != nil {
			return err
		}
		_ = s.saveServerConfig(*prev)
		if err := s.startServer(ctx, h, *prev); err != nil {
			return &apiError{Msg: "The restored world did not start (" + startErr.Error() + "). Your previous world was put back but did not start either: " + err.Error(), Hint: "Press Start on the Overview. The failed restore was kept at " + failedAt + " for inspection."}
		}
		return &apiError{Msg: "The restored world did not start (" + startErr.Error() + "). Your previous world was put back and is running.", Hint: "The failed restore was kept at " + failedAt + " for inspection."}
	}
	worldSafe = true
	if startErr != nil {
		return startErr
	}
	if hadLive {
		crashAt("removing_aside")
		os.RemoveAll(aside)
	}
	detail := fmt.Sprintf("restored %s (sha256 %s)", m.LevelName, st.preview.SHA256)
	if rollback != nil {
		detail += "; rollback archive " + rollback.ID
	}
	h.set("downtimeMs", s.now().Sub(start).Milliseconds())
	s.recordEvent(s.now(), "world_restored", "", "playkeeper", detail)
	s.audit(actor, "restore.applied", st.preview.SHA256[:12], "succeeded", detail)
	return nil
}
