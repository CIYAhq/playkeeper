package schedule

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// requireTransition skips the test when this host's time zone data lacks the
// clock change the test is built around.
func requireTransition(t *testing.T, zone, at string, before, after time.Duration) {
	t.Helper()
	loc := mustLoc(t, zone)
	tr := utc(at)
	_, ob := tr.Add(-time.Second).In(loc).Zone()
	_, oa := tr.In(loc).Zone()
	if time.Duration(ob)*time.Second != before || time.Duration(oa)*time.Second != after {
		t.Skipf("%s on this host does not change from %v to %v at %s", zone, before, after, at)
	}
}

func runs(t *testing.T, tm Timing, after string, n int) []string {
	t.Helper()
	got, err := tm.NextRuns(utc(after), n)
	if err != nil {
		t.Fatalf("NextRuns: %v", err)
	}
	out := make([]string, len(got))
	for i, g := range got {
		out[i] = g.UTC().Format(time.RFC3339)
	}
	return out
}

func equalRuns(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d runs %v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("run %d = %s, want %s (all: %v)", i, got[i], want[i], got)
		}
	}
}

const h = time.Hour

func TestDailySkippedTimeRunsWhenClockResumes(t *testing.T) {
	requireTransition(t, "Europe/Berlin", "2026-03-29T01:00:00Z", 1*h, 2*h)
	daily := Timing{Kind: Daily, TimeZone: "Europe/Berlin", At: "02:30"}
	// 02:30 does not exist on 29 March; the clock jumps from 02:00 to 03:00.
	equalRuns(t, runs(t, daily, "2026-03-27T12:00:00Z", 4),
		"2026-03-28T01:30:00Z", // 02:30 CET
		"2026-03-29T01:00:00Z", // 03:00 CEST, when the clock resumes
		"2026-03-30T00:30:00Z", // 02:30 CEST
		"2026-03-31T00:30:00Z")
}

func TestDailyRepeatedTimeRunsFirstTimeOnly(t *testing.T) {
	requireTransition(t, "Europe/Berlin", "2026-10-25T01:00:00Z", 2*h, 1*h)
	daily := Timing{Kind: Daily, TimeZone: "Europe/Berlin", At: "02:30"}
	// 02:30 happens twice on 25 October: 00:30Z (CEST) and 01:30Z (CET).
	equalRuns(t, runs(t, daily, "2026-10-23T12:00:00Z", 4),
		"2026-10-24T00:30:00Z",
		"2026-10-25T00:30:00Z", // first 02:30
		"2026-10-26T01:30:00Z", // not 2026-10-25T01:30Z, the second 02:30
		"2026-10-27T01:30:00Z")
	// Asked from between the two 02:30s, the next run is the next day.
	equalRuns(t, runs(t, daily, "2026-10-25T01:00:00Z", 1), "2026-10-26T01:30:00Z")
}

func TestNewYorkClockChanges(t *testing.T) {
	requireTransition(t, "America/New_York", "2026-03-08T07:00:00Z", -5*h, -4*h)
	requireTransition(t, "America/New_York", "2026-11-01T06:00:00Z", -4*h, -5*h)
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "America/New_York", At: "02:30"}, "2026-03-07T12:00:00Z", 2),
		"2026-03-08T07:00:00Z", // 03:00 EDT
		"2026-03-09T06:30:00Z")
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "America/New_York", At: "01:30"}, "2026-10-31T12:00:00Z", 2),
		"2026-11-01T05:30:00Z", // 01:30 EDT, the first one
		"2026-11-02T06:30:00Z")
}

func TestHalfHourClockChanges(t *testing.T) {
	// Lord Howe Island moves its clocks by 30 minutes.
	requireTransition(t, "Australia/Lord_Howe", "2025-10-04T15:30:00Z", 10*h+30*time.Minute, 11*h)
	requireTransition(t, "Australia/Lord_Howe", "2025-04-05T15:00:00Z", 11*h, 10*h+30*time.Minute)
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "Australia/Lord_Howe", At: "02:15"}, "2025-10-04T00:00:00Z", 2),
		"2025-10-04T15:30:00Z", // 02:30 +11, when the clock resumes
		"2025-10-05T15:15:00Z")
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "Australia/Lord_Howe", At: "01:45"}, "2025-04-05T00:00:00Z", 2),
		"2025-04-05T14:45:00Z", // 01:45 +11; 01:45 +10:30 is skipped
		"2025-04-06T15:15:00Z")
}

func TestMidnightClockChanges(t *testing.T) {
	// Chile changes its clocks at midnight, so a day can start at 01:00.
	requireTransition(t, "America/Santiago", "2025-09-07T04:00:00Z", -4*h, -3*h)
	requireTransition(t, "America/Santiago", "2025-04-06T03:00:00Z", -3*h, -4*h)
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "America/Santiago", At: "00:30"}, "2025-09-06T00:00:00Z", 3),
		"2025-09-06T04:30:00Z",
		"2025-09-07T04:00:00Z", // 01:00 -03 on Sunday
		"2025-09-08T03:30:00Z")
	sunday := Timing{Kind: Weekly, TimeZone: "America/Santiago", At: "00:30", Days: []string{"sun"}}
	equalRuns(t, runs(t, sunday, "2025-09-01T00:00:00Z", 2), "2025-09-07T04:00:00Z", "2025-09-14T03:30:00Z")
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "America/Santiago", At: "23:30"}, "2025-04-05T12:00:00Z", 2),
		"2025-04-06T02:30:00Z", // 23:30 -03, the first one
		"2025-04-07T03:30:00Z")
}

func TestSkippedDayStillRunsOnce(t *testing.T) {
	// Samoa skipped 30 December 2011 entirely.
	requireTransition(t, "Pacific/Apia", "2011-12-30T10:00:00Z", -10*h, 14*h)
	equalRuns(t, runs(t, Timing{Kind: Daily, TimeZone: "Pacific/Apia", At: "12:00"}, "2011-12-29T00:00:00Z", 3),
		"2011-12-29T22:00:00Z",
		"2011-12-30T10:00:00Z", // 31 December 00:00, when the clock resumes
		"2011-12-30T22:00:00Z")
}

func TestIntervalKeepsClockTimesAcrossChanges(t *testing.T) {
	requireTransition(t, "Europe/Berlin", "2026-03-29T01:00:00Z", 1*h, 2*h)
	requireTransition(t, "Europe/Berlin", "2026-10-25T01:00:00Z", 2*h, 1*h)
	hourly := Timing{Kind: Interval, TimeZone: "Europe/Berlin", At: "00:00", EveryHours: 1}
	// 02:00 is skipped, so it folds into 03:00: one run, not two.
	equalRuns(t, runs(t, hourly, "2026-03-28T22:30:00Z", 4),
		"2026-03-28T23:00:00Z", "2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z", "2026-03-29T02:00:00Z")
	// 02:00 happens twice; only the first runs.
	equalRuns(t, runs(t, hourly, "2026-10-24T21:30:00Z", 5),
		"2026-10-24T22:00:00Z", "2026-10-24T23:00:00Z", "2026-10-25T00:00:00Z", "2026-10-25T02:00:00Z", "2026-10-25T03:00:00Z")

	sixHourly := Timing{Kind: Interval, TimeZone: "Europe/Berlin", At: "22:00", EveryHours: 6}
	equalRuns(t, runs(t, sixHourly, "2026-06-01T00:00:00Z", 4),
		"2026-06-01T02:00:00Z", "2026-06-01T08:00:00Z", "2026-06-01T14:00:00Z", "2026-06-01T20:00:00Z")
}

func TestWeekly(t *testing.T) {
	// 2026-06-01 is a Monday.
	w := Timing{Kind: Weekly, TimeZone: "UTC", At: "20:00", Days: []string{"wed", "sat"}}
	equalRuns(t, runs(t, w, "2026-06-01T00:00:00Z", 3), "2026-06-03T20:00:00Z", "2026-06-06T20:00:00Z", "2026-06-10T20:00:00Z")
	equalRuns(t, runs(t, w, "2026-06-03T20:00:00Z", 1), "2026-06-06T20:00:00Z")
}

func TestOnce(t *testing.T) {
	requireTransition(t, "Europe/Berlin", "2026-10-25T01:00:00Z", 2*h, 1*h)
	once := Timing{Kind: Once, TimeZone: "Europe/Berlin", Date: "2026-10-25", At: "02:30"}
	equalRuns(t, runs(t, once, "2026-10-01T00:00:00Z", 3), "2026-10-25T00:30:00Z")
	equalRuns(t, runs(t, once, "2026-10-25T00:30:00Z", 1))
	gap := Timing{Kind: Once, TimeZone: "Europe/Berlin", Date: "2026-03-29", At: "02:30"}
	equalRuns(t, runs(t, gap, "2026-03-01T00:00:00Z", 1), "2026-03-29T01:00:00Z")
}

func TestTimingErrors(t *testing.T) {
	now := utc("2026-06-01T00:00:00Z")
	cases := []struct {
		name string
		tm   Timing
		code string
	}{
		{"no time zone", Timing{Kind: Daily, At: "04:00"}, "time_zone_required"},
		{"local", Timing{Kind: Daily, TimeZone: "Local", At: "04:00"}, "time_zone_invalid"},
		{"unknown zone", Timing{Kind: Daily, TimeZone: "Mars/Olympus_Mons", At: "04:00"}, "time_zone_invalid"},
		{"path", Timing{Kind: Daily, TimeZone: "../../etc/passwd", At: "04:00"}, "time_zone_invalid"},
		{"absolute path", Timing{Kind: Daily, TimeZone: "/etc/localtime", At: "04:00"}, "time_zone_invalid"},
		{"hour 24", Timing{Kind: Daily, TimeZone: "UTC", At: "24:00"}, "time_invalid"},
		{"minute 60", Timing{Kind: Daily, TimeZone: "UTC", At: "04:60"}, "time_invalid"},
		{"words", Timing{Kind: Daily, TimeZone: "UTC", At: "noon"}, "time_invalid"},
		{"seconds", Timing{Kind: Daily, TimeZone: "UTC", At: "04:00:00"}, "time_invalid"},
		{"no days", Timing{Kind: Weekly, TimeZone: "UTC", At: "04:00"}, "days_required"},
		{"bad day", Timing{Kind: Weekly, TimeZone: "UTC", At: "04:00", Days: []string{"funday"}}, "day_invalid"},
		{"every 5 hours", Timing{Kind: Interval, TimeZone: "UTC", At: "04:00", EveryHours: 5}, "every_hours_invalid"},
		{"every 24 hours", Timing{Kind: Interval, TimeZone: "UTC", At: "04:00", EveryHours: 24}, "every_hours_invalid"},
		{"30 February", Timing{Kind: Once, TimeZone: "UTC", At: "04:00", Date: "2026-02-30"}, "date_invalid"},
		{"date format", Timing{Kind: Once, TimeZone: "UTC", At: "04:00", Date: "1/6/2026"}, "date_invalid"},
		{"once in the past", Timing{Kind: Once, TimeZone: "UTC", At: "04:00", Date: "2026-05-31"}, "once_past"},
		{"cron never", Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 0 30 2 *"}, "cron_never"},
		{"cron bad", Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 25 * * *"}, "cron_invalid"},
		{"grace", Timing{Kind: Daily, TimeZone: "UTC", At: "04:00", GraceMinutes: 1441}, "grace_invalid"},
		{"kind", Timing{Kind: "fortnightly", TimeZone: "UTC", At: "04:00"}, "timing_kind_invalid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.tm.validate(now)
			ve, ok := err.(*ValidationError)
			if !ok || ve.Code != c.code {
				t.Fatalf("validate = %v, want code %s", err, c.code)
			}
			if ve.Msg == "" || ve.Msg[len(ve.Msg)-1] != '.' {
				t.Fatalf("message %q is not a sentence", ve.Msg)
			}
		})
	}
}

func TestTimingNormalize(t *testing.T) {
	got := Timing{Kind: Weekly, TimeZone: " Europe/Berlin ", At: " 4:05", Days: []string{"SUN", "mon", "Mon", "xyz"}, Cron: "0 4 * * *", EveryHours: 3}.normalize()
	if got.At != "04:05" || got.TimeZone != "Europe/Berlin" || got.Cron != "" || got.EveryHours != 0 {
		t.Fatalf("normalize = %+v", got)
	}
	if want := []string{"mon", "sun", "xyz"}; len(got.Days) != 3 || got.Days[0] != want[0] || got.Days[1] != want[1] || got.Days[2] != want[2] {
		t.Fatalf("days = %v, want %v", got.Days, want)
	}
	if c := (Timing{Kind: Cron, Cron: "  0   4 * *  1 ", At: "04:00"}).normalize(); c.Cron != "0 4 * * 1" || c.At != "" {
		t.Fatalf("cron normalize = %+v", c)
	}
}

func TestSummary(t *testing.T) {
	cases := []struct {
		tm   Timing
		want string
	}{
		{Timing{Kind: Daily, TimeZone: "UTC", At: "04:00"}, "Every day at 04:00"},
		{Timing{Kind: Weekly, TimeZone: "UTC", At: "04:00", Days: []string{"mon", "fri"}}, "Mondays and Fridays at 04:00"},
		{Timing{Kind: Weekly, TimeZone: "UTC", At: "04:00", Days: []string{"mon", "tue", "wed", "thu", "fri"}}, "Weekdays at 04:00"},
		{Timing{Kind: Weekly, TimeZone: "UTC", At: "10:00", Days: []string{"sat", "sun"}}, "Weekends at 10:00"},
		{Timing{Kind: Interval, TimeZone: "UTC", At: "00:15", EveryHours: 1}, "Every hour at :15"},
		{Timing{Kind: Interval, TimeZone: "UTC", At: "04:00", EveryHours: 6}, "Every 6 hours, at 04:00, 10:00, 16:00 and 22:00"},
		{Timing{Kind: Once, TimeZone: "UTC", At: "18:00", Date: "2026-12-24"}, "Once, on 2026-12-24 at 18:00"},
		{Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 4 * * 1"}, "Cron: 0 4 * * 1"},
		{Timing{Kind: Daily, TimeZone: "Nowhere/Nothing", At: "04:00"}, "Not valid"},
	}
	for _, c := range cases {
		if got := c.tm.Summary(); got != c.want {
			t.Errorf("Summary(%+v) = %q, want %q", c.tm, got, c.want)
		}
	}
}
