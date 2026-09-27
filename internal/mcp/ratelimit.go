package mcp

import (
	"math"
	"sync"
	"time"
)

const maxKeys = 10000

// limiter is a keyed token bucket with bounded memory. It is kept in the
// form of the generic cell rate algorithm: one time per key, when the key is
// back to its full burst, so waits are exact durations rather than float
// approximations.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration // one request per interval at the sustained rate
	burst    time.Duration // how far ahead of now a key's time may run
	next     map[string]time.Time
	now      func() time.Time
}

// newLimiter allows each key capacity requests at once and capacity per
// period on average.
func newLimiter(capacity int, per time.Duration, now func() time.Time) *limiter {
	interval := per / time.Duration(capacity)
	return &limiter{interval: interval, burst: interval * time.Duration(capacity-1), next: map[string]time.Time{}, now: now}
}

// take records a request for key; when key is over its rate it records
// nothing and returns how long to wait.
func (l *limiter) take(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	next, wait := l.check(key, now)
	if wait > 0 {
		return false, wait
	}
	if _, known := l.next[key]; !known && len(l.next) >= maxKeys {
		l.gc(now)
	}
	l.next[key] = next.Add(l.interval)
	return true, 0
}

// peek reports whether key may make a request, without recording one.
func (l *limiter) peek(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, wait := l.check(key, l.now())
	return wait == 0, wait
}

func (l *limiter) check(key string, now time.Time) (time.Time, time.Duration) {
	next := l.next[key]
	if next.Before(now) {
		next = now
	}
	if ahead := next.Sub(now); ahead > l.burst {
		return next, ahead - l.burst
	}
	return next, 0
}

// gc drops keys that are back to their full burst, which behave like keys
// never seen. Many distinct keys (IPv6 clients, say) can all be active, so
// arbitrary keys go next; that only refills their buckets early.
func (l *limiter) gc(now time.Time) {
	for k, next := range l.next {
		if !next.After(now) {
			delete(l.next, k)
		}
	}
	for k := range l.next {
		if len(l.next) < maxKeys {
			break
		}
		delete(l.next, k)
	}
}

// retryAfter rounds a wait up to whole seconds, for Retry-After headers and
// messages.
func retryAfter(d time.Duration) int { return max(1, int(math.Ceil(d.Seconds()))) }
