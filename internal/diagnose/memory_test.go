package diagnose

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

var memNow = time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)

// gcWindows is one 15-minute window every 6 hours from fromDays up to toDays
// back, with the heap after collections between lowMB and highMB.
func gcWindows(fromDays, toDays, heapMB, lowMB, highMB int) []GCWindow {
	var out []GCWindow
	for h := toDays * 24; h < fromDays*24; h += 6 {
		out = append(out, GCWindow{
			Start: memNow.Add(-time.Duration(h)*time.Hour - 15*time.Minute), Collections: 42,
			MinAfterMB: lowMB, MaxAfterMB: highMB, HeapMB: heapMB, PauseMS: 760, MaxPauseMS: 31,
		})
	}
	return out
}

func changed(ws []GCWindow, i int, change func(*GCWindow)) []GCWindow {
	out := append([]GCWindow(nil), ws...)
	change(&out[i])
	return out
}

func memIn(budgetMB int, ws []GCWindow) MemoryInput {
	return MemoryInput{Now: memNow, Windows: ws, BudgetMB: budgetMB, HostMB: 16384, RoomMB: 4096, ViewDistance: 12}
}

func TestAdviseMemory(t *testing.T) {
	with := func(in MemoryInput, change func(*MemoryInput)) MemoryInput {
		change(&in)
		return in
	}
	fullGC := func(w *GCWindow) { w.FullGCs, w.MinAfterMB, w.MaxAfterMB = 1, 2700, 2950 }
	nearlyFull := func(w *GCWindow) { w.MinAfterMB, w.MaxAfterMB = 2580, 2900 }
	pressureBeforeRaise := append(changed(gcWindows(14, 7, 2304, 1900, 2200), 3, func(w *GCWindow) { w.FullGCs = 2 }), gcWindows(7, 0, 3072, 900, 1400)...)
	tooOldOrFuture := append(gcWindows(14, 0, 3072, 900, 1400),
		GCWindow{Start: memNow.Add(-15 * 24 * time.Hour), Collections: 9, MinAfterMB: 3000, MaxAfterMB: 3050, HeapMB: 3072, FullGCs: 2},
		GCWindow{Start: memNow.Add(time.Hour), Collections: 9, MinAfterMB: 3000, MaxAfterMB: 3050, HeapMB: 3072, FullGCs: 1})

	tests := []struct {
		name        string
		in          MemoryInput
		verdict     MemoryVerdict
		params      map[string]any
		actions     string
		explanation []string
		evidence    []string
	}{
		{
			name: "nothing measured yet", in: memIn(6144, nil),
			verdict: MemoryNotEnoughData, params: map[string]any{"days": 0},
			explanation: []string{"hasn't measured how much memory the server needs yet"},
		},
		{
			name: "windows with only remark pauses measure nothing", in: memIn(4096, []GCWindow{{Start: memNow.Add(-time.Hour), Collections: 1, HeapMB: 3072}}),
			verdict: MemoryNotEnoughData, explanation: []string{"hasn't measured"},
		},
		{
			name: "two days are not enough to suggest less", in: memIn(8192, gcWindows(2, 0, 6144, 900, 1500)),
			verdict: MemoryNotEnoughData, params: map[string]any{"days": 2, "min_days": 3, "min_span_days": 7, "peak_mb": 1500},
			explanation: []string{"So far it has measurements from 2 days: it needed up to 1.5 GB, and it has 6 GB for the game."},
			evidence:    []string{"Garbage collection was measured on 2 days of the last 2 days."},
		},
		{
			name:    "a long history with only two days the server ran",
			in:      memIn(8192, append(gcWindows(1, 0, 6144, 900, 1500), gcWindows(10, 9, 6144, 900, 1500)...)),
			verdict: MemoryNotEnoughData, params: map[string]any{"days": 2},
			evidence: []string{"measured on 2 days of the last 10 days"},
		},
		{
			name: "8 GB when it never needed more than 2.5 GB", in: memIn(8192, gcWindows(14, 0, 6144, 1400, 2560)),
			verdict: MemoryLower, params: map[string]any{"budget_mb": 8192, "heap_mb": 6144, "peak_mb": 2560, "days": 14, "to_mb": 6144, "to_heap_mb": 4608},
			actions: "lower_memory* from_mb=8192 to_mb=6144",
			explanation: []string{
				"You gave it 8 GB, but it never needed more than 2.5 GB in the last 14 days.",
				"With 6 GB it would still have 4.5 GB for the game", "2 GB of this machine's memory would be free",
			},
			evidence: []string{"At most 2.5 GB of the Java heap was in use after garbage collection in the last 14 days; the heap is now 6 GB.", "measured on 14 days of the last 14 days"},
		},
		{
			name: "lowers as far as the headroom allows", in: memIn(6144, gcWindows(14, 0, 4608, 350, 600)),
			verdict: MemoryLower, params: map[string]any{"to_mb": 1536}, actions: "lower_memory* from_mb=6144 to_mb=1536",
		},
		{
			name: "keeps a budget when the next smaller one would be too tight", in: memIn(6144, gcWindows(14, 0, 4608, 1400, 2560)),
			verdict: MemoryKeep, params: map[string]any{"peak_mb": 2560, "reason": KeepFits, "smaller_mb": 4096, "smaller_heap_mb": 3072},
			explanation: []string{"It needed up to 2.5 GB in the last 14 days, and it has 4.5 GB for the game.", "a 4 GB budget would give it only 3 GB for the game"},
		},
		{
			name: "keeps a tight budget that hasn't run short", in: memIn(4096, gcWindows(14, 0, 3072, 1800, 2400)),
			verdict: MemoryKeep, params: map[string]any{"reason": KeepTight},
			explanation: []string{"close to all of it, though it hasn't run short at this size"},
		},
		{
			name: "keeps the smallest budget", in: memIn(1536, gcWindows(14, 0, 1024, 200, 300)),
			verdict: MemoryKeep, params: map[string]any{"reason": KeepSmallest}, explanation: []string{"already has the smallest budget Playkeeper offers"},
		},
		{
			name:    "one evacuation failure is no reason to raise but rules out lowering",
			in:      memIn(8192, changed(gcWindows(14, 0, 6144, 1000, 1600), 10, func(w *GCWindow) { w.EvacuationFailures = 1 })),
			verdict: MemoryKeep, params: map[string]any{"reason": KeepRanShortOnce},
			explanation: []string{"It ran short of memory once in that time, so it shouldn't have less."},
		},
		{
			name: "a full collection after one day asks for the next budget", in: memIn(4096, changed(gcWindows(1, 0, 3072, 2100, 2800), 1, fullGC)),
			verdict: MemoryRaise, params: map[string]any{"full_gcs": 1, "to_mb": 6144, "days": 1},
			actions:     "raise_memory* from_mb=4096 to_mb=6144",
			explanation: []string{"In the last day it had to stop the game for a full clean-up once.", "That happens when the memory it has for the game, 3 GB, is too little."},
			evidence:    []string{"1 full garbage collection in the last day.", "This machine could give the server up to 4 GB more."},
		},
		{
			name:    "no room for more offers a lower view distance first",
			in:      with(memIn(4096, changed(gcWindows(1, 0, 3072, 2100, 2800), 1, fullGC)), func(in *MemoryInput) { in.RoomMB = 0 }),
			verdict: MemoryRaise, actions: "lower_view_distance* from=12 to=8; upgrade_host resource=memory",
			evidence: []string{"no memory to spare"},
		},
		{
			name:    "an unknown machine gets no action",
			in:      with(memIn(4096, changed(gcWindows(1, 0, 3072, 2100, 2800), 1, fullGC)), func(in *MemoryInput) { in.HostMB = 0 }),
			verdict: MemoryRaise, actions: "",
		},
		{
			name:    "two windows nearly full after every collection",
			in:      memIn(4096, changed(changed(gcWindows(3, 0, 3072, 1500, 2600), 2, nearlyFull), 5, nearlyFull)),
			verdict: MemoryRaise, params: map[string]any{"high_windows": 2, "full_gcs": 0},
			actions:     "raise_memory* from_mb=4096 to_mb=6144",
			explanation: []string{"At worst, 84% of the memory it has for the game was still in use even right after cleaning up."},
			evidence:    []string{"In 2 measured periods the Java heap stayed at least 80% full after every garbage collection, at worst 84%."},
		},
		{
			name: "running short before a raise doesn't count now, but rules out going back", in: memIn(4096, pressureBeforeRaise),
			verdict: MemoryKeep, params: map[string]any{"reason": KeepRanShortWithLess, "short_heap_mb": 2304, "smaller_mb": 3072},
			explanation: []string{"It ran short of memory with 2.2 GB for the game in that time, and a 3 GB budget would give it only 2.2 GB, so it shouldn't have less."},
		},
		{
			name:    "after following a suggestion to lower, it doesn't suggest lowering again",
			in:      memIn(6144, append(gcWindows(14, 1, 6144, 1400, 2560), gcWindows(1, 0, 4608, 1300, 2400)...)),
			verdict: MemoryKeep, params: map[string]any{"reason": KeepFits, "peak_mb": 2560},
		},
		{
			name: "windows older than 14 days or in the future are ignored", in: memIn(4096, tooOldOrFuture),
			verdict: MemoryLower, params: map[string]any{"peak_mb": 1400, "to_mb": 3072}, actions: "lower_memory* from_mb=4096 to_mb=3072",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := AdviseMemory(tt.in)
			if a.Verdict != tt.verdict {
				t.Fatalf("got %s: %s %s", a.Verdict, a.Title, a.Explanation)
			}
			for k, v := range tt.params {
				if fmt.Sprint(a.Params[k]) != fmt.Sprint(v) {
					t.Errorf("params[%s] = %v, want %v (all: %v)", k, a.Params[k], v, a.Params)
				}
			}
			if got := actionSummary(a.Actions); got != tt.actions {
				t.Errorf("actions = %q, want %q", got, tt.actions)
			}
			for _, s := range tt.explanation {
				if !strings.Contains(a.Explanation, s) {
					t.Errorf("explanation %q lacks %q", a.Explanation, s)
				}
			}
			var ev []string
			for _, e := range a.Evidence {
				ev = append(ev, e.Text)
			}
			for _, s := range tt.evidence {
				if !strings.Contains(strings.Join(ev, "\n"), s) {
					t.Errorf("evidence lacks %q:\n%s", s, strings.Join(ev, "\n"))
				}
			}
			recommended := 0
			for _, act := range a.Actions {
				if act.Recommended {
					recommended++
				}
			}
			if len(a.Actions) > 0 && recommended != 1 {
				t.Errorf("%d recommended actions: %s", recommended, actionSummary(a.Actions))
			}
			if a.Title == "" || a.Explanation == "" || a.Params == nil {
				t.Errorf("incomplete advice: %+v", a)
			}
			if _, err := json.Marshal(a); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestAdviseMemoryReadsARealGCLog(t *testing.T) {
	windows := SummarizeGC(parseGCFixture(t, "gc/g1_aikar.txt", time.Time{}), 15*time.Minute)
	in := MemoryInput{Now: time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC), Windows: windows, BudgetMB: 6144, HostMB: 16384, RoomMB: 2048, ViewDistance: 10}
	a := AdviseMemory(in)
	if a.Verdict != MemoryRaise || actionSummary(a.Actions) != "raise_memory* from_mb=6144 to_mb=8192" {
		t.Fatalf("got %s %s: %s", a.Verdict, actionSummary(a.Actions), a.Explanation)
	}
	if a.Params["full_gcs"] != 1 || a.Params["evacuation_failures"] != 1 {
		t.Errorf("the System.gc() collection must not count: %v", a.Params)
	}
	in.Windows = windows[:1]
	if a := AdviseMemory(in); a.Verdict != MemoryNotEnoughData || a.Params["peak_mb"] != 731 {
		t.Errorf("a quiet first window: %s %v", a.Verdict, a.Params)
	}
}
