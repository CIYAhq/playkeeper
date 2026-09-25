package diagnose

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GCEvent is one garbage collection pause from the JVM's unified GC log
// (-Xlog:gc). AfterMB is the heap still in use once the pause is over: with
// G1 it is the data the server keeps in memory plus garbage not collected
// yet. That is the honest measure of how much memory the server needs; the
// container's memory use is not, because Aikar's flags make the JVM touch
// its whole heap at start.
//
// EvacuationFailure means G1 ran out of free heap while copying objects.
// Since Java 22 it also fails evacuating regions that native code has pinned,
// which says nothing about the heap's size; those alone don't set it.
type GCEvent struct {
	At                time.Time     `json:"at"` // zero when neither the log nor the caller gave a time
	Uptime            time.Duration `json:"uptime,omitempty"`
	ID                int           `json:"id"`
	Kind              GCKind        `json:"kind"`
	Cause             string        `json:"cause,omitempty"`
	BeforeMB          int           `json:"before_mb"`
	AfterMB           int           `json:"after_mb"`
	HeapMB            int           `json:"heap_mb"` // heap capacity at the time
	Pause             time.Duration `json:"pause"`
	EvacuationFailure bool          `json:"evacuation_failure,omitempty"`
}

// GCKind is the kind of pause.
type GCKind string

const (
	GCYoung   GCKind = "young" // Normal, Concurrent Start and Prepare Mixed young pauses
	GCMixed   GCKind = "mixed"
	GCFull    GCKind = "full"
	GCRemark  GCKind = "remark"
	GCCleanup GCKind = "cleanup"
)

var (
	reGCDecorations = regexp.MustCompile(`^((?:\[[^\[\]]{0,40}\])+) ?`)
	reGCDecoration  = regexp.MustCompile(`\[([^\[\]]*)\]`)
	reGCPause       = regexp.MustCompile(`^GC\((\d{1,9})\) Pause (Young|Full|Remark|Cleanup)(.*) (\d{1,7})M->(\d{1,7})M\((\d{1,7})M\) (\d{1,7}(?:\.\d{1,6})?)ms$`)
	reGCUptime      = regexp.MustCompile(`^(\d{1,9}(?:\.\d{1,9})?)(s|ms)$`)
	reGCEvacFailure = regexp.MustCompile(` \(Evacuation Failure(?:: ([A-Za-z /]{1,40}))?\)`)
)

// g1YoungTypes are the first parenthesised word of a G1 young pause; other
// collectors put the cause there instead.
var g1YoungTypes = map[string]GCKind{"Normal": GCYoung, "Concurrent Start": GCYoung, "Prepare Mixed": GCYoung, "Mixed": GCMixed}

// ParseGCLine reads one line of a unified GC log such as
//
//	[2026-09-25T14:03:11.123+0000][info][gc] GC(12) Pause Young (Normal) (G1 Evacuation Pause) 1843M->612M(4096M) 12.345ms
//
// The JVM pads decorations to a common width ("[info ]", "[gc     ]").
// Lines that are not a pause with heap figures (GC start lines, concurrent
// phases, other collectors' formats) return false. When the line carries only
// the JVM's uptime, At is jvmStart plus that uptime; pass the container's
// start time.
func ParseGCLine(line string, jvmStart time.Time) (GCEvent, bool) {
	if len(line) > 512 {
		return GCEvent{}, false
	}
	var e GCEvent
	msg := line
	if d := reGCDecorations.FindStringSubmatch(line); d != nil {
		msg = line[len(d[0]):]
		for _, m := range reGCDecoration.FindAllStringSubmatch(d[1], -1) {
			decoration(strings.TrimSpace(m[1]), &e)
		}
	}
	m := reGCPause.FindStringSubmatch(strings.TrimSpace(msg))
	if m == nil {
		return GCEvent{}, false
	}
	e.ID, _ = strconv.Atoi(m[1])
	e.BeforeMB, _ = strconv.Atoi(m[4])
	e.AfterMB, _ = strconv.Atoi(m[5])
	e.HeapMB, _ = strconv.Atoi(m[6])
	ms, _ := strconv.ParseFloat(m[7], 64)
	e.Pause = time.Duration(ms * float64(time.Millisecond))
	rest := m[3]
	if f := reGCEvacFailure.FindStringSubmatchIndex(rest); f != nil {
		e.EvacuationFailure = f[2] < 0 || rest[f[2]:f[3]] != "Pinned"
		rest = rest[:f[0]]
	}
	switch m[2] {
	case "Young":
		e.Kind = GCYoung
		if typ, after, ok := strings.Cut(strings.TrimPrefix(rest, " ("), ") "); ok {
			if k, g1 := g1YoungTypes[typ]; g1 {
				e.Kind, rest = k, " "+after
			}
		}
	case "Full":
		e.Kind = GCFull
	case "Remark":
		e.Kind = GCRemark
	case "Cleanup":
		e.Kind = GCCleanup
	}
	if c := strings.TrimSpace(rest); strings.HasPrefix(c, "(") && strings.HasSuffix(c, ")") {
		e.Cause = c[1 : len(c)-1]
	}
	if e.At.IsZero() && e.Uptime > 0 && !jvmStart.IsZero() {
		e.At = jvmStart.Add(e.Uptime)
	}
	return e, true
}

// unforcedFull are causes of full collections that something asked for
// (System.gc(), a heap dump or histogram, jcmd) or that free memory outside
// the heap. They stop the game, but don't mean the heap was short.
var unforcedFull = map[string]bool{
	"System.gc()": true, "Diagnostic Command": true, "Heap Dump Initiated GC": true, "Heap Inspection Initiated GC": true,
	"JvmtiEnv ForceGarbageCollection": true, "WhiteBox Initiated Full GC": true,
	"Metadata GC Threshold": true, "Metadata GC Clear Soft References": true, "CodeCache GC Threshold": true, "CodeCache GC Aggressive": true,
}

// forcedFull reports whether the pause is a full collection the JVM had to
// make because the heap ran out.
func (e GCEvent) forcedFull() bool {
	return e.Kind == GCFull && !unforcedFull[e.Cause]
}

// decoration reads the time and uptime decorations and ignores the rest
// (level, tags, pid, tid).
func decoration(s string, e *GCEvent) {
	if t, err := time.Parse("2006-01-02T15:04:05.000-0700", s); err == nil {
		e.At = t.UTC()
		return
	}
	m := reGCUptime.FindStringSubmatch(s)
	if m == nil {
		return
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	unit := time.Second
	if m[2] == "ms" {
		// timemillis is also "<n>ms" but counts from 1970; no uptime is that long.
		if v > 1e11 {
			return
		}
		unit = time.Millisecond
	}
	e.Uptime = time.Duration(v * float64(unit))
}

// GCWindow summarises the pauses of one period, so the agent can keep weeks
// of history for AdviseMemory in a few rows.
type GCWindow struct {
	Start              time.Time `json:"start"`
	Collections        int       `json:"collections"`
	MinAfterMB         int       `json:"min_after_mb"` // lowest heap in use after a pause
	MaxAfterMB         int       `json:"max_after_mb"`
	HeapMB             int       `json:"heap_mb"`  // largest heap capacity seen
	FullGCs            int       `json:"full_gcs"` // only those a full heap forced, not System.gc() or heap dumps
	EvacuationFailures int       `json:"evacuation_failures"`
	PauseMS            float64   `json:"pause_ms"` // total
	MaxPauseMS         float64   `json:"max_pause_ms"`
}

// Add folds one event into the window.
func (w *GCWindow) Add(e GCEvent) {
	if w.Collections == 0 || e.AfterMB < w.MinAfterMB {
		w.MinAfterMB = e.AfterMB
	}
	w.MaxAfterMB = max(w.MaxAfterMB, e.AfterMB)
	w.HeapMB = max(w.HeapMB, e.HeapMB)
	w.Collections++
	if e.forcedFull() {
		w.FullGCs++
	}
	if e.EvacuationFailure {
		w.EvacuationFailures++
	}
	ms := float64(e.Pause) / float64(time.Millisecond)
	w.PauseMS += ms
	w.MaxPauseMS = max(w.MaxPauseMS, ms)
}

// SummarizeGC groups events into windows of the given size (15 minutes suits
// AdviseMemory), oldest first. Events without a time are skipped.
func SummarizeGC(events []GCEvent, size time.Duration) []GCWindow {
	byStart := map[time.Time]*GCWindow{}
	for _, e := range events {
		if e.At.IsZero() {
			continue
		}
		start := e.At.UTC().Truncate(size)
		w := byStart[start]
		if w == nil {
			w = &GCWindow{Start: start}
			byStart[start] = w
		}
		w.Add(e)
	}
	out := make([]GCWindow, 0, len(byStart))
	for _, w := range byStart {
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}
