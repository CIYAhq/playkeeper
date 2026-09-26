package agent

import (
	"context"
	"encoding/json"
	"net/http"
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
