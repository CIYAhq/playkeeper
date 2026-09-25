package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
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

func (a *Agent) loadCursor() logCursor {
	var c logCursor
	if v, ok, _ := a.kvGet(kvLogCursor); ok {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

func (a *Agent) saveCursor(c logCursor) {
	b, _ := json.Marshal(c)
	_ = a.kvSet(kvLogCursor, string(b))
}

// followLoop tails the container log with Docker timestamps. The persisted
// cursor plus per-line de-duplication means an agent restart replays missed
// lines (events recorded with their true time) without double counting.
func (a *Agent) followLoop(ctx context.Context) {
	prefilled := false
	var attached time.Time
	for ctx.Err() == nil {
		c, err := a.docker.ContainerInspect(ctx, containerName)
		if err != nil {
			sleepCtx(ctx, 2*time.Second)
			continue
		}
		if !prefilled {
			a.prefillConsole(ctx, c.ID)
			prefilled = true
		}
		runStart, _ := c.State.Started()
		a.attachRun(c, runStart)
		fin, _ := c.State.Finished()
		live := c.State.Running || fin.After(a.started)
		cur := a.loadCursor()
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
		scanner, err := a.docker.ContainerLogs(ctx, c.ID, docker.LogsOptions{Follow: true, Since: since})
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
			a.ingest(c.ID, l, runStart, live)
			if l.TS.After(last.TS) {
				last.TS = l.TS
			}
			if time.Since(lastSave) > time.Second {
				a.saveCursor(last)
				lastSave = time.Now()
			}
		}
		scanner.Close()
		a.saveCursor(last)
		if c2, err := a.docker.ContainerInspect(ctx, c.ID); err == nil && !c2.State.Running {
			a.mu.Lock()
			a.followEnded[c.ID] = a.now()
			a.mu.Unlock()
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
func (a *Agent) prefillConsole(ctx context.Context, id string) {
	sc, err := a.docker.ContainerLogs(ctx, id, docker.LogsOptions{Tail: "300"})
	if err != nil {
		return
	}
	defer sc.Close()
	for {
		l, err := sc.Next()
		if err != nil {
			return
		}
		a.console.append(l.TS, minecraft.CleanLine(l.Text))
	}
}

// attachRun resets per-run state when the follower sees a new container start.
func (a *Agent) attachRun(c docker.ContainerJSON, runStart time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if runStart.Equal(a.runStartedAt) {
		return
	}
	a.runStartedAt = runStart
	a.sawStopping = false
	if c.State.Running && a.runPhase != api.PhaseStartingContainer {
		a.runPhase = api.PhaseStartingContainer
	}
}

func dedupKey(container string, l docker.LogLine) string {
	h := sha256.Sum256([]byte(container + "\x00" + l.Raw))
	return hex.EncodeToString(h[:16])
}

// ingest records a log line's events. live tells whether the run the follower
// attached to was still going when the agent started.
func (a *Agent) ingest(container string, l docker.LogLine, runStart time.Time, live bool) {
	ts := l.TS
	if ts.IsZero() {
		ts = a.now()
	}
	text := minecraft.CleanLine(l.Text)
	if ts.After(a.console.lastTS()) {
		a.console.append(ts, text)
	}
	p := minecraft.Parse(text)
	if p.Kind == minecraft.EventNone {
		return
	}
	// Only the current run's lines change the live phase and errors. The run
	// the follower replays after a host reboot or an agent restart ended
	// before the agent started: its lines are recorded, but they are history.
	current := !ts.Before(runStart) && (live || !ts.Before(a.started))
	key := dedupKey(container, l)
	switch p.Kind {
	case minecraft.EventUUID:
		a.mu.Lock()
		a.uuids[p.Player] = p.UUID
		a.mu.Unlock()
	case minecraft.EventJoin:
		a.mu.Lock()
		uuid := a.uuids[p.Player]
		a.mu.Unlock()
		if a.insertEvent(ts, "join", p.Player, uuid, "server_log", "", key) {
			a.openSession(ts, p.Player, uuid, "server_log", false)
		}
	case minecraft.EventLeave:
		if a.insertEvent(ts, "leave", p.Player, "", "server_log", "", key) {
			a.closeSession(ts, p.Player, "left", false)
		}
	case minecraft.EventReady:
		a.insertEvent(ts, "server_ready", "", "", "server_log", p.Detail+"s", key)
		if current {
			a.mu.Lock()
			a.runPhase = api.PhaseOnline
			a.crashed = false
			a.lastError, a.lastErrorHint = "", ""
			a.mu.Unlock()
		}
	case minecraft.EventStopping:
		a.insertEvent(ts, "server_stopping", "", "", "server_log", "", key)
		if current {
			a.mu.Lock()
			a.runPhase = api.PhaseStopping
			a.sawStopping = true
			a.mu.Unlock()
		}
	case minecraft.EventDownloading, minecraft.EventStarting, minecraft.EventPreparing:
		if current {
			a.mu.Lock()
			if a.runPhase != api.PhaseOnline && a.runPhase != api.PhaseStopping {
				// Downloads happen in the setup-only container; in the server
				// container the image only re-checks files it already has.
				a.runPhase = map[minecraft.EventKind]api.Phase{
					minecraft.EventDownloading: api.PhaseStarting,
					minecraft.EventStarting:    api.PhaseStarting,
					minecraft.EventPreparing:   api.PhasePreparingWorld,
				}[p.Kind]
				a.runPhaseDetail = p.Detail
			}
			a.mu.Unlock()
		}
	case minecraft.EventInitError:
		if current {
			a.mu.Lock()
			a.lastError = "The server could not download or install its software: " + p.Detail
			a.lastErrorHint = "Check that this host can reach fill.papermc.io and piston-data.mojang.com, then press Start again."
			a.mu.Unlock()
		}
	case minecraft.EventOOM:
		if current {
			a.mu.Lock()
			a.lastError = "Java ran out of memory."
			a.lastErrorHint = "Choose a larger memory budget in Settings."
			a.mu.Unlock()
		}
	case minecraft.EventBindFailed:
		if current {
			a.mu.Lock()
			a.lastError = "The server could not open its network port."
			a.lastErrorHint = "Another program may be using the port; see Settings for the port in use."
			a.mu.Unlock()
		}
	}
}

// insertEvent stores an event once; it returns false for duplicates.
func (a *Agent) insertEvent(ts time.Time, kind, player, uuid, source, detail, key string) bool {
	res, err := a.db.Exec(`INSERT OR IGNORE INTO events(ts, kind, player, uuid, source, detail, dedup_key, ingested_at) VALUES(?,?,?,?,?,?,?,?)`,
		ts.UnixMilli(), kind, nullStr(player), nullStr(uuid), source, detail, key, a.now().UnixMilli())
	if err != nil {
		a.log.Error("event insert failed", "err", err)
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func (a *Agent) recordEvent(ts time.Time, kind, player, source, detail string) {
	key := sha256.Sum256([]byte(kind + "\x00" + player + "\x00" + ts.UTC().Format(time.RFC3339Nano) + "\x00" + detail))
	a.insertEvent(ts, kind, player, "", source, detail, hex.EncodeToString(key[:16]))
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (a *Agent) openSession(ts time.Time, player, uuid, source string, startUncertain bool) {
	tx, err := a.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE sessions SET end_ts = ?, end_reason = 'rejoined_without_leave', end_uncertain = 1
		WHERE player = ? AND end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), player, ts.UnixMilli()); err != nil {
		return
	}
	if _, err := tx.Exec(`INSERT INTO sessions(player, uuid, start_ts, start_uncertain, source) VALUES(?,?,?,?,?)`,
		player, nullStr(uuid), ts.UnixMilli(), boolInt(startUncertain), source); err != nil {
		return
	}
	_ = tx.Commit()
}

func (a *Agent) closeSession(ts time.Time, player, reason string, uncertain bool) {
	_, err := a.db.Exec(`UPDATE sessions SET end_ts = ?, end_reason = ?, end_uncertain = ?
		WHERE player = ? AND end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), reason, boolInt(uncertain), player, ts.UnixMilli())
	if err != nil {
		a.log.Error("close session", "err", err)
	}
}

// closeOpenSessions ends every open session at ts. uncertain marks sessions
// whose players left no leave event (for example, the server crashed).
func (a *Agent) closeOpenSessions(ts time.Time, reason string, uncertain bool) {
	_, err := a.db.Exec(`UPDATE sessions SET end_ts = ?, end_reason = ?, end_uncertain = ?
		WHERE end_ts IS NULL AND start_ts <= ?`, ts.UnixMilli(), reason, boolInt(uncertain), ts.UnixMilli())
	if err != nil {
		a.log.Error("close open sessions", "err", err)
	}
}

func (a *Agent) openSessionPlayers() map[string]bool {
	rows, err := a.db.Query(`SELECT player FROM sessions WHERE end_ts IS NULL`)
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
func (a *Agent) reconcileWithList(ts time.Time, names []string) {
	online := map[string]bool{}
	for _, n := range names {
		online[n] = true
	}
	open := a.openSessionPlayers()
	a.mu.Lock()
	var toOpen []string
	var toClose []string
	for n := range online {
		if open[n] {
			delete(a.listExtra, n)
			continue
		}
		a.listExtra[n]++
		if a.listExtra[n] >= 2 {
			toOpen = append(toOpen, n)
			delete(a.listExtra, n)
		}
	}
	for n := range open {
		if online[n] {
			delete(a.listMissing, n)
			continue
		}
		a.listMissing[n]++
		if a.listMissing[n] >= 2 {
			toClose = append(toClose, n)
			delete(a.listMissing, n)
		}
	}
	a.mu.Unlock()
	for _, n := range toOpen {
		a.openSession(ts, n, "", "player_list", true)
	}
	for _, n := range toClose {
		a.closeSession(ts, n, "not_in_player_list", true)
	}
}

func (a *Agent) sampleLoop(ctx context.Context) {
	t := time.NewTicker(a.opts.SampleInterval)
	defer t.Stop()
	for {
		a.sample(ctx)
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

func (a *Agent) sample(ctx context.Context) {
	now := a.now().UTC()
	row := sampleRow{}
	res := &api.Resources{At: now}
	if free, total, err := a.opts.DiskUsage(a.cfg.DataDir); err == nil {
		row.diskFree = &free
		res.DiskFreeBytes, res.DiskTotalBytes = &free, &total
	}
	sc, _ := a.serverConfig()
	c, err := a.docker.ContainerInspect(ctx, containerName)
	var snap *api.PlayerSnapshot
	reachable := false
	switch {
	case err != nil && !docker.IsNotFound(err):
		a.setDockerOK(false)
		row.state = "docker_unavailable"
	case sc == nil:
		a.setDockerOK(true)
		row.state = "not_created"
	case err != nil:
		a.setDockerOK(true)
		row.state = "stopped"
	case c.State.Running:
		a.setDockerOK(true)
		if st, err := a.docker.ContainerStats(ctx, c.ID); err == nil {
			a.mu.Lock()
			row.cpu = cpuPercent(a.prevCPU, &st)
			a.prevCPU = &st
			a.mu.Unlock()
			used, limit := int64(st.MemoryUsed()), int64(st.MemoryStats.Limit)
			row.mem, row.memLimit = &used, &limit
			res.CPUPercent, res.MemBytes, res.MemLimitBytes = row.cpu, row.mem, row.memLimit
		}
		a.mu.Lock()
		phase := a.runPhase
		a.mu.Unlock()
		pingAddr := a.opts.PingAddr
		if pingAddr == "" {
			pingAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(a.cfg.GamePort))
		}
		if st, err := minecraft.Ping(pingAddr, 3*time.Second); err == nil {
			reachable = true
			snap = &api.PlayerSnapshot{Online: st.Online, Max: st.Max, Names: st.Sample, Source: "status ping", At: now}
		}
		if phase == api.PhaseOnline {
			if out, err := a.rconCommand("list"); err == nil {
				if on, max, names, ok := minecraft.ParseList(out); ok {
					sort.Strings(names)
					snap = &api.PlayerSnapshot{Online: on, Max: max, Names: names, Source: "rcon list", At: now}
					a.reconcileWithList(now, names)
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
		a.setDockerOK(true)
		a.mu.Lock()
		if a.crashed {
			row.state = "crashed"
		} else {
			row.state = "stopped"
		}
		a.prevCPU = nil
		a.mu.Unlock()
	}
	if snap != nil && snap.Names == nil {
		snap.Names = []string{}
	}
	a.mu.Lock()
	a.resources = res
	a.players = snap
	a.reachable = reachable
	if reachable {
		a.reachableAt = now
	}
	a.mu.Unlock()
	_, err = a.db.Exec(`INSERT OR REPLACE INTO samples(ts, state, players_online, players_max, cpu_pct, mem_bytes, mem_limit, disk_free) VALUES(?,?,?,?,?,?,?,?)`,
		now.UnixMilli(), row.state, row.online, row.max, row.cpu, row.mem, row.memLimit, row.diskFree)
	if err != nil {
		a.log.Error("sample insert failed", "err", err)
	}
	if _, ok, _ := a.kvGet(kvCollectingSince); !ok {
		_ = a.kvSet(kvCollectingSince, now.Format(time.RFC3339Nano))
	}
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

// rconCommand sends one console command over the private Docker bridge.
func (a *Agent) rconCommand(cmd string) (string, error) {
	a.rconMu.Lock()
	defer a.rconMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if a.rcon == nil {
			if err := a.dialRCON(); err != nil {
				return "", err
			}
		}
		out, err := a.rcon.Command(cmd, 10*time.Second)
		if err == nil {
			return out, nil
		}
		a.rcon.Close()
		a.rcon = nil
		if attempt == 1 {
			return "", err
		}
	}
	return "", errors.New("rcon unavailable")
}

func (a *Agent) dialRCON() error {
	c, err := a.docker.ContainerInspect(a.ctx, containerName)
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
	pass, err := a.rconPassword()
	if err != nil {
		return err
	}
	r, err := minecraft.DialRCON(a.opts.RCONAddr(n.IPAddress), pass, 5*time.Second)
	if err != nil {
		return err
	}
	a.rcon = r
	a.rconIP = n.IPAddress
	return nil
}

func (a *Agent) resetRCON() {
	a.rconMu.Lock()
	if a.rcon != nil {
		a.rcon.Close()
		a.rcon = nil
	}
	a.rconMu.Unlock()
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
		{`DELETE FROM samples WHERE ts NOT IN (SELECT ts FROM samples ORDER BY ts DESC LIMIT ?)`, []any{r.MaxSamples}},
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
