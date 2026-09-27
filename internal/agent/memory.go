package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
	"github.com/CIYAhq/playkeeper/internal/diagnose"
	"github.com/CIYAhq/playkeeper/internal/minecraft"
)

// memoryDays is how many local days Settings › Memory charts.
const memoryDays = 14

// MemoryAdvice is Settings › Memory: diagnose.AdviseMemory over the stored
// GC windows, the peak of each of the last 14 days in loc, and how every
// budget the machine offers would fit.
func (s *server) MemoryAdvice(ctx context.Context, tzName string, now time.Time) (api.MemoryAdvice, error) {
	loc, err := time.LoadLocation(tzName)
	if err != nil || tzName == "" {
		return api.MemoryAdvice{}, errInvalid("tz must be an IANA time zone name")
	}
	sc, err := s.serverConfig()
	if err != nil {
		return api.MemoryAdvice{}, err
	}
	if sc == nil {
		return api.MemoryAdvice{}, errNotCreated()
	}
	windows, err := s.gcWindows(now.Add(-gcKeep))
	if err != nil {
		return api.MemoryAdvice{}, err
	}
	hostMB := s.opts.HostMemoryMB()
	_, _, maxMB := s.memoryFor(s.id)
	view, _ := s.distances()
	mods := s.modJars(*sc)
	// A budget saved since the container was made gets the heap its next
	// start gives it.
	budget, heap := s.runMemory(ctx, *sc)
	if budget != sc.MemoryMB {
		heap = heapMB(*sc)
	}
	in := diagnose.MemoryInput{Now: now, Windows: windows, BudgetMB: sc.MemoryMB, HostMB: hostMB, RoomMB: max(maxMB-sc.MemoryMB, 0), ViewDistance: view,
		ServerType: serverTypeOf(*sc), Mods: mods, HeapMB: heap}
	a := diagnose.AdviseMemory(in)
	out := api.MemoryAdvice{
		Verdict: string(a.Verdict), Params: a.Params, Title: a.Title, Explanation: a.Explanation,
		Evidence: apiEvidence(a.Evidence), Actions: apiActions(a.Actions),
		BudgetMB: sc.MemoryMB, HeapMB: heap, Days: []api.MemoryDay{}, Options: []api.MemoryOption{},
	}
	switch a.Verdict {
	case diagnose.MemoryKeep:
		out.RecommendedMB = sc.MemoryMB
	case diagnose.MemoryLower, diagnose.MemoryRaise:
		out.RecommendedMB, _ = a.Params["to_mb"].(int)
	}
	if c, err := s.docker.ContainerInspect(ctx, s.containerName()); err == nil && c.State.Running {
		out.FromNextStart = c.Config.Labels[labelGCLog] != gcLogVersion
	}

	local := now.In(loc)
	first := time.Date(local.Year(), local.Month(), local.Day()-(memoryDays-1), 0, 0, 0, 0, loc)
	for d := first; d.Before(now); d = d.AddDate(0, 0, 1) {
		day, next := api.MemoryDay{Date: d.Format(time.DateOnly)}, d.AddDate(0, 0, 1)
		for _, w := range windows {
			if !w.Start.Before(d) && w.Start.Before(next) {
				day.PeakMB = max(day.PeakMB, w.MaxAfterMB)
			}
		}
		out.Days = append(out.Days, day)
	}

	budgets, _, _ := minecraft.MemoryOptions(hostMB)
	var fits []diagnose.MemoryFit
	if a.Verdict != diagnose.MemoryNotEnoughData {
		fits = diagnose.FitBudgets(in, budgets)
	}
	for i, mb := range budgets {
		o := api.MemoryOption{MemoryMB: mb, HeapMB: minecraft.HeapFor(mb, serverTypeOf(*sc), mods), Fits: mb <= maxMB || mb == sc.MemoryMB}
		if mb == sc.MemoryMB {
			o.HeapMB = heap
		}
		if fits != nil {
			o.Fit = string(fits[i])
		}
		out.Options = append(out.Options, o)
	}
	return out, nil
}

func (s *server) hMemory(w http.ResponseWriter, r *http.Request) {
	a, err := s.MemoryAdvice(r.Context(), r.URL.Query().Get("tz"), s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}
