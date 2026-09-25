package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/addons"
	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/pregen"
)

// Pre-generating a server's map with Chunky. The pregen package drives Chunky
// through the server console; the pregen table keeps the task Playkeeper
// started last, and pregenLoop follows it: it records when the task finishes
// and pauses it while people play.

const (
	// pregenUpdateInterval is how often Chunky logs its progress, in
	// seconds; its default of every second floods the console.
	pregenUpdateInterval = 60
	// pregenIdleGrace is how long Chunky may report no task at all for an
	// unfinished one before Playkeeper takes it as gone.
	pregenIdleGrace      = time.Minute
	pregenCommandTimeout = 30 * time.Second

	pregenFinished  = "finished"
	pregenCancelled = "cancelled"
)

// errChunkyConsole matches a Chunky command that never reached the console.
var errChunkyConsole = &pregen.Error{Code: pregen.CodeConsole}

// pregenTask is the task Playkeeper started last on a server.
type pregenTask struct {
	World           string
	Preset          string
	Radius          int
	PauseForPlayers bool
	PausedByUser    bool
	PausedByPolicy  bool
	PausedFor       string
	StartedAt       time.Time
	// Ended is "" while the task is unfinished.
	Ended       string
	EndedAt     *time.Time
	BytesBefore sql.NullInt64
	BytesAfter  sql.NullInt64
	Chunks      int64
	Total       int64
	Elapsed     int64
	Rate        float64
}

func (t *pregenTask) unfinished() bool { return t != nil && t.Ended == "" }

const pregenColumns = `world, preset, radius, pause_for_players, paused_by_user, paused_by_policy, paused_for,
	started_at, ended, ended_at, world_bytes_before, world_bytes_after, chunks, total, elapsed_secs, rate`

// lastPregen is the task Playkeeper started last on the server, or nil.
func (s *server) lastPregen() (*pregenTask, error) {
	var t pregenTask
	var started int64
	var ended sql.NullInt64
	err := s.db.QueryRow(`SELECT `+pregenColumns+` FROM pregen WHERE server_id = ?`, s.id).Scan(&t.World, &t.Preset, &t.Radius,
		&t.PauseForPlayers, &t.PausedByUser, &t.PausedByPolicy, &t.PausedFor, &started, &t.Ended, &ended, &t.BytesBefore, &t.BytesAfter,
		&t.Chunks, &t.Total, &t.Elapsed, &t.Rate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.StartedAt = time.UnixMilli(started).UTC()
	if ended.Valid {
		e := time.UnixMilli(ended.Int64).UTC()
		t.EndedAt = &e
	}
	return &t, nil
}

func (s *server) updatePregen(set string, args ...any) error {
	_, err := s.db.Exec(`UPDATE pregen SET `+set+` WHERE server_id = ?`, append(args, s.id)...)
	return err
}

// endPregen records that the task finished or was cancelled, with what
// Chunky saved of it.
func (s *server) endPregen(t *pregenTask, how string, saved *pregen.Task, bytesAfter any) error {
	chunks, elapsed, rate := t.Chunks, t.Elapsed, t.Rate
	if saved != nil {
		chunks, elapsed = saved.Chunks, saved.ElapsedSeconds
	}
	if chunks > 0 && elapsed > 0 {
		rate = float64(chunks) / float64(elapsed)
	}
	err := s.updatePregen(`ended = ?, ended_at = ?, chunks = ?, elapsed_secs = ?, rate = ?, world_bytes_after = ?,
		paused_by_user = 0, paused_by_policy = 0, paused_for = ''`, how, s.now().UnixMilli(), chunks, elapsed, rate, bytesAfter)
	s.pg.reset()
	return err
}

// pregenCache is what Chunky reported last for the server's unfinished
// task, so showing it sends no console command, and what the pausing
// policy remembers between checks.
type pregenCache struct {
	// run serializes asking Chunky about the task with acting on it.
	run sync.Mutex

	mu         sync.Mutex
	ctrl       *pregen.Controller
	status     *pregen.Status
	at         time.Time
	idleSince  time.Time
	emptySince time.Time
	// override is set when someone continues the task while people play,
	// so the policy leaves it running until the server is empty.
	override bool
}

// forget drops Chunky's last report.
func (c *pregenCache) forget() {
	c.mu.Lock()
	c.status, c.at, c.idleSince = nil, time.Time{}, time.Time{}
	c.mu.Unlock()
}

// reset forgets everything about the previous task.
func (c *pregenCache) reset() {
	c.mu.Lock()
	c.status, c.at, c.idleSince, c.override = nil, time.Time{}, time.Time{}, false
	c.mu.Unlock()
}

func (c *pregenCache) latest(now time.Time, fresh time.Duration) *pregen.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status == nil || now.Sub(c.at) >= fresh {
		return nil
	}
	st := *c.status
	return &st
}

// pregenFresh is how long a report from Chunky stands.
func (s *server) pregenFresh() time.Duration { return 3*s.opts.PregenInterval + 5*time.Second }

// chunky is the server's Chunky controller. There is one per server: Chunky
// keeps one selection and one pending confirmation per console.
func (s *server) chunky(p pregen.Platform) *pregen.Controller {
	s.pg.mu.Lock()
	defer s.pg.mu.Unlock()
	if s.pg.ctrl == nil || s.pg.ctrl.Platform != p {
		s.pg.ctrl = &pregen.Controller{Console: rconConsole{s}, Platform: p, DataDir: s.dataDir(), Owner: s.pregenOwner()}
	}
	return s.pg.ctrl
}

func (s *server) pregenOwner() *pregen.Owner {
	if o := s.gameOwner(); o != nil {
		return &pregen.Owner{UID: o.UID, GID: o.GID}
	}
	return nil
}

// pregenContext is what every pre-generation request needs: the server's
// settings, the platform Chunky runs on, and the world to pre-generate, the
// overworld.
func (s *server) pregenContext() (*api.ServerConfig, pregen.Platform, string, error) {
	sc, err := s.serverConfig()
	if err != nil {
		return nil, "", "", err
	}
	if sc == nil {
		return nil, "", "", errNotCreated()
	}
	p, err := pregen.PlatformFor(s.serverType(sc))
	if err != nil {
		return nil, "", "", pregenError(err)
	}
	return sc, p, pregen.Worlds(p, s.levelName(*sc))[0].Name, nil
}

// pregenError maps a pregen error to an HTTP response, keeping its code.
func pregenError(err error) error {
	var e *pregen.Error
	if !errors.As(err, &e) {
		return err
	}
	status := http.StatusConflict
	switch e.Code {
	case pregen.CodeInvalidWorld, pregen.CodeInvalidShape, pregen.CodeRadiusTooSmall, pregen.CodeRadiusTooLarge, pregen.CodeOutsideWorld:
		status = http.StatusBadRequest
	case pregen.CodeConsole, pregen.CodeUnexpectedReply:
		status = http.StatusBadGateway
	case pregen.CodeConfig:
		status = http.StatusInternalServerError
	}
	return &apiError{Status: status, Code: e.Code, Msg: e.Msg, Hint: e.Hint}
}

// Reading.

func (s *server) hPregen(w http.ResponseWriter, r *http.Request) {
	out, err := s.pregenView(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pregenView is where pre-generating the map stands, with the sizes to
// choose from and what each would take on this machine.
func (s *server) pregenView(ctx context.Context) (api.Pregen, error) {
	sc, p, world, err := s.pregenContext()
	if err != nil {
		return api.Pregen{}, err
	}
	task, err := s.lastPregen()
	if err != nil {
		return api.Pregen{}, err
	}
	online := s.online(ctx)
	if task.unfinished() && online && s.pg.latest(s.now(), s.pregenFresh()) == nil {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		s.pregenCheck(cctx, p)
		cancel()
		if task, err = s.lastPregen(); err != nil {
			return api.Pregen{}, err
		}
	}
	out := api.Pregen{State: "idle", World: world, ETASeconds: -1, PauseForPlayers: true}
	_, derr := pregen.Detect(s.dataDir(), p)
	out.Installed = derr == nil
	free := int64(-1)
	if f, _, err := s.opts.DiskUsage(s.dataDir()); err == nil {
		free = f
		out.DiskFreeBytes = &f
	}
	rate := 0.0
	if task != nil {
		rate, out.PauseForPlayers = task.Rate, task.PauseForPlayers
	}
	out.Presets = pregenPresets(p, s.levelName(*sc), world, rate, free)
	if op := s.currentOp(); op != nil && op.Kind == "pregen-start" {
		out.State = "starting"
		out.Preset, _ = op.Detail["preset"].(string)
		out.Radius = detailInt(op.Detail["radius"])
		out.Step, _ = op.Detail["step"].(string)
		if v, ok := op.Detail["pauseForPlayers"].(bool); ok {
			out.PauseForPlayers = v
		}
		return out, nil
	}
	if task == nil || task.Ended == pregenCancelled {
		if last := s.lastFinishedOperation(); last != nil && last.Kind == "pregen-start" && last.Status == api.OpFailed {
			out.Error = last.Error
		}
		return out, nil
	}
	started := task.StartedAt
	out.Preset, out.Radius, out.StartedAt, out.Total, out.Chunks = task.Preset, task.Radius, &started, task.Total, task.Chunks
	if task.Ended == pregenFinished {
		out.State, out.Percent, out.ElapsedSeconds, out.FinishedAt = "finished", 100, task.Elapsed, task.EndedAt
		if task.BytesBefore.Valid && task.BytesAfter.Valid {
			grew := max(task.BytesAfter.Int64-task.BytesBefore.Int64, 0)
			out.DiskBytes = &grew
		}
		return out, nil
	}
	if st := s.pg.latest(s.now(), s.pregenFresh()); online && st != nil && st.State == pregen.StateRunning && st.Progress != nil {
		out.State = "running"
		out.Chunks, out.Percent, out.Rate, out.ETASeconds = st.Progress.Chunks, st.Progress.Percent, st.Progress.Rate, st.Progress.ETASeconds
		return out, nil
	}
	out.State = "paused"
	if saved, found, err := pregen.ReadTask(s.dataDir(), p, task.World); err == nil && found && !saved.Cancelled {
		out.Chunks, out.Percent, out.ElapsedSeconds = saved.Chunks, saved.Percent(), saved.ElapsedSeconds
	}
	switch {
	case task.PausedByUser:
		out.PausedBy = "user"
	case !online:
		out.PausedBy = "server"
	case task.PausedByPolicy:
		out.PausedBy, out.PausedFor = "players", task.PausedFor
	}
	return out, nil
}

// pregenPresets are the sizes on offer with what each is expected to take:
// at the rate the last task ran on this server, or else at the middle of
// the estimate for this machine's cores.
func pregenPresets(p pregen.Platform, level, world string, rate float64, free int64) []api.PregenPreset {
	dim := pregen.DimensionOf(p, level, world)
	out := []api.PregenPreset{}
	for _, pr := range pregen.Presets() {
		plan, _ := pregen.PresetPlan(pr.ID, world)
		est := plan.Estimate(dim, numCPU())
		secs, ok := est.SecondsAt(rate)
		if !ok {
			secs = int64(math.Sqrt(float64(est.SecondsLow) * float64(est.SecondsHigh)))
		}
		out = append(out, api.PregenPreset{ID: pr.ID, Radius: pr.Radius, Chunks: est.Chunks, Seconds: secs,
			DiskBytes: (est.DiskLow + est.DiskHigh) / 2, Fits: free < 0 || est.CheckDisk(free) == nil})
	}
	return out
}

func detailInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// Following the task.

// pregenLoop follows the server's unfinished map pre-generation.
func (s *server) pregenLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.PregenInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.pregenTick(ctx)
	}
}

func (s *server) pregenTick(ctx context.Context) {
	// Operations stop, restart or replace the server and its world, so the
	// task is left alone until they are done.
	if s.currentOp() != nil {
		return
	}
	task, err := s.lastPregen()
	if err != nil || !task.unfinished() {
		return
	}
	_, p, _, err := s.pregenContext()
	if err != nil || !s.online(ctx) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	task, st := s.pregenCheck(cctx, p)
	cancel()
	if task != nil && st != nil {
		s.pregenPolicy(ctx, p, task, st)
	}
}

// pregenCheck asks Chunky where the unfinished task stands and records it
// once it finished, was cancelled from the console or is gone. It returns
// the task and Chunky's report while the task stays unfinished.
func (s *server) pregenCheck(ctx context.Context, p pregen.Platform) (*pregenTask, *pregen.Status) {
	s.pg.run.Lock()
	defer s.pg.run.Unlock()
	task, err := s.lastPregen()
	if err != nil || !task.unfinished() {
		return nil, nil
	}
	st, err := s.chunky(p).Status(ctx, task.World)
	if errors.Is(err, pregen.ErrNotInstalled) {
		st, err = pregen.Status{World: task.World, State: pregen.StateIdle}, nil
	}
	if err != nil {
		s.log.Debug("could not check the map pre-generation", "server", s.id, "err", err)
		return nil, nil
	}
	now := s.now()
	switch st.State {
	case pregen.StateFinished:
		saved := st.Task
		if st.Progress != nil && st.Progress.Finished {
			saved = &pregen.Task{Chunks: st.Progress.Chunks, ElapsedSeconds: st.Progress.ElapsedSeconds}
		}
		var after any
		if sc, _ := s.serverConfig(); sc != nil {
			after = s.worldSize(s.levelName(*sc))
		}
		if err := s.endPregen(task, pregenFinished, saved, after); err != nil {
			s.log.Warn("could not record the finished map pre-generation", "server", s.id, "err", err)
			return nil, nil
		}
		detail := fmt.Sprintf("%d chunks", task.Total)
		if saved != nil {
			detail = fmt.Sprintf("%d chunks in %s", saved.Chunks, inWords(time.Duration(saved.ElapsedSeconds)*time.Second))
		}
		s.audit("playkeeper", "pregen.finished", task.World, "succeeded", detail)
		return nil, nil
	case pregen.StateCancelled:
		if err := s.endPregen(task, pregenCancelled, st.Task, nil); err == nil {
			s.audit("playkeeper", "pregen.cancelled", task.World, "succeeded", "cancelled from the console")
		}
		return nil, nil
	case pregen.StateIdle:
		s.pg.mu.Lock()
		if s.pg.idleSince.IsZero() {
			s.pg.idleSince = now
		}
		gone := now.Sub(s.pg.idleSince) >= pregenIdleGrace
		s.pg.mu.Unlock()
		if gone {
			if err := s.endPregen(task, pregenCancelled, nil, nil); err == nil {
				s.audit("playkeeper", "pregen.cancelled", task.World, "failed", "Chunky no longer has the task")
			}
			return nil, nil
		}
	default:
		s.pg.mu.Lock()
		s.pg.idleSince = time.Time{}
		s.pg.mu.Unlock()
	}
	s.pg.mu.Lock()
	s.pg.status, s.pg.at = &st, now
	s.pg.mu.Unlock()
	return task, &st
}

// pregenPolicy pauses the task while people play and continues it once the
// server has been empty for a while. It leaves alone a task a user paused,
// or continued while people were playing.
func (s *server) pregenPolicy(ctx context.Context, p pregen.Platform, task *pregenTask, st *pregen.Status) {
	now := s.now()
	s.mu.Lock()
	snap := s.players
	s.mu.Unlock()
	if snap == nil || now.Sub(snap.At) >= 3*s.opts.SampleInterval+5*time.Second {
		return
	}
	s.pg.mu.Lock()
	if snap.Online > 0 {
		s.pg.emptySince = time.Time{}
	} else {
		s.pg.override = false
		if s.pg.emptySince.IsZero() {
			s.pg.emptySince = snap.At
		}
	}
	override, emptySince := s.pg.override, s.pg.emptySince
	s.pg.mu.Unlock()
	if st.State == pregen.StateRunning && task.PausedByPolicy {
		// Continued some other way, such as by a restart.
		_ = s.updatePregen(`paused_by_policy = 0, paused_for = ''`)
		task.PausedByPolicy = false
	}
	policy := pregen.Policy{Enabled: task.PauseForPlayers && !task.PausedByUser && !override, ResumeAfter: s.opts.PregenResumeAfter}
	action := policy.Decide(pregen.Observation{Online: snap.Online, State: st.State, PausedByPolicy: task.PausedByPolicy, EmptySince: emptySince, Now: now})
	if action == pregen.ActionNone {
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		return
	}
	defer release()
	if s.machineBusy() != nil {
		return
	}
	s.pg.run.Lock()
	defer s.pg.run.Unlock()
	if cur, err := s.lastPregen(); err != nil || !cur.unfinished() || cur.PausedByUser != task.PausedByUser {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, pregenCommandTimeout)
	defer cancel()
	ctrl := s.chunky(p)
	switch action {
	case pregen.ActionPause:
		if err := ctrl.Pause(cctx, task.World); err != nil && !errors.Is(err, pregen.ErrNotRunning) {
			s.log.Warn("could not pause the map pre-generation for players", "server", s.id, "err", err)
			return
		}
		name := ""
		if len(snap.Names) > 0 {
			name = slices.Min(snap.Names)
		}
		_ = s.updatePregen(`paused_by_policy = 1, paused_for = ?`, name)
	case pregen.ActionContinue:
		if err := ctrl.Continue(cctx, task.World); err != nil {
			s.log.Warn("could not continue the map pre-generation", "server", s.id, "err", err)
			return
		}
		_ = s.updatePregen(`paused_by_policy = 0, paused_for = ''`)
	}
	s.pg.forget()
}

// Starting.

func (s *server) hPregenStart(w http.ResponseWriter, r *http.Request) {
	var req api.PregenStartRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	sc, p, world, err := s.pregenContext()
	if err != nil {
		writeError(w, err)
		return
	}
	plan, ok := pregen.PresetPlan(req.Preset, world)
	if !ok {
		writeError(w, errInvalid("Choose one of the sizes: small, medium, large or huge."))
		return
	}
	if err := plan.Check(p); err != nil {
		writeError(w, pregenError(err))
		return
	}
	task, err := s.lastPregen()
	if err != nil {
		writeError(w, err)
		return
	}
	if task.unfinished() {
		writeError(w, pregenBusy())
		return
	}
	est := plan.Estimate(pregen.DimensionOf(p, s.levelName(*sc), world), numCPU())
	if free, _, err := s.opts.DiskUsage(s.dataDir()); err == nil {
		if err := est.CheckDisk(free); err != nil {
			writeError(w, pregenError(err))
			return
		}
	}
	op, err := s.beginOp("pregen-start", actor, func(ctx context.Context, h *opHandle) error {
		return s.startPregen(ctx, h, actor, p, plan, req.Preset, req.PauseForPlayers, est.Total)
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, op)
}

func pregenBusy() *apiError {
	return &apiError{Status: http.StatusConflict, Code: pregen.CodeAlreadyRunning, Msg: "The map is already being pre-generated.", Hint: "Wait for it to finish, or cancel it first."}
}

// startPregen installs Chunky if the server doesn't have it, restarts or
// starts the server if Chunky isn't loaded, then starts the task.
func (s *server) startPregen(ctx context.Context, h *opHandle, actor string, p pregen.Platform, plan pregen.Plan, preset string, pauseForPlayers bool, total int64) error {
	h.set("preset", preset)
	h.set("radius", plan.Radius)
	h.set("pauseForPlayers", pauseForPlayers)
	h.phase("checking")
	if task, err := s.lastPregen(); err != nil {
		return err
	} else if task.unfinished() {
		return pregenBusy()
	}
	if err := s.installChunky(ctx, h, actor, p); err != nil {
		return err
	}
	sc, err := s.serverConfig()
	if err != nil {
		return err
	}
	if sc == nil {
		return errNotCreated()
	}
	ctrl := s.chunky(p)
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return err
	}
	loaded := false
	if running {
		if !s.online(ctx) {
			return errConflict(s.name()+" is still starting.", "Try again once it is online.")
		}
		switch err := s.chunkyReady(ctx, ctrl); {
		case err == nil:
			loaded = true
		case !errors.Is(err, pregen.ErrNotInstalled):
			return pregenError(err)
		}
	}
	if !loaded {
		if running {
			h.set("step", "restarting")
			s.warnPlayers(ctx, h, "Restarting")
			if err := s.stopServer(ctx, h); err != nil {
				return err
			}
		} else {
			h.set("step", "starting_server")
		}
		if err := s.setDesired(api.DesiredRunning); err != nil {
			return err
		}
		if err := s.startServer(ctx, h, *sc); err != nil {
			s.startFailed(ctx)
			return err
		}
		if err := s.chunkyReady(ctx, ctrl); errors.Is(err, pregen.ErrNotInstalled) {
			return &apiError{Msg: "Chunky did not load when " + s.name() + " started.", Hint: "Open the Console to see why."}
		} else if err != nil {
			return pregenError(err)
		}
	}
	h.set("step", "starting_task")
	h.phase("starting_task")
	if err := ctrl.Configure(ctx, pregen.Config{ContinueOnRestart: true, UpdateInterval: pregenUpdateInterval}); err != nil {
		return pregenError(err)
	}
	before := s.worldSize(s.levelName(*sc))
	started, err := ctrl.Start(ctx, plan, pregen.StartOptions{ContinueOnRestart: true, Replace: true})
	if err != nil {
		return pregenError(err)
	}
	_, err = s.db.Exec(`INSERT INTO pregen(server_id, world, preset, radius, pause_for_players, started_at, world_bytes_before, total)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(server_id) DO UPDATE SET world = excluded.world, preset = excluded.preset, radius = excluded.radius,
		pause_for_players = excluded.pause_for_players, paused_by_user = 0, paused_by_policy = 0, paused_for = '',
		started_at = excluded.started_at, ended = '', ended_at = NULL, world_bytes_before = excluded.world_bytes_before,
		world_bytes_after = NULL, chunks = 0, total = excluded.total, elapsed_secs = 0`,
		s.id, plan.World, preset, plan.Radius, pauseForPlayers, s.now().UnixMilli(), before, total)
	if err != nil {
		if cerr := ctrl.Cancel(ctx, plan.World); cerr != nil {
			s.log.Warn("could not cancel an unrecorded map pre-generation", "server", s.id, "err", cerr)
		}
		return err
	}
	s.pg.reset()
	s.audit(actor, "pregen.started", plan.World, "succeeded", fmt.Sprintf("%s: %d blocks around %d, %d", preset, plan.Radius, started.CenterX, started.CenterZ))
	return nil
}

// installChunky installs Chunky from Modrinth unless the server has it. A
// record of an earlier install whose file is gone is dropped first.
func (s *server) installChunky(ctx context.Context, h *opHandle, actor string, p pregen.Platform) error {
	_, err := pregen.Detect(s.dataDir(), p)
	if !errors.Is(err, pregen.ErrNotInstalled) {
		return err
	}
	h.set("step", "installing")
	key := addons.Key{Source: addons.Modrinth, ProjectID: pregen.ModrinthProjectID}
	installed, err := s.installedAddons()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(installed, func(rec addons.Installed) bool { return rec.Key() == key }) {
		if err := s.saveAddons(nil, []addons.Key{key}, false); err != nil {
			return err
		}
	}
	return s.installAddons(ctx, h, actor, func(srv addons.Server, installed []addons.Installed, progress func(addons.Progress)) (*addons.Result, error) {
		return s.lib().Install(ctx, srv, installed, addons.InstallRequest{Source: key.Source, Project: key.ProjectID, OnProgress: progress})
	})
}

// chunkyReady asks Chunky for its progress, giving the console a few
// seconds to come up after the server starts.
func (s *server) chunkyReady(ctx context.Context, ctrl *pregen.Controller) error {
	deadline := s.now().Add(20 * time.Second)
	for {
		_, err := ctrl.Progress(ctx)
		if err == nil || !errors.Is(err, errChunkyConsole) || s.now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Pausing, continuing and cancelling are quick and take the operation lock
// only while they run. While the server is stopped they change only
// whether Chunky resumes the task when it starts.

func (s *server) hPregenPause(w http.ResponseWriter, r *http.Request) {
	s.pregenAction(w, r, "pause")
}

func (s *server) hPregenContinue(w http.ResponseWriter, r *http.Request) {
	s.pregenAction(w, r, "continue")
}

func (s *server) hPregenCancel(w http.ResponseWriter, r *http.Request) {
	s.pregenAction(w, r, "cancel")
}

func (s *server) pregenAction(w http.ResponseWriter, r *http.Request, action string) {
	actor, err := actionActor(r)
	if err != nil {
		writeError(w, err)
		return
	}
	_, p, _, err := s.pregenContext()
	if err != nil {
		writeError(w, err)
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		writeError(w, s.busyError())
		return
	}
	err = s.pregenAct(r.Context(), p, actor, action)
	release()
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.pregenView(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

var pregenAudits = map[string]string{"pause": "pregen.paused", "continue": "pregen.continued", "cancel": "pregen.cancelled"}

// pregenAct pauses, continues or cancels the unfinished task; the caller
// holds the operation lock.
func (s *server) pregenAct(ctx context.Context, p pregen.Platform, actor, action string) error {
	if err := s.machineBusy(); err != nil {
		return err
	}
	s.pg.run.Lock()
	defer s.pg.run.Unlock()
	task, err := s.lastPregen()
	if err != nil {
		return err
	}
	if !task.unfinished() {
		return &apiError{Status: http.StatusConflict, Code: pregen.CodeNotRunning, Msg: "The map is not being pre-generated.", Hint: "Start pre-generating it first."}
	}
	ctx, cancel := context.WithTimeout(ctx, pregenCommandTimeout)
	defer cancel()
	online := s.online(ctx)
	ctrl := s.chunky(p)
	resume := func(on bool) error {
		cfg := pregen.Config{ContinueOnRestart: on}
		if online {
			return ctrl.Configure(ctx, cfg)
		}
		return pregen.WriteConfig(s.dataDir(), p, cfg, s.pregenOwner())
	}
	switch action {
	case "pause":
		if online {
			if err := ctrl.Pause(ctx, task.World); err != nil && !errors.Is(err, pregen.ErrNotRunning) {
				return pregenError(err)
			}
		}
		if err := resume(false); err != nil {
			return pregenError(err)
		}
		err = s.updatePregen(`paused_by_user = 1, paused_by_policy = 0, paused_for = ''`)
	case "continue":
		if err := resume(true); err != nil {
			return pregenError(err)
		}
		if online {
			if err := ctrl.Continue(ctx, task.World); err != nil {
				return pregenError(err)
			}
			s.mu.Lock()
			playing := s.players != nil && s.players.Online > 0
			s.mu.Unlock()
			s.pg.mu.Lock()
			s.pg.override = playing && task.PauseForPlayers
			s.pg.mu.Unlock()
		}
		err = s.updatePregen(`paused_by_user = 0, paused_by_policy = 0, paused_for = ''`)
	case "cancel":
		if online {
			if err := ctrl.Cancel(ctx, task.World); err != nil {
				return pregenError(err)
			}
		} else if err := resume(false); err != nil {
			return pregenError(err)
		}
		var saved *pregen.Task
		if t, found, rerr := pregen.ReadTask(s.dataDir(), p, task.World); rerr == nil && found {
			saved = &t
		}
		err = s.endPregen(task, pregenCancelled, saved, nil)
	default:
		return errInvalid("unknown action %q", action)
	}
	if err != nil {
		return err
	}
	s.pg.forget()
	s.audit(actor, pregenAudits[action], task.World, "succeeded", "")
	return nil
}

// A restored world.

// holdRestoredPregen keeps Chunky in a restored world from resuming a task
// the backup saved, which Playkeeper does not follow.
func (s *server) holdRestoredPregen(sc api.ServerConfig) {
	p, err := pregen.PlatformFor(s.serverType(&sc))
	if err != nil {
		return
	}
	if _, err := os.Lstat(filepath.Join(s.dataDir(), pregen.ConfigPath(p))); err != nil {
		return
	}
	if err := pregen.WriteConfig(s.dataDir(), p, pregen.Config{}, s.pregenOwner()); err != nil {
		s.log.Warn("could not keep Chunky from resuming the restored world's task", "server", s.id, "err", err)
	}
}

// forgetPregen drops the record of the map pre-generation once the world it
// was for is gone.
func (s *server) forgetPregen() {
	if _, err := s.db.Exec(`DELETE FROM pregen WHERE server_id = ?`, s.id); err != nil {
		s.log.Warn("could not forget the map pre-generation", "server", s.id, "err", err)
	}
	s.pg.reset()
}
