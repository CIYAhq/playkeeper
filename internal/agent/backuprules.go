package agent

// Wave 7 (0.4.0): backup rules. Automatic backups are the server's first
// backup schedule, so they also show under Schedules. What to keep is applied
// after every verified backup, on this machine here and at the destination
// after each copy there.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/backup/retention"
	"github.com/CIYAhq/playkeeper/internal/schedule"
)

// retentionActor is the actor of deletions the backup rules make.
const retentionActor = "backup rules"

// backupRulesDoc is the servers.backup_rules column.
type backupRulesDoc struct {
	Settings retention.Settings `json:"settings"`
	TimeZone string             `json:"timeZone,omitempty"`
}

// backupRules returns the saved rules, or the defaults, and the time zone
// they count days in.
func (s *server) backupRules() (retention.Settings, *time.Location, bool) {
	var raw string
	_ = s.db.QueryRow(`SELECT backup_rules FROM servers WHERE id = ?`, s.id).Scan(&raw)
	var doc backupRulesDoc
	if raw == "" || json.Unmarshal([]byte(raw), &doc) != nil {
		return retention.DefaultSettings(), s.scheduleTimeZone(context.Background()), false
	}
	if loc, err := time.LoadLocation(doc.TimeZone); err == nil && doc.TimeZone != "" {
		return doc.Settings, loc, true
	}
	return doc.Settings, s.scheduleTimeZone(context.Background()), true
}

// unsettledSwaps reads the swap journals in the restore stages, by stage
// name. A restore isn't over while its stage keeps one: it may still put the
// previous world back. A journal that can't be read is nil, and is logged.
func (a *Agent) unsettledSwaps() map[string]*swapJournal {
	entries, _ := os.ReadDir(a.cfg.StagingDir())
	out := map[string]*swapJournal{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		j, err := readSwapJournal(filepath.Join(a.cfg.StagingDir(), e.Name()))
		if err != nil {
			if prev, seen := a.unreadableSwaps.Swap(e.Name(), err.Error()); !seen || prev != err.Error() {
				a.log.Warn("a restore stage's swap journal can't be read, so every server keeps its rollback archives and world copies until the journal is fixed or removed", "stage", e.Name(), "err", err)
			}
			out[e.Name()] = nil
			continue
		}
		a.unreadableSwaps.Delete(e.Name())
		if j != nil {
			out[e.Name()] = j
		}
	}
	return out
}

// concerns says whether the swap journal may be the server's. One that can't
// be read may be any server's.
func (j *swapJournal) concerns(serverID string) bool {
	return j == nil || j.ServerID == serverID
}

// rollbacksNeeded names the rollback archives of the server's restores that
// aren't over, the one running and those whose stage keeps a swap journal,
// as the unfinished restore that may still need them. A journal that can't
// be read, or whose restore is no longer on record, may need any of them, so
// it keeps every rollback archive in list.
func (s *server) rollbacksNeeded(list []api.Backup) map[string]string {
	needed := map[string]string{}
	add := func(op *api.Operation) {
		if id, _ := op.Detail["rollbackBackupId"].(string); op.Kind == "restore" && id != "" {
			needed[id] = "restore"
		}
	}
	if op := s.currentOp(); op != nil {
		add(op)
	}
	for _, j := range s.unsettledSwaps() {
		if !j.concerns(s.id) {
			continue
		}
		var op *api.Operation
		if j != nil {
			op, _ = s.loadOperation(j.OpID)
		}
		if op != nil {
			add(op)
			continue
		}
		for _, b := range list {
			if b.Kind == retention.KindRollback || b.Kind == retention.KindBeforeRestore {
				needed[b.ID] = "restore"
			}
		}
	}
	return needed
}

// retentionBackups lists the server's backups for the rules: those on this
// machine, and the copies somewhere else.
func (s *server) retentionBackups() ([]retention.Backup, error) {
	list, err := s.listBackups("")
	if err != nil {
		return nil, err
	}
	needed := s.rollbacksNeeded(list)
	queued := map[string]bool{}
	if rows, err := s.db.Query(`SELECT backup_id FROM offsite_uploads WHERE server_id = ?`, s.id); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				queued[id] = true
			}
		}
		rows.Close()
	}
	var out []retention.Backup
	index := map[string]int{}
	for _, b := range list {
		index[b.ID] = len(out)
		out = append(out, retention.Backup{ID: b.ID, Kind: b.Kind, CreatedAt: b.CreatedAt, SizeBytes: b.SizeBytes, MinecraftVersion: b.MinecraftVersion,
			LevelName: b.LevelName, Verified: b.Verified, OnHost: true, Downloaded: b.DownloadedAt != nil, Pinned: queued[b.ID], NeededBy: needed[b.ID]})
	}
	rows, err := s.db.Query(`SELECT backup_id, kind, backup_created_at, size_bytes, minecraft_version, level_name FROM offsite_copies WHERE server_id = ?`, s.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, mc, level string
		var created, size int64
		if err := rows.Scan(&id, &kind, &created, &size, &mc, &level); err != nil {
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].OffSite = true
			continue
		}
		ok := true
		out = append(out, retention.Backup{ID: id, Kind: kind, CreatedAt: time.UnixMilli(created).UTC(), SizeBytes: size, MinecraftVersion: mc, LevelName: level, Verified: &ok, OffSite: true})
	}
	return out, rows.Err()
}

// retentionPlan is what the rules would do now.
func (s *server) retentionPlan() (retention.Result, error) {
	set, loc, _ := s.backupRules()
	backups, err := s.retentionBackups()
	if err != nil {
		return retention.Result{}, err
	}
	return retention.Decide(backups, set, loc), nil
}

// afterBackup runs inside a backup operation once its archive is verified:
// it queues the copy somewhere else, then deletes what the rules no longer
// keep on this machine.
func (s *server) afterBackup(b *api.Backup) {
	s.queueOffsite(b.ID)
	s.applyRetention()
}

// applyRetention deletes the backups on this machine the rules no longer
// keep. The caller holds the server's operation lock.
func (s *server) applyRetention() {
	res, err := s.retentionPlan()
	if err != nil {
		s.log.Warn("backup rules could not be applied", "server", s.id, "err", err)
		return
	}
	for _, id := range res.OnHost.DeleteIDs() {
		b, err := s.getBackup(id)
		if err != nil {
			continue
		}
		if err := s.removeBackup(b, retentionActor); err != nil {
			s.log.Warn("a backup the rules no longer keep could not be deleted", "server", s.id, "backup", id, "err", err)
		}
	}
}

// removeBackup deletes a backup's archive, checksum file and record.
func (s *server) removeBackup(b *api.Backup, actor string) error {
	for _, p := range []string{s.backupPath(b.FileName), s.backupPath(b.FileName) + ".sha256"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := s.db.Exec(`DELETE FROM backups WHERE id = ? AND server_id = ?`, b.ID, s.id); err != nil {
		return err
	}
	s.audit(actor, "backup.deleted", b.ID, "succeeded", b.FileName)
	return nil
}

// automaticBackups is the Backup rules page's "Automatic backups".
type automaticBackups struct {
	Enabled      bool       `json:"enabled"`
	EveryHours   int        `json:"everyHours"`
	OnlyIfPlayed bool       `json:"onlyIfPlayed"`
	ScheduleID   string     `json:"scheduleId,omitempty"`
	NextRun      *time.Time `json:"nextRun,omitempty"`
}

// automaticEvery is how often automatic backups can run, in hours.
var automaticEvery = []int{1, 2, 3, 4, 6, 8, 12, 24}

// automaticSchedule is the server's first backup schedule that runs every
// few hours or every day.
func (s *server) automaticSchedule(ctx context.Context) (scheduleRow, bool) {
	rows, err := s.scheduleRows(ctx, `kind = 'backup'`)
	if err != nil {
		return scheduleRow{}, false
	}
	for _, r := range rows {
		if r.Timing.Kind == schedule.Interval || r.Timing.Kind == schedule.Daily {
			return r, true
		}
	}
	return scheduleRow{}, false
}

func (s *server) automaticBackups(ctx context.Context) automaticBackups {
	row, ok := s.automaticSchedule(ctx)
	if !ok {
		return automaticBackups{EveryHours: 24, OnlyIfPlayed: true}
	}
	a := automaticBackups{Enabled: row.Enabled, EveryHours: row.Timing.EveryHours, OnlyIfPlayed: row.Payload.OnlyIfPlayed, ScheduleID: row.ID}
	if row.Timing.Kind == schedule.Daily {
		a.EveryHours = 24
	}
	if t := schedule.NextRun(row.Schedule, s.now()); !t.IsZero() && row.Enabled {
		a.NextRun = &t
	}
	return a
}

func sameJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func automaticTiming(everyHours int, prev schedule.Timing, tz string) schedule.Timing {
	t := schedule.Timing{Kind: schedule.Interval, EveryHours: everyHours, At: "00:00", TimeZone: prev.TimeZone}
	if everyHours == 24 {
		t = schedule.Timing{Kind: schedule.Daily, At: "04:00", TimeZone: prev.TimeZone}
	}
	if prev.Kind == t.Kind && prev.At != "" {
		t.At = prev.At
	}
	if tz != "" {
		t.TimeZone = tz
	}
	if t.TimeZone == "" {
		t.TimeZone = "UTC"
	}
	return t
}

// backupRulesView is the Backup rules page.
type backupRulesView struct {
	Automatic automaticBackups   `json:"automatic"`
	Rules     retention.Settings `json:"rules"`
	Custom    bool               `json:"custom"`
	Describe  []retention.Text   `json:"describe"`
	OnHost    retention.Estimate `json:"onHost"`
	OffSite   retention.Estimate `json:"offSite"`
	Limits    map[string]int     `json:"limits"`
}

// backupPace is how automatic backups run now, for the estimates: how often,
// the newest backup's size, and the time zone days are counted in (tz when
// it is valid).
func (s *server) backupPace(ctx context.Context, tz string, loc *time.Location, auto automaticBackups) retention.Pace {
	if l, err := time.LoadLocation(tz); err == nil && tz != "" {
		loc = l
	}
	pace := retention.Pace{Location: loc}
	if auto.Enabled {
		pace.Every = time.Duration(auto.EveryHours) * time.Hour
	}
	if list, err := s.listBackups(`verified = 1 AND kind IN ('manual', 'scheduled')`); err == nil && len(list) > 0 {
		pace.Bytes = list[0].SizeBytes
	}
	return pace
}

func (s *server) backupRulesView(ctx context.Context, tz string) backupRulesView {
	set, loc, custom := s.backupRules()
	auto := s.automaticBackups(ctx)
	pace := s.backupPace(ctx, tz, loc, auto)
	return backupRulesView{
		Automatic: auto, Rules: set, Custom: custom, Describe: set.Describe(),
		OnHost: set.Estimate(retention.OnHost, pace), OffSite: set.Estimate(retention.OffSite, pace),
		Limits: map[string]int{"hours": retention.MaxHours, "last": retention.MaxLast, "daily": retention.MaxDaily, "weekly": retention.MaxWeekly, "monthly": retention.MaxMonthly},
	}
}

func (s *server) hBackupRules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backupRulesView(r.Context(), r.URL.Query().Get("tz")))
}

// hBackupRulesEstimate is what rules not saved yet would keep, for the
// rules editor's totals. Nothing is saved.
func (s *server) hBackupRulesEstimate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Actor    string             `json:"actor"`
		Rules    retention.Settings `json:"rules"`
		TimeZone string             `json:"timeZone,omitempty"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if _, err := validActor(req.Actor); err != nil {
		writeError(w, err)
		return
	}
	if err := req.Rules.Validate(); err != nil {
		writeError(w, automationError(err))
		return
	}
	_, loc, _ := s.backupRules()
	pace := s.backupPace(r.Context(), strings.TrimSpace(req.TimeZone), loc, s.automaticBackups(r.Context()))
	writeJSON(w, http.StatusOK, map[string]retention.Estimate{
		"onHost": req.Rules.Estimate(retention.OnHost, pace), "offSite": req.Rules.Estimate(retention.OffSite, pace),
	})
}

type backupRulesRequest struct {
	Actor     string                   `json:"actor"`
	Automatic *automaticBackupsRequest `json:"automatic,omitempty"`
	Rules     *retention.Settings      `json:"rules,omitempty"`
	// TimeZone is the dashboard's, for new automatic backups and for the
	// days the rules count.
	TimeZone string `json:"timeZone,omitempty"`
}

type automaticBackupsRequest struct {
	Enabled      bool `json:"enabled"`
	EveryHours   int  `json:"everyHours"`
	OnlyIfPlayed bool `json:"onlyIfPlayed"`
}

func (s *server) hBackupRulesSet(w http.ResponseWriter, r *http.Request) {
	var req backupRulesRequest
	if err := decode(r, &req); err != nil {
		writeError(w, err)
		return
	}
	actor, err := validActor(req.Actor)
	if err != nil {
		writeError(w, err)
		return
	}
	tz := strings.TrimSpace(req.TimeZone)
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil || len(tz) > 64 {
			writeError(w, &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Msg: "Unknown time zone.", Field: "timeZone", Reason: "time_zone_invalid"})
			return
		}
	}
	if req.Rules != nil {
		if err := req.Rules.Validate(); err != nil {
			writeError(w, automationError(err))
			return
		}
	}
	if req.Automatic != nil {
		if err := s.setAutomaticBackups(r.Context(), *req.Automatic, tz, actor); err != nil {
			writeError(w, err)
			return
		}
	}
	if req.Rules != nil {
		_, _, custom := s.backupRules()
		doc := backupRulesDoc{Settings: *req.Rules, TimeZone: tz}
		if tz == "" && custom {
			var raw string
			var prev backupRulesDoc
			_ = s.db.QueryRow(`SELECT backup_rules FROM servers WHERE id = ?`, s.id).Scan(&raw)
			if json.Unmarshal([]byte(raw), &prev) == nil {
				doc.TimeZone = prev.TimeZone
			}
		}
		b, _ := json.Marshal(doc)
		if _, err := s.db.Exec(`UPDATE servers SET backup_rules = ? WHERE id = ?`, string(b), s.id); err != nil {
			writeError(w, err)
			return
		}
		var texts []string
		for _, t := range req.Rules.Describe() {
			texts = append(texts, t.Text)
		}
		s.audit(actor, "backup_rules.changed", "server", "succeeded", strings.Join(texts, " "))
	}
	writeJSON(w, http.StatusOK, s.backupRulesView(r.Context(), tz))
}

// setAutomaticBackups turns automatic backups on or off through the server's
// automatic backup schedule, making it the first time.
func (s *server) setAutomaticBackups(ctx context.Context, req automaticBackupsRequest, tz, actor string) error {
	if !slices.Contains(automaticEvery, req.EveryHours) {
		return &apiError{Status: http.StatusBadRequest, Code: api.CodeInvalid, Field: "automatic.everyHours", Reason: "every_hours_invalid",
			Msg: fmt.Sprintf("Automatic backups can run every 1, 2, 3, 4, 6, 8 or 12 hours, or once a day, not every %d hours.", req.EveryHours)}
	}
	row, ok := s.automaticSchedule(ctx)
	if !ok {
		if !req.Enabled {
			return nil
		}
		kind := schedule.KindBackup
		timing := automaticTiming(req.EveryHours, schedule.Timing{}, tz)
		payload := schedule.Payload{OnlyIfPlayed: req.OnlyIfPlayed}
		_, err := s.createSchedule(ctx, scheduleRequest{Kind: &kind, Timing: &timing, Payload: &payload}, actor)
		return err
	}
	timing := automaticTiming(req.EveryHours, row.Timing, tz)
	payload := row.Payload
	payload.OnlyIfPlayed = req.OnlyIfPlayed
	enabled := req.Enabled
	if sameJSON(timing, row.Timing) && sameJSON(payload, row.Payload) {
		if enabled == row.Enabled {
			return nil
		}
		_, err := s.updateSchedule(ctx, row.ID, scheduleRequest{Enabled: &enabled}, actor)
		return err
	}
	_, err := s.updateSchedule(ctx, row.ID, scheduleRequest{Timing: &timing, Payload: &payload, Enabled: &enabled}, actor)
	return err
}
