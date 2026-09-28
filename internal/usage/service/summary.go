package service

import (
	"context"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/usage"
)

// Summary is what GET /v1/summary answers: counts only, never an install ID.
// Test installs (usage.System.Test) are left out of everything but Test.
type Summary struct {
	GeneratedAt time.Time `json:"generatedAt"`
	// Installs counts the installer's reports in the last day, 7 days, 30
	// days, and everything the service keeps ("all").
	Installs map[string]*Installs `json:"installs"`
	// Active counts the installs that sent a heartbeat in the last day, 7
	// days and 30 days: machines where Playkeeper runs.
	Active map[string]*Active `json:"active"`
	// Daily is the last 30 days, oldest first (UTC).
	Daily []Day `json:"daily"`
	// Test counts the test installs kept out of everything above.
	Test TestInstalls `json:"test"`
}

// Outcomes are installs that started, and how the ones that ended did.
type Outcomes struct {
	Started   int `json:"started"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Refused   int `json:"refused"`
}

// Installs are the installer's reports in one window.
type Installs struct {
	Outcomes
	// Unfinished started more than an hour ago and never said how they
	// ended: stopped, cut off, or unable to reach the service.
	Unfinished int `json:"unfinished"`
	// BySource splits them by how Playkeeper got onto the machine; the
	// playkeeper.io command is "on our domain".
	BySource map[string]Outcomes `json:"bySource"`
	// ByChannel splits those from playkeeper.io/install/<code> by code.
	ByChannel map[string]Outcomes `json:"byChannel"`
	// FailedSteps counts the steps failed installs stopped at, and
	// RefusedChecks the checks that turned installs away.
	FailedSteps   map[string]int `json:"failedSteps"`
	RefusedChecks map[string]int `json:"refusedChecks"`
}

// Active are the installs that sent a heartbeat in one window, each as its
// last heartbeat described it.
type Active struct {
	Installs int `json:"installs"`
	// OnOurDomain were installed with playkeeper.io's command,
	// OffOurDomain any other way; UnknownSource were installed before
	// Playkeeper recorded how.
	OnOurDomain   int            `json:"onOurDomain"`
	OffOurDomain  int            `json:"offOurDomain"`
	UnknownSource int            `json:"unknownSource"`
	BySource      map[string]int `json:"bySource"`
	ByChannel     map[string]int `json:"byChannel"`
	ByVersion     map[string]int `json:"byVersion"`
	// ByOS is by system and release, like "ubuntu 24.04".
	ByOS      map[string]int `json:"byOS"`
	ByArch    map[string]int `json:"byArch"`
	ByAddress map[string]int `json:"byAddress"`
	ByKind    map[string]int `json:"byKind"`
	// Servers and Running are the Minecraft servers on them, and those
	// running; ServersPerInstall counts installs by how many they have.
	Servers           int            `json:"servers"`
	Running           int            `json:"running"`
	ServersPerInstall map[string]int `json:"serversPerInstall"`
}

// Day is one UTC day.
type Day struct {
	Day string `json:"day"`
	// Active sent a heartbeat that day; Started and Succeeded are installs
	// that started or succeeded that day.
	Active    int `json:"active"`
	Started   int `json:"started"`
	Succeeded int `json:"succeeded"`
}

// TestInstalls are the project's own test installs, which a working setup
// keeps at zero: its CI sends nothing.
type TestInstalls struct {
	Started30d int `json:"started30d"`
	Active7d   int `json:"active7d"`
}

var windows = []struct {
	name string
	span time.Duration
}{{"1d", day}, {"7d", 7 * day}, {"30d", 30 * day}}

// dailyDays is how many days Daily covers.
const dailyDays = 30

// perInstallBucket names the ServersPerInstall bucket of n servers.
func perInstallBucket(n int) string {
	switch {
	case n <= 2:
		return string(rune('0' + n))
	case n <= 5:
		return "3-5"
	case n <= 10:
		return "6-10"
	}
	return "11+"
}

func newInstalls() *Installs {
	return &Installs{BySource: map[string]Outcomes{}, ByChannel: map[string]Outcomes{}, FailedSteps: map[string]int{}, RefusedChecks: map[string]int{}}
}

func newActive() *Active {
	return &Active{BySource: map[string]int{}, ByChannel: map[string]int{}, ByVersion: map[string]int{}, ByOS: map[string]int{},
		ByArch: map[string]int{}, ByAddress: map[string]int{}, ByKind: map[string]int{}, ServersPerInstall: map[string]int{}}
}

// Summary counts what the service keeps.
func (s *Service) Summary(ctx context.Context) (*Summary, error) {
	now := s.now().UTC()
	sum := &Summary{GeneratedAt: now.Truncate(time.Second), Installs: map[string]*Installs{}, Active: map[string]*Active{}}
	// Times are kept to the hour, so each window starts on one: an install
	// from within the last day counts in it.
	for _, w := range windows {
		in, err := s.installs(ctx, now, hour(now)-int64(w.span/time.Second))
		if err != nil {
			return nil, err
		}
		sum.Installs[w.name] = in
		act, err := s.active(ctx, hour(now)-int64(w.span/time.Second))
		if err != nil {
			return nil, err
		}
		sum.Active[w.name] = act
	}
	all, err := s.installs(ctx, now, 0)
	if err != nil {
		return nil, err
	}
	sum.Installs["all"] = all
	if sum.Daily, err = s.daily(ctx, now); err != nil {
		return nil, err
	}
	since30 := hour(now) - int64(30*day/time.Second)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM installs WHERE test = 1 AND (started_at >= ? OR outcome_at >= ?)`, since30, since30).Scan(&sum.Test.Started30d); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM installs WHERE test = 1 AND last_seen >= ?`, hour(now)-int64(7*day/time.Second)).Scan(&sum.Test.Active7d); err != nil {
		return nil, err
	}
	return sum, nil
}

// installs counts the installer's reports from since on.
func (s *Service) installs(ctx context.Context, now time.Time, since int64) (*Installs, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, channel, started_at, outcome, outcome_at, step FROM installs
		WHERE test = 0 AND (started_at >= ? OR outcome_at >= ?) AND (started_at != 0 OR outcome != '')`, since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	in := newInstalls()
	stale := hour(now) - 3600
	for rows.Next() {
		var source, channel, outcome, step string
		var started, outcomeAt int64
		if err := rows.Scan(&source, &channel, &started, &outcome, &outcomeAt, &step); err != nil {
			return nil, err
		}
		var o Outcomes
		ended := outcomeAt >= since && outcome != ""
		// An install that ended started, even when its first report was lost.
		if (started != 0 && started >= since) || (started == 0 && ended && (outcome == usage.EventSucceeded || outcome == usage.EventFailed)) {
			o.Started = 1
		}
		if ended {
			switch outcome {
			case usage.EventSucceeded:
				o.Succeeded = 1
			case usage.EventFailed:
				o.Failed = 1
				in.FailedSteps[step]++
			case usage.EventRefused:
				o.Refused = 1
				for _, c := range strings.Split(step, "+") {
					if c != "" {
						in.RefusedChecks[c]++
					}
				}
			}
		}
		if started >= since && started != 0 && outcome == "" && started < stale {
			in.Unfinished++
		}
		in.Outcomes = in.Outcomes.add(o)
		in.BySource[source] = in.BySource[source].add(o)
		if channel != "" {
			in.ByChannel[channel] = in.ByChannel[channel].add(o)
		}
	}
	return in, rows.Err()
}

func (o Outcomes) add(p Outcomes) Outcomes {
	return Outcomes{Started: o.Started + p.Started, Succeeded: o.Succeeded + p.Succeeded, Failed: o.Failed + p.Failed, Refused: o.Refused + p.Refused}
}

// active counts the installs whose last heartbeat came at since or later.
func (s *Service) active(ctx context.Context, since int64) (*Active, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, channel, kind, version, os, os_version, arch, address, servers, running FROM installs
		WHERE test = 0 AND last_seen != 0 AND last_seen >= ?`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	a := newActive()
	for rows.Next() {
		var source, channel, kind, version, os, osVersion, arch, address string
		var servers, running int
		if err := rows.Scan(&source, &channel, &kind, &version, &os, &osVersion, &arch, &address, &servers, &running); err != nil {
			return nil, err
		}
		a.Installs++
		switch source {
		case usage.SourceSite:
			a.OnOurDomain++
		case "":
			a.UnknownSource++
		default:
			a.OffOurDomain++
		}
		if source == "" {
			source = "unknown"
		}
		a.BySource[source]++
		if channel != "" {
			a.ByChannel[channel]++
		}
		a.ByVersion[version]++
		a.ByOS[strings.TrimSpace(os+" "+osVersion)]++
		a.ByArch[arch]++
		a.ByAddress[address]++
		a.ByKind[kind]++
		a.Servers += servers
		a.Running += running
		a.ServersPerInstall[perInstallBucket(servers)]++
	}
	return a, rows.Err()
}

// daily counts each of the last dailyDays days.
func (s *Service) daily(ctx context.Context, now time.Time) ([]Day, error) {
	first := dayOf(now) - dailyDays + 1
	days := make([]Day, dailyDays)
	for i := range days {
		days[i].Day = time.Unix((first+int64(i))*86400, 0).UTC().Format("2006-01-02")
	}
	count := func(query string, set func(d *Day, n int), args ...any) error {
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d int64
			var n int
			if err := rows.Scan(&d, &n); err != nil {
				return err
			}
			if i := d - first; i >= 0 && i < dailyDays {
				set(&days[i], n)
			}
		}
		return rows.Err()
	}
	if err := count(`SELECT a.day, COUNT(*) FROM active_days a JOIN installs i ON i.id = a.id WHERE i.test = 0 AND a.day >= ? GROUP BY a.day`,
		func(d *Day, n int) { d.Active = n }, first); err != nil {
		return nil, err
	}
	if err := count(`SELECT started_at / 86400, COUNT(*) FROM installs WHERE test = 0 AND started_at >= ? GROUP BY 1`,
		func(d *Day, n int) { d.Started = n }, first*86400); err != nil {
		return nil, err
	}
	if err := count(`SELECT outcome_at / 86400, COUNT(*) FROM installs WHERE test = 0 AND outcome = ? AND outcome_at >= ? GROUP BY 1`,
		func(d *Day, n int) { d.Succeeded = n }, usage.EventSucceeded, first*86400); err != nil {
		return nil, err
	}
	return days, nil
}
