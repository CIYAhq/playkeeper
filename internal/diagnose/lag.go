package diagnose

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// LagStatus is the headline of ExplainLag.
type LagStatus string

const (
	LagSmooth    LagStatus = "smooth"
	LagBitBehind LagStatus = "a_bit_behind"
	LagLagging   LagStatus = "lagging"
	LagFrozen    LagStatus = "frozen" // stopped on purpose with /tick freeze
	LagUnknown   LagStatus = "unknown"
)

// CauseKind identifies a likely cause of lag.
type CauseKind string

const (
	CauseCPUSteal        CauseKind = "cpu_steal"
	CauseCPULimit        CauseKind = "cpu_limit"
	CauseHostCPUBusy     CauseKind = "host_cpu_busy"
	CauseMemoryPressure  CauseKind = "memory_pressure"
	CauseChunkGeneration CauseKind = "chunk_generation"
	CauseSlowDisk        CauseKind = "slow_disk"
	CauseHighDistance    CauseKind = "high_distance"
	CauseWorldWorkload   CauseKind = "world_workload"
)

// LagInput is what the agent measured for one server over the last few
// minutes. Pointer fields are nil when not measured.
type LagInput struct {
	Now    time.Time
	Window time.Duration // the period the measurements cover; 10 minutes when zero

	ServerType string // registry id: paper, purpur, vanilla, fabric, neoforge…

	Ticks   *TickStats    // from ReadTicks
	Console []ConsoleLine // recent console lines, for "Can't keep up!" warnings

	ServerCPU     *float64  // the container's CPU use averaged over the window; 100 = one core
	CPULimitCores float64   // the container's CPU limit; 0 for none
	HostCores     int       //
	HostCPU       *CPUUsage // from /proc/stat snapshots at both ends of the window

	GC       []GCEvent // GC pauses in the window
	BudgetMB int       // the server's memory budget (its container limit)
	HostMB   int       // the machine's memory, for the budgets minecraft.MemoryOptions offers
	RoomMB   int       // memory the machine could still give this server on top of BudgetMB

	Players            *int // online now
	NewChunks          *int // chunks created in the window, from CountChunks at both ends
	ViewDistance       int  // from ParseDistances; 0 when unknown
	SimulationDistance int
}

// LagDiagnosis says how the server is running and, when it falls behind, the
// likely causes, most likely first.
type LagDiagnosis struct {
	Status      LagStatus      `json:"status"`
	Params      map[string]any `json:"params,omitempty"`
	Title       string         `json:"title"`
	Explanation string         `json:"explanation"`
	Evidence    []Evidence     `json:"evidence,omitempty"`
	Causes      []Cause        `json:"causes,omitempty"`
}

// Cause is one likely reason for lag. Score (1–100) is how strongly the
// evidence points at it.
type Cause struct {
	Kind        CauseKind      `json:"kind"`
	Params      map[string]any `json:"params,omitempty"`
	Score       int            `json:"score"`
	Title       string         `json:"title"`
	Explanation string         `json:"explanation"`
	Evidence    []Evidence     `json:"evidence"`
	Actions     []Action       `json:"actions,omitempty"`
}

const defaultDistance = 10

// ExplainLag turns a server's recent measurements into a status and ranked
// causes.
func ExplainLag(in LagInput) LagDiagnosis {
	if in.Window <= 0 {
		in.Window = 10 * time.Minute
	}
	d := lagStatus(in)
	if d.Status != LagBitBehind && d.Status != LagLagging {
		return d
	}
	var causes []Cause
	for _, detect := range []func(LagInput) (Cause, bool){
		stealCause, cpuLimitCause, hostBusyCause, memoryCause, chunkCause, diskCause, distanceCause,
	} {
		if c, ok := detect(in); ok {
			causes = append(causes, c)
		}
	}
	if c, ok := workloadCause(in, causes); ok {
		causes = append(causes, c)
	}
	sort.SliceStable(causes, func(i, j int) bool { return causes[i].Score > causes[j].Score })
	d.Causes = causes
	if len(causes) == 0 {
		d.Explanation += " The measurements Playkeeper has don't point at a clear cause."
	}
	return d
}

func lagStatus(in LagInput) LagDiagnosis {
	var count int
	var worst time.Duration
	since := in.Now.Add(-in.Window)
	for _, l := range in.Console {
		if o, ok := ParseOverload(l); ok && !o.At.Before(since) && !o.At.After(in.Now) {
			count++
			worst = max(worst, o.Behind)
		}
	}
	window := minutesText(in.Window)
	spikes := fmt.Sprintf("it fell behind %s in the last %s, by up to %s seconds", timesText(count), window, trimZero(worst.Seconds()))
	d := LagDiagnosis{Params: map[string]any{"overloads": count}}
	t := in.Ticks
	var target float64
	if t != nil && !t.Frozen && !t.Sprinting {
		target = t.TargetTPS
		if target <= 0 {
			target = 20
		}
		d.Params["tps"], d.Params["target_tps"] = round1(t.TPS), target
		d.Evidence = append(d.Evidence, Evidence{Kind: EvidenceTickRate, Params: map[string]any{"tps": round1(t.TPS), "target": target},
			Text: fmt.Sprintf("It runs %s of %s ticks per second.", trimZero(t.TPS), trimZero(target))})
		if t.MSPT > 0 {
			d.Params["mspt"] = round1(t.MSPT)
			d.Evidence = append(d.Evidence, Evidence{Kind: EvidenceTickTime,
				Params: map[string]any{"mspt": round1(t.MSPT), "target_ms": 1000 / target, "max_ms": round1(t.MaxMSPT)},
				Text:   fmt.Sprintf("A tick takes %s on average; it has %s.", msText(t.MSPT), msText(1000/target))})
		}
	}
	if count > 0 {
		d.Evidence = append(d.Evidence, Evidence{Kind: EvidenceOverloads,
			Params: map[string]any{"count": count, "max_behind_ms": worst.Milliseconds(), "minutes": in.Window.Minutes()},
			Text:   fmt.Sprintf("The server logged \"Can't keep up!\" %s in the last %s.", timesText(count), window)})
	}
	if in.Players != nil {
		d.Evidence = append(d.Evidence, Evidence{Kind: EvidencePlayers, Params: map[string]any{"count": *in.Players},
			Text: fmt.Sprintf("%d %s online.", *in.Players, plural(*in.Players, "player is", "players are"))})
	}

	switch {
	case t != nil && t.Frozen:
		d.Status, d.Title = LagFrozen, "The game is paused"
		d.Explanation = "The game clock is frozen with /tick freeze, so nothing moves until someone runs /tick unfreeze."
		return d
	case t != nil && t.Sprinting:
		d.Status, d.Title = LagUnknown, "The game is sprinting"
		d.Params["sprinting"] = true
		d.Explanation = "Someone started /tick sprint, so the game runs as fast as it can on purpose and its speed says nothing about lag right now."
		return d
	case t == nil && count == 0:
		d.Status, d.Title = LagUnknown, "Not measured yet"
		d.Explanation = "Playkeeper has no tick measurements for this server yet, and it hasn't reported falling behind."
		return d
	case t == nil:
		d.Title = "A bit behind"
		d.Status = LagBitBehind
		if float64(count)/in.Window.Minutes() >= 2 {
			d.Status, d.Title = LagLagging, "Lagging"
		}
		d.Explanation = upperFirst(spikes) + ". Players notice this as the game freezing and then catching up."
		return d
	}

	targetMS := 1000 / target
	speed := t.TPS / target
	tickTime := ""
	if t.MSPT > 0 {
		tickTime = fmt.Sprintf(" Each tick takes %s of the %s it has.", msText(t.MSPT), msText(targetMS))
	}
	rate := fmt.Sprintf("%s of %s ticks per second", trimZero(t.TPS), trimZero(target))
	switch {
	case speed >= 0.95 && count == 0 && t.MaxMSPT >= 500:
		d.Status, d.Title = LagBitBehind, "A bit behind"
		d.Explanation = fmt.Sprintf("It keeps up on average, but its slowest recent tick took %s. Players notice that as a short freeze.", msText(t.MaxMSPT))
	case speed >= 0.95 && count == 0:
		d.Status, d.Title = LagSmooth, "Running smoothly"
		d.Explanation = "The server keeps up: " + rate + "." + tickTime
		if t.MSPT >= 0.8*targetMS {
			d.Explanation += " That is close to the limit, so more players or busier farms could make it fall behind."
		}
	case speed >= 0.95:
		d.Status, d.Title = LagBitBehind, "A bit behind"
		d.Explanation = "It keeps up right now, but " + spikes + ". Players notice this as short freezes."
	case speed >= 0.8:
		d.Status, d.Title = LagBitBehind, "A bit behind"
		d.Explanation = "The server manages " + rate + ", so the game runs slightly slower than it should. Players may notice blocks and mobs reacting a little late."
	default:
		d.Status, d.Title = LagLagging, "Lagging"
		d.Explanation = fmt.Sprintf("The server manages only %s, so the game runs at %.0f%% speed. Players will notice: blocks break late and mobs stutter.", rate, speed*100)
	}
	return d
}

func stealCause(in LagInput) (Cause, bool) {
	u := in.HostCPU
	if u == nil || u.StealPercent < 5 {
		return Cause{}, false
	}
	score := 45
	if u.StealPercent >= 20 {
		score = 90
	} else if u.StealPercent >= 10 {
		score = 75
	}
	pct := pctText(u.StealPercent)
	return Cause{
		Kind: CauseCPUSteal, Params: map[string]any{"percent": round1(u.StealPercent)}, Score: score,
		Title: "The hosting provider is holding back processor time",
		Explanation: fmt.Sprintf("For %s of the last %s, this machine wanted to run but the hosting provider gave the processor to other customers. "+
			"Linux measures this as steal time. A tick can only finish when the processor is available, so the game falls behind.", pct, minutesText(in.Window)),
		Evidence: []Evidence{{Kind: EvidenceCPUSteal, Params: map[string]any{"percent": round1(u.StealPercent)},
			Text: fmt.Sprintf("CPU steal: %s of the machine's processor time.", pct)}},
		Actions: []Action{{Kind: ActionDedicatedCPU, Title: "Move to a plan with dedicated CPU cores, or ask your provider about CPU steal", Recommended: true}},
	}, true
}

func cpuLimitCause(in LagInput) (Cause, bool) {
	if in.CPULimitCores <= 0 || in.ServerCPU == nil || *in.ServerCPU < 0.9*in.CPULimitCores*100 {
		return Cause{}, false
	}
	used := *in.ServerCPU / 100
	return Cause{
		Kind: CauseCPULimit, Params: map[string]any{"cores": in.CPULimitCores, "used_cores": round1(used)}, Score: 80,
		Title: "The server uses all the processor time it is allowed",
		Explanation: fmt.Sprintf("Its limit is %s and it used %s on average over the last %s, so it had to wait for processor time.",
			coresText(in.CPULimitCores), trimZero(used), minutesText(in.Window)),
		Evidence: []Evidence{serverCPUEvidence(in)},
		Actions:  []Action{{Kind: ActionRaiseCPULimit, Params: map[string]any{"cores": in.CPULimitCores}, Title: "Allow the server more CPU cores", Recommended: true}},
	}, true
}

func hostBusyCause(in LagInput) (Cause, bool) {
	u := in.HostCPU
	if u == nil || u.BusyPercent < 90 {
		return Cause{}, false
	}
	busy := pctText(u.BusyPercent)
	c := Cause{Kind: CauseHostCPUBusy, Params: map[string]any{"busy_percent": round1(u.BusyPercent)}}
	ev := Evidence{Kind: EvidenceHostCPU, Params: map[string]any{"busy_percent": round1(u.BusyPercent)},
		Text: fmt.Sprintf("The machine's processor was %s busy.", busy)}
	upgrade := Action{Kind: ActionUpgradeHost, Params: map[string]any{"resource": "cpu"}, Title: "Move to a machine with more or faster CPU cores"}
	reduce := Action{Kind: ActionReduceOtherLoad, Title: "Stop or move other servers or programs on this machine"}
	if in.ServerCPU == nil || in.HostCores <= 0 {
		c.Score, c.Title = 60, "The machine's processor is fully busy"
		c.Explanation = fmt.Sprintf("The machine's processor was %s busy over the last %s. Playkeeper couldn't tell how much of that was this server.", busy, minutesText(in.Window))
		reduce.Recommended = true
		c.Evidence, c.Actions = []Evidence{ev}, []Action{reduce, upgrade}
		return c, true
	}
	share := *in.ServerCPU / float64(in.HostCores)
	others := max(u.BusyPercent-share, 0)
	c.Params["others_percent"] = round1(others)
	ev.Params["others_percent"] = round1(others)
	c.Evidence = []Evidence{ev, serverCPUEvidence(in)}
	if others >= 30 {
		c.Score, c.Title = 70, "Other programs are using most of the processor"
		c.Explanation = fmt.Sprintf("The machine's processor was %s busy over the last %s, and this server accounted for only %s of it. The rest went to other servers or programs on this machine.",
			busy, minutesText(in.Window), pctText(share))
		reduce.Recommended = true
		c.Actions = []Action{reduce, upgrade}
		return c, true
	}
	c.Score, c.Title = 65, "The server needs more processor than this machine has"
	c.Explanation = fmt.Sprintf("The machine's processor was %s busy over the last %s, and this server used %s of it.", busy, minutesText(in.Window), pctText(share))
	upgrade.Recommended = true
	c.Actions = []Action{upgrade}
	return c, true
}

func memoryCause(in LagInput) (Cause, bool) {
	since := in.Now.Add(-in.Window)
	var full, evac, lowestMB int
	var pauses, longest time.Duration
	lowest, heap := math.MaxFloat64, 0
	for _, e := range in.GC {
		if !e.At.IsZero() && (e.At.Before(since) || e.At.After(in.Now)) {
			continue
		}
		if e.HeapMB > 0 && e.clearsYoung() {
			if pct := float64(e.AfterMB) / float64(e.HeapMB) * 100; pct < lowest {
				lowest, lowestMB = pct, e.AfterMB
			}
			heap = max(heap, e.HeapMB)
		}
		if e.Kind == GCFull && !e.forcedFull() {
			continue
		}
		if e.Kind == GCFull {
			full++
		}
		if e.EvacuationFailure {
			evac++
		}
		pauses += e.Pause
		longest = max(longest, e.Pause)
	}
	if heap == 0 {
		return Cause{}, false
	}
	share := float64(pauses) / float64(in.Window) * 100
	window := minutesText(in.Window)
	c := Cause{Kind: CauseMemoryPressure, Title: "The server is short on memory", Params: map[string]any{"heap_mb": heap}}
	var said []string
	if full > 0 {
		c.Score = max(c.Score, 85)
		said = append(said, fmt.Sprintf("It had to stop everything for a full clean-up %s in the last %s, the longest for %s seconds.",
			timesText(full), window, trimZero(longest.Seconds())))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceFullGC, Params: map[string]any{"count": full, "minutes": in.Window.Minutes()},
			Text: fmt.Sprintf("%d full garbage %s in the last %s.", full, plural(full, "collection", "collections"), window)})
	}
	if evac > 0 {
		c.Score = max(c.Score, 80)
		said = append(said, fmt.Sprintf("Memory ran out in the middle of a clean-up %s.", timesText(evac)))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceEvacuationFailure, Params: map[string]any{"count": evac},
			Text: fmt.Sprintf("%d %s.", evac, plural(evac, "evacuation failure", "evacuation failures"))})
	}
	if lowest >= 80 {
		score := 55
		if lowest >= 90 {
			score = 80
		}
		c.Score = max(c.Score, score)
		said = append(said, fmt.Sprintf("Even right after cleaning up, %s of its %s was still in use.", pctText(lowest), sizeText(heap)))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceHeapAfterGC,
			Params: map[string]any{"used_mb": lowestMB, "heap_mb": heap, "percent": round1(lowest)},
			Text:   fmt.Sprintf("At least %s of the Java heap was in use after every garbage collection.", pctText(lowest))})
	}
	if share >= 5 || longest >= time.Second {
		score := 45
		if share >= 10 {
			score = 70
		} else if longest >= time.Second {
			score = 60
		}
		c.Score = max(c.Score, score)
		said = append(said, fmt.Sprintf("Clean-ups stopped the game for %s of the time.", pctText(share)))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceGCPauses, Params: map[string]any{"percent": round1(share), "longest_ms": longest.Milliseconds()},
			Text: fmt.Sprintf("Garbage collection pauses: %s of the time, the longest %s.", pctText(share), msText(float64(longest)/float64(time.Millisecond)))})
	}
	if c.Score == 0 {
		return Cause{}, false
	}
	c.Explanation = "Java stops the game while it cleans up memory (garbage collection), and it has to do that a lot when memory is tight. " + strings.Join(said, " ")
	c.Actions = memoryFixes(in.BudgetMB, in.HostMB, in.RoomMB, in.ViewDistance)
	return c, true
}

// memoryFixes offers the next budget when the machine has room for it, and
// otherwise what to do instead.
func memoryFixes(budgetMB, hostMB, roomMB, viewDistance int) []Action {
	if next, ok := nextBudget(budgetMB, hostMB, roomMB); ok {
		return []Action{{Kind: ActionRaiseMemory, Params: map[string]any{"from_mb": budgetMB, "to_mb": next},
			Title: fmt.Sprintf("Give it %s instead of %s", sizeText(next), sizeText(budgetMB)), Recommended: true}}
	}
	var out []Action
	if viewDistance > 8 {
		out = append(out, Action{Kind: ActionLowerView, Params: map[string]any{"from": viewDistance, "to": 8},
			Title: fmt.Sprintf("Lower the view distance from %d to 8 so fewer chunks stay in memory", viewDistance), Recommended: true})
	}
	return append(out, Action{Kind: ActionUpgradeHost, Params: map[string]any{"resource": "memory"},
		Title: "This machine has no memory left for a bigger budget; move to one with more memory", Recommended: len(out) == 0})
}

// nextBudget is the smallest offered budget above budgetMB that still fits
// in the memory the machine has left for this server.
func nextBudget(budgetMB, hostMB, roomMB int) (int, bool) {
	if budgetMB <= 0 {
		return 0, false
	}
	options, _, _ := minecraft.MemoryOptions(hostMB)
	for _, o := range options {
		if o > budgetMB && o <= budgetMB+roomMB {
			return o, true
		}
	}
	return 0, false
}

func chunkCause(in LagInput) (Cause, bool) {
	if in.NewChunks == nil || *in.NewChunks < 100 {
		return Cause{}, false
	}
	n := *in.NewChunks
	score := 45
	if n >= 1000 {
		score = 70
	} else if n >= 300 {
		score = 60
	}
	who := ""
	if in.Players != nil && *in.Players > 0 {
		who = fmt.Sprintf(" while %d %s online", *in.Players, plural(*in.Players, "player was", "players were"))
	}
	return Cause{
		Kind: CauseChunkGeneration, Params: map[string]any{"count": n}, Score: score,
		Title: "Players are exploring new land",
		Explanation: fmt.Sprintf("%s new chunks were created in the last %s%s. Generating new terrain is some of the heaviest work a Minecraft server does. "+
			"Pre-generating the map does this work ahead of time, when nobody is playing.", thousands(n), minutesText(in.Window), who),
		Evidence: []Evidence{{Kind: EvidenceNewChunks, Params: map[string]any{"count": n, "minutes": in.Window.Minutes()},
			Text: fmt.Sprintf("%s new chunks in the last %s.", thousands(n), minutesText(in.Window))}},
		Actions: []Action{{Kind: ActionPregenerate, Title: "Pre-generate the map around spawn", Recommended: true}},
	}, true
}

func diskCause(in LagInput) (Cause, bool) {
	u := in.HostCPU
	if u == nil || u.IOWaitPercent < 15 {
		return Cause{}, false
	}
	score := 50
	if u.IOWaitPercent >= 30 {
		score = 65
	}
	pct := pctText(u.IOWaitPercent)
	return Cause{
		Kind: CauseSlowDisk, Params: map[string]any{"percent": round1(u.IOWaitPercent)}, Score: score,
		Title: "The disk is slow to respond",
		Explanation: fmt.Sprintf("For %s of the last %s the processor sat waiting for the disk, and loading and saving chunks has to wait for it too. "+
			"A backup or another program may be keeping the disk busy, or the disk is slow.", pct, minutesText(in.Window)),
		Evidence: []Evidence{{Kind: EvidenceIOWait, Params: map[string]any{"percent": round1(u.IOWaitPercent)},
			Text: fmt.Sprintf("Waiting for the disk: %s of the machine's processor time.", pct)}},
		Actions: []Action{{Kind: ActionUpgradeHost, Params: map[string]any{"resource": "disk"}, Title: "Move to a machine with a faster SSD or NVMe disk", Recommended: true}},
	}, true
}

func distanceCause(in LagInput) (Cause, bool) {
	view, sim := in.ViewDistance, in.SimulationDistance
	if (in.Players != nil && *in.Players == 0) || (sim <= defaultDistance && view <= 12) {
		return Cause{}, false
	}
	c := Cause{Kind: CauseHighDistance, Title: "The server keeps a lot of the world running", Params: map[string]any{}}
	var said []string
	if sim > defaultDistance {
		c.Score = 45
		if sim >= 16 {
			c.Score = 55
		}
		c.Params["simulation_distance"] = sim
		said = append(said, fmt.Sprintf("Simulation distance is %d (the default is %d), so around each player about %s chunks keep mobs, crops and redstone running instead of %s.",
			sim, defaultDistance, thousands(square(sim)), thousands(square(defaultDistance))))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceSimulationDistance, Params: map[string]any{"value": sim, "default": defaultDistance},
			Text: fmt.Sprintf("simulation-distance=%d", sim)})
		c.Actions = append(c.Actions, Action{Kind: ActionLowerSimulation, Params: map[string]any{"from": sim, "to": defaultDistance},
			Title: fmt.Sprintf("Lower the simulation distance from %d to %d", sim, defaultDistance), Recommended: true})
	}
	if view > 12 {
		score := 35
		if view >= 16 {
			score = 45
		}
		c.Score = max(c.Score, score)
		c.Params["view_distance"] = view
		said = append(said, fmt.Sprintf("View distance is %d (the default is %d), so each player keeps about %s chunks loaded instead of %s.",
			view, defaultDistance, thousands(square(view)), thousands(square(defaultDistance))))
		c.Evidence = append(c.Evidence, Evidence{Kind: EvidenceViewDistance, Params: map[string]any{"value": view, "default": defaultDistance},
			Text: fmt.Sprintf("view-distance=%d", view)})
		c.Actions = append(c.Actions, Action{Kind: ActionLowerView, Params: map[string]any{"from": view, "to": defaultDistance},
			Title: fmt.Sprintf("Lower the view distance from %d to %d", view, defaultDistance), Recommended: len(c.Actions) == 0})
	}
	c.Explanation = strings.Join(said, " ")
	return c, true
}

// workloadCause is what is left when the server is busy on its own core and
// nothing outside it looks short.
func workloadCause(in LagInput, found []Cause) (Cause, bool) {
	if in.ServerCPU == nil || *in.ServerCPU < 85 {
		return Cause{}, false
	}
	for _, c := range found {
		if c.Score >= 60 {
			return Cause{}, false
		}
	}
	c := Cause{
		Kind: CauseWorldWorkload, Score: 40, Params: map[string]any{"server_cpu": round1(*in.ServerCPU)},
		Title: "The world itself keeps the server busy",
		Explanation: fmt.Sprintf("The server used %s on average over the last %s, and nothing outside it (memory, the machine's processor or disk) looks short. "+
			"Minecraft runs the world on one core, so the time most likely goes into the world itself: mobs, farms, redstone and the chunks around players.",
			coresText(*in.ServerCPU/100), minutesText(in.Window)),
		Evidence: []Evidence{serverCPUEvidence(in)},
	}
	if in.ServerType != "vanilla" {
		c.Explanation += " A profiler such as spark can show exactly what takes the time."
		c.Actions = append(c.Actions, Action{Kind: ActionRunProfiler, Title: "Run a profiler to see what takes the time", Recommended: true})
	}
	if sim := in.SimulationDistance; sim > 6 && sim <= defaultDistance {
		c.Actions = append(c.Actions, Action{Kind: ActionLowerSimulation, Params: map[string]any{"from": sim, "to": sim - 2},
			Title: fmt.Sprintf("Lower the simulation distance from %d to %d", sim, sim-2), Recommended: len(c.Actions) == 0})
	}
	return c, true
}

func serverCPUEvidence(in LagInput) Evidence {
	p := map[string]any{"percent": round1(*in.ServerCPU)}
	if in.CPULimitCores > 0 {
		p["limit_cores"] = in.CPULimitCores
	}
	return Evidence{Kind: EvidenceServerCPU, Params: p, Text: fmt.Sprintf("The server used %s on average.", coresText(*in.ServerCPU/100))}
}

func coresText(v float64) string {
	if s := trimZero(v); s != "1" {
		return s + " cores"
	}
	return "1 core"
}

// ParseDistances reads view-distance and simulation-distance from
// server.properties; missing or invalid values are 0. Nothing else is read:
// the file also holds the RCON password.
func ParseDistances(properties []byte) (view, simulation int) {
	for _, line := range strings.Split(string(properties), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 2 || n > 32 {
			continue
		}
		switch strings.TrimSpace(k) {
		case "view-distance":
			view = n
		case "simulation-distance":
			simulation = n
		}
	}
	return view, simulation
}

func square(r int) int { return (2*r + 1) * (2*r + 1) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func pctText(v float64) string { return fmt.Sprintf("%.0f%%", v) }

func msText(v float64) string {
	if v >= 10 {
		return fmt.Sprintf("%.0f ms", v)
	}
	return trimZero(v) + " ms"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// timesText renders a count of occurrences: "once", "twice", "3 times".
func timesText(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
