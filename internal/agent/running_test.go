package agent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diagnose"
	"github.com/CIYAhq/playkeeper/internal/docker"
)

// gcLine is one pause as the JVM logs it with Playkeeper's GC log flag.
func gcLine(at time.Time, n, before, after, heap int, pauseMS float64) string {
	return fmt.Sprintf("[%s][%d.000s][info][gc] GC(%d) Pause Young (Normal) (G1 Evacuation Pause) %dM->%dM(%dM) %.3fms\n",
		at.UTC().Format("2006-01-02T15:04:05.000-0700"), 100+n, n, before, after, heap, pauseMS)
}

// withoutGCLog is a container definition as 0.3.0 made it, before the GC log.
func withoutGCLog(cfg docker.ContainerConfig) docker.ContainerConfig {
	delete(cfg.Labels, labelGCLog)
	cfg.Env = slices.DeleteFunc(slices.Clone(cfg.Env), func(v string) bool { return strings.HasPrefix(v, "JVM_OPTS=") })
	return cfg
}

func (e *agentEnv) running() api.Running {
	e.t.Helper()
	var r api.Running
	e.decode("GET", e.sp("/running"), &r)
	return r
}

func (e *agentEnv) lag() string {
	if r := e.status().Resources; r != nil {
		return r.Lag
	}
	return ""
}

func (e *agentEnv) setTicks(tps, mspt string) {
	e.rcon.mu.Lock()
	e.rcon.tps, e.rcon.mspt = tps, mspt
	e.rcon.mu.Unlock()
}

// withoutProcStat restarts the agent without /proc/stat, so the test decides
// what the machine's processor did.
func (e *agentEnv) withoutProcStat() {
	e.t.Helper()
	e.stop()
	e.procStat = func() ([]byte, error) { return nil, errors.New("no /proc/stat in this test") }
	e.start()
}

func (e *agentEnv) gcCollections() int {
	return e.countRows(`SELECT COALESCE(SUM(collections), 0) FROM gc_windows WHERE server_id = ?`, e.sid)
}

// "How it's running" reads the tick rate over RCON and explains a server that
// falls behind with the causes its measurements point at, most likely first,
// each with the numbers the page's buttons need.
func TestHowItsRunningExplainsTheLag(t *testing.T) {
	e := newAgentEnv(t)
	e.withoutProcStat()
	e.create()
	e.rcon.setOnline("Alex", "Steve")
	now := time.Now()
	if err := os.WriteFile(filepath.Join(e.dataDir(), "server.properties"), []byte("view-distance=16\nsimulation-distance=12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gc strings.Builder
	for i := range 10 {
		gc.WriteString(gcLine(now.Add(-5*time.Minute+time.Duration(i)*20*time.Second), i, 1010, 950, 1024, 150))
	}
	if err := os.WriteFile(filepath.Join(e.dataDir(), "logs", "gc.log"), []byte(gc.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	e.waitFor("a smooth diagnosis", func() bool { return e.lag() == "smooth" })
	if res := e.status().Resources; res.TPS == nil || *res.TPS != 20 || res.MSPT == nil || *res.MSPT != 4.9 {
		t.Fatalf("Paper's tps and mspt replies: %+v", res)
	}
	if r := e.running(); r.Status != "smooth" || len(r.Causes) != 0 || r.WindowMinutes != 10 || r.At == nil || r.BehindSince != nil || r.Players == nil || *r.Players != 2 {
		t.Fatalf("a smooth server: %+v", r)
	}
	if n := e.gcCollections(); n != 10 {
		t.Fatalf("the GC log's pauses are stored for the memory advice: %d, want 10", n)
	}

	e.setTicks(paperTPSBehind, paperMSPTBehind)
	e.waitFor("a bit behind", func() bool { return e.lag() == "a_bit_behind" })
	r := e.running()
	if r.Params["tps"] != 17.1 || r.Params["mspt"] != 58.4 || r.BehindSince == nil || r.BehindSince.After(time.Now()) {
		t.Fatalf("the headline's numbers: %+v", r)
	}
	if len(r.Causes) != 2 || r.Causes[0].Kind != "memory_pressure" || r.Causes[1].Kind != "high_distance" {
		t.Fatalf("the causes, most likely first: %+v", r.Causes)
	}
	if raise := r.Causes[0].Actions[0]; raise.Kind != "raise_memory" || raise.Params["from_mb"] != 1536.0 || raise.Params["to_mb"] != 2048.0 || !raise.Recommended {
		t.Fatalf("memory pressure offers the next budget that fits: %+v", raise)
	}
	if p := r.Causes[1].Params; p["view_distance"] != 16.0 || p["simulation_distance"] != 12.0 {
		t.Fatalf("the distances come from server.properties: %+v", p)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM samples WHERE server_id = ? AND tps = 17.1 AND mspt = 58.4`, e.sid); n == 0 {
		t.Fatal("samples keep the tick rate and tick time")
	}
	var m api.MetricsResponse
	e.decode("GET", e.sp("/metrics?range=1h"), &m)
	charted := false
	for _, b := range m.Buckets {
		charted = charted || b.TPSAvg != nil && *b.TPSAvg < 20 && b.MSPTAvg != nil && *b.MSPTAvg > 4.9
	}
	if !charted {
		t.Fatalf("the charts average the tick rate and tick time: %+v", m.Buckets)
	}

	code, out := e.call("POST", e.sp("/stop"), map[string]any{"actor": "admin"})
	if code != 202 {
		t.Fatalf("stop: %d %v", code, out)
	}
	e.waitOp(out["id"].(string))
	e.waitFor("no diagnosis while stopped", func() bool { return e.running().Status == "unknown" })
	if r := e.running(); r.Params["running"] != false || len(r.Causes) != 0 || e.lag() != "" {
		t.Fatalf("a stopped server is not measured: %+v", r)
	}
}

// Steal time, the processor time a hosting provider gives other customers,
// shows in the machine's /proc/stat and leads the causes when it is high
// while the server falls behind.
func TestCPUStealExplainsLag(t *testing.T) {
	e := newAgentEnv(t)
	e.withoutProcStat()
	e.create()
	now := e.a.now()
	e.a.recordCPU(now.Add(-2*time.Minute), diagnose.CPUTimes{User: 1000, System: 200, Idle: 800})
	e.a.recordCPU(now.Add(-time.Second), diagnose.CPUTimes{User: 1400, System: 300, Idle: 1050, Steal: 250})
	var m api.Machine
	e.decode("GET", "/v1/machine", &m)
	if m.CPUPercent == nil || *m.CPUPercent != 50 {
		t.Fatalf("the machine's CPU use leaves steal out: %v", m.CPUPercent)
	}
	e.setTicks(paperTPSBehind, paperMSPTBehind)
	e.waitFor("a bit behind", func() bool { return e.lag() == "a_bit_behind" })
	if r := e.running(); len(r.Causes) == 0 || r.Causes[0].Kind != "cpu_steal" || r.Causes[0].Params["percent"] != 25.0 {
		t.Fatalf("steal leads the causes: %+v", r.Causes)
	}
}

// The GC log belongs to the game, which can write anything to it or put
// something else in its place. Each pause is counted once, across lines still
// being written, agent restarts and the JVM rotating the file; a pipe or a
// link out of the server's folder is not read, and two weeks are kept.
func TestGCLogIsReadOnce(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	logPath := filepath.Join(e.dataDir(), "logs", "gc.log")
	now := time.Now()
	appendLog := func(s string) {
		t.Helper()
		f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	settle := func() { time.Sleep(500 * time.Millisecond) }

	appendLog(gcLine(now.Add(-3*time.Minute), 1, 900, 400, 1024, 10) + gcLine(now.Add(-2*time.Minute), 2, 1000, 600, 1024, 20) + gcLine(now.Add(-time.Minute), 3, 950, 500, 1024, 30))
	e.waitFor("three pauses", func() bool { return e.gcCollections() == 3 })
	var minAfter, maxAfter, heap int
	var pause, longest float64
	if err := e.a.db.QueryRow(`SELECT MIN(min_after_mb), MAX(max_after_mb), MAX(heap_mb), SUM(pause_ms), MAX(max_pause_ms) FROM gc_windows WHERE server_id = ?`, e.sid).
		Scan(&minAfter, &maxAfter, &heap, &pause, &longest); err != nil {
		t.Fatal(err)
	}
	if minAfter != 400 || maxAfter != 600 || heap != 1024 || pause != 60 || longest != 30 {
		t.Fatalf("the windows sum up the pauses: after %d–%d of %d MB, pauses %v ms, longest %v ms", minAfter, maxAfter, heap, pause, longest)
	}

	line := gcLine(now.Add(-30*time.Second), 4, 900, 450, 1024, 15)
	appendLog(line[:len(line)-1])
	settle()
	if n := e.gcCollections(); n != 3 {
		t.Fatalf("a line still being written is not read: %d", n)
	}
	appendLog("\n")
	e.waitFor("the finished line", func() bool { return e.gcCollections() == 4 })

	e.stop()
	e.start()
	e.waitFor("online after the agent restart", func() bool { return e.status().Phase == api.PhaseOnline })
	settle()
	if n := e.gcCollections(); n != 4 {
		t.Fatalf("an agent restart must not count pauses again: %d", n)
	}

	if err := os.Rename(logPath, logPath+".0"); err != nil {
		t.Fatal(err)
	}
	appendLog(gcLine(now.Add(-10*time.Second), 5, 900, 480, 1024, 12))
	e.waitFor("the new log after rotation", func() bool { return e.gcCollections() == 5 })

	os.Remove(logPath)
	if err := syscall.Mkfifo(logPath, 0o644); err != nil {
		t.Fatal(err)
	}
	// A reader stuck opening the pipe would keep the agent from stopping; a
	// writer lets it go.
	t.Cleanup(func() {
		if f, err := os.OpenFile(logPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	samples := func() int { return e.countRows(`SELECT COUNT(*) FROM samples WHERE server_id = ?`, e.sid) }
	before := samples()
	e.waitFor("sampling to go on past a pipe", func() bool { return samples() >= before+3 })
	os.Remove(logPath)
	outside := filepath.Join(e.dir, "outside.log")
	if err := os.WriteFile(outside, []byte(gcLine(now, 6, 900, 480, 1024, 12)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, logPath); err != nil {
		t.Fatal(err)
	}
	settle()
	if n := e.gcCollections(); n != 5 {
		t.Fatalf("a link out of the server's folder is not read: %d", n)
	}

	old := now.Add(-15 * 24 * time.Hour).Truncate(gcWindow)
	if _, err := e.a.db.Exec(`INSERT INTO gc_windows(server_id, start, collections, min_after_mb, max_after_mb, heap_mb, full_gcs, evacuation_failures, pause_ms, max_pause_ms) VALUES(?, ?, 9, 1, 1, 1024, 0, 0, 1, 1)`,
		e.sid, old.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	e.a.prune()
	if n := e.gcCollections(); n != 5 {
		t.Fatalf("pruning keeps the last two weeks: %d", n)
	}
}

// The GC log needs a JVM flag, and a container with a different definition is
// a new container. A server running in a container made before the flag keeps
// running untouched, without a pending restart; the flag arrives with its
// next start.
func TestGCLogFlagAppliesFromTheNextStart(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	if fi, err := os.Stat(filepath.Join(e.dataDir(), "logs")); err != nil || !fi.IsDir() {
		t.Fatalf("Java refuses to start without the GC log's folder: %v", err)
	}
	flagged := func() bool {
		e.fd.mu.Lock()
		defer e.fd.mu.Unlock()
		c := e.fd.server()
		return env(c.cfg, "JVM_OPTS") == gcLogFlag && c.cfg.Labels[labelGCLog] == gcLogVersion
	}
	if !flagged() {
		t.Fatal("a new container logs GC pauses")
	}
	e.fd.mu.Lock()
	c := e.fd.server()
	c.cfg = withoutGCLog(c.cfg)
	e.fd.mu.Unlock()
	started := e.status().StartedAt
	replaced := func() int {
		return e.fd.called("POST /containers/create") + e.fd.called("DELETE /containers/") + e.fd.called("POST /containers/"+e.cname()+"/stop")
	}
	before := replaced()

	e.stop()
	e.start()
	e.waitFor("online after the agent restart", func() bool { return e.status().Phase == api.PhaseOnline })
	time.Sleep(300 * time.Millisecond)
	if st := e.status(); replaced() != before || st.PendingRestart || st.StartedAt == nil || !st.StartedAt.Equal(*started) {
		t.Fatalf("a running server is never restarted for the GC log: calls %d → %d, pending %v, started %v → %v", before, replaced(), st.PendingRestart, started, st.StartedAt)
	}

	for _, action := range []string{"/stop", "/start"} {
		code, out := e.call("POST", e.sp(action), map[string]any{"actor": "admin"})
		if code != 202 {
			t.Fatalf("%s: %d %v", action, code, out)
		}
		if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
			t.Fatalf("%s: %+v", action, op)
		}
	}
	e.waitFor("online", func() bool { return e.status().Phase == api.PhaseOnline })
	if !flagged() {
		t.Fatal("the next start logs GC pauses")
	}
}

// regionHeader is the location table of a region file holding chunks chunks.
func regionHeader(chunks int) []byte {
	h := make([]byte, 8192)
	for i := range chunks {
		binary.BigEndian.PutUint32(h[i*4:], uint32(2+i)<<8|1)
	}
	return h
}

// New land shows in the region files' location tables. Only region folders
// count: entities and poi folders use the same format for other data, and a
// link the game could have put in place of a file is not followed.
func TestNewChunksComeFromRegionFiles(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	write := func(rel string, chunks int) {
		t.Helper()
		p := filepath.Join(e.dataDir(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, regionHeader(chunks), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("world/region/r.0.0.mca", 40)
	write("world/region/r.-1.0.mca", 60)
	write("world_nether/DIM-1/region/r.0.0.mca", 7)
	write("world_the_end/DIM1/region/r.0.0.mca", 3)
	write("world/entities/r.0.0.mca", 500)
	write("world/poi/r.0.0.mca", 500)
	write("world/region/r.0.0.mca.bak", 500)
	outside := filepath.Join(e.dir, "elsewhere.mca")
	if err := os.WriteFile(outside, regionHeader(900), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(e.dataDir(), "world", "region", "r.5.5.mca")); err != nil {
		t.Fatal(err)
	}
	if n, ok := e.srv().countChunks("world"); !ok || n != 110 {
		t.Fatalf("chunks in the world's region files: %d %v, want 110", n, ok)
	}

	s := &server{}
	t0 := time.Now()
	for _, c := range []struct {
		after time.Duration
		count int
		want  int // -1: not known
	}{
		{0, 1000, -1},
		{5 * time.Minute, 1100, 100},
		{10 * time.Minute, 1400, 400},
		{15 * time.Minute, 1400, 300},
		{20 * time.Minute, 900, 0},
	} {
		s.recordChunks(t0.Add(c.after), c.count)
		got := s.newChunks(t0.Add(c.after))
		if (c.want < 0) != (got == nil) || got != nil && *got != c.want {
			t.Fatalf("after %v: new chunks %v, want %d", c.after, got, c.want)
		}
	}
	if s.newChunks(t0.Add(31*time.Minute)) != nil {
		t.Fatal("counts that stopped coming say nothing about now")
	}
}
