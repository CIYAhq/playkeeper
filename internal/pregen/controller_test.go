package pregen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Replies as Paper's remote console returns them: one line per message,
// usage lines colored. Fabric and NeoForge replies run messages together.
const (
	rNoTasks  = "[Chunky] No tasks running.\n"
	rReloaded = "[Chunky] Successfully reloaded configuration.\n"
	rPattern  = "[Chunky] Pattern changed to region.\n"
	rConfirm  = "[Chunky] A task was already started for this world. To continue running it, type '/chunky continue'. To start a new task, type '/chunky confirm'.\n"
	rUnknown  = "Unknown or incomplete command. See below for error\nchunky progress<--[HERE]"
)

type step struct {
	cmd, reply string
	err        error
}

// script is a console that expects exactly the given commands, in order.
type script struct {
	t     *testing.T
	steps []step
}

func (s *script) Command(_ context.Context, cmd string) (string, error) {
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected command %q", cmd)
	}
	st := s.steps[0]
	s.steps = s.steps[1:]
	if cmd != st.cmd {
		s.t.Fatalf("sent %q, want %q", cmd, st.cmd)
	}
	return st.reply, st.err
}

func newController(t *testing.T, p Platform, steps ...step) *Controller {
	t.Helper()
	s := &script{t: t, steps: steps}
	t.Cleanup(func() {
		if len(s.steps) > 0 && !t.Failed() {
			t.Errorf("commands never sent: %+v", s.steps)
		}
	})
	return &Controller{Console: s, Platform: p, DataDir: t.TempDir()}
}

// prelude is what Start sends before the start command.
func prelude(steps ...step) []step {
	return append([]step{{"chunky progress", rNoTasks, nil}, {"chunky reload", rReloaded, nil}, {"chunky pattern region", rPattern, nil}}, steps...)
}

func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("error = %v (%#v), want code %s", err, err, code)
	}
	if e.Msg == "" {
		t.Errorf("error %s has no message", code)
	}
	return e
}

func TestStart(t *testing.T) {
	ctx := context.Background()
	c := newController(t, Bukkit, prelude(
		step{"chunky start world square 100 -200 2500", "[Chunky] Task started in world for the square region centered at 100, -200 with radius 2500.\n", nil},
	)...)
	got, err := c.Start(ctx, Plan{World: "world", CenterX: 100, CenterZ: -200, Radius: 2500, Shape: Square}, StartOptions{ContinueOnRestart: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Started{World: "world", Shape: Square, CenterX: 100, CenterZ: -200, Radius: 2500}); got != want {
		t.Errorf("Start = %+v, want %+v", got, want)
	}
	cfg, err := os.ReadFile(filepath.Join(c.DataDir, "plugins/Chunky/config.yml"))
	if err != nil || !strings.Contains(string(cfg), "continue-on-restart: true\n") || !strings.Contains(string(cfg), "language: en\n") {
		t.Errorf("config.yml = %q, %v", cfg, err)
	}
}

func TestStartOnSpawn(t *testing.T) {
	c := newController(t, Fabric,
		step{"chunky progress", "[Chunky] No tasks running.", nil},
		step{"chunky reload", "[Chunky] Successfully reloaded configuration.", nil},
		step{"chunky pattern region", "[Chunky] Pattern changed to region.", nil},
		step{"chunky world minecraft:overworld", "[Chunky] World changed to minecraft:overworld.", nil},
		step{"chunky spawn", "[Chunky] Center changed to -120.5, 64.", nil},
		step{"chunky start minecraft:overworld circle -121 64 1000", "[Chunky] Task started in minecraft:overworld for the circle region centered at -121, 64 with radius 1000.", nil},
	)
	got, err := c.Start(context.Background(), Plan{World: "minecraft:overworld", CenterOnSpawn: true, Radius: 1000, Shape: Circle}, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CenterX != -121 || got.CenterZ != 64 || got.Shape != Circle {
		t.Errorf("Start = %+v", got)
	}
	cfg, err := os.ReadFile(filepath.Join(c.DataDir, "config/chunky/config.json"))
	if err != nil || !strings.Contains(string(cfg), `"continueOnRestart": false`) {
		t.Errorf("config.json = %q, %v", cfg, err)
	}
}

func TestStartSavedTask(t *testing.T) {
	plan := Plan{World: "world", Radius: 1000, Shape: Square}
	c := newController(t, Bukkit, prelude(step{"chunky start world square 0 0 1000", rConfirm, nil})...)
	_, err := c.Start(context.Background(), plan, StartOptions{})
	if e := wantCode(t, err, CodeSavedTask); e.Params["world"] != "world" || !errors.Is(err, ErrSavedTask) {
		t.Errorf("saved task error = %+v", e)
	}

	c = newController(t, Bukkit, prelude(
		step{"chunky start world square 0 0 1000", rConfirm, nil},
		step{"chunky confirm", "[Chunky] Task started in world for the square region centered at 0, 0 with radius 1000.\n", nil},
	)...)
	if _, err := c.Start(context.Background(), plan, StartOptions{Replace: true}); err != nil {
		t.Errorf("Start with Replace = %v", err)
	}

	c = newController(t, Bukkit, prelude(
		step{"chunky start world square 0 0 1000", rConfirm, nil},
		step{"chunky confirm", "[Chunky] Nothing to confirm!\n", nil},
	)...)
	_, err = c.Start(context.Background(), plan, StartOptions{Replace: true})
	if e := wantCode(t, err, CodeUnexpectedReply); e.Params["command"] != "chunky confirm" {
		t.Errorf("expired confirmation error = %+v", e)
	}
}

func TestStartRefused(t *testing.T) {
	plan := Plan{World: "world_the_end", Radius: 20_000, Shape: Square}
	start := "chunky start world_the_end square 0 0 20000"
	cases := []struct {
		name, reply, code string
	}{
		{"already running", "[Chunky] Task already started for world_the_end!\n", CodeAlreadyRunning},
		{"radius limit", "[Chunky] Your host has limited the maximum pre-generation radius to 5000 to avoid excessive disk space usage. Reduce the radius or contact your host if you wish to have this limit removed.\n", CodeRadiusLimit},
		{"unknown world", "§2chunky start§r - Start a new chunk generation task\n", CodeUnknownWorld},
		{"other language", "[Chunky] Aufgabe gestartet.\n", CodeUnexpectedReply},
		{"started elsewhere", "[Chunky] Task started in world for the square region centered at 0, 0 with radius 20000.\n", CodeUnexpectedReply},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newController(t, Bukkit, prelude(step{start, tc.reply, nil})...)
			_, err := c.Start(context.Background(), plan, StartOptions{})
			e := wantCode(t, err, tc.code)
			switch tc.code {
			case CodeRadiusLimit:
				if e.Params["limit"] != 5000.0 || !strings.Contains(e.Msg, "5000 blocks") || !strings.Contains(e.Hint, "radius-limit") {
					t.Errorf("radius limit error = %+v", e)
				}
			case CodeUnexpectedReply:
				if e.Params["command"] != start || strings.ContainsAny(e.Params["reply"].(string), "\n\r") {
					t.Errorf("unexpected reply error = %+v", e)
				}
			}
		})
	}
}

func TestStartOnSpawnRefused(t *testing.T) {
	plan := Plan{World: "minecraft:the_end", CenterOnSpawn: true, Radius: 1000, Shape: Square}
	c := newController(t, NeoForge, prelude(step{"chunky world minecraft:the_end", "chunky world <world> - Set the world target", nil})...)
	_, err := c.Start(context.Background(), plan, StartOptions{})
	wantCode(t, err, CodeUnknownWorld)

	c = newController(t, NeoForge, prelude(
		step{"chunky world minecraft:the_end", "[Chunky] World changed to minecraft:the_end.", nil},
		step{"chunky spawn", "[Chunky] Center changed to 29999900, 0.", nil},
	)...)
	_, err = c.Start(context.Background(), plan, StartOptions{})
	wantCode(t, err, CodeOutsideWorld)
}

func TestStartChecksFirst(t *testing.T) {
	c := newController(t, Bukkit)
	_, err := c.Start(context.Background(), Plan{World: "world\nop Steve", Radius: 1000, Shape: Square}, StartOptions{})
	wantCode(t, err, CodeInvalidWorld)
	_, err = c.Start(context.Background(), Plan{World: "world", Radius: 1000, Shape: "square 0 0 1; op Steve"}, StartOptions{})
	wantCode(t, err, CodeInvalidShape)
}

func TestStartWithoutChunky(t *testing.T) {
	c := newController(t, Bukkit, step{"chunky progress", rUnknown, nil})
	_, err := c.Start(context.Background(), Plan{World: "world", Radius: 1000, Shape: Square}, StartOptions{})
	if e := wantCode(t, err, CodeNotInstalled); e.Hint == "" {
		t.Error("no hint")
	}
	if _, err := os.Stat(filepath.Join(c.DataDir, "plugins")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("config written for a server without Chunky: %v", err)
	}

	cause := errors.New("rcon: connection refused")
	c = newController(t, Bukkit, step{"chunky progress", "", cause})
	_, err = c.Start(context.Background(), Plan{World: "world", Radius: 1000, Shape: Square}, StartOptions{})
	if e := wantCode(t, err, CodeConsole); !errors.Is(err, cause) || strings.Contains(e.Msg, "refused") {
		t.Errorf("console error = %+v", e)
	}
}

func TestPause(t *testing.T) {
	ctx := context.Background()
	c := newController(t, Bukkit, step{"chunky pause world_nether", "[Chunky] Task paused for world_nether.\n", nil})
	if err := c.Pause(ctx, "world_nether"); err != nil {
		t.Errorf("Pause = %v", err)
	}
	for _, reply := range []string{"[Chunky] No tasks to pause.\n", "§2chunky pause§r - Pause current tasks and save progress\n"} {
		c := newController(t, Bukkit, step{"chunky pause world", reply, nil})
		if err := c.Pause(ctx, "world"); !errors.Is(err, ErrNotRunning) {
			t.Errorf("Pause with reply %q = %v, want %s", reply, err, CodeNotRunning)
		}
	}
	c = newController(t, Bukkit)
	wantCode(t, c.Pause(ctx, "world stop"), CodeInvalidWorld)
}

func TestContinue(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		reply string
		want  error
	}{
		{"[Chunky] Task continuing for minecraft:overworld.", nil},
		{"[Chunky] Task already started for minecraft:overworld!", nil},
		{"[Chunky] No tasks to continue.", ErrNothingToContinue},
		{"chunky continue - Continue current or saved tasks", ErrUnknownWorld},
		{"[Chunky] Task continuing for minecraft:the_nether.", ErrUnexpectedReply},
	}
	for _, tc := range cases {
		c := newController(t, Fabric, step{"chunky continue minecraft:overworld", tc.reply, nil})
		err := c.Continue(ctx, "minecraft:overworld")
		if (tc.want == nil) != (err == nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("Continue with reply %q = %v, want %v", tc.reply, err, tc.want)
		}
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	c := newController(t, Bukkit,
		step{"chunky cancel world", "[Chunky] Cancelled tasks cannot be continued. If you are sure you want to cancel, type '/chunky confirm'.\n", nil},
		step{"chunky confirm", "[Chunky] Task cancelled for world.\n", nil},
	)
	if err := c.Cancel(ctx, "world"); err != nil {
		t.Errorf("Cancel = %v", err)
	}
	c = newController(t, Bukkit, step{"chunky cancel world", "[Chunky] No tasks to cancel.\n", nil})
	if err := c.Cancel(ctx, "world"); err != nil {
		t.Errorf("Cancel without tasks = %v", err)
	}
	c = newController(t, Bukkit, step{"chunky cancel world_the_end", "§2chunky cancel§r - Stop and delete current or saved tasks\n", nil})
	if err := c.Cancel(ctx, "world_the_end"); !errors.Is(err, ErrUnknownWorld) {
		t.Errorf("Cancel of an unknown world = %v", err)
	}
	c = newController(t, Bukkit,
		step{"chunky cancel world", "[Chunky] Cancelled tasks cannot be continued. If you are sure you want to cancel, type '/chunky confirm'.\n", nil},
		step{"chunky confirm", "[Chunky] Nothing to confirm!\n", nil},
	)
	if err := c.Cancel(ctx, "world"); !errors.Is(err, ErrUnexpectedReply) {
		t.Errorf("Cancel with an expired confirmation = %v", err)
	}
}

func TestProgress(t *testing.T) {
	ctx := context.Background()
	c := newController(t, Bukkit, step{"chunky progress", rNoTasks, nil})
	got, err := c.Progress(ctx)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Progress without tasks = %v, %v", got, err)
	}
	c = newController(t, Fabric, step{"chunky progress", "[Chunky] Task running for minecraft:overworld. Processed: 4410 chunks (4.44%), ETA: 0:40:55, Rate: 38.6 cps, Current: -12, 30[Chunky] Task running for minecraft:the_nether. Processed: 120 chunks (0.12%), ETA: 1:02:03, Rate: 26.1 cps, Current: 2, -5", nil})
	got, err = c.Progress(ctx)
	if err != nil || len(got) != 2 || got[0].World != "minecraft:overworld" || got[1].Chunks != 120 {
		t.Errorf("Progress = %+v, %v", got, err)
	}
	c = newController(t, Bukkit, step{"chunky progress", "[Chunky] Keine Aufgaben aktiv.\n", nil})
	if _, err := c.Progress(ctx); !errors.Is(err, ErrUnexpectedReply) {
		t.Errorf("Progress in another language = %v", err)
	}
}

func writeTask(t *testing.T, dataDir, rel, body string) {
	t.Helper()
	p := filepath.Join(dataDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatus(t *testing.T) {
	ctx := context.Background()
	running := "[Chunky] Task running for world. Processed: 1313 chunks (20.01%), ETA: 0:02:13, Rate: 39.4 cps, Current: 32, -1\n"
	cases := []struct {
		name, reply, task string
		want              State
	}{
		{"idle", rNoTasks, "", StateIdle},
		{"running", running, "", StateRunning},
		{"running after a pause", running, "world=world\ncancelled=false\ncenter-x=0.0\ncenter-z=0.0\nradius=640.0\nshape=square\npattern=region\nchunks=600\ntime=15000\n", StateRunning},
		{"paused", rNoTasks, "world=world\ncancelled=false\ncenter-x=0.0\ncenter-z=0.0\nradius=640.0\nshape=square\npattern=region\nchunks=1313\ntime=33000\n", StatePaused},
		{"finished", rNoTasks, "world=world\ncancelled=true\ncenter-x=0.0\ncenter-z=0.0\nradius=640.0\nshape=square\npattern=region\nchunks=6561\ntime=154000\n", StateFinished},
		{"cancelled", rNoTasks, "world=world\ncancelled=true\ncenter-x=0.0\ncenter-z=0.0\nradius=640.0\nshape=square\npattern=region\nchunks=1313\ntime=33000\n", StateCancelled},
		{"another world's task", rNoTasks, "world=world_nether\ncancelled=false\n", StateIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newController(t, Bukkit, step{"chunky progress", tc.reply, nil})
			if tc.task != "" {
				writeTask(t, c.DataDir, "plugins/Chunky/tasks/world.properties", tc.task)
			}
			st, err := c.Status(ctx, "world")
			if err != nil || st.State != tc.want || st.World != "world" {
				t.Fatalf("Status = %+v, %v; want %s", st, err, tc.want)
			}
			if (st.Progress != nil) != (tc.want == StateRunning) {
				t.Errorf("progress = %+v", st.Progress)
			}
			if tc.want == StateFinished && (st.Task == nil || st.Task.ElapsedSeconds != 154 || st.Task.Percent() != 100) {
				t.Errorf("finished task = %+v", st.Task)
			}
		})
	}

	c := newController(t, Fabric, step{"chunky progress", "[Chunky] No tasks running.", nil})
	writeTask(t, c.DataDir, "config/chunky/tasks/minecraft/the_nether.properties", "world=minecraft:the_nether\ncancelled=false\ncenter-x=12.5\ncenter-z=-40.0\nradius=2500.0\nshape=circle\npattern=region\nchunks=500\ntime=61234\n")
	st, err := c.Status(ctx, "minecraft:the_nether")
	if err != nil || st.State != StatePaused || st.Task == nil || st.Task.Total != 99_225 || st.Task.CenterX != 12.5 || st.Task.Shape != "circle" {
		t.Errorf("Fabric Status = %+v %+v, %v", st, st.Task, err)
	}
}

func TestConfigure(t *testing.T) {
	c := newController(t, NeoForge, step{"chunky reload", "[Chunky] Successfully reloaded configuration.", nil})
	if err := c.Configure(context.Background(), Config{ContinueOnRestart: true, UpdateInterval: 5}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(c.DataDir, "config/chunky/config.json"))
	if err != nil || !strings.Contains(string(b), `"updateInterval": 5`) || !strings.Contains(string(b), `"continueOnRestart": true`) {
		t.Errorf("config.json = %s, %v", b, err)
	}
}

func TestUnexpectedReplies(t *testing.T) {
	ctx := context.Background()
	spawnPlan := Plan{World: "world", CenterOnSpawn: true, Radius: 1000, Shape: Square}
	long := "[Chunky] " + strings.Repeat("é", 300) + "\x07\n"
	cases := []struct {
		name, command string
		steps         []step
		run           func(*Controller) error
	}{
		{"reload in another language", "chunky reload", []step{{"chunky reload", "[Chunky] Konfiguration erfolgreich neu geladen.\n", nil}},
			func(c *Controller) error { return c.Configure(ctx, Config{}) }},
		{"pattern refused", "chunky pattern region", []step{{"chunky progress", rNoTasks, nil}, {"chunky reload", rReloaded, nil}, {"chunky pattern region", "§2chunky pattern <pattern>§r - Set the generation pattern\n", nil}},
			func(c *Controller) error { _, err := c.Start(ctx, spawnPlan, StartOptions{}); return err }},
		{"other world selected", "chunky world", prelude(step{"chunky world world", "[Chunky] World changed to world_nether.\n", nil}),
			func(c *Controller) error { _, err := c.Start(ctx, spawnPlan, StartOptions{}); return err }},
		{"no spawn", "chunky spawn", prelude(step{"chunky world world", "[Chunky] World changed to world.\n", nil}, step{"chunky spawn", long, nil}),
			func(c *Controller) error { _, err := c.Start(ctx, spawnPlan, StartOptions{}); return err }},
		{"cancel", "chunky cancel", []step{{"chunky cancel world", "[Chunky] Task paused for world.\n", nil}},
			func(c *Controller) error { return c.Cancel(ctx, "world") }},
		{"pause of another world", "chunky pause", []step{{"chunky pause world", "[Chunky] Task paused for world_nether.\n", nil}},
			func(c *Controller) error { return c.Pause(ctx, "world") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newController(t, Bukkit, tc.steps...)
			e := wantCode(t, tc.run(c), CodeUnexpectedReply)
			reply := e.Params["reply"].(string)
			if e.Params["command"] != tc.command || len([]rune(reply)) > 201 || strings.ContainsFunc(reply, func(r rune) bool { return r < ' ' }) || e.Hint == "" {
				t.Errorf("error = %q, params %v", e.Msg, e.Params)
			}
		})
	}
}

// serialConsole fails when two commands overlap.
type serialConsole struct {
	inFlight atomic.Int32
	overlaps atomic.Int32
}

func (s *serialConsole) Command(context.Context, string) (string, error) {
	if s.inFlight.Add(1) > 1 {
		s.overlaps.Add(1)
	}
	for range 10 {
		runtime.Gosched()
	}
	s.inFlight.Add(-1)
	return "[Chunky] No tasks running.\n", nil
}

func TestControllerSerializes(t *testing.T) {
	con := &serialConsole{}
	c := &Controller{Console: con, Platform: Bukkit, DataDir: t.TempDir()}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 25 {
				if _, err := c.Progress(context.Background()); err != nil {
					t.Error(err)
				}
				c.Status(context.Background(), "world")
			}
		})
	}
	wg.Wait()
	if n := con.overlaps.Load(); n > 0 {
		t.Errorf("%d commands overlapped", n)
	}
}
