package agent

// Wave 7 (0.4.0): schedules. Each server's runner starts with the server's
// loops. Scheduled restarts and backups are ordinary operations with the
// actor schedule:<id>; scheduled console commands go straight over RCON.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup"
	"github.com/CIYAhq/playkeeper/internal/docker"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
	"github.com/CIYAhq/playkeeper/internal/schedule"
)

const (
	maxSchedules = 50
	// scheduleRunAge is how long the "Recent runs" list remembers a run.
	scheduleRunAge = 90 * 24 * time.Hour
)

var reScheduleID = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

func (s *server) scheduleLoop(ctx context.Context) {
	r, err := schedule.NewRunner(schedule.Config{
		ServerID: s.id,
		Load:     s.loadSchedules,
		Save:     s.saveScheduleRun,
		Server:   scheduleServer{s},
		Now:      s.now,
		Logf: func(format string, args ...any) {
			s.log.Info(fmt.Sprintf(format, args...), "server", s.id)
		},
	})
	if err != nil {
		s.log.Error("schedules cannot run", "server", s.id, "err", err)
		return
	}
	s.auto.mu.Lock()
	s.auto.runner = r
	s.auto.mu.Unlock()
	_ = r.Run(ctx)
}

// scheduleWorking reports whether a schedule is restarting the server or
// backing it up, a restart's warnings included, so the server doesn't fall
// asleep before the restart its players were warned of.
func (s *server) scheduleWorking() bool {
	s.auto.mu.Lock()
	r := s.auto.runner
	s.auto.mu.Unlock()
	if r == nil {
		return false
	}
	act, ok := r.Current()
	return ok && (act.Job.Schedule.Kind == schedule.KindRestart || act.Job.Schedule.Kind == schedule.KindBackup)
}

// reloadSchedules makes the runner read the server's schedules again.
func (s *server) reloadSchedules() {
	s.auto.mu.Lock()
	r := s.auto.runner
	s.auto.mu.Unlock()
	if r != nil {
		r.Reload()
	}
}

// scheduleRow is a stored schedule with who made and last changed it.
type scheduleRow struct {
	schedule.Schedule
	CreatedBy string `json:"createdBy"`
	UpdatedBy string `json:"updatedBy"`
}

func (s *server) scheduleRows(ctx context.Context, cond string, args ...any) ([]scheduleRow, error) {
	q := `SELECT id, name, kind, timing, payload, enabled, created_at, updated_at, created_by, updated_by, last_run FROM schedules WHERE server_id = ?`
	if cond != "" {
		q += ` AND (` + cond + `)`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`, append([]any{s.id}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []scheduleRow{}
	for rows.Next() {
		var row scheduleRow
		var kind, timing, payload, lastRun string
		var enabled int
		var created, updated int64
		if err := rows.Scan(&row.ID, &row.Name, &kind, &timing, &payload, &enabled, &created, &updated, &row.CreatedBy, &row.UpdatedBy, &lastRun); err != nil {
			return nil, err
		}
		row.ServerID = s.id
		row.Kind = schedule.Kind(kind)
		row.Enabled = enabled == 1
		row.CreatedAt, row.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
		// A row that can't be read stays in the list with an empty timing,
		// which the runner reports as not valid instead of running it.
		_ = json.Unmarshal([]byte(timing), &row.Timing)
		_ = json.Unmarshal([]byte(payload), &row.Payload)
		if lastRun != "" {
			var run schedule.Run
			if json.Unmarshal([]byte(lastRun), &run) == nil {
				row.LastRun = &run
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *server) loadSchedules(ctx context.Context) ([]schedule.Schedule, error) {
	rows, err := s.scheduleRows(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]schedule.Schedule, len(rows))
	for i, r := range rows {
		out[i] = r.Schedule
	}
	return out, nil
}

// saveScheduleRun records a schedule's latest run on the schedule and in the
// "Recent runs" list. A console command a schedule sent is audited like one
// typed in the Console.
func (s *server) saveScheduleRun(ctx context.Context, id string, run schedule.Run) error {
	b, err := json.Marshal(run)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_run = ? WHERE id = ? AND server_id = ?`, string(b), id, s.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	var kind, payload string
	if err := s.db.QueryRowContext(ctx, `SELECT kind, payload FROM schedules WHERE id = ?`, id).Scan(&kind, &payload); err != nil {
		return err
	}
	var started, finished, players any
	if !run.Started.IsZero() {
		started = run.Started.UnixMilli()
	}
	if !run.Finished.IsZero() {
		finished = run.Finished.UnixMilli()
	}
	if run.Players != nil {
		players = *run.Players
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schedule_runs(schedule_id, server_id, kind, due, started_at, finished_at, result, reason, detail, operation_id, players)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(schedule_id, due) DO UPDATE SET started_at = excluded.started_at, finished_at = excluded.finished_at, result = excluded.result,
			reason = excluded.reason, detail = excluded.detail, operation_id = excluded.operation_id, players = excluded.players`,
		id, s.id, kind, run.Due.UnixMilli(), started, finished, string(run.Result), string(run.Reason), run.Detail, run.OperationID, players); err != nil {
		return err
	}
	if kind == string(schedule.KindCommand) && (run.Result == schedule.ResultSucceeded || run.Result == schedule.ResultFailed) {
		var p schedule.Payload
		_ = json.Unmarshal([]byte(payload), &p)
		s.audit(schedule.Actor(id), "console.command", "server", string(run.Result), minecraft.RedactIPs(p.Command))
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM schedule_runs WHERE server_id = ? AND due < ?`, s.id, s.now().Add(-scheduleRunAge).UnixMilli())
	return err
}

// scheduleServer is what the runner needs from the agent for one server.
type scheduleServer struct{ s *server }

func (ss scheduleServer) State(ctx context.Context) (schedule.ServerState, error) {
	s := ss.s
	_, running, err := s.containerRunning(ctx)
	if err != nil {
		return schedule.ServerState{}, err
	}
	if !running {
		return schedule.ServerState{Sleeping: s.desired() == api.DesiredSleeping}, nil
	}
	s.mu.Lock()
	online := s.runPhase == api.PhaseOnline
	s.mu.Unlock()
	st := schedule.ServerState{Running: online, Busy: s.busy()}
	if !online {
		return st, nil
	}
	if out, err := s.rconCommand("list"); err == nil {
		if n, _, _, ok := minecraft.ParseList(out); ok {
			st.Players, st.PlayersKnown = n, true
		}
	}
	return st, nil
}

func (ss scheduleServer) Command(ctx context.Context, cmd string) (string, error) {
	return ss.s.rconOnce(ctx, cmd)
}

// Run runs a scheduled restart or backup as the server's operation and waits
// for it to end.
func (ss scheduleServer) Run(ctx context.Context, op schedule.Operation) (string, error) {
	s := ss.s
	switch op.Kind {
	case schedule.OpRestart:
		if _, running, err := s.containerRunning(ctx); err != nil {
			return "", err
		} else if !running {
			return "", schedule.ErrNotRunning
		}
	case schedule.OpBackup:
		if op.OnlyIfPlayed && !s.playedSinceLastBackup() {
			return "", schedule.ErrNobodyPlayed
		}
	default:
		return "", fmt.Errorf("unknown scheduled operation %q", op.Kind)
	}
	done := make(chan error, 1)
	started, err := s.beginOp(string(op.Kind), op.Actor, func(ctx context.Context, h *opHandle) (err error) {
		defer func() {
			if v := recover(); v != nil {
				err = fmt.Errorf("internal error: %v", v)
			}
			done <- err
		}()
		h.set("scheduleId", op.ScheduleID)
		if op.Kind == schedule.OpBackup {
			err = s.backupOp(ctx, h, op.Actor, op.Note, false)
			if why := pauseRefusal(err); why != "" {
				s.noteBackupRefused(h.op.ID, op.ScheduleID, why, err)
			}
			return err
		}
		if err := s.stopServer(ctx, h); err != nil {
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
	})
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == api.CodeBusy {
			return "", schedule.ErrBusy
		}
		return "", err
	}
	select {
	case err := <-done:
		return started.ID, err
	case <-ctx.Done():
		return started.ID, ctx.Err()
	}
}

// refusedNotOnline is why a backup of a server that is starting or stopping
// is refused: its console can't pause saving yet.
const refusedNotOnline = "not_online"

// pauseRefusals are the ways an online backup fails when world saving
// couldn't be paused, or not for long enough to copy one moment's world. A
// backup with the server stopped needs no pause, so it avoids each.
var pauseRefusals = map[backup.ErrorKind]bool{
	backup.KindConsoleUnavailable: true, backup.KindSaveTimeout: true, backup.KindUnexpectedReply: true,
	backup.KindSavingResumed: true, backup.KindSavingPaused: true, backup.KindFileChanging: true,
}

// pauseRefusal is why a backup was refused for want of a pause in world
// saving: the backup error's kind, or refusedNotOnline. It is "" for any
// other outcome.
func pauseRefusal(err error) string {
	var ae *apiError
	switch {
	case !errors.As(err, &ae):
		return ""
	case ae.Reason == refusedNotOnline:
		return refusedNotOnline
	case pauseRefusals[backup.ErrorKind(ae.Code)]:
		return ae.Code
	}
	return ""
}

// noteBackupRefused records a scheduled backup refused because world saving
// couldn't be paused. Scheduled backups never stop a running server, so the
// refusal mustn't pass unseen: it gets a line in the recent activity, and the
// World tab shows it until a backup succeeds. The failed operation sends the
// backup-failed Discord alert.
func (s *server) noteBackupRefused(opID, scheduleID, why string, err error) {
	now := s.now().UTC()
	r := api.BackupRefusal{At: now, Since: now, Count: 1, Kind: why, Error: err.Error(), ScheduleID: scheduleID, OperationID: opID}
	var ae *apiError
	if errors.As(err, &ae) {
		r.Hint = ae.Hint
	}
	if prev := s.backupRefusal(); prev != nil {
		r.Since, r.Count = prev.Since, prev.Count+1
	}
	raw, _ := json.Marshal(r)
	if _, err := s.db.Exec(`UPDATE servers SET backup_refused = ? WHERE id = ?`, string(raw), s.id); err != nil {
		s.log.Warn("a refused scheduled backup could not be recorded", "server", s.id, "err", err)
	}
	s.recordEvent(now, "backup_refused", "", "playkeeper", why)
}

// backupRefusal is the scheduled backups refused since the last backup that
// succeeded, or nil.
func (s *server) backupRefusal() *api.BackupRefusal {
	var raw string
	if err := s.db.QueryRow(`SELECT backup_refused FROM servers WHERE id = ?`, s.id).Scan(&raw); err != nil || raw == "" {
		return nil
	}
	var r api.BackupRefusal
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	return &r
}

// clearBackupRefused forgets the refused scheduled backups once a backup
// succeeds.
func (s *server) clearBackupRefused() {
	if _, err := s.db.Exec(`UPDATE servers SET backup_refused = '' WHERE id = ? AND backup_refused != ''`, s.id); err != nil {
		s.log.Warn("refused scheduled backups could not be cleared", "server", s.id, "err", err)
	}
}

// rconOnce sends one command over a connection of its own and never sends
// it twice, since a command whose reply timed out may still have run.
func (s *server) rconOnce(ctx context.Context, cmd string) (string, error) {
	c, err := s.docker.ContainerInspect(ctx, s.containerName())
	if docker.IsNotFound(err) || err == nil && !c.State.Running {
		return "", schedule.ErrNotRunning
	}
	if err != nil {
		return "", s.dockerErr(err)
	}
	n, ok := c.NetworkSettings.Networks[networkName]
	if !ok || n.IPAddress == "" {
		return "", errors.New("the server has no address on the Playkeeper network")
	}
	pass, err := s.rconPassword()
	if err != nil {
		return "", err
	}
	r, err := minecraft.DialRCON(s.opts.RCONAddr(n.IPAddress), pass, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.Command(cmd, 10*time.Second)
}

// playedSinceLastBackup reports whether anyone played since the newest good
// backup, or there is none.
func (s *server) playedSinceLastBackup() bool {
	var last sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(created_at) FROM backups WHERE server_id = ? AND verified = 1 AND kind IN ('manual', 'scheduled')`, s.id).Scan(&last)
	if !last.Valid {
		return true
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE server_id = ? AND (end_ts IS NULL OR end_ts > ?)`, s.id, last.Int64).Scan(&n)
	return n > 0
}

// scheduleView is a schedule as the dashboard lists it.
type scheduleView struct {
	scheduleRow
	NextRun *time.Time `json:"nextRun,omitempty"`
	Summary string     `json:"summary"`
}

func (s *server) viewSchedule(row scheduleRow, now time.Time) scheduleView {
	v := scheduleView{scheduleRow: row, Summary: row.Timing.Summary()}
	if t := schedule.NextRun(row.Schedule, now); !t.IsZero() {
		v.NextRun = &t
	}
	return v
}

func (s *server) hSchedules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.scheduleRows(r.Context(), "")
	if err != nil {
		writeError(w, err)
		return
	}
	now := s.now()
	resp := schedulesResponse{Schedules: make([]scheduleView, 0, len(rows))}
	for _, row := range rows {
		resp.Schedules = append(resp.Schedules, s.viewSchedule(row, now))
	}
	s.auto.mu.Lock()
	runner := s.auto.runner
	s.auto.mu.Unlock()
	if runner != nil {
		if act, ok := runner.Current(); ok {
			resp.Current = &scheduleCurrent{ScheduleID: act.Job.Schedule.ID, Kind: act.Job.Schedule.Kind, Due: act.Job.Due, RestartAt: act.RestartAt}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// schedulesResponse is a server's schedules, and the one running now.
type schedulesResponse struct {
	Schedules []scheduleView   `json:"schedules"`
	Current   *scheduleCurrent `json:"current,omitempty"`
}

// scheduleCurrent is the schedule running now, and when it restarts the
// server if it does.
type scheduleCurrent struct {
	ScheduleID string        `json:"scheduleId"`
	Kind       schedule.Kind `json:"kind"`
	Due        time.Time     `json:"due"`
	RestartAt  time.Time     `json:"restartAt,omitzero"`
}

// scheduleRequest creates or changes a schedule. On a change, fields left out
// stay as they are.
type scheduleRequest struct {
	Actor   string            `json:"actor"`
	Name    *string           `json:"name,omitempty"`
	Kind    *schedule.Kind    `json:"kind,omitempty"`
	Timing  *schedule.Timing  `json:"timing,omitempty"`
	Payload *schedule.Payload `json:"payload,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

func (req scheduleRequest) apply(sc schedule.Schedule) schedule.Schedule {
	if req.Name != nil {
		sc.Name = *req.Name
	}
	if req.Kind != nil {
		sc.Kind = *req.Kind
	}
	if req.Timing != nil {
		sc.Timing = *req.Timing
	}
	if req.Payload != nil {
		sc.Payload = *req.Payload
	}
	if req.Enabled != nil {
		sc.Enabled = *req.Enabled
	}
	return sc.Normalize()
}

// checkSchedule validates a schedule, and a console command against the
// Console's own rules too.
func checkSchedule(sc schedule.Schedule, now time.Time) error {
	if err := sc.Validate(now); err != nil {
		return automationError(err)
	}
	if sc.Kind == schedule.KindCommand {
		if _, err := validateCommand(sc.Payload.Command); err != nil {
			return err
		}
	}
	return nil
}

func newScheduleID() string { return newServerID() }

func (s *server) hScheduleCreate(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Kind == nil || req.Timing == nil {
		writeError(w, errInvalid("Say what the schedule does and when."))
		return
	}
	row, err := s.createSchedule(r.Context(), req, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, s.viewSchedule(row, s.now()))
}

func (s *server) createSchedule(ctx context.Context, req scheduleRequest, actor string) (scheduleRow, error) {
	now := s.now().UTC()
	sc := req.apply(schedule.Schedule{ID: newScheduleID(), ServerID: s.id, Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err := checkSchedule(sc, now); err != nil {
		return scheduleRow{}, err
	}
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schedules WHERE server_id = ?`, s.id).Scan(&n)
	if n >= maxSchedules {
		return scheduleRow{}, errConflict(fmt.Sprintf("A server can have at most %d schedules.", maxSchedules), "Delete one you no longer need first.")
	}
	timing, _ := json.Marshal(sc.Timing)
	payload, _ := json.Marshal(sc.Payload)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO schedules(id, server_id, name, kind, timing, payload, enabled, created_at, updated_at, created_by, updated_by) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		sc.ID, s.id, sc.Name, string(sc.Kind), string(timing), string(payload), boolInt(sc.Enabled), now.UnixMilli(), now.UnixMilli(), actor, actor); err != nil {
		return scheduleRow{}, err
	}
	s.audit(actor, "schedule.created", sc.ID, "succeeded", scheduleDetail(sc))
	s.reloadSchedules()
	return scheduleRow{Schedule: sc, CreatedBy: actor, UpdatedBy: actor}, nil
}

// scheduleDetail describes a schedule for the audit trail, without the text
// of messages and with addresses in commands hidden.
func scheduleDetail(sc schedule.Schedule) string {
	d := string(sc.Kind) + ": " + sc.Timing.Summary()
	if sc.Kind == schedule.KindCommand {
		d += " · " + minecraft.RedactIPs(sc.Payload.Command)
	}
	if !sc.Enabled {
		d += " · off"
	}
	return d
}

func (s *server) scheduleByID(ctx context.Context, sid string) (scheduleRow, error) {
	if !reScheduleID.MatchString(sid) {
		return scheduleRow{}, errInvalid("invalid schedule id")
	}
	rows, err := s.scheduleRows(ctx, `id = ?`, sid)
	if err != nil {
		return scheduleRow{}, err
	}
	if len(rows) == 0 {
		return scheduleRow{}, errNotFound("Schedule")
	}
	return rows[0], nil
}

func (s *server) hScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.updateSchedule(r.Context(), r.PathValue("sid"), req, actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.viewSchedule(row, s.now()))
}

// scheduleEditRead runs once an edit has read the schedule, before it saves
// the change; tests save a run there.
var scheduleEditRead = func(scheduleRow) {}

func (s *server) updateSchedule(ctx context.Context, sid string, req scheduleRequest, actor string) (scheduleRow, error) {
	row, err := s.scheduleByID(ctx, sid)
	if err != nil {
		return scheduleRow{}, err
	}
	scheduleEditRead(row)
	now := s.now().UTC()
	prev := row.Schedule
	sc := req.apply(prev)
	sc.UpdatedAt = now
	// Only an edit of what the schedule does or when checks it again: an old
	// one-off time in the past can still be turned off.
	onlySwitch := req.Name == nil && req.Kind == nil && req.Timing == nil && req.Payload == nil
	if !onlySwitch || sc.Enabled {
		if err := checkSchedule(sc, now); err != nil {
			return scheduleRow{}, err
		}
	}
	// A retry an hour later belongs to the schedule as it was when the run
	// was skipped. The planner drops it once the schedule changes, even
	// when it is only switched off and on, so every change clears it. The
	// runner may have saved a run since the read, so the save clears the
	// retry of the run saved then and leaves the rest of it as it is.
	if sc.LastRun != nil && !sc.LastRun.RetryAt.IsZero() {
		last := *sc.LastRun
		last.RetryAt = time.Time{}
		sc.LastRun = &last
	}
	timing, _ := json.Marshal(sc.Timing)
	payload, _ := json.Marshal(sc.Payload)
	if _, err := s.db.ExecContext(ctx, `UPDATE schedules SET name = ?, kind = ?, timing = ?, payload = ?, enabled = ?, updated_at = ?, updated_by = ?,
			last_run = CASE WHEN json_valid(last_run) THEN json_remove(last_run, '$.retryAt') ELSE last_run END
		WHERE id = ? AND server_id = ?`,
		sc.Name, string(sc.Kind), string(timing), string(payload), boolInt(sc.Enabled), now.UnixMilli(), actor, sc.ID, s.id); err != nil {
		return scheduleRow{}, err
	}
	action := "schedule.changed"
	if onlySwitch && prev.Enabled != sc.Enabled {
		action = "schedule.disabled"
		if sc.Enabled {
			action = "schedule.enabled"
		}
	}
	s.audit(actor, action, sc.ID, "succeeded", scheduleDetail(sc))
	s.reloadSchedules()
	row.Schedule, row.UpdatedBy = sc, actor
	return row, nil
}

func (s *server) hScheduleDelete(w http.ResponseWriter, r *http.Request) {
	actor, err := validActor(r.URL.Query().Get("actor"))
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := s.scheduleByID(r.Context(), r.PathValue("sid"))
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.db.ExecContext(r.Context(), `DELETE FROM schedules WHERE id = ? AND server_id = ?`, row.ID, s.id); err != nil {
		writeError(w, err)
		return
	}
	s.audit(actor, "schedule.deleted", row.ID, "succeeded", scheduleDetail(row.Schedule))
	s.reloadSchedules()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": row.ID})
}

// hSchedulePreview checks a schedule being edited and says when it would
// run, without saving it.
func (s *server) hSchedulePreview(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	now := s.now().UTC()
	base := schedule.Schedule{ID: "preview", ServerID: s.id, Enabled: true, CreatedAt: now, UpdatedAt: now}
	resp := schedulePreview{NextRuns: []time.Time{}}
	if req.Kind == nil || req.Timing == nil {
		writeError(w, errInvalid("Say what the schedule does and when."))
		return
	}
	sc := req.apply(base)
	if err := checkSchedule(sc, now); err != nil {
		var ae *apiError
		if errors.As(err, &ae) {
			resp.Error = &api.Error{Error: ae.Msg, Code: ae.Code, Hint: ae.Hint, Field: ae.Field, Reason: ae.Reason, Params: ae.Params}
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	next, _ := sc.Timing.NextRuns(now, 3)
	resp.Valid, resp.NextRuns, resp.Summary = true, next, sc.Timing.Summary()
	writeJSON(w, http.StatusOK, resp)
}

// schedulePreview says whether a schedule being edited is valid and, if it
// is, when it would run next.
type schedulePreview struct {
	Valid    bool        `json:"valid"`
	NextRuns []time.Time `json:"nextRuns"`
	Summary  string      `json:"summary,omitempty"`
	Error    *api.Error  `json:"error,omitempty"`
}

// scheduleRunView is one line of "Recent runs".
type scheduleRunView struct {
	ScheduleID  string         `json:"scheduleId"`
	Kind        string         `json:"kind"`
	Due         time.Time      `json:"due"`
	StartedAt   *time.Time     `json:"startedAt,omitempty"`
	FinishedAt  *time.Time     `json:"finishedAt,omitempty"`
	Result      string         `json:"result"`
	Reason      string         `json:"reason,omitempty"`
	Detail      string         `json:"detail,omitempty"`
	OperationID string         `json:"operationId,omitempty"`
	Players     *int           `json:"players,omitempty"`
	Backup      *runBackupInfo `json:"backup,omitempty"`
}

// runBackupInfo is what a scheduled backup made.
type runBackupInfo struct {
	ID         string `json:"id"`
	SizeBytes  int64  `json:"sizeBytes"`
	Verified   bool   `json:"verified"`
	DowntimeMs int64  `json:"downtimeMs"`
}

func (s *server) hScheduleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, errInvalid("limit must be between 1 and 200"))
			return
		}
		limit = n
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT r.schedule_id, r.kind, r.due, r.started_at, r.finished_at, r.result, r.reason, r.detail, r.operation_id, r.players,
			b.id, b.size_bytes, b.verified, b.downtime_ms
		FROM schedule_runs r
		LEFT JOIN operations o ON o.id = r.operation_id AND r.operation_id != ''
		LEFT JOIN backups b ON b.id = json_extract(o.detail, '$.backupId')
		WHERE r.server_id = ? AND r.result != 'running'
		ORDER BY r.due DESC, r.id DESC LIMIT ?`, s.id, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	defer rows.Close()
	out := []scheduleRunView{}
	for rows.Next() {
		var v scheduleRunView
		var due int64
		var started, finished, players, size, verified, downtime sql.NullInt64
		var bid sql.NullString
		if err := rows.Scan(&v.ScheduleID, &v.Kind, &due, &started, &finished, &v.Result, &v.Reason, &v.Detail, &v.OperationID, &players, &bid, &size, &verified, &downtime); err != nil {
			writeError(w, err)
			return
		}
		v.Due = time.UnixMilli(due).UTC()
		if started.Valid {
			t := time.UnixMilli(started.Int64).UTC()
			v.StartedAt = &t
		}
		if finished.Valid {
			t := time.UnixMilli(finished.Int64).UTC()
			v.FinishedAt = &t
		}
		if players.Valid {
			n := int(players.Int64)
			v.Players = &n
		}
		if bid.Valid && bid.String != "" {
			v.Backup = &runBackupInfo{ID: bid.String, SizeBytes: size.Int64, Verified: verified.Valid && verified.Int64 == 1, DowntimeMs: downtime.Int64}
		}
		if v.Kind == string(schedule.KindCommand) {
			v.Detail = minecraft.RedactIPs(v.Detail)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// scheduleTimeZone is the time zone of the server's first schedule, for
// texts about times when the dashboard didn't say which zone it uses.
func (s *server) scheduleTimeZone(ctx context.Context) *time.Location {
	rows, err := s.scheduleRows(ctx, "")
	if err == nil {
		for _, r := range rows {
			if loc, err := time.LoadLocation(strings.TrimSpace(r.Timing.TimeZone)); err == nil && r.Timing.TimeZone != "" {
				return loc
			}
		}
	}
	return time.UTC
}
