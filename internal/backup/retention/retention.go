// Package retention decides which of one server's backups the rules keep
// and which they delete, on this machine and off the server: the newest few,
// then one a day, one a week and one a month. It is a pure function of the
// backups and the rules. It deletes nothing itself, and every decision
// carries plain reasons with a stable code for translation.
//
// Days, weeks and months are counted in the user's time zone, and only those
// that have a backup count, so a pause in backups never makes the rules
// delete older ones. Some backups are always kept: pinned ones, the newest
// one that passed its check, ones a restore or update may still need, and
// ones that are the only copy of something no other backup has (the world
// as it was before a restore replaced it, or the last world on an earlier
// Minecraft version) unless they were downloaded or copied off the server,
// or Settings.DeleteOnlyCopies says otherwise.
package retention

import (
	"cmp"
	"slices"
	"time"
)

// Backup kinds. Playkeeper 0.2 records KindManual and KindRollback. The
// others are for the agent to record once schedules exist.
const (
	KindManual        = "manual"
	KindScheduled     = "scheduled"
	KindBeforeRestore = "before-restore"
	KindBeforeUpdate  = "before-update"
	// KindRollback is an automatic archive from before a restore or an
	// update. Which of the two was not recorded, so it is treated like
	// KindBeforeRestore.
	KindRollback = "rollback"
)

// Where is a place a backup is kept.
type Where string

const (
	OnHost  Where = "on-host"
	OffSite Where = "off-site"
)

// Backup is what the rules need to know about one backup.
type Backup struct {
	ID               string
	Kind             string
	CreatedAt        time.Time
	SizeBytes        int64
	MinecraftVersion string
	LevelName        string
	// Verified is the result of the archive's last check on this machine,
	// nil when it hasn't been checked. A copy off the server counts as
	// checked: OffSite is only set once its size and checksum matched.
	Verified   *bool
	OnHost     bool
	OffSite    bool
	Downloaded bool
	Pinned     bool
	// NeededBy names the unfinished restore or update ("restore",
	// "update") that may still need the archive on this machine to undo
	// its changes. Empty when nothing does.
	NeededBy string
}

// Rules say how many backups one place keeps.
type Rules struct {
	// KeepAll keeps every backup and ignores the numbers below.
	KeepAll bool `json:"keepAll"`
	// Last keeps the newest backups.
	Last int `json:"last"`
	// Daily keeps the newest backup of each of the most recent days that
	// have one; Weekly and Monthly do the same for weeks (Monday to Sunday)
	// and calendar months.
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

// Settings are one server's rules.
type Settings struct {
	OnHost  Rules `json:"onHost"`
	OffSite Rules `json:"offSite"`
	// IncludeManual lets the rules delete backups someone made by hand.
	// Without it those are kept until someone deletes them, and they don't
	// take the place of automatic backups in the rules.
	IncludeManual bool `json:"includeManual"`
	// DeleteOnlyCopies lets the rules delete a backup that is the only copy
	// of something no other backup has, even when it hasn't been
	// downloaded or copied off the server.
	DeleteOnlyCopies bool `json:"deleteOnlyCopies"`
}

// Upper bounds Validate enforces.
const (
	MaxLast    = 1000
	MaxDaily   = 3660
	MaxWeekly  = 520
	MaxMonthly = 240
)

// DefaultSettings keep about a dozen backups on this machine with daily
// backups (the newest 3, one a day for a week, one a week for 4 weeks and one
// a month for 3 months) and more off the server, where space is cheaper.
func DefaultSettings() Settings {
	return Settings{
		OnHost:  Rules{Last: 3, Daily: 7, Weekly: 4, Monthly: 3},
		OffSite: Rules{Last: 3, Daily: 14, Weekly: 8, Monthly: 12},
	}
}

// MaxKept is the most backups r can keep in one place, not counting the
// backups that are always kept, or -1 when it keeps every backup.
func (r Rules) MaxKept() int {
	if r.KeepAll {
		return -1
	}
	return r.Last + r.Daily + r.Weekly + r.Monthly
}

// rules are the place's rules, with numbers out of Validate's range moved
// into it, so the newest backup always counts.
func (s Settings) rules(where Where) Rules {
	r := s.OnHost
	if where == OffSite {
		r = s.OffSite
	}
	r.Last = min(max(r.Last, 1), MaxLast)
	r.Daily = min(max(r.Daily, 0), MaxDaily)
	r.Weekly = min(max(r.Weekly, 0), MaxWeekly)
	r.Monthly = min(max(r.Monthly, 0), MaxMonthly)
	return r
}

// Result is what the rules do in each place.
type Result struct {
	OnHost  Plan `json:"onHost"`
	OffSite Plan `json:"offSite"`
}

// Plan is what the rules do in one place.
type Plan struct {
	Where Where `json:"where"`
	// Decisions has one entry per backup kept in this place, newest first.
	Decisions   []Decision `json:"decisions"`
	Keep        int        `json:"keep"`
	Delete      int        `json:"delete"`
	KeepBytes   int64      `json:"keepBytes"`
	DeleteBytes int64      `json:"deleteBytes"`
	Summary     Text       `json:"summary"`
}

// DeleteIDs lists the backups to delete from this place, newest first.
func (p Plan) DeleteIDs() []string {
	var ids []string
	for _, d := range p.Decisions {
		if !d.Keep {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

// Decision is what happens to one backup in one place.
type Decision struct {
	ID   string `json:"id"`
	Keep bool   `json:"keep"`
	// Reasons explains the decision; the first is the main reason.
	Reasons []Text `json:"reasons"`
	// OnlyCopy is set when no other copy is left once both plans are done:
	// the backup wasn't downloaded and the other place doesn't keep it.
	OnlyCopy bool `json:"onlyCopy"`
}

// Text is a plain English sentence with a stable code and parameters, so
// the UI can show its own translation.
type Text struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
	Text   string            `json:"text"`
}

// Decide applies s to one server's backups. Days, weeks and months are
// counted in loc (UTC when nil). Backups are identified by ID, which must be
// unique.
//
// The two places are decided together: when the rules delete a backup that
// is the only copy of something from this machine because a copy is off the
// server, the off-site plan keeps that copy.
func Decide(backups []Backup, s Settings, loc *time.Location) Result {
	if loc == nil {
		loc = time.UTC
	}
	all := slices.Clone(backups)
	slices.SortStableFunc(all, func(a, b Backup) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	only := onlyCopies(all)
	host := plan(all, s, loc, OnHost, only, func(b Backup) bool { return b.Downloaded || b.OffSite })
	hostKept := host.kept()
	off := plan(all, s, loc, OffSite, only, func(b Backup) bool { return b.Downloaded || (b.OnHost && hostKept[b.ID]) })
	offKept := off.kept()

	byID := make(map[string]Backup, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}
	host.finish(byID, s.rules(OnHost), func(b Backup) bool { return b.Downloaded || (b.OffSite && offKept[b.ID]) })
	off.finish(byID, s.rules(OffSite), func(b Backup) bool { return b.Downloaded || (b.OnHost && hostKept[b.ID]) })
	return Result{OnHost: host, OffSite: off}
}

type check int

const (
	unchecked check = iota
	passed
	failed
)

func (b Backup) check(where Where) check {
	switch {
	case where == OffSite || (b.Verified != nil && *b.Verified):
		return passed
	case b.Verified == nil:
		return unchecked
	default:
		return failed
	}
}

func (b Backup) in(where Where) bool {
	if where == OffSite {
		return b.OffSite
	}
	return b.OnHost
}

func (b Backup) good() bool {
	return b.OffSite || (b.OnHost && b.check(OnHost) == passed)
}

// What makes a backup the only copy of something.
const (
	beforeRestore = "before_restore"
	lastOnVersion = "last_on_version"
	lastOfWorld   = "last_of_world"
)

type onlyCopy struct {
	what  string
	value string // the earlier version or world
	newer string // the version or world of newer backups
}

// onlyCopies finds the backups that hold something no other backup has:
// archives from before a restore, and the newest good backup of each world
// and Minecraft version other than the newest good backup's. all is newest
// first.
func onlyCopies(all []Backup) map[string]onlyCopy {
	m := map[string]onlyCopy{}
	for _, b := range all {
		if b.Kind == KindBeforeRestore || b.Kind == KindRollback {
			m[b.ID] = onlyCopy{what: beforeRestore}
		}
	}
	var seen []Backup
	for _, b := range all {
		if !b.good() || slices.ContainsFunc(seen, func(s Backup) bool { return sameLineage(s, b) }) {
			continue
		}
		seen = append(seen, b)
		if len(seen) == 1 {
			continue
		}
		if _, ok := m[b.ID]; ok {
			continue
		}
		cur := seen[0]
		if !same(b.LevelName, cur.LevelName) {
			m[b.ID] = onlyCopy{what: lastOfWorld, value: b.LevelName, newer: cur.LevelName}
		} else {
			m[b.ID] = onlyCopy{what: lastOnVersion, value: b.MinecraftVersion, newer: cur.MinecraftVersion}
		}
	}
	return m
}

// same treats an unknown (empty) world or version as matching any other.
func same(a, b string) bool { return a == "" || b == "" || a == b }

func sameLineage(a, b Backup) bool {
	return same(a.LevelName, b.LevelName) && same(a.MinecraftVersion, b.MinecraftVersion)
}

type period struct {
	key    string
	first  time.Time // the period's first day, in the time zone
	newest Backup
	kept   Backup
}

// periods returns the n most recent periods that have a backup in ruled
// (newest first), each with the backup that stands for it: the newest one
// that passed its check, or else the newest one. ruled is newest first, so
// once n periods are full every later backup belongs to an older one.
func periods(ruled []Backup, n int, where Where, of func(time.Time) (string, time.Time)) []period {
	var out []period
	for _, b := range ruled {
		key, first := of(b.CreatedAt)
		if i := len(out) - 1; i >= 0 && out[i].key == key {
			if out[i].kept.check(where) != passed && b.check(where) == passed {
				out[i].kept = b
			}
			continue
		}
		if len(out) == n {
			break
		}
		out = append(out, period{key: key, first: first, newest: b, kept: b})
	}
	return out
}

func day(loc *time.Location) func(time.Time) (string, time.Time) {
	return func(t time.Time) (string, time.Time) {
		t = t.In(loc)
		y, m, d := t.Date()
		return t.Format("2006-01-02"), time.Date(y, m, d, 12, 0, 0, 0, loc)
	}
}

func week(loc *time.Location) func(time.Time) (string, time.Time) {
	return func(t time.Time) (string, time.Time) {
		t = t.In(loc)
		iy, iw := t.ISOWeek()
		y, m, d := t.Date()
		back := (int(t.Weekday()) + 6) % 7
		return isoWeek(iy, iw), time.Date(y, m, d-back, 12, 0, 0, 0, loc)
	}
}

func month(loc *time.Location) func(time.Time) (string, time.Time) {
	return func(t time.Time) (string, time.Time) {
		t = t.In(loc)
		y, m, _ := t.Date()
		return t.Format("2006-01"), time.Date(y, m, 1, 12, 0, 0, 0, loc)
	}
}

func plan(all []Backup, s Settings, loc *time.Location, where Where, only map[string]onlyCopy, otherCopy func(Backup) bool) Plan {
	r := s.rules(where)
	var here []Backup
	for _, b := range all {
		if b.in(where) {
			here = append(here, b)
		}
	}
	reasons := make(map[string][]Text, len(here))
	add := func(b Backup, t Text) { reasons[b.ID] = append(reasons[b.ID], t) }
	manual := func(b Backup) bool { return !s.IncludeManual && b.Kind == KindManual }

	for _, b := range here {
		if b.Pinned {
			add(b, pinnedText())
		}
		if where == OnHost && b.NeededBy != "" {
			add(b, neededText(b.NeededBy))
		}
	}

	var ruled []Backup
	for _, b := range here {
		if b.check(where) != failed && !manual(b) {
			ruled = append(ruled, b)
		}
	}
	var days, weeks, months []period
	if r.KeepAll {
		for _, b := range here {
			add(b, keepAllText(where))
		}
	} else {
		for i, b := range ruled[:min(r.Last, len(ruled))] {
			add(b, lastText(r.Last, i+1))
		}
		days = periods(ruled, r.Daily, where, day(loc))
		for _, p := range days {
			add(p.kept, dailyText(p, r.Daily))
		}
		weeks = periods(ruled, r.Weekly, where, week(loc))
		for _, p := range weeks {
			add(p.kept, weeklyText(p, r.Weekly))
		}
		months = periods(ruled, r.Monthly, where, month(loc))
		for _, p := range months {
			add(p.kept, monthlyText(p, r.Monthly))
		}
	}

	for _, b := range here {
		if manual(b) {
			add(b, manualText())
		}
	}
	newest := slices.IndexFunc(here, func(b Backup) bool { return b.check(where) == passed })
	if newest >= 0 {
		add(here[newest], newestText(where))
	}
	for _, b := range here {
		if oc, ok := only[b.ID]; ok && !otherCopy(b) && !s.DeleteOnlyCopies {
			add(b, onlyCopyText(oc, where))
		}
	}
	for i, b := range here {
		if b.check(where) == failed && len(reasons[b.ID]) == 0 && (newest < 0 || newest > i) {
			add(b, failedNewestText())
		}
	}

	p := Plan{Where: where}
	for _, b := range here {
		d := Decision{ID: b.ID, Keep: len(reasons[b.ID]) > 0, Reasons: reasons[b.ID]}
		if !d.Keep {
			d.Reasons = []Text{deleteText(b, where, r, loc, days, weeks, months)}
			if oc, ok := only[b.ID]; ok && !otherCopy(b) {
				d.Reasons = append(d.Reasons, onlyCopyAllowedText(oc, where))
			}
		}
		p.Decisions = append(p.Decisions, d)
	}
	return p
}

// deleteText explains why no rule keeps b: the period it falls in is kept by
// another backup, or it is older than every rule reaches.
func deleteText(b Backup, where Where, r Rules, loc *time.Location, days, weeks, months []period) Text {
	if b.check(where) == failed {
		return failedText()
	}
	for _, c := range []struct {
		code string
		ps   []period
		of   func(time.Time) (string, time.Time)
		n    int
	}{
		{"same_day", days, day(loc), r.Daily},
		{"same_week", weeks, week(loc), r.Weekly},
		{"same_month", months, month(loc), r.Monthly},
	} {
		key, _ := c.of(b.CreatedAt)
		for _, p := range c.ps {
			if p.key == key {
				return sameText(c.code, p, c.n)
			}
		}
	}
	return beyondText(r, where)
}

func (p *Plan) kept() map[string]bool {
	m := make(map[string]bool, len(p.Decisions))
	for _, d := range p.Decisions {
		if d.Keep {
			m[d.ID] = true
		}
	}
	return m
}

// finish marks the decisions that leave no other copy, says so for the
// deletions, and counts the plan.
func (p *Plan) finish(byID map[string]Backup, r Rules, otherCopy func(Backup) bool) {
	extra := map[string]int{}
	for i := range p.Decisions {
		d := &p.Decisions[i]
		b := byID[d.ID]
		d.OnlyCopy = !otherCopy(b)
		if d.Keep {
			p.Keep++
			p.KeepBytes += b.SizeBytes
			if !slices.ContainsFunc(d.Reasons, func(t Text) bool { return isRule(t.Code) }) {
				extra[d.Reasons[0].Code]++
			}
			continue
		}
		p.Delete++
		p.DeleteBytes += b.SizeBytes
		if d.OnlyCopy && !slices.ContainsFunc(d.Reasons, func(t Text) bool { return t.Code == "only_copy_allowed" }) {
			d.Reasons = append(d.Reasons, noCopyLeftText(b, p.Where))
		}
	}
	p.Summary = summaryText(*p, r, extra)
}
