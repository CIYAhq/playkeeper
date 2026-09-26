package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// crashEnv is an agent whose crash policy waits an hour before restarting a
// crashed server, so the crash stays to be looked at.
func crashEnv(t *testing.T) *agentEnv {
	t.Helper()
	e := newAgentEnv(t)
	e.stop()
	e.crashBackoff = []time.Duration{time.Hour}
	e.start()
	e.create()
	return e
}

func (e *agentEnv) waitCrash() *api.Crash {
	e.t.Helper()
	var c *api.Crash
	e.waitFor("the crash explained", func() bool { c = e.status().Crash; return c != nil })
	return c
}

func crashLines(c *api.Crash) string {
	var out []string
	for _, l := range c.Lines {
		out = append(out, strings.Join(slices.DeleteFunc([]string{l.Time, l.Level, l.Text}, func(s string) bool { return s == "" }), " "))
	}
	return strings.Join(out, "\n")
}

func writeGameFile(t *testing.T, path, text string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
}

// A server that logs "Stopping server" after it crashed has still crashed:
// the helper reads the run's own log from Docker, as the server printed it,
// and shows the lines that explain it without the addresses in them.
func TestCrashIsExplainedFromTheRunsLog(t *testing.T) {
	e := crashEnv(t)
	for _, l := range []string{
		"[03:10:02 INFO]: PkBotBuilder[/203.0.113.7:51422] logged in with entity id 412 at ([world]1204.5, 71.0, -388.2)",
		"[03:10:44 WARN]: Can't keep up! Is the server overloaded? Running 5214ms or 104 ticks behind",
		"[03:11:30 ERROR]: Encountered an unexpected exception",
		"java.lang.OutOfMemoryError: Java heap space",
		"\tat java.base/java.util.Arrays.copyOf(Arrays.java:3541) ~[?:?]",
		"[03:11:31 INFO]: Stopping server",
	} {
		e.fd.addLog(l)
	}
	e.fd.crash(1)
	c := e.waitCrash()
	if c.Kind != "heap_out_of_memory" || c.Start || !c.Certain {
		t.Fatalf("got %s (start %v, certain %v): %s", c.Kind, c.Start, c.Certain, c.Explanation)
	}
	if st := e.status(); st.Phase != api.PhaseCrashed || e.crashEvents() != 1 {
		t.Fatalf("a crash followed by Stopping server was not counted: phase %s, %d crash events", st.Phase, e.crashEvents())
	}
	if got, want := crashLines(c), "03:11:30 ERROR java.lang.OutOfMemoryError: Java heap space\n03:11:31 Stopping server"; got != want {
		t.Errorf("lines:\n%s\nwant\n%s", got, want)
	}
	if c.RoomMB != 3328-1536 {
		t.Errorf("room %d MB, want what the machine could still give", c.RoomMB)
	}
	if len(c.Fixes) == 0 || c.Fixes[0].Kind != "raise_memory" || !c.Fixes[0].Recommended || c.Fixes[len(c.Fixes)-1].Kind != "restart" {
		t.Errorf("fixes: %+v", c.Fixes)
	}
	b, _ := json.Marshal(e.status())
	if strings.Contains(string(b), "203.0.113.7") {
		t.Errorf("a player's address reached the status: %s", b)
	}

	code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("online", e.onlineIdle)
	if c := e.status().Crash; c != nil {
		t.Fatalf("the crash is still shown once the server is back: %+v", c)
	}
}

// The newest crash report written during the run is read; one left from an
// earlier run explains nothing.
func TestCrashReportIsTheOneThisRunWrote(t *testing.T) {
	e := crashEnv(t)
	reports := filepath.Join(e.dataDir(), "crash-reports")
	writeGameFile(t, filepath.Join(reports, "crash-2026-09-20_10.00.00-server.txt"),
		"---- Minecraft Crash Report ----\nDescription: Exception in server tick loop\n\nnet.minecraft.ReportedException: Failed to load world data from level.dat and level.dat_old. World files may be corrupted\n",
		time.Now().Add(-48*time.Hour))
	e.fd.crash(1)
	if c := e.waitCrash(); c.Kind != "unknown" {
		t.Fatalf("an old crash report explained this crash: %s %s", c.Kind, c.Explanation)
	}

	code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("online", e.onlineIdle)
	writeGameFile(t, filepath.Join(reports, "crash-2026-09-25_21.40.12-server.txt"),
		"---- Minecraft Crash Report ----\nDescription: Watching Server\n\njava.lang.Error: ServerHangWatchdog detected that a single server tick took 60.00 seconds (should be max 0.05)\n", time.Time{})
	e.fd.crash(1)
	c := e.waitCrash()
	if c.Kind != "watchdog" {
		t.Fatalf("got %s: %s", c.Kind, c.Explanation)
	}
	found := false
	for _, ev := range c.Evidence {
		found = found || ev.Kind == "crash_report_line" && ev.Params["file"] == "crash-2026-09-25_21.40.12-server.txt"
	}
	if !found {
		t.Errorf("the crash report is not in the evidence: %+v", c.Evidence)
	}
}

// A start that fails is explained too, whether the server exits while it
// starts or Docker cannot start its container; stopping the server puts the
// explanation away.
func TestFailedStartIsExplained(t *testing.T) {
	e := crashEnv(t)
	run := func(verb string) *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	run("stop")
	e.fd.mu.Lock()
	e.fd.bootExit = 134
	e.fd.mu.Unlock()
	if op := run("start"); op.Status != api.OpFailed {
		t.Fatalf("start: %+v", op)
	}
	c := e.waitCrash()
	if !c.Start || !strings.Contains(crashLines(c), "ERROR Encountered an unexpected exception") {
		t.Fatalf("got start %v, lines:\n%s", c.Start, crashLines(c))
	}

	e.fd.mu.Lock()
	e.fd.bootExit = 0
	e.fd.startErr = "driver failed programming external connectivity on endpoint pk: Bind for 0.0.0.0:25565 failed: port is already allocated"
	e.fd.mu.Unlock()
	if op := run("start"); op.Status != api.OpFailed {
		t.Fatalf("start: %+v", op)
	}
	c = e.waitCrash()
	if c.Kind != "port_in_use" || !c.Start || c.Params["port"] != float64(25565) && c.Params["port"] != 25565 {
		t.Fatalf("got %s start %v params %v", c.Kind, c.Start, c.Params)
	}
	if got := crashLines(c); got != "ERROR driver failed programming external connectivity on endpoint pk: Bind for [ip redacted] failed: port is already allocated" {
		t.Errorf("lines: %s", got)
	}
	e.fd.mu.Lock()
	e.fd.startErr = ""
	e.fd.mu.Unlock()
	if code, _ := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"}); code != 200 {
		t.Fatalf("stop: %d", code)
	}
	if c := e.status().Crash; c != nil {
		t.Fatalf("stopping the server did not put the crash away: %+v", c)
	}
}

// Java can log "Stopping server" after running out of memory, with no crash
// line of its own. The run still crashed: it is explained and counted, the
// automatic restart waits for its backoff, and the open session ends as a
// crash, not as a stop.
func TestAnOutOfMemoryErrorThenStoppingServerIsACrash(t *testing.T) {
	e := crashEnv(t)
	e.fd.addLog("[03:10:02 INFO]: PkBotBuilder joined the game")
	e.waitFor("the session open", func() bool { return e.countRows(`SELECT COUNT(*) FROM sessions WHERE end_ts IS NULL`) == 1 })
	e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
	e.fd.addLog("[03:11:31 INFO]: Stopping server")
	e.fd.crash(1)
	c := e.waitCrash()
	if c.Kind != "heap_out_of_memory" || c.Start {
		t.Fatalf("got %s (start %v): %s", c.Kind, c.Start, c.Explanation)
	}
	if st := e.status(); st.Phase != api.PhaseCrashed || st.CrashCount != 1 || e.crashEvents() != 1 || e.autoRestarts() != 0 {
		t.Fatalf("phase %s, %d crash(es) counted, %d crash event(s), %d automatic restart(s); want crashed, 1, 1, none before the backoff",
			st.Phase, st.CrashCount, e.crashEvents(), e.autoRestarts())
	}
	var reason string
	if err := e.a.db.QueryRow(`SELECT end_reason FROM sessions WHERE player = 'PkBotBuilder'`).Scan(&reason); err != nil || reason != "server_crashed" {
		t.Fatalf("the session ended as %q (%v), want server_crashed", reason, err)
	}
}

// Forge logs that the server failed to start when a mod fails in its setup,
// then keeps running: the start stops it after a short wait and explains
// why, instead of waiting out ReadyTimeout. The next start forgets it.
func TestAStartThatGaveUpButKeptRunningIsStoppedAndExplained(t *testing.T) {
	old := hungStartWait
	hungStartWait = 500 * time.Millisecond
	t.Cleanup(func() { hungStartWait = old })
	e := crashEnv(t)
	run := func(verb string) *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	run("stop")
	e.fd.mu.Lock()
	e.fd.hangsAfterFailing = true
	e.fd.mu.Unlock()
	began := time.Now()
	op := run("start")
	if op.Status != api.OpFailed || !strings.Contains(op.Error, "The server stopped while starting") {
		t.Fatalf("start: %+v", op)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Fatalf("the start waited %s for a server that had given up", took)
	}
	if c, err := e.a.docker.ContainerInspect(context.Background(), e.srv().containerName()); err != nil || c.State.Running {
		t.Fatalf("the server that gave up is still running: %v %v", c.State.Running, err)
	}
	c := e.waitCrash()
	if !c.Start || c.Kind != "addon_failed" || c.Params["addon"] != "waila" {
		t.Fatalf("got %s start %v params %v:\n%s", c.Kind, c.Start, c.Params, crashLines(c))
	}
	e.fd.mu.Lock()
	e.fd.hangsAfterFailing = false
	e.fd.mu.Unlock()
	if op := run("start"); op.Status != api.OpSucceeded {
		t.Fatalf("the next start: %+v", op)
	}
}

// A start Docker refused because the game port is taken names the program
// holding the port, when the agent can see it; one it can't see is left out.
// A start that failed for another reason doesn't look.
func TestPortCrashNamesTheProgramHoldingThePort(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.crashBackoff = []time.Duration{time.Hour}
	var mu sync.Mutex
	var asked []int
	holder := "java"
	e.portHolder = func(port int) (string, int, bool) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, port)
		return holder, 48211, holder != ""
	}
	e.start()
	e.create()
	run := func(verb string) *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	start := func() *api.Crash {
		t.Helper()
		if op := run("start"); op.Status != api.OpFailed {
			t.Fatalf("start: %+v", op)
		}
		return e.waitCrash()
	}
	run("stop")

	e.fd.mu.Lock()
	e.fd.bootExit = 134
	e.fd.mu.Unlock()
	if c := start(); c.Params["holder"] != nil {
		t.Fatalf("a start that failed inside the server named a port holder: %v", c.Params)
	}
	mu.Lock()
	if len(asked) != 0 {
		t.Errorf("looked for a port holder %d time(s) for a failure that wasn't the port", len(asked))
	}
	mu.Unlock()

	e.fd.mu.Lock()
	e.fd.bootExit = 0
	e.fd.startErr = "driver failed programming external connectivity on endpoint pk: Bind for 0.0.0.0:25565 failed: port is already allocated"
	e.fd.mu.Unlock()
	c := start()
	if c.Kind != "port_in_use" || c.Params["holder"] != "java" || c.Params["holder_pid"] != 48211 {
		t.Fatalf("got %s with %v", c.Kind, c.Params)
	}
	mu.Lock()
	if len(asked) != 1 || asked[0] != e.srv().gamePort {
		t.Errorf("asked about ports %v, want the server's %d", asked, e.srv().gamePort)
	}
	holder = ""
	mu.Unlock()
	if c := start(); c.Kind != "port_in_use" || c.Params["holder"] != nil || c.Params["holder_pid"] != nil {
		t.Fatalf("a holder the agent can't see: got %s with %v", c.Kind, c.Params)
	}
}

// A start Docker refused because another container publishes the game port
// names that container, from Docker's list of running containers, and then
// doesn't look for a program. A container on another port or on the same
// port over UDP is passed over; one Playkeeper made, or one with a name
// Docker wouldn't give, is left unnamed; with no running container on the
// port, the program holding it is looked for as before.
func TestPortCrashNamesTheDockerContainerHoldingThePort(t *testing.T) {
	e := newAgentEnv(t)
	e.stop()
	e.crashBackoff = []time.Duration{time.Hour}
	var mu sync.Mutex
	asked := 0
	e.portHolder = func(int) (string, int, bool) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return "java", 48211, true
	}
	e.start()
	e.create()
	run := func(verb string) *api.Operation {
		t.Helper()
		code, out := e.call("POST", e.sp("/"+verb), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", verb, code, out)
		}
		return e.waitOp(out["id"].(string))
	}
	port := e.srv().gamePort
	start := func(others ...fakeListed) *api.Crash {
		t.Helper()
		e.fd.mu.Lock()
		e.fd.others = others
		e.fd.mu.Unlock()
		if op := run("start"); op.Status != api.OpFailed {
			t.Fatalf("start: %+v", op)
		}
		return e.waitCrash()
	}
	lookedFor := func() int {
		mu.Lock()
		defer mu.Unlock()
		return asked
	}
	run("stop")
	e.fd.mu.Lock()
	e.fd.startErr = "driver failed programming external connectivity on endpoint pk: Bind for 0.0.0.0:" + strconv.Itoa(port) + " failed: port is already allocated"
	e.fd.mu.Unlock()

	c := start(
		fakeListed{name: "web", running: true, ports: []fakePort{{port + 1, "tcp"}}},
		fakeListed{name: "voice", running: true, ports: []fakePort{{port, "udp"}}},
		fakeListed{name: "old-minecraft", running: true, ports: []fakePort{{port, "tcp"}, {port, "udp"}}},
	)
	if c.Kind != "port_in_use" || c.Params["holder_container"] != "old-minecraft" || c.Params["holder"] != nil {
		t.Fatalf("got %s with %v", c.Kind, c.Params)
	}
	if n := lookedFor(); n != 0 {
		t.Errorf("looked for a program %d time(s) where a container publishes the port", n)
	}
	for _, other := range []fakeListed{
		{name: "playkeeper-mc-zyxwvutsrq", labels: map[string]string{labelManaged: "true"}, running: true, ports: []fakePort{{port, "tcp"}}},
		{name: "old minecraft", running: true, ports: []fakePort{{port, "tcp"}}},
	} {
		if c := start(other); c.Kind != "port_in_use" || c.Params["holder_container"] != nil || c.Params["holder"] != nil {
			t.Fatalf("container %q: got %s with %v", other.name, c.Kind, c.Params)
		}
	}
	if n := lookedFor(); n != 0 {
		t.Errorf("looked for a program %d time(s) where a container publishes the port", n)
	}
	c = start(fakeListed{name: "stopped-minecraft", ports: []fakePort{{port, "tcp"}}})
	if c.Kind != "port_in_use" || c.Params["holder_container"] != nil || c.Params["holder"] != "java" || c.Params["holder_pid"] != 48211 {
		t.Fatalf("no running container on the port: got %s with %v", c.Kind, c.Params)
	}
	if n := lookedFor(); n != 1 {
		t.Errorf("looked for a program %d time(s), want once", n)
	}
}

// When Docker kills a server for memory and an automatic restart brings it
// back, the status keeps saying why, with the memory to give it, through a
// restart, until its memory changes or a day has passed; the activity says
// it ran out of memory.
func TestAMemoryKillIsExplainedAfterTheServerComesBack(t *testing.T) {
	e := newAgentEnv(t)
	e.createWith(map[string]any{"memoryMB": 2048})
	e.fd.oomKill()
	e.waitFor("the automatic restart", func() bool { return e.crashEvents() == 1 && e.onlineIdle() })
	st := e.status()
	c := st.RecoveredCrash
	if c == nil || c.Kind != "container_memory_limit" || !c.Certain || st.Crash != nil {
		t.Fatalf("after the automatic restart: recovered %+v, crash %+v", c, st.Crash)
	}
	if len(c.Fixes) == 0 || c.Fixes[0].Kind != "raise_memory" || c.Fixes[0].Params["to_mb"] != 3072 {
		t.Fatalf("fixes: %+v", c.Fixes)
	}
	acts, err := e.a.Activity(e.sid, 5)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, 0, len(acts))
	for _, a := range acts {
		kinds = append(kinds, a.Kind)
	}
	if !slices.Contains(kinds, "crashed_memory") || slices.Contains(kinds, "crashed") {
		t.Fatalf("activity: %v", kinds)
	}

	if op := e.runOp("POST", "/restart"); op.Status != api.OpSucceeded {
		t.Fatalf("restart: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if e.status().RecoveredCrash == nil {
		t.Fatal("a restart that left its memory as it was forgot why it crashed")
	}
	e.skew.Add(int64(recoveredFor))
	if e.status().RecoveredCrash != nil {
		t.Fatal("still shown a day later")
	}
	e.skew.Add(-int64(recoveredFor))
	if code, out := e.call("POST", e.sp("/settings"), map[string]any{"memoryMB": 3072, "actor": "admin"}); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	if c := e.status().RecoveredCrash; c != nil {
		t.Fatalf("still shown after its memory changed: %+v", c)
	}
}

// Java running out of memory and stopping the server is a crash for memory,
// as Docker's kill is: the error says so, and so does the activity. The next
// run that crashes without that line is a plain crash.
func TestJavaRunningOutOfMemoryIsAMemoryCrash(t *testing.T) {
	e := crashEnv(t)
	kinds := func() []string {
		acts, err := e.a.Activity(e.sid, 10)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, a := range acts {
			out = append(out, a.Kind)
		}
		return out
	}
	e.fd.addLog("java.lang.OutOfMemoryError: Java heap space")
	e.fd.addLog("[03:11:31 INFO]: Stopping server")
	e.fd.crash(1)
	e.waitCrash()
	if st := e.status(); st.LastError != heapCrash {
		t.Fatalf("the error says %q, want %q", st.LastError, heapCrash)
	}
	if k := kinds(); !slices.Contains(k, "crashed_memory") || slices.Contains(k, "crashed") {
		t.Fatalf("activity after running out of memory: %v", k)
	}

	if op := e.runOp("POST", "/start"); op.Status != api.OpSucceeded {
		t.Fatalf("start: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	e.fd.crash(1)
	e.waitFor("the second crash", func() bool { return e.crashEvents() == 2 })
	if k := kinds(); len(k) == 0 || k[0] != "crashed" || !slices.Contains(k, "crashed_memory") {
		t.Fatalf("activity after a crash without the line: %v", k)
	}
}

// A start that isn't accepted, and a remove-and-start whose remove fails,
// leave the crash and the crash count as they were: nothing started, so the
// crash still says why the server is down.
func TestAStartThatDoesNotGoAheadKeepsTheCrash(t *testing.T) {
	e := crashEnv(t)
	writeGameFile(t, filepath.Join(e.dataDir(), "plugins", "Multiverse-Portals-5.0.2.jar"), "portals", time.Time{})
	e.fd.crash(1)
	e.waitCrash()
	s := e.srv()
	s.mu.Lock()
	crash, crashes := s.crash, len(s.crashes)
	s.mu.Unlock()
	if crash == nil || crashes == 0 {
		t.Fatalf("no crash to keep: %v, %d counted", crash, crashes)
	}
	kept := func(what string) {
		t.Helper()
		s.mu.Lock()
		c, n := s.crash, len(s.crashes)
		s.mu.Unlock()
		if c != crash || n != crashes {
			t.Fatalf("%s: crash %v (want %v), %d counted (want %d)", what, c, crash, n, crashes)
		}
	}
	removeAndStart := func() (int, map[string]any) {
		return e.call("POST", e.sp("/addons/remove-file"), map[string]any{"actor": "admin", "jar": "Multiverse-Portals-5.0.2.jar", "start": true})
	}

	e.a.upd.mu.Lock()
	e.a.upd.installing = "0.4.1"
	e.a.upd.mu.Unlock()
	if code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"}); code != 409 {
		t.Fatalf("start during an update: %d %v", code, out)
	}
	kept("a start refused during an update")
	if code, out := removeAndStart(); code != 409 {
		t.Fatalf("remove and start during an update: %d %v", code, out)
	}
	kept("a remove-and-start refused during an update")
	e.a.upd.mu.Lock()
	e.a.upd.installing = ""
	e.a.upd.mu.Unlock()

	writeGameFile(t, filepath.Join(e.dataDir(), "..", removedAddonsDir), "a file where the folder goes", time.Time{})
	code, out := removeAndStart()
	if code != 202 {
		t.Fatalf("remove and start: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed {
		t.Fatalf("a remove that can't move the jar: %+v", op)
	}
	kept("a remove-and-start whose remove failed")
	if st := e.status(); st.Crash == nil || st.Crash.Kind != crash.Kind {
		t.Fatalf("status after the failed remove: %+v", st.Crash)
	}
}

// A Start someone asks for starts the crash policy over only once it goes
// ahead. One refused for a restore that isn't finished, for the world folder
// a restore left missing, for another job or during an update keeps the
// crash card, the crash count and the wait before the next automatic start;
// one that goes ahead forgets them.
func TestAStartForgetsTheCrashOnlyOnceItGoesAhead(t *testing.T) {
	stopped := func(t *testing.T, e *agentEnv) {
		e.create()
		if op := e.runOp("POST", "/stop"); op.Status != api.OpSucceeded {
			t.Fatalf("stop: %+v", op)
		}
	}
	for _, tc := range []struct {
		name string
		// setup says whether the start is accepted as an operation (true) or
		// refused before it (409).
		setup func(t *testing.T, e *agentEnv) (accepted bool)
		// errorKind is how an accepted start fails; "" for one that goes ahead.
		errorKind string
		kept      bool
	}{
		{"a restore that isn't finished", func(t *testing.T, e *agentEnv) bool {
			_, aside := restoreLeftUnsettled(t, e)
			e.failConfigSaves()
			if err := os.Rename(aside, e.dataDir()); err != nil {
				t.Fatal(err)
			}
			return true
		}, codeRestoreUnsettled, true},
		{"the world folder a restore left missing", func(t *testing.T, e *agentEnv) bool {
			restoreLeftUnsettled(t, e)
			return true
		}, "world_missing", true},
		{"another job", func(t *testing.T, e *agentEnv) bool {
			e.create()
			release, ok := e.srv().holdOpLock()
			if !ok {
				t.Fatal("the operation lock is taken")
			}
			t.Cleanup(release)
			return false
		}, "", true},
		{"an update", func(t *testing.T, e *agentEnv) bool {
			stopped(t, e)
			e.a.upd.mu.Lock()
			e.a.upd.installing = "0.4.1"
			e.a.upd.mu.Unlock()
			return false
		}, "", true},
		{"nothing: the start goes ahead", func(t *testing.T, e *agentEnv) bool {
			stopped(t, e)
			return true
		}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgentEnvWith(t, func(e *agentEnv) {
				e.tweak = func(o *Options) { o.ReconcileInterval = time.Hour }
			})
			e.withSources()
			accepted := tc.setup(t, e)
			s := e.srv()
			crash, next := &api.Crash{Kind: "heap_out_of_memory", Title: "Your Paper server ran out of memory"}, time.Now().Add(time.Hour)
			s.mu.Lock()
			s.crash, s.crashes, s.crashed, s.nextAutoRestart = crash, []time.Time{time.Now()}, true, next
			s.mu.Unlock()

			code, out := e.call("POST", e.sp("/start"), map[string]any{"actor": "admin"})
			switch {
			case accepted && code == 202:
				op := e.waitOp(out["id"].(string))
				if tc.errorKind == "" && op.Status != api.OpSucceeded || tc.errorKind != "" && (op.Status != api.OpFailed || op.Detail["errorKind"] != tc.errorKind) {
					t.Fatalf("the start: %+v, want error kind %q", op, tc.errorKind)
				}
			case !accepted && code == 409:
			default:
				t.Fatalf("start: %d %v", code, out)
			}
			s.mu.Lock()
			c, n, at := s.crash, len(s.crashes), s.nextAutoRestart
			s.mu.Unlock()
			if tc.kept && (c != crash || n != 1 || !at.Equal(next)) {
				t.Fatalf("after the refused start: crash %v, %d counted, next automatic start %v; want them kept", c, n, at)
			}
			if !tc.kept && (c != nil || n != 0 || !at.IsZero()) {
				t.Fatalf("after the start: crash %v, %d counted, next automatic start %v; want them forgotten", c, n, at)
			}
		})
	}
}

// Removing an add-on takes a jar file name, only for a jar directly in the
// server's plugin folder and never through a symlink, and moves it aside
// where the owner can find it.
func TestRemoveAddonMovesOnlyThatJarAside(t *testing.T) {
	e := crashEnv(t)
	plugins := filepath.Join(e.dataDir(), "plugins")
	writeGameFile(t, filepath.Join(plugins, "Multiverse-Portals-5.0.2.jar"), "portals", time.Time{})
	writeGameFile(t, filepath.Join(e.dataDir(), "outside.jar"), "not a plugin", time.Time{})
	if err := os.Mkdir(filepath.Join(plugins, "folder.jar"), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.jar")
	writeGameFile(t, secret, "host file", time.Time{})
	if err := os.Symlink(secret, filepath.Join(plugins, "link.jar")); err != nil {
		t.Fatal(err)
	}
	remove := func(jar string, start bool) (int, map[string]any) {
		return e.call("POST", e.sp("/addons/remove-file"), map[string]any{"actor": "admin", "jar": jar, "start": start})
	}
	for _, jar := range []string{"../outside.jar", "plugins/x.jar", `..\outside.jar`, "notes.txt", ".jar", ".hidden.jar", "a\nb.jar", strings.Repeat("a", 201) + ".jar", ""} {
		if code, out := remove(jar, false); code != 400 {
			t.Errorf("jar %q: %d %v", jar, code, out)
		}
	}
	for _, jar := range []string{"missing.jar", "folder.jar", "link.jar"} {
		if code, out := remove(jar, false); code != 404 {
			t.Errorf("jar %q: %d %v", jar, code, out)
		}
	}
	if code, out := remove("Multiverse-Portals-5.0.2.jar", false); code != 409 {
		t.Errorf("removing from a running server: %d %v", code, out)
	}

	e.fd.crash(1)
	e.waitCrash()
	code, out := remove("Multiverse-Portals-5.0.2.jar", true)
	if code != 202 {
		t.Fatalf("remove: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Kind != "remove-addon" {
		t.Fatalf("remove: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if _, err := os.Stat(filepath.Join(plugins, "Multiverse-Portals-5.0.2.jar")); !os.IsNotExist(err) {
		t.Errorf("the jar is still loaded: %v", err)
	}
	moved, _ := filepath.Glob(filepath.Join(e.dataDir(), "..", removedAddonsDir, "*-Multiverse-Portals-5.0.2.jar"))
	if len(moved) != 1 {
		t.Errorf("the jar was not kept aside: %v", moved)
	}
	for _, p := range []string{secret, filepath.Join(e.dataDir(), "outside.jar")} {
		if b, err := os.ReadFile(p); err != nil || len(b) == 0 {
			t.Errorf("%s was touched: %v", p, err)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE action = 'addon.remove' AND target = ? AND result = 'succeeded'`, "Multiverse-Portals-5.0.2.jar"); n != 1 {
		t.Errorf("want one audit entry, got %d", n)
	}

	// A plugin folder that is a symlink out of the server's directory.
	e.fd.crash(1)
	e.waitCrash()
	elsewhere := t.TempDir()
	writeGameFile(t, filepath.Join(elsewhere, "Chunky-1.4.10.jar"), "chunky", time.Time{})
	if err := os.RemoveAll(plugins); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, plugins); err != nil {
		t.Fatal(err)
	}
	if code, out := remove("Chunky-1.4.10.jar", false); code != 404 {
		t.Errorf("through a symlinked folder: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "Chunky-1.4.10.jar")); err != nil {
		t.Errorf("the file outside was moved: %v", err)
	}
}
