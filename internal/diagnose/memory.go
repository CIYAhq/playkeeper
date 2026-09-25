package diagnose

import (
	"fmt"
	"strings"
	"time"

	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// MemoryVerdict is what AdviseMemory recommends for a memory budget.
type MemoryVerdict string

const (
	MemoryLower         MemoryVerdict = "lower"
	MemoryRaise         MemoryVerdict = "raise"
	MemoryKeep          MemoryVerdict = "keep"
	MemoryNotEnoughData MemoryVerdict = "not_enough_data"
)

// MemoryInput is a server's recent garbage collection history and the memory
// it has and could have.
type MemoryInput struct {
	Now          time.Time
	Windows      []GCWindow // from SummarizeGC; only the last 14 days are read
	BudgetMB     int        // the server's memory budget (its container limit)
	HostMB       int        // the machine's memory, for the budgets minecraft.MemoryOptions offers
	RoomMB       int        // memory the machine could still give this server on top of BudgetMB
	ViewDistance int        // from ParseDistances, for advice when there is no room
}

// KeepReason says why AdviseMemory keeps a budget, in Params["reason"].
type KeepReason string

const (
	KeepFits             KeepReason = "fits"                // a smaller budget would leave too little room: smaller_mb, smaller_heap_mb
	KeepSmallest         KeepReason = "smallest"            // it has the smallest budget offered
	KeepTight            KeepReason = "tight"               // at its busiest it used most of its heap, without running short
	KeepRanShortOnce     KeepReason = "ran_short_once"      // it ran short once at this size
	KeepRanShortWithLess KeepReason = "ran_short_with_less" // it ran short with a smaller heap: short_heap_mb, smaller_mb, smaller_heap_mb
)

// MemoryAdvice says whether a server's memory budget fits what it needed
// recently. Lower, and raise when the machine's memory is known, come with
// exactly one recommended action.
type MemoryAdvice struct {
	Verdict     MemoryVerdict  `json:"verdict"`
	Params      map[string]any `json:"params,omitempty"`
	Title       string         `json:"title"`
	Explanation string         `json:"explanation"`
	Evidence    []Evidence     `json:"evidence,omitempty"`
	Actions     []Action       `json:"actions,omitempty"`
}

// What a server needs is the heap still in use right after garbage
// collection at its busiest. Java runs smoothly while that stays within
// about two thirds of the heap; when even the lowest point of a window is
// 80% or more, it is collecting almost all the time.
const (
	memoryLookbackDays = 14
	memoryMinSpanDays  = 7 // history needed before saying keep or lower,
	memoryMinDays      = 3 // with measurements from this many of its days
	comfortableShare   = 0.65
	pressureShare      = 0.80

	oneDay = 24 * time.Hour
)

// AdviseMemory recommends lowering, raising or keeping a server's memory
// budget from the heap it had in use after garbage collection in the last 14
// days. The container's memory use can't tell: with Aikar's flags the JVM
// touches its whole heap at start.
//
// It says raise when the heap ran short at its current size: a full
// collection, or two windows with an evacuation failure or the heap at least
// 80% full after every collection. It says lower only after a week of
// history, to the smallest offered budget whose heap keeps the busiest moment
// within two thirds and is larger than any heap the server ran short with.
func AdviseMemory(in MemoryInput) MemoryAdvice {
	heap := minecraft.HeapMB(in.BudgetMB)
	var h memoryHistory
	if in.BudgetMB > 0 {
		h = readMemoryHistory(in, heap)
	}
	switch {
	case h.windows == 0:
		return notEnoughMemoryData(h, heap)
	case h.fullGCs > 0 || h.short >= 2:
		return raiseMemory(in, h, heap)
	case h.span < memoryMinSpanDays*oneDay || h.days < memoryMinDays:
		return notEnoughMemoryData(h, heap)
	}
	if to, ok := smallerBudget(in.BudgetMB, h); ok && h.short == 0 {
		return lowerMemory(in, h, heap, to)
	}
	return keepMemory(in, h, heap)
}

// memoryHistory is what AdviseMemory reads from the windows. Signs of a short
// heap count at the current heap size or larger; at a smaller size they only
// rule out going back to it.
type memoryHistory struct {
	windows    int
	days       int           // of the last periodDays, those with measurements
	span       time.Duration // from the oldest measurement to now
	peakMB     int           // most heap in use after a collection
	fullGCs    int
	evacFails  int
	short      int      // windows with any sign of a short heap
	high       int      // windows at least pressureShare full after every collection
	worst      GCWindow // the fullest of those
	tooSmallMB int      // largest smaller heap the server ran short with
}

func readMemoryHistory(in MemoryInput, heapMB int) memoryHistory {
	var h memoryHistory
	from := in.Now.Add(-memoryLookbackDays * oneDay)
	days := map[int]bool{}
	oldest := in.Now
	for _, w := range in.Windows {
		if w.MaxAfterMB <= 0 || w.HeapMB <= 0 || w.Start.Before(from) || w.Start.After(in.Now) {
			continue
		}
		h.windows++
		days[min(int(in.Now.Sub(w.Start)/oneDay), memoryLookbackDays-1)] = true
		if w.Start.Before(oldest) {
			oldest = w.Start
		}
		h.peakMB = max(h.peakMB, w.MaxAfterMB)
		high := fullShare(w) >= pressureShare
		short := high || w.FullGCs > 0 || w.EvacuationFailures > 0
		if w.HeapMB*100 < heapMB*95 {
			if short {
				h.tooSmallMB = max(h.tooSmallMB, w.HeapMB)
			}
			continue
		}
		h.fullGCs += w.FullGCs
		h.evacFails += w.EvacuationFailures
		if short {
			h.short++
		}
		if high {
			if h.high == 0 || fullShare(w) > fullShare(h.worst) {
				h.worst = w
			}
			h.high++
		}
	}
	h.days = len(days)
	h.span = in.Now.Sub(oldest)
	return h
}

// fullShare is how full the heap stayed after every collection in w.
func fullShare(w GCWindow) float64 {
	return float64(w.MinAfterMB) / float64(w.HeapMB)
}

// periodDays is how many days the history reaches back, for "the last N
// days".
func (h memoryHistory) periodDays() int {
	return min(int(h.span/oneDay)+1, memoryLookbackDays)
}

func (h memoryHistory) evidence(heap int) []Evidence {
	days := h.periodDays()
	return []Evidence{
		{Kind: EvidenceHeapNeeded, Params: map[string]any{"peak_mb": h.peakMB, "heap_mb": heap, "days": days},
			Text: fmt.Sprintf("At most %s of the Java heap was in use after garbage collection in %s; the heap is now %s.", sizeText(h.peakMB), lastDays(days), sizeText(heap))},
		{Kind: EvidenceGCSamples, Params: map[string]any{"windows": h.windows, "days": h.days, "span_days": days},
			Text: fmt.Sprintf("Garbage collection was measured on %d %s of %s.", h.days, plural(h.days, "day", "days"), lastDays(days))},
	}
}

func lastDays(n int) string {
	if n == 1 {
		return "the last day"
	}
	return fmt.Sprintf("the last %d days", n)
}

// smallerBudget is the smallest offered budget below budgetMB whose heap
// keeps the busiest moment within comfortableShare and is larger than any
// heap the server ran short with.
func smallerBudget(budgetMB int, h memoryHistory) (int, bool) {
	options, _, _ := minecraft.MemoryOptions(budgetMB + minecraft.HostReserveMB)
	for _, o := range options {
		heap := minecraft.HeapMB(o)
		if o < budgetMB && heap > h.tooSmallMB && float64(h.peakMB) <= comfortableShare*float64(heap) {
			return o, true
		}
	}
	return 0, false
}

// nextSmaller is the largest offered budget below budgetMB.
func nextSmaller(budgetMB int) (int, bool) {
	options, _, _ := minecraft.MemoryOptions(budgetMB + minecraft.HostReserveMB)
	for i := len(options) - 1; i >= 0; i-- {
		if options[i] < budgetMB {
			return options[i], true
		}
	}
	return 0, false
}

func notEnoughMemoryData(h memoryHistory, heap int) MemoryAdvice {
	a := MemoryAdvice{
		Verdict: MemoryNotEnoughData, Title: "Not enough data yet",
		Params: map[string]any{"days": h.days, "min_days": memoryMinDays, "min_span_days": memoryMinSpanDays},
	}
	if h.windows == 0 {
		a.Explanation = "Playkeeper hasn't measured how much memory the server needs yet."
		return a
	}
	a.Params["peak_mb"], a.Params["heap_mb"] = h.peakMB, heap
	a.Explanation = fmt.Sprintf("Playkeeper suggests a different budget once it has a week of measurements that include at least %d days the server ran. "+
		"So far it has measurements from %d %s: it needed up to %s, and it has %s for the game.",
		memoryMinDays, h.days, plural(h.days, "day", "days"), sizeText(h.peakMB), sizeText(heap))
	a.Evidence = h.evidence(heap)
	return a
}

func raiseMemory(in MemoryInput, h memoryHistory, heap int) MemoryAdvice {
	days := h.periodDays()
	last := lastDays(days)
	a := MemoryAdvice{
		Verdict: MemoryRaise, Title: "It needs more memory",
		Params: map[string]any{"budget_mb": in.BudgetMB, "heap_mb": heap, "days": days,
			"full_gcs": h.fullGCs, "evacuation_failures": h.evacFails, "high_windows": h.high},
	}
	var said []string
	if h.fullGCs > 0 {
		said = append(said, fmt.Sprintf("In %s it had to stop the game for a full clean-up %s.", last, timesText(h.fullGCs)))
		a.Evidence = append(a.Evidence, Evidence{Kind: EvidenceFullGC, Params: map[string]any{"count": h.fullGCs, "minutes": days * 24 * 60},
			Text: fmt.Sprintf("%d full garbage %s in %s.", h.fullGCs, plural(h.fullGCs, "collection", "collections"), last)})
	}
	if h.evacFails > 0 {
		said = append(said, fmt.Sprintf("Memory ran out in the middle of a clean-up %s.", timesText(h.evacFails)))
		a.Evidence = append(a.Evidence, Evidence{Kind: EvidenceEvacuationFailure, Params: map[string]any{"count": h.evacFails},
			Text: fmt.Sprintf("%d %s in %s.", h.evacFails, plural(h.evacFails, "evacuation failure", "evacuation failures"), last)})
	}
	if h.high > 0 {
		pct := fullShare(h.worst) * 100
		said = append(said, fmt.Sprintf("At worst, %s of the memory it has for the game was still in use even right after cleaning up.", pctText(pct)))
		a.Evidence = append(a.Evidence, Evidence{Kind: EvidenceHeapAfterGC,
			Params: map[string]any{"used_mb": h.worst.MinAfterMB, "heap_mb": h.worst.HeapMB, "percent": round1(pct), "windows": h.high},
			Text: fmt.Sprintf("In %d measured %s the Java heap stayed at least %s full after every garbage collection, at worst %s.",
				h.high, plural(h.high, "period", "periods"), pctText(pressureShare*100), pctText(pct))})
	}
	a.Explanation = strings.Join(said, " ") + fmt.Sprintf(" That happens when the memory it has for the game, %s, is too little.", sizeText(heap))
	a.Evidence = append(a.Evidence, roomEvidence(in.BudgetMB, in.HostMB, in.RoomMB)...)
	if in.HostMB > 0 {
		a.Actions = memoryFixes(in.BudgetMB, in.HostMB, in.RoomMB, in.ViewDistance)
		if a.Actions[0].Kind == ActionRaiseMemory {
			a.Params["to_mb"] = a.Actions[0].Params["to_mb"]
		}
	}
	return a
}

func lowerMemory(in MemoryInput, h memoryHistory, heap, to int) MemoryAdvice {
	days := h.periodDays()
	toHeap := minecraft.HeapMB(to)
	return MemoryAdvice{
		Verdict: MemoryLower,
		Params:  map[string]any{"budget_mb": in.BudgetMB, "heap_mb": heap, "peak_mb": h.peakMB, "days": days, "to_mb": to, "to_heap_mb": toHeap},
		Title:   fmt.Sprintf("%s would be enough", sizeText(to)),
		Explanation: fmt.Sprintf("You gave it %s, but it never needed more than %s in %s. With %s it would still have %s for the game, "+
			"enough to spare, and %s of this machine's memory would be free for other things.",
			sizeText(in.BudgetMB), sizeText(h.peakMB), lastDays(days), sizeText(to), sizeText(toHeap), sizeText(in.BudgetMB-to)),
		Evidence: h.evidence(heap),
		Actions: []Action{{Kind: ActionLowerMemory, Params: map[string]any{"from_mb": in.BudgetMB, "to_mb": to},
			Title: fmt.Sprintf("Give it %s instead of %s", sizeText(to), sizeText(in.BudgetMB)), Recommended: true}},
	}
}

func keepMemory(in MemoryInput, h memoryHistory, heap int) MemoryAdvice {
	days := h.periodDays()
	a := MemoryAdvice{
		Verdict:  MemoryKeep,
		Params:   map[string]any{"budget_mb": in.BudgetMB, "heap_mb": heap, "peak_mb": h.peakMB, "days": days},
		Title:    "Its memory fits",
		Evidence: h.evidence(heap),
	}
	text := fmt.Sprintf("It needed up to %s in %s, and it has %s for the game.", sizeText(h.peakMB), lastDays(days), sizeText(heap))
	smaller, hasSmaller := nextSmaller(in.BudgetMB)
	smallerHeap := minecraft.HeapMB(smaller)
	var reason KeepReason
	switch {
	case h.short > 0:
		reason, a.Title = KeepRanShortOnce, "It shouldn't have less memory"
		text += " It ran short of memory once in that time, so it shouldn't have less."
	case hasSmaller && smallerHeap <= h.tooSmallMB:
		reason, a.Title = KeepRanShortWithLess, "It shouldn't have less memory"
		a.Params["short_heap_mb"], a.Params["smaller_mb"], a.Params["smaller_heap_mb"] = h.tooSmallMB, smaller, smallerHeap
		text += fmt.Sprintf(" It ran short of memory with %s for the game in that time, and a %s budget would give it only %s, so it shouldn't have less.",
			sizeText(h.tooSmallMB), sizeText(smaller), sizeText(smallerHeap))
	case float64(h.peakMB) > comfortableShare*float64(heap):
		reason, a.Title = KeepTight, "Its memory is just enough"
		text += " That is close to all of it, though it hasn't run short at this size. If players notice freezes, give it more."
	case !hasSmaller:
		reason = KeepSmallest
		text += " That leaves room to spare, and it already has the smallest budget Playkeeper offers."
	default:
		reason = KeepFits
		a.Params["smaller_mb"], a.Params["smaller_heap_mb"] = smaller, smallerHeap
		text += fmt.Sprintf(" That leaves room to spare, but a %s budget would give it only %s for the game, too little to keep that room.",
			sizeText(smaller), sizeText(smallerHeap))
	}
	a.Params["reason"] = reason
	a.Explanation = text
	return a
}
