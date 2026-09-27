package panel

import (
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/machinelink"
)

// Refusals (tool calls a token wasn't allowed, machines that couldn't
// join) go in the audit log without letting a flood of them fill it: the
// first of each kind gets its own row at once, and the ones like it in the
// next ten minutes one row that counts them. There is no timer; counts are
// written when the next refusal comes, when the audit log is read, and
// when the panel stops.

const (
	refusalWindow = 10 * time.Minute
	// maxRefusalKinds bounds the kinds of refusal counted at once. Past it,
	// new kinds are counted together as one.
	maxRefusalKinds = 1000
	refusalOverflow = "*"
)

type auditRow struct {
	at                                    time.Time
	actor, action, target, result, detail string
}

type refusalRun struct {
	// first is the row the first refusal got, and like what the counts
	// say they are.
	first auditRow
	like  string
	// start is when the run began; from and last span the refusals
	// counted since the last count was written.
	start, from, last time.Time
	count             int
}

func (run *refusalRun) summary() auditRow {
	r := run.first
	r.at = run.last
	from, last := run.from.UTC().Format("15:04"), run.last.UTC().Format("15:04")
	span := "at " + last + " UTC"
	if from != last {
		span = "between " + from + " and " + last + " UTC"
	}
	r.detail = fmt.Sprintf("%s · %d more %s", run.like, run.count, span)
	return r
}

type refusalSink struct {
	mu   sync.Mutex
	runs map[string]*refusalRun
}

// note counts a refusal. It returns the audit rows to write: this refusal
// when it is the first of its kind in a while, and the counts of runs that
// have ended.
func (k *refusalSink) note(key, like string, r auditRow) []auditRow {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.runs == nil {
		k.runs = map[string]*refusalRun{}
	}
	out := k.endLocked(r.at, false)
	if _, ok := k.runs[key]; !ok && len(k.runs) >= maxRefusalKinds-1 {
		key, like = refusalOverflow, "refusals of many kinds"
		r = auditRow{at: r.at, actor: "(many)", action: "refusals", result: "refused",
			detail: "More kinds of refusal came at once than the log keeps apart, so the rest are counted together."}
	}
	if run, ok := k.runs[key]; ok {
		if run.count == 0 {
			run.from = r.at
		}
		run.count++
		run.last = r.at
		return out
	}
	k.runs[key] = &refusalRun{first: r, like: like, start: r.at}
	return append(out, r)
}

// flush returns the counts so far. Runs go on counting unless all is set.
func (k *refusalSink) flush(now time.Time, all bool) []auditRow {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := k.endLocked(now, all)
	for _, run := range k.runs {
		if run.count > 0 {
			out = append(out, run.summary())
			run.count = 0
		}
	}
	sortRows(out)
	return out
}

// endLocked removes the runs that are over (all of them, with all) and
// returns their counts.
func (k *refusalSink) endLocked(now time.Time, all bool) []auditRow {
	var out []auditRow
	for key, run := range k.runs {
		if !all && now.Sub(run.start) < refusalWindow {
			continue
		}
		if run.count > 0 {
			out = append(out, run.summary())
		}
		delete(k.runs, key)
	}
	sortRows(out)
	return out
}

func sortRows(rows []auditRow) {
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].at.Equal(rows[j].at) {
			return rows[i].at.Before(rows[j].at)
		}
		return rows[i].detail < rows[j].detail
	})
}

// refused audits a refusal through the sink.
func (s *Server) refused(key, like string, r auditRow) {
	for _, row := range s.refusals.note(key, like, r) {
		s.writeAudit(row)
	}
}

// flushRefusals writes the refusals counted so far; all ends every run.
func (s *Server) flushRefusals(all bool) {
	for _, row := range s.refusals.flush(s.now(), all) {
		s.writeAudit(row)
	}
}

// joinRefused audits a machine the hub refused. Refusals of one kind from
// one network (an IPv4 address or an IPv6 /64) are counted together, and
// refusals while joining is paused from everywhere, as a pause refuses
// everyone.
func (s *Server) joinRefused(e machinelink.Event) {
	key, like := "join "+e.Code, e.Code
	if e.Code != machinelink.CodeJoinRateLimited {
		from := networkOf(e.Address)
		key, like = key+" "+from, like+" from "+from
	}
	s.refused(key, like, auditRow{at: e.At, actor: "(unknown machine)", action: "machine.join", result: "refused", detail: e.Code + " from " + e.Address})
}

// networkOf is an address as the hub counts failures: an IPv4 address, or
// the /64 of an IPv6 one.
func networkOf(addr string) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	if a = a.Unmap(); a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return addr
	}
	return p.String()
}
