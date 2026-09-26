package agent

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

func (e *agentEnv) memory(tz string) api.MemoryAdvice {
	e.t.Helper()
	var a api.MemoryAdvice
	e.decode("GET", e.sp("/memory?tz="+url.QueryEscape(tz)), &a)
	return a
}

func (e *agentEnv) addGCWindow(at time.Time, minAfter, maxAfter, heap, fullGCs int) {
	e.t.Helper()
	if _, err := e.a.db.Exec(`INSERT OR REPLACE INTO gc_windows(server_id, start, collections, min_after_mb, max_after_mb, heap_mb, full_gcs, evacuation_failures, pause_ms, max_pause_ms) VALUES(?, ?, 40, ?, ?, ?, ?, 0, 600, 30)`,
		e.sid, at.Truncate(gcWindow).UnixMilli(), minAfter, maxAfter, heap, fullGCs); err != nil {
		e.t.Fatal(err)
	}
}

func memoryOptions(a api.MemoryAdvice) string {
	var out []string
	for _, o := range a.Options {
		s := fmt.Sprintf("%d:%s", o.MemoryMB, o.Fit)
		if !o.Fits {
			s += "(no room)"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

// Settings › Memory reads two weeks of garbage collection: nothing to say at
// first, then that the budget fits with each day's peak in the viewer's time
// zone, and a bigger budget once the server runs short. Budgets the machine
// has no room for are offered, but don't fit.
func TestMemoryAdviceFromTwoWeeksOfGC(t *testing.T) {
	e := newAgentEnv(t)
	e.create()
	for _, q := range []string{"", "?tz=Not/AZone"} {
		if code, out := e.call("GET", e.sp("/memory"+q), nil); code != 400 {
			t.Fatalf("memory%s: %d %v", q, code, out)
		}
	}

	a := e.memory("UTC")
	if a.Verdict != "not_enough_data" || a.RecommendedMB != 0 || a.BudgetMB != 1536 || a.HeapMB != 1024 || a.FromNextStart || len(a.Days) != 14 {
		t.Fatalf("nothing measured yet: %+v", a)
	}
	if got := memoryOptions(a); got != "1536: 2048: 3072:" {
		t.Fatalf("options without history: %s", got)
	}

	now := time.Now().UTC()
	peaks := map[time.Time]int{}
	for h := 6; h < 14*24; h += 6 {
		at := now.Add(-time.Duration(h) * time.Hour)
		e.addGCWindow(at, 300, 600, 1024, 0)
		peaks[at.Truncate(gcWindow)] = 600
	}
	evening := time.Date(now.Year(), now.Month(), now.Day()-1, 20, 0, 0, 0, time.UTC)
	e.addGCWindow(evening, 300, 650, 1024, 0)
	peaks[evening] = 650
	a = e.memory("UTC")
	if a.Verdict != "keep" || a.Params["reason"] != "smallest" || a.RecommendedMB != 1536 {
		t.Fatalf("a budget with room to spare: %s %v %d", a.Verdict, a.Params, a.RecommendedMB)
	}
	if got := memoryOptions(a); got != "1536:room_to_grow 2048:more_than_needed 3072:more_than_needed" {
		t.Fatalf("options: %s", got)
	}
	for _, tz := range []string{"UTC", "Asia/Tokyo"} {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]int{}
		for at, mb := range peaks {
			date := at.In(loc).Format(time.DateOnly)
			want[date] = max(want[date], mb)
		}
		days := e.memory(tz).Days
		if len(days) != 14 || days[13].Date != now.In(loc).Format(time.DateOnly) {
			t.Fatalf("%s: 14 days up to today: %+v", tz, days)
		}
		for _, d := range days {
			if d.PeakMB != want[d.Date] {
				t.Errorf("%s: %s peaked at %d MB, want %d", tz, d.Date, d.PeakMB, want[d.Date])
			}
		}
		if want[evening.In(loc).Format(time.DateOnly)] != 650 {
			t.Fatalf("%s: the evening's peak counts on its local day", tz)
		}
	}

	e.addGCWindow(now.Add(-time.Hour), 900, 950, 1024, 1)
	a = e.memory("UTC")
	if a.Verdict != "raise" || a.RecommendedMB != 2048 || len(a.Actions) != 1 || a.Actions[0].Kind != "raise_memory" || !a.Actions[0].Recommended {
		t.Fatalf("a full collection asks for more: %s %d %+v", a.Verdict, a.RecommendedMB, a.Actions)
	}
	if got := memoryOptions(a); got != "1536:too_tight 2048:room_to_grow 3072:more_than_needed" {
		t.Fatalf("options after running short: %s", got)
	}

	e.fd.mu.Lock()
	c := e.fd.server()
	c.cfg = withoutGCLog(c.cfg)
	e.fd.mu.Unlock()
	if a := e.memory("UTC"); !a.FromNextStart {
		t.Fatal("a server running without the GC log is measured from its next start")
	}

	first := e.sid
	e.createWith(map[string]any{"name": "Creative"})
	e.sid = first
	a = e.memory("UTC")
	if got := memoryOptions(a); got != "1536:too_tight 2048:room_to_grow(no room) 3072:more_than_needed(no room)" {
		t.Fatalf("with a second server reserving memory: %s", got)
	}
	if a.RecommendedMB != 0 || len(a.Actions) == 0 || a.Actions[len(a.Actions)-1].Kind != "upgrade_host" {
		t.Fatalf("no room for more: %d %+v", a.RecommendedMB, a.Actions)
	}
}
