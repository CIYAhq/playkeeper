package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// settled gives the reconcile loop, which looks every 50 ms here, time for
// the automatic restarts it would make with no backoff.
func settled() { time.Sleep(500 * time.Millisecond) }

// A minecart that throws each time it's ticked crashes the server at every
// start, so the first crash isn't restarted: the crash and its fix show at
// once. Starting it by hand starts the policy over, so a crash Playkeeper
// doesn't recognise is restarted again.
func TestACrashThatRepeatsIsNotRestarted(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.waitFor("online", e.onlineIdle)
	e.tickCrash(tickingReport(false, "minecraft:minecart", 6, 120, 6, "minecraft:overworld"))
	e.waitFor("the crash counted", func() bool { return e.crashEvents() == 1 })
	settled()
	st := e.status()
	if n := e.autoRestarts(); n != 0 || st.Phase != api.PhaseCrashed || st.CrashCount != 1 || st.Crash == nil || st.Crash.Kind != "ticking_entity" || !st.Crash.Repeats {
		t.Fatalf("%d automatic restarts, phase %s, %d crashes, crash %+v; want none, crashed, and the minecart shown", n, st.Phase, st.CrashCount, st.Crash)
	}
	if !strings.Contains(st.LastError, "It would crash the same way again, so Playkeeper didn't restart it.") || !strings.Contains(st.LastErrorHint, "Fix the cause, then press Start.") {
		t.Errorf("error %q, hint %q", st.LastError, st.LastErrorHint)
	}

	// The minecart's report is from the run that crashed, a minute before.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(e.dataDir(), "crash-reports", "crash-2026-09-29_17.42.52-server.txt"), old, old); err != nil {
		t.Fatal(err)
	}
	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	e.fd.addLog("[12:00:05 INFO]: Timings Reset")
	e.fd.crash(137)
	e.waitFor("the automatic restart after a crash it doesn't recognise", func() bool { return e.autoRestarts() == 1 })
}

// A server that stops at every start, over both level.dat files damaged or
// a mod made for players' games, isn't started again after the automatic
// start that found it out, and says why.
func TestAStartThatStopsTheSameWayEachTimeIsNotTriedAgain(t *testing.T) {
	for _, c := range []struct {
		name, kind string
		lines      []string
	}{
		{"both level.dat files damaged", "corrupt_world", []string{
			"[12:00:00 WARN]: Failed to load world data from ./world/level.dat",
			"[12:00:00 ERROR]: Failed to load world data. World files may be corrupted. Shutting down.",
		}},
		{"a mod made for players' games", "incompatible_addon", []string{
			"[12:00:00 INFO]: Clearing ModLoader",
			"java.lang.NoClassDefFoundError: org/lwjgl/Version",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.create()
			e.waitFor("online", e.onlineIdle)
			e.fd.mu.Lock()
			e.fd.bootExit, e.fd.bootLines = 1, c.lines
			e.fd.mu.Unlock()
			e.fd.addLog("[12:00:05 INFO]: Timings Reset")
			e.fd.crash(137)
			e.waitFor("the automatic restart that stops", func() bool { return e.opsOf("auto-restart", api.OpFailed) == 1 })
			settled()
			st := e.status()
			if n := e.autoRestarts(); n != 1 || st.Phase != api.PhaseCrashed || st.Crash == nil || st.Crash.Kind != c.kind || !st.Crash.Start || !st.Crash.Repeats {
				t.Fatalf("%d automatic starts, phase %s, crash %+v; want the one that stopped, crashed, and why", n, st.Phase, st.Crash)
			}
			if !strings.Contains(st.LastError, "Playkeeper stopped trying to start the server, which would stop the same way each time") {
				t.Errorf("error %q", st.LastError)
			}
		})
	}
}

// Someone who starts the server while its crash is still being explained
// has moved on from it: whatever became of that start, the crash neither
// holds the server off nor restarts it. With no start since, a crash that
// repeats holds it, until the next start lets go; one of a server meant to
// be off does neither; and one Playkeeper doesn't recognise restarts it.
func TestAStartLetsGoOfACrashThatRepeats(t *testing.T) {
	e := crashEnv(t)
	s := e.srv()
	after := func(run int, wanted bool) (bool, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.afterCrash(run, wanted)
	}
	s.mu.Lock()
	run := s.runs
	s.crash, s.crashes = &api.Crash{Kind: "ticking_entity", Repeats: true}, []time.Time{time.Now()}
	s.mu.Unlock()
	s.resetRun(api.PhaseStartingContainer)
	if holds, restart := after(run, true); holds || restart {
		t.Fatalf("a crash someone started the server after: holds %v, restart %v; want neither", holds, restart)
	}

	s.mu.Lock()
	run = s.runs
	s.mu.Unlock()
	if holds, restart := after(run, false); holds || restart {
		t.Fatalf("a server meant to be off: holds %v, restart %v; want neither", holds, restart)
	}
	if holds, restart := after(run, true); !holds || restart {
		t.Fatalf("a crash that repeats: holds %v, restart %v; want it held off", holds, restart)
	}
	s.resetRun(api.PhaseStartingContainer)
	s.mu.Lock()
	gaveUp := s.givenUp()
	run = s.runs
	s.crash = &api.Crash{Kind: "unknown"}
	s.mu.Unlock()
	if gaveUp {
		t.Error("a start didn't let go of the crash")
	}
	if holds, restart := after(run, true); holds || !restart {
		t.Errorf("a crash Playkeeper doesn't recognise: holds %v, restart %v; want it restarted", holds, restart)
	}
}

// Someone who stops the server while its crash is still being explained
// keeps it stopped: nothing restarts it once the explanation is done.
func TestAStopWhileTheCrashIsExplainedKeepsItOff(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.waitFor("online", e.onlineIdle)
	e.fd.mu.Lock()
	e.fd.logDelay = 2 * time.Second
	e.fd.mu.Unlock()
	e.fd.addLog("[12:00:05 INFO]: Timings Reset")
	e.fd.crash(137)
	e.waitFor("the crash counted", func() bool { return e.crashEvents() == 1 })
	if code, out := e.callWhenFree("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("stop: %d %v", code, out)
	}
	time.Sleep(3 * time.Second)
	if n := e.autoRestarts(); n != 0 {
		t.Fatalf("%d automatic restarts of a server stopped while its crash was explained", n)
	}
}

// A crash Playkeeper doesn't recognise is restarted, as before.
func TestAnUnrecognisedCrashIsStillRestarted(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.waitFor("online", e.onlineIdle)
	e.fd.addLog("[12:00:30 ERROR]: Encountered an unexpected exception")
	e.fd.addLog("java.lang.IllegalStateException: something nobody planned for")
	e.fd.crash(1)
	e.waitFor("the automatic restart", func() bool { return e.autoRestarts() == 1 })
	e.waitFor("online again", e.onlineIdle)
	if st := e.status(); st.CrashCount != 1 {
		t.Errorf("%d crashes counted, want 1", st.CrashCount)
	}
}
