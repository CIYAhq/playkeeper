package retention

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
	_ "time/tzdata"
)

// size is a typical backup, as in the design's "Last copy: 312 MB".
const size = 312_000_000

func passedCheck() *bool { v := true; return &v }
func failedCheck() *bool { v := false; return &v }

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// backup is a checked scheduled backup of "world" on 1.21.8, on this
// machine only, created at when (UTC).
func backup(t *testing.T, id, when string, edit ...func(*Backup)) Backup {
	b := Backup{ID: id, Kind: KindScheduled, CreatedAt: at(t, when), SizeBytes: size, MinecraftVersion: "1.21.8", LevelName: "world", Verified: passedCheck(), OnHost: true}
	for _, e := range edit {
		e(&b)
	}
	return b
}

// daily is one backup a day at 04:00 UTC for n days up to last, with ids
// like "d2026-09-25".
func daily(t *testing.T, last string, n int, edit ...func(*Backup)) []Backup {
	end := at(t, last+" 04:00")
	var out []Backup
	for i := range n {
		d := end.AddDate(0, 0, -i)
		out = append(out, backup(t, "d"+d.Format("2006-01-02"), d.Format("2006-01-02 15:04"), edit...))
	}
	return out
}

// every is n backups made every step up to newest, with ids like
// "s09-25T18".
func every(newest time.Time, step time.Duration, n int) []Backup {
	out := make([]Backup, 0, n)
	for i := range n {
		when := newest.Add(-time.Duration(i) * step)
		out = append(out, Backup{ID: when.Format("s01-02T15"), Kind: KindScheduled, CreatedAt: when, SizeBytes: size,
			MinecraftVersion: "1.21.8", LevelName: "world", Verified: passedCheck(), OnHost: true})
	}
	return out
}

func keptIDs(p Plan) []string {
	var ids []string
	for _, d := range p.Decisions {
		if d.Keep {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func decision(t *testing.T, p Plan, id string) Decision {
	t.Helper()
	for _, d := range p.Decisions {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("no decision for %s in the %s plan", id, p.Where)
	return Decision{}
}

func codes(d Decision) []string {
	var c []string
	for _, r := range d.Reasons {
		c = append(c, r.Code)
	}
	return c
}

func reason(t *testing.T, d Decision, code string) Text {
	t.Helper()
	for _, r := range d.Reasons {
		if r.Code == code {
			return r
		}
	}
	t.Fatalf("%s: no %q reason in %v", d.ID, code, codes(d))
	return Text{}
}

func onlyRules(last, daily, weekly, monthly int) Settings {
	s := DefaultSettings()
	s.OnHost = Rules{Last: last, Daily: daily, Weekly: weekly, Monthly: monthly}
	return s
}

func TestDefaultRulesWithADailyBackup(t *testing.T) {
	res := Decide(daily(t, "2026-09-25", 120), DefaultSettings(), nil)
	want := []string{
		"d2026-09-25", "d2026-09-24", "d2026-09-23", "d2026-09-22", "d2026-09-21", "d2026-09-20", "d2026-09-19",
		"d2026-09-13", "d2026-09-06",
	}
	if got := keptIDs(res.OnHost); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 9 backups (2.8 GB) on this machine: every backup from the last 24 hours, one a day for 7 days and one a week for 4 weeks. Deletes 111 (34.6 GB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	if p := res.OnHost.Summary.Params; p["keep"] != "9" || p["keepBytes"] != fmt.Sprint(9*size) || p["delete"] != "111" || p["deleteBytes"] != fmt.Sprint(111*size) || p["hours"] != "24" {
		t.Fatalf("summary params %v", p)
	}
	if res.OffSite.Decisions != nil || res.OffSite.Summary.Code != "summary_empty" {
		t.Fatalf("nothing is off the server, but the off-site plan is %+v", res.OffSite)
	}

	newest := decision(t, res.OnHost, "d2026-09-25")
	if got := codes(newest); !slices.Equal(got, []string{"hours", "daily", "weekly", "newest"}) {
		t.Fatalf("newest backup reasons %v", got)
	}
	if got := newest.Reasons[0].Text; got != "The newest backup (every backup from the last 24 hours)." {
		t.Fatalf("hours reason %q", got)
	}
	if got := codes(decision(t, res.OnHost, "d2026-09-24")); !slices.Equal(got, []string{"daily"}) {
		t.Fatalf("a backup made exactly 24 hours before the newest is not from the last 24 hours: %v", got)
	}
	if got := reason(t, decision(t, res.OnHost, "d2026-09-13"), "weekly"); got.Text != "The newest backup of the week starting Monday 7 September 2026 (one a week for 4 weeks)." || got.Params["week"] != "2026-W37" || got.Params["weekStart"] != "2026-09-07" {
		t.Fatalf("weekly reason %+v", got)
	}

	for id, want := range map[string]Text{
		"d2026-09-18": {Code: "same_week", Text: "Another backup from the week starting Monday 14 September 2026 is kept (one a week for 4 weeks)."},
		"d2026-09-01": {Code: "same_week", Text: "Another backup from the week starting Monday 31 August 2026 is kept (one a week for 4 weeks)."},
		"d2026-08-30": {Code: "beyond_rules", Text: "Older than what the rules keep on this machine: every backup from the last 24 hours, one a day for 7 days and one a week for 4 weeks."},
		"d2026-06-30": {Code: "beyond_rules", Text: "Older than what the rules keep on this machine: every backup from the last 24 hours, one a day for 7 days and one a week for 4 weeks."},
	} {
		d := decision(t, res.OnHost, id)
		if d.Keep || d.Reasons[0].Code != want.Code || d.Reasons[0].Text != want.Text {
			t.Errorf("%s: %+v, want deleted because %q", id, d, want.Text)
		}
		if got := d.Reasons[len(d.Reasons)-1]; !d.OnlyCopy || got.Code != "no_copy_left" || got.Text != "It hasn't been downloaded or copied off the server, so no copy of it will be left." {
			t.Errorf("%s does not say that no copy is left: %+v", id, d)
		}
	}
	if kept := reason(t, decision(t, res.OnHost, "d2026-09-18"), "same_week").Params["kept"]; kept != "d2026-09-20" {
		t.Errorf("same_week names %s as the kept backup, want d2026-09-20", kept)
	}
}

func TestDefaultRulesWithABackupEverySixHours(t *testing.T) {
	bs := every(at(t, "2026-09-25 18:00"), 6*time.Hour, 60*4)
	res := Decide(bs, DefaultSettings(), nil)
	want := []string{
		"s09-25T18", "s09-25T12", "s09-25T06", "s09-25T00",
		"s09-24T18", "s09-23T18", "s09-22T18", "s09-21T18", "s09-20T18", "s09-19T18",
		"s09-13T18", "s09-06T18",
	}
	if got := keptIDs(res.OnHost); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 12 backups (3.7 GB) on this machine: every backup from the last 24 hours, one a day for 7 days and one a week for 4 weeks. Deletes 228 (71.1 GB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	if r := reason(t, decision(t, res.OnHost, "s09-25T12"), "hours"); r.Params["rank"] != "2" || r.Text != "Made in the 24 hours up to the newest backup (every backup from the last 24 hours)." {
		t.Fatalf("hours reason %+v", r)
	}
	if got := codes(decision(t, res.OnHost, "s09-24T18")); !slices.Equal(got, []string{"daily"}) {
		t.Fatalf("yesterday's last backup, exactly 24 hours before the newest: %v", got)
	}
	if r := reason(t, decision(t, res.OnHost, "s09-24T12"), "same_day"); r.Params["kept"] != "s09-24T18" {
		t.Fatalf("yesterday's other backups: %+v", r)
	}

	// Counting back from the newest backup, not from now, keeps the last
	// day whole when backups stop, for example because nobody played.
	res = Decide(bs[20:], DefaultSettings(), nil)
	if got := keptIDs(res.OnHost)[:4]; !slices.Equal(got, []string{"s09-20T18", "s09-20T12", "s09-20T06", "s09-20T00"}) {
		t.Fatalf("five days after the last backup the rules kept %v", got)
	}
}

func TestTheNewestBackupIsAlwaysKept(t *testing.T) {
	bs := daily(t, "2026-09-25", 3, func(b *Backup) { b.Verified = nil })
	s := DefaultSettings()
	s.OnHost = Rules{Daily: -4}
	res := Decide(bs, s, nil)
	if got := keptIDs(res.OnHost); !slices.Equal(got, []string{"d2026-09-25"}) {
		t.Fatalf("kept %v", got)
	}
	if d := decision(t, res.OnHost, "d2026-09-25"); !slices.Equal(codes(d), []string{"latest"}) || d.Reasons[0].Text != "The newest backup is always kept." {
		t.Fatalf("newest backup %+v", d)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 1 backup (312 MB) on this machine: the newest. Deletes 2 (624 MB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	if got := decision(t, res.OnHost, "d2026-09-24").Reasons[0].Text; got != "Older than what the rules keep on this machine: only the backups that are always kept." {
		t.Fatalf("deleted backup %q", got)
	}

	bs = []Backup{
		backup(t, "new", "2026-09-25 16:00", func(b *Backup) { b.Verified = nil }),
		backup(t, "morning", "2026-09-25 04:00"),
		backup(t, "yesterday", "2026-09-24 04:00"),
	}
	res = Decide(bs, onlyRules(0, 7, 0, 0), nil)
	if got := keptIDs(res.OnHost); !slices.Equal(got, []string{"new", "morning", "yesterday"}) {
		t.Fatalf("a backup just made and not yet checked must stay beside its day's checked one: kept %v", got)
	}
	if got := codes(decision(t, res.OnHost, "new")); !slices.Equal(got, []string{"latest"}) {
		t.Fatalf("newest unchecked backup %v", got)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 3 backups (936 MB) on this machine: one a day for 7 days, plus the newest. Deletes none."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
}

func TestDaysWithoutBackupsDontCount(t *testing.T) {
	before := daily(t, "2026-08-10", 10)
	after := daily(t, "2026-09-25", 2)
	res := Decide(append(after, before...), onlyRules(1, 7, 0, 0), nil)
	want := []string{"d2026-09-25", "d2026-09-24", "d2026-08-10", "d2026-08-09", "d2026-08-08", "d2026-08-07", "d2026-08-06"}
	if got := keptIDs(res.OnHost); !slices.Equal(got, want) {
		t.Fatalf("after a pause of six weeks the rules kept %v, want %v", got, want)
	}
}

func TestManyBackupsADayKeepTheNewestOfEachDay(t *testing.T) {
	var bs []Backup
	start := at(t, "2026-09-23 00:30")
	for h := range 72 {
		when := start.Add(time.Duration(h) * time.Hour)
		bs = append(bs, backup(t, when.Format("h01-02T15"), when.Format("2006-01-02 15:04")))
	}
	res := Decide(bs, onlyRules(3, 7, 0, 0), nil)
	want := []string{"h09-25T23", "h09-25T22", "h09-25T21", "h09-24T23", "h09-23T23"}
	if got := keptIDs(res.OnHost); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	d := decision(t, res.OnHost, "h09-24T10")
	r := reason(t, d, "same_day")
	if r.Params["kept"] != "h09-24T23" || r.Params["date"] != "2026-09-24" || r.Text != "Another backup from Thursday 24 September 2026 is kept (one a day for 7 days)." {
		t.Fatalf("same_day reason %+v", r)
	}
	if got := reason(t, decision(t, res.OnHost, "h09-25T22"), "last"); got.Text != "One of the newest 3 backups." || got.Params["rank"] != "2" {
		t.Fatalf("last reason %+v", got)
	}
}

func TestEqualTimesAndInputOrderGiveTheSameResult(t *testing.T) {
	bs := daily(t, "2026-09-25", 40)
	bs = append(bs, backup(t, "a-same-time", "2026-09-25 04:00"), backup(t, "z-same-time", "2026-09-25 04:00"))
	bs[5].Pinned = true
	want := Decide(bs, DefaultSettings(), nil)
	if got := keptIDs(want.OnHost)[0]; got != "z-same-time" {
		t.Fatalf("with equal times the newest is %s, want the highest id", got)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		shuffled := slices.Clone(bs)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := Decide(shuffled, DefaultSettings(), nil); !reflect.DeepEqual(got, want) {
			t.Fatal("the order of the input changed the result")
		}
	}
}

func TestPinnedBackupsAreNeverDeleted(t *testing.T) {
	bs := daily(t, "2026-09-25", 30)
	bs[29].Pinned = true
	res := Decide(bs, onlyRules(2, 0, 0, 0), nil)
	d := decision(t, res.OnHost, "d2026-08-27")
	if !d.Keep || d.Reasons[0].Code != "pinned" || d.Reasons[0].Text != "Pinned: the rules never delete it." {
		t.Fatalf("pinned backup: %+v", d)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 3 backups (936 MB) on this machine: the newest 2, plus 1 pinned. Deletes 27 (8.4 GB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
}

func TestFailedChecks(t *testing.T) {
	t.Run("the newest checked backup stays when newer ones failed", func(t *testing.T) {
		bs := daily(t, "2026-09-25", 5)
		bs[0].Verified, bs[1].Verified = failedCheck(), failedCheck()
		res := Decide(bs, onlyRules(1, 0, 0, 0), nil)
		if got, want := keptIDs(res.OnHost), []string{"d2026-09-25", "d2026-09-24", "d2026-09-23"}; !slices.Equal(got, want) {
			t.Fatalf("kept %v, want %v", got, want)
		}
		if got := codes(decision(t, res.OnHost, "d2026-09-23")); !slices.Equal(got, []string{"last", "newest"}) {
			t.Fatalf("newest checked backup reasons %v", got)
		}
		if d := decision(t, res.OnHost, "d2026-09-25"); d.Reasons[0].Code != "failed_newest" || d.Reasons[0].Text != "Its check failed, but no newer backup passed its check, so it is kept for now." {
			t.Fatalf("failed backup newer than every checked one: %+v", d)
		}
		if got, want := res.OnHost.Summary.Text, "Keeps 3 backups (936 MB) on this machine: the newest, plus 2 whose checks failed. Deletes 2 (624 MB)."; got != want {
			t.Fatalf("summary\n got %q\nwant %q", got, want)
		}
	})
	t.Run("a failed backup goes once a newer one passed", func(t *testing.T) {
		bs := daily(t, "2026-09-25", 3)
		bs[1].Verified = failedCheck()
		res := Decide(bs, onlyRules(3, 0, 0, 0), nil)
		d := decision(t, res.OnHost, "d2026-09-24")
		if d.Keep || d.Reasons[0].Code != "failed_check" || d.Reasons[0].Text != "Its check failed, so it can't be restored, and a newer backup passed its check." {
			t.Fatalf("failed backup: %+v", d)
		}
		if got, want := keptIDs(res.OnHost), []string{"d2026-09-25", "d2026-09-23"}; !slices.Equal(got, want) {
			t.Fatalf("a failed backup took a place in the newest 3: kept %v", got)
		}
	})
	t.Run("a failed backup is not from the last 24 hours", func(t *testing.T) {
		bs := every(at(t, "2026-09-25 18:00"), 6*time.Hour, 8)
		bs[1].Verified = failedCheck()
		res := Decide(bs, DefaultSettings(), nil)
		if d := decision(t, res.OnHost, "s09-25T12"); d.Keep || d.Reasons[0].Code != "failed_check" {
			t.Fatalf("failed backup inside the last 24 hours: %+v", d)
		}
		if got := keptIDs(res.OnHost); !slices.Equal(got, []string{"s09-25T18", "s09-25T06", "s09-25T00", "s09-24T18"}) {
			t.Fatalf("kept %v", got)
		}
	})
	t.Run("a failed backup that is pinned stays", func(t *testing.T) {
		bs := daily(t, "2026-09-25", 3)
		bs[2].Verified, bs[2].Pinned = failedCheck(), true
		if d := decision(t, Decide(bs, onlyRules(1, 0, 0, 0), nil).OnHost, "d2026-09-23"); !d.Keep || d.Reasons[0].Code != "pinned" {
			t.Fatalf("pinned failed backup: %+v", d)
		}
	})
}

func TestUncheckedBackups(t *testing.T) {
	bs := []Backup{
		backup(t, "new", "2026-09-25 04:00", func(b *Backup) { b.Verified = nil }),
		backup(t, "noon", "2026-09-24 12:00", func(b *Backup) { b.Verified = nil }),
		backup(t, "morning", "2026-09-24 10:00"),
		backup(t, "tuesday", "2026-09-22 12:00", func(b *Backup) { b.Verified = nil }),
		backup(t, "older", "2026-09-21 12:00"),
	}
	res := Decide(bs, onlyRules(1, 3, 0, 0), nil)
	if got, want := keptIDs(res.OnHost), []string{"new", "morning", "tuesday"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if got := codes(decision(t, res.OnHost, "new")); !slices.Equal(got, []string{"last", "daily"}) {
		t.Fatalf("the newest backup, not checked yet: %v", got)
	}
	if got := codes(decision(t, res.OnHost, "morning")); !slices.Equal(got, []string{"daily", "newest"}) {
		t.Fatalf("the newest checked backup: %v", got)
	}
	if r := reason(t, decision(t, res.OnHost, "morning"), "daily"); r.Params["checked"] != "true" || r.Text != "The newest backup of Thursday 24 September 2026 that passed its check (one a day for 3 days)." {
		t.Fatalf("a day's newest backup is not checked, so the checked one stands for the day: %+v", r)
	}
	if r := reason(t, decision(t, res.OnHost, "noon"), "same_day"); r.Params["kept"] != "morning" {
		t.Fatalf("the unchecked backup of a day with a checked one: %+v", r)
	}
	if r := reason(t, decision(t, res.OnHost, "tuesday"), "daily"); r.Params["checked"] != "false" {
		t.Fatalf("a day with only an unchecked backup keeps it: %+v", r)
	}
}

func TestRollbackArchivesARestoreOrUpdateStillNeedsAreKept(t *testing.T) {
	bs := daily(t, "2026-09-25", 10)
	bs[9].Kind, bs[9].NeededBy, bs[9].OffSite = KindBeforeUpdate, "update", true
	res := Decide(bs, onlyRules(1, 0, 0, 0), nil)
	d := decision(t, res.OnHost, "d2026-09-16")
	if !d.Keep || d.Reasons[0].Code != "needed" || d.Reasons[0].Text != "An update that hasn't finished may still need it to undo its changes." || d.Reasons[0].Params["by"] != "update" {
		t.Fatalf("archive an update still needs: %+v", d)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 2 backups (624 MB) on this machine: the newest, plus 1 a restore or update still needs. Deletes 8 (2.5 GB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	if d := decision(t, res.OffSite, "d2026-09-16"); codes(d)[0] == "needed" {
		t.Fatalf("an update only needs the archive on this machine, but the off-site copy says %v", codes(d))
	}
}

func TestTheWorldBeforeARestoreIsKeptUntilItHasAnotherCopy(t *testing.T) {
	for _, kind := range []string{KindBeforeRestore, KindRollback} {
		t.Run(kind, func(t *testing.T) {
			bs := daily(t, "2026-09-25", 10)
			bs[6].Kind = kind
			res := Decide(bs, onlyRules(2, 0, 0, 0), nil)
			d := decision(t, res.OnHost, "d2026-09-19")
			if !d.Keep || d.Reasons[0].Code != "only_copy" || !d.OnlyCopy {
				t.Fatalf("archive from before a restore: %+v", d)
			}
			r := d.Reasons[0]
			if r.Params["what"] != "before_restore" || r.Text != "It holds the world as it was before a restore replaced it, and it hasn't been downloaded or copied off the server. Download it or copy it off the server to let the rules delete it here." {
				t.Fatalf("only_copy reason %+v", r)
			}
			if got, want := res.OnHost.Summary.Text, "Keeps 3 backups (936 MB) on this machine: the newest 2, plus 1 that is the only copy of an earlier world. Deletes 7 (2.2 GB)."; got != want {
				t.Fatalf("summary\n got %q\nwant %q", got, want)
			}

			bs[6].Downloaded = true
			if d := decision(t, Decide(bs, onlyRules(2, 0, 0, 0), nil).OnHost, "d2026-09-19"); d.Keep || d.OnlyCopy || slices.Contains(codes(d), "no_copy_left") {
				t.Fatalf("once downloaded the rules may delete it: %+v", d)
			}

			bs[6].Downloaded = false
			s := onlyRules(2, 0, 0, 0)
			s.DeleteOnlyCopies = true
			d = decision(t, Decide(bs, s, nil).OnHost, "d2026-09-19")
			if d.Keep || !slices.Equal(codes(d), []string{"beyond_rules", "only_copy_allowed"}) {
				t.Fatalf("with DeleteOnlyCopies: %+v", d)
			}
			if got := d.Reasons[1].Text; got != "It holds the world as it was before a restore replaced it, and no other copy of it exists, but your rules allow deleting backups like this." {
				t.Fatalf("only_copy_allowed text %q", got)
			}
		})
	}
}

func TestTheLastBackupOfAnEarlierVersionOrWorldIsKept(t *testing.T) {
	bs := append(daily(t, "2026-09-25", 5), daily(t, "2026-09-20", 5, func(b *Backup) { b.MinecraftVersion = "1.21.7" })...)
	bs[5].Kind = KindBeforeUpdate
	res := Decide(bs, onlyRules(2, 0, 0, 0), nil)
	if got, want := keptIDs(res.OnHost), []string{"d2026-09-25", "d2026-09-24", "d2026-09-20"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	r := decision(t, res.OnHost, "d2026-09-20").Reasons[0]
	if r.Code != "only_copy" || r.Params["what"] != "last_on_version" || r.Params["value"] != "1.21.7" || r.Params["newer"] != "1.21.8" ||
		r.Text != "It is the last backup of your world on Minecraft 1.21.7 (newer backups are on 1.21.8), and it hasn't been downloaded or copied off the server. Download it or copy it off the server to let the rules delete it here." {
		t.Fatalf("last backup on 1.21.7: %+v", r)
	}

	bs = append(daily(t, "2026-09-25", 5), daily(t, "2026-09-20", 5, func(b *Backup) { b.LevelName = "survival" })...)
	r = decision(t, Decide(bs, onlyRules(2, 0, 0, 0), nil).OnHost, "d2026-09-20").Reasons[0]
	if r.Params["what"] != "last_of_world" || r.Text != "It is the last backup of the world \"survival\" (newer backups are of \"world\"), and it hasn't been downloaded or copied off the server. Download it or copy it off the server to let the rules delete it here." {
		t.Fatalf("last backup of another world: %+v", r)
	}

	bs = append(daily(t, "2026-09-25", 5), daily(t, "2026-09-20", 5, func(b *Backup) { b.MinecraftVersion = "" })...)
	if got := keptIDs(Decide(bs, onlyRules(2, 0, 0, 0), nil).OnHost); len(got) != 2 {
		t.Fatalf("a backup with an unknown version is not an earlier version: kept %v", got)
	}

	bs = append(daily(t, "2026-09-25", 5), daily(t, "2026-09-20", 5, func(b *Backup) { b.MinecraftVersion = "1.21.7" })...)
	bs[5].Verified = failedCheck()
	if got, want := keptIDs(Decide(bs, onlyRules(2, 0, 0, 0), nil).OnHost), []string{"d2026-09-25", "d2026-09-24", "d2026-09-19"}; !slices.Equal(got, want) {
		t.Fatalf("the last good backup on 1.21.7 is the one to keep: kept %v, want %v", got, want)
	}
}

func TestBothPlacesTogetherNeverDeleteTheOnlyCopy(t *testing.T) {
	bs := daily(t, "2026-09-25", 10, func(b *Backup) { b.OffSite = true })
	bs[6].Kind = KindBeforeRestore
	s := DefaultSettings()
	s.OnHost = Rules{Last: 2}
	s.OffSite = Rules{Last: 3}
	res := Decide(bs, s, nil)

	host := decision(t, res.OnHost, "d2026-09-19")
	if host.Keep || host.OnlyCopy || slices.Contains(codes(host), "no_copy_left") {
		t.Fatalf("with a copy off the server this machine can let it go: %+v", host)
	}
	off := decision(t, res.OffSite, "d2026-09-19")
	if !off.Keep || !off.OnlyCopy || off.Reasons[0].Code != "only_copy" ||
		off.Reasons[0].Text != "It holds the world as it was before a restore replaced it, and this is its only copy: it isn't kept on this machine and hasn't been downloaded. Download it to let the rules delete it off the server." {
		t.Fatalf("the copy off the server must stay once this machine deletes its own: %+v", off)
	}

	gone := decision(t, res.OnHost, "d2026-09-23")
	if gone.Keep || gone.OnlyCopy {
		t.Fatalf("d2026-09-23 is kept off the server: %+v", gone)
	}
	both := decision(t, res.OnHost, "d2026-09-22")
	if r := both.Reasons[len(both.Reasons)-1]; !both.OnlyCopy || r.Code != "no_copy_left" || r.Params["otherDeleted"] != "true" ||
		r.Text != "Its copy off the server is deleted too, and it hasn't been downloaded, so no copy of it will be left." {
		t.Fatalf("deleted in both places: %+v", both)
	}
	if r := decision(t, res.OffSite, "d2026-09-22").Reasons; r[len(r)-1].Text != "Its copy on this machine is deleted too, and it hasn't been downloaded, so no copy of it will be left." {
		t.Fatalf("deleted in both places, off-site reasons: %+v", r)
	}
	if got, want := res.OffSite.Summary.Text, "Keeps 4 backups (1.2 GB) off the server: the newest 3, plus 1 that is the only copy of an earlier world. Deletes 6 (1.9 GB)."; got != want {
		t.Fatalf("off-site summary\n got %q\nwant %q", got, want)
	}

	s.DeleteOnlyCopies = true
	res = Decide(bs, s, nil)
	if d := decision(t, res.OffSite, "d2026-09-19"); d.Keep || !slices.Equal(codes(d), []string{"beyond_rules", "only_copy_allowed"}) {
		t.Fatalf("DeleteOnlyCopies lets both places delete it: %+v", d)
	}
	if d := decision(t, res.OnHost, "d2026-09-19"); d.Keep || !d.OnlyCopy || codes(d)[len(d.Reasons)-1] != "no_copy_left" {
		t.Fatalf("this machine's decision must say no copy is left: %+v", d)
	}
}

func TestCopiesOffTheServerFollowTheirOwnRules(t *testing.T) {
	bs := daily(t, "2026-09-25", 30, func(b *Backup) { b.OffSite = true })
	for i := 20; i < 30; i++ {
		bs[i].OnHost = false
	}
	bs[25].Verified = failedCheck()
	res := Decide(bs, DefaultSettings(), nil)
	if got := len(res.OnHost.Decisions); got != 20 {
		t.Fatalf("the on-host plan has %d decisions for 20 backups on this machine", got)
	}
	want := []string{
		"d2026-09-25", "d2026-09-24", "d2026-09-23", "d2026-09-22", "d2026-09-21", "d2026-09-20", "d2026-09-19",
		"d2026-09-18", "d2026-09-17", "d2026-09-16", "d2026-09-15", "d2026-09-14", "d2026-09-13", "d2026-09-12",
		"d2026-09-06", "d2026-08-31", "d2026-08-30",
	}
	if got := keptIDs(res.OffSite); !slices.Equal(got, want) {
		t.Fatalf("off the server kept %v, want %v", got, want)
	}
	if got, want := res.OffSite.Summary.Text, "Keeps 17 backups (5.3 GB) off the server: one a day for 14 days, one a week for 8 weeks and one a month for a year. Deletes 13 (4.1 GB)."; got != want {
		t.Fatalf("off-site summary\n got %q\nwant %q", got, want)
	}
	if d := decision(t, res.OffSite, "d2026-08-31"); !d.Keep {
		t.Fatalf("a copy off the server counts as checked even when the archive on this machine failed a later check: %+v", d)
	}
	if d := decision(t, res.OnHost, "d2026-09-18"); d.Keep || d.OnlyCopy {
		t.Fatalf("deleted here but kept off the server: %+v", d)
	}
	if d := decision(t, res.OffSite, "d2026-09-01"); d.Reasons[len(d.Reasons)-1].Text != "It is no longer on this machine and hasn't been downloaded, so no copy of it will be left." {
		t.Fatalf("only off the server: %+v", d)
	}
}

func TestBackupsMadeByHand(t *testing.T) {
	bs := []Backup{
		backup(t, "manual-today", "2026-09-25 12:00", func(b *Backup) { b.Kind = KindManual }),
		backup(t, "sched-today", "2026-09-25 04:00"),
		backup(t, "sched-yesterday", "2026-09-24 04:00"),
		backup(t, "manual-old", "2026-06-01 12:00", func(b *Backup) { b.Kind = KindManual }),
		backup(t, "sched-old", "2026-06-01 04:00"),
	}
	res := Decide(bs, onlyRules(1, 2, 0, 0), nil)
	if got, want := keptIDs(res.OnHost), []string{"manual-today", "sched-today", "sched-yesterday", "manual-old"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if d := decision(t, res.OnHost, "manual-old"); d.Reasons[0].Text != "Made by hand: the rules only delete backups made automatically." {
		t.Fatalf("manual backup: %+v", d)
	}
	if got := codes(decision(t, res.OnHost, "sched-today")); !slices.Equal(got, []string{"last", "daily"}) {
		t.Fatalf("a newer backup made by hand does not take the place of an automatic one: %v", got)
	}
	if got := codes(decision(t, res.OnHost, "manual-today")); !slices.Equal(got, []string{"manual", "newest"}) {
		t.Fatalf("the newest backup, made by hand: %v", got)
	}
	if got, want := res.OnHost.Summary.Text, "Keeps 4 backups (1.2 GB) on this machine: the newest and one a day for 2 days, plus 2 made by hand. Deletes 1 (312 MB)."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}

	s := onlyRules(1, 2, 0, 0)
	s.IncludeManual = true
	res = Decide(bs, s, nil)
	if got, want := keptIDs(res.OnHost), []string{"manual-today", "sched-yesterday"}; !slices.Equal(got, want) {
		t.Fatalf("with IncludeManual kept %v, want %v", got, want)
	}
	if r := reason(t, decision(t, res.OnHost, "sched-today"), "same_day"); r.Params["kept"] != "manual-today" {
		t.Fatalf("same_day %+v", r)
	}
}

func TestTheUsersTimeZoneDecidesTheDay(t *testing.T) {
	bs := []Backup{
		backup(t, "c", "2026-09-25 03:00"),
		backup(t, "b", "2026-09-24 23:30"),
		backup(t, "a", "2026-09-24 22:30"),
	}
	for _, c := range []struct {
		zone string
		want []string
		date string
	}{
		{"UTC", []string{"c", "b"}, "2026-09-24"},
		{"Europe/Berlin", []string{"c"}, "2026-09-25"},
		{"America/Los_Angeles", []string{"c"}, "2026-09-24"},
	} {
		res := Decide(bs, onlyRules(1, 2, 0, 0), zone(t, c.zone))
		if got := keptIDs(res.OnHost); !slices.Equal(got, c.want) {
			t.Errorf("%s: kept %v, want %v", c.zone, got, c.want)
		}
		if got := reason(t, decision(t, res.OnHost, "a"), "same_day").Params["date"]; got != c.date {
			t.Errorf("%s: backup a is on %s, want %s", c.zone, got, c.date)
		}
	}
}

func TestDaylightSavingTimeChanges(t *testing.T) {
	ny := zone(t, "America/New_York")
	bs := []Backup{
		backup(t, "mon-0030-edt", "2026-03-09 04:30"),
		backup(t, "sun-0330-edt", "2026-03-08 07:30"),
		backup(t, "sun-0130-est", "2026-03-08 06:30"),
		backup(t, "sat-2330-est", "2026-03-08 04:30"),
	}
	res := Decide(bs, onlyRules(1, 3, 0, 0), ny)
	if got, want := keptIDs(res.OnHost), []string{"mon-0030-edt", "sun-0330-edt", "sat-2330-est"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if got := reason(t, decision(t, res.OnHost, "sat-2330-est"), "daily").Params["date"]; got != "2026-03-07" {
		t.Fatalf("23:30 EST is on %s, want 2026-03-07", got)
	}

	berlin := zone(t, "Europe/Berlin")
	bs = []Backup{
		backup(t, "after", "2026-10-25 22:30"),
		backup(t, "before", "2026-10-24 22:30"),
	}
	res = Decide(bs, onlyRules(1, 2, 0, 0), berlin)
	if got := keptIDs(res.OnHost); !slices.Equal(got, []string{"after"}) {
		t.Fatalf("both backups are on 25 October in Berlin, but the rules kept %v", got)
	}
	if got := reason(t, decision(t, res.OnHost, "after"), "daily").Params["date"]; got != "2026-10-25" {
		t.Fatalf("22:30 UTC on 25 October is 23:30 on the 25th in Berlin (winter time), got %s", got)
	}
	if got := reason(t, decision(t, res.OnHost, "before"), "same_day").Params["date"]; got != "2026-10-25" {
		t.Fatalf("22:30 UTC on 24 October is 00:30 on the 25th in Berlin (summer time), got %s", got)
	}

	// The last 24 hours are real hours: on the night the clocks go back,
	// 23:30 the evening before is 25 hours before 23:30.
	s := DefaultSettings()
	s.OnHost = Rules{Hours: 24}
	bs = []Backup{
		backup(t, "sun-2330", "2026-10-25 22:30"),
		backup(t, "sun-0030", "2026-10-24 22:30"),
		backup(t, "sat-2330", "2026-10-24 21:30"),
	}
	if got := keptIDs(Decide(bs, s, berlin).OnHost); !slices.Equal(got, []string{"sun-2330"}) {
		t.Fatalf("kept %v", got)
	}
}

func TestWeeksStartOnMondayInTheUsersTimeZone(t *testing.T) {
	bs := []Backup{
		backup(t, "mon", "2026-09-28 10:00"),
		backup(t, "sun-late", "2026-09-27 23:30"),
		backup(t, "sun-noon", "2026-09-27 12:00"),
	}
	for _, c := range []struct {
		zone string
		want []string
	}{
		{"UTC", []string{"mon", "sun-late"}},
		{"Europe/Berlin", []string{"mon", "sun-noon"}},
	} {
		if got := keptIDs(Decide(bs, onlyRules(1, 0, 3, 0), zone(t, c.zone)).OnHost); !slices.Equal(got, c.want) {
			t.Errorf("%s: kept %v, want %v", c.zone, got, c.want)
		}
	}

	bs = []Backup{
		backup(t, "jan-2", "2027-01-02 10:00"),
		backup(t, "dec-31", "2026-12-31 10:00"),
		backup(t, "dec-27", "2026-12-27 10:00"),
	}
	res := Decide(bs, onlyRules(1, 0, 2, 2), nil)
	if got := keptIDs(res.OnHost); !slices.Equal(got, []string{"jan-2", "dec-31", "dec-27"}) {
		t.Fatalf("kept %v", got)
	}
	jan := decision(t, res.OnHost, "jan-2")
	if r := reason(t, jan, "weekly"); r.Params["week"] != "2026-W53" || r.Params["weekStart"] != "2026-12-28" {
		t.Fatalf("2 January 2027 is in the 53rd week of 2026: %+v", r)
	}
	dec31 := decision(t, res.OnHost, "dec-31")
	if got := codes(dec31); !slices.Equal(got, []string{"monthly"}) {
		t.Fatalf("31 December shares its week with 2 January but not its month: %v", got)
	}
	if r := reason(t, decision(t, res.OnHost, "dec-27"), "weekly"); r.Params["week"] != "2026-W52" {
		t.Fatalf("27 December 2026 is a Sunday in week 52: %+v", r)
	}
}

func TestKeepAll(t *testing.T) {
	bs := daily(t, "2026-09-25", 5)
	bs[3].Verified = failedCheck()
	s := DefaultSettings()
	s.OnHost.KeepAll = true
	res := Decide(bs, s, nil)
	if res.OnHost.Delete != 0 || res.OnHost.Summary.Text != "Keeps all 5 backups (1.6 GB) on this machine." {
		t.Fatalf("KeepAll: %+v", res.OnHost.Summary)
	}
	if r := decision(t, res.OnHost, "d2026-09-22").Reasons[0]; r.Text != "The rules keep every backup on this machine." {
		t.Fatalf("KeepAll reason %+v", r)
	}
}

func TestBackupsWithoutACopyAreIgnoredAndEmptyPlansSaySo(t *testing.T) {
	res := Decide([]Backup{backup(t, "gone", "2026-09-25 04:00", func(b *Backup) { b.OnHost = false })}, DefaultSettings(), nil)
	if len(res.OnHost.Decisions) != 0 || len(res.OffSite.Decisions) != 0 {
		t.Fatalf("a backup with no copy anywhere got decisions: %+v", res)
	}
	if res.OnHost.Summary.Text != "No backups are kept on this machine." || res.OffSite.Summary.Text != "No backups are copied off the server." {
		t.Fatalf("empty summaries %q, %q", res.OnHost.Summary.Text, res.OffSite.Summary.Text)
	}
}

func TestEstimate(t *testing.T) {
	e := DefaultSettings().Estimate(OnHost, Pace{Every: 6 * time.Hour, Bytes: size})
	var rows []string
	for _, r := range e.Rows {
		rows = append(rows, r.Text.Text)
	}
	if want := []string{
		"Every backup from the last 24 hours: up to 4.",
		"One a day for 7 days: 7.",
		"One a week for 4 weeks: 4.",
		"Backups you make by hand: kept until you delete them.",
	}; !slices.Equal(rows, want) {
		t.Fatalf("rows\n got %q\nwant %q", rows, want)
	}
	if r := e.Rows[0]; r.Rule != "hours" || r.N != 24 || r.Count != 4 || !r.UpTo || r.Text.Code != "estimate_hours" || r.Text.Params["count"] != "4" {
		t.Fatalf("hours row %+v", r)
	}
	// Up to 4 from today, 6 more days, and 3 more weeks when the newest
	// backup is on a Sunday evening.
	if e.Count != 13 || e.Bytes != 13*size {
		t.Fatalf("count %d, bytes %d", e.Count, e.Bytes)
	}
	if got, want := e.Summary.Text, "About 13 backups, roughly 4.1 GB, on this machine. Some backups count for more than one rule."; got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	if p := e.Summary.Params; p["count"] != "13" || p["bytes"] != fmt.Sprint(13*size) || p["rulesSum"] != "15" {
		t.Fatalf("summary params %v", p)
	}

	for _, c := range []struct {
		name  string
		s     Settings
		where Where
		every time.Duration
		count int
	}{
		{"a daily backup", DefaultSettings(), OnHost, 24 * time.Hour, 10},
		{"a weekly backup", DefaultSettings(), OnHost, 7 * 24 * time.Hour, 7},
		{"a backup every hour", DefaultSettings(), OnHost, time.Hour, 24 + 6 + 3},
		{"the newest 5 cover 3 days", onlyRules(5, 3, 0, 0), OnHost, 12 * time.Hour, 5},
	} {
		if got := c.s.Estimate(c.where, Pace{Every: c.every, Bytes: size}).Count; got != c.count {
			t.Errorf("%s: count %d, want %d", c.name, got, c.count)
		}
	}

	off := DefaultSettings().Estimate(OffSite, Pace{Every: 24 * time.Hour, Bytes: size, Location: zone(t, "Europe/Berlin")})
	if len(off.Rows) != 4 || off.Rows[2].Text.Text != "One a month for a year: 12." || off.Count > 14+8+12 || off.Count < 14+8 {
		t.Fatalf("off-site estimate %+v", off)
	}

	s := DefaultSettings()
	s.IncludeManual = true
	e = s.Estimate(OnHost, Pace{})
	if e.Count != 0 || e.Summary.Code != "estimate_off" || e.Summary.Text != "Automatic backups are off, so the rules have nothing to keep yet." {
		t.Fatalf("without automatic backups %+v", e.Summary)
	}
	if got := e.Rows[0]; got.Count != 0 || got.UpTo || got.Text.Text != "Every backup from the last 24 hours." {
		t.Fatalf("rows without automatic backups %+v", got)
	}
	if got := e.Rows[len(e.Rows)-1].Text.Text; got != "Backups you make by hand: the rules above count them like the others." {
		t.Fatalf("manual row with IncludeManual %q", got)
	}

	s.OffSite.KeepAll = true
	if e := s.Estimate(OffSite, Pace{Every: time.Hour, Bytes: size}); e.Count != -1 || e.Summary.Text != "Keeps every backup off the server, so they take more space over time." {
		t.Fatalf("KeepAll estimate %+v", e)
	}
}

// TestEstimateMatchesTheRules builds the backups a steady pace makes and
// checks that Decide keeps exactly what the estimate counted, at every hour
// of a week.
func TestEstimateMatchesTheRules(t *testing.T) {
	loc := zone(t, "Europe/Berlin")
	for _, c := range []struct {
		name  string
		rules Rules
		every time.Duration
		step  int
	}{
		{"defaults every hour", DefaultSettings().OnHost, time.Hour, 7},
		{"defaults every 6 hours", DefaultSettings().OnHost, 6 * time.Hour, 1},
		{"defaults every 5 hours", DefaultSettings().OnHost, 5 * time.Hour, 1},
		{"defaults once a day", DefaultSettings().OnHost, 24 * time.Hour, 1},
		{"defaults every 3 days", DefaultSettings().OnHost, 72 * time.Hour, 1},
		{"off-site defaults every 12 hours", DefaultSettings().OffSite, 12 * time.Hour, 5},
		{"off-site defaults once a week", DefaultSettings().OffSite, 7 * 24 * time.Hour, 5},
		{"newest 5, 3 days, 2 months", Rules{Last: 5, Daily: 3, Monthly: 2}, 8 * time.Hour, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := DefaultSettings()
			s.OnHost = c.rules
			r := s.rules(OnHost)
			span := max(time.Duration(r.Hours)*time.Hour, time.Duration(r.Last)*c.every,
				time.Duration(r.Daily)*max(c.every, 24*time.Hour), time.Duration(r.Weekly)*max(c.every, 7*24*time.Hour),
				time.Duration(r.Monthly)*max(c.every, 31*24*time.Hour)) + 62*24*time.Hour
			hours := int((time.Duration(r.Hours)*time.Hour + c.every - 1) / c.every)
			most := 0
			start := time.Date(2026, time.January, 5, 0, 0, 0, 0, loc)
			for h := 0; h < 7*24; h += c.step {
				newest := start.Add(time.Duration(h) * time.Hour)
				bs := every(newest, c.every, int(span/c.every)+1)
				for i := range bs {
					bs[i].ID = fmt.Sprintf("b%06d", len(bs)-i)
				}
				kept := len(keptIDs(Decide(bs, s, loc).OnHost))
				if want := r.steady(newest, c.every, hours, loc); kept != want {
					t.Fatalf("newest at %s: Decide kept %d, the estimate counted %d", newest.Format("Mon 15:04"), kept, want)
				}
				most = max(most, kept)
			}
			if got := s.Estimate(OnHost, Pace{Every: c.every, Location: loc}).Count; c.step == 1 && got != most {
				t.Fatalf("estimate %d, most kept %d", got, most)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("default settings: %v", err)
	}
	if err := (Settings{}).Validate(); err != nil {
		t.Fatalf("no rules at all is allowed, the newest backup is always kept: %v", err)
	}
	for _, c := range []struct {
		edit  func(*Settings)
		field string
		msg   string
	}{
		{func(s *Settings) { s.OnHost.Hours = MaxHours + 1 }, "onHost.hours", "The number of hours to keep every backup for on this machine must be between 0 and 720, not 721."},
		{func(s *Settings) { s.OnHost.Last = -1 }, "onHost.last", "The number of newest backups to keep on this machine must be between 0 and 1000, not -1."},
		{func(s *Settings) { s.OffSite.Daily = -1 }, "offSite.daily", "The number of days to keep one backup a day for off the server must be between 0 and 3660, not -1."},
		{func(s *Settings) { s.OnHost.Weekly = MaxWeekly + 1 }, "onHost.weekly", "The number of weeks to keep one backup a week for on this machine must be between 0 and 520, not 521."},
		{func(s *Settings) { s.OffSite.KeepAll, s.OffSite.Monthly = true, 1000 }, "offSite.monthly", "The number of months to keep one backup a month for off the server must be between 0 and 240, not 1000."},
	} {
		s := DefaultSettings()
		c.edit(&s)
		var e *Error
		if err := s.Validate(); !errors.As(err, &e) || e.Field != c.field || e.Msg != c.msg || e.Kind != "out_of_range" || e.Hint == "" {
			t.Errorf("%s: %#v", c.field, err)
		}
	}
}

func TestDescribe(t *testing.T) {
	got := DefaultSettings().Describe()
	want := []string{
		"Keeps every backup from the last 24 hours, one a day for 7 days and one a week for 4 weeks on this machine.",
		"Keeps one a day for 14 days, one a week for 8 weeks and one a month for a year off the server.",
		"The last hours are counted back from the newest backup, and days, weeks and months without a backup don't count, so a pause in backups never makes the rules delete older ones.",
		"Always kept: pinned backups, the newest backup, the newest backup that passed its check, backups a restore or update may still need, backups made by hand and backups that are the only copy of an earlier world (from before a restore, or the last one on an earlier Minecraft version or of another world) until they are downloaded or copied off the server.",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d texts", len(got))
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Errorf("text %d\n got %q\nwant %q", i, got[i].Text, want[i])
		}
	}
	if got[0].Params["hours"] != "24" || got[0].Params["daily"] != "7" || got[1].Params["where"] != "off-site" {
		t.Errorf("params %v %v", got[0].Params, got[1].Params)
	}

	s := Settings{OnHost: Rules{KeepAll: true, Last: 1}, OffSite: Rules{Hours: 72, Last: 1, Weekly: 52, Monthly: 24}, IncludeManual: true, DeleteOnlyCopies: true}
	got = s.Describe()
	for i, want := range []string{
		"Keeps every backup on this machine.",
		"Keeps every backup from the last 3 days, the newest, one a week for a year and one a month for 2 years off the server.",
		"",
		"Always kept: pinned backups, the newest backup, the newest backup that passed its check and backups a restore or update may still need.",
	} {
		if want != "" && got[i].Text != want {
			t.Errorf("text %d\n got %q\nwant %q", i, got[i].Text, want)
		}
	}
	if got := (Rules{}).Describe(OnHost).Text; got != "Keeps only the backups that are always kept on this machine." {
		t.Errorf("no rules: %q", got)
	}
	if dailyPhrase(1) != "one a day for 1 day" || hoursPhrase(1) != "every backup from the last hour" || hoursPhrase(36) != "every backup from the last 36 hours" {
		t.Errorf("phrases %q %q %q", dailyPhrase(1), hoursPhrase(1), hoursPhrase(36))
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0:                 "0 B",
		999:               "999 B",
		1_499:             "1 kB",
		999_600:           "1 MB",
		312_000_000:       "312 MB",
		999_600_000:       "1.0 GB",
		4_600_000_000:     "4.6 GB",
		999_960_000_000:   "1.0 TB",
		2_345_000_000_000: "2.3 TB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
