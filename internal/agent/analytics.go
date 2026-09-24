package agent

import (
	"database/sql"
	"math"
	"sort"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// Ranges offered by the UI and the bucket width used for each.
var metricRanges = map[string]struct {
	span, bucket time.Duration
}{
	"1h":  {time.Hour, time.Minute},
	"24h": {24 * time.Hour, 10 * time.Minute},
	"7d":  {7 * 24 * time.Hour, time.Hour},
	"30d": {30 * 24 * time.Hour, 6 * time.Hour},
}

type sample struct {
	ts     time.Time
	state  string
	online sql.NullInt64
	cpu    sql.NullFloat64
	mem    sql.NullInt64
}

func (a *Agent) samplesBetween(from, to time.Time) ([]sample, error) {
	rows, err := a.db.Query(`SELECT ts, state, players_online, cpu_pct, mem_bytes FROM samples WHERE ts >= ? AND ts < ? ORDER BY ts`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sample
	for rows.Next() {
		var s sample
		var ts int64
		if err := rows.Scan(&ts, &s.state, &s.online, &s.cpu, &s.mem); err != nil {
			return nil, err
		}
		s.ts = time.UnixMilli(ts).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// Metrics buckets samples over a range. Buckets without samples are no_data
// (the collector was not running), buckets where the server was not running
// are offline; neither reports a player count of zero.
func (a *Agent) Metrics(rangeKey string, now time.Time) (api.MetricsResponse, error) {
	rg, ok := metricRanges[rangeKey]
	if !ok {
		return api.MetricsResponse{}, errInvalid("range must be one of 1h, 24h, 7d, 30d")
	}
	to := now.UTC().Truncate(rg.bucket).Add(rg.bucket)
	from := to.Add(-rg.span)
	since := a.collectingSince()
	samples, err := a.samplesBetween(from, to)
	if err != nil {
		return api.MetricsResponse{}, err
	}
	resp := api.MetricsResponse{
		From: from, To: to, BucketSeconds: int(rg.bucket.Seconds()), SampleIntervalSeconds: int(a.opts.SampleInterval.Seconds()),
		CollectingSince: since, Source: "Playkeeper agent samples: Docker stats, Server List Ping and RCON list",
		Buckets: []api.MetricsBucket{}, Gaps: []api.Gap{},
	}
	resp.Buckets = bucketize(samples, from, to, rg.bucket, a.opts.SampleInterval, since, now)
	resp.Gaps = findGaps(samples, from, now, a.opts.SampleInterval, since)
	return resp, nil
}

func bucketize(samples []sample, from, to time.Time, bucket, interval time.Duration, since *time.Time, now time.Time) []api.MetricsBucket {
	var out []api.MetricsBucket
	i := 0
	for start := from; start.Before(to); start = start.Add(bucket) {
		end := start.Add(bucket)
		b := api.MetricsBucket{Start: start}
		var n, onlineN, cpuN, memN int
		var maxPlayers int
		var cpuSum float64
		var memSum int64
		for i < len(samples) && samples[i].ts.Before(end) {
			s := samples[i]
			i++
			if s.ts.Before(start) {
				continue
			}
			n++
			if s.state == "online" {
				onlineN++
				if s.online.Valid && int(s.online.Int64) > maxPlayers {
					maxPlayers = int(s.online.Int64)
				}
			}
			if s.cpu.Valid {
				cpuSum += s.cpu.Float64
				cpuN++
			}
			if s.mem.Valid {
				memSum += s.mem.Int64
				memN++
			}
		}
		// Expected samples only count time that has passed since collection began.
		effStart, effEnd := start, end
		if since != nil && since.After(effStart) {
			effStart = *since
		}
		if now.Before(effEnd) {
			effEnd = now
		}
		expected := effEnd.Sub(effStart).Seconds() / interval.Seconds()
		switch {
		case since == nil || !end.After(*since):
			b.State = "not_collected"
		case n == 0:
			b.State = "no_data"
		case onlineN > 0:
			b.State = "online"
			mp := maxPlayers
			b.PlayersMax = &mp
		default:
			b.State = "offline"
		}
		if expected > 0 {
			b.Coverage = math.Min(1, float64(n)/math.Max(1, math.Floor(expected)))
		}
		if cpuN > 0 {
			v := cpuSum / float64(cpuN)
			b.CPUAvg = &v
		}
		if memN > 0 {
			v := memSum / int64(memN)
			b.MemAvg = &v
		}
		out = append(out, b)
	}
	return out
}

// findGaps reports collector outages (no samples for more than 2.5 sample
// intervals) and periods where the server was not running.
func findGaps(samples []sample, from, now time.Time, interval time.Duration, since *time.Time) []api.Gap {
	gaps := []api.Gap{}
	if since == nil {
		return gaps
	}
	limit := time.Duration(float64(interval) * 2.5)
	prev := from
	if since.After(prev) {
		prev = *since
	}
	var offStart *time.Time
	for _, s := range samples {
		if s.ts.Sub(prev) > limit {
			gaps = append(gaps, api.Gap{From: prev, To: s.ts, Kind: "collector_down"})
		}
		prev = s.ts
		if s.state != "online" && s.state != "starting" {
			if offStart == nil {
				t := s.ts
				offStart = &t
			}
		} else if offStart != nil {
			gaps = append(gaps, api.Gap{From: *offStart, To: s.ts, Kind: "server_offline"})
			offStart = nil
		}
	}
	if offStart != nil {
		gaps = append(gaps, api.Gap{From: *offStart, To: prev, Kind: "server_offline"})
	}
	if now.Sub(prev) > limit {
		gaps = append(gaps, api.Gap{From: prev, To: now, Kind: "collector_down"})
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].From.Before(gaps[j].From) })
	return gaps
}

func (a *Agent) Sessions(from, to, now time.Time, limit int) ([]api.Session, error) {
	rows, err := a.db.Query(`SELECT id, player, COALESCE(uuid, ''), start_ts, end_ts, end_reason, start_uncertain, end_uncertain, source
		FROM sessions WHERE start_ts < ? AND (end_ts IS NULL OR end_ts >= ?) ORDER BY start_ts DESC LIMIT ?`,
		to.UnixMilli(), from.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Session{}
	for rows.Next() {
		var s api.Session
		var start int64
		var end sql.NullInt64
		var su, eu int
		if err := rows.Scan(&s.ID, &s.Player, &s.UUID, &start, &end, &s.EndReason, &su, &eu, &s.Source); err != nil {
			return nil, err
		}
		s.Start = time.UnixMilli(start).UTC()
		s.StartUncertain, s.EndUncertain = su == 1, eu == 1
		stop := now
		if end.Valid {
			t := time.UnixMilli(end.Int64).UTC()
			s.End = &t
			stop = t
		}
		s.DurationSeconds = int64(stop.Sub(s.Start).Seconds())
		out = append(out, s)
	}
	return out, rows.Err()
}

// Summary aggregates observed sessions per local day. Playtime counts only
// observed time; days with collection gaps report their coverage.
func (a *Agent) Summary(days int, tzName string, now time.Time) (api.PlayersSummary, error) {
	loc, err := time.LoadLocation(tzName)
	if err != nil || tzName == "" {
		return api.PlayersSummary{}, errInvalid("tz must be an IANA time zone name")
	}
	if days < 1 || days > 90 {
		return api.PlayersSummary{}, errInvalid("days must be between 1 and 90")
	}
	localNow := now.In(loc)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	from := today.AddDate(0, 0, -(days - 1))
	sessions, err := a.Sessions(from, now, now, 100000)
	if err != nil {
		return api.PlayersSummary{}, err
	}
	samples, err := a.samplesBetween(from, now)
	if err != nil {
		return api.PlayersSummary{}, err
	}
	since := a.collectingSince()
	out := api.PlayersSummary{TZ: tzName, CollectingSince: since, RetentionDays: int(a.opts.Retention.Events.Hours() / 24), Days: []api.DailyActivity{}, Players: []api.PlayerStat{}}
	stats := map[string]*api.PlayerStat{}
	online := map[string]bool{}
	for _, s := range sessions {
		out.ObservedSessions++
		if s.StartUncertain || s.EndUncertain {
			out.UncertainSessions++
		}
		ps := stats[s.Player]
		if ps == nil {
			ps = &api.PlayerStat{Name: s.Player, UUID: s.UUID}
			stats[s.Player] = ps
		}
		if ps.UUID == "" {
			ps.UUID = s.UUID
		}
		ps.Sessions++
		ps.PlaytimeSeconds += s.DurationSeconds
		last := now
		if s.End != nil {
			last = *s.End
		} else {
			online[s.Player] = true
		}
		if last.After(ps.LastSeen) {
			ps.LastSeen = last
		}
	}
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		act := api.DailyActivity{Date: d.Format("2006-01-02")}
		players := map[string]bool{}
		for _, s := range sessions {
			end := now
			if s.End != nil {
				end = *s.End
			}
			lo, hi := maxTime(s.Start, d), minTime(end, dayEnd)
			if !hi.After(lo) {
				continue
			}
			players[s.Player] = true
			act.Sessions++
			act.PlaytimeSeconds += int64(hi.Sub(lo).Seconds())
			if s.EndUncertain || s.StartUncertain {
				act.PlaytimeLowerBound = true
			}
		}
		act.UniquePlayers = len(players)
		act.Coverage = coverage(samples, d, minTime(dayEnd, now), a.opts.SampleInterval, since)
		out.Days = append(out.Days, act)
	}
	for name, ps := range stats {
		ps.Online = online[name]
		out.Players = append(out.Players, *ps)
	}
	sort.Slice(out.Players, func(i, j int) bool { return out.Players[i].LastSeen.After(out.Players[j].LastSeen) })
	return out, nil
}

func coverage(samples []sample, from, to time.Time, interval time.Duration, since *time.Time) float64 {
	if since == nil || !to.After(*since) {
		return 0
	}
	if since.After(from) {
		from = *since
	}
	expected := to.Sub(from).Seconds() / interval.Seconds()
	if expected < 1 {
		return 1
	}
	n := 0
	for _, s := range samples {
		if !s.ts.Before(from) && s.ts.Before(to) {
			n++
		}
	}
	return math.Min(1, float64(n)/math.Floor(expected))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (a *Agent) Events(limit int) ([]api.Event, error) {
	rows, err := a.db.Query(`SELECT id, ts, kind, COALESCE(player, ''), source, detail FROM events ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Event{}
	for rows.Next() {
		var e api.Event
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.Kind, &e.Player, &e.Source, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
