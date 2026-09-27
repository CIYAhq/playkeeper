package agent

import (
	"sync"
	"time"

	"github.com/CIYAhq/playkeeper/internal/api"
)

// consoleCapacity bounds the in-memory console buffer (lines).
const consoleCapacity = 2000

// ring is a bounded, sequence-numbered buffer of redacted console lines.
type ring struct {
	mu    sync.Mutex
	lines []api.LogLine
	cap   int
	next  int64
	epoch string
	last  time.Time
}

func newRing(capacity int) *ring {
	return &ring{cap: capacity, next: 1, epoch: newID()}
}

func (r *ring) append(ts time.Time, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, api.LogLine{Seq: r.next, TS: ts, Text: text})
	r.next++
	if ts.After(r.last) {
		r.last = ts
	}
	if over := len(r.lines) - r.cap; over > 0 {
		r.lines = append(r.lines[:0:0], r.lines[over:]...)
	}
}

func (r *ring) lastTS() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last
}

func (r *ring) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.lines)
}

// window returns the lines Docker dated at or after from, oldest first.
func (r *ring) window(from time.Time) []api.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []api.LogLine
	for _, l := range r.lines {
		if !l.TS.Before(from) {
			out = append(out, l)
		}
	}
	return out
}

// since returns lines with Seq > after (at most limit, newest last).
func (r *ring) since(epoch string, after int64, limit int) api.LogsResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	resp := api.LogsResponse{Epoch: r.epoch, Next: r.next - 1, Lines: []api.LogLine{}}
	if epoch != r.epoch {
		after = 0
	}
	start := 0
	for start < len(r.lines) && r.lines[start].Seq <= after {
		start++
	}
	if len(r.lines) > 0 && after > 0 && r.lines[0].Seq > after+1 {
		resp.Truncated = true
	}
	sel := r.lines[start:]
	if len(sel) > limit {
		sel = sel[len(sel)-limit:]
		resp.Truncated = true
	}
	resp.Lines = append(resp.Lines, sel...)
	return resp
}
