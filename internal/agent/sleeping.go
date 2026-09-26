package agent

// Wave 7 (0.4.0): sleep when nobody's playing. The sampler feeds a tracker;
// when the server has been empty long enough, the "sleep" operation stops it
// and a stand-in answers on its game port. A join attempt from a player who
// may wake it starts the "wake" operation. A sleeping server's desired state
// is "sleeping", so the reconciler never starts it by itself.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/sleep"
)

// standInAddr is where the stand-in listens for a server's game port: every
// address, like the port Docker publishes.
var standInAddr = func(gamePort int) string { return ":" + strconv.Itoa(gamePort) }

const (
	wakeRetry = 2 * time.Second
	// sleepCheck is how often a sleeping server's stand-in is checked, so it
	// answers again after its port was busy.
	sleepCheck     = 30 * time.Second
	standInBind    = 10 * time.Second
	sleepPeriodAge = 90 * 24 * time.Hour
)

// sleepLooks runs as the sleep operation looks again before it stops the
// server; tests act there.
var sleepLooks = func() {}

func (s *server) sleepSettings() sleep.Settings {
	s.auto.mu.Lock()
	if set := s.auto.sleepSet; set != nil {
		defer s.auto.mu.Unlock()
		return *set
	}
	s.auto.mu.Unlock()
	var raw string
	_ = s.db.QueryRow(`SELECT sleep FROM servers WHERE id = ?`, s.id).Scan(&raw)
	var set sleep.Settings
	_ = json.Unmarshal([]byte(raw), &set)
	s.auto.mu.Lock()
	s.auto.sleepSet = &set
	s.auto.mu.Unlock()
	return set
}

// standIn is the server's stand-in, made on first use.
func (s *server) standIn() (*sleep.Manager, error) {
	s.auto.mu.Lock()
	defer s.auto.mu.Unlock()
	if s.auto.standIn != nil {
		return s.auto.standIn, nil
	}
	m, err := sleep.NewManager(sleep.Config{
		Addr:        standInAddr(s.gamePort),
		OnWake:      s.wakeFor,
		Admit:       s.mayWake,
		Now:         s.now,
		BindTimeout: standInBind,
		Logf: func(format string, args ...any) {
			s.log.Info(fmt.Sprintf(format, args...), "server", s.id)
		},
	})
	if err != nil {
		return nil, err
	}
	s.auto.standIn = m
	return m, nil
}

// mayWake admits the players who may join anyway: nobody banned, and then
// with the allowlist on those on it and operators, with it off anyone. If
// server.properties or the ban list can't be read, the allowlist rule holds.
// Names from the stand-in are only claims.
func (s *server) mayWake(player string) bool {
	if banned, err := s.bannedNames(); err == nil {
		for _, name := range banned {
			if strings.EqualFold(name, player) {
				return false
			}
		}
		if allowlistOff(readProperties(s.dataDir())) {
			return true
		}
	}
	if list, err := s.whitelist(); err == nil {
		for _, e := range list {
			if strings.EqualFold(e.Name, player) {
				return true
			}
		}
	}
	if ops, err := s.operators(); err == nil {
		for _, o := range ops {
			if strings.EqualFold(o.Name, player) {
				return true
			}
		}
	}
	return false
}

// allowlistOff reports whether server.properties, as readProperties read it,
// lets anyone join. Minecraft turns the allowlist on only for white-list=true,
// in any case; nil props, a file that couldn't be read, counts as on.
func allowlistOff(props map[string]string) bool {
	return props != nil && !strings.EqualFold(props["white-list"], "true")
}

// refreshStandIn sets what the stand-in shows: the server's name, version,
// player limit and icon. protocol is 0 when unknown.
func (s *server) refreshStandIn(m *sleep.Manager, version string, protocol int) {
	st := sleep.Status{Name: s.name(), Version: version, Protocol: protocol}
	if sc, _ := s.serverConfig(); sc != nil {
		st.MaxPlayers = sc.MaxPlayers
		if st.Version == "" {
			typ := sc.Type
			if row, err := s.row(); err == nil && row.Type != "" {
				typ = row.Type
			}
			st.Version = typeName(typ) + " " + sc.MinecraftVersion
		}
	}
	st.Icon = s.standInIcon()
	m.SetStatus(st)
}

// standInIcon is the server's list icon for the stand-in, or "" when it has
// none or it can't be shown.
func (s *server) standInIcon() string {
	d, err := s.gameFiles()
	if err != nil {
		return ""
	}
	defer d.Close()
	b, err := d.ReadFile(sleep.IconFile, sleep.MaxIconBytes)
	if err != nil {
		return ""
	}
	icon, _ := sleep.IconURI(b)
	return icon
}

// observeSleep feeds one sample to the sleep tracker and puts an empty
// server to sleep when it is time. A running map pre-generation or a
// scheduled restart's countdown keeps it awake, as an operation does.
func (s *server) observeSleep(now time.Time, state string, snap *api.PlayerSnapshot) {
	set := s.sleepSettings()
	s.mu.Lock()
	startedAt := s.runStartedAt
	s.mu.Unlock()
	o := sleep.Observation{At: now, Running: state == "online", Busy: s.busy() || s.pregenRunning() || s.scheduleWorking(), StartedAt: startedAt}
	if snap != nil {
		o.Players, o.PlayersKnown = snap.Online, true
	}
	s.auto.mu.Lock()
	if s.auto.tracker == nil {
		s.auto.tracker = sleep.NewTracker(set)
	}
	d := s.auto.tracker.Observe(o)
	s.auto.decision = d
	s.auto.mu.Unlock()
	if d.Sleep && s.desired() == api.DesiredRunning {
		s.fallAsleep(set)
	}
}

// nobodyOn asks the server who is on, and is true only when it answers that
// nobody is.
func (s *server) nobodyOn() bool {
	out, err := s.rconCommand("list")
	if err != nil {
		return false
	}
	n, _, _, ok := minecraft.ParseList(out)
	return ok && n == 0
}

// fallAsleep checks the player list and starts the sleep operation, which
// looks again before it stops the server.
func (s *server) fallAsleep(set sleep.Settings) {
	if !s.nobodyOn() {
		return
	}
	m, err := s.standIn()
	if err != nil {
		s.log.Warn("the server can't sleep", "server", s.id, "err", err)
		return
	}
	version, protocol := "", 0
	pingAddr := s.opts.PingAddr
	if pingAddr == "" {
		pingAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(s.gamePort))
	}
	if st, err := minecraft.Ping(pingAddr, 3*time.Second); err == nil {
		version, protocol = st.VersionName, st.Protocol
	}
	s.refreshStandIn(m, version, protocol)
	if _, err := s.beginOp("sleep", "sleep", func(ctx context.Context, h *opHandle) error {
		return s.sleepOp(ctx, h, m, set)
	}); err != nil {
		s.log.Info("the server stays awake for now", "server", s.id, "err", err)
	}
}

// sleepOp puts the server to sleep, unless since the sleep watch decided
// someone joined, a map pre-generation or a scheduled restart's countdown
// began, the server stopped being meant to run or set stopped being its
// sleep setting: then the operation is called off and the server stays
// awake. Saving the setting takes the operation lock too, so the setting
// can't change between this look and the stop.
func (s *server) sleepOp(ctx context.Context, h *opHandle, m *sleep.Manager, set sleep.Settings) error {
	sleepLooks()
	if !s.nobodyOn() || s.pregenRunning() || s.scheduleWorking() {
		h.callOff()
		return nil
	}
	if s.desired() != api.DesiredRunning || s.sleepSettings() != set {
		h.callOff()
		return nil
	}
	if err := s.setDesired(api.DesiredSleeping); err != nil {
		return err
	}
	stopped := false
	err := m.Sleep(ctx, func(ctx context.Context) error {
		if err := s.stopServer(ctx, h); err != nil {
			return err
		}
		stopped = true
		return nil
	})
	if err != nil && !stopped {
		if _, running, rerr := s.containerRunning(context.WithoutCancel(ctx)); rerr == nil && running {
			_ = s.setDesired(api.DesiredRunning)
		}
		return err
	}
	now := s.now().UTC()
	s.startSleepPeriod(now)
	s.recordEvent(now, "server_fell_asleep", "", "playkeeper", strconv.Itoa(int(set.Idle().Minutes())))
	if err != nil {
		// Asleep, but nothing answers on the port yet; the sleep loop tries
		// again.
		return automationError(err)
	}
	h.phase(string(api.PhaseAsleep))
	return nil
}

// wakeFor starts the wake operation for a player who tried to join. While
// another operation runs, such as a scheduled backup, the wake waits for it
// however long it takes, and starts once it ends, unless the server stopped
// being meant to sleep meanwhile. One join's wake waits at a time.
func (s *server) wakeFor(player string) {
	s.auto.mu.Lock()
	if s.auto.wakePending {
		s.auto.mu.Unlock()
		return
	}
	s.auto.wakePending = true
	s.auto.mu.Unlock()
	defer func() {
		s.auto.mu.Lock()
		s.auto.wakePending = false
		s.auto.mu.Unlock()
	}()
	for {
		if s.ctx.Err() != nil || s.desired() != api.DesiredSleeping {
			return
		}
		_, err := s.beginOp("wake", "wake:"+player, s.wakeOp(player))
		if err == nil {
			return
		}
		var ae *apiError
		if !errors.As(err, &ae) || ae.Code != api.CodeBusy {
			s.log.Warn("could not wake the server", "server", s.id, "err", err)
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(wakeRetry):
		}
	}
}

func (s *server) wakeOp(player string) func(ctx context.Context, h *opHandle) error {
	return func(ctx context.Context, h *opHandle) error {
		if s.desired() != api.DesiredSleeping {
			return nil
		}
		m, err := s.standIn()
		if err != nil {
			return err
		}
		h.set("player", player)
		err = m.Wake(ctx, func(ctx context.Context) error {
			s.endSleepPeriod(s.now().UTC(), "wake:"+player)
			if err := s.setDesired(api.DesiredRunning); err != nil {
				return err
			}
			cur, _ := s.serverConfig()
			if cur == nil {
				return errNotCreated()
			}
			return s.startServer(ctx, h, *cur)
		})
		if err != nil {
			// After a failed wake the stand-in is back on the game port, so
			// the server sleeps again unless its container is known to be
			// running. If Docker can't say, the sleep loop looks again. A
			// start that stopped the server for good, as when its software
			// changed, leaves it stopped, with nothing answering for it.
			if d := s.desired(); d != api.DesiredRunning && d != api.DesiredSleeping {
				s.leaveSleep()
			} else if _, running, rerr := s.containerRunning(context.WithoutCancel(ctx)); rerr != nil || !running {
				_ = s.setDesired(api.DesiredSleeping)
				s.startSleepPeriod(s.now().UTC())
			}
			return err
		}
		s.recordEvent(s.now().UTC(), "server_woke_up", player, "playkeeper", "")
		return nil
	}
}

// leaveSleep hands the game port back before the server starts, or when it
// is stopped for good while asleep. Inside a wake the stand-in has already
// let go of the port, so it never waits for the wake.
func (s *server) leaveSleep() {
	s.auto.mu.Lock()
	m := s.auto.standIn
	s.auto.mu.Unlock()
	if m != nil && m.Listening() {
		m.Close()
	}
	s.endSleepPeriod(s.now().UTC(), "")
}

func (s *server) sleepLoop(ctx context.Context) {
	defer func() {
		s.auto.mu.Lock()
		m := s.auto.standIn
		s.auto.mu.Unlock()
		if m != nil {
			m.Close()
		}
	}()
	t := time.NewTicker(sleepCheck)
	defer t.Stop()
	for {
		s.resumeSleep(ctx)
		_, _ = s.db.Exec(`DELETE FROM sleep_periods WHERE server_id = ? AND end_ts IS NOT NULL AND end_ts < ?`, s.id, s.now().Add(-sleepPeriodAge).UnixMilli())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// resumeSleep makes a sleeping server's stand-in answer: when the agent
// starts, and again after its port was busy. A sleeping server found running
// was started outside Playkeeper, so it is awake, and the stand-in lets go.
func (s *server) resumeSleep(ctx context.Context) {
	if s.desired() != api.DesiredSleeping {
		return
	}
	release, ok := s.holdOpLock()
	if !ok {
		return
	}
	defer release()
	if s.desired() != api.DesiredSleeping {
		return
	}
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return
	}
	if running {
		_ = s.setDesired(api.DesiredRunning)
		s.leaveSleep()
		return
	}
	m, err := s.standIn()
	if err != nil || m.Listening() {
		return
	}
	s.refreshStandIn(m, "", 0)
	if err := m.Listen(ctx); err != nil {
		s.log.Warn("nothing answers players while the server sleeps", "server", s.id, "err", err)
		return
	}
	s.startSleepPeriod(s.now().UTC())
}

func (s *server) startSleepPeriod(now time.Time) {
	var open int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sleep_periods WHERE server_id = ? AND end_ts IS NULL`, s.id).Scan(&open)
	if open > 0 {
		return
	}
	_, _ = s.db.Exec(`INSERT INTO sleep_periods(server_id, start_ts) VALUES(?, ?)`, s.id, now.UnixMilli())
}

func (s *server) endSleepPeriod(now time.Time, wokeBy string) {
	_, _ = s.db.Exec(`UPDATE sleep_periods SET end_ts = ?, woke_by = ? WHERE server_id = ? AND end_ts IS NULL`, now.UnixMilli(), wokeBy, s.id)
}

func (s *server) asleepSince() *time.Time {
	var ts sql.NullInt64
	_ = s.db.QueryRow(`SELECT MIN(start_ts) FROM sleep_periods WHERE server_id = ? AND end_ts IS NULL`, s.id).Scan(&ts)
	if !ts.Valid {
		return nil
	}
	t := time.UnixMilli(ts.Int64).UTC()
	return &t
}

// sleepStatus is the server's sleep setting and what it is doing, for its
// status.
func (s *server) sleepStatus(desired string) *api.SleepStatus {
	set := s.sleepSettings()
	st := &api.SleepStatus{Enabled: set.Enabled, IdleMinutes: int(set.Idle().Minutes())}
	s.auto.mu.Lock()
	m, d := s.auto.standIn, s.auto.decision
	s.auto.mu.Unlock()
	if m != nil {
		st.Listening = m.Listening()
	}
	if desired == api.DesiredSleeping {
		st.AsleepSince = s.asleepSince()
	} else if set.Enabled && !d.SleepAt.IsZero() {
		t := d.SleepAt.UTC()
		st.SleepAt = &t
	}
	return st
}

// sleepToday is how often the server fell asleep today in loc, and how long
// it slept today in all, counting a sleep that began before midnight from
// midnight.
func (s *server) sleepToday(loc *time.Location) (int, time.Duration) {
	now := s.now()
	y, m, d := now.In(loc).Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	rows, err := s.db.Query(`SELECT start_ts, end_ts FROM sleep_periods WHERE server_id = ? AND (end_ts IS NULL OR end_ts > ?)`, s.id, midnight.UnixMilli())
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	count, total := 0, time.Duration(0)
	for rows.Next() {
		var start int64
		var end sql.NullInt64
		if rows.Scan(&start, &end) != nil {
			continue
		}
		from, to := time.UnixMilli(start), now
		if end.Valid {
			to = time.UnixMilli(end.Int64)
		}
		if from.Before(midnight) {
			from = midnight
		} else {
			count++
		}
		if to.After(from) {
			total += to.Sub(from)
		}
	}
	return count, total
}

func (s *server) hSleep(w http.ResponseWriter, r *http.Request) {
	loc := time.UTC
	if tz := r.URL.Query().Get("tz"); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil || len(tz) > 64 {
			writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "Unknown time zone.", Field: "timeZone", Reason: "time_zone_invalid"})
			return
		}
		loc = l
	}
	v := sleepView{SleepStatus: *s.sleepStatus(s.desired()), DefaultIdleMinutes: sleep.DefaultIdleMinutes, MinIdleMinutes: sleep.MinIdleMinutes, MaxIdleMinutes: sleep.MaxIdleMinutes}
	count, slept := s.sleepToday(loc)
	v.Today.Count, v.Today.Seconds = count, int(slept.Seconds())
	writeJSON(w, http.StatusOK, v)
}

// sleepView is the Sleep page: the server's sleep setting and what it is
// doing, the idle times it can be set to, and how much it slept today.
type sleepView struct {
	api.SleepStatus
	DefaultIdleMinutes int `json:"defaultIdleMinutes"`
	MinIdleMinutes     int `json:"minIdleMinutes"`
	MaxIdleMinutes     int `json:"maxIdleMinutes"`
	Today              struct {
		Count   int `json:"count"`
		Seconds int `json:"seconds"`
	} `json:"today"`
}

type sleepRequest struct {
	Actor       string `json:"actor"`
	Enabled     bool   `json:"enabled"`
	IdleMinutes int    `json:"idleMinutes,omitempty"`
}

// hSleepSet saves the setting while no other operation runs. Turning it off
// wakes a sleeping server, so then nothing changes unless the server can
// start now.
func (s *server) hSleepSet(w http.ResponseWriter, r *http.Request) {
	var req sleepRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	set := sleep.Settings{Enabled: req.Enabled, IdleMinutes: req.IdleMinutes}
	if err := set.Validate(); err != nil {
		writeError(w, automationError(err))
		return
	}
	op, err := s.saveSleep(set, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := map[string]any{}
	if op != nil {
		resp["operation"] = op
	}
	detail := "off"
	if set.Enabled {
		detail = fmt.Sprintf("after %d minutes with nobody on", int(set.Idle().Minutes()))
	}
	s.audit(actor, "sleep.changed", "server", "succeeded", detail)
	resp["sleep"] = s.sleepStatus(s.desired())
	writeJSON(w, http.StatusOK, resp)
}

// saveSleep saves the setting, then starts a sleeping server when it turns
// sleep off. It holds the operation lock, so no sleep or wake runs while it
// looks and saves; the start takes the lock over.
func (s *server) saveSleep(set sleep.Settings, actor string) (*api.Operation, error) {
	release, ok := s.holdOpLock()
	if !ok {
		return nil, s.busyError()
	}
	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()
	wake := !set.Enabled && s.desired() == api.DesiredSleeping
	if wake {
		if err := s.machineBusy(); err != nil {
			return nil, err
		}
	}
	b, _ := json.Marshal(set)
	if _, err := s.db.Exec(`UPDATE servers SET sleep = ? WHERE id = ?`, string(b), s.id); err != nil {
		return nil, err
	}
	s.auto.mu.Lock()
	s.auto.sleepSet = &set
	if s.auto.tracker != nil {
		s.auto.tracker.SetSettings(set)
	}
	s.auto.decision = sleep.Decision{}
	s.auto.mu.Unlock()
	if !wake {
		return nil, nil
	}
	handedOver = true
	return s.startOp("start", actor, func(ctx context.Context, h *opHandle) error {
		if err := s.setDesired(api.DesiredRunning); err != nil {
			return err
		}
		cur, _ := s.serverConfig()
		if cur == nil {
			return errNotCreated()
		}
		if err := s.startServer(ctx, h, *cur); err != nil {
			s.startFailed(ctx)
			return err
		}
		return nil
	}), nil
}

// sleepingMemoryMB is the memory sleeping servers gave back for now.
func (a *Agent) sleepingMemoryMB() int {
	total := 0
	for _, s := range a.serverList() {
		if s.desired() != api.DesiredSleeping {
			continue
		}
		if sc, _ := s.serverConfig(); sc != nil {
			total += sc.MemoryMB
		}
	}
	return total
}
