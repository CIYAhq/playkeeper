package panel

import (
	"math"
	"sync"
	"time"
)

// limiter is a keyed token bucket with bounded memory.
type limiter struct {
	mu       sync.Mutex
	capacity float64
	refill   float64 // tokens per second
	buckets  map[string]*bucket
	now      func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(capacity int, per time.Duration, now func() time.Time) *limiter {
	return &limiter{capacity: float64(capacity), refill: float64(capacity) / per.Seconds(), buckets: map[string]*bucket{}, now: now}
}

// allow consumes one token for key; when empty it returns the wait time.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) > 10000 {
			l.gc(now)
		}
		b = &bucket{tokens: l.capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.capacity, b.tokens+now.Sub(b.last).Seconds()*l.refill)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.refill * float64(time.Second))
	return false, wait
}

// ready reports whether key has a token left, without taking it.
func (l *limiter) ready(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.buckets[key]
	if b == nil {
		return true, 0
	}
	tokens := math.Min(l.capacity, b.tokens+l.now().Sub(b.last).Seconds()*l.refill)
	if tokens >= 1 {
		return true, 0
	}
	return false, time.Duration((1 - tokens) / l.refill * float64(time.Second))
}

func (l *limiter) gc(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.last).Seconds()*l.refill >= l.capacity {
			delete(l.buckets, k)
		}
	}
}

// lockout tracks consecutive failed logins per key with exponential
// back-off. Sign-in keys it by account and address prefix, so failures
// from one place can't keep a correct password out everywhere.
type lockout struct {
	mu       sync.Mutex
	failures map[string]int
	until    map[string]time.Time
	now      func() time.Time
}

// maxLockoutKeys bounds the lockout's memory: past it, keys that aren't
// locked right now are forgotten.
const maxLockoutKeys = 10000

func newLockout(now func() time.Time) *lockout {
	return &lockout{failures: map[string]int{}, until: map[string]time.Time{}, now: now}
}

func (l *lockout) locked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if u, ok := l.until[key]; ok && l.now().Before(u) {
		return true, u.Sub(l.now())
	}
	return false, 0
}

func (l *lockout) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.failures[key]; !ok && len(l.failures) >= maxLockoutKeys {
		now := l.now()
		for k := range l.failures {
			if u, ok := l.until[k]; !ok || !now.Before(u) {
				delete(l.failures, k)
				delete(l.until, k)
			}
		}
	}
	l.failures[key]++
	if n := l.failures[key]; n >= 5 {
		d := time.Minute << min(n-5, 4)
		l.until[key] = l.now().Add(d)
	}
}

func (l *lockout) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
	delete(l.until, key)
}
