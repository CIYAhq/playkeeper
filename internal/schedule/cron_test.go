package schedule

import (
	"strings"
	"testing"
)

func TestCronParses(t *testing.T) {
	for _, expr := range []string{
		"0 4 * * 1", "*/5 * * * *", "0 0 1,15 * *", "0 22 * * mon-fri", "0 0 * JAN,jul *",
		"5/15 * * * *", "0 0 * * 7", "0-30/10 8-18 * * *", "@daily", "@hourly", "@WEEKLY", "@yearly",
	} {
		if _, err := parseCron(expr); err != nil {
			t.Errorf("parseCron(%q): %v", expr, err)
		}
	}
}

func TestCronRejects(t *testing.T) {
	for _, expr := range []string{
		"", "0 4 * *", "0 4 * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * 32 * *", "* * * 13 *",
		"* * * * 8", "5-1 * * * *", "*/0 * * * *", "*/61 * * * *", "a * * * *", "@reboot", "@often",
		"1,,2 * * * *", "-1 * * * *", "007 * * * *", "* * * * mon-", strings.Repeat("1,", 60) + "1 * * * *",
	} {
		_, err := parseCron(expr)
		ve, ok := err.(*ValidationError)
		if !ok || ve.Code != "cron_invalid" {
			t.Errorf("parseCron(%q) = %v, want a cron_invalid error", expr, err)
			continue
		}
		if !strings.HasSuffix(ve.Msg, ".") || ve.Hint == "" {
			t.Errorf("parseCron(%q) message %q / hint %q", expr, ve.Msg, ve.Hint)
		}
	}
}

func TestCronDayOfMonthOrDayOfWeek(t *testing.T) {
	// Both restricted: Fridays or the 13th. In April 2026 the 13th is a Monday.
	either := Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 0 13 * 5"}
	equalRuns(t, runs(t, either, "2026-04-01T00:00:00Z", 4),
		"2026-04-03T00:00:00Z", "2026-04-10T00:00:00Z", "2026-04-13T00:00:00Z", "2026-04-17T00:00:00Z")
	// One of them is *: both must match.
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 0 13 * *"}, "2026-04-01T00:00:00Z", 2),
		"2026-04-13T00:00:00Z", "2026-05-13T00:00:00Z")
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "30 18 * * sun"}, "2026-04-01T00:00:00Z", 2),
		"2026-04-05T18:30:00Z", "2026-04-12T18:30:00Z")
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 0 * * 7"}, "2026-04-01T00:00:00Z", 1),
		"2026-04-05T00:00:00Z")
}

func TestCronLeapDay(t *testing.T) {
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "0 12 29 2 *"}, "2026-01-01T00:00:00Z", 2),
		"2028-02-29T12:00:00Z", "2032-02-29T12:00:00Z")
}

func TestCronAcrossClockChanges(t *testing.T) {
	requireTransition(t, "Europe/Berlin", "2026-03-29T01:00:00Z", 1*h, 2*h)
	requireTransition(t, "Europe/Berlin", "2026-10-25T01:00:00Z", 2*h, 1*h)
	quarter := Timing{Kind: Cron, TimeZone: "Europe/Berlin", Cron: "*/15 * * * *"}
	// 02:00 to 02:45 do not exist; they fold into 03:00.
	equalRuns(t, runs(t, quarter, "2026-03-29T00:40:00Z", 3),
		"2026-03-29T00:45:00Z", "2026-03-29T01:00:00Z", "2026-03-29T01:15:00Z")
	half := Timing{Kind: Cron, TimeZone: "Europe/Berlin", Cron: "*/30 * * * *"}
	// The second pass through 02:00 to 02:59 does not run again.
	equalRuns(t, runs(t, half, "2026-10-24T23:15:00Z", 4),
		"2026-10-24T23:30:00Z", "2026-10-25T00:00:00Z", "2026-10-25T00:30:00Z", "2026-10-25T02:00:00Z")
	equalRuns(t, runs(t, half, "2026-10-25T01:15:00Z", 1), "2026-10-25T02:00:00Z")
}

func TestCronMacros(t *testing.T) {
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "@weekly"}, "2026-04-01T00:00:00Z", 1), "2026-04-05T00:00:00Z")
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "@monthly"}, "2026-04-01T00:00:00Z", 1), "2026-05-01T00:00:00Z")
	equalRuns(t, runs(t, Timing{Kind: Cron, TimeZone: "UTC", Cron: "@hourly"}, "2026-04-01T00:00:00Z", 2), "2026-04-01T01:00:00Z", "2026-04-01T02:00:00Z")
}
