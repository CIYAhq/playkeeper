package retention

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Pace is how automatic backups run, for Estimate.
type Pace struct {
	// Every is the time between automatic backups, zero when they are off.
	Every time.Duration
	// Bytes is a typical backup's size, such as the newest one's.
	Bytes int64
	// Location is the user's time zone, UTC when nil.
	Location *time.Location
}

// Estimate is what one place's rules come to once automatic backups have
// run at a steady pace for long enough: the rules editor's rows ("Every
// backup from the last 24 hours: up to 4") and its total ("About 13
// backups, roughly 4.1 GB").
type Estimate struct {
	Where Where `json:"where"`
	Rows  []Row `json:"rows"`
	// Count is the most backups the rules keep at once, whatever the time
	// of day and day of the week. One backup often counts for several
	// rules, so it is usually less than the rows add up to. Backups that
	// are always kept for another reason, such as being pinned, come on
	// top. It is -1 when the place keeps every backup.
	Count   int   `json:"count"`
	Bytes   int64 `json:"bytes"`
	Summary Text  `json:"summary"`
}

// Row is one rule in the editor, in the order the design lists them.
type Row struct {
	// Rule is "keep_all", "hours", "last", "daily", "weekly", "monthly" or
	// "manual".
	Rule string `json:"rule"`
	N    int    `json:"n"`
	// Count is how many backups the rule keeps by itself. UpTo says it may
	// keep fewer, when automatic backups are skipped because nobody played.
	Count int  `json:"count"`
	UpTo  bool `json:"upTo"`
	Text  Text `json:"text"`
}

// Estimate says what s keeps in one place at pace p.
func (s Settings) Estimate(where Where, p Pace) Estimate {
	r := s.rules(where)
	loc := p.Location
	if loc == nil {
		loc = time.UTC
	}
	e := Estimate{Where: where}
	manual := Row{Rule: "manual", Text: Text{Code: "estimate_manual", Params: map[string]string{"includeManual": strconv.FormatBool(s.IncludeManual)},
		Text: "Backups you make by hand: kept until you delete them."}}
	if s.IncludeManual {
		manual.Text.Text = "Backups you make by hand: the rules above count them like the others."
	}
	params := map[string]string{"where": string(where)}

	if r.KeepAll {
		e.Rows = []Row{{Rule: "keep_all", Count: -1, Text: Text{Code: "estimate_keep_all", Text: "Every backup: kept until you delete it."}}}
		e.Count, e.Bytes = -1, -1
		e.Summary = Text{Code: "estimate_keep_all", Params: params, Text: "Keeps every backup " + place(where) + ", so they take more space over time."}
		return e
	}
	if p.Every <= 0 {
		e.Rows = append(r.rows(0, false), manual)
		e.Summary = Text{Code: "estimate_off", Params: params, Text: "Automatic backups are off, so the rules have nothing to keep yet."}
		return e
	}

	every := max(p.Every, time.Minute)
	hours := 0
	if r.Hours > 0 {
		span := time.Duration(r.Hours) * time.Hour
		hours = int((span + every - 1) / every)
	}
	e.Rows = append(r.rows(hours, true), manual)
	sum := 0
	for _, row := range e.Rows {
		sum += row.Count
	}
	// The overlap between the rules depends on when the newest backup is
	// made, so try each hour of a week and report the most.
	start := time.Date(2026, time.January, 5, 0, 0, 0, 0, loc)
	for h := range 7 * 24 {
		e.Count = max(e.Count, r.steady(start.Add(time.Duration(h)*time.Hour), every, hours, loc))
	}
	e.Bytes = int64(e.Count) * max(p.Bytes, 0)
	params["count"], params["bytes"], params["rulesSum"] = itoa(e.Count), strconv.FormatInt(e.Bytes, 10), itoa(sum)
	text := fmt.Sprintf("About %d %s, roughly %s, %s.", e.Count, plural(e.Count, "backup", "backups"), humanBytes(e.Bytes), place(where))
	if sum > e.Count {
		text += " Some backups count for more than one rule."
	}
	e.Summary = Text{Code: "estimate", Params: params, Text: text}
	return e
}

// rows lists the rules in use. Without automatic backups (on is false) they
// have no count.
func (r Rules) rows(hours int, on bool) []Row {
	var rows []Row
	add := func(rule string, n, count int, upTo bool, label string) {
		text := strings.ToUpper(label[:1]) + label[1:]
		switch {
		case !on:
			count, upTo = 0, false
		case upTo:
			text += ": up to " + itoa(count)
		default:
			text += ": " + itoa(count)
		}
		rows = append(rows, Row{Rule: rule, N: n, Count: count, UpTo: upTo,
			Text: Text{Code: "estimate_" + rule, Params: map[string]string{"n": itoa(n), "count": itoa(count), "upTo": strconv.FormatBool(upTo)}, Text: text + "."}})
	}
	if r.Hours > 0 {
		add("hours", r.Hours, hours, true, hoursPhrase(r.Hours))
	}
	if r.Last > 0 {
		add("last", r.Last, r.Last, false, lastPhrase(r.Last))
	}
	if r.Daily > 0 {
		add("daily", r.Daily, r.Daily, false, dailyPhrase(r.Daily))
	}
	if r.Weekly > 0 {
		add("weekly", r.Weekly, r.Weekly, false, weeklyPhrase(r.Weekly))
	}
	if r.Monthly > 0 {
		add("monthly", r.Monthly, r.Monthly, false, monthlyPhrase(r.Monthly))
	}
	return rows
}

// steady counts the backups r keeps when automatic backups have run every
// `every` for long enough and the newest was made at newest. Backups are
// numbered back from the newest (0); the hours and last rules keep a run of
// the newest ones, and each period rule keeps the newest backup of each of
// its periods.
func (r Rules) steady(newest time.Time, every time.Duration, hours int, loc *time.Location) int {
	run := max(hours, r.Last, 1)
	more := map[int]bool{}
	for _, c := range []struct {
		n  int
		of func(time.Time) (string, time.Time)
	}{{r.Daily, day(loc)}, {r.Weekly, week(loc)}, {r.Monthly, month(loc)}} {
		k := 0
		for range c.n {
			if k >= run {
				more[k] = true
			}
			_, first := c.of(newest.Add(-time.Duration(k) * every))
			y, m, d := first.Date()
			begins := time.Date(y, m, d, 0, 0, 0, 0, loc)
			k = max(int(newest.Sub(begins)/every)+1, k+1)
		}
	}
	return run + len(more)
}
