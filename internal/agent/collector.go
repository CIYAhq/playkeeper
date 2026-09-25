package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

type logCursor struct {
	Container string    `json:"container"`
	TS        time.Time `json:"ts"`
}

func (s *server) loadCursor() logCursor {
	var c logCursor
	var v string
	if s.db.QueryRow(`SELECT log_cursor FROM servers WHERE id = ?`, s.id).Scan(&v) == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (s *server) saveCursor(c logCursor) {
	b, _ := json.Marshal(c)
	_, _ = s.db.Exec(`UPDATE servers SET log_cursor = ? WHERE id = ?`, string(b), s.id)
}

// followLoop tails the container log with Docker timestamps. The persisted
// cursor plus per-line de-duplication means an agent restart replays missed
// lines (events recorded with their true time) without double counting.
func (s *server) followLoop(ctx context.Context) {
	prefilled := false
	var attached time.Time
	for ctx.Err() == nil {
		c, err := s.docker.ContainerInspect(ctx, s.containerName())
		if err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if !prefilled {
			s.prefillConsole(ctx, c.ID)
			prefilled = true
		}
		runStart, _ := c.State.Started()
		s.attachRun(c, runStart)
		fin, _ := c.State.Finished()
		live := c.State.Running || fin.After(s.started)
		cur := s.loadCursor()
		since := time.Time{}
		if cur.Container == c.ID {
			since = cur.TS
		}
		// On the first attach to a run, replay it from its start so the run's
		// phase and clean-shutdown marker are rebuilt; events de-duplicate.
		if !runStart.Equal(attached) {
			if !runStart.IsZero() && runStart.Before(since) {
				since = runStart
			}
			attached = runStart
		}
		scanner, err := s.docker.ContainerLogs(ctx, c.ID, docker.LogsOptions{Follow: true, Since: since})
		if err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		last := cur
		last.Container = c.ID
		lastSave := time.Now()
		for {
			l, err := scanner.Next()
			if err != nil {
				break
			}
			s.ingest(c.ID, l, runStart, live)
			if l.TS.After(last.TS) {
				last.TS = l.TS
			}
			if time.Since(lastSave) > time.Second {
				s.saveCursor(last)
				lastSave = time.Now()
			}
		}
		scanner.Close()
		s.saveCursor(last)
		if c2, err := s.docker.ContainerInspect(ctx, c.ID); err == nil && !c2.State.Running {
			s.mu.Lock()
			s.followEnded[c.ID] = s.now()
			s.mu.Unlock()
			sleepCtx(ctx, 2*time.Second)
		} else {
			sleepCtx(ctx, 300*time.Millisecond)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// prefillConsole shows recent output after an agent restart. These lines are
// display-only; event ingestion is driven by the cursor.
func (s *server) prefillConsole(ctx context.Context, id string) {
	sc, err := s.docker.ContainerLogs(ctx, id, docker.LogsOptions{Tail: "300"})
	if err != nil {
		return
	}
	defer sc.Close()
	for {
		l, err := sc.Next()
		if err != nil {
			return
		}
		s.console.append(l.TS, minecraft.CleanLine(l.Text))
	}
}

// attachRun resets per-run state when the follower sees a new container start.
func (s *server) attachRun(c docker.ContainerJSON, runStart time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runStart.Equal(s.runStartedAt) {
		return
	}
	s.runStartedAt = runStart
	s.sawStopping = false
	if c.State.Running && s.runPhase != api.PhaseStartingContainer {
		s.runPhase = api.PhaseStartingContainer
	}
}

func dedupKey(container string, l docker.LogLine) string {
	h := sha256.Sum256([]byte(container + "\x00" + l.Raw))
	return hex.EncodeToString(h[:16])
}

// ingest records a log line's events. live tells whether the run the follower
// attached to was still going when the agent started.
func (s *server) ingest(container string, l docker.LogLine, runStart time.Time, live bool) {
	ts := l.TS
	if ts.IsZero() {
		ts = s.now()
	}
	text := minecraft.CleanLine(l.Text)
	if ts.After(s.console.lastTS()) {
		s.console.append(ts, text)
	}
	p := minecraft.Parse(text)
	if p.Kind == minecraft.EventNone {
		return
	}
	// Only the current run's lines change the live phase and errors. The run
	// the follower replays after a host reboot or an agent restart ended
	// before the agent started: its lines are recorded, but they are history.
	current := !ts.Before(runStart) && (live || !ts.Before(s.started))
	key := dedupKey(container, l)
	switch p.Kind {
	case minecraft.EventUUID:
		s.mu.Lock()
		s.uuids[p.Player] = p.UUID
		s.mu.Unlock()
	case minecraft.EventJoin:
		s.mu.Lock()
		uuid := s.uuids[p.Player]
		s.mu.Unlock()
		if s.insertEvent(ts, "join", p.Player, uuid, "server_log", "", key) {
			s.openSession(ts, p.Player, uuid, "server_log", false)
		}
	case minecraft.EventLeave:
		if s.insertEvent(ts, "leave", p.Player, "", "server_log", "", key) {
			s.closeSession(ts, p.Player, "left", false)
		}
	case minecraft.EventReady:
		s.insertEvent(ts, "server_ready", "", "", "server_log", p.Detail+"s", key)
		if current {
			s.mu.Lock()
			s.runPhase = api.PhaseOnline
			s.crashed = false
			s.lastError, s.lastErrorHint = "", ""
			s.mu.Unlock()
		}
	case minecraft.EventStopping:
		s.insertEvent(ts, "server_stopping", "", "", "server_log", "", key)
		if current {
			s.mu.Lock()
			s.runPhase = api.PhaseStopping
			s.sawStopping = true
			s.mu.Unlock()
		}
	case minecraft.EventDownloading, minecraft.EventStarting, minecraft.EventPreparing:
		if current {
			s.mu.Lock()
			if s.runPhase != api.PhaseOnline && s.runPhase != api.PhaseStopping {
				// Downloads happen in the setup-only container; in the server
				// container the image only re-checks files it already has.
				s.runPhase = map[minecraft.EventKind]api.Phase{
					minecraft.EventDownloading: api.PhaseStarting,
					minecraft.EventStarting:    api.PhaseStarting,
					minecraft.EventPreparing:   api.PhasePreparingWorld,
				}[p.Kind]
				s.runPhaseDetail = p.Detail
			}
			s.mu.Unlock()
		}
	case minecraft.EventInitError:
		if current {
			s.mu.Lock()
			s.lastError = "The server could not download or install its software: " + p.Detail
			s.lastErrorHint = "Check that this host can reach fill.papermc.io and piston-data.mojang.com, then press Start again."
			s.mu.Unlock()
		}
	case minecraft.EventOOM:
		if current {
			s.mu.Lock()
			s.lastError = "Java ran out of memory."
			s.lastErrorHint = "Choose a larger memory budget in Settings."
			s.mu.Unlock()
		}
	case minecraft.EventBindFailed:
		if current {
			s.mu.Lock()
			s.lastError = "The server could not open its network port."
			s.lastErrorHint = "Another program may be using the port; see Settings for the port in use."
			s.mu.Unlock()
		}
	}
}

// insertEvent stores an event once; it returns false for duplicates.
func (s *server) insertEvent(ts time.Time, kind, player, uuid, source, detail, key string) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO events(server_id, ts, kind, player, uuid, source, detail, dedup_key, ingested_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		s.id, ts.UnixMilli(), kind, nullStr(player), nullStr(uuid), source, detail, key, s.now().UnixMilli())
	if err != nil {
		s.log.Error("event insert failed", "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func (s *server) recordEvent(ts time.Time, kind, player, source, detail string) {
	key := sha256.Sum256([]byte(s.id + "\x00" + kind + "\x00" + player + "\x00" + ts.UTC().Format(time.RFC3339Nano) + "\x00" + detail))
	s.insertEvent(ts, kind, player, "", source, detail, hex.EncodeToString(key[:16]))
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *server) openSession(ts time.Time, player, uuid, source string, startUncertain bool) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE sessions SET end_ts = ?, end_reason = 'rejoined_without_leave', end_uncertain = 1
		WHERE server_id = ? AND player = ? AND end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), s.id, player, ts.UnixMilli()); err != nil {
		return
	}
	if _, err := tx.Exec(`INSERT INTO sessions(server_id, player, uuid, start_ts, start_uncertain, source) VALUES(?,?,?,?,?,?)`,
		s.id, player, nullStr(uuid), ts.UnixMilli(), boolInt(startUncertain), source); err != nil {
		return
	}
	_ = tx.Commit()
}

func (s *server) closeSession(ts time.Time, player, reason string, uncertain bool) {
	_, err := s.db.Exec(`UPDATE sessions SET end_ts = ?, end_reason = ?, end_uncertain = ?
		WHERE server_id = ? AND player = ? AND end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), reason, boolInt(uncertain), s.id, player, ts.UnixMilli())
	if err != nil {
		s.log.Error("close session", "err", err)
	}
}

// closeOpenSessions ends every open session at ts. uncertain marks sessions
// whose players left no leave event (for example, the server crashed).
func (s *server) closeOpenSessions(ts time.Time, reason string, uncertain bool) {
	_, err := s.db.Exec(`UPDATE sessions SET end_ts = ?, end_reason = ?, end_uncertain = ?
		WHERE server_id = ? AND end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), reason, boolInt(uncertain), s.id, ts.UnixMilli())
	if err != nil {
		s.log.Error("close open sessions", "err", err)
	}
}

func (s *server) openSessionPlayers() map[string]bool {
	rows, err := s.db.Query(`SELECT player FROM sessions WHERE server_id = ? AND end_ts IS NULL`, s.id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// reconcileWithList cross-checks sessions against an authoritative `list`
// snapshot. A mismatch must persist across two samples before Playkeeper
// opens or closes a session, and such sessions are flagged uncertain.
func (s *server) reconcileWithList(ts time.Time, names []string) {
	online := map[string]bool{}
	for _, n := range names {
		online[n] = true
	}
	open := s.openSessionPlayers()
	s.mu.Lock()
	var toOpen []string
	var toClose []string
	for n := range online {
		if open[n] {
			delete(s.listExtra, n)
			continue
		}
		s.listExtra[n]++
		if s.listExtra[n] >= 2 {
			toOpen = append(toOpen, n)
			delete(s.listExtra, n)
		}
	}
	for n := range open {
		if online[n] {
			delete(s.listMissing, n)
			continue
		}
		s.listMissing[n]++
		if s.listMissing[n] >= 2 {
			toClose = append(toClose, n)
			delete(s.listMissing, n)
		}
	}
	s.mu.Unlock()
	for _, n := range toOpen {
		s.openSession(ts, n, "", "player_list", true)
	}
	for _, n := range toClose {
		s.closeSession(ts, n, "not_in_player_list", true)
	}
}

// worldEvery is how often the world's size on disk is measured.
const worldEvery = 5 * time.Minute

// measureWorld adds up the files of the world's dimensions every few minutes.
func (s *server) measureWorld(now time.Time, level string) {
	s.mu.Lock()
	due := now.Sub(s.worldAt) >= worldEvery
	s.mu.Unlock()
	if !due || level == "" {
		return
	}
	var total int64
	for _, dir := range []string{level, level + "_nether", level + "_the_end"} {
		filepath.WalkDir(filepath.Join(s.dataDir(), dir), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
	}
	s.mu.Lock()
	s.worldBytes, s.worldAt = total, now
	s.mu.Unlock()
}

func (s *server) sampleLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.SampleInterval)
	defer t.Stop()
	for {
		s.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type sampleRow struct {
	state    string
	online   *int
	max      *int
	cpu      *float64
	mem      *int64
	memLimit *int64
	diskFree *int64
}

func (s *server) sample(ctx context.Context) {
	now := s.now().UTC()
	row := sampleRow{}
	res := &api.Resources{At: now}
	if free, total, err := s.opts.DiskUsage(s.cfg.DataDir); err == nil {
		row.diskFree = &free
		res.DiskFreeBytes, res.DiskTotalBytes = &free, &total
	}
	sc, _ := s.serverConfig()
	if sc != nil {
		s.measureWorld(now, sc.LevelName)
	}
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	var snap *api.PlayerSnapshot
	reachable := false
	switch {
	case err != nil && !docker.IsNotFound(err):
		s.setDockerOK(false)
		row.state = "docker_unavailable"
	case sc == nil:
		s.setDockerOK(true)
		row.state = "not_created"
	case err != nil:
		s.setDockerOK(true)
		row.state = "stopped"
	case c.State.Running:
		s.setDockerOK(true)
		if st, err := s.docker.ContainerStats(ctx, c.ID); err == nil {
			s.mu.Lock()
			row.cpu = cpuPercent(s.prevCPU, &st)
			s.prevCPU = &st
			s.mu.Unlock()
			used, limit := int64(st.MemoryUsed()), int64(st.MemoryStats.Limit)
			row.mem, row.memLimit = &used, &limit
			res.CPUPercent, res.MemBytes, res.MemLimitBytes = row.cpu, row.mem, row.memLimit
		}
		s.mu.Lock()
		phase := s.runPhase
		s.mu.Unlock()
		pingAddr := s.opts.PingAddr
		if pingAddr == "" {
			pingAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(s.gamePort))
		}
		if st, err := minecraft.Ping(pingAddr, 3*time.Second); err == nil {
			reachable = true
			snap = &api.PlayerSnapshot{Online: st.Online, Max: st.Max, Names: st.Sample, Source: "status ping", At: now}
		}
		if phase == api.PhaseOnline {
			if out, err := s.rconCommand("list"); err == nil {
				if on, max, names, ok := minecraft.ParseList(out); ok {
					sort.Strings(names)
					snap = &api.PlayerSnapshot{Online: on, Max: max, Names: names, Source: "rcon list", At: now}
					s.reconcileWithList(now, names)
				}
			}
			if out, err := s.rconCommand("tps"); err == nil {
				if tps, ok := minecraft.ParseTPS(out); ok {
					res.TPS = &tps
				}
			}
			row.state = "online"
		} else {
			row.state = "starting"
		}
		if snap != nil {
			row.online, row.max = &snap.Online, &snap.Max
		}
	default:
		s.setDockerOK(true)
		s.mu.Lock()
		if s.crashed {
			row.state = "crashed"
		} else {
			row.state = "stopped"
		}
		s.prevCPU = nil
		s.mu.Unlock()
	}
	if snap != nil && snap.Names == nil {
		snap.Names = []string{}
	}
	s.mu.Lock()
	s.resources = res
	s.players = snap
	s.reachable = reachable
	if reachable {
		s.reachableAt = now
	}
	s.mu.Unlock()
	_, err = s.db.Exec(`INSERT OR REPLACE INTO samples(server_id, ts, state, players_online, players_max, cpu_pct, mem_bytes, mem_limit, disk_free) VALUES(?,?,?,?,?,?,?,?,?)`,
		s.id, now.UnixMilli(), row.state, row.online, row.max, row.cpu, row.mem, row.memLimit, row.diskFree)
	if err != nil {
		s.log.Error("sample insert failed", "err", err)
	}
	s.setCollectingSince(now)
}

func cpuPercent(prev, cur *docker.Stats) *float64 {
	if prev == nil || cur.CPUStats.CPUUsage.TotalUsage < prev.CPUStats.CPUUsage.TotalUsage ||
		cur.CPUStats.SystemCPUUsage <= prev.CPUStats.SystemCPUUsage {
		return nil
	}
	dCPU := float64(cur.CPUStats.CPUUsage.TotalUsage - prev.CPUStats.CPUUsage.TotalUsage)
	dSys := float64(cur.CPUStats.SystemCPUUsage - prev.CPUStats.SystemCPUUsage)
	cpus := float64(cur.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = 1
	}
	v := dCPU / dSys * cpus * 100
	return &v
}

func (a *Agent) setDockerOK(ok bool) {
	a.mu.Lock()
	a.dockerOK = ok
	a.mu.Unlock()
}

// rconCommand sends one console command, waiting up to 10 seconds.
func (s *server) rconCommand(cmd string) (string, error) {
	ctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
	defer cancel()
	return s.rconExec(ctx, cmd)
}

// rconExec sends one console command over the private Docker bridge within
// ctx's deadline. A connection found closed is replaced before the command
// is written, but a command whose reply was lost is never sent again: the
// server may have run it, and a second save-on would read as saving turned
// back on by someone else, which throws a good backup away.
func (s *server) rconExec(ctx context.Context, cmd string) (string, error) {
	select {
	case s.rconLock <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-s.rconLock }()
	for attempt := 0; ; attempt++ {
		if s.rcon == nil {
			if err := s.dialRCON(ctx); err != nil {
				return "", err
			}
		}
		out, err := s.rcon.CommandContext(ctx, cmd)
		if err == nil {
			return out, nil
		}
		s.rcon.Close()
		s.rcon = nil
		if attempt > 0 || !errors.Is(err, minecraft.ErrNotSent) || ctx.Err() != nil {
			return "", err
		}
	}
}

// rconConsole is a server's console for backup.Take and the tick probes.
type rconConsole struct{ s *server }

func (c rconConsole) Command(ctx context.Context, cmd string) (string, error) {
	return c.s.rconExec(ctx, cmd)
}

func (s *server) dialRCON(ctx context.Context) error {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if err != nil {
		return err
	}
	if !c.State.Running {
		return errors.New("the server is not running")
	}
	n, ok := c.NetworkSettings.Networks[networkName]
	if !ok || n.IPAddress == "" {
		return errors.New("the server has no address on the Playkeeper network")
	}
	pass, err := s.rconPassword()
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	r, err := minecraft.DialRCONContext(dctx, s.opts.RCONAddr(n.IPAddress), pass)
	if err != nil {
		return err
	}
	s.rcon = r
	s.rconIP = n.IPAddress
	return nil
}

func (s *server) resetRCON() {
	s.rconLock <- struct{}{}
	if s.rcon != nil {
		s.rcon.Close()
		s.rcon = nil
	}
	<-s.rconLock
}

// pruneLoop enforces retention for analytics, events, operations and audit.
func (a *Agent) pruneLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		a.prune()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (a *Agent) prune() {
	r := a.opts.Retention
	now := a.now()
	stmts := []struct {
		q    string
		args []any
	}{
		{`DELETE FROM samples WHERE ts < ?`, []any{now.Add(-r.Samples).UnixMilli()}},
		{`DELETE FROM events WHERE ts < ?`, []any{now.Add(-r.Events).UnixMilli()}},
		{`DELETE FROM sessions WHERE start_ts < ? AND end_ts IS NOT NULL`, []any{now.Add(-r.Events).UnixMilli()}},
		{`DELETE FROM operations WHERE started_at < ? AND status != 'running'`, []any{now.Add(-r.Operations).UnixMilli()}},
		{`DELETE FROM audit WHERE ts < ?`, []any{now.Add(-r.Audit).UnixMilli()}},
		{`DELETE FROM samples WHERE rowid NOT IN (SELECT rowid FROM samples ORDER BY ts DESC LIMIT ?)`, []any{r.MaxSamples}},
		{`DELETE FROM events WHERE id NOT IN (SELECT id FROM events ORDER BY id DESC LIMIT ?)`, []any{r.MaxEvents}},
		{`DELETE FROM audit WHERE id NOT IN (SELECT id FROM audit ORDER BY id DESC LIMIT ?)`, []any{r.MaxAudit}},
	}
	for _, s := range stmts {
		if _, err := a.db.Exec(s.q, s.args...); err != nil {
			a.log.Error("prune failed", "err", err)
		}
	}
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}
