package diagnose

import (
	"strings"
	"testing"
	"time"
)

func parseGCFixture(t *testing.T, name string, jvmStart time.Time) []GCEvent {
	t.Helper()
	var out []GCEvent
	for _, line := range strings.Split(fixture(t, name), "\n") {
		if e, ok := ParseGCLine(line, jvmStart); ok {
			out = append(out, e)
		}
	}
	return out
}

func ms(v float64) time.Duration { return time.Duration(v * float64(time.Millisecond)) }

func TestParseGCLineReadsG1PausesWithAikarsFlags(t *testing.T) {
	events := parseGCFixture(t, "gc/g1_aikar.log", time.Time{})
	if len(events) != 11 {
		t.Fatalf("got %d events, want 11: %+v", len(events), events)
	}
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for i, want := range []GCEvent{
		{At: at("2026-09-25T14:00:21.004Z"), Uptime: ms(20513), ID: 0, Kind: GCYoung, Cause: "G1 Evacuation Pause", BeforeMB: 1382, AfterMB: 176, HeapMB: 4608, Pause: ms(38.214)},
		{At: at("2026-09-25T14:03:47.880Z"), Uptime: ms(227389), ID: 1, Kind: GCYoung, Cause: "G1 Evacuation Pause", BeforeMB: 1560, AfterMB: 402, HeapMB: 4608, Pause: ms(21.907)},
		{At: at("2026-09-25T14:05:02.113Z"), Uptime: ms(301622), ID: 2, Kind: GCYoung, Cause: "G1 Humongous Allocation", BeforeMB: 1790, AfterMB: 655, HeapMB: 4608, Pause: ms(17.33)},
		{At: at("2026-09-25T14:05:02.301Z"), Uptime: ms(301810), ID: 3, Kind: GCRemark, BeforeMB: 702, AfterMB: 690, HeapMB: 4608, Pause: ms(9.874)},
		{At: at("2026-09-25T14:05:02.377Z"), Uptime: ms(301886), ID: 3, Kind: GCCleanup, BeforeMB: 690, AfterMB: 690, HeapMB: 4608, Pause: ms(0.412)},
		{At: at("2026-09-25T14:06:40.519Z"), Uptime: ms(400028), ID: 4, Kind: GCYoung, Cause: "G1 Evacuation Pause", BeforeMB: 2052, AfterMB: 731, HeapMB: 4608, Pause: ms(19.556)},
		{At: at("2026-09-25T14:06:58.240Z"), Uptime: ms(417749), ID: 5, Kind: GCMixed, Cause: "G1 Evacuation Pause", BeforeMB: 1105, AfterMB: 540, HeapMB: 4608, Pause: ms(24.118)},
		{At: at("2026-09-25T14:31:12.007Z"), Uptime: ms(1871516), ID: 6, Kind: GCYoung, Cause: "G1 Evacuation Pause", BeforeMB: 4512, AfterMB: 4460, HeapMB: 4608, Pause: ms(118.402), EvacuationFailure: true},
		{At: at("2026-09-25T14:31:14.664Z"), Uptime: ms(1874173), ID: 7, Kind: GCFull, Cause: "G1 Compaction Pause", BeforeMB: 4598, AfterMB: 3122, HeapMB: 4608, Pause: ms(2641.557)},
		{At: at("2026-09-25T14:40:00Z"), Uptime: ms(2399509), ID: 8, Kind: GCFull, Cause: "System.gc()", BeforeMB: 3301, AfterMB: 2980, HeapMB: 4608, Pause: ms(1904.22)},
		{At: at("2026-09-25T14:44:10.250Z"), Uptime: ms(2649759), ID: 9, Kind: GCYoung, Cause: "G1 Humongous Allocation", BeforeMB: 3400, AfterMB: 3010, HeapMB: 4608, Pause: ms(30.112)},
	} {
		got := events[i]
		if !got.At.Equal(want.At) || got.Uptime != want.Uptime || got.Pause != want.Pause {
			t.Errorf("event %d times: got %v %v %v, want %v %v %v", i, got.At, got.Uptime, got.Pause, want.At, want.Uptime, want.Pause)
		}
		got.At, got.Uptime, got.Pause = want.At, want.Uptime, want.Pause
		if got != want {
			t.Errorf("event %d: got %+v, want %+v", i, got, want)
		}
	}
}

func TestParseGCLineAnchorsUptimeAndSkipsEverythingElse(t *testing.T) {
	start := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	events := parseGCFixture(t, "gc/g1_uptime_padded.log", start)
	if len(events) != 1 || events[0].AfterMB != 18 || events[0].HeapMB != 2048 || !events[0].At.Equal(start.Add(ms(4230))) {
		t.Fatalf("got %+v", events)
	}
	if e, ok := ParseGCLine("[1.500s][info][gc] GC(1) Pause Young (Normal) (G1 Evacuation Pause) 300M->20M(2048M) 3.100ms", time.Time{}); !ok || !e.At.IsZero() {
		t.Errorf("without a start time: got %+v, %v", e, ok)
	}
	parallel, ok := ParseGCLine("[2026-09-25T09:00:04.000+0000][info][gc] GC(1) Pause Young (Allocation Failure) 511M->96M(1963M) 12.345ms", time.Time{})
	if !ok || parallel.Kind != GCYoung || parallel.Cause != "Allocation Failure" || parallel.AfterMB != 96 {
		t.Errorf("parallel collector: got %+v, %v", parallel, ok)
	}
	for _, line := range []string{
		"",
		"[0.009s][info][gc] Using G1",
		"[4.217s][info][gc,start] GC(0) Pause Young (Normal) (G1 Evacuation Pause)",
		"[3.100s][info][gc] GC(3) Major Collection (Proactive) 1024M(25%)->512M(12%) 0.345s",
		"[12:00:00 INFO]: <Steve> GC(1) Pause Full (System.gc()) 4000M->100M(4096M) 5.000ms trust me",
		"[0.5s][info][gc] GC(1) Pause Full (G1 Compaction Pause) 4000M->100M(4096M) 5.000ms" + strings.Repeat(" ", 600),
	} {
		if e, ok := ParseGCLine(line, start); ok {
			t.Errorf("%q parsed as %+v", line, e)
		}
	}
}

func TestSummarizeGCKeepsTheLowestHeapAfterEachWindow(t *testing.T) {
	events := parseGCFixture(t, "gc/g1_aikar.log", time.Time{})
	events = append(events, GCEvent{Kind: GCYoung, AfterMB: 1})
	windows := SummarizeGC(events, 15*time.Minute)
	if len(windows) != 2 {
		t.Fatalf("got %d windows: %+v", len(windows), windows)
	}
	first, second := windows[0], windows[1]
	if !first.Start.Equal(time.Date(2026, 9, 25, 14, 0, 0, 0, time.UTC)) || first.Collections != 7 || first.MinAfterMB != 176 ||
		first.MaxAfterMB != 731 || first.HeapMB != 4608 || first.FullGCs != 0 || first.EvacuationFailures != 0 || !near(first.MaxPauseMS, 38.214) {
		t.Errorf("first window: %+v", first)
	}
	if !second.Start.Equal(time.Date(2026, 9, 25, 14, 30, 0, 0, time.UTC)) || second.Collections != 4 || second.MinAfterMB != 2980 ||
		second.MaxAfterMB != 4460 || second.FullGCs != 2 || second.EvacuationFailures != 1 || !near(second.MaxPauseMS, 2641.557) ||
		!near(second.PauseMS, 118.402+2641.557+1904.22+30.112) {
		t.Errorf("second window: %+v", second)
	}
}
