package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// restoreScenario makes a server, backs its world up and stages the backup
// for a restore, then changes the world and the MOTD, so the restored world
// and settings can be told from the previous ones. It returns the restore's
// id and confirmation phrase and the hashes of both worlds.
func (e *agentEnv) restoreScenario() (id, phrase, restored, previous string) {
	e.t.Helper()
	e.create()
	id, phrase = e.backupAndStage()
	restored = worldHash(e.t, e.dataDir())
	if err := os.WriteFile(filepath.Join(e.dataDir(), "world", "later.dat"), []byte("built after the backup"), 0o640); err != nil {
		e.t.Fatal(err)
	}
	previous = worldHash(e.t, e.dataDir())
	sc, err := e.srv().serverConfig()
	if err != nil {
		e.t.Fatal(err)
	}
	sc.MOTD = "Before the restore"
	if err := e.srv().saveServerConfig(*sc); err != nil {
		e.t.Fatal(err)
	}
	return id, phrase, restored, previous
}

// startRestore applies a staged restore and returns its operation's id
// without waiting for it.
func (e *agentEnv) startRestore(id, phrase string) string {
	e.t.Helper()
	code, out := e.call("POST", "/v1/restore/"+id+"/apply", map[string]any{"confirm": phrase, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("apply: %d %v", code, out)
	}
	return out["id"].(string)
}

// opAtRest reads an operation while the agent is stopped.
func (e *agentEnv) opAtRest(id string) *api.Operation {
	e.t.Helper()
	db, err := store.Open(filepath.Join(e.cfg.AgentDir(), "agent.db"), migrations)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	op, err := (&Agent{db: db}).loadOperation(id)
	if err != nil {
		e.t.Fatal(err)
	}
	return op
}

// setRestoreStep runs fn at each step of a restore after its world swap,
// until the test ends.
func setRestoreStep(t *testing.T, fn func(ctx context.Context, step string)) {
	restoreStep = fn
	t.Cleanup(func() { restoreStep = func(context.Context, string) {} })
}

func waitClosed(t *testing.T, ch chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// restoreCopies are the world folders restores left next to live.
func restoreCopies(live string) (previous, failed []string) {
	previous, _ = filepath.Glob(live + ".replaced-*")
	failed, _ = filepath.Glob(live + ".failed-restore-*")
	return previous, failed
}

// Stopping the agent, or the agent process dying, after a restore swapped
// the worlds never undoes the restore: the next agent process finishes it,
// checks the restored world and keeps it once it is online, and records
// that it did.
func TestRestoreSurvivesTheAgentStopping(t *testing.T) {
	for _, tc := range []struct {
		name string
		// step is the restore step where the agent process dies or, with
		// stop, how the agent is stopped gracefully.
		step, stop string
	}{
		{name: "dies after the swap", step: "moved"},
		{name: "dies with the restored settings saved", step: "checking"},
		{name: "dies once the restored world is online", step: "online"},
		{name: "dies once the restore is kept", step: "kept"},
		{name: "stops after the swap", step: "moved", stop: "at the step"},
		{name: "stops while the restored world's image is checked", step: "checking", stop: "while the image is checked"},
		{name: "stops while the restored world boots", stop: "while it boots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnv(t)
			id, phrase, restored, _ := e.restoreScenario()
			live := e.dataDir()
			reached := make(chan struct{})
			images := 0
			setRestoreStep(t, func(ctx context.Context, step string) {
				if step != tc.step {
					return
				}
				if tc.stop == "while the image is checked" {
					images = e.fd.called("GET /images/")
					e.fd.mu.Lock()
					e.fd.holdImages = true
					e.fd.mu.Unlock()
				}
				close(reached)
				switch tc.stop {
				case "":
					runtime.Goexit()
				case "at the step":
					<-ctx.Done()
				}
			})
			if tc.stop == "while it boots" {
				e.fd.mu.Lock()
				e.fd.bootDelay = 2 * time.Second
				e.fd.mu.Unlock()
			}
			applied := time.Now()
			opID := e.startRestore(id, phrase)
			switch tc.stop {
			case "while it boots":
				e.waitFor("the restored world to boot", func() bool {
					e.fd.mu.Lock()
					defer e.fd.mu.Unlock()
					c := e.fd.byName[e.cname()]
					return c != nil && c.running && c.started.After(applied)
				})
			case "while the image is checked":
				waitClosed(t, reached, "the restored world's start")
				e.waitFor("the image check to hang", func() bool { return e.fd.called("GET /images/") > images })
			default:
				waitClosed(t, reached, "the restore step "+tc.step)
			}
			e.stop()
			restoreStep = func(context.Context, string) {}
			if got := worldHash(t, live); got != restored {
				t.Fatal("the agent going away undid the restore")
			}
			if _, failed := restoreCopies(live); len(failed) != 0 {
				t.Fatalf("the agent going away moved the restored world out: %v", failed)
			}
			if op := e.opAtRest(opID); op.Status != api.OpRunning || op.Error != "" {
				t.Fatalf("the restore must stay running for the next agent process to finish: %+v", op)
			}
			e.fd.mu.Lock()
			e.fd.holdImages, e.fd.bootDelay = false, 30*time.Millisecond
			e.fd.mu.Unlock()
			if tc.step == "kept" {
				// Once kept, the previous world's copy may be partly deleted, so
				// the restored world stays even if it now crashes and fails to
				// start once.
				previous, _ := restoreCopies(live)
				if len(previous) != 1 {
					t.Fatalf("want the previous world's copy, got %v", previous)
				}
				if err := os.Remove(filepath.Join(previous[0], "world", "later.dat")); err != nil {
					t.Fatal(err)
				}
				e.fd.crash(137)
				e.fd.mu.Lock()
				e.fd.failBoots = 1
				e.fd.mu.Unlock()
			}
			e.start()
			op := e.waitOp(opID)
			if op.Status != api.OpSucceeded || op.Detail["resumedAfterRestart"] != true || op.Phase != string(api.PhaseOnline) {
				t.Fatalf("the next agent process must finish the restore once its world is online: %+v", op)
			}
			if got := worldHash(t, live); got != restored {
				t.Fatal("the restored world was not kept")
			}
			if previous, failed := restoreCopies(live); len(previous)+len(failed) != 0 {
				t.Fatalf("the finished restore left world copies: %v %v", previous, failed)
			}
			if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
				t.Fatalf("the finished restore left its stage: %v", left)
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MOTD == "Before the restore" {
				t.Fatalf("the restored settings are not in place: %+v", sc)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.applied' AND actor = 'admin' AND result = 'succeeded' AND detail LIKE '%; finished after the Playkeeper agent restarted'`); n != 1 {
				t.Fatalf("want the restore audited once as finished after the restart, got %d", n)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore'`); n != 1 || e.opsOf("restore", api.OpSucceeded) != 1 {
				t.Fatalf("want the restore recorded once, as succeeded; %d audit rows", n)
			}
			e.waitFor("the restored world online", e.onlineIdle)
			if got := worldHash(t, live); got != restored {
				t.Fatal("starting the restored world again changed it")
			}
		})
	}
}

// A restored world that does not start is swapped back out: the previous
// world and its settings are put back and started, and the restored world is
// kept as a failed-restore copy. The next agent process checks a restore it
// finishes the same way, including a restored world that was still starting
// when the agent stopped.
func TestRestoredWorldThatDoesNotStartIsSwappedBackOut(t *testing.T) {
	for _, stop := range []string{"", "dies with the restored settings saved", "stops while the restored world boots"} {
		name := "during the restore"
		if stop != "" {
			name = "after the agent " + stop
		}
		resumed := stop != ""
		t.Run(name, func(t *testing.T) {
			e := newAgentEnv(t)
			id, phrase, restored, previous := e.restoreScenario()
			live := e.dataDir()
			var opID string
			switch stop {
			case "":
				e.fd.mu.Lock()
				e.fd.failBoots = 1
				e.fd.mu.Unlock()
				opID = e.startRestore(id, phrase)
			case "dies with the restored settings saved":
				reached := make(chan struct{})
				setRestoreStep(t, func(ctx context.Context, step string) {
					if step == "checking" {
						close(reached)
						runtime.Goexit()
					}
				})
				opID = e.startRestore(id, phrase)
				waitClosed(t, reached, "the restored settings to be saved")
				e.stop()
				restoreStep = func(context.Context, string) {}
				e.fd.mu.Lock()
				e.fd.failBoots = 1
				e.fd.mu.Unlock()
				e.start()
			case "stops while the restored world boots":
				e.fd.mu.Lock()
				e.fd.bootDelay = 3 * time.Second
				e.fd.mu.Unlock()
				applied := time.Now()
				opID = e.startRestore(id, phrase)
				e.waitFor("the restored world to boot", func() bool {
					e.fd.mu.Lock()
					defer e.fd.mu.Unlock()
					c := e.fd.byName[e.cname()]
					if c == nil || !c.running || !c.started.After(applied) {
						return false
					}
					// It fails when its boot ends, after the agent restarted.
					e.fd.failBoots, e.fd.bootDelay = 1, 30*time.Millisecond
					return true
				})
				e.stop()
				if op := e.opAtRest(opID); op.Status != api.OpRunning {
					t.Fatalf("the restore must stay running for the next agent process to finish: %+v", op)
				}
				e.start()
			}
			op := e.waitOp(opID)
			if op.Status != api.OpFailed || !strings.HasPrefix(op.Error, "The restored world did not start (The server stopped while starting (exit code 1).") ||
				!strings.HasSuffix(op.Error, "). Your previous world was put back and is running.") {
				t.Fatalf("the restore must say its world did not start and the previous one is back: %+v", op)
			}
			if got := op.Detail["resumedAfterRestart"] == true; got != resumed {
				t.Fatalf("resumedAfterRestart is %v: %+v", got, op)
			}
			asides, failed := restoreCopies(live)
			if len(asides) != 0 || len(failed) != 1 || worldHash(t, failed[0]) != restored {
				t.Fatalf("want the restored world kept as one failed-restore copy, got %v and %v", asides, failed)
			}
			if !strings.Contains(op.Hint, failed[0]) {
				t.Fatalf("the hint must name the failed restore's copy: %q", op.Hint)
			}
			if got := worldHash(t, live); got != previous {
				t.Fatal("the previous world is not back in place")
			}
			if sc, _ := e.srv().serverConfig(); sc == nil || sc.MOTD != "Before the restore" {
				t.Fatalf("the previous settings are not back: %+v", sc)
			}
			if left, _ := os.ReadDir(e.cfg.StagingDir()); len(left) != 0 {
				t.Fatalf("the undone restore left its stage: %v", left)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore.applied'`); n != 0 {
				t.Fatalf("an undone restore was audited as applied %d times", n)
			}
			if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'restore' AND result = 'failed'`); n != 1 {
				t.Fatalf("want the restore audited once as failed, got %d", n)
			}
			e.waitFor("the previous world running again", e.onlineIdle)
		})
	}
}
