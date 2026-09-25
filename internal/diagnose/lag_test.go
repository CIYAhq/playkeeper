package diagnose

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

var lagNow = time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)

func paperTicks(tps, mspt float64) *TickStats {
	return &TickStats{TPS: tps, MSPT: mspt, MaxMSPT: mspt * 2, TargetTPS: 20}
}

func causeKinds(d LagDiagnosis) []CauseKind {
	var out []CauseKind
	for _, c := range d.Causes {
		out = append(out, c.Kind)
	}
	return out
}

func findCause(t *testing.T, d LagDiagnosis, k CauseKind) Cause {
	t.Helper()
	for _, c := range d.Causes {
		if c.Kind == k {
			return c
		}
	}
	t.Fatalf("no %s cause in %v", k, causeKinds(d))
	return Cause{}
}

func actionSummary(actions []Action) string {
	var parts []string
	for _, a := range actions {
		s := string(a.Kind)
		if a.Recommended {
			s += "*"
		}
		for _, k := range []string{"from_mb", "to_mb", "from", "to", "resource", "jar", "name", "pack", "port", "free_mb"} {
			if v, ok := a.Params[k]; ok {
				s += fmt.Sprintf(" %s=%v", k, v)
			}
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

func TestExplainLagSaysSmoothAndLooksNoFurther(t *testing.T) {
	d := ExplainLag(LagInput{Now: lagNow, Ticks: paperTicks(20, 12), HostCPU: &CPUUsage{StealPercent: 30}, Players: ptr(4)})
	if d.Status != LagSmooth || len(d.Causes) != 0 || !strings.Contains(d.Explanation, "12 ms of the 50 ms") {
		t.Fatalf("got %+v", d)
	}
	if strings.Contains(d.Explanation, "close to the limit") {
		t.Error("12 ms is not close to the limit")
	}
	if d := ExplainLag(LagInput{Now: lagNow, Ticks: paperTicks(20, 45)}); d.Status != LagSmooth || !strings.Contains(d.Explanation, "close to the limit") {
		t.Errorf("45 ms of 50: got %+v", d)
	}
}

func TestExplainLagStatusThresholds(t *testing.T) {
	overload := func(ago time.Duration, ms int) ConsoleLine {
		return ConsoleLine{At: lagNow.Add(-ago), Text: fmt.Sprintf("[19:55:00] [Server thread/WARN]: Can't keep up! Is the server overloaded? Running %dms or %d ticks behind", ms, ms/50)}
	}
	var continuous []ConsoleLine
	for i := range 25 {
		continuous = append(continuous, overload(time.Duration(i)*20*time.Second, 2500))
	}
	for _, tc := range []struct {
		name string
		in   LagInput
		want LagStatus
	}{
		{"19 of 20", LagInput{Ticks: paperTicks(19, 50)}, LagSmooth},
		{"18.4 of 20", LagInput{Ticks: paperTicks(18.4, 54)}, LagBitBehind},
		{"15.9 of 20", LagInput{Ticks: paperTicks(15.9, 63)}, LagLagging},
		{"smooth but spiky", LagInput{Ticks: &TickStats{TPS: 20, MSPT: 20, MaxMSPT: 612, TargetTPS: 20}}, LagBitBehind},
		{"smooth with overloads", LagInput{Ticks: paperTicks(20, 20), Console: []ConsoleLine{overload(time.Minute, 4200)}}, LagBitBehind},
		{"overloads only", LagInput{Console: []ConsoleLine{overload(time.Minute, 2034), overload(3*time.Minute, 4200), overload(20*time.Minute, 9000)}}, LagBitBehind},
		{"continuous overloads", LagInput{Console: continuous}, LagLagging},
		{"forged overloads", LagInput{Console: []ConsoleLine{{At: lagNow, Text: "[19:59:00] [Server thread/INFO]: <Steve> Can't keep up! Is the server overloaded? Running 9000ms or 180 ticks behind"}}}, LagUnknown},
		{"nothing measured", LagInput{}, LagUnknown},
		{"frozen", LagInput{Ticks: &TickStats{TPS: 20, TargetTPS: 20, Frozen: true}}, LagFrozen},
		{"sprinting", LagInput{Ticks: &TickStats{TPS: 20, TargetTPS: 20, Sprinting: true}}, LagUnknown},
	} {
		tc.in.Now = lagNow
		d := ExplainLag(tc.in)
		if d.Status != tc.want {
			t.Errorf("%s: got %s (%s), want %s", tc.name, d.Status, d.Explanation, tc.want)
		}
		if d.Title == "" || d.Explanation == "" {
			t.Errorf("%s: missing title or explanation: %+v", tc.name, d)
		}
	}
	d := ExplainLag(LagInput{Now: lagNow, Console: []ConsoleLine{overload(time.Minute, 2034), overload(3*time.Minute, 4200), overload(20*time.Minute, 9000)}})
	if d.Params["overloads"] != 2 || !strings.Contains(d.Explanation, "fell behind 2 times in the last 10 minutes, by up to 4.2 seconds") {
		t.Errorf("overloads outside the window must not count: %+v", d)
	}
}

func TestExplainLagRanksCausesByTheirEvidence(t *testing.T) {
	d := ExplainLag(LagInput{
		Now:        lagNow,
		ServerType: "paper",
		Ticks:      paperTicks(11.2, 89),
		ServerCPU:  ptr(140.0),
		HostCores:  2,
		HostCPU:    &CPUUsage{BusyPercent: 70, StealPercent: 25},
		GC: []GCEvent{
			{At: lagNow.Add(-4 * time.Minute), Kind: GCYoung, AfterMB: 2900, HeapMB: 3072, Pause: 90 * time.Millisecond},
			{At: lagNow.Add(-3 * time.Minute), Kind: GCFull, AfterMB: 2600, HeapMB: 3072, Pause: 2600 * time.Millisecond},
		},
		BudgetMB:           4096,
		HostMB:             16384,
		RoomMB:             4096,
		Players:            ptr(3),
		NewChunks:          ptr(1240),
		ViewDistance:       10,
		SimulationDistance: 12,
	})
	want := []CauseKind{CauseCPUSteal, CauseMemoryPressure, CauseChunkGeneration, CauseHighDistance}
	if d.Status != LagLagging || !reflect.DeepEqual(causeKinds(d), want) {
		t.Fatalf("got %s %v, want lagging %v", d.Status, causeKinds(d), want)
	}
	if !strings.Contains(d.Explanation, "56% speed") {
		t.Errorf("explanation: %s", d.Explanation)
	}
	mem := findCause(t, d, CauseMemoryPressure)
	if got := actionSummary(mem.Actions); got != "raise_memory* from_mb=4096 to_mb=6144" {
		t.Errorf("memory actions: %s", got)
	}
	if !strings.Contains(mem.Explanation, "full clean-up 1 time") || len(mem.Evidence) == 0 {
		t.Errorf("memory cause: %+v", mem)
	}
	chunks := findCause(t, d, CauseChunkGeneration)
	if !strings.Contains(chunks.Explanation, "1,240 new chunks") || !strings.Contains(chunks.Explanation, "while 3 players were online") {
		t.Errorf("chunk cause: %s", chunks.Explanation)
	}
	for _, c := range d.Causes {
		if c.Title == "" || c.Explanation == "" || len(c.Evidence) == 0 || c.Score < 1 || c.Score > 100 {
			t.Errorf("incomplete cause %+v", c)
		}
	}
}

func TestMemoryFixIsOnlyOfferedWhenTheMachineHasRoom(t *testing.T) {
	base := LagInput{
		Now: lagNow, Ticks: paperTicks(14, 71), BudgetMB: 4096, HostMB: 8192,
		GC: []GCEvent{{At: lagNow.Add(-time.Minute), Kind: GCYoung, AfterMB: 2950, HeapMB: 3072, Pause: 40 * time.Millisecond}},
	}
	for _, tc := range []struct {
		name string
		room int
		view int
		want string
	}{
		{"room for the next budget", 2048, 10, "raise_memory* from_mb=4096 to_mb=6144"},
		{"room, but not for a whole step", 1024, 10, "lower_view_distance* from=10 to=8; upgrade_host resource=memory"},
		{"no room, view distance already low", 0, 8, "upgrade_host* resource=memory"},
	} {
		in := base
		in.RoomMB, in.ViewDistance = tc.room, tc.view
		c := findCause(t, ExplainLag(in), CauseMemoryPressure)
		if got := actionSummary(c.Actions); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	old := base
	old.GC = []GCEvent{{At: lagNow.Add(-40 * time.Minute), Kind: GCFull, AfterMB: 3000, HeapMB: 3072, Pause: 3 * time.Second}}
	for _, c := range ExplainLag(old).Causes {
		if c.Kind == CauseMemoryPressure {
			t.Errorf("GC pauses outside the window must not count: %+v", c)
		}
	}
}

func TestHostCPUCauseTellsOtherProgramsFromTheServerItself(t *testing.T) {
	in := LagInput{Now: lagNow, Ticks: paperTicks(15, 66), HostCores: 4, HostCPU: &CPUUsage{BusyPercent: 96}}
	in.ServerCPU = ptr(150.0)
	c := findCause(t, ExplainLag(in), CauseHostCPUBusy)
	if c.Title != "Other programs are using most of the processor" || actionSummary(c.Actions) != "reduce_other_load*; upgrade_host resource=cpu" {
		t.Errorf("others busy: %s / %s", c.Title, actionSummary(c.Actions))
	}
	in.ServerCPU = ptr(360.0)
	c = findCause(t, ExplainLag(in), CauseHostCPUBusy)
	if c.Score != 65 || actionSummary(c.Actions) != "upgrade_host* resource=cpu" || !strings.Contains(c.Explanation, "this server used 90%") {
		t.Errorf("server busy: %+v", c)
	}
	in.ServerCPU = nil
	if c := findCause(t, ExplainLag(in), CauseHostCPUBusy); !strings.Contains(c.Explanation, "couldn't tell") {
		t.Errorf("unknown share: %s", c.Explanation)
	}
}

func TestWorldWorkloadIsOnlyTheLastExplanation(t *testing.T) {
	in := LagInput{Now: lagNow, ServerType: "paper", Ticks: paperTicks(15, 66), ServerCPU: ptr(100.0), SimulationDistance: 10}
	d := ExplainLag(in)
	if !reflect.DeepEqual(causeKinds(d), []CauseKind{CauseWorldWorkload}) {
		t.Fatalf("got %v", causeKinds(d))
	}
	if c := d.Causes[0]; actionSummary(c.Actions) != "run_profiler*; lower_simulation_distance from=10 to=8" || !strings.Contains(c.Explanation, "1 core on average") {
		t.Errorf("paper: %s / %s", actionSummary(c.Actions), c.Explanation)
	}
	in.ServerType = "vanilla"
	if c := ExplainLag(in).Causes[0]; actionSummary(c.Actions) != "lower_simulation_distance* from=10 to=8" {
		t.Errorf("vanilla has no profiler: %s", actionSummary(c.Actions))
	}
	in.HostCPU = &CPUUsage{BusyPercent: 40, StealPercent: 22}
	if got := causeKinds(ExplainLag(in)); !reflect.DeepEqual(got, []CauseKind{CauseCPUSteal}) {
		t.Errorf("a strong outside cause replaces the guess: %v", got)
	}
	in = LagInput{Now: lagNow, Ticks: paperTicks(15, 66), ServerCPU: ptr(30.0)}
	if d := ExplainLag(in); len(d.Causes) != 0 || !strings.Contains(d.Explanation, "don't point at a clear cause") {
		t.Errorf("no evidence, no cause: %+v", d)
	}
}

func TestOtherLagCauses(t *testing.T) {
	d := ExplainLag(LagInput{Now: lagNow, Ticks: paperTicks(14, 70), ServerCPU: ptr(195.0), CPULimitCores: 2,
		HostCPU: &CPUUsage{BusyPercent: 50, IOWaitPercent: 35}, Players: ptr(0), SimulationDistance: 16, ViewDistance: 16})
	if got := causeKinds(d); !reflect.DeepEqual(got, []CauseKind{CauseCPULimit, CauseSlowDisk}) {
		t.Fatalf("got %v (no players online, so distances don't matter)", got)
	}
	if c := findCause(t, d, CauseCPULimit); actionSummary(c.Actions) != "raise_cpu_limit*" || !strings.Contains(c.Explanation, "limit is 2 cores") {
		t.Errorf("cpu limit: %+v", c)
	}
	d = ExplainLag(LagInput{Now: lagNow, Ticks: paperTicks(17, 58), Players: ptr(6), SimulationDistance: 16, ViewDistance: 20})
	c := findCause(t, d, CauseHighDistance)
	if c.Score != 55 || actionSummary(c.Actions) != "lower_simulation_distance* from=16 to=10; lower_view_distance from=20 to=10" ||
		!strings.Contains(c.Explanation, "about 1,089 chunks") {
		t.Errorf("distance: %d %s %s", c.Score, actionSummary(c.Actions), c.Explanation)
	}
}

func TestParseDistancesReadsOnlyTheDistances(t *testing.T) {
	props := []byte("#Minecraft server properties\nrcon.password=hunter2\nview-distance=16\r\nsimulation-distance = 8\nmax-players=20\n")
	if v, s := ParseDistances(props); v != 16 || s != 8 {
		t.Errorf("got %d %d", v, s)
	}
	if v, s := ParseDistances([]byte("view-distance=far\nsimulation-distance=99\n")); v != 0 || s != 0 {
		t.Errorf("invalid values: got %d %d", v, s)
	}
}
