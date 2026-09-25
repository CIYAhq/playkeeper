package retention

import (
	"fmt"
	"strconv"
	"strings"
)

func place(where Where) string {
	if where == OffSite {
		return "off the server"
	}
	return "on this machine"
}

func isRule(code string) bool {
	switch code {
	case "keep_all", "hours", "last", "daily", "weekly", "monthly":
		return true
	}
	return false
}

func itoa(n int) string { return strconv.Itoa(n) }

func pinnedText() Text {
	return Text{Code: "pinned", Text: "Pinned: the rules never delete it."}
}

func neededText(by string) Text {
	what := "A restore or update"
	switch by {
	case "restore":
		what = "A restore"
	case "update":
		what = "An update"
	}
	return Text{Code: "needed", Params: map[string]string{"by": by}, Text: what + " that hasn't finished may still need it to undo its changes."}
}

func keepAllText(where Where) Text {
	return Text{Code: "keep_all", Params: map[string]string{"where": string(where)}, Text: "The rules keep every backup " + place(where) + "."}
}

func hoursText(n, rank int) Text {
	s := "Made in the " + hoursSpan(n) + " up to the newest backup"
	if rank == 1 {
		s = "The newest backup"
	}
	return Text{Code: "hours", Params: map[string]string{"n": itoa(n), "rank": itoa(rank)}, Text: s + " (" + hoursPhrase(n) + ")."}
}

func latestText() Text {
	return Text{Code: "latest", Text: "The newest backup is always kept."}
}

func lastText(n, rank int) Text {
	s := fmt.Sprintf("One of the newest %d backups.", n)
	if n == 1 {
		s = "The newest backup."
	}
	return Text{Code: "last", Params: map[string]string{"n": itoa(n), "rank": itoa(rank)}, Text: s}
}

func periodText(code, of string, p period, n int, phrase string) Text {
	s := "The newest backup of " + of
	checked := p.kept.ID != p.newest.ID
	if checked {
		s += " that passed its check"
	}
	params := map[string]string{"n": itoa(n), "checked": strconv.FormatBool(checked)}
	switch code {
	case "daily":
		params["date"] = p.key
	case "weekly":
		params["week"], params["weekStart"] = p.key, p.first.Format("2006-01-02")
	case "monthly":
		params["month"] = p.key
	}
	return Text{Code: code, Params: params, Text: s + " (" + phrase + ")."}
}

func dayName(p period) string   { return p.first.Format("Monday 2 January 2006") }
func weekName(p period) string  { return "the week starting " + p.first.Format("Monday 2 January 2006") }
func monthName(p period) string { return p.first.Format("January 2006") }

func dailyText(p period, n int) Text {
	return periodText("daily", dayName(p), p, n, dailyPhrase(n))
}

func weeklyText(p period, n int) Text {
	return periodText("weekly", weekName(p), p, n, weeklyPhrase(n))
}

func monthlyText(p period, n int) Text {
	return periodText("monthly", monthName(p), p, n, monthlyPhrase(n))
}

func newestText(where Where) Text {
	s := "The newest backup that passed its check is always kept."
	if where == OffSite {
		s = "The newest copy off the server is always kept."
	}
	return Text{Code: "newest", Params: map[string]string{"where": string(where)}, Text: s}
}

func manualText() Text {
	return Text{Code: "manual", Text: "Made by hand: the rules only delete backups made automatically."}
}

func (oc onlyCopy) params(where Where) map[string]string {
	p := map[string]string{"what": oc.what, "where": string(where)}
	if oc.what != beforeRestore {
		p["value"], p["newer"] = oc.value, oc.newer
	}
	return p
}

func (oc onlyCopy) sentence() string {
	switch oc.what {
	case lastOnVersion:
		return "It is the last backup of your world on Minecraft " + oc.value + " (newer backups are on " + oc.newer + ")"
	case lastOfWorld:
		return "It is the last backup of the world \"" + oc.value + "\" (newer backups are of \"" + oc.newer + "\")"
	}
	return "It holds the world as it was before a restore replaced it"
}

func onlyCopyText(oc onlyCopy, where Where) Text {
	s := oc.sentence() + ", and it hasn't been downloaded or copied off the server. Download it or copy it off the server to let the rules delete it here."
	if where == OffSite {
		s = oc.sentence() + ", and this is its only copy: it isn't kept on this machine and hasn't been downloaded. Download it to let the rules delete it off the server."
	}
	return Text{Code: "only_copy", Params: oc.params(where), Text: s}
}

func onlyCopyAllowedText(oc onlyCopy, where Where) Text {
	return Text{Code: "only_copy_allowed", Params: oc.params(where),
		Text: oc.sentence() + ", and no other copy of it exists, but your rules allow deleting backups like this."}
}

func failedNewestText() Text {
	return Text{Code: "failed_newest", Text: "Its check failed, but no newer backup passed its check, so it is kept for now."}
}

func failedText() Text {
	return Text{Code: "failed_check", Text: "Its check failed, so it can't be restored, and a newer backup passed its check."}
}

func sameText(code string, p period, n int) Text {
	var of, phrase string
	params := map[string]string{"kept": p.kept.ID, "n": itoa(n)}
	switch code {
	case "same_day":
		of, phrase, params["date"] = dayName(p), dailyPhrase(n), p.key
	case "same_week":
		of, phrase, params["week"] = weekName(p), weeklyPhrase(n), p.key
		params["weekStart"] = p.first.Format("2006-01-02")
	default:
		of, phrase, params["month"] = monthName(p), monthlyPhrase(n), p.key
	}
	return Text{Code: code, Params: params, Text: "Another backup from " + of + " is kept (" + phrase + ")."}
}

func beyondText(r Rules, where Where) Text {
	return Text{Code: "beyond_rules", Params: r.params(where), Text: "Older than what the rules keep " + place(where) + ": " + r.keeps() + "."}
}

func noCopyLeftText(b Backup, where Where) Text {
	var s string
	other := false
	switch {
	case where == OnHost && b.OffSite:
		s, other = "Its copy off the server is deleted too, and it hasn't been downloaded, so no copy of it will be left.", true
	case where == OnHost:
		s = "It hasn't been downloaded or copied off the server, so no copy of it will be left."
	case b.OnHost:
		s, other = "Its copy on this machine is deleted too, and it hasn't been downloaded, so no copy of it will be left.", true
	default:
		s = "It is no longer on this machine and hasn't been downloaded, so no copy of it will be left."
	}
	return Text{Code: "no_copy_left", Params: map[string]string{"where": string(where), "otherDeleted": strconv.FormatBool(other)}, Text: s}
}

// hoursSpan is n hours in words after "the last", e.g. "hour", "24 hours"
// or "3 days".
func hoursSpan(n int) string {
	switch {
	case n == 1:
		return "hour"
	case n%24 == 0 && n > 24:
		return fmt.Sprintf("%d days", n/24)
	}
	return fmt.Sprintf("%d hours", n)
}

func hoursPhrase(n int) string { return "every backup from the last " + hoursSpan(n) }

func lastPhrase(n int) string {
	if n == 1 {
		return "the newest"
	}
	return fmt.Sprintf("the newest %d", n)
}

func dailyPhrase(n int) string {
	return fmt.Sprintf("one a day for %d %s", n, plural(n, "day", "days"))
}

func weeklyPhrase(n int) string {
	switch {
	case n == 52:
		return "one a week for a year"
	case n%52 == 0:
		return fmt.Sprintf("one a week for %d years", n/52)
	}
	return fmt.Sprintf("one a week for %d %s", n, plural(n, "week", "weeks"))
}

func monthlyPhrase(n int) string {
	switch {
	case n == 12:
		return "one a month for a year"
	case n%12 == 0:
		return fmt.Sprintf("one a month for %d years", n/12)
	}
	return fmt.Sprintf("one a month for %d %s", n, plural(n, "month", "months"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// joinAnd joins "a", "b" and "c" as "a, b and c".
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// phrase is the rules in words, e.g. "every backup from the last 24 hours,
// one a day for 7 days and one a week for 4 weeks".
func (r Rules) phrase() string {
	if r.KeepAll {
		return "every backup"
	}
	var parts []string
	if r.Hours > 0 {
		parts = append(parts, hoursPhrase(r.Hours))
	}
	if r.Last > 0 {
		parts = append(parts, lastPhrase(r.Last))
	}
	if r.Daily > 0 {
		parts = append(parts, dailyPhrase(r.Daily))
	}
	if r.Weekly > 0 {
		parts = append(parts, weeklyPhrase(r.Weekly))
	}
	if r.Monthly > 0 {
		parts = append(parts, monthlyPhrase(r.Monthly))
	}
	return joinAnd(parts)
}

// keeps is phrase for a sentence that needs one even when no rule is set.
func (r Rules) keeps() string {
	if p := r.phrase(); p != "" {
		return p
	}
	return "only the backups that are always kept"
}

func (r Rules) params(where Where) map[string]string {
	return map[string]string{"where": string(where), "keepAll": strconv.FormatBool(r.KeepAll), "hours": itoa(r.Hours),
		"last": itoa(r.Last), "daily": itoa(r.Daily), "weekly": itoa(r.Weekly), "monthly": itoa(r.Monthly)}
}

// Describe says in a sentence what r keeps in one place.
func (r Rules) Describe(where Where) Text {
	return Text{Code: "rules", Params: r.params(where), Text: "Keeps " + r.keeps() + " " + place(where) + "."}
}

// Describe explains s for the rules editor: what each place keeps, how
// periods are counted, and which backups are always kept.
func (s Settings) Describe() []Text {
	always := []string{"pinned backups", "the newest backup", "the newest backup that passed its check", "backups a restore or update may still need"}
	if !s.IncludeManual {
		always = append(always, "backups made by hand")
	}
	if !s.DeleteOnlyCopies {
		always = append(always, "backups that are the only copy of an earlier world (from before a restore, or the last one on an earlier Minecraft version or of another world) until they are downloaded or copied off the server")
	}
	return []Text{
		s.rules(OnHost).Describe(OnHost),
		s.rules(OffSite).Describe(OffSite),
		{Code: "gaps", Text: "The last hours are counted back from the newest backup, and days, weeks and months without a backup don't count, so a pause in backups never makes the rules delete older ones."},
		{Code: "always_kept", Params: map[string]string{"includeManual": strconv.FormatBool(s.IncludeManual), "deleteOnlyCopies": strconv.FormatBool(s.DeleteOnlyCopies)},
			Text: "Always kept: " + joinAnd(always) + "."},
	}
}

func summaryText(p Plan, r Rules, extra map[string]int) Text {
	params := r.params(p.Where)
	params["keep"], params["delete"] = itoa(p.Keep), itoa(p.Delete)
	params["keepBytes"], params["deleteBytes"] = strconv.FormatInt(p.KeepBytes, 10), strconv.FormatInt(p.DeleteBytes, 10)
	if len(p.Decisions) == 0 {
		s := "No backups are kept on this machine."
		if p.Where == OffSite {
			s = "No backups are copied off the server."
		}
		return Text{Code: "summary_empty", Params: params, Text: s}
	}
	kept := fmt.Sprintf("%d %s (%s) %s", p.Keep, plural(p.Keep, "backup", "backups"), humanBytes(p.KeepBytes), place(p.Where))
	if r.KeepAll {
		return Text{Code: "summary", Params: params, Text: "Keeps all " + kept + "."}
	}
	newest := "the newest that passed its check"
	if p.Where == OffSite {
		newest = "the newest copy"
	}
	var plus []string
	for _, e := range []struct {
		code      string
		one, many string
	}{
		{"pinned", "1 pinned", "%d pinned"},
		{"needed", "1 a restore or update still needs", "%d a restore or update still needs"},
		{"latest", "the newest", "the newest"},
		{"newest", newest, newest},
		{"manual", "1 made by hand", "%d made by hand"},
		{"only_copy", "1 that is the only copy of an earlier world", "%d that are the only copies of earlier worlds"},
		{"failed_newest", "1 whose check failed", "%d whose checks failed"},
	} {
		n := extra[e.code]
		params[e.code] = itoa(n)
		switch {
		case n == 1:
			plus = append(plus, e.one)
		case n > 1:
			plus = append(plus, strings.ReplaceAll(e.many, "%d", itoa(n)))
		}
	}
	s := "Keeps " + kept
	switch rules := r.phrase(); {
	case rules != "" && len(plus) > 0:
		s += ": " + rules + ", plus " + joinAnd(plus)
	case rules != "":
		s += ": " + rules
	case len(plus) > 0:
		s += ": " + joinAnd(plus)
	}
	s += "."
	if p.Delete == 0 {
		s += " Deletes none."
	} else {
		s += fmt.Sprintf(" Deletes %d (%s).", p.Delete, humanBytes(p.DeleteBytes))
	}
	return Text{Code: "summary", Params: params, Text: s}
}

func isoWeek(year, week int) string { return fmt.Sprintf("%04d-W%02d", year, week) }

// humanBytes writes n in decimal units the way the design does: "312 MB",
// "4.6 GB".
func humanBytes(n int64) string {
	const k, m, g, t = 1_000, 1_000_000, 1_000_000_000, 1_000_000_000_000
	switch {
	case n >= t || (n+g/20)/(g/10) >= 10*k:
		return fmt.Sprintf("%.1f TB", float64(n)/t)
	case n >= g || (n+m/2)/m >= k:
		return fmt.Sprintf("%.1f GB", float64(n)/g)
	case n >= m || (n+k/2)/k >= k:
		return fmt.Sprintf("%d MB", (n+m/2)/m)
	case n >= k:
		return fmt.Sprintf("%d kB", (n+k/2)/k)
	}
	return fmt.Sprintf("%d B", n)
}

// Error is a setting Validate refuses. Field names it, e.g. "offSite.daily".
type Error struct {
	Kind  string // "out_of_range"
	Field string
	Value int
	Min   int
	Max   int
	Msg   string
	Hint  string
}

func (e *Error) Error() string { return e.Msg }

// Validate checks that every number is in range, including those a KeepAll
// place ignores, so switching KeepAll off never brings back a bad value.
func (s Settings) Validate() error {
	for _, c := range []struct {
		where Where
		field string
		r     Rules
	}{{OnHost, "onHost", s.OnHost}, {OffSite, "offSite", s.OffSite}} {
		for _, f := range []struct {
			name     string
			v        int
			min, max int
			what     string
			hint     string
		}{
			{"hours", c.r.Hours, 0, MaxHours, "The number of hours to keep every backup for", "Use 0 to let the other rules decide alone. The most is 720 hours, which is 30 days."},
			{"last", c.r.Last, 0, MaxLast, "The number of newest backups to keep", "Use 0 to keep no fixed number of newest backups; the newest one is always kept."},
			{"daily", c.r.Daily, 0, MaxDaily, "The number of days to keep one backup a day for", "Use 0 to keep no daily backups."},
			{"weekly", c.r.Weekly, 0, MaxWeekly, "The number of weeks to keep one backup a week for", "Use 0 to keep no weekly backups."},
			{"monthly", c.r.Monthly, 0, MaxMonthly, "The number of months to keep one backup a month for", "Use 0 to keep no monthly backups."},
		} {
			if f.v < f.min || f.v > f.max {
				return &Error{Kind: "out_of_range", Field: c.field + "." + f.name, Value: f.v, Min: f.min, Max: f.max,
					Msg:  fmt.Sprintf("%s %s must be between %d and %d, not %d.", f.what, place(c.where), f.min, f.max, f.v),
					Hint: f.hint}
			}
		}
	}
	return nil
}
