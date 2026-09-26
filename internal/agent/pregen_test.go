package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/pregen"
)

// fakeChunky is Chunky on the fake server. It answers Chunky's console
// commands as Chunky on Paper does, for the overworld "world" only; it
// loads when the server starts with its jar among the plugins, saves a
// running task when the server stops, and resumes the saved task at start
// when its config says so.
type fakeChunky struct {
	dataDir string

	mu      sync.Mutex
	loaded  bool
	running bool
	// broken keeps Chunky from loading, as a plugin that fails to enable.
	broken bool
	// task is the running task, or the one Chunky saved last.
	task *fakeChunkyTask
	// confirm is what "chunky confirm" does now.
	confirm func() string
	// strange are the chunky commands the fake doesn't know.
	strange []string
}

type fakeChunkyTask struct {
	centerX, centerZ, radius int
	chunks, total, millis    int64
	cancelled                bool
}

var reChunkyStart = regexp.MustCompile(`^chunky start world square (-?[0-9]+) (-?[0-9]+) ([0-9]+)$`)

// chunky puts Chunky on the current server's console. It loads the next
// time the server starts with its jar in the plugins folder.
func (e *agentEnv) chunky() *fakeChunky {
	e.t.Helper()
	fc := &fakeChunky{dataDir: e.dataDir()}
	e.rcon.mu.Lock()
	e.rcon.answer = fc.answer
	e.rcon.mu.Unlock()
	e.fd.mu.Lock()
	e.fd.started = func(*fakeContainer) { fc.serverStarted() }
	e.fd.stopped = func(*fakeContainer) { fc.serverStopped() }
	e.fd.mu.Unlock()
	e.t.Cleanup(func() {
		fc.mu.Lock()
		defer fc.mu.Unlock()
		if len(fc.strange) > 0 {
			e.t.Errorf("Chunky got commands it doesn't know: %q", fc.strange)
		}
	})
	return fc
}

func (fc *fakeChunky) answer(cmd string) (string, bool) {
	if !strings.HasPrefix(cmd, "chunky ") {
		return "", false
	}
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if !fc.loaded {
		return "", false
	}
	t := fc.task
	unfinished := t != nil && !t.cancelled
	switch {
	case cmd == "chunky progress":
		if !fc.running {
			return "[Chunky] No tasks running.", true
		}
		return fmt.Sprintf("[Chunky] Task running for world. Processed: %d chunks (%.2f%%), ETA: 0:14:00, Rate: 118.2 cps, Current: 3, -4",
			t.chunks, 100*float64(t.chunks)/float64(t.total)), true
	case cmd == "chunky reload":
		return "[Chunky] Successfully reloaded configuration.", true
	case cmd == "chunky pattern region":
		return "[Chunky] Pattern changed to region.", true
	case cmd == "chunky world world":
		return "[Chunky] World changed to world.", true
	case cmd == "chunky spawn":
		return "[Chunky] Center changed to 16, -32.", true
	case reChunkyStart.MatchString(cmd):
		if fc.running {
			return "[Chunky] Task already started for world!", true
		}
		m := reChunkyStart.FindStringSubmatch(cmd)
		x, _ := strconv.Atoi(m[1])
		z, _ := strconv.Atoi(m[2])
		r, _ := strconv.Atoi(m[3])
		start := func() string {
			side := 2*int64(math.Ceil(float64(r)/16)) + 1
			fc.task, fc.running = &fakeChunkyTask{centerX: x, centerZ: z, radius: r, total: side * side}, true
			return fmt.Sprintf("[Chunky] Task started in world for the square region centered at %d, %d with radius %d.", x, z, r)
		}
		if unfinished {
			fc.confirm = start
			return "[Chunky] A task was already started for this world. To continue running it, type '/chunky continue'. To start a new task, type '/chunky confirm'.", true
		}
		return start(), true
	case cmd == "chunky confirm":
		do := fc.confirm
		fc.confirm = nil
		if do == nil {
			return "[Chunky] Nothing to confirm!", true
		}
		return do(), true
	case cmd == "chunky pause world":
		if !fc.running {
			return "[Chunky] No tasks to pause.", true
		}
		fc.running = false
		fc.save()
		return "[Chunky] Task paused for world.", true
	case cmd == "chunky continue world":
		switch {
		case fc.running:
			return "[Chunky] Task already started for world!", true
		case !unfinished:
			return "[Chunky] No tasks to continue.", true
		}
		fc.running = true
		return "[Chunky] Task continuing for world.", true
	case cmd == "chunky cancel world":
		if !fc.running && !unfinished {
			return "[Chunky] No tasks to cancel.", true
		}
		fc.confirm = func() string {
			fc.running, fc.task.cancelled = false, true
			fc.save()
			return "[Chunky] Task cancelled for world."
		}
		return "[Chunky] Cancelled tasks cannot be continued. Type '/chunky confirm' to proceed.", true
	}
	fc.strange = append(fc.strange, cmd)
	return "", true
}

func (fc *fakeChunky) taskFile() string {
	return filepath.Join(fc.dataDir, filepath.FromSlash(pregen.TaskDir(pregen.Bukkit)), "world.properties")
}

// save writes the task to Chunky's tasks folder, as Chunky does when a
// task stops.
func (fc *fakeChunky) save() {
	t := fc.task
	props := fmt.Sprintf("world=world\ncancelled=%t\ncenter-x=%d.0\ncenter-z=%d.0\nradius=%d.0\nshape=square\npattern=region\nchunks=%d\ntime=%d\n",
		t.cancelled, t.centerX, t.centerZ, t.radius, t.chunks, t.millis)
	if err := os.MkdirAll(filepath.Dir(fc.taskFile()), 0o755); err == nil {
		os.WriteFile(fc.taskFile(), []byte(props), 0o644)
	}
}

// serverStarted loads Chunky if its jar is among the plugins, reading the
// task it saved, and resumes that task if the config says to.
func (fc *fakeChunky) serverStarted() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	_, err := pregen.Detect(fc.dataDir, pregen.Bukkit)
	fc.loaded, fc.running, fc.confirm, fc.task = err == nil && !fc.broken, false, nil, nil
	if !fc.loaded {
		return
	}
	if t, found, err := pregen.ReadTask(fc.dataDir, pregen.Bukkit, "world"); err == nil && found {
		fc.task = &fakeChunkyTask{centerX: int(t.CenterX), centerZ: int(t.CenterZ), radius: int(t.Radius),
			chunks: t.Chunks, total: t.Total, millis: t.ElapsedSeconds * 1000, cancelled: t.Cancelled}
	}
	cfg, _ := os.ReadFile(filepath.Join(fc.dataDir, filepath.FromSlash(pregen.ConfigPath(pregen.Bukkit))))
	fc.running = fc.task != nil && !fc.task.cancelled && strings.Contains(string(cfg), "continue-on-restart: true\n")
}

func (fc *fakeChunky) serverStopped() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.running {
		fc.save()
	}
	fc.loaded, fc.running = false, false
}

// advance has the running task process n more chunks, 100 a second.
func (fc *fakeChunky) advance(n int64) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.task.chunks = min(fc.task.chunks+n, fc.task.total)
	fc.task.millis += n * 10
}

// finish has the running task process the rest of its area, taking took
// in all, and saves it as Chunky saves a finished task. The world grows by
// one 8 KiB region file.
func (fc *fakeChunky) finish(took time.Duration) {
	fc.mu.Lock()
	total := fc.task.total
	fc.mu.Unlock()
	fc.endAt(total, took)
}

// endAt has the running task reach the end of its area, taking took in
// all, and saves it as Chunky saves a task that ran to its end: cancelled,
// with chunks processed. Chunky saves it while its last chunks still load,
// so chunks can fall short of the area. The world grows by one 8 KiB
// region file.
func (fc *fakeChunky) endAt(chunks int64, took time.Duration) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.task.chunks, fc.task.millis, fc.task.cancelled = chunks, took.Milliseconds(), true
	fc.running = false
	fc.save()
	region := filepath.Join(fc.dataDir, "world", "region", "r.0.0.mca")
	if err := os.MkdirAll(filepath.Dir(region), 0o755); err == nil {
		os.WriteFile(region, make([]byte, 8192), 0o644)
	}
}

// lose makes Chunky forget the task, as when someone deletes its files.
func (fc *fakeChunky) lose() {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.running, fc.task = false, nil
	os.Remove(fc.taskFile())
}

func (fc *fakeChunky) setBroken(broken bool) {
	fc.mu.Lock()
	fc.broken = broken
	fc.mu.Unlock()
}

// state is whether Chunky runs a task, and the task it has.
func (fc *fakeChunky) state() (running bool, task fakeChunkyTask) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if fc.task != nil {
		task = *fc.task
	}
	return fc.running, task
}

func (e *agentEnv) pregen() api.Pregen {
	e.t.Helper()
	var out api.Pregen
	e.decode("GET", e.sp("/pregen"), &out)
	return out
}

// startPregen pre-generates a preset's area and waits until Chunky runs it.
func (e *agentEnv) startPregen(preset string, pauseForPlayers bool) *api.Operation {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": preset, "pauseForPlayers": pauseForPlayers, "actor": "admin"})
	if code != 202 {
		e.t.Fatalf("start pre-generating: %d %v", code, out)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || op.Kind != "pregen-start" {
		e.t.Fatalf("pre-generation start: %+v", op)
	}
	return op
}

// pregenAct pauses, continues or cancels the task and returns the state the
// route answers with.
func (e *agentEnv) pregenAct(action string) api.Pregen {
	e.t.Helper()
	code, out := e.call("POST", e.sp("/pregen/"+action), map[string]any{"actor": "admin"})
	if code != 200 {
		e.t.Fatalf("%s: %d %v", action, code, out)
	}
	var v api.Pregen
	b, _ := json.Marshal(out)
	if err := json.Unmarshal(b, &v); err != nil {
		e.t.Fatal(err)
	}
	return v
}

// serverOp starts, stops or restarts the server and waits until it is done.
func (e *agentEnv) serverOp(path string) {
	e.t.Helper()
	code, out := e.call("POST", e.sp(path), map[string]any{"actor": "admin"})
	if code != 202 {
		e.t.Fatalf("POST %s: %d %v", path, code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		e.t.Fatalf("%s: %+v", path, op)
	}
	if path != "/stop" {
		e.waitFor("online", e.onlineIdle)
	}
}

// resumesOnRestart is what Chunky's config says about resuming tasks.
func (e *agentEnv) resumesOnRestart() bool {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dataDir(), "plugins", "Chunky", "config.yml"))
	if err != nil {
		e.t.Fatal(err)
	}
	return strings.Contains(string(b), "continue-on-restart: true\n")
}

func (e *agentEnv) playersOnline() int {
	s := e.srv()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.players == nil {
		return -1
	}
	return s.players.Online
}

// Starting installs Chunky and restarts the server to load it, then the
// agent follows the task to the end and keeps its rate for the estimates.
func TestPregenInstallsChunkyAndFollowsTheTask(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()

	v := e.pregen()
	sizes := map[string]int64{}
	for _, p := range v.Presets {
		sizes[p.ID] = p.Chunks
		if !p.Fits || p.Seconds <= 0 || p.DiskBytes <= 0 {
			t.Errorf("preset %+v", p)
		}
	}
	if v.State != "idle" || v.World != "world" || v.Installed || !v.PauseForPlayers || v.ETASeconds != -1 || v.DiskFreeBytes == nil || *v.DiskFreeBytes != 50<<30 ||
		!maps.Equal(sizes, map[string]int64{"small": 16129, "medium": 99225, "large": 393129, "huge": 1565001}) {
		t.Fatalf("before pre-generating: %+v", v)
	}

	e.fd.mu.Lock()
	e.fd.bootDelay = 500 * time.Millisecond
	e.fd.mu.Unlock()
	e.rcon.setOnline("mara_k")
	e.waitFor("mara_k to be seen", func() bool { return e.playersOnline() == 1 })
	code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": "medium", "pauseForPlayers": false, "actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	e.waitFor("the restart to load Chunky", func() bool { return e.pregen().Step == "restarting" })
	if v := e.pregen(); v.State != "starting" || v.Preset != "medium" || v.Radius != 2500 || v.PauseForPlayers || !v.Installed {
		t.Fatalf("while starting: %+v", v)
	}
	op := e.waitOp(out["id"].(string))
	if op.Status != api.OpSucceeded || opDetail[string](t, op, "step") != "starting_task" {
		t.Fatalf("start: %+v", op)
	}
	e.fd.mu.Lock()
	e.fd.bootDelay = 30 * time.Millisecond
	e.fd.mu.Unlock()
	if n := e.rcon.count("say Restarting in 1 second, back soon!"); n != 1 {
		t.Errorf("players were warned %d times before the restart", n)
	}
	if running, task := fc.state(); !running || task.radius != 2500 || task.centerX != 16 || task.centerZ != -32 || task.total != 99225 {
		t.Fatalf("Chunky runs %v: %+v", running, task)
	}
	if !e.resumesOnRestart() {
		t.Error("Chunky will not resume the task after a restart")
	}
	if got := fileStatus(e.addonList().Files); !maps.Equal(got, map[string]string{"Chunky-1.4.40.jar": "managed"}) {
		t.Errorf("add-ons after the start: %v", got)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'pregen.started' AND detail = 'medium: 2500 blocks around 16, -32'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the start", n)
	}

	fc.advance(41675)
	e.waitFor("progress", func() bool { return e.pregen().Chunks == 41675 })
	v = e.pregen()
	if v.State != "running" || v.Total != 99225 || v.Percent != 42 || v.Rate != 118.2 || v.ETASeconds != 840 || v.StartedAt == nil || !v.Installed || v.PausedBy != "" {
		t.Fatalf("running: %+v", v)
	}
	if running, _ := fc.state(); !running {
		t.Fatal("the task paused for mara_k although the user said not to")
	}

	e.stop()
	e.start()
	e.waitFor("the task after an agent restart", func() bool { v := e.pregen(); return v.State == "running" && v.Chunks == 41675 })

	fc.finish(24 * time.Minute)
	e.waitFor("the finished task", func() bool { return e.pregen().State == "finished" })
	v = e.pregen()
	if v.Percent != 100 || v.Chunks != 99225 || v.ElapsedSeconds != 1440 || v.FinishedAt == nil || v.DiskBytes == nil || *v.DiskBytes != 8192 || v.Preset != "medium" || v.Radius != 2500 {
		t.Fatalf("finished: %+v", v)
	}
	for _, p := range v.Presets {
		if p.ID == "medium" && p.Seconds != 1440 {
			t.Errorf("the medium size is expected to take %d seconds after one took 1440", p.Seconds)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'playkeeper' AND action = 'pregen.finished' AND detail = '99225 chunks in 24 minutes'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the finish", n)
	}
}

// A task pauses while people play and continues once the server has been
// empty for a while. A task someone paused stays paused, and one someone
// continued while people play keeps running until the server is empty.
func TestPregenPausesWhilePeoplePlay(t *testing.T) {
	e := newAgentEnv(t)
	e.pregenResumeAfter = 300 * time.Millisecond
	e.withSources()
	e.create()
	fc := e.chunky()
	e.startPregen("small", true)

	e.rcon.setOnline("mara_k")
	e.waitFor("the pause for players", func() bool { return e.pregen().PausedBy == "players" })
	v := e.pregen()
	if running, _ := fc.state(); running || v.State != "paused" || v.PausedFor != "mara_k" {
		t.Fatalf("Chunky runs %v while mara_k plays: %+v", running, v)
	}
	if !e.resumesOnRestart() {
		t.Error("a task paused for players must still resume after a restart")
	}
	e.rcon.setOnline()
	e.waitFor("the task to continue", func() bool { return e.pregen().State == "running" })
	if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ? AND (paused_by_policy = 1 OR paused_for != '')`, e.sid); n != 0 {
		t.Fatal("the task still counts as paused for players")
	}

	v = e.pregenAct("pause")
	if running, _ := fc.state(); running || v.State != "paused" || v.PausedBy != "user" || e.resumesOnRestart() {
		t.Fatalf("after pausing, Chunky runs %v: %+v", running, v)
	}
	time.Sleep(3 * e.pregenResumeAfter)
	if running, _ := fc.state(); running || e.pregen().PausedBy != "user" {
		t.Fatal("the policy continued a task the user paused")
	}

	e.rcon.setOnline("mara_k")
	e.waitFor("mara_k to be seen", func() bool { return e.playersOnline() == 1 })
	v = e.pregenAct("continue")
	if running, _ := fc.state(); !running || v.State != "running" || v.PausedBy != "" || !e.resumesOnRestart() {
		t.Fatalf("after continuing, Chunky runs %v: %+v", running, v)
	}
	time.Sleep(3 * e.pregenResumeAfter)
	if running, _ := fc.state(); !running {
		t.Fatal("the policy paused a task the user continued while mara_k plays")
	}
	e.rcon.setOnline()
	s := e.srv()
	e.waitFor("the server to be seen empty", func() bool {
		s.pg.mu.Lock()
		defer s.pg.mu.Unlock()
		return !s.pg.override
	})
	e.rcon.setOnline("mara_k")
	e.waitFor("the pause for players", func() bool { return e.pregen().PausedBy == "players" })
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action LIKE 'pregen.%' AND actor != 'admin'`, e.sid); n != 0 {
		t.Errorf("%d audit entries for pausing for players", n)
	}
}

// A task pauses with the server and resumes when it starts again, and a
// task someone paused stays paused through restarts. Starting on a stopped
// server starts it.
func TestPregenAcrossServerStops(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()
	e.serverOp("/stop")
	stops := e.fd.called("POST /containers/" + e.cname() + "/stop")

	e.startPregen("small", true)
	if st := e.status(); st.Phase != api.PhaseOnline || st.Desired != api.DesiredRunning {
		t.Fatalf("after starting on a stopped server: %+v", st)
	}
	if n := e.fd.called("POST /containers/" + e.cname() + "/stop"); n != stops {
		t.Errorf("the stopped server was stopped %d more times", n-stops)
	}
	if running, task := fc.state(); !running || task.radius != 1000 || task.total != 16129 {
		t.Fatalf("Chunky runs %v: %+v", running, task)
	}

	fc.advance(5000)
	e.waitFor("progress", func() bool { return e.pregen().Chunks == 5000 })
	e.serverOp("/stop")
	v := e.pregen()
	if v.State != "paused" || v.PausedBy != "server" || v.Chunks != 5000 || v.ElapsedSeconds != 50 || math.Abs(v.Percent-100*5000.0/16129) > 1e-9 {
		t.Fatalf("with the server stopped: %+v", v)
	}
	e.serverOp("/start")
	e.waitFor("the task to resume", func() bool { return e.pregen().State == "running" })

	if v := e.pregenAct("pause"); v.PausedBy != "user" {
		t.Fatalf("after pausing: %+v", v)
	}
	e.serverOp("/restart")
	if running, _ := fc.state(); running {
		t.Fatal("Chunky resumed a task the user paused")
	}
	if v := e.pregen(); v.State != "paused" || v.PausedBy != "user" || v.Chunks != 5000 {
		t.Fatalf("after a restart: %+v", v)
	}
	e.serverOp("/stop")
	if v := e.pregen(); v.PausedBy != "user" {
		t.Fatalf("a task the user paused, with the server stopped: %+v", v)
	}
	if v := e.pregenAct("continue"); v.State != "paused" || v.PausedBy != "server" || !e.resumesOnRestart() {
		t.Fatalf("continuing while the server is stopped: %+v", v)
	}
	e.serverOp("/start")
	e.waitFor("the task to resume", func() bool { return e.pregen().State == "running" })
}

// Cancelling ends the task for good, with the server running or stopped,
// and a new task replaces what Chunky kept. A task Chunky no longer has
// ends as cancelled.
func TestPregenCancel(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()
	e.startPregen("small", true)
	fc.advance(1000)
	e.waitFor("progress", func() bool { return e.pregen().Chunks == 1000 })

	v := e.pregenAct("cancel")
	if _, task := fc.state(); !task.cancelled || v.State != "idle" || v.Error != "" || v.Preset != "" {
		t.Fatalf("after cancelling, Chunky has %+v: %+v", task, v)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ? AND ended = 'cancelled' AND chunks = 1000 AND elapsed_secs = 10 AND rate = 100`, e.sid); n != 1 {
		t.Fatal("the cancelled task was not recorded with its rate")
	}
	for _, p := range v.Presets {
		if p.ID == "small" && p.Seconds != 162 {
			t.Errorf("the small size is expected to take %d seconds at 100 chunks a second", p.Seconds)
		}
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'admin' AND action = 'pregen.cancelled'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the cancel", n)
	}

	e.startPregen("small", true)
	e.serverOp("/stop")
	if v := e.pregenAct("cancel"); v.State != "idle" || e.resumesOnRestart() {
		t.Fatalf("after cancelling with the server stopped: %+v", v)
	}
	e.serverOp("/start")
	if running, task := fc.state(); running || task.cancelled {
		t.Fatalf("Chunky runs %v with %+v", running, task)
	}
	if v := e.pregen(); v.State != "idle" {
		t.Fatalf("after starting the server: %+v", v)
	}
	confirms := e.rcon.count("chunky confirm")
	e.startPregen("small", true)
	if running, _ := fc.state(); !running || e.rcon.count("chunky confirm") != confirms+1 {
		t.Fatalf("the new task did not replace the one Chunky kept (running %v)", running)
	}

	fc.lose()
	s := e.srv()
	e.waitFor("Chunky to report no task", func() bool {
		s.pg.mu.Lock()
		defer s.pg.mu.Unlock()
		return !s.pg.idleSince.IsZero()
	})
	if v := e.pregen(); v.State != "paused" {
		t.Fatalf("a task Chunky just lost: %+v", v)
	}
	s.pg.mu.Lock()
	s.pg.idleSince = s.pg.idleSince.Add(-pregenIdleGrace)
	s.pg.mu.Unlock()
	e.waitFor("the lost task to end", func() bool { return e.pregen().State == "idle" })
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'pregen.cancelled' AND result = 'failed' AND detail = 'Chunky no longer has the task'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the lost task", n)
	}
}

// A task finishes when Chunky logs so, although it saved the task short of
// the area a moment before. A task cancelled from the console that close to
// its end, which Chunky never logs as finished, ends as cancelled.
func TestPregenFinishesWhenChunkyLogsIt(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()
	e.fd.addLog("[22:30:00 INFO]: [Chunky] Task finished for world. Processed: 16129 chunks (100.00%), Total time: 0:07:21")
	e.startPregen("small", true)
	fc.advance(15000)
	e.waitFor("progress", func() bool { return e.pregen().Chunks == 15000 })
	s := e.srv()
	waiting := func() bool {
		s.pg.mu.Lock()
		defer s.pg.mu.Unlock()
		return !s.pg.idleSince.IsZero()
	}

	// What Chunky 1.4.40 on Paper 26.2 saved and logged at the end of a
	// small task: the save came as the last 50 chunks were loading.
	fc.endAt(16079, 440900*time.Millisecond)
	e.waitFor("Chunky to report the saved task", waiting)
	if v := e.pregen(); v.State != "paused" {
		t.Fatalf("before Chunky logged the finish: %+v", v)
	}
	e.fd.addLog("[22:56:56 INFO]: [Chunky] Task finished for world. Processed: 16129 chunks (100.00%), Total time: 0:07:21")
	e.waitFor("the finished task", func() bool { return e.pregen().State == "finished" })
	v := e.pregen()
	if v.Chunks != 16129 || v.Total != 16129 || v.Percent != 100 || v.ElapsedSeconds != 441 || v.DiskBytes == nil || *v.DiskBytes != 8192 {
		t.Fatalf("finished: %+v", v)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'playkeeper' AND action = 'pregen.finished' AND detail = '16129 chunks in 441 seconds'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the finish", n)
	}

	e.startPregen("small", true)
	fc.advance(16079)
	e.waitFor("progress", func() bool { return e.pregen().Chunks == 16079 })
	fc.answer("chunky cancel world")
	fc.answer("chunky confirm")
	e.fd.addLog("[23:10:02 INFO]: [Chunky] Task stopped for world.")
	e.fd.addLog("[23:10:02 INFO]: [Chunky] Task cancelled for world.")
	e.waitFor("Chunky to report the cancelled task", waiting)
	if v := e.pregen(); v.State != "paused" {
		t.Fatalf("a task just cancelled from the console: %+v", v)
	}
	s.pg.mu.Lock()
	s.pg.idleSince = s.pg.idleSince.Add(-pregenIdleGrace)
	s.pg.mu.Unlock()
	e.waitFor("the cancelled task to end", func() bool { return e.pregen().State == "idle" })
	if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ? AND ended = 'cancelled' AND chunks = 16079`, e.sid); n != 1 {
		t.Error("the task cancelled from the console was not recorded as cancelled")
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'playkeeper' AND action = 'pregen.cancelled' AND result = 'succeeded' AND detail = 'cancelled from the console'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the cancel", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'pregen.finished'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for finishes", n)
	}
}

// Requests that can't work are refused with a reason, and a start that
// fails says why on the page.
func TestPregenRefusals(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()
	for _, c := range []struct {
		name, path string
		body       any
		code       int
		errCode    string
	}{
		{"unknown size", "/pregen/start", map[string]any{"preset": "gigantic", "actor": "admin"}, 400, api.CodeInvalid},
		{"no size", "/pregen/start", map[string]any{"actor": "admin"}, 400, api.CodeInvalid},
		{"no actor", "/pregen/start", map[string]any{"preset": "small"}, 400, api.CodeInvalid},
		{"own radius", "/pregen/start", map[string]any{"preset": "small", "radius": 99999, "actor": "admin"}, 400, api.CodeInvalid},
		{"pause without a task", "/pregen/pause", map[string]any{"actor": "admin"}, 409, pregen.CodeNotRunning},
		{"continue without a task", "/pregen/continue", map[string]any{"actor": "admin"}, 409, pregen.CodeNotRunning},
		{"cancel without a task", "/pregen/cancel", map[string]any{"actor": "admin"}, 409, pregen.CodeNotRunning},
		{"pause without an actor", "/pregen/pause", map[string]any{}, 400, api.CodeInvalid},
	} {
		code, out := e.call("POST", e.sp(c.path), c.body)
		if msg, _ := out["error"].(string); code != c.code || out["code"] != c.errCode || msg == "" {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, out, c.code, c.errCode)
		}
	}
	if code, _ := e.call("GET", "/v1/servers/zzzzzzzzzz/pregen", nil); code != 404 {
		t.Errorf("an unknown server's pre-generation: %d", code)
	}

	e.diskFree.Store(2 << 30)
	fits := map[string]bool{}
	for _, p := range e.pregen().Presets {
		fits[p.ID] = p.Fits
	}
	if !maps.Equal(fits, map[string]bool{"small": true, "medium": false, "large": false, "huge": false}) {
		t.Errorf("sizes that fit in 2 GiB: %v", fits)
	}
	if code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": "medium", "actor": "admin"}); code != 409 || out["code"] != pregen.CodeNotEnoughDisk {
		t.Errorf("a size that doesn't fit: %d %v", code, out)
	}
	e.diskFree.Store(0)

	fc.setBroken(true)
	code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": "small", "actor": "admin"})
	if code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	want := "Chunky did not load when " + e.srv().name() + " started."
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed || op.Error != want {
		t.Fatalf("starting with a Chunky that doesn't load: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	if v := e.pregen(); v.State != "idle" || v.Error != want || !v.Installed {
		t.Fatalf("after the failed start: %+v", v)
	}

	fc.setBroken(false)
	e.startPregen("small", false)
	if v := e.pregen(); v.State != "running" || v.Error != "" {
		t.Fatalf("after starting again: %+v", v)
	}
	if code, out := e.call("POST", e.sp("/pregen/start"), map[string]any{"preset": "small", "actor": "admin"}); code != 409 || out["code"] != pregen.CodeAlreadyRunning {
		t.Errorf("a second start: %d %v", code, out)
	}
}

// A restore brings back a world without the task: Chunky in it doesn't
// resume what the backup saved, and the agent forgets the task. Deleting
// the server forgets it too. Chunky saves its task when the server stops,
// so the backup is made with the server stopped.
func TestPregenIsForgottenWithTheWorld(t *testing.T) {
	e := newAgentEnv(t)
	e.withSources()
	e.create()
	fc := e.chunky()
	e.startPregen("small", true)
	fc.advance(2000)
	id, phrase := e.backupWithAndStage(map[string]any{"actor": "admin", "stopped": true})
	if running, _ := fc.state(); !running {
		t.Fatal("the task did not resume after the backup")
	}

	if op := e.applyRestore(id, phrase); op.Status != api.OpSucceeded {
		t.Fatalf("restore: %+v", op)
	}
	e.waitFor("online after the restore", e.onlineIdle)
	if running, task := fc.state(); running || task.chunks != 2000 || task.cancelled {
		t.Fatalf("in the restored world Chunky runs %v with %+v", running, task)
	}
	if e.resumesOnRestart() {
		t.Error("Chunky in the restored world will resume the backup's task")
	}
	if v := e.pregen(); v.State != "idle" || e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ?`, e.sid) != 0 {
		t.Fatalf("after the restore: %+v", v)
	}

	e.startPregen("small", true)
	code, out := e.call("POST", e.sp("/delete"), map[string]any{"confirm": e.srv().name(), "actor": "admin"})
	if code != 202 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded {
		t.Fatalf("delete: %+v", op)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ?`, e.sid); n != 0 {
		t.Fatal("the deleted server's task is still recorded")
	}
}
