package agent

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/pregen"
	"github.com/CIYAhq/playkeeper/internal/store"
)

// chunkyJar puts Chunky among the server's plugins, as the Plugins tab
// would; fake Chunky (e.chunky) loads it when the server next starts.
func (e *agentEnv) chunkyJar() {
	e.t.Helper()
	dir := filepath.Join(e.dataDir(), "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	jar := jarOf("plugin.yml", "name: Chunky\nversion: 1.5.3\nmain: org.popcraft.chunky.ChunkyBukkit\n")
	if err := os.WriteFile(filepath.Join(dir, "Chunky-Bukkit-1.5.3.jar"), jar, 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// mapOn turns the map on and waits for squaremap's first render.
func (e *agentEnv) mapOn() {
	e.t.Helper()
	if op := e.mapOp("/map/enable", map[string]any{}); op.Status != api.OpSucceeded {
		e.t.Fatalf("enable: %+v", op)
	}
	e.waitFor("online", e.onlineIdle)
	e.waitFor("the first render", func() bool { return e.rcon.count("squaremap fullrender minecraft:overworld") == 1 })
}

func (e *agentEnv) mapArea() api.MapArea {
	e.t.Helper()
	var out api.MapArea
	e.decode("GET", e.sp("/map/area"), &out)
	return out
}

func (e *agentEnv) setMapArea(area string, pauseForPlayers bool) (int, map[string]any) {
	e.t.Helper()
	return e.call("POST", e.sp("/map/area"), map[string]any{"area": area, "pauseForPlayers": pauseForPlayers, "actor": "admin"})
}

// fillMapArea chooses a bigger area and waits until Chunky fills it in.
func (e *agentEnv) fillMapArea(area string, pauseForPlayers bool) {
	e.t.Helper()
	code, out := e.setMapArea(area, pauseForPlayers)
	if code != 202 {
		e.t.Fatalf("choose %s: %d %v", area, code, out)
	}
	if op := e.waitOp(out["id"].(string)); op.Status != api.OpSucceeded || op.Kind != "pregen-start" {
		e.t.Fatalf("filling in %s: %+v", area, op)
	}
}

// withBorder sets the world border: the server says how wide it is, and
// Chunky where it is.
func (e *agentEnv) withBorder(fc *fakeChunky, x, z, width int) {
	e.t.Helper()
	fc.mu.Lock()
	fc.borderX, fc.borderZ, fc.borderRadius = x, z, width/2
	fc.mu.Unlock()
	e.rcon.mu.Lock()
	chunky := e.rcon.answer
	e.rcon.answer = func(cmd string) (string, bool) {
		if cmd == "minecraft:worldborder get" {
			return "The world border is currently " + strconv.Itoa(width) + " block(s) wide", true
		}
		return chunky(cmd)
	}
	e.rcon.mu.Unlock()
}

func optionIDs(a api.MapArea, keep func(api.MapAreaOption) bool) []string {
	var out []string
	for _, o := range a.Options {
		if keep(o) {
			out = append(out, o.ID)
		}
	}
	return out
}

// A new map shows the explored land. A bigger area is pre-generated, and
// once it is done squaremap is asked to draw it; what was generated stays,
// so only a bigger area can be chosen after it.
func TestTheMapAreaFillsInABiggerAreaThatSquaremapThenDraws(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.mapOn()
	e.chunkyJar()
	fc := e.chunky()

	a := e.mapArea()
	radii := map[string]int{}
	for _, o := range a.Options {
		radii[o.ID] = o.Radius
		if !o.Fits || o.Done || o.PastBorder || o.Seconds <= 0 || o.DiskBytes <= 0 || o.Chunks <= 0 {
			t.Errorf("option %+v", o)
		}
	}
	if a.Area != api.MapAreaExplored || a.Radius != 0 || a.Fill.State != "idle" ||
		!maps.Equal(radii, map[string]int{"small": 1000, "medium": 2500, "large": 5000, "huge": 10000}) {
		t.Fatalf("a new map's area: %+v", a)
	}
	if e.rcon.count("minecraft:worldborder get") != 1 {
		t.Error("the world border was not looked at")
	}

	e.fillMapArea("medium", true)
	if running, task := fc.state(); !running || task.radius != 2500 || task.centerX != 16 || task.centerZ != -32 {
		t.Fatalf("Chunky runs %v: %+v", running, task)
	}
	if a := e.mapArea(); a.Area != "medium" || a.Radius != 2500 || a.Fill.State != "running" || !a.Fill.PauseForPlayers {
		t.Fatalf("while filling in: %+v", a)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'admin' AND action = 'map.area' AND detail = 'medium: 2500 blocks around spawn'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the choice", n)
	}

	fc.finish(20 * time.Minute)
	e.waitFor("squaremap to draw the new land", func() bool { return e.rcon.count("squaremap fullrender minecraft:overworld") == 2 })
	a = e.mapArea()
	if a.Area != "medium" || a.Fill.State != "finished" ||
		!slices.Equal(optionIDs(a, func(o api.MapAreaOption) bool { return o.Done }), []string{"small", "medium"}) {
		t.Fatalf("once filled in: %+v", a)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM events WHERE kind = 'map_drawing' AND detail = 'squaremap draws the pre-generated land'`); n != 1 {
		t.Errorf("%d events for drawing the new land", n)
	}

	for _, area := range []string{api.MapAreaExplored, "small", "medium"} {
		if code, out := e.setMapArea(area, true); code != 409 || out["code"] != api.CodeAreaOnMap {
			t.Errorf("choosing %s once medium is done: %d %v", area, code, out)
		}
	}
	e.fillMapArea("large", false)
	if running, task := fc.state(); !running || task.radius != 5000 {
		t.Fatalf("going further, Chunky runs %v: %+v", running, task)
	}
}

// Explored only stops the area being filled in, and a different area
// replaces it; the same area again is refused.
func TestTheMapAreaStopsOrReplacesTheOneBeingFilledIn(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.mapOn()
	e.chunkyJar()
	fc := e.chunky()

	e.fillMapArea("large", true)
	fc.advance(3000)
	e.fillMapArea("small", false)
	if running, task := fc.state(); !running || task.radius != 1000 {
		t.Fatalf("after choosing small, Chunky runs %v: %+v", running, task)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND actor = 'admin' AND action = 'pregen.cancelled' AND detail = 'replaced by small'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for replacing large", n)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ? AND preset = 'small' AND radius = 1000 AND pause_for_players = 0 AND ended = ''`, e.sid); n != 1 {
		t.Error("small was not recorded as the area being filled in")
	}
	if code, out := e.setMapArea("small", false); code != 409 || out["code"] != pregen.CodeAlreadyRunning {
		t.Errorf("small again: %d %v", code, out)
	}

	code, out := e.setMapArea(api.MapAreaExplored, true)
	if code != 200 || out["area"] != api.MapAreaExplored {
		t.Fatalf("explored only: %d %v", code, out)
	}
	if running, task := fc.state(); running || !task.cancelled {
		t.Fatalf("after explored only, Chunky runs %v: %+v", running, task)
	}
	if a := e.mapArea(); a.Area != api.MapAreaExplored || a.Fill.State != "idle" {
		t.Fatalf("after explored only: %+v", a)
	}
	if code, _ := e.setMapArea(api.MapAreaExplored, true); code != 200 {
		t.Errorf("explored only again: %d", code)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'map.area' AND detail = 'explored only'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for explored only", n)
	}
}

// A different area takes over from the one being filled in only once
// Chunky has started it. While something else holds the server, or when
// Chunky won't start it, the old one carries on as it was: running, or
// paused and not resuming by itself.
func TestReplacingTheMapAreaKeepsTheOldOneUntilTheNewOneStarts(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.mapOn()
	e.chunkyJar()
	fc := e.chunky()
	e.fillMapArea("large", true)
	fc.advance(3000)
	kept := func(when string, running, pausedByUser bool) {
		t.Helper()
		if r, task := fc.state(); r != running || task.radius != 5000 || task.cancelled || task.chunks != 3000 {
			t.Errorf("%s, Chunky runs %v: %+v", when, r, task)
		}
		if a := e.mapArea(); a.Area != "large" || a.Radius != 5000 {
			t.Errorf("%s, the area: %+v", when, a)
		}
		if n := e.countRows(`SELECT COUNT(*) FROM pregen WHERE server_id = ? AND preset = 'large' AND ended = '' AND paused_by_user = ?`, e.sid, pausedByUser); n != 1 {
			t.Errorf("%s, large is no longer the area being filled in", when)
		}
		if e.resumesOnRestart() == pausedByUser {
			t.Errorf("%s, Chunky resumes after a restart: %v", when, !pausedByUser)
		}
		if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action IN ('pregen.cancelled', 'map.area') AND detail != 'large: 5000 blocks around spawn'`, e.sid); n != 0 {
			t.Errorf("%s, %d audit entries for replacing large", when, n)
		}
	}

	release, ok := e.srv().holdOpLock()
	if !ok {
		t.Fatal("could not hold the server")
	}
	if code, out := e.setMapArea("small", true); code != 409 {
		t.Errorf("replacing large while the server is busy: %d %v", code, out)
	}
	release()
	kept("after the server was busy", true, false)

	fc.mu.Lock()
	fc.limit = 500
	fc.mu.Unlock()
	refused := func() {
		t.Helper()
		code, out := e.setMapArea("small", true)
		if code != 202 {
			t.Fatalf("replacing large: %d %v", code, out)
		}
		if op := e.waitOp(out["id"].(string)); op.Status != api.OpFailed || op.Error != "Chunky on this server is limited to a radius of 500 blocks." {
			t.Fatalf("replacing large with an area Chunky won't start: %+v", op)
		}
	}
	refused()
	if e.rcon.count("chunky pause world") != 1 || e.rcon.count("chunky continue world") != 1 {
		t.Error("large was not paused for small, then continued")
	}
	kept("after Chunky refused small", true, false)

	e.pregenAct("pause")
	refused()
	if e.rcon.count("chunky continue world") != 1 {
		t.Error("large carried on though it was paused")
	}
	kept("after Chunky refused small in place of large paused", false, true)
}

// What a task finished stays done after a bigger one is stopped or
// replaced, or a smaller one finishes.
func TestTheLargestFinishedAreaStaysDone(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.mapOn()
	e.chunkyJar()
	fc := e.chunky()
	done := func(when string, want ...string) {
		t.Helper()
		if got := optionIDs(e.mapArea(), func(o api.MapAreaOption) bool { return o.Done }); !slices.Equal(got, want) {
			t.Errorf("done %s: %v, want %v", when, got, want)
		}
	}
	e.fillMapArea("small", true)
	fc.finish(5 * time.Minute)
	e.waitFor("small to be filled in", func() bool { return e.mapArea().Fill.State == "finished" })

	e.fillMapArea("medium", true)
	done("while medium is filled in", "small")
	fc.advance(1000)
	if code, out := e.setMapArea(api.MapAreaExplored, true); code != 200 {
		t.Fatalf("stopping medium: %d %v", code, out)
	}
	done("once medium was stopped", "small")
	if code, out := e.setMapArea("small", true); code != 409 || out["code"] != api.CodeAreaOnMap {
		t.Errorf("small once medium was stopped: %d %v", code, out)
	}

	e.fillMapArea("large", true)
	e.fillMapArea("medium", true)
	done("once large was replaced by medium", "small")
	fc.finish(10 * time.Minute)
	e.waitFor("medium to be filled in", func() bool { return e.mapArea().Fill.State == "finished" })
	done("once medium is filled in", "small", "medium")

	e.startPregen("small", true)
	fc.finish(time.Minute)
	e.waitFor("small to be filled in again", func() bool { return e.pregen().State == "finished" })
	done("once small is filled in again", "small", "medium")
}

// Pre-generations that finished before the map's area was recorded count
// as done.
func TestPregenerationsFinishedBeforeCountAsDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := store.Open(path, migrations[:len(migrations)-1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pregen(server_id, world, preset, radius, started_at, ended) VALUES
		('finished', 'world', 'medium', 2500, 1, 'finished'), ('cancelled', 'world', 'large', 5000, 1, 'cancelled'),
		('running', 'world', 'huge', 10000, 1, ''), ('border', 'world', 'border', 3000, 1, 'finished')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if db, err = store.Open(path, migrations); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got := map[string][2]int{}
	rows, err := db.Query(`SELECT server_id, done_radius, done_border FROM pregen`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r, b int
		if err := rows.Scan(&id, &r, &b); err != nil {
			t.Fatal(err)
		}
		got[id] = [2]int{r, b}
	}
	if want := map[string][2]int{"finished": {2500, 0}, "cancelled": {0, 0}, "running": {0, 0}, "border": {3000, 3000}}; !maps.Equal(got, want) {
		t.Errorf("done after the upgrade: %v, want %v", got, want)
	}
}

// With a world border set, the map can be filled up to it, where Chunky
// finds it when it starts; sizes past it aren't offered.
func TestTheMapAreaFillsUpToTheWorldBorder(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.mapOn()
	e.chunkyJar()
	fc := e.chunky()
	if code, out := e.setMapArea(api.MapAreaBorder, true); code != 409 || out["code"] != api.CodeNoBorder {
		t.Fatalf("the border of a world without one: %d %v", code, out)
	}

	e.srv().forgetMapLive()
	e.withBorder(fc, 100, -200, 6000)
	a := e.mapArea()
	last := a.Options[len(a.Options)-1]
	if last.ID != api.MapAreaBorder || last.Radius != 3000 || !last.Fits || last.PastBorder ||
		!slices.Equal(optionIDs(a, func(o api.MapAreaOption) bool { return o.PastBorder }), []string{"large", "huge"}) {
		t.Fatalf("with a border 6000 blocks wide: %+v", a.Options)
	}
	if code, out := e.setMapArea("large", true); code != 409 || out["code"] != api.CodePastBorder {
		t.Fatalf("a size past the border: %d %v", code, out)
	}

	// The border moved in since it was looked at.
	fc.mu.Lock()
	fc.borderRadius = 2500
	fc.mu.Unlock()
	e.fillMapArea(api.MapAreaBorder, true)
	if running, task := fc.state(); !running || task.centerX != 100 || task.centerZ != -200 || task.radius != 2500 || task.total != 99225 {
		t.Fatalf("filling up to the border, Chunky runs %v: %+v", running, task)
	}
	if a := e.mapArea(); a.Area != api.MapAreaBorder || a.Radius != 2500 || a.Fill.Total != 99225 {
		t.Fatalf("while filling up to the border: %+v", a)
	}
	if n := e.countRows(`SELECT COUNT(*) FROM audit WHERE server_id = ? AND action = 'pregen.started' AND detail = 'border: 2500 blocks around 100, -200'`, e.sid); n != 1 {
		t.Errorf("%d audit entries for the start", n)
	}
	fc.finish(time.Hour)
	e.waitFor("the border to be filled in", func() bool { return e.mapArea().Fill.State == "finished" })
	e.srv().forgetMapLive()
	e.withBorder(fc, 100, -200, 5000)
	if done := optionIDs(e.mapArea(), func(o api.MapAreaOption) bool { return o.Done }); !slices.Equal(done, []string{"small", "medium", "border"}) {
		t.Errorf("done once the border is filled in: %v", done)
	}
	e.srv().forgetMapLive()
	e.withBorder(fc, 100, -200, 7000)
	if done := optionIDs(e.mapArea(), func(o api.MapAreaOption) bool { return o.Done }); !slices.Equal(done, []string{"small", "medium"}) {
		t.Errorf("done once the border moved out: %v", done)
	}
}

// A pre-generation started on the World tab is the map's area too, and
// squaremap draws it once it is done, but only while the map is on.
func TestPreGeneratingOnTheWorldTabGrowsTheMap(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.chunkyJar()
	fc := e.chunky()
	e.startPregen("small", true)
	fc.finish(5 * time.Minute)
	e.waitFor("the finished task", func() bool { return e.pregen().State == "finished" })
	time.Sleep(200 * time.Millisecond)
	if n := e.rcon.count("squaremap fullrender minecraft:overworld"); n != 0 {
		t.Fatalf("squaremap was asked to draw %d times while the map is off", n)
	}

	e.mapOn()
	if a := e.mapArea(); a.Area != "small" || a.Radius != 1000 || a.Fill.State != "finished" {
		t.Fatalf("the World tab's area: %+v", a)
	}
	e.startPregen("medium", true)
	fc.finish(10 * time.Minute)
	e.waitFor("squaremap to draw the new land", func() bool { return e.rcon.count("squaremap fullrender minecraft:overworld") == 2 })
}

// Choices that can't be made are refused before anything changes.
func TestMapAreaRefusals(t *testing.T) {
	e, _, _ := newMapEnv(t)
	e.create()
	e.chunkyJar()
	fc := e.chunky()
	if code, out := e.setMapArea("medium", true); code != 409 || out["error"] != "Turn on the map first." {
		t.Fatalf("with the map off: %d %v", code, out)
	}
	e.mapOn()
	for _, c := range []struct {
		name string
		body any
	}{
		{"unknown area", map[string]any{"area": "gigantic", "actor": "admin"}},
		{"no area", map[string]any{"actor": "admin"}},
		{"no actor", map[string]any{"area": "small"}},
		{"own radius", map[string]any{"area": "small", "radius": 99999, "actor": "admin"}},
	} {
		if code, out := e.call("POST", e.sp("/map/area"), c.body); code != 400 || out["code"] != api.CodeInvalid {
			t.Errorf("%s: %d %v", c.name, code, out)
		}
	}

	e.diskFree.Store(2 << 30)
	fits := optionIDs(e.mapArea(), func(o api.MapAreaOption) bool { return o.Fits })
	if !slices.Equal(fits, []string{"small"}) {
		t.Errorf("areas that fit in 2 GiB: %v", fits)
	}
	if code, out := e.setMapArea("medium", true); code != 409 || out["code"] != pregen.CodeNotEnoughDisk {
		t.Errorf("an area that doesn't fit: %d %v", code, out)
	}
	e.diskFree.Store(0)
	if n := e.countRows(`SELECT COUNT(*) FROM pregen`) + e.countRows(`SELECT COUNT(*) FROM operations WHERE kind = 'pregen-start'`); n != 0 {
		t.Errorf("refused choices started %d things", n)
	}

	e.fillMapArea("large", true)
	e.diskFree.Store(2 << 30)
	if code, out := e.setMapArea("medium", true); code != 409 || out["code"] != pregen.CodeNotEnoughDisk {
		t.Errorf("replacing large with an area that doesn't fit: %d %v", code, out)
	}
	if running, task := fc.state(); !running || task.radius != 5000 {
		t.Errorf("a refused replacement stopped large: running %v, %+v", running, task)
	}
}
