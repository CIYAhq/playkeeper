package agent

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diagnose"
	"github.com/CIYAhq/playkeeper/internal/gamefiles"
)

const (
	// lagWindow is the stretch of recent time "How it's running" explains.
	lagWindow = 10 * time.Minute

	// The JVM logs its garbage collection pauses to logs/gc.log: the history
	// behind the lag and memory advice. labelGCLog marks containers created
	// with the flag.
	gcLogFlag    = "-Xlog:gc:file=/data/logs/gc.log:time,uptime,level,tags:filecount=2,filesize=5M"
	gcLogRel     = "logs/gc.log"
	labelGCLog   = "io.playkeeper.gclog"
	gcLogVersion = "1"

	// gcWindow is the period one gc_windows row sums up; rows are kept for
	// gcKeep, the history AdviseMemory reads.
	gcWindow = 15 * time.Minute
	gcKeep   = 14 * 24 * time.Hour
	// gcReadLimit bounds one read of the log; a backlog takes a few samples.
	gcReadLimit = 1 << 20

	// chunkFileLimit bounds the region files one chunk count opens, and the
	// entries it lists in one folder.
	chunkFileLimit = 20000
)

// lagState is what the sampler keeps between samples to explain lag,
// guarded by server.mu.
type lagState struct {
	diagnosis   *diagnose.LagDiagnosis
	at          time.Time
	behindSince time.Time
	players     *int
	chunks      []chunkCount
	gc          []diagnose.GCEvent
}

type chunkCount struct {
	at    time.Time
	count int
}

// cpuSnapshot is one reading of the machine's /proc/stat counters.
type cpuSnapshot struct {
	at    time.Time
	times diagnose.CPUTimes
}

func serverTypeOf(sc api.ServerConfig) string {
	if sc.Type == "" {
		return api.TypePaper
	}
	return sc.Type
}

// readTicks asks the server how fast it ticks, with the commands its type
// and version understand.
func (s *server) readTicks(sc api.ServerConfig) (diagnose.TickStats, bool) {
	replies := map[string]string{}
	for _, c := range diagnose.TickCommands(serverTypeOf(sc), sc.MinecraftVersion) {
		if out, err := s.rconCommand(c); err == nil {
			replies[c] = out
		}
	}
	return diagnose.ReadTicks(replies)
}

// recordCPU keeps /proc/stat snapshots reaching back one lag window, and the
// machine's CPU use since the previous snapshot.
func (a *Agent) recordCPU(now time.Time, cur diagnose.CPUTimes) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.hostTimes); n > 0 {
		if u, ok := cur.Since(a.hostTimes[n-1].times); ok {
			v := u.BusyPercent
			a.hostCPU = &v
		} else {
			a.hostTimes = nil
		}
	}
	a.hostTimes = append(a.hostTimes, cpuSnapshot{now, cur})
	for len(a.hostTimes) > 2 && now.Sub(a.hostTimes[1].at) >= lagWindow {
		a.hostTimes = a.hostTimes[1:]
	}
}

// hostUsage is the machine's CPU use over the lag window, or over as much of
// it as the agent has seen, once that is a minute.
func (a *Agent) hostUsage(now time.Time) *diagnose.CPUUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := len(a.hostTimes)
	if n < 2 || now.Sub(a.hostTimes[0].at) < time.Minute {
		return nil
	}
	u, ok := a.hostTimes[n-1].times.Since(a.hostTimes[0].times)
	if !ok {
		return nil
	}
	return &u
}

// serverCPU is the container's CPU use averaged over the lag window's online
// samples; 100 is one core.
func (s *server) serverCPU(now time.Time) *float64 {
	var avg sql.NullFloat64
	var n int
	err := s.db.QueryRow(`SELECT AVG(cpu_pct), COUNT(cpu_pct) FROM samples WHERE server_id = ? AND ts > ? AND ts <= ? AND state = 'online'`,
		s.id, now.Add(-lagWindow).UnixMilli(), now.UnixMilli()).Scan(&avg, &n)
	if err != nil || !avg.Valid || n < 2 {
		return nil
	}
	v := avg.Float64
	return &v
}

// countChunks adds up the chunks in the world's region files: every region
// folder below its dimension folders, as Paper 26.1
// (world/dimensions/minecraft/the_nether/region), older Paper
// (world_nether/DIM-1/region) and vanilla (world/DIM-1/region) lay them out.
// Entities and poi folders hold other data in the same format.
func (s *server) countChunks(level string) (int, bool) {
	d, err := s.gameFiles()
	if err != nil {
		return 0, false
	}
	defer d.Close()
	c := chunkCounter{d: d}
	for _, dir := range []string{level, level + "_nether", level + "_the_end"} {
		c.walk(dir)
	}
	return c.total, true
}

// chunkCounter walks a world's folders through internal/gamefiles, which
// neither follows a link the game put in place of a folder or file nor waits
// on a named pipe, reading at most chunkFileLimit region headers.
type chunkCounter struct {
	d            *gamefiles.Dir
	total, files int
}

func (c *chunkCounter) walk(dir string) {
	entries, err := c.d.ReadDir(dir, chunkFileLimit)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := dir + "/" + e.Name()
		switch {
		case c.files >= chunkFileLimit:
			return
		case e.IsDir():
			if strings.Count(p, "/") <= 6 {
				c.walk(p)
			}
		case e.Type().IsRegular() && path.Base(dir) == "region" && strings.HasSuffix(p, ".mca"):
			c.files++
			if b, _, err := c.d.ReadRange(p, 0, 4096); err == nil {
				c.total += diagnose.CountChunks(b)
			}
		}
	}
}

func (s *server) recordChunks(now time.Time, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lag.chunks = append(s.lag.chunks, chunkCount{now, count})
	for len(s.lag.chunks) > 0 && now.Sub(s.lag.chunks[0].at) > 3*lagWindow {
		s.lag.chunks = s.lag.chunks[1:]
	}
}

// newChunks is how many chunks the world gained over about the lag window,
// from the counts taken every few minutes; nil until two counts span at
// least half of it.
func (s *server) newChunks(now time.Time) *int {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.lag.chunks
	if len(c) < 2 || now.Sub(c[len(c)-1].at) > 2*worldEvery {
		return nil
	}
	last, base := c[len(c)-1], -1
	for i := len(c) - 2; i >= 0; i-- {
		base = i
		if last.at.Sub(c[i].at) >= lagWindow {
			break
		}
	}
	if last.at.Sub(c[base].at) < lagWindow/2 {
		return nil
	}
	n := max(last.count-c[base].count, 0)
	return &n
}

// gcCursor is how far the agent has read the GC log. A different inode is a
// rotated or new log, read from its start.
type gcCursor struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
}

func (s *server) loadGCCursor() gcCursor {
	var c gcCursor
	var v string
	if s.db.QueryRow(`SELECT gc_cursor FROM servers WHERE id = ?`, s.id).Scan(&v) == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

// readGCLog folds the pauses logged since the last read into the recent
// events ExplainLag reads and the stored windows AdviseMemory reads. Only
// complete lines are read. The game can write anything to the file, or put a
// link or a named pipe in its place, so it is read through internal/gamefiles,
// and pauses dated outside the last day are dropped: they would only add rows.
func (s *server) readGCLog(now time.Time) {
	d, err := s.gameFiles()
	if err != nil {
		return
	}
	defer d.Close()
	cur := s.loadGCCursor()
	buf, st, err := d.ReadRange(gcLogRel, cur.Offset, gcReadLimit)
	if err != nil {
		return
	}
	if ino, _ := fileInode(st); cur.Inode != ino || st.Size() < cur.Offset {
		cur = gcCursor{Inode: ino}
		buf, st, err = d.ReadRange(gcLogRel, 0, gcReadLimit)
		if err != nil {
			return
		}
		if again, _ := fileInode(st); again != ino {
			return
		}
	}
	end := bytes.LastIndexByte(buf, '\n')
	if end < 0 {
		if len(buf) < gcReadLimit {
			return
		}
		end = len(buf) - 1
	}
	s.mu.Lock()
	jvmStart := s.runStartedAt
	s.mu.Unlock()
	var events []diagnose.GCEvent
	for _, line := range strings.Split(string(buf[:end]), "\n") {
		e, ok := diagnose.ParseGCLine(strings.TrimSuffix(line, "\r"), jvmStart)
		if ok && e.At.After(now.Add(-24*time.Hour)) && e.At.Before(now.Add(time.Minute)) {
			events = append(events, e)
		}
	}
	cur.Offset += int64(end + 1)
	if err := s.storeGC(events, cur); err != nil {
		s.log.Error("storing GC windows failed", "err", err)
		return
	}
	s.mu.Lock()
	s.lag.gc = recentGC(append(s.lag.gc, events...), now)
	s.mu.Unlock()
}

// recentGC keeps the pauses of the last lag window, at most 4,096.
func recentGC(events []diagnose.GCEvent, now time.Time) []diagnose.GCEvent {
	i := 0
	for i < len(events) && events[i].At.Before(now.Add(-lagWindow)) {
		i++
	}
	events = events[i:]
	if len(events) > 4096 {
		events = events[len(events)-4096:]
	}
	return slices.Clone(events)
}

// storeGC adds events to their windows and moves the cursor past them, in
// one transaction so an agent restart neither loses nor counts them twice.
func (s *server) storeGC(events []diagnose.GCEvent, cur gcCursor) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, w := range diagnose.SummarizeGC(events, gcWindow) {
		var old diagnose.GCWindow
		err := tx.QueryRow(`SELECT collections, min_after_mb, max_after_mb, heap_mb, full_gcs, evacuation_failures, pause_ms, max_pause_ms FROM gc_windows WHERE server_id = ? AND start = ?`,
			s.id, w.Start.UnixMilli()).Scan(&old.Collections, &old.MinAfterMB, &old.MaxAfterMB, &old.HeapMB, &old.FullGCs, &old.EvacuationFailures, &old.PauseMS, &old.MaxPauseMS)
		switch {
		case err == nil:
			w = mergeGCWindows(old, w)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO gc_windows(server_id, start, collections, min_after_mb, max_after_mb, heap_mb, full_gcs, evacuation_failures, pause_ms, max_pause_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			s.id, w.Start.UnixMilli(), w.Collections, w.MinAfterMB, w.MaxAfterMB, w.HeapMB, w.FullGCs, w.EvacuationFailures, w.PauseMS, w.MaxPauseMS); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(cur)
	if _, err := tx.Exec(`UPDATE servers SET gc_cursor = ? WHERE id = ?`, string(b), s.id); err != nil {
		return err
	}
	return tx.Commit()
}

// mergeGCWindows adds window b's pauses to a, as GCWindow.Add would have.
func mergeGCWindows(a, b diagnose.GCWindow) diagnose.GCWindow {
	out := b
	out.MinAfterMB, out.MaxAfterMB = a.MinAfterMB, a.MaxAfterMB
	if b.MaxAfterMB > 0 {
		if out.MaxAfterMB == 0 || b.MinAfterMB < out.MinAfterMB {
			out.MinAfterMB = b.MinAfterMB
		}
		out.MaxAfterMB = max(out.MaxAfterMB, b.MaxAfterMB)
	}
	out.HeapMB = max(a.HeapMB, b.HeapMB)
	out.Collections = a.Collections + b.Collections
	out.FullGCs = a.FullGCs + b.FullGCs
	out.EvacuationFailures = a.EvacuationFailures + b.EvacuationFailures
	out.PauseMS = a.PauseMS + b.PauseMS
	out.MaxPauseMS = max(a.MaxPauseMS, b.MaxPauseMS)
	return out
}

// gcWindows are the stored windows that start at or after from, oldest first.
func (s *server) gcWindows(from time.Time) ([]diagnose.GCWindow, error) {
	rows, err := s.db.Query(`SELECT start, collections, min_after_mb, max_after_mb, heap_mb, full_gcs, evacuation_failures, pause_ms, max_pause_ms FROM gc_windows WHERE server_id = ? AND start >= ? ORDER BY start`,
		s.id, from.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []diagnose.GCWindow
	for rows.Next() {
		var w diagnose.GCWindow
		var start int64
		if err := rows.Scan(&start, &w.Collections, &w.MinAfterMB, &w.MaxAfterMB, &w.HeapMB, &w.FullGCs, &w.EvacuationFailures, &w.PauseMS, &w.MaxPauseMS); err != nil {
			return nil, err
		}
		w.Start = time.UnixMilli(start).UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}

// distances reads view-distance and simulation-distance from
// server.properties (0 when unknown).
func (s *server) distances() (view, simulation int) {
	d, err := s.gameFiles()
	if err != nil {
		return 0, 0
	}
	defer d.Close()
	b, err := d.ReadProperties()
	if err != nil {
		return 0, 0
	}
	return diagnose.ParseDistances(b)
}

// updateLag works out how the server has run over the lag window. The
// sampler calls it after every sample while the server is online.
func (s *server) updateLag(now time.Time, sc api.ServerConfig, ticks *diagnose.TickStats, players *int) diagnose.LagDiagnosis {
	var console []diagnose.ConsoleLine
	for _, l := range s.console.window(now.Add(-lagWindow)) {
		console = append(console, diagnose.ConsoleLine{At: l.TS, Text: l.Text})
	}
	view, sim := s.distances()
	_, _, maxMB := s.memoryFor(s.id)
	s.mu.Lock()
	gc := slices.Clone(s.lag.gc)
	s.mu.Unlock()
	d := diagnose.ExplainLag(diagnose.LagInput{
		Now: now, Window: lagWindow, ServerType: serverTypeOf(sc), Ticks: ticks, Console: console,
		ServerCPU: s.serverCPU(now), HostCores: numCPU(), HostCPU: s.hostUsage(now),
		GC: gc, BudgetMB: sc.MemoryMB, HostMB: s.opts.HostMemoryMB(), RoomMB: max(maxMB-sc.MemoryMB, 0),
		Players: players, NewChunks: s.newChunks(now), ViewDistance: view, SimulationDistance: sim,
	})
	var since time.Time
	if d.Status == diagnose.LagBitBehind || d.Status == diagnose.LagLagging {
		target := 20.0
		if ticks != nil && ticks.TargetTPS > 0 {
			target = ticks.TargetTPS
		}
		since = s.behindSince(now, 0.95*target)
	}
	s.mu.Lock()
	s.lag.diagnosis, s.lag.at, s.lag.behindSince, s.lag.players = &d, now, since, players
	s.mu.Unlock()
	return d
}

func (s *server) clearLag() {
	s.mu.Lock()
	s.lag.diagnosis = nil
	s.mu.Unlock()
}

// behindSince is when the current stretch of online samples below full speed
// began, looking back a day; zero when the samples are at full speed.
func (s *server) behindSince(now time.Time, fullTPS float64) time.Time {
	from := now.Add(-24 * time.Hour).UnixMilli()
	var good, first sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(ts) FROM samples WHERE server_id = ? AND ts >= ? AND (state != 'online' OR tps >= ?)`, s.id, from, fullTPS).Scan(&good)
	if good.Valid {
		from = good.Int64 + 1
	}
	_ = s.db.QueryRow(`SELECT MIN(ts) FROM samples WHERE server_id = ? AND ts >= ? AND state = 'online' AND tps < ?`, s.id, from, fullTPS).Scan(&first)
	if !first.Valid {
		return time.Time{}
	}
	return time.UnixMilli(first.Int64).UTC()
}

// Running is "How it's running": the sampler's latest lag diagnosis while it
// is fresh.
func (s *server) Running() api.Running {
	s.mu.Lock()
	l := s.lag
	s.mu.Unlock()
	out := api.Running{WindowMinutes: int(lagWindow / time.Minute), Evidence: []api.DiagnosisEvidence{}, Causes: []api.LagCause{}}
	if l.diagnosis == nil || s.now().Sub(l.at) > 3*s.opts.SampleInterval+5*time.Second {
		out.Status, out.Params = string(diagnose.LagUnknown), map[string]any{"running": false}
		out.Title = "Not running"
		out.Explanation = "Playkeeper measures how the server runs while it is online."
		return out
	}
	d := *l.diagnosis
	at := l.at
	out.Status, out.Params, out.Title, out.Explanation = string(d.Status), d.Params, d.Title, d.Explanation
	out.Evidence, out.At, out.Players = apiEvidence(d.Evidence), &at, l.players
	if !l.behindSince.IsZero() {
		t := l.behindSince
		out.BehindSince = &t
	}
	for _, c := range d.Causes {
		out.Causes = append(out.Causes, api.LagCause{Kind: string(c.Kind), Params: c.Params, Score: c.Score, Title: c.Title,
			Explanation: c.Explanation, Evidence: apiEvidence(c.Evidence), Actions: apiActions(c.Actions)})
	}
	return out
}

func (s *server) hRunning(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Running())
}

func apiEvidence(ev []diagnose.Evidence) []api.DiagnosisEvidence {
	out := []api.DiagnosisEvidence{}
	for _, e := range ev {
		out = append(out, api.DiagnosisEvidence{Kind: string(e.Kind), Params: e.Params, Text: e.Text})
	}
	return out
}

func apiActions(acts []diagnose.Action) []api.DiagnosisAction {
	out := []api.DiagnosisAction{}
	for _, a := range acts {
		out = append(out, api.DiagnosisAction{Kind: string(a.Kind), Params: a.Params, Title: a.Title, Recommended: a.Recommended})
	}
	return out
}
