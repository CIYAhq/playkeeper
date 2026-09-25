package schedule

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TimingKind says how a schedule's times are described.
type TimingKind string

const (
	// Daily runs every day at At.
	Daily TimingKind = "daily"
	// Weekly runs on Days at At.
	Weekly TimingKind = "weekly"
	// Interval runs every EveryHours hours, at At and the times that follow it
	// through the day. EveryHours divides 24, so the times repeat every day.
	Interval TimingKind = "interval"
	// Once runs on Date at At.
	Once TimingKind = "once"
	// Cron runs when a five-field cron expression matches.
	Cron TimingKind = "cron"
)

// Timing says when a schedule runs. Every kind is wall-clock time in TimeZone:
//
//   - a time the clock skips (02:30 on the night the clocks go forward) runs
//     when the clock resumes (03:00);
//   - a time the clock shows twice (02:30 on the night the clocks go back)
//     runs the first time only.
type Timing struct {
	Kind TimingKind `json:"kind"`
	// TimeZone is an IANA name such as "Europe/Berlin". It is copied from the
	// project when the schedule is saved, so the agent can plan on its own.
	TimeZone string `json:"timeZone"`
	// At is "HH:MM" (24-hour) for every kind except Cron.
	At string `json:"at,omitempty"`
	// Days are Weekly's days: "mon", "tue", "wed", "thu", "fri", "sat", "sun".
	Days []string `json:"days,omitempty"`
	// EveryHours is Interval's spacing: 1, 2, 3, 4, 6, 8 or 12.
	EveryHours int `json:"everyHours,omitempty"`
	// Date is Once's day, "YYYY-MM-DD".
	Date string `json:"date,omitempty"`
	// Cron is minute, hour, day of month, month and day of week, or a macro
	// such as @daily.
	Cron string `json:"cron,omitempty"`
	// GraceMinutes is how late a run may still start, for example when the
	// agent was down at the time. Zero uses the default for the schedule's kind.
	GraceMinutes int `json:"graceMinutes,omitempty"`
}

const (
	maxGraceMinutes = 24 * 60
	maxTimeZoneLen  = 64
	// cronSearchDays covers the longest wait between matching days: 29 February
	// can be eight years away (2096 to 2104).
	cronSearchDays = 8*366 + 2
)

var (
	clockRe    = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})$`)
	timeZoneRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]*$`)
	dayNames   = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	everyHours = []int{1, 2, 3, 4, 6, 8, 12}
)

// rule is a compiled Timing.
type rule struct {
	kind TimingKind
	loc  *time.Location
	// mins are minutes after midnight, ascending.
	mins []int
	days [7]bool
	date time.Time
	cron *cronSpec
}

// Next returns the first time after after that t runs, or the zero time if it
// never runs again.
func (t Timing) Next(after time.Time) (time.Time, error) {
	r, err := t.compile()
	if err != nil {
		return time.Time{}, err
	}
	return r.next(after), nil
}

// NextRuns returns up to n run times after after, for previews such as the
// schedule dialog's "Next runs".
func (t Timing) NextRuns(after time.Time, n int) ([]time.Time, error) {
	r, err := t.compile()
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for len(out) < n {
		next := r.next(after)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		after = next
	}
	return out, nil
}

func (t Timing) compile() (*rule, error) {
	loc, err := loadTimeZone(t.TimeZone)
	if err != nil {
		return nil, err
	}
	r := &rule{kind: t.Kind, loc: loc}
	switch t.Kind {
	case Daily, Weekly, Interval, Once:
		at, err := parseClock(t.At)
		if err != nil {
			return nil, err
		}
		r.mins = []int{at}
	case Cron:
		spec, err := parseCron(t.Cron)
		if err != nil {
			return nil, err
		}
		r.cron = spec
		return r, nil
	default:
		return nil, &ValidationError{Field: "timing.kind", Code: "timing_kind_invalid",
			Msg: "Choose when the schedule runs: every day, on chosen days, every few hours, once, or with a cron expression."}
	}
	switch t.Kind {
	case Weekly:
		if len(t.Days) == 0 {
			return nil, &ValidationError{Field: "timing.days", Code: "days_required", Msg: "Choose at least one day of the week."}
		}
		for _, d := range t.Days {
			i := slices.Index(dayNames[:], d)
			if i < 0 {
				return nil, &ValidationError{Field: "timing.days", Code: "day_invalid",
					Msg:    fmt.Sprintf("%s is not a day of the week.", quote(d)),
					Hint:   "Use mon, tue, wed, thu, fri, sat or sun.",
					Params: map[string]any{"value": d}}
			}
			r.days[i] = true
		}
	case Interval:
		if !slices.Contains(everyHours, t.EveryHours) {
			return nil, &ValidationError{Field: "timing.everyHours", Code: "every_hours_invalid",
				Msg:    "Choose every 1, 2, 3, 4, 6, 8 or 12 hours.",
				Hint:   "These repeat at the same times every day. For other patterns, use a cron expression.",
				Params: map[string]any{"value": t.EveryHours}}
		}
		first := r.mins[0] % (t.EveryHours * 60)
		r.mins = r.mins[:0]
		for m := first; m < 24*60; m += t.EveryHours * 60 {
			r.mins = append(r.mins, m)
		}
	case Once:
		d, err := time.Parse(time.DateOnly, t.Date)
		if err != nil || len(t.Date) != len(time.DateOnly) {
			return nil, &ValidationError{Field: "timing.date", Code: "date_invalid",
				Msg:    "Enter the date as YYYY-MM-DD, for example 2026-12-24.",
				Params: map[string]any{"value": t.Date}}
		}
		r.date = d
	}
	return r, nil
}

func (r *rule) next(after time.Time) time.Time {
	switch r.kind {
	case Once:
		t := resolve(r.date.Year(), r.date.Month(), r.date.Day(), r.mins[0]/60, r.mins[0]%60, r.loc)
		if t.After(after) {
			return t
		}
		return time.Time{}
	case Cron:
		return r.cron.next(after, r.loc)
	}
	// A wall time earlier than after's own wall time resolves to an instant
	// no later than after, so the search starts on after's day.
	y, m, d := after.In(r.loc).Date()
	for i := range 9 {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, time.UTC)
		if r.kind == Weekly && !r.days[day.Weekday()] {
			continue
		}
		for _, mins := range r.mins {
			t := resolve(day.Year(), day.Month(), day.Day(), mins/60, mins%60, r.loc)
			if t.After(after) {
				return t
			}
		}
	}
	return time.Time{}
}

// dayTimes returns the minutes after midnight a rule can run at, ascending,
// for the check on how close together runs are.
func (r *rule) dayTimes() []int {
	if r.cron == nil {
		return r.mins
	}
	var out []int
	for h := range 24 {
		for m := range 60 {
			if r.cron.hour[h] && r.cron.minute[m] {
				out = append(out, h*60+m)
			}
		}
	}
	return out
}

// minGap returns the shortest wall-clock gap between two runs, assuming runs
// on consecutive days, or 0 for a rule that runs at most once.
func (r *rule) minGap() time.Duration {
	if r.kind == Once {
		return 0
	}
	times := r.dayTimes()
	if len(times) == 0 {
		return 0
	}
	gap := times[0] + 24*60 - times[len(times)-1]
	for i := 1; i < len(times); i++ {
		gap = min(gap, times[i]-times[i-1])
	}
	return time.Duration(gap) * time.Minute
}

// resolve returns the instant a wall-clock time in loc stands for. A time the
// clock skips resolves to the moment the clock resumes; a time the clock shows
// twice resolves to its first instance.
func resolve(y int, mo time.Month, d, h, mi int, loc *time.Location) time.Time {
	want := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	// time.Date leaves the choice of instance unspecified in both cases, so
	// both are settled here from the zone's bounds.
	t := time.Date(y, mo, d, h, mi, 0, 0, loc)
	got := wallOf(t)
	switch {
	case got.Equal(want):
		if first, ok := earlierInstance(t, want); ok {
			return first
		}
		return t
	case got.After(want):
		start, _ := t.ZoneBounds()
		return start
	default:
		_, end := t.ZoneBounds()
		return end
	}
}

// wallOf returns t's wall-clock reading as a UTC time.
func wallOf(t time.Time) time.Time {
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	return time.Date(y, mo, d, h, mi, s, t.Nanosecond(), time.UTC)
}

// earlierInstance returns the instance of want in the zone period before t's,
// if that period also showed it.
func earlierInstance(t, want time.Time) (time.Time, bool) {
	start, _ := t.ZoneBounds()
	if start.IsZero() {
		return time.Time{}, false
	}
	_, offset := start.Add(-time.Nanosecond).Zone()
	cand := want.Add(-time.Duration(offset) * time.Second)
	if cand.Before(start) && wallOf(cand.In(t.Location())).Equal(want) {
		return cand, true
	}
	return time.Time{}, false
}

func loadTimeZone(name string) (*time.Location, error) {
	bad := func() error {
		return &ValidationError{Field: "timing.timeZone", Code: "time_zone_invalid",
			Msg:    fmt.Sprintf("%s is not a time zone Playkeeper knows.", quote(name)),
			Hint:   "Use a name like Europe/Berlin or America/New_York.",
			Params: map[string]any{"timeZone": name}}
	}
	if name == "" {
		return nil, &ValidationError{Field: "timing.timeZone", Code: "time_zone_required",
			Msg: "Choose a time zone for the schedule.", Hint: "Use a name like Europe/Berlin or America/New_York."}
	}
	// "Local" would follow the host's setting, which can change under a
	// schedule, so only named zones are accepted.
	if len(name) > maxTimeZoneLen || name == "Local" || !timeZoneRe.MatchString(name) {
		return nil, bad()
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, bad()
	}
	return loc, nil
}

func parseClock(s string) (int, error) {
	m := clockRe.FindStringSubmatch(s)
	if m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		if h < 24 && mi < 60 {
			return h*60 + mi, nil
		}
	}
	return 0, &ValidationError{Field: "timing.at", Code: "time_invalid",
		Msg:    "Enter the time as HH:MM on a 24-hour clock, for example 04:00.",
		Params: map[string]any{"value": s}}
}

func formatClock(mins int) string {
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

func (t Timing) normalize() Timing {
	n := Timing{Kind: t.Kind, TimeZone: strings.TrimSpace(t.TimeZone), GraceMinutes: t.GraceMinutes}
	switch t.Kind {
	case Daily:
		n.At = normClock(t.At)
	case Weekly:
		n.At = normClock(t.At)
		n.Days = normDays(t.Days)
	case Interval:
		n.At = normClock(t.At)
		n.EveryHours = t.EveryHours
	case Once:
		n.At = normClock(t.At)
		n.Date = strings.TrimSpace(t.Date)
	case Cron:
		n.Cron = strings.Join(strings.Fields(t.Cron), " ")
	default:
		return t
	}
	return n
}

func normClock(s string) string {
	s = strings.TrimSpace(s)
	if m := clockRe.FindStringSubmatch(s); m != nil && len(m[1]) == 1 {
		return "0" + s
	}
	return s
}

// normDays lower-cases and de-duplicates days and puts them in week order,
// Monday first. Unknown values are kept, last, so validation reports them.
func normDays(days []string) []string {
	var known [7]bool
	var unknown []string
	for _, d := range days {
		d = strings.ToLower(strings.TrimSpace(d))
		if i := slices.Index(dayNames[:], d); i >= 0 {
			known[i] = true
		} else if !slices.Contains(unknown, d) {
			unknown = append(unknown, d)
		}
	}
	var out []string
	for i := range 7 {
		if wd := (i + 1) % 7; known[wd] {
			out = append(out, dayNames[wd])
		}
	}
	return append(out, unknown...)
}

func (t Timing) validate(now time.Time) error {
	r, err := t.compile()
	if err != nil {
		return err
	}
	if t.GraceMinutes < 0 || t.GraceMinutes > maxGraceMinutes {
		return &ValidationError{Field: "timing.graceMinutes", Code: "grace_invalid",
			Msg:    "A late run can start at most 24 hours after its time.",
			Params: map[string]any{"min": 0, "max": maxGraceMinutes}}
	}
	next := r.next(now)
	switch {
	case next.IsZero() && t.Kind == Once:
		return &ValidationError{Field: "timing.date", Code: "once_past", Msg: "Pick a date and time in the future."}
	case next.IsZero():
		return &ValidationError{Field: "timing.cron", Code: "cron_never",
			Msg:  "This cron expression never matches a real date.",
			Hint: "Check the day of month and month, for example 31 only exists in some months."}
	}
	return nil
}

// Summary describes the timing in English, for example "Every day at 04:00".
// The UI builds its own text from the fields; this is for logs and the audit
// trail.
func (t Timing) Summary() string {
	r, err := t.compile()
	if err != nil {
		return "Not valid"
	}
	switch t.Kind {
	case Daily:
		return "Every day at " + t.At
	case Weekly:
		return daysPhrase(r.days) + " at " + t.At
	case Interval:
		if t.EveryHours == 1 {
			return fmt.Sprintf("Every hour at :%02d", r.mins[0]%60)
		}
		times := make([]string, len(r.mins))
		for i, m := range r.mins {
			times[i] = formatClock(m)
		}
		return fmt.Sprintf("Every %d hours, at %s", t.EveryHours, joinAnd(times))
	case Once:
		return "Once, on " + t.Date + " at " + t.At
	case Cron:
		return "Cron: " + t.Cron
	}
	return "Not valid"
}

func daysPhrase(days [7]bool) string {
	var names []string
	count := 0
	for i := range 7 {
		wd := time.Weekday((i + 1) % 7)
		if days[wd] {
			count++
			names = append(names, wd.String()+"s")
		}
	}
	switch {
	case count == 7:
		return "Every day"
	case count == 5 && !days[time.Saturday] && !days[time.Sunday]:
		return "Weekdays"
	case count == 2 && days[time.Saturday] && days[time.Sunday]:
		return "Weekends"
	}
	return joinAnd(names)
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// quote shows a user-supplied value in a message, shortened and with control
// characters escaped.
func quote(s string) string {
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return strconv.Quote(s)
}
