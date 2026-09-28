package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/schedule"
)

// A restart skipped because people were playing tries again an hour later.
// The runner drops that retry once the schedule changes, even when it is
// only switched off and on, so the schedule list promises the retry exactly
// while NextRun and Plan still have it.
func TestAScheduleListsItsRetryOnlyWhileTheRunnerPlansIt(t *testing.T) {
	cases := []struct {
		name    string
		changes []map[string]any
		retry   bool
	}{
		{name: "no change", retry: true},
		{name: "renamed", changes: []map[string]any{{"name": "Nightly restart"}}},
		{name: "switched off", changes: []map[string]any{{"enabled": false}}},
		{name: "switched off, then on", changes: []map[string]any{{"enabled": false}, {"enabled": true}}},
		{name: "switched on while on", changes: []map[string]any{{"enabled": true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.addIdleServer()
			now := e.a.now().UTC()
			due := now.Truncate(time.Minute).Add(-30 * time.Minute)
			code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "restart",
				"timing":  map[string]any{"kind": "daily", "at": due.Format("15:04"), "timeZone": "UTC"},
				"payload": map[string]any{"warnSeconds": []int{600}, "message": "Survival restarts in {minutes} minutes.", "skipIfPlaying": true}})
			if code != http.StatusCreated {
				t.Fatalf("create: %d %v", code, out)
			}
			id := out["id"].(string)
			retryAt := due.Add(time.Hour)
			players := 3
			last, _ := json.Marshal(schedule.Run{Due: due, Finished: due, Result: schedule.ResultSkipped, Reason: schedule.ReasonPeoplePlaying, Players: &players, RetryAt: retryAt})
			made := due.Add(-24 * time.Hour).UnixMilli()
			if _, err := e.a.db.Exec(`UPDATE schedules SET created_at = ?, updated_at = ?, last_run = ? WHERE id = ?`, made, made, string(last), id); err != nil {
				t.Fatal(err)
			}
			for _, ch := range c.changes {
				ch["actor"] = "owner"
				if code, out := e.call("POST", e.sp("/schedules/"+id), ch); code != http.StatusOK {
					t.Fatalf("change %v: %d %v", ch, code, out)
				}
			}

			// What the list shows, as the dashboard reads it.
			var list struct {
				Schedules []struct {
					Enabled bool       `json:"enabled"`
					NextRun *time.Time `json:"nextRun"`
					LastRun *struct {
						RetryAt *time.Time `json:"retryAt"`
					} `json:"lastRun"`
				} `json:"schedules"`
			}
			e.decode("GET", e.sp("/schedules"), &list)
			if len(list.Schedules) != 1 || list.Schedules[0].LastRun == nil {
				t.Fatalf("list: %+v", list)
			}
			v := list.Schedules[0]
			promised := v.Enabled && v.LastRun.RetryAt != nil && v.LastRun.RetryAt.After(now)

			// What the runner does.
			rows, err := e.srv().scheduleRows(context.Background(), `id = ?`, id)
			if err != nil || len(rows) != 1 {
				t.Fatalf("rows: %v %v", rows, err)
			}
			next := schedule.NextRun(rows[0].Schedule, now)
			wake := schedule.Plan([]schedule.Schedule{rows[0].Schedule}, now, now.Add(-time.Hour)).Wake
			planned := next.Equal(retryAt)
			if woken := wake.Equal(retryAt.Add(-10 * time.Minute)); woken != planned {
				t.Fatalf("NextRun is %v but Plan wakes at %v", next, wake)
			}
			if promised != planned {
				t.Fatalf("the list promises the retry at %v: %v, but the runner plans it: %v (next run %v)", retryAt, promised, planned, next)
			}
			if planned != c.retry {
				t.Fatalf("the retry at %v is planned: %v, want %v", retryAt, planned, c.retry)
			}
			if promised && (v.NextRun == nil || !v.NextRun.Equal(retryAt)) {
				t.Fatalf("the list's next run is %v, not the retry at %v", v.NextRun, retryAt)
			}
		})
	}
}

// The automatic backups keep their time zone while they keep their time, so
// changing Backup rules from a dashboard in another zone never moves them. A
// time that starts afresh is in the dashboard's zone.
func TestAutomaticBackupsKeepTheirTimeZoneWhileTheyKeepTheirTime(t *testing.T) {
	const ny, tokyo = "America/New_York", "Asia/Tokyo"
	daily := func(at, tz string) schedule.Timing {
		return schedule.Timing{Kind: schedule.Daily, At: at, TimeZone: tz}
	}
	every := func(h int, at, tz string) schedule.Timing {
		return schedule.Timing{Kind: schedule.Interval, EveryHours: h, At: at, TimeZone: tz}
	}
	cases := []struct {
		name       string
		everyHours int
		prev       schedule.Timing
		tz         string
		want       schedule.Timing
	}{
		{"daily, and only whether someone played changes", 24, daily("05:30", ny), tokyo, daily("05:30", ny)},
		{"every 6 hours, and only whether someone played changes", 6, every(6, "02:00", ny), tokyo, every(6, "02:00", ny)},
		{"every 6 hours, now every 12", 12, every(6, "02:00", ny), tokyo, every(12, "02:00", ny)},
		{"every 6 hours, now daily", 24, every(6, "02:00", ny), tokyo, daily("04:00", tokyo)},
		{"daily, now every 8 hours", 8, daily("05:30", ny), tokyo, every(8, "00:00", tokyo)},
		{"new", 24, schedule.Timing{}, tokyo, daily("04:00", tokyo)},
		{"new, from a dashboard that names no zone", 6, schedule.Timing{}, "", every(6, "00:00", "UTC")},
		{"daily, from a dashboard that names no zone", 24, daily("05:30", ny), "", daily("05:30", ny)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := automaticTiming(c.everyHours, c.prev, c.tz); !sameJSON(got, c.want) {
				t.Fatalf("automaticTiming(%d, %+v, %q) = %+v, want %+v", c.everyHours, c.prev, c.tz, got, c.want)
			}
		})
	}

	t.Run("through Backup rules", func(t *testing.T) {
		e := newAgentEnv(t)
		e.addIdleServer()
		set := func(onlyIfPlayed bool, tz string) {
			t.Helper()
			code, out := e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "timeZone": tz,
				"automatic": map[string]any{"enabled": true, "everyHours": 24, "onlyIfPlayed": onlyIfPlayed}})
			if code != http.StatusOK {
				t.Fatalf("backup rules from %s: %d %v", tz, code, out)
			}
		}
		set(true, ny)
		before, ok, err := e.srv().automaticSchedule(context.Background())
		if err != nil || !ok || !sameJSON(before.Timing, daily("04:00", ny)) {
			t.Fatalf("automatic backups made from New York: %+v (%v)", before.Timing, err)
		}
		set(false, tokyo)
		after, _, _ := e.srv().automaticSchedule(context.Background())
		now := e.a.now()
		if !sameJSON(after.Timing, before.Timing) || after.Payload.OnlyIfPlayed || !schedule.NextRun(after.Schedule, now).Equal(schedule.NextRun(before.Schedule, now)) {
			t.Fatalf("after a change from Tokyo: %+v %+v, next run %v, was %+v next %v", after.Timing, after.Payload,
				schedule.NextRun(after.Schedule, now), before.Timing, schedule.NextRun(before.Schedule, now))
		}
	})
}

// While the schedules can't be read, the automatic backups are neither shown
// nor saved: a save is refused rather than make a second backup schedule, or
// answer that it turned them off, and the page doesn't show the defaults.
func TestAutomaticBackupsThatCantBeReadAreNeitherShownNorSaved(t *testing.T) {
	every := func(on bool, hours int) map[string]any {
		return map[string]any{"enabled": on, "everyHours": hours, "onlyIfPlayed": true}
	}
	cases := []struct {
		name string
		// existing is whether automatic backups every day were made before.
		existing  bool
		automatic map[string]any
	}{
		{name: "turned on", automatic: every(true, 24)},
		{name: "every few hours instead", existing: true, automatic: every(true, 6)},
		{name: "turned off", existing: true, automatic: every(false, 24)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.addIdleServer()
			if c.existing {
				if code, out := e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "automatic": every(true, 24)}); code != http.StatusOK {
					t.Fatalf("automatic backups: %d %v", code, out)
				}
			}
			schedules := func() string {
				t.Helper()
				var s string
				if err := e.a.db.QueryRow(`SELECT COALESCE(group_concat(id || ' ' || timing || ' ' || enabled, ';'), '') FROM schedules`).Scan(&s); err != nil {
					t.Fatal(err)
				}
				return s
			}
			before := schedules()
			back := e.renameColumn("schedules", "last_run", "last_run_gone")
			saved, out := e.call("POST", e.sp("/backup-rules"), map[string]any{"actor": "admin", "automatic": c.automatic})
			shown, _ := e.call("GET", e.sp("/backup-rules"), nil)
			estimated, _ := e.call("POST", e.sp("/backup-rules/estimate"), map[string]any{"actor": "admin",
				"rules": map[string]any{"onHost": map[string]any{"keepAll": true}, "offSite": map[string]any{"keepAll": true}, "includeManual": true}})
			back()
			if saved != http.StatusInternalServerError || shown != http.StatusInternalServerError || estimated != http.StatusInternalServerError {
				t.Fatalf("with the schedules unreadable: save %d %v, page %d, estimate %d", saved, out, shown, estimated)
			}
			if after := schedules(); after != before {
				t.Fatalf("the refused save changed the schedules from %q to %q", before, after)
			}
		})
	}
}

// An edit of a schedule saves what it changes, but never writes back the
// last run it read: a run the runner saves meanwhile stays, on the schedule
// and in Recent runs, so a restart doesn't record it as interrupted.
func TestEditingAScheduleKeepsTheRunTheRunnerSaved(t *testing.T) {
	due := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	running := schedule.Run{Due: due, Started: due, Result: schedule.ResultRunning}
	done := schedule.Run{Due: due, Started: due, Finished: due.Add(time.Minute), Result: schedule.ResultSucceeded, OperationID: "opopopopopopopop"}
	yesterday := done
	yesterday.Due, yesterday.Started, yesterday.Finished = due.Add(-24*time.Hour), due.Add(-24*time.Hour), due.Add(-24*time.Hour+time.Minute)
	cases := []struct {
		name   string
		change map[string]any
		// read is the last run the edit reads, and saved the run the runner
		// saves before the edit saves.
		read, saved schedule.Run
		column      string
		value       any
	}{
		{name: "renamed as its run finishes", change: map[string]any{"name": "Nightly"}, read: running, saved: done, column: "name", value: "Nightly"},
		{name: "switched off as a run starts", change: map[string]any{"enabled": false}, read: yesterday, saved: running, column: "enabled", value: int64(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newAgentEnv(t)
			e.addIdleServer()
			s := e.srv()
			// The hook below plays the runner, so the server's loops stop
			// first: the real runner closes a run left running as
			// interrupted when it first reads the schedules, whenever that is.
			s.cancel()
			s.loops.Wait()
			at := time.Now().UTC().Add(12 * time.Hour).Format("15:04")
			code, out := e.call("POST", e.sp("/schedules"), map[string]any{"actor": "admin", "kind": "backup",
				"timing": map[string]any{"kind": "daily", "at": at, "timeZone": "UTC"}})
			if code != http.StatusCreated {
				t.Fatalf("create: %d %v", code, out)
			}
			id := out["id"].(string)
			if err := s.saveScheduleRun(context.Background(), id, c.read); err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			prev := scheduleEditRead
			scheduleEditRead = func(row scheduleRow) {
				once.Do(func() {
					if err := s.saveScheduleRun(context.Background(), row.ID, c.saved); err != nil {
						t.Error(err)
					}
				})
			}
			t.Cleanup(func() { scheduleEditRead = prev })
			c.change["actor"] = "admin"
			if code, out := e.call("POST", e.sp("/schedules/"+id), c.change); code != http.StatusOK {
				t.Fatalf("edit: %d %v", code, out)
			}
			var raw string
			var changed any
			if err := e.a.db.QueryRow(`SELECT last_run, `+c.column+` FROM schedules WHERE id = ?`, id).Scan(&raw, &changed); err != nil {
				t.Fatal(err)
			}
			var last schedule.Run
			if err := json.Unmarshal([]byte(raw), &last); err != nil || !sameJSON(last, c.saved) || changed != c.value {
				t.Fatalf("after the edit: last run %s (%v), %s %v; want the runner's %s run due %s, and %v", raw, err, c.column, changed, c.saved.Result, c.saved.Due, c.value)
			}
			var result, reason string
			if err := e.a.db.QueryRow(`SELECT result, reason FROM schedule_runs WHERE schedule_id = ? AND due = ?`, id, c.saved.Due.UnixMilli()).Scan(&result, &reason); err != nil ||
				result != string(c.saved.Result) || reason != string(c.saved.Reason) {
				t.Fatalf("Recent runs lost the runner's run: %s %s (%v)", result, reason, err)
			}
		})
	}
}

// The runner hears that the server is busy while another operation holds
// it, so a scheduled restart waits before it tells players it restarts now.
func TestTheRunnerHearsWhenAnotherOperationHoldsTheServer(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	e.waitFor("the server to be online and idle", e.onlineIdle)
	state := func() schedule.ServerState {
		t.Helper()
		st, err := scheduleServer{e.srv()}.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := state(); !st.Running || st.Busy {
		t.Fatalf("idle: %+v", st)
	}
	release := make(chan struct{})
	op, err := e.srv().beginOp("backup", "admin", func(context.Context, *opHandle) error {
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st := state()
	close(release)
	e.waitOp(op.ID)
	if !st.Running || !st.Busy {
		t.Fatalf("during another operation: %+v", st)
	}
	if st := state(); st.Busy {
		t.Fatalf("once it ended: %+v", st)
	}
}
