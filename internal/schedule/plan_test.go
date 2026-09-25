package schedule

import (
	"testing"
	"time"
)

var created = utc("2026-01-01T00:00:00Z")

func planned(id string, kind Kind, tm Timing, p Payload) Schedule {
	return Schedule{ID: id, ServerID: "srv1", Kind: kind, Timing: tm, Payload: p, Enabled: true, CreatedAt: created, UpdatedAt: created}
}

func dailyUTC(at string) Timing { return Timing{Kind: Daily, TimeZone: "UTC", At: at} }

func withLastRun(s Schedule, due string) Schedule {
	s.LastRun = &Run{Due: utc(due), Result: ResultSucceeded}
	return s
}

func TestPlanNothingDueWakesAtFirstWarning(t *testing.T) {
	s := withLastRun(planned("r1", KindRestart, dailyUTC("04:00"), Payload{WarnSeconds: DefaultWarnings()}), "2026-05-31T04:00:00Z")
	d := Plan([]Schedule{s}, utc("2026-06-01T03:00:00Z"), created)
	if len(d.Due) != 0 || len(d.Missed) != 0 || !d.Wake.Equal(utc("2026-06-01T03:50:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
}

func TestPlanRestartIsDueFromItsFirstWarning(t *testing.T) {
	s := planned("r1", KindRestart, dailyUTC("04:00"), Payload{WarnSeconds: DefaultWarnings()})
	d := Plan([]Schedule{s}, utc("2026-06-01T03:50:00Z"), created)
	if len(d.Due) != 1 || !d.Due[0].Due.Equal(utc("2026-06-01T04:00:00Z")) || !d.Due[0].Start.Equal(utc("2026-06-01T03:50:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
}

func TestPlanGraceAndMissedReasons(t *testing.T) {
	a := planned("a1", KindAnnouncement, dailyUTC("12:00"), Payload{Message: "hi"})
	cases := []struct {
		now, since string
		due        bool
		reason     Reason
	}{
		{"2026-06-01T12:00:00Z", "2026-06-01T00:00:00Z", true, ""},
		{"2026-06-01T12:05:00Z", "2026-06-01T00:00:00Z", true, ""},
		{"2026-06-01T12:06:00Z", "2026-06-01T00:00:00Z", false, ReasonLate},
		{"2026-06-01T12:06:00Z", "2026-06-01T12:06:00Z", false, ReasonAgentDown},
	}
	for _, c := range cases {
		d := Plan([]Schedule{a}, utc(c.now), utc(c.since))
		switch {
		case c.due && (len(d.Due) != 1 || len(d.Missed) != 0):
			t.Errorf("at %s: Plan = %+v, want due", c.now, d)
		case !c.due && (len(d.Due) != 0 || len(d.Missed) != 1 || d.Missed[0].Reason != c.reason):
			t.Errorf("at %s: Plan = %+v, want missed (%s)", c.now, d, c.reason)
		}
	}
}

func TestPlanRunsOnceAfterDowntime(t *testing.T) {
	// A nightly backup last ran three days ago; the agent is back at 09:00.
	b := withLastRun(planned("b1", KindBackup, dailyUTC("04:00"), Payload{}), "2026-05-29T04:00:00Z")
	now := utc("2026-06-01T09:00:00Z")
	d := Plan([]Schedule{b}, now, now)
	if len(d.Due) != 1 || len(d.Missed) != 0 || !d.Due[0].Due.Equal(utc("2026-06-01T04:00:00Z")) {
		t.Fatalf("Plan = %+v, want one backup for today's 04:00", d)
	}
	// Seven hours late is past the backup's six hours of grace: one missed
	// record, never a burst.
	now = utc("2026-06-01T11:00:00Z")
	d = Plan([]Schedule{b}, now, now)
	if len(d.Due) != 0 || len(d.Missed) != 1 || d.Missed[0].Reason != ReasonAgentDown || !d.Missed[0].Due.Equal(utc("2026-06-01T04:00:00Z")) {
		t.Fatalf("Plan = %+v, want one missed backup", d)
	}
	if !d.Wake.IsZero() {
		t.Fatalf("Wake = %v; schedules with a missed job are planned again after it is recorded", d.Wake)
	}
}

func TestPlanHourlyAfterDowntimeRunsOnlyTheLatest(t *testing.T) {
	c := withLastRun(planned("c1", KindCommand, Timing{Kind: Interval, TimeZone: "UTC", At: "00:00", EveryHours: 1}, Payload{Command: "save-all"}),
		"2026-06-01T05:00:00Z")
	now := utc("2026-06-01T10:10:00Z")
	d := Plan([]Schedule{c}, now, now)
	if len(d.Due) != 1 || !d.Due[0].Due.Equal(utc("2026-06-01T10:00:00Z")) {
		t.Fatalf("Plan = %+v, want only 10:00", d)
	}
}

func TestPlanNeverRepeatsAnOccurrence(t *testing.T) {
	s := withLastRun(planned("r1", KindRestart, dailyUTC("04:00"), Payload{}), "2026-06-01T04:00:00Z")
	d := Plan([]Schedule{s}, utc("2026-06-01T04:01:00Z"), created)
	if len(d.Due)+len(d.Missed) != 0 || !d.Wake.Equal(utc("2026-06-02T04:00:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
}

func TestPlanEditingNeverTriggersACatchUp(t *testing.T) {
	s := planned("r1", KindBackup, dailyUTC("04:00"), Payload{})
	s.UpdatedAt = utc("2026-06-01T04:01:00Z")
	d := Plan([]Schedule{s}, utc("2026-06-01T04:02:00Z"), created)
	if len(d.Due)+len(d.Missed) != 0 || !d.Wake.Equal(utc("2026-06-02T04:00:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
}

func TestPlanSkipsDisabledAndReportsInvalid(t *testing.T) {
	off := planned("x1", KindBackup, dailyUTC("04:00"), Payload{})
	off.Enabled = false
	bad := planned("x2", KindBackup, Timing{Kind: Daily, TimeZone: "Atlantis/Capital", At: "04:00"}, Payload{})
	d := Plan([]Schedule{off, bad}, utc("2026-06-01T04:00:00Z"), created)
	if len(d.Due)+len(d.Missed) != 0 || len(d.Invalid) != 1 || d.Invalid[0].Schedule.ID != "x2" || !d.Wake.IsZero() {
		t.Fatalf("Plan = %+v", d)
	}
}

func TestPlanOrdersJobsDueTogether(t *testing.T) {
	list := []Schedule{
		planned("z9", KindRestart, dailyUTC("04:00"), Payload{}),
		planned("b2", KindBackup, dailyUTC("04:00"), Payload{}),
		planned("c3", KindCommand, dailyUTC("04:00"), Payload{Command: "save-all"}),
		planned("a4", KindAnnouncement, dailyUTC("04:00"), Payload{Message: "hi"}),
		planned("w5", KindRestart, dailyUTC("04:00"), Payload{WarnSeconds: []int{60}}),
	}
	d := Plan(list, utc("2026-06-01T04:00:00Z"), created)
	var got []string
	for _, j := range d.Due {
		got = append(got, j.Schedule.ID)
	}
	want := []string{"w5", "a4", "c3", "b2", "z9"}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestPlanGraceOverride(t *testing.T) {
	s := planned("r1", KindRestart, Timing{Kind: Daily, TimeZone: "UTC", At: "04:00", GraceMinutes: 60}, Payload{})
	if d := Plan([]Schedule{s}, utc("2026-06-01T04:45:00Z"), created); len(d.Due) != 1 {
		t.Fatalf("Plan = %+v, want due within an hour of grace", d)
	}
	s.Timing.GraceMinutes = 0
	if d := Plan([]Schedule{s}, utc("2026-06-01T04:45:00Z"), created); len(d.Missed) != 1 {
		t.Fatalf("Plan = %+v, want missed after the default 15 minutes", d)
	}
}

func TestPlanOnce(t *testing.T) {
	s := planned("o1", KindAnnouncement, Timing{Kind: Once, TimeZone: "UTC", Date: "2026-06-01", At: "12:00"}, Payload{Message: "hi"})
	if d := Plan([]Schedule{s}, utc("2026-06-01T11:00:00Z"), created); !d.Wake.Equal(utc("2026-06-01T12:00:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
	done := withLastRun(s, "2026-06-01T12:00:00Z")
	if d := Plan([]Schedule{done}, utc("2026-06-02T00:00:00Z"), created); len(d.Due)+len(d.Missed) != 0 || !d.Wake.IsZero() {
		t.Fatalf("Plan = %+v", d)
	}
	if n := NextRun(done, utc("2026-06-02T00:00:00Z")); !n.IsZero() {
		t.Fatalf("NextRun = %v, want none", n)
	}
}

func TestPlanLongDowntimeIsCheap(t *testing.T) {
	c := withLastRun(planned("c1", KindCommand, Timing{Kind: Cron, TimeZone: "Europe/Berlin", Cron: "*/5 * * * *"}, Payload{Command: "save-all"}),
		"2024-01-01T00:00:00Z")
	now := utc("2026-06-01T10:02:00Z")
	start := time.Now()
	d := Plan([]Schedule{c}, now, now)
	if len(d.Due) != 1 || !d.Due[0].Due.Equal(utc("2026-06-01T10:00:00Z")) {
		t.Fatalf("Plan = %+v", d)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Plan took %v after a long downtime", took)
	}
}

func TestNextRun(t *testing.T) {
	s := planned("r1", KindRestart, dailyUTC("04:00"), Payload{WarnSeconds: DefaultWarnings()})
	if n := NextRun(s, utc("2026-06-01T03:55:00Z")); !n.Equal(utc("2026-06-01T04:00:00Z")) {
		t.Fatalf("NextRun during the warnings = %v", n)
	}
	s.Enabled = false
	if n := NextRun(s, utc("2026-06-01T03:55:00Z")); !n.IsZero() {
		t.Fatalf("NextRun of a disabled schedule = %v", n)
	}
}

func TestPlanRetriesAnHourAfterPeoplePlaying(t *testing.T) {
	s := planned("r1", KindRestart, dailyUTC("04:00"), Payload{WarnSeconds: []int{600}, SkipIfPlaying: true})
	due, skipped := utc("2026-06-01T04:00:00Z"), utc("2026-06-01T03:50:00Z")
	s.LastRun = &Run{Due: due, Finished: skipped, Result: ResultSkipped, Reason: ReasonPeoplePlaying, RetryAt: s.retryAt(due, skipped)}
	if want := utc("2026-06-01T05:00:00Z"); !s.LastRun.RetryAt.Equal(want) {
		t.Fatalf("retryAt = %v, want %v", s.LastRun.RetryAt, want)
	}
	d := Plan([]Schedule{s}, utc("2026-06-01T04:10:00Z"), created)
	if len(d.Due) != 0 || !d.Wake.Equal(utc("2026-06-01T04:50:00Z")) {
		t.Fatalf("Plan = %+v, want to wake for the retry's first warning", d)
	}
	d = Plan([]Schedule{s}, utc("2026-06-01T04:50:00Z"), created)
	if len(d.Due) != 1 || !d.Due[0].Due.Equal(utc("2026-06-01T05:00:00Z")) || !d.Due[0].Start.Equal(utc("2026-06-01T04:50:00Z")) {
		t.Fatalf("Plan = %+v, want the retry due", d)
	}
	if next := NextRun(s, utc("2026-06-01T04:10:00Z")); !next.Equal(utc("2026-06-01T05:00:00Z")) {
		t.Fatalf("NextRun = %v, want the retry", next)
	}
	edited := s
	edited.UpdatedAt = utc("2026-06-01T04:20:00Z")
	if next := NextRun(edited, utc("2026-06-01T04:30:00Z")); !next.Equal(utc("2026-06-02T04:00:00Z")) {
		t.Fatalf("NextRun after an edit = %v, want tomorrow's run: editing drops the retry", next)
	}
	if d := Plan([]Schedule{edited}, utc("2026-06-01T04:50:00Z"), created); len(d.Due) != 0 {
		t.Fatalf("Plan after an edit = %+v, want no retry", d)
	}
}

func TestRetryNeverOvertakesTheNextRun(t *testing.T) {
	every := planned("b1", KindBackup, Timing{Kind: Interval, TimeZone: "UTC", At: "00:00", EveryHours: 1}, Payload{SkipIfPlaying: true})
	if rt := every.retryAt(utc("2026-06-01T04:00:00Z"), utc("2026-06-01T04:00:00Z")); !rt.IsZero() {
		t.Fatalf("an hourly backup retries at %v; its next run comes first", rt)
	}
	two := planned("b2", KindBackup, Timing{Kind: Interval, TimeZone: "UTC", At: "00:00", EveryHours: 2}, Payload{SkipIfPlaying: true})
	if rt := two.retryAt(utc("2026-06-01T04:00:00Z"), utc("2026-06-01T04:00:05Z")); !rt.Equal(utc("2026-06-01T05:00:05Z")) {
		t.Fatalf("retryAt = %v", rt)
	}
	daily := planned("r1", KindRestart, dailyUTC("04:00"), Payload{SkipIfPlaying: true})
	daily.LastRun = &Run{Due: utc("2026-06-01T04:00:00Z"), Finished: utc("2026-06-01T04:00:00Z"), Result: ResultSkipped,
		Reason: ReasonPeoplePlaying, RetryAt: utc("2026-06-02T04:30:00Z")}
	if next := NextRun(daily, utc("2026-06-01T12:00:00Z")); !next.Equal(utc("2026-06-02T04:00:00Z")) {
		t.Fatalf("NextRun = %v, want the regular run before a later retry", next)
	}
	d := Plan([]Schedule{daily}, utc("2026-06-02T04:00:00Z"), created)
	if len(d.Due) != 1 || !d.Due[0].Due.Equal(utc("2026-06-02T04:00:00Z")) {
		t.Fatalf("Plan = %+v, want the regular run", d)
	}
}
