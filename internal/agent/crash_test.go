package agent

import (
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
