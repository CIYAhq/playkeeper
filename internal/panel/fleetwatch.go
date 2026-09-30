package panel

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Watching the fleet (the fleet plan's section 5): a dashboard with a fleet,
// one that sells on Whop or has joined machines, has its Discord told when a
// joined machine has been off the dashboard for fleetOffAfter, and when it's
// back; when the machines have room for fewer than fleetRoomLow servers of
// the smallest paid plan on sale, counted once across every store; when a
// machine's disk passes fleetDiskFull; when a machine's busiest hour used
// more than fleetCPUBusy of its CPU fleetCPUDays days running; when a
// server's ticks took more than fleetSlowMSPT for fleetSlowFor while players
// were on; and when a machine's customers' plans set aside more memory than
// it has, as when a plan grew past what it can hold. The dashboard only says
// so: buying a machine stays the owner's.

const (
	fleetWatchEvery = time.Minute
	fleetOffAfter   = 5 * time.Minute
	fleetRoomLow    = 2
	// fleetDiskFull is the share of a disk, in percent, past which it's
	// posted, and fleetDiskClear the share it must fall below before it's
	// posted again.
	fleetDiskFull  = 75
	fleetDiskClear = 70
	fleetCPUBusy   = 70
	fleetCPUDays   = 3
	// fleetCPUKept is how many days of busiest hours are kept.
	fleetCPUKept   = 14
	fleetSlowMSPT  = 50
	fleetSlowFor   = 10 * time.Minute
	fleetSlowQuiet = 24 * time.Hour
)

// fleetAskTimeout bounds each question the watch asks a machine, so one
// that's connected but doesn't answer holds up no other part of the look.
var fleetAskTimeout = machineTimeout

// fleetWatch is what the watch keeps between looks.
type fleetWatch struct {
	mu sync.Mutex
	// off is when each joined machine was first seen off the dashboard, and
	// offPosted whether that was posted.
	off       map[string]time.Time
	offPosted map[string]bool
	// low says room for fewer than fleetRoomLow was posted.
	low bool
	// full says which machines' disks were posted as filling up.
	full map[string]bool
	// hours adds up each machine's CPU over the hour under way.
	hours map[string]cpuHour
	// slow is each lagging server's run, by machine and server, and
	// slowPosted when it was last posted.
	slow       map[string]slowRun
	slowPosted map[string]time.Time
	// over says which machines were posted as overbooked.
	over map[string]bool
}

// cpuHour is a machine's CPU samples over one hour.
type cpuHour struct {
	hour time.Time
	sum  float64
	n    int
}

// endedHour is a machine's hour that ended, with its average CPU.
type endedHour struct {
	m    machine
	hour time.Time
	avg  float64
}

// slowRun is a server's samples since its ticks first took too long with
// players on.
type slowRun struct {
	since time.Time
	sum   float64
	n     int
}

// setIn sets m[k] to v, making m first.
func setIn[K comparable, V any](m *map[K]V, k K, v V) {
	if *m == nil {
		*m = map[K]V{}
	}
	(*m)[k] = v
}

// runFleetWatch watches the fleet every fleetWatchEvery until ctx ends.
func (s *Server) runFleetWatch(ctx context.Context) {
	t := time.NewTicker(fleetWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.watchFleet(ctx)
		}
	}
}

// watchFleet looks at the fleet once and posts what changed.
func (s *Server) watchFleet(ctx context.Context) {
	list, err := s.machines()
	if err != nil {
		s.log.Warn("could not list the machines to watch", "err", err)
		return
	}
	// A look that can't read the plans can't tell whether there's a fleet,
	// so it's skipped, and what the watch kept stays for the next.
	plans, err := s.sales.SalePlans(ctx)
	if err != nil {
		s.log.Warn("could not read the plans on sale to watch the fleet", "err", err)
		return
	}
	if len(plans) == 0 && !slices.ContainsFunc(list, func(m machine) bool { return m.Kind == remoteKind }) {
		s.forgetFleet()
		return
	}
	now := s.now()
	var posts []api.DiscordNotifyRequest
	var ended []endedHour
	for _, m := range list {
		online := m.Kind == localKind || s.hub != nil && s.hub.Connected(m.ID)
		if m.Kind == remoteKind {
			posts = append(posts, s.watchOnline(m, online, now)...)
		}
		if !online {
			continue
		}
		ask := func(path string, out any) error {
			ctx, cancel := context.WithTimeout(ctx, fleetAskTimeout)
			defer cancel()
			return askAgent(ctx, m, http.MethodGet, path, nil, out)
		}
		var live api.Machine
		if err := ask("/v1/machine", &live); err == nil {
			posts = append(posts, s.watchDisk(m, live)...)
			if h, ok := s.watchCPU(m, live, now); ok {
				ended = append(ended, h)
			}
		}
		var servers []api.ServerStatus
		if err := ask("/v1/servers", &servers); err == nil {
			posts = append(posts, s.watchTicks(m, servers, now)...)
		}
	}
	for _, h := range ended {
		posts = append(posts, s.keepBusiestHour(ctx, h)...)
	}
	rooms, waiting, err := s.roomsNow(ctx)
	if err != nil {
		s.log.Warn("could not work out the machines' room to watch it", "err", err)
	} else {
		posts = append(posts, s.watchOverbooked(rooms)...)
		if len(plans) > 0 {
			posts = append(posts, s.watchRoom(plans, rooms, waiting)...)
		}
	}
	for _, p := range posts {
		s.postFleet(ctx, p)
	}
}

// forgetFleet forgets what the watch kept, as a dashboard without a fleet
// watches nothing.
func (s *Server) forgetFleet() {
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	f.off, f.offPosted, f.low, f.full, f.hours, f.slow, f.slowPosted, f.over = nil, nil, false, nil, nil, nil, nil, nil
}

// watchOnline posts joined machine m off the dashboard once it's been off
// for fleetOffAfter, and back once it's online again after that.
func (s *Server) watchOnline(m machine, online bool, now time.Time) []api.DiscordNotifyRequest {
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	since, off := f.off[m.ID]
	switch {
	case online && off:
		posted := f.offPosted[m.ID]
		delete(f.off, m.ID)
		delete(f.offPosted, m.ID)
		if posted {
			return []api.DiscordNotifyRequest{{Kind: api.DiscordMachineBack, Machine: machineLabel(m), Minutes: int(now.Sub(since) / time.Minute)}}
		}
	case !online && !off:
		setIn(&f.off, m.ID, now)
	case !online && !f.offPosted[m.ID] && now.Sub(since) >= fleetOffAfter:
		setIn(&f.offPosted, m.ID, true)
		return []api.DiscordNotifyRequest{{Kind: api.DiscordMachineOff, Machine: machineLabel(m), Minutes: int(now.Sub(since) / time.Minute)}}
	}
	return nil
}

// watchDisk posts m's disk once it's more than fleetDiskFull full, and
// again only after it was less than fleetDiskClear.
func (s *Server) watchDisk(m machine, live api.Machine) []api.DiscordNotifyRequest {
	if live.DiskFreeBytes == nil || live.DiskTotalBytes == nil || *live.DiskTotalBytes <= 0 {
		return nil
	}
	full := int(100 - *live.DiskFreeBytes*100 / *live.DiskTotalBytes)
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case full > fleetDiskFull && !f.full[m.ID]:
		setIn(&f.full, m.ID, true)
		return []api.DiscordNotifyRequest{{Kind: api.DiscordDiskFilling, Machine: machineLabel(m), Percent: full}}
	case full < fleetDiskClear:
		delete(f.full, m.ID)
	}
	return nil
}

// watchCPU adds m's CPU now to the hour under way, and returns the hour it
// ended, if a new one began.
func (s *Server) watchCPU(m machine, live api.Machine, now time.Time) (endedHour, bool) {
	if live.CPUPercent == nil {
		return endedHour{}, false
	}
	hour := now.UTC().Truncate(time.Hour)
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	h := f.hours[m.ID]
	var done endedHour
	ended := h.n > 0 && !h.hour.Equal(hour)
	if ended {
		done, h = endedHour{m: m, hour: h.hour, avg: h.sum / float64(h.n)}, cpuHour{}
	}
	if h.n == 0 {
		h.hour = hour
	}
	h.sum += *live.CPUPercent
	h.n++
	setIn(&f.hours, m.ID, h)
	return done, ended
}

// keepBusiestHour keeps h as its day's busiest hour on its machine if it was
// busier than the others, and posts the machine once its busiest hour used
// more than fleetCPUBusy of its CPU fleetCPUDays days running, none of which
// was posted.
func (s *Server) keepBusiestHour(ctx context.Context, h endedHour) []api.DiscordNotifyRequest {
	const layout = "2006-01-02"
	var days []string
	for i := fleetCPUDays - 1; i >= 0; i-- {
		days = append(days, h.hour.AddDate(0, 0, -i).Format(layout))
	}
	today := days[len(days)-1]
	var out []api.DiscordNotifyRequest
	err := s.immediate(ctx, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `INSERT INTO fleet_cpu_days(machine_id, day, peak) VALUES(?,?,?)
			ON CONFLICT(machine_id, day) DO UPDATE SET peak = max(peak, excluded.peak)`, h.m.ID, today, h.avg); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `DELETE FROM fleet_cpu_days WHERE machine_id = ? AND day < ?`, h.m.ID, h.hour.AddDate(0, 0, -fleetCPUKept).Format(layout)); err != nil {
			return err
		}
		rows, err := c.QueryContext(ctx, `SELECT day, peak, posted FROM fleet_cpu_days WHERE machine_id = ? AND day IN (`+strings.Repeat("?,", len(days)-1)+`?)`,
			append([]any{h.m.ID}, anys(days)...)...)
		if err != nil {
			return err
		}
		busy, posted, peak := 0, false, 0.0
		for rows.Next() {
			var day string
			var p float64
			var was bool
			if err := rows.Scan(&day, &p, &was); err != nil {
				rows.Close()
				return err
			}
			if p > fleetCPUBusy {
				busy++
			}
			if day == today {
				peak = p
			}
			posted = posted || was
		}
		rows.Close()
		if err := rows.Err(); err != nil || busy < fleetCPUDays || posted {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE fleet_cpu_days SET posted = 1 WHERE machine_id = ? AND day = ?`, h.m.ID, today); err != nil {
			return err
		}
		out = append(out, api.DiscordNotifyRequest{Kind: api.DiscordBusyCPU, Machine: machineLabel(h.m), Percent: int(math.Round(peak)), Days: fleetCPUDays})
		return nil
	})
	if err != nil {
		s.log.Warn("could not keep a machine's busiest hour", "machine", h.m.ID, "err", err)
		return nil
	}
	return out
}

// anys is ss as the arguments of a query.
func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// watchTicks posts each of m's servers whose ticks took more than
// fleetSlowMSPT for fleetSlowFor while players were on, at most once each
// fleetSlowQuiet.
func (s *Server) watchTicks(m machine, servers []api.ServerStatus, now time.Time) []api.DiscordNotifyRequest {
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.DiscordNotifyRequest
	seen := map[string]bool{}
	for _, st := range servers {
		key := m.ID + "/" + st.ID
		seen[key] = true
		players, mspt := 0, 0.0
		if st.Players != nil {
			players = st.Players.Online
		}
		if st.Resources != nil && st.Resources.MSPT != nil {
			mspt = *st.Resources.MSPT
		}
		if players == 0 || mspt <= fleetSlowMSPT {
			delete(f.slow, key)
			continue
		}
		r, ok := f.slow[key]
		if !ok {
			r = slowRun{since: now}
		}
		r.sum += mspt
		r.n++
		setIn(&f.slow, key, r)
		if last := f.slowPosted[key]; now.Sub(r.since) >= fleetSlowFor && (last.IsZero() || now.Sub(last) >= fleetSlowQuiet) {
			setIn(&f.slowPosted, key, now)
			out = append(out, api.DiscordNotifyRequest{Kind: api.DiscordSlowTicks, ServerName: st.Name, Machine: machineLabel(m),
				MSPT: int(math.Round(r.sum / float64(r.n))), Players: players, Minutes: int(now.Sub(r.since) / time.Minute)})
		}
	}
	for key := range f.slow {
		if strings.HasPrefix(key, m.ID+"/") && !seen[key] {
			delete(f.slow, key)
		}
	}
	return out
}

// watchRoom posts the machines having room for fewer than fleetRoomLow
// servers of the smallest paid plan on sale, counted once across every
// store after the customers waiting for room get theirs, and again only
// after there was room for fleetRoomLow.
func (s *Server) watchRoom(plans []SalePlan, rooms []machineRoom, waiting []int) []api.DiscordNotifyRequest {
	size := 0
	for _, p := range plans {
		if !p.Free && p.MemoryMB > 0 && (size == 0 || p.MemoryMB < size) {
			size = p.MemoryMB
		}
	}
	if size == 0 {
		return nil
	}
	free := slices.Clone(rooms)
	left := 0
	for _, mb := range waiting {
		if mb > 0 && !give(free, mb) {
			left++
		}
	}
	room := 0
	for _, r := range free {
		if r.Takes {
			room += max(r.FreeMB, 0) / size
		}
	}
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case room < fleetRoomLow && !f.low:
		f.low = true
		return []api.DiscordNotifyRequest{{Kind: api.DiscordLowRoom, Room: room, MemoryMB: size, Waiting: left}}
	case room >= fleetRoomLow:
		f.low = false
	}
	return nil
}

// roomsNow is the machines as placement sees them, each asked within the
// time the watch gives a machine, and the memory each customer waiting for
// room needs, with no customer placed meanwhile. A machine that spends that
// time leaves the rest to read in the look's own.
func (s *Server) roomsNow(ctx context.Context) ([]machineRoom, []int, error) {
	s.placeMu.Lock()
	defer s.placeMu.Unlock()
	rctx, cancel := context.WithTimeout(ctx, fleetAskTimeout)
	defer cancel()
	rooms, err := s.fleetRooms(rctx, 0)
	if err != nil {
		return nil, nil, err
	}
	waiting, err := s.waitingMemory(ctx)
	return rooms, waiting, err
}

// watchOverbooked posts each machine whose customers' plans set aside more
// memory than it has, as when a plan grew past what it can hold, once, and
// again only after it had room for them. A machine that didn't answer says
// nothing either way.
func (s *Server) watchOverbooked(rooms []machineRoom) []api.DiscordNotifyRequest {
	f := &s.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []api.DiscordNotifyRequest
	for _, r := range rooms {
		switch {
		case !r.Answered:
		case r.FreeMB < 0 && !f.over[r.ID]:
			setIn(&f.over, r.ID, true)
			out = append(out, api.DiscordNotifyRequest{Kind: api.DiscordOverbooked, Machine: machineLabel(r.machine), MemoryMB: -r.FreeMB})
		case r.FreeMB >= 0:
			delete(f.over, r.ID)
		}
	}
	return out
}

// postFleet has the dashboard's own agent, which holds the Discord settings
// and words the alert, post req. Discord is optional, so a failure only
// shows in the log.
func (s *Server) postFleet(ctx context.Context, req api.DiscordNotifyRequest) {
	m, err := s.localMachine()
	if err != nil {
		s.log.Info("could not post to Discord about the fleet", "kind", req.Kind, "err", err)
		return
	}
	req.Actor = placementActor
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := m.agent.Do(asActor(ctx, placementActor), "POST", "/v1/discord/notify", nil, req, nil); err != nil {
		s.log.Info("could not post to Discord about the fleet", "kind", req.Kind, "err", err)
	}
}
